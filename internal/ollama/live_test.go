package ollama

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// These exercise a real Ollama daemon. They are opt-in because CI machines and
// most dev boxes will not have one running with models pulled.
//
//	ALPACA_LIVE=1 go test ./internal/ollama/ -run Live -v
func requireLive(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("ALPACA_LIVE") == "" {
		t.Skip("set ALPACA_LIVE=1 to run against a real ollama daemon")
	}
	host := os.Getenv("OLLAMA_HOST")
	c, err := New(host)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Version(ctx); err != nil {
		t.Skipf("no ollama daemon reachable: %v", err)
	}
	return c
}

func TestLiveVersionAndModels(t *testing.T) {
	c := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	version, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version == "" {
		t.Error("Version returned empty string")
	}
	t.Logf("daemon version %s", version)

	models, err := c.Models(ctx)
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) == 0 {
		t.Skip("daemon has no models pulled")
	}
	for _, m := range models {
		t.Logf("model %s (%s %s, %.1f GB)", m.Name, m.Details.ParameterSize,
			m.Details.Quantization, float64(m.Size)/1e9)
	}
}

func TestLiveChatStreams(t *testing.T) {
	c := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	models, err := c.Models(ctx)
	if err != nil || len(models) == 0 {
		t.Skip("no models available")
	}

	var reply strings.Builder
	var chunks int
	var stats Stats
	err = c.Chat(ctx, ChatRequest{
		Model: models[0].Name,
		Messages: []Message{
			{Role: "user", Content: "Reply with exactly the word: pong"},
		},
		Options: &Options{NumPredict: intPtr(16)},
	}, func(ch Chunk) error {
		reply.WriteString(ch.Content)
		chunks++
		if ch.Done {
			stats = ch.Stats
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Len() == 0 {
		t.Error("model produced no output")
	}
	// More than one frame is what proves streaming actually works end to end.
	if chunks < 2 {
		t.Errorf("got %d chunks, expected the reply to arrive incrementally", chunks)
	}
	if stats.EvalTokens == 0 {
		t.Error("final frame carried no token counts")
	}
	t.Logf("reply=%q chunks=%d tokens=%d/%d %.1f tok/s",
		strings.TrimSpace(reply.String()), chunks,
		stats.PromptTokens, stats.EvalTokens, stats.TokensPerSecond())
}

func intPtr(v int) *int { return &v }
