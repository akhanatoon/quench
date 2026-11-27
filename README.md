<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg" />
  <source media="(prefers-color-scheme: light)" srcset="assets/logo-light.svg" />
  <img alt="quench" src="assets/logo-dark.svg" width="260" />
</picture>

<p><strong>Warm-sandbox, diff-aware verification for AI coding agents — in ~1 second, not CI minutes.</strong></p>

<p>
  <a href="https://github.com/akhanatoon/quench/actions/workflows/ci.yml"><img src="https://github.com/akhanatoon/quench/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://github.com/akhanatoon/quench/releases"><img src="https://img.shields.io/github/v/release/akhanatoon/quench?color=22b8cf" alt="Release" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT" /></a>
</p>

</div>

`quench` is a single-binary Go CLI and native `stdio` MCP server that verifies only what changed in `git diff` (plus direct reverse-importers) inside a persistent warm Docker container or locally, returning a compressed, token-lean JSON report coding agents can fix in a single turn.

<p align="center">
  <img src="assets/demo.gif" alt="quench warm-sandbox diff verification demo" width="880" />
</p>

---

## Why `quench`?

Coding agents waste **minutes** and **thousands of context tokens** running full test suites or cold container builds on every edit:

| Workflow | Cold Setup | Scope | Measured Latency | Agent Output Cost |
| :--- | :--- | :--- | :--- | :--- |
| **Classic CI Push Loop** | Full image + dep install | Entire repository | `2m – 8m` | Manual log scraping |
| **Raw `go test -v ./...`** *(4-service monorepo)* | Host state drift | All 4 Go services | `41.4s` | `43.5 KB` (~10.9k tokens) |
| **`quench` (Diff-Aware + Compact JSON)** | **0ms** (warm / local) | **Changed pkg + reverse importers** | **`4.13s`** *(uncached)* / **`2.18s`** *(cached)* | **`702 B`** (`98.4%` smaller, 9 checks skipped) |

> Measured on a real polyglot monorepo (4 Go services + Next.js) after a single-file edit: `quench run --local --compact --no-cache --json` ran in `4.13s` (`~10x` faster than full `go test -v ./...` at `41.4s`) and compressed `16.6 KB` of scoped raw stdout (`43.5 KB` full suite) down to `702 B` of indented JSON (`97.3%–98.4%` reduction).

### Key Capabilities

1. **Diff-Aware + Transitive Dependency Graph** — Inspects staged, unstaged, untracked, **deleted**, and `--base <ref>` changes; resolves affected Go packages **plus full transitive reverse-importers** via stdlib `go/parser`, JS/TS 1-hop relative imports & Vitest/Jest related tests, Python filename + reverse-import tests, and Rust `cargo check`/`cargo test` crate checks. Automatically escalates to a full-suite run when critical manifests (`go.mod`, `package.json`, `tsconfig.json`, `pyproject.toml`, `Cargo.toml`) or deleted packages require it.
2. **Polyglot & Monorepo Zero-Config** — Detects root and nested workspaces (`apps/*`, `services/*`, `packages/*`, `backend/`, `frontend/`, `web/`, `api/`, `src-tauri/`) across **Go, Node/TypeScript, Python, and Rust** with per-check `working_dir` isolation.
3. **Warm Docker Sandbox + Lockfile Hash Sync** — Keeps a per-repo background container (`quench-warm-<hash8>`) alive with named dependency cache volumes (`/go/pkg/mod`, `node_modules`, `pip`, `cargo/registry`). Hashes lockfiles so dependency installs run **only** when lockfiles actually change, and supports post-sync `network: none` isolation.
4. **Token-Lean Output Contract + Compression Stats** — Swallows passing test noise, strips ANSI codes, deduplicates diagnostics, caps output at 10 unique failures (`file`, `line`, `symbol`, `message`, `suggested_scope`, `raw_excerpt`), and reports exact `output_stats` (`raw_bytes`, `report_bytes`, `reduction_pct`).
5. **One-Command MCP Onboarding** — Native `stdio` MCP server (`quench mcp`) with `quench mcp install --client claude|codex|cursor|opencode` and `quench doctor`.

---

## Installation

### Homebrew (macOS / Linux)

```bash
brew install akhanatoon/tap/quench
```

### One-Line Installer (Linux / macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/akhanatoon/quench/main/scripts/install.sh | sh
```

### One-Line Installer (Windows PowerShell)

```powershell
irm https://raw.githubusercontent.com/akhanatoon/quench/main/scripts/install.ps1 | iex
```

### Go Install

```bash
go install github.com/akhanatoon/quench/cmd/quench@latest
```

### Prebuilt Binaries

Download single static binaries for **Linux**, **macOS** (`amd64` / `arm64`), and **Windows** from [GitHub Releases](https://github.com/akhanatoon/quench/releases).

---

## Quick Start (CLI)

```bash
# Check git, Docker, detected stack(s), and local toolchains
quench doctor

