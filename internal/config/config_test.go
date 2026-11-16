package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func validSourceRegistry() SourceRegistry {
	return SourceRegistry{
		Version: 1,
		Defaults: DefaultsConfig{
			TLSMode:          TLSModeChromeAuto,
			SchemaMode:       SchemaModeJSONLD,
			MinInterval:      2 * time.Second,
			Timeout:          20 * time.Second,
			MaxRetries:       2,
			MaxPerRun:        25,
			AllowURLFallback: false,
		},
		ExcludedDomains: []ExcludedDomain{
			{Domain: "foodnetwork.com", Reason: "Aggressive Cloudflare bot challenge and anti-scraping paywall"},
			{Domain: "nytimes.com", Reason: "Hard paywall"},
		},
		Sources: []SourceConfig{
			{
				Domain:        "seriouseats.com",
				CanonicalHost: "seriouseats.com",
				Enabled:       true,
				Tier:          "core",
				TLSMode:       TLSModeChromeAuto,
				SchemaMode:    SchemaModeJSONLD,
				MinInterval:   2 * time.Second,
				Timeout:       20 * time.Second,
				MaxRetries:    2,
				MaxPerRun:     25,
				SitemapURLs:   []string{"https://www.seriouseats.com/sitemap_1.xml"},
				AllowPatterns: []string{`^https://www\.seriouseats\.com/.*-recipe-`},
				DenyPatterns:  []string{`/roundups/`, `/best-`},
			},
			{
				Domain:        "smittenkitchen.com",
				CanonicalHost: "smittenkitchen.com",
				Enabled:       true,
				Tier:          "core",
				TLSMode:       TLSModePlain,
				SchemaMode:    SchemaModeMicrodata,
				MinInterval:   4 * time.Second,
				Timeout:       30 * time.Second,
				MaxRetries:    3,
				MaxPerRun:     10,
				RSSURLs:       []string{"https://smittenkitchen.com/feed/"},
			},
		},
	}
}

func validSeedManifest() SeedManifest {
	return SeedManifest{
		Version:     1,
		Description: "Curated greatest hits recipe collection",
		Recipes: []SeedEntry{
			{
				URL:      "https://www.seriouseats.com/the-best-slow-cooked-bolognese-sauce-recipe",
				Title:    "The Best Slow-Cooked Bolognese Sauce",
				Domain:   "seriouseats.com",
				Cuisine:  "italian",
				Category: "Main Course",
			},
			{
				URL:      "https://smittenkitchen.com/2006/09/apple-tarte-tatin/",
				Title:    "Apple Tarte Tatin",
				Domain:   "smittenkitchen.com",
				Cuisine:  "french",
				Category: "Dessert",
			},
		},
	}
}

func TestNormalizeDomain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "clean domain", input: "seriouseats.com", expected: "seriouseats.com"},
		{name: "with www", input: "www.seriouseats.com", expected: "seriouseats.com"},
		{name: "uppercase and www", input: "WWW.SERIOUSEATS.COM", expected: "seriouseats.com"},
		{name: "with https scheme", input: "https://www.seriouseats.com", expected: "seriouseats.com"},
		{name: "with http scheme", input: "http://seriouseats.com", expected: "seriouseats.com"},
		{name: "with trailing slash", input: "seriouseats.com/", expected: "seriouseats.com"},
		{name: "with path and scheme", input: "https://www.seriouseats.com/recipes/all", expected: "seriouseats.com"},
		{name: "with port", input: "seriouseats.com:8080", expected: "seriouseats.com"},
		{name: "with scheme and port", input: "http://www.seriouseats.com:8443/feed", expected: "seriouseats.com"},
		{name: "surrounding whitespace", input: "   https://allrecipes.com/   ", expected: "allrecipes.com"},
		{name: "trailing dot fqdn", input: "bbcgoodfood.com.", expected: "bbcgoodfood.com"},
		{name: "empty string", input: "", expected: ""},
		{name: "only whitespace", input: "   ", expected: ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			actual := NormalizeDomain(tc.input)
			if actual != tc.expected {
				t.Fatalf("NormalizeDomain(%q) = %q; want %q", tc.input, actual, tc.expected)
			}
		})
	}
}

