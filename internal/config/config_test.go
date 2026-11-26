package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectGoDynamicVersionAndGolangCI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.25.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".golangci.yml"), []byte("run:\n  timeout: 1m\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, fromFile, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if fromFile {
		t.Fatalf("expected zero-config detection, got fromFile=true")
	}
	if cfg.Stack != StackGo {
		t.Fatalf("expected stack go, got %s", cfg.Stack)
	}
	if cfg.Sandbox.Image != "golang:1.25-alpine" {
		t.Fatalf("expected dynamic image golang:1.25-alpine, got %s", cfg.Sandbox.Image)
	}
	if len(cfg.Checks) != 3 || cfg.Checks[0].Name != "lint" || cfg.Checks[1].Name != "vet" || cfg.Checks[2].Name != "test" {
		t.Fatalf("unexpected checks: %+v", cfg.Checks)
	}
}

func TestDetectNodeDynamicVersionPnpmAndJest(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{
  "name": "web",
  "engines": { "node": ">=20.11.0" },
  "devDependencies": {
    "typescript": "^5.0.0",
    "eslint": "^9.0.0",
    "jest": "^29.0.0"
  }
}`
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte("{}"), 0o644)

	cfg, _, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Sandbox.Image != "node:20-alpine" {
		t.Fatalf("expected node:20-alpine, got %s", cfg.Sandbox.Image)
	}
	if len(cfg.Checks) != 3 {
		t.Fatalf("expected 3 checks, got %d (%+v)", len(cfg.Checks), cfg.Checks)
	}
	if cfg.Checks[0].Command != "pnpm exec tsc --noEmit" {
		t.Fatalf("unexpected typecheck cmd: %s", cfg.Checks[0].Command)
	}
	if cfg.Checks[2].Parser != "jest" {
		t.Fatalf("expected jest parser, got %s", cfg.Checks[2].Parser)
	}
}

func TestDetectPythonDynamicVersionAndMypy(t *testing.T) {
	dir := t.TempDir()
	pyproject := `[project]
name = "svc"
requires-python = ">=3.11"

[tool.mypy]
strict = true
`
	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pyproject), 0o644)

	cfg, _, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Sandbox.Image != "python:3.11-slim" {
		t.Fatalf("expected python:3.11-slim, got %s", cfg.Sandbox.Image)
	}
	if len(cfg.Checks) != 3 || cfg.Checks[1].Name != "typecheck" {
		t.Fatalf("unexpected python checks: %+v", cfg.Checks)
	}
}

func TestLoadCustomYAMLAndValidation(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `version: 1
mode: local
checks:
  - name: lint
    command: "ruff check {{files}}"
    scope: changed_files
    timeout: 15s
`
	_ = os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(yamlContent), 0o644)
	cfg, fromFile, err := Load(dir)
	if err != nil {
		t.Fatalf("Load YAML failed: %v", err)
	}
	if !fromFile || cfg.Mode != ModeLocal || len(cfg.Checks) != 1 {
		t.Fatalf("unexpected loaded config: %+v", cfg)
	}

	// Invalid scope must fail validation
	badYAML := `version: 1
mode: local
checks:
  - name: bad
    command: "echo hi"
    scope: unknown_scope
`
	_ = os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(badYAML), 0o644)
	if _, _, err := Load(dir); err == nil {
		t.Fatalf("expected validation error for unknown_scope")
	}
}
