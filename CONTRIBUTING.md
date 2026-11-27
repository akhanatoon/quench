# Contributing to `quench`

Thanks for your interest in contributing to `quench`.

## Prerequisites

- Go 1.23+ (zero CGO required)
- Git
- Docker *(optional — only needed for warm-sandbox integration tests; `--local` tests run without Docker)*

## Local Development

```bash
git clone https://github.com/akhanatoon/quench.git
cd quench

# Static analysis and full test suite
go vet ./...
go test ./...

# Build and install locally
go install ./cmd/quench
quench --version
```

## Architecture Overview

- `cmd/quench/` — CLI entrypoint
- `internal/cli/` — Cobra commands (`run`, `doctor`, `init`, `warm`, `watch`, `hook`, `bench`, `mcp`, `stats`)
- `internal/config/` — Zero-config stack/monorepo detection, `quench.yaml` schema, and `ref:path` loading
- `internal/diff/` — Git diff inspection and reverse-dependency graph resolution (Go, JS/TS, Python, Rust)
- `internal/sandbox/` — Local and warm Docker (`bind` / `copy`) sandboxes, locking, and orphan GC
- `internal/runner/` — Topological DAG execution, memoization cache, compact/SARIF/GitHub formatters
- `internal/parser/` — Structured parsers for `go test -json`, `go vet`, `tsc`, `eslint`, `vitest`, `jest`, `pytest`, `ruff`, `cargo`
- `internal/mcp/` — Native `stdio` JSON-RPC 2.0 MCP server with workspace root confinement

## Pull Request Guidelines

1. Open or reference an existing issue before starting large changes.
2. Keep PRs focused — one feature or bugfix per branch.
3. Add or update unit tests for any behavioral change.
4. Ensure `go vet ./...` and `go test ./...` pass locally before opening the PR.
5. Keep output contracts backward-compatible (`--json`, MCP tool schemas, exit codes `0`/`1`/`2`).
