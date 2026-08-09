package netx

import (
	"net"
	"sort"
)

// tailscaleCGNAT is the 100.64.0.0/10 range Tailscale assigns. Addresses there
// behave like LAN addresses — routable, stable, already authenticated by the
// tailnet — so they belong in the LAN hint list even though RFC 1918 does not
// cover them.
var tailscaleCGNAT = net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// LocalIPs lists addresses on which this machine can plausibly be reached by
// another machine, best candidate first.
//
// Ordering matters because these become the LAN hints in a connect string, and
// the client races them: putting the likeliest winner first keeps the common
// case fast even when several routes answer.
func LocalIPs() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var out []net.IP
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
			out = append(out, ip)
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return ipRank(out[i]) < ipRank(out[j]) })
	return out
}

// ipRank scores an address by how likely it is to be the one that works.
func ipRank(ip net.IP) int {
	v4 := ip.To4() != nil
	switch {
	case v4 && ip.IsPrivate():
		return 0 // ordinary home/office LAN
	case v4 && tailscaleCGNAT.Contains(ip):
		return 1 // tailnet: works from anywhere, slightly slower than LAN
	case v4:
		return 2 // a publicly routable v4 on the host itself
	case ip.IsPrivate():
		return 3 // IPv6 unique-local
	default:
		return 4 // IPv6 global
	}
}

// LocalHostPorts renders LocalIPs as host:port strings ready for a connect
// string, bracketing IPv6 correctly.
func LocalHostPorts(port string) []string {
	ips := LocalIPs()
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.JoinHostPort(ip.String(), port))
	}
	return out
}
