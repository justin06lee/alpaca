package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDirHonoursAlpacaHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ALPACA_HOME", home)

	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if dir != home {
		t.Errorf("Dir() = %q, want %q", dir, home)
	}
}

func TestWriteJSONIsAtomicAndPrivate(t *testing.T) {
	t.Setenv("ALPACA_HOME", t.TempDir())
	path, err := Path("secrets.json")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}

	if err := writeJSON(path, map[string]string{"key": "alp_secret"}); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600 — this file holds the API key", perm)
	}

	// No temp file may survive a successful write.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s after a successful write", e.Name())
		}
	}

	var got map[string]string
	found, err := readJSON(path, &got)
	if err != nil || !found {
		t.Fatalf("readJSON: found=%v err=%v", found, err)
	}
	if got["key"] != "alp_secret" {
		t.Errorf("round trip lost data: %v", got)
	}
}

func TestReadJSONMissingFileIsNotAnError(t *testing.T) {
	t.Setenv("ALPACA_HOME", t.TempDir())
	var v struct{}
	found, err := readJSON(filepath.Join(t.TempDir(), "absent.json"), &v)
	if err != nil {
		t.Errorf("missing file returned error %v, want nil", err)
	}
	if found {
		t.Error("missing file reported as found")
	}
}

func TestSubdirCreatesTheDirectoryItself(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ALPACA_HOME", home)

	dir, err := Subdir("certs")
	if err != nil {
		t.Fatalf("Subdir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Subdir did not create the directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("permissions = %o, want 700", perm)
	}
}

func TestNewAPIKeyFormat(t *testing.T) {
	key, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if !strings.HasPrefix(key, "alp_") {
		t.Errorf("key %q lacks the alp_ prefix", key)
	}
	if len(key) < 4+32 {
		t.Errorf("key %q is too short for 24 bytes of entropy", key)
	}
	second, _ := NewAPIKey()
	if key == second {
		t.Error("two keys came out identical")
	}
}

func TestUniqueNameSuffixesCollisions(t *testing.T) {
	p := &Profiles{Entries: map[string]*Profile{
		"box":   {},
		"box-2": {},
	}}
	if got := p.uniqueName("box"); got != "box-3" {
		t.Errorf("uniqueName = %q, want box-3", got)
	}
	if got := p.uniqueName("fresh"); got != "fresh" {
		t.Errorf("uniqueName = %q, want it untouched", got)
	}
	if got := p.uniqueName("  "); got != "alpaca" {
		t.Errorf("uniqueName of blank = %q, want the fallback name", got)
	}
}

func TestGetFallbackChain(t *testing.T) {
	empty := &Profiles{Entries: map[string]*Profile{}}
	if _, err := empty.Get(""); err == nil || !strings.Contains(err.Error(), "alpaca link") {
		t.Errorf("empty store error = %v, want a pointer at `alpaca link`", err)
	}

	sole := &Profiles{Entries: map[string]*Profile{"only": {Name: "only"}}}
	prof, err := sole.Get("")
	if err != nil || prof.Name != "only" {
		t.Errorf("sole entry: got %v, %v — want the unambiguous choice", prof, err)
	}

	multi := &Profiles{
		Default: "b",
		Entries: map[string]*Profile{"a": {Name: "a"}, "b": {Name: "b"}},
	}
	if prof, err := multi.Get(""); err != nil || prof.Name != "b" {
		t.Errorf("default fallback: got %v, %v", prof, err)
	}
	if _, err := multi.Get("nope"); err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Errorf("unknown name error = %v, want it to list what exists", err)
	}
}

func TestRemoveReassignsDefault(t *testing.T) {
	p := &Profiles{
		Default: "gone",
		Entries: map[string]*Profile{"gone": {}, "stays": {}},
	}
	if !p.Remove("gone") {
		t.Fatal("Remove reported the profile missing")
	}
	if p.Default != "stays" {
		t.Errorf("Default = %q, want it reassigned to the survivor", p.Default)
	}
	if p.Remove("gone") {
		t.Error("removing twice reported success")
	}
}

func TestAddDefaultsTheFirstProfile(t *testing.T) {
	p := &Profiles{Entries: map[string]*Profile{}}
	name := p.Add(&Profile{Name: "box"})
	if name != "box" || p.Default != "box" {
		t.Errorf("first Add: name=%q default=%q", name, p.Default)
	}
	second := p.Add(&Profile{Name: "box"})
	if second != "box-2" {
		t.Errorf("colliding Add stored as %q, want box-2", second)
	}
	if p.Default != "box" {
		t.Errorf("Default moved to %q on a later Add", p.Default)
	}
}

// The reason UpdateProfiles exists: concurrent read-modify-write cycles must
// not lose each other's updates. Without the lock this test drops writes.
func TestUpdateProfilesSerialisesWriters(t *testing.T) {
	t.Setenv("ALPACA_HOME", t.TempDir())

	if _, err := UpdateProfiles(func(p *Profiles) error {
		p.Entries["box"] = &Profile{Name: "box"}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const writers = 8
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := UpdateProfiles(func(p *Profiles) error {
				p.Entries["box"].LAN = append(p.Entries["box"].LAN, string(rune('a'+n)))
				return nil
			})
			if err != nil {
				t.Errorf("writer %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	final, err := LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if got := len(final.Entries["box"].LAN); got != writers {
		t.Errorf("%d of %d concurrent updates survived — writers clobbered each other", got, writers)
	}
}

// A failing mutation must leave the store untouched.
func TestUpdateProfilesDoesNotSaveOnError(t *testing.T) {
	t.Setenv("ALPACA_HOME", t.TempDir())

	if _, err := UpdateProfiles(func(p *Profiles) error {
		p.Default = "before"
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := UpdateProfiles(func(p *Profiles) error {
		p.Default = "after"
		return json.Unmarshal([]byte("{"), &struct{}{}) // any error
	})
	if err == nil {
		t.Fatal("UpdateProfiles swallowed the mutation error")
	}

	final, _ := LoadProfiles()
	if final.Default != "before" {
		t.Errorf("Default = %q after a failed update, want %q", final.Default, "before")
	}
}
