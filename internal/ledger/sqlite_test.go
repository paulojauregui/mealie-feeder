package ledger

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpenSQLite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("empty path fails", func(t *testing.T) {
		t.Parallel()
		store, err := OpenSQLite(ctx, "")
		if err == nil {
			t.Fatal("expected error for empty path, got nil")
		}
		if store != nil {
			_ = store.Close()
		}
	})

	t.Run("memory db opens and closes", func(t *testing.T) {
		t.Parallel()
		store, err := OpenSQLite(ctx, ":memory:")
		if err != nil {
			t.Fatalf("OpenSQLite(:memory:) failed: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("Close failed: %v", err)
		}
		// Second close should be safe
		if err := store.Close(); err != nil {
			t.Errorf("second Close failed: %v", err)
		}
	})

	t.Run("file db opens and reopens idempotently", func(t *testing.T) {
		t.Parallel()
		dbPath := filepath.Join(t.TempDir(), "sub", "test.db")
		store1, err := OpenSQLite(ctx, dbPath)
		if err != nil {
			t.Fatalf("first OpenSQLite failed: %v", err)
		}

		rec := RecipeRecord{
			CanonicalURL: "https://example.com/recipe1",
			Title:        "Recipe 1",
			Cuisine:      "Italian",
		}
		inserted, err := store1.UpsertDiscovered(ctx, rec)
		if err != nil || !inserted {
			t.Fatalf("UpsertDiscovered failed: inserted=%v, err=%v", inserted, err)
		}
		if err := store1.Close(); err != nil {
			t.Fatalf("Close store1 failed: %v", err)
		}

		// Reopen existing db
		store2, err := OpenSQLite(ctx, dbPath)
		if err != nil {
			t.Fatalf("reopening OpenSQLite failed: %v", err)
		}
		defer store2.Close()

		found, ok, err := store2.GetByCanonicalURL(ctx, "https://example.com/recipe1")
		if err != nil || !ok {
			t.Fatalf("GetByCanonicalURL after reopen failed: ok=%v, err=%v", ok, err)
		}
		if found.Title != "Recipe 1" {
			t.Errorf("Title = %q, want Recipe 1", found.Title)
		}
	})
}

func TestUpsertDiscoveredAndDeduplication(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer store.Close()

	rec := RecipeRecord{
		CanonicalURL: "https://www.seriouseats.com/foo-recipe-123/",
		Title:        "Foo Recipe",
		Cuisine:      "Mexican",
	}

	inserted, err := store.UpsertDiscovered(ctx, rec)
	if err != nil || !inserted {
		t.Fatalf("first insert failed: inserted=%v, err=%v", inserted, err)
	}

	// Exact duplicate should not insert
	inserted2, err := store.UpsertDiscovered(ctx, rec)
	if err != nil || inserted2 {
		t.Fatalf("exact duplicate should not be inserted: inserted=%v, err=%v", inserted2, err)
	}

	// DedupeKey equivalent (missing www. and trailing slash) should not insert
	equivRec := RecipeRecord{
		CanonicalURL: "https://seriouseats.com/foo-recipe-123",
		Title:        "Foo Recipe Alternate",
	}
	inserted3, err := store.UpsertDiscovered(ctx, equivRec)
	if err != nil || inserted3 {
		t.Fatalf("dedupeKey equivalent should not be inserted: inserted=%v, err=%v", inserted3, err)
	}

	// Mark the recipe imported
	err = store.MarkImported(ctx, "https://www.seriouseats.com/foo-recipe-123/", "foo-slug", "Foo Recipe Final", 200, time.Now())
	if err != nil {
		t.Fatalf("MarkImported failed: %v", err)
	}

	// Attempting to upsert again should not change state back to discovered
	inserted4, err := store.UpsertDiscovered(ctx, rec)
	if err != nil || inserted4 {
		t.Fatalf("re-upserting imported URL should return false: inserted=%v, err=%v", inserted4, err)
	}

	r, ok, err := store.GetByCanonicalURL(ctx, "https://www.seriouseats.com/foo-recipe-123/")
	if err != nil || !ok {
		t.Fatalf("GetByCanonicalURL failed: ok=%v, err=%v", ok, err)
	}
	if r.State != StateImported {
		t.Errorf("state = %q, want imported", r.State)
	}
	if r.MealieSlug != "foo-slug" {
		t.Errorf("mealie_slug = %q, want foo-slug", r.MealieSlug)
	}
}

