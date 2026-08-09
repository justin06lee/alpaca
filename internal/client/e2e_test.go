package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/netx"
	"github.com/justin06lee/alpaca/internal/ollama"
	"github.com/justin06lee/alpaca/internal/server"
)

// stack stands up the entire production arrangement: a stub ollama daemon, the
// real gateway, and the real single-port sniffer serving plain HTTP and pinned
// TLS together. Only the model itself is faked.
type stack struct {
	endpoint    string
	fingerprint string
	apiKey      string
	serverID    string
}

func newStack(t *testing.T, tokens ...string) *stack {
	t.Helper()

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			flusher, _ := w.(http.Flusher)
			for _, tok := range tokens {
				fmt.Fprintf(w, `{"message":{"role":"assistant","content":%q},"done":false}`+"\n", tok)
				if flusher != nil {
					flusher.Flush()
				}
			}
			fmt.Fprint(w, `{"message":{"content":""},"done":true,"done_reason":"stop",`+
				`"prompt_eval_count":9,"eval_count":4,"total_duration":1000000000,"eval_duration":500000000}`+"\n")
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"llama3.2:latest","size":2019393189,`+
				`"details":{"family":"llama","parameter_size":"3.2B","quantization_level":"Q4_K_M"}}]}`)
		case "/api/version":
			fmt.Fprint(w, `{"version":"0.32.5"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(daemon.Close)

	ollamaClient, err := ollama.New(daemon.URL)
	if err != nil {
		t.Fatalf("ollama.New: %v", err)
	}

	const serverID = "e2e-server"
	apiKey, err := config.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}

	gateway := server.New(server.Options{
		Ollama: ollamaClient, APIKey: apiKey, ID: serverID, Name: "e2e", Version: "test",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	sniffer := netx.Sniff(ln)
	t.Cleanup(func() { sniffer.Close() })

	identity, err := netx.CreateIdentity(t.TempDir(), []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	plainSrv := gateway.NewHTTPServer()
	tlsSrv := gateway.NewHTTPServer()
	go plainSrv.Serve(sniffer.Plain())
	go tlsSrv.Serve(tls.NewListener(sniffer.TLS(), identity.ServerTLSConfig()))
	t.Cleanup(func() {
		plainSrv.Close()
		tlsSrv.Close()
	})

	return &stack{
		endpoint:    ln.Addr().String(),
		fingerprint: identity.Fingerprint,
		apiKey:      apiKey,
		serverID:    serverID,
	}
}

// The full path a user takes on a LAN: plain HTTP, no handshake, streamed reply.
func TestEndToEndOverLAN(t *testing.T) {
	s := newStack(t, "The ", "quick ", "brown ", "fox")

	prof := &config.Profile{
		ID: s.serverID, Name: "e2e", APIKey: s.apiKey,
		Fingerprint: s.fingerprint,
		LAN:         []string{s.endpoint},
	}

	c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.Route().TLS {
		t.Error("LAN route used TLS; the fast path should be plain HTTP")
	}

	var reply strings.Builder
	var usage *Usage
	err = c.Chat(context.Background(), ChatRequest{
		Model:    "llama3.2:latest",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}, func(ch Chunk) error {
		reply.WriteString(ch.Content)
		if ch.Usage != nil {
			usage = ch.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if reply.String() != "The quick brown fox" {
		t.Errorf("reply = %q, want %q", reply.String(), "The quick brown fox")
	}
	if usage == nil || usage.PromptTokens != 9 || usage.CompletionTokens != 4 {
		t.Errorf("usage = %+v, want 9/4 — token stats must survive the whole chain", usage)
	}
}

// The same server, same port, reached over pinned TLS — the fallback path.
func TestEndToEndOverPinnedTLS(t *testing.T) {
	s := newStack(t, "over ", "tls")

	prof := &config.Profile{
		ID: s.serverID, Name: "e2e", APIKey: s.apiKey,
		Fingerprint: s.fingerprint,
		Public:      s.endpoint,
	}

	c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect over TLS: %v", err)
	}
	if !c.Route().TLS {
		t.Fatal("public route did not use TLS")
	}

	var reply strings.Builder
	if err := c.Chat(context.Background(), ChatRequest{
		Model:    "llama3.2:latest",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}, func(ch Chunk) error {
		reply.WriteString(ch.Content)
		return nil
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.String() != "over tls" {
		t.Errorf("reply = %q, want %q", reply.String(), "over tls")
	}
}

// Both transports against one port, interleaved — the property the sniffer
// exists to provide, verified through the real client.
func TestEndToEndBothTransportsOnOnePort(t *testing.T) {
	s := newStack(t, "ok")

	lanProf := &config.Profile{ID: s.serverID, Name: "lan", APIKey: s.apiKey,
		Fingerprint: s.fingerprint, LAN: []string{s.endpoint}}
	tlsProf := &config.Profile{ID: s.serverID, Name: "tls", APIKey: s.apiKey,
		Fingerprint: s.fingerprint, Public: s.endpoint}

	for i := 0; i < 3; i++ {
		plain, err := Connect(context.Background(), lanProf, Options{SkipDiscovery: true})
		if err != nil {
			t.Fatalf("plain connect %d: %v", i, err)
		}
		secure, err := Connect(context.Background(), tlsProf, Options{SkipDiscovery: true})
		if err != nil {
			t.Fatalf("tls connect %d: %v", i, err)
		}

		for _, c := range []*Client{plain, secure} {
			models, err := c.Models(context.Background())
			if err != nil {
				t.Fatalf("Models over %s: %v", c.Route().Describe(), err)
			}
			if len(models) != 1 || models[0].ParameterSize != "3.2B" {
				t.Errorf("models over %s = %+v", c.Route().Describe(), models)
			}
		}
	}
}

// A wrong key must be rejected by the real auth middleware, over both transports.
func TestEndToEndRejectsWrongKey(t *testing.T) {
	s := newStack(t, "nope")

	prof := &config.Profile{
		ID: s.serverID, Name: "e2e", APIKey: "alp_wrongkeywrongkeywrongkeywrong",
		Fingerprint: s.fingerprint,
		LAN:         []string{s.endpoint},
	}

	// The health check is unauthenticated, so connecting still succeeds; the
	// failure has to surface on the first real call.
	c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := c.Models(context.Background()); err == nil {
		t.Fatal("Models succeeded with a wrong API key")
	} else if !strings.Contains(err.Error(), "re-link") {
		t.Errorf("error = %v, want guidance about re-linking", err)
	}
}

// Stopping generation mid-stream is a core TUI interaction; it must not leave
// the connection wedged for the next request.
func TestEndToEndCancelMidStreamThenReuse(t *testing.T) {
	tokens := make([]string, 200)
	for i := range tokens {
		tokens[i] = "tok "
	}
	s := newStack(t, tokens...)

	prof := &config.Profile{ID: s.serverID, Name: "e2e", APIKey: s.apiKey,
		Fingerprint: s.fingerprint, LAN: []string{s.endpoint}}

	c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	seen := 0
	err = c.Chat(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "go"}}},
		func(ch Chunk) error {
			seen++
			if seen == 5 {
				cancel()
			}
			return nil
		})
	if err == nil {
		t.Fatal("Chat returned success despite cancellation")
	}
	cancel()

	// The client must still be usable afterwards.
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer checkCancel()
	if _, err := c.Models(checkCtx); err != nil {
		t.Fatalf("client unusable after a cancelled stream: %v", err)
	}
}
