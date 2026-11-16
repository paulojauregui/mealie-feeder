package feeder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/paulojauregui/mealie-feeder/internal/config"
	"github.com/paulojauregui/mealie-feeder/internal/discover"
	"github.com/paulojauregui/mealie-feeder/internal/fetch"
	"github.com/paulojauregui/mealie-feeder/internal/ledger"
	"github.com/paulojauregui/mealie-feeder/internal/mealie"
	"github.com/paulojauregui/mealie-feeder/internal/preflight"
)

// Dependencies aggregates injected subsystems for Engine construction and deterministic unit testing.
// Pre-conditions: Fetcher, Store, and Registry must be non-nil; Mealie may be nil only for offline Inspect/Status.
// Post-conditions: Validated by NewEngine.
type Dependencies struct {
	Registry *config.SourceRegistry
	Seeds    *config.SeedManifest
	Store    ledger.Store
	Fetcher  fetch.Fetcher
	Mealie   mealie.Client
	Limiter  *fetch.DomainLimiter
	Clock    func() time.Time
}

// Engine coordinates Mealie reconciliation, candidate discovery, polite fetching, preflighting, and ingestion.
// Pre-conditions: Constructed via NewEngine.
// Post-conditions: Safe for clean cancellation via ctx; executes strictly serial work per domain with bounded cross-domain workers.
type Engine struct {
	registry *config.SourceRegistry
	seeds    *config.SeedManifest
	store    ledger.Store
	fetcher  fetch.Fetcher
	mealie   mealie.Client
	limiter  *fetch.DomainLimiter
	clock    func() time.Time
	logger   *slog.Logger
}

// NewEngine validates Dependencies and returns a configured *Engine.
// Pre-conditions: deps.Registry, deps.Store, and deps.Fetcher must be non-nil.
// Post-conditions: Uses time.Now().UTC when deps.Clock is nil.
func NewEngine(deps Dependencies, logger *slog.Logger) (*Engine, error) {
	if deps.Registry == nil {
		return nil, errors.New("feeder: Registry is required")
	}
	if deps.Store == nil {
		return nil, errors.New("feeder: Store is required")
	}
	if deps.Fetcher == nil {
		return nil, errors.New("feeder: Fetcher is required")
	}

	clock := deps.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}

	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return &Engine{
		registry: deps.Registry,
		seeds:    deps.Seeds,
		store:    deps.Store,
		fetcher:  deps.Fetcher,
		mealie:   deps.Mealie,
		limiter:  deps.Limiter,
		clock:    clock,
		logger:   logger,
	}, nil
}

// reconcileMealieInventory syncs existing Mealie orgURLs into local ledger.
func (e *Engine) reconcileMealieInventory(ctx context.Context) (int, error) {
	if e.mealie == nil {
		return 0, nil
	}

	summaries, err := e.mealie.ListAllRecipeURLs(ctx, 100)
	if err != nil {
		return 0, fmt.Errorf("feeder: list mealie recipe urls: %w", err)
	}

	reconcileMap := make(map[string]string)
	for _, s := range summaries {
		if strings.TrimSpace(s.OrgURL) != "" {
			reconcileMap[s.OrgURL] = s.Slug
		}
	}

	return e.store.ReconcileMealieOrgURLs(ctx, reconcileMap, e.clock())
}

