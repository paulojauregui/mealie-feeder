# 0004. Operate via Manual Doppler Batches and Defer Kubernetes `CronJob` Rollout

* **Status**: Accepted
* **Date**: 2026-11-16
* **Scope**: Deployment Topology, Scheduling, Operational Overhead

---

## 1. Context

Phase 2 delivered container builds (`ghcr.io/paulojauregui/mealie-feeder`) and ready-to-apply Kubernetes manifests under [`deploy/k8s/`](../../deploy/k8s/) (`pvc.yaml`, `externalsecret.yaml`, `cronjob.yaml`) designed for recurring execution in the homelab `mealie` namespace.

Before promoting the `CronJob` into `paulojauregui/k8s-gitops`, we inspected live RSS `<pubDate>` publication cadence (`2026-11-16`) across core sources and completed full multi-volume seeding plus manual `sync` batches:

* **Slow upstream blog cadence:** Premier sources publish ~1–4 new recipes per week (`recipetineats.com`: 4 recent items / ~2–3 wk; `sallysbakingaddiction.com`: 8 / ~1–2 wk; `onceuponachef.com`: 10 / ~1–2 wk; `minimalistbaker.com`: 4 / ~1 mo; `cookieandkate.com`: 10 / ~1 wk; `budgetbytes.com`: 10 / ~3–4 wk).
* **Large initial corpus already achieved:** Combining curated Vol 1 (`320`), Vol 2 (`320`), manual Vol 3 RSS/sitemap batches (`293`), and Vol 4 (`360`) populated **1,293** validated recipes (`492M` `/app/data`, `34 MB` Postgres).

---

## 2. Considered Alternatives

| Alternative | Operational Profile | Verdict |
| :--- | :--- | :--- |
| Deploy daily/weekly Kubernetes `CronJob` immediately | Continuous background scraping, PVC/ExternalSecret lifecycle management, ArgoCD monitoring, API token resident in cluster | **Deferred:** High churn relative to ~1–4/wk new blog posts when a 1,293-recipe library already covers practical needs for months/years. |
| On-demand CLI execution (`doppler run` + `./mealie-feeder.db`) | Zero idle cluster footprint; instant operator-triggered RSS catch-up (`~1–2 min`) when new seasonal recipes are desired | **Adopted:** Matches real publication velocity and simplifies secret/state custody. |

---

## 3. Decision

1. **Do not deploy `deploy/k8s/` to `paulojauregui/k8s-gitops` at this time.** Keep Mealie (`mealie-db` + `mealie` app) strictly focused on serving and backing up the populated `1,293`-recipe catalog.
2. **Operate `mealie-feeder` on demand from workstation** using Doppler (`k8s-eso/dev` `MEALIE_API_TOKEN`) and the local SQLite ledger (`./mealie-feeder.db`).
3. **Preserve `deploy/k8s/` and container CI builds** in `mealie-feeder` so automated scheduled syncs (`--include-sitemaps=false`) can be enabled cleanly in GitOps if future requirements change (e.g., following frequent seasonal newsletters or integrating a user-facing ingest webhook).

---

## 4. Consequences

* **Positive:** Zero ongoing external scraping noise against partner blogs, zero cluster CronJob alert/maintenance overhead, and deterministic manual visibility into every imported batch.
* **Positive:** Mealie media (`/app/data` `492M` inside `10Gi` PVC) and PostgreSQL (`34 MB` inside `5Gi` CNPG PVC backed up nightly by `CronJob/cnpg-cluster-backup`) remain compact and stable.
* **Tradeoff:** New blog recipes published after `2026-11-16` do not appear automatically in Mealie until an operator runs `mealie-feeder sync --include-sitemaps=false` (or imports individual URLs via Mealie UI).
* **Tradeoff:** The canonical SQLite ledger (`mealie-feeder.db`) resides locally on the workstation rather than on a cluster PVC; if moved or replaced, run `mealie-feeder status --reconcile` against `GET /api/recipes` to rebuild `imported` inventory state cleanly.
