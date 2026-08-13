package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCertHostsAlwaysCoverLoopback(t *testing.T) {
	hosts := certHosts("")
	for _, want := range []string{"localhost", "127.0.0.1", "::1"} {
		found := false
		for _, h := range hosts {
			if h == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("certHosts is missing %q", want)
		}
	}
}

func TestCertHostsIncludeThePublicHost(t *testing.T) {
	hosts := certHosts("example.com:8080")
	found := false
	for _, h := range hosts {
		if h == "example.com" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("certHosts(%q) did not include the public host: %v", "example.com:8080", hosts)
	}

	// A public address without a port must not sneak in malformed.
	for _, h := range certHosts("no-port-here") {
		if h == "no-port-here" {
			t.Error("certHosts accepted a public address that is not host:port")
		}
	}
}

func TestCertHostsHostnameGetsLocalSuffixOnce(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		t.Skip("no hostname on this machine")
	}
	var locals int
	for _, h := range certHosts("") {
		if h == strings.TrimSuffix(hostname, ".local")+".local" || h == hostname+".local" {
			locals++
		}
	}
	if strings.HasSuffix(hostname, ".local") && locals > 1 {
		t.Errorf("a .local hostname was suffixed again: %v", certHosts(""))
	}
}

func TestTrimHintsCapsTheList(t *testing.T) {
	long := []string{"a:1", "b:1", "c:1", "d:1", "e:1", "f:1"}
	if got := trimHints(long); len(got) != maxLANHints {
		t.Errorf("trimHints kept %d hints, want %d", len(got), maxLANHints)
	}
	short := []string{"a:1"}
	if got := trimHints(short); len(got) != 1 {
		t.Errorf("trimHints shortened an already-short list: %v", got)
	}
}

func TestBuildSearchOffByDefault(t *testing.T) {
	provider, note, err := buildSearch("", "ignored")
	if err != nil {
		t.Fatalf("buildSearch: %v", err)
	}
	if provider != nil {
		t.Error("no --search flag still produced a provider")
	}
	if !strings.Contains(note, "off") {
		t.Errorf("note = %q, want it to say search is off", note)
	}
}

func TestBuildSearchRejectsUnknownKinds(t *testing.T) {
	if _, _, err := buildSearch("bing", ""); err == nil || !strings.Contains(err.Error(), "searxng") {
		t.Errorf("err = %v, want a rejection naming the supported provider", err)
	}
}

// An instance that is not answering yet is a warning, not a refusal to start.
func TestBuildSearchToleratesAnUnreachableInstance(t *testing.T) {
	provider, note, err := buildSearch("searxng", "http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("buildSearch treated an unreachable instance as fatal: %v", err)
	}
	if provider == nil {
		t.Fatal("no provider despite a valid configuration")
	}
	if !strings.Contains(note, "not responding") {
		t.Errorf("note = %q, want it to flag the instance as not responding yet", note)
	}
}

func TestShortenErrorKeepsTheBannerReadable(t *testing.T) {
	long := errors.New(strings.Repeat("x", 200) + "\nsecond line")
	got := shortenError(long)
	if len(got) > 120 {
		t.Errorf("shortened error is still %d characters", len(got))
	}
	if strings.Contains(got, "\n") {
		t.Errorf("shortened error still spans lines: %q", got)
	}
	if !strings.HasPrefix(got, "unavailable: ") {
		t.Errorf("shortened error lost its prefix: %q", got)
	}

	multi := errors.New("first line\nsecond line")
	if got := shortenError(multi); !strings.Contains(got, "first line") || strings.Contains(got, "second") {
		t.Errorf("shortenError kept the wrong line: %q", got)
	}
}
