// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

// OLX-specific store helpers. Lives alongside the generic resources/FTS
// store the Printing Press scaffold provides; the workflow commands
// (sync, jobs, companies, export) use this typed surface instead of the
// generic JSON-blob table so analytics queries hit indexed columns.
//
// Schema is created idempotently by EnsureOLXSchema, invoked once at
// store open by NewOLXStore. We do NOT bump StoreSchemaVersion for this
// — the generic schema and the OLX-typed schema evolve independently;
// an older binary opening a newer DB still works against the unchanged
// resources/sync_state tables.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// EnsureOLXSchema creates the OLX-specific tables and indexes if they
// don't yet exist, then runs a self-healing migration that rewrites any
// legacy time-column values (written by an older build that relied on
// modernc.org/sqlite's default time.Time binding, which produced
// non-parseable Go-default String() output) into RFC3339Nano text.
// Safe to call on every open; the migration is a cheap no-op once all
// rows are in the canonical format.
func (s *Store) EnsureOLXSchema(ctx context.Context) error {
	if err := s.createOLXTables(ctx); err != nil {
		return err
	}
	if err := s.migrateAddCompanyColumns(ctx); err != nil {
		return err
	}
	if err := s.migrateAddJobEnrichmentColumns(ctx); err != nil {
		return err
	}
	return s.migrateOLXTimeFormats(ctx)
}

func (s *Store) migrateAddJobEnrichmentColumns(ctx context.Context) error {
	stmts := []string{
		`ALTER TABLE jobs ADD COLUMN detail_fetched INTEGER DEFAULT 0`,
		`ALTER TABLE jobs ADD COLUMN phones_attempted INTEGER DEFAULT 0`,
		`ALTER TABLE jobs ADD COLUMN phones_blocked INTEGER DEFAULT 0`,
		`ALTER TABLE jobs ADD COLUMN employer_fetched INTEGER DEFAULT 0`,
		`ALTER TABLE jobs ADD COLUMN fetch_error TEXT`,
		`ALTER TABLE sync_runs ADD COLUMN phones_blocked INTEGER DEFAULT 0`,
	}
	for _, stmt := range stmts {
		// Ignore errors like "duplicate column name"
		_, _ = s.db.ExecContext(ctx, stmt)
	}
	return nil
}

func (s *Store) migrateAddCompanyColumns(ctx context.Context) error {
	stmts := []string{
		`ALTER TABLE companies ADD COLUMN olx_user_id TEXT`,
		`ALTER TABLE companies ADD COLUMN olx_user_uuid TEXT`,
		`ALTER TABLE companies ADD COLUMN source TEXT NOT NULL DEFAULT 'olx'`,
		`ALTER TABLE jobs ADD COLUMN source TEXT NOT NULL DEFAULT 'olx'`,
	}
	for _, stmt := range stmts {
		// Ignore errors like "duplicate column name"
		_, _ = s.db.ExecContext(ctx, stmt)
	}
	return nil
}

