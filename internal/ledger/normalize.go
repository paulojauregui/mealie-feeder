package ledger

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"sort"
	"strings"
)

var trackingParamSet = map[string]struct{}{
	"fbclid":  {},
	"gclid":   {},
	"dclid":   {},
	"msclkid": {},
	"mc_cid":  {},
	"mc_eid":  {},
	"igshid":  {},
	"ref":     {},
	"ref_src": {},
	"_hsenc":  {},
	"_hsmi":   {},
	"mkt_tok": {},
	"yclid":   {},
}

// isTrackingParam checks whether a query parameter key represents an analytics or marketing tracking tag.
// Pre-conditions: key is a raw URL query parameter name.
// Post-conditions: Returns true for utm_* and recognized tracking keys.
func isTrackingParam(key string) bool {
	lower := strings.ToLower(key)
	if strings.HasPrefix(lower, "utm_") {
		return true
	}
	_, found := trackingParamSet[lower]
	return found
}

// NormalizeURL canonicalizes rawURL for deduplication across seeds, sitemaps, RSS feeds, and Mealie orgURLs.
// Pre-conditions: rawURL must parse as an absolute http or https URL with a non-empty host.
// Post-conditions: Returns a URL string with lowercase scheme/host, default ports (:80, :443) stripped,
// empty fragment, path cleaned without collapsing intentional trailing slash semantics,
// tracking query params removed, and remaining query params sorted lexicographically.
func NormalizeURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", errors.New("ledger: rawURL is empty")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("ledger: parse URL %q: %w", rawURL, err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("ledger: invalid URL scheme %q, must be http or https", parsed.Scheme)
	}
	parsed.Scheme = scheme

	host := strings.ToLower(parsed.Host)
	if host == "" {
		return "", fmt.Errorf("ledger: URL %q has empty host", rawURL)
	}

	// Strip default ports
	hostname, port, err := net.SplitHostPort(host)
	if err == nil {
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			host = hostname
		} else {
			host = net.JoinHostPort(hostname, port)
		}
	}
	parsed.Host = host

	// Empty fragment
	parsed.Fragment = ""

	// Clean path while preserving trailing slash
	rawPath := parsed.EscapedPath()
	hadTrailingSlash := strings.HasSuffix(rawPath, "/") && len(rawPath) > 1

	cleanedPath := path.Clean(parsed.Path)
	if cleanedPath == "." || cleanedPath == "" {
		cleanedPath = "/"
	} else if hadTrailingSlash && !strings.HasSuffix(cleanedPath, "/") {
		cleanedPath += "/"
	}
	parsed.Path = cleanedPath
	parsed.RawPath = ""

	// Filter tracking query params and sort remaining
	q := parsed.Query()
	filteredValues := make(url.Values)
	for key, values := range q {
		if isTrackingParam(key) {
			continue
		}
		sort.Strings(values)
		filteredValues[key] = values
	}

	if len(filteredValues) > 0 {
		parsed.RawQuery = filteredValues.Encode()
	} else {
		parsed.RawQuery = ""
	}

	return parsed.String(), nil
}

// DedupeKey returns a trailing-slash-normalized comparison key derived from canonicalURL for matching Mealie orgURLs.
// Pre-conditions: canonicalURL must be a valid http/https URL.
// Post-conditions: Strips www. host prefix and trailing slash (except root "/") for secondary orgURL collision detection.
func DedupeKey(canonicalURL string) (string, error) {
	norm, err := NormalizeURL(canonicalURL)
	if err != nil {
		return "", err
	}

	parsed, err := url.Parse(norm)
	if err != nil {
		return "", err
	}

	// Strip www. prefix from host
	host := parsed.Host
	hostname, port, err := net.SplitHostPort(host)
	if err == nil {
		hostname = strings.TrimPrefix(hostname, "www.")
		parsed.Host = net.JoinHostPort(hostname, port)
	} else {
		parsed.Host = strings.TrimPrefix(host, "www.")
	}

	// Strip trailing slash except root "/"
	if len(parsed.Path) > 1 && strings.HasSuffix(parsed.Path, "/") {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}

	return parsed.String(), nil
}

// ExtractDomain returns the lowercase apex-friendly hostname (stripping leading "www." and port) from a URL.
// Pre-conditions: rawURL must parse with a non-empty host.
// Post-conditions: Returns lowercase hostname without "www." prefix (e.g., "seriouseats.com").
func ExtractDomain(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", errors.New("ledger: rawURL is empty")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("ledger: parse URL %q: %w", rawURL, err)
	}

	host := strings.ToLower(parsed.Host)
	if host == "" {
		// Attempt parsing as hostname directly if no scheme was provided
		if !strings.Contains(trimmed, "://") {
			parsed, err = url.Parse("https://" + trimmed)
			if err == nil {
				host = strings.ToLower(parsed.Host)
			}
		}
	}

	if host == "" {
		return "", fmt.Errorf("ledger: URL %q has empty host", rawURL)
	}

	hostname, _, err := net.SplitHostPort(host)
	if err == nil {
		host = hostname
	}

	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimRight(host, ".")
	if host == "" {
		return "", fmt.Errorf("ledger: URL %q has invalid host", rawURL)
	}

	return host, nil
}
