# OLX.pl Sales-Intelligence CLI — Research Brief

**Input:** `~/projects/har/olx/21www.olx.pl.har` (51 MB, captured 2026-05-21).
**Goal:** Production-ready CLI + MCP server that pulls OLX.pl job
listings (production / warehouse / logistics focus) into a local SQLite
store and surfaces which companies post unusually many ads — a sales-
prospecting tool, not a job board.

## What the HAR actually reveals

OLX.pl exposes three coexisting HTTP surfaces. The CLI only needs the
first two; the third is captured here for completeness.

### 1. REST under `https://www.olx.pl/api/`

| Endpoint | Method | Notes |
|---|---|---|
| `/api/v1/offers/metadata/search/?category_id=&offset=&limit=&facets=…` | GET | Returns **facet aggregations only** (region/category counts), not offers. Misleadingly named. |
| `/api/v1/offers/metadata/search-categories/?…` | GET | Category facet tree. |
| `/api/v1/offers/metadata/breadcrumbs/?category_id=` | GET | Breadcrumb metadata per category. |
| `/api/v1/offers/metadata/filters/` | GET | Filter schema. |
| `/api/v2/offers/{id}/` | GET | **Full offer detail.** Includes `description` (HTML), `user`, `location`, `category`, `contact`, `photos`, `params`. The richest single-offer endpoint. |
| `/api/v1/offers/{id}/breadcrumbs/` | GET | Per-offer category trail. |
| `/api/v1/offers/{id}/limited-phones/` | GET | Phone numbers, **rate-limited**. Returns 200 with hidden phone if not allowed. |
| `/api/v1/users/{id}/` | GET | Seller profile: id, name, company_name (optional), photos, banner, social. |
| `/api/v1/friendly-links/query-params/<slug-path>/` | GET | Resolves OLX URL slugs (`praca,produkcja,obsluga-produkcji`) into query params (category_id etc). Useful as a slug→category resolver. |
| `/api/v1/seo/searches/?category_id=` | GET | "Popular searches" inside a category — discovers related sub-categories. |
| `/api/v1/seo/d/content/?category_id=` | GET | SEO content per category. |

All return JSON. **No authentication required** — confirmed by the HAR
(no `Authorization`, no `x-api-key`, no `Cookie: PHPSESSID=…`). The
session cookie is set after the first response but rate-limiting is
keyed primarily on IP, not session.

### 2. GraphQL at `https://www.olx.pl/apigateway/graphql` (POST)

The **actual listing surface**. Operations observed in the HAR:

| Operation | Purpose | Variables |
|---|---|---|
| `ListingSearchQuery` | **The workhorse.** Returns the offer list for a category page. Response: `data.clientCompatibleListings.data[]` (40 items per page typical). | `searchParameters: [{key,value}]` including `offset`, `limit`, `category_id`, `suggest_filters`, `sl` (search session id) |
| `OtherSellerAdsQuery` | All offers from one seller. Excellent for company analytics: "what else is this account running?" | `sellerId`, `offset`, `limit` |
| `AdRecommendationsQuery` | Recommended offers. Less useful. |
| `PageViews` | View counts. |

Each offer in `ListingSearchQuery` has the full shape — `id`, `title`,
`description` (HTML), `url`, `created_time`, `last_refresh_time`,
`valid_to_time`, `location.{city,district,region}`, `category.{id,type}`,
`contact.{name,phone(bool),chat,negotiation,courier}`, `business`,
`photos[]`, `user.{id,uuid,name,company_name,…}`, `params[]`. One round
trip gives 90% of what the analytics needs — `limited-phones` is the
only extra fetch needed per offer (and only if `contact.phone == true`).

### 3. `https://jobs-api.olx.pl/` (employer profiles)

A separate service for company / employer profile pages. Persisted-query
GraphQL with operations `CompanyProfileJobAd`, `CompanyProfileEmployerSegment`,
`CompanyProfileJobListingCarousel`, plus REST
`/api/candidates/v1/offers/{id}/applications-count`. Useful for richer
company metadata (logo, verified badge, application counts) but
**optional** — the v2 offer detail already exposes `business`,
`company_name`, and seller `uuid`.

## Category map (production / warehouse / logistics)

Confirmed from HAR (URL paths + `category_id` values):

| Slug path | `category_id` | Label |
|---|---|---|
| `praca/produkcja/obsluga-produkcji` | `1754` | Production handling |
| `praca/produkcja` (parent) | `1447` | Production (parent) |

OLX category IDs for adjacent verticals worth seeding (not in HAR — to
be confirmed via `/api/v1/friendly-links/query-params/<slug>/` on first
sync):

| Slug | Likely role |
|---|---|
| `praca/magazyn-logistyka` | Warehouse / logistics parent |
| `praca/produkcja/pakowanie` | Packing |
| `praca/produkcja/operator-maszyn` | Machine operator |
| `praca/magazyn-logistyka/operator-wozka` | Forklift operator |