// Seed reconciles Mealie orgURLs, filters Seeds by CuisineFilter/DomainFilter, upserts candidates, and ingests up to budgets.
// Pre-conditions: deps.Seeds and deps.Mealie must be non-nil.
// Post-conditions: Honors DryRun (skips Mealie POST and DB state mutations on items); updates runs table cleanly.
func (e *Engine) Seed(ctx context.Context, opts SeedOptions) (*RunReport, error) {
	if e.seeds == nil {
		return nil, errors.New("feeder: seed manifest not configured")
	}
	if e.mealie == nil && !opts.DryRun {
		return nil, errors.New("feeder: mealie client not configured")
	}

	startedAt := e.clock()
	runID := fmt.Sprintf("seed-%d", startedAt.UnixNano())

	report := &RunReport{
		RunID:     runID,
		Command:   "seed",
		DryRun:    opts.DryRun,
		StartedAt: startedAt,
		Status:    "running",
		ByDomain:  make(map[string]*DomainStats),
	}

	_ = e.store.RecordRunStart(ctx, ledger.RunRecord{
		RunID:     runID,
		Command:   "seed",
		StartedAt: startedAt,
		Status:    "running",
		DryRun:    opts.DryRun,
	})

	// 1. Reconcile existing Mealie recipes
	reconciled, err := e.reconcileMealieInventory(ctx)
	if err != nil {
		e.logger.Warn("Mealie inventory reconciliation failed", "error", err)
	}
	report.Reconciled = reconciled

	// 2. Filter seeds
	var targetSeeds []config.SeedEntry
	cuisineFilter := strings.ToLower(strings.TrimSpace(opts.CuisineFilter))
	domainFilter := strings.ToLower(strings.TrimSpace(opts.DomainFilter))

	for _, s := range e.seeds.Recipes {
		if cuisineFilter != "" && !strings.Contains(strings.ToLower(s.Cuisine), cuisineFilter) {
			continue
		}
		if domainFilter != "" && !strings.Contains(strings.ToLower(s.Domain), domainFilter) {
			continue
		}
		targetSeeds = append(targetSeeds, s)
	}

	// 3. Upsert discovered seeds
	var domainOrder []string
	seedsByDomain := make(map[string][]config.SeedEntry)

	for _, s := range targetSeeds {
		inserted, err := e.store.UpsertDiscovered(ctx, ledger.RecipeRecord{
			CanonicalURL:    s.URL,
			Domain:          s.Domain,
			DiscoverySource: ledger.DiscoverySeed,
			Cuisine:         s.Cuisine,
			Title:           s.Title,
			DiscoveredAt:    e.clock(),
		})
		if err == nil && inserted {
			report.Discovered++
		}

		if report.ByDomain[s.Domain] == nil {
			report.ByDomain[s.Domain] = &DomainStats{Domain: s.Domain}
			domainOrder = append(domainOrder, s.Domain)
		}
		if inserted {
			report.ByDomain[s.Domain].Discovered++
		}
		seedsByDomain[s.Domain] = append(seedsByDomain[s.Domain], s)
	}

	// 4. Process pending seeds per domain
	workers := opts.Workers
	if workers <= 0 {
		workers = 4
	}

	maxPerRun := opts.MaxPerRun
	if maxPerRun <= 0 {
		maxPerRun = 1000000
	}

	var mu sync.Mutex
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(workers)

	for _, domain := range domainOrder {
		dom := domain
		g.Go(func() error {
			srcConfig, hasConfig := e.registry.SourceByDomain(dom)
			if !hasConfig {
				srcConfig = config.SourceConfig{
					Domain:     dom,
					TLSMode:    config.TLSModeChromeAuto,
					SchemaMode: config.SchemaModeJSONLD,
				}
			}

			pending, err := e.store.ListPendingByDomain(gCtx, dom, e.clock(), maxPerRun)
			if err != nil {
				return nil
			}

			for _, rec := range pending {
				if gCtx.Err() != nil {
					return gCtx.Err()
				}

				mu.Lock()
				if report.Imported >= maxPerRun {
					mu.Unlock()
					break
				}
				mu.Unlock()

				outcome := e.processCandidate(gCtx, rec, srcConfig, opts.DryRun)

				mu.Lock()
				stats := report.ByDomain[dom]
				switch outcome {
				case "imported":
					report.Imported++
					stats.Imported++
				case "skipped":
					report.Skipped++
					stats.Skipped++
				case "failed":
					report.Failed++
					stats.Failed++
				}
				mu.Unlock()
			}
			return nil
		})
	}

	_ = g.Wait()

	finishedAt := e.clock()
	report.FinishedAt = finishedAt
	report.DurationMS = finishedAt.Sub(startedAt).Milliseconds()

	status := "success"
	if report.Failed > 0 && report.Imported == 0 {
		status = "failed"
	} else if report.Failed > 0 {
		status = "partial"
	}
	report.Status = status

	_ = e.store.RecordRunFinish(ctx, ledger.RunRecord{
		RunID:           runID,
		Command:         "seed",
		FinishedAt:      finishedAt,
		Status:          status,
		DiscoveredCount: report.Discovered,
		ImportedCount:   report.Imported,
		SkippedCount:    report.Skipped,
		FailedCount:     report.Failed,
		DryRun:          opts.DryRun,
	})

	return report, nil
}

