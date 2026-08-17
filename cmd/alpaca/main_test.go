package main

import (
	"os"
	"testing"
)

// A bundled launch is the case worth pinning: it arrives with no terminal on
// either end, and it used to be mistaken for a shell pipeline — which exited 2
// into a discarded stdout, so clicking the app looked exactly like a crash.
func TestBareCommandPicksTheSurface(t *testing.T) {
	tests := []struct {
		name                string
		stdinTTY, stdoutTTY bool
		want                string
	}{
		{"terminal on both ends runs the tui", true, true, "chat"},
		{"finder, dock, or a desktop launcher opens the window", false, false, "gui"},
		{"piped input is neither surface", false, true, ""},
		{"redirected output is neither surface", true, false, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bareCommand(tc.stdinTTY, tc.stdoutTTY); got != tc.want {
				t.Errorf("bareCommand(%v, %v) = %q, want %q",
					tc.stdinTTY, tc.stdoutTTY, got, tc.want)
			}
		})
	}
}

// /dev/null is a character device, which a file-mode sniff happily calls a
// terminal — and it is exactly what a bundled app is handed for stdin.
func TestIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close()

	if isTerminal(f) {
		t.Error("isTerminal(/dev/null) = true, which routes a desktop launch into the tui")
	}
}
