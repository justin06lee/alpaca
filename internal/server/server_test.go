package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justin06lee/alpaca/internal/ollama"
)

const testKey = "alp_testkeytestkeytestkeytestkey00"

// newGateway stands up the full gateway (middleware included) in front of a
// stub ollama daemon, and returns its base URL.
func newGateway(t *testing.T, daemon http.HandlerFunc) string {
	t.Helper()

	upstream := httptest.NewServer(daemon)
	t.Cleanup(upstream.Close)

	client, err := ollama.New(upstream.URL)
	if err != nil {
		t.Fatalf("ollama.New: %v", err)
	}

	srv := New(Options{
		Ollama:  client,
		APIKey:  testKey,
		ID:      "server-id",
		Name:    "test-box",
		Version: "test",
		// Keep test output readable; errors are asserted, not read.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	gateway := httptest.NewServer(srv.Handler())
	t.Cleanup(gateway.Close)
	return gateway.URL
}

// do issues an authenticated request unless key is empty.
func do(t *testing.T, method, url, key, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// streamingDaemon replies with the given content split into per-token frames.
func streamingDaemon(tokens ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, tok := range tokens {
			fmt.Fprintf(w, `{"message":{"role":"assistant","content":%q},"done":false}`+"\n", tok)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		fmt.Fprint(w, `{"message":{"content":""},"done":true,"done_reason":"stop",`+
			`"prompt_eval_count":7,"eval_count":3,"total_duration":1000000000,"eval_duration":500000000}`+"\n")
	}
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

func TestAuthRejectsMissingAndWrongKeys(t *testing.T) {
	base := newGateway(t, streamingDaemon("hi"))

	cases := []struct {
		name string
		key  string
	}{
		{"no key", ""},
		{"wrong key", "alp_wrongwrongwrongwrongwrongwron"},
		{"right length wrong value", strings.Repeat("x", len(testKey))},
		{"prefix of the real key", testKey[:len(testKey)-1]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, http.MethodGet, base+"/v1/models", tc.key, "")
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			// The error must be in OpenAI's envelope so clients surface it.
			var body errorBody
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body.Error.Type != "authentication_error" {
				t.Errorf("error type = %q, want authentication_error", body.Error.Type)
			}
		})
	}
}

func TestAuthAcceptsBearerAndApiKeyHeader(t *testing.T) {
	base := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[]}`)
	})

	resp := do(t, http.MethodGet, base+"/v1/models", testKey, "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("bearer auth: status = %d, want 200", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", testKey)
	alt, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("x-api-key request: %v", err)
	}
	defer alt.Body.Close()
	if alt.StatusCode != http.StatusOK {
		t.Errorf("x-api-key auth: status = %d, want 200", alt.StatusCode)
	}
}

// A server configured with no key must refuse everything rather than run open.
func TestEmptyConfiguredKeyRefusesAll(t *testing.T) {
	s := New(Options{APIKey: "", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if s.keyMatches("") {
		t.Error("empty presented key matched an empty configured key — server would be wide open")
	}
	if s.keyMatches("anything") {
		t.Error("arbitrary key matched an empty configured key")
	}
}

func TestHealthIsUnauthenticated(t *testing.T) {
	base := newGateway(t, streamingDaemon())

	resp := do(t, http.MethodGet, base+"/healthz", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		OK   bool   `json:"ok"`
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	// The dial race matches on ID to confirm it found the right server.
	if !body.OK || body.ID != "server-id" || body.Name != "test-box" {
		t.Errorf("health body = %+v, want ok with the server identity", body)
	}
}

// ---------------------------------------------------------------------------
// Non-streaming chat
// ---------------------------------------------------------------------------

func TestChatCompletionNonStreaming(t *testing.T) {
	base := newGateway(t, streamingDaemon("Hello", ", ", "world"))

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}

	var out completionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Object != "chat.completion" || len(out.Choices) != 1 {
		t.Fatalf("response = %+v", out)
	}
	if got := out.Choices[0].Message.Content; got != "Hello, world" {
		t.Errorf("content = %q, want %q", got, "Hello, world")
	}
	if out.Choices[0].Message.Role != "assistant" {
		t.Errorf("role = %q, want assistant", out.Choices[0].Message.Role)
	}
	if out.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", out.Choices[0].FinishReason)
	}
	if out.Usage == nil || out.Usage.PromptTokens != 7 || out.Usage.CompletionTokens != 3 || out.Usage.TotalTokens != 10 {
		t.Errorf("usage = %+v, want 7/3/10", out.Usage)
	}
	if !strings.HasPrefix(out.ID, "chatcmpl-") {
		t.Errorf("id = %q, want a chatcmpl- prefix", out.ID)
	}
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

// sseEvents splits an SSE body into its data payloads.
func sseEvents(t *testing.T, body io.Reader) []string {
	t.Helper()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	var out []string
	for _, block := range strings.Split(string(raw), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if !strings.HasPrefix(block, "data: ") {
			t.Errorf("stream block is not an SSE data frame: %q", block)
			continue
		}
		out = append(out, strings.TrimPrefix(block, "data: "))
	}
	return out
}

func TestChatCompletionStreaming(t *testing.T) {
	base := newGateway(t, streamingDaemon("Hel", "lo", "!"))

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","stream":true,"stream_options":{"include_usage":true},`+
			`"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}

	events := sseEvents(t, resp.Body)
	if len(events) < 3 {
		t.Fatalf("got %d events, want at least 3: %v", len(events), events)
	}
	if events[len(events)-1] != "[DONE]" {
		t.Errorf("last event = %q, want [DONE]", events[len(events)-1])
	}

	var content strings.Builder
	var sawRole bool
	var finish string
	var gotUsage *usage
	for _, ev := range events[:len(events)-1] {
		var chunk chunkResponse
		if err := json.Unmarshal([]byte(ev), &chunk); err != nil {
			t.Fatalf("event %q is not a chunk: %v", ev, err)
		}
		if chunk.Object != "chat.completion.chunk" {
			t.Errorf("object = %q, want chat.completion.chunk", chunk.Object)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if chunk.Choices[0].Delta.Role == "assistant" {
			sawRole = true
		}
		content.WriteString(chunk.Choices[0].Delta.Content)
		if chunk.Choices[0].FinishReason != nil {
			finish = *chunk.Choices[0].FinishReason
		}
		if chunk.Usage != nil {
			gotUsage = chunk.Usage
		}
	}

	if !sawRole {
		t.Error("stream never announced the assistant role")
	}
	if content.String() != "Hello!" {
		t.Errorf("assembled content = %q, want %q", content.String(), "Hello!")
	}
	if finish != "stop" {
		t.Errorf("finish_reason = %q, want stop", finish)
	}
	if gotUsage == nil || gotUsage.TotalTokens != 10 {
		t.Errorf("usage = %+v, want 10 total tokens (include_usage was set)", gotUsage)
	}
}

// Without stream_options.include_usage, OpenAI omits usage entirely.
func TestStreamingOmitsUsageUnlessRequested(t *testing.T) {
	base := newGateway(t, streamingDaemon("x"))

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	for _, ev := range sseEvents(t, resp.Body) {
		if ev == "[DONE]" {
			continue
		}
		var chunk chunkResponse
		json.Unmarshal([]byte(ev), &chunk)
		if chunk.Usage != nil {
			t.Errorf("usage present without include_usage: %+v", chunk.Usage)
		}
	}
}

// A failure before any token must produce a real HTTP status, not a 200 with an
// error buried in the stream.
func TestStreamingFailureBeforeFirstTokenUsesHTTPStatus(t *testing.T) {
	base := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"model 'ghost' not found, try pulling it first"}`)
	})

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"ghost","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 propagated from the daemon", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json for a pre-stream error", ct)
	}
	var body errorBody
	json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body.Error.Message, "not found") {
		t.Errorf("message = %q, want the daemon's explanation", body.Error.Message)
	}
}

