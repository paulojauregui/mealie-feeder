package config_test

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

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

	// 2. Verify source counts: 28 enabled, 2 disabled, 4 excluded domains
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

	if enabledCount != 28 {
		t.Errorf("expected 28 enabled sources, got %d", enabledCount)
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

// TestVol2SeedsContract verifies the curated Volume 2 seed manifest (seeds/greatest-hits-vol2.yaml)
// against sources.yaml schema invariants and strict disjointness from Volume 1 (seeds/greatest-hits.yaml).
// Pre-conditions: configs/sources.yaml, seeds/greatest-hits.yaml, and seeds/greatest-hits-vol2.yaml exist on disk and are readable.
// Post-conditions: Asserts strict YAML decoding, total recipe count (320), exact 8 gap-fill cuisines (40 recipes each),
// resolution to enabled sources, allow/deny regex compliance, zero URL overlap with Volume 1, zero internal duplicates,
// complete absence of emoji characters, and zero Condé Nast ba-syn slug or title collisions with bonappetit.com.
func TestVol2SeedsContract(t *testing.T) {
	sourcesPath := resolveAssetPath("configs/sources.yaml")
	vol1Path := resolveAssetPath("seeds/greatest-hits.yaml")
	vol2Path := resolveAssetPath("seeds/greatest-hits-vol2.yaml")

	// 1. Load sources.yaml
	registry, err := config.LoadSources(sourcesPath)
	if err != nil {
		t.Fatalf("LoadSources(%q) failed: %v", sourcesPath, err)
	}
	if registry == nil {
		t.Fatal("LoadSources returned nil registry")
	}

	// 2. Load Volume 1 seeds to assert disjointness
	vol1Manifest, err := config.LoadSeeds(vol1Path)
	if err != nil {
		t.Fatalf("LoadSeeds(%q) failed: %v", vol1Path, err)
	}
	if vol1Manifest == nil {
		t.Fatal("LoadSeeds returned nil Volume 1 manifest")
	}

	canonicalURLKey := func(rawURL string) string {
		u, parseErr := url.Parse(strings.TrimSpace(rawURL))
		if parseErr != nil {
			return strings.ToLower(rawURL)
		}
		host := config.NormalizeDomain(u.Hostname())
		path := strings.TrimRight(u.Path, "/")
		return strings.ToLower(host + path)
	}

	lastPathSegment := func(rawURL string) string {
		u, parseErr := url.Parse(strings.TrimSpace(rawURL))
		if parseErr != nil {
			return ""
		}
		p := strings.TrimRight(u.Path, "/")
		idx := strings.LastIndex(p, "/")
		if idx >= 0 {
			return p[idx+1:]
		}
		return p
	}

	vol1Exact := make(map[string]bool, len(vol1Manifest.Recipes))
	vol1Canon := make(map[string]bool, len(vol1Manifest.Recipes))
	for _, r := range vol1Manifest.Recipes {
		vol1Exact[r.URL] = true
		vol1Canon[canonicalURLKey(r.URL)] = true
	}

	// 3. Load Volume 2 seeds
	vol2Manifest, err := config.LoadSeeds(vol2Path)
	if err != nil {
		t.Fatalf("LoadSeeds(%q) failed: %v", vol2Path, err)
	}
	if vol2Manifest == nil {
		t.Fatal("LoadSeeds returned nil Volume 2 manifest")
	}

	if len(vol2Manifest.Recipes) != 320 {
		t.Fatalf("expected 320 recipes in Volume 2, got %d", len(vol2Manifest.Recipes))
	}

	// Index all bonappetit.com entries across Volume 1 and Volume 2 for cross-syndication checks
	baSlugs := make(map[string]string)
	baTitles := make(map[string]string)
	indexBA := func(recipes []config.SeedEntry) {
		for _, r := range recipes {
			if config.NormalizeDomain(r.Domain) == "bonappetit.com" || strings.Contains(r.URL, "bonappetit.com") {
				slug := strings.ToLower(lastPathSegment(r.URL))
				if slug != "" {
					baSlugs[slug] = r.URL
				}
				normTitle := strings.ToLower(strings.TrimSpace(r.Title))
				if normTitle != "" {
					baTitles[normTitle] = r.URL
				}
			}
		}
	}
	indexBA(vol1Manifest.Recipes)
	indexBA(vol2Manifest.Recipes)

	// 4. Verify each recipe entry in Volume 2
	seenURLs := make(map[string]bool, len(vol2Manifest.Recipes))
	seenCanon := make(map[string]bool, len(vol2Manifest.Recipes))
	cuisineCounts := make(map[string]int)

	expectedCuisines := map[string]int{
		"korean":           40,
		"vietnamese":       40,
		"spanish":          40,
		"breakfast-brunch": 40,
		"soups-stews":      40,
		"grilling-bbq":     40,
		"weeknight-30min":  40,
		"drinks-cocktails": 40,
	}

	isRuneEmoji := func(r rune) bool {
		if r > 0xFFFF {
			return true
		}
		if r >= 0x2600 && r <= 0x27BF {
			return true
		}
		if r >= 0xFE00 && r <= 0xFE0F {
			return true
		}
		return false
	}

	hasEmojiOrInvalidUTF8 := func(s string) bool {
		if !utf8.ValidString(s) {
			return true
		}
		for _, r := range s {
			if isRuneEmoji(r) {
				return true
			}
		}
		return false
	}

	for i, recipe := range vol2Manifest.Recipes {
		// Verify zero duplicates within Volume 2
		if seenURLs[recipe.URL] {
			t.Errorf("duplicate recipe URL in Volume 2 [%d]: %s", i, recipe.URL)
		}
		seenURLs[recipe.URL] = true

		cKey := canonicalURLKey(recipe.URL)
		if seenCanon[cKey] {
			t.Errorf("duplicate canonical recipe URL in Volume 2 [%d]: %s (%s)", i, recipe.URL, cKey)
		}
		seenCanon[cKey] = true

		// Verify zero overlap with Volume 1
		if vol1Exact[recipe.URL] {
			t.Errorf("recipe [%d] (%s) overlaps exactly with Volume 1 seed: %s", i, recipe.Title, recipe.URL)
		}
		if vol1Canon[cKey] {
			t.Errorf("recipe [%d] (%s) overlaps canonically with Volume 1 seed: %s (%s)", i, recipe.Title, recipe.URL, cKey)
		}

		// Verify zero emojis and valid UTF-8
		if hasEmojiOrInvalidUTF8(recipe.Title) {
			t.Errorf("recipe [%d] title contains emojis or invalid UTF-8: %q", i, recipe.Title)
		}
		if hasEmojiOrInvalidUTF8(recipe.Cuisine) {
			t.Errorf("recipe [%d] cuisine contains emojis or invalid UTF-8: %q", i, recipe.Cuisine)
		}
		if hasEmojiOrInvalidUTF8(recipe.Category) {
			t.Errorf("recipe [%d] category contains emojis or invalid UTF-8: %q", i, recipe.Category)
		}

		// Verify zero Condé Nast cross-syndication collisions between epicurious.com ba-syn-* and bonappetit.com
		if config.NormalizeDomain(recipe.Domain) == "epicurious.com" {
			seg := lastPathSegment(recipe.URL)
			if strings.HasPrefix(seg, "ba-syn-") {
				stripped := strings.ToLower(strings.TrimPrefix(seg, "ba-syn-"))
				if baURL, collides := baSlugs[stripped]; collides {
					t.Errorf("recipe [%d] (%s) epicurious ba-syn slug %q collides with bonappetit entry: %s",
						i, recipe.URL, stripped, baURL)
				}
				normTitle := strings.ToLower(strings.TrimSpace(recipe.Title))
				if baURL, collides := baTitles[normTitle]; collides {
					t.Errorf("recipe [%d] (%s) epicurious ba-syn title %q collides with bonappetit entry: %s",
						i, recipe.URL, recipe.Title, baURL)
				}
			}
		}

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

		// Verify recipe URL against allow patterns
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

	// 5. Verify all 8 gap categories have exactly 40 entries
	if len(cuisineCounts) != len(expectedCuisines) {
		t.Errorf("expected %d cuisines in Volume 2, got %d: %v", len(expectedCuisines), len(cuisineCounts), cuisineCounts)
	}
	for cuisine, expectedCount := range expectedCuisines {
		count := cuisineCounts[cuisine]
		if count != expectedCount {
			t.Errorf("expected cuisine %q to have %d recipes, got %d", cuisine, expectedCount, count)
		}
	}
}

// TestVol4SeedsContract verifies the curated Volume 4 seed manifest (seeds/greatest-hits-vol4.yaml)
// against sources.yaml schema invariants and strict disjointness from Volume 1 (seeds/greatest-hits.yaml)
// and Volume 2 (seeds/greatest-hits-vol2.yaml).
// Pre-conditions: configs/sources.yaml, seeds/greatest-hits.yaml, seeds/greatest-hits-vol2.yaml,
// and seeds/greatest-hits-vol4.yaml exist on disk and are readable.
// Post-conditions: Asserts strict YAML decoding, total recipe count (360), exact 10 categories (36 recipes each),
// resolution to enabled sources, allow/deny regex compliance, zero URL overlap with Volume 1 or Volume 2,
// zero internal duplicates, complete absence of emoji characters, and zero Condé Nast ba-syn collisions.
func TestVol4SeedsContract(t *testing.T) {
	sourcesPath := resolveAssetPath("configs/sources.yaml")
	vol1Path := resolveAssetPath("seeds/greatest-hits.yaml")
	vol2Path := resolveAssetPath("seeds/greatest-hits-vol2.yaml")
	vol4Path := resolveAssetPath("seeds/greatest-hits-vol4.yaml")

	// 1. Load sources.yaml
	registry, err := config.LoadSources(sourcesPath)
	if err != nil {
		t.Fatalf("LoadSources(%q) failed: %v", sourcesPath, err)
	}
	if registry == nil {
		t.Fatal("LoadSources returned nil registry")
	}

	canonicalURLKey := func(rawURL string) string {
		u, parseErr := url.Parse(strings.TrimSpace(rawURL))
		if parseErr != nil {
			return strings.ToLower(rawURL)
		}
		host := config.NormalizeDomain(u.Hostname())
		path := strings.TrimRight(u.Path, "/")
		return strings.ToLower(host + path)
	}

	lastPathSegment := func(rawURL string) string {
		u, parseErr := url.Parse(strings.TrimSpace(rawURL))
		if parseErr != nil {
			return ""
		}
		p := strings.TrimRight(u.Path, "/")
		idx := strings.LastIndex(p, "/")
		if idx >= 0 {
			return p[idx+1:]
		}
		return p
	}

	// 2. Load Volume 1 & Volume 2 seeds to assert disjointness
	vol1Manifest, err := config.LoadSeeds(vol1Path)
	if err != nil {
		t.Fatalf("LoadSeeds(%q) failed: %v", vol1Path, err)
	}
	vol2Manifest, err := config.LoadSeeds(vol2Path)
	if err != nil {
		t.Fatalf("LoadSeeds(%q) failed: %v", vol2Path, err)
	}

	priorExact := make(map[string]string)
	priorCanon := make(map[string]string)
	for _, r := range vol1Manifest.Recipes {
		priorExact[r.URL] = "Volume 1"
		priorCanon[canonicalURLKey(r.URL)] = "Volume 1"
	}
	for _, r := range vol2Manifest.Recipes {
		priorExact[r.URL] = "Volume 2"
		priorCanon[canonicalURLKey(r.URL)] = "Volume 2"
	}

	// 3. Load Volume 4 seeds
	vol4Manifest, err := config.LoadSeeds(vol4Path)
	if err != nil {
		t.Fatalf("LoadSeeds(%q) failed: %v", vol4Path, err)
	}
	if vol4Manifest == nil {
		t.Fatal("LoadSeeds returned nil Volume 4 manifest")
	}

	if len(vol4Manifest.Recipes) != 360 {
		t.Fatalf("expected 360 recipes in Volume 4, got %d", len(vol4Manifest.Recipes))
	}

	// Index all bonappetit.com entries across Volume 1, Volume 2, and Volume 4
	baSlugs := make(map[string]string)
	baTitles := make(map[string]string)
	indexBA := func(recipes []config.SeedEntry) {
		for _, r := range recipes {
			if config.NormalizeDomain(r.Domain) == "bonappetit.com" || strings.Contains(r.URL, "bonappetit.com") {
				slug := strings.ToLower(lastPathSegment(r.URL))
				if slug != "" {
					baSlugs[slug] = r.URL
				}
				normTitle := strings.ToLower(strings.TrimSpace(r.Title))
				if normTitle != "" {
					baTitles[normTitle] = r.URL
				}
			}
		}
	}
	indexBA(vol1Manifest.Recipes)
	indexBA(vol2Manifest.Recipes)
	indexBA(vol4Manifest.Recipes)

	// 4. Verify each recipe entry in Volume 4
	seenURLs := make(map[string]bool, len(vol4Manifest.Recipes))
	seenCanon := make(map[string]bool, len(vol4Manifest.Recipes))
	cuisineCounts := make(map[string]int)

	expectedCuisines := map[string]int{
		"japanese-deep-cuts":             36,
		"cantonese-sichuan-taiwanese":    36,
		"korean-deep-cuts":               36,
		"singaporean-malaysian-filipino": 36,
		"bangkok-thai-regional":          36,
		"mexico-city-oaxacan":            36,
		"lima-peruvian-latin":            36,
		"caribbean-diaspora":             36,
		"paris-london-classics":          36,
		"nyc-sf-copenhagen-coastal":      36,
	}

	isRuneEmoji := func(r rune) bool {
		if r > 0xFFFF {
			return true
		}
		if r >= 0x2600 && r <= 0x27BF {
			return true
		}
		if r >= 0xFE00 && r <= 0xFE0F {
			return true
		}
		return false
	}

	hasEmojiOrInvalidUTF8 := func(s string) bool {
		if !utf8.ValidString(s) {
			return true
		}
		for _, r := range s {
			if isRuneEmoji(r) {
				return true
			}
		}
		return false
	}

	for i, recipe := range vol4Manifest.Recipes {
		// Verify zero duplicates within Volume 4
		if seenURLs[recipe.URL] {
			t.Errorf("duplicate recipe URL in Volume 4 [%d]: %s", i, recipe.URL)
		}
		seenURLs[recipe.URL] = true

		cKey := canonicalURLKey(recipe.URL)
		if seenCanon[cKey] {
			t.Errorf("duplicate canonical recipe URL in Volume 4 [%d]: %s (%s)", i, recipe.URL, cKey)
		}
		seenCanon[cKey] = true

		// Verify zero overlap with Volume 1 or Volume 2
		if sourceVol, exists := priorExact[recipe.URL]; exists {
			t.Errorf("recipe [%d] (%s) overlaps exactly with %s seed: %s", i, recipe.Title, sourceVol, recipe.URL)
		}
		if sourceVol, exists := priorCanon[cKey]; exists {
			t.Errorf("recipe [%d] (%s) overlaps canonically with %s seed: %s (%s)", i, recipe.Title, sourceVol, recipe.URL, cKey)
		}

		// Verify zero emojis and valid UTF-8
		if hasEmojiOrInvalidUTF8(recipe.Title) {
			t.Errorf("recipe [%d] title contains emojis or invalid UTF-8: %q", i, recipe.Title)
		}
		if hasEmojiOrInvalidUTF8(recipe.Cuisine) {
			t.Errorf("recipe [%d] cuisine contains emojis or invalid UTF-8: %q", i, recipe.Cuisine)
		}
		if hasEmojiOrInvalidUTF8(recipe.Category) {
			t.Errorf("recipe [%d] category contains emojis or invalid UTF-8: %q", i, recipe.Category)
		}

		// Verify zero Condé Nast cross-syndication collisions between epicurious.com ba-syn-* and bonappetit.com
		if config.NormalizeDomain(recipe.Domain) == "epicurious.com" {
			seg := lastPathSegment(recipe.URL)
			if strings.HasPrefix(seg, "ba-syn-") {
				stripped := strings.ToLower(strings.TrimPrefix(seg, "ba-syn-"))
				if baURL, collides := baSlugs[stripped]; collides {
					t.Errorf("recipe [%d] (%s) epicurious ba-syn slug %q collides with bonappetit entry: %s",
						i, recipe.URL, stripped, baURL)
				}
				normTitle := strings.ToLower(strings.TrimSpace(recipe.Title))
				if baURL, collides := baTitles[normTitle]; collides {
					t.Errorf("recipe [%d] (%s) epicurious ba-syn title %q collides with bonappetit entry: %s",
						i, recipe.URL, recipe.Title, baURL)
				}
			}
		}

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

		// Verify recipe URL against allow patterns
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

	// 5. Verify all 10 categories have exactly 36 entries
	if len(cuisineCounts) != len(expectedCuisines) {
		t.Errorf("expected %d cuisines in Volume 4, got %d: %v", len(expectedCuisines), len(cuisineCounts), cuisineCounts)
	}
	for cuisine, expectedCount := range expectedCuisines {
		count := cuisineCounts[cuisine]
		if count != expectedCount {
			t.Errorf("expected cuisine %q to have %d recipes, got %d", cuisine, expectedCount, count)
		}
	}
}
