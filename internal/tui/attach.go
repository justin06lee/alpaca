package tui

import (
	"fmt"
	"image"
	_ "image/gif"  // registered so pasted .gif paths decode
	_ "image/jpeg" // registered so pasted .jpg paths decode
	_ "image/png"  // registered so pasted .png paths decode
	"os"
	"path/filepath"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// A paste bigger than either limit is staged as a chip instead of dumped into
// the composer: a wall of text in a small box hides where the cursor went and
// makes the message unreadable before it is even sent.
const (
	pasteLineLimit = 3
	pasteCharLimit = 400
)

// attachKind separates pasted text from dropped-in images.
type attachKind int

const (
	attachText attachKind = iota
	attachImage
)

// attachment is content staged in the composer without living in its text: an
// oversized paste, or an image file dropped onto the terminal. It rides along
// as a chip until the message is sent.
type attachment struct {
	kind    attachKind
	content string // the pasted text, or the image's path
	name    string // base name, images only
	lines   int    // text only
	imgW    int
	imgH    int

	// preview caches the half-block rendering, which costs a full decode of
	// the source image to build.
	preview     string
	previewCols int
	previewRows int
}

// label is the chip text. Numbering restarts as chips are removed, and the
// popup title uses the same wording so the two are obviously the same thing.
func (a attachment) label(i int) string {
	if a.kind == attachImage {
		return fmt.Sprintf("[image #%d · %s]", i+1, a.name)
	}
	if a.lines == 1 {
		// A one-line paste only lands here by being enormous.
		return fmt.Sprintf("[#%d · %d chars pasted]", i+1, len(a.content))
	}
	return fmt.Sprintf("[#%d · %d lines pasted]", i+1, a.lines)
}

// handlePaste routes one bracketed paste: image paths become image chips,
// oversized text becomes a paste chip, and anything small lands in the
// composer as ordinary typing would.
func (m *Model) handlePaste(text string) tea.Cmd {
	// Terminals paste line breaks as carriage returns; the composer and the
	// wire format both speak \n.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	if cmd, ok := m.attachImages(text); ok {
		return cmd
	}

	lines := strings.Count(text, "\n") + 1
	if lines > pasteLineLimit || len(text) > pasteCharLimit {
		m.attachments = append(m.attachments, attachment{
			kind: attachText, content: text, lines: lines,
		})
		return m.setStatus(fmt.Sprintf("%d lines staged as a chip — ↑ then enter to view", lines), false)
	}

	m.input.InsertString(text)
	return nil
}

// attachImages recognises a paste that is nothing but image paths — which is
// what dragging a file onto the terminal produces — and stages each as a chip.
func (m *Model) attachImages(text string) (tea.Cmd, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}

	// A single path first (spaces may be escaped or quoted); failing that, a
	// multi-file drop as whitespace-separated paths, all of which must match —
	// one stray word means this is prose, not a drop.
	var paths []string
	if p, ok := imagePath(trimmed); ok {
		paths = []string{p}
	} else {
		for _, field := range strings.Fields(trimmed) {
			p, ok := imagePath(field)
			if !ok {
				return nil, false
			}
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, false
	}

	added := 0
	for _, p := range paths {
		att, err := newImageAttachment(p)
		if err != nil {
			return m.setStatus("could not read image: "+err.Error(), true), true
		}
		m.attachments = append(m.attachments, att)
		added++
	}
	noun := "image"
	if added > 1 {
		noun = "images"
	}
	return m.setStatus(fmt.Sprintf("%d %s attached — ↑ then enter to preview", added, noun), false), true
}

// imagePath reports whether s names an existing image file, undoing the
// quoting and escaping terminals apply when a file is dropped on them.
func imagePath(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	}
	s = strings.ReplaceAll(s, `\ `, " ")

	switch strings.ToLower(filepath.Ext(s)) {
	case ".png", ".jpg", ".jpeg", ".gif":
	default:
		return "", false
	}
	info, err := os.Stat(s)
	if err != nil || info.IsDir() {
		return "", false
	}
	return s, true
}

