package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// TLSMode enumerates supported outbound TLS fingerprinting profiles.
// Pre-conditions: Must match one of the enumerated string literals when configured.
// Post-conditions: Validated by SourceRegistry.Validate().
type TLSMode string

const (
	// TLSModeChromeAuto selects dynamic uTLS Chrome browser impersonation.
	// Pre-conditions: Used for TLS fingerprinting configuration.
	// Post-conditions: Represented as string literal "chrome_auto".
	TLSModeChromeAuto TLSMode = "chrome_auto"

	// TLSModeSafariAuto selects dynamic uTLS Safari browser impersonation.
	// Pre-conditions: Used for TLS fingerprinting configuration.
	// Post-conditions: Represented as string literal "safari_auto".
	TLSModeSafariAuto TLSMode = "safari_auto"

	// TLSModePlain selects standard Go crypto/tls without browser fingerprinting.
	// Pre-conditions: Used for TLS fingerprinting configuration.
	// Post-conditions: Represented as string literal "plain".
	TLSModePlain TLSMode = "plain"
)

// SchemaMode controls which Recipe markup detectors satisfy preflight validation.
// Pre-conditions: Must match one of the enumerated string literals when configured.
// Post-conditions: Validated by SourceRegistry.Validate().
type SchemaMode string

const (
	// SchemaModeJSONLD requires valid schema.org/Recipe JSON-LD script blocks.
	// Pre-conditions: Used for recipe metadata extraction mode.
	// Post-conditions: Represented as string literal "jsonld".
	SchemaModeJSONLD SchemaMode = "jsonld"

	// SchemaModeMicrodata requires valid microdata Recipe itemscopes.
	// Pre-conditions: Used for recipe metadata extraction mode.
	// Post-conditions: Represented as string literal "microdata".
	SchemaModeMicrodata SchemaMode = "microdata"

	// SchemaModeAuto allows either JSON-LD or microdata to satisfy validation.
	// Pre-conditions: Used for recipe metadata extraction mode.
	// Post-conditions: Represented as string literal "auto".
	SchemaModeAuto SchemaMode = "auto"
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

// NormalizeDomain canonicalizes a domain string by trimming whitespace, lowercasing,
// removing schemes (http://, https://), paths, ports, and optional "www." prefix.
// Pre-conditions: Input string may be a raw domain, hostname, or full URL.
// Post-conditions: Returns lowercase normalized host string without "www." prefix or trailing slashes.
func NormalizeDomain(domain string) string {
	d := strings.TrimSpace(domain)
	if d == "" {
		return ""
	}
	d = strings.ToLower(d)
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	if idx := strings.Index(d, "/"); idx != -1 {
		d = d[:idx]
	}
	if host, _, err := net.SplitHostPort(d); err == nil {
		d = host
	}
	d = strings.TrimPrefix(d, "www.")
	d = strings.TrimSuffix(d, ".")
	return strings.TrimSpace(d)
}

// ApplyDefaults populates zero-valued fields on SourceRegistry.Defaults and propagates defaults into Sources.
// Pre-conditions: r must be non-nil.
// Post-conditions: Every SourceConfig has non-empty TLSMode, SchemaMode, CanonicalHost (defaults to Domain), MinInterval > 0, and Timeout > 0.
func (r *SourceRegistry) ApplyDefaults() {
	if r == nil {
		return
	}
	if r.Defaults.TLSMode == "" {
		r.Defaults.TLSMode = TLSModeChromeAuto
	}
	if r.Defaults.SchemaMode == "" {
		r.Defaults.SchemaMode = SchemaModeJSONLD
	}
	if r.Defaults.MinInterval == 0 {
		r.Defaults.MinInterval = 2 * time.Second
	}
	if r.Defaults.Timeout == 0 {
		r.Defaults.Timeout = 20 * time.Second
	}
	if r.Defaults.MaxRetries == 0 {
		r.Defaults.MaxRetries = 2
	}
	if r.Defaults.MaxPerRun == 0 {
		r.Defaults.MaxPerRun = 25
	}

	for i := range r.Sources {
		s := &r.Sources[i]
		if s.CanonicalHost == "" {
			s.CanonicalHost = s.Domain
		}
		if s.TLSMode == "" {
			s.TLSMode = r.Defaults.TLSMode
		}
		if s.SchemaMode == "" {
			s.SchemaMode = r.Defaults.SchemaMode
		}
		if s.MinInterval == 0 {
			s.MinInterval = r.Defaults.MinInterval
		}
		if s.Timeout == 0 {
			s.Timeout = r.Defaults.Timeout
		}
		if s.MaxRetries == 0 {
			s.MaxRetries = r.Defaults.MaxRetries
		}
		if s.MaxPerRun == 0 {
			s.MaxPerRun = r.Defaults.MaxPerRun
		}
	}
}

// Validate checks structural, regex, domain-uniqueness, and exclusion invariants across the registry.
// Pre-conditions: ApplyDefaults should be called prior to Validate.
// Post-conditions: Returns nil iff Version == 1, all modes/URLs/regexes compile cleanly, domains are unique, and no enabled source matches ExcludedDomains.
func (r *SourceRegistry) Validate() error {
	if r == nil {
		return errors.New("source registry is nil")
	}
	if r.Version != 1 {
		return fmt.Errorf("unsupported source registry version %d: expected 1", r.Version)
	}

	switch r.Defaults.TLSMode {
	case TLSModeChromeAuto, TLSModeSafariAuto, TLSModePlain, "":
	default:
		return fmt.Errorf("defaults has invalid tls_mode: %q", r.Defaults.TLSMode)
	}

	switch r.Defaults.SchemaMode {
	case SchemaModeJSONLD, SchemaModeMicrodata, SchemaModeAuto, "":
	default:
		return fmt.Errorf("defaults has invalid schema_mode: %q", r.Defaults.SchemaMode)
	}

	if r.Defaults.MinInterval < 0 {
		return fmt.Errorf("defaults min_interval must be non-negative, got %v", r.Defaults.MinInterval)
	}
	if r.Defaults.Timeout < 0 {
		return fmt.Errorf("defaults timeout must be non-negative, got %v", r.Defaults.Timeout)
	}
	if r.Defaults.MaxRetries < 0 {
		return fmt.Errorf("defaults max_retries must be non-negative, got %d", r.Defaults.MaxRetries)
	}
	if r.Defaults.MaxPerRun < 0 {
		return fmt.Errorf("defaults max_per_run must be non-negative, got %d", r.Defaults.MaxPerRun)
	}

	excludedMap := make(map[string]ExcludedDomain, len(r.ExcludedDomains))
	for i, ex := range r.ExcludedDomains {
		normEx := NormalizeDomain(ex.Domain)
		if normEx == "" {
			return fmt.Errorf("excluded domain [%d] has empty domain", i)
		}
		if strings.TrimSpace(ex.Reason) == "" {
			return fmt.Errorf("excluded domain %q must have non-empty reason", ex.Domain)
		}
		excludedMap[normEx] = ex
	}

	seenDomains := make(map[string]string, len(r.Sources))
	for i, s := range r.Sources {
		if strings.TrimSpace(s.Domain) == "" {
			return fmt.Errorf("source [%d] has empty domain", i)
		}
		normDom := NormalizeDomain(s.Domain)
		if orig, exists := seenDomains[normDom]; exists {
			return fmt.Errorf("duplicate source domain %q (normalized %q already defined as %q)", s.Domain, normDom, orig)
		}
		seenDomains[normDom] = s.Domain

		switch s.TLSMode {
		case TLSModeChromeAuto, TLSModeSafariAuto, TLSModePlain:
		default:
			return fmt.Errorf("source %q has invalid tls_mode: %q", s.Domain, s.TLSMode)
		}

		switch s.SchemaMode {
		case SchemaModeJSONLD, SchemaModeMicrodata, SchemaModeAuto:
		default:
			return fmt.Errorf("source %q has invalid schema_mode: %q", s.Domain, s.SchemaMode)
		}

		if s.Enabled {
			if ex, isEx := excludedMap[normDom]; isEx {
				return fmt.Errorf("source %q is enabled but domain is excluded: %s", s.Domain, ex.Reason)
			}
			if len(s.SitemapURLs) == 0 && len(s.RSSURLs) == 0 {
				return fmt.Errorf("source %q is enabled but defines no sitemap_urls or rss_urls", s.Domain)
			}
			if s.MaxPerRun <= 0 {
				return fmt.Errorf("source %q is enabled but max_per_run must be > 0, got %d", s.Domain, s.MaxPerRun)
			}
			if s.MinInterval <= 0 {
				return fmt.Errorf("source %q is enabled but min_interval must be > 0, got %v", s.Domain, s.MinInterval)
			}
			if s.Timeout <= 0 {
				return fmt.Errorf("source %q is enabled but timeout must be > 0, got %v", s.Domain, s.Timeout)
			}
		} else {
			if s.MinInterval < 0 {
				return fmt.Errorf("source %q min_interval must be non-negative, got %v", s.Domain, s.MinInterval)
			}
			if s.Timeout < 0 {
				return fmt.Errorf("source %q timeout must be non-negative, got %v", s.Domain, s.Timeout)
			}
			if s.MaxRetries < 0 {
				return fmt.Errorf("source %q max_retries must be non-negative, got %d", s.Domain, s.MaxRetries)
			}
			if s.MaxPerRun < 0 {
				return fmt.Errorf("source %q max_per_run must be non-negative, got %d", s.Domain, s.MaxPerRun)
			}
		}

		for _, pat := range s.AllowPatterns {
			if _, err := regexp.Compile(pat); err != nil {
				return fmt.Errorf("source %q invalid allow_pattern %q: %w", s.Domain, pat, err)
			}
		}
		for _, pat := range s.DenyPatterns {
			if _, err := regexp.Compile(pat); err != nil {
				return fmt.Errorf("source %q invalid deny_pattern %q: %w", s.Domain, pat, err)
			}
		}
		for _, pat := range s.SitemapIncludePatterns {
			if _, err := regexp.Compile(pat); err != nil {
				return fmt.Errorf("source %q invalid sitemap_include_pattern %q: %w", s.Domain, pat, err)
			}
		}

		for _, u := range s.SitemapURLs {
			if err := validateHTTPURL(u); err != nil {
				return fmt.Errorf("source %q invalid sitemap_url: %w", s.Domain, err)
			}
		}
		for _, u := range s.RSSURLs {
			if err := validateHTTPURL(u); err != nil {
				return fmt.Errorf("source %q invalid rss_url: %w", s.Domain, err)
			}
		}
	}

	return nil
}

// SourceByDomain returns a configured source matching domain (case-insensitive, ignoring optional "www." prefix).
// Pre-conditions: domain is non-empty.
// Post-conditions: Returns matched SourceConfig and true, or zero value and false.
func (r *SourceRegistry) SourceByDomain(domain string) (SourceConfig, bool) {
	if r == nil {
		return SourceConfig{}, false
	}
	target := NormalizeDomain(domain)
	if target == "" {
		return SourceConfig{}, false
	}
	for _, s := range r.Sources {
		if NormalizeDomain(s.Domain) == target || (s.CanonicalHost != "" && NormalizeDomain(s.CanonicalHost) == target) {
			return s, true
		}
	}
	return SourceConfig{}, false
}

// IsExcluded checks whether domain appears in ExcludedDomains (case-insensitive, ignoring optional "www." prefix).
// Pre-conditions: domain is non-empty.
// Post-conditions: Returns matching ExcludedDomain and true when excluded.
func (r *SourceRegistry) IsExcluded(domain string) (ExcludedDomain, bool) {
	if r == nil {
		return ExcludedDomain{}, false
	}
	target := NormalizeDomain(domain)
	if target == "" {
		return ExcludedDomain{}, false
	}
	for _, ex := range r.ExcludedDomains {
		if NormalizeDomain(ex.Domain) == target {
			return ex, true
		}
	}
	return ExcludedDomain{}, false
}

// Validate checks SeedManifest schema invariants, URL syntax, domain host agreement, and URL uniqueness.
// Pre-conditions: m must be non-nil.
// Post-conditions: Returns nil iff Version == 1, Recipes is non-empty, every URL is http/https matching its Entry.Domain, and no duplicate URLs exist.
func (m *SeedManifest) Validate() error {
	if m == nil {
		return errors.New("seed manifest is nil")
	}
	if m.Version != 1 {
		return fmt.Errorf("unsupported seed manifest version %d: expected 1", m.Version)
	}
	if len(m.Recipes) == 0 {
		return errors.New("seed manifest contains zero recipes")
	}

	seenURLs := make(map[string]int, len(m.Recipes))
	for i, recipe := range m.Recipes {
		cleanURL := strings.TrimSpace(recipe.URL)
		if cleanURL == "" {
			return fmt.Errorf("recipe [%d]: empty url", i)
		}
		if strings.TrimSpace(recipe.Title) == "" {
			return fmt.Errorf("recipe [%d] (%s): empty title", i, cleanURL)
		}
		if strings.TrimSpace(recipe.Domain) == "" {
			return fmt.Errorf("recipe [%d] (%s): empty domain", i, cleanURL)
		}
		if strings.TrimSpace(recipe.Cuisine) == "" {
			return fmt.Errorf("recipe [%d] (%s): empty cuisine", i, cleanURL)
		}

		parsed, err := url.ParseRequestURI(cleanURL)
		if err != nil {
			return fmt.Errorf("recipe [%d] (%s): malformed url: %w", i, cleanURL, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("recipe [%d] (%s): scheme must be http or https, got %q", i, cleanURL, parsed.Scheme)
		}
		if parsed.Host == "" {
			return fmt.Errorf("recipe [%d] (%s): empty host in url", i, cleanURL)
		}

		normDomain := NormalizeDomain(recipe.Domain)
		normURLHost := NormalizeDomain(parsed.Hostname())
		if normDomain != normURLHost {
			return fmt.Errorf("recipe [%d] (%s): domain %q (normalized %q) does not match URL host %q (normalized %q)",
				i, cleanURL, recipe.Domain, normDomain, parsed.Hostname(), normURLHost)
		}

		if prevIdx, exists := seenURLs[cleanURL]; exists {
			return fmt.Errorf("recipe [%d]: duplicate url %q (already defined at index %d)", i, cleanURL, prevIdx)
		}
		seenURLs[cleanURL] = i
	}

	return nil
}

// ResolveEnv inspects the runtime environment and populates an EnvConfig instance.
// Pre-conditions: None. Reads environment variables MEALIE_URL, MEALIE_API_TOKEN,
// MEALIE_DB_PATH, MEALIE_SOURCES_PATH, and MEALIE_SEEDS_PATH.
// Post-conditions: Returns an EnvConfig with defaults applied for unset or empty paths.
func ResolveEnv() EnvConfig {
	cfg := EnvConfig{
		MealieURL:      os.Getenv("MEALIE_URL"),
		MealieAPIToken: os.Getenv("MEALIE_API_TOKEN"),
		DBPath:         os.Getenv("MEALIE_DB_PATH"),
		SourcesPath:    os.Getenv("MEALIE_SOURCES_PATH"),
		SeedsPath:      os.Getenv("MEALIE_SEEDS_PATH"),
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "data/feeder.db"
	}
	if cfg.SourcesPath == "" {
		cfg.SourcesPath = "configs/sources.yaml"
	}
	if cfg.SeedsPath == "" {
		cfg.SeedsPath = "seeds/greatest-hits.yaml"
	}
	return cfg
}

func validateHTTPURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errors.New("empty URL")
	}
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil {
		return fmt.Errorf("malformed URL %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL %q scheme must be http or https, got %q", raw, parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("URL %q has empty host", raw)
	}
	return nil
}
