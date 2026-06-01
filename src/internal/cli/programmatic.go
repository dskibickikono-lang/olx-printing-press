// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

// Programmatic entry points for the workflow commands, so callers
// outside the CLI (notably the MCP server) can invoke sync and export
// without Cobra plumbing.

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dskibickikono-lang/olx-pp-cli/internal/olx"
	"github.com/dskibickikono-lang/olx-pp-cli/internal/store"
)

// SyncOptions mirrors the sync command's flags.
type SyncOptions struct {
	Categories      string
	Pages           int
	PerPage         int
	IncludePhones   bool
	IncludeEmployer bool
	FetchDetail     bool
}

// SyncResult is what RunSyncProgrammatic returns and what the MCP
// tool surfaces back to its caller.
type SyncResult struct {
	RunID         int64  `json:"run_id"`
	PagesFetched  int    `json:"pages_fetched"`
	JobsSeen      int    `json:"jobs_seen"`
	JobsNew       int    `json:"jobs_new"`
	CompaniesNew  int    `json:"companies_new"`
	PhonesNew     int    `json:"phones_new"`
	Categories    string `json:"categories"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	ErrorMessage  string `json:"error,omitempty"`
}

// RunSyncProgrammatic runs sync against the given store + client. Used
// by the MCP server. Honors ctx cancellation.
func RunSyncProgrammatic(ctx context.Context, st *store.Store, client *olx.Client, opt SyncOptions) (*SyncResult, error) {
	cats, err := parseCategoryList(opt.Categories)
	if err != nil {
		return nil, err
	}
	if len(cats) == 0 {
		cats = defaultCategoryIDs
	}
	if opt.PerPage <= 0 {
		opt.PerPage = 40
	}

	startedAt := time.Now()
	runID, err := st.BeginSyncRun(ctx, joinInts(cats))
	if err != nil {
		return nil, err
	}
	stats := store.SyncRun{ID: runID}
	f := &syncFlags{
		pages:           opt.Pages,
		perPage:         opt.PerPage,
		includePhones:   opt.IncludePhones,
		includeEmployer: opt.IncludeEmployer,
		fetchDetail:     opt.FetchDetail,
	}

	dummyCmd := newSilentCmd()
	for _, cat := range cats {
		if err := syncCategory(ctx, dummyCmd, st, client, cat, f, &stats); err != nil {
			stats.Error = err.Error()
			_ = st.FinishSyncRun(ctx, stats)
			return nil, err
		}
	}
	if err := st.FinishSyncRun(ctx, stats); err != nil {
		return nil, err
	}
	return &SyncResult{
		RunID:        runID,
		PagesFetched: stats.PagesFetched,
		JobsSeen:     stats.JobsSeen,
		JobsNew:      stats.JobsNew,
		CompaniesNew: stats.CompaniesNew,
		PhonesNew:    stats.PhonesNew,
		Categories:   joinInts(cats),
		StartedAt:    startedAt.UTC().Format(time.RFC3339),
		FinishedAt:   time.Now().UTC().Format(time.RFC3339),
		ErrorMessage: stats.Error,
	}, nil
}

// ExportOptions mirrors the export command's flags.
type ExportOptions struct {
	Kind        string
	Format      string
	Out         string
	Categories  string
	City        string
	PostedSince string
	MinJobs     int
	TitleQuery  string
	Limit       int
}

// RunExportProgrammatic runs export against the given store and returns
// the written path and row count.
func RunExportProgrammatic(ctx context.Context, st *store.Store, opt ExportOptions) (string, int, error) {
	if opt.Kind != "jobs" && opt.Kind != "companies" {
		return "", 0, fmt.Errorf("kind must be 'jobs' or 'companies', got %q", opt.Kind)
	}
	format := opt.Format
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		return "", 0, fmt.Errorf("format must be 'csv' or 'json', got %q", format)
	}
	out := opt.Out
	if out == "" {
		dir := resolveExportDir("")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", 0, err
		}
		stamp := time.Now().UTC().Format("20060102-150405")
		out = filepath.Join(dir, fmt.Sprintf("%s-%s.%s", stamp, opt.Kind, format))
	}

	w, err := os.Create(out)
	if err != nil {
		return "", 0, err
	}
	defer w.Close()

	limit := opt.Limit
	if limit <= 0 {
		limit = 1000
	}
	switch opt.Kind {
	case "jobs":
		rows, err := QueryJobs(ctx, st.DB(), JobsQuery{
			Categories:  opt.Categories,
			City:        opt.City,
			PostedSince: opt.PostedSince,
			TitleQuery:  opt.TitleQuery,
			Limit:       limit,
		})
		if err != nil {
			return "", 0, err
		}
		if err := writeRows(w, format, jobsHeaders(), jobRows(rows), rows); err != nil {
			return "", 0, err
		}
		return out, len(rows), nil
	case "companies":
		min := opt.MinJobs
		if min < 1 {
			min = 1
		}
		rows, err := QueryCompanies(ctx, st.DB(), CompaniesQuery{
			MinJobs:     min,
			Categories:  opt.Categories,
			PostedSince: opt.PostedSince,
			City:        opt.City,
			Limit:       limit,
		})
		if err != nil {
			return "", 0, err
		}
		if err := writeRows(w, format, companiesHeaders(), companyRows(rows), rows); err != nil {
			return "", 0, err
		}
		return out, len(rows), nil
	}
	return out, 0, nil
}

// silentCmd satisfies progressSink with io.Discard for both streams.
// Used so syncCategory can write progress without spamming the MCP
// transport. Matches the shape *cobra.Command satisfies for the CLI
// path.
type silentCmd struct{}

func (silentCmd) ErrOrStderr() io.Writer { return io.Discard }
func (silentCmd) OutOrStdout() io.Writer { return io.Discard }

func newSilentCmd() silentCmd { return silentCmd{} }
