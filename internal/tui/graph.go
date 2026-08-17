package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/session"
)

// The /graph view: the whole conversation tree on one screen, every prompt
// and reply compressed to a line. A chain of messages stays at one indent —
// only a fork pushes its variants sideways — so a long conversation reads as
// a column and a branched one reads as a river delta.
//
// Summaries come from the "graphing model" (/graph model), are written onto
// the tree nodes, and persist with the session. A node's content never
// changes, so a summary never goes stale and is only ever computed once.

// graphRow is one line of the rendered tree.
type graphRow struct {
	id     string
	prefix string // the tree connectors to the left of the glyph
	role   string
	label  string // the summary, or an excerpt until one exists
	onPath bool
	fork   bool // this message has sibling variants
	sum    bool // label is a model summary rather than an excerpt
}

// graphSumMsg delivers one node's summary from the graphing model.
type graphSumMsg struct {
	id  string
	sum string
	err error
}

// graphHeaderRows / graphFooterRows frame the tree window.
const (
	graphHeaderRows = 2
	graphFooterRows = 2
)

// graphModelName is the model /graph summarizes with: the profile's chosen
// graphing model, or the chat model doing double duty.
func (m *Model) graphModelName() string {
	if prof, ok := m.profiles.Entries[m.profileName]; ok && prof.GraphModel != "" {
		return prof.GraphModel
	}
	return m.sess.Model
}

// openGraph builds the tree view and kicks off summarization of any node
// that does not have a summary yet.
func (m *Model) openGraph() tea.Cmd {
	if m.sess.Empty() {
		return m.setStatus("nothing to graph yet", false)
	}
	m.sess.EnsureTree()
	m.buildGraphRows()
	m.graphOpen = true
	m.graphOff = 0
	// Start on the newest message of the live branch — where the user is.
	m.graphCur = 0
	for i, row := range m.graphRows {
		if row.id == m.sess.Head {
			m.graphCur = i
		}
	}
	return m.startSummaries()
}

func (m *Model) closeGraph() {
	m.graphOpen = false
	// The chain stops scheduling itself once the view is gone; leaving busy
	// state set would strand the next /graph in a fake "summarizing".
	m.graphBusy = false
}

// buildGraphRows flattens the tree into drawable lines. Single children
// continue at their parent's indent; only forks branch out, ├/└ marking the
// variants and │ carrying earlier variants past their younger siblings.
func (m *Model) buildGraphRows() {
	m.graphRows = m.graphRows[:0]

	onPath := map[string]bool{}
	m.sess.Walk(func(node *session.Node, depth int, on bool) {
		if on {
			onPath[node.ID] = true
		}
	})

	var walk func(id, rowPrefix, contPrefix string, fork bool)
	walk = func(id, rowPrefix, contPrefix string, fork bool) {
		node, ok := m.sess.Tree[id]
		if !ok {
			return
		}
		label, isSum := node.Sum, true
		if label == "" {
			label, isSum = excerpt(node.Msg.Content), false
		}
		m.graphRows = append(m.graphRows, graphRow{
			id:     id,
			prefix: rowPrefix,
			role:   node.Msg.Role,
			label:  label,
			onPath: onPath[id],
			fork:   fork,
			sum:    isSum,
		})

		switch len(node.Children) {
		case 0:
		case 1:
			walk(node.Children[0], contPrefix, contPrefix, false)
		default:
			for i, child := range node.Children {
				last := i == len(node.Children)-1
				connector, carry := "├─ ", "│  "
				if last {
					connector, carry = "└─ ", "   "
				}
				walk(child, contPrefix+connector, contPrefix+carry, true)
			}
		}
	}

	roots := m.sess.Roots
	if len(roots) == 1 {
		walk(roots[0], "", "", false)
		return
	}
	for i, root := range roots {
		last := i == len(roots)-1
		connector, carry := "├─ ", "│  "
		if last {
			connector, carry = "└─ ", "   "
		}
		walk(root, connector, carry, true)
	}
}

