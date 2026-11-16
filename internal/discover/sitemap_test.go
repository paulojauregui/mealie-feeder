package discover

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/paulojauregui/mealie-feeder/internal/fetch"
)

type mockFetcher struct {
	responses map[string]*fetch.Response
	requests  []string
}

func (m *mockFetcher) Fetch(ctx context.Context, rawURL string, opts fetch.RequestOptions) (*fetch.Response, error) {
	m.requests = append(m.requests, rawURL)
	resp, ok := m.responses[rawURL]
	if !ok {
		return nil, fmt.Errorf("mock: 404 not found for %s", rawURL)
	}
	return resp, nil
}

func gzipBytes(data []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes()
}

func TestParseRobots(t *testing.T) {
	t.Parallel()

	robotsTxt := `
# Sample robots.txt
User-agent: AhrefsBot
Crawl-delay: 12
Disallow: /

User-agent: *
Disallow: /wp-admin/
Disallow: /recipes/
Allow: /recipes/free/
Disallow: /premium/
Crawl-delay: 2.5

Sitemap: https://example.com/sitemap_index.xml
Sitemap: http:///sitemap.xml # Malformed sitemap from smittenkitchen.com
Sitemap: https://example.com/sitemap-recent.xml
`

	policy := ParseRobots([]byte(robotsTxt), "*")

	if policy.CrawlDelay() != 2500*time.Millisecond {
		t.Errorf("CrawlDelay = %v, want 2.5s", policy.CrawlDelay())
	}

	// Longest prefix check
	if policy.Allowed("/wp-admin/settings.php") {
		t.Error("expected /wp-admin/ to be disallowed")
	}
	if policy.Allowed("/recipes/secret-sauce") {
		t.Error("expected /recipes/secret-sauce to be disallowed")
	}
	// Allow: /recipes/free/ beats Disallow: /recipes/
	if !policy.Allowed("/recipes/free/chicken") {
		t.Error("expected /recipes/free/chicken to be allowed by longest match")
	}
	if !policy.Allowed("/public/about") {
		t.Error("expected /public/about to be allowed (no rule)")
	}

	// Sitemaps validation
	sitemaps := policy.Sitemaps()
	if len(sitemaps) != 2 {
		t.Fatalf("expected 2 valid sitemaps, got %d: %v", len(sitemaps), sitemaps)
	}
	if sitemaps[0] != "https://example.com/sitemap_index.xml" {
		t.Errorf("sitemap[0] = %q", sitemaps[0])
	}
	if sitemaps[1] != "https://example.com/sitemap-recent.xml" {
		t.Errorf("sitemap[1] = %q", sitemaps[1])
	}
}

func TestAllowAllRobotsAndFetchRobots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	allowAll := AllowAllRobots()
	if !allowAll.Allowed("/anything") || !allowAll.Allowed("/secret") {
		t.Error("AllowAllRobots should permit all paths")
	}
	if allowAll.CrawlDelay() != 0 {
		t.Errorf("AllowAllRobots CrawlDelay = %v, want 0", allowAll.CrawlDelay())
	}

	mock := &mockFetcher{
		responses: map[string]*fetch.Response{
			"https://example.com/robots.txt": {
				StatusCode: 404,
				Body:       []byte("404 Not Found"),
			},
		},
	}

	policy, err := FetchRobots(ctx, mock, "https://example.com", fetch.RequestOptions{})
	if err != nil {
		t.Fatalf("FetchRobots failed on 404: %v", err)
	}
	if !policy.Allowed("/any/path") {
		t.Error("expected permissive fallback on 404")
	}
}

func TestDiscoverSitemapURLs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	sitemapIndexXML := `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
   <sitemap>
      <loc>https://example.com/2026-Q1-recipe.xml</loc>
   </sitemap>
   <sitemap>
      <loc>https://example.com/2026-Q1-post.xml</loc>
   </sitemap>
   <sitemap>
      <loc>https://example.com/2026-Q2-recipe.xml.gz</loc>
   </sitemap>
   <sitemap>
      <loc>https://example.com/cycle.xml</loc>
   </sitemap>
</sitemapindex>`

	q1RecipeXML := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
   <url>
      <loc>https://example.com/recipes/pasta</loc>
      <lastmod>2026-02-15T10:00:00Z</lastmod>
   </url>
   <url>
      <loc>https://example.com/recipes/undated</loc>
   </url>
