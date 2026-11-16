package discover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/paulojauregui/mealie-feeder/internal/config"
	"github.com/paulojauregui/mealie-feeder/internal/fetch"
)

// FeedCursorState decouples discover from internal/ledger while passing conditional GET metadata.
// Pre-conditions: FeedURL is canonical feed URL.
// Post-conditions: Returned updated by DiscoverSourceCandidates after feed checks.
type FeedCursorState struct {
	FeedURL             string
	ETag                string
	LastModified        string
	LastItemPublishedAt time.Time
	LastCheckedAt       time.Time
}

// FeedResult captures parsed feed items and HTTP cache validators.
// Pre-conditions: Returned by FetchFeed.
// Post-conditions: When NotModified is true, Entries is empty and HTTP validators remain unchanged.
type FeedResult struct {
	Entries         []SitemapEntry
	ETag            string
	LastModified    string
	LatestPublished time.Time
	NotModified     bool
}

// Candidate represents one filtered, deduplicated recipe permalink yielded by discovery.
// Pre-conditions: SourceKind is "rss" or "sitemap".
// Post-conditions: Ready for ledger UpsertDiscovered.
type Candidate struct {
	URL        string
	Domain     string
	SourceKind string
	LastMod    time.Time
}

// DiscoveryOutcome bundles filtered candidates, updated feed cursors, and effective robots crawl-delay.
// Pre-conditions: Returned by DiscoverSourceCandidates.
// Post-conditions: Candidates slice contains deduplicated URLs matching source allow/deny and robots rules.
type DiscoveryOutcome struct {
	Candidates       []Candidate
	UpdatedCursors   []FeedCursorState
	RobotsCrawlDelay time.Duration
}

