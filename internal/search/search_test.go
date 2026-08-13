package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newSearx(t *testing.T, handler http.HandlerFunc) *SearXNG {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p, err := NewSearXNG(srv.URL)
	if err != nil {
		t.Fatalf("NewSearXNG: %v", err)
	}
	return p
}

const twoHits = `{"results":[
  {"title":"Go 1.26 release notes","url":"https://go.dev/doc/go1.26","content":"Go 1.26  is\n  released.","engine":"google"},
  {"title":"Discussion","url":"https://example.com/thread","content":"chatter","engine":"duckduckgo"}
]}`

func TestSearchParsesResults(t *testing.T) {
	p := newSearx(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Errorf("format = %q, want json", got)
		}
		if got := r.URL.Query().Get("q"); got != "go 1.26" {
			t.Errorf("q = %q", got)
		}
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search", r.URL.Path)
		}
		fmt.Fprint(w, twoHits)
	})

	results, err := p.Search(context.Background(), "go 1.26", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Title != "Go 1.26 release notes" || results[0].URL != "https://go.dev/doc/go1.26" {
		t.Errorf("first result = %+v", results[0])
	}
	// Snippets arrive with newlines and doubled spaces; they must be flattened.
	if results[0].Snippet != "Go 1.26 is released." {
		t.Errorf("snippet = %q, want whitespace collapsed", results[0].Snippet)
	}
	if results[0].Engine != "google" {
		t.Errorf("engine = %q", results[0].Engine)
	}
}

func TestSearchRespectsLimit(t *testing.T) {
	p := newSearx(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, twoHits) })

	results, err := p.Search(context.Background(), "q", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("got %d results, want 1", len(results))
	}
}

func TestSearchDropsDuplicatesAndJunk(t *testing.T) {
	p := newSearx(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[
			{"title":"A","url":"https://a.example","content":"one"},
			{"title":"A again","url":"https://a.example","content":"dupe"},
			{"title":"","url":"https://b.example","content":"no title"},
			{"title":"C","url":"","content":"no url"},
			{"title":"D","url":"https://d.example","content":"four"}
		]}`)
	})

	results, err := p.Search(context.Background(), "q", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (dupe and untitled/urlless dropped): %+v", len(results), results)
	}
	if results[0].URL != "https://a.example" || results[1].URL != "https://d.example" {
		t.Errorf("results = %+v", results)
	}
}

func TestSnippetsAreTruncated(t *testing.T) {
	long := strings.Repeat("word ", 200)
	p := newSearx(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"results":[{"title":"T","url":"https://x.example","content":%q}]}`, long)
	})

	results, _ := p.Search(context.Background(), "q", 1)
	if len(results[0].Snippet) > 340 {
		t.Errorf("snippet is %d chars, want it capped", len(results[0].Snippet))
	}
	if !strings.HasSuffix(results[0].Snippet, "…") {
		t.Errorf("truncated snippet should be marked: %q", results[0].Snippet)
	}
}

// A 403 is nearly always the same misconfiguration, and the error has to say
// which one or the user is sent digging through SearXNG's docs.
func TestForbiddenExplainsJSONFormat(t *testing.T) {
	p := newSearx(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := p.Search(context.Background(), "q", 5)
	if err == nil {
		t.Fatal("Search succeeded against a 403")
	}
	for _, want := range []string{"formats", "json", "settings.yml", "limiter"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%s", want, err)
		}
	}
}

func TestUnreachableInstanceIsExplained(t *testing.T) {
	p, err := NewSearXNG("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewSearXNG: %v", err)
	}
	_, err = p.Search(context.Background(), "q", 5)
	if err == nil {
		t.Fatal("Search succeeded against a dead instance")
	}
	if !strings.Contains(err.Error(), "is the instance running") {
		t.Errorf("error = %v, want a hint about the instance being down", err)
	}
}

func TestNonSearxngEndpointIsExplained(t *testing.T) {
	p := newSearx(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>hello</html>")
	})
	_, err := p.Search(context.Background(), "q", 5)
	if err == nil || !strings.Contains(err.Error(), "really a SearXNG instance") {
		t.Errorf("error = %v, want a hint that the url is wrong", err)
	}
}

func TestNewSearXNGRejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "   "} {
		if _, err := NewSearXNG(in); err == nil {
			t.Errorf("NewSearXNG(%q) succeeded, want error", in)
		}
	}
	// A bare host:port is a reasonable thing to type and should be accepted.
	p, err := NewSearXNG("localhost:8888")
	if err != nil {
		t.Fatalf("NewSearXNG(host:port): %v", err)
	}
	if !strings.Contains(p.Name(), "localhost:8888") {
		t.Errorf("Name() = %q", p.Name())
	}
}

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

