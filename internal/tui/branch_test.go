package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/justin06lee/alpaca/internal/client"
)

// branchedModel is a ready model whose second prompt has two variants.
func branchedModel(t *testing.T) *Model {
	t.Helper()
	m := readyModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "first question"})
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "first answer"})
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "original prompt"})
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "original answer"})
	m.sess.Rebase(2)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "edited prompt"})
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "edited answer"})
	m.rebuildCache()
	m.refreshViewport(true)
	return m
}

// e in the message popup pulls the prompt into the composer and arms the
// branch, without touching the transcript yet.
func TestEditKeyLoadsThePromptIntoTheComposer(t *testing.T) {
	m := readyModel(t)
	m.sess.Append(client.Message{Role: client.RoleUser, Content: "the prompt to edit"})
	m.sess.Append(client.Message{Role: client.RoleAssistant, Content: "its answer"})
	m.rebuildCache()

	m.viewMsg = 0
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})

	if m.viewMsg != -1 {
		t.Error("popup stayed open after e")
	}
	if m.editFrom != 0 {
		t.Fatalf("editFrom = %d, want 0", m.editFrom)
	}
	if got := m.input.Value(); got != "the prompt to edit" {
		t.Errorf("composer = %q, want the prompt", got)
	}
	// Nothing sent yet, so the conversation is untouched.
	if len(m.sess.Messages) != 2 {
		t.Errorf("edit truncated the transcript before send: %d messages", len(m.sess.Messages))
	}

	// esc abandons the edit and empties the composer.
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.editFrom != -1 || m.input.Value() != "" {
		t.Errorf("esc did not cancel the edit: editFrom=%d input=%q", m.editFrom, m.input.Value())
	}
}

// A branched prompt renders its star and variant counter under the bubble.
func TestBranchedPromptWearsItsMarker(t *testing.T) {
	m := branchedModel(t)
	rendered := stripANSI(m.renderMessage(2, m.sess.Messages[2]))

	if !strings.Contains(rendered, "✦") {
		t.Errorf("branched prompt has no star:\n%s", rendered)
	}
	if !strings.Contains(rendered, "‹ 2/2 ›") {
		t.Errorf("branched prompt has no variant counter:\n%s", rendered)
	}

	// The unbranched first prompt stays plain.
	plain := stripANSI(m.renderMessage(0, m.sess.Messages[0]))
	if strings.Contains(plain, "✦") || strings.Contains(plain, "‹") {
		t.Errorf("unbranched prompt grew a marker:\n%s", plain)
	}
}

// ←/→ in the popup swap the tail of the conversation between variants.
func TestPopupArrowsSwitchBranches(t *testing.T) {
	m := branchedModel(t)
	m.viewMsg = 2

	m.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
	if got := m.sess.Messages[2].Content; got != "original prompt" {
		t.Fatalf("after ←, message 2 = %q, want the original", got)
	}
	if got := m.sess.Messages[3].Content; got != "original answer" {
		t.Fatalf("after ←, message 3 = %q, want the original answer", got)
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyRight})
	if got := m.sess.Messages[2].Content; got != "edited prompt" {
		t.Fatalf("after →, message 2 = %q, want the edit", got)
	}

	// The popup title carries the counter, so you can see where you are.
	view := stripANSI(m.messagePopover(m.chatView()))
	if !strings.Contains(view, "‹ 2/2 ›") {
		t.Errorf("popup title has no variant counter:\n%s", view)
	}
}

// Streaming locks branching: a reply in flight must land on the branch that
// asked for it.
func TestBranchingIsLockedWhileStreaming(t *testing.T) {
	m := branchedModel(t)
	m.streaming = true

	m.viewMsg = 2
	m.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
	if got := m.sess.Messages[2].Content; got != "edited prompt" {
		t.Errorf("← switched branches mid-stream to %q", got)
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if m.editFrom != -1 {
		t.Error("e armed an edit mid-stream")
	}
}
