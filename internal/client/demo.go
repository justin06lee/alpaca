package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/justin06lee/alpaca/internal/config"
)

// NewDemo returns a client backed by a canned server running inside this
// process, so the chat interface can be driven with no gateway, no ollama, and
// no network.
//
// It serves over a real loopback socket rather than faking the transport. That
// costs one ephemeral port while the TUI is open and buys certainty: the client
// parses genuine SSE frames over a genuine connection, so what you see in demo
// mode is what the interface does for real.
//
// The returned function shuts the server down.
func NewDemo() (*Client, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("start demo server: %w", err)
	}

	srv := &http.Server{Handler: demoHandler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(listener)

	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}

	profile := &config.Profile{
		ID:     "demo",
		Name:   "demo",
		APIKey: "demo",
		Model:  demoModels[0].ID,
	}
	c, err := newClient(profile, Route{
		Endpoint: listener.Addr().String(),
		Source:   SourceDemo,
	})
	if err != nil {
		stop()
		return nil, nil, err
	}
	return c, stop, nil
}

var demoModels = []Model{
	{ID: "llama3.2:latest", ParameterSize: "3.2B", Quantization: "Q4_K_M", Family: "llama", Size: 2019393189},
	{ID: "qwen2.5:7b", ParameterSize: "7B", Quantization: "Q4_K_M", Family: "qwen2", Size: 4683087519},
	{ID: "mistral:latest", ParameterSize: "7B", Quantization: "Q5_K_M", Family: "llama", Size: 5137025024},
}

func demoHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeDemoJSON(w, map[string]any{"ok": true, "id": "demo", "name": "demo", "service": "alpaca"})
	})

	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		writeDemoJSON(w, map[string]any{
			"id": "demo", "name": "demo", "version": "demo", "models": len(demoModels),
			"ollama": map[string]any{"version": "offline", "url": "none"},
			"search": map[string]any{"enabled": true, "provider": "demo"},
		})
	})

	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		data := make([]map[string]any, 0, len(demoModels))
		for _, m := range demoModels {
			data = append(data, map[string]any{
				"id": m.ID, "object": "model", "owned_by": "demo",
				"parameter_size": m.ParameterSize, "quantization": m.Quantization,
				"family": m.Family, "size": m.Size,
			})
		}
		writeDemoJSON(w, map[string]any{"object": "list", "data": data})
	})

	mux.HandleFunc("POST /api/search", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		writeDemoJSON(w, map[string]any{
			"query": req.Query, "provider": "demo",
			"results": demoSearchResults(req.Query),
		})
	})

	mux.HandleFunc("POST /v1/chat/completions", demoChat)
	return mux
}

func writeDemoJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func demoSearchResults(query string) []map[string]string {
	return []map[string]string{
		{"title": "Result for " + query, "url": "https://example.com/one",
			"snippet": "A canned search result. In demo mode nothing leaves this machine."},
		{"title": query + " — reference", "url": "https://example.com/two",
			"snippet": "A second result, so the formatting has more than one entry to lay out."},
	}
}