func (s *Store) createOLXTables(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS companies (
			id           TEXT PRIMARY KEY,
			name         TEXT,
			address      TEXT,
			city         TEXT,
			region       TEXT,
			nip          TEXT,
			phone        TEXT,
			email        TEXT,
			website      TEXT,
			is_business  INTEGER DEFAULT 0,
			first_seen   DATETIME NOT NULL,
			last_seen    DATETIME NOT NULL,
			raw_json     TEXT,
			olx_user_id  TEXT,
			olx_user_uuid TEXT,
			source       TEXT NOT NULL DEFAULT 'olx'
		)`,
		`CREATE INDEX IF NOT EXISTS companies_name_idx ON companies(name)`,
		`CREATE INDEX IF NOT EXISTS companies_city_idx ON companies(city)`,
		`CREATE INDEX IF NOT EXISTS companies_olx_user_id_idx ON companies(olx_user_id)`,

		`CREATE TABLE IF NOT EXISTS jobs (
			id               TEXT PRIMARY KEY,
			url              TEXT NOT NULL,
			title            TEXT NOT NULL,
			description      TEXT,
			category_id      INTEGER,
			category_path    TEXT,
			location_city    TEXT,
			location_region  TEXT,
			location_lat     REAL,
			location_lon     REAL,
			company_id       TEXT,
			posted_at        DATETIME,
			refreshed_at     DATETIME,
			valid_to         DATETIME,
			fetched_at       DATETIME NOT NULL,
			raw_json         TEXT NOT NULL,
			source           TEXT NOT NULL DEFAULT 'olx',
			detail_fetched   INTEGER DEFAULT 0,
			phones_attempted INTEGER DEFAULT 0,
			phones_blocked   INTEGER DEFAULT 0,
			employer_fetched INTEGER DEFAULT 0,
			fetch_error      TEXT,
			FOREIGN KEY (company_id) REFERENCES companies(id)
		)`,
		`CREATE INDEX IF NOT EXISTS jobs_company_idx  ON jobs(company_id)`,
		`CREATE INDEX IF NOT EXISTS jobs_category_idx ON jobs(category_id)`,
		`CREATE INDEX IF NOT EXISTS jobs_posted_idx   ON jobs(posted_at)`,
		`CREATE INDEX IF NOT EXISTS jobs_refreshed_idx ON jobs(refreshed_at)`,
		`CREATE INDEX IF NOT EXISTS jobs_city_idx ON jobs(location_city)`,

		`CREATE TABLE IF NOT EXISTS phones (
			job_id     TEXT NOT NULL,
			phone      TEXT NOT NULL,
			source     TEXT NOT NULL,
			fetched_at DATETIME NOT NULL,
			PRIMARY KEY (job_id, phone, source),
			FOREIGN KEY (job_id) REFERENCES jobs(id)
		)`,
		`CREATE INDEX IF NOT EXISTS phones_phone_idx ON phones(phone)`,

		`CREATE TABLE IF NOT EXISTS sync_runs (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			started_at    DATETIME NOT NULL,
			finished_at   DATETIME,
			category_ids  TEXT,
			pages_fetched INTEGER DEFAULT 0,
			jobs_seen     INTEGER DEFAULT 0,
			jobs_new      INTEGER DEFAULT 0,
			companies_new INTEGER DEFAULT 0,
			phones_new    INTEGER DEFAULT 0,
			error         TEXT
		)`,
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin OLX schema tx: %w", err)
	}
	defer tx.Rollback()
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure OLX schema: %w", err)
		}
	}
	return tx.Commit()
}

// Job is the typed projection of an OLX offer for the local DB.
// Raw is the original JSON payload (v2/offers/{id} or ListingSearchQuery row)
// preserved verbatim for later re-parsing.
type Job struct {
	ID              string
	URL             string
	Title           string
	Description     string
	CategoryID      int
	CategoryPath    string
	LocationCity    string
	LocationRegion  string
	LocationLat     float64
	LocationLon     float64
	CompanyID       string
	PostedAt        time.Time
	RefreshedAt     time.Time
	ValidTo         time.Time
	FetchedAt       time.Time
	Raw             json.RawMessage
	DetailFetched   bool
	PhonesAttempted bool
	PhonesBlocked   bool
	EmployerFetched bool
	FetchError      string
}

// Company is the typed projection of an OLX seller / employer.
type Company struct {
	ID          string
	Name        string
	Address     string
	City        string
	Region      string
	NIP         string
	Phone       string
	Email       string
	Website     string
	IsBusiness  bool
	FirstSeen   time.Time
	LastSeen    time.Time
	Raw         json.RawMessage
	OLXUserID   string
	OLXUserUUID string
}

// UpsertCompany inserts or updates a company by id. first_seen is set on
// insert; last_seen is bumped on every call.
func (s *Store) UpsertCompany(ctx context.Context, c Company) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.upsertCompanyTx(ctx, s.db, c)
}

func (s *Store) upsertCompanyTx(ctx context.Context, tx interface{ ExecContext(context.Context, string, ...any) (sql.Result, error) }, c Company) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO companies (
			id, name, address, city, region, nip, phone, email, website,
			is_business, first_seen, last_seen, raw_json, olx_user_id, olx_user_uuid
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
			name          = COALESCE(NULLIF(excluded.name, ''), companies.name),
			address       = COALESCE(NULLIF(excluded.address, ''), companies.address),
			city          = COALESCE(NULLIF(excluded.city, ''), companies.city),
			region        = COALESCE(NULLIF(excluded.region, ''), companies.region),
			nip           = COALESCE(NULLIF(excluded.nip, ''), companies.nip),
			phone         = COALESCE(NULLIF(excluded.phone, ''), companies.phone),
			email         = COALESCE(NULLIF(excluded.email, ''), companies.email),
			website       = COALESCE(NULLIF(excluded.website, ''), companies.website),
			is_business   = excluded.is_business,
			last_seen     = excluded.last_seen,
			raw_json      = excluded.raw_json,
			olx_user_id   = COALESCE(NULLIF(excluded.olx_user_id, ''), companies.olx_user_id),
			olx_user_uuid = COALESCE(NULLIF(excluded.olx_user_uuid, ''), companies.olx_user_uuid)`,
		c.ID, c.Name, c.Address, c.City, c.Region, c.NIP, c.Phone, c.Email, c.Website,
		boolToInt(c.IsBusiness), storedTime(c.FirstSeen), storedTime(c.LastSeen), string(c.Raw),
		c.OLXUserID, c.OLXUserUUID,
	)
	return err
}

