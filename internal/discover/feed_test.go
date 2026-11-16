package discover

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/paulojauregui/mealie-feeder/internal/config"
	"github.com/paulojauregui/mealie-feeder/internal/fetch"
)

func TestURLFilter(t *testing.T) {
	t.Parallel()

	robotsTxt := `
User-agent: *
Disallow: /wp-admin/
Disallow: /blocked-path/
`
	robots := ParseRobots([]byte(robotsTxt), "*")

	allowPatterns := []string{
		`^https?://(?:www\.)?seriouseats\.com/[a-z0-9-]+-recipe-\d+$`,
		`^https?://(?:www\.)?seriouseats\.com/[a-z0-9-]+-recipe$`,
	}
	denyPatterns := []string{
		`^https?://(?:www\.)?seriouseats\.com/best-.*`,
		`^https?://(?:www\.)?seriouseats\.com/.*-recipes-\d+$`,
	}

	filter, err := NewURLFilter(allowPatterns, denyPatterns, robots)
	if err != nil {
		t.Fatalf("NewURLFilter failed: %v", err)
	}

	tests := []struct {
		url       string
		wantMatch bool
	}{
		// Valid allow match
		{"https://www.seriouseats.com/crispy-roasted-potatoes-recipe-12345", true},
		{"https://seriouseats.com/french-onion-soup-recipe", true},
		// Deny overrides allow
		{"https://www.seriouseats.com/best-pasta-recipes-12345", false},
		{"https://www.seriouseats.com/quick-weeknight-recipes-999", false},
		// Robots disallow overrides allow
		{"https://www.seriouseats.com/blocked-path/pie-recipe-555", false},
		// Non-matching allow pattern
		{"https://www.seriouseats.com/equipment/best-skillets", false},
		// Empty/invalid URLs
		{"", false},
		{"not-a-url", false},
	}

	for _, tc := range tests {
		got := filter.Match(tc.url)
		if got != tc.wantMatch {
			t.Errorf("filter.Match(%q) = %v, want %v", tc.url, got, tc.wantMatch)
		}
	}
}

func TestFetchFeedRSSAndAtom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	rssXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Recipe Blog</title>
    <link>https://example.com</link>
    <item>
      <title>Chicken Curry</title>
      <link>https://example.com/chicken-curry</link>
      <pubDate>Mon, 10 Nov 2026 12:00:00 GMT</pubDate>
    </item>
    <item>
      <title>Old Soup</title>
      <link>https://example.com/old-soup</link>
      <pubDate>Sun, 01 Nov 2026 12:00:00 GMT</pubDate>
    </item>
  </channel>
