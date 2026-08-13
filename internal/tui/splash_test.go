package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/session"
	"github.com/muesli/termenv"
)

// TestSplashFinalFrame dumps the completed image so it can be eyeballed.
func TestSplashFinalFrame(t *testing.T) {
	art := splashArt(true)
	out := renderSplash(90, 34, len(art)+4, "offline demo · canned replies, no network")
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
	art := splashArt(true)

	counts := make([]int, 0, 4)
	for _, scan := range []int{3, 8, 16, len(art)} {
		plain := stripANSI(renderSplash(90, 34, scan, ""))
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

// A cramped terminal must still show the animal by packing two pixel rows per
// line, and must never overflow.
func TestSplashAdaptsToSmallTerminals(t *testing.T) {
	roomy := layoutFor(120, 44)
	if roomy.half || roomy.pixelWidth != 2 {
		t.Errorf("a large terminal should get chunky full blocks, got %+v", roomy)
	}
	if len(roomy.art) != len(splashArt(true)) {
		t.Error("a large terminal should show the animal")
	}

	// 24x80 is an ordinary terminal and must not lose the animal.
	ordinary := layoutFor(80, 24)
	if !ordinary.half {
		t.Errorf("a 24-row terminal should pack rows with half blocks, got %+v", ordinary)
	}
	if len(ordinary.art) != len(splashArt(true)) {
		t.Errorf("a 24-row terminal lost the animal (%d rows of art)", len(ordinary.art))
	}
	if ordinary.rows()+2 > 24 {
		t.Errorf("image is %d rows, which does not fit 24", ordinary.rows()+2)
	}

	// Genuinely tiny: the wordmark alone, still no overflow.
	tiny := layoutFor(44, 10)
	if len(tiny.art) != len(wordmark()) {
		t.Errorf("a tiny terminal should fall back to the wordmark, got %d rows", len(tiny.art))
	}

	for _, size := range [][2]int{{80, 24}, {44, 10}, {120, 44}, {40, 12}} {
		w, h := size[0], size[1]
		out := stripANSI(renderSplash(w, h, 99, "tagline"))
		for _, line := range strings.Split(out, "\n") {
			if len([]rune(line)) > w {
				t.Errorf("%dx%d: line overflows (%d runes)", w, h, len([]rune(line)))
			}
		}
		if len(strings.Split(out, "\n")) > h {
			t.Errorf("%dx%d: image is taller than the terminal", w, h)
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
	if !strings.Contains(view, "Ask anything") {
		t.Errorf("chat interface did not take over:\n%s", view)
	}
}

func TestSplashSkipIgnoresStartupChatter(t *testing.T) {
	m := newRenderedModel(t)
	m.splashDone, m.splashScan = false, 0

	// A terminal answers a TUI's startup queries within a few milliseconds, and
	// those replies arrive as input. They must not dismiss the opening before
	// anyone has seen it.
	m.Update(splashTickMsg{})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r', 'g', 'b'}})
	if m.splashDone {
		t.Fatal("input during the grace window skipped the opening")
	}

	// Sequence replies also decode into assorted control keys, which are not
	// something a person would press to dismiss a splash.
	for i := 0; i < splashGrace+2; i++ {
		m.Update(splashTickMsg{})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.splashDone {
		t.Error("a control key skipped the opening; only deliberate keys should")
	}

	// A real keypress, after the grace window, does skip it.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if !m.splashDone {
		t.Error("a deliberate keypress after the grace window did not skip the opening")
	}
	if m.input.Value() != "" {
		t.Errorf("composer = %q, want the skip key swallowed", m.input.Value())
	}
}

func TestIsDeliberateKey(t *testing.T) {
	deliberate := []tea.KeyType{tea.KeyRunes, tea.KeySpace, tea.KeyEnter, tea.KeyEsc}
	for _, kt := range deliberate {
		if !isDeliberateKey(tea.KeyMsg{Type: kt}) {
			t.Errorf("%v should count as a deliberate keypress", kt)
		}
	}
	for _, kt := range []tea.KeyType{tea.KeyCtrlL, tea.KeyF1, tea.KeyUp, tea.KeyCtrlA} {
		if isDeliberateKey(tea.KeyMsg{Type: kt}) {
			t.Errorf("%v should not dismiss the opening", kt)
		}
	}
}

// A cell holding two filled pixels must set a background as well as a
// foreground, or it paints only its upper half and the art looks hollow.
func TestHalfBlockPairs(t *testing.T) {
	// Without a forced profile lipgloss strips colour when stdout is not a
	// terminal, and every style renders identically.
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	p := splashPalette(false)
	both := halfBlock(pxWool, pxWool, p, p)
	topOnly := halfBlock(pxWool, pxEmpty, p, p)
	bottomOnly := halfBlock(pxEmpty, pxWool, p, p)
	neither := halfBlock(pxEmpty, pxEmpty, p, p)

	t.Logf("both       = %q", both)
	t.Logf("topOnly    = %q", topOnly)
	t.Logf("bottomOnly = %q", bottomOnly)

	if !strings.Contains(both, "48;2;") {
		t.Errorf("a fully filled cell has no background, so it will look hollow: %q", both)
	}
	if strings.Contains(topOnly, "48;2;") {
		t.Errorf("a half-filled cell should not set a background: %q", topOnly)
	}
	if !strings.Contains(bottomOnly, "▄") {
		t.Errorf("bottom-only cell should use the lower half block: %q", bottomOnly)
	}
	if neither != " " {
		t.Errorf("empty cell = %q, want a space", neither)
	}
}

// The opening doubles as the loading screen, so it must not be dismissable
// before the connection lands: handing over early would render a chat surface
// with no client behind it, which panicked.
func TestSplashHoldsUntilConnected(t *testing.T) {
	t.Setenv("ALPACA_HOME", t.TempDir())
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	profiles := &config.Profiles{Entries: map[string]*config.Profile{}}

	// A connector that never returns stands in for a slow route race.
	stalled := func(ctx context.Context) (*client.Client, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	m := New(stalled, store, profiles, "test", session.New("m", "test"))
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 34})

	// Run well past the point the reveal would otherwise end.
	for i := 0; i < m.splashTotal()+30; i++ {
		m.Update(splashTickMsg{})
	}
	if m.splashDone {
		t.Error("the opening finished while still connecting")
	}

	// Nor can it be skipped by hand while there is nothing to talk to.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.splashDone {
		t.Error("a keypress dismissed the loading screen before connecting")
	}

	// Rendering in that state must not panic on the absent client.
	if view := stripANSI(m.View()); !strings.Contains(view, "connecting") {
		t.Errorf("loading screen does not say it is connecting:\n%s", view)
	}

	// Once connected it hands over.
	c, stop, err := client.NewDemo()
	if err != nil {
		t.Fatalf("NewDemo: %v", err)
	}
	defer stop()
	m.Update(connectedMsg{client: c})
	m.Update(splashTickMsg{})
	if !m.splashDone {
		t.Error("the opening did not finish after the connection landed")
	}
}

// A connection failure has to reach the command line, not vanish into the UI.
func TestConnectFailureEndsTheSession(t *testing.T) {
	t.Setenv("ALPACA_HOME", t.TempDir())
	store, _ := session.NewStore()
	m := New(func(context.Context) (*client.Client, error) {
		return nil, errFake{"no route to host"}
	}, store, &config.Profiles{Entries: map[string]*config.Profile{}}, "test",
		session.New("m", "test"))

	m.Update(connectedMsg{err: errFake{"no route to host"}})

	if m.Err() == nil || !strings.Contains(m.Err().Error(), "no route to host") {
		t.Errorf("Err() = %v, want the connection failure", m.Err())
	}
	if !m.quitting {
		t.Error("a failed connection should end the session")
	}
}

// ctrl+c works from anywhere, including the opening — where no client exists
// yet. Quitting there must exit cleanly, not dereference the connection that
// never happened.
func TestQuitDuringConnectDoesNotPanic(t *testing.T) {
	t.Setenv("ALPACA_HOME", t.TempDir())
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	profiles := &config.Profiles{Entries: map[string]*config.Profile{}}

	stalled := func(ctx context.Context) (*client.Client, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	m := New(stalled, store, profiles, "test", session.New("m", "test"))
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 34})
	m.Update(splashTickMsg{})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c during connect produced no command, want quit")
	}
}

