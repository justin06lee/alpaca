// Package client connects to an alpaca gateway and talks to it.
//
// The connection strategy is the reason alpaca feels like it "just works": the
// user pastes one connect string and never thinks about addresses again. On
// every launch the client races every route it knows — the one that worked last
// time, whatever mDNS turns up right now, the hints baked into the connect
// string, and the public endpoint — and keeps whichever answers first.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/discovery"
	"github.com/justin06lee/alpaca/internal/netx"
)

// publicDelay staggers the public endpoint behind the LAN candidates.
//
// Starting it immediately would sometimes let the public path win a race it
// should lose — routing back through the internet to reach a machine in the
// same room. A short head start for LAN costs nothing when LAN works (the
// public probe is never opened) and only this much when it doesn't.
const publicDelay = 300 * time.Millisecond

// probeTimeout bounds a single health check. Anything slower than this is not a
// route worth using for interactive chat.
const probeTimeout = 4 * time.Second

// Source labels how a working route was found, for display in the TUI.
type Source string

const (
	SourceCached Source = "cached"
	SourceMDNS   Source = "mdns"
	SourceHint   Source = "hint"
	SourcePublic Source = "public"
	// SourceDemo is the in-process canned server used by `alpaca chat --demo`.
	SourceDemo Source = "demo"
)

// Route is the connection that won the race.
type Route struct {
	Endpoint string
	TLS      bool
	Source   Source
	Latency  time.Duration
}

// Describe renders the route for a status bar.
func (r Route) Describe() string {
	transport := "http"
	if r.TLS {
		transport = "tls"
	}
	return fmt.Sprintf("%s via %s (%s, %s)", r.Endpoint, r.Source, transport, r.Latency.Round(time.Millisecond))
}

// Client is a connected gateway session.
type Client struct {
	profile *config.Profile
	http    *http.Client
	baseURL string
	route   Route
}

// Route reports how this client reached the server.
func (c *Client) Route() Route { return c.route }

// Profile returns the profile this client was built from.
func (c *Client) Profile() *config.Profile { return c.profile }

// Options tunes connection behaviour.
type Options struct {
	// ForceEndpoint skips the race and uses exactly this host:port.
	ForceEndpoint string
	// ForceTLS requires TLS on every route, including LAN ones.
	ForceTLS bool
	// SkipDiscovery turns off mDNS, for networks where multicast is blocked
	// and the scan would only add latency.
	SkipDiscovery bool
	// Timeout bounds the whole connection attempt.
	Timeout time.Duration
}

type candidate struct {
	endpoint string
	tls      bool
	source   Source
	delay    time.Duration
}

type probeResult struct {
	cand    candidate
	latency time.Duration
	err     error
}

