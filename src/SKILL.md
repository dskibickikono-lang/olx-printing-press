---
name: pp-olx
description: "Printing Press CLI for Olx. OLX.pl job-listings reverse-engineered API (public, no auth)"
author: "dskibickikono-lang"
license: "Apache-2.0"
argument-hint: "<command> [args] | install cli|mcp"
allowed-tools: "Read Bash"
metadata:
  openclaw:
    requires:
      bins:
        - olx-pp-cli
---

# Olx — Printing Press CLI

## Prerequisites: Install the CLI

This skill drives the `olx-pp-cli` binary. **You must verify the CLI is installed before invoking any command from this skill.** If it is missing, install it first:

1. Install via the Printing Press installer:
   ```bash
   npx -y @mvanhorn/printing-press install olx --cli-only
   ```
2. Verify: `olx-pp-cli --version`
3. Ensure `$GOPATH/bin` (or `$HOME/go/bin`) is on `$PATH`.

If the `npx` install fails before this CLI has a public-library category, install Node or use the category-specific Go fallback after publish.

If `--version` reports "command not found" after install, the install step did not put the binary on `$PATH`. Do not proceed with skill commands until verification succeeds.

OLX.pl job-listings reverse-engineered API (public, no auth)

## Command Reference

**apigateway** — Operations on graphql

- `olx-pp-cli apigateway` — POST /apigateway/graphql

**candidates** — Operations on applications-count

- `olx-pp-cli candidates get-applications-count` — GET /api/candidates/v1/offers/{id}/applications-count
- `olx-pp-cli candidates options-applications-count` — OPTIONS /api/candidates/v1/offers/{id}/applications-count

**data** — Operations on cookies.json

- `olx-pp-cli data` — GET /data/olx/cookies.json

**friendly-links** — Operations on praca,produkcja

- `olx-pp-cli friendly-links` — GET /api/v1/friendly-links/query-params/praca,produkcja,obsluga-produkcji/

**graphql** — Operations on graphql

- `olx-pp-cli graphql create-graphql` — POST /graphql
- `olx-pp-cli graphql list-graphql` — GET /graphql
- `olx-pp-cli graphql options-graphql` — OPTIONS /graphql

**offers** — Operations on filters

- `olx-pp-cli offers create-offers` — POST /v1/offers
- `olx-pp-cli offers get-breadcrumbs` — GET /api/v1/offers/{id}/breadcrumbs/
- `olx-pp-cli offers get-offers` — GET /api/v2/offers/{id}/
- `olx-pp-cli offers list-breadcrumbs` — GET /api/v1/offers/metadata/breadcrumbs/
- `olx-pp-cli offers list-search` — GET /api/v1/offers/metadata/search/
- `olx-pp-cli offers list-search-categories` — GET /api/v1/offers/metadata/search-categories/

**seo** — Operations on searches

- `olx-pp-cli seo list-content` — GET /api/v1/seo/d/content/
- `olx-pp-cli seo list-searches` — GET /api/v1/seo/searches/

**users** — Operations on users

- `olx-pp-cli users <id>` — GET /api/v1/users/{id}/

**widgets** — Operations on widgets

- `olx-pp-cli widgets` — POST /api/widgets


### Finding the right command

When you know what you want to do but not which command does it, ask the CLI directly:

```bash
olx-pp-cli which "<capability in your own words>"
```

`which` resolves a natural-language capability query to the best matching command from this CLI's curated feature index. Exit code `0` means at least one match; exit code `2` means no confident match — fall back to `--help` or use a narrower query.

## Auth Setup

No authentication required.

Run `olx-pp-cli doctor` to verify setup.

## Build & Deploy

This is compiled Go — **source edits do nothing until you rebuild the binary**.

- `make build-all` (run from `src/`) writes to **`src/bin/`**.
- The binaries on `$PATH` are symlinked from **`<project>/bin/`** (root bin), e.g. `~/.local/bin/olx-pp-cli` → `<project>/bin/olx-pp-cli`.
- ⚠️ These are **two different locations**. `make build-all` alone does **not** update the root `bin/` that the symlinks point at. After building, either copy `src/bin/*` → `bin/`, or build straight into root bin:

  ```bash
  cd src
  go build -o ../bin/olx-pp-cli ./cmd/olx-pp-cli
  go build -o ../bin/olx-pp-mcp ./cmd/olx-pp-mcp
  ```

  Keep **both** locations current to avoid running stale code from whichever path is resolved.

