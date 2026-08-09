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

  --profile NAME       which linked server to use (default: the default one)
  --model NAME         start with this model
  --resume             continue the most recent chat instead of starting fresh
  --endpoint HOST:PORT connect to exactly this address, skipping discovery
  --tls                force TLS even on the local network
  --no-discovery       skip the mDNS scan

`, "\n"))
	}
	profileName := fs.String("profile", "", "")
	model := fs.String("model", "", "")
	resume := fs.Bool("resume", false, "")
	endpoint := fs.String("endpoint", "", "")
	forceTLS := fs.Bool("tls", false, "")
	noDiscovery := fs.Bool("no-discovery", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	profiles, profile, err := loadProfile(*profileName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	c, err := client.Connect(ctx, profile, client.Options{
		ForceEndpoint: *endpoint,
		ForceTLS:      *forceTLS,
		SkipDiscovery: *noDiscovery,
	})
	cancel()
	if err != nil {
		return err
	}
	if err := c.RememberRoute(profiles); err != nil {
		return err
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

	program := tea.NewProgram(
		tui.New(c, store, profiles, profile.Name, sess),
		tea.WithAltScreen(),
	)
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("chat interface: %w", err)
	}
	return nil
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
