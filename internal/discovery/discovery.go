// Package discovery lets a client find its server on the local network without
// being told an address.
//
// This is what makes a connect string survive a DHCP lease change: the address
// captured when the string was issued goes stale, but the server's identity
// does not, so the client can simply ask the network where that identity lives
// now.
package discovery

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

const (
	// Service is alpaca's DNS-SD service type.
	Service = "_alpaca._tcp"
	// Domain is the mDNS domain; ".local." is the only one multicast DNS uses.
	Domain = "local."
)

// TXT record keys. Kept short because DNS-SD TXT records are size-constrained.
const (
	keyID          = "id"
	keyName        = "name"
	keyFingerprint = "fp"
	keyVersion     = "v"
)

// Advertisement is a running mDNS registration.
type Advertisement struct {
	server *zeroconf.Server
}

// Advertise announces this server on the local network.
//
// The certificate fingerprint travels in the TXT record so a client that
// discovers a server on a network where TLS is wanted can pin it without
// another round trip.
func Advertise(id, name string, port int, fingerprint string) (*Advertisement, error) {
	txt := []string{
		keyVersion + "=1",
		keyID + "=" + id,
		keyName + "=" + name,
		keyFingerprint + "=" + fingerprint,
	}

	// The instance name is what shows up in network browsers, so it should be
	// the human name; the id in TXT is what clients actually match on.
	instance := name
	if instance == "" {
		instance = "alpaca-" + id
	}

	server, err := zeroconf.Register(instance, Service, Domain, port, txt, nil)
	if err != nil {
		return nil, fmt.Errorf("advertise on mdns: %w", err)
	}
	return &Advertisement{server: server}, nil
}

// Close withdraws the advertisement, sending goodbye packets so clients drop
// the stale record immediately instead of waiting for it to time out.
func (a *Advertisement) Close() {
	if a != nil && a.server != nil {
		a.server.Shutdown()
	}
}

// Result is a server found on the network.
type Result struct {
	ID          string
	Name        string
	Fingerprint string
	// Endpoints are dialable host:port strings, best candidate first.
	Endpoints []string
}

// Find waits for the server with the given id and returns as soon as it
// answers.
//
// It returns early on the first match rather than waiting out the full window,
// because this sits directly in the client's startup path.
func Find(ctx context.Context, wantID string) (*Result, error) {
	found := make(chan Result, 1)
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		_ = browse(scanCtx, func(r Result) bool {
			if wantID == "" || r.ID == wantID {
				select {
				case found <- r:
				default:
				}
				return false // stop browsing
			}
			return true
		})
	}()

	select {
	case r := <-found:
		return &r, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("no alpaca server answered on the local network: %w", ctx.Err())
	}
}

// List collects every server that answers before ctx expires.
func List(ctx context.Context) ([]Result, error) {
	var out []Result
	seen := map[string]bool{}

	err := browse(ctx, func(r Result) bool {
		if !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, r)
		}
		return true // keep going until the context expires
	})
	// A context deadline is the normal way a full scan ends, not a failure.
	if err != nil && ctx.Err() == nil {
		return out, err
	}
	return out, nil
}

// browse runs an mDNS query, invoking onResult for each answer. Returning false
// from onResult stops the scan.
func browse(ctx context.Context, onResult func(Result) bool) error {
	entries := make(chan *zeroconf.ServiceEntry, 8)
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for entry := range entries {
			result := fromEntry(entry)
			if result.ID == "" || len(result.Endpoints) == 0 {
				continue // not one of ours, or unusable
			}
			if !onResult(result) {
				cancel()
				// Keep draining so the resolver's send never blocks.
			}
		}
	}()

	err := zeroconf.Browse(scanCtx, Service, Domain, entries)
	<-done
	return err
}

// fromEntry converts an mDNS answer into a Result.
func fromEntry(entry *zeroconf.ServiceEntry) Result {
	r := Result{Name: entry.Instance}
	for _, txt := range entry.Text {
		key, value, found := strings.Cut(txt, "=")
		if !found {
			continue
		}
		switch key {
		case keyID:
			r.ID = value
		case keyName:
			r.Name = value
		case keyFingerprint:
			r.Fingerprint = value
		}
	}

	port := strconv.Itoa(entry.Port)
	// IPv4 first: it is what works on the widest range of home networks, and
	// the client races these in order.
	for _, ip := range entry.AddrIPv4 {
		r.Endpoints = append(r.Endpoints, net.JoinHostPort(ip.String(), port))
	}
	for _, ip := range entry.AddrIPv6 {
		// A link-local v6 address needs a zone to be dialable and would only
		// waste a slot in the client's race.
		if ip.IsLinkLocalUnicast() {
			continue
		}
		r.Endpoints = append(r.Endpoints, net.JoinHostPort(ip.String(), port))
	}
	return r
}

// DefaultTimeout is how long a discovery scan runs before giving up. Chosen to
// be long enough for a sleepy router to answer but short enough that a client
// falling back to the public path does not feel stalled.
const DefaultTimeout = 2 * time.Second
