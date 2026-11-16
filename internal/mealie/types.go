package mealie

import (
	"context"
	"fmt"
)

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
// Pre-conditions: Deserialized from Mealie GET /api/recipes/{slug}.
// Post-conditions: Note and Display describe the ingredient text.
type RecipeIngredient struct {
	Note    string `json:"note"`
	Display string `json:"display"`
}

// RecipeInstruction models a subset of Mealie recipeInstructions JSON fields needed for stub verification.
// Pre-conditions: Deserialized from Mealie GET /api/recipes/{slug}.
// Post-conditions: Text holds the step instruction.
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

// Error formats the APIError into a human-readable diagnostic string without exposing credentials.
// Pre-conditions: e is non-nil.
// Post-conditions: Returns string formatted with method, path, status code, and bounded detail.
func (e *APIError) Error() string {
	return fmt.Sprintf("mealie api %s %s: status %d: %s", e.Method, e.Path, e.StatusCode, e.Detail)
}

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
