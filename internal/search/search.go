// Package search gives the gateway a way to look things up on the web.
//
// The provider interface is deliberately tiny — one method — because the point
// is to keep alpaca's dependency on any particular search service shallow
// enough to swap. SearXNG is the first implementation because it is
// self-hosted: queries leave your machine only as far as your own instance,
// which is the same reason the model is self-hosted in the first place.
package search

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Result is one hit, normalised across providers.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	// Engine records which upstream engine produced the hit, when the provider
	// reports it. Useful for debugging a thin result set.
	Engine string `json:"engine,omitempty"`
}

// Provider looks up a query.
type Provider interface {
	// Search returns at most limit results, best first.
	Search(ctx context.Context, query string, limit int) ([]Result, error)
	// Name identifies the provider for logs and the startup banner.
	Name() string
}

// Format renders results as the compact text block handed back to the model.
//
// The shape matters more than it looks: a small model reading this needs the
// URL attached to each snippet so it can cite, and numbered entries so it can
// refer to them without repeating the whole title.
func Format(query string, results []Result) string {
	if len(results) == 0 {
		return fmt.Sprintf("No web results found for %q.", query)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Web results for %q:\n\n", query)
	for i, r := range results {
		fmt.Fprintf(&b, "[%d] %s\n%s\n", i+1, strings.TrimSpace(r.Title), r.URL)
		if snippet := strings.TrimSpace(r.Snippet); snippet != "" {
			fmt.Fprintf(&b, "%s\n", snippet)
		}
		b.WriteString("\n")
	}
	b.WriteString("Cite the sources you use by their URL.")
	return b.String()
}

// Cached wraps a provider with a short in-memory cache.
//
// A model asked a follow-up question will often re-issue a near-identical
// query, and an agent loop can repeat one within a single turn. Serving those
// from memory avoids hammering the instance and keeps outbound traffic down,
// which is the whole reason to self-host the search layer.
type Cached struct {
	inner Provider
	ttl   time.Duration
	max   int

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	results []Result
	expires time.Time
}

// NewCached wraps p. A ttl of zero disables caching entirely.
func NewCached(p Provider, ttl time.Duration, max int) *Cached {
	if max <= 0 {
		max = 64
	}
	return &Cached{inner: p, ttl: ttl, max: max, entries: map[string]cacheEntry{}}
}

func (c *Cached) Name() string { return c.inner.Name() }

func (c *Cached) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	if c.ttl <= 0 {
		return c.inner.Search(ctx, query, limit)
	}

	key := cacheKey(query, limit)
	if results, ok := c.lookup(key); ok {
		return results, nil
	}

	results, err := c.inner.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	c.store(key, results)
	return results, nil
}

func (c *Cached) lookup(key string) ([]Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.results, true
}

func (c *Cached) store(key string, results []Result) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evicting everything expired, and failing that the whole map, keeps this
	// to a few lines. A search cache does not warrant an LRU.
	if len(c.entries) >= c.max {
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= c.max {
			c.entries = map[string]cacheEntry{}
		}
	}
	c.entries[key] = cacheEntry{results: results, expires: time.Now().Add(c.ttl)}
}

func cacheKey(query string, limit int) string {
	return fmt.Sprintf("%d\x00%s", limit, strings.ToLower(strings.TrimSpace(query)))
}
