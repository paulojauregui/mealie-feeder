package discover

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// URLFilter enforces robots.txt path rules, deny regexes, and allow regexes for one source domain.
// Pre-conditions: Constructed via NewURLFilter; regex patterns must compile cleanly.
// Post-conditions: Safe for concurrent Match calls.
type URLFilter struct {
	allow  []*regexp.Regexp
	deny   []*regexp.Regexp
	robots *RobotsPolicy
}

// NewURLFilter compiles allowPatterns and denyPatterns and attaches an optional *RobotsPolicy.
// Pre-conditions: All strings in allowPatterns and denyPatterns must be valid Go regexps.
// Post-conditions: Returns compiled *URLFilter or regex compilation error.
func NewURLFilter(allowPatterns, denyPatterns []string, robots *RobotsPolicy) (*URLFilter, error) {
	filter := &URLFilter{
		robots: robots,
	}

	for _, p := range allowPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("filter: compile allow pattern %q: %w", p, err)
		}
		filter.allow = append(filter.allow, re)
	}

	for _, p := range denyPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("filter: compile deny pattern %q: %w", p, err)
		}
		filter.deny = append(filter.deny, re)
	}

	return filter, nil
}

// Match evaluates whether rawURL satisfies robots.Allowed(path), matches zero denyPatterns, and matches at least one allowPattern (if any are configured).
// Pre-conditions: rawURL parses as a valid http/https URL.
// Post-conditions: Deny patterns take strict precedence over allow patterns.
func (f *URLFilter) Match(rawURL string) bool {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return false
	}

	urlPath := parsed.Path
	if urlPath == "" {
		urlPath = "/"
	}

	// 1. Robots.txt policy
	if f.robots != nil && !f.robots.Allowed(urlPath) {
		return false
	}

	// 2. Deny patterns have strict precedence over allow patterns
	for _, denyRe := range f.deny {
		if denyRe.MatchString(trimmed) {
			return false
		}
	}

	// 3. Allow patterns (if specified, candidate must match at least one)
	if len(f.allow) > 0 {
		matched := false
		for _, allowRe := range f.allow {
			if allowRe.MatchString(trimmed) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}