// FetchFeed retrieves an RSS 2.0 or Atom 1.0 feed using optional ETag/Last-Modified conditional headers and parses items via gofeed.
// Pre-conditions: fetcher non-nil, feedURL valid http/https URL.
// Post-conditions: Handles 304 Not Modified cleanly; filters items strictly older than prevPublishedAt when non-zero; falls back safely when PublishedParsed is nil.
func FetchFeed(ctx context.Context, fetcher ResourceFetcher, feedURL string, prev FeedCursorState, opts fetch.RequestOptions) (*FeedResult, error) {
	if fetcher == nil {
		return nil, errors.New("discover: fetcher is nil")
	}
	trimmedURL := strings.TrimSpace(feedURL)
	if trimmedURL == "" {
		return nil, errors.New("discover: feedURL is empty")
	}

	reqHeaders := make(map[string]string)
	for k, v := range opts.Headers {
		reqHeaders[k] = v
	}
	if prev.ETag != "" {
		reqHeaders["If-None-Match"] = prev.ETag
	}
	if prev.LastModified != "" {
		reqHeaders["If-Modified-Since"] = prev.LastModified
	}
	opts.Headers = reqHeaders

	resp, err := fetcher.Fetch(ctx, trimmedURL, opts)
	if err != nil {
		return nil, fmt.Errorf("discover: fetch feed %q: %w", trimmedURL, err)
	}

	// 304 Not Modified
	if resp.StatusCode == http.StatusNotModified {
		return &FeedResult{
			Entries:         nil,
			ETag:            prev.ETag,
			LastModified:    prev.LastModified,
			LatestPublished: prev.LastItemPublishedAt,
			NotModified:     true,
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discover: feed returned unexpected status %d", resp.StatusCode)
	}

	parser := gofeed.NewParser()
	parsedFeed, err := parser.Parse(bytes.NewReader(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("discover: parse feed %q: %w", trimmedURL, err)
	}

	etag := resp.Header.Get("ETag")
	lastModHeader := resp.Header.Get("Last-Modified")

	var entries []SitemapEntry
	latestPublished := prev.LastItemPublishedAt

	for _, item := range parsedFeed.Items {
		itemURL := strings.TrimSpace(item.Link)
		if itemURL == "" {
			continue
		}

		var pubDate time.Time
		if item.PublishedParsed != nil {
			pubDate = item.PublishedParsed.UTC()
		} else if item.UpdatedParsed != nil {
			pubDate = item.UpdatedParsed.UTC()
		}

		// Filter out items already covered by the watermark
		if !prev.LastItemPublishedAt.IsZero() && !pubDate.IsZero() && !pubDate.After(prev.LastItemPublishedAt) {
			continue
		}

		if pubDate.After(latestPublished) {
			latestPublished = pubDate
		}

		entries = append(entries, SitemapEntry{
			URL:     itemURL,
			LastMod: pubDate,
		})
	}

	return &FeedResult{
		Entries:         entries,
		ETag:            etag,
		LastModified:    lastModHeader,
		LatestPublished: latestPublished,
		NotModified:     false,
	}, nil
}

// DiscoverSourceCandidates fetches robots.txt, polls configured RSS feeds (if any), and traverses sitemaps for src,
// returning filtered, deduplicated Candidates with RSS items ordered ahead of sitemap entries.
// Pre-conditions: fetcher non-nil; src must pass config validation; prevCursors may be nil.
// Post-conditions: Explicitly respects src.SitemapURLs when robots.txt lacks valid Sitemap entries; never loops on empty feeds.
func DiscoverSourceCandidates(ctx context.Context, fetcher ResourceFetcher, src config.SourceConfig, prevCursors map[string]FeedCursorState, includeSitemaps bool) (*DiscoveryOutcome, error) {
	if fetcher == nil {
		return nil, errors.New("discover: fetcher is nil")
	}

	host := src.CanonicalHost
	if host == "" {
		host = src.Domain
	}
	baseOrigin := "https://" + host

	reqOpts := fetch.RequestOptions{
		TLSMode:     string(src.TLSMode),
		MinInterval: src.MinInterval,
		Timeout:     src.Timeout,
		MaxRetries:  src.MaxRetries,
	}

	// 1. Robots.txt
	robots, _ := FetchRobots(ctx, fetcher, baseOrigin, reqOpts)
	var crawlDelay time.Duration
	if robots != nil {
		crawlDelay = robots.CrawlDelay()
	}

	// 2. URL Filter
	filter, err := NewURLFilter(src.AllowPatterns, src.DenyPatterns, robots)
	if err != nil {
		return nil, fmt.Errorf("discover: initialize url filter for %s: %w", src.Domain, err)
	}

	outcome := &DiscoveryOutcome{
		Candidates:       make([]Candidate, 0),
		UpdatedCursors:   make([]FeedCursorState, 0),
		RobotsCrawlDelay: crawlDelay,
	}

	seenURLs := make(map[string]bool)

	// 3. RSS Feeds (Active feed items prioritized ahead of sitemap backfill)
	for _, feedURL := range src.RSSURLs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var prevCursor FeedCursorState
		if prevCursors != nil {
			prevCursor = prevCursors[feedURL]
		}

		feedRes, err := FetchFeed(ctx, fetcher, feedURL, prevCursor, reqOpts)
		if err != nil {
			continue // Gracefully degrade on transient feed failure
		}

		outcome.UpdatedCursors = append(outcome.UpdatedCursors, FeedCursorState{
			FeedURL:             feedURL,
			ETag:                feedRes.ETag,
			LastModified:        feedRes.LastModified,
			LastItemPublishedAt: feedRes.LatestPublished,
			LastCheckedAt:       time.Now().UTC(),
		})

		for _, item := range feedRes.Entries {
			if !seenURLs[item.URL] && filter.Match(item.URL) {
				seenURLs[item.URL] = true
				outcome.Candidates = append(outcome.Candidates, Candidate{
					URL:        item.URL,
					Domain:     src.Domain,
					SourceKind: "rss",
					LastMod:    item.LastMod,
				})
			}
		}
	}

	// 4. Sitemaps
	if includeSitemaps {
		sitemapURLs := src.SitemapURLs
		if len(sitemapURLs) == 0 && robots != nil {
			sitemapURLs = robots.Sitemaps()
		}

		if len(sitemapURLs) > 0 {
			sitemapOpts := SitemapOptions{
				IncludePatterns: src.SitemapIncludePatterns,
				Request:         reqOpts,
			}
			entries, err := DiscoverSitemapURLs(ctx, fetcher, sitemapURLs, sitemapOpts)
			if err == nil {
				for _, entry := range entries {
					if !seenURLs[entry.URL] && filter.Match(entry.URL) {
						seenURLs[entry.URL] = true
						outcome.Candidates = append(outcome.Candidates, Candidate{
							URL:        entry.URL,
							Domain:     src.Domain,
							SourceKind: "sitemap",
							LastMod:    entry.LastMod,
						})
					}
				}
			}
		}
	}

	return outcome, nil
}
