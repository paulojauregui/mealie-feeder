package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Default runtime paths and environment variable names for the feeder CLI and CronJob.
// Pre-conditions: Values are immutable package-level defaults; callers must not shadow them with
// divergent literals in CLI flags or Kubernetes manifests.
// Post-conditions: ResolveEnv applies the path defaults when environment lookups are absent or
// whitespace-only, and every environment variable is read through the matching Env* constant.
const (
	// DefaultDBPath is the SQLite database path used when MEALIE_FEEDER_DB is unset.
	// Pre-conditions: No operator-supplied database path is present in the environment.
	// Post-conditions: Interpreted relative to the process working directory; "./mealie-feeder.db".
	DefaultDBPath = "./mealie-feeder.db"

	// DefaultSourcesPath is the SourceRegistry YAML path used when MEALIE_FEEDER_SOURCES is unset.
	// Pre-conditions: No operator-supplied sources path is present in the environment.
	// Post-conditions: Interpreted relative to the process working directory; "./configs/sources.yaml".
	DefaultSourcesPath = "./configs/sources.yaml"

	// DefaultSeedsPath is the SeedManifest YAML path used when MEALIE_FEEDER_SEEDS is unset.
	// Pre-conditions: No operator-supplied seeds path is present in the environment.
	// Post-conditions: Interpreted relative to the process working directory; "./seeds/greatest-hits.yaml".
	DefaultSeedsPath = "./seeds/greatest-hits.yaml"

	// EnvMealieURL is the environment key holding the base URL of the target Mealie instance.
	// Pre-conditions: Value, when set, is an absolute http/https URL.
	// Post-conditions: Read by ResolveEnv; surrounding whitespace and trailing slashes are trimmed.
	EnvMealieURL = "MEALIE_URL"

	// EnvMealieAPIToken is the environment key holding the Mealie long-lived API token.
	// Pre-conditions: Value, when set, is a runtime-injected bearer token never committed to the repo.
	// Post-conditions: Read by ResolveEnv and surfaced whitespace-trimmed in EnvConfig.MealieAPIToken.
	EnvMealieAPIToken = "MEALIE_API_TOKEN"

	// EnvDBPath is the environment key overriding DefaultDBPath.
	// Pre-conditions: Value, when set, is a writable filesystem path.
	// Post-conditions: Read by ResolveEnv; falls back to DefaultDBPath when unset or blank.
	EnvDBPath = "MEALIE_FEEDER_DB"

	// EnvSourcesPath is the environment key overriding DefaultSourcesPath.
	// Pre-conditions: Value, when set, is a readable YAML path conforming to SourceRegistry.
	// Post-conditions: Read by ResolveEnv; falls back to DefaultSourcesPath when unset or blank.
	EnvSourcesPath = "MEALIE_FEEDER_SOURCES"

	// EnvSeedsPath is the environment key overriding DefaultSeedsPath.
	// Pre-conditions: Value, when set, is a readable YAML path conforming to SeedManifest.
	// Post-conditions: Read by ResolveEnv; falls back to DefaultSeedsPath when unset or blank.
	EnvSeedsPath = "MEALIE_FEEDER_SEEDS"
)

// LoadSources reads, parses with strict unknown-field checking (dec.KnownFields(true)), applies
// defaults, and validates a SourceRegistry YAML file.
// Pre-conditions: path must point to a readable YAML file conforming to SourceRegistry and
// containing exactly one YAML document.
// Post-conditions: Returns a validated *SourceRegistry with defaults applied, or a descriptive
// error naming the failing path and, for structural faults, the offending field. Unknown keys
// (for example "allow_pattern" instead of "allow_patterns") fail fast instead of being ignored.
func LoadSources(path string) (*SourceRegistry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config: LoadSources requires a non-empty sources path")
	}

	registry := &SourceRegistry{}
	if err := decodeStrictYAML(path, registry); err != nil {
		return nil, fmt.Errorf("config: load sources file %q: %w", path, err)
	}

	registry.ApplyDefaults()
	if err := registry.Validate(); err != nil {
		return nil, fmt.Errorf("config: validate sources file %q: %w", path, err)
	}
	return registry, nil
}

