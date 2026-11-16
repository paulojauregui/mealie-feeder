# M09 Specification: Typed Mealie v3.25.1 REST Client

## 1. Objective & Target Files

Implement the typed HTTP REST client for Mealie `v3.25.1` in `internal/mealie`, covering readiness checks, paginated `orgURL` inventory retrieval, single-fetch HTML ingestion (`create/html-or-json`), optional `test-scrape-url` / `create/url` fallback, and post-ingest recipe inspection/cleanup.

**Target Files (strict scope):**
1. `internal/mealie/types.go`
2. `internal/mealie/client.go`
3. `internal/mealie/client_test.go`

---

## 2. Package Contract (`package mealie`)

### 2.1 API Types & Client Interface (`internal/mealie/types.go`)

```go
package mealie

import "context"

// AboutResponse mirrors GET /api/app/about from Mealie v3.25.1.
// Pre-conditions: Returned on HTTP 200 OK.
// Post-conditions: Version exposes running Mealie release string.
type AboutResponse struct {
	Version    string `json:"version"`
	Production bool   `json:"production"`
}

// RecipeSummary represents one item returned by paginated GET /api/recipes.
// Pre-conditions: OrgURL may be empty for manually authored recipes.
// Post-conditions: Used to reconcile existing recipes against local SQLite ledger.
type RecipeSummary struct {
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	OrgURL string `json:"orgURL"`
}

// RecipeListPage models Mealie pagination envelope on GET /api/recipes.
// Pre-conditions: Page and TotalPages are 1-indexed when recipes exist (TotalPages may be 0 on empty DB).
// Post-conditions: Drives deterministic pagination stop condition.
type RecipeListPage struct {
	Items      []RecipeSummary `json:"items"`
	Page       int             `json:"page"`
	PerPage    int             `json:"per_page"`
	Total      int             `json:"total"`
	TotalPages int             `json:"total_pages"`
}

// CreateFromHTMLRequest maps to POST /api/recipes/create/html-or-json.
// Pre-conditions: Data (raw HTML) and URL (canonical permalink) must be non-empty.
// Post-conditions: Preserves includeTags and includeCategories flags to prevent Mealie bulk-scraper organizer loss.
type CreateFromHTMLRequest struct {
	Data              string `json:"data"`
	URL               string `json:"url"`
	IncludeTags       bool   `json:"includeTags"`
	IncludeCategories bool   `json:"includeCategories"`
}

// CreateFromURLRequest maps to POST /api/recipes/create/url.
// Pre-conditions: URL must be http/https; used only when AllowURLFallback is explicitly enabled.
// Post-conditions: Serializes exact JSON keys expected by Mealie v3.25.1.
type CreateFromURLRequest struct {
	URL               string `json:"url"`
	IncludeTags       bool   `json:"includeTags"`
	IncludeCategories bool   `json:"includeCategories"`
}

// TestScrapeRequest maps to POST /api/recipes/test-scrape-url.
// Pre-conditions: URL must be non-empty.
// Post-conditions: UseOpenAI should default to false in automated diagnostics.
type TestScrapeRequest struct {
	URL       string `json:"url"`
	UseOpenAI bool   `json:"useOpenai"`
}

// RecipeIngredient models a subset of Mealie recipeIngredient JSON fields needed for stub verification.
type RecipeIngredient struct {
	Note    string `json:"note"`
	Display string `json:"display"`
}

// RecipeInstruction models a subset of Mealie recipeInstructions JSON fields needed for stub verification.
type RecipeInstruction struct {
	Text string `json:"text"`
}

// RecipeDetail models GET /api/recipes/{slug} and test-scrape-url payloads for stub detection.
// Pre-conditions: Returned on HTTP 200 OK.
// Post-conditions: Allows OpenGraph stub verification via preflight.IsOpenGraphStub.
type RecipeDetail struct {
	ID           string              `json:"id"`
	Slug         string              `json:"slug"`
	Name         string              `json:"name"`
	OrgURL       string              `json:"orgURL"`
	Description  string              `json:"description"`
	Ingredients  []RecipeIngredient  `json:"recipeIngredient"`
	Instructions []RecipeInstruction `json:"recipeInstructions"`
}

// APIError captures non-2xx HTTP status and bounded response message from Mealie.
// Pre-conditions: StatusCode >= 400.
// Post-conditions: Never exposes Bearer token in Error() output.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Detail     string
}

func (e *APIError) Error() string

// Client defines Mealie v3.25.1 operations required by internal/feeder.
// Pre-conditions: Implementations must inject Authorization: Bearer <token> header cleanly.
// Post-conditions: Safe for concurrent invocations.
type Client interface {
	About(ctx context.Context) (*AboutResponse, error)
	ListAllRecipeURLs(ctx context.Context, perPage int) ([]RecipeSummary, error)
	CreateFromHTML(ctx context.Context, req CreateFromHTMLRequest) (slug string, err error)
	TestScrapeURL(ctx context.Context, rawURL string) (*RecipeDetail, error)
	CreateFromURL(ctx context.Context, req CreateFromURLRequest) (slug string, err error)
	GetRecipe(ctx context.Context, slug string) (*RecipeDetail, error)
	DeleteRecipe(ctx context.Context, slug string) error
}
```

### 2.2 HTTP Implementation (`internal/mealie/client.go`)

```go
// HTTPClient implements Client over standard net/http against a Mealie v3.25.1 server.
// Pre-conditions: Constructed via NewHTTPClient.
// Post-conditions: Never leaks Authorization token in error messages or logs; bounds error body reads to 4 KiB.
type HTTPClient struct {
	// unexported fields
}

// NewHTTPClient creates an HTTPClient targeting baseURL with Bearer token and optional *http.Client (nil uses 60s default timeout).
// Pre-conditions: baseURL must be valid http/https URL; token must be non-empty.
// Post-conditions: Trims trailing "/" from baseURL cleanly.
func NewHTTPClient(baseURL, token string, httpClient *http.Client) (*HTTPClient, error)
```

---

## 3. Edge Cases & Test Plan (`internal/mealie/client_test.go`)

1. `NewHTTPClient` validates `baseURL` and non-empty `token`.
2. `About` decodes `GET /api/app/about` and verifies `Authorization: Bearer ...` header.
3. `ListAllRecipeURLs` paginates across 3 pages (`page=1..3`, `perPage=2`), handles `total_pages=0` on empty Mealie instance without infinite loops, and stops cleanly.
4. `CreateFromHTML` posts exact JSON payload (`data`, `url`, `includeTags: true`, `includeCategories: true`), parses quoted JSON string slug `"classic-carbonara"` (HTTP 201), and maps `400`/`401`/`422`/`500` to `*APIError` without leaking Bearer token.
5. `TestScrapeURL`, `CreateFromURL`, `GetRecipe` (URL-escaped slug), and `DeleteRecipe` tested against `httptest.NewServer`.

---

## 4. Verification Command

```bash
go test -v -race -cover ./internal/mealie
```
