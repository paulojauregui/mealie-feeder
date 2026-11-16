# M03 Specification: Canonical URL Normalizer & Ledger Domain Models

## 1. Objective & Target Files

Implement deterministic canonical URL normalization, retry backoff calculation, and the recipe ingestion state machine in `internal/ledger` with zero database side effects.

**Target Files (strict scope):**
1. `internal/ledger/models.go`
2. `internal/ledger/normalize.go`
3. `internal/ledger/normalize_test.go`

---

## 2. Package Contract (`package ledger`)

### 2.1 Models & State Machine (`internal/ledger/models.go`)

```go
package ledger

import "time"

// State enumerates valid lifecycle states for a URL in recipes_ledger.
// Pre-conditions: Must equal one of the six defined constants.
// Post-conditions: Transitions are strictly governed by CanTransition.
type State string

const (
	StateDiscovered       State = "discovered"
	StateImported         State = "imported"
	StateSkippedNonRecipe State = "skipped_non_recipe"
	StateSkippedPaywall   State = "skipped_paywall"
	StateFailedRetryable  State = "failed_retryable"
	StateFailedPermanent  State = "failed_permanent"
)

// DiscoverySource identifies how a candidate URL entered the ledger.
// Pre-conditions: Must equal one of the defined constants.
// Post-conditions: Persisted verbatim in recipes_ledger.discovery_source.
type DiscoverySource string

const (
	DiscoverySeed       DiscoverySource = "seed"
	DiscoverySitemap    DiscoverySource = "sitemap"
	DiscoveryRSS        DiscoverySource = "rss"
	DiscoveryMealieSync DiscoverySource = "mealie_sync"
)

// DefaultMaxAttempts bounds retries before transitioning failed_retryable to failed_permanent.
const DefaultMaxAttempts = 5

// RecipeRecord mirrors one row in recipes_ledger.
// Pre-conditions: CanonicalURL and Domain must be normalized via NormalizeURL/ExtractDomain prior to persistence.
// Post-conditions: All timestamps are stored and returned in UTC.
type RecipeRecord struct {
	ID              int64
	CanonicalURL    string
	SourceURL       string
	Domain          string
	DiscoverySource DiscoverySource
	Cuisine         string
	Title           string
	State           State
	MealieSlug      string
	HTTPStatus      int
	Attempts        int
	LastError       string
	NextRetryAt     time.Time
	DiscoveredAt    time.Time
	UpdatedAt       time.Time
}

// FeedCursor tracks conditional GET and published watermark state per RSS/Atom feed.
// Pre-conditions: FeedURL must be a canonicalized http/https URL.
// Post-conditions: Persisted in feed_cursors table.
type FeedCursor struct {
	FeedURL             string
	Domain              string
	ETag                string
	LastModified        string
	LastItemPublishedAt time.Time
	LastCheckedAt       time.Time
}

// RunRecord records audit metrics for a single seed or sync invocation.
// Pre-conditions: RunID and Command must be non-empty.
// Post-conditions: Persisted in runs table on start and finalized on completion.
type RunRecord struct {
	RunID           string
	Command         string
	StartedAt       time.Time
	FinishedAt      time.Time
	Status          string // "running" | "success" | "partial" | "failed"
	DiscoveredCount int
	ImportedCount   int
	SkippedCount    int
	FailedCount     int
	DryRun          bool
	ErrorSummary    string
}

// StatusSummary aggregates ledger state counts and recent execution history for the status command.
// Pre-conditions: Populated by Store.GetStatusSummary.
// Post-conditions: Non-nil maps for ByState and ByDomain.
type StatusSummary struct {
	TotalRecipes int
	ByState      map[State]int
	ByDomain     map[string]map[State]int
	RecentRuns   []RunRecord
}

// IsValidState reports whether s is a defined ledger State.
// Pre-conditions: None.
// Post-conditions: Returns true iff s matches one of the six State constants.
func IsValidState(s State) bool

// CanTransition validates whether moving a record from current state `from` to `to` is permitted.
// Pre-conditions: Both from and to must be valid States.
// Post-conditions: Terminal states (imported, skipped_non_recipe, skipped_paywall, failed_permanent) reject transitions out except idempotent self-retention or reconciliation to imported.
func CanTransition(from, to State) bool

// ComputeNextRetry computes deterministic exponential backoff from attempt count (1h, 6h, 24h, 72h, capped at 72h).
// Pre-conditions: now is UTC time; attempts >= 1.
// Post-conditions: Returned timestamp is strictly after now in UTC.
func ComputeNextRetry(now time.Time, attempts int) time.Time
```

### 2.2 Canonical URL Normalizer (`internal/ledger/normalize.go`)

```go
// NormalizeURL canonicalizes rawURL for deduplication across seeds, sitemaps, RSS feeds, and Mealie orgURLs.
// Pre-conditions: rawURL must parse as an absolute http or https URL with a non-empty host.
// Post-conditions: Returns a URL string with lowercase scheme/host, default ports (:80, :443) stripped,
// empty fragment, path cleaned without collapsing intentional trailing slash semantics,
// tracking query params removed (utm_*, fbclid, gclid, dclid, msclkid, mc_cid, mc_eid, igshid, ref, ref_src, _hsenc, _hsmi, mkt_tok, yclid),
// and remaining query params sorted lexicographically.
func NormalizeURL(rawURL string) (string, error)

// DedupeKey returns a trailing-slash-normalized comparison key derived from canonicalURL for matching Mealie orgURLs.
// Pre-conditions: canonicalURL must be a valid http/https URL.
// Post-conditions: Strips www. host prefix and trailing slash (except root "/") for secondary orgURL collision detection.
func DedupeKey(canonicalURL string) (string, error)

// ExtractDomain returns the lowercase apex-friendly hostname (stripping leading "www." and port) from a URL.
// Pre-conditions: rawURL must parse with a non-empty host.
// Post-conditions: Returns lowercase hostname without "www." prefix (e.g., "seriouseats.com").
func ExtractDomain(rawURL string) (string, error)
```

---

## 3. Edge Cases & Test Plan (`internal/ledger/normalize_test.go`)

1. `NormalizeURL`:
   - Strips `:443` on `https` and `:80` on `http`, preserves non-standard ports like `:9000`.
   - Lowercases `HTTPS://WWW.SeriousEats.COM/Path` -> `https://www.seriouseats.com/Path` (preserves path case while cleaning `/../`).
   - Drops fragments (`#recipe-card`, `#comments`) and strips all tracking params (`utm_source`, `utm_medium`, `fbclid`, `gclid`, `mc_cid`, `igshid`, `ref`, etc.) while keeping meaningful params like `?page=2&sort=asc` sorted cleanly.
   - Rejects empty strings, `ftp://`, `javascript:`, or relative URLs lacking a host.
2. `DedupeKey`: maps `https://www.seriouseats.com/foo/` and `https://seriouseats.com/foo` to the same comparison key while keeping `NormalizeURL` faithful to each site's canonical path form.
3. `CanTransition`: verifies `discovered -> imported`, `discovered -> failed_retryable -> imported`, `failed_retryable -> failed_permanent`, and blocks invalid reversals like `imported -> discovered` or `skipped_non_recipe -> failed_retryable`.
4. `ComputeNextRetry`: verifies monotonic backoff schedule (`+1h`, `+6h`, `+24h`, `+72h`, cap `+72h`).

---

## 4. Verification Command

```bash
go test -v -race -cover ./internal/ledger
```
