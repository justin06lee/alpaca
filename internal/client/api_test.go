package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/justin06lee/alpaca/internal/config"
)

// connectTo builds a client pointed at a fake gateway.
func connectTo(t *testing.T, extra http.HandlerFunc) *Client {
	t.Helper()
	srv := fakeGateway(t, "server-1", extra)
	prof := &config.Profile{
		ID:     "server-1",
		Name:   "test",
		APIKey: testKey,
		LAN:    []string{hostPort(srv.URL)},
	}
	c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

func TestChatAssemblesStream(t *testing.T) {
	c := connectTo(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testKey {
			t.Errorf("Authorization = %q, want the profile key", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],"+
			"\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	var text strings.Builder
	var finish string
	var usage *Usage
	var sawDone bool

	err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(ch Chunk) error {
		text.WriteString(ch.Content)
		if ch.FinishReason != "" {
			finish = ch.FinishReason
		}
		if ch.Usage != nil {
			usage = ch.Usage
		}
		if ch.Done {
			sawDone = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if text.String() != "Hello" {
		t.Errorf("content = %q, want %q", text.String(), "Hello")
	}
	if finish != "stop" {
		t.Errorf("finish_reason = %q, want stop", finish)
	}
	if usage == nil || usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want 7 total tokens", usage)
	}
	if !sawDone {
		t.Error("callback never received a Done chunk")
	}
}

// The gateway reports a post-headers failure as an error frame in the stream.
func TestChatSurfacesStreamErrorFrame(t *testing.T) {
	c := connectTo(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"gpu ran out of memory\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	err := c.Chat(context.Background(), ChatRequest{Model: "m"}, func(Chunk) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "gpu ran out of memory") {
		t.Fatalf("Chat error = %v, want the server's message", err)
	}
}

// A dropped connection must not look like a complete answer.
func TestChatDetectsTruncatedStream(t *testing.T) {
	c := connectTo(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"half a sen\"}}]}\n\n")
		// No [DONE]: the connection simply ends.
	})

	err := c.Chat(context.Background(), ChatRequest{Model: "m"}, func(Chunk) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "closed before the reply finished") {
		t.Fatalf("Chat error = %v, want a truncation error", err)
	}
}

func TestChatPropagatesCallbackError(t *testing.T) {
	c := connectTo(t, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 50; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%d\"}}]}\n\n", i)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	stop := errors.New("user pressed escape")
	err := c.Chat(context.Background(), ChatRequest{Model: "m"}, func(Chunk) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("Chat error = %v, want the callback's error", err)
	}
}

func TestChatCancellation(t *testing.T) {
	c := connectTo(t, func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 1000; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%d \"}}]}\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	err := c.Chat(ctx, ChatRequest{Model: "m"}, func(Chunk) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat error = %v, want context.Canceled", err)
	}
}

// An expired key is a common real failure; the message must say what to do.
func TestUnauthorizedExplainsHowToRecover(t *testing.T) {
	c := connectTo(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"missing or invalid API key","type":"authentication_error"}}`)
	})

	_, err := c.Models(context.Background())
	if err == nil {
		t.Fatal("Models succeeded against a 401")
	}
	if !strings.Contains(err.Error(), "re-link") {
		t.Errorf("error = %v, want it to suggest re-linking", err)
	}
}

func TestModelsDecodesMetadata(t *testing.T) {
	c := connectTo(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[
			{"id":"llama3.2:latest","size":2019393189,"parameter_size":"3.2B","quantization":"Q4_K_M","family":"llama"}]}`)
	})

	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "llama3.2:latest" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ParameterSize != "3.2B" || models[0].Family != "llama" {
		t.Errorf("metadata lost: %+v", models[0])
	}
}

func TestInfoDecodes(t *testing.T) {
	c := connectTo(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"server-1","name":"workshop","version":"1.0","models":3,
			"ollama":{"version":"0.32.5","url":"http://127.0.0.1:11434"}}`)
	})

	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "workshop" || info.Models != 3 || info.Ollama.Version != "0.32.5" {
		t.Errorf("info = %+v", info)
	}
}

func TestRouteDescribeIsReadable(t *testing.T) {
	r := Route{Endpoint: "192.168.1.20:8080", Source: SourceMDNS, Latency: 1234567}
	got := r.Describe()
	for _, want := range []string{"192.168.1.20:8080", "mdns", "http"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe() = %q, missing %q", got, want)
		}
	}

	secure := Route{Endpoint: "203.0.113.9:8080", Source: SourcePublic, TLS: true}
	if !strings.Contains(secure.Describe(), "tls") {
		t.Errorf("Describe() = %q, want it to mention tls", secure.Describe())
	}
}
