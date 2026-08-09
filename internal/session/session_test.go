package session

import (
	"strings"
	"testing"
	"time"

	"github.com/justin06lee/alpaca/internal/client"
)

func TestAppendDerivesTitleFromFirstPrompt(t *testing.T) {
	s := New("llama3.2", "box")
	s.Append(client.Message{Role: client.RoleUser, Content: "How do I center a div?"})
	s.Append(client.Message{Role: client.RoleAssistant, Content: "Use flexbox."})
	s.Append(client.Message{Role: client.RoleUser, Content: "A later question"})

	if s.Title != "How do I center a div?" {
		t.Errorf("Title = %q, want the first prompt", s.Title)
	}
}

func TestTitleCollapsesWhitespaceAndTruncates(t *testing.T) {
	s := New("m", "box")
	s.Append(client.Message{
		Role:    client.RoleUser,
		Content: "  explain\n\nthis   very long question that keeps going well past any sensible title length  ",
	})

	if strings.ContainsAny(s.Title, "\n\r\t") {
		t.Errorf("Title %q still contains whitespace control characters", s.Title)
	}
	if strings.Contains(s.Title, "  ") {
		t.Errorf("Title %q has collapsed runs of spaces remaining", s.Title)
	}
	if len([]rune(s.Title)) > 50 {
		t.Errorf("Title %q is %d runes, want it truncated", s.Title, len([]rune(s.Title)))
	}
	if !strings.HasSuffix(s.Title, "…") {
		t.Errorf("Title %q should be marked as truncated", s.Title)
	}
}

// An assistant message must never become the title, even if it somehow arrives
// first.
func TestTitleIgnoresAssistantMessages(t *testing.T) {
	s := New("m", "box")
	s.Append(client.Message{Role: client.RoleAssistant, Content: "unprompted"})
	if s.Title != "" {
		t.Errorf("Title = %q, want empty", s.Title)
	}
}

// The system prompt lives outside the message list so it can be changed
// mid-conversation without rewriting history.
func TestWirePrependsSystemPrompt(t *testing.T) {
	s := New("m", "box")
	s.Append(client.Message{Role: client.RoleUser, Content: "hi"})

	if got := s.Wire(); len(got) != 1 {
		t.Fatalf("Wire() = %d messages with no system prompt, want 1", len(got))
	}

	s.System = "You are terse."
	wire := s.Wire()
	if len(wire) != 2 {
		t.Fatalf("Wire() = %d messages, want 2", len(wire))
	}
	if wire[0].Role != client.RoleSystem || wire[0].Content != "You are terse." {
		t.Errorf("Wire()[0] = %+v, want the system prompt first", wire[0])
	}
	// Wire must not mutate the stored history.
	if len(s.Messages) != 1 {
		t.Errorf("Wire() mutated Messages: now %d entries", len(s.Messages))
	}
}

func TestWireIgnoresBlankSystemPrompt(t *testing.T) {
	s := New("m", "box")
	s.Append(client.Message{Role: client.RoleUser, Content: "hi"})
	s.System = "   \n  "
	if got := s.Wire(); len(got) != 1 {
		t.Errorf("Wire() = %d messages, want the whitespace-only system prompt ignored", len(got))
	}
}

func TestDropAfterLastUserEnablesRetry(t *testing.T) {
	s := New("m", "box")
	s.Append(client.Message{Role: client.RoleUser, Content: "first"})
	s.Append(client.Message{Role: client.RoleAssistant, Content: "reply one"})
	s.Append(client.Message{Role: client.RoleUser, Content: "second"})
	s.Append(client.Message{Role: client.RoleAssistant, Content: "reply two"})

	prompt, ok := s.DropAfterLastUser()
	if !ok {
		t.Fatal("DropAfterLastUser returned false")
	}
	if prompt.Content != "second" {
		t.Errorf("prompt = %q, want the last user message", prompt.Content)
	}
	if len(s.Messages) != 3 {
		t.Fatalf("left %d messages, want 3 (the last reply dropped)", len(s.Messages))
	}
	if last := s.Messages[len(s.Messages)-1]; last.Role != client.RoleUser {
		t.Errorf("last message is %s, want it to be the user prompt", last.Role)
	}
}