// newImageAttachment stats the dimensions up front — DecodeConfig reads only
// the header, so a 4k photo costs nothing to stage.
func newImageAttachment(path string) (attachment, error) {
	f, err := os.Open(path)
	if err != nil {
		return attachment{}, err
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return attachment{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return attachment{
		kind: attachImage, content: path, name: filepath.Base(path),
		imgW: cfg.Width, imgH: cfg.Height,
	}, nil
}

// attachmentChips renders the staged attachments as one row of chips, the
// focused one highlighted whole — which is what makes chip focus legible.
func (m *Model) attachmentChips(width int) string {
	if len(m.attachments) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m.attachments))
	for i, a := range m.attachments {
		style := styleChip
		if i == m.attachFocus {
			style = styleChipFocused
		}
		parts = append(parts, style.Render(a.label(i)))
	}
	return ansi.Truncate(strings.Join(parts, " "), width, "…")
}

// handleChipKey drives the chip row while it has focus.
func (m *Model) handleChipKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyLeft:
		if m.attachFocus > 0 {
			m.attachFocus--
		}
		return m, nil

	case tea.KeyRight:
		if m.attachFocus < len(m.attachments)-1 {
			m.attachFocus++
		}
		return m, nil

	case tea.KeyDown, tea.KeyEsc:
		m.attachFocus = -1
		return m, nil

	case tea.KeyEnter:
		m.viewAttach = m.attachFocus
		m.attachScroll = 0
		return m, nil

	case tea.KeyBackspace, tea.KeyDelete:
		m.attachments = append(m.attachments[:m.attachFocus], m.attachments[m.attachFocus+1:]...)
		if len(m.attachments) == 0 {
			m.attachFocus = -1
		} else if m.attachFocus >= len(m.attachments) {
			m.attachFocus = len(m.attachments) - 1
		}
		return m, nil
	}

	// Any other key belongs to the composer: hand focus back and let the
	// ordinary path have it, so typing never feels trapped in the chips.
	m.attachFocus = -1
	return m.handleChatKey(msg)
}

// attachViewPage is how far pgup/pgdn move the popup, in lines.
const attachViewPage = 10

// handleAttachViewKey drives the full-content popup.
func (m *Model) handleAttachViewKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyEnter:
		m.viewAttach = -1
		return nil
	case tea.KeyUp:
		m.attachScroll = maxInt(0, m.attachScroll-1)
		return nil
	case tea.KeyDown:
		m.attachScroll++ // clamped against the content when rendered
		return nil
	case tea.KeyPgUp:
		m.attachScroll = maxInt(0, m.attachScroll-attachViewPage)
		return nil
	case tea.KeyPgDown:
		m.attachScroll += attachViewPage
		return nil
	}

	if msg.String() == "y" || msg.String() == "c" {
		if m.viewAttach < len(m.attachments) {
			a := m.attachments[m.viewAttach]
			if a.kind == attachText {
				if err := clipboard.WriteAll(a.content); err != nil {
					return m.setStatus("clipboard unavailable: "+err.Error(), true)
				}
				return m.setStatus(fmt.Sprintf("copied %d lines", a.lines), false)
			}
		}
	}
	return nil
}

// attachmentPopover floats the full content of one attachment over the chat:
// the pasted text scrollable line by line, or the image as a block preview.
func (m *Model) attachmentPopover(base string) string {
	if m.viewAttach < 0 || m.viewAttach >= len(m.attachments) {
		return base
	}
	a := &m.attachments[m.viewAttach]

	outer := clampInt(m.width*78/100, 40, 110)
	if m.width < outer+2 || m.height < 12 {
		outer = maxInt(20, m.width-2)
	}
	inner := outer - 4
	rows := clampInt(m.height-10, 4, 32)

	var title, footer string
	var body []string
	if a.kind == attachImage {
		title = fmt.Sprintf("image #%d · %s · %d×%d", m.viewAttach+1, a.name, a.imgW, a.imgH)
		footer = "esc to close"
		body = strings.Split(m.imagePreview(a, inner, rows), "\n")
	} else {
		lines := strings.Split(a.content, "\n")
		m.attachScroll = clampInt(m.attachScroll, 0, maxInt(0, len(lines)-rows))
		last := minInt(len(lines), m.attachScroll+rows)
		title = fmt.Sprintf("paste #%d · lines %d–%d of %d",
			m.viewAttach+1, m.attachScroll+1, last, len(lines))
		footer = "↑/↓ scroll · y copy · esc close"
		for _, line := range lines[m.attachScroll:last] {
			line = strings.ReplaceAll(line, "\t", "    ")
			body = append(body, truncate(line, inner))
		}
	}

	content := []string{stylePickerTitle.Render(truncate(title, inner)), ""}
	content = append(content, body...)
	content = append(content, "", styleFaint.Render(truncate(footer, inner)))

	popup := panel(strings.Join(content, "\n"), outer)
	x := (m.width - outer) / 2
	y := maxInt(1, (m.height-(len(content)+2))/2)
	return overlay(base, popup, x, y)
}

