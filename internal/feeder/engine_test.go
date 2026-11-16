package feeder

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/paulojauregui/mealie-feeder/internal/config"
	"github.com/paulojauregui/mealie-feeder/internal/fetch"
	"github.com/paulojauregui/mealie-feeder/internal/ledger"
	"github.com/paulojauregui/mealie-feeder/internal/mealie"
)

type mockFetcher struct {
	responses map[string]*fetch.Response
	errs      map[string]error
}

func (m *mockFetcher) Fetch(ctx context.Context, rawURL string, opts fetch.RequestOptions) (*fetch.Response, error) {
	if err, ok := m.errs[rawURL]; ok {
		return nil, err
	}
	if resp, ok := m.responses[rawURL]; ok {
		return resp, nil
	}
	return &fetch.Response{
		StatusCode: http.StatusOK,
		FinalURL:   rawURL,
		Body:       []byte("<html><p>Empty</p></html>"),
	}, nil
}

type mockMealie struct {
	summaries   []mealie.RecipeSummary
	createCount int32
	createdSlug string
	deleteCount int32
	recipe      *mealie.RecipeDetail
}

func (m *mockMealie) About(ctx context.Context) (*mealie.AboutResponse, error) {
	return &mealie.AboutResponse{Version: "v3.25.1", Production: true}, nil
}

func (m *mockMealie) ListAllRecipeURLs(ctx context.Context, perPage int) ([]mealie.RecipeSummary, error) {
	return m.summaries, nil
}

func (m *mockMealie) CreateFromHTML(ctx context.Context, req mealie.CreateFromHTMLRequest) (string, error) {
	atomic.AddInt32(&m.createCount, 1)
	if m.createdSlug != "" {
		return m.createdSlug, nil
	}
	return "test-slug", nil
}

func (m *mockMealie) TestScrapeURL(ctx context.Context, rawURL string) (*mealie.RecipeDetail, error) {
	return m.recipe, nil
}

func (m *mockMealie) CreateFromURL(ctx context.Context, req mealie.CreateFromURLRequest) (string, error) {
	atomic.AddInt32(&m.createCount, 1)
	return "test-url-slug", nil
}

func (m *mockMealie) GetRecipe(ctx context.Context, slug string) (*mealie.RecipeDetail, error) {
	return m.recipe, nil
}

func (m *mockMealie) DeleteRecipe(ctx context.Context, slug string) error {
	atomic.AddInt32(&m.deleteCount, 1)
	return nil
}