func TestReconcileMealieOrgURLs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer store.Close()

	// Seed one existing discovered item
	_, err = store.UpsertDiscovered(ctx, RecipeRecord{
		CanonicalURL: "https://recipetineats.com/chicken-curry/",
		Title:        "Chicken Curry",
	})
	if err != nil {
		t.Fatalf("UpsertDiscovered failed: %v", err)
	}

	now := time.Now().UTC()
	orgURLs := map[string]string{
		"https://www.recipetineats.com/chicken-curry": "chicken-curry-slug", // DedupeKey match
		"https://budgetbytes.com/pasta/":              "pasta-slug",         // Brand new URL
	}

	reconciled, err := store.ReconcileMealieOrgURLs(ctx, orgURLs, now)
	if err != nil {
		t.Fatalf("ReconcileMealieOrgURLs failed: %v", err)
	}
	if reconciled != 2 {
		t.Errorf("reconciled = %d, want 2", reconciled)
	}

	// Verify recipetineats URL is now imported with slug
	rec1, ok, err := store.GetByCanonicalURL(ctx, "https://recipetineats.com/chicken-curry/")
	if err != nil || !ok {
		t.Fatalf("rec1 lookup failed: %v", err)
	}
	if rec1.State != StateImported || rec1.MealieSlug != "chicken-curry-slug" {
		t.Errorf("rec1 = %+v, want imported chicken-curry-slug", rec1)
	}

	// Verify budgetbytes URL was inserted as imported with discovery_source mealie_sync
	rec2, ok, err := store.GetByCanonicalURL(ctx, "https://budgetbytes.com/pasta/")
	if err != nil || !ok {
		t.Fatalf("rec2 lookup failed: %v", err)
	}
	if rec2.State != StateImported || rec2.DiscoverySource != DiscoveryMealieSync {
		t.Errorf("rec2 = %+v, want imported mealie_sync", rec2)
	}

	// Running reconciliation again with same URLs should be idempotent
	reconciled2, err := store.ReconcileMealieOrgURLs(ctx, orgURLs, now)
	if err != nil {
		t.Fatalf("second ReconcileMealieOrgURLs failed: %v", err)
	}
	if reconciled2 != 0 {
		t.Errorf("second reconciled = %d, want 0", reconciled2)
	}
}

func TestMarkFailedAndListPendingByDomain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer store.Close()

	recURL := "https://example.com/test-recipe"
	_, err = store.UpsertDiscovered(ctx, RecipeRecord{
		CanonicalURL: recURL,
		Domain:       "example.com",
		Title:        "Test Recipe",
	})
	if err != nil {
		t.Fatalf("UpsertDiscovered failed: %v", err)
	}

	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

	// Before any failure, it is pending
	pending, err := store.ListPendingByDomain(ctx, "example.com", now, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected 1 pending recipe, got %d, err=%v", len(pending), err)
	}

	// First transient failure
	err = store.MarkFailed(ctx, recURL, "503 service unavailable", 503, false, 3, now)
	if err != nil {
		t.Fatalf("MarkFailed failed: %v", err)
	}

	// Immediately after failure at 'now', it should NOT be in pending because next_retry_at is now + 1h
	pendingNow, err := store.ListPendingByDomain(ctx, "example.com", now, 10)
	if err != nil {
		t.Fatalf("ListPendingByDomain failed: %v", err)
	}
	if len(pendingNow) != 0 {
		t.Errorf("expected 0 pending immediately after failure, got %d", len(pendingNow))
	}

	// 2 hours later, next_retry_at has passed, so it should be pending again
	pendingLater, err := store.ListPendingByDomain(ctx, "example.com", now.Add(2*time.Hour), 10)
	if err != nil || len(pendingLater) != 1 {
		t.Fatalf("expected 1 pending after backoff, got %d, err=%v", len(pendingLater), err)
	}
	if pendingLater[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1", pendingLater[0].Attempts)
	}

	// Second transient failure at attempts=2
	err = store.MarkFailed(ctx, recURL, "500 internal server error", 500, false, 3, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("second MarkFailed failed: %v", err)
	}

	// Third failure reaches maxAttempts=3 -> should become failed_permanent
	err = store.MarkFailed(ctx, recURL, "404 not found", 404, false, 3, now.Add(10*time.Hour))
	if err != nil {
		t.Fatalf("third MarkFailed failed: %v", err)
	}

	r, ok, err := store.GetByCanonicalURL(ctx, recURL)
	if err != nil || !ok {
		t.Fatalf("GetByCanonicalURL failed: %v", err)
	}
	if r.State != StateFailedPermanent {
		t.Errorf("state = %q, want failed_permanent", r.State)
	}
	if !r.NextRetryAt.IsZero() {
		t.Errorf("expected NextRetryAt to be zero for permanent failure, got %v", r.NextRetryAt)
	}

	// Even 100 days later, failed_permanent is never listed in pending
	pendingNever, err := store.ListPendingByDomain(ctx, "example.com", now.Add(2400*time.Hour), 10)
	if err != nil || len(pendingNever) != 0 {
		t.Errorf("expected 0 pending for permanent failure, got %d", len(pendingNever))
	}
}

