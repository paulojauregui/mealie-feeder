package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/paulojauregui/mealie-feeder/internal/config"
	"github.com/paulojauregui/mealie-feeder/internal/feeder"
	"github.com/paulojauregui/mealie-feeder/internal/fetch"
	"github.com/paulojauregui/mealie-feeder/internal/ledger"
	"github.com/paulojauregui/mealie-feeder/internal/mealie"
)

// Deterministic CLI exit codes.
const (
	ExitSuccess        = 0
	ExitFatalError     = 1
	ExitUsageOrConfig  = 2
	ExitPartialFailure = 3
)

// Build metadata injected via -ldflags during container builds.
var (
	BuildVersion = "dev"
	BuildCommit  = "none"
	BuildDate    = "unknown"
)

// RunCLI parses global flags and subcommand arguments, builds runtime dependencies, executes the requested command,
// writes structured/human report output to stdout/stderr, and returns a deterministic exit code.
// Pre-conditions: ctx should handle SIGINT/SIGTERM cancellation; envLookup resolves Twelve-Factor defaults.
// Post-conditions: Never calls os.Exit directly (main delegates to os.Exit(RunCLI(...))), enabling deterministic unit tests.
func RunCLI(ctx context.Context, args []string, stdout, stderr io.Writer, envLookup func(string) (string, bool)) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if envLookup == nil {
		envLookup = os.LookupEnv
	}

	envCfg := config.ResolveEnv(envLookup)

	// 1. Parse global flags
	globalFS := flag.NewFlagSet("mealie-feeder", flag.ContinueOnError)
	globalFS.SetOutput(stderr)

	logFormat := globalFS.String("log-format", "text", "Log format: text | json")
	logLevelStr := globalFS.String("log-level", "info", "Log level: debug | info | warn | error")
	globalTimeout := globalFS.Duration("timeout", 25*time.Minute, "Global run context timeout")

	if err := globalFS.Parse(args); err != nil {
		return ExitUsageOrConfig
	}

	remaining := globalFS.Args()
	if len(remaining) == 0 {
		printUsage(stderr)
		return ExitUsageOrConfig
	}

	subcommand := remaining[0]
	subArgs := remaining[1:]

	// 2. Configure structured logger
	var level slog.Level
	switch strings.ToLower(*logLevelStr) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var handler slog.Handler
	if strings.ToLower(*logFormat) == "json" {
		handler = slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level})
	}
	logger := slog.New(handler)

	// 3. Apply global timeout
	if *globalTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *globalTimeout)
		defer cancel()
	}

	// 4. Route subcommand
	switch subcommand {
	case "version":
		fmt.Fprintf(stdout, "mealie-feeder %s (commit: %s, date: %s, runtime: %s)\n", BuildVersion, BuildCommit, BuildDate, runtime.Version())
		return ExitSuccess

	case "seed":
		return runSeed(ctx, subArgs, stdout, stderr, envCfg, logger)

	case "sync":
		return runSync(ctx, subArgs, stdout, stderr, envCfg, logger)

	case "inspect":
		return runInspect(ctx, subArgs, stdout, stderr, envCfg, logger)

	case "status":
		return runStatus(ctx, subArgs, stdout, stderr, envCfg, logger)

	default:
		fmt.Fprintf(stderr, "error: unknown subcommand %q\n", subcommand)
		printUsage(stderr)
		return ExitUsageOrConfig
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `Usage: mealie-feeder [global-flags] <subcommand> [flags]

Subcommands:
  seed     Ingest curated seeds/greatest-hits.yaml manifest
  sync     Incremental crawl of RSS/Atom feeds + sitemaps
  inspect  Single-URL diagnostic: fetch, preflight, optional test-scrape
  status   Print SQLite ledger summary and reconcile Mealie count
  version  Print build version and runtime info

Global Flags:
  --log-format  Log format: text | json (default: text)
  --log-level   Log level: debug | info | warn | error (default: info)
  --timeout     Global execution timeout (default: 25m)
`)
}

