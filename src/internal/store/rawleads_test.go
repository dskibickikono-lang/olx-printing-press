// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openTestStore opens a fresh, temporary SQLite store for the duration of
// the test. The file is deleted when the test ends.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	ctx := context.Background()
	st, err := OpenWithContext(ctx, path)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	if err := st.EnsureOLXSchema(ctx); err != nil {
		st.Close()
		t.Fatalf("ensure OLX schema: %v", err)
	}
	t.Cleanup(func() {
		st.Close()
		os.RemoveAll(dir)
	})
	return st
}

// mustExec runs a raw SQL statement against the store's DB, fataling on error.
func mustExec(t *testing.T, st *Store, sql string, args ...any) {
	t.Helper()
	if _, err := st.db.Exec(sql, args...); err != nil {
		t.Fatalf("mustExec(%q): %v", sql, err)
	}
}

func TestRawLeadRows(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// companies requires: id, first_seen, last_seen (NOT NULL).
	// source has a DEFAULT so it's optional. Other columns are nullable.
	mustExec(t, st, `INSERT INTO companies (id, name, nip, phone, first_seen, last_seen)
		VALUES ('olx:c1', 'Stalmet sp. z o.o.', '', '+48600700800', datetime('now'), datetime('now'))`)

	// jobs requires: id, url, title, fetched_at, raw_json (NOT NULL).
	// source has a DEFAULT.
	mustExec(t, st, `INSERT INTO jobs (id, url, title, location_city, company_id, fetched_at, raw_json)
		VALUES ('olx:j1', 'https://olx.pl/x', 'Operator wtryskarki', 'Warszawa', 'olx:c1', datetime('now'), '{}')`)

	rows, err := st.RawLeadRows(ctx, 7)
	if err != nil {
		t.Fatalf("RawLeadRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.JobID != "olx:j1" || r.CompanyName != "Stalmet sp. z o.o." || r.Phone != "+48600700800" {
		t.Errorf("unexpected row: %+v", r)
	}
	if time.Since(r.FetchedAt) > time.Hour {
		t.Errorf("FetchedAt looks wrong: %v", r.FetchedAt)
	}
}

func TestRawLeadRows_SkipsOldJobs(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	mustExec(t, st, `INSERT INTO companies (id, name, nip, phone, first_seen, last_seen)
		VALUES ('olx:c2', 'OldCo', '', '', datetime('now'), datetime('now'))`)

	// Job fetched 10 days ago — outside the 7-day window.
	mustExec(t, st, `INSERT INTO jobs (id, url, title, company_id, fetched_at, raw_json)
		VALUES ('olx:j2', 'https://olx.pl/y', 'Stary etat', 'olx:c2', datetime('now', '-10 days'), '{}')`)

	rows, err := st.RawLeadRows(ctx, 7)
	if err != nil {
		t.Fatalf("RawLeadRows: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows for old job, got %d", len(rows))
	}
}
