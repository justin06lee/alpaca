package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// runSlash executes a /command typed into the composer.
//
// Slash commands duplicate the key bindings on purpose: keys are faster once
// learned, but a discoverable typed command is what a new user reaches for.
func (m *Model) runSlash(line string) tea.Cmd {
	command, rest, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	rest = strings.TrimSpace(rest)

	switch strings.ToLower(command) {

	case "help", "h", "?":
		m.showHelp = true
		return nil

	case "quit", "q", "exit":
		return m.quit()

	case "new", "n":
		return m.newSession()

	case "sessions", "chats", "s":
		return m.openSessionPicker()

	case "model", "m":
		if rest == "" {
			return m.openModelPicker()
		}
		return m.setModelByName(rest)

	case "system":
		return m.setSystem(rest)

	case "retry", "r", "regen":
		return m.retry()

	case "copy", "y":
		return m.copyLastReply()

	case "clear":
		return m.clearMessages()

	case "search", "web":
		if rest == "" {
			return m.setStatus("usage: /search <query>", true)
		}
		return m.runSearch(rest)

	case "stats", "info":
		return m.stats()

	default:
		return m.setStatus(fmt.Sprintf("unknown command /%s — try /help", command), true)
	}
}

// setModelByName resolves a partial model name, so "/model llama" works without
// typing the full "llama3.2:latest".
func (m *Model) setModelByName(want string) tea.Cmd {
	want = strings.ToLower(want)

	var exact, prefix, contains string
	for _, mod := range m.models {
		id := strings.ToLower(mod.ID)
		switch {
		case id == want:
			exact = mod.ID
		case strings.HasPrefix(id, want) && prefix == "":
			prefix = mod.ID
		case strings.Contains(id, want) && contains == "":
			contains = mod.ID
		}
	}

	chosen := exact
	if chosen == "" {
		chosen = prefix
	}
	if chosen == "" {
		chosen = contains
	}
	if chosen == "" {
		return m.setStatus(fmt.Sprintf("no model matching %q — ctrl+p to see the list", want), true)
	}

	m.sess.Model = chosen
	m.rememberModel()
	m.rebuildCache()
	m.refreshViewport(true)
	return tea.Batch(m.persist(), m.setStatus("model: "+chosen, false))
}

// setSystem changes the system prompt; an empty argument clears it.
func (m *Model) setSystem(text string) tea.Cmd {
	m.sess.System = text
	m.refreshViewport(true)

	if text == "" {
		return tea.Batch(m.persist(), m.setStatus("system prompt cleared", false))
	}
	return tea.Batch(m.persist(), m.setStatus("system prompt set ("+fmt.Sprint(len(text))+" chars)", false))
}

// clearMessages empties the current chat but keeps the model and system prompt.
func (m *Model) clearMessages() tea.Cmd {
	if m.streaming {
		m.stopStream()
	}
	m.sess.Messages = nil
	m.sess.Title = ""
	m.rendered = m.rendered[:0]
	m.lastUsage = nil
	m.lastElapsed = 0
	m.refreshViewport(true)
	return tea.Batch(m.persist(), m.setStatus("chat cleared", false))
}
