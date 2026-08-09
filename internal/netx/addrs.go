package netx

import (
	"net"
	"sort"
)

// tailscaleCGNAT is the 100.64.0.0/10 range Tailscale assigns. Addresses there
// behave like LAN addresses — routable, stable, and already encrypted and
// authenticated by WireGuard underneath — so they are safe to reach over plain
// HTTP even though RFC 1918 does not cover them.
var tailscaleCGNAT = net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// Reachability describes what kind of network an address is reachable from,
// which decides whether plain HTTP is acceptable on it.
type Reachability int

const (
	// ReachLAN is a private address: RFC 1918 v4 or unique-local v6.
	ReachLAN Reachability = iota
	// ReachTailnet is a Tailscale address — remote, but already encrypted.
	ReachTailnet
	// ReachGlobal is routable from the open internet.
	ReachGlobal
)

// Trusted reports whether traffic to this address stays on a network alpaca can
// treat as private, and may therefore skip TLS.
//
// This is the line that keeps the fast path honest. A globally routable
// address — including an IPv6 GUA, which a home machine usually has — must
// never be offered as a plain-HTTP route: reached from another network that
// traffic would cross the public internet in cleartext, carrying the API key
// with it.
func (r Reachability) Trusted() bool { return r != ReachGlobal }

// Label names the reachability for the startup banner, in terms a user thinks
// in rather than RFC numbers.
func (r Reachability) Label() string {
	switch r {
	case ReachLAN:
		return "lan"
	case ReachTailnet:
		return "tailscale"
	default:
		return "internet"
	}
}

// Note explains the route in the banner.
func (r Reachability) Note() string {
	switch r {
	case ReachLAN:
		return "same network · plain http, fastest"
	case ReachTailnet:
		return "anywhere on your tailnet · encrypted by wireguard"
	default:
		return "reachable from outside · tls with a pinned certificate"
	}
}

// ClassifyIP determines an address's reachability.
func ClassifyIP(ip net.IP) Reachability {
	if ip.To4() != nil && tailscaleCGNAT.Contains(ip) {
		return ReachTailnet
	}
	// IsPrivate covers RFC 1918 for v4 and fc00::/7 unique-local for v6.
	if ip.IsPrivate() {
		return ReachLAN
	}
	return ReachGlobal
}

// Addr is a local address together with how it can be reached.
type Addr struct {
	IP    net.IP
	Reach Reachability
}

// HostPort renders the address for dialing, bracketing IPv6 correctly.
func (a Addr) HostPort(port string) string {
	return net.JoinHostPort(a.IP.String(), port)
}

// LocalAddrs lists the addresses on which this machine can be reached by
// another machine, best candidate first.
//
// Ordering matters because these become the hints in a connect string, and the
// client races them: putting the likeliest winner first keeps the common case
// fast when several routes answer at once.
func LocalAddrs() []Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var out []Addr
	for _, iface := range ifaces {
		// Skip interfaces that are administratively down or purely local.
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			// Link-local addresses need a zone index to be usable and never
			// survive being written into a config file, so they are useless
			// as hints.
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
				continue
			}
			out = append(out, Addr{IP: ip, Reach: ClassifyIP(ip)})
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// rank scores an address by how likely it is to be the one that works well.
func rank(a Addr) int {
	v4 := a.IP.To4() != nil
	switch {
	case a.Reach == ReachLAN && v4:
		return 0 // ordinary home/office LAN
	case a.Reach == ReachLAN:
		return 1 // IPv6 unique-local
	case a.Reach == ReachTailnet:
		return 2 // works from anywhere, slightly slower than LAN
	case v4:
		return 3 // publicly routable v4 on the host itself
	default:
		return 4 // IPv6 global
	}
}

// TrustedHostPorts returns the addresses safe to offer as plain-HTTP routes.
func TrustedHostPorts(port string) []string {
	var out []string
	for _, addr := range LocalAddrs() {
		if addr.Reach.Trusted() {
			out = append(out, addr.HostPort(port))
		}
	}
	return out
}

// GlobalHostPorts returns the internet-routable addresses, which alpaca only
// ever offers over TLS.
func GlobalHostPorts(port string) []string {
	var out []string
	for _, addr := range LocalAddrs() {
		if !addr.Reach.Trusted() {
			out = append(out, addr.HostPort(port))
		}
	}
	return out
}

// AllIPs lists every usable local IP, for certificate SANs.
func AllIPs() []net.IP {
	addrs := LocalAddrs()
	out := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.IP)
	}
	return out
}