func TestSourceRegistry_ApplyDefaults(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver safe", func(t *testing.T) {
		t.Parallel()
		var r *SourceRegistry
		r.ApplyDefaults()
	})

	t.Run("populates zero-value defaults and propagates to source", func(t *testing.T) {
		t.Parallel()
		reg := SourceRegistry{
			Version: 1,
			Sources: []SourceConfig{
				{
					Domain:      "minimal.example.com",
					Enabled:     true,
					SitemapURLs: []string{"https://minimal.example.com/sitemap.xml"},
				},
				{
					Domain:        "custom.example.com",
					CanonicalHost: "custom-host.example.com",
					Enabled:       true,
					TLSMode:       TLSModeSafariAuto,
					SchemaMode:    SchemaModeMicrodata,
					MinInterval:   5 * time.Second,
					Timeout:       45 * time.Second,
					MaxRetries:    5,
					MaxPerRun:     50,
					SitemapURLs:   []string{"https://custom.example.com/sitemap.xml"},
				},
			},
		}

		reg.ApplyDefaults()

		// Verify defaults resolution
		if reg.Defaults.TLSMode != TLSModeChromeAuto {
			t.Errorf("Defaults.TLSMode = %q; want %q", reg.Defaults.TLSMode, TLSModeChromeAuto)
		}
		if reg.Defaults.SchemaMode != SchemaModeJSONLD {
			t.Errorf("Defaults.SchemaMode = %q; want %q", reg.Defaults.SchemaMode, SchemaModeJSONLD)
		}
		if reg.Defaults.MinInterval != 2*time.Second {
			t.Errorf("Defaults.MinInterval = %v; want 2s", reg.Defaults.MinInterval)
		}
		if reg.Defaults.Timeout != 20*time.Second {
			t.Errorf("Defaults.Timeout = %v; want 20s", reg.Defaults.Timeout)
		}
		if reg.Defaults.MaxRetries != 2 {
			t.Errorf("Defaults.MaxRetries = %d; want 2", reg.Defaults.MaxRetries)
		}
		if reg.Defaults.MaxPerRun != 25 {
			t.Errorf("Defaults.MaxPerRun = %d; want 25", reg.Defaults.MaxPerRun)
		}

		// Verify minimal source inherited defaults
		s0 := reg.Sources[0]
		if s0.CanonicalHost != "minimal.example.com" {
			t.Errorf("s0.CanonicalHost = %q; want %q", s0.CanonicalHost, "minimal.example.com")
		}
		if s0.TLSMode != TLSModeChromeAuto {
			t.Errorf("s0.TLSMode = %q; want %q", s0.TLSMode, TLSModeChromeAuto)
		}
		if s0.SchemaMode != SchemaModeJSONLD {
			t.Errorf("s0.SchemaMode = %q; want %q", s0.SchemaMode, SchemaModeJSONLD)
		}
		if s0.MinInterval != 2*time.Second {
			t.Errorf("s0.MinInterval = %v; want 2s", s0.MinInterval)
		}
		if s0.Timeout != 20*time.Second {
			t.Errorf("s0.Timeout = %v; want 20s", s0.Timeout)
		}
		if s0.MaxRetries != 2 {
			t.Errorf("s0.MaxRetries = %d; want 2", s0.MaxRetries)
		}
		if s0.MaxPerRun != 25 {
			t.Errorf("s0.MaxPerRun = %d; want 25", s0.MaxPerRun)
		}

		// Verify custom source preserved explicit overrides
		s1 := reg.Sources[1]
		if s1.CanonicalHost != "custom-host.example.com" {
			t.Errorf("s1.CanonicalHost = %q; want custom-host.example.com", s1.CanonicalHost)
		}
		if s1.TLSMode != TLSModeSafariAuto {
			t.Errorf("s1.TLSMode = %q; want %q", s1.TLSMode, TLSModeSafariAuto)
		}
		if s1.SchemaMode != SchemaModeMicrodata {
			t.Errorf("s1.SchemaMode = %q; want %q", s1.SchemaMode, SchemaModeMicrodata)
		}
		if s1.MinInterval != 5*time.Second {
			t.Errorf("s1.MinInterval = %v; want 5s", s1.MinInterval)
		}
		if s1.Timeout != 45*time.Second {
			t.Errorf("s1.Timeout = %v; want 45s", s1.Timeout)
		}
		if s1.MaxRetries != 5 {
			t.Errorf("s1.MaxRetries = %d; want 5", s1.MaxRetries)
		}
		if s1.MaxPerRun != 50 {
			t.Errorf("s1.MaxPerRun = %d; want 50", s1.MaxPerRun)
		}
	})
}

