package discover

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/paulojauregui/mealie-feeder/internal/fetch"
)

// SitemapEntry represents one discovered URL with optional <lastmod> timestamp.
// Pre-conditions: URL is a trimmed, non-empty string.
// Post-conditions: LastMod is UTC (or zero time if absent/unparseable).
type SitemapEntry struct {
	URL     string
	LastMod time.Time
}

// SitemapOptions bounds recursive sitemap traversal and memory consumption.
// Pre-conditions: MaxDepth >= 1, MaxSitemaps >= 1, MaxURLs >= 1.
// Post-conditions: Zero fields default to MaxDepth=3, MaxSitemaps=100, MaxURLs=100000, MaxDecompressedBytes=64MiB.
type SitemapOptions struct {
	IncludePatterns      []string
	MaxDepth             int
	MaxSitemaps          int
	MaxURLs              int
	MaxDecompressedBytes int64
	Request              fetch.RequestOptions
}

const (
	defaultMaxDepth             = 3
	defaultMaxSitemaps          = 100
	defaultMaxURLs              = 100000
	defaultMaxDecompressedBytes = 64 * 1024 * 1024 // 64 MiB
)

type xmlSitemapIndex struct {
	XMLName  xml.Name     `xml:"sitemapindex"`
	Sitemaps []xmlSitemap `xml:"sitemap"`
}

type xmlSitemap struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

type xmlURLSet struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []xmlURL `xml:"url"`
}

type xmlURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// DiscoverSitemapURLs recursively fetches <sitemapindex> and <urlset> documents (including .xml.gz / gzip Content-Encoding),
// filters child sitemaps against opts.IncludePatterns before fetching, deduplicates URLs, and sorts entries descending by LastMod.
// Pre-conditions: fetcher non-nil; rootURLs non-empty.
// Post-conditions: Respects cycle detection, depth limits, and gzip decompression bounds; returns slice sorted newest-first.
func DiscoverSitemapURLs(ctx context.Context, fetcher ResourceFetcher, rootURLs []string, opts SitemapOptions) ([]SitemapEntry, error) {
	if fetcher == nil {
		return nil, errors.New("discover: fetcher is nil")
	}
	if len(rootURLs) == 0 {
		return nil, nil
	}

	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	maxSitemaps := opts.MaxSitemaps
	if maxSitemaps <= 0 {
		maxSitemaps = defaultMaxSitemaps
	}
	maxURLs := opts.MaxURLs
	if maxURLs <= 0 {
		maxURLs = defaultMaxURLs
	}
	maxDecompress := opts.MaxDecompressedBytes
	if maxDecompress <= 0 {
		maxDecompress = defaultMaxDecompressedBytes
	}

	// Pre-compile include pattern regexes
	var includeRegexes []*regexp.Regexp
	for _, pat := range opts.IncludePatterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("discover: invalid include pattern %q: %w", pat, err)
		}
		includeRegexes = append(includeRegexes, re)
	}

	type queueItem struct {
		url   string
		depth int
	}

	queue := make([]queueItem, 0, len(rootURLs))
	visited := make(map[string]bool)

	for _, root := range rootURLs {
		trimmed := strings.TrimSpace(root)
		if trimmed != "" && !visited[trimmed] {
			visited[trimmed] = true
			queue = append(queue, queueItem{url: trimmed, depth: 1})
		}
	}

	urlEntriesMap := make(map[string]SitemapEntry)
	sitemapsProcessed := 0

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if sitemapsProcessed >= maxSitemaps {
			break
		}

		item := queue[0]
		queue = queue[1:]
		sitemapsProcessed++

		resp, err := fetcher.Fetch(ctx, item.url, opts.Request)
		if err != nil {
			continue // Degrade gracefully on broken sitemap links
		}

		decompressedBytes, err := decompressBody(resp.Body, maxDecompress)
		if err != nil {
			return nil, fmt.Errorf("discover: decompress sitemap %s: %w", item.url, err)
		}

		// Try parsing as sitemapindex first
		var sIndex xmlSitemapIndex
		if err := xml.Unmarshal(decompressedBytes, &sIndex); err == nil && len(sIndex.Sitemaps) > 0 {
			if item.depth < maxDepth {
				for _, child := range sIndex.Sitemaps {
					childURL := strings.TrimSpace(child.Loc)
					if childURL == "" || visited[childURL] {
						continue
					}
					// Validate child URL
					if _, err := url.ParseRequestURI(childURL); err != nil {
						continue
					}
					// Check include patterns
					if len(includeRegexes) > 0 && !matchesAnyRegex(childURL, includeRegexes) {
						continue
					}
					visited[childURL] = true
					queue = append(queue, queueItem{url: childURL, depth: item.depth + 1})
				}
			}
			continue
		}

		// Try parsing as urlset
		var uSet xmlURLSet
		if err := xml.Unmarshal(decompressedBytes, &uSet); err == nil {
			for _, u := range uSet.URLs {
				rawLoc := strings.TrimSpace(u.Loc)
				if rawLoc == "" {
					continue
				}
				if _, exists := urlEntriesMap[rawLoc]; !exists {
					lastMod := parseLastMod(u.LastMod)
					urlEntriesMap[rawLoc] = SitemapEntry{
						URL:     rawLoc,
						LastMod: lastMod,
					}
					if len(urlEntriesMap) >= maxURLs {
						break
					}
				}
			}
		}
	}

	// Convert map to sorted slice
	results := make([]SitemapEntry, 0, len(urlEntriesMap))
	for _, entry := range urlEntriesMap {
		results = append(results, entry)
	}

	// Sort descending by LastMod (newest first); missing LastMod sorted after dated entries
	sort.Slice(results, func(i, j int) bool {
		hasI := !results[i].LastMod.IsZero()
		hasJ := !results[j].LastMod.IsZero()

		if hasI && hasJ {
			if !results[i].LastMod.Equal(results[j].LastMod) {
				return results[i].LastMod.After(results[j].LastMod)
			}
			return results[i].URL < results[j].URL
		}
		if hasI && !hasJ {
			return true
		}
		if !hasI && hasJ {
			return false
		}
		return results[i].URL < results[j].URL
	})

	return results, nil
}

func matchesAnyRegex(val string, regexes []*regexp.Regexp) bool {
	for _, re := range regexes {
		if re.MatchString(val) {
			return true
		}
	}
	return false
}

func decompressBody(data []byte, maxBytes int64) ([]byte, error) {
	// Gzip magic number check: 0x1f, 0x8b
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer r.Close()

		limited := io.LimitReader(r, maxBytes+1)
		var buf bytes.Buffer
		n, err := buf.ReadFrom(limited)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if n > maxBytes {
			return nil, fmt.Errorf("sitemap payload exceeded decompression limit of %d bytes", maxBytes)
		}
		return buf.Bytes(), nil
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("sitemap payload exceeded size limit of %d bytes", maxBytes)
	}
	return data, nil
}

func parseLastMod(raw string) time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}
	}

	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}

	for _, layout := range formats {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
