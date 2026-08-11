package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SearXNG queries a self-hosted SearXNG instance.
//
// Only the snippets SearXNG already returns are used; alpaca never fetches the
// result pages themselves. That keeps a search to exactly one outbound request
// from this machine instead of one per hit, and the snippets are usually enough
// for a model to answer or to decide it needs a different query.
type SearXNG struct {
	base *url.URL
	http *http.Client
	// Language and SafeSearch mirror SearXNG's own parameters.
	Language   string
	SafeSearch int
	// Categories narrows which engine groups are queried, e.g. "general" or
	// "general,news". Empty leaves the instance default alone.
	Categories string
}

// NewSearXNG builds a provider for the instance at base, e.g.
// "http://localhost:8888".
func NewSearXNG(base string) (*SearXNG, error) {
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("searxng: no instance url configured")
	}
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("searxng: parse url %q: %w", base, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("searxng: url %q has no host", base)
	}

	return &SearXNG{
		base:       u,
		Language:   "en",
		SafeSearch: 1,
		http: &http.Client{
			Timeout: 20 * time.Second, // a metasearch fans out to several engines
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     60 * time.Second,
			},
		},
	}, nil
}

func (s *SearXNG) Name() string { return "searxng (" + s.base.Host + ")" }

type searxResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
	Engine  string `json:"engine"`
}

func (s *SearXNG) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("searxng: empty query")
	}
	if limit <= 0 {
		limit = 5
	}

	endpoint := *s.base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/search"

	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	if s.Language != "" {
		params.Set("language", s.Language)
	}
	params.Set("safesearch", strconv.Itoa(s.SafeSearch))
	if s.Categories != "" {
		params.Set("categories", s.Categories)
	}
	endpoint.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	// SearXNG's bot limiter rejects requests without a plausible User-Agent.
	req.Header.Set("User-Agent", "alpaca/1.0 (+https://github.com/justin06lee/alpaca)")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng: cannot reach %s — is the instance running? (%w)", s.base.Host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, s.describeFailure(resp)
	}

	var payload struct {
		Results []searxResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("searxng: unreadable response (is %s really a SearXNG instance?): %w",
			s.base.Host, err)
	}

	return normalise(payload.Results, limit), nil
}

// describeFailure turns SearXNG's terse status codes into something actionable.
//
// A 403 here is almost always one specific misconfiguration, and saying so
// outright saves a long detour through the instance's documentation.
func (s *SearXNG) describeFailure(resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	body := strings.TrimSpace(string(snippet))

	switch resp.StatusCode {
	case http.StatusForbidden:
		return fmt.Errorf("searxng: the instance refused a JSON search (403).\n" +
			"  SearXNG ships with JSON output disabled. In its settings.yml add:\n" +
			"    search:\n" +
			"      formats:\n" +
			"        - html\n" +
			"        - json\n" +
			"  then restart it. If that is already set, the bot limiter may be blocking\n" +
			"  alpaca — set server.limiter to false for a local-only instance.")
	case http.StatusNotFound:
		return fmt.Errorf("searxng: %s has no /search endpoint — check the url points at the "+
			"instance root", s.base.Host)
	case http.StatusTooManyRequests:
		return fmt.Errorf("searxng: rate limited by the instance (429)")
	default:
		if len(body) > 200 {
			body = body[:200] + "…"
		}
		return fmt.Errorf("searxng: instance returned %d: %s", resp.StatusCode, body)
	}
}

// normalise drops unusable hits, de-duplicates by URL, and truncates.
func normalise(raw []searxResult, limit int) []Result {
	seen := make(map[string]bool, len(raw))
	out := make([]Result, 0, limit)

	for _, r := range raw {
		link := strings.TrimSpace(r.URL)
		title := strings.TrimSpace(r.Title)
		if link == "" || title == "" || seen[link] {
			continue
		}
		seen[link] = true

		out = append(out, Result{
			Title:   title,
			URL:     link,
			Snippet: collapse(r.Content),
			Engine:  r.Engine,
		})
		if len(out) == limit {
			break
		}
	}
	return out
}

// collapse flattens whitespace and caps snippet length. Long snippets crowd out
// the conversation in a small model's context for very little added signal.
func collapse(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 320
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if idx := strings.LastIndex(cut, " "); idx > max/2 {
		cut = cut[:idx]
	}
	return cut + "…"
}

// Ping checks the instance is up without running a search.
//
// It hits the instance root, which is served locally and never fans out to any
// upstream engine — so a startup check costs one request on the loopback and
// zero outbound traffic.
func (s *SearXNG) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "alpaca/1.0")

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("searxng: cannot reach %s — is the instance running? (%w)", s.base.Host, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	if resp.StatusCode >= 500 {
		return fmt.Errorf("searxng: instance at %s returned %d", s.base.Host, resp.StatusCode)
	}
	return nil
}
