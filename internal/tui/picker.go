package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type pickerKind int

const (
	pickerNone pickerKind = iota
	pickerModel
	pickerSession
)

type pickerItem struct {
	id    string
	title string
	desc  string
}

// picker is a filterable list overlay, used for both models and sessions.
//
// It is hand-rolled rather than built on bubbles/list because the needs here
// are small — filter, move, choose — and a bespoke 100 lines is easier to
// reason about than configuring a general-purpose component into this shape.
type picker struct {
	kind   pickerKind
	title  string
	items  []pickerItem
	filter string
	// visible indexes into items, narrowed by the filter.
	visible []int
	cursor  int
	// offset is the first visible row, for scrolling long lists.
	offset int
}

func newPicker(kind pickerKind, title string, items []pickerItem) picker {
	p := picker{kind: kind, title: title, items: items}
	p.refilter()
	return p
}

// refilter recomputes the visible set, keeping the cursor in range.
func (p *picker) refilter() {
	p.visible = p.visible[:0]
	needle := strings.ToLower(strings.TrimSpace(p.filter))

	for i, item := range p.items {
		if needle == "" ||
			strings.Contains(strings.ToLower(item.title), needle) ||
			strings.Contains(strings.ToLower(item.desc), needle) {
			p.visible = append(p.visible, i)
		}
	}

	if p.cursor >= len(p.visible) {
		p.cursor = len(p.visible) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	p.offset = 0
}

func (p *picker) move(delta int) {
	if len(p.visible) == 0 {
		return
	}
	p.cursor += delta
	// Wrap, so holding a key does not dead-end at the edges.
	if p.cursor < 0 {
		p.cursor = len(p.visible) - 1
	}
	if p.cursor >= len(p.visible) {
		p.cursor = 0
	}
}

func (p *picker) selected() (pickerItem, bool) {
	if p.cursor < 0 || p.cursor >= len(p.visible) {
		return pickerItem{}, false
	}
	return p.items[p.visible[p.cursor]], true
}

func (p *picker) appendFilter(s string) {
	p.filter += s
	p.refilter()
}

func (p *picker) backspaceFilter() {
	if p.filter == "" {
		return
	}
	runes := []rune(p.filter)
	p.filter = string(runes[:len(runes)-1])
	p.refilter()
}

// headerLine is the panel or screen title, with the live filter beside it.
func (p *picker) headerLine(width int) string {
	header := stylePickerTitle.Render(p.title)
	if p.filter != "" {
		header += styleMuted.Render("  ·  " + p.filter)
	}
	return truncate(header, width)
}

// footerLine names the keys that matter for this list.
func (p *picker) footerLine(width int) string {
	footer := "↑/↓ move · enter choose · esc close · type to filter"
	if p.kind == pickerSession {
		footer = "↑/↓ move · enter open · ctrl+d delete · esc close"
	}
	return styleFaint.Render(truncate(footer, width))
}

// ensureVisible scrolls the window so the cursor stays on screen when
// perScreen items fit at once.
func (p *picker) ensureVisible(perScreen int) {
	if perScreen < 1 {
		perScreen = 1
	}
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+perScreen {
		p.offset = p.cursor - perScreen + 1
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

// itemLines renders one line per item, cursor marked, scrolled to keep it on
// screen. Shared by the floating popup and the small-terminal fallback.
func (p *picker) itemLines(width, rows int) []string {
	p.ensureVisible(rows)
	if len(p.visible) == 0 {
		return []string{styleMuted.Render(truncate("  nothing matches "+p.filter, width))}
	}

	var out []string
	end := minInt(p.offset+rows, len(p.visible))
	for i := p.offset; i < end; i++ {
		item := p.items[p.visible[i]]
		if i == p.cursor {
			// The selected row styles its two halves differently, so each is
			// truncated on its own: the description gets whatever the title
			// left over, rather than overflowing the row.
			title := truncate("› "+item.title, width)
			line := stylePickerSelected.Render(title)
			if remaining := width - lipgloss.Width(title); item.desc != "" && remaining > 3 {
				line += stylePickerDesc.Render(truncate("  "+item.desc, remaining))
			}
			out = append(out, line)
		} else {
			line := "  " + item.title
			if item.desc != "" {
				line += "  " + item.desc
			}
			out = append(out, stylePickerDesc.Render(truncate(line, width)))
		}
	}
	return out
}

// sidebarItemLines renders two rows per item — the title, then its details —
// which reads far better in a tall, narrow panel than one crowded line.
func (p *picker) sidebarItemLines(width, perScreen int) []string {
	p.ensureVisible(perScreen)
	if len(p.visible) == 0 {
		return []string{styleMuted.Render(truncate("nothing matches "+p.filter, width))}
	}

	var out []string
	end := minInt(p.offset+perScreen, len(p.visible))
	for i := p.offset; i < end; i++ {
		item := p.items[p.visible[i]]
		if i == p.cursor {
			out = append(out, stylePickerSelected.Render(truncate("› "+item.title, width)))
		} else {
			out = append(out, styleGreeting.Render(truncate("  "+item.title, width)))
		}
		out = append(out, stylePickerDesc.Render(truncate("    "+item.desc, width)))
	}
	return out
}

// view renders the list over the whole screen — the fallback for terminals too
// small to float a panel in.
func (p *picker) view(width, height int) string {
	var b strings.Builder
	b.WriteString(p.headerLine(width))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render(strings.Repeat("─", maxInt(1, minInt(width, 60)))))
	b.WriteString("\n")

	rows := maxInt(1, height-4)
	for _, line := range p.itemLines(width-1, rows) {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(p.footerLine(width))

	return lipgloss.NewStyle().Width(width).Render(b.String())
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