// Once the stream has begun the status is committed, so a later failure has to
// arrive as an error event followed by a well-formed terminator.
func TestStreamingFailureMidStreamEmitsErrorEvent(t *testing.T) {
	base := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"message":{"content":"partial"},"done":false}`+"\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		fmt.Fprint(w, `{"error":"gpu ran out of memory"}`+"\n")
	})

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (headers were already sent)", resp.StatusCode)
	}

	events := sseEvents(t, resp.Body)
	if events[len(events)-1] != "[DONE]" {
		t.Errorf("stream did not terminate with [DONE]: %v", events)
	}

	var sawError bool
	for _, ev := range events {
		if strings.Contains(ev, "gpu ran out of memory") {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("mid-stream error was not reported to the client: %v", events)
	}
}

// ---------------------------------------------------------------------------
// Request translation
// ---------------------------------------------------------------------------

// captureRequest records what the gateway forwarded to the daemon.
func captureRequest(t *testing.T, body string) map[string]any {
	t.Helper()
	captured := make(chan map[string]any, 1)
	base := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		captured <- got
		fmt.Fprint(w, `{"message":{"content":"ok"},"done":true,"done_reason":"stop"}`+"\n")
	})

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey, body)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
	return <-captured
}

func TestTranslatesSamplingOptions(t *testing.T) {
	got := captureRequest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}],
		"temperature":0.25,"top_p":0.8,"seed":42,"max_tokens":128,"stop":"END"}`)

	opts, ok := got["options"].(map[string]any)
	if !ok {
		t.Fatalf("no options forwarded: %+v", got)
	}
	if opts["temperature"] != 0.25 || opts["top_p"] != 0.8 {
		t.Errorf("sampling options = %+v", opts)
	}
	if opts["seed"] != float64(42) {
		t.Errorf("seed = %v, want 42", opts["seed"])
	}
	if opts["num_predict"] != float64(128) {
		t.Errorf("num_predict = %v, want 128 (from max_tokens)", opts["num_predict"])
	}
	// `stop` may arrive as a bare string and must become a list.
	stop, ok := opts["stop"].([]any)
	if !ok || len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop = %v, want [\"END\"]", opts["stop"])
	}
}

