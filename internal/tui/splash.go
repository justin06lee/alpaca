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

// splashArt is the whole image as colour-key rows: the animal, a gap, then the
// wordmark. Sizing decisions belong to the layout, not here.
func splashArt(withAnimal bool) []string {
	mark := wordmark()
	if !withAnimal {
		return mark
	}
	// The animal is drawn on its own narrow canvas; shift it here so it sits
	// centred over the wordmark, since the renderer aligns every row of the
	// combined image to a single left edge.
	pad := strings.Repeat(string(pxEmpty), maxInt(0, (widestRow(mark)-widestRow(alpacaPixels))/2))
	rows := make([]string, 0, len(alpacaPixels)+1+len(mark))
	for _, row := range alpacaPixels {
		rows = append(rows, pad+row)
	}
	rows = append(rows, "")
	return append(rows, mark...)
}

// splashLayout picks how the image is drawn for a given terminal.
//
// There are exactly two sizes worth using, because only these two put square
// pixels on screen: a character cell is about twice as tall as it is wide, so a
// full block spanning two columns is square, and a half block spanning one
// column is square at half the scale. Anything else stretches the art.
type splashLayout struct {
	art []string
	// half packs two pixel rows into each terminal row using ▀, which is what
	// lets the animal survive on a 24-row terminal.
	half bool
	// pixelWidth is columns per pixel: 2 for full blocks, 1 for half blocks.
	pixelWidth int
}

// rows is how many terminal rows the finished image occupies.
func (l splashLayout) rows() int {
	if l.half {
		return (len(l.art) + 1) / 2
	}
	return len(l.art)
}

// steps is how many scan positions the reveal has. Scanning by pixel row rather
// than terminal row keeps the animation the same duration at either size.
func (l splashLayout) steps() int { return len(l.art) }

func layoutFor(width, height int) splashLayout {
	full := splashArt(true)
	widest := widestRow(full)

	// Chunky first: it is the look worth having when there is room for it.
	if width >= widest*2+4 && height >= len(full)+4 {
		return splashLayout{art: full, half: false, pixelWidth: 2}
	}
	// Half blocks halve the height, which is usually enough to keep the animal.
	if width >= widest+2 && height >= (len(full)+1)/2+4 {
		return splashLayout{art: full, half: true, pixelWidth: 1}
	}
	// Nothing fits the animal: keep the wordmark rather than clipping.
	mark := splashArt(false)
	if width >= widestRow(mark)*2+4 && height >= len(mark)+3 {
		return splashLayout{art: mark, half: false, pixelWidth: 2}
	}
	return splashLayout{art: mark, half: true, pixelWidth: 1}
}

func widestRow(rows []string) int {
	widest := 0
	for _, row := range rows {
		if len(row) > widest {
			widest = len(row)
		}
	}
	return widest
}

// pixelAt reads a colour key, treating anything off the grid as transparent.
func pixelAt(art []string, x, y int) rune {
	if y < 0 || y >= len(art) || x < 0 || x >= len(art[y]) {
		return pxEmpty
	}
	return rune(art[y][x])
}

// renderSplash paints the image up to the scan position, measured in pixel rows.
func renderSplash(width, height, scan int, tagline string) string {
	layout := layoutFor(width, height)
	art := layout.art
	widest := widestRow(art)

	settled := splashPalette(false)
	beam := splashPalette(true)
	// The two pixel rows behind the scan carry the beam. One alone flickers past
	// too quickly to register as a sweep.
	paletteFor := func(pixelRow int) map[rune]lipgloss.Style {
		if pixelRow >= scan-2 {
			return beam
		}
		return settled
	}

	visible := minInt(scan, len(art))
	// Every line keeps its full width, trailing blanks included. The centring
	// below aligns each line individually, so trimming would centre every row
	// on its own content: rows whose art ends early — the animal's head, the
	// top strokes of the letters — would drift sideways relative to the rest.
	var lines []string

	if layout.half {
		for row := 0; row*2 < visible; row++ {
			top, bottom := row*2, row*2+1
			var b strings.Builder
			for x := 0; x < widest; x++ {
				topKey := pixelAt(art, x, top)
				bottomKey := pixelAt(art, x, bottom)
				if bottom >= visible {
					bottomKey = pxEmpty // the scan has not reached it yet
				}
				b.WriteString(halfBlock(topKey, bottomKey, paletteFor(top), paletteFor(bottom)))
			}
			lines = append(lines, b.String())
		}
	} else {
		block := strings.Repeat("█", layout.pixelWidth)
		blank := strings.Repeat(" ", layout.pixelWidth)
		for y := 0; y < visible; y++ {
			palette := paletteFor(y)
			var b strings.Builder
			for x := 0; x < widest; x++ {
				key := pixelAt(art, x, y)
				if style, ok := palette[key]; ok && key != pxEmpty {
					b.WriteString(style.Render(block))
				} else {
					b.WriteString(blank)
				}
			}
			lines = append(lines, b.String())
		}
	}

	// The tagline arrives only once the picture is complete.
	settledYet := scan > len(art)+1
	if settledYet && tagline != "" {
		lines = append(lines, "", styleMuted.Render(tagline))
	}

	centre := lipgloss.NewStyle().Width(width).Align(lipgloss.Center)
	body := centre.Render(strings.Join(lines, "\n"))

	// Hold the image at a fixed position rather than letting it creep downward
	// as rows appear.
	topPad := (height - layout.rows() - 2) / 2
	if topPad < 0 {
		topPad = 0
	}

	out := make([]string, 0, height)
	for i := 0; i < topPad; i++ {
		out = append(out, "")
	}
	out = append(out, strings.Split(body, "\n")...)

	// The credit sits on the last row, arriving with the tagline so it does not
	// pre-empt the reveal sweeping down from the top.
	if settledYet && height > len(out)+1 {
		for len(out) < height-1 {
			out = append(out, "")
		}
		out = append(out, centre.Render(styleCredit.Render(credit)))
	}
	if len(out) > height && height > 0 {
		out = out[:height]
	}
	return strings.Join(out, "\n")
}

// credit is deliberately quiet: dark enough on a dark terminal, and light
// enough on a light one, to read as a signature rather than a banner.
const credit = "made by justin06lee.dev"

// halfBlock renders two stacked pixels into one cell. ▀ paints the upper half
// in the foreground colour and leaves the lower half to the background, so a
// single cell carries two independently coloured pixels.
func halfBlock(top, bottom rune, topPalette, bottomPalette map[rune]lipgloss.Style) string {
	topStyle, hasTop := topPalette[top]
	bottomStyle, hasBottom := bottomPalette[bottom]
	hasTop = hasTop && top != pxEmpty
	hasBottom = hasBottom && bottom != pxEmpty

	switch {
	case !hasTop && !hasBottom:
		return " "
	case hasTop && !hasBottom:
		return topStyle.Render("▀")
	case !hasTop && hasBottom:
		return bottomStyle.Render("▄")
	default:
		return topStyle.Background(bottomStyle.GetForeground()).Render("▀")
	}
}
