package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/justin06lee/alpaca/internal/ollama"
)

// maxRequestBytes is generous because chat requests carry base64 images for
// multimodal models.
const maxRequestBytes = 32 << 20

// ---------------------------------------------------------------------------
// Request types
// ---------------------------------------------------------------------------

type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`

	Temperature *float64 `json:"temperature"`
	TopP        *float64 `json:"top_p"`
	TopK        *int     `json:"top_k"`
	// OpenAI deprecated max_tokens in favour of max_completion_tokens; accept
	// both so old and new clients each work.
	MaxTokens           *int `json:"max_tokens"`
	MaxCompletionTokens *int `json:"max_completion_tokens"`

	Stop           stringList      `json:"stop"`
	Seed           *int            `json:"seed"`
	ResponseFormat *responseFormat `json:"response_format"`
	StreamOptions  *streamOptions  `json:"stream_options"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role    string  `json:"role"`
	Content content `json:"content"`
}

// content accepts both shapes OpenAI allows for a message body: a plain string,
// or an array of typed parts for multimodal input.
type content struct {
	Text   string
	Images []string
}

func (c *content) UnmarshalJSON(data []byte) error {
	// The common case: a bare string.
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		c.Text = text
		return nil
	}
	// An assistant message carrying only tool calls has null content.
	if string(data) == "null" {
		return nil
	}

	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(data, &parts); err != nil {
		return fmt.Errorf("message content must be a string or an array of content parts")
	}

	var sb strings.Builder
	for _, part := range parts {
		switch part.Type {
		case "text":
			sb.WriteString(part.Text)
		case "image_url":
			if part.ImageURL == nil {
				continue
			}
			// Ollama wants raw base64, so a data: URL has to be unwrapped.
			// Remote URLs are refused rather than fetched: making the gateway
			// fetch arbitrary URLs would turn it into an SSRF proxy into
			// whatever network it is running on.
			payload, ok := decodeDataURL(part.ImageURL.URL)
			if !ok {
				return fmt.Errorf("image_url must be a base64 data: URL; alpaca does not fetch remote images")
			}
			c.Images = append(c.Images, payload)
		}
	}
	c.Text = sb.String()
	return nil
}

// decodeDataURL pulls the base64 payload out of a data: URL.
func decodeDataURL(url string) (string, bool) {
	if !strings.HasPrefix(url, "data:") {
		return "", false
	}
	_, payload, found := strings.Cut(url, ",")
	if !found {
		return "", false
	}
	// Validate now so a malformed image fails here with a clear message rather
	// than deep inside the daemon.
	if _, err := base64.StdEncoding.DecodeString(payload); err != nil {
		return "", false
	}
	return payload, true
}

// stringList accepts OpenAI's "one or many" fields, which may be a bare string
// or an array of them.
type stringList []string

func (s *stringList) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []string{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("expected a string or an array of strings")
	}
	*s = many
	return nil
}

// toOllama translates the request, returning a client-facing error for anything
// invalid.
func (r *chatCompletionRequest) toOllama() (ollama.ChatRequest, error) {
	if strings.TrimSpace(r.Model) == "" {
		return ollama.ChatRequest{}, errors.New("`model` is required")
	}
	if len(r.Messages) == 0 {
		return ollama.ChatRequest{}, errors.New("`messages` must contain at least one message")
	}

	msgs := make([]ollama.Message, 0, len(r.Messages))
	for i, m := range r.Messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			return ollama.ChatRequest{}, fmt.Errorf("messages[%d] is missing `role`", i)
		}
		msgs = append(msgs, ollama.Message{Role: role, Content: m.Content.Text, Images: m.Content.Images})
	}

	opts := &ollama.Options{
		Temperature: r.Temperature,
		TopP:        r.TopP,
		TopK:        r.TopK,
		Seed:        r.Seed,
		Stop:        r.Stop,
	}
	if r.MaxCompletionTokens != nil {
		opts.NumPredict = r.MaxCompletionTokens
	} else if r.MaxTokens != nil {
		opts.NumPredict = r.MaxTokens
	}

	req := ollama.ChatRequest{Model: r.Model, Messages: msgs, Options: opts}
	if r.ResponseFormat != nil && r.ResponseFormat.Type == "json_object" {
		req.Format = "json"
	}
	return req, nil
}

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

type completionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   *usage             `json:"usage,omitempty"`
}

