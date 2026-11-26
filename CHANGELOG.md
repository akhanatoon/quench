# Changelog

All notable changes to `quench` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.1] - 2026-11-26

### Added
- **Auto-fix mode (`quench run --fix` / MCP `fix: true`)**: Runs check `fix_command` (`gofmt -w`, `golangci-lint --fix`, `eslint --fix`, `ruff check --fix`) before verification and bypasses memo cache.
- **Compact report mode (`quench run --compact`, enabled by default in MCP `verify_changes`)**: Omits skipped checks from `checks` while preserving `summary.skipped`, cutting monorepo JSON payloads by 60–80%.
- **TypeScript `tsconfig.json` / `jsconfig.json` path alias resolution**: Parses JSONC comments and `baseUrl`/`paths` (`@/*`, `@components/*`) in the transitive JS/TS reverse-importer graph.
- **`sandbox.copy_exclude` & `quench warm status --all`**: Custom glob exclusions (in addition to default `.env*`, `*.key`, `*.pem` exclusions) for remote Docker `copy` mode, plus a daemon-wide managed warm container table.
- **`passed_with_filtered` status**: Distinct status (CLI `[PASS*]`, GitHub Actions `::warning`) when `--changed-lines-only` suppresses legacy out-of-diff linter diagnostics.

### Fixed
- **Go proxy retraction (`retract [v0.2.0, v0.5.3]`)**: Retracted pre-hardening tags from `proxy.golang.org` so `go install github.com/akhanatoon/quench/cmd/quench@latest` resolves cleanly to `v0.1.1+`.
- **Real Veripod monorepo field fixes**: Strip Windows `vet.exe:` prefix, parse Go 1.24+ `ImportPath`/`FailedBuild` JSON events, prefix `Failure.File` with `working_dir`, use `npx --no-install` with actionable `npm ci` hint, and hash real project toolchain versions / Docker image digests in memo cache keys.

## [0.1.0] - 2026-11-26

Initial public alpha release of `quench` — a single-binary Go CLI and native `stdio` MCP server for warm-sandbox, diff-aware code verification.

### Highlights
- **Zero-config & polyglot monorepo support**: Auto-detects Go, Node/TypeScript, Python, and Rust at the repository root and nested workspaces (`apps/*`, `services/*`, `packages/*`, `backend/`, `frontend/`, `web/`, `api/`, `src-tauri/`) with per-check `working_dir` and `image` overrides.
- **Diff-aware target & dependency resolution**: Tracks staged, unstaged, untracked, deleted (`--diff-filter=D`), and `--base <ref>` changes; expands Go packages with full cycle-safe transitive reverse-importers (`go/parser`), JS/TS 1-hop relative imports + Vitest/Jest related tests, Python filename + reverse-import tests, and Rust `cargo check`/`cargo test` crate checks.
- **Warm Docker sandbox & remote daemon support**:
  - Persistent per-repo / per-stack container pool (`quench-warm-<hash8>`) with named dependency cache volumes and lockfile-hashed auto-sync.
  - `workspace_mode: auto|bind|copy` automatically detects remote `DOCKER_HOST` / `docker context` endpoints and syncs via streaming `tar` without host bind mounts.
  - Container hardening (`--security-opt no-new-privileges:true --cap-drop ALL`, optional `sandbox.user`, `sandbox.memory`, `sandbox.cpus`, post-sync `network: none`) and automatic best-effort idle GC.
- **Execution engine & token-lean outputs**:
  - Topological `depends_on` DAG waves with `max_parallel` concurrency cap, `--fail-fast`, and POSIX single-quoted shell argument safety.
  - Parsers for Go (`go test -json`, `go vet`, `golangci-lint`), Node/TS (`tsc`, `eslint`, `vitest`, `jest`), Python (`ruff`, `mypy`, `pyright`, `pytest`), Rust (`cargo check/clippy --message-format=json`, `cargo test`), and generic fallback; capped at 10 unique failures with `output_stats` compression metrics.
  - Output formats: `text`, `json`, `sarif` (2.1.0), and `github` (`::error` annotations), plus linter-only `--changed-lines-only` git hunk filtering and env-aware auto-pruned memoization cache.
- **CLI & native `stdio` MCP server**:
  - Commands: `init`, `run`, `watch`, `warm`, `hook`, `bench`, `doctor`, and `mcp` (`install --client claude|codex|cursor|opencode`).
  - MCP tools (`verify_changes`, `sandbox_status`, `sandbox_reset`) confined to the startup workspace root by default (additional roots require explicit `--allow-dir` or `QUENCH_MCP_ALLOW_DIRS`).

[0.1.0]: https://github.com/akhanatoon/quench/releases/tag/v0.1.0
