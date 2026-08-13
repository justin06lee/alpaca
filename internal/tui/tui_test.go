package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/session"
	"github.com/muesli/termenv"
)

// newTestModel builds a Model backed by a temp store. The client is nil, so
// tests here must avoid the paths that reach the network — everything they do
// exercise is local state management.
func newTestModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("ALPACA_HOME", t.TempDir())

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	profiles, err := config.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	profiles.Add(&config.Profile{ID: "id", Name: "test", APIKey: "k"})

	sess := session.New("llama3.2:latest", "test")
	return New(Connected(nil), store, profiles, "test", sess)
}

// ---------------------------------------------------------------------------
// Picker
// ---------------------------------------------------------------------------

func testItems() []pickerItem {
	return []pickerItem{
		{id: "llama3.2:latest", title: "llama3.2:latest", desc: "3.2B · Q4_K_M"},
		{id: "qwen2.5:7b", title: "qwen2.5:7b", desc: "7B · Q4_K_M"},
		{id: "mistral:latest", title: "mistral:latest", desc: "7B · Q5_K_M"},
	}
}

func TestPickerFiltersOnTitleAndDescription(t *testing.T) {
	p := newPicker(pickerModel, "Models", testItems())
	if len(p.visible) != 3 {
		t.Fatalf("unfiltered visible = %d, want 3", len(p.visible))
	}

	p.appendFilter("llama")
	if len(p.visible) != 1 {
		t.Fatalf("filter %q matched %d items, want 1", p.filter, len(p.visible))
	}
	got, _ := p.selected()
	if got.id != "llama3.2:latest" {
		t.Errorf("selected = %q, want llama3.2:latest", got.id)
	}

	// Descriptions are searchable too, which is how a user finds "the 7B ones".
	p2 := newPicker(pickerModel, "Models", testItems())
	p2.appendFilter("7B")
	if len(p2.visible) != 2 {
		t.Errorf("filter %q matched %d items, want the 2 with 7B in the description", p2.filter, len(p2.visible))
	}
}

func TestPickerFilterIsCaseInsensitive(t *testing.T) {
	p := newPicker(pickerModel, "Models", testItems())
	p.appendFilter("MISTRAL")
	if len(p.visible) != 1 {
		t.Errorf("case-insensitive filter matched %d, want 1", len(p.visible))
	}
}

func TestPickerBackspaceRestoresMatches(t *testing.T) {
	p := newPicker(pickerModel, "Models", testItems())
	p.appendFilter("llama")
	p.backspaceFilter()
	p.backspaceFilter()
	if p.filter != "lla" {
		t.Errorf("filter = %q, want %q", p.filter, "lla")
	}
	if len(p.visible) != 1 {
		t.Errorf("visible = %d for filter %q", len(p.visible), p.filter)
	}

	for range "lla" {
		p.backspaceFilter()
	}
	if len(p.visible) != 3 {
		t.Errorf("clearing the filter left %d visible, want all 3", len(p.visible))
	}
	// Backspacing an empty filter must not panic or underflow.
	p.backspaceFilter()
}

func TestPickerMoveWraps(t *testing.T) {
	p := newPicker(pickerModel, "Models", testItems())

	p.move(-1) // up from the first entry
	if p.cursor != 2 {
		t.Errorf("cursor = %d after moving up from the top, want it wrapped to 2", p.cursor)
	}
	p.move(1)
	if p.cursor != 0 {
		t.Errorf("cursor = %d after moving down from the bottom, want it wrapped to 0", p.cursor)
	}
}

func TestPickerHandlesNoMatches(t *testing.T) {
	p := newPicker(pickerModel, "Models", testItems())
	p.appendFilter("nothing-matches-this")

	if len(p.visible) != 0 {
		t.Fatalf("visible = %d, want 0", len(p.visible))
	}
	if _, ok := p.selected(); ok {
		t.Error("selected() returned an item when nothing matched")
	}
	p.move(1) // must not panic
	// The view must still render rather than crash on an empty list.
	if out := p.view(60, 10); !strings.Contains(out, "nothing matches") {
		t.Errorf("view did not explain the empty result:\n%s", out)
	}
}

