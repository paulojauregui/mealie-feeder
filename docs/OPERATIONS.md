# Operations & Maintenance Runbook

This runbook documents day-to-day manual execution, ledger hygiene, source vetting, and homelab Mealie verification for `mealie-feeder`.

---

## 1. Prerequisites & Authentication

### 1.1 Required Environment Variables
| Variable | Source | Notes |
| :--- | :--- | :--- |
| `MEALIE_URL` | Shell env or `--mealie-url` | Set to `https://mealie.paulojauregui.com` (trailing slash stripped automatically). |
| `MEALIE_API_TOKEN` | Doppler project `k8s-eso`, config `dev` | Scoped Mealie API bearer token. Never commit or print raw token values. |
| `MEALIE_FEEDER_DB` | `--db` flag (default `./mealie-feeder.db`) | Persistent local SQLite ledger tracking `imported`, `discovered`, `skipped_*`, and `failed_*` states. |

### 1.2 Verify Token & Endpoint Connectivity
```bash
MEALIE_URL="https://mealie.paulojauregui.com" doppler run --project k8s-eso --config dev -- \
  sh -c 'curl -fsS -o /dev/null -w "HTTP %{http_code}\n" -H "Authorization: Bearer $MEALIE_API_TOKEN" "$MEALIE_URL/api/users/self"'
```
Expected: `HTTP 200`.

### 1.3 CLI Flag Ordering
Global logging and context timeout flags **must precede** the subcommand (`seed`, `sync`, `inspect`, `status`, `version`):
```bash
go run ./cmd/mealie-feeder -log-format=text -log-level=info -timeout=25m <subcommand> [subcommand flags]
```
*Note: `-timeout` defaults to `25m0s`. Extend it (e.g. `-timeout=45m`) if running large multi-domain sync batches.*

---

## 2. Standard Manual Workflows

### 2.1 Fast RSS Catch-Up (`~1–2 min`)
Most curated food blogs publish only 1–4 new recipes per week on RSS feeds (`recipetineats.com`, `budgetbytes.com`, `sallysbakingaddiction.com`, `onceuponachef.com`, `cookieandkate.com`, `minimalistbaker.com`, `bbcgoodfood.com`, `bonappetit.com`). Run RSS-only catch-up periodically without traversing sitemaps:

```bash
cd /Users/paulojauregui/projects/mealie-feeder
MEALIE_URL="https://mealie.paulojauregui.com" doppler run --project k8s-eso --config dev -- \
  go run ./cmd/mealie-feeder sync \
    --sources=./configs/sources.yaml \
    --db=./mealie-feeder.db \
    --include-sitemaps=false \
    --max-per-run=150 \
    --workers=4 \
    --json
```

### 2.2 Targeted Single-Domain Sitemap Sync
Run bounded sitemap batches on one domain at a time using `--domain` and `--max-per-run`:

```bash
MEALIE_URL="https://mealie.paulojauregui.com" doppler run --project k8s-eso --config dev -- \
  go run ./cmd/mealie-feeder sync \
    --sources=./configs/sources.yaml \
    --db=./mealie-feeder.db \
    --domain=seriouseats.com \
    --max-per-run=25 \
    --workers=1 \
    --json
```

**Operational caveats on large sitemaps (before running `--include-sitemaps=true`):**
1. **`bbcgoodfood.com`, `bonappetit.com`, `epicurious.com`:** Expose ~100 monthly/quarterly child sitemaps walked sequentially at 2.5s intervals (~4 minutes per domain purely for discovery). Prefer RSS-only catch-up or curated additions on these publishers.
2. **`allrecipes.com`:** Exposes **40,074** URLs in `sitemap_1.xml`. Calling `UpsertDiscovered` individually across all 40,074 rows scans `WHERE domain = 'allrecipes.com'` when checking `DedupeKey` aliases for previously unseen URLs (`O(N^2)`), exceeding the default `25m` `-timeout` before reaching ingestion. Use RSS or targeted seeds unless `UpsertDiscoveredBatch` is implemented (see [ADR 0002](adr/0002-sqlite-ledger-concurrency-and-seed-scoping.md)).

### 2.3 Idempotent Seed Ingestion
Re-running `seed` against any manifest is safe and skips `imported` / `skipped_*` records via canonical URL + `DedupeKey` normalization, while retrying eligible `failed_retryable` items:

```bash
MEALIE_URL="https://mealie.paulojauregui.com" doppler run --project k8s-eso --config dev -- \
  go run ./cmd/mealie-feeder seed \
    --sources=./configs/sources.yaml \
    --seeds=./seeds/greatest-hits-vol4.yaml \
    --db=./mealie-feeder.db \
    --workers=4 \
    --json
```

---

## 3. SQLite Ledger Maintenance & Queries

`mealie-feeder.db` uses SQLite in WAL mode (`*.db-shm` and `*.db-wal` are gitignored).

### 3.1 Inspect Ledger Health & Checkpoint WAL
```bash
sqlite3 ./mealie-feeder.db '
  PRAGMA wal_checkpoint(TRUNCATE);
  SELECT state, count(*) FROM recipes_ledger GROUP BY state;
  SELECT domain, count(*) FROM recipes_ledger WHERE state = "failed_retryable" GROUP BY domain;
  SELECT run_id, command, started_at, finished_at, status, imported_count, failed_count FROM runs ORDER BY rowid DESC LIMIT 10;
'
```