func TestSourceRegistry_Validate(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver error", func(t *testing.T) {
		t.Parallel()
		var r *SourceRegistry
		if err := r.Validate(); err == nil {
			t.Fatal("expected error on nil registry")
		}
	})

	t.Run("valid registry passes", func(t *testing.T) {
		t.Parallel()
		reg := validSourceRegistry()
		reg.ApplyDefaults()
		if err := reg.Validate(); err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}
	})

	t.Run("version invariants", func(t *testing.T) {
		t.Parallel()
		for _, v := range []int{0, 2, -1, 10} {
			reg := validSourceRegistry()
			reg.Version = v
			if err := reg.Validate(); err == nil || !strings.Contains(err.Error(), "version") {
				t.Fatalf("expected version error for version %d, got: %v", v, err)
			}
		}
	})

	t.Run("defaults invalid fields", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name   string
			modify func(*SourceRegistry)
		}{
			{
				name: "invalid tls mode in defaults",
				modify: func(r *SourceRegistry) {
					r.Defaults.TLSMode = "bogus_tls"
				},
			},
			{
				name: "invalid schema mode in defaults",
				modify: func(r *SourceRegistry) {
					r.Defaults.SchemaMode = "bogus_schema"
				},
			},
			{
				name: "negative min_interval in defaults",
				modify: func(r *SourceRegistry) {
					r.Defaults.MinInterval = -1 * time.Second
				},
			},
			{
				name: "negative timeout in defaults",
				modify: func(r *SourceRegistry) {
					r.Defaults.Timeout = -5 * time.Second
				},
			},
			{
				name: "negative max_retries in defaults",
				modify: func(r *SourceRegistry) {
					r.Defaults.MaxRetries = -1
				},
			},
			{
				name: "negative max_per_run in defaults",
				modify: func(r *SourceRegistry) {
					r.Defaults.MaxPerRun = -1
				},
			},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				reg := validSourceRegistry()
				tc.modify(&reg)
				if err := reg.Validate(); err == nil {
					t.Fatalf("expected validation error for %s", tc.name)
				}
			})
		}
	})

	t.Run("duplicate source domains normalized", func(t *testing.T) {
		t.Parallel()
		reg := validSourceRegistry()
		reg.Sources = append(reg.Sources, SourceConfig{
			Domain:      "www.seriouseats.com", // duplicate of seriouseats.com
			Enabled:     true,
			SitemapURLs: []string{"https://www.seriouseats.com/sitemap_2.xml"},
		})
		reg.ApplyDefaults()
		err := reg.Validate()
		if err == nil || !strings.Contains(err.Error(), "duplicate source domain") {
			t.Fatalf("expected duplicate source domain error, got: %v", err)
		}
	})

	t.Run("enabled source matches excluded domain", func(t *testing.T) {
		t.Parallel()
		reg := validSourceRegistry()
		reg.Sources = append(reg.Sources, SourceConfig{
			Domain:      "www.foodnetwork.com", // excluded in validSourceRegistry
			Enabled:     true,
			SitemapURLs: []string{"https://www.foodnetwork.com/sitemap.xml"},
		})
		reg.ApplyDefaults()
		err := reg.Validate()
		if err == nil || !strings.Contains(err.Error(), "is excluded") {
			t.Fatalf("expected excluded domain error, got: %v", err)
		}
	})

	t.Run("disabled source matches excluded domain is permitted", func(t *testing.T) {
		t.Parallel()
		reg := validSourceRegistry()
		reg.Sources = append(reg.Sources, SourceConfig{
			Domain:      "www.foodnetwork.com",
			Enabled:     false,
			SitemapURLs: []string{"https://www.foodnetwork.com/sitemap.xml"},
		})
		reg.ApplyDefaults()
		if err := reg.Validate(); err != nil {
			t.Fatalf("disabled source in excluded domain should be allowed, got error: %v", err)
		}
	})

	t.Run("source with empty domain", func(t *testing.T) {
		t.Parallel()
		reg := validSourceRegistry()
		reg.Sources = append(reg.Sources, SourceConfig{
			Domain:      "",
			Enabled:     true,
			SitemapURLs: []string{"https://example.com/sitemap.xml"},
		})
		reg.ApplyDefaults()
		if err := reg.Validate(); err == nil || !strings.Contains(err.Error(), "empty domain") {
			t.Fatalf("expected empty domain error, got: %v", err)
		}
	})

	t.Run("excluded domain with empty domain or reason", func(t *testing.T) {
		t.Parallel()
		reg := validSourceRegistry()
		reg.ExcludedDomains = append(reg.ExcludedDomains, ExcludedDomain{Domain: "", Reason: "foo"})
		reg.ApplyDefaults()
		if err := reg.Validate(); err == nil {
			t.Fatal("expected error for empty excluded domain")
		}

		reg2 := validSourceRegistry()
		reg2.ExcludedDomains = append(reg2.ExcludedDomains, ExcludedDomain{Domain: "example.com", Reason: "   "})
		reg2.ApplyDefaults()
		if err := reg2.Validate(); err == nil {
			t.Fatal("expected error for empty excluded reason")
		}
	})

	t.Run("enabled source with no discovery URLs", func(t *testing.T) {
		t.Parallel()
		reg := validSourceRegistry()
		reg.Sources[0].SitemapURLs = nil
		reg.Sources[0].RSSURLs = nil
		reg.ApplyDefaults()
		err := reg.Validate()
		if err == nil || !strings.Contains(err.Error(), "defines no sitemap_urls or rss_urls") {
			t.Fatalf("expected error for zero discovery URLs, got: %v", err)
		}
	})

	t.Run("disabled source with zero discovery URLs is allowed", func(t *testing.T) {
		t.Parallel()
		reg := validSourceRegistry()
		reg.Sources[0].Enabled = false
		reg.Sources[0].SitemapURLs = nil
		reg.Sources[0].RSSURLs = nil
		reg.ApplyDefaults()
		if err := reg.Validate(); err != nil {
			t.Fatalf("disabled source without discovery URLs should pass, got: %v", err)
		}
	})

	t.Run("invalid tls_mode on source", func(t *testing.T) {
		t.Parallel()
		reg := validSourceRegistry()
		reg.Sources[0].TLSMode = "unsupported_mode"
		err := reg.Validate()
		if err == nil || !strings.Contains(err.Error(), "invalid tls_mode") {
			t.Fatalf("expected invalid tls_mode error, got: %v", err)
		}
	})

	t.Run("invalid schema_mode on source", func(t *testing.T) {
		t.Parallel()
		reg := validSourceRegistry()
		reg.Sources[0].SchemaMode = "unsupported_schema"
		err := reg.Validate()
		if err == nil || !strings.Contains(err.Error(), "invalid schema_mode") {
			t.Fatalf("expected invalid schema_mode error, got: %v", err)
		}
	})

	t.Run("enabled source with non-positive runtime bounds", func(t *testing.T) {
		t.Parallel()
		bounds := []struct {
			name   string
			modify func(*SourceConfig)
		}{
			{"max_per_run zero", func(s *SourceConfig) { s.MaxPerRun = 0 }},
			{"max_per_run negative", func(s *SourceConfig) { s.MaxPerRun = -5 }},
			{"min_interval zero", func(s *SourceConfig) { s.MinInterval = 0 }},
			{"min_interval negative", func(s *SourceConfig) { s.MinInterval = -1 * time.Second }},
			{"timeout zero", func(s *SourceConfig) { s.Timeout = 0 }},
			{"timeout negative", func(s *SourceConfig) { s.Timeout = -10 * time.Second }},
		}

		for _, b := range bounds {
			b := b
			t.Run(b.name, func(t *testing.T) {
				t.Parallel()
				reg := validSourceRegistry()
				reg.ApplyDefaults()
				b.modify(&reg.Sources[0])
				if err := reg.Validate(); err == nil {
					t.Fatalf("expected validation error for %s", b.name)
				}
			})
		}
	})

	t.Run("disabled source with negative bounds", func(t *testing.T) {
		t.Parallel()
		for _, field := range []string{"min_interval", "timeout", "max_retries", "max_per_run"} {
			reg := validSourceRegistry()
			reg.Sources[0].Enabled = false
			switch field {
			case "min_interval":
				reg.Sources[0].MinInterval = -2 * time.Second
			case "timeout":
				reg.Sources[0].Timeout = -10 * time.Second
			case "max_retries":
				reg.Sources[0].MaxRetries = -1
			case "max_per_run":
				reg.Sources[0].MaxPerRun = -5
			}
			if err := reg.Validate(); err == nil {
				t.Fatalf("expected error on negative %s for disabled source", field)
			}
		}
	})

	t.Run("invalid regex patterns", func(t *testing.T) {
		t.Parallel()
		patterns := []struct {
			name   string
			modify func(*SourceConfig)
		}{
			{"allow pattern", func(s *SourceConfig) { s.AllowPatterns = []string{"(?P<invalid"} }},
			{"deny pattern", func(s *SourceConfig) { s.DenyPatterns = []string{"[unclosed"} }},
			{"sitemap pattern", func(s *SourceConfig) { s.SitemapIncludePatterns = []string{"*leading_repetition"} }},
		}

		for _, p := range patterns {
			p := p
			t.Run(p.name, func(t *testing.T) {
				t.Parallel()
				reg := validSourceRegistry()
				reg.ApplyDefaults()
				p.modify(&reg.Sources[0])
				if err := reg.Validate(); err == nil {
					t.Fatalf("expected error for malformed regex in %s", p.name)
				}
			})
		}
	})

	t.Run("invalid URLs in sitemap or RSS", func(t *testing.T) {
		t.Parallel()
		invalidURLs := []struct {
			name   string
			modify func(*SourceConfig)
		}{
			{"empty sitemap url", func(s *SourceConfig) { s.SitemapURLs = []string{""} }},
			{"malformed sitemap url", func(s *SourceConfig) { s.SitemapURLs = []string{"://not-a-url"} }},
			{"ftp scheme sitemap url", func(s *SourceConfig) { s.SitemapURLs = []string{"ftp://example.com/sitemap.xml"} }},
			{"missing host sitemap url", func(s *SourceConfig) { s.SitemapURLs = []string{"https:///sitemap.xml"} }},
			{"empty rss url", func(s *SourceConfig) { s.RSSURLs = []string{"   "} }},
			{"malformed rss url", func(s *SourceConfig) { s.RSSURLs = []string{"://not-a-url"} }},
			{"javascript scheme rss url", func(s *SourceConfig) { s.RSSURLs = []string{"javascript:alert(1)"} }},
		}

		for _, u := range invalidURLs {
			u := u
			t.Run(u.name, func(t *testing.T) {
				t.Parallel()
				reg := validSourceRegistry()
				reg.ApplyDefaults()
				u.modify(&reg.Sources[0])
				if err := reg.Validate(); err == nil {
					t.Fatalf("expected error for %s", u.name)
				}
			})
		}
	})
}

