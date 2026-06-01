// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dskibickikono-lang/olx-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

type jobsFlags struct {
	categories   string
	city         string
	companyID    string
	postedSince  string
	titleQuery   string
	limit        int
	withPhones   bool
}

// JobRow is the read-side projection used by both the CLI table renderer
// and the MCP server. Times are RFC3339 strings so JSON output is
// agent-friendly.
type JobRow struct {
	ID           string   `json:"id"`
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	CategoryID   int      `json:"category_id"`
	CompanyID    string   `json:"company_id"`
	CompanyName  string   `json:"company_name,omitempty"`
	City         string   `json:"city,omitempty"`
	Region       string   `json:"region,omitempty"`
	PostedAt     string   `json:"posted_at,omitempty"`
	RefreshedAt  string   `json:"refreshed_at,omitempty"`
	Phones       []string `json:"phones,omitempty"`
}

func newJobsCmd(root *rootFlags) *cobra.Command {
	f := &jobsFlags{}
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "Query job listings from the local store (no network)",
		Long: `jobs queries the local SQLite store of synced offers. No OLX traffic.

Examples:
  olx-pp-cli jobs --limit 20
  olx-pp-cli jobs --category 1754 --city Opole
  olx-pp-cli jobs --company 1263998802 --json
  olx-pp-cli jobs --title pakowanie --posted-since 7d`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJobs(cmd.Context(), cmd, root, f)
		},
	}
	cmd.Flags().StringVar(&f.categories, "category", "", "Filter by category_id (comma-separated)")
	cmd.Flags().StringVar(&f.city, "city", "", "Filter by location_city (exact, case-insensitive)")
	cmd.Flags().StringVar(&f.companyID, "company", "", "Filter by company_id")
	cmd.Flags().StringVar(&f.postedSince, "posted-since", "", "Only jobs posted within this duration (e.g. 7d, 24h)")
	cmd.Flags().StringVar(&f.titleQuery, "title", "", "Substring match on title (case-insensitive)")
	cmd.Flags().IntVar(&f.limit, "limit", 50, "Max rows to return")
	cmd.Flags().BoolVar(&f.withPhones, "with-phones", false, "Include any phones we've cached for each job")
	return cmd
}

func runJobs(ctx context.Context, cmd *cobra.Command, root *rootFlags, f *jobsFlags) error {
	st, err := openStore(ctx, root)
	if err != nil {
		return err
	}
	defer st.Close()

	rows, err := QueryJobs(ctx, st.DB(), JobsQuery{
		Categories:  f.categories,
		City:        f.city,
		CompanyID:   f.companyID,
		PostedSince: f.postedSince,
		TitleQuery:  f.titleQuery,
		Limit:       f.limit,
		WithPhones:  f.withPhones,
	})
	if err != nil {
		return err
	}

	if root.asJSON {
		return printJSON(cmd.OutOrStdout(), rows)
	}
	table := make([][]string, 0, len(rows))
	for _, r := range rows {
		table = append(table, []string{
			r.ID,
			truncate(r.Title, 60),
			r.CompanyName,
			r.City,
			r.PostedAt,
		})
	}
	return printTable(cmd.OutOrStdout(), []string{"ID", "TITLE", "COMPANY", "CITY", "POSTED"}, table)
}

// JobsQuery is the filter set used by QueryJobs. Shared by the CLI and
// the MCP server.
type JobsQuery struct {
	Categories  string
	City        string
	CompanyID   string
	PostedSince string // e.g. "7d", "24h"
	TitleQuery  string
	Limit       int
	WithPhones  bool
}

// QueryJobs runs the read-side jobs query against db and returns rows.
func QueryJobs(ctx context.Context, db *sql.DB, q JobsQuery) ([]JobRow, error) {
	var where []string
	var args []any

	if q.Categories != "" {
		ids, err := parseCategoryList(q.Categories)
		if err != nil {
			return nil, fmt.Errorf("category: %w", err)
		}
		placeholders := make([]string, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args = append(args, id)
		}
		where = append(where, "j.category_id IN ("+strings.Join(placeholders, ",")+")")
	}
	if q.City != "" {
		where = append(where, "LOWER(j.location_city) = LOWER(?)")
		args = append(args, q.City)
	}
	if q.CompanyID != "" {
		where = append(where, "j.company_id = ?")
		args = append(args, q.CompanyID)
	}
	if q.TitleQuery != "" {
		where = append(where, "LOWER(j.title) LIKE LOWER(?)")
		args = append(args, "%"+q.TitleQuery+"%")
	}
	if q.PostedSince != "" {
		d, err := parseDuration(q.PostedSince)
		if err != nil {
			return nil, fmt.Errorf("posted-since: %w", err)
		}
		cutoff := time.Now().Add(-d).UTC()
		where = append(where, "j.posted_at >= ?")
		args = append(args, store.FormatStoredTime(cutoff))
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}

	sqlText := `SELECT j.id, j.url, j.title, j.category_id, j.company_id,
		COALESCE(c.name, ''), COALESCE(j.location_city, ''), COALESCE(j.location_region, ''),
		COALESCE(j.posted_at, ''), COALESCE(j.refreshed_at, '')
		FROM jobs j LEFT JOIN companies c ON c.id = j.company_id`
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " ORDER BY j.posted_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRow
	for rows.Next() {
		var r JobRow
		var catID sql.NullInt64
		if err := rows.Scan(&r.ID, &r.URL, &r.Title, &catID, &r.CompanyID,
			&r.CompanyName, &r.City, &r.Region, &r.PostedAt, &r.RefreshedAt); err != nil {
			return nil, err
		}
		if catID.Valid {
			r.CategoryID = int(catID.Int64)
		}
		if q.WithPhones {
			phones, _ := loadPhones(ctx, db, r.ID)
			r.Phones = phones
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadPhones(ctx context.Context, db *sql.DB, jobID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT phone FROM phones WHERE job_id = ?`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// JobsTotal returns the total number of jobs matching the same query
// (without the LIMIT). Useful for pagination.
func JobsTotal(ctx context.Context, db *sql.DB, q JobsQuery) (int, error) {
	q.Limit = 0
	var where []string
	var args []any
	if q.Categories != "" {
		ids, err := parseCategoryList(q.Categories)
		if err != nil {
			return 0, err
		}
		placeholders := make([]string, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args = append(args, id)
		}
		where = append(where, "j.category_id IN ("+strings.Join(placeholders, ",")+")")
	}
	if q.City != "" {
		where = append(where, "LOWER(j.location_city) = LOWER(?)")
		args = append(args, q.City)
	}
	if q.CompanyID != "" {
		where = append(where, "j.company_id = ?")
		args = append(args, q.CompanyID)
	}
	if q.TitleQuery != "" {
		where = append(where, "LOWER(j.title) LIKE LOWER(?)")
		args = append(args, "%"+q.TitleQuery+"%")
	}
	if q.PostedSince != "" {
		d, err := parseDuration(q.PostedSince)
		if err != nil {
			return 0, err
		}
		cutoff := time.Now().Add(-d).UTC()
		where = append(where, "j.posted_at >= ?")
		args = append(args, store.FormatStoredTime(cutoff))
	}
	sqlText := "SELECT COUNT(*) FROM jobs j"
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	var n int
	if err := db.QueryRowContext(ctx, sqlText, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// formatPostedSinceForHelp keeps the help text honest about defaults.
func formatPostedSinceForHelp(d time.Duration) string { return strconv.FormatFloat(d.Hours()/24, 'f', 0, 64) + "d" }
