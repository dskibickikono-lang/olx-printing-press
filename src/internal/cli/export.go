// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

type exportFlags struct {
	kind        string
	format      string
	out         string
	categories  string
	city        string
	postedSince string
	minJobs     int
	titleQuery  string
	limit       int
}

func newExportCmd(root *rootFlags) *cobra.Command {
	f := &exportFlags{}
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Dump query results to CSV or JSON",
		Long: `export writes the result of a jobs or companies query to a file under
the exports directory (default: <project>/data/exports/).

Examples:
  olx-pp-cli export --kind companies --min-jobs 5 --posted-since 30d
  olx-pp-cli export --kind jobs --category 1754 --format csv --out /tmp/jobs.csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExport(cmd.Context(), cmd, root, f)
		},
	}
	cmd.Flags().StringVar(&f.kind, "kind", "jobs", "What to export: jobs | companies")
	cmd.Flags().StringVar(&f.format, "format", "csv", "Output format: csv | json")
	cmd.Flags().StringVar(&f.out, "out", "", "Output file path (default: <project>/data/exports/<timestamp>-<kind>.<format>)")
	cmd.Flags().StringVar(&f.categories, "category", "", "Filter by category_id (comma-separated)")
	cmd.Flags().StringVar(&f.city, "city", "", "Filter by location_city (case-insensitive)")
	cmd.Flags().StringVar(&f.postedSince, "posted-since", "", "Restrict to jobs posted within this window (e.g. 30d)")
	cmd.Flags().IntVar(&f.minJobs, "min-jobs", 1, "(--kind companies) minimum jobs per company")
	cmd.Flags().StringVar(&f.titleQuery, "title", "", "(--kind jobs) substring match on title")
	cmd.Flags().IntVar(&f.limit, "limit", 1000, "Max rows to export")
	return cmd
}

func runExport(ctx context.Context, cmd *cobra.Command, root *rootFlags, f *exportFlags) error {
	if f.kind != "jobs" && f.kind != "companies" {
		return usageErr("--kind must be 'jobs' or 'companies', got %q", f.kind)
	}
	if f.format != "csv" && f.format != "json" {
		return usageErr("--format must be 'csv' or 'json', got %q", f.format)
	}

	out := f.out
	if out == "" {
		dir := resolveExportDir(root.exportDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("ensure export dir: %w", err)
		}
		stamp := time.Now().UTC().Format("20060102-150405")
		out = filepath.Join(dir, fmt.Sprintf("%s-%s.%s", stamp, f.kind, f.format))
	}
	w, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("create %s: %w", out, err)
	}
	defer w.Close()

	st, err := openStore(ctx, root)
	if err != nil {
		return err
	}
	defer st.Close()

	switch f.kind {
	case "jobs":
		rows, err := QueryJobs(ctx, st.DB(), JobsQuery{
			Categories:  f.categories,
			City:        f.city,
			PostedSince: f.postedSince,
			TitleQuery:  f.titleQuery,
			Limit:       f.limit,
		})
		if err != nil {
			return err
		}
		if err := writeRows(w, f.format, jobsHeaders(), jobRows(rows), rows); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %d jobs to %s\n", len(rows), out)
	case "companies":
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
		if err := writeRows(w, f.format, companiesHeaders(), companyRows(rows), rows); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %d companies to %s\n", len(rows), out)
	}
	return nil
}

func jobsHeaders() []string {
	return []string{"id", "title", "company_id", "company_name", "city", "region", "category_id", "posted_at", "refreshed_at", "url"}
}

func jobRows(rs []JobRow) [][]string {
	out := make([][]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, []string{
			r.ID, r.Title, r.CompanyID, r.CompanyName, r.City, r.Region,
			strconv.Itoa(r.CategoryID), r.PostedAt, r.RefreshedAt, r.URL,
		})
	}
	return out
}

func companiesHeaders() []string {
	return []string{"id", "name", "is_business", "city", "region", "phone", "email", "jobs_count", "first_seen", "last_seen"}
}

func companyRows(rs []CompanyRow) [][]string {
	out := make([][]string, 0, len(rs))
	for _, r := range rs {
		biz := "0"
		if r.IsBusiness {
			biz = "1"
		}
		out = append(out, []string{
			r.ID, r.Name, biz, r.City, r.Region, r.Phone, r.Email,
			strconv.Itoa(r.JobsCount), r.FirstSeen, r.LastSeen,
		})
	}
	return out
}

func writeRows(w io.Writer, format string, headers []string, rows [][]string, jsonPayload any) error {
	switch format {
	case "csv":
		cw := csv.NewWriter(w)
		if err := cw.Write(headers); err != nil {
			return err
		}
		for _, r := range rows {
			if err := cw.Write(r); err != nil {
				return err
			}
		}
		cw.Flush()
		return cw.Error()
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(jsonPayload)
	}
	return fmt.Errorf("unknown format %q", format)
}