// excerpt is the stand-in label before a summary exists: the message's first
// words, whitespace collapsed.
func excerpt(text string) string {
	fields := strings.FieldsFunc(text, unicode.IsSpace)
	out := strings.Join(fields, " ")
	const max = 72
	runes := []rune(out)
	if len(runes) > max {
		out = string(runes[:max]) + "…"
	}
	return out
}

// ---------------------------------------------------------------------------
// Summarization
// ---------------------------------------------------------------------------

// startSummaries begins filling in missing summaries, one request at a time —
// the server is one ollama instance, and parallel requests would just fight
// over the loaded model.
func (m *Model) startSummaries() tea.Cmd {
	if m.graphBusy || m.client == nil {
		return nil
	}
	missing := 0
	for _, row := range m.graphRows {
		if !row.sum {
			missing++
		}
	}
	if missing == 0 {
		return nil
	}
	m.graphBusy = true
	m.graphDone = 0
	m.graphTotal = missing
	return m.summarizeNext()
}

// summarizeNext issues one summary request for the first node still missing
// one; the reply's handler schedules the next.
func (m *Model) summarizeNext() tea.Cmd {
	var target *session.Node
	for _, row := range m.graphRows {
		if node, ok := m.sess.Tree[row.id]; ok && node.Sum == "" {
			target = node
			break
		}
	}
	if target == nil {
		m.graphBusy = false
		return nil
	}

	c := m.client
	model := m.graphModelName()
	id, content, role := target.ID, target.Msg.Content, target.Msg.Role
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		sum, err := summarize(ctx, c, model, role, content)
		return graphSumMsg{id: id, sum: sum, err: err}
	}
}

// summarize asks the graphing model for a one-line compression of a message.
func summarize(ctx context.Context, c *client.Client, model, role, content string) (string, error) {
	// Long messages are clipped: the head of a message is where its point
	// lives, and a summary prompt does not deserve a 100k-token context.
	const clip = 6000
	if len(content) > clip {
		content = content[:clip] + "…"
	}

	who := "the user"
	if role == client.RoleAssistant {
		who = "the assistant"
	}

	temp := 0.2
	maxTok := 60
	req := client.ChatRequest{
		Model:       model,
		Temperature: &temp,
		MaxTokens:   &maxTok,
		Messages: []client.Message{
			{Role: client.RoleSystem, Content: "You label chat messages for a map of a conversation. " +
				"Reply with one short sentence, at most twelve words, no quotes, no preamble."},
			{Role: client.RoleUser, Content: "Summarize what " + who + " said here in one short sentence:\n\n" + content},
		},
	}

	var b strings.Builder
	err := c.Chat(ctx, req, func(ch client.Chunk) error {
		b.WriteString(ch.Content)
		return nil
	})
	if err != nil {
		return "", err
	}
	return firstSentenceLine(b.String()), nil
}

// firstSentenceLine tidies a model reply into a label: first non-empty line,
// quotes stripped, length capped.
func firstSentenceLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, `"'`)
		if line == "" {
			continue
		}
		runes := []rune(line)
		const max = 90
		if len(runes) > max {
			line = string(runes[:max]) + "…"
		}
		return line
	}
	return ""
}

// handleGraphSum records one finished summary and schedules the next while
// the view is open.
func (m *Model) handleGraphSum(msg graphSumMsg) tea.Cmd {
	if msg.err != nil {
		m.graphBusy = false
		return m.setStatus("summarizing failed: "+msg.err.Error(), true)
	}
	if node, ok := m.sess.Tree[msg.id]; ok {
		sum := msg.sum
		if sum == "" {
			// The model said nothing usable; fall back to the excerpt and
			// stop asking about this node.
			sum = excerpt(node.Msg.Content)
		}
		node.Sum = sum
	}
	m.graphDone++

	saveCmd := m.persist()
	if !m.graphOpen {
		// The user left; finish bookkeeping but stop burning the server.
		m.graphBusy = false
		return saveCmd
	}
	m.buildGraphRows()
	return tea.Batch(saveCmd, m.summarizeNext())
}

