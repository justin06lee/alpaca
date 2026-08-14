package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/justin06lee/alpaca/internal/client"
)

func TestSplitFencesSeparatesProseAndCode(t *testing.T) {
	text := "Intro prose.\n\n```go\nfunc A() {}\n```\n\nMiddle.\n\n~~~python\nprint(1)\n~~~\n\nOutro."
	segs := splitFences(text)

	var kinds []string
	for _, s := range segs {
		if s.code {
			kinds = append(kinds, "code:"+s.lang)
		} else {
			kinds = append(kinds, "prose")
		}
	}
	want := []string{"prose", "code:go", "prose", "code:python", "prose"}
	if strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Fatalf("segments = %v, want %v", kinds, want)
	}
	if segs[1].body != "func A() {}" {
		t.Errorf("code body = %q", segs[1].body)
	}
}

// A reply mid-stream has an opened fence and no closer yet; it must render as
// code from the first token rather than as prose that reflows later.
func TestSplitFencesTreatsUnterminatedFenceAsCode(t *testing.T) {
	segs := splitFences("Look:\n```rust\nfn main() {")
	if len(segs) != 2 || !segs[1].code || segs[1].lang != "rust" {
		t.Fatalf("segments = %+v, want prose then open rust code", segs)
	}
	if segs[1].body != "fn main() {" {
		t.Errorf("code body = %q", segs[1].body)
	}
}

func TestCodeBlocksRenderWithACopyHeader(t *testing.T) {
	m := readyModel(t)
	out := stripANSI(m.renderRichText("Here:\n\n```go\nfmt.Println(1)\n```\n\nDone."))

	if !strings.Contains(out, copyMarker) {
		t.Errorf("no copy control in the rendered block:\n%s", out)
	}
	if !strings.Contains(out, "─ go ") {
		t.Errorf("header does not name the language:\n%s", out)
	}
	if strings.Contains(out, "```") {
		t.Errorf("raw fences leaked into the render:\n%s", out)
	}
	if !strings.Contains(out, "fmt.Println") {
		t.Errorf("code body missing:\n%s", out)
	}
}

// The click handler indexes blocks by counting header lines; the registry and
// the rendered transcript must therefore agree on how many blocks there are.
func TestCodeHeadersMatchTheCollectedBlocks(t *testing.T) {
	m := readyModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "two snippets please — and a decoy ⧉ copy"})
	m.sess.Append(client.Message{Role: client.RoleAssistant,
		Content: "First:\n```go\na := 1\n```\nSecond:\n```sh\nls -la\n```"})
	m.rebuildCache()
	m.refreshViewport(true)

	headers := 0
	for _, line := range m.paneLines {
		if isCodeHeader(ansi.Strip(line)) {
			headers++
		}
	}
	blocks := m.collectCodeBlocks()

	if headers != 2 || len(blocks) != 2 {
		t.Fatalf("headers = %d, registry = %d, want 2 and 2", headers, len(blocks))
	}
	if blocks[0].body != "a := 1" || blocks[1].body != "ls -la" {
		t.Errorf("registry out of order: %+v", blocks)
	}
}

// Clicks land only on the control, not the rest of the rule.
func TestClickTranscriptHitsOnlyTheCopyControl(t *testing.T) {
	m := readyModel(t)
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "```go\na := 1\n```"})
	m.rebuildCache()
	m.refreshViewport(true)
	m.viewport.GotoTop()

	headerIdx := -1
	var headerLine string
	for i, line := range m.paneLines {
		if isCodeHeader(ansi.Strip(line)) {
			headerIdx = i
			headerLine = ansi.Strip(line)
			break
		}
	}
	if headerIdx < 0 {
		t.Fatalf("no code header in the pane:\n%s", strings.Join(m.paneLines, "\n"))
	}
	if headerIdx >= m.viewport.Height {
		t.Fatalf("header at line %d is off the first screen (pane %d rows)", headerIdx, m.viewport.Height)
	}

	y := headerHeight + headerIdx
	// The far left of the rule is not the control.
	if cmd := m.clickTranscript(0, y); cmd != nil {
		t.Error("a click on the rule, away from the control, copied something")
	}
	// A row that is not a header is inert wherever it is clicked.
	if cmd := m.clickTranscript(0, y+1); cmd != nil {
		t.Error("a click on a non-header line copied something")
	}
	// Dead on the control copies.
	col := ansi.StringWidth(headerLine[:strings.Index(headerLine, copyMarker)])
	if cmd := m.clickTranscript(col+1, y); cmd == nil {
		t.Error("a click on the copy control did nothing")
	} else {
		cmd()
	}
	if !strings.Contains(m.status, "copied code block") {
		t.Errorf("status = %q, want a copy confirmation", m.status)
	}
}

