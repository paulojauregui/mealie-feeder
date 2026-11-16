package ledger

import (
	"testing"
	"time"
)

func TestIsValidState(t *testing.T) {
	t.Parallel()

	validStates := []State{
		StateDiscovered,
		StateImported,
		StateSkippedNonRecipe,
		StateSkippedPaywall,
		StateFailedRetryable,
		StateFailedPermanent,
	}

	for _, s := range validStates {
		if !IsValidState(s) {
			t.Errorf("expected state %q to be valid", s)
		}
	}

	invalidStates := []State{
		"",
		"unknown",
		"pending",
		"error",
		"DISCOVERED",
	}

	for _, s := range invalidStates {
		if IsValidState(s) {
			t.Errorf("expected state %q to be invalid", s)
		}
	}
}

func TestCanTransition(t *testing.T) {
	t.Parallel()

	// Idempotent self-transitions always allowed
	states := []State{
		StateDiscovered,
		StateImported,
		StateSkippedNonRecipe,
		StateSkippedPaywall,
		StateFailedRetryable,
		StateFailedPermanent,
	}
	for _, s := range states {
		if !CanTransition(s, s) {
			t.Errorf("self-transition %s -> %s should be allowed", s, s)
		}
	}

	// Any state can transition to StateImported
	for _, s := range states {
		if !CanTransition(s, StateImported) {
			t.Errorf("transition %s -> imported should be allowed", s)
		}
	}

	// Discovered transitions
	if !CanTransition(StateDiscovered, StateSkippedNonRecipe) {
		t.Error("discovered -> skipped_non_recipe should be allowed")
	}
	if !CanTransition(StateDiscovered, StateSkippedPaywall) {
		t.Error("discovered -> skipped_paywall should be allowed")
	}
	if !CanTransition(StateDiscovered, StateFailedRetryable) {
		t.Error("discovered -> failed_retryable should be allowed")
	}
	if !CanTransition(StateDiscovered, StateFailedPermanent) {
		t.Error("discovered -> failed_permanent should be allowed")
	}

	// FailedRetryable transitions
	if !CanTransition(StateFailedRetryable, StateFailedPermanent) {
		t.Error("failed_retryable -> failed_permanent should be allowed")
	}
	if !CanTransition(StateFailedRetryable, StateSkippedNonRecipe) {
		t.Error("failed_retryable -> skipped_non_recipe should be allowed")
	}
	if !CanTransition(StateFailedRetryable, StateSkippedPaywall) {
		t.Error("failed_retryable -> skipped_paywall should be allowed")
	}
	if CanTransition(StateFailedRetryable, StateDiscovered) {
		t.Error("failed_retryable -> discovered should NOT be allowed")
	}

	// Terminal state invalid transitions
	terminalStates := []State{
		StateImported,
		StateSkippedNonRecipe,
		StateSkippedPaywall,
		StateFailedPermanent,
	}
	for _, from := range terminalStates {
		for _, to := range []State{StateDiscovered, StateFailedRetryable} {
			if CanTransition(from, to) {
				t.Errorf("terminal transition %s -> %s should NOT be allowed", from, to)
			}
		}
	}

	// Invalid states reject transition
	if CanTransition("invalid", StateImported) {
		t.Error("invalid source state should be rejected")
	}
	if CanTransition(StateDiscovered, "invalid") {
		t.Error("invalid target state should be rejected")
	}
	if CanTransition("invalid", "invalid") {
		t.Error("invalid states should be rejected")
	}
}

