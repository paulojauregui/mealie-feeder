package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

var memCounter uint64

// Store defines the persistence contract required by the ingestion orchestrator and CLI status reporter.
// Pre-conditions: Implementations must be safe for concurrent use across goroutines or serialize SQLite writes cleanly.
// Post-conditions: All timestamps persisted and returned in UTC RFC3339 format.
type Store interface {
	Close() error
	UpsertDiscovered(ctx context.Context, rec RecipeRecord) (inserted bool, err error)
	GetByCanonicalURL(ctx context.Context, canonicalURL string) (RecipeRecord, bool, error)
	ReconcileMealieOrgURLs(ctx context.Context, orgURLToSlug map[string]string, now time.Time) (reconciled int, err error)
	ListPendingByDomain(ctx context.Context, domain string, now time.Time, limit int) ([]RecipeRecord, error)
	MarkImported(ctx context.Context, canonicalURL, mealieSlug, title string, httpStatus int, now time.Time) error
	MarkSkipped(ctx context.Context, canonicalURL string, state State, reason string, httpStatus int, now time.Time) error
	MarkFailed(ctx context.Context, canonicalURL string, errMsg string, httpStatus int, permanent bool, maxAttempts int, now time.Time) error
	GetFeedCursor(ctx context.Context, feedURL string) (FeedCursor, bool, error)
	SaveFeedCursor(ctx context.Context, cursor FeedCursor) error
	RecordRunStart(ctx context.Context, run RunRecord) error
	RecordRunFinish(ctx context.Context, run RunRecord) error
	GetStatusSummary(ctx context.Context, recentLimit int) (StatusSummary, error)
}

// SQLiteStore persists mealie-feeder state in SQLite via modernc.org/sqlite ("sqlite" driver).
// Pre-conditions: Created exclusively via OpenSQLite.
// Post-conditions: Enforces WAL journal mode, busy_timeout=5000ms, and foreign_keys=ON; single-writer connection pool avoids SQLITE_BUSY under concurrency.
type SQLiteStore struct {
	db *sql.DB
	mu sync.Mutex
}

const ddlSchema = `
CREATE TABLE IF NOT EXISTS recipes_ledger (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_url     TEXT NOT NULL UNIQUE,
    source_url        TEXT NOT NULL,
    domain            TEXT NOT NULL,
    discovery_source  TEXT NOT NULL,
    cuisine           TEXT NOT NULL DEFAULT '',
    title             TEXT NOT NULL DEFAULT '',
    state             TEXT NOT NULL CHECK (state IN (
        'discovered',
        'imported',
        'skipped_non_recipe',
        'skipped_paywall',
        'failed_retryable',
        'failed_permanent'
    )),
    mealie_slug       TEXT NOT NULL DEFAULT '',
    http_status       INTEGER NOT NULL DEFAULT 0,
    attempts          INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT NOT NULL DEFAULT '',
    next_retry_at     TEXT NOT NULL DEFAULT '',
    discovered_at     TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ledger_state_retry
    ON recipes_ledger(state, next_retry_at);
CREATE INDEX IF NOT EXISTS idx_ledger_domain_state
    ON recipes_ledger(domain, state);

CREATE TABLE IF NOT EXISTS feed_cursors (
    feed_url               TEXT PRIMARY KEY,
    domain                 TEXT NOT NULL,
    etag                   TEXT NOT NULL DEFAULT '',
    last_modified          TEXT NOT NULL DEFAULT '',
    last_item_published_at TEXT NOT NULL DEFAULT '',
    last_checked_at        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
    run_id            TEXT PRIMARY KEY,
    command           TEXT NOT NULL,
    started_at        TEXT NOT NULL,
    finished_at       TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL,
    discovered_count  INTEGER NOT NULL DEFAULT 0,
    imported_count    INTEGER NOT NULL DEFAULT 0,
    skipped_count     INTEGER NOT NULL DEFAULT 0,
    failed_count      INTEGER NOT NULL DEFAULT 0,
    dry_run           INTEGER NOT NULL DEFAULT 0,
    error_summary     TEXT NOT NULL DEFAULT ''
);
`

