package tui

import (
	"math/rand"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// composerMinRows is how many text rows the input shows at rest. Typing more
// grows the box one row at a time up to composerMaxRows, and past that the
// textarea scrolls internally, cursor always in view.
const composerMinRows = 3

// composerRows is the height of the framed input at rest: the minimum text
// rows plus the border above and below.
const composerRows = composerMinRows + 2

// slideDuration is how long the composer takes to travel from the middle of the
// screen down to its dock. A plain glide reads best kept brisk: long enough to
// register as motion, short enough that the reply never waits on it.
const slideDuration = 400 * time.Millisecond

// alpacaHead is just the face, trimmed to two terminal rows of half blocks so
// the header stays modest. Same colour keys as the splash.
var alpacaHead = []string{
	".W....W.",
	"WWWWWWWW",
	"WWEWWEWW",
	".WWMMWW.",
}

// miniAlpaca is a smaller sprite than the opening's, sized to sit above a line
// of text without dominating it. Same colour keys as the splash.
var miniAlpaca = []string{
	"..W...W.......",
	"..WW.WW.......",
	".WWWWWWW......",
	".WWEWWEW......",
	".WWWWWWW......",
	"..WWMWW.......",
	"...WWW........",
	"...WW.........",
	"...WWW........",
	"...WWWWWWWWW..",
	"..WWWWWWWWWWW.",
	"..WWWWWWWWWWW.",
	"..WW.WW.WW.WW.",
	"..HH.HH.HH.HH.",
}

// greetings open an empty chat. Written to sound like the animal rather than a
// product, and picked once per session so the screen does not reshuffle itself
// on every repaint.
var greetings = []string{
	"What are we chewing over today?",
	"The herd is listening.",
	"Ask me something.",
	"Fresh pasture. What's on your mind?",
	"Ready when you are.",
	"Say the word.",
	"What can I help you with?",
	"All ears, and there are two of them.",
	"Let's get into it.",
	"Nothing on the mind yet?",
}

// thinkingWords fill the wait before the first token. Alpacas hum, ruminate,
// and wool-gather, so the vocabulary was sitting right there.
var thinkingWords = []string{
	"Ruminating",
	"Wool-gathering",
	"Humming",
	"Chewing it over",
	"Grazing",
	"Pondering",
	"Combing the fleece",
	"Trekking",
	"Mulling",
	"Herding thoughts",
	"Untangling",
	"Consulting the herd",
	"Sizing up the pasture",
	"Spinning yarn",
}

func randomGreeting() string { return greetings[rand.Intn(len(greetings))] }

// easeOutCubic starts fast and brakes smoothly into the dock, stopping exactly
// there.
//
// It replaced a damped-sine spring. The bounce needed the box to overshoot its
// dock and settle back, but vertical travel is only about eight terminal rows
// and a row is the smallest step there is — so the overshoot quantised to a
// single row flickering over the status line instead of reading as motion. A
// monotone ease spends the same time where the eye actually is: quick off the
// mark, decelerating into the landing, never past it.
func easeOutCubic(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	inv := 1 - t
	return 1 - inv*inv*inv
}

func lerpInt(from, to int, t float64) int {
	return from + int(float64(to-from)*t+0.5)
}

// slide is how far the composer has travelled: 0 in the middle of an empty
// screen, 1 docked at the bottom.
// Progress is measured against the clock rather than counted in frames. If
// anything stalls the update loop, a clock-based slide resumes where it should
// be by now, whereas a frame count would freeze mid-flight and then play the
// rest of the animation late.
func (m *Model) slide() float64 {
	if !m.sliding {
		return 1
	}
	return easeOutCubic(float64(time.Since(m.slideStart)) / float64(slideDuration))
}

// composerWidth narrows the box while it is centred and lets it fill the width
// once docked.
func (m *Model) composerWidth(slide float64) int {
	full := m.width - 2*uiPadX
	narrow := clampInt(m.width*64/100, 36, 88)
	if narrow > full {
		narrow = full
	}
	// Clamped as a guard: the width must never end up past the terminal edge,
	// where the box would wrap its own border.
	return clampInt(lerpInt(narrow, full, slide), narrow, full)
}

// composerMaxRows caps the input's growth at roughly a third of the screen,
// so a long message can never squeeze the transcript out of view.
func (m *Model) composerMaxRows() int {
	return clampInt(m.height/3, composerMinRows, 10)
}

// inputRowsNeeded estimates how many display rows the composer text occupies
// at the textarea's current width, soft-wrapped lines included. The estimate
// leans high on exact-width lines, which only means the box grows a row
// early — never that the cursor ends up outside it.
func (m *Model) inputRowsNeeded() int {
	width := maxInt(1, m.input.Width())
	rows := 0
	for _, line := range strings.Split(m.input.Value(), "\n") {
		rows += 1 + lipgloss.Width(line)/width
	}
	return rows
}

// syncInput resizes the input to fit its content and then nudges the textarea
// with an empty update, which runs its internal view repositioning. The nudge
// is the fix for a real bug: InsertString and SetHeight bypass repositioning,
// so a ctrl+j newline used to push the cursor below the visible rows and keep
// typing into a part of the box that was not on screen.
func (m *Model) syncInput() {
	rows := clampInt(m.inputRowsNeeded(), composerMinRows, m.composerMaxRows())
	if m.input.Height() != rows {
		m.input.SetHeight(rows)
	}
	// The textarea's scroll position is clamped against its internal
	// viewport's content, and that content is only refreshed inside View —
	// so a throwaway view must come first or the reposition has nothing to
	// scroll through and quietly stays put.
	_ = m.input.View()
	m.input, _ = m.input.Update(nil)
}

// renderComposer frames the input, or a stop hint while the model is talking.
func (m *Model) renderComposer(width int) string {
	frame := styleComposer
	if m.editFrom >= 0 {
		frame = styleComposerEditing
	} else if !m.streaming {
		frame = styleComposerActive
	}
	// The border eats two columns and the padding two more, so the text gets
	// width-4. The frame's Width covers content plus padding — lipgloss wraps
	// content at Width minus padding — so it must be inner+2. Setting it to
	// inner wrapped the textarea's cursor line, whose styled trailing spaces
	// survive the word-wrapper where bare ones are dropped, and the composer
	// grew a phantom row the moment anything was typed.
	inner := width - 4
	if inner < 8 {
		inner = 8
	}
	frameWidth := inner + 2

	if m.streaming {
		body := m.spinner.View() + " " + styleMuted.Render(m.thinkingLabel()+
			strings.Repeat(" ", maxInt(0, inner-lipgloss.Width(m.thinkingLabel())-2)))
		return frame.Width(frameWidth).Render(body + "\n" +
			styleFaint.Render("esc to stop") + "\n")
	}

	// Width first, then height: the row count depends on where lines wrap.
	m.input.SetWidth(inner)
	m.syncInput()

	body := m.input.View()
	if chips := m.attachmentChips(inner); chips != "" {
		body = chips + "\n" + body
	}
	return frame.Width(frameWidth).Render(body)
}

// thinkingLabel is the rotating status shown before the first token lands.
func (m *Model) thinkingLabel() string {
	return thinkingWords[m.thinkingIdx%len(thinkingWords)] + "…"
}

// welcomeView is the empty state: the animal, a greeting, and a composer
// floating in the middle of the screen.
func (m *Model) welcomeView() string {
	body := m.welcomeBlock(m.composerWidth(0))

	rows := strings.Split(body, "\n")
	// The header rows, one for the footer hint.
	available := m.height - headerHeight - 1
	top := (available - len(rows)) / 2
	if top < 0 {
		top = 0
	}

	out := append(strings.Split(m.header(), "\n"), "")
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	out = append(out, rows...)
	for len(out) < m.height-1 {
		out = append(out, "")
	}
	out = append(out, m.statusBar())
	return strings.Join(out[:minInt(len(out), m.height)], "\n")
}

// welcomeComposerTop is the screen row the welcome screen puts the composer on.
//
// The slide reads from the same function, because when the two layouts worked
// it out separately they disagreed by a couple of rows, and the box visibly
// jumped upward before it began travelling down.
func (m *Model) welcomeComposerTop() int {
	rows := strings.Split(m.welcomeBlock(m.composerWidth(0)), "\n")
	top := (m.height - headerHeight - 1 - len(rows)) / 2
	if top < 0 {
		top = 0
	}
	// The header rows, then whatever the block stacks above the composer.
	return headerHeight + top + len(strings.Split(renderSprite(miniAlpaca), "\n")) + 3
}

// welcomeBlock is the centred stack, without the surrounding padding.
func (m *Model) welcomeBlock(composerWidth int) string {
	centre := lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center)

	parts := []string{
		centre.Render(renderSprite(miniAlpaca)),
		"",
		centre.Render(styleGreeting.Render(m.greeting)),
		"",
		centre.Render(m.renderComposer(composerWidth)),
		"",
		centre.Render(styleFaint.Render("enter to send  ·  ctrl+j newline  ·  ? for keys")),
	}
	return strings.Join(parts, "\n")
}