// resummarize throws every summary away and refills them with the current
// graphing model.
func (m *Model) resummarize() tea.Cmd {
	if m.graphBusy {
		return m.setStatus("already summarizing", false)
	}
	for _, node := range m.sess.Tree {
		node.Sum = ""
	}
	m.buildGraphRows()
	return m.startSummaries()
}

// ---------------------------------------------------------------------------
// Keys, mouse, and the jump
// ---------------------------------------------------------------------------

func (m *Model) handleGraphKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.closeGraph()
		return m, nil
	case tea.KeyUp:
		m.moveGraphCursor(-1)
		return m, nil
	case tea.KeyDown:
		m.moveGraphCursor(1)
		return m, nil
	case tea.KeyPgUp:
		m.moveGraphCursor(-m.graphWindow())
		return m, nil
	case tea.KeyPgDown:
		m.moveGraphCursor(m.graphWindow())
		return m, nil
	case tea.KeyEnter:
		return m, m.graphJump()
	}

	switch msg.String() {
	case "k", "ctrl+p":
		m.moveGraphCursor(-1)
	case "j", "ctrl+n":
		m.moveGraphCursor(1)
	case "m":
		return m, m.openGraphModelPicker()
	case "r":
		return m, m.resummarize()
	case "q":
		m.closeGraph()
	}
	return m, nil
}

func (m *Model) moveGraphCursor(delta int) {
	if len(m.graphRows) == 0 {
		return
	}
	m.graphCur = clampInt(m.graphCur+delta, 0, len(m.graphRows)-1)
}

// graphWindow is how many tree rows fit on screen.
func (m *Model) graphWindow() int {
	return maxInt(1, m.height-graphHeaderRows-graphFooterRows)
}

// graphJump makes the chosen node's branch the live conversation and lands
// the transcript on that message.
func (m *Model) graphJump() tea.Cmd {
	if m.graphCur < 0 || m.graphCur >= len(m.graphRows) {
		return nil
	}
	if m.streaming {
		return m.setStatus("wait for the reply to finish", true)
	}
	row := m.graphRows[m.graphCur]

	beforeHead := m.sess.Head
	idx := m.sess.ActivateNode(row.id)
	if idx < 0 {
		return m.setStatus("that message is gone", true)
	}
	m.closeGraph()

	if m.sess.Head != beforeHead {
		// A different branch is live now; nothing measured on the old one —
		// nor an edit armed against its indexes — still applies.
		m.editFrom = -1
		m.lastUsage = nil
		m.lastElapsed = 0
	}
	m.rebuildCache()
	m.refreshViewport(false)

	// Scroll the transcript so the chosen message tops the pane.
	for _, r := range m.msgRanges {
		if r.msg == idx {
			m.viewport.SetYOffset(r.start)
			break
		}
	}
	return tea.Batch(m.persist(), m.setStatus(fmt.Sprintf("message %d of %d", idx+1, len(m.sess.Messages)), false))
}

// handleGraphMouse scrolls the tree with the wheel and jumps on click.
func (m *Model) handleGraphMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.moveGraphCursor(-wheelStep)
		return nil
	case tea.MouseButtonWheelDown:
		m.moveGraphCursor(wheelStep)
		return nil
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionRelease {
			return nil
		}
		idx := m.graphOff + msg.Y - graphHeaderRows
		if idx < 0 || idx >= len(m.graphRows) || msg.Y < graphHeaderRows {
			return nil
		}
		m.graphCur = idx
		return m.graphJump()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// graphView draws the whole tree, replacing the chat: a conversation with
