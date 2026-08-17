package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/justin06lee/alpaca/internal/client"
)

// Palette, taken from the opening's pixel art so the interface and the splash
// look like the same program. Warm terracotta for the animal and its replies,
// muted hay for the human — the same pasture, two different plants — so the
// two sides of a conversation separate at a glance without either shouting.
var (
	colorAccent = lipgloss.Color("#E0A177") // terracotta: alpaca itself
	colorUser   = lipgloss.Color("#B3986B") // hay: the human
	colorModel  = lipgloss.Color("#D9A283") // warm wool: the model
	colorText   = lipgloss.Color("#D8D2CB")
	colorMuted  = lipgloss.Color("#6F6459")
	colorFaint  = lipgloss.Color("#4A423B")
	colorError  = lipgloss.Color("#D98A72")
	colorWarn   = lipgloss.Color("#E0C08A")
)

var (
	styleHeader = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	styleHeaderMeta = lipgloss.NewStyle().Foreground(colorMuted)

	// The human's turn is a bubble that hugs its text on the right, the way a
	// message from you sits in any chat app.
	styleUserBubble = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorUser).
			Foreground(colorText).
			Padding(0, 1)

	styleModelLabel = lipgloss.NewStyle().
			Foreground(colorModel).
			Bold(true)

	styleMuted = lipgloss.NewStyle().Foreground(colorMuted)

	styleFaint = lipgloss.NewStyle().Foreground(colorFaint)

	styleError = lipgloss.NewStyle().Foreground(colorError)

	styleWarn = lipgloss.NewStyle().Foreground(colorWarn)

	styleStatusKey = lipgloss.NewStyle().Foreground(colorAccent)

	stylePickerTitle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	stylePickerSelected = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#2A211B")).
				Background(colorAccent).
				Bold(true)

	stylePickerDesc = lipgloss.NewStyle().Foreground(colorMuted)

	styleSystemNote = lipgloss.NewStyle().
			Foreground(colorWarn).
			Italic(true)

	styleSearchNote = lipgloss.NewStyle().Foreground(colorAccent)

	styleGreeting = lipgloss.NewStyle().Foreground(colorText)

	// The composer's frame, quiet until it has focus.
	styleComposer = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorFaint).
			Padding(0, 1)

	styleComposerActive = styleComposer.BorderForeground(colorAccent)

	// The frame turns warm yellow while the composer holds an edit, so a
	// branch-in-progress never masquerades as a fresh message.
	styleComposerEditing = styleComposer.BorderForeground(colorWarn)

	// Mouse-drag selection. Cool slate on a warm screen: the one thing that
	// is transient interface state rather than conversation, so it gets the
	// one colour nothing else uses.
	styleSelection = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E8EEF5")).
			Background(lipgloss.Color("#3D4E63"))

	// Attachment chips in the composer: quiet at rest, unmistakable when the
	// arrow keys land on one.
	styleChip        = lipgloss.NewStyle().Foreground(colorAccent)
	styleChipFocused = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#2A211B")).
				Background(colorAccent).
				Bold(true)

	// Barely there on either background, which is the point.
	styleCredit = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{
		Light: "#CDCDCD",
		Dark:  "#3A3A3A",
	})
)

// newRenderer builds a markdown renderer sized to the chat pane.
func newRenderer(width int) (*glamour.TermRenderer, error) {
	if width < 20 {
		width = 20
	}
	return glamour.NewTermRenderer(
		glamour.WithStandardStyle(markdownStyle()),
		glamour.WithWordWrap(width),
	)
}

// markdownStyle chooses a glamour theme without asking the terminal anything.
//
// glamour's auto style calls termenv.HasDarkBackground, which writes an OSC 11
// query and blocks waiting for the answer. Inside a Bubble Tea program that
// answer never arrives — Bubble Tea owns stdin and consumes it — so the call
// sits there until it times out, freezing whatever frame it landed in. It
// landed on the first token of the first reply, mid-animation.
//
// COLORFGBG is set by a good number of terminals and costs nothing to read.
// Failing that, dark is both the safer guess and the one this theme is built
// for; GLAMOUR_STYLE overrides either way.
func markdownStyle() string {
	if style := os.Getenv("GLAMOUR_STYLE"); style != "" {
		return style
	}
	if fgbg := os.Getenv("COLORFGBG"); fgbg != "" {
		fields := strings.Split(fgbg, ";")
		switch fields[len(fields)-1] {
		case "7", "15":
			return "light"
		}
	}
	return "dark"
}

// renderMarkdown formats assistant output, falling back to the raw text if the
// renderer fails — a formatting problem must never swallow the model's answer.
//
// The renderer is built on first use rather than on resize, keeping glamour's
// terminal-background query off the startup path.
func (m *Model) renderMarkdown(text string) string {
	if m.renderer == nil {
		renderer, err := newRenderer(m.contentWidth())
		if err != nil {
			return text
		}
		m.renderer = renderer
	}
	out, err := m.renderer.Render(text)
	if err != nil {
		return text
	}
	// Glamour pads generously; trimming keeps message spacing even.
	return strings.Trim(out, "\n")
}

