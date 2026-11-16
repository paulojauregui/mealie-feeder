package ledger

import (
	"time"
)

// State enumerates valid lifecycle states for a URL in recipes_ledger.
// Pre-conditions: Must equal one of the six defined constants.
// Post-conditions: Transitions are strictly governed by CanTransition.
type State string

const (
	// StateDiscovered indicates candidate URL has been found and queued for ingest.
	StateDiscovered State = "discovered"
	// StateImported indicates recipe has been successfully created in Mealie.
	StateImported State = "imported"
	// StateSkippedNonRecipe indicates page was evaluated and lacked valid Schema.org recipe markup.
	StateSkippedNonRecipe State = "skipped_non_recipe"
	// StateSkippedPaywall indicates page presented an unbypassable paywall or anti-bot challenge.
	StateSkippedPaywall State = "skipped_paywall"
	// StateFailedRetryable indicates transient fetch/network failure eligible for exponential backoff.
	StateFailedRetryable State = "failed_retryable"
	// StateFailedPermanent indicates terminal failure after maximum retry attempts exhausted.
	StateFailedPermanent State = "failed_permanent"
)

// DiscoverySource identifies how a candidate URL entered the ledger.
// Pre-conditions: Must equal one of the defined constants.
// Post-conditions: Persisted verbatim in recipes_ledger.discovery_source.
type DiscoverySource string

const (
	// DiscoverySeed indicates URL originated from the curated greatest-hits seed manifest.
	DiscoverySeed DiscoverySource = "seed"
	// DiscoverySitemap indicates URL originated from sitemap or sitemap index discovery.
	DiscoverySitemap DiscoverySource = "sitemap"
	// DiscoveryRSS indicates URL originated from an RSS or Atom feed.
	DiscoveryRSS DiscoverySource = "rss"
	// DiscoveryMealieSync indicates URL was ingested during initial reconciliation with Mealie inventory.
	DiscoveryMealieSync DiscoverySource = "mealie_sync"
)

// DefaultMaxAttempts bounds retries before transitioning failed_retryable to failed_permanent.
// Pre-conditions: Non-zero positive integer threshold.
// Post-conditions: Used by retry evaluator when determining terminal state.
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
func IsValidState(s State) bool {
	switch s {
	case StateDiscovered,
		StateImported,
		StateSkippedNonRecipe,
		StateSkippedPaywall,
		StateFailedRetryable,
		StateFailedPermanent:
		return true
	default:
		return false
	}
}

// CanTransition validates whether moving a record from current state `from` to `to` is permitted.
// Pre-conditions: Both from and to must be valid States.
// Post-conditions: Terminal states (imported, skipped_non_recipe, skipped_paywall, failed_permanent) reject
// transitions out except idempotent self-retention or reconciliation to imported.
func CanTransition(from, to State) bool {
	if !IsValidState(from) || !IsValidState(to) {
		return false
	}

	// Idempotent self-transition is always permitted.
	if from == to {
		return true
	}

	// Any state can transition to StateImported (e.g. eventual manual import or Mealie reconciliation).
	if to == StateImported {
		return true
	}

	switch from {
	case StateDiscovered:
		// Discovered can move to any active or terminal state.
		return true

	case StateFailedRetryable:
		// Retryable failure can be retried (self), marked permanent failure, skipped as non-recipe/paywall,
		// or imported (handled above). Cannot revert back to discovered.
		switch to {
		case StateFailedPermanent, StateSkippedNonRecipe, StateSkippedPaywall:
			return true
		default:
			return false
		}

	case StateImported, StateSkippedNonRecipe, StateSkippedPaywall, StateFailedPermanent:
		// Terminal states cannot transition to other states except imported (already handled above).
		return false

	default:
		return false
	}
}

// ComputeNextRetry computes deterministic exponential backoff from attempt count (1h, 6h, 24h, 72h, capped at 72h).
// Pre-conditions: now is UTC time; attempts >= 1.
// Post-conditions: Returned timestamp is strictly after now in UTC.
func ComputeNextRetry(now time.Time, attempts int) time.Time {
	utcNow := now.UTC()
	var delay time.Duration

	switch {
	case attempts <= 1:
		delay = 1 * time.Hour
	case attempts == 2:
		delay = 6 * time.Hour
	case attempts == 3:
		delay = 24 * time.Hour
	default:
		delay = 72 * time.Hour
	}

	return utcNow.Add(delay)
}
