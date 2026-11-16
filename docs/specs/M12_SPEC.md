# M12 Specification: End-to-End Integration Suite & Distroless Container

## 1. Objective & Target Files

Deliver an offline end-to-end integration test suite (`internal/feeder/e2e_test.go`) exercising the entire `seed` -> `sync` -> SQLite ledger -> `preflight` -> Mealie `v3.25.1` pipeline over `httptest` servers and real `:memory:`/`t.TempDir()` SQLite, alongside the multi-stage non-root `Dockerfile`.

**Target Files (strict scope):**
1. `internal/feeder/e2e_test.go`
2. `Dockerfile`

---

## 2. End-to-End Test Matrix (`internal/feeder/e2e_test.go`)

Wire real `config.SourceRegistry`, `config.SeedManifest`, `ledger.OpenSQLite`, `fetch.NewClientWithTransport`, `discover`, `preflight`, `mealie.NewHTTPClient`, and `feeder.NewEngine` together without mocks for core logic:

1. **Mock Origin Food Blogs (`httptest.NewTLSServer`):**
   - `/robots.txt` pointing to `/sitemap_index.xml` and `/feed/`.
   - `/sitemap_index.xml` linking to `/post-sitemap.xml` and `/non-recipe-sitemap.xml` (filtered via `sitemap_include_patterns`).
   - `/feed/` RSS 2.0 feed containing 1 new valid JSON-LD recipe and 1 roundup article.
   - Recipe endpoints covering:
     - Valid `@graph` JSON-LD recipe (`200 OK`)
     - Valid Microdata recipe (`200 OK`, `schema_mode: microdata`)
     - Non-recipe roundup HTML (`200 OK`, no Recipe schema -> `skipped_non_recipe`)
     - Paywalled/challenged endpoint (`403` + `cf-mitigated: challenge` -> `skipped_paywall`, zero retries)
     - Transient `429` with `Retry-After: 0` succeeding on attempt 2
     - Pre-existing Mealie recipe URL already returned by `GET /api/recipes`
2. **Mock Mealie `v3.25.1` Server (`httptest.NewServer`):**
   - Enforces `Authorization: Bearer test-token`.
   - `GET /api/app/about` returns `{"version":"v3.25.1","production":true}`.
   - `GET /api/recipes` serves paginated `RecipeSummary` list and records every created recipe so subsequent runs return updated inventory.
   - `POST /api/recipes/create/html-or-json` asserts `includeTags == true`, `includeCategories == true`, non-empty `data` HTML, and returns HTTP `201` with JSON slug.
   - `GET /api/recipes/{slug}` returns populated ingredients/instructions (or simulated OpenGraph stub on stub-guard test).
3. **Assertions:**
   - Run 1 (`Seed` + `Sync`) imports exact expected new recipes (`0` duplicate imports of pre-existing `orgURL`), marks roundup `skipped_non_recipe`, marks challenge `skipped_paywall`.
   - Run 2 (idempotent `Sync` immediately following Run 1) triggers **zero** origin recipe fetches and **zero** `POST /api/recipes/create/html-or-json` calls.

---

## 3. Container Specification (`Dockerfile`)

```dockerfile
# Stage 1: Static Go 1.27.4 Builder
FROM golang:1.27.4-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY configs/ ./configs/
COPY seeds/ ./seeds/
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -ldflags="-s -w -X main.BuildVersion=${VERSION} -X main.BuildCommit=${COMMIT} -X main.BuildDate=${DATE}" \
    -o /out/mealie-feeder ./cmd/mealie-feeder

# Stage 2: Minimal Non-Root Distroless Runtime
FROM gcr.io/distroless/static-debian12:nonroot
USER 10001:10001
WORKDIR /app
COPY --from=builder /out/mealie-feeder /usr/local/bin/mealie-feeder
COPY --from=builder /src/configs/sources.yaml /app/configs/sources.yaml
COPY --from=builder /src/seeds/greatest-hits.yaml /app/seeds/greatest-hits.yaml
ENV MEALIE_FEEDER_SOURCES=/app/configs/sources.yaml \
    MEALIE_FEEDER_SEEDS=/app/seeds/greatest-hits.yaml \
    MEALIE_FEEDER_DB=/var/lib/mealie-feeder/state.db
ENTRYPOINT ["/usr/local/bin/mealie-feeder"]
CMD ["sync"]
```

---

## 4. Verification Command

```bash
CGO_ENABLED=0 go test -v -race -cover ./...
```
