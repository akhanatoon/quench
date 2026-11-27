package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadFromGitRefSpec(t *testing.T) {
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")

	yamlContent := `version: 1
mode: local
default_timeout: 3m
checks:
  - name: race-test
    command: "go test -race ./..."
    scope: project
    env:
      CGO_ENABLED: "1"
      GOFLAGS: "-count=1"
`
	if err := os.WriteFile(filepath.Join(dir, "quench.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "quench.yaml")
	runGit("commit", "-m", "add quench.yaml")

	// Switch to a branch where quench.yaml is deleted (simulates older worktree)
	runGit("checkout", "-b", "old-feature")
	if err := os.Remove(filepath.Join(dir, "quench.yaml")); err != nil {
		t.Fatal(err)
	}

	cfg, fromFile, err := LoadFrom(dir, "main:quench.yaml")
	if err != nil {
		t.Fatalf("LoadFrom(main:quench.yaml) error: %v", err)
	}
	if !fromFile {
		t.Fatalf("expected fromFile=true")
	}
	if len(cfg.Checks) != 1 || cfg.Checks[0].Name != "race-test" {
		t.Fatalf("unexpected checks: %+v", cfg.Checks)
	}
	if cfg.Checks[0].ParsedTimeout() != 3*time.Minute {
		t.Fatalf("expected default_timeout 3m applied, got %v", cfg.Checks[0].ParsedTimeout())
	}
	if cfg.Checks[0].Env["CGO_ENABLED"] != "1" || cfg.Checks[0].Env["GOFLAGS"] != "-count=1" {
		t.Fatalf("unexpected check env: %+v", cfg.Checks[0].Env)
	}
}

func TestValidateRejectsInvalidEnvAndDefaultTimeout(t *testing.T) {
	cfg := &Config{
		Mode:           ModeLocal,
		DefaultTimeout: "not-a-duration",
		Checks:         []CheckConfig{{Name: "test", Command: "echo ok", Scope: ScopeProject}},
	}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "default_timeout") {
		t.Fatalf("expected default_timeout validation error, got: %v", err)
	}

	cfg2 := &Config{
		Mode: ModeLocal,
		Checks: []CheckConfig{{
			Name:    "test",
			Command: "echo ok",
			Scope:   ScopeProject,
			Env:     map[string]string{"BAD-KEY": "1"},
		}},
	}
	if err := Validate(cfg2); err == nil || !strings.Contains(err.Error(), "invalid env variable name") {
		t.Fatalf("expected invalid env variable name error, got: %v", err)
	}
}

func TestIsGitRefSpecDoesNotConfuseWindowsDrivePath(t *testing.T) {
	dir := t.TempDir()
	if isGitRefSpec(dir, `C:\Users\dev\quench.yaml`) {
		t.Fatal("Windows drive path must not be treated as git ref spec")
	}
	if isGitRefSpec(dir, `D:/Proje/quench/quench.yaml`) {
		t.Fatal("Windows slash drive path must not be treated as git ref spec")
	}
	if !isGitRefSpec(dir, "origin/main:quench.yaml") {
		t.Fatal("origin/main:quench.yaml should be detected as git ref spec")
	}
}
