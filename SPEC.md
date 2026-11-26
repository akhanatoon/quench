# `quench` — Teknik Spesifikasyon (SPEC)

> *"Warm-sandbox, diff-aware verification for AI coding agents — in milliseconds, not CI minutes."*

## 1. Proje Özeti ve Hedef

**`quench`**, kodlama ajanlarının (Claude Code, Codex, Cursor, OpenCode, Antigravity vb.) push/PR öncesi kod değişikliklerini klasik CI/CD beklemeden **saniyeler içinde doğrulamasını** sağlayan, tek binary olarak dağıtılan açık kaynaklı Go aracıdır.

Metaforunu demircilikten alır: kor halindeki kodu sıcak bekleyen ocağa/suya (*warm sandbox*) batırıp anında sertliğini doğrular.

### Temel Değer Önerisi

1. **Diff-Aware:** Tüm projeyi değil, yalnızca `git diff` ile değişen dosyaların etkilediği lint, type-check ve testleri çalıştırır.
2. **Warm Sandbox + Lockfile Sync:** Her çalıştırmada sıfırdan imaj/ortam kurmak yerine arka planda hazır bekleyen sıcak (warm) Docker container üzerinde `exec` ile anlık çalışır; lockfile hash kontrolüyle yalnızca bağımlılıklar gerçekten değiştiğinde kurulum tetikler.
3. **Token-Dostu Çıktı:** Yüzlerce satırlık ham log yerine ajanın tek hamlede düzeltebileceği sıkıştırılmış JSON hata raporu (`file`, `line`, `symbol`, `message`, `suggested_scope`, `raw_excerpt`) döner.
4. **Çift Arayüz:** Hem insan/script için ergonomik CLI (`quench run`) hem kodlama ajanları için dizin-bağımsız (`project_dir` destekli) native **MCP Server (`stdio`)** sunar.

---

## 2. Teknoloji Yığını ve Dizin Yapısı

* **Modül:** `github.com/akhanatoon/quench`
* **Dil:** Go 1.23+
* **CLI Framework:** `github.com/spf13/cobra`
* **MCP Katmanı:** Hafif, sıfır-şişkinlikli JSON-RPC 2.0 `stdio` MCP sunucusu (`2024-11-05` / `2025-03-26` uyumlu)
* **Container Kontrolü:** `os/exec` üzerinden Docker CLI (`docker run/exec/inspect/rm/volume`) — sıfır bağımlılık şişkinliği, Colima / OrbStack / Docker Desktop / WSL2 / Linux tam uyumu
* **Konfigürasyon:** `gopkg.in/yaml.v3` (`quench.yaml`)

```text
quench/
├── cmd/quench/
│   └── main.go                 # Entrypoint
├── internal/
│   ├── config/
│   │   ├── config.go           # quench.yaml şeması, validasyon ve loader
│   │   └── detect.go           # Zero-config stack + dinamik imaj sürümü tespiti (Go, Node/TS, Python)
│   ├── diff/
│   │   ├── git.go              # Changed files (staged, unstaged, untracked, --base ref)
│   │   └── resolver.go         # Değişen dosyaları ilgili test/lint hedeflerine ve scope'a eşleme
│   ├── sandbox/
│   │   ├── sandbox.go          # Interface: EnsureWarm, Exec, Status, Reset, Teardown
│   │   ├── docker.go           # Warm container havuzu, cache volume ve lockfile hash senkronizasyonu
│   │   └── local.go            # Docker yoksa/istenmezse host fallback modu
│   ├── runner/
│   │   ├── runner.go           # Paralel/sıralı check orkestratörü + context timeout
│   │   └── types.go            # CheckResult, Failure, RunReport struct'ları
│   ├── parser/
│   │   ├── parser.go           # Parser registry + 10 benzersiz hata limiti (truncation)
│   │   ├── golang.go           # go test -json, go vet, golangci-lint parser
│   │   ├── node.go             # tsc, eslint, vitest/jest parser
│   │   ├── python.go           # ruff, mypy/pyright, pytest parser
│   │   └── generic.go          # Fallback (son N anlamlı hata satırı + file:line çıkarımı)
│   ├── mcp/
│   │   └── server.go           # stdio MCP sunucusu (verify_changes, sandbox_status, sandbox_reset)
│   └── cli/
│       ├── root.go             # Global bayraklar (--dir / -C)
│       ├── run.go              # quench run
│       ├── warm.go             # quench warm [up|down|status|gc]
│       ├── init.go             # quench init
│       └── mcp.go              # quench mcp
├── testdata/                   # Örnek Go, TS, Python mini repoları (integration test)
├── .goreleaser.yaml            # Linux / macOS / Windows tek binary paketleme
├── .github/workflows/          # CI & Release otomasyonu
├── SPEC.md
├── go.mod
└── README.md
```

