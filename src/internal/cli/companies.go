// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dskibickikono-lang/olx-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

type companiesFlags struct {
	minJobs     int
	categories  string
	postedSince string
	city        string
	limit       int
}

// CompanyRow is the analytics projection: one row per company with the
// count of jobs posted in the filter window.
type CompanyRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsBusiness bool   `json:"is_business"`
	City       string `json:"city,omitempty"`
	Region     string `json:"region,omitempty"`
	Phone      string `json:"phone,omitempty"`
	Email      string `json:"email,omitempty"`
	JobsCount  int    `json:"jobs_count"`
	FirstSeen  string `json:"first_seen,omitempty"`
	LastSeen   string `json:"last_seen,omitempty"`
}

func newCompaniesCmd(root *rootFlags) *cobra.Command {
	f := &companiesFlags{}
	cmd := &cobra.Command{
		Use:     "companies",
		Aliases: []string{"accounts"},
		Short:   "Surface companies posting many job listings (sales-prospecting view)",
		Long: `companies groups synced jobs by company and ranks employers by how
many distinct listings they have in the filter window.

Examples:
  olx-pp-cli companies --min-jobs 5 --posted-since 30d
  olx-pp-cli companies --category 1447,1754 --min-jobs 3 --json
  olx-pp-cli companies --city Opole --min-jobs 2`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCompanies(cmd.Context(), cmd, root, f)
		},
	}
	cmd.Flags().IntVar(&f.minJobs, "min-jobs", 3, "Only show companies with at least this many jobs in the window")
	cmd.Flags().StringVar(&f.categories, "category", "", "Filter jobs by category_id (comma-separated)")
	cmd.Flags().StringVar(&f.postedSince, "posted-since", "30d", "Only count jobs posted within this window (e.g. 7d, 24h, 90d)")
	cmd.Flags().StringVar(&f.city, "city", "", "Filter jobs by location_city (case-insensitive)")
	cmd.Flags().IntVar(&f.limit, "limit", 50, "Max rows to return")
	return cmd
}

func runCompanies(ctx context.Context, cmd *cobra.Command, root *rootFlags, f *companiesFlags) error {
	st, err := openStoreReadOnly(ctx, root)
	if err != nil {
		return err
	}
	defer st.Close()

	rows, err := QueryCompanies(ctx, st.DB(), CompaniesQuery{
		MinJobs:     f.minJobs,
		Categories:  f.categories,
		PostedSince: f.postedSince,
		City:        f.city,
		Limit:       f.limit,
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
			fmt.Sprintf("%d", r.JobsCount),
			r.ID,
			truncate(r.Name, 40),
			r.City,
			r.Phone,
			r.Email,
		})
	}
	return printTable(cmd.OutOrStdout(), []string{"JOBS", "ID", "COMPANY", "CITY", "PHONE", "EMAIL"}, table)
}

// CompaniesQuery is the filter set for the companies analytics command.
// Shared by the CLI and the MCP server.
type CompaniesQuery struct {
	MinJobs     int
	Categories  string
	PostedSince string
	City        string
	Limit       int
}

// QueryCompanies runs the analytics query and returns ranked rows.
func QueryCompanies(ctx context.Context, db *sql.DB, q CompaniesQuery) ([]CompanyRow, error) {
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
	if q.PostedSince != "" {
		d, err := parseDuration(q.PostedSince)
		if err != nil {
			return nil, fmt.Errorf("posted-since: %w", err)
		}
		cutoff := time.Now().Add(-d).UTC()
		where = append(where, "j.posted_at >= ?")
		args = append(args, store.FormatStoredTime(cutoff))
	}

	whereJobs := ""
	if len(where) > 0 {
		whereJobs = " WHERE " + strings.Join(where, " AND ")
	}
	min := q.MinJobs
	if min < 1 {
		min = 1
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}

	// Use a CTE so the HAVING clause filters the aggregated count without
	// dragging the LEFT JOIN through every potential filter.
	sqlText := `WITH job_counts AS (
		SELECT j.company_id AS cid, COUNT(*) AS n
		FROM jobs j` + whereJobs + `
		WHERE j.company_id IS NOT NULL AND j.company_id != ''
		GROUP BY j.company_id
		HAVING n >= ?
	)
	SELECT c.id, COALESCE(c.name, ''), COALESCE(c.is_business, 0),
		COALESCE(c.city, ''), COALESCE(c.region, ''),
		COALESCE(c.phone, ''), COALESCE(c.email, ''),
		jc.n, COALESCE(c.first_seen, ''), COALESCE(c.last_seen, '')
	FROM job_counts jc
	LEFT JOIN companies c ON c.id = jc.cid
	ORDER BY jc.n DESC, c.name ASC
	LIMIT ?`
	// We need to handle WHERE-then-GROUP carefully: the WHERE clause above
	// included `company_id IS NOT NULL` which combined with whereJobs may
	// produce malformed SQL if whereJobs was empty. Rebuild cleanly.
	{
		// Always have the company_id NOT NULL/empty filter present.
		var jobWhere []string
		var jobArgs []any
		jobWhere = append(jobWhere, "j.company_id IS NOT NULL", "j.company_id != ''")
		if q.Categories != "" {
			ids, _ := parseCategoryList(q.Categories)
			placeholders := make([]string, len(ids))
			for i, id := range ids {
				placeholders[i] = "?"
				jobArgs = append(jobArgs, id)
			}
			jobWhere = append(jobWhere, "j.category_id IN ("+strings.Join(placeholders, ",")+")")
		}
		if q.City != "" {
			jobWhere = append(jobWhere, "LOWER(j.location_city) = LOWER(?)")
			jobArgs = append(jobArgs, q.City)
		}
		if q.PostedSince != "" {
			d, _ := parseDuration(q.PostedSince)
			cutoff := time.Now().Add(-d).UTC()
			jobWhere = append(jobWhere, "j.posted_at >= ?")
			jobArgs = append(jobArgs, store.FormatStoredTime(cutoff))
		}
		sqlText = `WITH job_counts AS (
			SELECT j.company_id AS cid, COUNT(*) AS n
			FROM jobs j
			WHERE ` + strings.Join(jobWhere, " AND ") + `
			GROUP BY j.company_id
			HAVING n >= ?
		)
		SELECT c.id, COALESCE(c.name, ''), COALESCE(c.is_business, 0),
			COALESCE(c.city, ''), COALESCE(c.region, ''),
			COALESCE(c.phone, ''), COALESCE(c.email, ''),
			jc.n, COALESCE(c.first_seen, ''), COALESCE(c.last_seen, '')
		FROM job_counts jc
		LEFT JOIN companies c ON c.id = jc.cid OR ('olx:' || c.id) = jc.cid OR c.id = ('olx:' || jc.cid)
		ORDER BY jc.n DESC, c.name ASC
		LIMIT ?`
		args = append(jobArgs, min, limit)
	}

	rows, err := db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CompanyRow
	for rows.Next() {
		var r CompanyRow
		var bizInt int
		if err := rows.Scan(&r.ID, &r.Name, &bizInt, &r.City, &r.Region, &r.Phone, &r.Email, &r.JobsCount, &r.FirstSeen, &r.LastSeen); err != nil {
			return nil, err
		}
		r.IsBusiness = bizInt != 0
		out = append(out, r)
	}
	return out, rows.Err()
}
