# M02 Specification: Config Loaders, Environment Resolution & Starter Registry

## 1. Objective & Target Files

Implement deterministic YAML loaders and environment variable resolution in `internal/config`, and materialize the vetted 16-domain starter registry `configs/sources.yaml`.

**Target Files (strict scope):**
1. `internal/config/loader.go`
2. `configs/sources.yaml`
3. `internal/config/loader_test.go`

---

## 2. Package Contract (`package config`)

### 2.1 Exported Constants & Functions

```go
package config

// Default runtime path and environment variable names.
// Pre-conditions: Constant identifiers are immutable package defaults.
// Post-conditions: Used uniformly by ResolveEnv and CLI flag defaults.
const (
	DefaultDBPath      = "./mealie-feeder.db"
	DefaultSourcesPath = "./configs/sources.yaml"
	DefaultSeedsPath   = "./seeds/greatest-hits.yaml"

	EnvMealieURL      = "MEALIE_URL"
	EnvMealieAPIToken = "MEALIE_API_TOKEN"
	EnvDBPath         = "MEALIE_FEEDER_DB"
	EnvSourcesPath    = "MEALIE_FEEDER_SOURCES"
	EnvSeedsPath      = "MEALIE_FEEDER_SEEDS"
)

// LoadSources reads, parses with strict unknown-field checking, applies defaults, and validates a SourceRegistry YAML file.
// Pre-conditions: path must point to a readable YAML file conforming to SourceRegistry.
// Post-conditions: Returns a validated *SourceRegistry or a descriptive error naming the failing path/field.
func LoadSources(path string) (*SourceRegistry, error)

// LoadSeeds reads, parses with strict unknown-field checking, and validates a SeedManifest YAML file.
// Pre-conditions: path must point to a readable YAML file conforming to SeedManifest.
// Post-conditions: Returns a validated *SeedManifest or a descriptive error.
func LoadSeeds(path string) (*SeedManifest, error)

// ResolveEnv builds an EnvConfig from an injectable lookup function (e.g., os.LookupEnv), applying defaults for omitted paths.
// Pre-conditions: lookup must be non-nil.
// Post-conditions: DBPath, SourcesPath, and SeedsPath are always non-empty; MealieURL has any trailing slash trimmed.
func ResolveEnv(lookup func(string) (string, bool)) EnvConfig

// ResolveEnvFromOS resolves runtime configuration from process environment variables via os.LookupEnv.
// Pre-conditions: None.
// Post-conditions: Equivalent to ResolveEnv(os.LookupEnv).
func ResolveEnvFromOS() EnvConfig
```

### 2.2 Production Registry Asset (`configs/sources.yaml`)

Must match Section 5 of `docs/ARCHITECTURE.md` verbatim:
- `version: 1`
- `excluded_domains`: `foodnetwork.com`, `food52.com`, `cooking.nytimes.com`, `americastestkitchen.com`
- `sources`: All 16 empirically verified starter domains (`recipetineats.com`, `budgetbytes.com`, `sallysbakingaddiction.com`, `onceuponachef.com`, `minimalistbaker.com`, `cookieandkate.com`, `smittenkitchen.com`, `loveandlemons.com`, `bonappetit.com`, `epicurious.com`, `kingarthurbaking.com`, `bbcgoodfood.com`, `seriouseats.com`, `allrecipes.com`, `simplyrecipes.com`, `thespruceeats.com`).
- Unknown YAML fields must be rejected via `yaml.NewDecoder` with `dec.KnownFields(true)` so typos like `allow_pattern` (singular) fail fast.

---

## 3. Edge Cases & Test Plan (`internal/config/loader_test.go`)

1. `LoadSources` parses valid YAML into `*SourceRegistry`, applies defaults, and returns an error on missing file, malformed YAML, unknown YAML field, or failed `Validate()`.
2. `LoadSeeds` parses valid seed YAML, rejects unknown YAML fields, and returns an error on duplicate URLs.
3. `ResolveEnv` uses defaults when env vars are unset or whitespace-only, trims trailing `/` from `MEALIE_URL`, and respects explicit overrides.
4. Regression test loading `../../configs/sources.yaml` (if present in repo tree) confirming `LoadSources` succeeds, lists 16 active sources, and lists 4 excluded domains.

---

## 4. Verification Command

```bash
go test -v -race -cover ./internal/config
```
