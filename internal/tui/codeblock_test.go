package tui

import (
	"strings"
	"testing"

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
