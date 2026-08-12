package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/session"
)

// newRenderedModel builds a Model wired to a stub gateway and sized to a
// terminal, so View() exercises the real layout path.
func newRenderedModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("ALPACA_HOME", t.TempDir())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"id":"render-id","name":"workshop","service":"alpaca"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	endpoint := strings.TrimPrefix(srv.URL, "http://")

	profiles, err := config.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	prof := &config.Profile{ID: "render-id", Name: "workshop", APIKey: "k", LAN: []string{endpoint}}
	profiles.Add(prof)

	c, err := client.Connect(context.Background(), prof, client.Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sess := session.New("llama3.2:latest", "workshop")
	m := New(c, store, profiles, "workshop", sess)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 28})
	// These tests exercise the chat surface, so step past the opening animation.
	m.splashDone = true
	return m
}

// stripANSI removes styling so assertions match on text, not escape codes.
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape && (r == 'm' || r == 'K' || r == 'H' || r == 'J'):
			inEscape = false
		case !inEscape:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestViewRendersEmptyState(t *testing.T) {
	m := newRenderedModel(t)
	view := stripANSI(m.View())

	for _, want := range []string{"alpaca", "llama3.2:latest", "workshop", "enter", "ctrl+p"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
	t.Logf("empty state:\n%s", view)
}

func TestViewRendersConversation(t *testing.T) {
	m := newRenderedModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "What is a goroutine?"})
	m.sess.Append(client.Message{Role: client.RoleAssistant,
		Content: "A **goroutine** is a lightweight thread.\n\n```go\ngo doWork()\n```\n\nThat's it."})
	m.rebuildCache()
	m.refreshViewport(true)

	view := stripANSI(m.View())

	if !strings.Contains(view, "What is a goroutine?") {
		t.Errorf("user message missing:\n%s", view)
	}
	if !strings.Contains(view, "lightweight thread") {
		t.Errorf("assistant reply missing or mangled:\n%s", view)
	}
	// The markdown must have gone through the parser. Fence markers disappearing
	// while the code survives is the reliable signal: emphasis is *not*, because
	// with no TTY glamour renders bold as literal "**" on purpose, which is the
	// correct plain-text representation.
	if strings.Contains(view, "```") {
		t.Errorf("code fences were not processed — markdown did not render:\n%s", view)
	}
	if !strings.Contains(view, "doWork") {
		t.Errorf("code block content missing:\n%s", view)
	}
	t.Logf("conversation:\n%s", view)
}

func TestViewRendersStreamingState(t *testing.T) {
	m := newRenderedModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "hello"})
	m.rebuildCache()
	primeStream(m)
	m.handleStreamEvent(streamEvent{content: "Partial answer so far"})
	m.refreshViewport(true)

	view := stripANSI(m.View())

	if !strings.Contains(view, "Partial answer so far") {
		t.Errorf("streaming text missing:\n%s", view)
	}
	// The composer is replaced by a stop hint while generating.
	if !strings.Contains(view, "esc to stop") {
		t.Errorf("no stop hint while streaming:\n%s", view)
	}
	t.Logf("streaming:\n%s", view)
}

func TestViewRendersHelp(t *testing.T) {
	m := newRenderedModel(t)
	m.showHelp = true

	view := stripANSI(m.View())
	for _, want := range []string{"keys", "ctrl+j", "slash commands", "/system"} {
		if !strings.Contains(view, want) {
			t.Errorf("help is missing %q:\n%s", want, view)
		}
	}
}

func TestViewRendersModelPicker(t *testing.T) {
	m := newRenderedModel(t)
	m.models = []client.Model{
		{ID: "llama3.2:latest", ParameterSize: "3.2B", Quantization: "Q4_K_M", Size: 2019393189},
		{ID: "qwen2.5:7b", ParameterSize: "7B", Quantization: "Q4_K_M", Size: 4700000000},
	}
	m.openModelPicker()

	view := stripANSI(m.View())
	for _, want := range []string{"Choose a model", "llama3.2:latest", "3.2B", "2.0 GB", "qwen2.5:7b"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker is missing %q:\n%s", want, view)
		}
	}
	t.Logf("model picker:\n%s", view)
}

// A narrow terminal must still produce usable output rather than panicking on
// negative widths.
func TestViewSurvivesTinyTerminal(t *testing.T) {
	m := newRenderedModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "hello there"})

	for _, size := range []tea.WindowSizeMsg{
		{Width: 20, Height: 6},
		{Width: 1, Height: 1},
		{Width: 200, Height: 60},
	} {
		m.Update(size)
		if out := m.View(); out == "" && size.Width > 1 {
			t.Errorf("empty view at %dx%d", size.Width, size.Height)
		}
	}
}

// Resizing has to rebuild the width-bound markdown cache, or text stays wrapped
// to the old width.
func TestResizeRebuildsRenderCache(t *testing.T) {
	m := newRenderedModel(t)
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("word ", 60)})
	m.rebuildCache()

	wide := m.rendered[0]
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	narrow := m.rendered[0]

	if wide == narrow {
		t.Error("render cache was not rebuilt after a resize")
	}
	if maxLineWidth(stripANSI(narrow)) > 45 {
		t.Errorf("text still wrapped to the old width after resizing:\n%s", narrow)
	}
}

func maxLineWidth(s string) int {
	longest := 0
	for _, line := range strings.Split(s, "\n") {
		if n := len([]rune(line)); n > longest {
			longest = n
		}
	}
	return longest
}
