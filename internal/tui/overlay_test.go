package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/muesli/termenv"
)

func TestSpliceRowReplacesTheMiddle(t *testing.T) {
	got := spliceRow("abcdefghij", "XYZ", 3)
	if got != "abcXYZghij" {
		t.Errorf("spliceRow = %q, want abcXYZghij", got)
	}
}

func TestSpliceRowPadsShortRows(t *testing.T) {
	got := spliceRow("ab", "XYZ", 5)
	if got != "ab   XYZ" {
		t.Errorf("spliceRow = %q, want the gap filled with spaces", got)
	}
}

// Cutting a styled row must not split escape sequences or let an open style
// bleed into the panel.
func TestSpliceRowSurvivesStyledRows(t *testing.T) {
	styled := "\x1b[38;5;208m" + strings.Repeat("x", 20) + "\x1b[0m"
	got := spliceRow(styled, "PANEL", 4)

	plain := ansi.Strip(got)
	if plain != "xxxxPANELxxxxxxxxxxx" {
		t.Errorf("visible cells = %q, want the panel spliced into the row", plain)
	}
	if w := lipgloss.Width(got); w != 20 {
		t.Errorf("row width changed to %d after splicing, want 20", w)
	}
}

func TestOverlayLeavesRowsOutsideThePanelAlone(t *testing.T) {
	base := "aaaa\nbbbb\ncccc\ndddd"
	got := strings.Split(overlay(base, "XX", 1, 2), "\n")
	want := []string{"aaaa", "bbbb", "cXXc", "dddd"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOverlayIgnoresRowsBeyondTheBase(t *testing.T) {
	got := overlay("aaaa", "X\nY\nZ", 0, 0)
	if lines := strings.Split(got, "\n"); len(lines) != 1 || lines[0] != "Xaaa" {
		t.Errorf("overlay = %q, want the out-of-range panel rows dropped", got)
	}
}

// panelModel is a chat with enough history that the base view has content in
// every region a panel could cover.
func panelModel(t *testing.T) *Model {
	m := newTestModel(t)
	m.splashDone = true
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	for i := 0; i < 6; i++ {
		m.sess.Append(client.Message{Role: client.RoleUser, Content: fmt.Sprintf("question number %d", i)})
		m.sess.Append(client.Message{Role: client.RoleAssistant, Content: fmt.Sprintf("answer %d in some detail", i)})
	}
	m.rebuildCache()
	m.refreshViewport(true)
	return m
}

// assertWellFormed checks the composed frame still fits the terminal exactly.
func assertWellFormed(t *testing.T, m *Model, view string) {
	t.Helper()
	rows := strings.Split(view, "\n")
	if len(rows) > m.height {
		t.Errorf("view has %d rows for a %d-row terminal", len(rows), m.height)
	}
	for i, row := range rows {
		if w := lipgloss.Width(row); w > m.width {
			t.Errorf("row %d is %d cells wide for a %d-cell terminal", i, w, m.width)
		}
	}
}

func TestModelPickerFloatsOverTheChat(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	m := panelModel(t)
	m.models = []client.Model{
		{ID: "llama3.2:latest", ParameterSize: "3.2B"},
		{ID: "qwen2.5:7b", ParameterSize: "7B"},
	}
	m.openModelPicker()

	view := m.View()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "Choose a model") {
		t.Fatal("the picker panel is missing from the view")
	}
	// The conversation must still be visible around the popup.
	if !strings.Contains(plain, "question number") {
		t.Error("the popup replaced the chat instead of floating over it")
	}
	assertWellFormed(t, m, view)
}

func TestSessionSidebarSticksToTheLeftEdge(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	m := panelModel(t)
	m.pick = newPicker(pickerSession, "Saved chats", []pickerItem{
		{id: "1", title: "What's up?", desc: "2m ago · 4 msgs"},
		{id: "2", title: "Reverse a string", desc: "2h ago · 2 msgs"},
	})
	m.mode = pickerSession

	view := m.View()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "Saved chats") {
		t.Fatal("the sidebar is missing from the view")
	}
	rows := strings.Split(plain, "\n")
	if !strings.HasPrefix(rows[headerHeight], "╭") {
		t.Errorf("sidebar does not start at the left edge: %q", rows[headerHeight])
	}
	if !strings.Contains(plain, "question number") {
		t.Error("the sidebar replaced the chat instead of floating beside it")
	}
	assertWellFormed(t, m, view)
}

func TestHelpFloatsOverTheChat(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	m := panelModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 44})
	m.showHelp = true

	view := m.View()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "slash commands") {
		t.Fatal("help content is missing from the view")
	}
	// The panel covers the middle of the screen, so only fragments of the
	// conversation survive at its edges — but they must survive.
	if !strings.Contains(plain, "answer") {
		t.Error("help replaced the chat instead of floating over it")
	}
	assertWellFormed(t, m, view)
}

// Terminals with no room to float fall back to the full-screen list.
func TestPanelsFallBackOnTinyTerminals(t *testing.T) {
	m := panelModel(t)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})

	m.pick = newPicker(pickerModel, "Choose a model", []pickerItem{{id: "a", title: "a"}})
	m.mode = pickerModel
	if !strings.Contains(ansi.Strip(m.View()), "Choose a model") {
		t.Error("tiny-terminal fallback lost the picker")
	}

	m.mode = pickerSession
	if !strings.Contains(ansi.Strip(m.View()), "Choose a model") {
		t.Error("tiny-terminal fallback lost the sidebar picker")
	}

	m.mode = pickerNone
	m.showHelp = true
	if !strings.Contains(ansi.Strip(m.View()), "slash commands") {
		t.Error("tiny-terminal fallback lost the help")
	}
}
