package config

import (
	"fmt"
	"sort"
	"strings"
)

const profilesFile = "profiles.json"

// Profile is everything a client machine needs to reach one alpaca server.
// It is created by `alpaca link <connect-string>` and updated as the client
// learns better routes.
type Profile struct {
	// ID matches the server's identity, so mDNS results can be filtered to
	// this exact server rather than whatever else is on the network.
	ID   string `json:"id"`
	Name string `json:"name"`
	// APIKey authenticates to the server.
	APIKey string `json:"api_key"`
	// Fingerprint is the SHA-256 of the server's self-signed certificate,
	// pinned by the client so the TLS fallback needs no certificate authority.
	Fingerprint string `json:"fingerprint,omitempty"`
	// LAN holds host:port hints captured when the connect string was issued.
	// They go stale across DHCP leases, which is why mDNS exists.
	LAN []string `json:"lan,omitempty"`
	// Public is the internet-reachable host:port, when the server managed to
	// map one.
	Public string `json:"public,omitempty"`

	// LastGood is the endpoint that most recently answered, tried first on the
	// next launch so the common case skips discovery entirely.
	LastGood string `json:"last_good,omitempty"`
	// LastGoodTLS records whether LastGood needed TLS.
	LastGoodTLS bool `json:"last_good_tls,omitempty"`

	// Model and System persist the user's last choices for this server.
	Model  string `json:"model,omitempty"`
	System string `json:"system,omitempty"`
	// GraphModel is the model /graph summarizes conversations with; empty
	// means the session's chat model does double duty.
	GraphModel string `json:"graph_model,omitempty"`
}

// Profiles is the client-side profile store.
type Profiles struct {
	Default string              `json:"default"`
	Entries map[string]*Profile `json:"profiles"`
}

// LoadProfiles reads the profile store, returning an empty one if none exists.
func LoadProfiles() (*Profiles, error) {
	path, err := Path(profilesFile)
	if err != nil {
		return nil, err
	}
	profiles := &Profiles{Entries: map[string]*Profile{}}
	if _, err := readJSON(path, profiles); err != nil {
		return nil, err
	}
	if profiles.Entries == nil {
		profiles.Entries = map[string]*Profile{}
	}
	return profiles, nil
}

// Save persists the profile store as-is. Callers that read, mutate, and write
// back should go through UpdateProfiles instead, which holds the lock across
// the whole cycle.
func (p *Profiles) Save() error {
	path, err := Path(profilesFile)
	if err != nil {
		return err
	}
	if err := writeJSON(path, p); err != nil {
		return fmt.Errorf("save profiles: %w", err)
	}
	return nil
}

// UpdateProfiles reloads the store, applies fn, and saves the result, all under
// an exclusive advisory lock.
//
// The individual write was already atomic; this closes the read-modify-write
// gap around it. Two alpaca processes on one machine is an ordinary situation —
// a chat recording its route while a link runs in another terminal — and
// without the lock the slower writer would resurrect whatever state it loaded
// before the faster one saved.
func UpdateProfiles(fn func(*Profiles) error) (*Profiles, error) {
	path, err := Path(profilesFile)
	if err != nil {
		return nil, err
	}
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()

	profiles, err := LoadProfiles()
	if err != nil {
		return nil, err
	}
	if err := fn(profiles); err != nil {
		return nil, err
	}
	if err := profiles.Save(); err != nil {
		return nil, err
	}
	return profiles, nil
}

// Add stores a profile under a unique name and makes it the default when it is
// the only one. The stored name is returned, which may differ from the
// requested one if a distinct server already claimed it.
func (p *Profiles) Add(prof *Profile) string {
	name := p.uniqueName(prof.Name)
	prof.Name = name
	p.Entries[name] = prof
	if p.Default == "" || len(p.Entries) == 1 {
		p.Default = name
	}
	return name
}

// uniqueName suffixes a name until it is free. Re-linking an existing server is
// handled by ByID before this is reached, so any collision here is a genuinely
// different server that happens to share a hostname.
func (p *Profiles) uniqueName(want string) string {
	want = strings.TrimSpace(want)
	if want == "" {
		want = "alpaca"
	}
	if _, taken := p.Entries[want]; !taken {
		return want
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", want, i)
		if _, taken := p.Entries[candidate]; !taken {
			return candidate
		}
	}
}

// ByID finds an already-linked profile for a server ID, so re-linking the same
// server updates it in place instead of creating a duplicate.
func (p *Profiles) ByID(id string) (*Profile, bool) {
	if id == "" {
		return nil, false
	}
	for _, prof := range p.Entries {
		if prof.ID == id {
			return prof, true
		}
	}
	return nil, false
}

// Get resolves a profile by name, falling back to the default when name is
// empty. It reports a helpful error listing what is available.
func (p *Profiles) Get(name string) (*Profile, error) {
	if len(p.Entries) == 0 {
		return nil, fmt.Errorf("no servers linked yet — run `alpaca link <connect-string>` " +
			"with the string printed by `alpaca serve`")
	}
	if name == "" {
		name = p.Default
	}
	if name == "" && len(p.Entries) == 1 {
		// No default recorded but only one candidate: the choice is unambiguous.
		for _, prof := range p.Entries {
			return prof, nil
		}
	}
	if prof, ok := p.Entries[name]; ok {
		return prof, nil
	}
	return nil, fmt.Errorf("no profile named %q (have: %s)", name, strings.Join(p.Names(), ", "))
}

// Names lists profile names in stable sorted order.
func (p *Profiles) Names() []string {
	names := make([]string, 0, len(p.Entries))
	for name := range p.Entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Remove deletes a profile, reassigning the default if it pointed there.
func (p *Profiles) Remove(name string) bool {
	if _, ok := p.Entries[name]; !ok {
		return false
	}
	delete(p.Entries, name)
	if p.Default == name {
		p.Default = ""
		if names := p.Names(); len(names) > 0 {
			p.Default = names[0]
		}
	}
	return true
}