// Sync reconciles Mealie orgURLs, runs RSS/sitemap discovery for enabled sources, processes pending/retryable items up to budgets.
// Pre-conditions: deps.Mealie must be non-nil.
// Post-conditions: Isolates single-domain errors so other healthy domains continue; bounds cross-domain concurrency via errgroup.
func (e *Engine) Sync(ctx context.Context, opts SyncOptions) (*RunReport, error) {
	if e.mealie == nil && !opts.DryRun {
		return nil, errors.New("feeder: mealie client not configured")
	}

	startedAt := e.clock()
	runID := fmt.Sprintf("sync-%d", startedAt.UnixNano())

	report := &RunReport{
		RunID:     runID,
		Command:   "sync",
		DryRun:    opts.DryRun,
		StartedAt: startedAt,
		Status:    "running",
		ByDomain:  make(map[string]*DomainStats),
	}

	_ = e.store.RecordRunStart(ctx, ledger.RunRecord{
		RunID:     runID,
		Command:   "sync",
		StartedAt: startedAt,
		Status:    "running",
		DryRun:    opts.DryRun,
	})

	// 1. Reconcile existing Mealie recipes
	reconciled, err := e.reconcileMealieInventory(ctx)
	if err != nil {
		e.logger.Warn("Mealie inventory reconciliation failed", "error", err)
	}
	report.Reconciled = reconciled

	// 2. Select sources
	var activeSources []config.SourceConfig
	domainFilter := strings.ToLower(strings.TrimSpace(opts.DomainFilter))

	for _, src := range e.registry.Sources {
		if !src.Enabled {
			continue
		}
		if domainFilter != "" && !strings.Contains(strings.ToLower(src.Domain), domainFilter) {
			continue
		}
		activeSources = append(activeSources, src)
	}

	// 3. Process sources concurrently
	workers := opts.Workers
	if workers <= 0 {
		workers = 4
	}

	maxPerRun := opts.MaxPerRun
	if maxPerRun <= 0 {
		maxPerRun = 100
	}

	var mu sync.Mutex
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(workers)

	for _, source := range activeSources {
		src := source
		g.Go(func() error {
			stats := &DomainStats{Domain: src.Domain}
			mu.Lock()
			report.ByDomain[src.Domain] = stats
			mu.Unlock()

			// Load feed cursors
			prevCursors := make(map[string]discover.FeedCursorState)
			for _, feedURL := range src.RSSURLs {
				if cursor, ok, _ := e.store.GetFeedCursor(gCtx, feedURL); ok {
					prevCursors[feedURL] = discover.FeedCursorState{
						FeedURL:             cursor.FeedURL,
						ETag:                cursor.ETag,
						LastModified:        cursor.LastModified,
						LastItemPublishedAt: cursor.LastItemPublishedAt,
					}
				}
			}

			// Run discovery
			outcome, err := discover.DiscoverSourceCandidates(gCtx, e.fetcher, src, prevCursors, opts.IncludeSitemaps)
			if err != nil {
				mu.Lock()
				report.Errors = append(report.Errors, fmt.Sprintf("%s discovery: %v", src.Domain, err))
				mu.Unlock()
				return nil // Isolate domain error
			}

			// Apply crawl delay floor if limiter configured
			if e.limiter != nil && outcome.RobotsCrawlDelay > 0 {
				e.limiter.SetFloor(src.Domain, outcome.RobotsCrawlDelay)
			}

			// Save updated cursors
			for _, cur := range outcome.UpdatedCursors {
				_ = e.store.SaveFeedCursor(gCtx, ledger.FeedCursor{
					FeedURL:             cur.FeedURL,
					Domain:              src.Domain,
					ETag:                cur.ETag,
					LastModified:        cur.LastModified,
					LastItemPublishedAt: cur.LastItemPublishedAt,
					LastCheckedAt:       cur.LastCheckedAt,
				})
			}

			// Upsert discovered candidates
			for _, cand := range outcome.Candidates {
				discSource := ledger.DiscoverySitemap
				if cand.SourceKind == "rss" {
					discSource = ledger.DiscoveryRSS
				}
				inserted, err := e.store.UpsertDiscovered(gCtx, ledger.RecipeRecord{
					CanonicalURL:    cand.URL,
					Domain:          cand.Domain,
					DiscoverySource: discSource,
					DiscoveredAt:    e.clock(),
				})
				if err == nil && inserted {
					mu.Lock()
					report.Discovered++
					stats.Discovered++
					mu.Unlock()
				}
			}

			// Process pending candidates for this domain
			domainLimit := src.MaxPerRun
			if domainLimit <= 0 || domainLimit > maxPerRun {
				domainLimit = maxPerRun
			}

			pending, err := e.store.ListPendingByDomain(gCtx, src.Domain, e.clock(), domainLimit)
			if err != nil {
				return nil
			}

			for _, rec := range pending {
				if gCtx.Err() != nil {
					return gCtx.Err()
				}

				mu.Lock()
				if report.Imported >= maxPerRun {
					mu.Unlock()
					break
				}
				mu.Unlock()

				itemOutcome := e.processCandidate(gCtx, rec, src, opts.DryRun)

				mu.Lock()
				switch itemOutcome {
				case "imported":
					report.Imported++
					stats.Imported++
				case "skipped":
					report.Skipped++
					stats.Skipped++
				case "failed":
					report.Failed++
					stats.Failed++
				}
				mu.Unlock()
			}

			return nil
		})
	}

	_ = g.Wait()

	finishedAt := e.clock()
	report.FinishedAt = finishedAt
	report.DurationMS = finishedAt.Sub(startedAt).Milliseconds()

	status := "success"
	if report.Failed > 0 && report.Imported == 0 {
		status = "failed"
	} else if report.Failed > 0 {
		status = "partial"
	}
	report.Status = status

	_ = e.store.RecordRunFinish(ctx, ledger.RunRecord{
		RunID:           runID,
		Command:         "sync",
		FinishedAt:      finishedAt,
		Status:          status,
		DiscoveredCount: report.Discovered,
		ImportedCount:   report.Imported,
		SkippedCount:    report.Skipped,
		FailedCount:     report.Failed,
		DryRun:          opts.DryRun,
	})

	return report, nil
}

