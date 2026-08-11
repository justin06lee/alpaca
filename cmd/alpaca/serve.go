package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/connect"
	"github.com/justin06lee/alpaca/internal/discovery"
	"github.com/justin06lee/alpaca/internal/netx"
	"github.com/justin06lee/alpaca/internal/ollama"
	"github.com/justin06lee/alpaca/internal/portmap"
	"github.com/justin06lee/alpaca/internal/search"
	"github.com/justin06lee/alpaca/internal/server"
)

// maxLANHints caps how many addresses go into a connect string. The list is
// already ranked best-first, and a machine with several interfaces plus IPv6
// would otherwise produce a token too long to copy comfortably.
const maxLANHints = 4

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, strings.TrimLeft(`
alpaca serve — expose the local ollama daemon as a networked API

  --port N          port to listen on (default 8080)
  --bind ADDR       interface to bind (default 0.0.0.0, all interfaces)
  --ollama URL      ollama daemon address (default http://127.0.0.1:11434)
  --name NAME       friendly name shown to clients (default: this hostname)
  --public HOST:PORT  advertise this internet-reachable address, for when you
                    have forwarded a port on the router yourself
  --search KIND     enable web search for the model ("searxng")
  --search-url URL  the SearXNG instance, e.g. http://localhost:8888
  --search-results N  hits per query (default 5)
  --search-rounds N   how many searches the model may run per reply (default 3)
  --no-mdns         do not announce on the local network
  --no-portmap      do not ask the router to forward a port
  --rotate-key      issue a new API key, invalidating every existing client
  --rotate-cert     issue a new TLS certificate, invalidating every existing client
  --quiet           do not print the connect string
  --verbose         log every request

`, "\n"))
	}

	port := fs.Int("port", 8080, "")
	bind := fs.String("bind", "0.0.0.0", "")
	ollamaURL := fs.String("ollama", "", "")
	name := fs.String("name", "", "")
	publicAddr := fs.String("public", "", "")
	searchKind := fs.String("search", "", "")
	searchURL := fs.String("search-url", "", "")
	searchResults := fs.Int("search-results", 5, "")
	searchRounds := fs.Int("search-rounds", 3, "")
	noMDNS := fs.Bool("no-mdns", false, "")
	noPortmap := fs.Bool("no-portmap", false, "")
	rotateKey := fs.Bool("rotate-key", false, "")
	rotateCert := fs.Bool("rotate-cert", false, "")
	quiet := fs.Bool("quiet", false, "")
	verbose := fs.Bool("verbose", false, "")

	if err := fs.Parse(args); err != nil {
		return err
	}

	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	// --- identity ---------------------------------------------------------

	identity, fresh, err := config.LoadServer()
	if err != nil {
		return err
	}
	if *name != "" && *name != identity.Name {
		identity.Name = *name
		if err := identity.Save(); err != nil {
			return err
		}
	}
	if *rotateKey {
		if err := identity.RotateKey(); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "issued a new API key — every previously linked client must re-link")
	}

	// --- ollama -----------------------------------------------------------

	daemon, err := ollama.New(*ollamaURL)
	if err != nil {
		return err
	}

	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
	daemonVersion, err := daemon.Version(probeCtx)
	cancelProbe()
	if err != nil {
		return fmt.Errorf("cannot reach ollama at %s.\n"+
			"  Start it with `ollama serve`, or point alpaca elsewhere with --ollama URL.\n"+
			"  (%v)", daemon.BaseURL(), err)
	}

	modelCtx, cancelModels := context.WithTimeout(context.Background(), 10*time.Second)
	models, err := daemon.Models(modelCtx)
	cancelModels()
	if err != nil {
		return fmt.Errorf("connected to ollama but could not list models: %w", err)
	}
	if len(models) == 0 {
		fmt.Fprintln(os.Stderr, "warning: ollama has no models installed — run `ollama pull llama3.2` first")
	}

	// --- search -----------------------------------------------------------

	searchProvider, searchNote, err := buildSearch(*searchKind, *searchURL)
	if err != nil {
		return err
	}

	// --- tls --------------------------------------------------------------

	certDir, err := config.Path("certs", "placeholder")
	if err != nil {
		return err
	}
	certDir = strings.TrimSuffix(certDir, string(os.PathSeparator)+"placeholder")

	hosts := certHosts(*publicAddr)
	var tlsIdentity *netx.Identity
	if *rotateCert {
		tlsIdentity, err = netx.CreateIdentity(certDir, hosts)
		if err == nil {
			fmt.Fprintln(os.Stderr, "issued a new TLS certificate — every previously linked client must re-link")
		}
	} else {
		tlsIdentity, err = netx.LoadOrCreateIdentity(certDir, hosts)
	}
	if err != nil {
		return err
	}

	// --- listen -----------------------------------------------------------

	listenAddr := net.JoinHostPort(*bind, strconv.Itoa(*port))
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		if strings.Contains(err.Error(), "address already in use") {
			return fmt.Errorf("port %d is already in use — stop whatever is using it, "+
				"or pick another with --port", *port)
		}
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}

	sniffer := netx.Sniff(listener)
	defer sniffer.Close()

	gateway := server.New(server.Options{
		Ollama:        daemon,
		APIKey:        identity.APIKey,
		ID:            identity.ID,
		Name:          identity.Name,
		Version:       buildVersion(),
		Logger:        log,
		Search:        searchProvider,
		SearchResults: *searchResults,
		SearchRounds:  *searchRounds,
	})

	plainSrv := gateway.NewHTTPServer()
	tlsSrv := gateway.NewHTTPServer()
	serveErr := make(chan error, 2)
	go func() {
		if err := plainSrv.Serve(sniffer.Plain()); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	go func() {
		secure := tls.NewListener(sniffer.TLS(), tlsIdentity.ServerTLSConfig())
		if err := tlsSrv.Serve(secure); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	// --- reachability -----------------------------------------------------

	portStr := strconv.Itoa(*port)
	// Only addresses on networks alpaca can treat as private become plain-HTTP
	// hints. Globally routable ones are held back for the TLS route below.
	trustedEndpoints := netx.TrustedHostPorts(portStr)
	globalEndpoints := netx.GlobalHostPorts(portStr)

	var advertisement *discovery.Advertisement
	if !*noMDNS {
		advertisement, err = discovery.Advertise(identity.ID, identity.Name, *port, tlsIdentity.Fingerprint)
		if err != nil {
			log.Warn("could not announce on the local network", "error", err)
		}
		defer advertisement.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var mapping *portmap.Mapping
	var mapErr error
	public := *publicAddr
	publicKind := "manually configured"

	if public == "" && !*noPortmap {
		mapCtx, cancelMap := context.WithTimeout(ctx, 12*time.Second)
		mapping, mapErr = portmap.Map(mapCtx, *port)
		cancelMap()
		if mapping != nil {
			if mapping.Reachable() {
				public = mapping.Endpoint()
				publicKind = mapping.Method + " port forward"
				go mapping.Maintain(ctx, log)
			} else {
				// The forward exists but the address behind it is not routable
				// from the internet, so advertising it would only mislead.
				mapErr = fmt.Errorf("router reported the non-routable address %s (carrier-grade NAT)",
					mapping.ExternalIP)
				mapping.Release()
				mapping = nil
			}
		}
	}

	// A globally routable IPv6 address is the last resort for reaching this
	// machine from elsewhere, and needs no port forwarding at all — many home
	// networks have one already. Router firewalls often block inbound v6, so it
	// is offered rather than promised, and always over TLS.
	if public == "" && len(globalEndpoints) > 0 {
		public = globalEndpoints[0]
		publicKind = "direct ipv6 — may need a firewall rule"
	}

	// --- connect string ---------------------------------------------------

	bundle := connect.Bundle{
		ID:          identity.ID,
		Name:        identity.Name,
		Key:         identity.APIKey,
		Fingerprint: tlsIdentity.Fingerprint,
		LAN:         trimHints(trustedEndpoints),
		Public:      public,
	}
	connectString, err := bundle.Encode()
	if err != nil {
		return err
	}

	printBanner(bannerInfo{
		identity:      identity,
		fresh:         fresh,
		port:          *port,
		models:        len(models),
		firstModel:    firstModelName(models),
		daemonVersion: daemonVersion,
		trusted:       trustedEndpoints,
		public:        public,
		publicKind:    publicKind,
		mapErr:        mapErr,
		fingerprint:   tlsIdentity.Fingerprint,
		connectString: connectString,
		quiet:         *quiet,
		mdns:          advertisement != nil,
		search:        searchNote,
	})

	// --- run --------------------------------------------------------------

	select {
	case err := <-serveErr:
		return fmt.Errorf("server stopped: %w", err)
	case <-ctx.Done():
	}

	fmt.Fprintln(os.Stderr, "\nshutting down…")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = plainSrv.Shutdown(shutdownCtx)
	_ = tlsSrv.Shutdown(shutdownCtx)
	return nil
}

// certHosts collects the names and addresses to put in the certificate's SANs.
//
// alpaca's own client pins the fingerprint and ignores these, but anything else
// aimed at the gateway — curl, a browser, another OpenAI-compatible tool — does
// check them.
func certHosts(publicAddr string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		hosts = append(hosts, hostname)
		if !strings.HasSuffix(hostname, ".local") {
			hosts = append(hosts, hostname+".local")
		}
	}
	for _, ip := range netx.AllIPs() {
		hosts = append(hosts, ip.String())
	}
	if publicAddr != "" {
		if host, _, err := net.SplitHostPort(publicAddr); err == nil {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func trimHints(endpoints []string) []string {
	if len(endpoints) > maxLANHints {
		return endpoints[:maxLANHints]
	}
	return endpoints
}

func firstModelName(models []ollama.Model) string {
	if len(models) == 0 {
		return ""
	}
	return models[0].Name
}

// ---------------------------------------------------------------------------
// Banner
// ---------------------------------------------------------------------------

type bannerInfo struct {
	identity      *config.Server
	fresh         bool
	port          int
	models        int
	firstModel    string
	daemonVersion string
	trusted       []string
	public        string
	publicKind    string
	mapErr        error
	fingerprint   string
	connectString string
	quiet         bool
	mdns          bool
	search        string
}

func printBanner(info bannerInfo) {
	var (
		title  = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
		ok     = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
		bad    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		key    = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
		muted  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		accent = lipgloss.NewStyle().Foreground(lipgloss.Color("221"))
	)

	out := os.Stdout
	fmt.Fprintln(out)

	summary := fmt.Sprintf("serving %d model", info.models)
	if info.models != 1 {
		summary += "s"
	}
	if info.firstModel != "" {
		summary = fmt.Sprintf("serving %s", info.firstModel)
		if info.models > 1 {
			summary += fmt.Sprintf(" and %d more", info.models-1)
		}
	}
	fmt.Fprintf(out, "  %s  %s %s\n", title.Render("alpaca"), summary,
		muted.Render(fmt.Sprintf("· ollama %s · port %d", info.daemonVersion, info.port)))
	fmt.Fprintln(out)

	// Routes, laid out as a table whose columns fit the widest entry — IPv6
	// literals are long enough to wreck any fixed width.
	type row struct{ label, endpoint, note string }
	var rows []row

	for _, endpoint := range info.trusted {
		host, _, err := net.SplitHostPort(endpoint)
		if err != nil {
			continue
		}
		reach := netx.ClassifyIP(net.ParseIP(host))
		rows = append(rows, row{reach.Label(), endpoint, reach.Note()})
	}
	if info.public != "" {
		rows = append(rows, row{"internet", info.public, info.publicKind + " · tls"})
	}

	labelWidth, endpointWidth := 0, 0
	for _, r := range rows {
		labelWidth = max(labelWidth, len(r.label))
		endpointWidth = max(endpointWidth, len(r.endpoint))
	}

	fmt.Fprintln(out, muted.Render("  reachable at"))
	for _, r := range rows {
		fmt.Fprintf(out, "    %s %-*s  %-*s  %s\n",
			ok.Render("✓"), labelWidth, r.label, endpointWidth, r.endpoint, muted.Render(r.note))
	}

	if info.public == "" && info.mapErr != nil {
		fmt.Fprintf(out, "    %s %-*s  %s\n",
			bad.Render("✗"), labelWidth, "internet", muted.Render(shortenError(info.mapErr)))
		fmt.Fprintf(out, "      %s\n",
			muted.Render("LAN and tailnet still work. For access from anywhere else, enable"))
		fmt.Fprintf(out, "      %s\n",
			muted.Render("UPnP/NAT-PMP on the router, or forward a port and pass --public HOST:PORT."))
	}
	if !info.mdns {
		fmt.Fprintf(out, "    %s %-*s  %s\n", bad.Render("✗"), labelWidth, "discovery",
			muted.Render("mdns announcements disabled"))
	}
	fmt.Fprintln(out)

	mark := bad.Render("✗")
	if strings.HasPrefix(info.search, "on ") {
		mark = ok.Render("✓")
	}
	fmt.Fprintf(out, "  %s %s %s\n", muted.Render("web search"), mark, muted.Render(info.search))
	fmt.Fprintln(out)

	// The connect string.
	if !info.quiet {
		fmt.Fprintln(out, muted.Render("  run this on every other machine"))
		fmt.Fprintln(out)
		fmt.Fprintf(out, "    %s %s\n", accent.Render("alpaca link"), info.connectString)
		fmt.Fprintln(out)
		fmt.Fprintln(out, muted.Render("  that string contains the API key — treat it like a password"))
		fmt.Fprintln(out)
	}

	fmt.Fprintf(out, "  %s %s\n", muted.Render("api key    "), key.Render(info.identity.APIKey))
	fmt.Fprintf(out, "  %s %s\n", muted.Render("cert sha256"), muted.Render(info.fingerprint[:32]+"…"))
	if info.fresh {
		fmt.Fprintln(out)
		fmt.Fprintln(out, muted.Render("  (first run — this identity is saved and will be reused)"))
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, muted.Render("  ctrl-c to stop"))
	fmt.Fprintln(out)
}

// shortenError keeps the banner readable when port mapping fails with a long
// joined error from both protocols.
func shortenError(err error) string {
	text := err.Error()
	if idx := strings.IndexByte(text, '\n'); idx > 0 {
		text = text[:idx] + " …"
	}
	if len(text) > 90 {
		text = text[:90] + "…"
	}
	return "unavailable: " + text
}

// buildSearch wires up the search provider named on the command line.
//
// The instance is pinged rather than queried: that confirms it is up using one
// local request, without sending anything to an upstream engine just to check.
func buildSearch(kind, instanceURL string) (search.Provider, string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "":
		return nil, "off — start with --search searxng --search-url URL to enable", nil

	case "searxng":
		if instanceURL == "" {
			instanceURL = "http://localhost:8888"
		}
		provider, err := search.NewSearXNG(instanceURL)
		if err != nil {
			return nil, "", err
		}

		note := "on · " + provider.Name()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		err = provider.Ping(ctx)
		cancel()
		if err != nil {
			// Not fatal: the instance may still be starting. The model simply
			// gets a clear failure back if it tries to search.
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			note = "on · " + provider.Name() + " (not responding yet)"
		}

		// Repeat queries within a conversation are common, and every one saved
		// is one fewer round trip out of the network.
		return search.NewCached(provider, 10*time.Minute, 128), note, nil

	default:
		return nil, "", fmt.Errorf("unknown search provider %q (supported: searxng)", kind)
	}
}
