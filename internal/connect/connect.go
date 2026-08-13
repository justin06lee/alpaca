// Package connect encodes everything a client needs to reach a server into one
// pasteable token.
//
// The whole point of alpaca's setup story is that a user runs `alpaca serve`
// once, copies a single string, and pastes it on every other machine. That
// string therefore has to carry the API key, the pinned certificate
// fingerprint, and every known route (LAN hints plus the public fallback) —
// because asking a user to transcribe four separate values is how "it just
// works" turns into "it almost works".
package connect

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// Scheme prefixes every connect string so it is self-identifying when it shows
// up in a chat message or a note, and so the format can be versioned later.
const Scheme = "alpaca1"

// Bundle is the payload of a connect string. Field names are single letters
// because the encoded form is meant to be copied by hand.
type Bundle struct {
	ID          string   `json:"i"`
	Name        string   `json:"n,omitempty"`
	Key         string   `json:"k"`
	Fingerprint string   `json:"f,omitempty"`
	LAN         []string `json:"l,omitempty"`
	Public      string   `json:"p,omitempty"`
}

// Encode renders the bundle as a connect string.
func (b Bundle) Encode() (string, error) {
	if err := b.validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("encode connect bundle: %w", err)
	}
	return Scheme + ":" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode parses a connect string.
//
// Pasted tokens routinely pick up damage that is not the user's fault: a
// terminal hard-wraps the line, a chat client inserts a newline, a shell
// prompt adds surrounding spaces. All whitespace is stripped before decoding
// so those cases succeed rather than producing a baffling error.
func Decode(s string) (*Bundle, error) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return nil, fmt.Errorf("empty connect string")
	}

	prefix, payload, found := strings.Cut(s, ":")
	if !found {
		return nil, fmt.Errorf("not a connect string: expected it to start with %q", Scheme+":")
	}
	if prefix != Scheme {
		return nil, fmt.Errorf("unsupported connect string format %q (this build understands %q)", prefix, Scheme)
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("connect string is damaged — copy the whole line including the %q prefix", Scheme+":")
	}

	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("connect string is damaged or truncated: %w", err)
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	return &b, nil
}

func (b Bundle) validate() error {
	switch {
	case b.ID == "":
		return fmt.Errorf("connect string is missing the server id")
	case b.Key == "":
		return fmt.Errorf("connect string is missing the api key")
	case len(b.LAN) == 0 && b.Public == "":
		return fmt.Errorf("connect string carries no endpoints")
	}
	return nil
}
