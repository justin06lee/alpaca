// Package webui is the desktop face of alpaca: a self-contained local web
// application served from the binary itself.
//
// The page is embedded, the server binds loopback only, and every /api route
// demands a bearer token minted at startup and injected into the page — so
// the surface is exactly one browser tab, not the machine's network. The GUI
// shares the TUI's session store: a chat started in the terminal continues in
// the window, and vice versa.
package webui

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/session"
)

//go:embed index.html
var indexHTML string

// Connector opens the connection to an alpaca server, mirroring the TUI: the
// window comes up immediately and reports progress while routes race.
type Connector func(context.Context) (*client.Client, error)

// Connected wraps an already-open client, for demo mode and tests.
func Connected(c *client.Client) Connector {
	return func(context.Context) (*client.Client, error) { return c, nil }
}

// Server is one running GUI instance.
type Server struct {
	store       *session.Store
	profileName string
	version     string
	token       string

	mu      sync.Mutex
	client  *client.Client
	connErr error
	models  []client.Model

	// activity feeds the idle watchdog: the page heartbeats while open, and
	// streams in flight always count as activity.
	lastSeen time.Time
	inFlight int

	connect Connector
}

// New builds a GUI server. The connector runs in the background immediately.
func New(connect Connector, store *session.Store, profileName, version string) (*Server, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("mint session token: %w", err)
	}
	s := &Server{
		store:       store,
		profileName: profileName,
		version:     version,
		token:       hex.EncodeToString(raw),
		lastSeen:    time.Now(),
		connect:     connect,
	}
	go s.dial()
	return s, nil
}

// dial races routes off the request path; /api/boot reports how it went.
func (s *Server) dial() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := s.connect(ctx)

	s.mu.Lock()
	s.client, s.connErr = c, err
	s.mu.Unlock()
}

// Token is the bearer token guarding the API, exposed for tests.
func (s *Server) Token() string { return s.token }

// IdleSince reports how long the page has been silent, and whether a reply is
// still streaming. The caller owns the decision to exit.
func (s *Server) IdleSince() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastSeen), s.inFlight > 0
}

func (s *Server) touch() {
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

// Handler is the whole HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /api/boot", s.guard(s.handleBoot))
	mux.HandleFunc("POST /api/retry", s.guard(s.handleRetry))
	mux.HandleFunc("POST /api/ping", s.guard(s.handlePing))
	mux.HandleFunc("GET /api/sessions/{id}", s.guard(s.handleSession))
	mux.HandleFunc("DELETE /api/sessions/{id}", s.guard(s.handleDeleteSession))
	mux.HandleFunc("POST /api/chat", s.guard(s.handleChat))
	return mux
}

// guard enforces the bearer token and pins the Host to loopback, which is
// what keeps a hostile web page (or a DNS-rebound name) from driving the API.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "wrong host", http.StatusForbidden)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "missing or wrong token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	page := strings.ReplaceAll(indexHTML, "__ALPACA_TOKEN__", s.token)
	page = strings.ReplaceAll(page, "__ALPACA_SERVER__", s.profileName)
	page = strings.ReplaceAll(page, "__ALPACA_VERSION__", s.version)
	fmt.Fprint(w, page)
}

// bootReply is everything the page needs to draw itself.
type bootReply struct {
	Status  string         `json:"status"` // connecting | ready | error
	Error   string         `json:"error,omitempty"`
	Server  string         `json:"server"`
	Route   string         `json:"route,omitempty"`
	Model   string         `json:"model,omitempty"`
	Models  []client.Model `json:"models,omitempty"`
	Chats   []sessionCard  `json:"chats"`
	Version string         `json:"version"`
}

// sessionCard is one row of the saved-chat list.
type sessionCard struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Model    string    `json:"model"`
	Messages int       `json:"messages"`
	Updated  time.Time `json:"updated"`
}

func (s *Server) handleBoot(w http.ResponseWriter, r *http.Request) {
	s.touch()

	s.mu.Lock()
	c, connErr := s.client, s.connErr
	models := s.models
	s.mu.Unlock()

	reply := bootReply{Server: s.profileName, Version: s.version, Chats: s.listChats()}
	switch {
	case connErr != nil:
		reply.Status, reply.Error = "error", connErr.Error()
	case c == nil:
		reply.Status = "connecting"
	default:
		reply.Status = "ready"
		reply.Route = c.Route().Describe()
		reply.Model = c.Profile().Model
		if models == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			fetched, err := c.Models(ctx)
			cancel()
			if err == nil {
				s.mu.Lock()
				s.models = fetched
				s.mu.Unlock()
				models = fetched
			}
		}
		reply.Models = models
		if reply.Model == "" && len(models) > 0 {
			reply.Model = models[0].ID
		}
	}
	writeJSON(w, reply)
}

