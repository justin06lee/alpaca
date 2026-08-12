package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Quit works from anywhere, including mid-stream.
	if msg.Type == tea.KeyCtrlC {
		return m, m.quit()
	}

	// Nobody should have to sit through an animation, but the skip cannot be
	// hair-triggered either. A terminal answers the queries a TUI makes on
	// startup — the background-colour probe replies with an OSC 11 sequence,
	// for one — and those replies arrive as input a few milliseconds in. Taking
	// any of them as "the user pressed a key" dismissed the opening instantly
	// on every real terminal, while a bare pty, which answers nothing, looked
	// fine. Ignoring input for the first few frames outlasts that chatter and
	// costs a deliberate keypress nothing anyone can perceive.
	if !m.splashDone {
		// Not skippable until the connection lands: the opening doubles as the
		// loading screen, and dismissing it early would hand over an interface
		// with no server behind it.
		if m.client != nil && m.splashScan >= splashGrace && isDeliberateKey(msg) {
			m.splashDone = true
		}
		return m, nil
	}
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}
	if m.mode != pickerNone {
		return m.handlePickerKey(msg)
	}
	return m.handleChatKey(msg)
}

func (m *Model) handleChatKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {

	case tea.KeyEsc:
		if m.streaming {
			m.stopStream()
			return m, nil
		}
		if m.input.Value() != "" {
			m.input.Reset()
			return m, nil
		}
		return m, nil

	case tea.KeyEnter:
		if m.streaming {
			return m, nil // the composer is not accepting input right now
		}
		return m, m.send()

	// Enter is "send", so newline needs its own key. ctrl+j is the terminal's
	// literal line feed and works everywhere, unlike shift+enter which most
	// terminals never transmit.
	case tea.KeyCtrlJ:
		m.input.InsertString("\n")
		return m, nil

	case tea.KeyCtrlN:
		return m, m.newSession()

	case tea.KeyCtrlP:
		return m, m.openModelPicker()

	case tea.KeyCtrlS:
		return m, m.openSessionPicker()

	case tea.KeyCtrlR:
		return m, m.retry()

	case tea.KeyCtrlY:
		return m, m.copyLastReply()

	case tea.KeyPgUp:
		m.viewport.ViewUp()
		return m, nil

	case tea.KeyPgDown:
		m.viewport.ViewDown()
		return m, nil

	case tea.KeyCtrlU:
		m.viewport.HalfViewUp()
		return m, nil

	case tea.KeyCtrlD:
		m.viewport.HalfViewDown()
		return m, nil
	}

	// "?" opens help, but only when it cannot be part of a message being typed.
	if msg.String() == "?" && m.input.Value() == "" {
		m.showHelp = true
		return m, nil
	}

	if m.streaming {
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {

	case tea.KeyEsc:
		m.mode = pickerNone
		return m, nil

	case tea.KeyUp:
		m.pick.move(-1)
		return m, nil

	case tea.KeyDown:
		m.pick.move(1)
		return m, nil

	case tea.KeyEnter:
		return m, m.choosePicked()

	case tea.KeyBackspace:
		m.pick.backspaceFilter()
		return m, nil

	case tea.KeyCtrlD:
		if m.pick.kind == pickerSession {
			return m, m.deletePicked()
		}
		return m, nil

	case tea.KeyRunes, tea.KeySpace:
		m.pick.appendFilter(string(msg.Runes))
		return m, nil
	}

	// ctrl+p / ctrl+n also navigate, matching readline habits.
	switch msg.String() {
	case "ctrl+p":
		m.pick.move(-1)
	case "ctrl+n":
		m.pick.move(1)
	}
	return m, nil
}

// isDeliberateKey reports whether a key looks like a person pressing something,
// as opposed to a terminal answering a query. Escape-sequence replies decode
// into assorted control keys, so the skip only honours the handful of keys
// somebody would actually reach for to dismiss a splash.
func isDeliberateKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace, tea.KeyEnter, tea.KeyEsc:
		return true
	default:
		return false
	}
}
