package tui

import (
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/justin06lee/alpaca/internal/client"
)

// copyMarker is the clickable control on a code block's header line. The
// renderer and the click handler match on this exact text, so it lives in one
// place.
const copyMarker = "⧉ copy"

// mdSegment is a run of markdown: either prose, or the inside of one fenced
// code block.
type mdSegment struct {
	code bool
	lang string
	body string
}

// splitFences cuts markdown at its code fences. An unterminated fence — which
// is what a reply looks like mid-stream — is treated as code to the end, so a
// block renders as a block from its first token rather than flashing in as
// prose and reflowing.
func splitFences(text string) []mdSegment {
	var segs []mdSegment
	var buf []string
	inCode := false
	lang := ""

	flush := func() {
		body := strings.Join(buf, "\n")
		buf = buf[:0]
		if inCode {
			segs = append(segs, mdSegment{code: true, lang: lang, body: body})
		} else if strings.TrimSpace(body) != "" {
			segs = append(segs, mdSegment{body: body})
		}
	}

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		isFence := strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
		if !isFence {
			buf = append(buf, line)
			continue
		}
		flush()
		if !inCode {
			// The info string may carry more than the language; the first
			// field is the part chroma understands.
			fields := strings.Fields(strings.TrimLeft(trimmed, "`~"))
			lang = ""
			if len(fields) > 0 {
				lang = fields[0]
			}
		}
		inCode = !inCode
	}
	flush()
	return segs
}

// renderRichText renders a model-side message, framing every fenced code
// block with a header that carries the copy control. Prose still goes through
// the markdown renderer exactly as before.
func (m *Model) renderRichText(text string) string {
	segs := splitFences(text)
	if len(segs) == 0 {
		return m.renderMarkdown(text)
	}

	parts := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg.code {
			parts = append(parts, m.renderCodeBlock(seg))
		} else {
			parts = append(parts, m.renderMarkdown(seg.body))
		}
	}
	return strings.Join(parts, "\n\n")
}

// renderCodeBlock frames one block: a rule naming the language and offering
// the copy control, the highlighted code, and a closing rule.
//
//	─ go ─────────────────────────────── ⧉ copy ──
func (m *Model) renderCodeBlock(seg mdSegment) string {
	width := m.contentWidth()

	lang := seg.lang
	if lang == "" {
		lang = "code"
	}
	label := " " + lang + " "
	tail := " " + copyMarker + " "

	header := styleFaint.Render(strings.Repeat("─", maxInt(1, width)))
	if rule := width - 3 - lipgloss.Width(label) - lipgloss.Width(tail); rule >= 1 {
		header = styleFaint.Render("─") + styleMuted.Render(label) +
			styleFaint.Render(strings.Repeat("─", rule)) +
			styleStatusKey.Render(tail) + styleFaint.Render("──")
	}

	// The fence goes back around the body so glamour applies its syntax
	// highlighting; the frame drawn here is only the furniture around it.
	body := m.renderMarkdown("```" + seg.lang + "\n" + seg.body + "\n```")
	footer := styleFaint.Render(strings.Repeat("─", maxInt(1, width)))

	// Glamour leaves a padded blank line under the header; the extra newline
	// gives the footer the same breathing room so the frame reads symmetric.
	return header + "\n" + body + "\n\n" + footer
}

// collectCodeBlocks lists every fenced block the transcript shows, in the
// order their headers appear — messages first, then the streaming reply. The
// click handler counts header lines to index into this list, so the walk here
// must mirror what conversation() renders.
func (m *Model) collectCodeBlocks() []mdSegment {
	var out []mdSegment
	appendFrom := func(text string) {
		for _, seg := range splitFences(text) {
			if seg.code {
				out = append(out, seg)
			}
		}
	}
	for _, msg := range m.sess.Messages {
		if msg.Role == client.RoleAssistant || msg.Role == client.RoleSystem {
			appendFrom(msg.Content)
		}
	}
	if m.streaming && m.streamBuf != "" {
		appendFrom(m.streamBuf)
	}
	return out
}

// copyCodeBlock puts the idx-th block on the clipboard.
func (m *Model) copyCodeBlock(idx int) tea.Cmd {
	blocks := m.collectCodeBlocks()
	if idx < 0 || idx >= len(blocks) {
		return m.setStatus("no code block there", true)
	}
	body := blocks[idx].body
	if err := clipboard.WriteAll(body); err != nil {
		return m.setStatus("clipboard unavailable: "+err.Error(), true)
	}
	lines := strings.Count(body, "\n") + 1
	return m.setStatus(fmt.Sprintf("copied code block %d of %d · %d lines", idx+1, len(blocks), lines), false)
}

// copyLastCodeBlock is the keyboard path: the newest block is almost always
// the one just generated, which is the one worth a shortcut.
func (m *Model) copyLastCodeBlock() tea.Cmd {
	blocks := m.collectCodeBlocks()
	if len(blocks) == 0 {
		return m.setStatus("no code blocks yet", false)
	}
	return m.copyCodeBlock(len(blocks) - 1)
}

// clickTranscript maps a left click to the transcript line under it and, when
// that line is a code header's copy control, copies the block it belongs to.
func (m *Model) clickTranscript(x, y int) tea.Cmd {
	row := y - headerHeight
	if row < 0 || row >= m.viewport.Height {
		return nil
	}
	idx := m.viewport.YOffset + row
	if idx < 0 || idx >= len(m.paneLines) {
		return nil
	}

	line := ansi.Strip(m.paneLines[idx])
	if !isCodeHeader(line) {
		return nil
	}
	// Only the control itself is clickable, with a cell of slack either side;
	// a click on the rest of the rule is someone selecting text.
	at := strings.Index(line, copyMarker)
	col := lipgloss.Width(line[:at])
	if x < col-1 || x > col+lipgloss.Width(copyMarker)+1 {
		return nil
	}

	which := 0
	for i := 0; i < idx; i++ {
		if isCodeHeader(ansi.Strip(m.paneLines[i])) {
			which++
		}
	}
	return m.copyCodeBlock(which)
}

// isCodeHeader recognises a rendered code block header. Requiring the leading
// rule keeps a literal "⧉ copy" inside someone's message from miscounting the
// blocks: transcript text never starts a line with the rule character.
func isCodeHeader(stripped string) bool {
	return strings.HasPrefix(stripped, "─") && strings.Contains(stripped, copyMarker)
}