Resolution strategy: keep `category_id` as the SQLite key, but the
`sync` command accepts both slugs and ids. Slugs are resolved via the
`friendly-links` endpoint on first encounter and cached.

## Data we want to extract per offer (priority order)

Listed in the priority the user requested, mapped to where each field
lives in the response payloads:

1. **company name** — `v2/offers/{id}.user.company_name` (often empty for individual posters) → fallback to `v2/offers/{id}.user.name`. The `business: true` flag identifies company posters.
2. **company address** — Not directly exposed. Best proxy: `v2/offers/{id}.location.{city,region,district}` for offer location, plus `v2/offers/{id}.map.{lat,lon,zoom}`. Real street addresses are sometimes embedded inside `description` (free text) — we capture description and let downstream tooling parse.
3. **job title** — `v2/offers/{id}.title` (also in ListingSearchQuery).
4. **phone number** — `v1/offers/{id}/limited-phones/.data.phones[]`. Only fetched when `contact.phone == true` and the offer is newly seen, to respect OLX's rate limiting.
5. **email address** — Not exposed via API. Sometimes embedded in description HTML. We store `description` verbatim and extract with a regex on a best-effort basis.
6. **short job description** — `v2/offers/{id}.description` (HTML). We store both the raw HTML and a stripped plaintext excerpt (first 500 chars).
7. Plus: OLX listing id, url, created/refresh/valid timestamps, category_id, category path (resolved), location lat/lon, user id + uuid, business flag.

## Design choices

### Generation strategy — scaffold via Printing Press, hand-write the client

The sniffed spec (`olx-spec.yaml`) is excellent **input** to scaffolding
— it gives the project structure, Cobra/Viper wiring, MCP layer, SQLite
store, cache layer, README — but the generated REST client will not
fluently handle:
- the GraphQL endpoint that drives 90% of useful traffic,
- two coexisting base URLs (`www.olx.pl` and `jobs-api.olx.pl`),
- the `metadata/search` vs `apigateway/graphql` naming gap (sniffer
  treats the misleadingly-named REST one as the listing endpoint).

So the build uses the Printing Press scaffold for everything **around**
the client, and a hand-written `internal/olx/` package for the actual
OLX HTTP calls. That package exposes four methods:

```go
type Client interface {
    ListByCategory(ctx, categoryID, offset, limit) ([]OfferSummary, error)  // apigateway GraphQL ListingSearchQuery
    GetOffer(ctx, offerID) (Offer, error)                                    // GET /api/v2/offers/{id}/
    GetPhones(ctx, offerID) ([]string, error)                                // GET /api/v1/offers/{id}/limited-phones/
    GetUser(ctx, userID) (User, error)                                       // GET /api/v1/users/{id}/
    GetSellerAds(ctx, sellerID, offset, limit) ([]OfferSummary, error)       // apigateway GraphQL OtherSellerAdsQuery
}
```

### Schema

SQLite, four tables: `jobs`, `companies`, `phones`, `sync_runs`. Each
row carries a `raw_json` column so future commands can re-parse
historical data without re-fetching. See plan for full DDL.

### Politeness

- User-Agent `olx-pp-cli/0.1 (+sales-intel; contact: dskibicki.kono@gmail.com)`.
- Default 1 req/s to `www.olx.pl`, 0.5 req/s to `jobs-api.olx.pl`.
- Disk-backed HTTP cache under `../data/cache/`.
- Respect `Retry-After`, exponential backoff on 5xx.
- Public read-only data only (the user constraint in CLAUDE.md).

### Workflow commands

```
olx-pp-cli sync     [--category 1447,1754] [--pages N] [--include-phones] [--include-employer] [--db PATH] [--rps RATE]
olx-pp-cli jobs     [--category …] [--city …] [--company …] [--posted-since DUR] [--format json|table]
olx-pp-cli companies [--min-jobs N] [--category …] [--posted-since DUR] [--format json|csv]
olx-pp-cli export   --kind jobs|companies [--out PATH] [--format csv|json] [filters…]
```

### MCP surface

Same five tools mirroring the CLI: `olx_sync`, `olx_jobs`,
`olx_companies`, `olx_export`, `olx_db_query` (read-only SELECT).

## What this brief deliberately does NOT cover

- **Posting** to OLX — out of scope (read-only constraint).
- **REGON / OpenCLAW enrichment** — listed in CLAUDE.md as a *future*
  integration; the `companies` table has a `nip` column reserved for it
  but the CLI does not fetch from those sources in v0.
- **Pipedrive sync** — the `export` command emits CSV/JSON that the
  user can pipe into Pipedrive themselves; no direct Pipedrive client.

## References

- Sniffed spec: `./olx-spec.yaml` (18 endpoints across 9 resources, base
  `https://www.olx.pl`).
- Traffic analysis: `./olx-traffic-analysis.json` (full traffic samples
  including dropped/low-sample endpoints).
- Per-endpoint samples: `./olx-spec-samples/`.
- Raw HAR: `~/projects/har/olx/21www.olx.pl.har`.