func TestSourceRegistry_SourceByDomain(t *testing.T) {
	t.Parallel()

	reg := validSourceRegistry()
	reg.ApplyDefaults()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()
		var r *SourceRegistry
		_, found := r.SourceByDomain("seriouseats.com")
		if found {
			t.Fatal("expected false for nil registry")
		}
	})

	t.Run("empty domain", func(t *testing.T) {
		t.Parallel()
		_, found := reg.SourceByDomain("")
		if found {
			t.Fatal("expected false for empty query")
		}
	})

	t.Run("exact domain match", func(t *testing.T) {
		t.Parallel()
		src, found := reg.SourceByDomain("seriouseats.com")
		if !found {
			t.Fatal("expected source to be found")
		}
		if src.Domain != "seriouseats.com" {
			t.Fatalf("expected domain seriouseats.com, got %q", src.Domain)
		}
	})

	t.Run("www and mixed case match", func(t *testing.T) {
		t.Parallel()
		src, found := reg.SourceByDomain("WWW.SeriousEats.Com")
		if !found {
			t.Fatal("expected source to be found with www and mixed case")
		}
		if src.Domain != "seriouseats.com" {
			t.Fatalf("expected domain seriouseats.com, got %q", src.Domain)
		}
	})

	t.Run("canonical host match", func(t *testing.T) {
		t.Parallel()
		r := SourceRegistry{
			Sources: []SourceConfig{
				{
					Domain:        "oldbrand.com",
					CanonicalHost: "newbrand.com",
					Enabled:       true,
					SitemapURLs:   []string{"https://newbrand.com/sitemap.xml"},
				},
			},
		}
		r.ApplyDefaults()

		src, found := r.SourceByDomain("www.newbrand.com")
		if !found {
			t.Fatal("expected match on CanonicalHost")
		}
		if src.Domain != "oldbrand.com" {
			t.Fatalf("expected domain oldbrand.com, got %q", src.Domain)
		}
	})

	t.Run("domain not found", func(t *testing.T) {
		t.Parallel()
		_, found := reg.SourceByDomain("nonexistent.com")
		if found {
			t.Fatal("expected false for non-existent domain")
		}
	})
}

