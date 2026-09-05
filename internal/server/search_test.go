package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/justin06lee/alpaca/internal/ollama"
	"github.com/justin06lee/alpaca/internal/search"
)

// stubSearch records queries and returns canned results.
type stubSearch struct {
	queries   atomic.Value // []string
	results   []search.Result
	err       error
	calls     atomic.Int32
	lastLimit atomic.Int32
}

func newStubSearch(results ...search.Result) *stubSearch {
	s := &stubSearch{results: results}
	s.queries.Store([]string{})
	return s
}

func (s *stubSearch) Name() string { return "stub" }
func (s *stubSearch) Search(_ context.Context, q string, limit int) ([]search.Result, error) {
	s.calls.Add(1)
	s.lastLimit.Store(int32(limit))
	s.queries.Store(append(s.queries.Load().([]string), q))
	if s.err != nil {
		return nil, s.err
	}
	return s.results, nil
}
func (s *stubSearch) seen() []string { return s.queries.Load().([]string) }

// scriptedDaemon replies with a different scripted turn on each call, so a tool
// round and the answer that follows it can both be driven. The mutex matters
// even though requests in these tests arrive sequentially: the handler runs on
// the server's goroutine, and appending unlocked would be a latent race.
func scriptedDaemon(t *testing.T, turns ...string) (http.HandlerFunc, *[]map[string]any) {
	var mu sync.Mutex
	var seen []map[string]any
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		// Tool attachment is gated on the model advertising the "tools"
		// capability, which the gateway learns from /api/show. The scripted
		// model supports tools; a model that does not is exercised separately.
		if r.URL.Path == "/api/show" {
			fmt.Fprint(w, `{"capabilities":["completion","tools"]}`)
			return
		}
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body)
		mu.Unlock()

		i := int(n.Add(1)) - 1
		if i >= len(turns) {
			i = len(turns) - 1
		}
		fmt.Fprint(w, turns[i])
	}, &seen
}

// TestSearchToolSkippedWhenModelLacksToolCapability guards the qwen case: a
// model whose template has no tool support must not have tools attached, even
// when the gateway has a search provider. Sending tools to such a model makes
// Ollama reject the whole request ("does not support tools"); the gateway must
// silently let it answer instead.
func TestSearchToolSkippedWhenModelLacksToolCapability(t *testing.T) {
	var seen []map[string]any
	daemon := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			fmt.Fprint(w, `{"capabilities":["completion","vision"]}`) // no "tools"
			return
		}
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, body)
		fmt.Fprint(w, answerTurn("hi"))
	}

	base := newSearchGateway(t, newStubSearch(), daemon)
	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(seen) == 0 {
		t.Fatal("no chat request reached the daemon")
	}
	if _, hasTools := seen[0]["tools"]; hasTools {
		t.Errorf("tools forwarded to a model without the tools capability")
	}
}

// toolCallTurn is a daemon reply asking for a web search.
func toolCallTurn(query string) string {
	return fmt.Sprintf(`{"message":{"role":"assistant","content":"","tool_calls":`+
		`[{"function":{"name":"web_search","arguments":{"query":%q}}}]},"done":false}`+"\n"+
		`{"message":{"content":""},"done":true,"done_reason":"stop"}`+"\n", query)
}

// answerTurn is a daemon reply that streams prose.
func answerTurn(words ...string) string {
	var b strings.Builder
	for _, word := range words {
		fmt.Fprintf(&b, `{"message":{"role":"assistant","content":%q},"done":false}`+"\n", word)
	}
	b.WriteString(`{"message":{"content":""},"done":true,"done_reason":"stop",` +
		`"prompt_eval_count":9,"eval_count":4}` + "\n")
	return b.String()
}

