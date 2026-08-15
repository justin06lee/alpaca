package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/muesli/termenv"
)

// findOnScreen locates a substring in the last rendered frame, returning the
// row and the cell column where it starts.
func findOnScreen(t *testing.T, m *Model, needle string) (x, y int) {
	t.Helper()
	for row, line := range strings.Split(m.lastFrame, "\n") {
		plain := ansi.Strip(line)
		if at := strings.Index(plain, needle); at >= 0 {
			return lipgloss.Width(plain[:at]), row
		}
	}
	t.Fatalf("%q is not on screen:\n%s", needle, stripANSI(m.lastFrame))
	return 0, 0
}

func selectionModel(t *testing.T) *Model {
	m := readyModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "hello there"})
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "General Kenobi. You are bold."})
	m.rebuildCache()
	m.refreshViewport(true)
	m.View() // populate lastFrame so screen coordinates exist
	return m
}

// Dragging across text highlights it and releasing copies exactly what was
// lit up.
func TestDragSelectsAndCopiesWhatIsOnScreen(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	m := selectionModel(t)
	x, y := findOnScreen(t, m, "General Kenobi.")
	last := x + lipgloss.Width("General Kenobi.") - 1

	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: last, Y: y})

	if !m.sel.active {
		t.Fatal("dragging did not activate a selection")
	}
	// The highlight is painted with a background colour, and nothing else in
	// this frame paints one — termenv's channel rounding makes the exact
	// values a moving target, so the truecolor-background introducer is the
	// stable thing to look for.
	if !strings.Contains(m.View(), ";48;2;") {
		t.Errorf("no selection background in the frame")
	}
	if got := m.selectedText(); got != "General Kenobi." {
		t.Errorf("selectedText = %q, want %q", got, "General Kenobi.")
	}

	m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: last, Y: y})
	if !strings.Contains(m.status, "copied selection") {
		t.Errorf("status = %q, want a copy confirmation", m.status)
	}
	// The highlight survives the release as proof of what was copied.
	if !m.sel.active || m.sel.dragging {
		t.Errorf("selection after release: active=%v dragging=%v, want active only", m.sel.active, m.sel.dragging)
	}
}

// A drag spanning rows takes the interior rows whole, terminal-style.
func TestMultiRowSelectionTakesWholeInteriorRows(t *testing.T) {
	m := selectionModel(t)
	x1, y1 := findOnScreen(t, m, "hello there")
	_, y2 := findOnScreen(t, m, "General Kenobi.")
	if y2 <= y1 {
		t.Fatalf("expected the reply below the bubble (rows %d and %d)", y1, y2)
	}

	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x1, Y: y1})
	m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 5, Y: y2})

	got := m.selectedText()
	if !strings.HasPrefix(got, "hello there") {
		t.Errorf("selection does not start at the anchor: %q", got)
	}
	if lines := strings.Split(got, "\n"); len(lines) != y2-y1+1 {
		t.Errorf("selection has %d lines, want %d", len(lines), y2-y1+1)
	}
	// An interior row is the bubble's border line; its full trimmed width
	// must be present, not just the anchor column onwards.
	if !strings.Contains(got, "╰") {
		t.Errorf("interior rows were not taken whole:\n%s", got)
	}
}

// Press and release in place is still a click, not a selection.
func TestClickWithoutDragStillClicks(t *testing.T) {
	m := readyModel(t)
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "```go\na := 1\n```"})
	m.rebuildCache()
	m.refreshViewport(true)
	m.viewport.GotoTop()
	m.View()

	x, y := findOnScreen(t, m, copyMarker)
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x + 1, Y: y})
	m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x + 1, Y: y})

	if !strings.Contains(m.status, "copied code block") {
		t.Errorf("status = %q, want the copy control to have fired", m.status)
	}
	if m.sel.active {
		t.Error("a plain click left a selection behind")
	}
}

// Keys and the wheel both dismiss a finished highlight.
func TestSelectionClearsOnKeyAndWheel(t *testing.T) {
	m := selectionModel(t)
	x, y := findOnScreen(t, m, "hello")

	drag := func() {
		m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
		m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 4, Y: y})
		m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x + 4, Y: y})
	}

	drag()
	if !m.sel.active {
		t.Fatal("no selection to clear")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.sel.active {
		t.Error("typing did not clear the selection")
	}

	drag()
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.sel.active {
		t.Error("scrolling did not clear the selection")
	}
}
