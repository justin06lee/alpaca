// Package session persists chat history to disk.
//
// Conversations are stored as one JSON file each, rather than in a database,
// so that the whole store stays greppable, syncable, and trivially portable —
// and so alpaca keeps building as a single static binary with no cgo.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
)

// Session is one conversation.
type Session struct {
	ID       string           `json:"id"`
	Title    string           `json:"title"`
	Model    string           `json:"model"`
	System   string           `json:"system"`
	Messages []client.Message `json:"messages"`
	Created  time.Time        `json:"created"`
	Updated  time.Time        `json:"updated"`
	// Server records which profile this conversation belongs to, so switching
	// servers does not mix histories.
	Server string `json:"server,omitempty"`
}

// New starts an empty session.
func New(model, server string) *Session {
	id, err := config.NewID()
	if err != nil {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	now := time.Now()
	return &Session{
		ID:      id,
		Model:   model,
		Server:  server,
		Created: now,
		Updated: now,
	}
}

// Append adds a message and refreshes the derived title.
func (s *Session) Append(msg client.Message) {
	s.Messages = append(s.Messages, msg)
	s.Updated = time.Now()
	if s.Title == "" && msg.Role == client.RoleUser {
		s.Title = deriveTitle(msg.Content)
	}
}

// LastAssistant returns the most recent assistant reply.
func (s *Session) LastAssistant() (client.Message, bool) {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == client.RoleAssistant {
			return s.Messages[i], true
		}
	}
	return client.Message{}, false
}

// DropAfterLastUser removes the trailing assistant reply so it can be
// regenerated, and returns the user prompt that produced it.
func (s *Session) DropAfterLastUser() (client.Message, bool) {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == client.RoleUser {
			prompt := s.Messages[i]
			s.Messages = s.Messages[:i+1]
			s.Updated = time.Now()
			return prompt, true
		}
	}
	return client.Message{}, false
}

// Wire builds the message list to send, prepending the system prompt.
//
// The system prompt is stored separately rather than as messages[0] so it can
// be changed mid-conversation without rewriting history.
func (s *Session) Wire() []client.Message {
	if strings.TrimSpace(s.System) == "" {
		return s.Messages
	}
	out := make([]client.Message, 0, len(s.Messages)+1)
	out = append(out, client.Message{Role: client.RoleSystem, Content: s.System})
	return append(out, s.Messages...)
}

// Empty reports whether anything has been said yet.
func (s *Session) Empty() bool { return len(s.Messages) == 0 }

// deriveTitle summarises a session by its opening prompt.
func deriveTitle(prompt string) string {
	// Collapse whitespace so a multi-line prompt still yields a one-line title.
	fields := strings.FieldsFunc(prompt, unicode.IsSpace)
	title := strings.Join(fields, " ")

	const maxLen = 48
	if len(title) <= maxLen {
		return title
	}
	// Cut at a word boundary when there is one nearby, to avoid mid-word truncation.
	cut := title[:maxLen]
	if idx := strings.LastIndex(cut, " "); idx > maxLen/2 {
		cut = cut[:idx]
	}
	return cut + "…"
}

// Store is the on-disk collection of sessions.
type Store struct {
	dir string
}

// NewStore opens the session directory.
func NewStore() (*Store, error) {
	dir, err := config.Path("sessions", "placeholder")
	if err != nil {
		return nil, err
	}
	return &Store{dir: filepath.Dir(dir)}, nil
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// Save writes a session, skipping empty ones so abandoned sessions do not
// accumulate as clutter.
func (s *Store) Save(sess *Session) error {
	if sess == nil || sess.Empty() {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}

	// Same atomic write as the config store: a crash must not truncate history.
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write session: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path(sess.ID))
}

// Load reads one session.
func (s *Store) Load(id string) (*Session, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, fmt.Errorf("read session %s: %w", id, err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", id, err)
	}
	return &sess, nil
}

// List returns every session, most recently updated first.
//
// A single corrupt file is skipped rather than failing the whole listing: one
// bad file should not make the user's entire history inaccessible.
func (s *Store) List() ([]*Session, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session dir: %w", err)
	}

	var out []*Session
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		sess, err := s.Load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, sess)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Delete removes a session.
func (s *Store) Delete(id string) error {
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