## Known issues / gotchas

- **Stale-binary trap.** Because Go is compiled, the running CLI **and** the long-lived MCP server keep executing whatever binary was last built — a fresh `git pull`/commit changes nothing until you rebuild (see Build & Deploy). Worse: the MCP server is a persistent process, so even after you rebuild `bin/olx-pp-mcp` it **keeps serving the old code until the MCP server (Claude session) is restarted**. After any source fix: rebuild → for MCP, restart the server; for quick verification prefer the freshly-built **CLI**, which picks up new code immediately. Sanity check: compare binary mtime against the fix commit time (`git show -s --format=%ci <sha>` vs `stat -c %y bin/olx-pp-*`).
- **Blank-stamp bug (silent enrich corruption).** `enrich` always stamps `enriched_source='bizraport'` + `enriched_at=now`, but writes NIP/KRS/REGON with `COALESCE(NULLIF(?,''), col)`. So a run on **stale/broken parsing code** (e.g. before the `ParseProfile` API-format fix) reports status `"enriched"` while leaving NIP/KRS/REGON **blank** — the row then *looks* enriched and is skipped by future default-TTL runs. Detect with:

  ```bash
  sqlite3 data/olx_jobs.db \
    "SELECT id,name FROM companies WHERE enriched_source='bizraport' AND (nip IS NULL OR nip='');"
  ```

  A nonzero count after a run almost always means you ran a stale binary — rebuild, then force-retry (see Enrich).

## Enrich (registry data via bizraport.pl)

`olx-pp-cli enrich` resolves OLX companies to KRS/NIP/REGON, highest-volume employers first. **Bills per returned row** — gate with `--limit`, preview with `--dry-run`.

- **Force-retry already-stamped rows:** `--ttl-days 0`. The candidate filter is `enriched_at IS NULL OR enriched_at < (now - ttl_days)`, so `0` makes today's stamped rows re-qualify. Note `--ttl-days` also controls the per-KRS cache TTL, so `0` bypasses the cache and **re-bills even already-good rows** — scope with `--limit`/`--min-jobs`.
- **Clean up blank-stamp residue without re-billing the good rows:** NULL out only the broken stamps, then run a normal-TTL enrich. The default TTL keeps correctly-enriched rows "fresh" (skipped, no re-bill); only the freed rows + never-enriched + previous no-matches get processed:

  ```bash
  sqlite3 data/olx_jobs.db \
    "UPDATE companies SET enriched_at=NULL, enriched_source=NULL \
     WHERE enriched_source='bizraport' AND (nip IS NULL OR nip='');"
  olx-pp-cli enrich --min-jobs 5 --ttl-days 7 --limit 50 --json
  ```

