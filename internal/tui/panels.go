package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// stylePanel frames a floating overlay, borrowing the active composer's accent
// so popups read as the same furniture as the rest of the interface.
var stylePanel = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colorAccent).
	Padding(0, 1)

// panel wraps content in a floating box with the given outer width. Width on
// the style covers content plus padding, and the border adds two more, so the
// content area is outerWidth-4.
func panel(content string, outerWidth int) string {
	return stylePanel.Width(outerWidth - 2).Render(content)
}

// modelPopover floats the model picker over the conversation instead of
// replacing it: choosing a model is a small decision, and the chat underneath
// is the context it is made in.
func (m *Model) modelPopover(base string) string {
	outer := clampInt(m.width*60/100, 46, 72)
	if m.width < outer+4 || m.height < 14 {
		return m.pick.view(m.width, m.height) // no room to float; take the screen
	}
	inner := outer - 4
	rows := clampInt(m.height-11, 3, 12)

	lines := []string{m.pick.headerLine(inner), ""}
	lines = append(lines, m.pick.itemLines(inner, rows)...)
	lines = append(lines, "", m.pick.footerLine(inner))

	popup := panel(strings.Join(lines, "\n"), outer)
	x := (m.width - outer) / 2
	y := maxInt(2, (m.height-(len(lines)+2))/2)
	return overlay(base, popup, x, y)
}

// sessionSidebar slides the saved-chat list in from the left edge, leaving the
// conversation visible beside it the way a drawer would.
func (m *Model) sessionSidebar(base string) string {
	outer := clampInt(m.width*38/100, 34, 48)
	if m.width < outer+20 || m.height < 12 {
		return m.pick.view(m.width, m.height)
	}
	inner := outer - 4
	// Full height between the header and the status bar.
	height := m.height - 3
	contentRows := height - 2 // inside the border

	// Title, blank, items at two rows each, then a footer pinned to the bottom.
	perScreen := maxInt(1, (contentRows-4)/2)
	lines := []string{m.pick.headerLine(inner), ""}
	lines = append(lines, m.pick.sidebarItemLines(inner, perScreen)...)
	if len(lines) > contentRows-2 {
		lines = lines[:contentRows-2]
	}
	for len(lines) < contentRows-1 {
		lines = append(lines, "")
	}
	lines = append(lines, m.pick.footerLine(inner))

	sidebar := panel(strings.Join(lines, "\n"), outer)
	return overlay(base, sidebar, 0, headerHeight)
}

// helpOverlay floats the key reference over whatever the user was looking at,
// falling back to the whole screen when the terminal is too small for a panel.
func (m *Model) helpOverlay(base string) string {
	content := helpContent()
	rows := strings.Count(content, "\n") + 1
	outer := minInt(m.width-8, 68)

	if m.width < 56 || m.height < rows+4 {
		return lipgloss.NewStyle().Width(m.width).Render(content)
	}

	popup := panel(content, outer)
	x := (m.width - outer) / 2
	y := maxInt(1, (m.height-(rows+2))/2)
	return overlay(base, popup, x, y)
}

func helpContent() string {
	var b strings.Builder
	b.WriteString(stylePickerTitle.Render("keys"))
	b.WriteString("\n\n")
	for _, row := range [][2]string{
		{"enter", "send the message"},
		{"ctrl+j", "insert a newline (see README for shift+enter)"},
		{"esc", "stop generating, or clear the composer"},
		{"ctrl+c", "quit"},
		{"", ""},
		{"ctrl+p", "switch model"},
		{"ctrl+n", "new chat"},
		{"ctrl+s", "browse saved chats"},
		{"ctrl+r", "regenerate the last reply"},
		{"ctrl+y", "copy the last reply"},
		{"", ""},
		{"wheel · pgup/pgdn", "scroll the transcript"},
		{"ctrl+u/ctrl+d", "scroll half a page"},
		{"?", "toggle this help"},
	} {
		if row[0] == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(styleStatusKey.Render(fmt.Sprintf("%-18s", row[0])) + styleMuted.Render(row[1]))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(stylePickerTitle.Render("slash commands"))
	b.WriteString("\n\n")
	for _, row := range [][2]string{
		{"/model [name]", "switch model, or open the picker"},
		{"/new", "start a new chat"},
		{"/sessions", "browse saved chats"},
		{"/system <text>", "set the system prompt (empty to clear)"},
		{"/retry", "regenerate the last reply"},
		{"/copy", "copy the last reply to the clipboard"},
		{"/clear", "delete this chat's messages"},
		{"/search <query>", "search the web and add the results here"},
		{"/stats", "show connection and token details"},
		{"/help", "this screen"},
		{"/quit", "exit"},
	} {
		b.WriteString(styleStatusKey.Render(fmt.Sprintf("%-18s", row[0])) + styleMuted.Render(row[1]))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleFaint.Render("any key to close"))
	return b.String()
}