// Connect races every known route and returns a client on the first that
// answers as the expected server.
func Connect(ctx context.Context, prof *config.Profile, opts Options) (*Client, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	cands := staticCandidates(prof, opts)
	if opts.ForceEndpoint != "" {
		// An explicit endpoint is an instruction, not a hint: do not race it
		// against anything.
		cands = []candidate{{endpoint: opts.ForceEndpoint, tls: opts.ForceTLS, source: SourceHint}}
		opts.SkipDiscovery = true
	}

	raceCtx, stop := context.WithCancel(ctx)
	defer stop()

	results := make(chan probeResult, 16)
	var wg sync.WaitGroup

	launch := func(c candidate) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.delay > 0 {
				select {
				case <-time.After(c.delay):
				case <-raceCtx.Done():
					return
				}
			}
			latency, err := probe(raceCtx, c, prof)
			select {
			case results <- probeResult{cand: c, latency: latency, err: err}:
			case <-raceCtx.Done():
			}
		}()
	}

	for _, c := range cands {
		launch(c)
	}

	if !opts.SkipDiscovery && prof.ID != "" {
		// Discovery is itself part of the race. It is counted in the WaitGroup
		// before it can call launch, so adding from inside it is safe.
		wg.Add(1)
		go func() {
			defer wg.Done()
			scanCtx, scanCancel := context.WithTimeout(raceCtx, discovery.DefaultTimeout)
			defer scanCancel()

			found, err := discovery.Find(scanCtx, prof.ID)
			if err != nil {
				return
			}
			for _, c := range discoveredCandidates(found, prof, opts) {
				launch(c)
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var failures []error
	for res := range results {
		if res.err != nil {
			failures = append(failures, fmt.Errorf("%s (%s): %w", res.cand.endpoint, res.cand.source, res.err))
			continue
		}

		stop() // cancel the losing probes
		route := Route{
			Endpoint: res.cand.endpoint,
			TLS:      res.cand.tls,
			Source:   res.cand.source,
			Latency:  res.latency,
		}
		return newClient(prof, route)
	}

	return nil, connectionError(prof, failures)
}

// staticCandidates builds the routes known without touching the network.
func staticCandidates(prof *config.Profile, opts Options) []candidate {
	var out []candidate
	seen := map[string]bool{}

	add := func(endpoint string, useTLS bool, source Source, delay time.Duration) {
		if endpoint == "" || seen[endpoint] {
			return
		}
		seen[endpoint] = true
		// The trust boundary is enforced here as well as on the server: hints
		// come from a file on disk and could have been written by an older
		// alpaca or edited by hand, and plain HTTP carries the API key. Any
		// endpoint that is not verifiably private is upgraded to TLS.
		useTLS = useTLS || opts.ForceTLS || !trustedEndpoint(endpoint)
		out = append(out, candidate{endpoint: endpoint, tls: useTLS, source: source, delay: delay})
	}

	// The route that worked last time is overwhelmingly likely to work again,
	// so it goes first with no delay.
	add(prof.LastGood, prof.LastGoodTLS, SourceCached, 0)

	for _, endpoint := range prof.LAN {
		add(endpoint, false, SourceHint, 0)
	}

	// TLS is mandatory on the public route: that traffic crosses networks
	// alpaca has no reason to trust.
	add(prof.Public, true, SourcePublic, publicDelay)

	return out
}

// discoveredCandidates converts an mDNS answer into race candidates.
//
// Discovered addresses are claims from the network, not hints the user pasted,
// so an impersonator can direct the client anywhere. When the profile carries a
// pin, discovered routes are therefore probed over pinned TLS: whoever answers
// must present the pinned certificate before the client will speak to it, and
// the API key never reaches a machine that merely echoed the right ID.
//
// The advertised TXT fingerprint is attacker-writable and can never prove
// identity, but a mismatch against the pin we already hold is enough to skip
// the whole answer early.
func discoveredCandidates(found *discovery.Result, prof *config.Profile, opts Options) []candidate {
	if found == nil {
		return nil
	}
	if found.Fingerprint != "" && prof.Fingerprint != "" &&
		netx.NormalizeFingerprint(found.Fingerprint) != netx.NormalizeFingerprint(prof.Fingerprint) {
		return nil
	}
	out := make([]candidate, 0, len(found.Endpoints))
	for _, endpoint := range found.Endpoints {
		useTLS := opts.ForceTLS || prof.Fingerprint != "" || !trustedEndpoint(endpoint)
		out = append(out, candidate{endpoint: endpoint, tls: useTLS, source: SourceMDNS})
	}
	return out
}

// trustedEndpoint reports whether an endpoint may be probed over plain HTTP:
// loopback, or an IP literal on a network alpaca treats as private. A hostname
// could resolve anywhere, so it never qualifies.
func trustedEndpoint(endpoint string) bool {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		host = endpoint
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || netx.ClassifyIP(ip).Trusted()
}

// probe health-checks one candidate and verifies it is the right server.
func probe(ctx context.Context, c candidate, prof *config.Profile) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	httpClient := &http.Client{Transport: transportFor(c.tls, prof.Fingerprint)}
	defer httpClient.CloseIdleConnections()

	url := schemeFor(c.tls) + "://" + c.endpoint + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	latency := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("health check returned %d", resp.StatusCode)
	}

	var health struct {
		OK      bool   `json:"ok"`
		ID      string `json:"id"`
		Service string `json:"service"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return 0, fmt.Errorf("not an alpaca server: %w", err)
	}
	if health.Service != "alpaca" {
		return 0, errors.New("something else is listening on this address")
	}
	// Identity check. A LAN hint captured months ago may now point at a
	// completely different machine that DHCP handed the address to; without
	// this the client would happily talk to a stranger.
	if prof.ID != "" && health.ID != prof.ID {
		return 0, fmt.Errorf("different alpaca server here (found %s, want %s)", health.ID, prof.ID)
	}

	return latency, nil
}

func newClient(prof *config.Profile, route Route) (*Client, error) {
	c := &Client{
		profile: prof,
		baseURL: schemeFor(route.TLS) + "://" + route.Endpoint,
		route:   route,
		http: &http.Client{
			// No overall timeout: chat responses stream for as long as the
			// model takes. Per-request contexts handle cancellation.
			Transport: transportFor(route.TLS, prof.Fingerprint),
		},
	}
	return c, nil
}

func transportFor(useTLS bool, fingerprint string) *http.Transport {
	t := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 4 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	if useTLS {
		t.TLSClientConfig = netx.PinnedClientConfig(fingerprint)
		t.TLSHandshakeTimeout = 6 * time.Second
	}
	return t
}

func schemeFor(useTLS bool) string {
	if useTLS {
		return "https"
	}
	return "http"
}

// RememberRoute persists the winning route so the next launch tries it first.
func (c *Client) RememberRoute(profiles *config.Profiles) error {
	prof, ok := profiles.Entries[c.profile.Name]
	if !ok {
		return nil
	}
	if prof.LastGood == c.route.Endpoint && prof.LastGoodTLS == c.route.TLS {
		return nil // nothing changed; skip the write
	}
	prof.LastGood = c.route.Endpoint
	prof.LastGoodTLS = c.route.TLS
	return profiles.Save()
}

// connectionError explains a total failure in terms the user can act on.
func connectionError(prof *config.Profile, failures []error) error {
	if len(failures) == 0 {
		return fmt.Errorf("could not reach %q: no routes to try — the connect string had no endpoints "+
			"and mDNS found nothing on this network", prof.Name)
	}
	msg := fmt.Sprintf("could not reach %q on any known route:", prof.Name)
	for _, err := range failures {
		msg += "\n  - " + err.Error()
	}
	msg += "\n\nCheck that `alpaca serve` is running on the other machine. If it moved to a " +
		"different network, re-run `alpaca link` there with a fresh connect string."
	return errors.New(msg)
}
