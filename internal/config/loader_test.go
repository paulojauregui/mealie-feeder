package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadSources(t *testing.T) {
	t.Parallel()

	t.Run("empty path fails", func(t *testing.T) {
		t.Parallel()
		reg, err := LoadSources("")
		if err == nil {
			t.Fatal("expected error for empty path, got nil")
		}
		if reg != nil {
			t.Errorf("expected nil registry, got %+v", reg)
		}
	})

	t.Run("missing file fails", func(t *testing.T) {
		t.Parallel()
		reg, err := LoadSources(filepath.Join(t.TempDir(), "nonexistent.yaml"))
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
		if reg != nil {
			t.Errorf("expected nil registry, got %+v", reg)
		}
	})

	t.Run("malformed yaml fails", func(t *testing.T) {
		t.Parallel()
		tmp := filepath.Join(t.TempDir(), "malformed.yaml")
		if err := os.WriteFile(tmp, []byte("version: [unclosed list"), 0o600); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}
		reg, err := LoadSources(tmp)
		if err == nil {
			t.Fatal("expected error for malformed yaml, got nil")
		}
		if reg != nil {
			t.Errorf("expected nil registry, got %+v", reg)
		}
	})

	t.Run("unknown field fails fast", func(t *testing.T) {
		t.Parallel()
		unknownYAML := `
version: 1
unknown_key: true
defaults:
  tls_mode: chrome_auto
`
		tmp := filepath.Join(t.TempDir(), "unknown.yaml")
		if err := os.WriteFile(tmp, []byte(unknownYAML), 0o600); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}
		reg, err := LoadSources(tmp)
		if err == nil {
			t.Fatal("expected error for unknown yaml field, got nil")
		}
		if !strings.Contains(err.Error(), "unknown_key") && !strings.Contains(err.Error(), "not found") {
			t.Logf("error output: %v", err)
		}
		if reg != nil {
			t.Errorf("expected nil registry, got %+v", reg)
		}
	})

	t.Run("invalid registry schema fails validation", func(t *testing.T) {
		t.Parallel()
		invalidYAML := `
version: 2
defaults:
  tls_mode: chrome_auto
sources: []
`
		tmp := filepath.Join(t.TempDir(), "invalid_version.yaml")
		if err := os.WriteFile(tmp, []byte(invalidYAML), 0o600); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}
		reg, err := LoadSources(tmp)
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if reg != nil {
			t.Errorf("expected nil registry, got %+v", reg)
		}
	})

	t.Run("valid sources yaml succeeds with defaults applied", func(t *testing.T) {
		t.Parallel()
		validYAML := `
version: 1
defaults:
  tls_mode: chrome_auto
  schema_mode: jsonld
  min_interval: 2s
  timeout: 15s
  max_retries: 3
  max_per_run: 30
sources:
  - domain: recipetineats.com
    enabled: true
    sitemap_urls:
      - https://www.recipetineats.com/sitemap_index.xml
`
		tmp := filepath.Join(t.TempDir(), "valid_sources.yaml")
		if err := os.WriteFile(tmp, []byte(validYAML), 0o600); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}
		reg, err := LoadSources(tmp)
		if err != nil {
			t.Fatalf("LoadSources failed: %v", err)
		}
		if reg == nil {
			t.Fatal("expected non-nil registry")
		}
		if len(reg.Sources) != 1 {
			t.Fatalf("expected 1 source, got %d", len(reg.Sources))
		}
		src := reg.Sources[0]
		if src.CanonicalHost != "recipetineats.com" {
			t.Errorf("CanonicalHost = %q, want recipetineats.com", src.CanonicalHost)
		}
		if src.TLSMode != TLSModeChromeAuto {
			t.Errorf("TLSMode = %q, want %q", src.TLSMode, TLSModeChromeAuto)
		}
		if src.Timeout != 15*time.Second {
			t.Errorf("Timeout = %v, want 15s", src.Timeout)
		}
	})
}

