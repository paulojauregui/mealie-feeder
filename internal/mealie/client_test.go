package mealie

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewHTTPClient(t *testing.T) {
	t.Parallel()

	// Empty baseURL fails
	if _, err := NewHTTPClient("", "secret", nil); err == nil {
		t.Error("expected error for empty baseURL")
	}

	// Invalid scheme fails
	if _, err := NewHTTPClient("ftp://mealie.local", "secret", nil); err == nil {
		t.Error("expected error for non-http scheme")
	}

	// Empty token fails
	if _, err := NewHTTPClient("http://mealie.local:9000", "", nil); err == nil {
		t.Error("expected error for empty token")
	}

	// Valid creation trims trailing slash
	c, err := NewHTTPClient("http://mealie.local:9000/", "secret123", nil)
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}
	if c.baseURL != "http://mealie.local:9000" {
		t.Errorf("baseURL = %q, want http://mealie.local:9000", c.baseURL)
	}
}

func TestAbout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/app/about" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AboutResponse{
			Version:    "v3.25.1",
			Production: true,
		})
	}))
	defer srv.Close()

	client, err := NewHTTPClient(srv.URL, "test-token-123", srv.Client())
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	about, err := client.About(ctx)
	if err != nil {
		t.Fatalf("About failed: %v", err)
	}
	if about.Version != "v3.25.1" || !about.Production {
		t.Errorf("About = %+v, want v3.25.1 production", about)
	}
}

func TestListAllRecipeURLs_Pagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipes" {
			http.NotFound(w, r)
			return
		}
		page := r.URL.Query().Get("page")

		var res RecipeListPage
		switch page {
		case "1":
			res = RecipeListPage{
				Page:       1,
				PerPage:    2,
				Total:      5,
				TotalPages: 3,
				Items: []RecipeSummary{
					{ID: "1", Slug: "recipe-1", OrgURL: "https://example.com/1"},
					{ID: "2", Slug: "recipe-2", OrgURL: "https://example.com/2"},
				},
			}
		case "2":
			res = RecipeListPage{
				Page:       2,
				PerPage:    2,
				Total:      5,
				TotalPages: 3,
				Items: []RecipeSummary{
					{ID: "3", Slug: "recipe-3", OrgURL: "https://example.com/3"},
					{ID: "4", Slug: "recipe-4", OrgURL: "https://example.com/4"},
				},
			}
		case "3":
			res = RecipeListPage{
				Page:       3,
				PerPage:    2,
				Total:      5,
				TotalPages: 3,
				Items: []RecipeSummary{
					{ID: "5", Slug: "recipe-5", OrgURL: "https://example.com/5"},
				},
			}
		default:
			http.Error(w, "invalid page", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer srv.Close()

	client, err := NewHTTPClient(srv.URL, "token", srv.Client())
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	items, err := client.ListAllRecipeURLs(ctx, 2)
	if err != nil {
		t.Fatalf("ListAllRecipeURLs failed: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("expected 5 items, got %d", len(items))
	}
	if items[4].Slug != "recipe-5" {
		t.Errorf("items[4] slug = %q, want recipe-5", items[4].Slug)
	}

	// Empty instance test with total_pages=0
	emptySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(RecipeListPage{
			Page:       1,
			PerPage:    50,
			Total:      0,
			TotalPages: 0,
			Items:      nil,
		})
	}))
	defer emptySrv.Close()

	emptyClient, _ := NewHTTPClient(emptySrv.URL, "token", emptySrv.Client())
	emptyItems, err := emptyClient.ListAllRecipeURLs(ctx, 50)
	if err != nil {
		t.Fatalf("empty ListAllRecipeURLs failed: %v", err)
	}
	if len(emptyItems) != 0 {
		t.Errorf("expected 0 items, got %d", len(emptyItems))
	}
}

func TestCreateFromHTML_SuccessAndError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipes/create/html-or-json" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req CreateFromHTMLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if req.URL == "https://example.com/fail-422" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"detail":"could not parse recipe"}`))
			return
		}

		if req.Data == "" || req.URL == "" || !req.IncludeTags || !req.IncludeCategories {
			http.Error(w, "missing required fields", http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`"classic-carbonara"`))
	}))
	defer srv.Close()

	client, err := NewHTTPClient(srv.URL, "super-secret-token", srv.Client())
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	// 1. Success case
	slug, err := client.CreateFromHTML(ctx, CreateFromHTMLRequest{
		Data:              "<html>...</html>",
		URL:               "https://example.com/carbonara",
		IncludeTags:       true,
		IncludeCategories: true,
	})
	if err != nil {
		t.Fatalf("CreateFromHTML failed: %v", err)
	}
	if slug != "classic-carbonara" {
		t.Errorf("slug = %q, want classic-carbonara", slug)
	}

	// 2. 422 Unprocessable Entity mapped to APIError without credentials
	_, err = client.CreateFromHTML(ctx, CreateFromHTMLRequest{
		Data:              "<html>broken</html>",
		URL:               "https://example.com/fail-422",
		IncludeTags:       true,
		IncludeCategories: true,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("StatusCode = %d, want 422", apiErr.StatusCode)
	}
	if strings.Contains(apiErr.Error(), "super-secret-token") {
		t.Error("APIError must NEVER leak API token!")
	}
}

func TestRecipeOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/recipes/test-scrape-url" && r.Method == http.MethodPost:
			var req TestScrapeRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(RecipeDetail{
				Name:        "Test Scrape Recipe",
				Description: "Preview description",
				Ingredients: []RecipeIngredient{{Display: "1 cup milk"}},
			})

		case r.URL.Path == "/api/recipes/create/url" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`"url-scraped-slug"`))

		case r.URL.Path == "/api/recipes/apple-pie" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(RecipeDetail{
				Slug: "apple-pie",
				Name: "Apple Pie",
			})

		case r.URL.Path == "/api/recipes/apple-pie" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewHTTPClient(srv.URL, "token", srv.Client())
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	// TestScrapeURL
	detail, err := client.TestScrapeURL(ctx, "https://example.com/recipe")
	if err != nil {
		t.Fatalf("TestScrapeURL failed: %v", err)
	}
	if detail.Name != "Test Scrape Recipe" {
		t.Errorf("detail.Name = %q", detail.Name)
	}

	// CreateFromURL
	slug, err := client.CreateFromURL(ctx, CreateFromURLRequest{
		URL:               "https://example.com/recipe",
		IncludeTags:       true,
		IncludeCategories: true,
	})
	if err != nil {
		t.Fatalf("CreateFromURL failed: %v", err)
	}
	if slug != "url-scraped-slug" {
		t.Errorf("slug = %q, want url-scraped-slug", slug)
	}

	// GetRecipe
	rec, err := client.GetRecipe(ctx, "apple-pie")
	if err != nil {
		t.Fatalf("GetRecipe failed: %v", err)
	}
	if rec.Slug != "apple-pie" {
		t.Errorf("rec.Slug = %q, want apple-pie", rec.Slug)
	}

	// DeleteRecipe
	if err := client.DeleteRecipe(ctx, "apple-pie"); err != nil {
		t.Fatalf("DeleteRecipe failed: %v", err)
	}
}
