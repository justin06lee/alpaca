package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSplashFinalFrame dumps the completed image so it can be eyeballed.
func TestSplashFinalFrame(t *testing.T) {
	art := splashArt(30)
	out := renderSplash(90, 30, len(art)+4, "offline demo · canned replies, no network")
	t.Logf("FINAL FRAME\n%s", stripANSI(out))

	plain := stripANSI(out)
	if !strings.Contains(plain, "█") {
		t.Fatal("nothing was drawn")
	}
	if !strings.Contains(plain, "offline demo") {
		t.Error("tagline missing from the completed frame")
	}
}

// The reveal has to actually be partial part-way through, or it is not a reveal.
func TestSplashRevealsProgressively(t *testing.T) {
	art := splashArt(30)

	counts := make([]int, 0, 4)
	for _, scan := range []int{3, 8, 16, len(art)} {
		plain := stripANSI(renderSplash(90, 30, scan, ""))
		drawn := 0
		for _, line := range strings.Split(plain, "\n") {
			if strings.Contains(line, "█") {
				drawn++
			}
		}
		counts = append(counts, drawn)
		t.Logf("scan=%d -> %d rows drawn", scan, drawn)
	}

	for i := 1; i < len(counts); i++ {
		if counts[i] < counts[i-1] {
			t.Errorf("row count went backwards: %v", counts)
		}
	}
	if counts[0] >= counts[len(counts)-1] {
		t.Errorf("no progression across the reveal: %v", counts)
	}
}

// The leading rows are painted in the beam palette; those colours must differ
// from the settled ones or the scan is invisible.
//
// This compares the declared foregrounds rather than rendered output: with no
// TTY, lipgloss strips colour entirely and every style renders identically, so
// asserting on Render would pass or fail for reasons that have nothing to do
// with the palette.
func TestSplashBeamDiffersFromSettled(t *testing.T) {
	settled := splashPalette(false)
	beam := splashPalette(true)

	if len(settled) == 0 || len(settled) != len(beam) {
		t.Fatalf("palettes have %d and %d entries", len(settled), len(beam))
	}
	for key := range settled {
		a := settled[key].GetForeground()
		b := beam[key].GetForeground()
		if a == b {
			t.Errorf("colour key %q is the same in both palettes (%v)", string(key), a)
		}
	}
}

// A short terminal drops the animal rather than clipping it.
func TestSplashAdaptsToSmallTerminals(t *testing.T) {
	tall := splashArt(40)
	short := splashArt(12)

	if len(short) >= len(tall) {
		t.Errorf("short terminal art is %d rows, tall is %d — expected the animal dropped",
			len(short), len(tall))
	}
	// Whatever survives must still be the wordmark.
	if len(short) != len(wordmark()) {
		t.Errorf("short art is %d rows, want just the wordmark (%d)", len(short), len(wordmark()))
	}

	// Narrow terminals fall back to one column per pixel instead of overflowing.
	narrow := stripANSI(renderSplash(40, 30, 99, ""))
	for _, line := range strings.Split(narrow, "\n") {
		if len([]rune(line)) > 40 {
			t.Errorf("line overflows a 40-column terminal (%d runes): %q", len([]rune(line)), line)
		}
	}
}

func TestWordmarkHasExtrudedEdge(t *testing.T) {
	rows := wordmark()
	joined := strings.Join(rows, "\n")

	if !strings.ContainsRune(joined, pxText) {
		t.Error("wordmark has no face pixels")
	}
	if !strings.ContainsRune(joined, pxDrop) {
		t.Error("wordmark has no shadow pixels, so it will not read as 3d")
	}
	// The shadow must not swallow the face.
	faces := strings.Count(joined, string(pxText))
	drops := strings.Count(joined, string(pxDrop))
	if drops >= faces {
		t.Errorf("shadow pixels (%d) outnumber face pixels (%d)", drops, faces)
	}
}

// The opening must actually get out of the way, and must be skippable.
func TestSplashGivesWayToTheChat(t *testing.T) {
	m := newRenderedModel(t)
	m.splashDone, m.splashScan = false, 0

	// Nothing is drawn until the first tick, which is the point of a reveal.
	if strings.Contains(stripANSI(m.View()), "█") {
		t.Error("the opening drew before the scan started")
	}
	m.Update(splashTickMsg{})
	m.Update(splashTickMsg{})
	if !strings.Contains(stripANSI(m.View()), "█") {
		t.Error("opening frame drew nothing")
	}

	// Run the reveal to completion the way the tick command would.
	for i := 0; i < m.splashTotal()+2 && !m.splashDone; i++ {
		m.Update(splashTickMsg{})
	}
	if !m.splashDone {
		t.Fatalf("splash never finished after %d ticks", m.splashTotal()+2)
	}

	view := stripANSI(m.View())
	if strings.Contains(view, "█") {
		t.Errorf("still showing the opening after it finished:\n%s", view)
	}
	if !strings.Contains(view, "Send a message") {
		t.Errorf("chat interface did not take over:\n%s", view)
	}
}

func TestAnyKeySkipsTheSplash(t *testing.T) {
	m := newRenderedModel(t)
	m.splashDone, m.splashScan = false, 0
	m.Update(splashTickMsg{})

	if m.splashDone {
		t.Fatal("splash finished on its own too early")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	if !m.splashDone {
		t.Error("a keypress did not skip the opening")
	}
	// The key that skipped it must not also land in the composer.
	if m.input.Value() != "" {
		t.Errorf("composer = %q, want the skip key swallowed", m.input.Value())
	}
}