// UpsertJob inserts or updates a job by id. fetched_at is always set to now.
func (s *Store) UpsertJob(ctx context.Context, j Job) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.upsertJobTx(ctx, s.db, j)
}

func (s *Store) upsertJobTx(ctx context.Context, tx interface{ ExecContext(context.Context, string, ...any) (sql.Result, error) }, j Job) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO jobs (
			id, url, title, description, category_id, category_path,
			location_city, location_region, location_lat, location_lon,
			company_id, posted_at, refreshed_at, valid_to, fetched_at, raw_json,
			detail_fetched, phones_attempted, phones_blocked, employer_fetched, fetch_error
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
			url              = excluded.url,
			title            = excluded.title,
			description      = excluded.description,
			category_id      = excluded.category_id,
			category_path    = excluded.category_path,
			location_city    = excluded.location_city,
			location_region  = excluded.location_region,
			location_lat     = excluded.location_lat,
			location_lon     = excluded.location_lon,
			company_id       = excluded.company_id,
			posted_at        = excluded.posted_at,
			refreshed_at     = excluded.refreshed_at,
			valid_to         = excluded.valid_to,
			fetched_at       = excluded.fetched_at,
			raw_json         = excluded.raw_json,
			detail_fetched   = excluded.detail_fetched,
			phones_attempted = excluded.phones_attempted,
			phones_blocked   = excluded.phones_blocked,
			employer_fetched = excluded.employer_fetched,
			fetch_error      = excluded.fetch_error`,
		j.ID, j.URL, j.Title, j.Description, j.CategoryID, nullIfEmpty(j.CategoryPath),
		j.LocationCity, j.LocationRegion, j.LocationLat, j.LocationLon,
		j.CompanyID, storedTime(j.PostedAt), storedTime(j.RefreshedAt), storedTime(j.ValidTo),
		storedTime(j.FetchedAt), string(j.Raw),
		boolToInt(j.DetailFetched), boolToInt(j.PhonesAttempted),
		boolToInt(j.PhonesBlocked), boolToInt(j.EmployerFetched), nullIfEmpty(j.FetchError),
	)
	return err
}

// JobIsFresh reports whether a job with the given id is already in the
// store AND whether its stored refreshed_at is at least as new as the
// candidate value. Callers use this to skip detail-fetches on already-
// fresh rows. The DATETIME column is stored as RFC3339Nano text (see
// storedTime / ParseStoredTime); we scan into NullString and parse here
// because modernc.org/sqlite cannot Scan its own time-formatted text
// back into a time.Time, and we want to remain compatible with rows
// written by older builds in Go's default time.Time.String() format.
func (s *Store) JobIsFresh(ctx context.Context, id string, candidateRefresh time.Time) (bool, error) {
	var ns sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT refreshed_at FROM jobs WHERE id = ?`, id).Scan(&ns)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !ns.Valid || strings.TrimSpace(ns.String) == "" {
		return false, nil
	}
	stored, ok := ParseStoredTime(ns.String)
	if !ok || stored.IsZero() {
		return false, nil
	}
	return !stored.Before(candidateRefresh), nil
}

