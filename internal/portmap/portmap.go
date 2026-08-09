// Package portmap asks the router to forward a port, so the TLS fallback path
// works from outside the network without anyone opening the router's admin page.
//
// Both protocols here are best-effort by nature: plenty of routers have them
// disabled, and carrier-grade NAT makes them useless even when they succeed.
// Every failure is therefore reported as information rather than as a fatal
// error — LAN and Tailscale still work, and the caller decides what to tell the
// user.
package portmap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/jackpal/gateway"
	natpmp "github.com/jackpal/go-nat-pmp"
)

// leaseDuration is what alpaca asks for. A finite lease means a crashed server
// stops holding a hole open in the router forever; the maintainer renews it
// well before expiry.
const leaseDuration = 30 * time.Minute

// renewInterval refreshes at a third of the lease, so two consecutive failures
// still leave time to recover before the mapping lapses.
const renewInterval = leaseDuration / 3

// Mapping is an active port forward.
type Mapping struct {
	// ExternalIP is the address the router presents to the internet.
	ExternalIP net.IP
	// ExternalPort is the forwarded port, usually but not always the one asked for.
	ExternalPort int
	// InternalPort is the local port traffic arrives at.
	InternalPort int
	// Method names the protocol that worked, for display.
	Method string

	renew   func(context.Context) error
	release func(context.Context) error
}

// Endpoint renders the mapping as a host:port for a connect string.
func (m *Mapping) Endpoint() string {
	return net.JoinHostPort(m.ExternalIP.String(), fmt.Sprint(m.ExternalPort))
}

// Reachable reports whether the external address is actually routable from the
// internet.
//
// A router behind carrier-grade NAT happily reports a private or shared
// address; the forward it set up is real but useless from outside, and saying
// so up front is far kinder than letting the user discover it later.
func (m *Mapping) Reachable() bool {
	ip := m.ExternalIP
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return false
	}
	// 100.64.0.0/10 is the carrier-grade NAT range (RFC 6598).
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}

// Release tears down the mapping.
func (m *Mapping) Release() {
	if m == nil || m.release == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.release(ctx)
}

// Maintain renews the mapping until ctx is cancelled, then releases it.
func (m *Mapping) Maintain(ctx context.Context, log *slog.Logger) {
	ticker := time.NewTicker(renewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.Release()
			return
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := m.renew(renewCtx)
			cancel()
			if err != nil {
				// Losing the mapping is survivable: LAN and Tailscale are
				// unaffected, so log it and keep trying.
				log.Warn("could not renew port mapping", "method", m.Method, "error", err)
			} else {
				log.Debug("renewed port mapping", "method", m.Method, "endpoint", m.Endpoint())
			}
		}
	}
}

// Map forwards internalPort, trying NAT-PMP before UPnP.
//
// NAT-PMP goes first because it is a single small UDP exchange with a known
// address, whereas UPnP requires an SSDP multicast search and several SOAP
// round trips. On a router that speaks both, NAT-PMP answers in milliseconds.
func Map(ctx context.Context, internalPort int) (*Mapping, error) {
	var errs []error

	if m, err := mapNATPMP(ctx, internalPort); err == nil {
		return m, nil
	} else {
		errs = append(errs, fmt.Errorf("nat-pmp: %w", err))
	}

	if m, err := mapUPnP(ctx, internalPort); err == nil {
		return m, nil
	} else {
		errs = append(errs, fmt.Errorf("upnp: %w", err))
	}

	return nil, errors.Join(errs...)
}

// ---------------------------------------------------------------------------
// NAT-PMP
// ---------------------------------------------------------------------------