</urlset>`

	q2RecipeGzXML := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
   <url>
      <loc>https://example.com/recipes/tacos</loc>
      <lastmod>2026-04-01</lastmod>
   </url>
   <url>
      <loc>https://example.com/recipes/curry</loc>
      <lastmod>2026-05-10T14:30:00+00:00</lastmod>
   </url>
</urlset>`

	cycleXML := `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
   <sitemap>
      <loc>https://example.com/sitemap_index.xml</loc>
   </sitemap>
</sitemapindex>`

	mock := &mockFetcher{
		responses: map[string]*fetch.Response{
			"https://example.com/sitemap_index.xml": {
				StatusCode: http.StatusOK,
				Body:       []byte(sitemapIndexXML),
			},
			"https://example.com/2026-Q1-recipe.xml": {
				StatusCode: http.StatusOK,
				Body:       []byte(q1RecipeXML),
			},
			"https://example.com/2026-Q2-recipe.xml.gz": {
				StatusCode: http.StatusOK,
				Body:       gzipBytes([]byte(q2RecipeGzXML)),
			},
			"https://example.com/cycle.xml": {
				StatusCode: http.StatusOK,
				Body:       []byte(cycleXML),
			},
		},
	}

	opts := SitemapOptions{
		IncludePatterns: []string{`\d{4}-Q[1-4]-recipe\.xml(\.gz)?$`, `cycle\.xml$`},
		MaxDepth:        3,
	}

	entries, err := DiscoverSitemapURLs(ctx, mock, []string{"https://example.com/sitemap_index.xml"}, opts)
	if err != nil {
		t.Fatalf("DiscoverSitemapURLs failed: %v", err)
	}

	// Verify post sitemap was pruned before fetching
	for _, req := range mock.requests {
		if req == "https://example.com/2026-Q1-post.xml" {
			t.Errorf("unwanted sitemap %s was fetched!", req)
		}
	}

	// Should discover 4 URLs: curry (May 10), tacos (Apr 1), pasta (Feb 15), undated (zero LastMod)
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	// Verify descending order by LastMod
	if entries[0].URL != "https://example.com/recipes/curry" {
		t.Errorf("entries[0] = %s, want curry (newest May 10)", entries[0].URL)
	}
	if entries[1].URL != "https://example.com/recipes/tacos" {
		t.Errorf("entries[1] = %s, want tacos (Apr 1)", entries[1].URL)
	}
	if entries[2].URL != "https://example.com/recipes/pasta" {
		t.Errorf("entries[2] = %s, want pasta (Feb 15)", entries[2].URL)
	}
	if entries[3].URL != "https://example.com/recipes/undated" {
		t.Errorf("entries[3] = %s, want undated (zero time)", entries[3].URL)
	}
	if !entries[3].LastMod.IsZero() {
		t.Errorf("expected zero LastMod for undated entry, got %v", entries[3].LastMod)
	}
}

func TestDiscoverSitemapLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Decompression size cap
	oversizedData := make([]byte, 5000)
	gzipped := gzipBytes(oversizedData)

	mock := &mockFetcher{
		responses: map[string]*fetch.Response{
			"https://example.com/oversized.xml.gz": {
				StatusCode: http.StatusOK,
				Body:       gzipped,
			},
		},
	}

	opts := SitemapOptions{
		MaxDecompressedBytes: 100, // smaller than 5000
	}

	_, err := DiscoverSitemapURLs(ctx, mock, []string{"https://example.com/oversized.xml.gz"}, opts)
	if err == nil {
		t.Fatal("expected error for oversized decompressed payload, got nil")
	}
}
