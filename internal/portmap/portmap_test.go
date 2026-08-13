package portmap

import (
	"context"
	"errors"
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

// fakeIGD implements upnpConn in memory, so the UPnP mapping logic — lease
// fallback, endpoint wiring, renew and release — is tested without a router.
type fakeIGD struct {
	externalIP   string
	rejectFinite bool
	addCalls     []uint32 // lease durations seen
	deleted      []uint16
	localAddr    net.IP
}

func (f *fakeIGD) GetExternalIPAddressCtx(context.Context) (string, error) {
	return f.externalIP, nil
}

func (f *fakeIGD) AddPortMappingCtx(_ context.Context, _ string, externalPort uint16, _ string,
	_ uint16, _ string, _ bool, _ string, lease uint32) error {
	f.addCalls = append(f.addCalls, lease)
	if f.rejectFinite && lease != 0 {
		return errors.New("only permanent leases supported")
	}
	return nil
}

func (f *fakeIGD) DeletePortMappingCtx(_ context.Context, _ string, externalPort uint16, _ string) error {
	f.deleted = append(f.deleted, externalPort)
	return nil
}

func (f *fakeIGD) LocalAddr() net.IP { return f.localAddr }

func TestMapViaWiresTheMapping(t *testing.T) {
	igd := &fakeIGD{externalIP: "203.0.113.9", localAddr: net.ParseIP("192.168.1.20")}

	m, err := mapVia(context.Background(), igd, 8080)
	if err != nil {
		t.Fatalf("mapVia: %v", err)
	}
	if got := m.Endpoint(); got != "203.0.113.9:8080" {
		t.Errorf("Endpoint = %q", got)
	}
	if m.Method != "UPnP" {
		t.Errorf("Method = %q", m.Method)
	}
	if len(igd.addCalls) != 1 || igd.addCalls[0] == 0 {
		t.Errorf("addCalls = %v, want one finite-lease request", igd.addCalls)
	}

	m.Release()
	if len(igd.deleted) != 1 || igd.deleted[0] != 8080 {
		t.Errorf("Release deleted %v, want [8080]", igd.deleted)
	}
}

// Plenty of routers reject any finite lease; the retry with 0 is what makes
// mapping work on that hardware.
func TestMapViaFallsBackToPermanentLease(t *testing.T) {
	igd := &fakeIGD{externalIP: "203.0.113.9", localAddr: net.ParseIP("192.168.1.20"), rejectFinite: true}

	if _, err := mapVia(context.Background(), igd, 8080); err != nil {
		t.Fatalf("mapVia: %v", err)
	}
	if len(igd.addCalls) != 2 || igd.addCalls[1] != 0 {
		t.Errorf("addCalls = %v, want a finite attempt then a permanent one", igd.addCalls)
	}
}

func TestMapViaRejectsUnparseableExternalIP(t *testing.T) {
	igd := &fakeIGD{externalIP: "not-an-ip", localAddr: net.ParseIP("192.168.1.20")}
	if _, err := mapVia(context.Background(), igd, 8080); err == nil {
		t.Fatal("mapVia accepted an unparseable external IP")
	}
}

func TestMapViaNeedsALocalAddress(t *testing.T) {
	igd := &fakeIGD{externalIP: "203.0.113.9"}
	if _, err := mapVia(context.Background(), igd, 8080); err == nil {
		t.Fatal("mapVia proceeded without knowing which local address faces the gateway")
	}
}

// A DHCP renewal can move the WAN address while the mapping stays up; renew
// must report the address the router holds now, not the one from startup.
func TestRenewReportsAMovedExternalIP(t *testing.T) {
	igd := &fakeIGD{externalIP: "203.0.113.9", localAddr: net.ParseIP("192.168.1.20")}
	m, err := mapVia(context.Background(), igd, 8080)
	if err != nil {
		t.Fatalf("mapVia: %v", err)
	}

	igd.externalIP = "198.51.100.7"
	ip, port, err := m.renew(context.Background())
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !ip.Equal(net.ParseIP("198.51.100.7")) {
		t.Errorf("renew reported %v, want the router's current address", ip)
	}
	if port != 8080 {
		t.Errorf("renew reported port %d, want 8080", port)
	}
}
