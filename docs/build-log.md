# Build log — OLX Sales-Intelligence CLI v0.1.0

**Date:** 2026-05-21
**Operator:** dskibickikono-lang (`dskibicki.kono@gmail.com`)
**Spec input:** `~/projects/har/olx/21www.olx.pl.har` (51 MB).
**Generator:** `cli-printing-press` v4.9.0.

## Pipeline executed

1. `printing-press browser-sniff --har …` → `docs/research/olx-spec.yaml` + `olx-traffic-analysis.json`. Two iterations: the first sniff picked `tracking.olx-st.com` as `base_url`; tightened blocklist on second pass to land on 18 endpoints over 9 resources with `base_url: https://www.olx.pl`. The spec's auth block was hand-edited to `auth.type: none` (OLX endpoints are public).
2. Hand-authored `docs/research/olx-brief.md`: endpoint inventory, OLX category map for production / warehouse / logistics, data-priority mapping (company name → user.company_name; phone → limited-phones; email → description regex; etc.).
3. `printing-press generate --spec docs/research/olx-spec.yaml --output ~/projects/printing-press/olx/src --force --validate=false` produced a Go scaffold (cmd, cobra, MCP layer, SQLite store, cache).
4. Stripped the scaffolded endpoint-mirror commands (`candidates*`, `graphql*`, `offers*`, `seo*`, `promoted_*`, plus 1100-line generic `sync.go`, `export.go`, `import.go`, `search.go`, `which.go`, and the supporting `agent_context`, `channel_workflow`, `data_source`, `deliver`, `feedback`, `profile` files). Replaced with a slim `root.go` + four workflow commands + `doctor`.
5. Wrote a hand-rolled OLX HTTP client under `internal/olx/` covering: `apigateway/graphql:ListingSearchQuery`, `apigateway/graphql:OtherSellerAdsQuery`, `GET /api/v2/offers/{id}/`, `GET /api/v1/offers/{id}/limited-phones/`, `GET /api/v1/users/{id}/`, slug→category resolver via `friendly-links/query-params/`. Politeness defaults: User-Agent identifies the project, www.olx.pl at 1 rps, jobs-api.olx.pl at 0.5 rps, exponential backoff on 5xx, respects `Retry-After` on 429.
6. Added an OLX-aware schema (`internal/store/olx.go`): `companies`, `jobs`, `phones`, `sync_runs` tables with FK + indexes. Coexists with the scaffolded generic `resources`/`sync_state`/`resources_fts` tables.
7. Rewrote `internal/mcp/tools.go` from scratch to expose exactly five tools: `olx_sync`, `olx_jobs`, `olx_companies`, `olx_export`, `olx_db_query` (read-only). All wire through programmatic entry points in `internal/cli/programmatic.go` so the CLI and MCP share the same code paths.
8. Rewrote module path from bare `olx-pp-cli` to `github.com/dskibickikono-lang/olx-pp-cli` across go.mod + all internal imports.

## GraphQL adjustments observed during smoke testing

The HAR-captured `ListingSearchQuery` requested `metadata.promoted_count` and a `... on ListingError { message }` field. The live OLX schema rejected both. Trimmed:
- removed `metadata { ... }` block (we don't use it)
- removed `message` selection on `ListingError`
- removed unused `$fetchPayAndShip` variable from the query string and the variables payload

The HAR's query had been authored against an older deploy. This is normal for browser-sniffed GraphQL — when in doubt, send less.

## Build

```
go vet ./...                                                 # clean
go build -trimpath -o ../bin/olx-pp-cli ./cmd/olx-pp-cli     # 18 MB
go build -trimpath -o ../bin/olx-pp-mcp ./cmd/olx-pp-mcp     # 16 MB
```

No CGO; modernc.org/sqlite is pure Go. Cross-compilation should work without a C toolchain.

## Verification results

| Check | Result |
|---|---|
| `go vet ./...` | clean |
| `olx-pp-cli doctor` | OK (live ping returned 8 listings for the 5-row probe of category 1447) |
| `olx-pp-cli sync --category 1754 --pages 1 --fetch-detail=false` | 1 page, 52 offers seen, 52 new jobs, 38 new companies |
| `olx-pp-cli jobs --limit 5` | 5 rows rendered, table view + JSON view both confirmed |
| `olx-pp-cli companies --min-jobs 2 --posted-since 90d --limit 10` | 6 rows; top: Adecco (5 jobs), Daria Domagała (4), Manpower / Randstad / Steam Recruitment (3 each), Supplelab (2). Plausible staffing-agency signature. |
| `olx-pp-cli export --kind companies --min-jobs 2 --posted-since 90d` | wrote 6 rows to `../data/exports/20260521-201044-companies.csv` |
| `olx-pp-mcp` stdio | `initialize` + `tools/list` round-trip returned all 5 tools with descriptions |

## Post-mortem — v0.1.0 first real run (2026-05-22)

First full production sync against live OLX surfaced two distinct
failure modes which v0.1.1 (this revision) fixes:

### 1. `sql: Scan error on column index 0, name "refreshed_at"`

Root cause: `modernc.org/sqlite` binds a `time.Time` parameter by
formatting it via Go's `time.Time.String()` output (the
`2006-01-02 15:04:05.999999999 -0700 MST` shape), then on read returns
that text as a `driver.Value` of type string. The driver does *not*
parse its own text back into `time.Time` on the Scan side, so a
`Scan(&sql.NullTime{...})` fails with the message above. The freshness
check in `Store.JobIsFresh` was the SELECT that tripped this.

