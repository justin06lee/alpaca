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
			m.rebuildCache()
			m.refreshViewport(true)
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
	// The attachment popup is modal: its keys must not fall through and type
	// into the composer underneath it.
	if m.viewAttach >= 0 {
		return m, m.handleAttachViewKey(msg)
	}
	if m.attachFocus >= 0 {
		return m.handleChipKey(msg)
	}

	// A bracketed paste arrives as one big KeyRunes. It is always handled
	// here rather than fed to the textarea: oversized ones become chips, and
	// even small ones need their carriage returns normalised.
	if msg.Paste && msg.Type == tea.KeyRunes {
		if m.streaming {
			return m, nil
		}
		return m, m.handlePaste(string(msg.Runes))
	}

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
		if len(m.attachments) > 0 {
			m.attachments = nil
			m.attachFocus = -1
			return m, m.setStatus("attachments discarded", false)
		}
		return m, nil

	case tea.KeyUp:
		// Up from the top of the text climbs onto the attachment chips.
		if len(m.attachments) > 0 && m.input.Line() == 0 && m.input.LineInfo().RowOffset == 0 {
			m.attachFocus = 0
			return m, nil
		}
		// Otherwise the textarea below moves its own cursor.

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

	case tea.KeyCtrlG:
		return m, m.copyLastCodeBlock()

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

// wheelStep is how many transcript lines one wheel notch moves. Three matches
// what most terminal applications do, fast enough to travel and slow enough
// to track.
const wheelStep = 3

// handleMouse gives the mouse its obvious meanings: the wheel scrolls the
// transcript, the open picker, or the attachment popup; a left click is
// checked against the copy control on code block headers. Everything else —
// drags, other buttons — is deliberately ignored.
func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if !m.splashDone || m.showHelp {
		return nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.viewAttach >= 0 {
			m.attachScroll = maxInt(0, m.attachScroll-wheelStep)
			return nil
		}
		if m.mode != pickerNone {
			m.pick.move(-1)
			return nil
		}
		m.viewport.LineUp(wheelStep)
	case tea.MouseButtonWheelDown:
		if m.viewAttach >= 0 {
			m.attachScroll += wheelStep // clamped against the content when rendered
			return nil
		}
		if m.mode != pickerNone {
			m.pick.move(1)
			return nil
		}
		m.viewport.LineDown(wheelStep)
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress || m.mode != pickerNone || m.viewAttach >= 0 {
			return nil
		}
		return m.clickTranscript(msg.X, msg.Y)
	}
	return nil
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