- **Expected no-matches** (don't keep retrying): brand-only OLX display names that don't string-match a Polish legal entity (Manpower Poland, Saint-Gobain, Nestle, OTTO Work Force, EWL S.A.), and informal/private seller names (first names, "Dział Rekrutacji"). These need a manually-seeded NIP to resolve via the `GetByNIP` path. Raising `--max-candidates` only helps when the name search already returns hits.

## Phones (OLX limited-phones)

- **OLX anti-abuse blocks the limited-phones endpoint after ~12 calls**, then a sticky block for the rest of the run plus a **24h cooldown** (`--phones-cooldown`, default 24). A full walk typically harvests only ~a dozen phones before the block. Phone rps is throttled hard (`--rps-phones`, default 0.2).
- `sync --include-phones` fetches phones/details **only for new or bumped offers** (gated on `!fresh`, i.e. stored `refreshed_at` older than OLX's `last_refresh_time`). It does **not** backfill phones for the whole existing corpus in one pass.
- **To grow phone coverage:** run `sync --include-phones` repeatedly across days. Recruitment agencies re-bump listings constantly, so each run re-hydrates the freshly-bumped subset and grabs another small batch of phones before the block. Don't expect to backfill thousands of old jobs at once.

## Agent Mode

Add `--agent` to any command. Expands to: `--json --compact --no-input --no-color --yes`.

- **Pipeable** — JSON on stdout, errors on stderr
- **Filterable** — `--select` keeps a subset of fields. Dotted paths descend into nested structures; arrays traverse element-wise. Critical for keeping context small on verbose APIs:

  ```bash
  olx-pp-cli apigateway --data example-value --query example-value --agent --select id,name,status
  ```
- **Previewable** — `--dry-run` shows the request without sending
- **Offline-friendly** — sync/search commands can use the local SQLite store when available
- **Non-interactive** — never prompts, every input is a flag
- **Explicit retries** — use `--idempotent` only when an already-existing create should count as success

### Response envelope

Commands that read from the local store or the API wrap output in a provenance envelope:

```json
{
  "meta": {"source": "live" | "local", "synced_at": "...", "reason": "..."},
  "results": <data>
}
```

Parse `.results` for data and `.meta.source` to know whether it's live or local. A human-readable `N results (live)` summary is printed to stderr only when stdout is a terminal AND no machine-format flag (`--json`, `--csv`, `--compact`, `--quiet`, `--plain`, `--select`) is set — piped/agent consumers and explicit-format runs get pure JSON on stdout.

## Agent Feedback

When you (or the agent) notice something off about this CLI, record it:

```
olx-pp-cli feedback "the --since flag is inclusive but docs say exclusive"
olx-pp-cli feedback --stdin < notes.txt
olx-pp-cli feedback list --json --limit 10
```

Entries are stored locally at `~/.olx-pp-cli/feedback.jsonl`. They are never POSTed unless `OLX_FEEDBACK_ENDPOINT` is set AND either `--send` is passed or `OLX_FEEDBACK_AUTO_SEND=true`. Default behavior is local-only.

Write what *surprised* you, not a bug report. Short, specific, one line: that is the part that compounds.

## Output Delivery

Every command accepts `--deliver <sink>`. The output goes to the named sink in addition to (or instead of) stdout, so agents can route command results without hand-piping. Three sinks are supported:

| Sink | Effect |
|------|--------|
| `stdout` | Default; write to stdout only |
| `file:<path>` | Atomically write output to `<path>` (tmp + rename) |
| `webhook:<url>` | POST the output body to the URL (`application/json` or `application/x-ndjson` when `--compact`) |

Unknown schemes are refused with a structured error naming the supported set. Webhook failures return non-zero and log the URL + HTTP status on stderr.

## Named Profiles

A profile is a saved set of flag values, reused across invocations. Use it when a scheduled agent calls the same command every run with the same configuration - HeyGen's "Beacon" pattern.

```
olx-pp-cli profile save briefing --json
olx-pp-cli --profile briefing apigateway --data example-value --query example-value
olx-pp-cli profile list --json
olx-pp-cli profile show briefing
olx-pp-cli profile delete briefing --yes
```

Explicit flags always win over profile values; profile values win over defaults. `agent-context` lists all available profiles under `available_profiles` so introspecting agents discover them at runtime.

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 2 | Usage error (wrong arguments) |
| 3 | Resource not found |
| 5 | API error (upstream issue) |
| 7 | Rate limited (wait and retry) |
| 10 | Config error |

## Argument Parsing

Parse `$ARGUMENTS`:

1. **Empty, `help`, or `--help`** → show `olx-pp-cli --help` output
2. **Starts with `install`** → ends with `mcp` → MCP installation; otherwise → see Prerequisites above
3. **Anything else** → Direct Use (execute as CLI command with `--agent`)

## MCP Server Installation

Install the MCP binary from this CLI's published public-library entry or pre-built release, then register it:

```bash
claude mcp add olx-pp-mcp -- olx-pp-mcp
```

Verify: `claude mcp list`

## Direct Use

1. Check if installed: `which olx-pp-cli`
   If not found, offer to install (see Prerequisites at the top of this skill).
2. Match the user query to the best command from the Unique Capabilities and Command Reference above.
3. Execute with the `--agent` flag:
   ```bash
   olx-pp-cli <command> [subcommand] [args] --agent
   ```
4. If ambiguous, drill into subcommand help: `olx-pp-cli <command> --help`.
