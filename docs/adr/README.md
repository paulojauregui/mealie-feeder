# Architecture Decision Records (ADRs)

This directory captures durable architectural and engineering decisions for `mealie-feeder` alongside the system specification in [`docs/ARCHITECTURE.md`](../ARCHITECTURE.md) and operational playbook in [`docs/OPERATIONS.md`](../OPERATIONS.md).

---

## Index of Decisions

| ADR | Title | Date | Status | Scope |
| :--- | :--- | :--- | :--- | :--- |
| [0001](0001-html-or-json-preflight-orchestration.md) | External `uTLS` Preflight and `POST /api/recipes/create/html-or-json` Orchestration | 2026-11-15 | **Accepted** | Ingestion Pipeline & Scraper Safety |
| [0002](0002-sqlite-ledger-concurrency-and-seed-scoping.md) | SQLite Ledger Connection Discipline, `org_url` Reconciliation, and Seed Scoping | 2026-11-16 | **Accepted** | State Management & Reliability |
| [0003](0003-curated-multivolume-and-source-expansion.md) | Multi-Volume Curated Curation and Live Parser Vetting Across 28 Domains | 2026-11-16 | **Accepted** | Source Registry & Seed Catalogs |
| [0004](0004-manual-batch-sync-over-k8s-cronjob.md) | Operate via Manual Doppler Batches and Defer Kubernetes `CronJob` Rollout | 2026-11-16 | **Accepted** | Deployment & Operations |

---

## ADR Format

All ADRs follow a consistent four-part structure without emojis:
- **Context:** Observed problem, upstream constraints, and measurements.
- **Mental Model & Considered Alternatives:** First-principles evaluation of options.
- **Decision:** Explicit choice implemented in code, config, or operations.
- **Consequences:** Positive guarantees, tradeoffs, and open technical debt.