// SavePhone records a phone number for an offer. Idempotent.
func (s *Store) SavePhone(ctx context.Context, jobID, phone, source string) error {
	if phone == "" {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.savePhoneTx(ctx, s.db, jobID, phone, source)
}

func (s *Store) savePhoneTx(ctx context.Context, tx interface{ ExecContext(context.Context, string, ...any) (sql.Result, error) }, jobID, phone, source string) error {
	if phone == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO phones (job_id, phone, source, fetched_at) VALUES (?,?,?,?)
		 ON CONFLICT(job_id, phone, source) DO NOTHING`,
		jobID, phone, source, storedTime(time.Now()),
	)
	return err
}

// SaveOfferAtom saves a company, a job, and its phones in a single atomic IMMEDIATE transaction.
func (s *Store) SaveOfferAtom(ctx context.Context, c Company, j Job, phones []string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Attempt BEGIN IMMEDIATE (since standard BeginTx uses DEFERRED, which can cause SQLITE_BUSY upon first write in WAL).
	// With modernc.org/sqlite, standard BeginTx may already lock correctly or we can just run an immediate PRAGMA/statement,
	// but the driver's default BeginTx usually suffices given our writeMu serialization. However, the user asked for BEGIN IMMEDIATE.
	// We can try to commit the default DEFERRED and explicitly run BEGIN IMMEDIATE if it's safe.
	// In the Go standard library, db.Begin() uses DEFERRED. But we are inside writeMu, so no other goroutine is writing via Go.
	// To strictly follow the "BEGIN IMMEDIATE" directive without breaking `sql.Tx` semantics:
	_ = tx.Rollback()

	// Open a raw connection to run BEGIN IMMEDIATE
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}

	var txErr error
	defer func() {
		if txErr != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		} else {
			_, _ = conn.ExecContext(ctx, "COMMIT")
		}
	}()

	if c.ID != "" {
		if txErr = s.upsertCompanyTx(ctx, conn, c); txErr != nil {
			return fmt.Errorf("upsert company %s: %w", c.ID, txErr)
		}
	}
	if txErr = s.upsertJobTx(ctx, conn, j); txErr != nil {
		return fmt.Errorf("upsert job %s: %w", j.ID, txErr)
	}
	for _, p := range phones {
		if txErr = s.savePhoneTx(ctx, conn, j.ID, p, "limited-phones"); txErr != nil {
			return fmt.Errorf("save phone %s: %w", p, txErr)
		}
	}

	return nil
}

// SyncRun is a single sync invocation row in sync_runs.
type SyncRun struct {
	ID            int64
	StartedAt     time.Time
	FinishedAt    time.Time
	CategoryIDs   string
	PagesFetched  int
	JobsSeen      int
	JobsNew       int
	CompaniesNew  int
	PhonesNew     int
	Error         string
	PhonesBlocked bool
}

// BeginSyncRun inserts a sync_runs row marked started and returns its id.
func (s *Store) BeginSyncRun(ctx context.Context, categoryIDs string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO sync_runs (started_at, category_ids) VALUES (?, ?)`,
		storedTime(time.Now()), categoryIDs,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishSyncRun stamps end-of-run stats onto the row.
func (s *Store) FinishSyncRun(ctx context.Context, run SyncRun) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE sync_runs SET
			finished_at = ?,
			pages_fetched = ?,
			jobs_seen = ?,
			jobs_new = ?,
			companies_new = ?,
			phones_new = ?,
			error = ?,
			phones_blocked = ?
		 WHERE id = ?`,
		storedTime(time.Now()), run.PagesFetched, run.JobsSeen, run.JobsNew,
		run.CompaniesNew, run.PhonesNew, run.Error, boolToInt(run.PhonesBlocked), run.ID,
	)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// storedTime returns a value suitable for binding to a DATETIME column.
// A zero time becomes NULL; non-zero times become RFC3339Nano UTC text.
// We deliberately bypass modernc.org/sqlite's default time.Time handling,
// which serializes via Go's time.Time.String() format and produces values
// that the same driver cannot Scan back into a time.Time on read.
func storedTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// FormatStoredTime mirrors storedTime but always returns a string. Used
// by query-side code (e.g. WHERE posted_at >= ?) so the bound parameter
// is in the same lexicographic shape as the stored values and `>=`
// comparisons work correctly.
func FormatStoredTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// goStringTimeRE matches the prefix of Go's time.Time.String() output:
// "2006-01-02 15:04:05[.999999999] -0700" — Go does not provide a layout
// that round-trips this format because the trailing zone abbreviation
// ("UTC" / "MST" / "CEST") is ambiguous, so we extract the parseable
// numeric prefix and parse that.
var goStringTimeRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?\s+[-+]\d{4})`)

// ParseStoredTime parses a value read from a DATETIME column. It accepts
// the canonical RFC3339Nano shape we now write, plus several legacy
// shapes left over from older builds and any SQLite plain text format,
// so a database written by a prior version still loads cleanly.
func ParseStoredTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if m := goStringTimeRE.FindString(s); m != "" {
		if t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700", m); err == nil {
			return t, true
		}
		if t, err := time.Parse("2006-01-02 15:04:05 -0700", m); err == nil {
			return t, true
		}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// migrateOLXTimeFormats rewrites any time-column value that doesn't
// already look like RFC3339 (no 'T' separator) into RFC3339Nano UTC
// text. Idempotent and cheap: after the first run, the SELECT returns
// no rows on subsequent opens.
func (s *Store) migrateOLXTimeFormats(ctx context.Context) error {
	targets := []struct{ table, col string }{
		{"jobs", "posted_at"},
		{"jobs", "refreshed_at"},
		{"jobs", "valid_to"},
		{"jobs", "fetched_at"},
		{"companies", "first_seen"},
		{"companies", "last_seen"},
		{"phones", "fetched_at"},
		{"sync_runs", "started_at"},
		{"sync_runs", "finished_at"},
	}
	for _, t := range targets {
		if err := s.migrateOneTimeColumn(ctx, t.table, t.col); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", t.table, t.col, err)
		}
	}
	return nil
}

