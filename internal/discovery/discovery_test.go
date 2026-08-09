package discovery

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

func TestFromEntryParsesTXTRecords(t *testing.T) {
	entry := &zeroconf.ServiceEntry{
		Port: 8080,
		Text: []string{"v=1", "id=abc123", "name=workshop", "fp=deadbeef", "junk-without-equals"},
	}
	entry.Instance = "workshop"
	entry.AddrIPv4 = append(entry.AddrIPv4, parseIP(t, "192.168.1.20"))
	entry.AddrIPv6 = append(entry.AddrIPv6,
		parseIP(t, "fe80::1"),                // link-local: unusable without a zone
		parseIP(t, "2001:db8:1234:5678::30"), // global: fine
	)

	got := fromEntry(entry)

	if got.ID != "abc123" || got.Name != "workshop" || got.Fingerprint != "deadbeef" {
		t.Errorf("parsed = %+v, want the TXT values", got)
	}
	// IPv4 first, link-local v6 dropped.
	want := []string{"192.168.1.20:8080", "[2001:db8:1234:5678::30]:8080"}
	if len(got.Endpoints) != len(want) {
		t.Fatalf("endpoints = %v, want %v", got.Endpoints, want)
	}
	for i := range want {
		if got.Endpoints[i] != want[i] {
			t.Errorf("endpoints = %v, want %v", got.Endpoints, want)
		}
	}
}

func TestFromEntryIgnoresForeignServices(t *testing.T) {
	entry := &zeroconf.ServiceEntry{Port: 80, Text: []string{"something=else"}}
	entry.AddrIPv4 = append(entry.AddrIPv4, parseIP(t, "10.0.0.1"))

	// No id means it is not an alpaca server; browse() drops these.
	if got := fromEntry(entry); got.ID != "" {
		t.Errorf("ID = %q, want empty for a non-alpaca service", got.ID)
	}
}

// A genuine advertise-then-discover round trip over the loopback/local
// interfaces. Skipped when the environment blocks multicast, which is common in
// containers and locked-down CI.
func TestAdvertiseAndFind(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multicast test in short mode")
	}

	const id = "testserver01"
	ad, err := Advertise(id, "test-instance", 18080, "fingerprint123")
	if err != nil {
		t.Skipf("cannot advertise on this network: %v", err)
	}
	defer ad.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	got, err := Find(ctx, id)
	if err != nil {
		t.Skipf("multicast discovery unavailable in this environment: %v", err)
	}

	if got.ID != id {
		t.Errorf("ID = %q, want %q", got.ID, id)
	}
	if got.Fingerprint != "fingerprint123" {
		t.Errorf("fingerprint = %q, want it carried in TXT", got.Fingerprint)
	}
	if len(got.Endpoints) == 0 {
		t.Error("no endpoints discovered")
	}
	t.Logf("discovered %s (%s) at %v", got.Name, got.ID, got.Endpoints)
}

// Find must not return some other alpaca server that happens to be on the
// network.
func TestFindIgnoresOtherServers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multicast test in short mode")
	}

	ad, err := Advertise("otherserver99", "other-instance", 18081, "fp")
	if err != nil {
		t.Skipf("cannot advertise on this network: %v", err)
	}
	defer ad.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if got, err := Find(ctx, "theonewewant"); err == nil {
		t.Errorf("Find returned %+v for an id that was never advertised", got)
	}
}

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad test IP %q", s)
	}
	return ip
}