func TestMarkSkipped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer store.Close()

	recURL := "https://example.com/article"
	_, err = store.UpsertDiscovered(ctx, RecipeRecord{
		CanonicalURL: recURL,
	})
	if err != nil {
		t.Fatalf("UpsertDiscovered failed: %v", err)
	}

	now := time.Now().UTC()
	err = store.MarkSkipped(ctx, recURL, StateSkippedNonRecipe, "missing ingredients", 200, now)
	if err != nil {
		t.Fatalf("MarkSkipped failed: %v", err)
	}

	r, ok, err := store.GetByCanonicalURL(ctx, recURL)
	if err != nil || !ok {
		t.Fatalf("GetByCanonicalURL failed: %v", err)
	}
	if r.State != StateSkippedNonRecipe {
		t.Errorf("state = %q, want skipped_non_recipe", r.State)
	}
	if r.LastError != "missing ingredients" {
		t.Errorf("last_error = %q, want 'missing ingredients'", r.LastError)
	}

	// Invalid state rejected
	if err := store.MarkSkipped(ctx, recURL, StateDiscovered, "reason", 200, now); err == nil {
		t.Error("expected error for invalid skip state, got nil")
	}
}

func TestFeedCursorsAndRunTelemetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer store.Close()

	// Feed Cursor tests
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	cursor := FeedCursor{
		FeedURL:             "https://example.com/feed.xml",
		Domain:              "example.com",
		ETag:                `"etag-123"`,
		LastModified:        "Sun, 15 Nov 2026 12:00:00 GMT",
		LastItemPublishedAt: now.Add(-1 * time.Hour),
		LastCheckedAt:       now,
	}

	if err := store.SaveFeedCursor(ctx, cursor); err != nil {
		t.Fatalf("SaveFeedCursor failed: %v", err)
	}

	gotCursor, ok, err := store.GetFeedCursor(ctx, "https://example.com/feed.xml")
	if err != nil || !ok {
		t.Fatalf("GetFeedCursor failed: ok=%v, err=%v", ok, err)
	}
	if gotCursor.ETag != cursor.ETag || gotCursor.LastModified != cursor.LastModified {
		t.Errorf("cursor mismatch: got %+v, want %+v", gotCursor, cursor)
	}

	// Missing feed cursor
	_, okMissing, err := store.GetFeedCursor(ctx, "https://example.com/nonexistent")
	if err != nil || okMissing {
		t.Errorf("expected absent cursor, got ok=%v, err=%v", okMissing, err)
	}

	// Run Records and Status Summary
	run := RunRecord{
		RunID:           "run-101",
		Command:         "sync",
		StartedAt:       now,
		Status:          "running",
		DiscoveredCount: 10,
	}
	if err := store.RecordRunStart(ctx, run); err != nil {
		t.Fatalf("RecordRunStart failed: %v", err)
	}

	run.FinishedAt = now.Add(5 * time.Minute)
	run.Status = "success"
	run.ImportedCount = 8
	run.SkippedCount = 2
	if err := store.RecordRunFinish(ctx, run); err != nil {
		t.Fatalf("RecordRunFinish failed: %v", err)
	}

	summary, err := store.GetStatusSummary(ctx, 5)
	if err != nil {
		t.Fatalf("GetStatusSummary failed: %v", err)
	}
	if len(summary.RecentRuns) != 1 {
		t.Fatalf("expected 1 recent run, got %d", len(summary.RecentRuns))
	}
	if summary.RecentRuns[0].RunID != "run-101" || summary.RecentRuns[0].Status != "success" {
		t.Errorf("unexpected run in summary: %+v", summary.RecentRuns[0])
	}
}

