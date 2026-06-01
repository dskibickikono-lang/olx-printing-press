// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dskibickikono-lang/olx-pp-cli/internal/olx"
	"github.com/dskibickikono-lang/olx-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

// progressSink is the logging surface that syncCategory and upsertListing
// write status into. *cobra.Command satisfies it for the CLI path; a
// stdout/stderr-piping io.Writer pair satisfies it for the MCP path.
type progressSink interface {
	OutOrStdout() io.Writer
	ErrOrStderr() io.Writer
}

// defaultCategoryIDs are the OLX job categories the user cares about by
// default — production + production handling, both observed in the HAR.
// Override with --category.
var defaultCategoryIDs = []int{1447, 1754}

// emailRE is a conservative email matcher used to mine emails out of
// HTML offer descriptions. Misses obfuscated formats on purpose.
var emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

type syncFlags struct {
	categories      string
	pages           int
	perPage         int
	includePhones   bool
	includeEmployer bool
	fetchDetail     bool
}

func newSyncCmd(root *rootFlags) *cobra.Command {
	f := &syncFlags{}
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Pull OLX job listings into the local SQLite database",
		Long: `Sync walks the OLX job category pages via the apigateway/graphql
ListingSearchQuery, hydrates each new offer through /api/v2/offers/{id}/,
optionally fetches limited-phones, and upserts jobs + companies into the
local SQLite store.

Examples:
  olx-pp-cli sync                                # default categories 1447,1754
  olx-pp-cli sync --category 1754 --pages 3      # 3 pages of "obsługa produkcji"
  olx-pp-cli sync --category 1754 --include-phones --include-employer`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cmd.Context(), cmd, root, f)
		},
	}
	cmd.Flags().StringVar(&f.categories, "category", "", "Comma-separated OLX category_ids (default: 1447,1754)")
	cmd.Flags().IntVar(&f.pages, "pages", 0, "Stop after N pages per category (0 = until empty)")
	cmd.Flags().IntVar(&f.perPage, "per-page", 40, "Offers per page (OLX caps around 40)")
	cmd.Flags().BoolVar(&f.includePhones, "include-phones", false, "Fetch limited-phones for new offers (rate-limited by OLX)")
	cmd.Flags().BoolVar(&f.includeEmployer, "include-employer", true, "Resolve seller profile via /api/v1/users/{id}/")
	cmd.Flags().BoolVar(&f.fetchDetail, "fetch-detail", true, "Pull full offer description via /api/v2/offers/{id}/ (slower; needed for emails in description)")
	return cmd
}

func runSync(ctx context.Context, cmd *cobra.Command, root *rootFlags, f *syncFlags) error {
	ctx, cancel := withSignalContext(ctx)
	defer cancel()

	cats, err := parseCategoryList(f.categories)
	if err != nil {
		return usageErr("%v", err)
	}
	if len(cats) == 0 {
		cats = defaultCategoryIDs
	}

	st, err := openStore(ctx, root)
	if err != nil {
		return err
	}
	defer st.Close()

	client := newOLXClient(root)

	runID, err := st.BeginSyncRun(ctx, joinInts(cats))
	if err != nil {
		return fmt.Errorf("begin sync run: %w", err)
	}
	stats := store.SyncRun{ID: runID}
	defer func() {
		if ferr := st.FinishSyncRun(ctx, stats); ferr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: finish sync run: %v\n", ferr)
		}
	}()

	for _, cat := range cats {
		if err := syncCategory(ctx, cmd, st, client, cat, f, &stats); err != nil {
			stats.Error = err.Error()
			return err
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"sync complete: %d pages, %d offers seen, %d new jobs, %d new companies, %d phones\n",
		stats.PagesFetched, stats.JobsSeen, stats.JobsNew, stats.CompaniesNew, stats.PhonesNew,
	)
	if f.includePhones && client.PhonesBlocked() {
		fmt.Fprintf(cmd.OutOrStdout(),
			"note: OLX's anti-abuse system blocked the limited-phones endpoint partway through; %d phones harvested before the block. Wait an hour or so and rerun with --include-phones to resume.\n",
			stats.PhonesNew,
		)
	}
	return nil
}

func syncCategory(ctx context.Context, cmd progressSink, st *store.Store, client *olx.Client, categoryID int, f *syncFlags, stats *store.SyncRun) error {
	limit := f.perPage
	if limit <= 0 {
		limit = 40
	}
	pages := 0
	for offset := 0; ; offset += limit {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "[sync] category=%d offset=%d limit=%d\n", categoryID, offset, limit)
		listings, hasMore, err := client.ListByCategory(ctx, categoryID, offset, limit)
		if err != nil {
			return fmt.Errorf("listing query category=%d offset=%d: %w", categoryID, offset, err)
		}
		stats.PagesFetched++
		pages++
		stats.JobsSeen += len(listings)

		for i := range listings {
			if err := upsertListing(ctx, cmd, st, client, &listings[i], f, stats); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warn: upsert listing %s: %v\n", listings[i].ID, err)
				continue
			}
		}

		if !hasMore {
			break
		}
		if f.pages > 0 && pages >= f.pages {
			break
		}
	}
	return nil
}