func TestDropAfterLastUserOnEmptySession(t *testing.T) {
	s := New("m", "box")
	if _, ok := s.DropAfterLastUser(); ok {
		t.Error("DropAfterLastUser succeeded on an empty session")
	}
}

func TestLastAssistant(t *testing.T) {
	s := New("m", "box")
	if _, ok := s.LastAssistant(); ok {
		t.Error("LastAssistant found a reply in an empty session")
	}

	s.Append(client.Message{Role: client.RoleUser, Content: "q"})
	s.Append(client.Message{Role: client.RoleAssistant, Content: "first reply"})
	s.Append(client.Message{Role: client.RoleUser, Content: "q2"})
	s.Append(client.Message{Role: client.RoleAssistant, Content: "second reply"})

	got, ok := s.LastAssistant()
	if !ok || got.Content != "second reply" {
		t.Errorf("LastAssistant() = %+v, want the most recent reply", got)
	}
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("ALPACA_HOME", t.TempDir())
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestStoreRoundTrip(t *testing.T) {
	store := newTestStore(t)

	s := New("llama3.2", "box")
	s.System = "be brief"
	s.Append(client.Message{Role: client.RoleUser, Content: "hello"})
	s.Append(client.Message{Role: client.RoleAssistant, Content: "hi there"})

	if err := store.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Title != s.Title || loaded.Model != "llama3.2" || loaded.System != "be brief" {
		t.Errorf("loaded = %+v, want the saved fields", loaded)
	}
	if len(loaded.Messages) != 2 || loaded.Messages[1].Content != "hi there" {
		t.Errorf("messages did not survive: %+v", loaded.Messages)
	}
}

// An abandoned empty session should not litter the store.
func TestStoreSkipsEmptySessions(t *testing.T) {
	store := newTestStore(t)

	if err := store.Save(New("m", "box")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("List() = %d sessions, want the empty one skipped", len(sessions))
	}
}

func TestStoreSaveNilIsSafe(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save(nil); err != nil {
		t.Errorf("Save(nil) = %v, want nil", err)
	}
}

func TestStoreListIsNewestFirst(t *testing.T) {
	store := newTestStore(t)

	for i, name := range []string{"oldest", "middle", "newest"} {
		s := New("m", "box")
		s.Append(client.Message{Role: client.RoleUser, Content: name})
		// Explicit timestamps: writes within the same millisecond would
		// otherwise make the ordering arbitrary.
		s.Updated = time.Now().Add(time.Duration(i) * time.Hour)
		if err := store.Save(s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("List() = %d sessions, want 3", len(sessions))
	}
	if sessions[0].Title != "newest" || sessions[2].Title != "oldest" {
		t.Errorf("order = %s, %s, %s; want newest first",
			sessions[0].Title, sessions[1].Title, sessions[2].Title)
	}
}

func TestStoreDelete(t *testing.T) {
	store := newTestStore(t)

	s := New("m", "box")
	s.Append(client.Message{Role: client.RoleUser, Content: "doomed"})
	if err := store.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Delete(s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if sessions, _ := store.List(); len(sessions) != 0 {
		t.Errorf("List() = %d sessions after delete, want 0", len(sessions))
	}
	// Deleting again must not error — the caller should not have to check first.
	if err := store.Delete(s.ID); err != nil {
		t.Errorf("second Delete = %v, want nil", err)
	}
}

func TestStoreListOnMissingDirectory(t *testing.T) {
	store := newTestStore(t)
	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List on a fresh install = %v, want no error", err)
	}
	if len(sessions) != 0 {
		t.Errorf("List() = %d sessions, want 0", len(sessions))
	}
}