// OpenSQLite opens or creates the SQLite database at path (or ":memory:"), applies PRAGMAs, and executes idempotent DDL migrations.
// Pre-conditions: path must be non-empty; parent directory must exist or be creatable when file-backed.
// Post-conditions: Returns initialized *SQLiteStore with recipes_ledger, feed_cursors, runs tables and indexes ready.
func OpenSQLite(ctx context.Context, path string) (*SQLiteStore, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, errors.New("ledger: sqlite path is empty")
	}

	var dsn string
	if trimmed == ":memory:" {
		id := atomic.AddUint64(&memCounter, 1)
		dsn = fmt.Sprintf("file:mem_%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", id)
	} else {
		dir := filepath.Dir(trimmed)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("ledger: create directory %q: %w", dir, err)
			}
		}
		separator := "?"
		if strings.Contains(trimmed, "?") {
			separator = "&"
		}
		dsn = fmt.Sprintf("%s%s_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", trimmed, separator)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("ledger: open sqlite db %q: %w", path, err)
	}

	// Single writer connection pool ensures serial writes and avoids SQLITE_BUSY under concurrency
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ledger: ping sqlite db %q: %w", path, err)
	}

	if _, err := db.ExecContext(ctx, ddlSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ledger: execute ddl schema on %q: %w", path, err)
	}

	return &SQLiteStore{db: db}, nil
}

// Close releases the underlying SQLite database connection.
// Pre-conditions: Store must be initialized.
// Post-conditions: Closes the SQLite database.
func (s *SQLiteStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// formatTime converts a time.Time to an RFC3339 UTC string.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// parseTime parses an RFC3339 UTC string into time.Time.
func parseTime(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return parsed.UTC()
	}
	parsed, err = time.Parse(time.RFC3339, s)
	if err == nil {
		return parsed.UTC()
	}
	return time.Time{}
}