type completionChoice struct {
	Index        int         `json:"index"`
	Message      responseMsg `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chunkResponse struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usage        `json:"usage,omitempty"`
	// Alpaca carries out-of-band progress that has no place in the OpenAI
	// schema. Clients that do not know about it see a chunk with no choices
	// and skip it, which is exactly the desired behaviour.
	Alpaca *alpacaEvent `json:"alpaca,omitempty"`
}

// alpacaEvent reports something the gateway did on the client's behalf.
type alpacaEvent struct {
	Event  string `json:"event"`
	Detail string `json:"detail,omitempty"`
}

type chunkChoice struct {
	Index        int         `json:"index"`
	Delta        responseMsg `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type responseMsg struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func usageFrom(s ollama.Stats) *usage {
	return &usage{
		PromptTokens:     s.PromptTokens,
		CompletionTokens: s.EvalTokens,
		TotalTokens:      s.PromptTokens + s.EvalTokens,
	}
}

// finishReason maps Ollama's done_reason onto OpenAI's vocabulary.
func finishReason(reason string) string {
	switch reason {
	case "length":
		return "length"
	case "", "stop":
		return "stop"
	default:
		return reason
	}
}

func newCompletionID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		// An ID only has to be unique enough to correlate logs; a timestamp is
		// an acceptable fallback if the entropy source is unavailable.
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	return "chatcmpl-" + hex.EncodeToString(buf)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req chatCompletionRequest
	if !decodeBody(w, r, &req) {
		return
	}

	ollamaReq, err := req.toOllama()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}

	// The model can only search if the gateway was started with a provider.
	s.prepareTools(&ollamaReq, true)

	if req.Stream {
		s.streamChat(w, r, req, ollamaReq)
		return
	}
	s.bufferChat(w, r, ollamaReq)
}

// bufferChat collects the whole reply and returns it as one JSON document.
func (s *Server) bufferChat(w http.ResponseWriter, r *http.Request, req ollama.ChatRequest) {
	var body strings.Builder
	var final ollama.Chunk

	// Tool rounds run to completion before anything is returned; only the last
	// generation's text is the answer.
	for round := 0; ; round++ {
		lastRound := round >= s.maxRounds()
		if lastRound {
			// Withhold the tools so the model has no choice but to answer.
			req.Tools = nil
		}

		var calls []ollama.ToolCall
		body.Reset()

		err := s.opts.Ollama.Chat(r.Context(), req, func(ch ollama.Chunk) error {
			if len(ch.ToolCalls) > 0 {
				calls = append(calls, ch.ToolCalls...)
			}
			body.WriteString(ch.Content)
			if ch.Done {
				final = ch
			}
			return nil
		})
		if err != nil {
			s.writeUpstreamError(w, err)
			return
		}

		if len(calls) == 0 || lastRound {
			break
		}
		s.resolveToolCalls(r.Context(), &req, calls)
	}

	writeJSON(w, http.StatusOK, completionResponse{
		ID:      newCompletionID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []completionChoice{{
			Index:        0,
			Message:      responseMsg{Role: "assistant", Content: body.String()},
			FinishReason: finishReason(final.Reason),
		}},
		Usage: usageFrom(final.Stats),
	})
}

