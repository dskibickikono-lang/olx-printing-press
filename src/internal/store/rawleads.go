// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package store

import (
	"context"
	"fmt"
	"time"
)

// RawLeadRow is one offer row destined for the lead-engine raw-leads
// contract export.
type RawLeadRow struct {
	JobID       string
	Title       string
	City        string
	Region      string
	CompanyName string
	NIP         string
	Phone       string
	Email       string
	FetchedAt   time.Time
}

// RawLeadRows returns jobs fetched within the last `days` days joined with
// their company and best-known phone (per-job phone first, company phone
// as fallback).
//
// fetched_at is stored as RFC3339Nano text (see storedTime / ParseStoredTime)
// and cannot be scanned directly into time.Time by modernc.org/sqlite, so we
// scan it as a string and parse it via ParseStoredTime.
func (s *Store) RawLeadRows(ctx context.Context, days int) ([]RawLeadRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.id, j.title,
		       COALESCE(j.location_city, ''), COALESCE(j.location_region, ''),
		       COALESCE(c.name, ''), COALESCE(c.nip, ''),
		       COALESCE((SELECT p.phone FROM phones p WHERE p.job_id = j.id LIMIT 1),
		                COALESCE(c.phone, '')),
		       COALESCE(c.email, ''), j.fetched_at
		FROM jobs j
		LEFT JOIN companies c ON c.id = j.company_id
		WHERE j.fetched_at >= datetime('now', ?)
		ORDER BY j.fetched_at DESC`,
		fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil, fmt.Errorf("raw lead rows: %w", err)
	}
	defer rows.Close()
	var out []RawLeadRow
	for rows.Next() {
		var r RawLeadRow
		var fetchedAtStr string
		if err := rows.Scan(&r.JobID, &r.Title, &r.City, &r.Region,
			&r.CompanyName, &r.NIP, &r.Phone, &r.Email, &fetchedAtStr); err != nil {
			return nil, err
		}
		if t, ok := ParseStoredTime(fetchedAtStr); ok {
			r.FetchedAt = t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
