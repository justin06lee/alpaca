package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// overlay draws panel on top of base with the panel's top-left corner at
// (x, y), both measured in cells. The base stays visible above, below, and to
// either side, which is what makes a popup read as a popup rather than a
// scene change.
func overlay(base, panel string, x, y int) string {
	baseLines := strings.Split(base, "\n")
	for i, panelLine := range strings.Split(panel, "\n") {
		row := y + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLines[row] = spliceRow(baseLines[row], panelLine, x)
	}
	return strings.Join(baseLines, "\n")
}

// spliceRow overwrites the cells of row from x for the width of insert.
// Cutting a styled row must be ANSI-aware — slicing bytes could split an
// escape sequence — and each retained side is closed with a reset so a style
// that was open at the cut cannot bleed into what follows it.
func spliceRow(row, insert string, x int) string {
	width := lipgloss.Width(insert)

	left := ansi.Truncate(row, x, "")
	if pad := x - lipgloss.Width(left); pad > 0 {
		left += strings.Repeat(" ", pad)
	}
	right := ansi.TruncateLeft(row, x+width, "")

	return closeStyle(left) + closeStyle(insert) + right
}

// closeStyle appends an SGR reset when the segment carries escape sequences,
// so an unterminated style stops at the segment's edge.
func closeStyle(s string) string {
	if strings.Contains(s, "\x1b") {
		return s + "\x1b[0m"
	}
	return s
}