// processCandidate fetches, validates preflight, and ingests a single candidate URL.
func (e *Engine) processCandidate(ctx context.Context, rec ledger.RecipeRecord, src config.SourceConfig, dryRun bool) string {
	if dryRun {
		return "skipped"
	}

	reqOpts := fetch.RequestOptions{
		TLSMode:     string(src.TLSMode),
		MinInterval: src.MinInterval,
		Timeout:     src.Timeout,
		MaxRetries:  src.MaxRetries,
	}

	resp, err := e.fetcher.Fetch(ctx, rec.CanonicalURL, reqOpts)
	if err != nil {
		if errors.Is(err, fetch.ErrChallengeDetected) || errors.Is(err, fetch.ErrPaywallDetected) {
			_ = e.store.MarkSkipped(ctx, rec.CanonicalURL, ledger.StateSkippedPaywall, err.Error(), 0, e.clock())
			return "skipped"
		}
		if errors.Is(err, fetch.ErrPermanentHTTP) {
			_ = e.store.MarkFailed(ctx, rec.CanonicalURL, err.Error(), 404, true, src.MaxRetries, e.clock())
			return "failed"
		}
		_ = e.store.MarkFailed(ctx, rec.CanonicalURL, err.Error(), 0, false, src.MaxRetries, e.clock())
		return "failed"
	}

	// Preflight validation
	result := preflight.InspectHTML(resp.Body, src.SchemaMode)

	switch result.Verdict {
	case preflight.VerdictValid:
		// Send raw HTML directly to Mealie
		createReq := mealie.CreateFromHTMLRequest{
			Data:              string(resp.Body),
			URL:               rec.CanonicalURL,
			IncludeTags:       true,
			IncludeCategories: true,
		}

		slug, err := e.mealie.CreateFromHTML(ctx, createReq)
		if err != nil {
			_ = e.store.MarkFailed(ctx, rec.CanonicalURL, fmt.Sprintf("mealie create: %v", err), resp.StatusCode, false, src.MaxRetries, e.clock())
			return "failed"
		}

		// Optional stub verification
		if src.PurgeStubs {
			detail, err := e.mealie.GetRecipe(ctx, slug)
			if err == nil && detail != nil {
				var ingStrings, insStrings []string
				for _, ing := range detail.Ingredients {
					ingStrings = append(ingStrings, ing.Display)
				}
				for _, ins := range detail.Instructions {
					insStrings = append(insStrings, ins.Text)
				}
				if preflight.IsOpenGraphStub(ingStrings, insStrings, detail.Description) {
					_ = e.mealie.DeleteRecipe(ctx, slug)
					_ = e.store.MarkSkipped(ctx, rec.CanonicalURL, ledger.StateSkippedNonRecipe, "opengraph stub detected and purged", resp.StatusCode, e.clock())
					return "skipped"
				}
			}
		}

		title := result.Title
		if title == "" {
			title = rec.Title
		}
		_ = e.store.MarkImported(ctx, rec.CanonicalURL, slug, title, resp.StatusCode, e.clock())
		return "imported"

	case preflight.VerdictPaywall:
		_ = e.store.MarkSkipped(ctx, rec.CanonicalURL, ledger.StateSkippedPaywall, result.Reason, resp.StatusCode, e.clock())
		return "skipped"

	case preflight.VerdictEmptyRecipe, preflight.VerdictNonRecipe:
		fallthrough
	default:
		_ = e.store.MarkSkipped(ctx, rec.CanonicalURL, ledger.StateSkippedNonRecipe, result.Reason, resp.StatusCode, e.clock())
		return "skipped"
	}
}

