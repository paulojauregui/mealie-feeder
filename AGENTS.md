# AGENTS.md — Mealie Feeder Engineering Standards

> **For Coding Agents and Engineers:** Read this document before authoring specifications, implementation code, or pull requests in `paulojauregui/mealie-feeder`.

---

## 1. Project Mission & Scope

`mealie-feeder` is a deterministic, stateful Go CLI and Kubernetes `CronJob` orchestrator that populates and maintains a self-hosted **Mealie (`v3.25.1`)** recipe collection without re-implementing recipe parsing:

1. **Discover** recipe permalinks from curated seed lists, XML sitemaps (including sitemap indexes and `.xml.gz`), and RSS/Atom feeds.
2. **Deduplicate & Track State** locally in SQLite synced with Mealie's existing `GET /api/recipes` (`orgURL`) inventory.
3. **Pre-flight Check Once** using browser-TLS impersonation (`uTLS` in Go) and local `<script type="application/ld+json">` (`@type: "Recipe"`) verification so non-recipe articles, gadget reviews, and paywalls never trigger unnecessary LLM fallback or OpenGraph stub junk inside Mealie.
4. **Ingest Cleanly** via `POST /api/recipes/create/html-or-json` (passing already-fetched raw HTML + canonical `url` with `includeTags: true, includeCategories: true`).

---

## 2. Language & Runtime Standards (Go-First)

* **Language & Toolchain:** Go (`1.27.4` matching CI and `agent-workshop`), compiled with `CGO_ENABLED=0`.
* **Container Target:** Static binary packaged in `gcr.io/distroless/static-debian12:nonroot` (`USER 10001:10001`).
* **UI/UX Exception:** This repository builds a CLI / scheduled batch worker (`CronJob`), not a web frontend service. Omit `@ux-architect` and `@product-designer` web-UI workflows unless a web UI is explicitly requested in a future milestone.

---

## 3. Architectural Invariants

### Rule 1: Strict Modularity & Layout
* CLI entrypoint under `cmd/mealie-feeder/main.go`.
* Private packages strictly encapsulated under `internal/<package>/` with explicit module imports (`github.com/paulojauregui/mealie-feeder/internal/...`).
* Small, single-purpose packages forming an acyclic dependency graph.

### Rule 2: Deterministic Dependency Management
* `go.mod` and `go.sum` maintained via `go mod tidy` and `go mod verify`.
* Prefer pure-Go dependencies (`CGO_ENABLED=0` compatible, including pure-Go SQLite if SQLite is used) so multi-stage builds stay tiny and hermetic.

### Rule 3: Design by Contract (DbC) & Static Type Safety
* Every exported struct, interface, constant group, function, and method must include a structured Go doc comment (`// SymbolName ...`) explicitly documenting `Pre-conditions:` and `Post-conditions:`.
* Explicit types and small consumer-defined interfaces; zero unchecked `any` type assertions.
* Zero emojis anywhere in source code, comments, specs, YAML files, commit messages, or PR descriptions.

### Rule 4: Fast, Deterministic Unit Tests (Zero Live Network Dependencies)
* Unit tests must execute deterministically offline in sub-second time via `net/http/httptest` servers and mocked interfaces (`go test -v -race -cover ./...`).
* Never depend on live food blogs or a running Mealie server inside unit tests.

### Rule 5: Milestone Sizing for `@spark-coder`
* Every `docs/specs/M*_SPEC.md` must target **1–2 cohesive Go implementation files + 1 `*_test.go` file**, containing explicit struct/interface definitions, function signatures, `Pre-conditions` / `Post-conditions`, and a single deterministic verification command.

---

## 4. Verification Commands

```bash
test -z "$(gofmt -l .)"
go mod verify
go vet ./...
go test -v -race -cover ./...
```

---

## 5. GitHub Bot Authorship & Branch Protocol

* **Autonomous Identity:** Commits and PRs from `agent-workshop` use `tron-agent-mainframe[bot]` (`5533130+tron-agent-mainframe[bot]@users.noreply.github.com`) with `export GH_TOKEN=$(/home/tron/.local/bin/tron-github-token)`.
* **Branching Invariant:** Never push directly to `main` and never self-merge PRs. Create feature branches (`feat/...`, `docs/...`) and open a Pull Request for maintainer review (`HowDiggy`).
* **Anti-Challenge Rule:** Never write code or workflows to solve or bypass CAPTCHAs, proof-of-work gates, or anti-bot challenges. If a domain or URL presents a paywall or challenge gate, record/skip it cleanly.
