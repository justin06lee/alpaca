package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/justin06lee/alpaca/internal/client"
)

// Palette. Colours are ANSI 256 so they adapt to whatever theme the terminal
// already uses, rather than fighting it with hard-coded hex values.
var (
	colorAccent = lipgloss.Color("39")  // cyan-blue: alpaca itself
	colorUser   = lipgloss.Color("213") // pink: the human
	colorModel  = lipgloss.Color("78")  // green: the model
	colorMuted  = lipgloss.Color("245")
	colorError  = lipgloss.Color("203")
	colorWarn   = lipgloss.Color("221")
)

var (
	styleHeader = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	styleHeaderMeta = lipgloss.NewStyle().Foreground(colorMuted)

	styleUserLabel = lipgloss.NewStyle().
			Foreground(colorUser).
			Bold(true)

	styleModelLabel = lipgloss.NewStyle().
			Foreground(colorModel).
			Bold(true)

	styleUserText = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	styleMuted = lipgloss.NewStyle().Foreground(colorMuted)

	styleError = lipgloss.NewStyle().Foreground(colorError)

	styleWarn = lipgloss.NewStyle().Foreground(colorWarn)

	styleStatusKey = lipgloss.NewStyle().Foreground(colorAccent)

	stylePickerTitle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	stylePickerSelected = lipgloss.NewStyle().
				Foreground(lipgloss.Color("231")).
				Background(lipgloss.Color("24")).
				Bold(true)

	stylePickerDesc = lipgloss.NewStyle().Foreground(colorMuted)

	styleSystemNote = lipgloss.NewStyle().
			Foreground(colorWarn).
			Italic(true)
)

// newRenderer builds a markdown renderer sized to the chat pane.
func newRenderer(width int) (*glamour.TermRenderer, error) {
	if width < 20 {
		width = 20
	}
	return glamour.NewTermRenderer(
		// AutoStyle picks a light or dark theme from the terminal's own
		// background, so the output does not clash with the user's setup.
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
}

// renderMarkdown formats assistant output, falling back to the raw text if the
// renderer fails — a formatting problem must never swallow the model's answer.
func (m *Model) renderMarkdown(text string) string {
	if m.renderer == nil {
		return text
	}
	out, err := m.renderer.Render(text)
	if err != nil {
		return text
	}
	// Glamour pads generously; trimming keeps message spacing even.
	return strings.Trim(out, "\n")
}

// renderMessage formats one stored message.
func (m *Model) renderMessage(msg client.Message) string {
	switch msg.Role {
	case client.RoleUser:
		label := styleUserLabel.Render("▌ you")
		body := styleUserText.Render(indentPlain(msg.Content, m.contentWidth()))
		return label + "\n" + body

	case client.RoleAssistant:
		label := styleModelLabel.Render("▌ " + m.sess.Model)
		return label + "\n" + m.renderMarkdown(msg.Content)

	case client.RoleSystem:
		return styleSystemNote.Render("▌ system: " + truncate(msg.Content, 200))

	default:
		return styleMuted.Render(msg.Role + ": " + msg.Content)
	}
}

// indentPlain wraps user text without markdown processing, since what the user
// typed should be shown back verbatim.
func indentPlain(text string, width int) string {
	if width < 10 {
		width = 10
	}
	var out strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString(lipgloss.NewStyle().Width(width).Render(line))
	}
	return out.String()
}

// conversation assembles the full chat pane content.
//
// Completed messages are rendered once and cached: re-running the markdown
// parser over the entire history on every repaint would make a long
// conversation visibly stutter while tokens stream in.
func (m *Model) conversation() string {
	var b strings.Builder

	if m.sess.System != "" {
		b.WriteString(styleSystemNote.Render("▌ system prompt active — /system to change"))
		b.WriteString("\n\n")
	}

	for i, msg := range m.sess.Messages {
		if i < len(m.rendered) {
			b.WriteString(m.rendered[i])
		} else {
			b.WriteString(m.renderMessage(msg))
		}
		b.WriteString("\n\n")
	}

	if m.streaming {
		b.WriteString(styleModelLabel.Render("▌ " + m.sess.Model))
		b.WriteString("\n")
		if m.streamBuf == "" {
			b.WriteString(styleMuted.Render(m.spinner.View() + " thinking…"))
		} else {
			b.WriteString(m.renderMarkdown(m.streamBuf))
		}
		b.WriteString("\n\n")
	}

	if len(m.sess.Messages) == 0 && !m.streaming {
		b.WriteString(m.welcome())
	}

	return strings.TrimRight(b.String(), "\n")
}

