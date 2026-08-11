package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/justin06lee/alpaca/internal/ollama"
	"github.com/justin06lee/alpaca/internal/search"
)

// searchToolName is the function the model calls to look something up.
const searchToolName = "web_search"

// searchTimeout bounds a single lookup. A metasearch instance fanning out to
// several engines is the slow part, and a hung search must not hold a chat
// turn open indefinitely.
const searchTimeout = 25 * time.Second

// searchEnabled reports whether the gateway can run web searches.
func (s *Server) searchEnabled() bool { return s.opts.Search != nil }

// searchTool is the declaration handed to the model.
//
// A caveat measured rather than assumed: on llama3.2:3b the decision to call
// this tool is close to a coin flip for anything borderline. Running the same
// four prompts twice against two quite different descriptions — one broad, one
// with an explicit list of things not to search for — scored identically, and
// individual prompts flipped between repeats of the same description. Sampling
// noise dominates, so the wording below is written to read clearly rather than
// tuned against a benchmark it cannot actually move.
//
// The practical consequence is that automatic search is a convenience, not a
// guarantee. The /search command and POST /api/search exist because they always
// run, and larger tool-tuned models make far better use of this declaration.
func searchTool() ollama.Tool {
	return ollama.Tool{
		Type: "function",
		Function: ollama.ToolFunction{
			Name: searchToolName,
			Description: "Look something up on the public web. " +
				"Use it when the question concerns recent events or current data, when it names a " +
				"specific product, library, version, person, or organisation whose details you are " +
				"not certain of, or when the user asks you to search. " +
				"Do not use it for arithmetic, definitions, translation, writing or explaining code, " +
				"or general knowledge you already have \u2014 answer those directly.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "The search query, phrased as you would type it into a search engine.",
					},
				},
				"required": []string{"query"},
			},
		},
	}
}

// toolRound is what happened during one pass of the tool loop.
type toolRound struct {
	Query   string
	Results int
	Err     error
}

// runSearchCall executes one tool call and returns the text fed back to the
// model, along with a summary for the client's status line.
func (s *Server) runSearchCall(ctx context.Context, call ollama.ToolCall) (string, toolRound) {
	if call.Function.Name != searchToolName {
		// The model invented a tool. Say so plainly rather than failing the
		// turn: it usually recovers and answers directly.
		return fmt.Sprintf("There is no tool named %q. Answer using what you already know.",
			call.Function.Name), toolRound{Err: fmt.Errorf("unknown tool %q", call.Function.Name)}
	}

	query, ok := call.Function.StringArg("query")
	query = strings.TrimSpace(query)
	if !ok || query == "" {
		return "The web_search tool needs a non-empty `query` string.",
			toolRound{Err: fmt.Errorf("tool call had no query")}
	}

	limit := s.opts.SearchResults
	if limit <= 0 {
		limit = defaultSearchResults
	}

	searchCtx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	results, err := s.opts.Search.Search(searchCtx, query, limit)
	if err != nil {
		s.log.Warn("web search failed", "query", query, "error", err)
		// The model gets a plain-language failure so it can fall back to its
		// own knowledge instead of stalling.
		return fmt.Sprintf("The web search for %q failed: %v. Answer from what you know, "+
				"and say that you could not check the web.", query, err),
			toolRound{Query: query, Err: err}
	}

	s.log.Debug("web search", "query", query, "results", len(results))
	return search.Format(query, results), toolRound{Query: query, Results: len(results)}
}

// resolveToolCalls runs every call the model made and appends the resulting
// conversation turns, so the next generation sees the answers.
func (s *Server) resolveToolCalls(ctx context.Context, req *ollama.ChatRequest, calls []ollama.ToolCall) []toolRound {
	// The assistant turn that asked for the tools has to be replayed, or the
	// tool replies below have nothing to attach to.
	req.Messages = append(req.Messages, ollama.Message{
		Role:      "assistant",
		ToolCalls: calls,
	})

	rounds := make([]toolRound, 0, len(calls))
	for _, call := range calls {
		content, round := s.runSearchCall(ctx, call)
		req.Messages = append(req.Messages, ollama.Message{
			Role:     "tool",
			Content:  content,
			ToolName: call.Function.Name,
		})
		rounds = append(rounds, round)
	}
	return rounds
}

// prepareTools attaches the search tool when search is on and the caller did
// not disable it for this request.
func (s *Server) prepareTools(req *ollama.ChatRequest, wantSearch bool) {
	if s.searchEnabled() && wantSearch {
		req.Tools = append(req.Tools, searchTool())
	}
}

// maxRounds is how many tool passes are allowed before the model is made to
// answer. Without a cap a model that keeps searching never produces a reply.
func (s *Server) maxRounds() int {
	if s.opts.SearchRounds > 0 {
		return s.opts.SearchRounds
	}
	return defaultSearchRounds
}

const (
	defaultSearchResults = 5
	defaultSearchRounds  = 3
)

// handleSearch exposes the search provider directly.
//
// The TUI's /search command uses this so a lookup is deterministic, rather
// than depending on a small model choosing to call the tool.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if !s.searchEnabled() {
		writeError(w, http.StatusNotImplemented,
			"web search is not enabled on this server — start it with `alpaca serve --search searxng --search-url ...`",
			"search_disabled")
		return
	}

	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeError(w, http.StatusBadRequest, "`query` is required", "invalid_request")
		return
	}
	if req.Limit <= 0 {
		req.Limit = s.opts.SearchResults
	}

	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()

	results, err := s.opts.Search.Search(ctx, req.Query, req.Limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), "search_failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"query":    req.Query,
		"provider": s.opts.Search.Name(),
		"results":  results,
	})
}