func TestStopAcceptsArray(t *testing.T) {
	got := captureRequest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}],
		"stop":["A","B"]}`)
	opts := got["options"].(map[string]any)
	stop, ok := opts["stop"].([]any)
	if !ok || len(stop) != 2 {
		t.Fatalf("stop = %v, want two entries", opts["stop"])
	}
}

// max_completion_tokens is the current spelling and must win over the
// deprecated max_tokens when both appear.
func TestMaxCompletionTokensTakesPrecedence(t *testing.T) {
	got := captureRequest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}],
		"max_tokens":10,"max_completion_tokens":99}`)
	opts := got["options"].(map[string]any)
	if opts["num_predict"] != float64(99) {
		t.Errorf("num_predict = %v, want 99", opts["num_predict"])
	}
}

func TestJSONResponseFormatSetsOllamaFormat(t *testing.T) {
	got := captureRequest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}],
		"response_format":{"type":"json_object"}}`)
	if got["format"] != "json" {
		t.Errorf("format = %v, want json", got["format"])
	}
}

// Multimodal content arrays must flatten to text plus raw base64 images.
func TestContentPartsCarryTextAndImages(t *testing.T) {
	got := captureRequest(t, `{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"what is this?"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}
	]}]}`)

	msgs := got["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["content"] != "what is this?" {
		t.Errorf("content = %v, want the text part", first["content"])
	}
	images, ok := first["images"].([]any)
	if !ok || len(images) != 1 || images[0] != "aGVsbG8=" {
		t.Errorf("images = %v, want the decoded base64 payload", first["images"])
	}
}

// Fetching a remote image on the client's behalf would make the gateway an SSRF
// proxy into whatever network it runs on.
func TestRemoteImageURLIsRefused(t *testing.T) {
	base := newGateway(t, streamingDaemon("x"))

	resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey,
		`{"model":"m","messages":[{"role":"user","content":[
			{"type":"image_url","image_url":{"url":"http://169.254.169.254/latest/meta-data/"}}
		]}]}`)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a remote image url", resp.StatusCode)
	}
	var body errorBody
	json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body.Error.Message, "does not fetch remote images") {
		t.Errorf("message = %q, want an explicit refusal", body.Error.Message)
	}
}

func TestRejectsMissingModelAndMessages(t *testing.T) {
	base := newGateway(t, streamingDaemon("x"))

	cases := map[string]string{
		"no model":       `{"messages":[{"role":"user","content":"hi"}]}`,
		"blank model":    `{"model":"  ","messages":[{"role":"user","content":"hi"}]}`,
		"no messages":    `{"model":"m"}`,
		"empty messages": `{"model":"m","messages":[]}`,
		"no role":        `{"model":"m","messages":[{"content":"hi"}]}`,
		"bad json":       `{"model":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp := do(t, http.MethodPost, base+"/v1/chat/completions", testKey, body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Models, embeddings, CORS
// ---------------------------------------------------------------------------

func TestListModelsMapsToOpenAIShape(t *testing.T) {
	base := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"llama3.2:latest","size":2019393189,
			"modified_at":"2024-01-02T03:04:05Z",
			"details":{"family":"llama","parameter_size":"3.2B","quantization_level":"Q4_K_M"}}]}`)
	})

	resp := do(t, http.MethodGet, base+"/v1/models", testKey, "")
	var out struct {
		Object string `json:"object"`
		Data   []struct {
			ID            string `json:"id"`
			Object        string `json:"object"`
			OwnedBy       string `json:"owned_by"`
			ParameterSize string `json:"parameter_size"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Object != "list" || len(out.Data) != 1 {
		t.Fatalf("response = %+v", out)
	}
	if out.Data[0].ID != "llama3.2:latest" || out.Data[0].Object != "model" {
		t.Errorf("model = %+v", out.Data[0])
	}
	// The extra metadata is what the TUI's model picker displays.
	if out.Data[0].ParameterSize != "3.2B" {
		t.Errorf("parameter_size = %q, want 3.2B", out.Data[0].ParameterSize)
	}
}

