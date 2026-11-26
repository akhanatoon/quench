# `quench` Launch Kit

---

## 1. X (English) — Main Post + 2-Tweet Follow-Up

### Main Post *(attach `assets/demo-x.mp4`)*

AI coding agents burn minutes and thousands of context tokens re-running full test suites after every edit.

I built `quench` — a single-binary Go CLI + native `stdio` MCP server that runs diff-aware verification inside a persistent warm Docker sandbox (or locally).

On a real 4-Go-service + Next.js polyglot monorepo (single-file edit):
- Verification latency: `41.4s` full suite → `4.13s` uncached (`~10x` faster, `2.18s` cached)
- Output sent to the agent: `43.5 KB` (~10.9k tokens) → `702 B` compact JSON (`98.4%` smaller, 9 unrelated checks skipped)

Install:
```
brew install akhanatoon/tap/quench
# or: go install github.com/akhanatoon/quench/cmd/quench@latest
quench mcp install --client claude
```

MIT licensed: https://github.com/akhanatoon/quench

### Follow-Up 1/2 (How it scopes work)

Instead of running the whole repo on a 3-line edit, `quench` reads `git diff` (staged, unstaged, untracked, deleted, or `--base main`) and walks the reverse-dependency graph:
- Go: stdlib `go/parser` transitive reverse-importer BFS
- TS/JS: relative imports + `tsconfig.json` path aliases + Vitest/Jest related tests
- Python & Rust: reverse-import / crate checks

Manifest edits (`go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`) automatically escalate to a full run.

### Follow-Up 2/2 (Warm sandbox + agent ergonomics)

First run warms a per-repo container (`quench-warm-<hash8>`) with cached dep volumes and lockfile-hash sync, hardened with `--cap-drop ALL` and `--security-opt no-new-privileges:true`.

Works with Claude Code, Codex, Cursor, and OpenCode in one command:
```
quench mcp install --client claude|codex|cursor|opencode
```
Also ships `--fix`, `--changed-lines-only`, SARIF/GitHub Actions output, and `quench bench`.

---

## 2. X / LinkedIn (Turkish)

AI coding agent'lar her küçük değişiklikten sonra tüm test suite'ini çalıştırıp hem dakika yakıyor hem de context window'u binlerce satır yeşil logla dolduruyor.

Bunu çözmek için `quench`'i yazdım: tek binary Go CLI + native `stdio` MCP sunucusu. Sadece `git diff`'te değişen hedefleri ve bunları import eden paketleri sıcak (warm) Docker sandbox içinde ya da yerelde doğruluyor, agent'a sadece hataları içeren sıkıştırılmış JSON dönüyor.

