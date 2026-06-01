// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

// Package mcp exposes the olx-pp-cli workflow as MCP tools so an LLM
// agent (Claude, Goose, etc.) can sync, query, and export without
// shelling out. Each tool mirrors a CLI subcommand.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/dskibickikono-lang/olx-pp-cli/internal/cli"
	"github.com/dskibickikono-lang/olx-pp-cli/internal/olx"
	"github.com/dskibickikono-lang/olx-pp-cli/internal/store"
)

// RegisterTools registers the five OLX workflow tools on s.
func RegisterTools(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("olx_sync",
		mcp.WithDescription("Pull OLX.pl job listings into the local SQLite store. Walks each category page-by-page via apigateway/graphql ListingSearchQuery, hydrates new offers via /api/v2/offers/{id}/, optionally pulls phones and seller profiles."),
		mcp.WithString("category", mcp.Description("Comma-separated OLX category_ids. Defaults to 1447,1754 (production / production handling).")),
		mcp.WithNumber("pages", mcp.Description("Stop after N pages per category (0 = until empty).")),
		mcp.WithNumber("per_page", mcp.Description("Offers per page (OLX caps around 40; default 40).")),
		mcp.WithBoolean("include_phones", mcp.Description("Fetch limited-phones for new offers. Slower and rate-limited by OLX.")),
		mcp.WithBoolean("include_employer", mcp.Description("Resolve the seller profile via /api/v1/users/{id}/ (default true).")),
		mcp.WithBoolean("fetch_detail", mcp.Description("Pull full /api/v2/offers/{id}/ for each new offer (default true; needed for email mining from descriptions).")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	), handleSync)

	s.AddTool(mcp.NewTool("olx_jobs",
		mcp.WithDescription("Query job listings from the local SQLite store. No OLX traffic; reads only what 'olx_sync' has already pulled."),
		mcp.WithString("category", mcp.Description("Filter by category_id (comma-separated).")),
		mcp.WithString("city", mcp.Description("Filter by location_city (case-insensitive exact match).")),
		mcp.WithString("company", mcp.Description("Filter by company_id.")),
		mcp.WithString("posted_since", mcp.Description("Restrict to jobs posted within this window (e.g. 7d, 24h, 30d).")),
		mcp.WithString("title", mcp.Description("Substring match on title (case-insensitive).")),
		mcp.WithNumber("limit", mcp.Description("Max rows (default 50).")),
		mcp.WithBoolean("with_phones", mcp.Description("Include any cached phones for each job.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	), handleJobs)

	s.AddTool(mcp.NewTool("olx_companies",
		mcp.WithDescription("Surface companies posting many job listings (sales-prospecting analytics). Groups synced jobs by company and ranks employers by listing count in the window."),
		mcp.WithNumber("min_jobs", mcp.Description("Only show companies with at least this many jobs in the window (default 3).")),
		mcp.WithString("category", mcp.Description("Filter jobs by category_id (comma-separated).")),
		mcp.WithString("posted_since", mcp.Description("Counting window (default 30d).")),
		mcp.WithString("city", mcp.Description("Filter by location_city.")),
		mcp.WithNumber("limit", mcp.Description("Max rows (default 50).")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	), handleCompanies)

	s.AddTool(mcp.NewTool("olx_export",
		mcp.WithDescription("Dump a jobs or companies query to CSV or JSON. Returns the path of the written file."),
		mcp.WithString("kind", mcp.Required(), mcp.Description("'jobs' or 'companies'.")),
		mcp.WithString("format", mcp.Description("'csv' or 'json' (default csv).")),
		mcp.WithString("out", mcp.Description("Explicit output path. Defaults to <project>/data/exports/<timestamp>-<kind>.<format>.")),
		mcp.WithString("category", mcp.Description("Filter by category_id (comma-separated).")),
		mcp.WithString("city", mcp.Description("Filter by location_city.")),
		mcp.WithString("posted_since", mcp.Description("Restrict to jobs posted within this window.")),
		mcp.WithNumber("min_jobs", mcp.Description("(--kind companies) minimum jobs per company.")),
		mcp.WithString("title", mcp.Description("(--kind jobs) substring match on title.")),
		mcp.WithNumber("limit", mcp.Description("Max rows (default 1000).")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	), handleExport)

	s.AddTool(mcp.NewTool("olx_db_query",
		mcp.WithDescription("Run a read-only SELECT against the local OLX SQLite store. Refuses any statement that begins with anything other than SELECT or WITH. Use for ad-hoc analytics the other tools don't cover."),
		mcp.WithString("sql", mcp.Required(), mcp.Description("A single SELECT or WITH ... SELECT statement.")),
		mcp.WithNumber("limit", mcp.Description("Row cap applied even if the query has no LIMIT (default 500).")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	), handleDBQuery)
}

// --- handlers ------------------------------------------------------------

func handleSync(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	st, err := openStoreMCP(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer st.Close()

	client := olx.New(olx.Options{})
	stats, err := cli.RunSyncProgrammatic(ctx, st, client, cli.SyncOptions{
		Categories:      req.GetString("category", ""),
		Pages:           int(req.GetFloat("pages", 0)),
		PerPage:         int(req.GetFloat("per_page", 40)),
		IncludePhones:   req.GetBool("include_phones", false),
		IncludeEmployer: req.GetBool("include_employer", true),
		FetchDetail:     req.GetBool("fetch_detail", true),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out, _ := json.MarshalIndent(stats, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

func handleJobs(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	st, err := openStoreMCP(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer st.Close()
	rows, err := cli.QueryJobs(ctx, st.DB(), cli.JobsQuery{
		Categories:  req.GetString("category", ""),
		City:        req.GetString("city", ""),
		CompanyID:   req.GetString("company", ""),
		PostedSince: req.GetString("posted_since", ""),
		TitleQuery:  req.GetString("title", ""),
		Limit:       int(req.GetFloat("limit", 50)),
		WithPhones:  req.GetBool("with_phones", false),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out, _ := json.MarshalIndent(rows, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

func handleCompanies(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	st, err := openStoreMCP(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer st.Close()
	rows, err := cli.QueryCompanies(ctx, st.DB(), cli.CompaniesQuery{
		MinJobs:     int(req.GetFloat("min_jobs", 3)),
		Categories:  req.GetString("category", ""),
		PostedSince: req.GetString("posted_since", "30d"),
		City:        req.GetString("city", ""),
		Limit:       int(req.GetFloat("limit", 50)),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out, _ := json.MarshalIndent(rows, "", "  ")
	return mcp.NewToolResultText(string(out)), nil
}

func handleExport(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	kind := req.GetString("kind", "")
	if kind != "jobs" && kind != "companies" {
		return mcp.NewToolResultError(fmt.Sprintf("kind must be 'jobs' or 'companies', got %q", kind)), nil
	}
	st, err := openStoreMCP(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer st.Close()
	path, n, err := cli.RunExportProgrammatic(ctx, st, cli.ExportOptions{
		Kind:        kind,
		Format:      req.GetString("format", "csv"),
		Out:         req.GetString("out", ""),
		Categories:  req.GetString("category", ""),
		City:        req.GetString("city", ""),
		PostedSince: req.GetString("posted_since", ""),
		MinJobs:     int(req.GetFloat("min_jobs", 1)),
		TitleQuery:  req.GetString("title", ""),
		Limit:       int(req.GetFloat("limit", 1000)),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("wrote %d rows to %s", n, path)), nil
}

func handleDBQuery(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sqlText := req.GetString("sql", "")
	limit := int(req.GetFloat("limit", 500))
	if !isReadOnly(sqlText) {
		return mcp.NewToolResultError("olx_db_query only accepts SELECT or WITH ... SELECT statements"), nil
	}
	st, err := openStoreMCP(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer st.Close()
	rows, err := st.DB().QueryContext(ctx, sqlText)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out := make([]map[string]any, 0, 32)
	for rows.Next() {
		if len(out) >= limit {
			break
		}
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		row := map[string]any{}
		for i, c := range cols {
			row[c] = normalize(raw[i])
		}
		out = append(out, row)
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func normalize(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

func isReadOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			continue
		}
		j := i
		for j < len(s) && ((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z')) {
			j++
		}
		word := s[i:j]
		var u [16]byte
		n := len(word)
		if n > len(u) {
			n = len(u)
		}
		for k := 0; k < n; k++ {
			c := word[k]
			if c >= 'a' && c <= 'z' {
				c -= 32
			}
			u[k] = c
		}
		w := string(u[:n])
		return w == "SELECT" || w == "WITH"
	}
	return false
}

func openStoreMCP(ctx context.Context) (*store.Store, error) {
	path := os.Getenv("OLX_PP_DB")
	if path == "" {
		path = filepath.Join(walkUpForRoot(), "data", "olx_jobs.db")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	s, err := store.OpenWithContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := s.EnsureOLXSchema(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func walkUpForRoot() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for i := 0; i < 6 && dir != "/" && dir != ""; i++ {
			if fi, err := os.Stat(filepath.Join(dir, "data")); err == nil && fi.IsDir() {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "."
}
