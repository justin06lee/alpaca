package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The splash is drawn as a grid of colour keys and then painted one row at a
// time, which is what makes the scanline reveal possible: rendering stops at
// the row the scan has reached, and the leading row is painted brighter as if
// the beam were still on it.

// Colour keys used by the pixel art. '.' is transparent.
const (
	pxWool   = 'W'
	pxShade  = 'D'
	pxEye    = 'E'
	pxMuzzle = 'M'
	pxHoof   = 'H'
	pxInner  = 'I'
	pxText   = 'T'
	pxDrop   = 'S'
	pxEmpty  = '.'
)

// alpacaPixels is the animal, one character per pixel, facing left.
var alpacaPixels = []string{
	"..W....W............",
	"..WW..WW............",
	".WWWWWWWW...........",
	".WWEWWEWW...........",
	".WWWWWWWW...........",
	"..WWMMWW............",
	"...WWWW.............",
	"....WWW.............",
	"....WWW.............",
	".....WWW............",
	".....WWWWWWWWWW.....",
	"....WWWWWWWWWWWWW...",
	"....WWWWWWWWWWWWW...",
	".....WWWWWWWWWWW....",
	".....WW.WW.WW.WW....",
	".....HH.HH.HH.HH....",
}

// glyphs are 5x5 bitmaps for the four distinct letters in ALPACA.
var glyphs = map[rune][]string{
	'A': {
		".###.",
		"#...#",
		"#####",
		"#...#",
		"#...#",
	},
	'L': {
		"#....",
		"#....",
		"#....",
		"#....",
		"#####",
	},
	'P': {
		"####.",
		"#...#",
		"####.",
		"#....",
		"#....",
	},
	'C': {
		".###.",
		"#...#",
		"#....",
		"#...#",
		".###.",
	},
}

// grid is a mutable canvas of colour keys.
type grid struct {
	cells [][]rune
	w, h  int
}

func newGrid(w, h int) *grid {
	cells := make([][]rune, h)
	for y := range cells {
		row := make([]rune, w)
		for x := range row {
			row[x] = pxEmpty
		}
		cells[y] = row
	}
	return &grid{cells: cells, w: w, h: h}
}

func (g *grid) set(x, y int, key rune) {
	if x < 0 || y < 0 || x >= g.w || y >= g.h {
		return
	}
	g.cells[y][x] = key
}

// blit copies a pixel block onto the grid, skipping transparent cells.
func (g *grid) blit(rows []string, atX, atY int, key rune) {
	for y, row := range rows {
		for x, ch := range row {
			if ch == pxEmpty || ch == ' ' {
				continue
			}
			// Art rows carry their own keys; a non-zero key overrides them,
			// which is how the drop shadow reuses the letter shapes.
			if key != 0 {
				g.set(atX+x, atY+y, key)
			} else {
				g.set(atX+x, atY+y, ch)
			}
		}
	}
}

// wordmark renders ALPACA with a one-pixel drop shadow, giving the letters an
// extruded edge without needing a separate isometric font.
func wordmark() []string {
	const word = "ALPACA"
	const glyphW, glyphH, gap = 5, 5, 2

	width := len(word)*(glyphW+gap) - gap + 1 // +1 for the shadow overhang
	g := newGrid(width, glyphH+1)

	// Shadow first, offset down-right, then the face on top of it.
	for pass, key := range []rune{pxDrop, pxText} {
		offset := 1 - pass // shadow at +1, face at 0
		x := 0
		for _, letter := range word {
			g.blit(glyphs[letter], x+offset, offset, key)
			x += glyphW + gap
		}
	}
	return g.rows()
}

func (g *grid) rows() []string {
	out := make([]string, g.h)
	for y, row := range g.cells {
		out[y] = string(row)
	}
	return out
}

// splashPalette maps colour keys to styles. Two are built: the settled one and
// a brighter variant for the row the scan is currently on.
func splashPalette(bright bool) map[rune]lipgloss.Style {
	colors := map[rune]string{
		pxWool:   "#C98A6B",
		pxShade:  "#A66A50",
		pxEye:    "#2A2118",
		pxMuzzle: "#E4BA9B",
		pxHoof:   "#6B4A38",
		pxInner:  "#B5766A",
		pxText:   "#E8A87C",
		pxDrop:   "#7A4A35",
	}
	if bright {
		// The beam washes everything toward white as it passes.
		colors = map[rune]string{
			pxWool:   "#FFF1E4",
			pxShade:  "#FFE3CE",
			pxEye:    "#8A7460",
			pxMuzzle: "#FFF6EC",
			pxHoof:   "#C9A991",
			pxInner:  "#FFDCD2",
			pxText:   "#FFFFFF",
			pxDrop:   "#FFD9BC",
		}
	}

	out := make(map[rune]lipgloss.Style, len(colors))
	for key, hex := range colors {
		out[key] = lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
	}
	return out
}

// splashArt assembles the whole image as colour-key rows.
//
// The alpaca is dropped when the terminal is too short for both, because a
// clipped animal looks like a bug rather than a choice.
func splashArt(height int) []string {
	mark := wordmark()

	if height < len(alpacaPixels)+len(mark)+4 {
		return mark
	}

	rows := make([]string, 0, len(alpacaPixels)+len(mark)+1)
	rows = append(rows, alpacaPixels...)
	rows = append(rows, "")
	rows = append(rows, mark...)
	return rows
}

// renderSplash paints the art up to the scan position.
//
// pixelWidth is how many terminal columns each pixel occupies; two keeps the
// blocks roughly square, since a character cell is about twice as tall as it is
// wide. It drops to one when the terminal is too narrow for that.
func renderSplash(width, height, scan int, tagline string) string {
	art := splashArt(height)

	widest := 0
	for _, row := range art {
		if len(row) > widest {
			widest = len(row)
		}
	}

	pixelWidth := 2
	if widest*pixelWidth > width-2 {
		pixelWidth = 1
	}
	block := strings.Repeat("█", pixelWidth)
	blank := strings.Repeat(" ", pixelWidth)

	settled := splashPalette(false)
	beam := splashPalette(true)

	visible := minInt(scan, len(art))
	lines := make([]string, 0, visible+2)

	for y := 0; y < visible; y++ {
		palette := settled
		// The last two revealed rows carry the beam, which reads better than a
		// single row: one row alone flickers past too fast to register.
		if y >= visible-2 {
			palette = beam
		}

		var b strings.Builder
		for _, key := range art[y] {
			if key == pxEmpty || key == ' ' {
				b.WriteString(blank)
				continue
			}
			style, ok := palette[key]
			if !ok {
				b.WriteString(blank)
				continue
			}
			b.WriteString(style.Render(block))
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}

	// The tagline arrives only once the picture is complete.
	if scan > len(art)+1 && tagline != "" {
		lines = append(lines, "", styleMuted.Render(tagline))
	}

	body := lipgloss.NewStyle().Width(width).Align(lipgloss.Center).
		Render(strings.Join(lines, "\n"))

	// Hold the finished image at a stable vertical position rather than letting
	// it creep down the screen as rows appear.
	total := len(art) + 2
	topPad := (height - total) / 2
	if topPad < 0 {
		topPad = 0
	}
	return strings.Repeat("\n", topPad) + body
}