// upsertListing absorbs one offer from the listing query into jobs +
// companies. Per --include-phones / --include-employer / --fetch-detail
// it may make additional HTTP calls per offer.
func upsertListing(ctx context.Context, cmd progressSink, st *store.Store, client *olx.Client, lst *olx.OfferSummary, f *syncFlags, stats *store.SyncRun) error {
	now := time.Now().UTC()
	offerID := lst.ID.String()
	if offerID == "" {
		return fmt.Errorf("offer with empty id")
	}

	candidateRefresh := parseTime(lst.LastRefreshTime)
	fresh, err := st.JobIsFresh(ctx, offerID, candidateRefresh)
	if err != nil {
		return err
	}

	// Decide if we need the full detail.
	var detail *olx.OfferDetail
	var detailRaw json.RawMessage
	description := lst.Description
	if !fresh && f.fetchDetail {
		d, raw, derr := client.GetOffer(ctx, offerID)
		if derr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: GetOffer(%s): %v\n", offerID, derr)
		} else {
			detail = d
			detailRaw = raw
			if d.Description != "" {
				description = d.Description
			}
		}
	}

	rawJSON, err := json.Marshal(lst)
	if err != nil {
		return err
	}
	if detailRaw != nil {
		rawJSON = detailRaw
	}

	// Company upsert first so the FK on jobs.company_id resolves.
	companyID := lst.User.ID.String()
	if companyID == "" {
		companyID = lst.User.UUID
	}
	if companyID != "" {
		if isNewCompany(ctx, st, companyID) {
			stats.CompaniesNew++
		}
		c := store.Company{
			ID:         companyID,
			Name:       firstNonEmpty(lst.User.CompanyName, lst.User.Name),
			IsBusiness: lst.Business || lst.User.B2CBusinessPage,
			City:       lst.Location.City.Name,
			Region:     lst.Location.Region.Name,
			FirstSeen:  now,
			LastSeen:   now,
		}
		// Mine email from description as a best-effort signal.
		if email := emailRE.FindString(description); email != "" {
			c.Email = email
		}
		if f.includeEmployer && companyID != "" && !looksLikeUUID(companyID) {
			if u, raw, uerr := client.GetUser(ctx, companyID); uerr == nil && u != nil {
				if u.CompanyName != "" {
					c.Name = u.CompanyName
				}
				if c.Email == "" && raw != nil {
					if email := emailRE.FindString(string(raw)); email != "" {
						c.Email = email
					}
				}
			}
		}
		if err := st.UpsertCompany(ctx, c); err != nil {
			return fmt.Errorf("upsert company %s: %w", companyID, err)
		}
	}

	postedAt := parseTime(lst.CreatedTime)
	refreshedAt := candidateRefresh
	validTo := parseTime(lst.ValidToTime)
	categoryID := lst.Category.ID.Int()

	wasNew := !rowExists(ctx, st, "jobs", offerID)
	job := store.Job{
		ID:             offerID,
		URL:            lst.URL,
		Title:          lst.Title,
		Description:    truncate(stripTags(description), 4000),
		CategoryID:     categoryID,
		CategoryPath:   "", // populated below if breadcrumbs available
		LocationCity:   lst.Location.City.Name,
		LocationRegion: lst.Location.Region.Name,
		LocationLat:    lst.Map.Lat,
		LocationLon:    lst.Map.Lon,
		CompanyID:      companyID,
		PostedAt:       postedAt,
		RefreshedAt:    refreshedAt,
		ValidTo:        validTo,
		FetchedAt:      now,
		Raw:            rawJSON,
	}
	if detail != nil {
		// detail may carry richer fields, but currently we already use them above.
		_ = detail
	}
	if err := st.UpsertJob(ctx, job); err != nil {
		return fmt.Errorf("upsert job %s: %w", offerID, err)
	}
	if wasNew {
		stats.JobsNew++
	}

	// Phones. Gate on the client's sticky block flag so we don't keep
	// hammering /limited-phones/ after OLX's anti-abuse layer trips —
	// repeated 400s only deepen the block and waste request budget.
	if f.includePhones && lst.Contact.Phone && !fresh && !client.PhonesBlocked() {
		phones, _, perr := client.GetPhones(ctx, offerID)
		if perr != nil {
			if errors.Is(perr, olx.ErrPhonesBlocked) {
				// We only enter this branch on the request that *just*
				// tripped the block (the upfront PhonesBlocked() guard
				// suppresses later ones), so this logs exactly once
				// per sync run.
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warn: OLX anti-abuse system blocked limited-phones at offer %s; suppressing further phone fetches for this run\n",
					offerID,
				)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "warn: GetPhones(%s): %v\n", offerID, perr)
			}
		}
		for _, p := range phones {
			if err := st.SavePhone(ctx, offerID, p, "limited-phones"); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warn: SavePhone(%s, %s): %v\n", offerID, p, err)
				continue
			}
			stats.PhonesNew++
		}
	}
	return nil
}

func parseCategoryList(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid category id %q: %w", part, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z07:00", s); err == nil {
		return t
	}
	return time.Time{}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// looksLikeUUID is a heuristic to avoid calling /api/v1/users/{uuid}/
// when we only have an employer UUID from the jobs-api side (the v1
// users endpoint expects a numeric id).
func looksLikeUUID(s string) bool {
	return store.IsUUID(s)
}

func isNewCompany(ctx context.Context, st *store.Store, id string) bool {
	return !rowExists(ctx, st, "companies", id)
}

func rowExists(ctx context.Context, st *store.Store, table, id string) bool {
	row := st.DB().QueryRowContext(ctx, fmt.Sprintf(`SELECT 1 FROM %s WHERE id = ?`, table), id)
	var one int
	return row.Scan(&one) == nil
}

// stripTags removes HTML tags from a description in a best-effort way.
// We don't pull in golang.org/x/net/html here — descriptions are simple
// enough that a regex strip + entity decode hits the goal.
var htmlTagRE = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string {
	s = htmlTagRE.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
