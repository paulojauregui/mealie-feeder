# M01 Specification: Go Module & Configuration Schemas

## 1. Objective & Target Files

Establish the Go `1.27.4` module manifest (`CGO_ENABLED=0` compatible dependencies) and define the core configuration schemas, defaults resolution, and structural invariant checks in `internal/config`.

**Target Files (strict scope):**
1. `go.mod`
2. `internal/config/config.go`
3. `internal/config/config_test.go`

---

## 2. Dependency Manifest (`go.mod`)

Initialize module `github.com/paulojauregui/mealie-feeder` targeting `go 1.27.4` with pinned verified dependencies:
- `github.com/refraction-networking/utls v1.8.2`
- `modernc.org/sqlite v1.62.0`
- `golang.org/x/net v0.62.0`
- `gopkg.in/yaml.v3 v3.0.1`
- `github.com/mmcdole/gofeed v1.5.0`
- `golang.org/x/sync v0.25.0`
- `golang.org/x/time v0.16.0`

---

## 3. Package Contract (`package config`)

### 3.1 Exported Constants & Types

```go
package config

import "time"

// TLSMode enumerates supported outbound TLS fingerprinting profiles.
// Pre-conditions: Must match one of the enumerated string literals when configured.
// Post-conditions: Validated by SourceRegistry.Validate().
type TLSMode string

const (
	TLSModeChromeAuto TLSMode = "chrome_auto"
	TLSModeSafariAuto TLSMode = "safari_auto"
	TLSModePlain      TLSMode = "plain"
)

// SchemaMode controls which Recipe markup detectors satisfy preflight validation.
// Pre-conditions: Must match one of the enumerated string literals when configured.
// Post-conditions: Validated by SourceRegistry.Validate().
type SchemaMode string

const (
	SchemaModeJSONLD    SchemaMode = "jsonld"
	SchemaModeMicrodata SchemaMode = "microdata"
	SchemaModeAuto      SchemaMode = "auto"
)

// DefaultsConfig defines fallback operational bounds inherited by sources that omit overrides.
// Pre-conditions: Durations must be non-negative; MaxRetries >= 0; MaxPerRun >= 0.
// Post-conditions: Zero-value fields on SourceConfig inherit matching values during ApplyDefaults.
type DefaultsConfig struct {
	TLSMode          TLSMode       `yaml:"tls_mode"`
	SchemaMode       SchemaMode    `yaml:"schema_mode"`
	MinInterval      time.Duration `yaml:"min_interval"`
	Timeout          time.Duration `yaml:"timeout"`
	MaxRetries       int           `yaml:"max_retries"`
	MaxPerRun        int           `yaml:"max_per_run"`
	AllowURLFallback bool          `yaml:"allow_url_fallback"`
}

// ExcludedDomain documents an explicitly prohibited host and rationale.
// Pre-conditions: Domain and Reason must be non-empty.
// Post-conditions: Prevents accidental re-addition of excluded domains into active Sources.
type ExcludedDomain struct {
	Domain string `yaml:"domain"`
	Reason string `yaml:"reason"`
}

// SourceConfig configures discovery, rate limiting, TLS, and filtering for a single recipe domain.
// Pre-conditions: Domain must be lowercase apex domain without scheme; regex patterns must compile.
// Post-conditions: Provides deterministic per-domain crawler parameters after ApplyDefaults and Validate.
type SourceConfig struct {
	Domain                 string        `yaml:"domain"`
	CanonicalHost          string        `yaml:"canonical_host"`
	Enabled                bool          `yaml:"enabled"`
	Tier                   string        `yaml:"tier"`
	TLSMode                TLSMode       `yaml:"tls_mode"`
	SchemaMode             SchemaMode    `yaml:"schema_mode"`
	MinInterval            time.Duration `yaml:"min_interval"`
	Timeout                time.Duration `yaml:"timeout"`
	MaxRetries             int           `yaml:"max_retries"`
	MaxPerRun              int           `yaml:"max_per_run"`
	AllowURLFallback       bool          `yaml:"allow_url_fallback"`
	PurgeStubs             bool          `yaml:"purge_stubs"`
	SitemapURLs            []string      `yaml:"sitemap_urls"`
	SitemapIncludePatterns []string      `yaml:"sitemap_include_patterns"`
	RSSURLs                []string      `yaml:"rss_urls"`
	AllowPatterns          []string      `yaml:"allow_patterns"`
	DenyPatterns           []string      `yaml:"deny_patterns"`
	Notes                  string        `yaml:"notes,omitempty"`
}

// SourceRegistry represents the root sources.yaml configuration document.
// Pre-conditions: Version must equal 1; Sources must contain unique domains disjoint from ExcludedDomains.
// Post-conditions: ApplyDefaults followed by Validate guarantees safe runtime execution across all packages.
type SourceRegistry struct {
	Version         int              `yaml:"version"`
	Defaults        DefaultsConfig   `yaml:"defaults"`
	ExcludedDomains []ExcludedDomain `yaml:"excluded_domains"`
	Sources         []SourceConfig   `yaml:"sources"`
}

// SeedEntry represents one curated recipe permalink in seeds/greatest-hits.yaml.
// Pre-conditions: URL must be http/https; Title, Domain, and Cuisine must be non-empty.
// Post-conditions: Normalized and verified by SeedManifest.Validate().
type SeedEntry struct {
	URL      string `yaml:"url"`
	Title    string `yaml:"title"`
	Domain   string `yaml:"domain"`
	Cuisine  string `yaml:"cuisine"`
	Category string `yaml:"category,omitempty"`
}

// SeedManifest represents the root seeds/greatest-hits.yaml document.
// Pre-conditions: Version must equal 1; Recipes must contain unique URLs.
// Post-conditions: Guarantees deduplicated, schema-valid seed metadata.
type SeedManifest struct {
	Version     int         `yaml:"version"`
	Description string      `yaml:"description"`
	Recipes     []SeedEntry `yaml:"recipes"`
}

// EnvConfig stores Twelve-Factor runtime settings resolved from environment variables.
// Pre-conditions: Populated by config.ResolveEnv or CLI overrides.
// Post-conditions: Read-only value object passed into orchestrator setup.
type EnvConfig struct {
	MealieURL      string
	MealieAPIToken string
	DBPath         string
	SourcesPath    string
	SeedsPath      string
}
```

