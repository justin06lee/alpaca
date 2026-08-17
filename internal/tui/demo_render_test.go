package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/session"
)

// TestDemoModeRendersAConversation drives the interface exactly as `alpaca chat
// --demo` does and dumps the result, so the preview can be inspected without a
// terminal.
func TestDemoModeRendersAConversation(t *testing.T) {
	t.Setenv("ALPACA_HOME", t.TempDir())

	c, stop, err := client.NewDemo()
	if err != nil {
		t.Fatalf("NewDemo: %v", err)
	}
	defer stop()

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	profiles := &config.Profiles{Entries: map[string]*config.Profile{}}
	sess := session.New("llama3.2:latest", "demo")

	m := New(Connected(c), store, profiles, "demo", sess)
	m.Update(tea.WindowSizeMsg{Width: 92, Height: 32})
	finishSplash(t, m, c)

	// Empty state is the first thing anyone sees.
	t.Logf("EMPTY STATE\n%s", stripANSI(m.View()))

	// Drive a real turn through the demo server.
	m.input.SetValue("write me a go function")
	m.send()

	deadline := context.Background()
	_ = deadline
	for i := 0; i < 400 && m.streaming; i++ {
		ev, ok := <-m.events
		if !ok {
			m.finishStream()
			break
		}
		m.handleStreamEvent(ev)
	}
	if m.streaming {
		m.finishStream()
	}
	// Dock the composer by winding its clock past the end of the slide.
	m.slideStart = time.Now().Add(-2 * slideDuration)
	m.Update(slideTickMsg{})
	m.rebuildCache()
	m.refreshViewport(true)
	m.viewport.GotoBottom()

	view := stripANSI(m.View())
	t.Logf("AFTER A REPLY\n%s", view)

	// The prompt wraps inside its bubble, so match a fragment rather than the
	// whole line.
	if !strings.Contains(view, "write me a go") {
		t.Errorf("the prompt is missing from the transcript:\n%s", view)
	}
	if !strings.Contains(view, "offline demo") {
		t.Errorf("header does not say this is a demo:\n%s", view)
	}
	if !strings.Contains(view, "runes") {
		t.Errorf("the canned code reply did not render:\n%s", view)
	}
	// Fences must have been consumed by the markdown renderer.
	if strings.Contains(view, "```") {
		t.Errorf("code fences were not rendered:\n%s", view)
	}
}

func TestDemoModeSessionPickerHasContent(t *testing.T) {
	t.Setenv("ALPACA_HOME", t.TempDir())

	c, stop, err := client.NewDemo()
	if err != nil {
		t.Fatalf("NewDemo: %v", err)
	}
	defer stop()

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Mirrors what runDemoChat seeds, so the picker is not empty on first look.
	for _, s := range []struct{ prompt, reply string }{
		{"How do I reverse a string in Go?", "Convert to []rune first."},
		{"Slices versus arrays?", "An array's length is part of its type."},
	} {
		sess := session.New("llama3.2:latest", "demo")
		sess.Append(client.Message{Role: client.RoleUser, Content: s.prompt})
		sess.Append(client.Message{Role: client.RoleAssistant, Content: s.reply})
		if err := store.Save(sess); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	m := New(Connected(c), store, &config.Profiles{Entries: map[string]*config.Profile{}}, "demo",
		session.New("llama3.2:latest", "demo"))
	m.Update(tea.WindowSizeMsg{Width: 92, Height: 24})
	finishSplash(t, m, c)
	m.openSessionPicker()

	view := stripANSI(m.View())
	t.Logf("SESSION PICKER\n%s", view)

	if !strings.Contains(view, "Saved chats") || !strings.Contains(view, "reverse a string") {
		t.Errorf("session picker is empty or wrong:\n%s", view)
	}
}