func newSearchGateway(t *testing.T, provider search.Provider, daemon http.HandlerFunc) string {
	t.Helper()
	upstream := httptest.NewServer(daemon)
	t.Cleanup(upstream.Close)

	client, err := ollama.New(upstream.URL)
	if err != nil {
		t.Fatalf("ollama.New: %v", err)
	}
	srv := New(Options{
		Ollama: client, APIKey: testKey, ID: "id", Name: "n", Version: "test",
		Search: provider, SearchResults: 3,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	gateway := httptest.NewServer(srv.Handler())
	t.Cleanup(gateway.Close)
	return gateway.URL
}

// ---------------------------------------------------------------------------

func TestSearchToolOfferedOnlyWhenEnabled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider search.Provider
		wantTool bool
	}{
		{"enabled", newStubSearch(), true},
		{"disabled", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, seen := scriptedDaemon(t, answerTurn("hi"))
			base := newSearchGateway(t, tc.provider, handler)

			resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
				`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}

			_, hasTools := (*seen)[0]["tools"]
			if hasTools != tc.wantTool {
				t.Errorf("tools forwarded = %v, want %v", hasTools, tc.wantTool)
			}
		})
	}
}

// The whole point: the model asks, the gateway searches, the model answers,
// and the client sees only the finished answer.
func TestGatewayRunsTheSearchAndAnswers(t *testing.T) {
	provider := newStubSearch(
		search.Result{Title: "Go 1.26", URL: "https://go.dev/doc/go1.26", Snippet: "It shipped."},
	)
	handler, seen := scriptedDaemon(t,
		toolCallTurn("go 1.26 release notes"),
		answerTurn("Go ", "1.26 ", "shipped."),
	)
	base := newSearchGateway(t, provider, handler)

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"what is in go 1.26?"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var content strings.Builder
	var statuses []string
	for _, ev := range sseEvents(t, resp.Body) {
		if ev == "[DONE]" {
			continue
		}
		var chunk chunkResponse
		if err := json.Unmarshal([]byte(ev), &chunk); err != nil {
			t.Fatalf("bad chunk %q: %v", ev, err)
		}
		if chunk.Alpaca != nil {
			statuses = append(statuses, chunk.Alpaca.Event+":"+chunk.Alpaca.Detail)
		}
		for _, c := range chunk.Choices {
			content.WriteString(c.Delta.Content)
		}
	}

	if got := provider.seen(); len(got) != 1 || got[0] != "go 1.26 release notes" {
		t.Errorf("provider saw %v, want the model's query", got)
	}
	if content.String() != "Go 1.26 shipped." {
		t.Errorf("answer = %q", content.String())
	}
	// The tool-call turn must not leak into the visible answer.
	if strings.Contains(content.String(), "web_search") {
		t.Errorf("tool call leaked into the answer: %q", content.String())
	}
	if len(statuses) != 1 || !strings.Contains(statuses[0], "go 1.26 release notes") {
		t.Errorf("status frames = %v, want one naming the query", statuses)
	}

	// The follow-up generation must carry the search results back to the model.
	if len(*seen) != 2 {
		t.Fatalf("daemon called %d times, want 2", len(*seen))
	}
	second, _ := json.Marshal((*seen)[1])
	for _, want := range []string{"tool", "go.dev/doc/go1.26", "It shipped."} {
		if !strings.Contains(string(second), want) {
			t.Errorf("second request is missing %q:\n%s", want, second)
		}
	}
}

// A stock OpenAI client iterates choices; the status frames must be invisible
// to it rather than corrupting the reply.
func TestStatusFramesAreInertForStandardClients(t *testing.T) {
	provider := newStubSearch(search.Result{Title: "T", URL: "https://x.example", Snippet: "s"})
	handler, _ := scriptedDaemon(t, toolCallTurn("q"), answerTurn("answer"))
	base := newSearchGateway(t, provider, handler)

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	for _, ev := range sseEvents(t, resp.Body) {
		if ev == "[DONE]" {
			continue
		}
		// Every frame must parse as a normal chunk with a choices array present.
		var generic struct {
			Object  string            `json:"object"`
			Choices []json.RawMessage `json:"choices"`
		}
		if err := json.Unmarshal([]byte(ev), &generic); err != nil {
			t.Fatalf("frame is not valid chunk JSON: %q", ev)
		}
		if generic.Object != "chat.completion.chunk" {
			t.Errorf("frame has object %q", generic.Object)
		}
		if generic.Choices == nil {
			t.Errorf("frame has a null choices array, which breaks strict clients: %s", ev)
		}
	}
}

func TestNonStreamingRunsSearchToo(t *testing.T) {
	provider := newStubSearch(search.Result{Title: "T", URL: "https://x.example", Snippet: "s"})
	handler, _ := scriptedDaemon(t, toolCallTurn("weather"), answerTurn("It is sunny."))
	base := newSearchGateway(t, provider, handler)

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","messages":[{"role":"user","content":"weather?"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var out completionResponse
	json.NewDecoder(resp.Body).Decode(&out)
	if got := out.Choices[0].Message.Content; got != "It is sunny." {
		t.Errorf("content = %q, want only the final answer", got)
	}
	if provider.calls.Load() != 1 {
		t.Errorf("provider called %d times, want 1", provider.calls.Load())
	}
}

// A model that keeps calling the tool must still produce an answer.
func TestRoundCapForcesAnAnswer(t *testing.T) {
	provider := newStubSearch(search.Result{Title: "T", URL: "https://x.example", Snippet: "s"})
	// Every scripted turn asks for another search; the daemon repeats the last
	// entry forever once the script runs out.
	handler, seen := scriptedDaemon(t, toolCallTurn("again"))
	base := newSearchGateway(t, provider, handler)

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)

	// defaultSearchRounds tool passes, then one final call with tools withheld.
	if len(*seen) != defaultSearchRounds+1 {
		t.Fatalf("daemon called %d times, want %d", len(*seen), defaultSearchRounds+1)
	}
	if _, hasTools := (*seen)[len(*seen)-1]["tools"]; hasTools {
		t.Error("the final generation still offered tools, so the model was never forced to answer")
	}
	if provider.calls.Load() != int32(defaultSearchRounds) {
		t.Errorf("provider called %d times, want %d", provider.calls.Load(), defaultSearchRounds)
	}
}

// A search outage must degrade to an answer from the model's own knowledge,
// not fail the request.
func TestSearchFailureStillProducesAnAnswer(t *testing.T) {
	provider := newStubSearch()
	provider.err = fmt.Errorf("searxng: connection refused")

	handler, seen := scriptedDaemon(t, toolCallTurn("q"), answerTurn("I could not check."))
	base := newSearchGateway(t, provider, handler)

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the turn to survive a search failure", resp.StatusCode)
	}

	var out completionResponse
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Choices[0].Message.Content != "I could not check." {
		t.Errorf("content = %q", out.Choices[0].Message.Content)
	}
	// The model must have been told what went wrong.
	second, _ := json.Marshal((*seen)[1])
	if !strings.Contains(string(second), "connection refused") {
		t.Errorf("the failure was not reported back to the model:\n%s", second)
	}
}

func TestInventedToolIsRejectedGracefully(t *testing.T) {
	provider := newStubSearch()
	handler, seen := scriptedDaemon(t,
		`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"launch_rocket","arguments":{}}}]},"done":true}`+"\n",
		answerTurn("Sorry."),
	)
	base := newSearchGateway(t, provider, handler)

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if provider.calls.Load() != 0 {
		t.Errorf("a search ran for an unknown tool")
	}
	second, _ := json.Marshal((*seen)[1])
	if !strings.Contains(string(second), "no tool named") {
		t.Errorf("model was not told the tool does not exist:\n%s", second)
	}
}

func TestToolCallWithoutQueryIsHandled(t *testing.T) {
	provider := newStubSearch()
	handler, seen := scriptedDaemon(t,
		`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"web_search","arguments":{}}}]},"done":true}`+"\n",
		answerTurn("ok"),
	)
	base := newSearchGateway(t, provider, handler)

	do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	if provider.calls.Load() != 0 {
		t.Error("searched with an empty query")
	}
	second, _ := json.Marshal((*seen)[1])
	if !strings.Contains(string(second), "non-empty `query`") {
		t.Errorf("model was not told the argument was missing:\n%s", second)
	}
}

// Ollama sends tool arguments as a JSON object; some builds send a JSON string.
// Both must yield the same query.
func TestToolArgumentsAcceptObjectOrString(t *testing.T) {
	for name, args := range map[string]string{
		"object": `{"query":"go 1.26"}`,
		"string": `"{\"query\":\"go 1.26\"}"`,
	} {
		t.Run(name, func(t *testing.T) {
			provider := newStubSearch()
			handler, _ := scriptedDaemon(t,
				fmt.Sprintf(`{"message":{"role":"assistant","tool_calls":`+
					`[{"function":{"name":"web_search","arguments":%s}}]},"done":true}`+"\n", args),
				answerTurn("done"),
			)
			base := newSearchGateway(t, provider, handler)

			do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
				`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

			got := provider.seen()
			if len(got) != 1 || got[0] != "go 1.26" {
				t.Errorf("provider saw %v, want [go 1.26]", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// /api/search
// ---------------------------------------------------------------------------

func TestSearchEndpoint(t *testing.T) {
	provider := newStubSearch(
		search.Result{Title: "T", URL: "https://x.example", Snippet: "s"},
	)
	handler, _ := scriptedDaemon(t, answerTurn("x"))
	base := newSearchGateway(t, provider, handler)

	resp := do(t, http.MethodPost, base+"/api/search", testKey, `{"query":"go 1.26","limit":3}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Query   string          `json:"query"`
		Results []search.Result `json:"results"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Query != "go 1.26" || len(out.Results) != 1 || out.Results[0].URL != "https://x.example" {
		t.Errorf("response = %+v", out)
	}
}

func TestSearchEndpointWhenDisabled(t *testing.T) {
	handler, _ := scriptedDaemon(t, answerTurn("x"))
	base := newSearchGateway(t, nil, handler)

	resp := do(t, http.MethodPost, base+"/api/search", testKey, `{"query":"anything"}`)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
	var body errorBody
	json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body.Error.Message, "--search") {
		t.Errorf("message = %q, want it to say how to enable search", body.Error.Message)
	}
}

func TestSearchEndpointRequiresQuery(t *testing.T) {
	handler, _ := scriptedDaemon(t, answerTurn("x"))
	base := newSearchGateway(t, newStubSearch(), handler)

	resp := do(t, http.MethodPost, base+"/api/search", testKey, `{"query":"   "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestInfoReportsSearchState(t *testing.T) {
	handler, _ := scriptedDaemon(t, answerTurn("x"))
	base := newSearchGateway(t, newStubSearch(), handler)

	resp := do(t, http.MethodGet, base+"/api/info", testKey, "")
	var info struct {
		Search struct {
			Enabled  bool   `json:"enabled"`
			Provider string `json:"provider"`
		} `json:"search"`
	}
	json.NewDecoder(resp.Body).Decode(&info)
	if !info.Search.Enabled || info.Search.Provider != "stub" {
		t.Errorf("search info = %+v", info.Search)
	}
}

// proseThenToolCallTurn is a daemon reply that streams prose and then asks for
// a search anyway.
func proseThenToolCallTurn(query string, words ...string) string {
	var b strings.Builder
	for _, word := range words {
		fmt.Fprintf(&b, `{"message":{"role":"assistant","content":%q},"done":false}`+"\n", word)
	}
	fmt.Fprintf(&b, `{"message":{"role":"assistant","content":"","tool_calls":`+
		`[{"function":{"name":"web_search","arguments":{"query":%q}}}]},"done":false}`+"\n", query)
	b.WriteString(`{"message":{"content":""},"done":true,"done_reason":"stop"}` + "\n")
	return b.String()
}

// Once the model has produced prose, that prose is the answer — on both paths.
// The buffered path used to discard it and run another round, so the same
// request could return different replies depending on the stream flag.
func TestBufferedPathKeepsProseEmittedAlongsideAToolCall(t *testing.T) {
	provider := newStubSearch(search.Result{Title: "t", URL: "https://x", Snippet: "s"})
	handler, seen := scriptedDaemon(t,
		proseThenToolCallTurn("late query", "The ", "answer."),
		answerTurn("should ", "never ", "run"),
	)
	base := newSearchGateway(t, provider, handler)

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","messages":[{"role":"user","content":"q"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body.Choices[0].Message.Content; got != "The answer." {
		t.Errorf("content = %q, want the prose the model produced", got)
	}
	if provider.calls.Load() != 0 {
		t.Errorf("search ran %d times after the model already answered", provider.calls.Load())
	}
	if len(*seen) != 1 {
		t.Errorf("daemon saw %d generations, want 1", len(*seen))
	}
}

// A frame can carry prose and a tool call together; the prose must reach the
// stream rather than being consumed with the call.
func TestStreamKeepsProseCarriedInAToolCallFrame(t *testing.T) {
	provider := newStubSearch()
	oneFrame := `{"message":{"role":"assistant","content":"Partial thought","tool_calls":` +
		`[{"function":{"name":"web_search","arguments":{"query":"x"}}}]},"done":false}` + "\n" +
		`{"message":{"content":""},"done":true,"done_reason":"stop"}` + "\n"
	handler, _ := scriptedDaemon(t, oneFrame)
	base := newSearchGateway(t, provider, handler)

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"q"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var content strings.Builder
	for _, ev := range sseEvents(t, resp.Body) {
		if ev == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(ev), &chunk); err != nil {
			t.Fatalf("bad chunk %q: %v", ev, err)
		}
		for _, c := range chunk.Choices {
			content.WriteString(c.Delta.Content)
		}
	}
	if !strings.Contains(content.String(), "Partial thought") {
		t.Errorf("stream dropped the prose that shared a frame with the tool call; got %q", content.String())
	}
}

// The gateway does not implement tool-call passthrough; this pins the current
// contract — a client-supplied tools field is dropped entirely rather than
// half-forwarded — so future passthrough support has to change it deliberately.
func TestClientSuppliedToolsAreDropped(t *testing.T) {
	handler, seen := scriptedDaemon(t, answerTurn("hi"))
	base := newSearchGateway(t, nil, handler)

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","messages":[{"role":"user","content":"x"}],`+
			`"tools":[{"type":"function","function":{"name":"my_tool"}}],"tool_choice":"auto"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if _, has := (*seen)[0]["tools"]; has {
		t.Error("client-supplied tools reached the daemon; passthrough is not implemented")
	}
}

// /api/search must not hand the response payload over to the client's choice
// of limit.
func TestSearchLimitIsClamped(t *testing.T) {
	provider := newStubSearch()
	handler, _ := scriptedDaemon(t, answerTurn("x"))
	base := newSearchGateway(t, provider, handler)

	resp := do(t, http.MethodPost, base+"/api/search", testKey, `{"query":"q","limit":100000}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := provider.lastLimit.Load(); got != maxSearchLimit {
		t.Errorf("provider received limit %d, want it clamped to %d", got, maxSearchLimit)
	}
}
