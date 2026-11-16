# M06 Specification: Robots.txt Parser & Recursive Sitemap Discovery

## 1. Objective & Target Files

Implement `robots.txt` compliance parsing and recursive XML / `.xml.gz` sitemap discovery (with `<lastmod>` descending prioritization and `sitemap_include_patterns` pruning) in `internal/discover`.

**Target Files (strict scope):**
1. `internal/discover/robots.go`
2. `internal/discover/sitemap.go`
3. `internal/discover/sitemap_test.go`

---

## 2. Package Contract (`package discover`)

### 2.1 Shared Consumer Interface & Robots Policy (`internal/discover/robots.go`)

```go
package discover

import (
	"context"
	"time"

	"github.com/paulojauregui/mealie-feeder/internal/fetch"
)

// ResourceFetcher decouples discovery from concrete HTTP transport for deterministic offline tests.
// Pre-conditions: Implementations return *fetch.Response on 200/304 or typed fetch errors.
// Post-conditions: Matches fetch.Client.Fetch signature directly.
type ResourceFetcher interface {
	Fetch(ctx context.Context, rawURL string, opts fetch.RequestOptions) (*fetch.Response, error)
}

// RobotsPolicy stores parsed User-agent rules, Crawl-delay, and validated Sitemap URLs for one origin.
// Pre-conditions: Constructed via ParseRobots or AllowAllRobots.
// Post-conditions: Thread-safe for concurrent Allowed queries.
type RobotsPolicy struct {
	// unexported fields: allowPrefixes []string, disallowPrefixes []string, crawlDelay time.Duration, sitemaps []string
}

// ParseRobots parses raw robots.txt bytes prioritizing exact product agent match (if any) merged or falling back to "User-agent: *".
// Pre-conditions: userAgent may be empty (defaults to "*" matching).
// Post-conditions: Validates each Sitemap: line via url.Parse (requiring http/https and non-empty host), silently dropping malformed entries like "http:///sitemap.xml".
func ParseRobots(raw []byte, userAgent string) *RobotsPolicy

// AllowAllRobots returns a permissive fallback policy when origin returns 404 on /robots.txt.
// Pre-conditions: None.
// Post-conditions: Allowed returns true for all paths; CrawlDelay is 0; Sitemaps is empty.
func AllowAllRobots() *RobotsPolicy

// FetchRobots retrieves and parses <scheme>://<host>/robots.txt cleanly, returning AllowAllRobots on 404/410.
// Pre-conditions: fetcher non-nil, baseOrigin is "https://host".
// Post-conditions: Propagates ErrChallengeDetected; degrades cleanly on 404/transient robots failure if configured or returns error.
func FetchRobots(ctx context.Context, fetcher ResourceFetcher, baseOrigin string, opts fetch.RequestOptions) (*RobotsPolicy, error)

// Allowed evaluates longest-prefix Allow/Disallow path matching per standard robots.txt precedence (Allow beats equal-length Disallow).
// Pre-conditions: urlPath starts with "/" (empty defaults to "/").
// Post-conditions: Returns true when crawling urlPath is permitted.
func (p *RobotsPolicy) Allowed(urlPath string) bool

// CrawlDelay returns the parsed Crawl-delay duration (0 if unspecified).
// Pre-conditions: p non-nil.
// Post-conditions: Non-negative time.Duration.
func (p *RobotsPolicy) CrawlDelay() time.Duration

// Sitemaps returns validated absolute Sitemap URLs extracted from robots.txt.
// Pre-conditions: p non-nil.
// Post-conditions: Defensive copy of validated URLs.
func (p *RobotsPolicy) Sitemaps() []string
```

### 2.2 Recursive XML / `.xml.gz` Sitemap Parser (`internal/discover/sitemap.go`)

```go
// SitemapEntry represents one discovered URL with optional <lastmod> timestamp.
// Pre-conditions: URL is a trimmed, non-empty string.
// Post-conditions: LastMod is UTC (or zero time if absent/unparseable).
type SitemapEntry struct {
	URL     string
	LastMod time.Time
}

// SitemapOptions bounds recursive sitemap traversal and memory consumption.
// Pre-conditions: MaxDepth >= 1, MaxSitemaps >= 1, MaxURLs >= 1.
// Post-conditions: Zero fields default to MaxDepth=3, MaxSitemaps=100, MaxURLs=100000, MaxDecompressedBytes=64MiB.
type SitemapOptions struct {
	IncludePatterns      []string
	MaxDepth             int
	MaxSitemaps          int
	MaxURLs              int
	MaxDecompressedBytes int64
	Request              fetch.RequestOptions
}

// DiscoverSitemapURLs recursively fetches <sitemapindex> and <urlset> documents (including .xml.gz / gzip Content-Encoding),
// filters child sitemaps against opts.IncludePatterns before fetching, deduplicates URLs, and sorts entries descending by LastMod.
// Pre-conditions: fetcher non-nil; rootURLs non-empty.
// Post-conditions: Respects cycle detection, depth limits, and gzip decompression bounds; returns slice sorted newest-first.
func DiscoverSitemapURLs(ctx context.Context, fetcher ResourceFetcher, rootURLs []string, opts SitemapOptions) ([]SitemapEntry, error)
```

---

## 3. Edge Cases & Test Plan (`internal/discover/sitemap_test.go`)

1. `ParseRobots`:
   - Parses `User-agent: *` `Disallow: /wp-admin/`, `Disallow: /premium/`, `Allow: /recipes/`, `Crawl-delay: 2.5`.
   - Ignores `Crawl-delay: 12` scoped specifically to `User-agent: Scope3/2.0` or `User-agent: AhrefsBot` when querying `*`.
   - Drops malformed `Sitemap: http:///sitemap.xml` (observed on `smittenkitchen.com`) while keeping valid `https://...` sitemaps.
   - Longest-prefix rule: `Allow: /recipes/free/` beats `Disallow: /recipes/`.
2. `DiscoverSitemapURLs`:
   - Handles `<sitemapindex>` pointing to child `<urlset>` XML files AND `.xml.gz` gzip-compressed child sitemaps.
   - `IncludePatterns` (`\d{4}-Q[1-4]-recipe\.xml$`) prevents fetching non-matching child sitemaps (`2025-Q1-post.xml` never requested from mock fetcher!).
   - Cycle detection prevents infinite recursion when sitemap A references sitemap B references sitemap A.
   - Decompression size cap (`MaxDecompressedBytes`) aborts oversized gzipped XML streams cleanly without OOM.
   - Sorts entries descending by `<lastmod>` (handling RFC3339, `2006-01-02T15:04:05Z07:00`, and date-only `2006-01-02`, with missing `<lastmod>` sorted after dated entries).

---

## 4. Verification Command

```bash
go test -v -race -cover ./internal/discover
```