Gerçek bir polyglot monorepo (4 Go servisi + Next.js, tek dosya değişikliği) üzerindeki ölçüm:
- Doğrulama süresi: `41.4 sn` (tüm suite) → `4.13 sn` (uncached, `~10x` daha hızlı; cache'li tekrar `2.18 sn`)
- Agent'a giden çıktı küçülmesi: `43.5 KB` (~10.9k token) → `702 B` compact JSON (`%98.4` daha küçük, ilgisiz 9 kontrol otomatik atlandı)

Öne çıkanlar:
- Go (`go/parser` transitive reverse-importer BFS), TS/JS (`tsconfig` path alias + Vitest/Jest), Python, Rust için sıfır konfigürasyon monorepo algılama
- Lockfile hash'i değişmedikçe bağımlılık kurmayan warm Docker container havuzu (`--cap-drop ALL`, opsiyonel `network: none`)
- Tek komutla MCP kurulumu (`claude`, `codex`, `cursor`, `opencode`), `--fix` ve `--changed-lines-only`

Kurulum:
```bash
brew install akhanatoon/tap/quench
# veya: go install github.com/akhanatoon/quench/cmd/quench@latest
quench mcp install --client claude
```

Açık kaynak (MIT): https://github.com/akhanatoon/quench *(attach `assets/demo-x.mp4`)*

---

## 3. Hacker News (`Show HN`)

### Title
```
Show HN: Quench – Warm-sandbox, diff-aware code verification CLI and MCP server
```

### First Comment

Hi HN — I built `quench` because using coding agents on multi-package repos kept hitting the same two bottlenecks: waiting for full test/lint runs after small edits, and feeding 5,000–15,000 tokens of passing stdout back into the model context.

`quench` is a single static Go binary (zero CGO) that runs either as a CLI (`quench run`) or as a `stdio` JSON-RPC 2.0 MCP server (`quench mcp`).

**How it works:**

1. **Diff-aware target resolution + reverse-importer graph.**
   `quench` inspects staged, unstaged, untracked, deleted, and `--base <ref>` changes from `git`. For Go, it parses the repo with stdlib `go/parser` (`parser.ImportsOnly`) and runs a cycle-safe BFS over the reverse-import graph so changing a leaf package tests every transitive importer without running unrelated packages. For JS/TS, it parses relative imports plus `tsconfig.json`/`jsconfig.json` `baseUrl`/`paths` aliases and delegates to Vitest/Jest related-test selection. Python and Rust have analogous filename/reverse-import and crate-level checks. Any edit to a critical manifest (`go.mod`, `package.json`, `tsconfig.json`, `pyproject.toml`, `Cargo.toml`) or deleted package escalates to a full run.

2. **Warm Docker sandbox (`bind` and `copy` modes) + hardening.**
   Per repository/stack, `quench` keeps a warm container (`quench-warm-<hash8>`) alive with named dependency cache volumes (`/go/pkg/mod`, `node_modules`, `pip`, `cargo/registry`) and only re-runs dependency sync when the lockfile hash changes. `workspace_mode: auto|bind|copy` detects remote `DOCKER_HOST` / `docker context` daemons and switches from host bind mounts to streaming `tar` sync (excluding `.env*`, `*.key`, `*.pem`, plus configurable `sandbox.copy_exclude`). Containers start with `--cap-drop ALL --security-opt no-new-privileges:true`, optional `memory`/`cpus`/`user` limits, and optional post-sync `--network none`.

3. **Token-lean output contract & MCP confinement.**
   Passing output is dropped, ANSI codes are stripped, diagnostics are deduplicated and capped at 10 unique failures (`file`, `line`, `symbol`, `message`, `suggested_scope`, `raw_excerpt`), and `output_stats` reports `raw_bytes`, `report_bytes`, and `reduction_pct`. In MCP mode, `verify_changes` defaults to compact mode (omitting skipped checks) and all tools (`verify_changes`, `sandbox_status`, `sandbox_reset`) are confined to the server's startup workspace root unless extra roots are explicitly granted via `--allow-dir` or `QUENCH_MCP_ALLOW_DIRS`.

On a real 4-Go-service + Next.js polyglot monorepo (editing one file in `services/metering`), running `go test -v ./...` across all 4 services took **41.4s** and emitted **43.5 KB** (~10.9k tokens), whereas `quench run --local --compact --no-cache --json` finished in **4.13s** (**~10x faster**, **2.18s** memo-cached) and emitted **702 bytes** of JSON (**98.4%** smaller vs full suite, **97.3%** smaller vs scoped raw stdout, automatically skipping 9 unrelated checks).

**Honest limitations:**
- Static import graphs do not catch dynamic reflection, runtime plugin loading, or cross-service HTTP/RPC contract breaks — `quench` is a fast local/agent feedback loop, not a replacement for full CI before merge.
- First-time cold image pulls and initial lockfile sync still pay normal Docker/package-manager latency; the speedup is on warm incremental loops.
- `copy` mode tar streaming adds overhead on very large repos with thousands of untracked files if ignore rules aren't configured.
- Docker sandbox targets Linux containers (via Docker Desktop or a remote Linux daemon on macOS/Windows); native Windows containers aren't supported (use `--local` on host Windows).

Repo (MIT): https://github.com/akhanatoon/quench — happy to answer questions or take critique on the graph resolution and sandbox model.

---

## 4. Reddit Drafts

### `r/ClaudeAI`

**Title:** I built a Go CLI + MCP server (`quench`) so Claude Code verifies only changed code in ~1s and stops burning context on passing logs

**Body:**
When I use Claude Code on a medium/large repo, two things waste the most time and tokens: running the entire test suite after a 5-line edit, and dumping thousands of lines of passing test output into the context window.

I built `quench` (MIT, single binary) to fix that loop:
- **Diff-aware + reverse importers:** Checks only what changed in `git diff` plus packages/tests that depend on it (Go, TS/JS with `tsconfig` paths, Python, Rust). Escalates to full suite on manifest edits.
- **Warm Docker sandbox or `--local`:** Keeps a warm container with cached dependency volumes so incremental runs don't pay cold setup.
- **Compact JSON for MCP:** Drops passing noise, caps at 10 deduplicated failures, reports exact byte reduction, and supports `fix: true` (`gofmt`/`eslint --fix`/`ruff --fix`) before verifying.

Real monorepo numbers (4 Go services + Next.js, single-file edit): full `go test -v ./...` took **41.4s** and **43.5 KB** (~10.9k tokens); `quench run --local --compact --no-cache --json` took **4.13s** (**~10x faster**, **2.18s** cached) and **702 bytes** (**98.4%** smaller, 9 unrelated checks skipped).

Setup takes two commands:
```bash
brew install akhanatoon/tap/quench
quench mcp install --client claude
```
(Also supports Codex, Cursor, and OpenCode, plus `go install github.com/akhanatoon/quench/cmd/quench@latest`.)

Repo + demo: https://github.com/akhanatoon/quench

---

### `r/golang`

**Title:** `quench`: Zero-CGO Go CLI + MCP server for diff-aware, warm-sandbox verification using `go/parser` transitive reverse-importer BFS

**Body:**
I built `quench` in Go (zero CGO, stdlib-first) to give both developers and AI coding agents sub-second/second-level verification feedback without running `go test ./...` on every small edit.

A few Go implementation details that might be interesting here:
- **Dependency graph:** Walks the module with `go/parser` (`parser.ImportsOnly`) and computes the cycle-safe transitive reverse-importer closure via BFS from changed packages. Editing `go.mod` or deleting a package escalates to `./...`.
- **Structured parsing:** Parses `go test -json` (including Go 1.24+ `ImportPath`/`FailedBuild` events), `go vet`, and `golangci-lint`, deduplicating failures into a compact JSON/SARIF/GitHub Actions contract.
- **Execution engine:** Topological `depends_on` DAG waves with `max_parallel` concurrency, env/toolchain-version-aware memoization cache, and a persistent warm Docker container pool (`bind` locally, streaming `tar` `copy` mode for remote `DOCKER_HOST`, `--cap-drop ALL`).
- **Polyglot monorepos:** Also detects TS/JS, Python, and Rust workspaces under nested directories with per-check `working_dir`.

Benchmark on a real 4-Go-service + Next.js monorepo (single-file edit): full `go test -v ./...` took **41.4s** / **43.5 KB**; `quench run --local --compact --no-cache --json` took **4.13s** (**~10x faster**, **2.18s** cached) and **702 bytes** (**97.3%–98.4%** log reduction, skipping 9 unrelated checks).

Install:
```bash
go install github.com/akhanatoon/quench/cmd/quench@latest
```
Repo (MIT): https://github.com/akhanatoon/quench

---

## 5. `awesome-mcp-servers` PR Entry

Exact 1-line Markdown row (under **Developer Tools**):

```markdown
- [akhanatoon/quench](https://github.com/akhanatoon/quench) 🏎️ 🏠 - Warm-sandbox, diff-aware code verification CLI and `stdio` MCP server for Go, TypeScript/Node, Python, and Rust that returns token-lean failure JSON.
```

---

## 6. Pre-Launch Checklist

- [x] **Veripod benchmark run:** Measured 41.4s / 43.5 KB full suite vs 4.13s / 702 B uncached `quench` JSON (2.18s cached) and injected into `docs/LAUNCH.md` and `README.md`.
- [ ] **GitHub Social Preview:** Upload `assets/social-preview.png` under **Settings → General → Social preview** on `akhanatoon/quench`.
- [ ] **Release `v0.1.2`:** Push annotated tag `v0.1.2` and verify `.github/workflows/release.yml` (GoReleaser) publishes `checksums.txt` plus `darwin`/`linux`/`windows` (`amd64` + `arm64`) archives.
- [ ] **Update Homebrew tap (`akhanatoon/homebrew-tap`):**
  ```bash
  python3 scripts/render-homebrew-formula.py 0.1.2 \
    https://github.com/akhanatoon/quench/releases/download/v0.1.2/checksums.txt \
    -o ../homebrew-tap/Formula/quench.rb
  ```
  Commit and push `Formula/quench.rb` to `akhanatoon/homebrew-tap`.
- [ ] **Homebrew tap smoke test (macOS or Linux):**
  ```bash
  brew tap akhanatoon/tap
  brew install akhanatoon/tap/quench
  quench --version
  brew test akhanatoon/tap/quench
  ```
- [ ] **Go install smoke test:**
  ```bash
  go install github.com/akhanatoon/quench/cmd/quench@latest
  quench --version
  ```
- [ ] **Publish posts:** X (with `assets/demo-x.mp4`), LinkedIn/X TR, Show HN, Reddit (`r/ClaudeAI`, `r/golang`), and open `awesome-mcp-servers` PR.
