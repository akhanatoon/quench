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

func TestDetectPolyglotMonorepoAndRust(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "apps", "web"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "services", "engine"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "apps", "web", "package.json"), []byte(`{"name":"web","devDependencies":{"typescript":"^5.0.0"}}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "apps", "web", "tsconfig.json"), []byte(`{}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "services", "engine", "Cargo.toml"), []byte("[package]\nname = \"engine\"\nrust-version = \"1.84\"\n"), 0o644)

	cfg, fromFile, err := Load(dir)
	if err != nil {
		t.Fatalf("Load polyglot failed: %v", err)
	}
	if fromFile {
		t.Fatalf("expected zero-config detection")
	}
	if cfg.Stack != StackPolyglot {
		t.Fatalf("expected stack polyglot, got %s", cfg.Stack)
	}

	var foundWeb, foundRust bool
	for _, ch := range cfg.Checks {
		if ch.WorkingDir == "apps/web" && ch.Name == "apps-web-typecheck" {
			foundWeb = true
			if ch.Image != "node:22-alpine" {
				t.Fatalf("expected apps/web check image node:22-alpine, got %q", ch.Image)
			}
		}
		if ch.WorkingDir == "services/engine" && ch.Name == "services-engine-check" && ch.Stack == StackRust {
			foundRust = true
			if ch.Image != "rust:1.84-slim" {
				t.Fatalf("expected services/engine check image rust:1.84-slim, got %q", ch.Image)
			}
		}
	}
	if !foundWeb || !foundRust {
		t.Fatalf("expected polyglot checks for apps/web and services/engine, got: %+v", cfg.Checks)
	}
}

func TestDetectPolyglotPythonAndNodePerCheckImages(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "backend"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "frontend"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "backend", "pyproject.toml"), []byte("[project]\nname = \"api\"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "frontend", "package.json"), []byte(`{"name":"ui","devDependencies":{"typescript":"^5.0.0"}}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "frontend", "tsconfig.json"), []byte(`{}`), 0o644)

	cfg, _, err := Load(dir)
	if err != nil {
		t.Fatalf("Load polyglot failed: %v", err)
	}
	if cfg.Stack != StackPolyglot {
		t.Fatalf("expected stack polyglot, got %s", cfg.Stack)
	}
	if cfg.Sandbox.Image != "python:3.12-slim" {
		t.Fatalf("expected primary image python:3.12-slim, got %s", cfg.Sandbox.Image)
	}

	var sawPy, sawNode bool
	for _, ch := range cfg.Checks {
		switch ch.WorkingDir {
		case "backend":
			sawPy = true
			if ch.Image != "python:3.12-slim" {
				t.Fatalf("expected python check %q image python:3.12-slim, got %q", ch.Name, ch.Image)
			}
		case "frontend":
			sawNode = true
			if ch.Image != "node:22-alpine" {
				t.Fatalf("expected node check %q image node:22-alpine, got %q", ch.Name, ch.Image)
			}
		}
	}
	if !sawPy || !sawNode {
		t.Fatalf("expected both backend and frontend checks, got: %+v", cfg.Checks)
	}
}

func TestDetectNodeZeroConfigSafety(t *testing.T) {
	t.Run("custom npm test script without vitest or jest", func(t *testing.T) {
		dir := t.TempDir()
		pkgJSON := `{
  "name": "svc",
  "scripts": { "test": "node --test" }
}`
		_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{}`), 0o644)

		cfg, _, err := Load(dir)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		var testCheck *CheckConfig
		for i := range cfg.Checks {
			if cfg.Checks[i].Name == "test" {
				testCheck = &cfg.Checks[i]
			}
		}
		if testCheck == nil {
			t.Fatalf("expected npm test check, got: %+v", cfg.Checks)
		}
		if testCheck.Command != "npm test" || testCheck.Scope != ScopeProject || testCheck.Parser != "generic" {
			t.Fatalf("unexpected test check: %+v", *testCheck)
		}
	})

	t.Run("placeholder test script and tsconfig skips broken vitest", func(t *testing.T) {
		dir := t.TempDir()
		pkgJSON := `{
  "name": "app",
  "scripts": { "test": "echo \"Error: no test specified\" && exit 1" }
}`
		_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{}`), 0o644)

		cfg, _, err := Load(dir)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		for _, ch := range cfg.Checks {
			if ch.Name == "test" {
				t.Fatalf("expected no test check when only placeholder test script exists with tsconfig.json, got: %+v", ch)
			}
		}
	})

	t.Run("no test runner or script with tsconfig skips broken vitest", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"ts-only"}`), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{}`), 0o644)

		cfg, _, err := Load(dir)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		var sawTypecheck bool
		for _, ch := range cfg.Checks {
			if ch.Name == "typecheck" {
				sawTypecheck = true
			}
			if ch.Name == "test" {
				t.Fatalf("expected no broken vitest check when tsconfig.json exists without test runner, got: %+v", ch)
			}
		}
		if !sawTypecheck {
			t.Fatalf("expected typecheck check, got: %+v", cfg.Checks)
		}
	})
}

