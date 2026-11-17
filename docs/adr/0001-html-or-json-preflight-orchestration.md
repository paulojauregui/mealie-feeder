# 0001. External `uTLS` Preflight and `POST /api/recipes/create/html-or-json` Orchestration

* **Status**: Accepted
* **Date**: 2026-11-15
* **Scope**: Ingestion Pipeline, TLS Transport, Mealie API Contract, Scraper Safety

---

## 1. Context

Homelab Mealie runs `v3.25.1` (`ghcr.io/mealie-recipes/mealie:v3.25.1`) paired with PostgreSQL (`mealie-db` via CloudNativePG) and an OpenAI-compatible vLLM backend (`nvidia/Qwen3.8-Flash-Next-FP4-Local` on `happy.tail749110.ts.net:8000`).

Direct inspection of Mealie's FastAPI routes (`recipe_crud_routes.py`, `recipe_bulk_scraper.py`, `recipe_scraper.py`, `scraper_strategies.py`, `cleaner.py`) surfaced several critical behaviors:

1. **`POST /api/recipes/create/url/bulk` drops taxonomy and hammers upstream origins:** Forces `includeTags: false` / `includeCategories: false` and executes 3x concurrent URL fetches inside the Mealie container.
2. **Mealie's internal `requests`/`httpx` client hits Cloudflare TLS fingerprinting (`JA3`) blocks** on Dotdash Meredith properties (`seriouseats.com`, `allrecipes.com`, `simplyrecipes.com`) and specialty publishers (`justonecookbook.com`), which reset or challenge standard TLS handshakes.
3. **LLM fallback cost & OpenGraph stub risk:** Submitting non-recipe roundups or pages with missing structured markup triggers fallback calls to the configured vLLM endpoint, and `RecipeScraperPackage.clean_scraper()` accepts partial scrapes with empty ingredients when `try_get_default` sees `value == []` rather than `value == [""]`.
4. **Silent upstream `recipe-scrapers` regressions:** Live inspection and upstream verification across both `v3.25.1` (`recipe-scrapers==15.12.0`) and `v3.31.0` (`recipe-scrapers==15.13.0`) confirmed that `thespruceeats.com` (`li.structured-ingredients__list-item p` returning `[]`) stores `0` ingredients and `smittenkitchen.com` parses `0` instruction steps.

---

## 2. Mental Model & Considered Alternatives

| Alternative | Mechanism | Verdict |
| :--- | :--- | :--- |
| Call `/api/recipes/create/url/bulk` | Mealie fetches URLs directly | **Rejected:** Drops tags/categories, trips Cloudflare `JA3` blocks, triggers unwanted LLM calls on non-recipes. |
| Fork Mealie or rewrite Python recipe parser in Go | Custom ingredient/instruction extractor | **Rejected:** High maintenance burden duplicating Mealie's unit/food normalization and image pipeline. |
| External Go `uTLS` preflight + `POST /api/recipes/create/html-or-json` | Fetch HTML once externally, validate Schema.org `Recipe` JSON-LD, post raw HTML to Mealie | **Adopted:** Single fetch per URL, keeps Mealie's unit/tag/image pipeline intact, protects vLLM from non-recipes. |

---

## 3. Decision

1. **`mealie-feeder` owns discovery, `robots.txt` enforcement, polite pacing, and HTTP retrieval** using `refraction-networking/utls` (`chrome_auto` fingerprint) and strict per-domain rate limits (`internal/fetch`).
2. **Mandatory Preflight Gate (`internal/preflight`):** Before calling Mealie, HTML is parsed locally to require a Schema.org `Recipe` block (`@type: "Recipe"`) with `len(ingredients) >= 1 && len(instructions) >= 1`; `IsOpenGraphStub` rejects records where *either* slice is empty (`||`, not `&&`).
3. **Single-Fetch Ingestion (`internal/mealie`):** Validated HTML and canonical URL are submitted to `POST /api/recipes/create/html-or-json` with `includeTags: true` and `includeCategories: true`, bypassing outbound fetches from inside `deploy/mealie`.
4. **Broken Upstream Adapters Disabled by Default:** `smittenkitchen.com` and `thespruceeats.com` are defined with `enabled: false` and explicit schema notes in [`configs/sources.yaml`](../../configs/sources.yaml), while paywalled/bot-gated domains (`foodnetwork.com`, `food52.com`, `cooking.nytimes.com`, `americastestkitchen.com`) remain in `excluded_domains`.

---

## 4. Consequences

* **Positive:** Clean separation between network evasion/rate-limiting (`mealie-feeder`) and culinary persistence/unit normalization (`Mealie`), achieving `0` missing-ingredient and `0` missing-step rows across `1,293` live imports.
* **Positive:** Context lifetime in `internal/fetch/client.go` must span body reads (`defer cancel()` after `readBoundedBody`), verified via PR #3 (`325fac0`) regression tests.
* **Caveat:** `MEALIE_CREATE_HTML` triggers synchronous server-side hero image download inside `deploy/mealie`; slow external image CDNs occasionally cause `Client.Timeout` (`failed_retryable`), resolved automatically on subsequent runs.
