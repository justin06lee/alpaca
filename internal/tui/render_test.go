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
	m := New(Connected(c), store, profiles, "workshop", sess)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 28})
	// These tests exercise the chat surface, so run the opening through to the
	// end first.
	finishSplash(t, m, c)
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

	if !strings.Contains(view, "alpaca") {
		t.Errorf("header missing:\n%s", view)
	}
	// The animal sits above the greeting.
	if !strings.ContainsAny(view, "▀▄█") {
		t.Errorf("the mini alpaca did not render:\n%s", view)
	}
	if !strings.Contains(view, m.greeting) {
		t.Errorf("greeting %q missing:\n%s", m.greeting, view)
	}
	// A framed composer, floating rather than docked.
	if !strings.Contains(view, "╭") || !strings.Contains(view, "Ask anything") {
		t.Errorf("composer missing:\n%s", view)
	}
	t.Logf("empty state:\n%s", view)
}

// The empty-state composer is centred: narrower than the screen, and with room
// left below it rather than sitting on the status bar.
func TestEmptyStateComposerIsCentred(t *testing.T) {
	m := newRenderedModel(t)
	lines := strings.Split(stripANSI(m.View()), "\n")

	top := -1
	for i, line := range lines {
		if strings.Contains(line, "╭") {
			top = i
			break
		}
	}
	if top < 0 {
		t.Fatalf("no composer found:\n%s", strings.Join(lines, "\n"))
	}
	if top < 4 {
		t.Errorf("composer starts at row %d, expected it lower down the screen", top)
	}
	if top > len(lines)-8 {
		t.Errorf("composer starts at row %d of %d, expected room left below it", top, len(lines))
	}

	indent := len(lines[top]) - len(strings.TrimLeft(lines[top], " "))
	if indent < 4 {
		t.Errorf("composer is indented %d columns, expected it narrower and centred", indent)
	}
}

func TestViewRendersConversation(t *testing.T) {
	m := newRenderedModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "What is a goroutine?"})
	m.sess.Append(client.Message{Role: client.RoleAssistant,
		Content: "A **goroutine** is a lightweight thread.\n\n```go\ngo doWork()\n```\n\nThat's it."})
	m.rebuildCache()
	m.refreshViewport(true)

	view := stripANSI(m.View())

	if !strings.Contains(view, "goroutine") {
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

// The two sides of the conversation lean to opposite edges, which is what makes
// a transcript scannable without reading any of it.
func TestUserRightAssistantLeft(t *testing.T) {
	m := newRenderedModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "hello there"})
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "General Kenobi."})
	m.rebuildCache()
	m.refreshViewport(true)

	lines := strings.Split(stripANSI(m.View()), "\n")

	var bubbleIndent, assistantIndent = -1, -1
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if bubbleIndent < 0 && strings.HasPrefix(trimmed, "╭") {
			bubbleIndent = len(line) - len(trimmed)
		}
		if assistantIndent < 0 && strings.HasPrefix(trimmed, "▌") {
			assistantIndent = len(line) - len(trimmed)
		}
	}
	if bubbleIndent < 0 {
		t.Fatalf("no user bubble found:\n%s", strings.Join(lines, "\n"))
	}
	if assistantIndent < 0 {
		t.Fatalf("no assistant rail found:\n%s", strings.Join(lines, "\n"))
	}
	if bubbleIndent <= assistantIndent {
		t.Errorf("user bubble indent %d is not to the right of the assistant rail %d",
			bubbleIndent, assistantIndent)
	}
	if assistantIndent > 4 {
		t.Errorf("assistant rail is indented %d columns, expected it hard left", assistantIndent)
	}
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

// finishSplash drives the opening to completion the way the runtime does,
// rather than forcing the flag, so the work the model defers while the opening
// owns the screen actually happens.
func finishSplash(t *testing.T, m *Model, c *client.Client) {
	t.Helper()
	m.Update(connectedMsg{client: c})
	for i := 0; i < m.splashTotal()+4 && !m.splashDone; i++ {
		m.Update(splashTickMsg{})
	}
	if !m.splashDone {
		t.Fatal("opening never finished")
	}
}