---

## 3. Çekirdek Modül Spesifikasyonları

### 3.1. Zero-Config Stack & Dinamik İmaj Sürümü Tespiti (`internal/config`)

Kullanıcı `quench.yaml` oluşturmasa bile proje köküne bakarak hem kontrolleri hem de projeyle uyumlu container imaj sürümünü otomatik çıkarır:

| Tespit Edilen Dosya | Sürüm Kaynağı | Varsayılan İmaj | Varsayılan Kontroller (Diff-Aware) |
| --- | --- | --- | --- |
| `go.mod` | `go 1.xx` satırı | `golang:<ver>-alpine` (yoksa `golang:1.24-alpine`) | `go vet {{packages}}`, `go test -json {{packages}}` |
| `package.json` (+ `tsconfig.json`) | `.nvmrc`, `.node-version`, `engines.node` | `node:<ver>-alpine` (yoksa `node:22-alpine`) | `tsc --noEmit`, `eslint {{files}}`, `vitest/jest related {{files}}` |
| `pyproject.toml` / `requirements.txt` | `.python-version`, `requires-python` | `python:<ver>-slim` (yoksa `python:3.12-slim`) | `ruff check {{files}}`, `pytest {{tests}}` |

Opsiyonel `quench.yaml` şeması:

```yaml
version: 1
mode: docker # docker | local
sandbox:
  image: node:22-alpine
  idle_timeout: 30m
  network: bridge # bridge | none (kurulum sonrası izole doğrulama için)
  sync_deps: true # lockfile hash değiştiğinde otomatik kurulum
  cache_paths:
    - /root/.npm
    - /workspace/node_modules
checks:
  - name: typecheck
    command: "npx tsc --noEmit"
    scope: project # project | changed_files | changed_packages | mapped_tests
    parser: tsc    # auto | go-test | go-vet | tsc | eslint | vitest | ruff | pytest | generic
    timeout: 30s
  - name: lint
    command: "npx eslint {{files}}"
    scope: changed_files
    extensions: [".ts", ".tsx", ".js", ".jsx"]
  - name: test
    command: "npx vitest related --run {{files}}"
    scope: changed_files
    extensions: [".ts", ".tsx", ".js", ".jsx"]
```

### 3.2. Diff Motoru (`internal/diff`)

* **Varsayılan davranış:** `git status --porcelain -uall` + `git diff --name-only` + `git diff --cached --name-only` birleştirilir, silinmiş dosyalar filtrelenir ve çalışma dizinindeki tüm aktif değişiklikler normalize edilmiş göreli yollarla (`/` ayraçlı) döner.
* **`--base <ref>` modu:** `git diff --name-only --diff-filter=ACMRTUXB <ref>...HEAD` ile çalışma ağacı değişiklikleri birleştirilir.
* **Eşleştirme kuralları:**
  * **Go (`{{packages}}`):** Değişen `.go` dosyalarının dizinleri benzersiz Go paket yollarına (`./internal/diff`, kök için `.`) çevrilir.
  * **Node/TS (`{{files}}`):** Değişen kaynak dosyalar uzantı filtresinden geçirilerek doğrudan linter ve `vitest related --run` / `jest --findRelatedTests` komutuna aktarılır.
  * **Python (`{{tests}}`):** `foo/bar.py` değiştiyse `tests/test_bar.py`, `tests/foo/test_bar.py` veya aynı klasördeki `test_bar.py` varlığı kontrol edilerek eşleştirilir; doğrudan eşleşme yoksa en yakın test dizini veya `pytest` fallback'i devreye girer.
  * **Kritik dosya tetikleyici (`full_suite_triggered`):** `go.mod`, `go.sum`, `package.json`, `tsconfig.json`, `pyproject.toml`, `quench.yaml` değiştiyse kapsam otomatik olarak tüm projeye (`./...`, `.` vb.) genişletilir ve raporda `scope_reason` ile bildirilir.

