# M07 Specification: RSS/Atom Discovery, Regex URL Filtering & Source Facade

## 1. Objective & Target Files

Implement conditional RSS 2.0 / Atom 1.0 feed parsing via `github.com/mmcdole/gofeed v1.5.0`, per-source URL regex allow/deny filtering combined with `robots.txt` rules, and the unified `DiscoverSourceCandidates` facade in `internal/discover`.

**Target Files (strict scope):**
1. `internal/discover/filter.go`
2. `internal/discover/feed.go`
3. `internal/discover/feed_test.go`

---

## 2. Package Contract (`package discover`)

### 2.1 URL Filter Engine (`internal/discover/filter.go`)

```go
package discover

import "time"

// URLFilter enforces robots.txt path rules, deny regexes, and allow regexes for one source domain.
// Pre-conditions: Constructed via NewURLFilter; regex patterns must compile cleanly.
// Post-conditions: Safe for concurrent Match calls.
type URLFilter struct {
	// unexported fields: allow []*regexp.Regexp, deny []*regexp.Regexp, robots *RobotsPolicy
}

// NewURLFilter compiles allowPatterns and denyPatterns and attaches an optional *RobotsPolicy.
// Pre-conditions: All strings in allowPatterns and denyPatterns must be valid Go regexps.
// Post-conditions: Returns compiled *URLFilter or regex compilation error.
func NewURLFilter(allowPatterns, denyPatterns []string, robots *RobotsPolicy) (*URLFilter, error)

// Match evaluates whether rawURL satisfies robots.Allowed(path), matches zero denyPatterns, and matches at least one allowPattern (if any are configured).
// Pre-conditions: rawURL parses as a valid http/https URL.
// Post-conditions: Deny patterns take strict precedence over allow patterns.
func (f *URLFilter) Match(rawURL string) bool
```

### 2.2 RSS/Atom Feed Discovery & Source Facade (`internal/discover/feed.go`)

```go
// FeedCursorState decouples discover from internal/ledger while passing conditional GET metadata.
// Pre-conditions: FeedURL is canonical feed URL.
// Post-conditions: Returned updated by DiscoverSourceCandidates after feed checks.
type FeedCursorState struct {
	FeedURL             string
	ETag                string
	LastModified        string
	LastItemPublishedAt time.Time
	LastCheckedAt       time.Time
}

// FeedResult captures parsed feed items and HTTP cache validators.
// Pre-conditions: Returned by FetchFeed.
// Post-conditions: When NotModified is true, Entries is empty and HTTP validators remain unchanged.
type FeedResult struct {
	Entries         []SitemapEntry
	ETag            string
	LastModified    string
	LatestPublished time.Time
	NotModified     bool
}

// Candidate represents one filtered, deduplicated recipe permalink yielded by discovery.
// Pre-conditions: SourceKind is "rss" or "sitemap".
// Post-conditions: Ready for ledger UpsertDiscovered.
type Candidate struct {
	URL        string
	Domain     string
	SourceKind string
	LastMod    time.Time
}

// DiscoveryOutcome bundles filtered candidates, updated feed cursors, and effective robots crawl-delay.
// Pre-conditions: Returned by DiscoverSourceCandidates.
// Post-conditions: Candidates slice contains deduplicated URLs matching source allow/deny and robots rules.
type DiscoveryOutcome struct {
	Candidates      []Candidate
	UpdatedCursors  []FeedCursorState
	RobotsCrawlDelay time.Duration
}

// FetchFeed retrieves an RSS 2.0 or Atom 1.0 feed using optional ETag/Last-Modified conditional headers and parses items via gofeed.
// Pre-conditions: fetcher non-nil, feedURL valid http/https URL.
// Post-conditions: Handles 304 Not Modified cleanly; filters items strictly older than prevPublishedAt when non-zero; falls back safely when PublishedParsed is nil.
func FetchFeed(ctx context.Context, fetcher ResourceFetcher, feedURL string, prev FeedCursorState, opts fetch.RequestOptions) (*FeedResult, error)

// DiscoverSourceCandidates fetches robots.txt, polls configured RSS feeds (if any), and traverses sitemaps for src,
// returning filtered, deduplicated Candidates with RSS items ordered ahead of sitemap entries.
// Pre-conditions: fetcher non-nil; src must pass config validation; prevCursors may be nil.
// Post-conditions: Explicitly respects src.SitemapURLs when robots.txt lacks valid Sitemap entries; never loops on empty feeds.
func DiscoverSourceCandidates(ctx context.Context, fetcher ResourceFetcher, src config.SourceConfig, prevCursors map[string]FeedCursorState, includeSitemaps bool) (*DiscoveryOutcome, error)
```

---

## 3. Edge Cases & Test Plan (`internal/discover/feed_test.go`)

1. `URLFilter`:
   - Verifies deny overrides allow (`https://www.bbcgoodfood.com/premium/cake` rejected even if allow permits recipes; `https://www.seriouseats.com/best-pasta-recipes-123` rejected while `.../carbonara-recipe-456` passes).
   - Verifies `smittenkitchen.com` `/2021/02/rigatoni-alla-vodka/` passes while `/recipes/` index page fails.
   - Verifies `robots.txt` `Disallow` overrides regex allow.
2. `FetchFeed`:
   - Parses both RSS 2.0 (`recipetineats`, `bonappetit`) and Atom 1.0 feeds via `gofeed`.
   - Sends `If-None-Match` and `If-Modified-Since`; returns `NotModified: true` on HTTP `304`.
   - Filters out items already covered by `LastItemPublishedAt`, preserving newer items and items missing `<pubDate>`.
3. `DiscoverSourceCandidates`:
   - Combines RSS items + sitemap URLs (deduplicating URLs appearing in both), drops non-matching paths, sets `RobotsCrawlDelay`, and works when `RSSURLs` is empty (Dotdash Meredith mode).

---

## 4. Verification Command

```bash
go test -v -race -cover ./internal/discover
```
