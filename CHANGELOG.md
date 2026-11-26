# Changelog

All notable changes to `quench` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.6.0] - 2026-11-26

### Added
- **Sandbox resource limits**: `sandbox.memory` (e.g. `1g`) and `sandbox.cpus` (e.g. `2`) are passed to `docker run` as `--memory`, `--memory-swap` and `--cpus`, so a warm container cannot exceed its budget.
- **`max_parallel`**: caps how many checks of one DAG wave execute at the same time (`0` = unlimited).
- **Automatic idle-container GC**: every Docker-mode `quench run` and MCP `verify_changes` triggers a best-effort, non-blocking garbage collection of expired warm containers.
- **Memo cache pruning**: the result cache is trimmed once per process (24h age, 200 entries, 10 MiB), oldest first.
- **`quench watch --poll-interval`**: configurable poll interval (default 500ms, minimum 100ms), replacing the fixed 250ms.

## [0.5.2] - 2026-11-26

### Fixed
- **Cross-platform path normalization in `diff` and `parser`**: Normalize Windows backslashes (`\`) consistently on Linux/macOS hosts inside `normalizeRelPath` and `normalizeFailurePath`.

## [0.5.1] - 2026-11-26

### Fixed
- **Cross-platform SARIF & GitHub annotation paths**: Normalize Windows backslashes (`\`) to forward slashes (`/`) on Linux/macOS hosts as well (`strings.ReplaceAll` + `filepath.ToSlash`).

## [0.5.0] - 2026-11-26

### Added
- **JS/TS reverse-importer graph** (`internal/diff/graph_jsts.go`): Scans relative `import`/`export`/`require` specifiers (including TypeScript ESM `.js -> .ts` and barrel `index.*`) to expand affected JS/TS files by 1-hop direct importers.
- **Check dependency DAG (`depends_on`)**: Topological wave scheduler (`BuildWaves`) runs independent checks in parallel while skipping downstream checks when an upstream dependency fails (`dependency failed: <dep>`), with cycle validation in `config.Validate`.
- **Git hook manager (`quench hook install|uninstall`)**: Worktree-safe, idempotent `pre-push` / `pre-commit` hook installation with fenced blocks that preserve existing user hook commands.
- **Built-in benchmark runner (`quench bench`)**: Measures real `min / avg / max` latency across `local`, `memo-cached`, and `docker-warm` modes and outputs JSON or a ready-to-paste Markdown table.

## [0.4.0] - 2026-11-26

### Added
- **Watch mode (`quench watch`)**: Zero-dependency debounced file watcher that re-runs diff-aware checks inside the warm sandbox on every save.
- **Diff-line hunk filtering (`--changed-lines-only` / MCP `changed_lines_only`)**: Parses `git diff -U0` line ranges to suppress pre-existing legacy linter noise on untouched lines while always preserving test failures.
- **SARIF 2.1.0 & GitHub Actions reporters (`--format sarif|github`)**: Native `::error` workflow annotations and SARIF 2.1.0 output for GitHub Code Scanning and IDE Problems panels.
- **Result memoization cache (`--no-cache` to bypass)**: Content-hashed report cache returning unchanged verification results in `<15ms` (`cached: true`).

## [0.3.0] - 2026-11-26

### Added
- **Polyglot multi-container Docker pool**: Warm sandbox orchestration now supports dedicated per-stack containers in polyglot repositories so Go, Node/TypeScript, Python, and Rust checks execute in their respective runtime images without toolchain collisions.
- **Per-check image overrides**: Checks in `quench.yaml` can specify an explicit container `image` override for specialized toolchains or versions.
- **Real subprocess stdio MCP E2E tests** (`internal/mcp/e2e_subprocess_test.go`): Compiles the real `quench` CLI binary, spawns `quench mcp` over OS stdin/stdout pipes, exercises both Windows CRLF (`\r\n`) and Unix LF (`\n`) framing, verifies notification silence, tool discovery, failure-to-fix `verify_changes` reports with `output_stats`, and clean zero-stray-output shutdown.
- **Opt-in live multi-stack Docker test suite** (`internal/sandbox/docker_live_test.go`): End-to-end warm container creation, reuse, command execution, and cache-purging `Reset(ctx, true)` verification across Go (`golang:1.24-alpine`), Node (`node:22-alpine`), and Python (`python:3.12-slim`), gated behind `QUENCH_LIVE_DOCKER=1`.

### Changed
- **Python & Node setup hardening**: Improved dependency sync reliability and container setup defaults for Python and Node warm sandboxes.

## [0.2.0] - 2026-11-26

### Added
- **Monorepo & polyglot `working_dir` isolation**: Auto-detects root and nested workspaces (`apps/*`, `services/*`, `packages/*`, `backend/`, `frontend/`, `web/`, `api/`, `src-tauri/`) and scopes checks per workspace.
- **Rust stack support**: Zero-config detection for `Cargo.toml` with `cargo check --message-format=json` and `cargo test --no-fail-fast` parsers.
- **Reverse-importer dependency graph**: Expands Go and Python diff scopes to include 1-hop direct importers via stdlib `go/parser` and Python import scanning.
- **Output compression telemetry**: Reports `output_stats` (`raw_bytes`, `report_bytes`, `reduction_pct`) on verification runs.
- **Onboarding & diagnostics CLI**: Added `quench doctor` and `quench mcp install --client claude|codex|cursor|opencode`.

## [0.1.0] - 2026-11-26

### Added
- Initial `quench` single-binary Go CLI (`init`, `run`, `warm`, `mcp`).
- Diff-aware change resolution across staged, unstaged, untracked, and `--base <ref>` git diffs with automatic full-suite escalation on manifest changes.
- Persistent warm Docker sandbox (`quench-warm-<hash8>`) with lockfile-hashed dependency sync, named cache volumes, idle GC, and post-sync `network: none` isolation, plus `--local` host execution mode.
- Token-lean JSON failure parsers for Go (`go test`, `go vet`, `golangci-lint`), Node/TypeScript (`tsc`, `eslint`, `vitest`, `jest`), and Python (`ruff`, `mypy`, `pytest`).
- Native JSON-RPC 2.0 `stdio` MCP server exposing `verify_changes`, `sandbox_status`, and `sandbox_reset`.

[0.3.0]: https://github.com/akhanatoon/quench/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/akhanatoon/quench/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/akhanatoon/quench/releases/tag/v0.1.0
