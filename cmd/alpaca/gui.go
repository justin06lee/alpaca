package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/session"
	"github.com/justin06lee/alpaca/internal/webui"
)

// guiIdleLimit is how long a windowless gui lingers after its last heartbeat.
// It only applies when there is no terminal attached — a Dock-launched alpaca
// has nobody to ctrl+c it, so closing the tab is how it gets put away.
const guiIdleLimit = 90 * time.Second

func runGui(args []string) error {
	fs := flag.NewFlagSet("gui", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, strings.TrimLeft(`
alpaca gui — open the chat as a desktop window

Serves the interface on 127.0.0.1 and opens it in your browser. Launching
Alpaca.app (or running the bare binary outside a terminal) lands here too.

  --demo               open the interface with no server, using canned replies
  --profile NAME       which linked server to use (default: the default one)
  --port N             listen on a fixed port (default: whatever is free)
  --no-open            print the address instead of opening the browser
  --endpoint HOST:PORT connect to exactly this address, skipping discovery
  --tls                force TLS even on the local network
  --no-discovery       skip the mDNS scan

`, "\n"))
	}
	demo := fs.Bool("demo", false, "")
	profileName := fs.String("profile", "", "")
	port := fs.Int("port", 0, "")
	noOpen := fs.Bool("no-open", false, "")
	endpoint := fs.String("endpoint", "", "")
	forceTLS := fs.Bool("tls", false, "")
	noDiscovery := fs.Bool("no-discovery", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var connect webui.Connector
	serverName := "demo"

	if *demo {
		c, stop, err := client.NewDemo()
		if err != nil {
			return err
		}
		defer stop()

		cleanup, err := demoSandbox()
		if err != nil {
			return err
		}
		defer cleanup()
		connect = webui.Connected(c)
	} else {
		// Profiles load inside the connector rather than up front: a Finder
		// launch has no stderr anyone can see, so "no servers linked yet"
		// must reach the window — and its try-again then rereads the
		// profiles, so linking in a terminal fixes the open window.
		serverName = "alpaca"
		if _, profile, err := loadProfile(*profileName); err == nil {
			serverName = profile.Name
		}
		connect = func(ctx context.Context) (*client.Client, error) {
			profiles, profile, err := loadProfile(*profileName)
			if err != nil {
				return nil, err
			}
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
	}

	// The store opens after the demo sandbox is in place, so demo chats land
	// in the throwaway directory the same way the TUI's do.
	store, err := session.NewStore()
	if err != nil {
		return err
	}
	if *demo {
		seedDemoSessions(store)
	}

	srv, err := webui.New(connect, store, serverName, buildVersion())
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)

	onTerminal := isTerminal(os.Stdout)
	if onTerminal {
		fmt.Printf("alpaca gui · %s · ctrl+c to quit\n", url)
	}
	if !*noOpen {
		openBrowser(url)
	}

	httpSrv := &http.Server{Handler: srv.Handler()}

	// Three ways down: a signal, the window going quiet (Dock launches only),
	// or a serve error.
	done := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

		if onTerminal {
			<-sig
		} else {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-sig:
				case <-ticker.C:
					if idle, busy := srv.IdleSince(); busy || idle < guiIdleLimit {
						continue
					}
				}
				break
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
		close(done)
	}()

	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("gui server: %w", err)
	}
	<-done
	return nil
}

// isTerminal reports whether f is an interactive terminal.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// openBrowser hands the URL to the platform's opener; a failure is not fatal,
// the address is still printed for a terminal launch.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