### 3.2 Back Up Local Ledger Before Major Changes
```bash
sqlite3 ./mealie-feeder.db ".backup '/tmp/mealie-feeder-backup-$(date +%Y%m%d).db'"
```
*Note: Mealie itself does not enforce `org_url` uniqueness in PostgreSQL (`RecipeModel` only enforces `(slug, group_id)` uniqueness). Preserving `./mealie-feeder.db` prevents re-importing identical recipes with `-1` suffixes across manual sessions; if the local DB is ever lost, `mealie-feeder status --reconcile` or any `seed`/`sync` run rebuilds `imported` state from Mealie `GET /api/recipes`.*

---

## 4. Onboarding & Vetting New Source Domains

Never enable a new source domain without verifying both `mealie-feeder` preflight AND Mealie `v3.25.1`'s internal `RecipeScraperPackage` parser. Upstream `recipe-scrapers` can silently return empty ingredient arrays or `0` instructions (`thespruceeats.com` and `smittenkitchen.com` remain disabled for this exact reason).

1. **Check `robots.txt` and Sitemaps:** Confirm `User-agent: *` permits crawling recipe permalinks and inspect exact URL shapes (e.g., `patijinich.com` permits underscores `_`; `nyonyacooking.com` appends `~<id>`).
2. **Test Local Preflight (`uTLS chrome_auto`):**
   ```bash
   go run ./cmd/mealie-feeder inspect --sources ./configs/sources.yaml --url "<candidate-recipe-url>" --json
   ```
   Verify `http_status: 200`, `preflight.Verdict: "valid"`, `IngredientCount >= 1`, `InstructionCount >= 1`.
3. **Test Live Homelab Mealie Parser End-to-End (`/api/recipes/test-scrape-url`):**
   ```bash
   doppler run --project k8s-eso --config dev -- python3 -c '
   import os, json, subprocess
   tok = os.environ["MEALIE_API_TOKEN"]
   u = "<candidate-recipe-url>"
   d = json.loads(subprocess.check_output([
       "curl","-sS","-m","20","-H",f"Authorization: Bearer {tok}",
       "-H","Content-Type: application/json","-d",json.dumps({"url":u}),
       "https://mealie.paulojauregui.com/api/recipes/test-scrape-url"
   ]))
   print("name:", d.get("name"), "ings:", len(d.get("recipeIngredient") or []), "steps:", len(d.get("recipeInstructions") or []))
   '
   ```
   Confirm both `ings > 0` and `steps > 0`.
4. **Update Registry & Contract Tests:** Add the source entry to [`configs/sources.yaml`](../configs/sources.yaml) and update the enabled/total assertions in [`internal/config/starter_assets_test.go`](../internal/config/starter_assets_test.go) and [`internal/config/loader_test.go`](../internal/config/loader_test.go).
5. **Run Full Verification Gate:**
   ```bash
   CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test -count=1 -race -cover ./...
   ```

---

## 5. Homelab Mealie Postgres & PVC Integrity Audit

Run this read-only audit against namespace `mealie` to confirm database completeness and PVC headroom:

```bash
kubectl --context homelab -n mealie exec mealie-db-1 -c postgres -- psql -U postgres -d mealie \
  -c "SELECT count(*) AS total_recipes FROM recipes;" \
  -c "SELECT count(*) AS missing_ingredients FROM recipes r WHERE NOT EXISTS (SELECT 1 FROM recipes_ingredients ri WHERE ri.recipe_id = r.id);" \
  -c "SELECT count(*) AS missing_steps FROM recipes r WHERE NOT EXISTS (SELECT 1 FROM recipe_instructions ins WHERE ins.recipe_id = r.id);" \
  -c "SELECT pg_size_pretty(pg_database_size('mealie')) AS db_size;"
kubectl --context homelab -n mealie exec deploy/mealie -- sh -c 'df -h /app/data && du -sh /app/data && ls /app/data/recipes | wc -l'
```

Expected healthy invariants (`2026-11-16` baseline): `total_recipes` matches `/app/data/recipes` count (`1,293`), `missing_ingredients = 0`, `missing_steps = 0`, `/app/data` ~`492M` (`10Gi` PVC), Postgres DB ~`34 MB` (`5Gi` CNPG PVC).

---

## 6. Re-Enabling Automated Kubernetes Scheduling (Deferred)

Kubernetes manifests under [`deploy/k8s/`](../deploy/k8s/) (`pvc.yaml`, `externalsecret.yaml`, `cronjob.yaml`) remain tested in-repo against `ghcr.io/paulojauregui/mealie-feeder` (`linux/amd64`, non-root UID `10001:10001`). If automated weekly/monthly RSS runs become desirable later:
1. Seed `/data/mealie-feeder.db` on `mealie-feeder-ledger-pvc` or allow initial run to reconcile via `GET /api/recipes`.
2. Promote manifests into `paulojauregui/k8s-gitops` under `apps/base/mealie/` or `apps/overlays/home/mealie/` with `--include-sitemaps=false` on the `CronJob` command for predictable sub-2-minute runs.
