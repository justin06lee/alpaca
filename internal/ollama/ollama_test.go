package ollama

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient wires a Client to a stub daemon.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestChatStreamsChunksAndStats(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		// Ollama emits one JSON object per line, content arriving piecemeal.
		for _, line := range []string{
			`{"message":{"role":"assistant","content":"Hello"},"done":false}`,
			`{"message":{"role":"assistant","content":", "},"done":false}`,
			`{"message":{"role":"assistant","content":"world"},"done":false}`,
			`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop",` +
				`"prompt_eval_count":11,"eval_count":3,"total_duration":2000000000,"eval_duration":1000000000}`,
		} {
			w.Write([]byte(line + "\n"))
		}
	})

	var got strings.Builder
	var final Chunk
	err := c.Chat(context.Background(), ChatRequest{Model: "m"}, func(ch Chunk) error {
		got.WriteString(ch.Content)
		if ch.Done {
			final = ch
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got.String() != "Hello, world" {
		t.Errorf("assembled content = %q, want %q", got.String(), "Hello, world")
	}
	if !final.Done || final.Reason != "stop" {
		t.Errorf("final chunk = %+v, want done with reason stop", final)
	}
	if final.Stats.PromptTokens != 11 || final.Stats.EvalTokens != 3 {
		t.Errorf("token counts = %d/%d, want 11/3", final.Stats.PromptTokens, final.Stats.EvalTokens)
	}
	if final.Stats.TotalDuration != 2*time.Second {
		t.Errorf("total duration = %v, want 2s", final.Stats.TotalDuration)
	}
	if tps := final.Stats.TokensPerSecond(); tps != 3 {
		t.Errorf("TokensPerSecond() = %v, want 3", tps)
	}
}

func TestTokensPerSecondHandlesMissingTimings(t *testing.T) {
	if got := (Stats{EvalTokens: 5}).TokensPerSecond(); got != 0 {
		t.Errorf("TokensPerSecond() with no duration = %v, want 0", got)
	}
	if got := (Stats{EvalDuration: time.Second}).TokensPerSecond(); got != 0 {
		t.Errorf("TokensPerSecond() with no tokens = %v, want 0", got)
	}
}

// An error can arrive mid-stream, after a 200 has already been written.
func TestChatSurfacesMidStreamError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":{"content":"partial"},"done":false}` + "\n"))
		w.Write([]byte(`{"error":"model requires more system memory"}` + "\n"))
	})

	err := c.Chat(context.Background(), ChatRequest{Model: "m"}, func(Chunk) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "more system memory") {
		t.Fatalf("Chat error = %v, want the daemon's message", err)
	}
}

// A daemon that dies mid-generation closes the body with no done frame. That
// must not look like a successful (truncated) reply.
func TestChatDetectsTruncatedStream(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":{"content":"half a th"},"done":false}` + "\n"))
	})

	err := c.Chat(context.Background(), ChatRequest{Model: "m"}, func(Chunk) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "closed the stream") {
		t.Fatalf("Chat error = %v, want a truncation error", err)
	}
}

func TestChatAbortsWhenCallbackFails(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 100; i++ {
			w.Write([]byte(`{"message":{"content":"x"},"done":false}` + "\n"))
		}
	})

	stop := errors.New("caller stopped")
	seen := 0
	err := c.Chat(context.Background(), ChatRequest{Model: "m"}, func(Chunk) error {
		seen++
		if seen == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("Chat error = %v, want the callback's error", err)
	}
	if seen != 3 {
		t.Errorf("callback ran %d times after aborting, want 3", seen)
	}
}

func TestChatReportsAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"model 'nope' not found"}`))
	})

	err := c.Chat(context.Background(), ChatRequest{Model: "nope"}, func(Chunk) error { return nil })
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Chat error = %v (%T), want *APIError", err, err)
	}
	if !apiErr.NotFound() {
		t.Errorf("NotFound() = false, want true for status %d", apiErr.Status)
	}
	if !strings.Contains(apiErr.Error(), "not found") {
		t.Errorf("error text %q lost the daemon's message", apiErr.Error())
	}
}

func TestModelsParsesTags(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"llama3.2:latest","size":2019393189,` +
			`"details":{"family":"llama","parameter_size":"3.2B","quantization_level":"Q4_K_M"}}]}`))
	})

	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].Name != "llama3.2:latest" {
		t.Fatalf("Models() = %+v", models)
	}
	if models[0].Details.ParameterSize != "3.2B" {
		t.Errorf("parameter size = %q, want 3.2B", models[0].Details.ParameterSize)
	}
}

func TestNewNormalizesBareHostPort(t *testing.T) {
	c, err := New("192.168.1.5:11434")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.BaseURL(); got != "http://192.168.1.5:11434" {
		t.Errorf("BaseURL() = %q, want the http scheme filled in", got)
	}
}

func TestNewDefaultsToLocalDaemon(t *testing.T) {
	c, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.BaseURL() != DefaultHost {
		t.Errorf("BaseURL() = %q, want %q", c.BaseURL(), DefaultHost)
	}
}

func TestChatCancellationPropagates(t *testing.T) {
	release := make(chan struct{})
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":{"content":"a"},"done":false}` + "\n"))
		w.(http.Flusher).Flush()
		<-release
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	err := c.Chat(ctx, ChatRequest{Model: "m"}, func(Chunk) error {
		cancel() // cancel from inside the stream, as the TUI's stop key does
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat error = %v, want context.Canceled", err)
	}
}
