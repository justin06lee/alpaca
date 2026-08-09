package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/netx"
)

const testKey = "alp_clienttestkeyclienttestkey000"

// fakeGateway serves just enough for the dialer: a health endpoint identifying
// the server, plus whatever extra routes a test needs.
func fakeGateway(t *testing.T, id string, extra http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok":true,"id":%q,"name":"fake","service":"alpaca"}`, id)
	})
	if extra != nil {
		mux.HandleFunc("/", extra)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeTLSGateway is the same, over TLS. The real server sniffs both protocols
// on one port, so a public endpoint always speaks TLS; tests of the public path
// have to model that.
func fakeTLSGateway(t *testing.T, id string) (endpoint, fingerprint string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok":true,"id":%q,"name":"fake","service":"alpaca"}`, id)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return hostPort(srv.URL), netx.Fingerprint(srv.Certificate().Raw)
}

// hostPort strips the scheme from an httptest URL.
func hostPort(url string) string {
	return strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
}

func TestConnectPicksTheReachableEndpoint(t *testing.T) {
	live := fakeGateway(t, "server-1", nil)

	prof := &config.Profile{
		ID:     "server-1",
		Name:   "test",
		APIKey: testKey,
		// Two dead hints ahead of the live one: the race must not be derailed
		// by whichever candidate happens to be listed first.
		LAN: []string{"127.0.0.1:1", "192.0.2.1:9", hostPort(live.URL)},
	}

	c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.Route().Endpoint != hostPort(live.URL) {
		t.Errorf("connected to %q, want the live endpoint %q", c.Route().Endpoint, hostPort(live.URL))
	}
	if c.Route().Source != SourceHint {
		t.Errorf("source = %q, want hint", c.Route().Source)
	}
}

// A LAN hint captured months ago may now point at a different machine.
func TestConnectRejectsADifferentServer(t *testing.T) {
	stranger := fakeGateway(t, "someone-else", nil)

	prof := &config.Profile{
		ID:     "server-1",
		Name:   "test",
		APIKey: testKey,
		LAN:    []string{hostPort(stranger.URL)},
	}

	_, err := Connect(context.Background(), prof, Options{SkipDiscovery: true, Timeout: 3 * time.Second})
	if err == nil {
		t.Fatal("Connect succeeded against a different server, want failure")
	}
	if !strings.Contains(err.Error(), "different alpaca server") {
		t.Errorf("error = %v, want it to name the identity mismatch", err)
	}
}

func TestConnectRejectsSomethingElseListening(t *testing.T) {
	notAlpaca := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html>router admin page</html>`)
	}))
	defer notAlpaca.Close()

	prof := &config.Profile{ID: "server-1", Name: "test", APIKey: testKey,
		LAN: []string{hostPort(notAlpaca.URL)}}

	_, err := Connect(context.Background(), prof, Options{SkipDiscovery: true, Timeout: 3 * time.Second})
	if err == nil {
		t.Fatal("Connect succeeded against a non-alpaca server")
	}
}

// LAN must win when both routes work — otherwise a machine in the same room is
// reached by going out to the internet and back.
func TestConnectPrefersLANOverPublic(t *testing.T) {
	lan := fakeGateway(t, "server-1", nil)
	publicEndpoint, fingerprint := fakeTLSGateway(t, "server-1")

	prof := &config.Profile{
		ID: "server-1", Name: "test", APIKey: testKey,
		LAN:         []string{hostPort(lan.URL)},
		Public:      publicEndpoint,
		Fingerprint: fingerprint,
	}

	for i := 0; i < 5; i++ {
		c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		if c.Route().Source != SourceHint {
			t.Fatalf("attempt %d chose %s (%s), want the LAN hint", i, c.Route().Endpoint, c.Route().Source)
		}
	}
}

// When LAN is unreachable the public route must still get its turn.
func TestConnectFallsBackToPublic(t *testing.T) {
	publicEndpoint, fingerprint := fakeTLSGateway(t, "server-1")

	prof := &config.Profile{
		ID: "server-1", Name: "test", APIKey: testKey,
		LAN:         []string{"127.0.0.1:1"}, // refused immediately
		Public:      publicEndpoint,
		Fingerprint: fingerprint,
	}

	c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.Route().Source != SourcePublic {
		t.Errorf("source = %q, want public", c.Route().Source)
	}
	if !c.Route().TLS {
		t.Error("the public route must be TLS — that traffic crosses untrusted networks")
	}
}

// The public route runs over TLS with a pinned certificate and no CA involved.
func TestConnectUsesPinnedTLSForPublic(t *testing.T) {
	publicEndpoint, fingerprint := fakeTLSGateway(t, "server-1")

	prof := &config.Profile{
		ID: "server-1", Name: "test", APIKey: testKey,
		Public:      publicEndpoint,
		Fingerprint: fingerprint,
	}

	c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect over pinned TLS: %v", err)
	}
	if !c.Route().TLS {
		t.Error("route is not marked as TLS")
	}
	if !strings.HasPrefix(c.baseURL, "https://") {
		t.Errorf("baseURL = %q, want https", c.baseURL)
	}
}

func TestConnectFailsPinnedTLSOnWrongFingerprint(t *testing.T) {
	publicEndpoint, _ := fakeTLSGateway(t, "server-1")

	prof := &config.Profile{
		ID: "server-1", Name: "test", APIKey: testKey,
		Public:      publicEndpoint,
		Fingerprint: strings.Repeat("ab", 32), // not this server's certificate
	}

	_, err := Connect(context.Background(), prof, Options{SkipDiscovery: true, Timeout: 4 * time.Second})
	if err == nil {
		t.Fatal("Connect succeeded despite a fingerprint mismatch")
	}
}

func TestConnectErrorNamesEveryRouteTried(t *testing.T) {
	prof := &config.Profile{
		ID: "server-1", Name: "workshop", APIKey: testKey,
		LAN:    []string{"127.0.0.1:1", "127.0.0.1:2"},
		Public: "127.0.0.1:3",
	}

	_, err := Connect(context.Background(), prof, Options{SkipDiscovery: true, Timeout: 4 * time.Second})
	if err == nil {
		t.Fatal("Connect succeeded with no reachable routes")
	}
	for _, want := range []string{"workshop", "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3", "alpaca serve"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message is missing %q:\n%s", want, err)
		}
	}
}

func TestForceEndpointSkipsTheRace(t *testing.T) {
	live := fakeGateway(t, "server-1", nil)
	other := fakeGateway(t, "server-1", nil)

	prof := &config.Profile{ID: "server-1", Name: "test", APIKey: testKey,
		LAN: []string{hostPort(other.URL)}}

	c, err := Connect(context.Background(), prof, Options{ForceEndpoint: hostPort(live.URL)})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.Route().Endpoint != hostPort(live.URL) {
		t.Errorf("endpoint = %q, want the forced one", c.Route().Endpoint)
	}
}

func TestRememberRoutePersistsTheWinner(t *testing.T) {
	t.Setenv("ALPACA_HOME", t.TempDir())
	live := fakeGateway(t, "server-1", nil)

	profiles, err := config.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	name := profiles.Add(&config.Profile{
		ID: "server-1", Name: "test", APIKey: testKey,
		LAN: []string{hostPort(live.URL)},
	})
	if err := profiles.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	c, err := Connect(context.Background(), profiles.Entries[name], Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := c.RememberRoute(profiles); err != nil {
		t.Fatalf("RememberRoute: %v", err)
	}

	// Reload from disk to prove it was actually written.
	reloaded, err := config.LoadProfiles()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Entries[name].LastGood; got != hostPort(live.URL) {
		t.Errorf("LastGood = %q, want %q", got, hostPort(live.URL))
	}
}

// The cached route is tried with no delay, so a working cache should win.
func TestCachedRouteIsPreferred(t *testing.T) {
	cached := fakeGateway(t, "server-1", nil)

	prof := &config.Profile{
		ID: "server-1", Name: "test", APIKey: testKey,
		LastGood: hostPort(cached.URL),
		LAN:      []string{"127.0.0.1:1"},
	}

	c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.Route().Source != SourceCached {
		t.Errorf("source = %q, want cached", c.Route().Source)
	}
}

// A stale cache must not be fatal — the other routes still race.
func TestStaleCacheFallsThroughToHints(t *testing.T) {
	live := fakeGateway(t, "server-1", nil)

	prof := &config.Profile{
		ID: "server-1", Name: "test", APIKey: testKey,
		LastGood: "127.0.0.1:1", // machine moved networks since last time
		LAN:      []string{hostPort(live.URL)},
	}

	c, err := Connect(context.Background(), prof, Options{SkipDiscovery: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.Route().Endpoint != hostPort(live.URL) {
		t.Errorf("endpoint = %q, want the live hint", c.Route().Endpoint)
	}
}
