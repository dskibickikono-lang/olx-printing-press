// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

// OLX-specific store helpers. Lives alongside the generic resources/FTS
// store the Printing Press scaffold provides; the workflow commands
// (sync, jobs, companies, export) use this typed surface instead of the
// generic JSON-blob table so analytics queries hit indexed columns.
//
// Schema is created idempotently by EnsureOLXSchema, invoked once at
// store open. Most OLX schema additions are backward-compatible and do
// not move StoreSchemaVersion. The exception is the "olx:" id-namespace
// rewrite (migratePrefixLegacyIDs): it changes row identity, so v3 bumps
// StoreSchemaVersion to keep pre-namespace binaries from opening — and
// re-bare-id-ing — a migrated database.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	if err := s.migrateAddBizraportColumns(ctx); err != nil {
		return err
	}
	if err := s.migratePrefixLegacyIDs(ctx); err != nil {
		return err
	}
	return s.migrateOLXTimeFormats(ctx)
}

// migratePrefixLegacyIDs one-time-rewrites rows written by builds that
// predate the "olx:" id namespace (bare numeric/uuid ids) so they share
// the namespace new syncs write. Without this, a re-synced offer would
// INSERT a second "olx:<id>" row beside the legacy "<id>" row (the
// ON CONFLICT key never matches), duplicating jobs and splitting per-
// company counts across two ids. Idempotent: the NOT LIKE 'olx:%' guards
// make it a cheap no-op once every row carries the prefix.
//
// All rewrites run in one transaction with foreign-key checks deferred to
// COMMIT, so the transient state where companies.id is prefixed but
// jobs.company_id is not does not trip the jobs→companies FK.
func (s *Store) migratePrefixLegacyIDs(ctx context.Context) error {
	// Fast precheck: skip the whole transaction once all data is migrated.
	var hasLegacy int
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM jobs WHERE id NOT LIKE 'olx:%'
		 UNION ALL SELECT 1 FROM companies WHERE id NOT LIKE 'olx:%')`,
	).Scan(&hasLegacy); err != nil {
		return fmt.Errorf("precheck legacy ids: %w", err)
	}
	if hasLegacy == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin id-prefix migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmts := []string{
		`PRAGMA defer_foreign_keys = ON`,
		`UPDATE companies SET id = 'olx:' || id WHERE id NOT LIKE 'olx:%'`,
		`UPDATE jobs SET id = 'olx:' || id WHERE id NOT LIKE 'olx:%'`,
		`UPDATE jobs SET company_id = 'olx:' || company_id
			WHERE company_id IS NOT NULL AND company_id <> '' AND company_id NOT LIKE 'olx:%'`,
		`UPDATE phones SET job_id = 'olx:' || job_id WHERE job_id NOT LIKE 'olx:%'`,
		// Backfill the canonical OLX user id (the bare numeric id is now the
		// part after the "olx:" prefix) for rows migrated from the old schema.
		`UPDATE companies SET olx_user_id = substr(id, 5)
			WHERE (olx_user_id IS NULL OR olx_user_id = '') AND id LIKE 'olx:%'`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("id-prefix migration (%.40s): %w", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit id-prefix migration: %w", err)
	}
	return nil
}

// migrateAddBizraportColumns adds the registry-enrichment columns filled
// by the `enrich` command (bizraport.pl) and the per-KRS response cache.
func (s *Store) migrateAddBizraportColumns(ctx context.Context) error {
	stmts := []string{
		`ALTER TABLE companies ADD COLUMN krs TEXT`,
		`ALTER TABLE companies ADD COLUMN regon TEXT`,
		`ALTER TABLE companies ADD COLUMN legal_form TEXT`,
		`ALTER TABLE companies ADD COLUMN share_capital TEXT`,
		`ALTER TABLE companies ADD COLUMN enriched_source TEXT`,
		`ALTER TABLE companies ADD COLUMN enriched_at DATETIME`,
		`CREATE INDEX IF NOT EXISTS companies_nip_idx ON companies(nip)`,
		`CREATE INDEX IF NOT EXISTS companies_krs_idx ON companies(krs)`,
		`CREATE TABLE IF NOT EXISTS bizraport_cache (
			krs        TEXT PRIMARY KEY,
			raw_json   TEXT NOT NULL,
			fetched_at DATETIME NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		// Ignore errors like "duplicate column name" (idempotent re-run).
		_, _ = s.db.ExecContext(ctx, stmt)
	}
	return nil
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
		// Index created here (not in createOLXTables) so it runs AFTER the
		// column is added — on a pre-existing DB the column does not exist
		// when createOLXTables runs, and CREATE INDEX on it would fail.
		`CREATE INDEX IF NOT EXISTS companies_olx_user_id_idx ON companies(olx_user_id)`,
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