// handleRetry redials after a failed connect, so the window can recover
// without being relaunched.
func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	s.touch()
	s.mu.Lock()
	if s.client == nil {
		s.connErr = nil
		go s.dial()
	}
	s.mu.Unlock()
	writeJSON(w, map[string]string{"status": "connecting"})
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	s.touch()
	writeJSON(w, map[string]string{"ok": "1"})
}

// listChats is the saved conversations for this server, newest first.
func (s *Server) listChats() []sessionCard {
	out := []sessionCard{}
	sessions, err := s.store.List()
	if err != nil {
		return out
	}
	for _, sess := range sessions {
		if sess.Server != s.profileName {
			continue
		}
		title := sess.Title
		if title == "" {
			title = "(untitled)"
		}
		out = append(out, sessionCard{
			ID: sess.ID, Title: title, Model: sess.Model,
			Messages: len(sess.Messages), Updated: sess.Updated,
		})
	}
	return out
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	s.touch()
	sess, err := s.store.Load(r.PathValue("id"))
	if err != nil {
		http.Error(w, "no such chat", http.StatusNotFound)
		return
	}
	writeJSON(w, sess)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	s.touch()
	if err := s.store.Delete(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"ok": "1"})
}

// chatRequest is one send from the composer.
type chatRequest struct {
	SessionID string `json:"session_id"` // empty starts a new conversation
	Model     string `json:"model"`
	Text      string `json:"text"`
}

// chatEvent is one line of the reply stream (newline-delimited JSON).
type chatEvent struct {
	Type    string `json:"type"` // session | delta | note | usage | done | error
	ID      string `json:"id,omitempty"`
	Title   string `json:"title,omitempty"`
	Content string `json:"content,omitempty"`
	Text    string `json:"text,omitempty"`
	Prompt  int    `json:"prompt,omitempty"`
	Reply   int    `json:"reply,omitempty"`
	Error   string `json:"error,omitempty"`
}

// handleChat appends the prompt, streams the reply as NDJSON events, and
// persists the finished turn — the same lifecycle as the TUI, including
// keeping a partial reply when the stream dies or the tab aborts it.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	s.touch()

	s.mu.Lock()
	c := s.client
	s.inFlight++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.lastSeen = time.Now()
		s.mu.Unlock()
	}()

	if c == nil {
		http.Error(w, "not connected yet", http.StatusServiceUnavailable)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "unreadable request", http.StatusBadRequest)
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || req.Model == "" {
		http.Error(w, "text and model are required", http.StatusBadRequest)
		return
	}

	var sess *session.Session
	if req.SessionID == "" {
		sess = session.New(req.Model, s.profileName)
	} else {
		loaded, err := s.store.Load(req.SessionID)
		if err != nil {
			http.Error(w, "no such chat", http.StatusNotFound)
			return
		}
		sess = loaded
	}
	sess.Model = req.Model
	sess.Append(client.Message{Role: client.RoleUser, Content: req.Text})

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	send := func(ev chatEvent) {
		line, err := json.Marshal(ev)
		if err != nil {
			return
		}
		w.Write(line)
		w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}

	send(chatEvent{Type: "session", ID: sess.ID, Title: sess.Title})

	// The request context is the cancellation path: the page's stop button
	// aborts the fetch, the context dies, and the stream stops mid-token.
	var reply strings.Builder
	streamErr := c.Chat(r.Context(), client.ChatRequest{Model: sess.Model, Messages: sess.Wire()},
		func(ch client.Chunk) error {
			if ch.Content != "" {
				reply.WriteString(ch.Content)
				send(chatEvent{Type: "delta", Content: ch.Content})
			}
			if ch.Event != "" {
				send(chatEvent{Type: "note", Text: describeEvent(ch)})
			}
			if ch.Usage != nil {
				send(chatEvent{Type: "usage", Prompt: ch.Usage.PromptTokens, Reply: ch.Usage.CompletionTokens})
			}
			return nil
		})

	// Half an answer is still worth keeping — the user watched it appear.
	text := strings.TrimSpace(reply.String())
	if text != "" {
		sess.Append(client.Message{Role: client.RoleAssistant, Content: text})
	}
	if err := s.store.Save(sess); err != nil {
		send(chatEvent{Type: "error", Error: "could not save the chat: " + err.Error()})
	}

	if streamErr != nil && r.Context().Err() == nil {
		send(chatEvent{Type: "error", Error: streamErr.Error()})
		return
	}
	send(chatEvent{Type: "done", ID: sess.ID, Title: sess.Title})
}

// describeEvent renders a gateway event for the transcript, matching the TUI.
func describeEvent(ch client.Chunk) string {
	switch ch.Event {
	case "search":
		return "searched the web — " + ch.Detail
	case "search_failed":
		return "web search failed — " + ch.Detail
	default:
		return strings.TrimSpace(ch.Event + " " + ch.Detail)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