// imagePreview renders the image with half blocks, two pixels per cell, each
// pixel the average colour of its patch of the source — a 4k photo collapses
// into whatever fits the panel. The result is cached: a decode plus a few
// thousand styled cells is nothing once, and a repaint storm at 60 frames is
// not the place to do it again.
func (m *Model) imagePreview(a *attachment, maxCols, maxRows int) string {
	if a.preview != "" && a.previewCols == maxCols && a.previewRows == maxRows {
		return a.preview
	}

	f, err := os.Open(a.content)
	if err != nil {
		return styleError.Render("could not open " + a.name)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return styleError.Render("could not decode " + a.name)
	}

	bounds := img.Bounds()
	iw, ih := bounds.Dx(), bounds.Dy()
	if iw == 0 || ih == 0 {
		return styleError.Render(a.name + " is empty")
	}

	// A cell is one pixel wide and two tall, so the pixel grid is maxCols by
	// 2·maxRows; scale to fit, and never past 1:1 — enlarging a small image
	// buys blur, not detail.
	scale := minFloat(float64(maxCols)/float64(iw), float64(2*maxRows)/float64(ih))
	if scale > 1 {
		scale = 1
	}
	cols := maxInt(1, int(float64(iw)*scale))
	pxRows := maxInt(1, int(float64(ih)*scale))

	var b strings.Builder
	for row := 0; row*2 < pxRows; row++ {
		if row > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < cols; x++ {
			top := patchColor(img, bounds, x, row*2, cols, pxRows)
			style := lipgloss.NewStyle().Foreground(top)
			if row*2+1 < pxRows {
				style = style.Background(patchColor(img, bounds, x, row*2+1, cols, pxRows))
			}
			b.WriteString(style.Render("▀"))
		}
	}

	a.preview = b.String()
	a.previewCols, a.previewRows = maxCols, maxRows
	return a.preview
}

// patchColor is the average colour of the source-image region behind one
// preview pixel. Sampling strides through big patches rather than visiting
// every source pixel: a 4k frame shrunk to 80 columns has ~2500 pixels per
// patch, and eyes cannot tell a 36-sample average from the full sum.
func patchColor(img image.Image, bounds image.Rectangle, px, py, cols, rows int) lipgloss.Color {
	x0 := bounds.Min.X + px*bounds.Dx()/cols
	x1 := maxInt(x0+1, bounds.Min.X+(px+1)*bounds.Dx()/cols)
	y0 := bounds.Min.Y + py*bounds.Dy()/rows
	y1 := maxInt(y0+1, bounds.Min.Y+(py+1)*bounds.Dy()/rows)

	xStep := maxInt(1, (x1-x0)/6)
	yStep := maxInt(1, (y1-y0)/6)

	var r, g, bl, n uint64
	for y := y0; y < y1; y += yStep {
		for x := x0; x < x1; x += xStep {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			r += uint64(cr >> 8)
			g += uint64(cg >> 8)
			bl += uint64(cb >> 8)
			n++
		}
	}
	if n == 0 {
		return lipgloss.Color("#000000")
	}
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", uint8(r/n), uint8(g/n), uint8(bl/n)))
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
