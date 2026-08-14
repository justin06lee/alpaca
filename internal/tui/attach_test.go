package tui

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/muesli/termenv"
)

// pasteKey builds the KeyMsg a bracketed paste produces.
func pasteKey(text string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true}
}

func readyModel(t *testing.T) *Model {
	m := newTestModel(t)
	m.splashDone = true
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// ---------------------------------------------------------------------------
// Composer growth
// ---------------------------------------------------------------------------

// The box grows with its content up to the cap, then stops and scrolls.
func TestComposerGrowsWithContentUpToTheCap(t *testing.T) {
	m := readyModel(t)

	rows := func() int { return len(strings.Split(m.renderComposer(80), "\n")) }

	if got := rows(); got != composerRows {
		t.Fatalf("empty composer is %d rows, want %d", got, composerRows)
	}

	m.input.SetValue("one\ntwo\nthree\nfour\nfive")
	if got := rows(); got != 5+2 {
		t.Errorf("five lines render as %d rows, want %d", got, 5+2)
	}

	m.input.SetValue(strings.Repeat("line\n", 40))
	if got, want := rows(), m.composerMaxRows()+2; got != want {
		t.Errorf("forty lines render as %d rows, want the cap %d", got, want)
	}
}

// Newlines inserted via ctrl+j used to leave the cursor below the visible
// rows: InsertString bypasses the textarea's view repositioning. The text at
// the cursor must stay on screen no matter how far down it is.
func TestComposerKeepsTheCursorVisiblePastTheCap(t *testing.T) {
	m := readyModel(t)

	for i := 0; i < 25; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	}
	for _, r := range "END" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if !strings.Contains(ansi.Strip(m.renderComposer(80)), "END") {
		t.Error("the line being typed is not visible in the composer")
	}
}

// ---------------------------------------------------------------------------
// Paste staging
// ---------------------------------------------------------------------------

func TestSmallPasteLandsInTheComposer(t *testing.T) {
	m := readyModel(t)
	m.Update(pasteKey("just a phrase"))

	if m.input.Value() != "just a phrase" {
		t.Errorf("composer = %q, want the pasted text", m.input.Value())
	}
	if len(m.attachments) != 0 {
		t.Errorf("%d attachments staged for a small paste, want 0", len(m.attachments))
	}
}

// Pasted carriage returns must become newlines, not literal control bytes.
func TestPasteNormalisesCarriageReturns(t *testing.T) {
	m := readyModel(t)
	m.Update(pasteKey("one\r\ntwo"))

	if got := m.input.Value(); got != "one\ntwo" {
		t.Errorf("composer = %q, want carriage returns normalised", got)
	}
}

func TestBigPasteBecomesAChip(t *testing.T) {
	m := readyModel(t)
	paste := strings.Repeat("a line of pasted text\n", 41) + "tail"
	m.Update(pasteKey(paste))

	if m.input.Value() != "" {
		t.Errorf("composer = %q, want the paste kept out of it", m.input.Value())
	}
	if len(m.attachments) != 1 {
		t.Fatalf("%d attachments, want 1", len(m.attachments))
	}
	if got := m.attachments[0].lines; got != 42 {
		t.Errorf("chip counts %d lines, want 42", got)
	}

	// The chip is visible in the composer frame.
	box := ansi.Strip(m.renderComposer(90))
	if !strings.Contains(box, "42 lines pasted") {
		t.Errorf("composer does not show the chip:\n%s", box)
	}
}

func TestChipFocusViewAndDelete(t *testing.T) {
	m := readyModel(t)
	m.Update(pasteKey(strings.Repeat("needle in the paste\n", 10)))

	// Up from the (empty) composer focuses the chip.
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.attachFocus != 0 {
		t.Fatalf("attachFocus = %d after ↑, want 0", m.attachFocus)
	}

	// Enter opens the popup with the full content.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.viewAttach != 0 {
		t.Fatalf("viewAttach = %d after enter, want 0", m.viewAttach)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "needle in the paste") {
		t.Errorf("popup does not show the pasted content:\n%s", view)
	}
	if !strings.Contains(view, "paste #1") {
		t.Errorf("popup has no title:\n%s", view)
	}

	// Esc closes the popup, focus is still on the chip, backspace removes it.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewAttach != -1 {
		t.Fatalf("popup still open after esc")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if len(m.attachments) != 0 || m.attachFocus != -1 {
		t.Errorf("chip not removed: %d attachments, focus %d", len(m.attachments), m.attachFocus)
	}
}

// Typing while a chip is focused hands the key straight back to the composer.
func TestTypingLeavesChipFocus(t *testing.T) {
	m := readyModel(t)
	m.Update(pasteKey(strings.Repeat("x\n", 10)))
	m.Update(tea.KeyMsg{Type: tea.KeyUp})

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})

	if m.attachFocus != -1 {
		t.Errorf("attachFocus = %d after typing, want -1", m.attachFocus)
	}
	if m.input.Value() != "h" {
		t.Errorf("composer = %q, want the typed rune", m.input.Value())
	}
}

