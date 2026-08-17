package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/justin06lee/alpaca/internal/client"
)

// Editing and branching. Clicking one of your own bubbles opens it in the
// popup; e there loads the prompt back into the composer, and sending it
// branches the conversation at that point — the old continuation survives as a
// sibling variant, and ←/→ in the popup walk between them.

// editMessage begins an edit of sent prompt i: its text returns to the
// composer, and the transcript stays intact until the edit is actually sent.
func (m *Model) editMessage(i int) tea.Cmd {
	if m.streaming {
		return m.setStatus("wait for the reply to finish", true)
	}
	if i < 0 || i >= len(m.sess.Messages) || m.sess.Messages[i].Role != client.RoleUser {
		return nil
	}
	m.viewMsg = -1
	m.editFrom = i
	m.input.SetValue(m.sess.Messages[i].Content)
	m.input.Focus()
	return m.setStatus("editing — enter sends a new branch, esc cancels", false)
}

// cancelEdit abandons an edit, leaving the conversation exactly as it was.
func (m *Model) cancelEdit() tea.Cmd {
	m.editFrom = -1
	m.input.Reset()
	return m.setStatus("edit cancelled", false)
}

// switchVariant swaps the conversation from message i onward for the sibling
// branch delta steps away.
func (m *Model) switchVariant(i, delta int) tea.Cmd {
	if m.streaming {
		return m.setStatus("wait for the reply to finish", true)
	}
	if !m.sess.SwitchVariant(i, delta) {
		return nil
	}
	// The tail below the fork is a different conversation now; nothing
	// measured on the old one still applies.
	m.lastUsage = nil
	m.lastElapsed = 0
	m.attachScroll = 0
	m.rebuildCache()
	m.refreshViewport(true)
	k, n := m.sess.Variants(i)
	return tea.Batch(m.persist(), m.setStatus(fmt.Sprintf("branch %d of %d", k, n), false))
}