// findHeader returns the pane line index of the first code header.
func findHeader(t *testing.T, m *Model) (int, string) {
	t.Helper()
	for i, line := range m.paneLines {
		if s := ansi.Strip(line); isCodeHeader(s) {
			return i, s
		}
	}
	t.Fatalf("no code header in the pane:\n%s", strings.Join(m.paneLines, "\n"))
	return 0, ""
}

// After a copy the control reads "copied!", stays countable as a header so
// click indexes elsewhere keep working, and reverts when the flash expires.
func TestCopyControlFlashesCopied(t *testing.T) {
	m := readyModel(t)
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "```go\na := 1\n```"})
	m.rebuildCache()
	m.refreshViewport(true)
	m.viewport.GotoTop()

	idx, header := findHeader(t, m)
	col := ansi.StringWidth(header[:strings.Index(header, copyMarker)])
	m.clickTranscript(col+1, headerHeight+idx)

	_, flashed := findHeader(t, m)
	if !strings.Contains(flashed, copiedMarker) {
		t.Fatalf("control did not flash after the copy: %q", flashed)
	}
	if !isCodeHeader(flashed) {
		t.Error("a flashed header no longer counts as a header — click indexes would shift")
	}
	// Clicking the flashed control does nothing rather than re-copying.
	if cmd := m.clickTranscript(col+1, headerHeight+idx); cmd != nil {
		t.Error("a click on the flashed control did something")
	}

	// The expiry for this copy reverts it; a stale one must not.
	m.Update(copiedExpiredMsg(m.copiedSeq - 1))
	if _, h := findHeader(t, m); !strings.Contains(h, copiedMarker) {
		t.Error("a stale expiry timer reverted the flash")
	}
	m.Update(copiedExpiredMsg(m.copiedSeq))
	if _, h := findHeader(t, m); !strings.Contains(h, copyMarker) {
		t.Errorf("control did not revert after the flash: %q", h)
	}
}

// Clicking a sent bubble opens the full message in a popup; clicking the
// model's side, or the padding beside a bubble, does not.
func TestClickBubbleOpensTheFullMessage(t *testing.T) {
	m := readyModel(t)
	m.sess.System = "be brief" // the system note shifts every line down two
	long := strings.Repeat("row\n", 30) + "tail-needle"
	m.sess.Append(client.Message{Role: client.RoleUser, Content: long})
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "a reply"})
	m.rebuildCache()
	m.refreshViewport(true)
	m.viewport.GotoTop()

	bubbleRow := -1
	var bubbleLine string
	for i, line := range m.paneLines {
		if s := ansi.Strip(line); strings.Contains(s, "╭") {
			bubbleRow, bubbleLine = i, s
			break
		}
	}
	if bubbleRow < 0 {
		t.Fatalf("no bubble in the pane:\n%s", strings.Join(m.paneLines, "\n"))
	}

	// The empty space left of the right-aligned bubble is not the bubble.
	m.clickTranscript(0, headerHeight+bubbleRow)
	if m.viewMsg != -1 {
		t.Fatal("a click beside the bubble opened the popup")
	}

	left := len(bubbleLine) - len(strings.TrimLeft(bubbleLine, " "))
	m.clickTranscript(left+2, headerHeight+bubbleRow)
	if m.viewMsg != 0 {
		t.Fatalf("viewMsg = %d after clicking the bubble, want 0", m.viewMsg)
	}

	view := stripANSI(m.View())
	if !strings.Contains(view, "your message") || !strings.Contains(view, "of 31") {
		t.Errorf("popup title wrong:\n%s", view)
	}
	// The folded tail is reachable by scrolling.
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if !strings.Contains(stripANSI(m.View()), "tail-needle") {
		t.Error("scrolling never reached the folded tail")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewMsg != -1 {
		t.Error("esc did not close the popup")
	}

	// The assistant's side is inert.
	for i, line := range m.paneLines {
		if strings.Contains(ansi.Strip(line), "a reply") {
			if i < m.viewport.Height {
				m.clickTranscript(2, headerHeight+i)
			}
			break
		}
	}
	if m.viewMsg != -1 {
		t.Error("clicking the reply opened a popup")
	}
}