func TestSendFoldsAttachmentsIntoTheMessage(t *testing.T) {
	// A real (stub) client: send() starts a stream, and a nil client would
	// panic in the reader goroutine.
	m := newRenderedModel(t)
	paste := strings.Repeat("pasted line\n", 9) + "pasted tail"
	m.Update(pasteKey(paste))
	m.input.SetValue("explain this")

	m.send()

	if len(m.sess.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(m.sess.Messages))
	}
	got := m.sess.Messages[0].Content
	if !strings.HasPrefix(got, "explain this\n\n") {
		t.Errorf("typed text does not lead the message: %q", got)
	}
	if !strings.Contains(got, "pasted tail") {
		t.Errorf("paste content missing from the sent message: %q", got)
	}
	if len(m.attachments) != 0 {
		t.Errorf("%d attachments left after send, want 0", len(m.attachments))
	}
}

// A long sent message folds in the transcript instead of burying it.
func TestUserBubbleFoldsLongMessages(t *testing.T) {
	m := readyModel(t)
	bubble := stripANSI(m.renderMessage(client.Message{
		Role: client.RoleUser, Content: strings.Repeat("row\n", 30) + "last",
	}))

	if !strings.Contains(bubble, "+24 more lines") {
		t.Errorf("long message did not fold:\n%s", bubble)
	}
	if strings.Contains(bubble, "last") {
		t.Errorf("folded tail still rendered:\n%s", bubble)
	}
}

// ---------------------------------------------------------------------------
// Image attachments
// ---------------------------------------------------------------------------

// writeTestPNG paints a small two-tone image to disk.
func writeTestPNG(t *testing.T, dir string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			c := color.RGBA{R: 0xFF, A: 0xFF}
			if y >= 4 {
				c = color.RGBA{B: 0xFF, A: 0xFF}
			}
			img.Set(x, y, c)
		}
	}
	path := filepath.Join(dir, "swatch.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return path
}

func TestPastedImagePathBecomesAnImageChip(t *testing.T) {
	m := readyModel(t)
	path := writeTestPNG(t, t.TempDir())

	m.Update(pasteKey(path))

	if len(m.attachments) != 1 {
		t.Fatalf("%d attachments, want 1", len(m.attachments))
	}
	a := m.attachments[0]
	if a.kind != attachImage || a.name != "swatch.png" || a.imgW != 8 || a.imgH != 8 {
		t.Errorf("attachment = %+v, want an 8×8 swatch.png image", a)
	}
	if m.input.Value() != "" {
		t.Errorf("composer = %q, want the path kept out of it", m.input.Value())
	}
}

// Quoting and escaped spaces — what terminals do to dropped files — unwrap.
func TestImagePathUnwrapsQuotingAndEscapes(t *testing.T) {
	dir := t.TempDir()
	spaced := filepath.Join(dir, "two words")
	if err := os.MkdirAll(spaced, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeTestPNG(t, spaced)

	for _, in := range []string{
		"'" + path + "'",
		`"` + path + `"`,
		strings.ReplaceAll(path, " ", `\ `),
	} {
		if got, ok := imagePath(in); !ok || got != path {
			t.Errorf("imagePath(%q) = %q, %v; want %q, true", in, got, ok, path)
		}
	}
	if _, ok := imagePath("not an image at all"); ok {
		t.Error("prose was mistaken for an image path")
	}
}

// The popup renders the image as half-block cells in the file's colours.
func TestImagePopupRendersBlocks(t *testing.T) {
	// Colours only appear with a colour profile; tests have no TTY to infer
	// one from.
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	m := readyModel(t)
	m.Update(pasteKey(writeTestPNG(t, t.TempDir())))

	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	view := m.View()
	if !strings.Contains(view, "▀") {
		t.Errorf("no half-block cells in the image popup:\n%s", stripANSI(view))
	}
	if !strings.Contains(stripANSI(view), "8×8") {
		t.Errorf("popup title is missing the dimensions:\n%s", stripANSI(view))
	}
	// The swatch is red on top, blue below: both must survive the averaging.
	if !strings.Contains(view, "255;0;0") {
		t.Errorf("top half lost its colour")
	}
}

// A 4k-sized image must downsample to the panel rather than render at size.
func TestImagePreviewFitsThePanel(t *testing.T) {
	m := readyModel(t)
	a := attachment{kind: attachImage, name: "big.png", imgW: 3840, imgH: 2160}

	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 384, 216)) // same ratio, kinder to CI
	path := filepath.Join(dir, "big.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	a.content = path

	out := m.imagePreview(&a, 60, 20)
	lines := strings.Split(out, "\n")
	if len(lines) > 20 {
		t.Errorf("preview is %d rows, want at most 20", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("preview row %d is %d cells wide, want at most 60", i, w)
		}
	}
}
