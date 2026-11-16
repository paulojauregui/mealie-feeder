package feeder

import (
	"time"

	"github.com/paulojauregui/mealie-feeder/internal/ledger"
	"github.com/paulojauregui/mealie-feeder/internal/preflight"
)

// DomainStats aggregates per-domain ingestion counts during a run.
// Pre-conditions: Domain must be non-empty.
// Post-conditions: Discovered, Imported, Skipped, and Failed counters are non-negative.
type DomainStats struct {
	Domain     string `json:"domain"`
	Discovered int    `json:"discovered"`
	Imported   int    `json:"imported"`
	Skipped    int    `json:"skipped"`
	Failed     int    `json:"failed"`
}

// RunReport summarizes a Seed or Sync execution cleanly for CLI and structured logging.
// Pre-conditions: Returned by Engine.Seed and Engine.Sync.
// Post-conditions: Duration equals FinishedAt.Sub(StartedAt).
type RunReport struct {
	RunID      string                  `json:"run_id"`
	Command    string                  `json:"command"`
	DryRun     bool                    `json:"dry_run"`
	StartedAt  time.Time               `json:"started_at"`
	FinishedAt time.Time               `json:"finished_at"`
	DurationMS int64                   `json:"duration_ms"`
	Status     string                  `json:"status"` // "success" | "partial" | "failed"
	Reconciled int                     `json:"reconciled"`
	Discovered int                     `json:"discovered"`
	Imported   int                     `json:"imported"`
	Skipped    int                     `json:"skipped"`
	Failed     int                     `json:"failed"`
	ByDomain   map[string]*DomainStats `json:"by_domain"`
	Errors     []string                `json:"errors,omitempty"`
}

// InspectReport summarizes single-URL fetch, preflight, and optional Mealie test-scrape diagnostics.
// Pre-conditions: Returned by Engine.Inspect.
// Post-conditions: Preflight verdict is always populated.
type InspectReport struct {
	URL        string           `json:"url"`
	FinalURL   string           `json:"final_url"`
	HTTPStatus int              `json:"http_status"`
	Preflight  preflight.Result `json:"preflight"`
	MealieSlug string           `json:"mealie_slug,omitempty"`
	TestScrape string           `json:"test_scrape,omitempty"`
	Error      string           `json:"error,omitempty"`
}

// StatusReport merges local SQLite summary with optional live Mealie inventory count.
// Pre-conditions: Returned by Engine.Status.
// Post-conditions: LedgerSummary contains aggregated state and domain counts.
type StatusReport struct {
	LedgerSummary ledger.StatusSummary `json:"ledger_summary"`
	MealieTotal   int                  `json:"mealie_total"`
	Reconciled    int                  `json:"reconciled"`
}

// SeedOptions configures Engine.Seed execution filters and budgets.
// Pre-conditions: None.
// Post-conditions: Defaults applied for non-positive MaxPerRun and Workers.
type SeedOptions struct {
	CuisineFilter string
	DomainFilter  string
	MaxPerRun     int
	Workers       int
	DryRun        bool
}

// SyncOptions configures Engine.Sync execution filters and budgets.
// Pre-conditions: None.
// Post-conditions: Defaults applied for non-positive MaxPerRun and Workers.
type SyncOptions struct {
	DomainFilter    string
	MaxPerRun       int
	Workers         int
	IncludeSitemaps bool
	DryRun          bool
}

// InspectOptions configures Engine.Inspect single-URL diagnostics.
// Pre-conditions: URL must be valid.
// Post-conditions: TLSMode defaults to chrome_auto when blank.
type InspectOptions struct {
	URL        string
	TLSMode    string
	TestScrape bool
}
