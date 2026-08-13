package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/connect"
)

func runLink(args []string) error {
	fs := flag.NewFlagSet("link", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, strings.TrimLeft(`
alpaca link — save a server so this machine can reach it

  alpaca link alpaca1:...      paste the string printed by `+"`alpaca serve`"+`
  alpaca link                  read the string from stdin

  --name NAME    store it under this name instead of the server's own
  --no-verify    skip the connection test

`, "\n"))
	}
	name := fs.String("name", "", "")
	noVerify := fs.Bool("no-verify", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	raw, err := readConnectString(fs.Args())
	if err != nil {
		return err
	}

	bundle, err := connect.Decode(raw)
	if err != nil {
		return err
	}

	// The whole read-modify-write runs under the profile lock, so linking in
	// one terminal cannot clobber a chat saving its route in another.
	var profile *config.Profile
	var existing bool
	profiles, err := config.UpdateProfiles(func(p *config.Profiles) error {
		// Re-linking the same server updates it in place rather than piling up
		// duplicates — the usual reason to re-link is that the key or the
		// addresses changed.
		profile, existing = p.ByID(bundle.ID)
		if existing {
			profile.APIKey = bundle.Key
			profile.Fingerprint = bundle.Fingerprint
			profile.LAN = bundle.LAN
			profile.Public = bundle.Public
			// The cached route may be stale now; let the next connect re-race.
			profile.LastGood = ""
			profile.LastGoodTLS = false
			return nil
		}
		label := bundle.Name
		if *name != "" {
			label = *name
		}
		profile = &config.Profile{
			ID:          bundle.ID,
			Name:        label,
			APIKey:      bundle.Key,
			Fingerprint: bundle.Fingerprint,
			LAN:         bundle.LAN,
			Public:      bundle.Public,
		}
		p.Add(profile)
		return nil
	})
	if err != nil {
		return err
	}

	verb := "linked"
	if existing {
		verb = "updated"
	}
	fmt.Printf("%s %q\n", verb, profile.Name)

	if *noVerify {
		fmt.Println("run `alpaca chat` to start")
		return nil
	}

	fmt.Print("checking the connection… ")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := client.Connect(ctx, profile, client.Options{})
	if err != nil {
		fmt.Println("no answer")
		fmt.Fprintf(os.Stderr, "\n%v\n", err)
		fmt.Fprintln(os.Stderr, "\nThe profile is saved, so you can retry later with `alpaca status`.")
		return nil
	}

	fmt.Printf("reachable — %s\n", c.Route().Describe())
	if err := c.RememberRoute(profiles); err != nil {
		return err
	}

	if models, err := c.Models(ctx); err == nil {
		if len(models) == 0 {
			fmt.Println("the server has no models installed yet")
		} else {
			names := make([]string, 0, len(models))
			for _, m := range models {
				names = append(names, m.ID)
			}
			fmt.Printf("%d model(s): %s\n", len(models), strings.Join(names, ", "))
		}
	}

	fmt.Println("\nrun `alpaca chat` to start")
	return nil
}

// readConnectString takes the token from the command line or from stdin.
//
// Accepting stdin matters because the string is long: piping it avoids shell
// quoting problems and lets it come straight from a password manager.
func readConnectString(args []string) (string, error) {
	if len(args) > 0 {
		// An unquoted paste can arrive split across several argv entries; the
		// decoder strips whitespace, so rejoining is safe.
		return strings.Join(args, ""), nil
	}

	stat, err := os.Stdin.Stat()
	if err == nil && stat.Mode()&os.ModeCharDevice == 0 {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 64*1024))
		if err != nil {
			return "", fmt.Errorf("read connect string from stdin: %w", err)
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			return string(data), nil
		}
	}

	return "", errors.New("no connect string given — paste the one `alpaca serve` printed, " +
		"e.g. `alpaca link alpaca1:...`")
}