func TestValidateResourceControls(t *testing.T) {
	base := func() *Config {
		return &Config{Mode: ModeLocal, Checks: []CheckConfig{{Name: "a", Command: "true", Scope: ScopeProject}}}
	}
	okMem := []string{"", "512", "512m", "1g", "1G", "64K", "100b"}
	for _, m := range okMem {
		c := base()
		c.Sandbox.Memory = m
		if err := Validate(c); err != nil {
			t.Errorf("memory %q should be valid: %v", m, err)
		}
	}
	for _, m := range []string{"1gb", "g", "-1g", "1.5g", "abc", "1 g"} {
		c := base()
		c.Sandbox.Memory = m
		if err := Validate(c); err == nil {
			t.Errorf("memory %q should be invalid", m)
		}
	}
	for _, v := range []string{"", "1", "2", "0.5", "1.25"} {
		c := base()
		c.Sandbox.CPUs = v
		if err := Validate(c); err != nil {
			t.Errorf("cpus %q should be valid: %v", v, err)
		}
	}
	for _, v := range []string{"0", "-1", "abc", "NaN", "Inf", "0.0"} {
		c := base()
		c.Sandbox.CPUs = v
		if err := Validate(c); err == nil {
			t.Errorf("cpus %q should be invalid", v)
		}
	}
	for _, n := range []int{0, 1, 8} {
		c := base()
		c.MaxParallel = n
		if err := Validate(c); err != nil {
			t.Errorf("max_parallel %d should be valid: %v", n, err)
		}
	}
	c := base()
	c.MaxParallel = -1
	if err := Validate(c); err == nil {
		t.Error("negative max_parallel should be invalid")
	}
}

func TestLoadResourceControlsYAML(t *testing.T) {
	dir := t.TempDir()
	y := "mode: local\nmax_parallel: 3\nsandbox:\n  memory: 1g\n  cpus: \"2\"\nchecks:\n  - name: a\n    command: echo hi\n"
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxParallel != 3 || cfg.Sandbox.Memory != "1g" || cfg.Sandbox.CPUs != "2" {
		t.Fatalf("unexpected: %+v", cfg)
	}
}

func TestValidateWorkspaceMode(t *testing.T) {
	base := func() *Config {
		return &Config{Mode: ModeDocker, Checks: []CheckConfig{{Name: "a", Command: "true", Scope: ScopeProject}}}
	}
	for _, m := range []string{"", WorkspaceModeAuto, WorkspaceModeBind, WorkspaceModeCopy} {
		c := base()
		c.Sandbox.WorkspaceMode = m
		if err := Validate(c); err != nil {
			t.Errorf("workspace_mode %q should be valid: %v", m, err)
		}
	}
	for _, m := range []string{"remote", "volume", "rsync", "COPY", "Auto"} {
		c := base()
		c.Sandbox.WorkspaceMode = m
		if err := Validate(c); err == nil {
			t.Errorf("workspace_mode %q should be invalid", m)
		}
	}
}

func TestLoadWorkspaceModeAndUserYAML(t *testing.T) {
	dir := t.TempDir()
	y := "mode: docker\nsandbox:\n  image: golang:1.24-alpine\n  workspace_mode: copy\n  user: \"1000:1000\"\nchecks:\n  - name: a\n    command: echo hi\n"
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.WorkspaceMode != WorkspaceModeCopy || cfg.Sandbox.User != "1000:1000" {
		t.Fatalf("unexpected sandbox config: %+v", cfg.Sandbox)
	}

	// Omitting workspace_mode should default to "auto".
	yDefault := "mode: docker\nsandbox:\n  image: golang:1.24-alpine\nchecks:\n  - name: a\n    command: echo hi\n"
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(yDefault), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgDef, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfgDef.Sandbox.WorkspaceMode != WorkspaceModeAuto {
		t.Fatalf("expected default workspace_mode %q, got %q", WorkspaceModeAuto, cfgDef.Sandbox.WorkspaceMode)
	}

	// Invalid workspace_mode in YAML must fail Load.
	yBad := "mode: docker\nsandbox:\n  workspace_mode: invalid\nchecks:\n  - name: a\n    command: echo hi\n"
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(yBad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(dir); err == nil {
		t.Fatalf("expected error for invalid workspace_mode in YAML")
	}
}