### 3.3. Warm Sandbox Yöneticisi & Lockfile Sync (`internal/sandbox`)

* **Container Adlandırma:** Proje mutlak yolunun normalize edilmiş SHA256 ilk 8 karakteri ile `quench-warm-<hash8>`.
* **Yaşam Döngüsü:**
  1. `EnsureWarm()`: Container çalışıyor mu kontrol et (`docker inspect`). Durmuşsa `docker start`, yoksa `docker run -d --name quench-warm-<hash8> -v <repo>:/workspace -w /workspace ... <image> sleep infinity` ile başlat.
  2. **Platform-Güvenli Cache Volume:** Dil bazlı named volume'ler (`quench-cache-<hash8>-gomod`, `quench-cache-<hash8>-gocache`, `quench-cache-<hash8>-node-modules`, `quench-cache-<hash8>-npm`, `quench-cache-<hash8>-pip`, `quench-cache-<hash8>-venv`) bağlanır; böylece Windows/macOS host `node_modules`/`.venv` ile Linux container binary'leri birbirini ezmez ve indirilen paketler kalıcı olur.
  3. **Lockfile Hash Kontrolü (Otomatik Bağımlılık Güncelleme):** `go.sum`, `package-lock.json` / `pnpm-lock.yaml` / `yarn.lock`, `requirements.txt` / `pyproject.toml` / `uv.lock` dosyalarının SHA256 özeti hesaplanır ve container içindeki `/tmp/.quench-lockhash` ile karşılaştırılır. Yalnızca hash değiştiğinde (veya ilk kurulumda) ilgili kurulum komutu (`go mod download`, `npm ci || npm install`, `pip install -r ...`) çalıştırılır — sonraki tüm koşular sıfır kurulum süresiyle (`~30-80ms` exec overhead) çalışır.
  4. **İzolasyon:** Kurulum tamamlandıktan sonra doğrulama komutları isteğe bağlı olarak ağsız modda çalıştırılabilir (`sandbox.network: none`).
  5. **Temizlik (`gc`):** Son kullanım zamanı takip edilir; `quench warm gc` ile `idle_timeout` süresini aşan warm container'lar temizlenir.

### 3.4. Token-Dostu Parser & Çıktı Sözleşmesi (`internal/parser`)

Geçen testlerin ham çıktıları yutulur; yalnızca özet ve kırılan noktalar döndürülür.

**Standart JSON Çıktısı (`quench run --json` ve MCP `verify_changes` yanıtı):**