func TestEngineSeed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	validRecipeHTML := `
<script type="application/ld+json">
{
  "@type": "Recipe",
  "name": "Chicken Curry",
  "recipeIngredient": ["chicken", "curry paste"],
  "recipeInstructions": ["cook chicken", "simmer with paste"]
}
</script>`

	nonRecipeHTML := `<html><h1>Blog Post</h1><p>Not a recipe</p></html>`

	store, err := ledger.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer store.Close()

	registry := &config.SourceRegistry{
		Version: 1,
		Sources: []config.SourceConfig{
			{
				Domain:     "recipetineats.com",
				Enabled:    true,
				TLSMode:    config.TLSModeChromeAuto,
				SchemaMode: config.SchemaModeJSONLD,
				MaxPerRun:  10,
			},
		},
	}
	registry.ApplyDefaults()

	seeds := &config.SeedManifest{
		Version: 1,
		Recipes: []config.SeedEntry{
			{
				URL:     "https://www.recipetineats.com/chicken-curry",
				Title:   "Chicken Curry",
				Domain:  "recipetineats.com",
				Cuisine: "Indian",
			},
			{
				URL:     "https://www.recipetineats.com/non-recipe-article",
				Title:   "Kitchen Tips",
				Domain:  "recipetineats.com",
				Cuisine: "General",
			},
			{
				URL:     "https://www.recipetineats.com/challenge-page",
				Title:   "Blocked Page",
				Domain:  "recipetineats.com",
				Cuisine: "General",
			},
		},
	}

	fetcher := &mockFetcher{
		responses: map[string]*fetch.Response{
			"https://www.recipetineats.com/chicken-curry": {
				StatusCode: http.StatusOK,
				FinalURL:   "https://www.recipetineats.com/chicken-curry",
				Body:       []byte(validRecipeHTML),
			},
			"https://www.recipetineats.com/non-recipe-article": {
				StatusCode: http.StatusOK,
				FinalURL:   "https://www.recipetineats.com/non-recipe-article",
				Body:       []byte(nonRecipeHTML),
			},
		},
		errs: map[string]error{
			"https://www.recipetineats.com/challenge-page": fetch.ErrChallengeDetected,
		},
	}

	mealieMock := &mockMealie{
		createdSlug: "chicken-curry",
	}

	deps := Dependencies{
		Registry: registry,
		Seeds:    seeds,
		Store:    store,
		Fetcher:  fetcher,
		Mealie:   mealieMock,
	}

	engine, err := NewEngine(deps, nil)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	report, err := engine.Seed(ctx, SeedOptions{})
	if err != nil {
		t.Fatalf("Seed failed: %v", err)
	}

	if report.Imported != 1 {
		t.Errorf("Imported = %d, want 1", report.Imported)
	}
	if report.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2 (1 non-recipe + 1 challenge)", report.Skipped)
	}
	if mealieMock.createCount != 1 {
		t.Errorf("mealie.createCount = %d, want 1", mealieMock.createCount)
	}

	// Verify imported record in ledger
	rec, ok, _ := store.GetByCanonicalURL(ctx, "https://www.recipetineats.com/chicken-curry")
	if !ok || rec.State != ledger.StateImported || rec.MealieSlug != "chicken-curry" {
		t.Errorf("unexpected record state: %+v", rec)
	}

	// Verify non-recipe record in ledger
	nonRec, ok, _ := store.GetByCanonicalURL(ctx, "https://www.recipetineats.com/non-recipe-article")
	if !ok || nonRec.State != ledger.StateSkippedNonRecipe {
		t.Errorf("unexpected non-recipe state: %+v", nonRec)
	}

	// Verify challenge record in ledger
	challRec, ok, _ := store.GetByCanonicalURL(ctx, "https://www.recipetineats.com/challenge-page")
	if !ok || challRec.State != ledger.StateSkippedPaywall {
		t.Errorf("unexpected challenge state: %+v", challRec)
	}
}

func TestEngineSeedDryRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store, _ := ledger.OpenSQLite(ctx, ":memory:")
	defer store.Close()

	registry := &config.SourceRegistry{
		Version: 1,
		Sources: []config.SourceConfig{
			{Domain: "example.com", Enabled: true},
		},
	}
	registry.ApplyDefaults()

	seeds := &config.SeedManifest{
		Version: 1,
		Recipes: []config.SeedEntry{
			{URL: "https://example.com/dry-1", Domain: "example.com", Title: "Dry 1", Cuisine: "Test"},
		},
	}

	mealieMock := &mockMealie{}
	engine, _ := NewEngine(Dependencies{
		Registry: registry,
		Seeds:    seeds,
		Store:    store,
		Fetcher:  &mockFetcher{},
		Mealie:   mealieMock,
	}, nil)

	report, err := engine.Seed(ctx, SeedOptions{DryRun: true})
	if err != nil {
		t.Fatalf("DryRun Seed failed: %v", err)
	}

	if mealieMock.createCount != 0 {
		t.Errorf("DryRun should not call Mealie CreateFromHTML: count=%d", mealieMock.createCount)
	}
	if report.Imported != 0 {
		t.Errorf("DryRun report.Imported = %d, want 0", report.Imported)
	}
}

func TestEngineSyncAndDomainIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	validRecipeHTML := `
<script type="application/ld+json">
{
  "@type": "Recipe",
  "name": "Healthy Soup",
  "recipeIngredient": ["veg", "water"],
  "recipeInstructions": ["boil"]
}
</script>`

	rssXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Blog</title>
    <item>
      <title>Healthy Soup</title>
      <link>https://healthy.com/recipes/soup-1</link>
    </item>
  </channel>
