// Package ollama is a thin client for a local Ollama daemon.
//
// It covers only what the gateway needs — streaming chat, model listing,
// embeddings, health — and deliberately surfaces Ollama's token counts and
// timings, because those are what makes the TUI's status bar useful.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultHost is where Ollama listens unless told otherwise.
const DefaultHost = "http://127.0.0.1:11434"

// Client talks to one Ollama daemon.
type Client struct {
	base *url.URL
	http *http.Client
}

// New builds a client for the given base URL. A bare host:port is accepted and
// assumed to be http.
func New(base string) (*Client, error) {
	if base == "" {
		base = DefaultHost
	}
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse ollama url %q: %w", base, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("ollama url %q has no host", base)
	}

	return &Client{
		base: u,
		// Deliberately no Client.Timeout: a long generation is a long-lived
		// response body, and a blanket timeout would sever it mid-stream.
		// Cancellation is the caller's ctx; the per-phase timeouts below still
		// protect against a daemon that accepts a connection and then stalls.
		http: &http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				ResponseHeaderTimeout: 60 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				MaxIdleConnsPerHost:   4,
			},
		},
	}, nil
}

// BaseURL reports the daemon address, for diagnostics.
func (c *Client) BaseURL() string { return c.base.String() }

func (c *Client) url(path string) string {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String()
}

// Message is one turn of a conversation. Images carries base64-encoded image
// data for multimodal models.
type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
	// ToolCalls is set on an assistant message asking for a tool to run.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolName identifies which tool a role:"tool" message is answering.
	ToolName string `json:"tool_name,omitempty"`
}

// Tool describes a function the model may call.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is a tool's name and JSON Schema parameters.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// ToolCall is the model asking for a tool to run.
type ToolCall struct {
	// ID is absent on older daemons, so callers must tolerate an empty value.
	ID       string           `json:"id,omitempty"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction names the tool and carries its arguments.
type ToolCallFunction struct {
	Name string `json:"name"`
	// Arguments stays raw because Ollama emits a JSON object here while the
	// OpenAI wire format uses a JSON-encoded string. Keeping it undecoded lets
	// each side render the shape it expects without a lossy round trip.
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// StringArg pulls one string field out of the arguments object.
func (f ToolCallFunction) StringArg(name string) (string, bool) {
	raw := f.Arguments
	// Tolerate a JSON-encoded object arriving as a string.
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		raw = json.RawMessage(asString)
	}

	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil {
		return "", false
	}
	value, ok := fields[name].(string)
	return value, ok
}

// Options are Ollama's sampling knobs. Only non-nil fields are sent, so the
// model's own defaults apply to anything the user did not set.
type Options struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
	NumCtx      *int     `json:"num_ctx,omitempty"`
	Seed        *int     `json:"seed,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

// ChatRequest is a chat completion request.
type ChatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream"`
	Options   *Options  `json:"options,omitempty"`
	KeepAlive string    `json:"keep_alive,omitempty"`
	Format    string    `json:"format,omitempty"`
	// Tools the model may call. Only models advertising the "tools" capability
	// act on these; others ignore them.
	Tools []Tool `json:"tools,omitempty"`
}

// Stats are the counters Ollama reports when a generation finishes.
type Stats struct {
	PromptTokens  int
	EvalTokens    int
	TotalDuration time.Duration
	LoadDuration  time.Duration
	EvalDuration  time.Duration
}

// TokensPerSecond reports decode throughput, or 0 when unmeasurable.
func (s Stats) TokensPerSecond() float64 {
	if s.EvalDuration <= 0 || s.EvalTokens <= 0 {
		return 0
	}
	return float64(s.EvalTokens) / s.EvalDuration.Seconds()
}

// Chunk is one increment of a streamed reply.
type Chunk struct {
	Content string
	Done    bool
	Reason  string
	Stats   Stats
	// ToolCalls is populated when the model asks to run a tool. Ollama emits
	// the whole call in one frame rather than streaming it in fragments the
	// way the OpenAI wire format does, so this arrives complete.
	ToolCalls []ToolCall
}