```json
{
  "status": "failed",
  "mode": "docker-warm",
  "project_dir": "/path/to/repo",
  "duration_ms": 840,
  "full_suite_triggered": false,
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
      "duration_ms": 210
    },
    {
      "name": "test",
      "status": "failed",
      "duration_ms": 620,
      "truncated_count": 0,
      "failures": [
        {
          "file": "internal/diff/git_test.go",
          "line": 42,
          "column": 0,
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

* **Hata Tekilleştirme & Kırpma Kısıtı:** Bir check içinde 10'dan fazla benzersiz hata varsa yalnızca ilk 10 hata `failures` dizisinde döner ve geri kalan sayı `"truncated_count": N` alanında bildirilir.

### 3.5. MCP Server (`quench mcp`)

Kodlama ajanlarının MCP yapılandırmasına tek komutla eklenir. Tüm tool'lar opsiyonel `project_dir` parametresi alır; verilmezse sunucunun başlatıldığı çalışma dizini (veya `--dir` bayrağı) kullanılır.

Expose edilen tool'lar:

1. **`verify_changes`**
   * Parametreler:
     * `project_dir` (`string`, opsiyonel): Hedef git deposunun yolu.
     * `base_ref` (`string`, opsiyonel): Karşılaştırılacak git referansı (örn. `main`, `HEAD~1`).
     * `checks` (`string[]`, opsiyonel): Yalnızca seçili check isimlerini çalıştır (örn. `["lint", "test"]`).
     * `mode` (`string`, opsiyonel): `"docker"` | `"local"` (varsayılan config'den gelir, Docker erişilemezse otomatik veya açık fallback bilgisi verir).
   * Çıktı: Yukarıdaki sıkıştırılmış JSON `RunReport`.
2. **`sandbox_status`**
   * Parametreler: `project_dir` (`string`, opsiyonel)
   * Çıktı: Warm container adı, durumu (`running` / `stopped` / `absent`), imaj, uptime, bağlı cache volume'ler, lockfile senkron durumu ve tespit edilen stack.
3. **`sandbox_reset`**
   * Parametreler: `project_dir` (`string`, opsiyonel), `purge_cache` (`boolean`, opsiyonel)
   * Çıktı: Warm container'ı (ve `purge_cache: true` ise named cache volume'lerini) sıfırlar.

---

## 4. CLI Komutları

* `quench init [--dir <path>] [--force]`: Projeyi tarar, tespit edilen stack ve imaj sürümüne göre hazır `quench.yaml` üretir.
* `quench run [--dir <path>] [--base <ref>] [--json] [--local] [--only lint,test] [--fail-fast]`: Diff-aware doğrulamayı çalıştırır.
  * Exit code: `0` (tüm kontroller geçti veya değişen ilgili dosya yok), `1` (en az bir kontrol başarısız), `2` (konfigürasyon/runtime hatası).
* `quench warm [up|status|down|gc] [--dir <path>] [--purge-cache]`: Sıcak Docker sandbox'ı manuel yönetir.
* `quench mcp [--dir <path>]`: `stdio` üzerinden JSON-RPC 2.0 MCP sunucusunu başlatır.

---

## 5. Faz Bazlı Geliştirme Planı

1. **Faz 1 — İskelet, Config & Diff Motoru**
   * `go.mod` kurulumu, Cobra CLI iskeleti.
   * `internal/config` (YAML loader + Go/Node/Python auto-detect + dinamik sürüm çıkarımı).
   * `internal/diff` (`git` wrapper + path/package/test resolver + kritik dosya tetikleyicisi).
   * *Kabul Kriteri:* Geçici git repolarında unit testlerle değişen dosya, paket, Python test eşlemesi ve kritik dosya fallback tespiti %100 geçmeli.
2. **Faz 2 — Local Runner & Parser Katmanı**
   * `internal/sandbox/local.go` ve `internal/runner` (paralel orkestrasyon + context timeout + şablon değişken ikamesi).
   * `internal/parser`: Go (`go test -json`, `go vet`, `golangci-lint`), Node (`tsc`, `eslint`, `vitest`/`jest`), Python (`ruff`, `mypy`/`pyright`, `pytest`) ve `generic` parser'ları + 10 hata sınırı.
   * `quench run --local --json` uçtan uca çalışır hale gelir.
   * *Kabul Kriteri:* `testdata/` içindeki bilerek hatalı mini repolarda (Go, TS, Python) sıkıştırılmış doğru JSON üretilmeli.
3. **Faz 3 — Warm Docker Sandbox & Lockfile Sync**
   * `internal/sandbox/docker.go`: Container lifecycle (`EnsureWarm`, `Exec`, `Status`, `Reset`, `Teardown`, `GC`), named cache volume bağlama, lockfile hash senkronizasyonu, `quench warm` alt komutları.
   * Docker daemon erişilemiyorsa net hata mesajı veya fallback desteği.
   * *Kabul Kriteri:* İlk çalışmada container ayağa kalkmalı, ikinci `quench run` çağrısında container yeniden oluşturulmadan `<2s` içinde yanıt dönmeli.
4. **Faz 4 — MCP Server Entegrasyonu**
   * `internal/mcp/server.go` ve `quench mcp` komutu (`project_dir` destekli `verify_changes`, `sandbox_status`, `sandbox_reset`).
   * *Kabul Kriteri:* JSON-RPC `stdio` istemcisi üzerinden uçtan uca `initialize` → `tools/list` → `tools/call (verify_changes)` testi hatayı yakalayıp doğrulamalı.
5. **Faz 5 — Paketleme, Benchmark & Lansman Hazırlığı**
   * `.goreleaser.yaml` (Linux/macOS/Windows tek binary build) ve GitHub Actions CI + Release workflow'ları.
   * `README.md`: Mimari özet, demo akışı, Claude Code / Codex / Cursor / OpenCode MCP kurulum snippet'leri, "Classic CI vs `quench` Warm Sandbox" benchmark tablosu.
