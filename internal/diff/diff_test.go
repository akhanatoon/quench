package diff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/akhanatoon/quench/internal/config"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func writeFile(t *testing.T, repoDir, rel, content string) {
	t.Helper()
	full := filepath.Join(repoDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestChangedFilesWorkingTreeStagedUntrackedDeletedAndBaseRef(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")

	writeFile(t, dir, "go.mod", "module example.com/m\n\ngo 1.24\n")
	writeFile(t, dir, "internal/a/a.go", "package a\n")
	writeFile(t, dir, "internal/b/b.go", "package b\n")
	writeFile(t, dir, "obsolete.go", "package main\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	// Create feature branch with a committed change
	runGit(t, dir, "checkout", "-b", "feat/x")
	writeFile(t, dir, "internal/a/a.go", "package a\n// committed on branch\n")
	runGit(t, dir, "add", "internal/a/a.go")
	runGit(t, dir, "commit", "-m", "feat commit")

	// Unstaged modification
	writeFile(t, dir, "internal/b/b.go", "package b\n// unstaged\n")
	// Staged new file
	writeFile(t, dir, "internal/c/c.go", "package c\n")
	runGit(t, dir, "add", "internal/c/c.go")
	// Untracked file
	writeFile(t, dir, "cmd/app/main.go", "package main\n")
	// Deleted file (must NOT appear in changed files)
	_ = os.Remove(filepath.Join(dir, "obsolete.go"))

	ctx := context.Background()

	// Working tree only (without --base main, internal/a/a.go is clean vs HEAD)
	wtFiles, err := ChangedFiles(ctx, dir, "")
	if err != nil {
		t.Fatalf("ChangedFiles failed: %v", err)
	}
	wantWT := []string{"cmd/app/main.go", "internal/b/b.go", "internal/c/c.go"}
	if !reflect.DeepEqual(wtFiles, wantWT) {
		t.Fatalf("working tree mismatch:\ngot:  %v\nwant: %v", wtFiles, wantWT)
	}

	// With --base main, internal/a/a.go is included as well
	branchFiles, err := ChangedFiles(ctx, dir, "main")
	if err != nil {
		t.Fatalf("ChangedFiles(--base main) failed: %v", err)
	}
	wantBranch := []string{"cmd/app/main.go", "internal/a/a.go", "internal/b/b.go", "internal/c/c.go"}
	if !reflect.DeepEqual(branchFiles, wantBranch) {
		t.Fatalf("baseRef diff mismatch:\ngot:  %v\nwant: %v", branchFiles, wantBranch)
	}
}

func TestResolveGoPackagesPythonTestsAndCriticalTrigger(t *testing.T) {
	// 1. Go package deduplication
	goRepo := t.TempDir()
	writeFile(t, goRepo, "go.mod", "module example.com/x\n\ngo 1.24\n")
	writeFile(t, goRepo, "internal/diff/git.go", "package diff\n")
	writeFile(t, goRepo, "internal/diff/git_test.go", "package diff\n")
	writeFile(t, goRepo, "main.go", "package main\n")

	goCheck := config.CheckConfig{
		Name:       "test",
		Command:    "go test -json {{packages}}",
		Scope:      config.ScopeChangedPackages,
		Extensions: []string{".go"},
	}
	res := Resolve(goRepo, config.StackGo, goCheck, []string{"internal/diff/git.go", "internal/diff/git_test.go", "main.go", "README.md"})
	if res.Skip || res.FullSuite {
		t.Fatalf("unexpected skip/fullSuite: %+v", res)
	}
	if res.Command != "go test -json . ./internal/diff" {
		t.Fatalf("unexpected go command: %q", res.Command)
	}

	// Critical file go.mod triggers full suite ./...
	critRes := Resolve(goRepo, config.StackGo, goCheck, []string{"go.mod", "internal/diff/git.go"})
	if !critRes.FullSuite || critRes.Command != "go test -json ./..." {
		t.Fatalf("expected full suite ./..., got: %+v", critRes)
	}

	// Only README.md changed -> skip
	skipRes := Resolve(goRepo, config.StackGo, goCheck, []string{"README.md"})
	if !skipRes.Skip {
		t.Fatalf("expected skip when only README.md changed, got: %+v", skipRes)
	}

	// 2. Python mapped tests resolution
	pyRepo := t.TempDir()
	writeFile(t, pyRepo, "src/billing/invoice.py", "def total(): return 1\n")
	writeFile(t, pyRepo, "tests/billing/test_invoice.py", "def test_total(): assert True\n")
	pyCheck := config.CheckConfig{
		Name:       "test",
		Command:    "pytest -q {{tests}}",
		Scope:      config.ScopeMappedTests,
		Extensions: []string{".py"},
	}
	pyRes := Resolve(pyRepo, config.StackPython, pyCheck, []string{"src/billing/invoice.py"})
	if pyRes.Skip || pyRes.Command != "pytest -q tests/billing/test_invoice.py" {
		t.Fatalf("unexpected python mapped command: %+v", pyRes)
	}
}

func TestGoReverseImporterAndWorkingDirAndPythonReverseTests(t *testing.T) {
	// 1. Go 1-hop reverse importer expansion inside a monorepo working_dir
	mono := t.TempDir()
	writeFile(t, mono, "backend/go.mod", "module example.com/svc\n\ngo 1.24\n")
	writeFile(t, mono, "backend/pkg/core/core.go", "package core\n\nfunc Value() int { return 1 }\n")
	writeFile(t, mono, "backend/pkg/api/api.go", "package api\n\nimport \"example.com/svc/pkg/core\"\n\nfunc Handle() int { return core.Value() }\n")
	writeFile(t, mono, "backend/pkg/unrelated/u.go", "package unrelated\n")

	ch := config.CheckConfig{
		Name:       "backend-test",
		Command:    "go test -json {{packages}}",
		Scope:      config.ScopeChangedPackages,
		Stack:      config.StackGo,
		WorkingDir: "backend",
		Extensions: []string{".go"},
	}
	res := Resolve(mono, config.StackPolyglot, ch, []string{"backend/pkg/core/core.go", "frontend/app.tsx"})
	if res.Skip {
		t.Fatalf("expected backend check not to skip: %+v", res)
	}
	wantPkgs := []string{"./pkg/api", "./pkg/core"}
	if !reflect.DeepEqual(res.TargetPackages, wantPkgs) {
		t.Fatalf("reverse importer expansion mismatch:\ngot:  %v\nwant: %v", res.TargetPackages, wantPkgs)
	}

	// 2. Python reverse test import scan (test filename doesn't match module name)
	pyRepo := t.TempDir()
	writeFile(t, pyRepo, "app/pricing.py", "def discount(): return 5\n")
	writeFile(t, pyRepo, "tests/test_checkout_flow.py", "from app.pricing import discount\n\ndef test_flow(): assert discount() == 5\n")
	pyCheck := config.CheckConfig{
		Name:       "test",
		Command:    "pytest -q {{tests}}",
		Scope:      config.ScopeMappedTests,
		Extensions: []string{".py"},
	}
	pyRes := Resolve(pyRepo, config.StackPython, pyCheck, []string{"app/pricing.py"})
	if pyRes.Skip || !reflect.DeepEqual(pyRes.TargetTests, []string{"tests/test_checkout_flow.py"}) {
		t.Fatalf("expected reverse-imported python test, got: %+v", pyRes)
	}
}