// chatFrame mirrors one NDJSON line of Ollama's /api/chat stream.
type chatFrame struct {
	Model     string  `json:"model"`
	Message   Message `json:"message"`
	Done      bool    `json:"done"`
	Reason    string  `json:"done_reason"`
	Error     string  `json:"error"`
	PromptCnt int     `json:"prompt_eval_count"`
	EvalCnt   int     `json:"eval_count"`
	TotalNs   int64   `json:"total_duration"`
	LoadNs    int64   `json:"load_duration"`
	EvalNs    int64   `json:"eval_duration"`
}

func (f chatFrame) chunk() Chunk {
	return Chunk{
		Content:   f.Message.Content,
		Done:      f.Done,
		Reason:    f.Reason,
		ToolCalls: f.Message.ToolCalls,
		Stats: Stats{
			PromptTokens:  f.PromptCnt,
			EvalTokens:    f.EvalCnt,
			TotalDuration: time.Duration(f.TotalNs),
			LoadDuration:  time.Duration(f.LoadNs),
			EvalDuration:  time.Duration(f.EvalNs),
		},
	}
}

// Chat streams a completion, invoking onChunk for every increment. Returning an
// error from onChunk aborts the stream and is propagated to the caller.
func (c *Client) Chat(ctx context.Context, req ChatRequest, onChunk func(Chunk) error) error {
	req.Stream = true
	resp, err := c.post(ctx, "/api/chat", req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scan := bufio.NewScanner(resp.Body)
	// A single frame is small, but a model emitting one enormous token or a
	// daemon batching frames should not blow up the scanner.
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scan.Scan() {
		line := bytes.TrimSpace(scan.Bytes())
		if len(line) == 0 {
			continue
		}
		var frame chatFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			return fmt.Errorf("ollama sent an unreadable stream frame: %w", err)
		}
		if frame.Error != "" {
			return fmt.Errorf("ollama: %s", frame.Error)
		}
		if err := onChunk(frame.chunk()); err != nil {
			return err
		}
		if frame.Done {
			return nil
		}
	}
	if err := scan.Err(); err != nil {
		// A cancelled context surfaces here as a read error; report the cause.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("read ollama stream: %w", err)
	}
	// Stream ended without a done frame — the daemon died or was restarted.
	return fmt.Errorf("ollama closed the stream before finishing the reply")
}

// Model describes one locally installed model.
type Model struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	Details    struct {
		Family        string `json:"family"`
		ParameterSize string `json:"parameter_size"`
		Quantization  string `json:"quantization_level"`
	} `json:"details"`
}

// Models lists the models installed on the daemon.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	resp, err := c.get(ctx, "/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload struct {
		Models []Model `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	return payload.Models, nil
}

// Embed produces embeddings for one or more inputs.
func (c *Client) Embed(ctx context.Context, model string, input []string) ([][]float64, error) {
	resp, err := c.post(ctx, "/api/embed", map[string]any{
		"model": model,
		"input": input,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode embeddings: %w", err)
	}
	return payload.Embeddings, nil
}

// Version returns the daemon version, doubling as a health check.
func (c *Client) Version(ctx context.Context) (string, error) {
	resp, err := c.get(ctx, "/api/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode version: %w", err)
	}
	return payload.Version, nil
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, err
	}
	return c.do(req, path)
}

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, path)
}

func (c *Client) do(req *http.Request, path string) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach ollama at %s: %w", c.base.Host, err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		// Ollama reports failures as {"error": "..."}; fall back to raw text.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		var payload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(snippet, &payload) == nil && payload.Error != "" {
			return nil, &APIError{Status: resp.StatusCode, Message: payload.Error, Path: path}
		}
		return nil, &APIError{
			Status:  resp.StatusCode,
			Message: strings.TrimSpace(string(snippet)),
			Path:    path,
		}
	}
	return resp, nil
}

// APIError is a non-2xx response from the daemon.
type APIError struct {
	Status  int
	Message string
	Path    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("ollama %s returned %d", e.Path, e.Status)
	}
	return fmt.Sprintf("ollama %s: %s", e.Path, e.Message)
}

// NotFound reports whether the error is a 404, which for /api/chat means the
// model is not installed.
func (e *APIError) NotFound() bool { return e.Status == http.StatusNotFound }
