package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/session"
)

// send dispatches whatever is in the composer, staged attachments included.
func (m *Model) send() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" && len(m.attachments) == 0 {
		return nil
	}

	if strings.HasPrefix(text, "/") {
		m.input.Reset()
		return m.runSlash(text)
	}

	if m.sess.Model == "" {
		return m.setStatus("no model selected yet — press ctrl+p to choose one", true)
	}

	// The composer only makes its journey once: on the message that turns an
	// empty screen into a conversation. A branch of the first message is not
	// that — the composer is already docked.
	firstMessage := m.sess.Empty() && m.editFrom < 0

	if m.editFrom >= 0 {
		// The edit becomes a sibling of the original: the old continuation
		// stays in the tree, and the transcript is cut back to the fork.
		if m.sess.Rebase(m.editFrom) {
			m.rebuildCache()
		}
		m.editFrom = -1
	}

	m.input.Reset()
	m.sess.Append(client.Message{Role: client.RoleUser, Content: m.composeOutgoing(text)})
	m.cacheLast()

	stream := m.startStream()
	if firstMessage {
		m.sliding, m.slideStart = true, time.Now()
		return tea.Batch(stream, slideTick())
	}
	return stream
}

// composeOutgoing folds the staged attachments into the message: pasted text
// travels in full after whatever was typed. The wire format is text-only, so
// an image goes as a note naming it rather than a silent drop — the user
// watched it preview locally and deserves to know the model never saw it.
func (m *Model) composeOutgoing(text string) string {
	parts := make([]string, 0, len(m.attachments)+1)
	if text != "" {
		parts = append(parts, text)
	}
	for _, a := range m.attachments {
		if a.kind == attachImage {
			parts = append(parts, "[attached image: "+a.name+" — not sent, text-only connection]")
		} else {
			parts = append(parts, a.content)
		}
	}
	m.discardAttachments()
	return strings.Join(parts, "\n\n")
}

// newSession archives the current chat and starts a fresh one, carrying over
// the model and system prompt since those are usually still what the user wants.
func (m *Model) newSession() tea.Cmd {
	if m.streaming {
		m.stopStream()
	}
	saveCmd := m.persist()

	model, system := m.sess.Model, m.sess.System
	m.sess = session.New(model, m.profileName)
	m.sess.System = system

	m.editFrom = -1
	m.rendered = m.rendered[:0]
	m.lastUsage = nil
	m.lastElapsed = 0
	m.refreshViewport(true)

	return tea.Batch(saveCmd, m.setStatus("new chat", false))
}

func (m *Model) openModelPicker() tea.Cmd {
	if len(m.models) == 0 {
		return tea.Batch(loadModels(m.client), m.setStatus("fetching models…", false))
	}

	items := make([]pickerItem, 0, len(m.models))
	for _, mod := range m.models {
		var meta []string
		if mod.ParameterSize != "" {
			meta = append(meta, mod.ParameterSize)
		}
		if mod.Quantization != "" {
			meta = append(meta, mod.Quantization)
		}
		if mod.Size > 0 {
			meta = append(meta, humanBytes(mod.Size))
		}
		if mod.ID == m.sess.Model {
			meta = append(meta, "current")
		}
		items = append(items, pickerItem{id: mod.ID, title: mod.ID, desc: strings.Join(meta, " · ")})
	}

	m.pick = newPicker(pickerModel, "Choose a model", items)
	m.mode = pickerModel
	return nil
}

func (m *Model) openSessionPicker() tea.Cmd {
	sessions, err := m.store.List()
	if err != nil {
		return m.setStatus("could not read saved chats: "+err.Error(), true)
	}
	if len(sessions) == 0 {
		return m.setStatus("no saved chats yet", false)
	}

	items := make([]pickerItem, 0, len(sessions))
	for _, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		desc := fmt.Sprintf("%s · %d msgs · %s", relativeTime(s.Updated), len(s.Messages), s.Model)
		if s.ID == m.sess.ID {
			desc += " · current"
		}
		items = append(items, pickerItem{id: s.ID, title: title, desc: desc})
	}

	m.pick = newPicker(pickerSession, "Saved chats", items)
	m.mode = pickerSession
	return nil
}

