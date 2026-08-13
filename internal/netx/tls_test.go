package netx

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateIdentityPersistsWithTightPermissions(t *testing.T) {
	dir := t.TempDir()
	id, err := CreateIdentity(dir, []string{"127.0.0.1", "example.local"})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if len(id.Fingerprint) != 64 {
		t.Errorf("fingerprint = %q, want 64 hex chars", id.Fingerprint)
	}
	for _, name := range []string{"server.crt", "server.key"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s permissions = %o, want 600", name, perm)
		}
	}
}

func TestLoadOrCreateIdentityReusesExistingCert(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateIdentity(dir, []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("first LoadOrCreateIdentity: %v", err)
	}
	// A second boot — possibly with different hosts — must return the same
	// identity, because the fingerprint is pinned in circulating connect strings.
	second, err := LoadOrCreateIdentity(dir, []string{"10.0.0.99"})
	if err != nil {
		t.Fatalf("second LoadOrCreateIdentity: %v", err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Errorf("fingerprint changed across restarts: %s -> %s", first.Fingerprint, second.Fingerprint)
	}
}

// Half an identity means something outside alpaca went wrong. Regenerating at
// that point would silently invalidate every pinned client, so it must be an
// error the user sees.
func TestLoadOrCreateIdentityRefusesHalfAnIdentity(t *testing.T) {
	for _, missing := range []string{"server.key", "server.crt"} {
		t.Run("missing "+missing, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := CreateIdentity(dir, []string{"127.0.0.1"}); err != nil {
				t.Fatalf("CreateIdentity: %v", err)
			}
			if err := os.Remove(filepath.Join(dir, missing)); err != nil {
				t.Fatalf("remove %s: %v", missing, err)
			}

			_, err := LoadOrCreateIdentity(dir, []string{"127.0.0.1"})
			if err == nil {
				t.Fatal("LoadOrCreateIdentity silently regenerated over a half-missing identity")
			}
			if !strings.Contains(err.Error(), "--rotate-cert") {
				t.Errorf("error = %v, want it to point at --rotate-cert", err)
			}
			// The surviving half must be untouched.
			for _, name := range []string{"server.crt", "server.key"} {
				if name == missing {
					continue
				}
				if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
					t.Errorf("surviving file %s was disturbed: %v", name, statErr)
				}
			}
		})
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	canonical := strings.Repeat("ab", 32)
	cases := []struct {
		name string
		in   string
	}{
		{"already canonical", canonical},
		{"uppercase", strings.ToUpper(canonical)},
		{"openssl colons", strings.ToUpper(strings.Join(splitPairs(canonical), ":"))},
		{"stray spaces", " " + canonical + " "},
	}
	for _, tc := range cases {
		if got := NormalizeFingerprint(tc.in); got != canonical {
			t.Errorf("%s: NormalizeFingerprint(%q) = %q, want %q", tc.name, tc.in, got, canonical)
		}
	}
}

// A pin pasted in openssl's colon-separated uppercase form must still open a
// connection to the right server.
func TestPinnedClientConfigAcceptsOpensslFormat(t *testing.T) {
	addr, fingerprint := serveBoth(t)

	openssl := strings.ToUpper(strings.Join(splitPairs(fingerprint), ":"))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedClientConfig(openssl)}}

	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("request with an openssl-format pin failed: %v", err)
	}
	resp.Body.Close()
}

func TestMissingSANs(t *testing.T) {
	dir := t.TempDir()
	id, err := CreateIdentity(dir, []string{"127.0.0.1", "example.local"})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	missing := id.MissingSANs([]string{"127.0.0.1", "example.local", "10.9.8.7", "other.host", ""})
	want := []string{"10.9.8.7", "other.host"}
	if len(missing) != len(want) {
		t.Fatalf("MissingSANs = %v, want %v", missing, want)
	}
	for i := range want {
		if missing[i] != want[i] {
			t.Errorf("MissingSANs[%d] = %q, want %q", i, missing[i], want[i])
		}
	}
}

func splitPairs(s string) []string {
	pairs := make([]string, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		pairs = append(pairs, s[i:i+2])
	}
	return pairs
}
