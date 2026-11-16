package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulojauregui/mealie-feeder/internal/feeder"
)

func TestCLIVersion(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer

	code := RunCLI(context.Background(), []string{"version"}, &stdout, &stderr, nil)
	if code != ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d. stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "mealie-feeder") || !strings.Contains(out, "runtime: go") {
		t.Errorf("version output unexpected: %s", out)
	}
}

func TestCLIUsageAndUnknownCommands(t *testing.T) {
	t.Parallel()

	// 1. Missing subcommand
	var stdout1, stderr1 bytes.Buffer
	code1 := RunCLI(context.Background(), []string{}, &stdout1, &stderr1, nil)
	if code1 != ExitUsageOrConfig {
		t.Errorf("expected ExitUsageOrConfig (2), got %d", code1)
	}
	if !strings.Contains(stderr1.String(), "Usage:") {
		t.Errorf("expected Usage in stderr, got %s", stderr1.String())
	}

	// 2. Unknown subcommand
	var stdout2, stderr2 bytes.Buffer
	code2 := RunCLI(context.Background(), []string{"invalid-subcmd"}, &stdout2, &stderr2, nil)
	if code2 != ExitUsageOrConfig {
		t.Errorf("expected ExitUsageOrConfig (2), got %d", code2)
	}

	// 3. Unknown flag
	var stdout3, stderr3 bytes.Buffer
	code3 := RunCLI(context.Background(), []string{"--invalid-flag"}, &stdout3, &stderr3, nil)
	if code3 != ExitUsageOrConfig {
		t.Errorf("expected ExitUsageOrConfig (2), got %d", code3)
	}
}

func TestCLISeedAndSyncValidation(t *testing.T) {
	t.Parallel()

	// Seed without MEALIE_URL or token fails with ExitUsageOrConfig
	var stdout1, stderr1 bytes.Buffer
	emptyEnv := func(string) (string, bool) { return "", false }

	code1 := RunCLI(context.Background(), []string{"seed"}, &stdout1, &stderr1, emptyEnv)
	if code1 != ExitUsageOrConfig {
		t.Errorf("seed without env expected 2, got %d", code1)
	}
	if !strings.Contains(stderr1.String(), "MEALIE_URL") {
		t.Errorf("expected MEALIE_URL in stderr, got %s", stderr1.String())
	}

	// Sync without MEALIE_URL or token fails with ExitUsageOrConfig
	var stdout2, stderr2 bytes.Buffer
	code2 := RunCLI(context.Background(), []string{"sync"}, &stdout2, &stderr2, emptyEnv)
	if code2 != ExitUsageOrConfig {
		t.Errorf("sync without env expected 2, got %d", code2)
	}
	if !strings.Contains(stderr2.String(), "MEALIE_URL") {
		t.Errorf("expected MEALIE_URL in stderr, got %s", stderr2.String())
	}
}

func TestCLIInspectJSON(t *testing.T) {
	t.Parallel()

	html := `
<script type="application/ld+json">
{
  "@type": "Recipe",
  "name": "Roast Chicken",
  "recipeIngredient": ["whole chicken", "butter"],
  "recipeInstructions": ["roast at 400F for 1 hour"]
}
</script>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	args := []string{"inspect", "--url", srv.URL, "--json"}

	code := RunCLI(context.Background(), args, &stdout, &stderr, nil)
	if code != ExitSuccess {
		t.Fatalf("inspect failed: code=%d, stderr=%s", code, stderr.String())
	}

	var report feeder.InspectReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("failed to unmarshal JSON report: %v, raw: %s", err, stdout.String())
	}

	if report.Preflight.Verdict != "valid" {
		t.Errorf("Preflight.Verdict = %q, want valid", report.Preflight.Verdict)
	}
	if report.Preflight.Title != "Roast Chicken" {
		t.Errorf("Preflight.Title = %q, want Roast Chicken", report.Preflight.Title)
	}
}

func TestCLIStatusJSON(t *testing.T) {
	t.Parallel()

	tmpDB := filepath.Join(t.TempDir(), "status-test.db")
	var stdout, stderr bytes.Buffer
	args := []string{"status", "--db", tmpDB, "--json"}

	code := RunCLI(context.Background(), args, &stdout, &stderr, nil)
	if code != ExitSuccess {
		t.Fatalf("status failed: code=%d, stderr=%s", code, stderr.String())
	}

	var report feeder.StatusReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("failed to unmarshal JSON status report: %v, raw: %s", err, stdout.String())
	}

	if report.LedgerSummary.TotalRecipes != 0 {
		t.Errorf("TotalRecipes = %d, want 0", report.LedgerSummary.TotalRecipes)
	}
}

func TestCLILogFormatJSON(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	args := []string{"--log-format=json", "--log-level=info", "version"}

	code := RunCLI(context.Background(), args, &stdout, &stderr, nil)
	if code != ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d", code)
	}
}