func runSeed(ctx context.Context, args []string, stdout, stderr io.Writer, envCfg config.EnvConfig, logger *slog.Logger) int {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	fs.SetOutput(stderr)

	seedsPath := fs.String("seeds", envCfg.SeedsPath, "Path to seeds manifest")
	sourcesPath := fs.String("sources", envCfg.SourcesPath, "Path to sources registry")
	dbPath := fs.String("db", envCfg.DBPath, "Path to SQLite ledger database")
	mealieURL := fs.String("mealie-url", envCfg.MealieURL, "Mealie instance base URL")
	cuisineFilter := fs.String("cuisine", "", "Optional cuisine filter")
	domainFilter := fs.String("domain", "", "Optional source domain filter")
	maxPerRun := fs.Int("max-per-run", 0, "Max recipes to import (0 = unlimited)")
	workers := fs.Int("workers", 4, "Concurrent domain worker count")
	dryRun := fs.Bool("dry-run", false, "Simulate execution without modifying Mealie or marking imported")
	jsonOut := fs.Bool("json", false, "Emit report as JSON")

	if err := fs.Parse(args); err != nil {
		return ExitUsageOrConfig
	}

	trimmedURL := strings.TrimSpace(*mealieURL)
	if trimmedURL == "" {
		fmt.Fprintf(stderr, "error: --mealie-url or MEALIE_URL is required\n")
		return ExitUsageOrConfig
	}
	if strings.TrimSpace(envCfg.MealieAPIToken) == "" {
		fmt.Fprintf(stderr, "error: MEALIE_API_TOKEN environment variable is required\n")
		return ExitUsageOrConfig
	}

	registry, err := config.LoadSources(*sourcesPath)
	if err != nil {
		fmt.Fprintf(stderr, "error loading sources %q: %v\n", *sourcesPath, err)
		return ExitUsageOrConfig
	}

	seeds, err := config.LoadSeeds(*seedsPath)
	if err != nil {
		fmt.Fprintf(stderr, "error loading seeds %q: %v\n", *seedsPath, err)
		return ExitUsageOrConfig
	}

	store, err := ledger.OpenSQLite(ctx, *dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "error opening database %q: %v\n", *dbPath, err)
		return ExitFatalError
	}
	defer store.Close()

	mealieClient, err := mealie.NewHTTPClient(trimmedURL, envCfg.MealieAPIToken, nil)
	if err != nil {
		fmt.Fprintf(stderr, "error configuring mealie client: %v\n", err)
		return ExitUsageOrConfig
	}

	limiter := fetch.NewDomainLimiter(250*time.Millisecond, nil)
	fetcher := fetch.NewClient(limiter, nil)

	engine, err := feeder.NewEngine(feeder.Dependencies{
		Registry: registry,
		Seeds:    seeds,
		Store:    store,
		Fetcher:  fetcher,
		Mealie:   mealieClient,
		Limiter:  limiter,
	}, logger)
	if err != nil {
		fmt.Fprintf(stderr, "error initializing engine: %v\n", err)
		return ExitFatalError
	}

	opts := feeder.SeedOptions{
		CuisineFilter: *cuisineFilter,
		DomainFilter:  *domainFilter,
		MaxPerRun:     *maxPerRun,
		Workers:       *workers,
		DryRun:        *dryRun,
	}

	report, err := engine.Seed(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "seed error: %v\n", err)
		return ExitFatalError
	}

	if *jsonOut {
		_ = json.NewEncoder(stdout).Encode(report)
	} else {
		fmt.Fprintf(stdout, "Seed Complete (status: %s, duration: %dms)\n", report.Status, report.DurationMS)
		fmt.Fprintf(stdout, "  Reconciled: %d, Discovered: %d, Imported: %d, Skipped: %d, Failed: %d\n",
			report.Reconciled, report.Discovered, report.Imported, report.Skipped, report.Failed)
	}

	if report.Status == "partial" {
		return ExitPartialFailure
	}
	if report.Status == "failed" {
		return ExitFatalError
	}
	return ExitSuccess
}

