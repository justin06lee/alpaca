// Package config manages alpaca's on-disk state: the server's identity and
// API key, saved client profiles, TLS material, and chat sessions.
//
// Everything lives under a single directory (~/.config/alpaca by default).
// Files hold secrets, so directories are 0700 and files 0600, and every write
// goes through a temp-file-and-rename so a crash can't leave a half-written
// config behind.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// Dir returns alpaca's configuration directory, creating it if necessary.
//
// ALPACA_HOME overrides everything, which keeps tests hermetic and lets a user
// run two independent instances on one machine.
func Dir() (string, error) {
	if custom := os.Getenv("ALPACA_HOME"); custom != "" {
		return custom, ensureDir(custom)
	}

	var base string
	switch {
	case os.Getenv("XDG_CONFIG_HOME") != "":
		base = os.Getenv("XDG_CONFIG_HOME")
	case runtime.GOOS == "windows":
		var err error
		if base, err = os.UserConfigDir(); err != nil {
			return "", fmt.Errorf("locate config dir: %w", err)
		}
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}

	dir := filepath.Join(base, "alpaca")
	return dir, ensureDir(dir)
}

// Path resolves a path inside the config directory, creating any parent
// directories along the way.
func Path(elem ...string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	full := filepath.Join(append([]string{dir}, elem...)...)
	if err := ensureDir(filepath.Dir(full)); err != nil {
		return "", err
	}
	return full, nil
}

// Subdir resolves a directory inside the config directory, creating it (not
// just its parent, as Path does for files) along the way.
func Subdir(elem ...string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	full := filepath.Join(append([]string{dir}, elem...)...)
	return full, ensureDir(full)
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, dirPerm); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

// writeJSON atomically serializes v to path with 0600 permissions.
//
// The temp file is created in the destination directory so the rename stays on
// one filesystem, where it is atomic.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	data = append(data, '\n')

	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(filePerm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install %s: %w", filepath.Base(path), err)
	}
	return nil
}

// readJSON loads path into v. It reports whether the file existed; a missing
// file is not an error, so callers can fall back to defaults.
func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return true, nil
}

// NewAPIKey mints a bearer token. 24 random bytes is 192 bits of entropy,
// far beyond what a network attacker could grind against a rate-limited
// endpoint, and base64url keeps it copy-pasteable and shell-safe.
func NewAPIKey() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return "alp_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// NewID mints a short stable identifier for a server instance. It is not a
// secret; it only lets a client recognize "its" server among mDNS results.
func NewID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// DefaultName derives a friendly server name from the machine's hostname.
func DefaultName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "alpaca"
	}
	// Hostnames often arrive as "macbook.local" or "box.lan"; the suffix is noise.
	host = strings.TrimSuffix(host, ".local")
	host = strings.TrimSuffix(host, ".lan")
	return host
}
