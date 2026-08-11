package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message is one turn of a conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Roles used by the chat API.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// ChatRequest asks for a completion.
type ChatRequest struct {
	Model       string
	Messages    []Message
	Temperature *float64
	MaxTokens   *int
}

// Usage reports token counts for a completed reply.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Chunk is one increment of a streamed reply.
type Chunk struct {
	Content      string
	Done         bool
	FinishReason string
	Usage        *Usage
	// Event and Detail report work the gateway did on our behalf mid-turn,
	// such as a web search. They ride in a field standard OpenAI clients
	// ignore, so they cost nothing for anyone who does not want them.
	Event  string
	Detail string
}

// SearchResult is one hit from the server's search provider.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Model is a model available on the server.
type Model struct {
	ID            string `json:"id"`
	Size          int64  `json:"size"`
	ParameterSize string `json:"parameter_size"`
	Quantization  string `json:"quantization"`
	Family        string `json:"family"`
}

// Info describes the connected server.
type Info struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Models  int    `json:"models"`
	Ollama  struct {
		Version string `json:"version"`
		URL     string `json:"url"`
		Error   string `json:"error"`
	} `json:"ollama"`
}

// Chat streams a completion, calling onChunk for each increment.
func (c *Client) Chat(ctx context.Context, req ChatRequest, onChunk func(Chunk) error) error {
	payload := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
		// Ask for token counts so the TUI can show throughput; OpenAI omits
		// usage from streams unless this is set.
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		payload["max_tokens"] = *req.MaxTokens
	}

	resp, err := c.post(ctx, "/v1/chat/completions", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return parseSSE(ctx, resp.Body, onChunk)
}

// parseSSE reads an OpenAI-style event stream.
func parseSSE(ctx context.Context, body io.Reader, onChunk func(Chunk) error) error {
	scan := bufio.NewScanner(body)
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var sawTerminator bool
	for scan.Scan() {
		line := bytes.TrimSpace(scan.Bytes())
		if len(line) == 0 {
			continue // blank line separates events
		}
		payload, found := bytes.CutPrefix(line, []byte("data: "))
		if !found {
			continue // comments and other SSE fields are not used here
		}
		if string(payload) == "[DONE]" {
			sawTerminator = true
			break
		}

		// The gateway reports a failure that happened after the stream began
		// as an error frame; surface it instead of treating it as content.
		var maybeErr struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &maybeErr) == nil && maybeErr.Error != nil {
			return fmt.Errorf("server: %s", maybeErr.Error.Message)
		}

		var frame struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage  *Usage `json:"usage"`
			Alpaca *struct {
				Event  string `json:"event"`
				Detail string `json:"detail"`
			} `json:"alpaca"`
		}
		if err := json.Unmarshal(payload, &frame); err != nil {
			return fmt.Errorf("unreadable stream frame: %w", err)
		}

		chunk := Chunk{Usage: frame.Usage}
		if frame.Alpaca != nil {
			chunk.Event, chunk.Detail = frame.Alpaca.Event, frame.Alpaca.Detail
		}
		if len(frame.Choices) > 0 {
			chunk.Content = frame.Choices[0].Delta.Content
			if frame.Choices[0].FinishReason != nil {
				chunk.FinishReason = *frame.Choices[0].FinishReason
			}
		}
		if chunk.Content == "" && chunk.FinishReason == "" && chunk.Usage == nil && chunk.Event == "" {
			continue // role-announcement frame, nothing to report
		}
		if err := onChunk(chunk); err != nil {
			return err
		}
	}

	if err := scan.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("read stream: %w", err)
	}
	if !sawTerminator {
		// The connection dropped mid-reply. Saying so beats silently
		// presenting a truncated answer as complete.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("connection closed before the reply finished")
	}

	return onChunk(Chunk{Done: true})
}

// Models lists what the server can run.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	resp, err := c.get(ctx, "/v1/models")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	return out.Data, nil
}

// Info fetches server details.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	resp, err := c.get(ctx, "/api/info")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var info Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode server info: %w", err)
	}
	return &info, nil
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.profile.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", c.route.Endpoint, err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))

		var body struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(snippet, &body) == nil && body.Error.Message != "" {
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, fmt.Errorf("the server rejected this API key — re-link with a fresh "+
					"connect string from `alpaca serve` (%s)", body.Error.Message)
			}
			return nil, fmt.Errorf("%s", body.Error.Message)
		}
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode,
			strings.TrimSpace(string(snippet)))
	}
	return resp, nil
}

// Search runs a query against the server's search provider directly, rather
// than waiting for the model to decide to call the tool.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	resp, err := c.post(ctx, "/api/search", map[string]any{"query": query, "limit": limit})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Results []SearchResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode search results: %w", err)
	}
	return out.Results, nil
}
