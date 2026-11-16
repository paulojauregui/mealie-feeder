# mealie-feeder

A deterministic Go CLI and Kubernetes `CronJob` orchestrator that populates and continuously updates a self-hosted **Mealie (`v3.25.1+`)** instance from curated seed catalogs, XML sitemaps, and RSS/Atom feeds—delegating all recipe parsing, cleaning, tagging, and image processing to Mealie itself.

## Architecture Overview

1. **Discover (`Seed` & `Sync`):** Walks curated seed YAML manifests, `robots.txt` / XML sitemaps (`.xml`, `sitemap_index.xml`, `.xml.gz`), and RSS/Atom feeds.
2. **Deduplicate (`Ledger`):** Reconciles canonicalized `orgURL`s against `GET /api/recipes` and tracks per-URL ingestion state (`imported`, `skipped_non_recipe`, `failed_retryable`, `failed_permanent`) in SQLite.
3. **Single-Fetch Preflight (`Preflight`):** Fetches each candidate once with browser-TLS impersonation (`uTLS`) and validates `<script type="application/ld+json">` `@type: "Recipe"` before calling Mealie—avoiding duplicate fetches, expensive LLM fallbacks on non-recipe articles, and broken OpenGraph stubs.
4. **Ingest (`Mealie Client`):** Posts already-fetched HTML + canonical URL to `POST /api/recipes/create/html-or-json` with `includeTags: true` and `includeCategories: true`.

## Project Status

* **Phase 1 (Current):** Systems architecture (`docs/ARCHITECTURE.md`), milestone specifications (`docs/specs/M*_SPEC.md`), and curated multi-cuisine seed catalog (`seeds/greatest-hits.yaml`).
* **Phase 2 (Planned):** Go `1.27.4` implementation (`cmd/mealie-feeder`, `internal/*`), container packaging, initial greatest-hits backfill, and Kubernetes `CronJob` deployment in `k8s-gitops`.
