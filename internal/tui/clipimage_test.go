package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// stageClipImage stages path as a clipboard-style image chip, the way
// attachClipboard does once clipboardImage has produced a file.
func stageClipImage(t *testing.T, m *Model, path string) {
	t.Helper()
	att, err := newImageAttachment(path)
	if err != nil {
		t.Fatal(err)
	}
	att.temp = true
	att.name = "clipboard 8×8"
	m.attachments = append(m.attachments, att)
}

// A clipboard image lives in a temp file this app made, so removing its chip
// must remove the file; a dropped file is the user's own and must survive.
func TestClipboardTempFileFollowsItsChip(t *testing.T) {
	m := readyModel(t)
	temp := writeTestPNG(t, t.TempDir())
	stageClipImage(t, m, temp)

	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if _, err := os.Stat(temp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp file survived its chip's deletion: %v", err)
	}

	dropped := writeTestPNG(t, t.TempDir())
	att, err := newImageAttachment(dropped)
	if err != nil {
		t.Fatal(err)
	}
	m.attachments = append(m.attachments, att)
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if _, err := os.Stat(dropped); err != nil {
		t.Errorf("dropped file was deleted with its chip: %v", err)
	}
}

// Sending folds the image in as a note and cleans the temp file up.
func TestSendCleansUpClipboardTempFiles(t *testing.T) {
	m := readyModel(t)
	temp := writeTestPNG(t, t.TempDir())
	stageClipImage(t, m, temp)

	out := m.composeOutgoing("look at this")
	if !strings.Contains(out, "clipboard 8×8") {
		t.Errorf("outgoing message does not name the image:\n%s", out)
	}
	if len(m.attachments) != 0 {
		t.Errorf("%d attachments left after send, want 0", len(m.attachments))
	}
	if _, err := os.Stat(temp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp file survived the send: %v", err)
	}
}

// The image popup offers o — the full-resolution escape hatch.
func TestImagePopupOffersOpen(t *testing.T) {
	m := readyModel(t)
	stageClipImage(t, m, writeTestPNG(t, t.TempDir()))

	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	view := stripANSI(m.View())
	if !strings.Contains(view, "o open full-res") {
		t.Errorf("image popup footer does not offer o:\n%s", view)
	}
	if !strings.Contains(view, "clipboard 8×8") {
		t.Errorf("image popup title does not name the image:\n%s", view)
	}
}

// clipBytesToFile must keep only real PNG bytes: both Linux clipboard tools
// print a human-readable error to stdout when the clipboard has no image.
func TestClipBytesToFileRejectsNonPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.png")
	if err := clipBytesToFile(path, "echo", "no image available"); !errors.Is(err, errNoClipImage) {
		t.Errorf("error text accepted as an image: %v", err)
	}

	src := writeTestPNG(t, t.TempDir())
	if err := clipBytesToFile(path, "cat", src); err != nil {
		t.Errorf("real PNG rejected: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("PNG not written: %v", err)
	}
}