// LoadSeeds reads, parses with strict unknown-field checking (dec.KnownFields(true)), and
// validates a SeedManifest YAML file.
// Pre-conditions: path must point to a readable YAML file conforming to SeedManifest and
// containing exactly one YAML document.
// Post-conditions: Returns a validated *SeedManifest (non-empty recipe set, unique URLs, and
// http/https URLs whose hosts match each entry domain) or a descriptive error naming the failing
// path and entry. Unknown keys fail fast instead of silently dropping curated metadata.
func LoadSeeds(path string) (*SeedManifest, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config: LoadSeeds requires a non-empty seeds path")
	}

	manifest := &SeedManifest{}
	if err := decodeStrictYAML(path, manifest); err != nil {
		return nil, fmt.Errorf("config: load seeds file %q: %w", path, err)
	}

	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("config: validate seeds file %q: %w", path, err)
	}
	return manifest, nil
}

// ResolveEnv builds an EnvConfig from an injectable lookup function (for example os.LookupEnv),
// applying package defaults for omitted or whitespace-only paths.
// Pre-conditions: lookup must be non-nil for real environment resolution; a nil lookup is treated
// as "every variable unset" (defensive DbC guard) so callers can never receive empty paths.
// Post-conditions: DBPath, SourcesPath, and SeedsPath are always non-empty; MealieURL has
// surrounding whitespace and any trailing slashes trimmed; every value is whitespace-trimmed so
// blank environment entries are indistinguishable from unset ones.
func ResolveEnv(lookup func(string) (string, bool)) EnvConfig {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}

	cfg := EnvConfig{
		MealieURL:      lookupTrimmed(lookup, EnvMealieURL),
		MealieAPIToken: lookupTrimmed(lookup, EnvMealieAPIToken),
		DBPath:         lookupTrimmed(lookup, EnvDBPath),
		SourcesPath:    lookupTrimmed(lookup, EnvSourcesPath),
		SeedsPath:      lookupTrimmed(lookup, EnvSeedsPath),
	}

	cfg.MealieURL = strings.TrimRight(cfg.MealieURL, "/")

	if cfg.DBPath == "" {
		cfg.DBPath = DefaultDBPath
	}
	if cfg.SourcesPath == "" {
		cfg.SourcesPath = DefaultSourcesPath
	}
	if cfg.SeedsPath == "" {
		cfg.SeedsPath = DefaultSeedsPath
	}
	return cfg
}

// ResolveEnvFromOS resolves runtime configuration from process environment variables via
// os.LookupEnv.
// Pre-conditions: None. Reads only the Env* variable names declared in this package.
// Post-conditions: Equivalent to ResolveEnv(os.LookupEnv); path fields are always non-empty and
// MealieURL is trailing-slash trimmed.
func ResolveEnvFromOS() EnvConfig {
	return ResolveEnv(os.LookupEnv)
}

// decodeStrictYAML opens path and decodes exactly one YAML document into target with unknown
// fields rejected.
// Pre-conditions: target must be a non-nil pointer to a struct; path must name a readable file.
// Post-conditions: Returns nil on success, or an error describing the read fault, the YAML
// syntax or unknown-field fault (carrying decoder line information), an empty document, or the
// presence of more than one YAML document.
func decodeStrictYAML(path string, target any) error {
	if target == nil {
		return errors.New("decode target is nil")
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	if err := dec.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("document is empty")
		}
		return fmt.Errorf("parse yaml: %w", err)
	}

	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("parse yaml after first document: %w", err)
		}
		if extra != nil {
			return errors.New("contains multiple YAML documents; expected exactly one")
		}
	}
	return nil
}

// lookupTrimmed returns the whitespace-trimmed value reported by lookup for key.
// Pre-conditions: lookup must be non-nil; key should be one of the Env* constants.
// Post-conditions: Returns "" when the key is absent, empty, or whitespace-only; otherwise the
// value with leading and trailing whitespace removed.
func lookupTrimmed(lookup func(string) (string, bool), key string) string {
	raw, ok := lookup(key)
	if !ok {
		return ""
	}
	return strings.TrimSpace(raw)
}
