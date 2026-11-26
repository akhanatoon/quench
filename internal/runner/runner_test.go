package runner_test

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akhanatoon/quench/internal/config"
	"github.com/akhanatoon/quench/internal/runner"
	"github.com/akhanatoon/quench/internal/sandbox"
)

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copyDir(%s -> %s): %v", src, dst, err)
	}
}

func gitInitCommit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"add", "."},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
}

func touchModify(t *testing.T, filePath string) {
	t.Helper()
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndGoBrokenAndFix(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "go-broken")
	repo := t.TempDir()
	copyDir(t, src, repo)
	gitInitCommit(t, repo)

	// Touch calc/calc.go so git diff picks up ./calc
	touchModify(t, filepath.Join(repo, "calc", "calc.go"))

	cfg, _, err := config.Load(repo)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	sb := sandbox.NewLocal(repo, cfg.Stack)

	rep, err := runner.Run(context.Background(), cfg, sb, runner.Options{ProjectDir: repo})
	if err != nil {
		t.Fatalf("runner.Run: %v", err)
	}
	if rep.Status != runner.StatusFailed || rep.Summary.Failed != 1 || rep.Summary.Passed != 1 {
		t.Fatalf("expected vet pass + test fail, got: %+v", rep)
	}
	var testFailureFound bool
	for _, ch := range rep.Checks {
		if ch.Name == "test" && len(ch.Failures) == 1 {
			f := ch.Failures[0]
			if f.File == "calc/calc_test.go" && f.Symbol == "TestAdd" && strings.Contains(f.Message, "expected 5, got -1") {
				testFailureFound = true
			}
		}
	}
	if !testFailureFound {
		t.Fatalf("expected compact TestAdd failure in report: %+v", rep.Checks)
	}

	// Now fix the bug and confirm verification turns green!
	fixed := "package calc\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "calc", "calc.go"), []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	repPass, err := runner.Run(context.Background(), cfg, sb, runner.Options{ProjectDir: repo})
	if err != nil {
		t.Fatalf("runner.Run after fix: %v", err)
	}
	if repPass.Status != runner.StatusPassed || repPass.Summary.Passed != 2 {
		t.Fatalf("expected all checks passed after fix, got: %+v", repPass)
	}
}

func TestEndToEndTypeScriptBroken(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	src := filepath.Join("..", "..", "testdata", "ts-broken")
	repo := t.TempDir()
	copyDir(t, src, repo)
	gitInitCommit(t, repo)
	touchModify(t, filepath.Join(repo, "src", "price.ts"))

	cfg, _, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := runner.Run(context.Background(), cfg, sandbox.NewLocal(repo, cfg.Stack), runner.Options{ProjectDir: repo})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != runner.StatusFailed || len(rep.Checks[0].Failures) != 1 {
		t.Fatalf("expected TS failure, got: %+v", rep)
	}
	f := rep.Checks[0].Failures[0]
	if f.File != "src/price.ts" || f.Line != 2 || f.Symbol != "TS2322" {
		t.Fatalf("unexpected TS failure payload: %+v", f)
	}
}

func TestEndToEndPythonBrokenAndFix(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("python not installed")
	}
	src := filepath.Join("..", "..", "testdata", "py-broken")
	repo := t.TempDir()
	copyDir(t, src, repo)
	gitInitCommit(t, repo)
	touchModify(t, filepath.Join(repo, "app", "math_ops.py"))

	cfg, _, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := runner.Run(context.Background(), cfg, sandbox.NewLocal(repo, cfg.Stack), runner.Options{ProjectDir: repo})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != runner.StatusFailed || rep.Summary.Failed != 2 {
		t.Fatalf("expected both ruff lint and mapped pytest to fail, got: %+v", rep)
	}

	// Fix both lint (remove unused_var) and bug (a * b)
	fixedPy := "def multiply(a: int, b: int) -> int:\n    return a * b\n"
	if err := os.WriteFile(filepath.Join(repo, "app", "math_ops.py"), []byte(fixedPy), 0o644); err != nil {
		t.Fatal(err)
	}
	repPass, err := runner.Run(context.Background(), cfg, sandbox.NewLocal(repo, cfg.Stack), runner.Options{ProjectDir: repo})
	if err != nil {
		t.Fatal(err)
	}
	if repPass.Status != runner.StatusPassed || repPass.Summary.Passed != 2 {
		t.Fatalf("expected both python checks to pass after fix, got: %+v", repPass)
	}
}
