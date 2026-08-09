package portmap

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

// Reachable is the check that stops alpaca from printing a public endpoint that
// silently cannot be reached — the most confusing possible failure mode.
func TestReachableRejectsUnroutableAddresses(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
		why  string
	}{
		{"203.0.113.9", true, "ordinary public v4"},
		{"2001:db8:1234:5678::30", true, "global v6"},
		{"192.168.1.1", false, "rfc1918 — double NAT"},
		{"10.0.0.1", false, "rfc1918"},
		{"172.16.5.4", false, "rfc1918"},
		{"100.64.0.1", false, "carrier-grade NAT: mapping exists but is useless"},
		{"100.127.255.255", false, "top of the CGNAT range"},
		{"127.0.0.1", false, "loopback"},
		{"0.0.0.0", false, "unspecified"},
		{"169.254.10.1", false, "link-local"},
	}

	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			m := &Mapping{ExternalIP: net.ParseIP(tc.ip)}
			if got := m.Reachable(); got != tc.want {
				t.Errorf("Reachable() = %v for %s (%s), want %v", got, tc.ip, tc.why, tc.want)
			}
		})
	}
}

// 100.64.0.0/10 is CGNAT, but 100.128.0.0 and up are ordinary public space and
// must not be caught by the same check.
func TestReachableBoundaryOfCGNAT(t *testing.T) {
	if (&Mapping{ExternalIP: net.ParseIP("100.63.255.255")}).Reachable() != true {
		t.Error("100.63.255.255 is below the CGNAT range and should be reachable")
	}
	if (&Mapping{ExternalIP: net.ParseIP("100.128.0.1")}).Reachable() != true {
		t.Error("100.128.0.1 is above the CGNAT range and should be reachable")
	}
}

func TestReachableHandlesNilIP(t *testing.T) {
	if (&Mapping{}).Reachable() {
		t.Error("a mapping with no external IP must not claim to be reachable")
	}
}

func TestEndpointFormatsIPv6WithBrackets(t *testing.T) {
	m := &Mapping{ExternalIP: net.ParseIP("2001:db8::30"), ExternalPort: 8080}
	if got := m.Endpoint(); got != "[2001:db8::30]:8080" {
		t.Errorf("Endpoint() = %q, want brackets around the v6 literal", got)
	}
}

func TestReleaseOnNilMappingIsSafe(t *testing.T) {
	var m *Mapping
	m.Release() // must not panic: serve calls this unconditionally on shutdown
}

// Opt-in: this actually talks to the router on the current network.
//
//	ALPACA_LIVE=1 go test ./internal/portmap/ -run Live -v
func TestLiveMapPort(t *testing.T) {
	if os.Getenv("ALPACA_LIVE") == "" {
		t.Skip("set ALPACA_LIVE=1 to attempt a real port mapping on this network")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	m, err := Map(ctx, 18099)
	if err != nil {
		t.Skipf("no port mapping available on this network (this is normal): %v", err)
	}
	defer m.Release()

	t.Logf("mapped via %s to %s (reachable from the internet: %v)",
		m.Method, m.Endpoint(), m.Reachable())

	if m.ExternalIP == nil {
		t.Error("mapping has no external IP")
	}
	if m.ExternalPort == 0 {
		t.Error("mapping has no external port")
	}
}
