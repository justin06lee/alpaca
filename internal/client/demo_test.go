package client

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newDemo(t *testing.T) *Client {
	t.Helper()
	c, stop, err := NewDemo()
	if err != nil {
		t.Fatalf("NewDemo: %v", err)
	}
	t.Cleanup(stop)
	return c
}

func TestDemoStreamsAReply(t *testing.T) {
	c := newDemo(t)

	var reply strings.Builder
	var chunks int
	var usage *Usage
	err := c.Chat(context.Background(), ChatRequest{
		Model:    "llama3.2:latest",
		Messages: []Message{{Role: RoleUser, Content: "hello there"}},
	}, func(ch Chunk) error {
		reply.WriteString(ch.Content)
		if ch.Content != "" {
			chunks++
		}
		if ch.Usage != nil {
			usage = ch.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if !strings.Contains(reply.String(), "demo mode") {
		t.Errorf("reply did not identify itself as demo mode: %q", reply.String())
	}
	// Arriving in one lump would defeat the point of previewing the interface.
	if chunks < 5 {
		t.Errorf("reply arrived in %d chunks, want it streamed", chunks)
	}
	if usage == nil || usage.TotalTokens == 0 {
		t.Errorf("usage = %+v, want token counts so the status bar has something", usage)
	}
}

// Each canned reply exercises a different part of the renderer.
func TestDemoRepliesCoverTheRenderer(t *testing.T) {
	c := newDemo(t)

	cases := map[string]string{
		"write me a go function": "```go",
		"show me markdown":       "| Column |",
		"hello":                  "**demo mode**",
		"anything else entirely": "canned reply",
	}
	for prompt, want := range cases {
		t.Run(prompt, func(t *testing.T) {
			var reply strings.Builder
			err := c.Chat(context.Background(), ChatRequest{
				Model:    "m",
				Messages: []Message{{Role: RoleUser, Content: prompt}},
			}, func(ch Chunk) error {
				reply.WriteString(ch.Content)
				return nil
			})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if !strings.Contains(reply.String(), want) {
				t.Errorf("reply to %q is missing %q:\n%s", prompt, want, reply.String())
			}
		})
	}
}

// Demo mode should exercise the search status line too, since that is part of
// what someone previewing the interface wants to see.
func TestDemoEmitsSearchStatus(t *testing.T) {
	c := newDemo(t)

	var events []string
	err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "search for the latest news"}},
	}, func(ch Chunk) error {
		if ch.Event != "" {
			events = append(events, ch.Event)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(events) != 1 || events[0] != "search" {
		t.Errorf("events = %v, want one search event", events)
	}
}

func TestDemoModelsAndInfo(t *testing.T) {
	c := newDemo(t)
	ctx := context.Background()

	models, err := c.Models(ctx)
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) < 2 {
		t.Fatalf("got %d models, want several so the picker has content", len(models))
	}
	if models[0].ParameterSize == "" {
		t.Errorf("model metadata missing: %+v", models[0])
	}

	info, err := c.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "demo" {
		t.Errorf("info.Name = %q, want demo", info.Name)
	}
}

func TestDemoSearchEndpoint(t *testing.T) {
	c := newDemo(t)

	results, err := c.Search(context.Background(), "go 1.26", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 || results[0].URL == "" {
		t.Fatalf("results = %+v", results)
	}
}

// Stopping generation is a core interaction; it has to work in demo mode too,
// or the preview misrepresents the real thing.
func TestDemoRespectsCancellation(t *testing.T) {
	c := newDemo(t)

	ctx, cancel := context.WithCancel(context.Background())
	seen := 0
	start := time.Now()
	err := c.Chat(ctx, ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "show me markdown"}},
	}, func(ch Chunk) error {
		if ch.Content != "" {
			seen++
			if seen == 3 {
				cancel()
			}
		}
		return nil
	})
	if err == nil {
		t.Fatal("Chat returned success despite cancellation")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("cancellation took %v, want it to stop promptly", elapsed)
	}
}

func TestDemoRouteIsLabelledAsDemo(t *testing.T) {
	c := newDemo(t)
	if c.Route().Source != SourceDemo {
		t.Errorf("route source = %q, want %q", c.Route().Source, SourceDemo)
	}
}

func TestTokeniseReassemblesExactly(t *testing.T) {
	for _, in := range []string{
		"hello world",
		"a\nb\nc",
		"trailing space ",
		"# Heading\n\nSome **text** here.\n",
		"",
	} {
		if got := strings.Join(tokenise(in), ""); got != in {
			t.Errorf("tokenise(%q) reassembles to %q", in, got)
		}
	}
}
