//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock, blocking until it is free, and
// returns the function that releases it. The lock lives in a sidecar file so
// it survives the atomic rename that replaces the real one.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		// Closing releases the flock with it; the explicit unlock just makes
		// the intent visible.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
