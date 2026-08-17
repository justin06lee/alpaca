package main

import (
	"os"
	"testing"
)

// /dev/null is a character device, which a file-mode sniff happily calls a
// terminal. Anything launched with its stdio redirected there would then be
// handed a TUI it has no way to draw.
func TestIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close()

	if isTerminal(f) {
		t.Error("isTerminal(/dev/null) = true, which would start the tui with nowhere to draw")
	}
}