func TestLoadSeeds(t *testing.T) {
	t.Parallel()

	t.Run("empty path fails", func(t *testing.T) {
		t.Parallel()
		manifest, err := LoadSeeds("")
		if err == nil {
			t.Fatal("expected error for empty path, got nil")
		}
		if manifest != nil {
			t.Errorf("expected nil manifest, got %+v", manifest)
		}
	})

	t.Run("unknown field fails fast", func(t *testing.T) {
		t.Parallel()
		unknownYAML := `
version: 1
extra_field: false
description: test
recipes:
  - url: https://example.com/recipe
    title: Test
    domain: example.com
    cuisine: American
`
		tmp := filepath.Join(t.TempDir(), "unknown_seeds.yaml")
		if err := os.WriteFile(tmp, []byte(unknownYAML), 0o600); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}
		manifest, err := LoadSeeds(tmp)
		if err == nil {
			t.Fatal("expected error for unknown yaml field, got nil")
		}
		if manifest != nil {
			t.Errorf("expected nil manifest, got %+v", manifest)
		}
	})

	t.Run("duplicate url fails validation", func(t *testing.T) {
		t.Parallel()
		duplicateYAML := `
version: 1
description: test
recipes:
  - url: https://example.com/recipe-1
    title: Test 1
    domain: example.com
    cuisine: American
  - url: https://example.com/recipe-1
    title: Test 2
    domain: example.com
    cuisine: American
`
		tmp := filepath.Join(t.TempDir(), "duplicate_seeds.yaml")
		if err := os.WriteFile(tmp, []byte(duplicateYAML), 0o600); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}
		manifest, err := LoadSeeds(tmp)
		if err == nil {
			t.Fatal("expected error for duplicate URLs, got nil")
		}
		if manifest != nil {
			t.Errorf("expected nil manifest, got %+v", manifest)
		}
	})

	t.Run("valid seeds manifest loads cleanly", func(t *testing.T) {
		t.Parallel()
		validYAML := `
version: 1
description: Curated seed list
recipes:
  - url: https://www.recipetineats.com/chicken-curry/
    title: Chicken Curry
    domain: recipetineats.com
    cuisine: Indian
`
		tmp := filepath.Join(t.TempDir(), "valid_seeds.yaml")
		if err := os.WriteFile(tmp, []byte(validYAML), 0o600); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}
		manifest, err := LoadSeeds(tmp)
		if err != nil {
			t.Fatalf("LoadSeeds failed: %v", err)
		}
		if manifest == nil || len(manifest.Recipes) != 1 {
			t.Fatalf("expected 1 recipe, got %v", manifest)
		}
		if manifest.Recipes[0].Title != "Chicken Curry" {
			t.Errorf("Title = %q, want Chicken Curry", manifest.Recipes[0].Title)
		}
	})
}

