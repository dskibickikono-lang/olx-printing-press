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
olx-pp-cli apigateway --data example-value --query example-value
```

## Usage

Run `olx-pp-cli --help` for the full command reference and flag list.

## Commands

### apigateway

Operations on graphql

- **`olx-pp-cli apigateway`** - POST /apigateway/graphql

### candidates

Operations on applications-count

- **`olx-pp-cli candidates get-applications-count`** - GET /api/candidates/v1/offers/{id}/applications-count
- **`olx-pp-cli candidates options-applications-count`** - OPTIONS /api/candidates/v1/offers/{id}/applications-count

### data

Operations on cookies.json

- **`olx-pp-cli data`** - GET /data/olx/cookies.json

### friendly-links

Operations on praca,produkcja

- **`olx-pp-cli friendly-links`** - GET /api/v1/friendly-links/query-params/praca,produkcja,obsluga-produkcji/

### graphql

Operations on graphql

- **`olx-pp-cli graphql create-graphql`** - POST /graphql
- **`olx-pp-cli graphql list-graphql`** - GET /graphql
- **`olx-pp-cli graphql options-graphql`** - OPTIONS /graphql

### offers

Operations on filters

- **`olx-pp-cli offers create-offers`** - POST /v1/offers
- **`olx-pp-cli offers get-breadcrumbs`** - GET /api/v1/offers/{id}/breadcrumbs/
- **`olx-pp-cli offers get-offers`** - GET /api/v2/offers/{id}/
- **`olx-pp-cli offers list-breadcrumbs`** - GET /api/v1/offers/metadata/breadcrumbs/
- **`olx-pp-cli offers list-search`** - GET /api/v1/offers/metadata/search/
- **`olx-pp-cli offers list-search-categories`** - GET /api/v1/offers/metadata/search-categories/

### seo

Operations on searches

- **`olx-pp-cli seo list-content`** - GET /api/v1/seo/d/content/
- **`olx-pp-cli seo list-searches`** - GET /api/v1/seo/searches/

### users

Operations on users

- **`olx-pp-cli users <id>`** - GET /api/v1/users/{id}/

### widgets

Operations on widgets

- **`olx-pp-cli widgets`** - POST /api/widgets


## Output Formats

```bash
# Human-readable table (default in terminal, JSON when piped)
olx-pp-cli apigateway --data example-value --query example-value

# JSON for scripting and agents
olx-pp-cli apigateway --data example-value --query example-value --json

# Filter to specific fields
olx-pp-cli apigateway --data example-value --query example-value --json --select id,name,status

# Dry run — show the request without sending
olx-pp-cli apigateway --data example-value --query example-value --dry-run

# Agent mode — JSON + compact + no prompts in one flag
olx-pp-cli apigateway --data example-value --query example-value --agent
```

## Agent Usage

This CLI is designed for AI agent consumption:

- **Non-interactive** - never prompts, every input is a flag
- **Pipeable** - `--json` output to stdout, errors to stderr
- **Filterable** - `--select id,name` returns only fields you need
- **Previewable** - `--dry-run` shows the request without sending
- **Explicit retries** - add `--idempotent` to create retries when a no-op success is acceptable
- **Confirmable** - `--yes` for explicit confirmation of destructive actions
- **Piped input** - write commands can accept structured input when their help lists `--stdin`
- **Offline-friendly** - sync/search commands can use the local SQLite store when available
- **Agent-safe by default** - no colors or formatting unless `--human-friendly` is set

Exit codes: `0` success, `2` usage error, `3` not found, `5` API error, `7` rate limited, `10` config error.

## Health Check

```bash
olx-pp-cli doctor
```

Verifies configuration and connectivity to the API.

## Configuration

Config file: `~/.config/olx-pp-cli/config.toml`

Static request headers can be configured under `headers`; per-command header overrides take precedence.

## Troubleshooting
**Not found errors (exit code 3)**
- Check the resource ID is correct
- Run the `list` command to see available items

---

Generated by [CLI Printing Press](https://github.com/mvanhorn/cli-printing-press)
