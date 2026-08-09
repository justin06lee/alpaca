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

// view renders the overlay, scrolling to keep the cursor on screen.
func (p *picker) view(width, height int) string {
	var b strings.Builder

	header := stylePickerTitle.Render(p.title)
	if p.filter != "" {
		header += styleMuted.Render("  filter: " + p.filter)
	}
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(styleMuted.Render(strings.Repeat("─", maxInt(1, minInt(width, 60)))))
	b.WriteString("\n")

	// Rows available after the header, separator, and footer.
	rows := height - 4
	if rows < 1 {
		rows = 1
	}

	// Scroll the window so the cursor stays visible.
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+rows {
		p.offset = p.cursor - rows + 1
	}
	if p.offset < 0 {
		p.offset = 0
	}

	if len(p.visible) == 0 {
		b.WriteString(styleMuted.Render("  nothing matches " + p.filter))
		b.WriteString("\n")
	}

	end := minInt(p.offset+rows, len(p.visible))
	for i := p.offset; i < end; i++ {
		item := p.items[p.visible[i]]
		line := "  " + item.title
		if item.desc != "" {
			line += "  " + item.desc
		}
		line = truncate(line, width-1)

		if i == p.cursor {
			b.WriteString(stylePickerSelected.Render(truncate("› "+item.title, width-1)))
			if item.desc != "" {
				b.WriteString(stylePickerDesc.Render("  " + item.desc))
			}
		} else {
			b.WriteString(stylePickerDesc.Render(line))
		}
		b.WriteString("\n")
	}

	footer := "↑/↓ move · enter choose · esc cancel · type to filter"
	if p.kind == pickerSession {
		footer = "↑/↓ move · enter open · ctrl+d delete · esc cancel"
	}
	b.WriteString(styleMuted.Render(truncate(footer, width)))

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