func TestSourceRegistry_IsExcluded(t *testing.T) {
	t.Parallel()

	reg := validSourceRegistry()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()
		var r *SourceRegistry
		_, excluded := r.IsExcluded("foodnetwork.com")
		if excluded {
			t.Fatal("expected false on nil registry")
		}
	})

	t.Run("empty domain", func(t *testing.T) {
		t.Parallel()
		_, excluded := reg.IsExcluded("  ")
		if excluded {
			t.Fatal("expected false on empty query")
		}
	})

	t.Run("exact match", func(t *testing.T) {
		t.Parallel()
		ex, excluded := reg.IsExcluded("foodnetwork.com")
		if !excluded {
			t.Fatal("expected foodnetwork.com to be excluded")
		}
		if ex.Domain != "foodnetwork.com" {
			t.Fatalf("expected domain foodnetwork.com, got %q", ex.Domain)
		}
	})

	t.Run("www and mixed case match", func(t *testing.T) {
		t.Parallel()
		ex, excluded := reg.IsExcluded("WWW.FoodNetwork.COM")
		if !excluded {
			t.Fatal("expected WWW.FoodNetwork.COM to be excluded")
		}
		if ex.Domain != "foodnetwork.com" {
			t.Fatalf("expected domain foodnetwork.com, got %q", ex.Domain)
		}
	})

	t.Run("non-excluded domain", func(t *testing.T) {
		t.Parallel()
		_, excluded := reg.IsExcluded("seriouseats.com")
		if excluded {
			t.Fatal("expected seriouseats.com to NOT be excluded")
		}
	})
}

