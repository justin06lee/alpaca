package tui

import (
	"math/rand"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// composerRows is the height of the framed input: three rows of text plus the
// border above and below.
const composerRows = 5

// slideTicks is how long the composer takes to travel from the middle of the
// screen down to its dock. Short enough to feel like a response to the keypress
// rather than a cutscene.
const slideTicks = 26

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

// easeOutBack overshoots its target slightly before settling, which is what
// gives the composer its bounce as it lands.
func easeOutBack(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	const c1 = 1.9
	const c3 = c1 + 1
	u := t - 1
	return 1 + c3*u*u*u + c1*u*u
}

func lerpInt(from, to int, t float64) int {
	return from + int(float64(to-from)*t+0.5)
}

// slide is how far the composer has travelled: 0 in the middle of an empty
// screen, 1 docked at the bottom.
func (m *Model) slide() float64 {
	if m.sliding {
		return easeOutBack(float64(m.slideStep) / float64(slideTicks))
	}
	return 1
}

// composerWidth narrows the box while it is centred and lets it fill the width
// once docked.
func (m *Model) composerWidth(slide float64) int {
	full := m.width - 2
	narrow := clampInt(m.width*64/100, 36, 88)
	if narrow > full {
		narrow = full
	}
	return lerpInt(narrow, full, slide)
}

// renderComposer frames the input, or a stop hint while the model is talking.
func (m *Model) renderComposer(width int) string {
	frame := styleComposer
	if !m.streaming {
		frame = styleComposerActive
	}
	// Width on a bordered style is the inner width, and the border eats two
	// columns; padding takes two more.
	inner := width - 4
	if inner < 8 {
		inner = 8
	}

	if m.streaming {
		body := m.spinner.View() + " " + styleMuted.Render(m.thinkingLabel()+
			strings.Repeat(" ", maxInt(0, inner-lipgloss.Width(m.thinkingLabel())-2)))
		return frame.Width(inner).Render(body + "\n" +
			styleFaint.Render("esc to stop") + "\n")
	}

	m.input.SetWidth(inner)
	return frame.Width(inner).Render(m.input.View())
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
	// Two rows for the header, one for the footer hint.
	available := m.height - 3
	top := (available - len(rows)) / 2
	if top < 0 {
		top = 0
	}

	out := []string{m.header(), ""}
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

	var lines []string
	for row := 0; row*2 < len(art); row++ {
		var b strings.Builder
		for x := 0; x < widest; x++ {
			top := pixelAt(art, x, row*2)
			bottom := pixelAt(art, x, row*2+1)
			b.WriteString(halfBlock(top, bottom, palette, palette))
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
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
	composer := strings.Split(m.renderComposer(m.composerWidth(slide)), "\n")

	// The composer starts where the welcome screen left it and ends one row
	// above the status bar. easeOutBack can carry it briefly past the dock,
	// which pushes the status line off the bottom and reads as a bounce.
	docked := m.height - 1 - len(composer)
	centred := (m.height-3-len(composer))/2 + 2
	top := clampInt(lerpInt(centred, docked, slide), 2, m.height-1)

	// The transcript fills whatever room is left above.
	above := maxInt(0, top-2)
	if m.viewport.Height != above {
		m.viewport.Height = above
		m.viewport.Width = m.width
		m.refreshViewport(true)
	}

	out := make([]string, 0, m.height)
	out = append(out, m.header(), "")
	if above > 0 {
		// The viewport returns only the lines it has, not a full pane, so the
		// gap has to be filled here. Without this the composer lands directly
		// under the last line of the transcript instead of at its dock, and the
		// slide has nowhere to travel.
		pane := strings.Split(m.viewport.View(), "\n")
		for len(pane) < above {
			pane = append(pane, "")
		}
		out = append(out, pane[:above]...)
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