// real branches needs every column the terminal has.
func (m *Model) graphView() string {
	left := styleHeader.Render("conversation graph")
	right := styleHeaderMeta.Render("graphing model · " + m.graphModelName())
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	header := left
	if gap >= 1 {
		header = left + strings.Repeat(" ", gap) + right
	}

	window := m.graphWindow()
	// Keep the cursor inside the window.
	if m.graphCur < m.graphOff {
		m.graphOff = m.graphCur
	}
	if m.graphCur >= m.graphOff+window {
		m.graphOff = m.graphCur - window + 1
	}
	m.graphOff = clampInt(m.graphOff, 0, maxInt(0, len(m.graphRows)-window))

	out := make([]string, 0, m.height)
	out = append(out, header, "")
	end := minInt(len(m.graphRows), m.graphOff+window)
	for i := m.graphOff; i < end; i++ {
		out = append(out, m.renderGraphRow(i))
	}
	for len(out) < m.height-1 {
		out = append(out, "")
	}

	footer := "↑/↓ move · enter jump to message · m graphing model · r re-summarize · esc close"
	if m.graphBusy {
		footer = fmt.Sprintf("summarizing %d/%d with %s…  ·  %s",
			minInt(m.graphDone+1, m.graphTotal), m.graphTotal, m.graphModelName(), footer)
	}
	out = append(out, styleFaint.Render(truncate(footer, m.width)))
	return strings.Join(out[:minInt(len(out), m.height)], "\n")
}

// renderGraphRow draws one node line: connectors, the role glyph, the fork
// star, and the one-line label — live branch bright, the others dimmed.
func (m *Model) renderGraphRow(i int) string {
	row := m.graphRows[i]

	glyph, glyphStyle := "●", lipgloss.NewStyle().Foreground(colorUser)
	switch row.role {
	case client.RoleAssistant:
		glyph, glyphStyle = "○", lipgloss.NewStyle().Foreground(colorModel)
	case client.RoleSystem:
		glyph, glyphStyle = "◈", styleSearchNote
	}

	star := ""
	if row.fork {
		star = styleWarn.Render("✦ ")
	}

	labelStyle := styleMuted
	if row.onPath {
		labelStyle = lipgloss.NewStyle().Foreground(colorText)
	}
	if !row.sum {
		labelStyle = labelStyle.Italic(true)
	}

	prefix := styleFaint.Render(row.prefix)
	used := lipgloss.Width(row.prefix) + 2 + lipgloss.Width(star)
	label := truncate(row.label, maxInt(8, m.width-used-2))

	line := prefix + glyphStyle.Render(glyph) + " " + star + labelStyle.Render(label)
	if i == m.graphCur {
		// The selected row inverts its label, keeping the tree ink readable.
		line = prefix + glyphStyle.Render(glyph) + " " + star + stylePickerSelected.Render(" "+label+" ")
	}
	return line
}

// openGraphModelPicker chooses which model writes the summaries.
func (m *Model) openGraphModelPicker() tea.Cmd {
	if len(m.models) == 0 {
		return tea.Batch(loadModels(m.client), m.setStatus("fetching models…", false))
	}
	items := make([]pickerItem, 0, len(m.models))
	for _, mod := range m.models {
		desc := ""
		if mod.ID == m.graphModelName() {
			desc = "current"
		}
		items = append(items, pickerItem{id: mod.ID, title: mod.ID, desc: desc})
	}
	m.pick = newPicker(pickerGraphModel, "Choose a graphing model", items)
	m.mode = pickerGraphModel
	return nil
}

// setGraphModel records the choice on the profile, so it holds across
// sessions the same way the chat model does.
func (m *Model) setGraphModel(id string) tea.Cmd {
	if prof, ok := m.profiles.Entries[m.profileName]; ok {
		prof.GraphModel = id
	}
	_, _ = config.UpdateProfiles(func(p *config.Profiles) error {
		if entry, ok := p.Entries[m.profileName]; ok {
			entry.GraphModel = id
		}
		return nil
	})
	var cmd tea.Cmd
	if m.graphOpen {
		cmd = m.startSummaries()
	}
	return tea.Batch(cmd, m.setStatus("graphing model: "+id, false))
}