func TestPickerViewMarksSelection(t *testing.T) {
	p := newPicker(pickerModel, "Models", testItems())
	out := p.view(80, 12)
	if !strings.Contains(out, "Models") {
		t.Errorf("view is missing the title:\n%s", out)
	}
	if !strings.Contains(out, "llama3.2:latest") {
		t.Errorf("view is missing the items:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Slash commands
// ---------------------------------------------------------------------------

func TestSetModelByNameMatchesPartials(t *testing.T) {
	m := newTestModel(t)
	m.models = []client.Model{
		{ID: "llama3.2:latest"},
		{ID: "qwen2.5:7b"},
		{ID: "mistral:latest"},
	}

	cases := map[string]string{
		"llama3.2:latest": "llama3.2:latest", // exact
		"qwen":            "qwen2.5:7b",      // prefix
		"2.5":             "qwen2.5:7b",      // substring
		"MISTRAL":         "mistral:latest",  // case-insensitive
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			m.setModelByName(input)
			if m.sess.Model != want {
				t.Errorf("/model %s selected %q, want %q", input, m.sess.Model, want)
			}
		})
	}
}

func TestSetModelByNameRejectsUnknown(t *testing.T) {
	m := newTestModel(t)
	m.models = []client.Model{{ID: "llama3.2:latest"}}
	before := m.sess.Model

	m.setModelByName("does-not-exist")

	if m.sess.Model != before {
		t.Errorf("model changed to %q on a failed match", m.sess.Model)
	}
	if !m.statusErr || !strings.Contains(m.status, "no model matching") {
		t.Errorf("status = %q (err=%v), want an explanation", m.status, m.statusErr)
	}
}

func TestUnknownSlashCommandReportsError(t *testing.T) {
	m := newTestModel(t)
	m.runSlash("/nonsense")
	if !m.statusErr || !strings.Contains(m.status, "unknown command") {
		t.Errorf("status = %q (err=%v), want an unknown-command error", m.status, m.statusErr)
	}
}

func TestSlashHelpOpensHelp(t *testing.T) {
	m := newTestModel(t)
	m.runSlash("/help")
	if !m.showHelp {
		t.Error("/help did not open the help screen")
	}
}

func TestSlashSystemSetsAndClears(t *testing.T) {
	m := newTestModel(t)

	m.runSlash("/system You are a pirate.")
	if m.sess.System != "You are a pirate." {
		t.Errorf("System = %q, want the prompt text", m.sess.System)
	}

	m.runSlash("/system")
	if m.sess.System != "" {
		t.Errorf("System = %q after a bare /system, want it cleared", m.sess.System)
	}
	if !strings.Contains(m.status, "cleared") {
		t.Errorf("status = %q, want it to confirm clearing", m.status)
	}
}

func TestSlashClearKeepsModelAndSystem(t *testing.T) {
	m := newTestModel(t)
	m.sess.System = "be brief"
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "hi"})
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "hello"})
	m.cacheLast()

	m.runSlash("/clear")

	if len(m.sess.Messages) != 0 {
		t.Errorf("messages = %d, want 0", len(m.sess.Messages))
	}
	if len(m.rendered) != 0 {
		t.Errorf("render cache = %d entries, want it emptied alongside the messages", len(m.rendered))
	}
	if m.sess.System != "be brief" {
		t.Errorf("System = %q, want it preserved across /clear", m.sess.System)
	}
	if m.sess.Model != "llama3.2:latest" {
		t.Errorf("Model = %q, want it preserved across /clear", m.sess.Model)
	}
}

// A message beginning with "/" is a command, not chat input.
func TestSendRoutesSlashToCommand(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("/help")

	m.send()

	if !m.showHelp {
		t.Error("a leading slash was not treated as a command")
	}
	if len(m.sess.Messages) != 0 {
		t.Errorf("the command was also appended as a chat message: %+v", m.sess.Messages)
	}
	if m.input.Value() != "" {
		t.Errorf("composer = %q, want it cleared", m.input.Value())
	}
}

func TestSendRefusesWithNoModelSelected(t *testing.T) {
	m := newTestModel(t)
	m.sess.Model = ""
	m.input.SetValue("hello")

	m.send()

	if len(m.sess.Messages) != 0 {
		t.Error("message was queued with no model selected")
	}
	if !m.statusErr || !strings.Contains(m.status, "no model") {
		t.Errorf("status = %q, want an explanation", m.status)
	}
}