// bubbleMax is the widest a user bubble may grow. Letting it run the full width
// would defeat the point: the right edge is what marks the turn as yours, and
// that only reads if the left edge stops short of the model's replies.
func (m *Model) bubbleMax() int {
	max := m.contentWidth() * 62 / 100
	if max < 24 {
		max = minInt(24, m.contentWidth())
	}
	return max
}

// renderMessage formats one stored message. The index is what lets a fork
// point show its variant marker.
func (m *Model) renderMessage(i int, msg client.Message) string {
	switch msg.Role {
	case client.RoleUser:
		// Width covers content plus padding, and lipgloss wraps content at
		// Width minus padding — sized to the text alone, a message exactly as
		// wide as its bubble wrapped its own last word onto a second line.
		content := collapseLongText(msg.Content)
		bubble := styleUserBubble.Width(fitWidth(content, m.bubbleMax()) + 2).Render(content)
		block := lipgloss.NewStyle().Width(m.contentWidth()).
			Align(lipgloss.Right).Render(bubble)
		// A branched prompt wears its variant marker under the bubble: the
		// star says "this one forks", the counter says where you are, and
		// clicking the bubble is where ←/→ walk the alternatives.
		if k, n := m.sess.Variants(i); n > 1 {
			tag := styleWarn.Render("✦ ") + styleMuted.Render(fmt.Sprintf("‹ %d/%d ›", k, n))
			block += "\n" + lipgloss.NewStyle().Width(m.contentWidth()).
				Align(lipgloss.Right).Render(tag)
		}
		return block

	case client.RoleAssistant:
		return m.assistantBlock(m.renderRichText(msg.Content))

	case client.RoleSystem:
		// Context injected mid-conversation (search results, for instance) is
		// worth reading, so it is rendered in full rather than truncated.
		return styleSearchNote.Render("◈ context") + "\n" + m.renderRichText(msg.Content)

	default:
		return styleMuted.Render(msg.Role + ": " + msg.Content)
	}
}

// assistantBlock renders the model's side: left-aligned under a small rail, so
// code blocks and tables get the full width they need. The blank line under
// the label keeps the model's name from crowding its own first sentence.
func (m *Model) assistantBlock(body string) string {
	return styleModelLabel.Render("▌ "+m.sess.Model) + "\n\n" + body
}

// bubbleMaxLines is how much of a message the bubble shows before folding.
// A staged paste travels inside the sent message, and fifty lines of it would
// bury the conversation it was pasted into.
const bubbleMaxLines = 8

// collapseLongText shows the head of a long message and counts the rest.
func collapseLongText(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= bubbleMaxLines {
		return s
	}
	head := strings.Join(lines[:bubbleMaxLines-1], "\n")
	folded := len(lines) - (bubbleMaxLines - 1)
	return head + "\n" + styleFaint.Render(fmt.Sprintf("… +%d more lines", folded))
}

// fitWidth is how wide a bubble needs to be: the longest line, capped. Short
// messages hug their text instead of stretching to the cap.
func fitWidth(text string, max int) int {
	widest := 0
	for _, line := range strings.Split(text, "\n") {
		if w := lipgloss.Width(line); w > widest {
			widest = w
		}
	}
	if widest > max {
		widest = max
	}
	if widest < 6 {
		widest = 6
	}
	return widest
}

// msgRange is which transcript lines one message's rendering occupies, so a
// mouse click can be traced back to the message under it.
type msgRange struct {
	start, end int // inclusive line indexes into paneLines
	msg        int // index into sess.Messages
}

// messageAt is the message whose rendering covers the given transcript line.
func (m *Model) messageAt(line int) (int, bool) {
	for _, r := range m.msgRanges {
		if line >= r.start && line <= r.end {
			return r.msg, true
		}
	}
	return 0, false
}