func TestConcurrentAccessUnderRace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer store.Close()

	const numWorkers = 10
	const itemsPerWorker = 20
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		workerID := w
		go func() {
			defer wg.Done()
			for i := 0; i < itemsPerWorker; i++ {
				url := fmt.Sprintf("https://worker%d.com/recipe-%d", workerID, i)
				_, err := store.UpsertDiscovered(ctx, RecipeRecord{
					CanonicalURL: url,
					Title:        fmt.Sprintf("Recipe %d-%d", workerID, i),
				})
				if err != nil {
					t.Errorf("worker %d upsert %d failed: %v", workerID, i, err)
				}
				_ = store.MarkImported(ctx, url, fmt.Sprintf("slug-%d-%d", workerID, i), "", 200, time.Now())
			}
		}()
	}

	wg.Wait()

	summary, err := store.GetStatusSummary(ctx, 5)
	if err != nil {
		t.Fatalf("GetStatusSummary failed: %v", err)
	}
	expectedTotal := numWorkers * itemsPerWorker
	if summary.TotalRecipes != expectedTotal {
		t.Errorf("TotalRecipes = %d, want %d", summary.TotalRecipes, expectedTotal)
	}
}

func TestUpsertDiscoveredBackfillNoDeadlock(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	store, err := OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer store.Close()

	// Pre-populate unrelated row plus target sitemap-discovered row missing title and cuisine.
	if _, err := store.UpsertDiscovered(ctx, RecipeRecord{
		CanonicalURL:    "https://www.recipetineats.com/unrelated-sitemap-entry/",
		Domain:          "recipetineats.com",
		DiscoverySource: DiscoverySitemap,
	}); err != nil {
		t.Fatalf("insert unrelated row failed: %v", err)
	}

	targetURL := "https://www.recipetineats.com/teriyaki-chicken-meatballs/"
	inserted, err := store.UpsertDiscovered(ctx, RecipeRecord{
		CanonicalURL:    targetURL,
		Domain:          "recipetineats.com",
		DiscoverySource: DiscoverySitemap,
	})
	if err != nil || !inserted {
		t.Fatalf("insert target row failed: inserted=%v err=%v", inserted, err)
	}

	// Re-upserting with title/cuisine and trailing-slash/www alias must backfill without deadlocking MaxOpenConns(1).
	inserted, err = store.UpsertDiscovered(ctx, RecipeRecord{
		CanonicalURL:    "https://recipetineats.com/teriyaki-chicken-meatballs",
		Domain:          "recipetineats.com",
		DiscoverySource: DiscoverySeed,
		Title:           "Teriyaki Chicken Meatballs",
		Cuisine:         "japanese-deep-cuts",
	})
	if err != nil {
		t.Fatalf("backfill UpsertDiscovered failed: %v", err)
	}
	if inserted {
		t.Error("expected inserted=false when backfilling existing discovered row")
	}

	rec, found, err := store.GetByCanonicalURL(ctx, "https://recipetineats.com/teriyaki-chicken-meatballs")
	if err != nil || !found {
		t.Fatalf("GetByCanonicalURL alias lookup failed: found=%v err=%v", found, err)
	}
	if rec.Title != "Teriyaki Chicken Meatballs" || rec.Cuisine != "japanese-deep-cuts" {
		t.Errorf("backfilled metadata mismatch: Title=%q Cuisine=%q", rec.Title, rec.Cuisine)
	}
}
