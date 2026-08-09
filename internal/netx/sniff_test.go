package netx

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// harness is the real arrangement alpaca uses: one port, plain HTTP and TLS
// multiplexed onto it by the sniffer.
type harness struct {
	addr        string
	fingerprint string
	plainSrv    *http.Server
	tlsSrv      *http.Server
}

// serveBoth stands up the harness and returns the address to dial plus the
// certificate fingerprint clients should pin.
func serveBoth(t *testing.T) (addr, fingerprint string) {
	h := serveBothH(t)
	return h.addr, h.fingerprint
}

func serveBothH(t *testing.T) *harness {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	sniffer := Sniff(ln)
	t.Cleanup(func() { sniffer.Close() })

	id, err := CreateIdentity(t.TempDir(), []string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Echo back which transport carried the request so tests can prove the
		// sniffer routed it to the right server.
		scheme := "plain"
		if r.TLS != nil {
			scheme = "tls"
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
		fmt.Fprintf(w, "%s:%s:%s", scheme, r.URL.Path, body)
	})

	plainSrv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	tlsSrv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go plainSrv.Serve(sniffer.Plain())
	go tlsSrv.Serve(tls.NewListener(sniffer.TLS(), id.ServerTLSConfig()))
	t.Cleanup(func() {
		plainSrv.Close()
		tlsSrv.Close()
	})

	return &harness{
		addr:        ln.Addr().String(),
		fingerprint: id.Fingerprint,
		plainSrv:    plainSrv,
		tlsSrv:      tlsSrv,
	}
}

func TestSnifferRoutesPlainHTTP(t *testing.T) {
	addr, _ := serveBoth(t)

	resp, err := http.Post("http://"+addr+"/hello", "text/plain", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("plain request: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)

	if string(got) != "plain:/hello:body" {
		t.Errorf("response = %q, want %q", got, "plain:/hello:body")
	}
}

func TestSnifferRoutesTLS(t *testing.T) {
	addr, fingerprint := serveBoth(t)

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedClientConfig(fingerprint)}}
	resp, err := client.Post("https://"+addr+"/secure", "text/plain", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("tls request: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)

	if string(got) != "tls:/secure:body" {
		t.Errorf("response = %q, want %q", got, "tls:/secure:body")
	}
}

// The whole point of the design: both protocols on one port, at the same time.
func TestSnifferHandlesBothConcurrently(t *testing.T) {
	addr, fingerprint := serveBoth(t)
	tlsClient := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedClientConfig(fingerprint)}}

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			resp, err := http.Get(fmt.Sprintf("http://%s/p%d", addr, i))
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if want := fmt.Sprintf("plain:/p%d:", i); string(body) != want {
				errs <- fmt.Errorf("plain got %q want %q", body, want)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			resp, err := tlsClient.Get(fmt.Sprintf("https://%s/s%d", addr, i))
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if want := fmt.Sprintf("tls:/s%d:", i); string(body) != want {
				errs <- fmt.Errorf("tls got %q want %q", body, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// The sniffed byte must be replayed, or every request would arrive with its
// first character missing ("ET /" instead of "GET /").
func TestSnifferPreservesFirstByte(t *testing.T) {
	addr, _ := serveBoth(t)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /verbatim HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "plain:/verbatim:") {
		t.Errorf("raw response did not contain the echoed path:\n%s", raw)
	}
	if !strings.HasPrefix(string(raw), "HTTP/1.1 200") {
		t.Errorf("response did not start with a 200 status line:\n%s", raw)
	}
}

// A client that connects and says nothing must not wedge the listener for
// everyone else.
func TestSnifferSilentConnectionDoesNotBlockOthers(t *testing.T) {
	addr, _ := serveBoth(t)

	silent, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer silent.Close()

	// With the silent connection still open and unclassified, normal traffic
	// must keep flowing.
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/live", nil)
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			t.Fatalf("request %d blocked behind the silent connection: %v", i, err)
		}
		resp.Body.Close()
	}
}

func TestPinnedClientConfigRejectsWrongFingerprint(t *testing.T) {
	addr, _ := serveBoth(t)

	// A well-formed but different fingerprint stands in for an interceptor.
	wrong := strings.Repeat("ab", 32)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedClientConfig(wrong)}}

	_, err := client.Get("https://" + addr + "/")
	if err == nil {
		t.Fatal("request succeeded with a mismatched pin, want failure")
	}
	if !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Errorf("error = %v, want a fingerprint mismatch", err)
	}
}