// rebuildCache re-renders every message, used after a resize changes the wrap
// width or after the model name in the label changes.
func (m *Model) rebuildCache() {
	m.rendered = m.rendered[:0]
	for _, msg := range m.sess.Messages {
		m.rendered = append(m.rendered, m.renderMessage(msg))
	}
}

// cacheLast renders only the newest message.
func (m *Model) cacheLast() {
	if len(m.sess.Messages) == 0 {
		return
	}
	idx := len(m.sess.Messages) - 1
	for len(m.rendered) < idx {
		m.rendered = append(m.rendered, m.renderMessage(m.sess.Messages[len(m.rendered)]))
	}
	m.rendered = append(m.rendered, m.renderMessage(m.sess.Messages[idx]))
}

// cell renders a key and its description padded to a fixed column width.
//
// The padding is computed from the unstyled lengths: styled strings carry ANSI
// escapes, so a %-*s verb would count those invisible bytes and misalign every
// column.
func cell(key, desc string, keyWidth, descWidth int) string {
	pad := func(n int) string {
		if n < 1 {
			n = 1
		}
		return strings.Repeat(" ", n)
	}
	return styleStatusKey.Render(key) + pad(keyWidth-len(key)) +
		styleMuted.Render(desc) + pad(descWidth-len(desc))
}

func (m *Model) welcome() string {
	const keyCol, descCol = 9, 18

	lines := []string{
		styleMuted.Render("Type a message and press ") + styleStatusKey.Render("enter") +
			styleMuted.Render(" to send."),
		"",
		"  " + cell("ctrl+j", "newline", keyCol, descCol) + cell("ctrl+p", "switch model", keyCol, 0),
		"  " + cell("esc", "stop generating", keyCol, descCol) + cell("ctrl+s", "saved chats", keyCol, 0),
		"  " + cell("ctrl+n", "new chat", keyCol, descCol) + cell("?", "all keys", keyCol, 0),
		"",
		styleMuted.Render("Slash commands: /model /new /sessions /system /retry /copy /help"),
	}
	return strings.Join(lines, "\n")
}

// header renders the top bar: who we are talking to and how we got there.
func (m *Model) header() string {
	left := styleHeader.Render("alpaca")

	route := m.client.Route()
	transport := "lan"
	if route.TLS {
		transport = "tls"
	}

	meta := fmt.Sprintf("%s · %s · %s %s",
		m.sess.Model,
		m.profileName,
		transport,
		route.Latency.Round(time.Millisecond),
	)

	right := styleHeaderMeta.Render(meta)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		// Too narrow for both: the model name matters more than the route.
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// statusBar renders the bottom line: transient status, or token stats.
func (m *Model) statusBar() string {
	if m.status != "" {
		style := styleMuted
		if m.statusErr {
			style = styleError
		}
		return style.Render(truncate(m.status, m.width))
	}

	var parts []string
	if m.streaming {
		parts = append(parts, styleWarn.Render("streaming"), styleMuted.Render("esc to stop"))
	} else {
		parts = append(parts, styleMuted.Render(fmt.Sprintf("%d messages", len(m.sess.Messages))))
	}

	if m.lastUsage != nil {
		parts = append(parts, styleMuted.Render(fmt.Sprintf("%d→%d tokens",
			m.lastUsage.PromptTokens, m.lastUsage.CompletionTokens)))
		if m.lastElapsed > 0 && m.lastUsage.CompletionTokens > 0 {
			rate := float64(m.lastUsage.CompletionTokens) / m.lastElapsed.Seconds()
			parts = append(parts, styleMuted.Render(fmt.Sprintf("%.1f tok/s", rate)))
		}
	}

	return truncate(strings.Join(parts, styleMuted.Render(" · ")), m.width)
}

// contentWidth is the usable width inside the chat pane.
func (m *Model) contentWidth() int {
	w := m.width - 2
	if w < 20 {
		return 20
	}
	return w
}

func truncate(s string, max int) string {
	if max <= 1 || lipgloss.Width(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) > max-1 {
		runes = runes[:max-1]
	}
	return string(runes) + "…"
}
