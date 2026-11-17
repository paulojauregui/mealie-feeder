# 0003. Multi-Volume Curated Curation and Live Parser Vetting Across 28 Domains

* **Status**: Accepted
* **Date**: 2026-11-16
* **Scope**: `seeds/*.yaml`, `configs/sources.yaml`, Content Quality & Coverage

---

## 1. Context

Relying purely on generic XML sitemap and RSS ingestion produces skewed collections (heavy on recent seasonal posts, roundups, and mainstream American/European baking) while missing foundational dishes across global culinary traditions and major dining metropolises. At the same time, blind URL generation or unvetted source expansion risks hallucinated URLs, Cloudflare blocks, duplicate Condé Nast syndications, and broken `recipe-scrapers` adapters.

---

## 2. Evaluated Strategy & Empirical Checks

1. **Urban & Critical-Acclaim Gap Analysis:** Combined Wikimedia Michelin-starred restaurant breakdowns (New York, Singapore, London, California, global star-density capitals including Tokyo and Paris) and NYC DOHMH Open Data (`43nn-pn8j`) restaurant counts to identify high-signal gaps beyond the initial 10 culinary traditions (`Vol 1`).
2. **Pre-Onboarding End-to-End Source Gate:** Before adding 14 specialty culinary domains (`Group D` in `configs/sources.yaml`), every candidate domain was verified against:
   - `robots.txt` `User-agent: *` crawl permission and live XML sitemap layout,
   - `mealie-feeder inspect` (`uTLS chrome_auto` HTTP `200` + Schema.org `Recipe` JSON-LD),
   - Live Homelab Mealie `/api/recipes/test-scrape-url` parsing verifying non-empty `recipeIngredient` AND `recipeInstructions`.
3. **Condé Nast Cross-Syndication Deduplication:** Auditing Volume 2 PR #4 uncovered 15 exact duplicate recipes syndicated between `bonappetit.com` and `epicurious.com/recipes/food/views/ba-syn-*` (would generate `-1` duplicates in Mealie). `TestVol2SeedsContract` and `TestVol4SeedsContract` enforce zero `ba-syn` stripped-slug or title collisions with `bonappetit.com` across volumes.
4. **Manual Taxonomy & Domain Diversity Review:** Auditing all curated lists catches keyword-matcher false positives (e.g., Western potato salads in Japanese buckets or generic slow-cooker stews in Caribbean buckets) prior to merge.

---

## 3. Decision

Adopt an additive, contract-tested multi-volume catalog structure alongside the `28`-source enabled registry (`configs/sources.yaml`):

| Asset | Scope | Verified Count | Contract Test |
| :--- | :--- | :---: | :--- |
| [`seeds/greatest-hits.yaml`](../../seeds/greatest-hits.yaml) (**Vol 1**) | 10 core traditions (`32` each across `14` starter domains) | `320` | `TestStarterAssetsContract` |
| [`seeds/greatest-hits-vol2.yaml`](../../seeds/greatest-hits-vol2.yaml) (**Vol 2**) | 8 high-signal gaps (`korean`, `vietnamese`, `spanish`, `breakfast-brunch`, `soups-stews`, `grilling-bbq`, `weeknight-30min`, `drinks-cocktails`; `40` each) | `320` | `TestVol2SeedsContract` |
| [`seeds/greatest-hits-vol4.yaml`](../../seeds/greatest-hits-vol4.yaml) (**Vol 4**) | 10 Top-10 Global Metropolis & deep-cut Asian tracks (`36` each across `14` specialty + starter domains) | `360` | `TestVol4SeedsContract` |

Every volume enforces exact category quotas, enabled-source allow/deny regex compliance, emoji-free UTF-8 titles, zero Condé Nast `ba-syn` collisions, and zero exact/canonical URL overlap across prior volumes and active ledger exports.

---

## 4. Consequences

* **Positive:** Combined with targeted manual `sync` runs (`293` RSS/sitemap recipes), Mealie holds **1,293** verified recipes (`0` missing ingredients, `0` missing steps) spanning all 28 active domains (`justonecookbook.com`, `thewoksoflife.com`, `omnivorescookbook.com`, `madewithlau.com`, `maangchi.com`, `hot-thai-kitchen.com`, `nyonyacooking.com`, `kawalingpinoy.com`, `patijinich.com`, `eatperu.com`, `davidlebovitz.com`, `true-north-kitchen.com`, `africanbites.com`, `alicaspepperpot.com`, plus the 14 starter sites).
* **Tradeoff:** Adding new enabled domains to `configs/sources.yaml` requires explicitly updating the expected enabled/total source count assertions in `internal/config/starter_assets_test.go` and `internal/config/loader_test.go`.
* **Operational Note:** Because `seeds/*.yaml` are checked into git while Mealie receives page-native Schema.org categories (`includeCategories: true`), future curated volumes should follow the same live preflight + disjointness export pattern (`volN` -> contract test -> PR -> `seed`).
