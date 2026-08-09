package config

import "fmt"

const serverFile = "server.json"

// Server is the persistent identity of a machine running `alpaca serve`.
//
// It is generated once on first launch and then reused forever, so the connect
// strings a user has already pasted onto other machines keep working across
// restarts, IP changes, and upgrades.
type Server struct {
	// ID distinguishes this server among mDNS results. Public, not a secret.
	ID string `json:"id"`
	// Name is the human label shown in clients.
	Name string `json:"name"`
	// APIKey is the bearer token every request must present.
	APIKey string `json:"api_key"`
}

// LoadServer reads the server identity, creating and persisting a fresh one on
// first run. The second return value reports whether a new identity was minted,
// so `serve` can print a louder first-run banner.
func LoadServer() (*Server, bool, error) {
	path, err := Path(serverFile)
	if err != nil {
		return nil, false, err
	}

	var srv Server
	found, err := readJSON(path, &srv)
	if err != nil {
		return nil, false, err
	}
	// Treat a file missing any required field as a first run rather than
	// failing: a truncated or hand-edited config should self-heal.
	if found && srv.ID != "" && srv.APIKey != "" {
		if srv.Name == "" {
			srv.Name = DefaultName()
		}
		return &srv, false, nil
	}

	id, err := NewID()
	if err != nil {
		return nil, false, err
	}
	key, err := NewAPIKey()
	if err != nil {
		return nil, false, err
	}
	srv = Server{ID: id, Name: DefaultName(), APIKey: key}
	if err := srv.Save(); err != nil {
		return nil, false, err
	}
	return &srv, true, nil
}

// Save persists the server identity.
func (s *Server) Save() error {
	path, err := Path(serverFile)
	if err != nil {
		return err
	}
	if err := writeJSON(path, s); err != nil {
		return fmt.Errorf("save server identity: %w", err)
	}
	return nil
}

// RotateKey issues a new API key. Every previously distributed connect string
// stops working, which is the point: it is the revocation mechanism.
func (s *Server) RotateKey() error {
	key, err := NewAPIKey()
	if err != nil {
		return err
	}
	s.APIKey = key
	return s.Save()
}