func runSync(ctx context.Context, args []string, stdout, stderr io.Writer, envCfg config.EnvConfig, logger *slog.Logger) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stderr)

	sourcesPath := fs.String("sources", envCfg.SourcesPath, "Path to sources registry")
	dbPath := fs.String("db", envCfg.DBPath, "Path to SQLite ledger database")
	mealieURL := fs.String("mealie-url", envCfg.MealieURL, "Mealie instance base URL")
	domainFilter := fs.String("domain", "", "Optional source domain filter")
	maxPerRun := fs.Int("max-per-run", 100, "Max recipes to import across run")
	workers := fs.Int("workers", 4, "Concurrent domain worker count")
	includeSitemaps := fs.Bool("include-sitemaps", true, "Traverse sitemaps in addition to RSS")
	dryRun := fs.Bool("dry-run", false, "Simulate execution without modifying Mealie or marking imported")
	jsonOut := fs.Bool("json", false, "Emit report as JSON")

	if err := fs.Parse(args); err != nil {
		return ExitUsageOrConfig
	}

	trimmedURL := strings.TrimSpace(*mealieURL)
	if trimmedURL == "" {
		fmt.Fprintf(stderr, "error: --mealie-url or MEALIE_URL is required\n")
		return ExitUsageOrConfig
	}
	if strings.TrimSpace(envCfg.MealieAPIToken) == "" {
		fmt.Fprintf(stderr, "error: MEALIE_API_TOKEN environment variable is required\n")
		return ExitUsageOrConfig
	}

	registry, err := config.LoadSources(*sourcesPath)
	if err != nil {
		fmt.Fprintf(stderr, "error loading sources %q: %v\n", *sourcesPath, err)
		return ExitUsageOrConfig
	}

	store, err := ledger.OpenSQLite(ctx, *dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "error opening database %q: %v\n", *dbPath, err)
		return ExitFatalError
	}
	defer store.Close()

	mealieClient, err := mealie.NewHTTPClient(trimmedURL, envCfg.MealieAPIToken, nil)
	if err != nil {
		fmt.Fprintf(stderr, "error configuring mealie client: %v\n", err)
		return ExitUsageOrConfig
	}

	limiter := fetch.NewDomainLimiter(250*time.Millisecond, nil)
	fetcher := fetch.NewClient(limiter, nil)

	engine, err := feeder.NewEngine(feeder.Dependencies{
		Registry: registry,
		Store:    store,
		Fetcher:  fetcher,
		Mealie:   mealieClient,
		Limiter:  limiter,
	}, logger)
	if err != nil {
		fmt.Fprintf(stderr, "error initializing engine: %v\n", err)
		return ExitFatalError
	}

	opts := feeder.SyncOptions{
		DomainFilter:    *domainFilter,
		MaxPerRun:       *maxPerRun,
		Workers:         *workers,
		IncludeSitemaps: *includeSitemaps,
		DryRun:          *dryRun,
	}

	report, err := engine.Sync(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "sync error: %v\n", err)
		return ExitFatalError
	}

	if *jsonOut {
		_ = json.NewEncoder(stdout).Encode(report)
	} else {
		fmt.Fprintf(stdout, "Sync Complete (status: %s, duration: %dms)\n", report.Status, report.DurationMS)
		fmt.Fprintf(stdout, "  Reconciled: %d, Discovered: %d, Imported: %d, Skipped: %d, Failed: %d\n",
			report.Reconciled, report.Discovered, report.Imported, report.Skipped, report.Failed)
	}

	if report.Status == "partial" {
		return ExitPartialFailure
	}
	if report.Status == "failed" {
		return ExitFatalError
	}
	return ExitSuccess
}

