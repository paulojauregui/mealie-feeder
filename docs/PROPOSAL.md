# Architectural Brief & Phase 1 Proposal: `mealie-feeder`

## 1. Context & Empirical Constraints (Mealie v3.25.1)

Live inspection of our homelab Mealie `v3.25.1` instance established the exact server-side contract `mealie-feeder` must respect:

1. **No built-in recipe seeder or RSS ingestion:** `/api/groups/seeders/{foods,labels,units}` only seeds reference tables, not recipes, and Mealie has no RSS/Atom polling endpoints.
2. **`POST /api/recipes/create/url/bulk` drops organizers and lacks backpressure:**
   - `RecipeBulkScraperService` calls `create_from_html()` with `include_tags=False` and `include_categories=False` hardcoded, discarding JSON-LD `keywords` and `recipeCategory`.
   - It spawns background tasks under `asyncio.Semaphore(3)` without per-domain pacing or LLM fallback circuit-breaking.
3. **DGX Spark fallback & OpenGraph stub hazard (`RecipeScraper`):**
   - When `RecipeScraperPackage` (`recipe_scrapers`, 725 named sites + wild mode) cannot extract ingredients/instructions (e.g., on listicles, gear reviews, or blog index pages), Mealie falls back to `RecipeScraperOpenAI` (remote vLLM on DGX Spark, ~90-180s per call) and, on failure, falls through to `RecipeScraperOpenGraph`, which persists stub recipes containing `"Could not detect ingredients"`.
   - Therefore, `mealie-feeder` must perform **deterministic Schema.org `@type: "Recipe"` preflighting before calling Mealie** on automated crawls.
4. **`POST /api/recipes/create/html-or-json` avoids double fetching:**
   - Passing `{ "data": <rawHTML>, "url": <canonicalURL>, "includeTags": true, "includeCategories": true }` skips Mealie's `safe_scrape_html()` HTTP fetch while still executing the full `recipe_scrapers` cleaner, tag/category resolver, and image downloader (`finalize_scraped_recipe`).
5. **No `orgURL` deduplication in Mealie:**
   - `RecipeModel` enforces uniqueness only on `(slug, group_id)`. Re-submitting an existing `orgURL` causes `RecipeRepository.create()` to generate `Name (1)` .. `Name (10)` duplicates.
   - `mealie-feeder` must sync existing `orgURL` values via paginated `GET /api/recipes` (`RecipeSummary.orgURL`) and maintain a persistent SQLite ledger with canonical URL normalization.
6. **Cloudflare TLS fingerprinting (`JA3`/`JA4`) on Dotdash Meredith sitemaps & pages:**
   - Plain `curl`/`net/http` receives `HTTP 403` (`cf-mitigated: challenge`) on `seriouseats.com/sitemap_1.xml` and `allrecipes.com/sitemap_1.xml`, whereas browser-TLS impersonation resolves `HTTP 200`.
   - Note: `agent-workshop` runs behind `isolate-agent-workshop` (`CiliumNetworkPolicy` blocking RFC1918 and `mealie` cluster endpoints), so runtime execution belongs on the operator workstation (CLI seed) and a Kubernetes `CronJob` inside namespace `mealie` targeting `http://mealie.mealie.svc.cluster.local:9000`.

---

## 2. Approved Starter Domains & Ingestion Strategy

### Approved 16 Starter Domains (High-Signal Subset of `recipe_scrapers`)
* **WordPress / Yoast Sitemap (`post-sitemap*.xml`) + Working RSS `/feed/`:**
  1. `recipetineats.com`
  2. `budgetbytes.com`
  3. `sallysbakingaddiction.com`
  4. `onceuponachef.com`
  5. `minimalistbaker.com`
  6. `cookieandkate.com`
  7. `smittenkitchen.com`
  8. `loveandlemons.com`
* **Publisher Path-Filtered Sitemaps (+ Selective RSS):**
  9. `bonappetit.com` (allow path prefix `^/recipe/`; RSS `https://www.bonappetit.com/feed/rss`)
  10. `epicurious.com` (allow path prefix `^/recipe/`)
  11. `kingarthurbaking.com` (allow path prefix `^/recipes/`, excludes `/blog/` and `/author/`; sitemap-only since `/feed` is `404`)
  12. `bbcgoodfood.com` (prefer `YYYY-QN-recipe.xml` sitemaps, deny `^/premium/` paywalled paths; RSS `https://www.bbcgoodfood.com/feed`)
* **Dotdash Meredith (Cloudflare Browser-TLS Required + JSON-LD Preflight):**
  13. `seriouseats.com`
  14. `allrecipes.com`
  15. `simplyrecipes.com`
  16. `thespruceeats.com`

### Explicitly Excluded from Automated Crawling
* `foodnetwork.com` (`403` on `/robots.txt`), `food52.com` (`429`), `cooking.nytimes.com`, and `americastestkitchen.com` (subscriber paywalls on full catalogs).

### Prioritization & Phased Collection Sizing
* Prioritize curated "greatest hits" across diverse cuisines first (`seeds/greatest-hits.yaml`), supplemented by newest `<lastmod>` windows from sitemaps with configurable per-domain caps (`max_per_run`) and polite per-domain delays (`2s-3s` default).
* Incremental runs avoid hammering origin blogs and fit comfortably on Mealie's storage while PVC/DB headroom can later scale 5-7x in `k8s-gitops`.

---

## 3. Phase 1 Scope (This Milestone on `agent-workshop`)

**Deliverables ONLY for PR #1 (no Go source implementation yet):**
1. **`docs/ARCHITECTURE.md`:** Complete Go `1.27.4` system architecture, package DAG (`cmd/mealie-feeder`, `internal/*`), dependency choices (browser-TLS Go HTTP transport, pure-Go SQLite, `x/net/html`, YAML/XML/RSS parsing), SQLite schema, Mealie REST client contract, polite rate limiter & error/retry state machine, CLI commands (`seed`, `sync`, `inspect`, `status`), and Phase B Kubernetes `CronJob` deployment blueprint.
2. **`ROADMAP.md` & `docs/specs/M*_SPEC.md`:** Dynamic milestone decomposition sized strictly at `1-2 cohesive Go files + 1 *_test.go file` per spec for `@spark-coder` (`pre-conditions`, `post-conditions`, signatures, test plans).
3. **`seeds/greatest-hits.yaml`:** Curated starter collection targeting ~25-50 iconic, high-signal recipes per cuisine across diverse cuisines (~250-400+ total recipes) strictly from the 16 vetted starter domains, where every single URL has been **verified live** from `agent-workshop` to return `HTTP 200` and contain valid Schema.org `@type: "Recipe"` JSON-LD metadata without login walls or anti-bot challenges.