func TestSeedManifest_Validate(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver error", func(t *testing.T) {
		t.Parallel()
		var m *SeedManifest
		if err := m.Validate(); err == nil {
			t.Fatal("expected error on nil manifest")
		}
	})

	t.Run("valid manifest passes", func(t *testing.T) {
		t.Parallel()
		m := validSeedManifest()
		if err := m.Validate(); err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}
	})

	t.Run("version check", func(t *testing.T) {
		t.Parallel()
		for _, v := range []int{0, 2, -1} {
			m := validSeedManifest()
			m.Version = v
			if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "version") {
				t.Fatalf("expected version error for version %d, got: %v", v, err)
			}
		}
	})

	t.Run("zero recipes", func(t *testing.T) {
		t.Parallel()
		m := validSeedManifest()
		m.Recipes = nil
		if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "zero recipes") {
			t.Fatalf("expected zero recipes error, got: %v", err)
		}
	})

	t.Run("empty fields in recipe", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name   string
			modify func(*SeedEntry)
		}{
			{"empty url", func(e *SeedEntry) { e.URL = "" }},
			{"empty title", func(e *SeedEntry) { e.Title = "   " }},
			{"empty domain", func(e *SeedEntry) { e.Domain = "" }},
			{"empty cuisine", func(e *SeedEntry) { e.Cuisine = "  " }},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				m := validSeedManifest()
				tc.modify(&m.Recipes[0])
				if err := m.Validate(); err == nil {
					t.Fatalf("expected validation error for %s", tc.name)
				}
			})
		}
	})

	t.Run("invalid url scheme or malformed", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			url  string
		}{
			{"ftp scheme", "ftp://www.seriouseats.com/recipe"},
			{"malformed url", "://invalid-url"},
			{"empty host", "https:///recipe"},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				m := validSeedManifest()
				m.Recipes[0].URL = tc.url
				if err := m.Validate(); err == nil {
					t.Fatalf("expected validation error for %s", tc.name)
				}
			})
		}
	})

	t.Run("domain does not match url host", func(t *testing.T) {
		t.Parallel()
		m := validSeedManifest()
		m.Recipes[0].Domain = "allrecipes.com" // URL is https://www.seriouseats.com/...
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), "does not match URL host") {
			t.Fatalf("expected domain mismatch error, got: %v", err)
		}
	})

	t.Run("domain and url match with www normalization", func(t *testing.T) {
		t.Parallel()
		m := SeedManifest{
			Version: 1,
			Recipes: []SeedEntry{
				{
					URL:     "https://www.allrecipes.com/recipe/123/cake",
					Title:   "Cake",
					Domain:  "allrecipes.com", // normalized match with www.allrecipes.com
					Cuisine: "american",
				},
				{
					URL:     "https://bbcgoodfood.com/recipes/bread",
					Title:   "Bread",
					Domain:  "WWW.BBCGOODFOOD.COM", // normalized match with bbcgoodfood.com
					Cuisine: "british",
				},
			},
		}
		if err := m.Validate(); err != nil {
			t.Fatalf("expected www normalization to match cleanly, got: %v", err)
		}
	})

	t.Run("duplicate URLs rejected", func(t *testing.T) {
		t.Parallel()
		m := validSeedManifest()
		m.Recipes = append(m.Recipes, SeedEntry{
			URL:     m.Recipes[0].URL, // duplicate
			Title:   "Duplicate bolognese",
			Domain:  "seriouseats.com",
			Cuisine: "italian",
		})
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), "duplicate url") {
			t.Fatalf("expected duplicate url error, got: %v", err)
		}
	})
}