Functional effect: `JobIsFresh` errored on every already-seen offer.
`upsertListing` propagated the error, `syncCategory` logged it as a
warning and skipped the entire upsert — so on each subsequent sync, the
1245 already-seen offers were silently *not* refreshed. New offers were
inserted fine because that path doesn't read the column.

Fix: store all time columns as RFC3339Nano UTC text (new
`storedTime` / `FormatStoredTime` / `parseStoredTime` helpers in
`internal/store/olx.go`). Reads scan into `sql.NullString` and parse
through a tolerant matcher that accepts the new shape *and* every
legacy shape produced by the buggy driver path. `EnsureOLXSchema` now
runs `migrateOLXTimeFormats` on every open, which is a cheap no-op
once all rows are canonical.

Subtle gotcha during the migration write: the first iteration filtered
non-canonical rows with `WHERE col NOT LIKE '%T%'`, but the legacy
"+0000 UTC" suffix contains a 'T', so any `time.Now()`-derived value
(first_seen, fetched_at, started_at, finished_at) passed the filter and
was silently *not* migrated. Replaced with `NOT GLOB '????-??-??T*'`
which anchors on the RFC3339 'T' at index 11.

Also fixed: query-side `WHERE j.posted_at >= ?` in `jobs.go` and
`companies.go` was binding the cutoff as a `time.Time` (which the
driver formats in the buggy Go-default shape), which would compare
incorrectly against the new RFC3339Nano text values. Now binds via
`store.FormatStoredTime(cutoff)`.

### 2. `GetPhones(...): 400 ... "wykryliśmy podejrzaną aktywność"`

OLX's anti-abuse layer fingerprinted the limited-phones traffic and
started blanket-rejecting the endpoint after ~30 successful pulls. The
previous code logged the rejection and kept calling for the remaining
2700-ish offers; the noise was loud and only deepened the block.

Fix: introduced `olx.HTTPError` (typed error returned by `do()`),
`olx.ErrPhonesBlocked` sentinel, `Client.PhonesBlocked()` accessor, and
a sticky `atomic.Bool` flag inside `Client`. `GetPhones` detects the
specific 400 with "podejrzan" in the body, flips the flag, and returns
`ErrPhonesBlocked` thereafter — no further requests. `syncCategory`
gates the GetPhones call on `!client.PhonesBlocked()` and logs a single
clear warning when the block first trips. The end-of-run summary now
also notes the block when relevant.

Throttle: GetPhones now uses a dedicated `phonesLimiter` defaulting to
0.2 rps (one call every 5s, vs the previous shared 1 rps). Tunable via
`--rps-phones`. In live retest this kept us under the anti-abuse
threshold long enough to harvest 10 phones in a 51-offer page without
tripping anything.

### Re-verification (2026-05-22, after v0.1.1)

| Check | Result |
|---|---|
| `go vet ./...` | clean |
| `doctor` | OK, all live endpoints + new `rps: ... phones=0.20` line |
| Migration coverage | 0 legacy-format rows remaining across jobs / companies / phones / sync_runs |
| 1-page resync `--include-phones` on category 1754 | 51 offers seen, 9 new jobs, 5 new companies, **10 phones**, **0 scan errors**, **0 anti-abuse log lines**. Total wall time 87s (consistent with 0.2 rps × 10 phones = 50s minimum) |
| `companies --json --posted-since 30d` | clean RFC3339Nano timestamps in output |
| `jobs --limit 3` table | clean RFC3339Nano POSTED column |
| MCP `tools/list` | all 5 tools surfaced |

## Limitations and follow-ups

- **`limited-phones` not exercised under live load.** The 1-page smoke test used `--fetch-detail=false` to keep it fast; phones require `--include-phones` which fetches one extra request per offer and trips OLX's rate-limited path. Worth a careful manual test before relying on phone enrichment at scale.
- **Address / NIP enrichment not implemented.** The schema reserves `companies.address` and `companies.nip` columns, but v0 only fills city/region. REGON or OpenClaw look-ups belong in a follow-up tool, not in `sync`.
- **Email mining is best-effort.** A regex over the stripped HTML description catches plain `name@domain.tld` formats. Obfuscated emails (`name [at] domain`) are missed by design.
- **No retry/checkpoint for half-finished syncs.** If a sync run dies mid-category, the next run starts from offset 0 again. The `sync_runs` table records the failure but doesn't yet expose resume semantics.
- **The scaffold's generic `resources` table is dead weight.** Kept for now because `store.Store` lives in the same package and trimming it would invite drift with the upstream printing-press machine. Safe to remove once we're sure nothing else touches it.