// SaveOfferAtom saves a company, a job, and its phones in a single
// transaction so a row is never left half-written. writeMu serializes all
// Go-side writers, so the default DEFERRED transaction is sufficient here;
// the DSN's _busy_timeout covers any contention at first write.
func (s *Store) SaveOfferAtom(ctx context.Context, c Company, j Job, phones []string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback is a no-op once Commit succeeds; safe to always defer.
	defer func() { _ = tx.Rollback() }()

	if c.ID != "" {
		if err := s.upsertCompanyTx(ctx, tx, c); err != nil {
			return fmt.Errorf("upsert company %s: %w", c.ID, err)
		}
	}
	if err := s.upsertJobTx(ctx, tx, j); err != nil {
		return fmt.Errorf("upsert job %s: %w", j.ID, err)
	}
	for _, p := range phones {
		if err := s.savePhoneTx(ctx, tx, j.ID, p, "limited-phones"); err != nil {
			return fmt.Errorf("save phone %s: %w", p, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit offer %s: %w", j.ID, err)
	}
	return nil
}

// --- bizraport.pl enrichment (registry data) ---

// BizraportEnrichment carries registry fields resolved for one company.
// Empty fields are left untouched on the stored row (COALESCE/NULLIF), so
// re-enrichment never clobbers data already present.
type BizraportEnrichment struct {
	CompanyID    string // existing companies.id, e.g. "olx:123"
	NIP          string
	KRS          string
	REGON        string
	LegalForm    string
	ShareCapital string
	Name         string
	Address      string
	City         string
	Region       string
	Email        string
	Website      string
}

// EnrichCompany fills the registry columns on an existing company row and
// stamps enriched_source/enriched_at. It never creates a new row — the
// company must already exist (discovered via OLX sync).
func (s *Store) EnrichCompany(ctx context.Context, e BizraportEnrichment) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// Registry-authoritative columns (NIP/KRS/REGON/legal_form/share_capital)
	// take the new value when present. OLX-sourced columns (name/address/
	// city/region/email/website) are only filled when currently empty, so
	// enrichment never clobbers data the sync collected.
	_, err := s.db.ExecContext(ctx,
		`UPDATE companies SET
			nip           = COALESCE(NULLIF(?, ''), nip),
			krs           = COALESCE(NULLIF(?, ''), krs),
			regon         = COALESCE(NULLIF(?, ''), regon),
			legal_form    = COALESCE(NULLIF(?, ''), legal_form),
			share_capital = COALESCE(NULLIF(?, ''), share_capital),
			name          = COALESCE(NULLIF(name, ''), ?),
			address       = COALESCE(NULLIF(address, ''), ?),
			city          = COALESCE(NULLIF(city, ''), ?),
			region        = COALESCE(NULLIF(region, ''), ?),
			email         = COALESCE(NULLIF(email, ''), ?),
			website       = COALESCE(NULLIF(website, ''), ?),
			enriched_source = 'bizraport',
			enriched_at   = ?
		 WHERE id = ?`,
		e.NIP, e.KRS, e.REGON, e.LegalForm, e.ShareCapital,
		e.Name, e.Address, e.City, e.Region, e.Email, e.Website,
		storedTime(time.Now()), e.CompanyID,
	)
	return err
}

// EnrichCandidate is a company that may need registry enrichment, with its
// current OLX job count for prioritization.
type EnrichCandidate struct {
	ID       string
	Name     string
	NIP      string
	JobCount int
}

// CompaniesNeedingEnrichment returns companies never enriched (or enriched
// before staleCutoff), ordered by job count descending — the "posts many
// listings" prospects come first. minJobs filters low-signal sellers.
func (s *Store) CompaniesNeedingEnrichment(ctx context.Context, staleCutoff time.Time, minJobs, limit int) ([]EnrichCandidate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, COALESCE(c.name, ''), COALESCE(c.nip, ''), COUNT(j.id) AS n
		 FROM companies c
		 LEFT JOIN jobs j ON j.company_id = c.id
		 WHERE c.enriched_at IS NULL OR c.enriched_at < ?
		 GROUP BY c.id
		 HAVING n >= ?
		 ORDER BY n DESC, c.name ASC
		 LIMIT ?`,
		storedTime(staleCutoff), minJobs, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnrichCandidate
	for rows.Next() {
		var e EnrichCandidate
		if err := rows.Scan(&e.ID, &e.Name, &e.NIP, &e.JobCount); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetBizraportCache returns the cached /api/dane payload for a KRS, its
// fetch time, and whether a usable row was found.
func (s *Store) GetBizraportCache(ctx context.Context, krs string) (json.RawMessage, time.Time, bool, error) {
	var raw string
	var fetchedAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT raw_json, fetched_at FROM bizraport_cache WHERE krs = ?`, krs,
	).Scan(&raw, &fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	var t time.Time
	if fetchedAt.Valid {
		if parsed, ok := ParseStoredTime(fetchedAt.String); ok {
			t = parsed
		}
	}
	return json.RawMessage(raw), t, true, nil
}

// UpsertBizraportCache stores (or refreshes) the raw /api/dane payload for a KRS.
func (s *Store) UpsertBizraportCache(ctx context.Context, krs string, raw []byte, fetchedAt time.Time) error {
	if krs == "" {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO bizraport_cache (krs, raw_json, fetched_at) VALUES (?,?,?)
		 ON CONFLICT(krs) DO UPDATE SET raw_json = excluded.raw_json, fetched_at = excluded.fetched_at`,
		krs, string(raw), storedTime(fetchedAt),
	)
	return err
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