func TestComputeNextRetry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		attempts int
		wantDiff time.Duration
	}{
		{attempts: 0, wantDiff: 1 * time.Hour},
		{attempts: 1, wantDiff: 1 * time.Hour},
		{attempts: 2, wantDiff: 6 * time.Hour},
		{attempts: 3, wantDiff: 24 * time.Hour},
		{attempts: 4, wantDiff: 72 * time.Hour},
		{attempts: 5, wantDiff: 72 * time.Hour},
		{attempts: 10, wantDiff: 72 * time.Hour},
	}

	for _, tc := range tests {
		got := ComputeNextRetry(now, tc.attempts)
		diff := got.Sub(now)
		if diff != tc.wantDiff {
			t.Errorf("ComputeNextRetry(attempts=%d) diff = %v, want %v", tc.attempts, diff, tc.wantDiff)
		}
		if got.Location() != time.UTC {
			t.Errorf("ComputeNextRetry should return UTC time, got %v", got.Location())
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{
			name:   "standard port stripped on https",
			rawURL: "https://www.seriouseats.com:443/recipe-1",
			want:   "https://www.seriouseats.com/recipe-1",
		},
		{
			name:   "standard port stripped on http",
			rawURL: "http://example.com:80/recipe-1",
			want:   "http://example.com/recipe-1",
		},
		{
			name:   "non-standard port preserved",
			rawURL: "http://example.com:9000/recipe-1",
			want:   "http://example.com:9000/recipe-1",
		},
		{
			name:   "lowercase scheme and host while preserving path case",
			rawURL: "HTTPS://WWW.SeriousEats.COM/Path/To/Recipe",
			want:   "https://www.seriouseats.com/Path/To/Recipe",
		},
		{
			name:   "path cleaned of dot segments while preserving trailing slash",
			rawURL: "https://example.com/foo/../bar/",
			want:   "https://example.com/bar/",
		},
		{
			name:   "path cleaned without trailing slash",
			rawURL: "https://example.com/foo/../bar",
			want:   "https://example.com/bar",
		},
		{
			name:   "empty path resolves to root slash",
			rawURL: "https://example.com",
			want:   "https://example.com/",
		},
		{
			name:   "drops fragment",
			rawURL: "https://example.com/recipe#recipe-card",
			want:   "https://example.com/recipe",
		},
		{
			name:   "strips all tracking query params",
			rawURL: "https://example.com/recipe?utm_source=facebook&utm_medium=cpc&fbclid=123&gclid=456&mc_cid=789&igshid=abc&ref=home&ref_src=feed&_hsenc=xyz&_hsmi=111&mkt_tok=222&yclid=333",
			want:   "https://example.com/recipe",
		},
		{
			name:   "preserves meaningful query params and sorts them",
			rawURL: "https://example.com/recipe?sort=asc&page=2&utm_source=tw",
			want:   "https://example.com/recipe?page=2&sort=asc",
		},
		{
			name:    "empty string fails",
			rawURL:  "",
			wantErr: true,
		},
		{
			name:    "invalid scheme ftp fails",
			rawURL:  "ftp://example.com/recipe",
			wantErr: true,
		},
		{
			name:    "javascript scheme fails",
			rawURL:  "javascript:alert(1)",
			wantErr: true,
		},
		{
			name:    "relative url missing host fails",
			rawURL:  "/path/to/recipe",
			wantErr: true,
		},
		{
			name:    "malformed url parse error",
			rawURL:  "http://[::1:80/path",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeURL(tc.rawURL)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NormalizeURL(%q) err = %v, wantErr = %v", tc.rawURL, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tc.rawURL, got, tc.want)
			}
		})
	}
}

func TestDedupeKey(t *testing.T) {
	t.Parallel()

	key1, err := DedupeKey("https://www.seriouseats.com/foo/")
	if err != nil {
		t.Fatalf("DedupeKey failed: %v", err)
	}

	key2, err := DedupeKey("https://seriouseats.com/foo")
	if err != nil {
		t.Fatalf("DedupeKey failed: %v", err)
	}

	if key1 != key2 {
		t.Errorf("DedupeKey collision mismatch: key1=%q, key2=%q", key1, key2)
	}

	want := "https://seriouseats.com/foo"
	if key1 != want {
		t.Errorf("DedupeKey = %q, want %q", key1, want)
	}

	// Root path preservation
	rootKey, err := DedupeKey("https://www.example.com/")
	if err != nil {
		t.Fatalf("DedupeKey root failed: %v", err)
	}
	if rootKey != "https://example.com/" {
		t.Errorf("DedupeKey root = %q, want https://example.com/", rootKey)
	}

	// With custom port and www prefix
	portKey, err := DedupeKey("https://www.example.com:9000/recipe/")
	if err != nil {
		t.Fatalf("DedupeKey with port failed: %v", err)
	}
	if portKey != "https://example.com:9000/recipe" {
		t.Errorf("DedupeKey with port = %q, want https://example.com:9000/recipe", portKey)
	}

	// Invalid URL
	if _, err := DedupeKey("ftp://invalid"); err == nil {
		t.Error("expected error for invalid scheme in DedupeKey")
	}
}

func TestExtractDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rawURL  string
		want    string
		wantErr bool
	}{
		{rawURL: "https://www.seriouseats.com/recipe", want: "seriouseats.com"},
		{rawURL: "http://Seriouseats.com:8080/path", want: "seriouseats.com"},
		{rawURL: "www.budgetbytes.com", want: "budgetbytes.com"},
		{rawURL: "recipetineats.com/", want: "recipetineats.com"},
		{rawURL: "https://www.example.com.", want: "example.com"},
		{rawURL: "", wantErr: true},
		{rawURL: "   ", wantErr: true},
		{rawURL: "http://:8080", wantErr: true},
		{rawURL: "https://www./", wantErr: true},
	}

	for _, tc := range tests {
		got, err := ExtractDomain(tc.rawURL)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ExtractDomain(%q) err = %v, wantErr = %v", tc.rawURL, err, tc.wantErr)
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("ExtractDomain(%q) = %q, want %q", tc.rawURL, got, tc.want)
		}
	}
}