### 3.2 Exported Methods & Invariants

```go
// ApplyDefaults populates zero-valued fields on SourceRegistry.Defaults and propagates defaults into Sources.
// Pre-conditions: r must be non-nil.
// Post-conditions: Every SourceConfig has non-empty TLSMode, SchemaMode, CanonicalHost (defaults to Domain), MinInterval > 0, and Timeout > 0.
func (r *SourceRegistry) ApplyDefaults()

// Validate checks structural, regex, domain-uniqueness, and exclusion invariants across the registry.
// Pre-conditions: ApplyDefaults should be called prior to Validate.
// Post-conditions: Returns nil iff Version == 1, all modes/URLs/regexes compile cleanly, domains are unique, and no enabled source matches ExcludedDomains.
func (r *SourceRegistry) Validate() error

// SourceByDomain returns a configured source matching domain (case-insensitive, ignoring optional "www." prefix).
// Pre-conditions: domain is non-empty.
// Post-conditions: Returns matched SourceConfig and true, or zero value and false.
func (r *SourceRegistry) SourceByDomain(domain string) (SourceConfig, bool)

// IsExcluded checks whether domain appears in ExcludedDomains (case-insensitive, ignoring optional "www." prefix).
// Pre-conditions: domain is non-empty.
// Post-conditions: Returns matching ExcludedDomain and true when excluded.
func (r *SourceRegistry) IsExcluded(domain string) (ExcludedDomain, bool)

// Validate checks SeedManifest schema invariants, URL syntax, domain host agreement, and URL uniqueness.
// Pre-conditions: m must be non-nil.
// Post-conditions: Returns nil iff Version == 1, Recipes is non-empty, every URL is http/https matching its Entry.Domain, and no duplicate URLs exist.
func (m *SeedManifest) Validate() error
```

---

## 4. Edge Cases & Test Plan (`internal/config/config_test.go`)

1. `ApplyDefaults` populates sensible fallback values (`chrome_auto`, `jsonld`, `2s` interval, `20s` timeout, `2` retries, `25` max_per_run) without overwriting explicit per-source overrides (`4s` on `smittenkitchen.com`, `microdata` schema mode).
2. `Validate` rejects:
   - Unsupported `Version` (`0` or `2`).
   - Duplicate `domain` entries in `Sources`.
   - Overlap between `ExcludedDomains` and `Sources` (e.g., `foodnetwork.com` listed as enabled source).
   - Malformed regex in `AllowPatterns`, `DenyPatterns`, or `SitemapIncludePatterns`.
   - Invalid `TLSMode` / `SchemaMode`, negative `MinInterval`, or invalid `SitemapURLs` / `RSSURLs`.
   - Enabled source with zero `SitemapURLs` AND zero `RSSURLs`, or `MaxPerRun <= 0`.
3. `SeedManifest.Validate` rejects duplicate URLs, mismatched `entry.Domain` vs URL host (e.g., `url` points to `example.com` while `domain` says `seriouseats.com`), missing `title` or `cuisine`, and non-http/https URLs.
4. `SourceByDomain` and `IsExcluded` handle `www.seriouseats.com` vs `seriouseats.com` and mixed case cleanly.

---

## 5. Verification Command

```bash
go test -v -race -cover ./internal/config
```