// Inspect fetches one URL once, runs preflight validation, and optionally invokes Mealie TestScrapeURL without mutating SQLite.
// Pre-conditions: opts.URL must parse cleanly as http/https.
// Post-conditions: Never fails silently; records exact fetch or preflight verdict in *InspectReport.
func (e *Engine) Inspect(ctx context.Context, opts InspectOptions) (*InspectReport, error) {
	rawURL := strings.TrimSpace(opts.URL)
	if rawURL == "" {
		return nil, errors.New("feeder: Inspect requires non-empty URL")
	}

	tlsMode := opts.TLSMode
	if tlsMode == "" {
		tlsMode = "chrome_auto"
	}

	resp, err := e.fetcher.Fetch(ctx, rawURL, fetch.RequestOptions{
		TLSMode: tlsMode,
	})
	if err != nil {
		return &InspectReport{
			URL:   rawURL,
			Error: err.Error(),
		}, nil
	}

	result := preflight.InspectHTML(resp.Body, config.SchemaModeAuto)
	report := &InspectReport{
		URL:        rawURL,
		FinalURL:   resp.FinalURL,
		HTTPStatus: resp.StatusCode,
		Preflight:  result,
	}

	if opts.TestScrape && e.mealie != nil {
		detail, err := e.mealie.TestScrapeURL(ctx, rawURL)
		if err != nil {
			report.TestScrape = fmt.Sprintf("test-scrape error: %v", err)
		} else if detail != nil {
			report.TestScrape = fmt.Sprintf("scraped %q with %d ingredients, %d steps", detail.Name, len(detail.Ingredients), len(detail.Instructions))
		}
	}

	return report, nil
}

// Status queries SQLite ledger statistics and optionally reconciles live Mealie inventory when deps.Mealie != nil and reconcileMealie is true.
// Pre-conditions: deps.Store non-nil.
// Post-conditions: Returns complete StatusReport.
func (e *Engine) Status(ctx context.Context, reconcileMealie bool) (*StatusReport, error) {
	var reconciledCount int
	if reconcileMealie && e.mealie != nil {
		r, err := e.reconcileMealieInventory(ctx)
		if err == nil {
			reconciledCount = r
		}
	}

	summary, err := e.store.GetStatusSummary(ctx, 10)
	if err != nil {
		return nil, fmt.Errorf("feeder: get status summary: %w", err)
	}

	var mealieTotal int
	if e.mealie != nil {
		summaries, err := e.mealie.ListAllRecipeURLs(ctx, 100)
		if err == nil {
			mealieTotal = len(summaries)
		}
	}

	return &StatusReport{
		LedgerSummary: summary,
		MealieTotal:   mealieTotal,
		Reconciled:    reconciledCount,
	}, nil
}