// demoChat streams a canned reply, chosen to exercise whichever part of the
// interface the prompt is likely asking about.
func demoChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}

	prompt := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			prompt = req.Messages[i].Content
			break
		}
	}

	reply, searched := demoReply(prompt)

	ctrl := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	id := "chatcmpl-demo"
	created := time.Now().Unix()
	emit := func(payload map[string]any) bool {
		raw, _ := json.Marshal(payload)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return false
		}
		return ctrl.Flush() == nil
	}
	frame := func(extra map[string]any) map[string]any {
		out := map[string]any{"id": id, "object": "chat.completion.chunk",
			"created": created, "model": req.Model}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	emit(frame(map[string]any{"choices": []any{
		map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}}}}))

	if searched {
		// Exercises the search status line the real gateway emits.
		emit(frame(map[string]any{
			"choices": []any{},
			"alpaca":  map[string]any{"event": "search", "detail": prompt + " — 2 results"},
		}))
		time.Sleep(400 * time.Millisecond)
	}

	// Word-by-word with a small delay, so streaming looks like streaming.
	for _, token := range tokenise(reply) {
		select {
		case <-r.Context().Done():
			return // the user pressed esc; stop generating
		case <-time.After(22 * time.Millisecond):
		}
		if !emit(frame(map[string]any{"choices": []any{
			map[string]any{"index": 0, "delta": map[string]any{"content": token}}}})) {
			return
		}
	}

	stop := frame(map[string]any{"choices": []any{
		map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		words := len(strings.Fields(reply))
		stop["usage"] = map[string]any{
			"prompt_tokens":     len(strings.Fields(prompt)) + 8,
			"completion_tokens": words,
			"total_tokens":      len(strings.Fields(prompt)) + 8 + words,
		}
	}
	emit(stop)
	fmt.Fprint(w, "data: [DONE]\n\n")
	_ = ctrl.Flush()
}

// tokenise splits text the way a model streams it: words with their trailing
// space, so the rendered result reassembles exactly.
func tokenise(s string) []string {
	var out []string
	var current strings.Builder
	for _, r := range s {
		current.WriteRune(r)
		if r == ' ' || r == '\n' {
			out = append(out, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

// hasWord reports whether text contains any of these as a whole word.
//
// Substring matching is a trap here: strings.Contains(prompt, "hi") fires on
// "anything", "this", "which", and "machine", so an unrelated question gets
// greeted instead of answered.
func hasWord(text string, words ...string) bool {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	present := make(map[string]bool, len(fields))
	for _, f := range fields {
		present[f] = true
	}
	for _, w := range words {
		if present[w] {
			return true
		}
	}
	return false
}

// demoReply picks a canned answer. Each one is written to show off a different
// part of the renderer, so poking at demo mode reveals what the interface can
// actually do.
func demoReply(prompt string) (reply string, searched bool) {
	switch {
	case hasWord(prompt, "search", "latest", "news", "current", "today"):
		return "Based on what I found:\n\n" +
			"- The first result covers the main question directly.\n" +
			"- The second adds context worth knowing.\n\n" +
			"Sources: <https://example.com/one>, <https://example.com/two>\n\n" +
			"*This is demo mode — those results are canned and nothing was fetched.*", true

	case hasWord(prompt, "code", "go", "golang", "rust", "python", "javascript", "function", "write"):
		return "Here's one way to do it:\n\n" +
			"```go\n" +
			"func Reverse(s string) string {\n" +
			"\trunes := []rune(s)\n" +
			"\tfor i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {\n" +
			"\t\trunes[i], runes[j] = runes[j], runes[i]\n" +
			"\t}\n" +
			"\treturn string(runes)\n" +
			"}\n" +
			"```\n\n" +
			"Converting to `[]rune` first matters: indexing a string yields bytes, so a " +
			"byte-wise reverse corrupts any multi-byte character.", false

	case hasWord(prompt, "hello", "hi", "hey", "yo", "greetings"):
		return "Hello. You're in **demo mode**, so there's no model behind this — " +
			"the replies are canned and nothing leaves your machine.\n\n" +
			"Try `/help` for the keys, `ctrl+p` to open the model picker, or ask for " +
			"some code to see a syntax-highlighted block.", false

	case hasWord(prompt, "markdown", "format", "formatting", "table", "render"):
		return "# Heading\n\nA paragraph with **bold**, *italic*, and `inline code`.\n\n" +
			"1. An ordered list\n2. With a second entry\n\n" +
			"> A block quote, for good measure.\n\n" +
			"| Column | Value |\n| --- | --- |\n| one | 1 |\n| two | 2 |\n\n" +
			"---\n\nAnd a closing line.", false

	default:
		return "You said: *" + strings.TrimSpace(prompt) + "*\n\n" +
			"This is **demo mode** — a canned reply streamed from inside the alpaca " +
			"binary itself. There's no model and no network.\n\n" +
			"Things worth trying:\n\n" +
			"- ask for **code** to see a fenced block\n" +
			"- ask about **markdown** to see tables and quotes\n" +
			"- mention **search** to see the web-search status line\n" +
			"- press `ctrl+p` for models, `ctrl+s` for saved chats, `?` for keys", false
	}
}
