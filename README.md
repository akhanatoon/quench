# `quench` 🔥💧

**Warm-sandbox, diff-aware verification for AI coding agents — in milliseconds, not CI minutes.**

`quench` is a single-binary Go CLI and native `stdio` MCP server that verifies only what changed in `git diff` inside a persistent warm Docker container (or locally), returning a compressed, token-lean JSON report coding agents can fix in a single turn.

<!-- Demo GIF / Video Placeholder -->
> 🎬 **[15s Demo Placeholder]** — `assets/demo.gif`: Claude Code / Codex edits a broken Go/TypeScript handler → calls `verify_changes` over MCP → `quench` executes diff-scoped checks in a warm container in `~420ms` → returns a 12-line JSON pinpointing `file:line` → agent fixes it on the first retry.

---

## Why `quench`?

Coding agents waste **minutes** and **thousands of context tokens** running full test suites or cold container builds on every edit:

| Workflow | Cold Setup | Scope | Typical Latency | Agent Output Cost |
| :--- | :--- | :--- | :--- | :--- |
| **Classic CI Push Loop** | Full image + dep install | Entire repository | `2m – 8m` | Manual log scraping |
| **Raw `npm test` / `pytest` in shell** | Host state drift | Entire test suite | `8s – 45s` | `2,000 – 15,000` tokens of noisy stdout |
| **`quench` (Warm Sandbox + Diff-Aware)** | **0ms** (already warm + cached) | **Only affected packages/tests** | **`0.2s – 1.8s`** | **`~120 – 400` tokens** (failures only) |

### Key Capabilities

1. **Diff-Aware Target Resolution** — Inspects staged, unstaged, untracked, and `--base <ref>` changes; runs only affected Go packages (`./internal/diff`), related Vitest/Jest/ESLint files, or mapped Python `pytest` modules. Automatically escalates to a full-suite run when critical manifests (`go.mod`, `package.json`, `tsconfig.json`, `pyproject.toml`) change.
2. **Warm Docker Sandbox + Lockfile Hash Sync** — Keeps a per-repo background container (`quench-warm-<hash8>`) alive with named dependency cache volumes (`/go/pkg/mod`, `node_modules`, `pip`). Hashes lockfiles (`go.sum`, `package-lock.json`, `pnpm-lock.yaml`, `uv.lock`) so dependency installs run **only** when lockfiles actually change.
3. **Dynamic Stack & Version Zero-Config** — Works out of the box without a config file by reading `go.mod`, `.nvmrc`/`package.json#engines`, and `.python-version`/`pyproject.toml` to pick the matching container image and checks.
4. **Token-Lean Output Contract** — Swallows passing test noise, strips ANSI codes, deduplicates diagnostics, and caps output at 10 unique failures (`file`, `line`, `symbol`, `message`, `suggested_scope`, `raw_excerpt`).
5. **Dual Interface (CLI + `stdio` MCP)** — Built for both humans (`quench run`) and coding agents (`quench mcp`) with per-call `project_dir` support.

---

## Installation

### Go Install

```bash
go install github.com/akhanatoon/quench/cmd/quench@latest
```

### Prebuilt Binaries

Download single static binaries for **Linux**, **macOS** (`amd64` / `arm64`), and **Windows** from [GitHub Releases](https://github.com/akhanatoon/quench/releases).

---

## Quick Start (CLI)

```bash
# Zero-config diff-aware verification (warm Docker sandbox by default)
quench run

# Run directly on host without Docker
quench run --local

# Emit token-lean JSON report against main branch
quench run --base main --json

# Run only specific checks
quench run --only lint,test --fail-fast

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

`quench mcp` exposes three tools over `stdio` JSON-RPC 2.0:
- `verify_changes` (`project_dir`, `base_ref`, `checks`, `mode`, `fail_fast`)
- `sandbox_status` (`project_dir`)
- `sandbox_reset` (`project_dir`, `purge_cache`)

### Claude Code

```bash
claude mcp add quench -- quench mcp
```

Or in `.mcp.json`:

```json
{
  "mcpServers": {
    "quench": {
      "command": "quench",
      "args": ["mcp"]
    }
  }
}
```

### OpenAI Codex CLI (`~/.codex/config.toml`)

```toml
[mcp_servers.quench]
command = "quench"
args = ["mcp"]
```

### OpenCode / Cursor (`mcp.json`)

```json
{
  "mcpServers": {
    "quench": {
      "command": "quench",
      "args": ["mcp"]
    }
  }
}
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

## Optional `quench.yaml`

`quench` works with zero configuration for Go, Node/TypeScript, and Python repositories. When you want custom checks, air-gapped network execution, or custom cache paths, run `quench init`:

```yaml
version: 1
mode: docker # docker | local
sandbox:
  image: node:22-alpine
  idle_timeout: 30m
  network: bridge # bridge | none
  sync_deps: true
  cache_paths:
    - /root/.npm
    - /workspace/node_modules
checks:
  - name: typecheck
    command: "npx tsc --noEmit"
    scope: project
    parser: tsc
    timeout: 45s
    extensions: [".ts", ".tsx"]
  - name: lint
    command: "npx eslint {{files}}"
    scope: changed_files
    parser: eslint
    extensions: [".ts", ".tsx", ".js"]
  - name: test
    command: "npx vitest related --run {{files}}"
    scope: changed_files
    parser: vitest
    extensions: [".ts", ".tsx", ".js"]
```

---

## License

MIT © [Akhanatoon](https://github.com/akhanatoon)