// UpsertDiscovered attempts to insert a newly discovered candidate URL into recipes_ledger.
// Pre-conditions: rec.CanonicalURL must be a valid URL; ctx must not be nil.
// Post-conditions: Returns (true, nil) if inserted; returns (false, nil) if already present or matched via DedupeKey.
func (s *SQLiteStore) UpsertDiscovered(ctx context.Context, rec RecipeRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normURL, err := NormalizeURL(rec.CanonicalURL)
	if err != nil {
		return false, fmt.Errorf("ledger: normalize url %q: %w", rec.CanonicalURL, err)
	}
	rec.CanonicalURL = normURL

	if rec.Domain == "" {
		domain, err := ExtractDomain(normURL)
		if err != nil {
			return false, fmt.Errorf("ledger: extract domain from %q: %w", normURL, err)
		}
		rec.Domain = domain
	}

	dedupeKey, err := DedupeKey(normURL)
	if err != nil {
		return false, fmt.Errorf("ledger: dedupe key for %q: %w", normURL, err)
	}

	// Check if exact canonical_url or any matching dedupe key exists for this domain
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, canonical_url, state, title, cuisine
		FROM recipes_ledger
		WHERE domain = ?
	`, rec.Domain)
	if err != nil {
		return false, fmt.Errorf("ledger: check existing url: %w", err)
	}
	defer rows.Close()

	var existingID int64
	var existingState State
	var existingTitle, existingCuisine string
	var matchFound bool

	for rows.Next() {
		var id int64
		var cURL string
		var st State
		var tit, cuis string
		if err := rows.Scan(&id, &cURL, &st, &tit, &cuis); err != nil {
			return false, fmt.Errorf("ledger: scan row: %w", err)
		}
		if cURL == normURL {
			existingID = id
			existingState = st
			existingTitle = tit
			existingCuisine = cuis
			matchFound = true
			break
		}
		existingDedupeKey, err := DedupeKey(cURL)
		if err == nil && existingDedupeKey == dedupeKey {
			existingID = id
			existingState = st
			existingTitle = tit
			existingCuisine = cuis
			matchFound = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("ledger: rows iterate: %w", err)
	}

	if matchFound {
		// If in discovered state and incoming record has missing title/cuisine, backfill them
		if existingState == StateDiscovered && (existingTitle == "" || existingCuisine == "") {
			newTitle := existingTitle
			if newTitle == "" {
				newTitle = rec.Title
			}
			newCuisine := existingCuisine
			if newCuisine == "" {
				newCuisine = rec.Cuisine
			}
			if newTitle != existingTitle || newCuisine != existingCuisine {
				_, _ = s.db.ExecContext(ctx, `
					UPDATE recipes_ledger
					SET title = ?, cuisine = ?, updated_at = ?
					WHERE id = ?
				`, newTitle, newCuisine, formatTime(time.Now()), existingID)
			}
		}
		return false, nil
	}

	nowStr := formatTime(time.Now())
	if !rec.DiscoveredAt.IsZero() {
		nowStr = formatTime(rec.DiscoveredAt)
	}

	srcURL := rec.SourceURL
	if srcURL == "" {
		srcURL = normURL
	}
	discSrc := string(rec.DiscoverySource)
	if discSrc == "" {
		discSrc = string(DiscoverySeed)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO recipes_ledger (
			canonical_url, source_url, domain, discovery_source, cuisine, title,
			state, mealie_slug, http_status, attempts, last_error, next_retry_at,
			discovered_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		normURL,
		srcURL,
		rec.Domain,
		discSrc,
		rec.Cuisine,
		rec.Title,
		string(StateDiscovered),
		"",
		0,
		0,
		"",
		"",
		nowStr,
		nowStr,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return false, nil
		}
		return false, fmt.Errorf("ledger: insert discovered url: %w", err)
	}

	return true, nil
}

// GetByCanonicalURL retrieves a RecipeRecord by its exact canonical URL.
// Pre-conditions: canonicalURL must be non-empty; ctx must not be nil.
// Post-conditions: Returns (record, true, nil) if found; (zero, false, nil) if absent.
func (s *SQLiteStore) GetByCanonicalURL(ctx context.Context, canonicalURL string) (RecipeRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normURL, err := NormalizeURL(canonicalURL)
	if err != nil {
		return RecipeRecord{}, false, fmt.Errorf("ledger: normalize url %q: %w", canonicalURL, err)
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT id, canonical_url, source_url, domain, discovery_source, cuisine, title,
		       state, mealie_slug, http_status, attempts, last_error, next_retry_at,
		       discovered_at, updated_at
		FROM recipes_ledger
		WHERE canonical_url = ?
	`, normURL)

	var rec RecipeRecord
	var discSrc, st string
	var nextRetryStr, discStr, updStr string

	err = row.Scan(
		&rec.ID,
		&rec.CanonicalURL,
		&rec.SourceURL,
		&rec.Domain,
		&discSrc,
		&rec.Cuisine,
		&rec.Title,
		&st,
		&rec.MealieSlug,
		&rec.HTTPStatus,
		&rec.Attempts,
		&rec.LastError,
		&nextRetryStr,
		&discStr,
		&updStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RecipeRecord{}, false, nil
		}
		return RecipeRecord{}, false, fmt.Errorf("ledger: scan recipe: %w", err)
	}

	rec.DiscoverySource = DiscoverySource(discSrc)
	rec.State = State(st)
	rec.NextRetryAt = parseTime(nextRetryStr)
	rec.DiscoveredAt = parseTime(discStr)
	rec.UpdatedAt = parseTime(updStr)

	return rec, true, nil
}

