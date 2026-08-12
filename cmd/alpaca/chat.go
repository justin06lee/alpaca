package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/session"
	"github.com/justin06lee/alpaca/internal/tui"
)

func runChat(args []string) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, strings.TrimLeft(`
alpaca chat — open the chat interface

  --demo               open the interface with no server, using canned replies
  --profile NAME       which linked server to use (default: the default one)
  --model NAME         start with this model
  --resume             continue the most recent chat instead of starting fresh
  --endpoint HOST:PORT connect to exactly this address, skipping discovery
  --tls                force TLS even on the local network
  --no-discovery       skip the mDNS scan

`, "\n"))
	}
	demo := fs.Bool("demo", false, "")
	profileName := fs.String("profile", "", "")
	model := fs.String("model", "", "")
	resume := fs.Bool("resume", false, "")
	endpoint := fs.String("endpoint", "", "")
	forceTLS := fs.Bool("tls", false, "")
	noDiscovery := fs.Bool("no-discovery", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *demo {
		return runDemoChat(*model)
	}

	profiles, profile, err := loadProfile(*profileName)
	if err != nil {
		return err
	}

	// Connecting happens inside the interface, behind the opening animation:
	// racing routes and waiting on an mDNS scan is the slow part of a cold
	// start, and a blank terminal for those seconds reads as a hang.
	connect := func(ctx context.Context) (*client.Client, error) {
		c, err := client.Connect(ctx, profile, client.Options{
			ForceEndpoint: *endpoint,
			ForceTLS:      *forceTLS,
			SkipDiscovery: *noDiscovery,
		})
		if err != nil {
			return nil, err
		}
		if err := c.RememberRoute(profiles); err != nil {
			return nil, err
		}
		return c, nil
	}

	store, err := session.NewStore()
	if err != nil {
		return err
	}

	sess, err := pickSession(store, profile, *resume)
	if err != nil {
		return err
	}
	if *model != "" {
		sess.Model = *model
	}

	ui := tui.New(connect, store, profiles, profile.Name, sess)
	if _, err := tea.NewProgram(ui, tea.WithAltScreen()).Run(); err != nil {
		return fmt.Errorf("chat interface: %w", err)
	}
	// A connection failure ends the session; report it out here where it can be
	// printed in full rather than squeezed into a status bar.
	return ui.Err()
}

// pickSession decides which conversation to open.
//
// A new chat is the default rather than resuming the last one. Silently
// reattaching old history would let an unrelated question inherit context the
// user has forgotten about — and pay for those tokens. --resume and ctrl+s make
// history available when it is actually wanted.
func pickSession(store *session.Store, profile *config.Profile, resume bool) (*session.Session, error) {
	if resume {
		sessions, err := store.List()
		if err != nil {
			return nil, err
		}
		for _, s := range sessions {
			if s.Server == profile.Name {
				return s, nil
			}
		}
		fmt.Fprintln(os.Stderr, "no previous chat for this server — starting a new one")
	}

	sess := session.New(profile.Model, profile.Name)
	sess.System = profile.System
	return sess, nil
}

// loadProfile resolves a profile by name, or the default.
func loadProfile(name string) (*config.Profiles, *config.Profile, error) {
	profiles, err := config.LoadProfiles()
	if err != nil {
		return nil, nil, err
	}
	profile, err := profiles.Get(name)
	if err != nil {
		return nil, nil, err
	}
	return profiles, profile, nil
}

// connectProfile is the shared setup for the non-interactive commands.
func connectProfile(name string, opts client.Options) (*config.Profiles, *client.Client, error) {
	profiles, profile, err := loadProfile(name)
	if err != nil {
		return nil, nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := client.Connect(ctx, profile, opts)
	if err != nil {
		return nil, nil, err
	}
	if err := c.RememberRoute(profiles); err != nil {
		return nil, nil, err
	}
	return profiles, c, nil
}

// contextWithTimeout is a small helper for the one-shot commands.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// runDemoChat opens the interface against an in-process canned server, so it
// can be looked at with no gateway, no ollama, and no network.
//
// Sessions go to a throwaway directory rather than the real one: poking at a
// demo should not leave fake conversations mixed in with genuine history.
func runDemoChat(model string) error {
	c, stop, err := client.NewDemo()
	if err != nil {
		return err
	}
	defer stop()

	sandbox, err := os.MkdirTemp("", "alpaca-demo-*")
	if err != nil {
		return fmt.Errorf("create demo sandbox: %w", err)
	}
	defer os.RemoveAll(sandbox)

	// ALPACA_HOME is what config.Dir honours, so pointing it at the sandbox
	// redirects the session store without threading a path through the TUI.
	previous, hadPrevious := os.LookupEnv("ALPACA_HOME")
	os.Setenv("ALPACA_HOME", sandbox)
	defer func() {
		if hadPrevious {
			os.Setenv("ALPACA_HOME", previous)
		} else {
			os.Unsetenv("ALPACA_HOME")
		}
	}()

	store, err := session.NewStore()
	if err != nil {
		return err
	}
	seedDemoSessions(store)

	sess := session.New(c.Profile().Model, "demo")
	if model != "" {
		sess.Model = model
	}

	profiles := &config.Profiles{Entries: map[string]*config.Profile{}}
	ui := tui.New(tui.Connected(c), store, profiles, "demo", sess)
	if _, err := tea.NewProgram(ui, tea.WithAltScreen()).Run(); err != nil {
		return fmt.Errorf("chat interface: %w", err)
	}
	return ui.Err()
}

// seedDemoSessions puts a couple of conversations in the sandbox so the saved
// chats picker has something to show.
func seedDemoSessions(store *session.Store) {
	samples := []struct {
		prompt, reply string
		age           time.Duration
	}{
		{"How do I reverse a string in Go?",
			"Convert to `[]rune` first, then swap from both ends inward.", 2 * time.Hour},
		{"Explain the difference between a slice and an array.",
			"An array has a fixed length that is part of its type. A slice is a view " +
				"onto an array: pointer, length, capacity.", 26 * time.Hour},
	}

	for _, s := range samples {
		sess := session.New("llama3.2:latest", "demo")
		sess.Append(client.Message{Role: client.RoleUser, Content: s.prompt})
		sess.Append(client.Message{Role: client.RoleAssistant, Content: s.reply})
		sess.Updated = time.Now().Add(-s.age)
		_ = store.Save(sess)
	}
}
