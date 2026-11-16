# M11 Specification: CLI Entrypoint, Subcommands & Structured Logging

## 1. Objective & Target Files

Implement the production CLI command router, standard library `flag` subcommands (`seed`, `sync`, `inspect`, `status`, `version`), structured `log/slog` configuration, signal cancellation, and deterministic exit codes in `cmd/mealie-feeder`.

**Target Files (strict scope):**
1. `cmd/mealie-feeder/cli.go`
2. `cmd/mealie-feeder/main.go`
3. `cmd/mealie-feeder/cli_test.go`

---

## 2. Package Contract (`package main`)

### 2.1 CLI Runner & Exit Codes (`cmd/mealie-feeder/cli.go`, `main.go`)

```go
package main

import (
	"context"
	"io"
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
func RunCLI(ctx context.Context, args []string, stdout, stderr io.Writer, envLookup func(string) (string, bool)) int
```

### 2.2 Supported Subcommands & Flags

1. Global flags before subcommand (or normalized parsing):
   - `--log-format` (`text` | `json`, default `text`)
   - `--log-level` (`debug` | `info` | `warn` | `error`, default `info`)
   - `--timeout` (duration, default `25m`)
2. `seed`:
   - `--seeds`, `--sources`, `--db`, `--mealie-url`, `--cuisine`, `--domain`, `--max-per-run`, `--workers`, `--dry-run`, `--json`
3. `sync`:
   - `--sources`, `--db`, `--mealie-url`, `--domain`, `--max-per-run`, `--workers`, `--include-sitemaps` (default `true`), `--dry-run`, `--json`
4. `inspect`:
   - `--url` (required), `--sources`, `--tls-mode`, `--test-scrape`, `--mealie-url`, `--json`
5. `status`:
   - `--db`, `--sources`, `--mealie-url`, `--reconcile`, `--json`
6. `version`:
   - Prints `BuildVersion`, `BuildCommit`, `BuildDate`, and `runtime.Version()` cleanly and returns `ExitSuccess`.

### 2.3 Security & Hygiene Rules

- `MEALIE_API_TOKEN` is read **strictly from environment** (never accepted as a CLI flag so process argument lists `ps aux` never leak credentials) and never printed in logs or error messages.
- `seed` and `sync` require non-empty `MEALIE_URL` and `MEALIE_API_TOKEN` (including `--dry-run` mode, which reconciles against Mealie's existing `orgURL` inventory via `GET /api/recipes` without mutating Mealie); return `ExitUsageOrConfig` with a clear actionable error when missing.
- Return `ExitPartialFailure` (`3`) when `RunReport.Status == "partial"` and `ExitFatalError` (`1`) on `RunReport.Status == "failed"` or unrecoverable system error.

---

## 3. Edge Cases & Test Plan (`cmd/mealie-feeder/cli_test.go`)

1. `version` returns `ExitSuccess` and prints build metadata + `go1.27`.
2. Missing subcommand, unknown subcommand, or unknown flag returns `ExitUsageOrConfig` (`2`) with usage output on `stderr`.
3. `seed` / `sync` without `MEALIE_URL` or `MEALIE_API_TOKEN` fails fast with `ExitUsageOrConfig` (`2`) before touching SQLite or network.
4. `inspect --url <httptest-url> --json` executes full CLI wiring against a local `httptest.NewServer` serving JSON-LD HTML and writes valid JSON `InspectReport` to `stdout`, returning `ExitSuccess`.
5. `status --db <temp.db> --json` opens SQLite cleanly and emits valid JSON `StatusSummary`.
6. `--log-format=json` emits valid JSON log lines to `stderr` without polluting `--json` report output on `stdout`.

---

## 4. Verification Command

```bash
go test -v -race -cover ./cmd/mealie-feeder
```