// conversation assembles the full chat pane content.
//
// Completed messages are rendered once and cached: re-running the markdown
// parser over the entire history on every repaint would make a long
// conversation visibly stutter while tokens stream in.
func (m *Model) conversation() string {
	var b strings.Builder

	line := 0
	if m.sess.System != "" {
		b.WriteString(styleSystemNote.Render("▌ system prompt active — /system to change"))
		b.WriteString("\n\n")
		line += 2
	}

	m.msgRanges = m.msgRanges[:0]
	for i, msg := range m.sess.Messages {
		var rendered string
		if i < len(m.rendered) {
			rendered = m.rendered[i]
		} else {
			rendered = m.renderMessage(i, msg)
		}
		b.WriteString(rendered)
		b.WriteString("\n\n")

		rows := strings.Count(rendered, "\n") + 1
		m.msgRanges = append(m.msgRanges, msgRange{start: line, end: line + rows - 1, msg: i})
		line += rows + 1 // the blank row between messages
	}

	if m.streaming {
		b.WriteString(styleModelLabel.Render("▌ " + m.sess.Model))
		b.WriteString("\n")
		for _, note := range m.streamNotes {
			b.WriteString(styleSearchNote.Render("  ⌕ " + note))
			b.WriteString("\n")
		}
		// Nothing stands in for the reply before it starts: the composer shows
		// what the model is doing, and a second indicator here would just be
		// the same news twice.
		if m.streamBuf != "" {
			// The same breathing room the finished message gets from
			// assistantBlock, so nothing shifts when the reply commits.
			b.WriteString("\n")
			// Blocks in the streaming reply are numbered after every
			// committed one, matching the order collectCodeBlocks walks.
			m.blockSeq = countCodeBlocks(m.sess.Messages)
			b.WriteString(m.renderRichText(m.streamBuf))
		}
		b.WriteString("\n\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// rebuildCache re-renders every message, used after a resize changes the wrap
// width or after the model name in the label changes.
func (m *Model) rebuildCache() {
	m.blockSeq = 0 // code blocks number from the top on a full re-render
	m.rendered = m.rendered[:0]
	for i, msg := range m.sess.Messages {
		m.rendered = append(m.rendered, m.renderMessage(i, msg))
	}
}

// cacheLast renders only the newest message.
func (m *Model) cacheLast() {
	if len(m.sess.Messages) == 0 {
		return
	}
	// Blocks in the messages being rendered continue the numbering of the
	// ones already cached.
	m.blockSeq = countCodeBlocks(m.sess.Messages[:minInt(len(m.rendered), len(m.sess.Messages))])
	idx := len(m.sess.Messages) - 1
	for len(m.rendered) < idx {
		m.rendered = append(m.rendered, m.renderMessage(len(m.rendered), m.sess.Messages[len(m.rendered)]))
	}
	m.rendered = append(m.rendered, m.renderMessage(idx, m.sess.Messages[idx]))
}

// header renders the top bar: the animal's face on the left, and — who we are
// talking to and how we got there — on the right.
func (m *Model) header() string {
	pad := strings.Repeat(" ", uiPadX)
	rows := strings.Split(renderSprite(alpacaHead), "\n")
	for i, row := range rows {
		rows[i] = pad + row
	}

	if m.client == nil {
		return strings.Join(rows, "\n")
	}

	route := m.client.Route()
	var meta string
	if route.Source == client.SourceDemo {
		// No connection exists, so reporting a transport and a latency would be
		// theatre. Say plainly what this is.
		meta = fmt.Sprintf("%s · offline demo", m.sess.Model)
	} else {
		transport := "lan"
		if route.TLS {
			transport = "tls"
		}
		meta = fmt.Sprintf("%s · %s · %s %s",
			m.sess.Model, m.profileName, transport,
			route.Latency.Round(time.Millisecond))
	}

	// The meta sits level with the middle of the face.
	right := styleHeaderMeta.Render(meta)
	mid := len(rows) / 2
	gap := m.width - uiPadX - lipgloss.Width(rows[mid]) - lipgloss.Width(right)
	if gap >= 1 {
		// Too narrow for both means the face wins: the meta lives in /stats too.
		rows[mid] += strings.Repeat(" ", gap) + right
	}
	return strings.Join(rows, "\n")
}

// statusBar renders the bottom line: transient status, or token stats.
func (m *Model) statusBar() string {
	pad := strings.Repeat(" ", uiPadX)
	avail := m.width - 2*uiPadX
	if m.status != "" {
		style := styleMuted
		if m.statusErr {
			style = styleError
		}
		return pad + style.Render(truncate(m.status, avail))
	}

	var parts []string
	switch {
	case m.streaming:
		parts = append(parts, styleWarn.Render("streaming"))
	case m.editFrom >= 0:
		parts = append(parts, styleWarn.Render("✎ editing — enter sends a new branch · esc cancels"))
	default:
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

	return pad + truncate(strings.Join(parts, styleMuted.Render(" · ")), avail)
}

// contentWidth is the usable width inside the chat pane, after the pane's
// horizontal padding on both sides.
func (m *Model) contentWidth() int {
	w := m.width - 2*uiPadX - 2
	if w < 20 {
		return 20
	}
	return w
}

// padLines indents every non-empty line by n columns.
func padLines(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}

// truncate shortens a line to max cells. It must be ANSI-aware: status text
// arrives here already styled, and slicing by rune could cut an escape
// sequence in half, spilling raw bytes into the frame.
func truncate(s string, max int) string {
	if max <= 1 || lipgloss.Width(s) <= max {
		return s
	}
	return ansi.Truncate(s, max, "…")
}