// choosePicked applies the highlighted picker entry.
func (m *Model) choosePicked() tea.Cmd {
	item, ok := m.pick.selected()
	if !ok {
		m.mode = pickerNone
		return nil
	}
	kind := m.pick.kind
	m.mode = pickerNone

	switch kind {
	case pickerModel:
		if item.id == m.sess.Model {
			return nil
		}
		m.sess.Model = item.id
		m.rememberModel()
		// Assistant labels carry the model name, so the whole transcript
		// has to be re-rendered.
		m.rebuildCache()
		m.refreshViewport(true)
		return tea.Batch(m.persist(), m.setStatus("model: "+item.id, false))

	case pickerGraphModel:
		return m.setGraphModel(item.id)

	case pickerSession:
		if item.id == m.sess.ID {
			return nil
		}
		saveCmd := m.persist()
		loaded, err := m.store.Load(item.id)
		if err != nil {
			return m.setStatus("could not open that chat: "+err.Error(), true)
		}
		m.sess = loaded
		m.editFrom = -1
		m.lastUsage = nil
		m.lastElapsed = 0
		m.rebuildCache()
		m.refreshViewport(true)
		m.viewport.GotoBottom()
		return tea.Batch(saveCmd, m.setStatus("opened: "+item.title, false))
	}
	return nil
}

// deletePicked removes the highlighted session.
func (m *Model) deletePicked() tea.Cmd {
	item, ok := m.pick.selected()
	if !ok {
		return nil
	}
	if item.id == m.sess.ID {
		return m.setStatus("that chat is open — switch away before deleting it", true)
	}
	if err := m.store.Delete(item.id); err != nil {
		return m.setStatus("could not delete: "+err.Error(), true)
	}
	// Rebuild the picker so the list reflects the deletion immediately.
	filter := m.pick.filter
	cmd := m.openSessionPicker()
	if m.mode == pickerSession {
		m.pick.filter = filter
		m.pick.refilter()
	}
	return tea.Batch(cmd, m.setStatus("deleted: "+item.title, false))
}

// retry regenerates the last reply from the same prompt.
func (m *Model) retry() tea.Cmd {
	if m.streaming {
		return m.setStatus("already generating", false)
	}
	if _, ok := m.sess.DropAfterLastUser(); !ok {
		return m.setStatus("nothing to retry yet", false)
	}
	m.editFrom = -1
	m.rebuildCache()
	m.refreshViewport(true)
	return m.startStream()
}

// runSearch queries the server's provider directly and folds the hits into the
// conversation, so a follow-up question can build on them.
//
// The model can already search on its own; this exists for when you want a
// specific lookup to definitely happen rather than hoping a small model
// decides to reach for the tool.
func (m *Model) runSearch(query string) tea.Cmd {
	c := m.client
	return tea.Batch(
		m.setStatus("searching: "+query, false),
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			results, err := c.Search(ctx, query, 5)
			return searchDoneMsg{query: query, results: results, err: err}
		},
	)
}

// applySearch records the results as conversation context and shows them.
func (m *Model) applySearch(msg searchDoneMsg) tea.Cmd {
	if msg.err != nil {
		return m.setStatus("search failed: "+msg.err.Error(), true)
	}
	if len(msg.results) == 0 {
		return m.setStatus("no results for "+msg.query, false)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Web results for %q:\n\n", msg.query)
	for i, r := range msg.results {
		fmt.Fprintf(&b, "%d. [%s](%s)\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
	}

	m.sess.Append(client.Message{Role: client.RoleSystem, Content: b.String()})
	m.cacheLast()
	m.refreshViewport(true)
	return tea.Batch(m.persist(),
		m.setStatus(fmt.Sprintf("%d results added to the conversation", len(msg.results)), false))
}

func (m *Model) copyLastReply() tea.Cmd {
	msg, ok := m.sess.LastAssistant()
	if !ok {
		return m.setStatus("no reply to copy yet", false)
	}
	if err := clipboard.WriteAll(msg.Content); err != nil {
		return m.setStatus("clipboard unavailable: "+err.Error(), true)
	}
	return m.setStatus(fmt.Sprintf("copied %d characters", len(msg.Content)), false)
}

// quit saves everything worth keeping, then exits.
func (m *Model) quit() tea.Cmd {
	m.quitting = true
	if m.streaming {
		m.stopStream()
	}
	m.discardAttachments()
	_ = m.store.Save(m.sess)
	m.rememberModel()
	// ctrl+c works from anywhere — including the opening, while the
	// connection is still being raced. There is no client yet then, and
	// nothing worth remembering about a route that never existed.
	if m.client != nil {
		_ = m.client.RememberRoute(m.profiles)
	}
	return tea.Quit
}

// stats reports the connection and the last reply's numbers.
func (m *Model) stats() tea.Cmd {
	route := m.client.Route()
	parts := []string{
		"server " + m.profileName,
		route.Describe(),
		"model " + m.sess.Model,
		fmt.Sprintf("%d messages", len(m.sess.Messages)),
	}
	if m.lastUsage != nil {
		parts = append(parts, fmt.Sprintf("last reply %d→%d tokens in %s",
			m.lastUsage.PromptTokens, m.lastUsage.CompletionTokens,
			m.lastElapsed.Round(time.Millisecond)))
	}
	return m.setStatus(strings.Join(parts, " · "), false)
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGT"[exp])
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2 Jan")
	}
}
