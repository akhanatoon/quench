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

func TestPythonSrcLayoutFromPyproject(t *testing.T) {
	pyCheck := config.CheckConfig{
		Name:       "test",
		Command:    "pytest -q {{tests}}",
		Scope:      config.ScopeMappedTests,
		Extensions: []string{".py"},
	}

	// 1. setuptools packages.find with where = ["lib"]: a non-src root.
	libRepo := t.TempDir()
	writeFile(t, libRepo, "pyproject.toml", "[tool.setuptools.packages.find]\nwhere = [\"lib\"]\n")
	writeFile(t, libRepo, "lib/billing/invoice.py", "def total(): return 1\n")
	writeFile(t, libRepo, "tests/billing/test_invoice.py", "def test_total(): assert True\n")
	res := Resolve(libRepo, config.StackPython, pyCheck, []string{"lib/billing/invoice.py"})
	if res.Skip || res.Command != "pytest -q tests/billing/test_invoice.py" {
		t.Fatalf("lib src layout: unexpected command: %+v", res)
	}

	// 2. hatch wheel packages = ["src/mypkg"].
	hatchRepo := t.TempDir()
	writeFile(t, hatchRepo, "pyproject.toml", "[tool.hatch.build.targets.wheel]\npackages = [\"src/mypkg\"]\n")
	writeFile(t, hatchRepo, "src/mypkg/core.py", "def run(): return 1\n")
	writeFile(t, hatchRepo, "tests/mypkg/test_core.py", "def test_run(): assert True\n")
	res = Resolve(hatchRepo, config.StackPython, pyCheck, []string{"src/mypkg/core.py"})
	if res.Skip || res.Command != "pytest -q tests/mypkg/test_core.py" {
		t.Fatalf("hatch src layout: unexpected command: %+v", res)
	}

	// 3. setuptools find where = ["src"] still maps, driven by config now.
	srcRepo := t.TempDir()
	writeFile(t, srcRepo, "pyproject.toml", "[tool.setuptools.packages.find]\nwhere = [\"src\"]\n")
	writeFile(t, srcRepo, "src/billing/invoice.py", "def total(): return 1\n")
	writeFile(t, srcRepo, "tests/billing/test_invoice.py", "def test_total(): assert True\n")
	res = Resolve(srcRepo, config.StackPython, pyCheck, []string{"src/billing/invoice.py"})
	if res.Skip || res.Command != "pytest -q tests/billing/test_invoice.py" {
		t.Fatalf("src layout from pyproject: unexpected command: %+v", res)
	}

	// 4. __init__.py under a custom root maps to the package test dir.
	initRepo := t.TempDir()
	writeFile(t, initRepo, "pyproject.toml", "[tool.setuptools.packages.find]\nwhere = [\"lib\"]\n")
	writeFile(t, initRepo, "lib/billing/__init__.py", "")
	writeFile(t, initRepo, "tests/billing/test_invoice.py", "def test_total(): assert True\n")
	res = Resolve(initRepo, config.StackPython, pyCheck, []string{"lib/billing/__init__.py"})
	if res.Skip || res.Command != "pytest -q tests/billing" {
		t.Fatalf("__init__ under custom root: unexpected command: %+v", res)
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

func TestShellQuoteInjectionSafety(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"src/app.ts", "src/app.ts"},
		{"./internal/diff", "./internal/diff"},
		{"a;id;.ts", "'a;id;.ts'"},
		{"$(whoami).go", "'$(whoami).go'"},
		{"`id`.py", "'`id`.py'"},
		{"a|b.ts", "'a|b.ts'"},
		{"foo'bar.ts", `'foo'\''bar.ts'`},
		{"bad\x00\r\n;cmd.ts", "'bad;cmd.ts'"},
		{"clean\x00\r\nname.go", "cleanname.go"},
		{"", "''"},
	}

	for _, tc := range cases {
		got := shellQuote(tc.in)
		if got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Verify Resolve uses strict shell quoting for ScopeChangedFiles
	repo := t.TempDir()
	check := config.CheckConfig{
		Name:       "lint",
		Command:    "eslint {{files}}",
		Scope:      config.ScopeChangedFiles,
		Extensions: []string{".ts"},
	}
	res := Resolve(repo, config.StackNode, check, []string{"a;id;.ts", "foo'bar.ts"})
	wantCmd := `eslint 'a;id;.ts' 'foo'\''bar.ts'`
	if res.Command != wantCmd {
		t.Fatalf("Resolve command = %q, want %q", res.Command, wantCmd)
	}
}

func TestGoTransitiveReverseImporters3Hop(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/trans\n\ngo 1.24\n")
	writeFile(t, repo, "pkg/a/a.go", "package a\n\nfunc A() int { return 1 }\n")
	writeFile(t, repo, "pkg/b/b.go", "package b\n\nimport \"example.com/trans/pkg/a\"\n\nfunc B() int { return a.A() }\n")
	writeFile(t, repo, "pkg/c/c.go", "package c\n\nimport \"example.com/trans/pkg/b\"\n\nfunc C() int { return b.B() }\n")
	writeFile(t, repo, "pkg/d/d.go", "package d\n\nfunc D() int { return 42 }\n")

	ch := config.CheckConfig{
		Name:       "test",
		Command:    "go test -json {{packages}}",
		Scope:      config.ScopeChangedPackages,
		Extensions: []string{".go"},
	}
	res := Resolve(repo, config.StackGo, ch, []string{"pkg/a/a.go"})
	if res.Skip || res.FullSuite {
		t.Fatalf("unexpected skip/fullSuite: %+v", res)
	}
	wantPkgs := []string{"./pkg/a", "./pkg/b", "./pkg/c"}
	if !reflect.DeepEqual(res.TargetPackages, wantPkgs) {
		t.Fatalf("3-hop transitive reverse importers mismatch:\ngot:  %v\nwant: %v", res.TargetPackages, wantPkgs)
	}
}

func TestDeletedFileDetectionAndResolution(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")

	writeFile(t, dir, "go.mod", "module example.com/deltest\n\ngo 1.24\n")
	writeFile(t, dir, "pkg/a/a1.go", "package a\n\nfunc A1() int { return 1 }\n")
	writeFile(t, dir, "pkg/a/a2.go", "package a\n\nfunc A2() int { return 2 }\n")
	writeFile(t, dir, "pkg/b/b.go", "package b\n\nimport \"example.com/deltest/pkg/a\"\n\nfunc B() int { return a.A1() }\n")
	writeFile(t, dir, "pkg/c/c.go", "package c\n\nimport \"example.com/deltest/pkg/b\"\n\nfunc C() int { return b.B() }\n")
	writeFile(t, dir, "pkg/only/only.go", "package only\n\nfunc Only() {}\n")
	writeFile(t, dir, "web/app.ts", "export const x = 1;\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	// Delete one file from pkg/a (leaving pkg/a/a1.go intact)
	if err := os.Remove(filepath.Join(dir, "pkg", "a", "a2.go")); err != nil {
		t.Fatal(err)
	}

	cs, err := ChangedFilesDetail(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("ChangedFilesDetail failed: %v", err)
	}
	if len(cs.Changed) != 0 {
		t.Fatalf("expected no changed files, got: %v", cs.Changed)
	}
	if !reflect.DeepEqual(cs.Deleted, []string{"pkg/a/a2.go"}) {
		t.Fatalf("expected deleted [pkg/a/a2.go], got: %v", cs.Deleted)
	}

	goCheck := config.CheckConfig{
		Name:       "test",
		Command:    "go test -json {{packages}}",
		Scope:      config.ScopeChangedPackages,
		Extensions: []string{".go"},
	}

	// Partial package deletion: should NOT skip, should include ./pkg/a and transitive importers ./pkg/b, ./pkg/c
	resPartial := ResolveWithDeleted(dir, config.StackGo, goCheck, cs.Changed, cs.Deleted)
	if resPartial.Skip || resPartial.FullSuite {
		t.Fatalf("expected scoped package run for partial package deletion, got: %+v", resPartial)
	}
	wantPkgs := []string{"./pkg/a", "./pkg/b", "./pkg/c"}
	if !reflect.DeepEqual(resPartial.TargetPackages, wantPkgs) {
		t.Fatalf("target packages mismatch:\ngot:  %v\nwant: %v", resPartial.TargetPackages, wantPkgs)
	}

	// Entire package deletion (no .go files remain in pkg/only): must escalate to FullSuite (./...)
	if err := os.Remove(filepath.Join(dir, "pkg", "only", "only.go")); err != nil {
		t.Fatal(err)
	}
	resFull := ResolveWithDeleted(dir, config.StackGo, goCheck, nil, []string{"pkg/only/only.go"})
	if resFull.Skip || !resFull.FullSuite {
		t.Fatalf("expected FullSuite escalation when entire Go package deleted, got: %+v", resFull)
	}
	if resFull.Command != "go test -json ./..." {
		t.Fatalf("expected full suite command, got: %q", resFull.Command)
	}
	if resFull.FullSuiteReason != "deleted Go file: pkg/only/only.go" {
		t.Fatalf("unexpected full suite reason: %q", resFull.FullSuiteReason)
	}

	// Non-Go deleted file in ScopeChangedFiles must escalate to FullSuite rather than skip
	tsCheck := config.CheckConfig{
		Name:       "lint",
		Command:    "eslint {{files}}",
		Scope:      config.ScopeChangedFiles,
		Extensions: []string{".ts"},
	}
	resTS := ResolveWithDeleted(dir, config.StackNode, tsCheck, nil, []string{"web/app.ts"})
	if resTS.Skip || !resTS.FullSuite {
		t.Fatalf("expected FullSuite escalation for deleted .ts file, got: %+v", resTS)
	}
	if resTS.FullSuiteReason != "deleted source file: web/app.ts" {
		t.Fatalf("unexpected ts full suite reason: %q", resTS.FullSuiteReason)
	}
}
