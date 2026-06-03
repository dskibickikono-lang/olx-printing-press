// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dskibickikono-lang/olx-pp-cli/internal/bizraport"
	"github.com/dskibickikono-lang/olx-pp-cli/internal/config"
	"github.com/dskibickikono-lang/olx-pp-cli/internal/store"
	"github.com/spf13/cobra"
)

type enrichFlags struct {
	limit         int
	ttlDays       int
	minJobs       int
	maxCandidates int
	budgetPLN     float64
	rps           float64
	dryRun        bool
}

// EnrichedRow is the per-company result projection for output.
type EnrichedRow struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	NIP    string `json:"nip,omitempty"`
	KRS    string `json:"krs,omitempty"`
	REGON  string `json:"regon,omitempty"`
	Status string `json:"status"` // enriched | cached | no-match | ambiguous | error
	Note   string `json:"note,omitempty"`
}

func newEnrichCmd(root *rootFlags) *cobra.Command {
	f := &enrichFlags{}
	cmd := &cobra.Command{
		Use:   "enrich",
		Short: "Enrich synced companies with KRS/NIP registry data from bizraport.pl",
		Long: `enrich resolves companies discovered via OLX sync to registry data
(NIP, KRS, REGON, address, legal form) using the bizraport.pl API and writes
it back onto the local companies table. Highest-volume employers are enriched
first. Responses are cached per KRS to avoid repeat billing.

Credentials come from the [bizraport] config section or the
BIZRAPORT_EMAIL / BIZRAPORT_PASSWORD environment variables.

Examples:
  olx-pp-cli enrich --limit 20 --min-jobs 3
  olx-pp-cli enrich --dry-run            # preview candidates, no API calls
  olx-pp-cli enrich --limit 50 --budget-pln 100 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnrich(cmd.Context(), cmd, root, f)
		},
	}
	cmd.Flags().IntVar(&f.limit, "limit", 25, "Max companies to enrich in this run")
	cmd.Flags().IntVar(&f.ttlDays, "ttl-days", 7, "Re-enrich companies whose data is older than this; also the per-KRS cache TTL")
	cmd.Flags().IntVar(&f.minJobs, "min-jobs", 1, "Only enrich companies with at least this many synced jobs")
	cmd.Flags().IntVar(&f.maxCandidates, "max-candidates", 3, "When a name search is ambiguous, fetch at most this many KRS profiles to disambiguate")
	cmd.Flags().Float64Var(&f.budgetPLN, "budget-pln", 0, "Abort before the run if bizraport month-to-date net cost is at or above this (0 = no check)")
	cmd.Flags().Float64Var(&f.rps, "rps-bizraport", 0, "Max requests/sec to api.bizraport.pl (0 = use config or default)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "List candidate companies without calling the API or writing anything")
	return cmd
}

func runEnrich(ctx context.Context, cmd *cobra.Command, root *rootFlags, f *enrichFlags) error {
	st, err := openStore(ctx, root)
	if err != nil {
		return err
	}
	defer st.Close()

	staleCutoff := time.Now().Add(-time.Duration(f.ttlDays) * 24 * time.Hour)
	candidates, err := st.CompaniesNeedingEnrichment(ctx, staleCutoff, f.minJobs, f.limit)
	if err != nil {
		return fmt.Errorf("select candidates: %w", err)
	}

	// Dry run: cost-free preview of who would be enriched.
	if f.dryRun {
		rows := make([]EnrichedRow, 0, len(candidates))
		for _, c := range candidates {
			rows = append(rows, EnrichedRow{ID: c.ID, Name: c.Name, NIP: c.NIP, Status: "candidate", Note: fmt.Sprintf("%d jobs", c.JobCount)})
		}
		return renderEnriched(cmd, root, rows, fmt.Sprintf("dry-run: would enrich %d companies (no API calls made)\n", len(rows)))
	}

	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	if !cfg.Bizraport.Configured() {
		return usageErr("bizraport credentials not set — add [bizraport] email/password to %s or set BIZRAPORT_EMAIL/BIZRAPORT_PASSWORD", cfg.Path)
	}
	rps := f.rps
	if rps <= 0 {
		rps = cfg.Bizraport.RPS
	}
	client := bizraport.New(bizraport.Options{
		Email:    cfg.Bizraport.Email,
		Password: cfg.Bizraport.Password,
		BaseURL:  cfg.Bizraport.BaseURL,
		PerSec:   rps,
	})

	// Optional budget guard before spending anything.
	if f.budgetPLN > 0 {
		if u, uerr := client.Usage(ctx); uerr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: could not read bizraport usage: %v\n", uerr)
		} else if u.KosztNettoPLN >= f.budgetPLN {
			return usageErr("bizraport month-to-date cost %.2f PLN is at/over budget %.2f PLN; aborting", u.KosztNettoPLN, f.budgetPLN)
		}
	}

	rows, enriched, err := runEnrichLoop(ctx, cmd, st, client, candidates, f.maxCandidates, time.Duration(f.ttlDays)*24*time.Hour)
	if err != nil {
		return err
	}
	return renderEnriched(cmd, root, rows, fmt.Sprintf("enriched %d of %d candidate companies\n", enriched, len(candidates)))
}

// EnrichOptions mirrors the enrich command's tuning, for programmatic
// callers (the MCP server).
type EnrichOptions struct {
	Limit         int
	TTLDays       int
	MinJobs       int
	MaxCandidates int
}

// RunEnrichProgrammatic selects candidates and enriches them against the
// given store + bizraport client. Used by the MCP olx_enrich tool. Returns
// the per-company results and the count actually written.
func RunEnrichProgrammatic(ctx context.Context, st *store.Store, client *bizraport.Client, opt EnrichOptions) ([]EnrichedRow, int, error) {
	if opt.Limit <= 0 {
		opt.Limit = 25
	}
	if opt.TTLDays <= 0 {
		opt.TTLDays = 7
	}
	if opt.MaxCandidates <= 0 {
		opt.MaxCandidates = 3
	}
	ttl := time.Duration(opt.TTLDays) * 24 * time.Hour
	cands, err := st.CompaniesNeedingEnrichment(ctx, time.Now().Add(-ttl), opt.MinJobs, opt.Limit)
	if err != nil {
		return nil, 0, err
	}
	return runEnrichLoop(ctx, newSilentCmd(), st, client, cands, opt.MaxCandidates, ttl)
}

// runEnrichLoop is the shared resolve+persist loop for the CLI and MCP.
func runEnrichLoop(ctx context.Context, sink progressSink, st *store.Store, client *bizraport.Client, candidates []store.EnrichCandidate, maxCandidates int, ttl time.Duration) ([]EnrichedRow, int, error) {
	rows := make([]EnrichedRow, 0, len(candidates))
	var enriched int
	for _, cand := range candidates {
		if err := ctx.Err(); err != nil {
			return rows, enriched, err
		}
		row := enrichOne(ctx, sink, st, client, cand, maxCandidates, ttl)
		rows = append(rows, row)
		if row.Status == "enriched" || row.Status == "cached" {
			enriched++
		}
	}
	return rows, enriched, nil
}

// enrichOne resolves and persists registry data for a single company.
func enrichOne(ctx context.Context, sink progressSink, st *store.Store, client *bizraport.Client, cand store.EnrichCandidate, maxCandidates int, ttl time.Duration) EnrichedRow {
	row := EnrichedRow{ID: cand.ID, Name: cand.Name, NIP: cand.NIP}

	profile, fromCache, err := resolveProfile(ctx, st, client, cand, maxCandidates, ttl)
	if err != nil {
		row.Status = "error"
		row.Note = err.Error()
		fmt.Fprintf(sink.ErrOrStderr(), "warn: enrich %s (%s): %v\n", cand.ID, cand.Name, err)
		return row
	}
	if profile == nil {
		row.Status = "no-match"
		return row
	}

	// Persist the raw profile to the per-KRS cache (skip if it came from there).
	if !fromCache && profile.KRS != "" {
		if err := st.UpsertBizraportCache(ctx, profile.KRS, profile.Raw, time.Now()); err != nil {
			fmt.Fprintf(sink.ErrOrStderr(), "warn: cache %s: %v\n", profile.KRS, err)
		}
	}

	if err := st.EnrichCompany(ctx, profileToEnrichment(cand.ID, profile)); err != nil {
		row.Status = "error"
		row.Note = err.Error()
		fmt.Fprintf(sink.ErrOrStderr(), "warn: write enrich %s: %v\n", cand.ID, err)
		return row
	}

	row.NIP = firstNonEmpty(profile.NIP, profile.Info.NIP)
	row.KRS = profile.KRS
	row.REGON = profile.Info.REGON
	if fromCache {
		row.Status = "cached"
	} else {
		row.Status = "enriched"
	}
	return row
}

// resolveProfile finds the best registry profile for a candidate, using the
// per-KRS cache when fresh. Returns (nil, false, nil) when no confident
// match exists. fromCache is true when every fetched profile came from cache.
func resolveProfile(ctx context.Context, st *store.Store, client *bizraport.Client, cand store.EnrichCandidate, maxCandidates int, ttl time.Duration) (*bizraport.CompanyProfile, bool, error) {
	// Most precise path: we already know the NIP.
	if cand.NIP != "" {
		p, err := client.GetByNIP(ctx, cand.NIP)
		if err != nil {
			return nil, false, err
		}
		return p, false, nil
	}

	krsList, _, err := client.Search(ctx, cand.Name)
	if err != nil {
		return nil, false, err
	}
	if len(krsList) == 0 {
		return nil, false, nil
	}
	if len(krsList) == 1 {
		return fetchKRSCached(ctx, st, client, krsList[0], ttl)
	}

	// Ambiguous: fetch a bounded number of candidates and pick by name match.
	want := normalizeCompanyName(cand.Name)
	limit := maxCandidates
	if limit > len(krsList) {
		limit = len(krsList)
	}
	for _, krs := range krsList[:limit] {
		p, cached, err := fetchKRSCached(ctx, st, client, krs, ttl)
		if err != nil {
			return nil, false, err
		}
		if p == nil {
			continue
		}
		if normalizeCompanyName(p.Info.Nazwa) == want {
			return p, cached, nil
		}
	}
	// No confident match among the inspected candidates.
	return nil, false, nil
}

// fetchKRSCached returns a profile for a KRS, serving a fresh cached payload
// when available and falling back to a live /api/dane call otherwise.
func fetchKRSCached(ctx context.Context, st *store.Store, client *bizraport.Client, krs string, ttl time.Duration) (*bizraport.CompanyProfile, bool, error) {
	if raw, fetchedAt, ok, err := st.GetBizraportCache(ctx, krs); err == nil && ok {
		if !fetchedAt.IsZero() && time.Since(fetchedAt) < ttl {
			if p, perr := bizraport.ParseProfile(raw); perr == nil {
				return p, true, nil
			}
		}
	}
	p, err := client.GetByKRS(ctx, krs)
	if err != nil {
		return nil, false, err
	}
	return p, false, nil
}

func profileToEnrichment(companyID string, p *bizraport.CompanyProfile) store.BizraportEnrichment {
	addr := strings.TrimSpace(strings.Join(nonEmpty(p.Info.Ulica, joinSpace(p.Info.KodPocztowy, p.Info.Miejscowosc)), ", "))
	return store.BizraportEnrichment{
		CompanyID:    companyID,
		NIP:          firstNonEmpty(p.NIP, p.Info.NIP),
		KRS:          p.KRS,
		REGON:        p.Info.REGON,
		LegalForm:    p.Info.FormaPrawna,
		ShareCapital: p.Info.KapitalZakladowy,
		Name:         p.Info.Nazwa,
		Address:      addr,
		City:         p.Info.Miejscowosc,
		Region:       p.Info.Wojewodztwo,
		Email:        p.Info.Email,
		Website:      p.Info.StronaWWW,
	}
}

func renderEnriched(cmd *cobra.Command, root *rootFlags, rows []EnrichedRow, summary string) error {
	if root.asJSON {
		return printJSON(cmd.OutOrStdout(), rows)
	}
	fmt.Fprint(cmd.ErrOrStderr(), summary)
	table := make([][]string, 0, len(rows))
	for _, r := range rows {
		table = append(table, []string{r.Status, r.ID, truncate(r.Name, 36), r.NIP, r.KRS, r.REGON})
	}
	return printTable(cmd.OutOrStdout(), []string{"STATUS", "ID", "COMPANY", "NIP", "KRS", "REGON"}, table)
}

// normalizeCompanyName lowercases and strips common Polish legal-form
// suffixes and punctuation so registry names compare equal to OLX names.
func normalizeCompanyName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacers := []string{
		"spółka z ograniczoną odpowiedzialnością", "sp. z o.o.", "sp.z o.o.", "sp zoo",
		"spółka akcyjna", "s.a.", "s. a.", "spółka komandytowa", "sp. k.", "sp.k.",
	}
	for _, r := range replacers {
		s = strings.ReplaceAll(s, r, " ")
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || isPolishLetter(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func isPolishLetter(r rune) bool {
	switch r {
	case 'ą', 'ć', 'ę', 'ł', 'ń', 'ó', 'ś', 'ż', 'ź':
		return true
	}
	return false
}

func joinSpace(parts ...string) string {
	return strings.TrimSpace(strings.Join(nonEmpty(parts...), " "))
}

func nonEmpty(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return out
}