// Every row of the rendered image must sit on one shared left edge. The
// renderer centres lines individually, so a row trimmed to its own content —
// the animal's head, the top strokes of the letters — would drift sideways
// relative to wider rows below it, shearing the artwork.
func TestSplashRowsShareOneLeftEdge(t *testing.T) {
	layout := layoutFor(100, 60)
	if layout.half {
		t.Fatal("expected the chunky layout at this size")
	}

	rendered := strings.Split(renderSplash(100, 60, 999, ""), "\n")

	// Art rows start after the vertical centring pad, one per line.
	topPad := 0
	for _, l := range rendered {
		if strings.TrimSpace(ansi.Strip(l)) == "" {
			topPad++
			continue
		}
		break
	}

	offset := -1
	for i, artRow := range layout.art {
		if strings.Trim(artRow, ".") == "" {
			continue
		}
		leadingPixels := len(artRow) - len(strings.TrimLeft(artRow, "."))

		s := ansi.Strip(rendered[topPad+i])
		firstBlock := strings.IndexRune(s, '█')
		if firstBlock < 0 {
			t.Fatalf("art row %d rendered with no pixels", i)
		}
		rowOffset := firstBlock - leadingPixels*layout.pixelWidth
		if offset < 0 {
			offset = rowOffset
		}
		if rowOffset != offset {
			t.Errorf("art row %d sits at offset %d, want %d — rows are centred on their own content", i, rowOffset, offset)
		}
	}
}

// The animal must sit centred over the wordmark, not flush against the canvas
// edge, now that all rows share one left edge.
func TestSplashAnimalIsCentredOverTheWordmark(t *testing.T) {
	art := splashArt(true)
	mark := wordmark()

	animalRow := art[10] // a body row, the animal's widest part
	leading := len(animalRow) - len(strings.TrimLeft(animalRow, "."))
	trailing := widestRow(mark) - len(strings.TrimRight(animalRow, "."))
	if diff := leading - trailing; diff < -4 || diff > 4 {
		t.Errorf("animal margins are %d and %d pixels — it is not centred over the wordmark", leading, trailing)
	}
}
