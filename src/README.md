# Olx CLI

OLX.pl job-listings reverse-engineered API (public, no auth)

Learn more at [Olx](https://www.olx.pl).

## Install

The recommended path installs both the `olx-pp-cli` binary and the `pp-olx` agent skill (Claude Code, Codex, Cursor, Gemini CLI, GitHub Copilot, and other agents supported by the upstream [`skills`](https://github.com/vercel-labs/skills) CLI) in one shot:

```bash
npx -y @mvanhorn/printing-press install olx
```

For CLI only (no skill):

```bash
npx -y @mvanhorn/printing-press install olx --cli-only
```

For skill only — installs the skill into the same agents as the default command above, but skips the CLI binary (use this to update or reinstall just the skill):

```bash
npx -y @mvanhorn/printing-press install olx --skill-only
```

To constrain the skill install to one or more specific agents (repeatable — agent names match the [`skills`](https://github.com/vercel-labs/skills) CLI):

```bash
npx -y @mvanhorn/printing-press install olx --agent claude-code
npx -y @mvanhorn/printing-press install olx --agent claude-code --agent codex
```

### Without Node

The generated install path is category-agnostic until this CLI is published. If `npx` is not available before publish, install Node or use the category-specific Go fallback from the public-library entry after publish.

### Pre-built binary

Download a pre-built binary for your platform from the [latest release](https://github.com/mvanhorn/printing-press-library/releases/tag/olx-current). On macOS, clear the Gatekeeper quarantine: `xattr -d com.apple.quarantine <binary>`. On Unix, mark it executable: `chmod +x <binary>`.

<!-- pp-hermes-install-anchor -->
## Install for Hermes

From the Hermes CLI:

```bash
hermes skills install mvanhorn/printing-press-library/cli-skills/pp-olx --force
```

Inside a Hermes chat session:

```bash
/skills install mvanhorn/printing-press-library/cli-skills/pp-olx --force
```

## Install for OpenClaw

Tell your OpenClaw agent (copy this):

```
Install the pp-olx skill from https://github.com/mvanhorn/printing-press-library/tree/main/cli-skills/pp-olx. The skill defines how its required CLI can be installed.
```

## Use with Claude Desktop

This CLI ships an [MCPB](https://github.com/modelcontextprotocol/mcpb) bundle — Claude Desktop's standard format for one-click MCP extension installs (no JSON config required).

To install:

1. Download the `.mcpb` for your platform from the [latest release](https://github.com/mvanhorn/printing-press-library/releases/tag/olx-current).
2. Double-click the `.mcpb` file. Claude Desktop opens and walks you through the install.

Requires Claude Desktop 1.0.0 or later. Pre-built bundles ship for macOS Apple Silicon (`darwin-arm64`) and Windows (`amd64`, `arm64`); for other platforms, use the manual config below.

<details>
<summary>Manual JSON config (advanced)</summary>

If you can't use the MCPB bundle (older Claude Desktop, unsupported platform), install the MCP binary and configure it manually.


Install the MCP binary from this CLI's published public-library entry or pre-built release.

Add to your Claude Desktop config (`~/Library/Application Support/Claude/claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "olx": {
      "command": "olx-pp-mcp"
    }
  }
}
```

To run the MCP server in read-only mode (so agents cannot trigger live network calls to OLX via the `olx_sync` tool), add `--read-only` to the command arguments, or set the `READ_ONLY_MODE=true` environment variable.

</details>

## Quick Start

### 1. Install

See [Install](#install) above.

### 2. Verify Setup

```bash
olx-pp-cli doctor
```

This checks your configuration.

### 3. Try Your First Command

```bash
olx-pp-cli sync --category 1447,1754
```

## Usage

Run `olx-pp-cli --help` for the full command reference and flag list.

## Commands

### sync

Pull OLX.pl job listings into the local SQLite store.

- **`olx-pp-cli sync`** - Syncs listings

### jobs

Query job listings from the local store (no network)

- **`olx-pp-cli jobs`** - Queries jobs

### companies

Surface companies posting many job listings (sales-prospecting view)

- **`olx-pp-cli companies`** - Queries companies

### export

Dump query results to CSV or JSON

- **`olx-pp-cli export`** - Exports data

### doctor

Check OLX connectivity, store health, and config paths

- **`olx-pp-cli doctor`** - Checks health

## Contract for downstream consumers

The local SQLite database (`olx_jobs.db`) provides the following guarantees for downstream consumers (e.g., Python pipelines):
- **Stabilny kontrakt:** Tabele `companies`, `jobs`, `phones` i `sync_runs` to stabilne struktury.
- **Wartości Guaranteed vs Best-effort:** Część kolumn zawsze zawiera wartość (np. id, tytuł), a inne (np. szczegóły, email) są best-effort, wypełniane jeśli dostępne.
- **Null vs pusty string:** Brak rekordu w `phones` oznacza brak telefonów; statusy enrichmentu w `jobs` wskazują powody braku danych.
- **Statusy enrichmentu:** Tabela `jobs` zawiera `detail_fetched`, `phones_attempted`, `phones_blocked`, `employer_fetched`, oraz `fetch_error`, żeby móc odróżnić czy proces syncu napotkał błąd od rzeczywistego braku danych po stronie OLX.

## Output Formats

```bash
# Human-readable table (default in terminal, JSON when piped)
olx-pp-cli jobs --limit 10

# JSON for scripting and agents
olx-pp-cli jobs --limit 10 --json

# Dry run — show the request without sending
olx-pp-cli sync --dry-run
```

## Agent Usage

This CLI is designed for AI agent consumption:

- **Non-interactive** - never prompts, every input is a flag
- **Pipeable** - `--json` output to stdout, errors to stderr
- **Offline-friendly** - sync/search commands can use the local SQLite store when available
- **Agent-safe by default** - no colors or formatting unless `--human-friendly` is set

Exit codes: `0` success, `2` usage error, `3` not found, `5` API error, `7` rate limited, `10` config error.

## Health Check

```bash
olx-pp-cli doctor
```

Verifies configuration and connectivity to the API.

## Troubleshooting
**Not found errors (exit code 3)**
- Check the resource ID is correct
- Run the `list` command to see available items

---

Generated by [CLI Printing Press](https://github.com/mvanhorn/cli-printing-press)
