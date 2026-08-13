package connect

import (
	"strings"
	"testing"
)

func sample() Bundle {
	return Bundle{
		ID:          "a1b2c3d4e5f6",
		Name:        "workshop",
		Key:         "alp_0123456789abcdefghijklmnopqrstuv",
		Fingerprint: "aa:bb:cc",
		LAN:         []string{"192.168.1.20:8080", "10.0.0.5:8080"},
		Public:      "203.0.113.9:8080",
	}
}

func TestRoundTrip(t *testing.T) {
	want := sample()
	encoded, err := want.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(encoded, Scheme+":") {
		t.Fatalf("encoded string %q lacks the %q prefix", encoded, Scheme)
	}

	got, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.ID != want.ID || got.Key != want.Key || got.Fingerprint != want.Fingerprint {
		t.Errorf("identity fields did not survive round trip: %+v", got)
	}
	if got.Public != want.Public || len(got.LAN) != len(want.LAN) || got.LAN[0] != want.LAN[0] {
		t.Errorf("endpoints did not survive round trip: %+v", got)
	}
}

// A connect string is copied by hand out of a terminal, so it arrives wrapped,
// indented, or padded with stray spaces. All of those must still decode.
func TestDecodeToleratesWhitespace(t *testing.T) {
	encoded, err := sample().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	mangled := []string{
		"  " + encoded + "  ",
		encoded[:20] + "\n" + encoded[20:],
		encoded[:15] + " \t\r\n " + encoded[15:],
		"\n" + encoded + "\n",
	}
	for _, in := range mangled {
		got, err := Decode(in)
		if err != nil {
			t.Errorf("Decode(%q): %v", in, err)
			continue
		}
		if got.ID != sample().ID {
			t.Errorf("Decode(%q) returned wrong bundle %+v", in, got)
		}
	}
}

func TestDecodeRejectsBadInput(t *testing.T) {
	valid, err := sample().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", "   \n  "},
		{"no scheme", "justsomerandomtext"},
		{"wrong scheme", "alpaca99:" + strings.TrimPrefix(valid, Scheme+":")},
		{"corrupt payload", Scheme + ":!!!not-base64!!!"},
		{"truncated payload", valid[:len(valid)-12]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decode(tc.in); err == nil {
				t.Fatalf("Decode(%q) succeeded, want error", tc.in)
			}
		})
	}
}

func TestEncodeRequiresEssentials(t *testing.T) {
	cases := map[string]Bundle{
		"no id":        {Key: "k", LAN: []string{"h:1"}},
		"no key":       {ID: "i", LAN: []string{"h:1"}},
		"no endpoints": {ID: "i", Key: "k"},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := b.Encode(); err == nil {
				t.Fatalf("Encode(%+v) succeeded, want error", b)
			}
		})
	}
}