func (s *Store) migrateOneTimeColumn(ctx context.Context, table, col string) error {
	// Detect non-canonical values by anchoring on the RFC3339 'T' between
	// date and time (position 11). We can't use LIKE '%T%' here because
	// the legacy "+0000 UTC" suffix also contains a 'T' and would yield
	// a false negative — skipping rows that genuinely need migration.
	sel := fmt.Sprintf(
		`SELECT rowid, %s FROM %s WHERE %s IS NOT NULL AND %s != '' AND %s NOT GLOB '????-??-??T*'`,
		col, table, col, col, col,
	)
	rows, err := s.db.QueryContext(ctx, sel)
	if err != nil {
		return err
	}
	type pending struct {
		rowID int64
		val   string
	}
	var todo []pending
	for rows.Next() {
		var rid int64
		var v string
		if err := rows.Scan(&rid, &v); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, pending{rid, v})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(todo) == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	upd := fmt.Sprintf(`UPDATE %s SET %s = ? WHERE rowid = ?`, table, col)
	for _, p := range todo {
		parsed, ok := ParseStoredTime(p.val)
		if !ok || parsed.IsZero() {
			continue
		}
		if _, err := s.db.ExecContext(ctx, upd, parsed.UTC().Format(time.RFC3339Nano), p.rowID); err != nil {
			return err
		}
	}
	return nil
}