</rss>`

	mock := &mockFetcher{
		responses: map[string]*fetch.Response{
			"https://example.com/feed.xml": {
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Etag":          []string{`"etag-xyz"`},
					"Last-Modified": []string{"Mon, 10 Nov 2026 12:00:00 GMT"},
				},
				Body: []byte(rssXML),
			},
			"https://example.com/feed-304.xml": {
				StatusCode: http.StatusNotModified,
			},
		},
	}

	// 1. Nominal fetch with watermark filtering out 'Old Soup' (Nov 1)
	watermark := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	prev := FeedCursorState{
		LastItemPublishedAt: watermark,
	}

	res, err := FetchFeed(ctx, mock, "https://example.com/feed.xml", prev, fetch.RequestOptions{})
	if err != nil {
		t.Fatalf("FetchFeed failed: %v", err)
	}
	if res.NotModified {
		t.Error("expected NotModified=false")
	}
	if len(res.Entries) != 1 {
		t.Fatalf("expected 1 entry (Chicken Curry), got %d", len(res.Entries))
	}
	if res.Entries[0].URL != "https://example.com/chicken-curry" {
		t.Errorf("entry URL = %q, want Chicken Curry", res.Entries[0].URL)
	}
	if res.ETag != `"etag-xyz"` {
		t.Errorf("ETag = %q, want \"etag-xyz\"", res.ETag)
	}

	// 2. 304 Not Modified
	prev304 := FeedCursorState{
		ETag:                `"etag-cached"`,
		LastModified:        "Mon, 10 Nov 2026 10:00:00 GMT",
		LastItemPublishedAt: watermark,
	}
	res304, err := FetchFeed(ctx, mock, "https://example.com/feed-304.xml", prev304, fetch.RequestOptions{})
	if err != nil {
		t.Fatalf("FetchFeed 304 failed: %v", err)
	}
	if !res304.NotModified {
		t.Error("expected NotModified=true on 304")
	}
	if len(res304.Entries) != 0 {
		t.Errorf("expected 0 entries on 304, got %d", len(res304.Entries))
	}
	if res304.ETag != `"etag-cached"` {
		t.Errorf("ETag = %q, want \"etag-cached\"", res304.ETag)
	}
}

func TestDiscoverSourceCandidates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	robotsTxt := `
User-agent: *
Crawl-delay: 3.0
`
	rssXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Recipes</title>
    <link>https://example.com</link>
    <item>
      <title>Fresh Salad</title>
      <link>https://example.com/recipes/fresh-salad-101</link>
    </item>
    <item>
      <title>Overlapping Tacos</title>
      <link>https://example.com/recipes/tacos-102</link>
    </item>
  </channel>
</rss>`

	sitemapXML := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
   <url>
      <loc>https://example.com/recipes/tacos-102</loc>
   </url>
   <url>
      <loc>https://example.com/recipes/pasta-103</loc>
   </url>
   <url>
      <loc>https://example.com/ignored/article-999</loc>
   </url>
</urlset>`

	mock := &mockFetcher{
		responses: map[string]*fetch.Response{
			"https://example.com/robots.txt": {
				StatusCode: http.StatusOK,
				Body:       []byte(robotsTxt),
			},
			"https://example.com/feed/": {
				StatusCode: http.StatusOK,
				Body:       []byte(rssXML),
			},
			"https://example.com/sitemap.xml": {
				StatusCode: http.StatusOK,
				Body:       []byte(sitemapXML),
			},
		},
	}

	srcConfig := config.SourceConfig{
		Domain:        "example.com",
		CanonicalHost: "example.com",
		Enabled:       true,
		RSSURLs:       []string{"https://example.com/feed/"},
		SitemapURLs:   []string{"https://example.com/sitemap.xml"},
		AllowPatterns: []string{`^https?://example\.com/recipes/[a-z0-9-]+$`},
		DenyPatterns:  []string{`^https?://example\.com/ignored/`},
	}

	outcome, err := DiscoverSourceCandidates(ctx, mock, srcConfig, nil, true)
	if err != nil {
		t.Fatalf("DiscoverSourceCandidates failed: %v", err)
	}

	if outcome.RobotsCrawlDelay != 3*time.Second {
		t.Errorf("RobotsCrawlDelay = %v, want 3s", outcome.RobotsCrawlDelay)
	}

	// Candidates should include:
	// 1. Fresh Salad (rss)
	// 2. Overlapping Tacos (rss, deduplicated so not repeated from sitemap)
	// 3. Pasta (sitemap)
	// Article-999 ignored
	if len(outcome.Candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %d: %+v", len(outcome.Candidates), outcome.Candidates)
	}

	if outcome.Candidates[0].URL != "https://example.com/recipes/fresh-salad-101" || outcome.Candidates[0].SourceKind != "rss" {
		t.Errorf("candidate[0] = %+v", outcome.Candidates[0])
	}
	if outcome.Candidates[1].URL != "https://example.com/recipes/tacos-102" || outcome.Candidates[1].SourceKind != "rss" {
		t.Errorf("candidate[1] = %+v", outcome.Candidates[1])
	}
	if outcome.Candidates[2].URL != "https://example.com/recipes/pasta-103" || outcome.Candidates[2].SourceKind != "sitemap" {
		t.Errorf("candidate[2] = %+v", outcome.Candidates[2])
	}

	if len(outcome.UpdatedCursors) != 1 {
		t.Fatalf("expected 1 updated cursor, got %d", len(outcome.UpdatedCursors))
	}
}
