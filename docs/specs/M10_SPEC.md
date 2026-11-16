# M10 Specification: Ingestion Orchestrator, Budgets & Reporting

## 1. Objective & Target Files

Implement the core orchestration engine in `internal/feeder` wiring `config`, `discover`, `ledger`, `fetch`, `preflight`, and `mealie` across `Seed`, `Sync`, `Inspect`, and `Status` operations with bounded domain-partitioned concurrency and deterministic reporting.

**Target Files (strict scope):**
1. `internal/feeder/report.go`
2. `internal/feeder/engine.go`
3. `internal/feeder/engine_test.go`

---

## 2. Package Contract (`package feeder`)

### 2.1 Reports & Options (`internal/feeder/report.go`)

```go
package feeder

import (
	"time"

	"github.com/paulojauregui/mealie-feeder/internal/ledger"
	"github.com/paulojauregui/mealie-feeder/internal/preflight"
)

// DomainStats aggregates per-domain ingestion counts during a run.
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
	RunID         string                  `json:"run_id"`
	Command       string                  `json:"command"`
	DryRun        bool                    `json:"dry_run"`
	StartedAt     time.Time               `json:"started_at"`
	FinishedAt    time.Time               `json:"finished_at"`
	DurationMS    int64                   `json:"duration_ms"`
	Status        string                  `json:"status"` // "success" | "partial" | "failed"
	Reconciled    int                     `json:"reconciled"`
	Discovered    int                     `json:"discovered"`
	Imported      int                     `json:"imported"`
	Skipped       int                     `json:"skipped"`
	Failed        int                     `json:"failed"`
	ByDomain      map[string]*DomainStats `json:"by_domain"`
	Errors        []string                `json:"errors,omitempty"`
}

// InspectReport summarizes single-URL fetch, preflight, and optional Mealie test-scrape diagnostics.
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
type StatusReport struct {
	LedgerSummary ledger.StatusSummary `json:"ledger_summary"`
	MealieTotal   int                  `json:"mealie_total"`
	Reconciled    int                  `json:"reconciled"`
}

// SeedOptions configures Engine.Seed execution filters and budgets.
type SeedOptions struct {
	CuisineFilter string
	DomainFilter  string
	MaxPerRun     int
	Workers       int
	DryRun        bool
}

// SyncOptions configures Engine.Sync execution filters and budgets.
type SyncOptions struct {
	DomainFilter    string
	MaxPerRun       int
	Workers         int
	IncludeSitemaps bool
	DryRun          bool
}

// InspectOptions configures Engine.Inspect single-URL diagnostics.
type InspectOptions struct {
	URL        string
	TLSMode    string
	TestScrape bool
}
```

### 2.2 Orchestration Engine (`internal/feeder/engine.go`)

```go
// Dependencies aggregates injected subsystems for Engine construction and deterministic unit testing.
// Pre-conditions: Fetcher, Store, and Registry must be non-nil; Mealie may be nil only for offline Inspect/Status.
// Post-conditions: Validated by NewEngine.
type Dependencies struct {
	Registry *config.SourceRegistry
	Seeds    *config.SeedManifest // optional for Sync/Inspect/Status
	Store    ledger.Store
	Fetcher  fetch.Fetcher
	Mealie   mealie.Client
	Limiter  *fetch.DomainLimiter // optional; used to apply robots.txt Crawl-delay floors
	Clock    func() time.Time
}

// Engine coordinates Mealie reconciliation, candidate discovery, polite fetching, preflighting, and ingestion.
// Pre-conditions: Constructed via NewEngine.
// Post-conditions: Safe for clean cancellation via ctx; executes strictly serial work per domain with bounded cross-domain workers.
type Engine struct {
	// unexported fields
}

// NewEngine validates Dependencies and returns a configured *Engine.
// Pre-conditions: deps.Registry, deps.Store, and deps.Fetcher must be non-nil.
// Post-conditions: Uses time.Now().UTC when deps.Clock is nil.
func NewEngine(deps Dependencies, logger *slog.Logger) (*Engine, error)

// Seed reconciles Mealie orgURLs, filters Seeds by CuisineFilter/DomainFilter, upserts candidates, and ingests up to budgets.
// Pre-conditions: deps.Seeds and deps.Mealie must be non-nil.
// Post-conditions: Honors DryRun (skips Mealie POST and DB state mutations on items); updates runs table cleanly.
func (e *Engine) Seed(ctx context.Context, opts SeedOptions) (*RunReport, error)

// Sync reconciles Mealie orgURLs, runs RSS/sitemap discovery for enabled sources, processes pending/retryable items up to budgets.
// Pre-conditions: deps.Mealie must be non-nil.
// Post-conditions: Isolates single-domain errors so other healthy domains continue; bounds cross-domain concurrency via errgroup.
func (e *Engine) Sync(ctx context.Context, opts SyncOptions) (*RunReport, error)

// Inspect fetches one URL once, runs preflight validation, and optionally invokes Mealie TestScrapeURL without mutating SQLite or creating recipes.
// Pre-conditions: opts.URL must parse cleanly as http/https.
// Post-conditions: Never fails silently; records exact fetch or preflight verdict in *InspectReport.
func (e *Engine) Inspect(ctx context.Context, opts InspectOptions) (*InspectReport, error)

// Status queries SQLite ledger statistics and optionally reconciles live Mealie inventory when deps.Mealie != nil and reconcileMealie is true.
// Pre-conditions: deps.Store non-nil.
// Post-conditions: Returns complete StatusReport.
func (e *Engine) Status(ctx context.Context, reconcileMealie bool) (*StatusReport, error)
```

---

## 3. Orchestration Rules & Edge Cases

1. **Global & Per-Domain Budget Allocation:** `Sync` honors `min(source.MaxPerRun, remainingGlobalBudget)` per domain while ensuring every domain gets fair progress if `opts.MaxPerRun` is set.
2. **Post-Ingest Incomplete & OpenGraph Stub Guard:** After `CreateFromHTML` returns `slug`, if `src.AllowURLFallback` or `src.PurgeStubs` is enabled, verify `GetRecipe(ctx, slug)` with `preflight.IsOpenGraphStub` (detecting `"could not detect ingredients"` or `len(ingredients) == 0 || len(instructions) == 0`); if `PurgeStubs` is true and an incomplete stub was created, delete it and mark `skipped_non_recipe`.
3. **Zero Duplicate Imports:** Because `ReconcileMealieOrgURLs` executes before candidate processing and `ListPendingByDomain` returns only `discovered` or due `failed_retryable`, already-imported recipes are never re-fetched or re-posted.
4. **Resilient Domain Isolation:** An `ErrChallengeDetected` or transient failure on domain A updates domain A's ledger entries and records domain error without aborting domain B.

---

## 4. Test Plan (`internal/feeder/engine_test.go`)

1. `Seed` with mocked `Store`, `Fetcher`, `Mealie`: verifies existing Mealie `orgURL` is skipped, valid JSON-LD seed calls `CreateFromHTML` with `IncludeTags: true, IncludeCategories: true`, non-recipe marks `skipped_non_recipe`, and `403` challenge marks `skipped_paywall`.
2. `Seed` with `DryRun: true` executes zero `CreateFromHTML` calls.
3. `Sync` discovers from mocked RSS + sitemap, honors `MaxPerRun` cap, processes multiple domains concurrently with `-race`, and isolates a failing domain from healthy domains.
4. `Inspect` runs preflight and optional `TestScrapeURL` without mutating store.
5. `Status` returns aggregated ledger counts and live Mealie total.

---

## 5. Verification Command

```bash
go test -v -race -cover ./internal/feeder
```