// streamChat relays the reply as server-sent events.
//
// Headers are withheld until the first token arrives. That matters: once a 200
// and the SSE content type are on the wire the status can never be corrected,
// so an immediate failure — an unknown model, a daemon that is down — would
// have to be reported as a 200 containing an error. Deferring the header lets
// those cases return a truthful HTTP status instead.
func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, req chatCompletionRequest, ollamaReq ollama.ChatRequest) {
	ctrl := http.NewResponseController(w)
	id := newCompletionID()
	created := time.Now().Unix()
	started := false
	var final ollama.Chunk

	emit := func(payload any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return err
		}
		// Without an explicit flush the reply arrives in one lump when the
		// buffer fills, which defeats the point of streaming.
		return ctrl.Flush()
	}

	begin := func() error {
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		// Tell nginx and friends not to buffer the stream.
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		started = true

		// OpenAI's first chunk announces the role and carries no content.
		return emit(chunkResponse{
			ID: id, Object: "chat.completion.chunk", Created: created, Model: ollamaReq.Model,
			Choices: []chunkChoice{{Index: 0, Delta: responseMsg{Role: "assistant"}}},
		})
	}

	// notify reports a tool round to the client. The frame carries no choices,
	// so an ordinary OpenAI client iterates an empty list and moves on, while
	// alpaca's own client can surface it as a status line.
	notify := func(event, detail string) {
		_ = emit(chunkResponse{
			ID: id, Object: "chat.completion.chunk", Created: created, Model: ollamaReq.Model,
			Choices: []chunkChoice{},
			Alpaca:  &alpacaEvent{Event: event, Detail: detail},
		})
	}

	var err error
	for round := 0; ; round++ {
		lastRound := round >= s.maxRounds()
		if lastRound {
			ollamaReq.Tools = nil
		}

		var calls []ollama.ToolCall
		producedContent := false

		err = s.opts.Ollama.Chat(r.Context(), ollamaReq, func(ch ollama.Chunk) error {
			// A tool call arrives complete in its own frame, and a turn that
			// calls a tool carries no prose. Collecting it without starting the
			// stream is what lets a search happen invisibly before the first
			// token of the real answer.
			if len(ch.ToolCalls) > 0 && !producedContent && !lastRound {
				calls = append(calls, ch.ToolCalls...)
				return nil
			}
			if ch.Done {
				final = ch
				return nil
			}
			if ch.Content == "" {
				return nil
			}
			producedContent = true
			if !started {
				if err := begin(); err != nil {
					return err
				}
			}
			return emit(chunkResponse{
				ID: id, Object: "chat.completion.chunk", Created: created, Model: ollamaReq.Model,
				Choices: []chunkChoice{{Index: 0, Delta: responseMsg{Content: ch.Content}}},
			})
		})
		if err != nil {
			break
		}

		if len(calls) == 0 || producedContent || lastRound {
			break
		}

		// Committing to the stream here is deliberate: the request is clearly
		// valid and the model is working, so the status is worth more than
		// keeping the option of a non-200 status open.
		if !started {
			if err = begin(); err != nil {
				break
			}
		}
		for _, round := range s.resolveToolCalls(r.Context(), &ollamaReq, calls) {
			switch {
			case round.Err != nil:
				notify("search_failed", round.Err.Error())
			default:
				notify("search", fmt.Sprintf("%s — %d results", round.Query, round.Results))
			}
		}
	}

	if err != nil {
		if !started {
			// Nothing has been committed yet, so a real status code is possible.
			s.writeUpstreamError(w, err)
			return
		}
		// Mid-stream failure. The client already has a 200, so the only honest
		// signal left is an error event before closing the stream.
		s.log.Warn("chat stream failed after it began", "error", err)
		_ = emit(errorBody{Error: errorDetail{
			Message: err.Error(), Type: "api_error", Code: "stream_interrupted",
		}})
		fmt.Fprint(w, "data: [DONE]\n\n")
		_ = ctrl.Flush()
		return
	}

	if !started {
		// A reply with no tokens at all still needs a well-formed stream.
		if err := begin(); err != nil {
			return
		}
	}

	reason := finishReason(final.Reason)
	stop := chunkResponse{
		ID: id, Object: "chat.completion.chunk", Created: created, Model: ollamaReq.Model,
		Choices: []chunkChoice{{Index: 0, Delta: responseMsg{}, FinishReason: &reason}},
	}
	// OpenAI only reports usage on a stream when the client opts in.
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		stop.Usage = usageFrom(final.Stats)
	}
	_ = emit(stop)

	fmt.Fprint(w, "data: [DONE]\n\n")
	_ = ctrl.Flush()
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.opts.Ollama.Models(r.Context())
	if err != nil {
		s.writeUpstreamError(w, err)
		return
	}

	type modelObject struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
		// Beyond the OpenAI fields, pass through what the picker wants to show.
		Size          int64  `json:"size,omitempty"`
		ParameterSize string `json:"parameter_size,omitempty"`
		Quantization  string `json:"quantization,omitempty"`
		Family        string `json:"family,omitempty"`
	}

	data := make([]modelObject, 0, len(models))
	for _, m := range models {
		data = append(data, modelObject{
			ID:            m.Name,
			Object:        "model",
			Created:       m.ModifiedAt.Unix(),
			OwnedBy:       "ollama",
			Size:          m.Size,
			ParameterSize: m.Details.ParameterSize,
			Quantization:  m.Details.Quantization,
			Family:        m.Details.Family,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string     `json:"model"`
		Input stringList `json:"input"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeError(w, http.StatusBadRequest, "`model` is required", "invalid_request")
		return
	}
	if len(req.Input) == 0 {
		writeError(w, http.StatusBadRequest, "`input` is required", "invalid_request")
		return
	}

	vectors, err := s.opts.Ollama.Embed(r.Context(), req.Model, req.Input)
	if err != nil {
		s.writeUpstreamError(w, err)
		return
	}

	type embeddingObject struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	}
	data := make([]embeddingObject, 0, len(vectors))
	for i, v := range vectors {
		data = append(data, embeddingObject{Object: "embedding", Index: i, Embedding: v})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"model":  req.Model,
		"data":   data,
	})
}

// decodeBody reads and validates a JSON request body, reporting failures to the
// client. It returns false when the caller should stop.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	dec := json.NewDecoder(r.Body)

	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("request body exceeds %d MB", maxRequestBytes>>20), "payload_too_large")
			return false
		}
		writeError(w, http.StatusBadRequest, "could not parse request body: "+err.Error(), "invalid_json")
		return false
	}
	return true
}

// writeUpstreamError translates an Ollama failure into a client-facing one,
// preserving a 404 for an unknown model so clients can react to it specifically.
func (s *Server) writeUpstreamError(w http.ResponseWriter, err error) {
	var apiErr *ollama.APIError
	if errors.As(err, &apiErr) {
		status := apiErr.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		writeError(w, status, apiErr.Message, "ollama_error")
		return
	}
	s.log.Error("upstream ollama call failed", "error", err)
	writeError(w, http.StatusBadGateway,
		"could not reach the ollama daemon: "+err.Error(), "ollama_unreachable")
}