func TestResolveEnv(t *testing.T) {
	// Not parallel because modifying env variables
	t.Run("default paths when unset", func(t *testing.T) {
		t.Setenv("MEALIE_URL", "")
		t.Setenv("MEALIE_API_TOKEN", "")
		t.Setenv("MEALIE_FEEDER_DB", "")
		t.Setenv("MEALIE_FEEDER_SOURCES", "")
		t.Setenv("MEALIE_FEEDER_SEEDS", "")

		cfg := ResolveEnvFromOS()
		if cfg.DBPath != DefaultDBPath {
			t.Errorf("DBPath = %q; want %q", cfg.DBPath, DefaultDBPath)
		}
		if cfg.SourcesPath != DefaultSourcesPath {
			t.Errorf("SourcesPath = %q; want %q", cfg.SourcesPath, DefaultSourcesPath)
		}
		if cfg.SeedsPath != DefaultSeedsPath {
			t.Errorf("SeedsPath = %q; want %q", cfg.SeedsPath, DefaultSeedsPath)
		}
		if cfg.MealieURL != "" {
			t.Errorf("MealieURL = %q; want empty", cfg.MealieURL)
		}
		if cfg.MealieAPIToken != "" {
			t.Errorf("MealieAPIToken = %q; want empty", cfg.MealieAPIToken)
		}
	})

	t.Run("custom env overrides", func(t *testing.T) {
		t.Setenv("MEALIE_URL", "http://mealie.local:9000/")
		t.Setenv("MEALIE_API_TOKEN", "supersecret123")
		t.Setenv("MEALIE_FEEDER_DB", "/tmp/custom.db")
		t.Setenv("MEALIE_FEEDER_SOURCES", "/tmp/custom_sources.yaml")
		t.Setenv("MEALIE_FEEDER_SEEDS", "/tmp/custom_seeds.yaml")

		cfg := ResolveEnvFromOS()
		if cfg.MealieURL != "http://mealie.local:9000" {
			t.Errorf("MealieURL = %q; want http://mealie.local:9000", cfg.MealieURL)
		}
		if cfg.MealieAPIToken != "supersecret123" {
			t.Errorf("MealieAPIToken = %q; want supersecret123", cfg.MealieAPIToken)
		}
		if cfg.DBPath != "/tmp/custom.db" {
			t.Errorf("DBPath = %q; want /tmp/custom.db", cfg.DBPath)
		}
		if cfg.SourcesPath != "/tmp/custom_sources.yaml" {
			t.Errorf("SourcesPath = %q; want /tmp/custom_sources.yaml", cfg.SourcesPath)
		}
		if cfg.SeedsPath != "/tmp/custom_seeds.yaml" {
			t.Errorf("SeedsPath = %q; want /tmp/custom_seeds.yaml", cfg.SeedsPath)
		}
	})
}

