# M04 Specification: SQLite Ledger Store & Reconciliation

## 1. Objective & Target Files

Implement the persistent SQLite state store using pure-Go `modernc.org/sqlite` (`CGO_ENABLED=0`) in `internal/ledger`, covering DDL schema creation, Mealie `orgURL` inventory reconciliation, retry scheduling, feed cursors, and run telemetry.

**Target Files (strict scope):**
1. `internal/ledger/sqlite.go`
2. `internal/ledger/sqlite_test.go`

---

## 2. Package Contract (`package ledger`)

### 2.1 Consumer Interface & Implementation

```go
package ledger

import (
	"context"
	"time"
)

// Store defines the persistence contract required by the ingestion orchestrator and CLI status reporter.
// Pre-conditions: Implementations must be safe for concurrent use across goroutines or serialize SQLite writes cleanly.
// Post-conditions: All timestamps persisted and returned in UTC RFC3339 format.
type Store interface {
	Close() error
	UpsertDiscovered(ctx context.Context, rec RecipeRecord) (inserted bool, err error)
	GetByCanonicalURL(ctx context.Context, canonicalURL string) (RecipeRecord, bool, error)
	ReconcileMealieOrgURLs(ctx context.Context, orgURLToSlug map[string]string, now time.Time) (reconciled int, err error)
	ListPendingByDomain(ctx context.Context, domain string, now time.Time, limit int) ([]RecipeRecord, error)
	MarkImported(ctx context.Context, canonicalURL, mealieSlug, title string, httpStatus int, now time.Time) error
	MarkSkipped(ctx context.Context, canonicalURL string, state State, reason string, httpStatus int, now time.Time) error
	MarkFailed(ctx context.Context, canonicalURL string, errMsg string, httpStatus int, permanent bool, maxAttempts int, now time.Time) error
	GetFeedCursor(ctx context.Context, feedURL string) (FeedCursor, bool, error)
	SaveFeedCursor(ctx context.Context, cursor FeedCursor) error
	RecordRunStart(ctx context.Context, run RunRecord) error
	RecordRunFinish(ctx context.Context, run RunRecord) error
	GetStatusSummary(ctx context.Context, recentLimit int) (StatusSummary, error)
}

// SQLiteStore persists mealie-feeder state in SQLite via modernc.org/sqlite ("sqlite" driver).
// Pre-conditions: Created exclusively via OpenSQLite.
// Post-conditions: Enforces WAL journal mode, busy_timeout=5000ms, and foreign_keys=ON; single-writer connection pool avoids SQLITE_BUSY under concurrency.
type SQLiteStore struct {
	// unexported fields: db *sql.DB
}

// OpenSQLite opens or creates the SQLite database at path (or ":memory:"), applies PRAGMAs, and executes idempotent DDL migrations.
// Pre-conditions: path must be non-empty; parent directory must exist or be creatable when file-backed.
// Post-conditions: Returns initialized *SQLiteStore with recipes_ledger, feed_cursors, runs tables and indexes ready.
func OpenSQLite(ctx context.Context, path string) (*SQLiteStore, error)
```

### 2.2 Behavioral Contracts

1. **DDL Schema:** Exact tables (`recipes_ledger`, `feed_cursors`, `runs`) and indexes (`idx_ledger_state_retry`, `idx_ledger_domain_state`) defined in Section 4.6 of `docs/ARCHITECTURE.md`.
2. **`OpenSQLite` Connection Configuration:** Uses `sql.Open("sqlite", dsn)` with `_pragma=journal_mode(WAL)`, `_pragma=busy_timeout(5000)`, `_pragma=foreign_keys(ON)`; configures `db.SetMaxOpenConns(1)` for file-backed SQLite to prevent concurrent-writer lock contention across worker goroutines.
3. **`UpsertDiscovered`:**
   - Normalizes `rec.CanonicalURL` and validates `rec.Domain`.
   - If `canonical_url` (or an existing row sharing the same `DedupeKey`) already exists, returns `(false, nil)` without overwriting terminal states (`imported`, `skipped_*`, `failed_permanent`) or resetting retry counters! (Wait: optionally fills blank `cuisine`/`title` if still `discovered`).
   - Otherwise inserts with `state = 'discovered'` and returns `(true, nil)`.
4. **`ReconcileMealieOrgURLs`:**
   - Iterates `orgURLToSlug` inside a single SQL transaction.
   - Normalizes each `orgURL` and matches existing ledger rows via exact `canonical_url` or `DedupeKey`.
   - If matched in state `discovered` or `failed_retryable`, updates `state = 'imported'` and `mealie_slug`.
   - If absent from ledger, inserts a row with `state = 'imported'`, `discovery_source = DiscoveryMealieSync` (`'mealie_sync'`), `mealie_slug`.
5. **`ListPendingByDomain`:**
   - Returns records matching `domain` where `state = 'discovered'` OR (`state = 'failed_retryable'` AND `next_retry_at <= now`), ordered with `discovered` prioritized over retries and oldest `discovered_at` first, bounded by `limit`.
6. **`MarkFailed`:**
   - Increments `attempts`. If `permanent` is true OR `attempts >= maxAttempts`, sets `state = 'failed_permanent'` and clears `next_retry_at`. Otherwise sets `state = 'failed_retryable'` and `next_retry_at = ComputeNextRetry(now, attempts)`.

---

## 3. Edge Cases & Test Plan (`internal/ledger/sqlite_test.go`)

1. Open `:memory:` and `t.TempDir()/state.db` cleanly; verify reopening an existing DB is idempotent.
2. `UpsertDiscovered` skips duplicates (including `www.` / trailing-slash equivalent URLs via `DedupeKey`) and never clobbers `imported` or `skipped_non_recipe` rows.
3. `ReconcileMealieOrgURLs` imports pre-existing Mealie URLs in one transaction, prevents re-discovery from marking them pending, and skips already-imported rows cleanly.
4. `MarkFailed` increments `attempts`, schedules `next_retry_at`, excludes items before `next_retry_at` in `ListPendingByDomain`, returns them after `next_retry_at`, and promotes to `failed_permanent` at `maxAttempts`.
5. `SaveFeedCursor` / `GetFeedCursor` round-trip ETag, Last-Modified, and UTC timestamps; `RecordRunStart` / `RecordRunFinish` / `GetStatusSummary` aggregate counts accurately across states and domains.
6. Concurrent goroutine access under `-race` executes without `SQLITE_BUSY` or data races.

---

## 4. Verification Command

```bash
go test -v -race -cover ./internal/ledger
```
