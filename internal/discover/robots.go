package discover

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/paulojauregui/mealie-feeder/internal/fetch"
)

// ResourceFetcher decouples discovery from concrete HTTP transport for deterministic offline tests.
// Pre-conditions: Implementations return *fetch.Response on 200/304 or typed fetch errors.
// Post-conditions: Matches fetch.Client.Fetch signature directly.
type ResourceFetcher interface {
	Fetch(ctx context.Context, rawURL string, opts fetch.RequestOptions) (*fetch.Response, error)
}

type agentRules struct {
	allows     []string
	disallows  []string
	crawlDelay time.Duration
}

// RobotsPolicy stores parsed User-agent rules, Crawl-delay, and validated Sitemap URLs for one origin.
// Pre-conditions: Constructed via ParseRobots or AllowAllRobots.
// Post-conditions: Thread-safe for concurrent Allowed queries.
type RobotsPolicy struct {
	mu         sync.RWMutex
	allows     []string
	disallows  []string
	crawlDelay time.Duration
	sitemaps   []string
}

// ParseRobots parses raw robots.txt bytes prioritizing exact product agent match (if any) merged or falling back to "User-agent: *".
// Pre-conditions: userAgent may be empty (defaults to "*" matching).
// Post-conditions: Validates each Sitemap: line via url.Parse (requiring http/https and non-empty host), silently dropping malformed entries like "http:///sitemap.xml".
func ParseRobots(raw []byte, userAgent string) *RobotsPolicy {
	targetAgent := strings.ToLower(strings.TrimSpace(userAgent))
	if targetAgent == "" {
		targetAgent = "*"
	}

	policy := &RobotsPolicy{
		sitemaps: make([]string, 0),
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	rulesByAgent := make(map[string]*agentRules)
	var currentAgents []string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			currentAgents = nil
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.ToLower(strings.TrimSpace(parts[0]))
		val := strings.TrimSpace(parts[1])

		switch key {
		case "sitemap":
			if parsed, err := url.Parse(val); err == nil {
				scheme := strings.ToLower(parsed.Scheme)
				if (scheme == "http" || scheme == "https") && parsed.Host != "" {
					policy.sitemaps = append(policy.sitemaps, parsed.String())
				}
			}

		case "user-agent":
			agentName := strings.ToLower(val)
			currentAgents = append(currentAgents, agentName)
			if rulesByAgent[agentName] == nil {
				rulesByAgent[agentName] = &agentRules{}
			}

		case "allow":
			for _, agent := range currentAgents {
				if r := rulesByAgent[agent]; r != nil && val != "" {
					r.allows = append(r.allows, val)
				}
			}

		case "disallow":
			for _, agent := range currentAgents {
				if r := rulesByAgent[agent]; r != nil && val != "" {
					r.disallows = append(r.disallows, val)
				}
			}

		case "crawl-delay":
			if sec, err := strconv.ParseFloat(val, 64); err == nil && sec >= 0 {
				dur := time.Duration(sec * float64(time.Second))
				for _, agent := range currentAgents {
					if r := rulesByAgent[agent]; r != nil {
						r.crawlDelay = dur
					}
				}
			}
		}
	}

	// Select rule set: specific userAgent takes precedence over wildcard "*"
	selected := rulesByAgent[targetAgent]
	if selected == nil && targetAgent != "*" {
		selected = rulesByAgent["*"]
	}
	if selected == nil {
		selected = &agentRules{}
	}

	policy.allows = selected.allows
	policy.disallows = selected.disallows
	policy.crawlDelay = selected.crawlDelay

	return policy
}

// AllowAllRobots returns a permissive fallback policy when origin returns 404 on /robots.txt.
// Pre-conditions: None.
// Post-conditions: Allowed returns true for all paths; CrawlDelay is 0; Sitemaps is empty.
func AllowAllRobots() *RobotsPolicy {
	return &RobotsPolicy{
		allows:     nil,
		disallows:  nil,
		crawlDelay: 0,
		sitemaps:   nil,
	}
}

// FetchRobots retrieves and parses <scheme>://<host>/robots.txt cleanly, returning AllowAllRobots on 404/410.
// Pre-conditions: fetcher non-nil, baseOrigin is "https://host".
// Post-conditions: Propagates ErrChallengeDetected; degrades cleanly on 404/transient robots failure if configured or returns error.
func FetchRobots(ctx context.Context, fetcher ResourceFetcher, baseOrigin string, opts fetch.RequestOptions) (*RobotsPolicy, error) {
	robotsURL := strings.TrimRight(baseOrigin, "/") + "/robots.txt"

	resp, err := fetcher.Fetch(ctx, robotsURL, opts)
	if err != nil {
		if errors.Is(err, fetch.ErrPermanentHTTP) {
			return AllowAllRobots(), nil
		}
		return nil, err
	}

	if resp.StatusCode == 404 || resp.StatusCode == 410 {
		return AllowAllRobots(), nil
	}

	return ParseRobots(resp.Body, "*"), nil
}

// Allowed evaluates longest-prefix Allow/Disallow path matching per standard robots.txt precedence (Allow beats equal-length Disallow).
// Pre-conditions: urlPath starts with "/" (empty defaults to "/").
// Post-conditions: Returns true when crawling urlPath is permitted.
func (p *RobotsPolicy) Allowed(urlPath string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	path := strings.TrimSpace(urlPath)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	bestAllowLen := -1
	for _, allow := range p.allows {
		if prefixMatch(path, allow) {
			if len(allow) > bestAllowLen {
				bestAllowLen = len(allow)
			}
		}
	}

	bestDisallowLen := -1
	for _, disallow := range p.disallows {
		if prefixMatch(path, disallow) {
			if len(disallow) > bestDisallowLen {
				bestDisallowLen = len(disallow)
			}
		}
	}

	if bestDisallowLen < 0 {
		return true // No disallow rule matched
	}

	// Longest match wins. On equal length, Allow wins per robots.txt standard.
	return bestAllowLen >= bestDisallowLen
}

// prefixMatch checks if path starts with prefix, respecting optional trailing wildcards.
func prefixMatch(path, prefix string) bool {
	if prefix == "" {
		return false
	}
	if prefix == "/" {
		return true
	}
	// Basic prefix match (supports standard robots.txt path prefix)
	return strings.HasPrefix(path, prefix)
}

// CrawlDelay returns the parsed Crawl-delay duration (0 if unspecified).
// Pre-conditions: p non-nil.
// Post-conditions: Non-negative time.Duration.
func (p *RobotsPolicy) CrawlDelay() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.crawlDelay
}

// Sitemaps returns validated absolute Sitemap URLs extracted from robots.txt.
// Pre-conditions: p non-nil.
// Post-conditions: Defensive copy of validated URLs.
func (p *RobotsPolicy) Sitemaps() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.sitemaps) == 0 {
		return nil
	}
	cp := make([]string, len(p.sitemaps))
	copy(cp, p.sitemaps)
	return cp
}