func TestResolveEnvLookup(t *testing.T) {
	t.Parallel()

	t.Run("nil lookup uses defaults", func(t *testing.T) {
		t.Parallel()
		cfg := ResolveEnv(nil)
		if cfg.DBPath != DefaultDBPath {
			t.Errorf("DBPath = %q, want %q", cfg.DBPath, DefaultDBPath)
		}
		if cfg.SourcesPath != DefaultSourcesPath {
			t.Errorf("SourcesPath = %q, want %q", cfg.SourcesPath, DefaultSourcesPath)
		}
		if cfg.SeedsPath != DefaultSeedsPath {
			t.Errorf("SeedsPath = %q, want %q", cfg.SeedsPath, DefaultSeedsPath)
		}
		if cfg.MealieURL != "" {
			t.Errorf("MealieURL = %q, want empty", cfg.MealieURL)
		}
	})

	t.Run("custom map lookup with trailing slash trimmed", func(t *testing.T) {
		t.Parallel()
		envMap := map[string]string{
			EnvMealieURL:      "https://mealie.example.com///",
			EnvMealieAPIToken: "token-abc-123",
			EnvDBPath:         "/custom/path.db",
			EnvSourcesPath:    "/custom/sources.yaml",
			EnvSeedsPath:      "/custom/seeds.yaml",
		}
		lookup := func(k string) (string, bool) {
			v, ok := envMap[k]
			return v, ok
		}
		cfg := ResolveEnv(lookup)
		if cfg.MealieURL != "https://mealie.example.com" {
			t.Errorf("MealieURL = %q, want https://mealie.example.com", cfg.MealieURL)
		}
		if cfg.MealieAPIToken != "token-abc-123" {
			t.Errorf("MealieAPIToken = %q, want token-abc-123", cfg.MealieAPIToken)
		}
		if cfg.DBPath != "/custom/path.db" {
			t.Errorf("DBPath = %q, want /custom/path.db", cfg.DBPath)
		}
		if cfg.SourcesPath != "/custom/sources.yaml" {
			t.Errorf("SourcesPath = %q, want /custom/sources.yaml", cfg.SourcesPath)
		}
		if cfg.SeedsPath != "/custom/seeds.yaml" {
			t.Errorf("SeedsPath = %q, want /custom/seeds.yaml", cfg.SeedsPath)
		}
	})

	t.Run("whitespace-only values fall back to defaults", func(t *testing.T) {
		t.Parallel()
		envMap := map[string]string{
			EnvDBPath:      "   \t  ",
			EnvSourcesPath: "  ",
			EnvSeedsPath:   "",
		}
		lookup := func(k string) (string, bool) {
			v, ok := envMap[k]
			return v, ok
		}
		cfg := ResolveEnv(lookup)
		if cfg.DBPath != DefaultDBPath {
			t.Errorf("DBPath = %q, want %q", cfg.DBPath, DefaultDBPath)
		}
		if cfg.SourcesPath != DefaultSourcesPath {
			t.Errorf("SourcesPath = %q, want %q", cfg.SourcesPath, DefaultSourcesPath)
		}
		if cfg.SeedsPath != DefaultSeedsPath {
			t.Errorf("SeedsPath = %q, want %q", cfg.SeedsPath, DefaultSeedsPath)
		}
	})
}

func TestProductionConfigsSourcesYAML(t *testing.T) {
	t.Parallel()

	// Load the repository's configs/sources.yaml
	repoRoot, err := filepath.Abs("../../configs/sources.yaml")
	if err != nil {
		t.Fatalf("filepath.Abs failed: %v", err)
	}

	if _, err := os.Stat(repoRoot); os.IsNotExist(err) {
		// Fallback for run from repo root
		repoRoot = "configs/sources.yaml"
	}

	reg, err := LoadSources(repoRoot)
	if err != nil {
		t.Fatalf("failed to load production configs/sources.yaml: %v", err)
	}

	if reg.Version != 1 {
		t.Errorf("Version = %d, want 1", reg.Version)
	}

	if len(reg.ExcludedDomains) != 4 {
		t.Errorf("ExcludedDomains count = %d, want 4", len(reg.ExcludedDomains))
	}

	expectedExcluded := map[string]bool{
		"foodnetwork.com":         true,
		"food52.com":              true,
		"cooking.nytimes.com":     true,
		"americastestkitchen.com": true,
	}
	for _, excl := range reg.ExcludedDomains {
		if !expectedExcluded[excl.Domain] {
			t.Errorf("unexpected excluded domain %q", excl.Domain)
		}
	}

	enabledCount := 0
	disabledCount := 0
	for _, src := range reg.Sources {
		if src.Enabled {
			enabledCount++
		} else {
			disabledCount++
			if src.Domain != "smittenkitchen.com" && src.Domain != "thespruceeats.com" {
				t.Errorf("unexpected disabled source %q", src.Domain)
			}
		}
	}

	if enabledCount != 14 {
		t.Errorf("enabled sources count = %d, want 14", enabledCount)
	}
	if disabledCount != 2 {
		t.Errorf("disabled sources count = %d, want 2", disabledCount)
	}
	if len(reg.Sources) != 16 {
		t.Errorf("total sources count = %d, want 16", len(reg.Sources))
	}
}
