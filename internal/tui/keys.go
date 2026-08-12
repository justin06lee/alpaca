package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Quit works from anywhere, including mid-stream.
	if msg.Type == tea.KeyCtrlC {
		return m, m.quit()
	}

	// Nobody should have to sit through an animation.
	if !m.splashDone {
		m.splashDone = true
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