// ReconcileMealieOrgURLs reconciles Mealie existing inventory with recipes_ledger in a single transaction.
// Pre-conditions: orgURLToSlug map contains canonical or raw Mealie orgURLs mapped to slugs.
// Post-conditions: Existing matching rows are marked imported; absent URLs are inserted as imported. Returns reconciled count.
func (s *SQLiteStore) ReconcileMealieOrgURLs(ctx context.Context, orgURLToSlug map[string]string, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("ledger: begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	nowStr := formatTime(now)
	reconciled := 0

	for orgURL, slug := range orgURLToSlug {
		if strings.TrimSpace(orgURL) == "" {
			continue
		}
		normURL, err := NormalizeURL(orgURL)
		if err != nil {
			continue
		}
		domain, err := ExtractDomain(normURL)
		if err != nil {
			continue
		}
		dedupeKey, _ := DedupeKey(normURL)

		// Search domain candidates
		rows, err := tx.QueryContext(ctx, `
			SELECT id, canonical_url, state, mealie_slug
			FROM recipes_ledger
			WHERE domain = ?
		`, domain)
		if err != nil {
			return 0, fmt.Errorf("ledger: query domain %q: %w", domain, err)
		}

		var matchedID int64
		var matchedState State
		var matchedSlug string
		var found bool

		for rows.Next() {
			var id int64
			var cURL string
			var st State
			var sl string
			if err := rows.Scan(&id, &cURL, &st, &sl); err != nil {
				rows.Close()
				return 0, fmt.Errorf("ledger: scan domain row: %w", err)
			}
			if cURL == normURL {
				matchedID = id
				matchedState = st
				matchedSlug = sl
				found = true
				break
			}
			dk, _ := DedupeKey(cURL)
			if dk == dedupeKey {
				matchedID = id
				matchedState = st
				matchedSlug = sl
				found = true
				break
			}
		}
		rows.Close()

		if found {
			if matchedState != StateImported || matchedSlug != slug {
				_, err = tx.ExecContext(ctx, `
					UPDATE recipes_ledger
					SET state = ?, mealie_slug = ?, next_retry_at = '', updated_at = ?
					WHERE id = ?
				`, string(StateImported), slug, nowStr, matchedID)
				if err != nil {
					return 0, fmt.Errorf("ledger: update imported state: %w", err)
				}
				reconciled++
			}
		} else {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO recipes_ledger (
					canonical_url, source_url, domain, discovery_source, cuisine, title,
					state, mealie_slug, http_status, attempts, last_error, next_retry_at,
					discovered_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`,
				normURL,
				orgURL,
				domain,
				string(DiscoveryMealieSync),
				"",
				"",
				string(StateImported),
				slug,
				200,
				0,
				"",
				"",
				nowStr,
				nowStr,
			)
			if err != nil {
				if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
					return 0, fmt.Errorf("ledger: insert mealie synced recipe: %w", err)
				}
			} else {
				reconciled++
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("ledger: commit reconcile transaction: %w", err)
	}

	return reconciled, nil
}

// ListPendingByDomain returns candidate records for domain eligible for processing at time now.
// Pre-conditions: domain is non-empty; limit > 0; ctx must not be nil.
// Post-conditions: Discovered records prioritized before retries; ordered by discovered_at ascending.
func (s *SQLiteStore) ListPendingByDomain(ctx context.Context, domain string, now time.Time, limit int) ([]RecipeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normDomain, err := ExtractDomain(domain)
	if err != nil {
		normDomain = strings.ToLower(strings.TrimSpace(domain))
	}

	nowStr := formatTime(now)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, canonical_url, source_url, domain, discovery_source, cuisine, title,
		       state, mealie_slug, http_status, attempts, last_error, next_retry_at,
		       discovered_at, updated_at
		FROM recipes_ledger
		WHERE domain = ?
		  AND (
		    state = 'discovered'
		    OR (state = 'failed_retryable' AND next_retry_at <= ?)
		  )
		ORDER BY
		  CASE state WHEN 'discovered' THEN 0 ELSE 1 END,
		  discovered_at ASC
		LIMIT ?
	`, normDomain, nowStr, limit)
	if err != nil {
		return nil, fmt.Errorf("ledger: list pending recipes: %w", err)
	}
	defer rows.Close()

	var results []RecipeRecord
	for rows.Next() {
		var rec RecipeRecord
		var discSrc, st string
		var nextRetryStr, discStr, updStr string

		err := rows.Scan(
			&rec.ID,
			&rec.CanonicalURL,
			&rec.SourceURL,
			&rec.Domain,
			&discSrc,
			&rec.Cuisine,
			&rec.Title,
			&st,
			&rec.MealieSlug,
			&rec.HTTPStatus,
			&rec.Attempts,
			&rec.LastError,
			&nextRetryStr,
			&discStr,
			&updStr,
		)
		if err != nil {
			return nil, fmt.Errorf("ledger: scan pending recipe: %w", err)
		}

		rec.DiscoverySource = DiscoverySource(discSrc)
		rec.State = State(st)
		rec.NextRetryAt = parseTime(nextRetryStr)
		rec.DiscoveredAt = parseTime(discStr)
		rec.UpdatedAt = parseTime(updStr)

		results = append(results, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: rows iterate: %w", err)
	}

	return results, nil
}

// MarkImported updates a record to StateImported upon successful Mealie ingestion.
// Pre-conditions: canonicalURL must be valid; mealieSlug must be non-empty; ctx must not be nil.
// Post-conditions: State set to imported, next_retry_at cleared, updated_at recorded in UTC.
func (s *SQLiteStore) MarkImported(ctx context.Context, canonicalURL, mealieSlug, title string, httpStatus int, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normURL, err := NormalizeURL(canonicalURL)
	if err != nil {
		return fmt.Errorf("ledger: normalize url %q: %w", canonicalURL, err)
	}

	nowStr := formatTime(now)

	res, err := s.db.ExecContext(ctx, `
		UPDATE recipes_ledger
		SET state = ?,
		    mealie_slug = ?,
		    title = CASE WHEN ? != '' THEN ? ELSE title END,
		    http_status = ?,
		    next_retry_at = '',
		    updated_at = ?
		WHERE canonical_url = ?
	`, string(StateImported), mealieSlug, title, title, httpStatus, nowStr, normURL)
	if err != nil {
		return fmt.Errorf("ledger: mark imported %q: %w", canonicalURL, err)
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("ledger: recipe %q not found", canonicalURL)
	}
	return nil
}

// MarkSkipped updates a record to a terminal skipped state (skipped_non_recipe or skipped_paywall).
// Pre-conditions: state must be StateSkippedNonRecipe or StateSkippedPaywall; ctx must not be nil.
// Post-conditions: State updated, reason stored in last_error, next_retry_at cleared.
func (s *SQLiteStore) MarkSkipped(ctx context.Context, canonicalURL string, state State, reason string, httpStatus int, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if state != StateSkippedNonRecipe && state != StateSkippedPaywall {
		return fmt.Errorf("ledger: invalid skip state %q", state)
	}

	normURL, err := NormalizeURL(canonicalURL)
	if err != nil {
		return fmt.Errorf("ledger: normalize url %q: %w", canonicalURL, err)
	}

	nowStr := formatTime(now)

	res, err := s.db.ExecContext(ctx, `
		UPDATE recipes_ledger
		SET state = ?,
		    last_error = ?,
		    http_status = ?,
		    next_retry_at = '',
		    updated_at = ?
		WHERE canonical_url = ?
	`, string(state), reason, httpStatus, nowStr, normURL)
	if err != nil {
		return fmt.Errorf("ledger: mark skipped %q: %w", canonicalURL, err)
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("ledger: recipe %q not found", canonicalURL)
	}
	return nil
}

// MarkFailed handles transient or permanent failures, incrementing attempts and scheduling backoff.
// Pre-conditions: maxAttempts > 0; ctx must not be nil.
// Post-conditions: State updated to failed_retryable with backoff, or failed_permanent if attempts exhausted.
func (s *SQLiteStore) MarkFailed(ctx context.Context, canonicalURL string, errMsg string, httpStatus int, permanent bool, maxAttempts int, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normURL, err := NormalizeURL(canonicalURL)
	if err != nil {
		return fmt.Errorf("ledger: normalize url %q: %w", canonicalURL, err)
	}

	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}

	var currentAttempts int
	err = s.db.QueryRowContext(ctx, `
		SELECT attempts FROM recipes_ledger WHERE canonical_url = ?
	`, normURL).Scan(&currentAttempts)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("ledger: recipe %q not found", canonicalURL)
		}
		return fmt.Errorf("ledger: read attempts: %w", err)
	}

	newAttempts := currentAttempts + 1
	var targetState State
	var nextRetryStr string

	if permanent || newAttempts >= maxAttempts {
		targetState = StateFailedPermanent
		nextRetryStr = ""
	} else {
		targetState = StateFailedRetryable
		nextRetryStr = formatTime(ComputeNextRetry(now, newAttempts))
	}

	nowStr := formatTime(now)

	_, err = s.db.ExecContext(ctx, `
		UPDATE recipes_ledger
		SET state = ?,
		    attempts = ?,
		    last_error = ?,
		    http_status = ?,
		    next_retry_at = ?,
		    updated_at = ?
		WHERE canonical_url = ?
	`, string(targetState), newAttempts, errMsg, httpStatus, nextRetryStr, nowStr, normURL)
	if err != nil {
		return fmt.Errorf("ledger: update failed state: %w", err)
	}

	return nil
}

// GetFeedCursor reads watermark state for an RSS or Atom feed.
// Pre-conditions: feedURL must be non-empty; ctx must not be nil.
// Post-conditions: Returns (cursor, true, nil) if present; (zero, false, nil) if absent.
func (s *SQLiteStore) GetFeedCursor(ctx context.Context, feedURL string) (FeedCursor, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	trimmed := strings.TrimSpace(feedURL)
	if trimmed == "" {
		return FeedCursor{}, false, errors.New("ledger: feedURL is empty")
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT feed_url, domain, etag, last_modified, last_item_published_at, last_checked_at
		FROM feed_cursors
		WHERE feed_url = ?
	`, trimmed)

	var c FeedCursor
	var pubStr, checkStr string
	err := row.Scan(&c.FeedURL, &c.Domain, &c.ETag, &c.LastModified, &pubStr, &checkStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FeedCursor{}, false, nil
		}
		return FeedCursor{}, false, fmt.Errorf("ledger: scan feed cursor: %w", err)
	}

	c.LastItemPublishedAt = parseTime(pubStr)
	c.LastCheckedAt = parseTime(checkStr)
	return c, true, nil
}

// SaveFeedCursor persists watermark state for an RSS or Atom feed.
// Pre-conditions: cursor.FeedURL must be non-empty.
// Post-conditions: Upserts cursor row in feed_cursors table.
func (s *SQLiteStore) SaveFeedCursor(ctx context.Context, cursor FeedCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	trimmed := strings.TrimSpace(cursor.FeedURL)
	if trimmed == "" {
		return errors.New("ledger: cursor.FeedURL is empty")
	}

	if cursor.Domain == "" {
		d, err := ExtractDomain(trimmed)
		if err == nil {
			cursor.Domain = d
		}
	}

	pubStr := formatTime(cursor.LastItemPublishedAt)
	checkStr := formatTime(cursor.LastCheckedAt)
	if checkStr == "" {
		checkStr = formatTime(time.Now())
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO feed_cursors (
			feed_url, domain, etag, last_modified, last_item_published_at, last_checked_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(feed_url) DO UPDATE SET
			domain = excluded.domain,
			etag = excluded.etag,
			last_modified = excluded.last_modified,
			last_item_published_at = excluded.last_item_published_at,
			last_checked_at = excluded.last_checked_at
	`, trimmed, cursor.Domain, cursor.ETag, cursor.LastModified, pubStr, checkStr)
	if err != nil {
		return fmt.Errorf("ledger: save feed cursor %q: %w", trimmed, err)
	}

	return nil
}

// RecordRunStart records the beginning of an orchestrator run.
// Pre-conditions: run.RunID and run.Command must be non-empty.
// Post-conditions: Persists run record with running status in runs table.
func (s *SQLiteStore) RecordRunStart(ctx context.Context, run RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(run.RunID) == "" {
		return errors.New("ledger: run.RunID is empty")
	}
	if strings.TrimSpace(run.Command) == "" {
		return errors.New("ledger: run.Command is empty")
	}

	startStr := formatTime(run.StartedAt)
	if startStr == "" {
		startStr = formatTime(time.Now())
	}
	finStr := formatTime(run.FinishedAt)
	st := run.Status
	if st == "" {
		st = "running"
	}

	dryInt := 0
	if run.DryRun {
		dryInt = 1
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (
			run_id, command, started_at, finished_at, status,
			discovered_count, imported_count, skipped_count, failed_count,
			dry_run, error_summary
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		run.RunID,
		run.Command,
		startStr,
		finStr,
		st,
		run.DiscoveredCount,
		run.ImportedCount,
		run.SkippedCount,
		run.FailedCount,
		dryInt,
		run.ErrorSummary,
	)
	if err != nil {
		return fmt.Errorf("ledger: record run start %q: %w", run.RunID, err)
	}
	return nil
}

// RecordRunFinish updates metrics and completion timestamp for an orchestrator run.
// Pre-conditions: run.RunID must match an existing row.
// Post-conditions: Updates final counts, status, finished_at, and error_summary.
func (s *SQLiteStore) RecordRunFinish(ctx context.Context, run RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(run.RunID) == "" {
		return errors.New("ledger: run.RunID is empty")
	}

	finStr := formatTime(run.FinishedAt)
	if finStr == "" {
		finStr = formatTime(time.Now())
	}

	dryInt := 0
	if run.DryRun {
		dryInt = 1
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE runs
		SET finished_at = ?,
		    status = ?,
		    discovered_count = ?,
		    imported_count = ?,
		    skipped_count = ?,
		    failed_count = ?,
		    dry_run = ?,
		    error_summary = ?
		WHERE run_id = ?
	`,
		finStr,
		run.Status,
		run.DiscoveredCount,
		run.ImportedCount,
		run.SkippedCount,
		run.FailedCount,
		dryInt,
		run.ErrorSummary,
		run.RunID,
	)
	if err != nil {
		return fmt.Errorf("ledger: record run finish %q: %w", run.RunID, err)
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("ledger: run %q not found", run.RunID)
	}
	return nil
}

// GetStatusSummary aggregates counts and recent execution history for the CLI status subcommand.
// Pre-conditions: recentLimit >= 0.
// Post-conditions: Returns non-nil StatusSummary with populated ByState and ByDomain maps.
func (s *SQLiteStore) GetStatusSummary(ctx context.Context, recentLimit int) (StatusSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summary := StatusSummary{
		ByState:  make(map[State]int),
		ByDomain: make(map[string]map[State]int),
	}

	// Total count
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recipes_ledger`).Scan(&summary.TotalRecipes)
	if err != nil {
		return StatusSummary{}, fmt.Errorf("ledger: query total count: %w", err)
	}

	// By state
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM recipes_ledger GROUP BY state`)
	if err != nil {
		return StatusSummary{}, fmt.Errorf("ledger: query state counts: %w", err)
	}
	for rows.Next() {
		var st string
		var count int
		if err := rows.Scan(&st, &count); err == nil {
			summary.ByState[State(st)] = count
		}
	}
	rows.Close()

	// By domain and state
	dRows, err := s.db.QueryContext(ctx, `SELECT domain, state, COUNT(*) FROM recipes_ledger GROUP BY domain, state`)
	if err != nil {
		return StatusSummary{}, fmt.Errorf("ledger: query domain state counts: %w", err)
	}
	for dRows.Next() {
		var dom, st string
		var count int
		if err := dRows.Scan(&dom, &st, &count); err == nil {
			if summary.ByDomain[dom] == nil {
				summary.ByDomain[dom] = make(map[State]int)
			}
			summary.ByDomain[dom][State(st)] = count
		}
	}
	dRows.Close()

	// Recent runs
	if recentLimit <= 0 {
		recentLimit = 5
	}
	rRows, err := s.db.QueryContext(ctx, `
		SELECT run_id, command, started_at, finished_at, status,
		       discovered_count, imported_count, skipped_count, failed_count,
		       dry_run, error_summary
		FROM runs
		ORDER BY started_at DESC
		LIMIT ?
	`, recentLimit)
	if err != nil {
		return StatusSummary{}, fmt.Errorf("ledger: query recent runs: %w", err)
	}
	defer rRows.Close()

	for rRows.Next() {
		var r RunRecord
		var startStr, finStr string
		var dryInt int
		err := rRows.Scan(
			&r.RunID,
			&r.Command,
			&startStr,
			&finStr,
			&r.Status,
			&r.DiscoveredCount,
			&r.ImportedCount,
			&r.SkippedCount,
			&r.FailedCount,
			&dryInt,
			&r.ErrorSummary,
		)
		if err == nil {
			r.StartedAt = parseTime(startStr)
			r.FinishedAt = parseTime(finStr)
			r.DryRun = dryInt == 1
			summary.RecentRuns = append(summary.RecentRuns, r)
		}
	}

	return summary, nil
}