# Zero-config diff-aware verification (warm Docker sandbox by default)
quench run

# Run directly on host, auto-fix formatting/lint first, or suppress pre-existing legacy lint lines
quench run --local --fix
quench run --local --changed-lines-only --compact

# Emit token-lean JSON, SARIF 2.1.0, or GitHub Actions annotations
quench run --base main --format json
# Read config from another git ref (e.g. older branches/worktrees) or override timeout
quench run --base origin/main --config origin/main:quench.yaml --timeout 180s --json
quench run --format sarif
quench run --format github

# Continuous debounced watch mode inside warm sandbox
quench watch --local

# Install a non-destructive pre-push git hook
quench hook install pre-push --fail-fast

# Benchmark local vs memo-cached vs warm Docker on your repo
quench bench --runs 3

# Generate an explicit quench.yaml tailored to your repo
quench init

# Manage warm sandboxes manually
quench warm up
quench warm status
quench warm down --purge-cache
quench warm gc
```

**Exit Codes:** `0` = all checks passed (or no matching changed files), `1` = check failure, `2` = configuration/runtime error.

---

## MCP Setup for Coding Agents

`quench mcp` exposes three tools over `stdio` JSON-RPC 2.0 (all supporting optional `project_dir`, confined to the startup workspace root unless `--allow-dir` / `QUENCH_MCP_ALLOW_DIRS` is set):
- `verify_changes` (`project_dir`, `base_ref`, `checks`, `mode`, `fail_fast`, `fix`, `compact`)
- `sandbox_status` (`project_dir`)
- `sandbox_reset` (`project_dir`, `purge_cache`)

### One-Command Registration

```bash
# Preview changes first with --dry-run
quench mcp install --client claude --dry-run

# Register directly
quench mcp install --client claude      # writes .mcp.json (add --global for ~/.claude.json)
quench mcp install --client codex       # updates ~/.codex/config.toml
quench mcp install --client cursor      # writes .cursor/mcp.json
quench mcp install --client opencode    # writes opencode.json
```

Or via Claude Code CLI:

```bash
claude mcp add quench -- quench mcp
```

---

## JSON Output Contract

```json
{
  "status": "failed",
  "mode": "docker-warm",
  "project_dir": "/workspace/my-service",
  "duration_ms": 640,
  "changed_files": ["internal/diff/git.go"],
  "summary": {
    "total_checks": 2,
    "passed": 1,
    "failed": 1,
    "skipped": 0
  },
  "output_stats": {
    "raw_bytes": 14820,
    "report_bytes": 512,
    "reduction_pct": 96.5
  },
  "checks": [
    {
      "name": "vet",
      "status": "passed",
      "duration_ms": 180
    },
    {
      "name": "test",
      "status": "failed",
      "duration_ms": 460,
      "failures": [
        {
          "file": "internal/diff/git_test.go",
          "line": 42,
          "symbol": "TestParsePorcelain",
          "message": "expected 3 files, got 2",
          "suggested_scope": "internal/diff",
          "raw_excerpt": "git_test.go:42: expected 3 files, got 2"
        }
      ]
    }
  ]
}
```

---

## GitHub Action

Run diff-aware verification directly in pull requests with inline annotations:

```yaml
- uses: actions/checkout@v4
  with:
    fetch-depth: 0
- uses: akhanatoon/quench@v0.1.3
  with:
    base: ${{ github.event.pull_request.base.sha }}
    format: github
    changed-lines-only: "true"
```

---

## Optional `quench.yaml` (Including Monorepos)

`quench` works with zero configuration for Go, Node/TypeScript, Python, and Rust repositories. For custom checks, monorepo `working_dir` scopes, or air-gapped network execution, run `quench init`:

```yaml
version: 1
mode: docker # docker | local
sandbox:
  image: node:22-alpine
  idle_timeout: 30m
  network: bridge # bridge | none
  user: nonroot   # optional: "nonroot" (65532:65532) or "uid:gid" with writable /tmp caches
  sync_deps: true
  memory: 1g      # optional hard cap for the warm container
  cpus: "2"       # optional CPU cap
  cache_paths:
    - /root/.npm
    - /workspace/node_modules
default_timeout: 120s # optional: default per-check timeout (default 120s)
max_parallel: 2   # optional: max concurrent checks per wave (0 = unlimited)
checks:
  - name: web-typecheck
    working_dir: apps/web
    command: "npx tsc --noEmit"
    scope: project
    parser: tsc
    timeout: 45s
    extensions: [".ts", ".tsx"]
  - name: api-test
    working_dir: services/api
    command: "go test -json {{packages}}"
    scope: changed_packages
    stack: go
    parser: go-test
    env:
      CGO_ENABLED: "1"
    extensions: [".go"]
```

---

## License

MIT © [Akhanatoon](https://github.com/akhanatoon)
