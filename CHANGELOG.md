# Changelog

All notable changes to `quench` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2026-11-26

### Added
- **Polyglot multi-container Docker pool**: Warm sandbox orchestration now supports dedicated per-stack containers in polyglot repositories so Go, Node/TypeScript, Python, and Rust checks execute in their respective runtime images without toolchain collisions.
- **Per-check image overrides**: Checks in `quench.yaml` can specify an explicit container `image` override for specialized toolchains or versions.
- **Real subprocess stdio MCP E2E tests** (`internal/mcp/e2e_subprocess_test.go`): Compiles the real `quench` CLI binary, spawns `quench mcp` over OS stdin/stdout pipes, exercises both Windows CRLF (`\r\n`) and Unix LF (`\n`) framing, verifies notification silence, tool discovery, failure-to-fix `verify_changes` reports with `output_stats`, and clean zero-stray-output shutdown.
- **Opt-in live multi-stack Docker test suite** (`internal/sandbox/docker_live_test.go`): End-to-end warm container creation, reuse, command execution, and cache-purging `Reset(ctx, true)` verification across Go (`golang:1.24-alpine`), Node (`node:22-alpine`), and Python (`python:3.12-slim`), gated behind `QUENCH_LIVE_DOCKER=1`.

### Changed
- **Python & Node setup hardening**: Improved dependency sync reliability and container setup defaults for Python and Node warm sandboxes.

## [0.2.0] - 2026-04-06

### Added
- **Monorepo & polyglot `working_dir` isolation**: Auto-detects root and nested workspaces (`apps/*`, `services/*`, `packages/*`, `backend/`, `frontend/`, `web/`, `api/`, `src-tauri/`) and scopes checks per workspace.
- **Rust stack support**: Zero-config detection for `Cargo.toml` with `cargo check --message-format=json` and `cargo test --no-fail-fast` parsers.
- **Reverse-importer dependency graph**: Expands Go and Python diff scopes to include 1-hop direct importers via stdlib `go/parser` and Python import scanning.
- **Output compression telemetry**: Reports `output_stats` (`raw_bytes`, `report_bytes`, `reduction_pct`) on verification runs.
- **Onboarding & diagnostics CLI**: Added `quench doctor` and `quench mcp install --client claude|codex|cursor|opencode`.

## [0.1.0] - 2026-04-05

### Added
- Initial `quench` single-binary Go CLI (`init`, `run`, `warm`, `mcp`).
- Diff-aware change resolution across staged, unstaged, untracked, and `--base <ref>` git diffs with automatic full-suite escalation on manifest changes.
- Persistent warm Docker sandbox (`quench-warm-<hash8>`) with lockfile-hashed dependency sync, named cache volumes, idle GC, and post-sync `network: none` isolation, plus `--local` host execution mode.
- Token-lean JSON failure parsers for Go (`go test`, `go vet`, `golangci-lint`), Node/TypeScript (`tsc`, `eslint`, `vitest`, `jest`), and Python (`ruff`, `mypy`, `pytest`).
- Native JSON-RPC 2.0 `stdio` MCP server exposing `verify_changes`, `sandbox_status`, and `sandbox_reset`.

[0.3.0]: https://github.com/akhanatoon/quench/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/akhanatoon/quench/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/akhanatoon/quench/releases/tag/v0.1.0