// renderSprite draws pixel art with half blocks, the same trick the opening
// uses to keep pixels square.
func renderSprite(art []string) string {
	palette := splashPalette(false)
	widest := widestRow(art)

	// Every line keeps its full width, trailing blanks included. The caller
	// centres these lines individually, so trimming them would centre each row
	// on its own content and shear the sprite sideways wherever the art's
	// right edge moves.
	var lines []string
	for row := 0; row*2 < len(art); row++ {
		var b strings.Builder
		for x := 0; x < widest; x++ {
			top := pixelAt(art, x, row*2)
			bottom := pixelAt(art, x, row*2+1)
			b.WriteString(halfBlock(top, bottom, palette, palette))
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n")
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// chatView is the docked layout, and every frame in between while the composer
// is still travelling down from the middle of the screen.
func (m *Model) chatView() string {
	slide := m.slide()
	block := m.renderComposer(m.composerWidth(slide))
	// Centred while it travels — matching where the welcome screen drew it —
	// which settles to exactly the pane padding once the box is at full width.
	left := maxInt(uiPadX, (m.width-lipgloss.Width(block))/2)
	composer := strings.Split(padLines(block, left), "\n")

	// The composer starts where the welcome screen left it and ends one row
	// above the status bar. The ease is monotone, so it brakes into the dock
	// and stops there; the clamp only guards tiny terminals.
	docked := m.height - 1 - len(composer)
	centred := m.welcomeComposerTop()
	top := clampInt(lerpInt(centred, docked, slide), headerHeight, m.height-len(composer))

	// The transcript fills whatever room is left above, minus one blank row so
	// the composer never sits flush against the last line of the conversation.
	above := maxInt(0, top-headerHeight)
	paneRows := maxInt(0, above-1)
	if m.viewport.Height != paneRows {
		m.viewport.Height = paneRows
		m.viewport.Width = m.width
		m.refreshViewport(true)
	}

	out := make([]string, 0, m.height)
	out = append(out, strings.Split(m.header(), "\n")...)
	out = append(out, "")
	if paneRows > 0 {
		// The viewport returns only the lines it has, not a full pane, so the
		// gap has to be filled here. Without this the composer lands directly
		// under the last line of the transcript instead of at its dock, and the
		// slide has nowhere to travel.
		pane := strings.Split(m.viewport.View(), "\n")
		for len(pane) < paneRows {
			pane = append(pane, "")
		}
		out = append(out, pane[:paneRows]...)
	}
	if above > 0 {
		out = append(out, "") // the breathing row above the composer
	}
	out = append(out, composer...)
	for len(out) < m.height-1 {
		out = append(out, "")
	}
	out = append(out, m.statusBar())

	if len(out) > m.height {
		out = out[:m.height]
	}
	return strings.Join(out, "\n")
}