func TestSendIgnoresBlankInput(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("   \n  ")
	m.send()
	if len(m.sess.Messages) != 0 {
		t.Error("whitespace-only input was sent")
	}
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

// primeStream puts the model into the state startStream would leave it in,
// without needing a live client.
func primeStream(m *Model) chan streamEvent {
	events := make(chan streamEvent, 8)
	m.events = events
	m.streaming = true
	m.streamBuf = ""
	m.streamErr = nil
	return events
}

func TestStreamAccumulatesAndCommits(t *testing.T) {
	m := newTestModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "hi"})
	primeStream(m)

	m.handleStreamEvent(streamEvent{content: "Hel"})
	m.handleStreamEvent(streamEvent{content: "lo"})
	m.handleStreamEvent(streamEvent{usage: &client.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}})

	if m.streamBuf != "Hello" {
		t.Errorf("streamBuf = %q, want %q", m.streamBuf, "Hello")
	}
	if !m.dirty {
		t.Error("content arrived but the pane was not marked for repaint")
	}

	m.finishStream()

	if m.streaming {
		t.Error("still streaming after finishStream")
	}
	if len(m.sess.Messages) != 2 {
		t.Fatalf("messages = %d, want the reply committed", len(m.sess.Messages))
	}
	last := m.sess.Messages[1]
	if last.Role != client.RoleAssistant || last.Content != "Hello" {
		t.Errorf("committed = %+v, want the assistant reply", last)
	}
	if m.lastUsage == nil || m.lastUsage.TotalTokens != 5 {
		t.Errorf("lastUsage = %+v, want the token counts retained", m.lastUsage)
	}
}

// Half an answer is still worth keeping when the user stops generation.
func TestCancelledStreamKeepsPartialOutput(t *testing.T) {
	m := newTestModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "hi"})
	primeStream(m)

	m.handleStreamEvent(streamEvent{content: "half an ans"})
	m.handleStreamEvent(streamEvent{err: context.Canceled})
	m.finishStream()

	if len(m.sess.Messages) != 2 {
		t.Fatalf("messages = %d, want the partial reply kept", len(m.sess.Messages))
	}
	if m.sess.Messages[1].Content != "half an ans" {
		t.Errorf("kept %q, want the partial text", m.sess.Messages[1].Content)
	}
	// A deliberate stop is not an error.
	if m.statusErr {
		t.Error("cancelling was reported as an error")
	}
	if m.status != "stopped" {
		t.Errorf("status = %q, want %q", m.status, "stopped")
	}
}

func TestFailedStreamReportsError(t *testing.T) {
	m := newTestModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "hi"})
	primeStream(m)

	m.handleStreamEvent(streamEvent{err: errFake{"gpu ran out of memory"}})
	m.finishStream()

	if !m.statusErr || !strings.Contains(m.status, "gpu ran out of memory") {
		t.Errorf("status = %q (err=%v), want the failure surfaced", m.status, m.statusErr)
	}
}

func TestFinishStreamIsIdempotent(t *testing.T) {
	m := newTestModel(t)
	primeStream(m)
	m.handleStreamEvent(streamEvent{content: "x"})

	m.finishStream()
	before := len(m.sess.Messages)
	m.finishStream() // a second close must not double-commit

	if len(m.sess.Messages) != before {
		t.Errorf("messages = %d after a second finishStream, want %d", len(m.sess.Messages), before)
	}
}

func TestEmptyReplyIsNotCommitted(t *testing.T) {
	m := newTestModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "hi"})
	primeStream(m)

	m.finishStream()

	if len(m.sess.Messages) != 1 {
		t.Errorf("messages = %d, want no empty assistant message appended", len(m.sess.Messages))
	}
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func TestNewSessionCarriesModelAndSystem(t *testing.T) {
	m := newTestModel(t)
	m.sess.System = "be brief"
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "old chat"})
	oldID := m.sess.ID

	m.newSession()

	if m.sess.ID == oldID {
		t.Error("newSession reused the old session")
	}
	if len(m.sess.Messages) != 0 {
		t.Errorf("new session already has %d messages", len(m.sess.Messages))
	}
	if m.sess.Model != "llama3.2:latest" || m.sess.System != "be brief" {
		t.Errorf("new session lost the model or system prompt: %+v", m.sess)
	}

	// The previous conversation must have been saved, not dropped.
	saved, err := m.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(saved) != 1 || saved[0].ID != oldID {
		t.Errorf("previous session was not persisted: %+v", saved)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:        "512 B",
		2019393189: "2.0 GB",
		1500:       "1.5 kB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 20); got != "hello" {
		t.Errorf("truncate did not leave a short string alone: %q", got)
	}
	got := truncate("hello world this is long", 10)
	if lipgloss.Width(got) > 10 {
		t.Errorf("truncate(%q) = %q, longer than the limit", "hello world this is long", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate = %q, want an ellipsis marker", got)
	}
}