</rss>`

	store, _ := ledger.OpenSQLite(ctx, ":memory:")
	defer store.Close()

	registry := &config.SourceRegistry{
		Version: 1,
		Sources: []config.SourceConfig{
			{
				Domain:        "healthy.com",
				Enabled:       true,
				RSSURLs:       []string{"https://healthy.com/feed/"},
				AllowPatterns: []string{`^https?://healthy\.com/recipes/`},
			},
			{
				Domain:        "failing.com",
				Enabled:       true,
				RSSURLs:       []string{"https://failing.com/feed/"},
				AllowPatterns: []string{`^https?://failing\.com/recipes/`},
			},
		},
	}
	registry.ApplyDefaults()

	fetcher := &mockFetcher{
		responses: map[string]*fetch.Response{
			"https://healthy.com/robots.txt": {
				StatusCode: http.StatusOK,
				Body:       []byte("User-agent: *\nAllow: /\n"),
			},
			"https://healthy.com/feed/": {
				StatusCode: http.StatusOK,
				Body:       []byte(rssXML),
			},
			"https://healthy.com/recipes/soup-1": {
				StatusCode: http.StatusOK,
				FinalURL:   "https://healthy.com/recipes/soup-1",
				Body:       []byte(validRecipeHTML),
			},
		},
		errs: map[string]error{
			"https://failing.com/robots.txt": fetch.ErrChallengeDetected,
			"https://failing.com/feed/":      fetch.ErrChallengeDetected,
		},
	}

	mealieMock := &mockMealie{createdSlug: "healthy-soup"}

	engine, err := NewEngine(Dependencies{
		Registry: registry,
		Store:    store,
		Fetcher:  fetcher,
		Mealie:   mealieMock,
	}, nil)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	report, err := engine.Sync(ctx, SyncOptions{MaxPerRun: 10, Workers: 2})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// healthy.com should succeed despite failing.com error
	if report.Imported != 1 {
		t.Errorf("Imported = %d, want 1", report.Imported)
	}
	if report.ByDomain["healthy.com"].Imported != 1 {
		t.Errorf("healthy.com imported = %d, want 1", report.ByDomain["healthy.com"].Imported)
	}
}

func TestEngineInspectAndStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	recipeHTML := `
<script type="application/ld+json">
{
  "@type": "Recipe",
  "name": "Apple Pie",
  "recipeIngredient": ["apples", "crust"],
  "recipeInstructions": ["bake"]
}
</script>`

	store, _ := ledger.OpenSQLite(ctx, ":memory:")
	defer store.Close()

	registry := &config.SourceRegistry{Version: 1}
	registry.ApplyDefaults()

	fetcher := &mockFetcher{
		responses: map[string]*fetch.Response{
			"https://example.com/pie": {
				StatusCode: http.StatusOK,
				FinalURL:   "https://example.com/pie",
				Body:       []byte(recipeHTML),
			},
		},
	}

	mealieMock := &mockMealie{
		summaries: []mealie.RecipeSummary{{ID: "1", Slug: "pie", OrgURL: "https://example.com/pie"}},
		recipe:    &mealie.RecipeDetail{Name: "Scraped Pie", Ingredients: []mealie.RecipeIngredient{{Display: "apples"}}},
	}

	engine, _ := NewEngine(Dependencies{
		Registry: registry,
		Store:    store,
		Fetcher:  fetcher,
		Mealie:   mealieMock,
	}, nil)

	// Inspect test
	insp, err := engine.Inspect(ctx, InspectOptions{URL: "https://example.com/pie", TestScrape: true})
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if insp.Preflight.Verdict != "valid" {
		t.Errorf("preflight verdict = %q, want valid", insp.Preflight.Verdict)
	}
	if insp.TestScrape == "" {
		t.Error("expected non-empty TestScrape report")
	}

	// Status test
	stat, err := engine.Status(ctx, true)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if stat.MealieTotal != 1 {
		t.Errorf("MealieTotal = %d, want 1", stat.MealieTotal)
	}
	if stat.Reconciled != 1 {
		t.Errorf("Reconciled = %d, want 1", stat.Reconciled)
	}
}
