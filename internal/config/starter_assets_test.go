package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/paulojauregui/mealie-feeder/internal/config"
)

// resolveAssetPath locates a repository asset file whether tests execute from the package
// directory (internal/config) or the repository root.
// Pre-conditions: relPath is a relative path from the repository root (for example "configs/sources.yaml").
// Post-conditions: Returns a valid existing filesystem path, or the package-relative fallback path.
func resolveAssetPath(relPath string) string {
	candidates := []string{
		filepath.Join("..", "..", relPath),
		relPath,
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return filepath.Join("..", "..", relPath)
}

// TestStarterAssetsContract verifies the production starter configuration (configs/sources.yaml)
// and curated seed manifest (seeds/greatest-hits.yaml) against system schema and consistency invariants.
// Pre-conditions: configs/sources.yaml and seeds/greatest-hits.yaml exist on disk and are readable.
// Post-conditions: Asserts strict YAML decoding, domain counts, exclusions, seed domain resolution,
// regex allow/deny compliance, uniqueness, and exact per-cuisine distributions.
func TestStarterAssetsContract(t *testing.T) {
	sourcesPath := resolveAssetPath("configs/sources.yaml")
	seedsPath := resolveAssetPath("seeds/greatest-hits.yaml")

	// 1. Load and validate sources.yaml
	registry, err := config.LoadSources(sourcesPath)
	if err != nil {
		t.Fatalf("LoadSources(%q) failed: %v", sourcesPath, err)
	}
	if registry == nil {
		t.Fatal("LoadSources returned nil registry")
	}

	// 2. Verify source counts: 14 enabled, 2 disabled, 4 excluded domains
	var enabledCount, disabledCount int
	disabledDomains := make(map[string]bool)
	for _, src := range registry.Sources {
		if src.Enabled {
			enabledCount++
		} else {
			disabledCount++
			disabledDomains[config.NormalizeDomain(src.Domain)] = true
		}
	}

	if enabledCount != 14 {
		t.Errorf("expected 14 enabled sources, got %d", enabledCount)
	}
	if disabledCount != 2 {
		t.Errorf("expected 2 disabled sources, got %d", disabledCount)
	}
	if !disabledDomains["smittenkitchen.com"] {
		t.Errorf("expected smittenkitchen.com to be disabled")
	}
	if !disabledDomains["thespruceeats.com"] {
		t.Errorf("expected thespruceeats.com to be disabled")
	}

	if len(registry.ExcludedDomains) != 4 {
		t.Errorf("expected 4 excluded domains, got %d", len(registry.ExcludedDomains))
	}
	expectedExcluded := map[string]bool{
		"foodnetwork.com":         false,
		"food52.com":              false,
		"cooking.nytimes.com":     false,
		"americastestkitchen.com": false,
	}
	for _, ex := range registry.ExcludedDomains {
		norm := config.NormalizeDomain(ex.Domain)
		if _, expected := expectedExcluded[norm]; expected {
			expectedExcluded[norm] = true
		} else {
			t.Errorf("unexpected excluded domain: %s", ex.Domain)
		}
	}
	for dom, found := range expectedExcluded {
		if !found {
			t.Errorf("missing expected excluded domain: %s", dom)
		}
	}

	// 3. Load and validate greatest-hits.yaml
	manifest, err := config.LoadSeeds(seedsPath)
	if err != nil {
		t.Fatalf("LoadSeeds(%q) failed: %v", seedsPath, err)
	}
	if manifest == nil {
		t.Fatal("LoadSeeds returned nil manifest")
	}

	if len(manifest.Recipes) != 320 {
		t.Fatalf("expected 320 recipes, got %d", len(manifest.Recipes))
	}

	// 4. Verify each recipe entry
	seenURLs := make(map[string]bool, len(manifest.Recipes))
	cuisineCounts := make(map[string]int)

	expectedCuisines := map[string]bool{
		"american-comfort-weeknight":   true,
		"baking-pastry-desserts":       true,
		"chinese":                      true,
		"french-classic-european":      true,
		"indian-south-asian":           true,
		"italian":                      true,
		"japanese":                     true,
		"mediterranean-middle-eastern": true,
		"mexican-latin":                true,
		"thai-southeast-asian":         true,
	}

	for i, recipe := range manifest.Recipes {
		// Verify zero duplicates
		if seenURLs[recipe.URL] {
			t.Errorf("duplicate recipe URL [%d]: %s", i, recipe.URL)
		}
		seenURLs[recipe.URL] = true

		// Verify domain resolves to an enabled, non-excluded source
		src, ok := registry.SourceByDomain(recipe.Domain)
		if !ok {
			t.Errorf("recipe [%d] (%s) domain %q not found in sources.yaml", i, recipe.Title, recipe.Domain)
			continue
		}
		if !src.Enabled {
			t.Errorf("recipe [%d] (%s) domain %q is disabled in sources.yaml", i, recipe.Title, recipe.Domain)
		}
		if ex, isExcluded := registry.IsExcluded(recipe.Domain); isExcluded {
			t.Errorf("recipe [%d] (%s) domain %q is in excluded_domains: %s", i, recipe.Title, recipe.Domain, ex.Reason)
		}

		// Verify recipe URL against deny patterns
		for _, denyPat := range src.DenyPatterns {
			re, err := regexp.Compile(denyPat)
			if err != nil {
				t.Fatalf("invalid deny pattern %q for domain %q: %v", denyPat, src.Domain, err)
			}
			if re.MatchString(recipe.URL) {
				t.Errorf("recipe [%d] (%s) URL %s matches deny pattern %q", i, recipe.Title, recipe.URL, denyPat)
			}
		}

		// Verify recipe URL against allow patterns (must match at least one if allow patterns configured)
		if len(src.AllowPatterns) > 0 {
			matched := false
			for _, allowPat := range src.AllowPatterns {
				re, err := regexp.Compile(allowPat)
				if err != nil {
					t.Fatalf("invalid allow pattern %q for domain %q: %v", allowPat, src.Domain, err)
				}
				if re.MatchString(recipe.URL) {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("recipe [%d] (%s) URL %s does not match any allow pattern for domain %q (patterns: %v)",
					i, recipe.Title, recipe.URL, src.Domain, src.AllowPatterns)
			}
		}

		// Track cuisine distribution
		cuisineCounts[recipe.Cuisine]++
	}

	// Verify all 10 culinary traditions have exactly 32 entries
	if len(cuisineCounts) != 10 {
		t.Errorf("expected 10 culinary traditions, got %d: %v", len(cuisineCounts), cuisineCounts)
	}
	for cuisine := range expectedCuisines {
		count := cuisineCounts[cuisine]
		if count != 32 {
			t.Errorf("expected cuisine %q to have 32 recipes, got %d", cuisine, count)
		}
	}
}
