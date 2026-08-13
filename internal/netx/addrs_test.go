package netx

import (
	"net"
	"testing"
)

func TestClassifyIP(t *testing.T) {
	cases := []struct {
		ip   string
		want Reachability
	}{
		{"192.168.1.20", ReachLAN},
		{"10.4.4.4", ReachLAN},
		{"172.20.0.9", ReachLAN},
		{"fd7a:115c:a1e0::1", ReachLAN},   // IPv6 unique-local
		{"100.64.0.1", ReachTailnet},      // bottom of the Tailscale range
		{"100.127.255.255", ReachTailnet}, // top of it
		{"100.63.255.255", ReachGlobal},   // one below the CGNAT range
		{"100.128.0.1", ReachGlobal},      // one above it
		{"203.0.113.9", ReachGlobal},
		{"2001:db8:1234::30", ReachGlobal}, // v6 GUA — routable, must not be plain http
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			if got := ClassifyIP(net.ParseIP(tc.ip)); got != tc.want {
				t.Errorf("ClassifyIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

// Trusted is the line that decides whether the API key may cross a network in
// cleartext; only global addresses are on the wrong side of it.
func TestTrusted(t *testing.T) {
	if ReachGlobal.Trusted() {
		t.Error("a globally routable address must never be trusted with plain HTTP")
	}
	if !ReachLAN.Trusted() || !ReachTailnet.Trusted() {
		t.Error("LAN and tailnet addresses are private and should be trusted")
	}
}

// The ordering encodes which route usually wins: plain LAN v4 first, global
// IPv6 last. A regression here reorders every connect string's hints.
func TestRankOrdersLANFirst(t *testing.T) {
	addrs := []Addr{
		{IP: net.ParseIP("2001:db8::1"), Reach: ReachGlobal},
		{IP: net.ParseIP("100.64.0.5"), Reach: ReachTailnet},
		{IP: net.ParseIP("192.168.1.20"), Reach: ReachLAN},
		{IP: net.ParseIP("fd00::1"), Reach: ReachLAN},
		{IP: net.ParseIP("203.0.113.9"), Reach: ReachGlobal},
	}
	wantOrder := []string{"192.168.1.20", "fd00::1", "100.64.0.5", "203.0.113.9", "2001:db8::1"}

	for i := range addrs {
		for j := i + 1; j < len(addrs); j++ {
			less := rank(addrs[i]) < rank(addrs[j])
			iPos, jPos := indexOf(wantOrder, addrs[i].IP.String()), indexOf(wantOrder, addrs[j].IP.String())
			if less != (iPos < jPos) {
				t.Errorf("rank puts %s and %s in the wrong order", addrs[i].IP, addrs[j].IP)
			}
		}
	}
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// Container and VM bridges hold real RFC 1918 addresses no other machine can
// dial; offering them as hints wastes a probe slot. Tailscale interfaces must
// survive the filter — their addresses are precisely the remote-access route.
func TestIsVirtualIface(t *testing.T) {
	virtual := []string{"docker0", "veth1a2b3c", "br-4d5e6f", "virbr0", "vmnet8", "podman0", "cni0", "lxdbr0"}
	for _, name := range virtual {
		if !isVirtualIface(name) {
			t.Errorf("isVirtualIface(%q) = false, want true", name)
		}
	}
	physical := []string{"eth0", "en0", "wlan0", "tailscale0", "utun4", "wg0", "bond0", "enp3s0"}
	for _, name := range physical {
		if isVirtualIface(name) {
			t.Errorf("isVirtualIface(%q) = true, want false", name)
		}
	}
}

func TestHostPortBracketsIPv6(t *testing.T) {
	a := Addr{IP: net.ParseIP("fd00::1"), Reach: ReachLAN}
	if got := a.HostPort("8080"); got != "[fd00::1]:8080" {
		t.Errorf("HostPort = %q, want bracketed IPv6", got)
	}
	b := Addr{IP: net.ParseIP("192.168.1.20"), Reach: ReachLAN}
	if got := b.HostPort("8080"); got != "192.168.1.20:8080" {
		t.Errorf("HostPort = %q", got)
	}
}