func TestYAMLRoundTrip(t *testing.T) {
	t.Parallel()

	rawYAML := `
version: 1
defaults:
  tls_mode: safari_auto
  schema_mode: auto
  min_interval: 3s
  timeout: 15s
  max_retries: 4
  max_per_run: 30
excluded_domains:
  - domain: blocked.com
    reason: bot-wall
sources:
  - domain: example.com
    enabled: true
    sitemap_urls:
      - https://example.com/sitemap.xml
`
	var reg SourceRegistry
	if err := yaml.Unmarshal([]byte(rawYAML), &reg); err != nil {
		t.Fatalf("failed to unmarshal yaml: %v", err)
	}

	reg.ApplyDefaults()

	if err := reg.Validate(); err != nil {
		t.Fatalf("validation failed after unmarshal: %v", err)
	}

	if reg.Sources[0].TLSMode != TLSModeSafariAuto {
		t.Errorf("Sources[0].TLSMode = %q; want safari_auto", reg.Sources[0].TLSMode)
	}
	if reg.Sources[0].SchemaMode != SchemaModeAuto {
		t.Errorf("Sources[0].SchemaMode = %q; want auto", reg.Sources[0].SchemaMode)
	}
	if reg.Sources[0].MinInterval != 3*time.Second {
		t.Errorf("Sources[0].MinInterval = %v; want 3s", reg.Sources[0].MinInterval)
	}
	if reg.Sources[0].CanonicalHost != "example.com" {
		t.Errorf("Sources[0].CanonicalHost = %q; want example.com", reg.Sources[0].CanonicalHost)
	}
}

func TestRealGreatestHitsSeedManifest(t *testing.T) {
	t.Parallel()

	// Locate seeds/greatest-hits.yaml relative to test file or workspace root
	candidates := []string{
		"../../seeds/greatest-hits.yaml",
		"seeds/greatest-hits.yaml",
	}

	var data []byte
	var readErr error
	var foundPath string
	for _, p := range candidates {
		data, readErr = os.ReadFile(p)
		if readErr == nil {
			foundPath = p
			break
		}
	}

	if readErr != nil {
		t.Fatalf("failed to read seeds/greatest-hits.yaml: %v", readErr)
	}

	var manifest SeedManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("failed to unmarshal %s: %v", foundPath, err)
	}

	if err := manifest.Validate(); err != nil {
		t.Fatalf("real seeds/greatest-hits.yaml failed validation: %v", err)
	}

	if len(manifest.Recipes) == 0 {
		t.Fatal("expected recipes in greatest-hits.yaml, got 0")
	}

	t.Logf("Successfully validated %d curated recipes from %s", len(manifest.Recipes), filepath.Base(foundPath))
}