func runInspect(ctx context.Context, args []string, stdout, stderr io.Writer, envCfg config.EnvConfig, logger *slog.Logger) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)

	targetURL := fs.String("url", "", "Recipe URL to inspect (required)")
	sourcesPath := fs.String("sources", envCfg.SourcesPath, "Path to sources registry")
	tlsMode := fs.String("tls-mode", "chrome_auto", "Outbound TLS mode: chrome_auto | safari_auto | plain")
	testScrape := fs.Bool("test-scrape", false, "Test scrape preview against Mealie test-scrape-url")
	mealieURL := fs.String("mealie-url", envCfg.MealieURL, "Mealie instance base URL (for --test-scrape)")
	jsonOut := fs.Bool("json", false, "Emit report as JSON")

	if err := fs.Parse(args); err != nil {
		return ExitUsageOrConfig
	}

	trimmedURL := strings.TrimSpace(*targetURL)
	if trimmedURL == "" {
		fmt.Fprintf(stderr, "error: --url is required\n")
		return ExitUsageOrConfig
	}

	parsed, err := url.Parse(trimmedURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		fmt.Fprintf(stderr, "error: invalid target URL %q\n", trimmedURL)
		return ExitUsageOrConfig
	}

	// Sources registry is optional for single inspect
	registry, err := config.LoadSources(*sourcesPath)
	if err != nil {
		registry = &config.SourceRegistry{Version: 1}
		registry.ApplyDefaults()
	}

	// In-memory store for inspect
	store, _ := ledger.OpenSQLite(ctx, ":memory:")
	defer store.Close()

	var mealieClient mealie.Client
	if *testScrape && strings.TrimSpace(*mealieURL) != "" && strings.TrimSpace(envCfg.MealieAPIToken) != "" {
		mealieClient, _ = mealie.NewHTTPClient(*mealieURL, envCfg.MealieAPIToken, nil)
	}

	limiter := fetch.NewDomainLimiter(0, nil)
	fetcher := fetch.NewClient(limiter, nil)

	engine, _ := feeder.NewEngine(feeder.Dependencies{
		Registry: registry,
		Store:    store,
		Fetcher:  fetcher,
		Mealie:   mealieClient,
	}, logger)

	report, err := engine.Inspect(ctx, feeder.InspectOptions{
		URL:        trimmedURL,
		TLSMode:    *tlsMode,
		TestScrape: *testScrape,
	})
	if err != nil {
		fmt.Fprintf(stderr, "inspect error: %v\n", err)
		return ExitFatalError
	}

	if *jsonOut {
		_ = json.NewEncoder(stdout).Encode(report)
	} else {
		fmt.Fprintf(stdout, "URL: %s\n", report.URL)
		fmt.Fprintf(stdout, "Final URL: %s (Status: %d)\n", report.FinalURL, report.HTTPStatus)
		fmt.Fprintf(stdout, "Preflight: %s (%s)\n", report.Preflight.Verdict, report.Preflight.SchemaKind)
		fmt.Fprintf(stdout, "  Title: %q\n", report.Preflight.Title)
		fmt.Fprintf(stdout, "  Ingredients: %d, Instructions: %d\n", report.Preflight.IngredientCount, report.Preflight.InstructionCount)
		if report.TestScrape != "" {
			fmt.Fprintf(stdout, "Test Scrape: %s\n", report.TestScrape)
		}
		if report.Error != "" {
			fmt.Fprintf(stdout, "Error: %s\n", report.Error)
		}
	}

	return ExitSuccess
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer, envCfg config.EnvConfig, logger *slog.Logger) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db", envCfg.DBPath, "Path to SQLite ledger database")
	sourcesPath := fs.String("sources", envCfg.SourcesPath, "Path to sources registry")
	mealieURL := fs.String("mealie-url", envCfg.MealieURL, "Mealie instance base URL")
	reconcile := fs.Bool("reconcile", false, "Reconcile live Mealie inventory during status query")
	jsonOut := fs.Bool("json", false, "Emit report as JSON")

	if err := fs.Parse(args); err != nil {
		return ExitUsageOrConfig
	}

	registry, err := config.LoadSources(*sourcesPath)
	if err != nil {
		registry = &config.SourceRegistry{Version: 1}
		registry.ApplyDefaults()
	}

	store, err := ledger.OpenSQLite(ctx, *dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "error opening database %q: %v\n", *dbPath, err)
		return ExitFatalError
	}
	defer store.Close()

	var mealieClient mealie.Client
	if strings.TrimSpace(*mealieURL) != "" && strings.TrimSpace(envCfg.MealieAPIToken) != "" {
		mealieClient, _ = mealie.NewHTTPClient(*mealieURL, envCfg.MealieAPIToken, nil)
	}

	limiter := fetch.NewDomainLimiter(0, nil)
	fetcher := fetch.NewClient(limiter, nil)

	engine, _ := feeder.NewEngine(feeder.Dependencies{
		Registry: registry,
		Store:    store,
		Fetcher:  fetcher,
		Mealie:   mealieClient,
	}, logger)

	report, err := engine.Status(ctx, *reconcile)
	if err != nil {
		fmt.Fprintf(stderr, "status error: %v\n", err)
		return ExitFatalError
	}

	if *jsonOut {
		_ = json.NewEncoder(stdout).Encode(report)
	} else {
		fmt.Fprintf(stdout, "Total Recipes in Ledger: %d\n", report.LedgerSummary.TotalRecipes)
		for state, count := range report.LedgerSummary.ByState {
			fmt.Fprintf(stdout, "  %s: %d\n", state, count)
		}
		if report.MealieTotal > 0 {
			fmt.Fprintf(stdout, "Mealie Live Recipes: %d\n", report.MealieTotal)
		}
	}

	return ExitSuccess
}