func TestEmbeddings(t *testing.T) {
	base := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"embeddings":[[0.1,0.2],[0.3,0.4]]}`)
	})

	resp := do(t, http.MethodPost, base+"/v1/embeddings", testKey,
		`{"model":"embed","input":["a","b"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Object string `json:"object"`
		Data   []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Data) != 2 || len(out.Data[0].Embedding) != 2 {
		t.Fatalf("embeddings = %+v", out)
	}
	if out.Data[1].Index != 1 {
		t.Errorf("second embedding index = %d, want 1", out.Data[1].Index)
	}
}

func TestEmbeddingsAcceptsBareStringInput(t *testing.T) {
	base := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&got)
		if len(got.Input) != 1 || got.Input[0] != "solo" {
			t.Errorf("forwarded input = %v, want [solo]", got.Input)
		}
		fmt.Fprint(w, `{"embeddings":[[1]]}`)
	})

	resp := do(t, http.MethodPost, base+"/v1/embeddings", testKey, `{"model":"e","input":"solo"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestCORSPreflight(t *testing.T) {
	base := newGateway(t, streamingDaemon())

	req, _ := http.NewRequest(http.MethodOptions, base+"/v1/chat/completions", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("missing permissive CORS origin")
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Errorf("Authorization not allowed in CORS headers")
	}
}

func TestUnreachableDaemonReportsBadGateway(t *testing.T) {
	client, err := ollama.New("http://127.0.0.1:1") // nothing listens on port 1
	if err != nil {
		t.Fatalf("ollama.New: %v", err)
	}
	srv := New(Options{
		Ollama: client, APIKey: testKey,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	resp := do(t, http.MethodGet, gateway.URL+"/v1/models", testKey, "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	var body errorBody
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Error.Code != "ollama_unreachable" {
		t.Errorf("code = %q, want ollama_unreachable", body.Error.Code)
	}
}
