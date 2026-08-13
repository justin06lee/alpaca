// Package server is the gateway that turns a local Ollama daemon into a
// networked, authenticated, OpenAI-compatible API.
//
// The OpenAI shape is deliberate: it means the self-hosted model is reachable
// not just from alpaca's own TUI but from anything already speaking that
// protocol — existing SDKs, editor plugins, scripts — by changing a base URL.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/justin06lee/alpaca/internal/ollama"
	"github.com/justin06lee/alpaca/internal/search"
)

// Options configures a gateway.
type Options struct {
	Ollama *ollama.Client
	// APIKey is the bearer token clients must present.
	APIKey string
	// ID and Name identify this server to clients during discovery.
	ID   string
	Name string
	// Version is the alpaca build version, reported in /api/info.
	Version string
	Logger  *slog.Logger

	// Search, when set, lets the model look things up on the web. The gateway
	// runs the lookups itself so every client gets the capability without
	// configuring anything.
	Search search.Provider
	// SearchResults caps hits per query; SearchRounds caps tool passes per turn.
	SearchResults int
	SearchRounds  int
}

// Server is the gateway.
type Server struct {
	opts Options
	log  *slog.Logger
}

// New builds a gateway. A nil Ollama client is a programming error and fails
// here, loudly, rather than as a panic on the first request that dereferences
// it.
func New(opts Options) *Server {
	if opts.Ollama == nil {
		panic("server.New: Options.Ollama is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Server{opts: opts, log: opts.Logger}
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated. The client's connection race needs a cheap way to ask
	// "are you there, and are you the server I linked to?" before it can prove
	// who it is. The reply carries only the server's id and name — both of
	// which mDNS already shouts across the LAN, and neither of which grants
	// any access on its own.
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// Everything below requires the API key.
	mux.Handle("GET /api/info", s.authed(s.handleInfo))
	mux.Handle("GET /v1/models", s.authed(s.handleListModels))
	mux.Handle("POST /v1/chat/completions", s.authed(s.handleChatCompletions))
	mux.Handle("POST /v1/embeddings", s.authed(s.handleEmbeddings))
	// Native endpoint, so a client can run a search directly instead of hoping
	// the model decides to call the tool.
	mux.Handle("POST /api/search", s.authed(s.handleSearch))

	return s.recoverPanics(s.logRequests(withCORS(mux)))
}

// NewHTTPServer wraps the handler with timeouts suited to streaming.
func (s *Server) NewHTTPServer() *http.Server {
	return &http.Server{
		Handler: s.Handler(),
		// Bounds how long a client may dawdle over its request headers, which
		// is the cheap slowloris defence.
		ReadHeaderTimeout: 20 * time.Second,
		// Deliberately no WriteTimeout: it is an absolute deadline on the whole
		// response, and a long generation legitimately streams for minutes.
		// IdleTimeout reaps genuinely dead keep-alive connections instead.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
		ErrorLog:       slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"id":      s.opts.ID,
		"name":    s.opts.Name,
		"service": "alpaca",
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]any{
		"id":      s.opts.ID,
		"name":    s.opts.Name,
		"version": s.opts.Version,
		"ollama":  map[string]any{"url": s.opts.Ollama.BaseURL()},
	}

	if version, err := s.opts.Ollama.Version(r.Context()); err == nil {
		info["ollama"].(map[string]any)["version"] = version
	} else {
		// Report the daemon being down rather than failing the whole request:
		// a client asking for info while ollama restarts still wants the rest.
		info["ollama"].(map[string]any)["error"] = err.Error()
	}
	if models, err := s.opts.Ollama.Models(r.Context()); err == nil {
		info["models"] = len(models)
	}
	if s.searchEnabled() {
		info["search"] = map[string]any{"enabled": true, "provider": s.opts.Search.Name()}
	} else {
		info["search"] = map[string]any{"enabled": false}
	}

	writeJSON(w, http.StatusOK, info)
}

// logRequests records completed requests at debug level, and slow or failed
// ones more loudly.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// recoverPanics sits outside this middleware and already wrapped the
		// writer; wrapping again would hide the written flag it relies on.
		rec, ok := w.(*statusRecorder)
		if !ok {
			rec = &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		}
		next.ServeHTTP(rec, r)

		level := slog.LevelDebug
		if rec.status >= 500 {
			level = slog.LevelError
		} else if rec.status >= 400 {
			level = slog.LevelWarn
		}
		s.log.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"remote", clientIP(r),
			"duration", time.Since(start).Round(time.Millisecond),
		)
	})
}

// recoverPanics keeps one bad request from taking down a server that is meant
// to run unattended for weeks.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				// http.ErrAbortHandler is the documented way to abort a
				// response; it is not a bug and must not be logged as one.
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic serving request",
					"path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				// Only report the error if the response has not begun; once
				// bytes are on the wire a second WriteHeader cannot correct
				// anything, it can only corrupt the stream.
				if !rec.wrote {
					writeError(rec, http.StatusInternalServerError, "internal error", "internal_error")
				}
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

// withCORS lets browser-based clients reach the gateway.
//
// A wildcard origin is safe here because authentication is a bearer token the
// page must supply explicitly, never an ambient cookie — and the spec forbids
// pairing a wildcard origin with credentialed requests, so no browser will
// attach ambient credentials to these.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Api-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	// wrote flips once anything has gone to the client, at which point the
	// status can no longer be changed.
	wrote bool
}

func (r *statusRecorder) WriteHeader(code int) {
	r.wrote = true
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	// A Write with no prior WriteHeader commits an implicit 200.
	r.wrote = true
	return r.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the underlying writer for
// flushing, which SSE streaming depends on.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