func TestSnifferCloseUnblocksAccept(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	sniffer := Sniff(ln)

	done := make(chan error, 2)
	go func() { _, err := sniffer.Plain().Accept(); done <- err }()
	go func() { _, err := sniffer.TLS().Accept(); done <- err }()

	sniffer.Close()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err == nil {
				t.Error("Accept returned a connection after Close")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Accept did not return after Close")
		}
	}
}

// Both virtual listeners share one real socket, so shutting down one protocol's
// server must not take the other down with it. Closing must also actually
// return: http.Server.Close waits for Serve to exit, so a Close that failed to
// unblock Accept would hang shutdown forever.
func TestClosingOneProtocolLeavesTheOtherServing(t *testing.T) {
	h := serveBothH(t)
	tlsClient := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedClientConfig(h.fingerprint)}}

	// Both alive to begin with.
	resp, err := tlsClient.Get("https://" + h.addr + "/before")
	if err != nil {
		t.Fatalf("tls request before shutdown: %v", err)
	}
	resp.Body.Close()

	closed := make(chan error, 1)
	go func() { closed <- h.plainSrv.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("plain server Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("plain server Close hung — virtualListener.Close did not unblock Accept")
	}

	// TLS must be entirely unaffected.
	resp, err = tlsClient.Get("https://" + h.addr + "/after")
	if err != nil {
		t.Fatalf("tls stopped working after the plain server shut down: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "tls:/after:" {
		t.Errorf("response = %q, want %q", body, "tls:/after:")
	}
}

func TestIdentityPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreateIdentity(dir, []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	second, err := LoadOrCreateIdentity(dir, []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("second load: %v", err)
	}

	// Regenerating would invalidate every connect string already handed out.
	if first.Fingerprint != second.Fingerprint {
		t.Errorf("fingerprint changed across restarts: %s -> %s", first.Fingerprint, second.Fingerprint)
	}
	if len(first.Fingerprint) != 64 {
		t.Errorf("fingerprint %q is not a hex sha-256", first.Fingerprint)
	}
}

func TestLocalHostPortsAreDialable(t *testing.T) {
	for _, hp := range append(TrustedHostPorts("8080"), GlobalHostPorts("8080")...) {
		host, port, err := net.SplitHostPort(hp)
		if err != nil {
			t.Errorf("produced %q, which is not a host:port: %v", hp, err)
			continue
		}
		if port != "8080" {
			t.Errorf("%q has port %q, want 8080", hp, port)
		}
		if net.ParseIP(host) == nil {
			t.Errorf("%q has host %q, which is not an IP", hp, host)
		}
	}
	t.Logf("trusted: %v", TrustedHostPorts("8080"))
	t.Logf("global:  %v", GlobalHostPorts("8080"))
}

// The split between these two lists is a security boundary, not a cosmetic
// one: anything in the trusted list is offered as a plain-HTTP route, so a
// globally routable address appearing there would send the API key across the
// internet in cleartext.
func TestTrustedAndGlobalAreDisjointAndCorrect(t *testing.T) {
	trusted := TrustedHostPorts("8080")
	global := GlobalHostPorts("8080")

	inGlobal := map[string]bool{}
	for _, hp := range global {
		inGlobal[hp] = true
	}

	for _, hp := range trusted {
		if inGlobal[hp] {
			t.Errorf("%q appears in both lists", hp)
		}
		host, _, _ := net.SplitHostPort(hp)
		ip := net.ParseIP(host)
		if ClassifyIP(ip) == ReachGlobal {
			t.Errorf("%q is internet-routable but was offered as a plain-HTTP route", hp)
		}
	}
	for _, hp := range global {
		host, _, _ := net.SplitHostPort(hp)
		if ClassifyIP(net.ParseIP(host)) != ReachGlobal {
			t.Errorf("%q is not internet-routable but was put in the global list", hp)
		}
	}
}

func TestClassifyIP(t *testing.T) {
	cases := []struct {
		ip   string
		want Reachability
	}{
		{"192.168.1.10", ReachLAN},
		{"10.4.4.4", ReachLAN},
		{"172.20.0.9", ReachLAN},
		{"fd7a:115c:a1e0::1", ReachLAN},     // unique-local v6
		{"100.98.21.63", ReachTailnet},      // tailscale CGNAT
		{"2600:1702:891b::30", ReachGlobal}, // v6 GUA — routable, must not be plain http
		{"8.8.8.8", ReachGlobal},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			if got := ClassifyIP(net.ParseIP(tc.ip)); got != tc.want {
				t.Errorf("ClassifyIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
			if tc.want == ReachGlobal && ClassifyIP(net.ParseIP(tc.ip)).Trusted() {
				t.Errorf("%s is routable from the internet but reports as trusted", tc.ip)
			}
		})
	}
}
