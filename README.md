# mealie-feeder

A deterministic Go (`1.27.4`, `CGO_ENABLED=0`) CLI and Kubernetes `CronJob` orchestrator that populates and incrementally updates a self-hosted **Mealie (`v3.25.1+`)** instance from curated seed manifests, XML sitemaps, and RSS/Atom feeds—delegating recipe parsing, unit normalization, tagging, and image localization to Mealie itself.

## Architecture Overview

1. **Discover (`seed` & `sync`):** Reads curated seed YAML catalogs (`seeds/greatest-hits*.yaml`), `robots.txt`, XML sitemaps (`.xml`, `sitemap_index.xml`, `.xml.gz`), and conditional RSS/Atom feeds.
2. **Deduplicate (`ledger`):** Stores per-URL ingestion state (`discovered`, `imported`, `skipped_non_recipe`, `skipped_paywall`, `failed_retryable`, `failed_permanent`) in SQLite (`modernc.org/sqlite`, WAL mode) and reconciles live Mealie `org_url` inventory via `GET /api/recipes`.
3. **Single-Fetch Preflight (`preflight`):** Fetches each candidate once with browser-TLS impersonation (`uTLS` via `chrome_auto`), enforces `robots.txt` and per-domain pacing, and verifies Schema.org `@type: "Recipe"` (`RecipeIngredient` + `RecipeInstructions`) before contacting Mealie—eliminating duplicate fetches, open-graph stubs, and expensive LLM fallbacks on non-recipe articles.
4. **Ingest (`mealie`):** Posts the fetched HTML and canonical URL to `POST /api/recipes/create/html-or-json` with `includeTags: true` and `includeCategories: true`.

## Production Corpus & Status

* **Orchestrator & Container Image:** Implemented across `cmd/mealie-feeder` and `internal/{config,discover,feeder,fetch,ledger,mealie,preflight}`; published at `ghcr.io/paulojauregui/mealie-feeder`.
* **Populated Homelab Corpus (`https://mealie.paulojauregui.com`):** **1,293** recipes imported with `0` missing ingredients and `0` missing steps across **28** active enabled sources in [`configs/sources.yaml`](configs/sources.yaml) (`2` intentionally disabled sources, `4` excluded paywall/bot-gated domains):
  * [`seeds/greatest-hits.yaml`](seeds/greatest-hits.yaml) (**Vol 1**): `320` curated recipes across `10` core culinary traditions (`32` each).
  * [`seeds/greatest-hits-vol2.yaml`](seeds/greatest-hits-vol2.yaml) (**Vol 2**): `320` curated gap-fill recipes across `8` requested cuisines and categories (`40` each).
  * [`seeds/greatest-hits-vol4.yaml`](seeds/greatest-hits-vol4.yaml) (**Vol 4**): `360` curated recipes across `10` Top-10 Global Metropolis and deep-cut Asian tracks (`36` each).
  * **Targeted RSS & Sitemap Syncs (`Vol 3`):** `293` additional recipes captured from enabled feeds and sitemaps (`+1` pre-existing inventory reconciliation).
* **Execution Mode:** Operated on demand from workstation with Doppler-backed `MEALIE_API_TOKEN`; automated Kubernetes `CronJob` deployment ([`deploy/k8s/`](deploy/k8s/)) is preserved and intentionally deferred per [ADR 0004](docs/adr/0004-manual-batch-sync-over-k8s-cronjob.md).

## Documentation Index

* [Architecture Specification](docs/ARCHITECTURE.md) — System boundaries, package DAG, preflight rules, and Mealie API contract.
* [Day-2 Operations Runbook](docs/OPERATIONS.md) — Doppler auth, CLI commands, SQLite ledger checks, onboarding new domains, and Postgres/PVC audits.
* [Architecture Decision Records (ADRs)](docs/adr/README.md) — Rationale behind `html-or-json` orchestration, SQLite concurrency/scoping fixes, curated multi-volume expansion, and manual batch operation.
* [Phase 2 Engineering Roadmap](ROADMAP.md) & [Milestone Specifications](docs/specs/) — Original M01–M13 Design-by-Contract specifications.

## Quick Start

```bash
# Inspect a candidate URL before onboarding or importing
go run ./cmd/mealie-feeder inspect --sources ./configs/sources.yaml --url "https://www.seriouseats.com/classic-carbonara-recipe" --json

# Check local ledger and Mealie inventory counts
MEALIE_URL="https://mealie.paulojauregui.com" doppler run --project k8s-eso --config dev -- \
  go run ./cmd/mealie-feeder status --db ./mealie-feeder.db --reconcile --json

# Run a fast incremental RSS catch-up across enabled feeds
MEALIE_URL="https://mealie.paulojauregui.com" doppler run --project k8s-eso --config dev -- \
  go run ./cmd/mealie-feeder sync --sources ./configs/sources.yaml --db ./mealie-feeder.db --include-sitemaps=false --json
```