func TestFormatIncludesURLsForCitation(t *testing.T) {
	out := Format("go 1.26", []Result{
		{Title: "Release notes", URL: "https://go.dev/doc/go1.26", Snippet: "It shipped."},
		{Title: "Blog", URL: "https://go.dev/blog", Snippet: ""},
	})

	for _, want := range []string{"go 1.26", "[1]", "[2]", "https://go.dev/doc/go1.26", "It shipped.", "Cite"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted block missing %q:\n%s", want, out)
		}
	}
}

func TestFormatHandlesNoResults(t *testing.T) {
	out := Format("obscure thing", nil)
	if !strings.Contains(out, "No web results") || !strings.Contains(out, "obscure thing") {
		t.Errorf("empty formatting = %q", out)
	}
}

// ---------------------------------------------------------------------------
// Cache
// ---------------------------------------------------------------------------

type countingProvider struct {
	calls atomic.Int32
}

func (c *countingProvider) Name() string { return "counting" }
func (c *countingProvider) Search(_ context.Context, q string, limit int) ([]Result, error) {
	c.calls.Add(1)
	return []Result{{Title: q, URL: "https://example.com/" + q}}, nil
}

func TestCacheServesRepeats(t *testing.T) {
	inner := &countingProvider{}
	c := NewCached(inner, time.Minute, 8)

	for i := 0; i < 4; i++ {
		if _, err := c.Search(context.Background(), "same query", 5); err != nil {
			t.Fatalf("Search: %v", err)
		}
	}
	if got := inner.calls.Load(); got != 1 {
		t.Errorf("inner provider called %d times, want 1", got)
	}
}

// Case and surrounding whitespace should not defeat the cache; a model rarely
// reproduces its own query byte for byte.
func TestCacheKeyIsNormalised(t *testing.T) {
	inner := &countingProvider{}
	c := NewCached(inner, time.Minute, 8)

	for _, q := range []string{"Go 1.26", "go 1.26", "  GO 1.26  "} {
		c.Search(context.Background(), q, 5)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Errorf("inner provider called %d times, want 1", got)
	}
}

func TestCacheSeparatesDifferentQueriesAndLimits(t *testing.T) {
	inner := &countingProvider{}
	c := NewCached(inner, time.Minute, 8)

	c.Search(context.Background(), "a", 5)
	c.Search(context.Background(), "b", 5)
	c.Search(context.Background(), "a", 3) // different limit is a different result set
	if got := inner.calls.Load(); got != 3 {
		t.Errorf("inner provider called %d times, want 3", got)
	}
}

func TestCacheExpires(t *testing.T) {
	// A wide margin between TTL and sleep: a loaded CI machine can stall a
	// goroutine long enough to turn a 2:1 ratio into a flake.
	inner := &countingProvider{}
	c := NewCached(inner, 20*time.Millisecond, 8)

	c.Search(context.Background(), "q", 5)
	time.Sleep(200 * time.Millisecond)
	c.Search(context.Background(), "q", 5)

	if got := inner.calls.Load(); got != 2 {
		t.Errorf("inner provider called %d times, want 2 after expiry", got)
	}
}

func TestCacheDisabledWithZeroTTL(t *testing.T) {
	inner := &countingProvider{}
	c := NewCached(inner, 0, 8)

	c.Search(context.Background(), "q", 5)
	c.Search(context.Background(), "q", 5)
	if got := inner.calls.Load(); got != 2 {
		t.Errorf("inner provider called %d times, want 2 with caching off", got)
	}
}

func TestCacheEvictsWhenFull(t *testing.T) {
	inner := &countingProvider{}
	c := NewCached(inner, time.Minute, 4)

	for i := 0; i < 20; i++ {
		if _, err := c.Search(context.Background(), fmt.Sprintf("query-%d", i), 5); err != nil {
			t.Fatalf("Search: %v", err)
		}
	}
	c.mu.Lock()
	size := len(c.entries)
	c.mu.Unlock()
	if size > 4 {
		t.Errorf("cache holds %d entries, want at most 4", size)
	}
}

func TestCacheDoesNotStoreErrors(t *testing.T) {
	var calls atomic.Int32
	failing := providerFunc(func(ctx context.Context, q string, limit int) ([]Result, error) {
		calls.Add(1)
		return nil, fmt.Errorf("upstream down")
	})
	c := NewCached(failing, time.Minute, 8)

	c.Search(context.Background(), "q", 5)
	c.Search(context.Background(), "q", 5)
	if got := calls.Load(); got != 2 {
		t.Errorf("failing provider called %d times, want 2 — errors must not be cached", got)
	}
}

type providerFunc func(context.Context, string, int) ([]Result, error)

func (f providerFunc) Name() string { return "func" }
func (f providerFunc) Search(ctx context.Context, q string, limit int) ([]Result, error) {
	return f(ctx, q, limit)
}