// Status text arrives at truncate already styled. Cutting by rune count used to
// slice escape sequences in half, spilling raw fragments into the frame.
func TestTruncateIsANSIAware(t *testing.T) {
	styled := "\x1b[38;5;208mzero zero zero zero zero\x1b[0m plain tail"
	got := truncate(styled, 8)

	if w := lipgloss.Width(got); w > 8 {
		t.Errorf("visible width = %d, want <= 8 (%q)", w, got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("truncate = %q, want the ellipsis marker", got)
	}
	if strings.Contains(got, "\x1b") && !strings.Contains(got, "\x1b[38;5;208m") {
		t.Errorf("truncate cut an escape sequence in half: %q", got)
	}
}

// errFake is a stand-in for a server-side failure.
type errFake struct{ msg string }

func (e errFake) Error() string { return e.msg }

// Typing must not change the composer's shape. The frame's Width covers
// content plus padding; when it was set two columns too tight, lipgloss
// wrapped the textarea's cursor line — whose styled trailing spaces survive
// the word-wrapper where bare ones are dropped — and the box grew a phantom
// row on the first keypress, shifting the whole welcome screen up. The bug
// only shows with colours on, because colourless trailing spaces are trimmed.
func TestComposerKeepsItsShapeWhileTyping(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	m := newTestModel(t)
	m.splashDone = true
	m.Update(tea.WindowSizeMsg{Width: 117, Height: 40})

	shape := func() (top, rows, width int) {
		lines := strings.Split(m.View(), "\n")
		top = -1
		for i, l := range lines {
			s := ansi.Strip(l)
			if strings.Contains(s, "╭") {
				top = i
				width = lipgloss.Width(strings.TrimSpace(s))
			}
			if strings.Contains(s, "│") || strings.Contains(s, "╭") || strings.Contains(s, "╰") {
				rows++
			}
		}
		return top, rows, width
	}

	beforeTop, beforeRows, beforeWidth := shape()
	for _, r := range "hello?" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	afterTop, afterRows, afterWidth := shape()

	if beforeTop != afterTop {
		t.Errorf("composer top moved from row %d to %d after typing", beforeTop, afterTop)
	}
	if beforeRows != afterRows {
		t.Errorf("composer grew from %d to %d rows after typing", beforeRows, afterRows)
	}
	if beforeWidth != afterWidth {
		t.Errorf("composer width changed from %d to %d after typing", beforeWidth, afterWidth)
	}
}

// The requested composer width is the box's outer width, border included.
func TestComposerRendersAtTheRequestedWidth(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 117, Height: 40})

	box := strings.Split(m.renderComposer(74), "\n")
	if got := lipgloss.Width(box[0]); got != 74 {
		t.Errorf("outer width = %d, want the requested 74", got)
	}
	if len(box) != composerRows {
		t.Errorf("box has %d rows, want %d", len(box), composerRows)
	}
}

// Sprite rows are all rendered at the art's full width so that per-line
// centring cannot shear the image.
func TestSpriteRowsAreFullWidth(t *testing.T) {
	want := widestRow(miniAlpaca)
	for i, l := range strings.Split(renderSprite(miniAlpaca), "\n") {
		if got := lipgloss.Width(l); got != want {
			t.Errorf("sprite row %d is %d cells wide, want %d", i, got, want)
		}
	}
}

// The wheel scrolls the transcript; in a picker it moves the cursor instead.
func TestMouseWheelScrollsTheTranscript(t *testing.T) {
	m := newTestModel(t)
	m.splashDone = true
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})

	for i := 0; i < 30; i++ {
		m.sess.Append(client.Message{Role: client.RoleUser, Content: fmt.Sprintf("message %d", i)})
	}
	m.rebuildCache()
	m.refreshViewport(true)
	m.viewport.GotoTop()

	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if got := m.viewport.YOffset; got != wheelStep {
		t.Errorf("one wheel notch moved the view to offset %d, want %d", got, wheelStep)
	}
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if got := m.viewport.YOffset; got != 0 {
		t.Errorf("wheel up did not return to the top: offset %d", got)
	}
}

func TestMouseWheelMovesThePickerCursor(t *testing.T) {
	m := newTestModel(t)
	m.splashDone = true
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.pick = newPicker(pickerModel, "Choose a model", []pickerItem{
		{id: "a", title: "a"}, {id: "b", title: "b"}, {id: "c", title: "c"},
	})
	m.mode = pickerModel

	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.pick.cursor != 1 {
		t.Errorf("wheel down moved the cursor to %d, want 1", m.pick.cursor)
	}
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.pick.cursor != 0 {
		t.Errorf("wheel up moved the cursor to %d, want 0", m.pick.cursor)
	}
}
