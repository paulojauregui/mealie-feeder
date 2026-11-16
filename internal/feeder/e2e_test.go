package feeder_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paulojauregui/mealie-feeder/internal/config"
	"github.com/paulojauregui/mealie-feeder/internal/feeder"
	"github.com/paulojauregui/mealie-feeder/internal/fetch"
	"github.com/paulojauregui/mealie-feeder/internal/ledger"
	"github.com/paulojauregui/mealie-feeder/internal/mealie"
)

func TestEndToEndPipeline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var mealieCreateCount int32
	var originFetchCount int32

	// 1. Setup Mock Food Blog Server
	var blogURL string
	blogMux := http.NewServeMux()

	carbonaraHTML := `
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "Recipe",
  "name": "Classic Pasta Carbonara",
  "recipeIngredient": ["200g spaghetti", "100g guanciale", "2 eggs", "pecorino"],
  "recipeInstructions": ["Boil pasta", "Fry guanciale", "Mix with eggs and cheese"]
}
</script>`

	tacosHTML := `
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "Recipe",
  "name": "Street Style Tacos",
  "recipeIngredient": ["corn tortillas", "carne asada", "cilantro", "onion"],
  "recipeInstructions": ["Grill meat", "Warm tortillas", "Assemble with cilantro and onion"]
}
</script>`

	curryHTML := `
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "Recipe",
  "name": "Pre-existing Curry",
  "recipeIngredient": ["chicken", "curry sauce"],
  "recipeInstructions": ["Simmer together"]
}
</script>`

	roundupHTML := `<html><head><title>Top 10 Pastas</title></head><body><h1>Best Pastas</h1><p>No schema here</p></body></html>`

	blogMux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "User-agent: *\nAllow: /\nSitemap: %s/sitemap_index.xml\n", blogURL)
	})

	blogMux.HandleFunc("/sitemap_index.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
   <sitemap><loc>%s/post-sitemap.xml</loc></sitemap>
   <sitemap><loc>%s/non-recipe-sitemap.xml</loc></sitemap>
</sitemapindex>`, blogURL, blogURL)
	})

	blogMux.HandleFunc("/post-sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
   <url><loc>%s/recipe/pasta-carbonara</loc><lastmod>2026-03-01</lastmod></url>
</urlset>`, blogURL)
	})

	blogMux.HandleFunc("/non-recipe-sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
   <url><loc>%s/gear/best-pans</loc></url>
</urlset>`, blogURL)
	})

	blogMux.HandleFunc("/feed/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Blog</title>
    <item>
      <title>Street Style Tacos</title>
      <link>%s/recipe/fresh-tacos</link>
      <pubDate>Mon, 15 Nov 2026 12:00:00 GMT</pubDate>
    </item>
    <item>
      <title>Top 10 Pastas</title>
      <link>%s/roundup/top-10</link>
      <pubDate>Mon, 15 Nov 2026 10:00:00 GMT</pubDate>
    </item>
  </channel>
</rss>`, blogURL, blogURL)
	})

	blogMux.HandleFunc("/recipe/pasta-carbonara", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&originFetchCount, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(carbonaraHTML))
	})

	blogMux.HandleFunc("/recipe/fresh-tacos", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&originFetchCount, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(tacosHTML))
	})

	blogMux.HandleFunc("/recipe/existing-curry", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&originFetchCount, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(curryHTML))
	})

	blogMux.HandleFunc("/roundup/top-10", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&originFetchCount, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(roundupHTML))
	})

	blogMux.HandleFunc("/recipe/paywall", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&originFetchCount, 1)
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Cloudflare challenge page"))
	})

	blogSrv := httptest.NewServer(blogMux)
	defer blogSrv.Close()
	blogURL = blogSrv.URL

	parsedBlogURL, _ := url.Parse(blogURL)
	blogHost := parsedBlogURL.Host
	blogHostname := parsedBlogURL.Hostname()

	// 2. Setup Mock Mealie Server
	var mealieRecipesMu sync.Mutex
	mealieRecipes := []mealie.RecipeSummary{
		{
			ID:     "100",
			Slug:   "pre-existing-curry",
			Name:   "Pre-existing Curry",
			OrgURL: blogURL + "/recipe/existing-curry",
		},
	}

	mealieMux := http.NewServeMux()
	mealieMux.HandleFunc("/api/app/about", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(mealie.AboutResponse{
			Version:    "v3.25.1",
			Production: true,
		})
	})

	mealieMux.HandleFunc("/api/recipes", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mealieRecipesMu.Lock()
		defer mealieRecipesMu.Unlock()

		_ = json.NewEncoder(w).Encode(mealie.RecipeListPage{
			Items:      mealieRecipes,
			Page:       1,
			PerPage:    50,
			Total:      len(mealieRecipes),
			TotalPages: 1,
		})
	})

	mealieMux.HandleFunc("/api/recipes/create/html-or-json", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&mealieCreateCount, 1)

		var req mealie.CreateFromHTMLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if !req.IncludeTags || !req.IncludeCategories || req.Data == "" || req.URL == "" {
			http.Error(w, "missing required fields", http.StatusBadRequest)
			return
		}

		slug := "imported-" + strings.TrimPrefix(req.URL, blogURL+"/recipe/")
		mealieRecipesMu.Lock()
		mealieRecipes = append(mealieRecipes, mealie.RecipeSummary{
			ID:     fmt.Sprintf("%d", len(mealieRecipes)+1),
			Slug:   slug,
			OrgURL: req.URL,
		})
		mealieRecipesMu.Unlock()

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(slug)
	})

	mealieSrv := httptest.NewServer(mealieMux)
	defer mealieSrv.Close()

	// 3. Assemble Components
	store, err := ledger.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer store.Close()

	registry := &config.SourceRegistry{
		Version: 1,
		Defaults: config.DefaultsConfig{
			MinInterval: time.Millisecond,
			Timeout:     5 * time.Second,
		},
		Sources: []config.SourceConfig{
			{
				Domain:                 blogHostname,
				CanonicalHost:          blogHost,
				Enabled:                true,
				TLSMode:                config.TLSModePlain,
				SchemaMode:             config.SchemaModeJSONLD,
				MinInterval:            time.Millisecond,
				Timeout:                5 * time.Second,
				MaxRetries:             1,
				MaxPerRun:              10,
				RSSURLs:                []string{blogURL + "/feed/"},
				SitemapURLs:            []string{blogURL + "/sitemap_index.xml"},
				SitemapIncludePatterns: []string{`post-sitemap\.xml$`},
				AllowPatterns:          []string{`^` + blogURL + `/(recipe|roundup)/`},
				DenyPatterns:           []string{`^` + blogURL + `/gear/`},
			},
		},
	}
	registry.ApplyDefaults()

	seeds := &config.SeedManifest{
		Version: 1,
		Recipes: []config.SeedEntry{
			{
				URL:     blogURL + "/recipe/existing-curry",
				Title:   "Pre-existing Curry",
				Domain:  blogHostname,
				Cuisine: "Indian",
			},
			{
				URL:     blogURL + "/recipe/paywall",
				Title:   "Paywall Item",
				Domain:  blogHostname,
				Cuisine: "General",
			},
		},
	}

	limiter := fetch.NewDomainLimiter(0, nil)
	fetcher := fetch.NewClientWithTransport(limiter, blogSrv.Client().Transport, nil)

	mealieClient, err := mealie.NewHTTPClient(mealieSrv.URL, "test-token", mealieSrv.Client())
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	engine, err := feeder.NewEngine(feeder.Dependencies{
		Registry: registry,
		Seeds:    seeds,
		Store:    store,
		Fetcher:  fetcher,
		Mealie:   mealieClient,
		Limiter:  limiter,
	}, nil)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	// 4. Execution Pass 1: Seed
	seedReport, err := engine.Seed(ctx, feeder.SeedOptions{})
	if err != nil {
		t.Fatalf("Seed failed: %v", err)
	}

	if seedReport.Reconciled != 1 {
		t.Errorf("Seed Reconciled = %d, want 1 (pre-existing curry)", seedReport.Reconciled)
	}
	if seedReport.Skipped != 1 {
		t.Errorf("Seed Skipped = %d, want 1 (paywall)", seedReport.Skipped)
	}

	// 5. Execution Pass 1: Sync (traversing RSS + Sitemaps)
	syncReport, err := engine.Sync(ctx, feeder.SyncOptions{IncludeSitemaps: true})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if syncReport.Imported != 2 {
		t.Errorf("Sync Imported = %d, want 2 (fresh-tacos from RSS, pasta-carbonara from sitemap)", syncReport.Imported)
	}
	if syncReport.Skipped != 1 {
		t.Errorf("Sync Skipped = %d, want 1 (top-10 roundup non-recipe)", syncReport.Skipped)
	}

	// Total Mealie creates should be 2 (fresh-tacos and pasta-carbonara; existing-curry was reconciled)
	if atomic.LoadInt32(&mealieCreateCount) != 2 {
		t.Errorf("total mealieCreateCount = %d, want 2", mealieCreateCount)
	}

	// 6. Execution Pass 2: Immediate Idempotent Sync
	createsBeforePass2 := atomic.LoadInt32(&mealieCreateCount)
	originFetchesBeforePass2 := atomic.LoadInt32(&originFetchCount)

	syncReport2, err := engine.Sync(ctx, feeder.SyncOptions{IncludeSitemaps: true})
	if err != nil {
		t.Fatalf("Second Sync failed: %v", err)
	}

	if syncReport2.Imported != 0 {
		t.Errorf("Second Sync Imported = %d, want 0 (idempotent)", syncReport2.Imported)
	}
	if atomic.LoadInt32(&mealieCreateCount) != createsBeforePass2 {
		t.Errorf("Second Sync should not invoke Mealie CreateFromHTML: %d vs %d", mealieCreateCount, createsBeforePass2)
	}
	if atomic.LoadInt32(&originFetchCount) != originFetchesBeforePass2 {
		t.Errorf("Second Sync should not re-fetch origin recipe pages: %d vs %d", originFetchCount, originFetchesBeforePass2)
	}
}