func mapNATPMP(ctx context.Context, internalPort int) (*Mapping, error) {
	gw, err := gateway.DiscoverGateway()
	if err != nil {
		return nil, fmt.Errorf("find gateway: %w", err)
	}

	client := natpmp.NewClientWithTimeout(gw, 2*time.Second)

	external, err := client.GetExternalAddress()
	if err != nil {
		return nil, fmt.Errorf("get external address: %w", err)
	}
	ip := net.IPv4(external.ExternalIPAddress[0], external.ExternalIPAddress[1],
		external.ExternalIPAddress[2], external.ExternalIPAddress[3])

	add := func() (int, error) {
		res, err := client.AddPortMapping("tcp", internalPort, internalPort, int(leaseDuration.Seconds()))
		if err != nil {
			return 0, err
		}
		return int(res.MappedExternalPort), nil
	}

	externalPort, err := add()
	if err != nil {
		return nil, fmt.Errorf("add mapping: %w", err)
	}

	return &Mapping{
		ExternalIP:   ip,
		ExternalPort: externalPort,
		InternalPort: internalPort,
		Method:       "NAT-PMP",
		renew: func(context.Context) error {
			_, err := add()
			return err
		},
		release: func(context.Context) error {
			// A zero lifetime is how NAT-PMP deletes a mapping.
			_, err := client.AddPortMapping("tcp", internalPort, 0, 0)
			return err
		},
	}, nil
}

// ---------------------------------------------------------------------------
// UPnP
// ---------------------------------------------------------------------------

// upnpConn is the slice of the IGD interface alpaca needs, shared by the three
// service flavours routers implement.
type upnpConn interface {
	GetExternalIPAddressCtx(ctx context.Context) (string, error)
	AddPortMappingCtx(ctx context.Context, remoteHost string, externalPort uint16, protocol string,
		internalPort uint16, internalClient string, enabled bool, description string, leaseDuration uint32) error
	DeletePortMappingCtx(ctx context.Context, remoteHost string, externalPort uint16, protocol string) error
	LocalAddr() net.IP
}

func mapUPnP(ctx context.Context, internalPort int) (*Mapping, error) {
	conns, err := discoverUPnP(ctx)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
		return nil, errors.New("no internet gateway device responded")
	}

	var errs []error
	for _, conn := range conns {
		m, err := mapVia(ctx, conn, internalPort)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		return m, nil
	}
	return nil, errors.Join(errs...)
}

// discoverUPnP searches for every IGD service flavour in use. Routers implement
// one of three depending on age and WAN type, and there is no way to know which
// without asking for all of them.
func discoverUPnP(ctx context.Context) ([]upnpConn, error) {
	var out []upnpConn

	if clients, _, err := internetgateway2.NewWANIPConnection2ClientsCtx(ctx); err == nil {
		for _, c := range clients {
			out = append(out, c)
		}
	}
	if clients, _, err := internetgateway2.NewWANIPConnection1ClientsCtx(ctx); err == nil {
		for _, c := range clients {
			out = append(out, c)
		}
	}
	if clients, _, err := internetgateway2.NewWANPPPConnection1ClientsCtx(ctx); err == nil {
		for _, c := range clients {
			out = append(out, c)
		}
	}

	if len(out) == 0 {
		return nil, errors.New("no upnp gateway found (it may be disabled on the router)")
	}
	return out, nil
}

func mapVia(ctx context.Context, conn upnpConn, internalPort int) (*Mapping, error) {
	raw, err := conn.GetExternalIPAddressCtx(ctx)
	if err != nil {
		return nil, fmt.Errorf("get external ip: %w", err)
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return nil, fmt.Errorf("gateway reported an unparseable external ip %q", raw)
	}

	local := conn.LocalAddr()
	if local == nil {
		return nil, errors.New("could not determine which local address faces the gateway")
	}

	port := uint16(internalPort)
	add := func(ctx context.Context) error {
		err := conn.AddPortMappingCtx(ctx, "", port, "TCP", port, local.String(), true, "alpaca",
			uint32(leaseDuration.Seconds()))
		if err == nil {
			return nil
		}
		// A good number of routers reject any finite lease with a vague SOAP
		// error and only accept permanent mappings. Retrying with 0 is what
		// makes this work on that hardware.
		return conn.AddPortMappingCtx(ctx, "", port, "TCP", port, local.String(), true, "alpaca", 0)
	}

	if err := add(ctx); err != nil {
		return nil, fmt.Errorf("add mapping: %w", err)
	}

	return &Mapping{
		ExternalIP:   ip,
		ExternalPort: internalPort,
		InternalPort: internalPort,
		Method:       "UPnP",
		renew:        add,
		release: func(ctx context.Context) error {
			return conn.DeletePortMappingCtx(ctx, "", port, "TCP")
		},
	}, nil
}
