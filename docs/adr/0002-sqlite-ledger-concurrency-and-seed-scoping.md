# 0002. SQLite Ledger Connection Discipline, `org_url` Reconciliation, and Seed Scoping

* **Status**: Accepted
* **Date**: 2026-11-16
* **Scope**: `internal/ledger`, `internal/feeder`, Deduplication, Concurrency

---

## 1. Context

Two database behaviors dictated the design of `mealie-feeder`'s state layer:

1. **Mealie PostgreSQL does not deduplicate by `org_url`:** `RecipeModel` in Mealie enforces uniqueness only on `(slug, group_id)`. Submitting an identical URL twice via `create/html-or-json` silently creates `-1` slug duplicates rather than rejecting or returning existing records.
2. **Idempotent multi-batch operation requires durable client-side state:** Curated seeds (`Vol 1`, `Vol 2`, `Vol 4`) and manual `sync` batches share domains (e.g., `seriouseats.com`, `recipetineats.com`, `simplyrecipes.com`). Re-running commands must skip already-imported recipes, obey retry backoffs on transient errors, and reconcile URLs already present in Mealie.
3. **Production failure observed during Volume 4 seeding (PR #6 root cause):**
   - `ledger.OpenSQLite` pins `db.SetMaxOpenConns(1)` to avoid `SQLITE_BUSY` across worker goroutines.
   - `UpsertDiscovered` previously used `defer rows.Close()` across early `break` exits when scanning domain rows for `DedupeKey` aliases, then executed `s.db.ExecContext(ctx, UPDATE ...)` to backfill empty `title`/`cuisine` metadata on sitemap-discovered rows—causing an immediate self-deadlock on the sole pooled connection (`25m` hang at `recipetineats.com/teriyaki-chicken-meatballs/`).
   - Simultaneously, `Engine.Seed` previously invoked `ListPendingByDomain`, which ordered all `discovered` rows for a domain oldest-first and would have drained ~11,860 earlier unimported sitemap URLs ahead of the targeted seed manifest.

---

## 3. Decision

1. **Single-File Pure-Go SQLite Ledger (`modernc.org/sqlite`, WAL mode):** Tracks canonicalized URLs (`NormalizeURL` + trailing-slash/`www.`-insensitive `DedupeKey`) across 6 states (`discovered`, `imported`, `skipped_non_recipe`, `skipped_paywall`, `failed_retryable`, `failed_permanent`) plus `feed_cursors` and `runs` telemetry.
2. **Live Mealie Inventory Reconciliation (`reconcileMealieInventory`):** At the start of `seed` and `sync`, fetches paginated `GET /api/recipes` summaries (`Slug`, `OrgURL`) and reconciles matching URLs into `StateImported` inside a single SQLite transaction (`ReconcileMealieOrgURLs`).
3. **Explicit Cursor Closure & Indexed Fast Path (`findMatchingRecipeLocked`, PR #6 `6b8fd84`):**
   - Queries `WHERE canonical_url = ?` via `QueryRowContext` (indexed lookup) first.
   - Only falls back to domain-scoped `DedupeKey` scanning on `sql.ErrNoRows`, explicitly calling `_ = rows.Close()` before returning and before any follow-up `UPDATE` or `INSERT`.
   - Shared across both `UpsertDiscovered` and `GetByCanonicalURL` so `DedupeKey` alias matching is identical in both read and write paths.
4. **Scoped `Seed` Target Selection (`Engine.Seed`, PR #6 `6b8fd84`):** Per-domain workers iterate strictly over `seedsByDomain[dom]` using `GetByCanonicalURL` rather than `ListPendingByDomain`, reserving `ListPendingByDomain` exclusively for `Engine.Sync`.
5. **Read-Only `--dry-run` Guarantees:** `Engine.Sync` skips persisting `feed_cursors` (`SaveFeedCursor`) during `--dry-run` (`325fac0`) so simulation never advances RSS watermarks.

---

## 4. Consequences

* **Positive:** Volume 4 `seed` (`360` recipes overlapping ~12,000 existing sitemap rows across `19` domains) dropped from a `25m` self-deadlock to **`3m49s` live import (`360/360` imported, `0` failures)** and `9.887s` `--dry-run`.
* **Positive:** Verified by unit regression tests `TestUpsertDiscoveredBackfillNoDeadlock` and `TestEngineSeedIgnoresUnrelatedSitemapDiscovered` under `-race`.
* **Known Future Optimization (`O(N^2)` discovery upserts):** When `sync` discovers tens of thousands of *unseen* URLs in a single sitemap (observed on `allrecipes.com`: `40,074` URLs), `UpsertDiscovered` still falls back from `canonical_url` miss to scanning `WHERE domain = ?` per URL (`~25m` for `40k` items). Implementing a transactional `UpsertDiscoveredBatch` loading existing domain keys once per sitemap batch remains a clean future improvement if bulk sitemap ingestion of `allrecipes.com` is later needed.
