package tui

import (
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// selection is a mouse drag over the screen, measured in cells of the final
// rendered frame rather than in transcript lines. Screen space is what makes
// one mechanism cover everything — the chat, the composer, even an open
// popup — and it keeps a guarantee the user can feel: the copy is exactly
// the text that was lit up when the button was released.
type selection struct {
	// dragging is true while the button is down; active becomes true the
	// moment the drag leaves its anchor cell, which is also what separates a
	// selection from a plain click.
	dragging bool
	active   bool
	ax, ay   int // anchor: where the button went down
	ex, ey   int // end: where the pointer is now
}

// ordered returns the selection's corners with the start before the end,
// reading order, whichever direction the drag travelled.
func (s selection) ordered() (x0, y0, x1, y1 int) {
	if s.ay < s.ey || (s.ay == s.ey && s.ax <= s.ex) {
		return s.ax, s.ay, s.ex, s.ey
	}
	return s.ex, s.ey, s.ax, s.ay
}

// span is the highlighted cell range on one row: terminal-style linear
// selection, where interior rows light up whole.
func (s selection) span(y, width int) (from, to int, ok bool) {
	x0, y0, x1, y1 := s.ordered()
	if y < y0 || y > y1 {
		return 0, 0, false
	}
	from, to = 0, width
	if y == y0 {
		from = x0
	}
	if y == y1 {
		to = x1 + 1 // the cell under the pointer is included
	}
	if to > width {
		to = width
	}
	if from >= to {
		return 0, 0, false
	}
	return from, to, true
}

// applySelection paints the highlight onto the finished frame.
func (m *Model) applySelection(frame string) string {
	if !m.sel.active {
		return frame
	}
	rows := strings.Split(frame, "\n")
	_, y0, _, y1 := m.sel.ordered()
	for y := maxInt(0, y0); y <= y1 && y < len(rows); y++ {
		rows[y] = m.highlightRow(rows[y], y)
	}
	return strings.Join(rows, "\n")
}

// highlightRow repaints one row's selected cells: the underlying text is cut
// out ANSI-aware, restyled flat, and spliced back, the same surgery the
// popup overlay uses. Original colours inside the span give way to the
// highlight — which is what selection looks like everywhere else.
func (m *Model) highlightRow(row string, y int) string {
	from, to, ok := m.sel.span(y, lipgloss.Width(row))
	if !ok {
		return row
	}

	left := ansi.Truncate(row, from, "")
	mid := ansi.Strip(ansi.Truncate(ansi.TruncateLeft(row, from, ""), to-from, ""))
	right := ansi.TruncateLeft(row, to, "")

	return closeStyle(left) + styleSelection.Render(mid) + right
}

// selectedText reads the highlighted cells back out of the last frame drawn,
// one line per row, padding trimmed.
func (m *Model) selectedText() string {
	rows := strings.Split(m.lastFrame, "\n")
	_, y0, _, y1 := m.sel.ordered()

	var out []string
	for y := maxInt(0, y0); y <= y1 && y < len(rows); y++ {
		from, to, ok := m.sel.span(y, lipgloss.Width(rows[y]))
		if !ok {
			out = append(out, "")
			continue
		}
		seg := ansi.Strip(ansi.Truncate(ansi.TruncateLeft(rows[y], from, ""), to-from, ""))
		out = append(out, strings.TrimRight(seg, " "))
	}
	return strings.Join(out, "\n")
}

// copySelection puts the highlighted text on the clipboard as the drag ends.
// The highlight stays visible afterwards — proof of what was taken — until
// the next click, keypress, or scroll clears it.
func (m *Model) copySelection() tea.Cmd {
	text := m.selectedText()
	if strings.TrimSpace(text) == "" {
		m.sel = selection{}
		return nil
	}
	if err := clipboard.WriteAll(text); err != nil {
		return m.setStatus("clipboard unavailable: "+err.Error(), true)
	}
	if lines := strings.Count(text, "\n") + 1; lines > 1 {
		return m.setStatus(fmt.Sprintf("copied selection · %d lines", lines), false)
	}
	return m.setStatus(fmt.Sprintf("copied selection · %d chars", len(text)), false)
}

// clearSelection drops any highlight; cheap enough to call on every event
// that invalidates screen coordinates.
func (m *Model) clearSelection() {
	m.sel = selection{}
}
