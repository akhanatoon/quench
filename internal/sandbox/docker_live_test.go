package sandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akhanatoon/quench/internal/config"
	"github.com/akhanatoon/quench/internal/sandbox"
)

func requireLiveDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("QUENCH_LIVE_DOCKER") != "1" {
		t.Skip("set QUENCH_LIVE_DOCKER=1 to run live Docker tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sandbox.CheckDaemon(ctx); err != nil {
		t.Fatalf("QUENCH_LIVE_DOCKER=1 set, but Docker daemon is unavailable: %v", err)
	}
}

func TestDockerWarmSandboxGo(t *testing.T) {
	requireLiveDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/livego\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "add.go"), []byte("package livego\n\nfunc Add(a, b int) int { return a + b }\n"), 0o644); err != nil {
		t.Fatalf("write add.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "add_test.go"), []byte("package livego\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"bad sum\")\n\t}\n}\n"), 0o644); err != nil {
		t.Fatalf("write add_test.go: %v", err)
	}

	syncDeps := false
	cfg := &config.Config{
		Version: 1,
		Mode:    config.ModeDocker,
		Stack:   config.StackGo,
		Sandbox: config.SandboxConfig{
			Image:       "golang:1.24-alpine",
			IdleTimeout: "10m",
			Network:     "bridge",
			SyncDeps:    &syncDeps,
			CachePaths:  []string{"/go/pkg/mod", "/root/.cache/go-build"},
		},
	}

	sb := sandbox.NewDocker(dir, cfg)
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanCancel()
		_ = sb.Reset(cleanCtx, true)
	})

	// 1st run: creates and warms the container.
	if err := sb.EnsureWarm(ctx); err != nil {
		t.Fatalf("1st EnsureWarm failed: %v", err)
	}
	st1, err := sb.Status(ctx)
	if err != nil {
		t.Fatalf("Status after 1st EnsureWarm: %v", err)
	}
	if st1.State != "running" || st1.ContainerID == "" {
		t.Fatalf("expected running container with ID, got %+v", st1)
	}

	res1, err := sb.Exec(ctx, "", "go test ./...", 60*time.Second)
	if err != nil {
		t.Fatalf("1st Exec failed: %v", err)
	}
	if res1.ExitCode != 0 {
		t.Fatalf("expected exit code 0 on 1st go test, got %d:\n%s", res1.ExitCode, res1.Combined)
	}

	// 2nd run: reuses the exact same warm container.
	if err := sb.EnsureWarm(ctx); err != nil {
		t.Fatalf("2nd EnsureWarm failed: %v", err)
	}
	st2, err := sb.Status(ctx)
	if err != nil {
		t.Fatalf("Status after 2nd EnsureWarm: %v", err)
	}
	if st2.State != "running" || st2.ContainerID != st1.ContainerID {
		t.Fatalf("expected warm container reuse (id=%s), got %+v", st1.ContainerID, st2)
	}

	res2, err := sb.Exec(ctx, "", "go test ./...", 60*time.Second)
	if err != nil {
		t.Fatalf("2nd Exec failed: %v", err)
	}
	if res2.ExitCode != 0 {
		t.Fatalf("expected exit code 0 on 2nd go test, got %d:\n%s", res2.ExitCode, res2.Combined)
	}

	if err := sb.Reset(ctx, true); err != nil {
		t.Fatalf("Reset(true) failed: %v", err)
	}
	stAfter, err := sb.Status(ctx)
	if err != nil {
		t.Fatalf("Status after Reset: %v", err)
	}
	if stAfter.State != "absent" {
		t.Fatalf("expected absent state after Reset(true), got %+v", stAfter)
	}
}

func TestDockerWarmSandboxNode(t *testing.T) {
	requireLiveDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dir := t.TempDir()
	pkgJSON := `{"name":"live-node","version":"1.0.0","type":"module"}`
	calcJS := "export function mul(a, b) { return a * b; }\n"
	testJS := strings.Join([]string{
		`import test from "node:test";`,
		`import assert from "node:assert/strict";`,
		`import { mul } from "./calc.mjs";`,
		``,
		`test("mul multiplies numbers", () => {`,
		`  assert.equal(mul(6, 7), 42);`,
		`});`,
		``,
	}, "\n")

	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "calc.mjs"), []byte(calcJS), 0o644); err != nil {
		t.Fatalf("write calc.mjs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "calc.test.mjs"), []byte(testJS), 0o644); err != nil {
		t.Fatalf("write calc.test.mjs: %v", err)
	}

	syncDeps := false
	cfg := &config.Config{
		Version: 1,
		Mode:    config.ModeDocker,
		Stack:   config.StackNode,
		Sandbox: config.SandboxConfig{
			Image:       "node:22-alpine",
			IdleTimeout: "10m",
			Network:     "bridge",
			SyncDeps:    &syncDeps,
			CachePaths:  []string{"/root/.npm"},
		},
	}

	sb := sandbox.NewDocker(dir, cfg)
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanCancel()
		_ = sb.Reset(cleanCtx, true)
	})

	if err := sb.EnsureWarm(ctx); err != nil {
		t.Fatalf("EnsureWarm failed: %v", err)
	}
	st1, err := sb.Status(ctx)
	if err != nil {
		t.Fatalf("Status after EnsureWarm: %v", err)
	}
	if st1.State != "running" || st1.ContainerID == "" {
		t.Fatalf("expected running node container, got %+v", st1)
	}

	res, err := sb.Exec(ctx, "", "node --test", 60*time.Second)
	if err != nil {
		t.Fatalf("Exec node --test failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0 from node --test, got %d:\n%s", res.ExitCode, res.Combined)
	}

	// Verify warm reuse on 2nd EnsureWarm.
	if err := sb.EnsureWarm(ctx); err != nil {
		t.Fatalf("2nd EnsureWarm failed: %v", err)
	}
	st2, err := sb.Status(ctx)
	if err != nil {
		t.Fatalf("Status after 2nd EnsureWarm: %v", err)
	}
	if st2.ContainerID != st1.ContainerID {
		t.Fatalf("expected warm node container reuse (id=%s), got %+v", st1.ContainerID, st2)
	}

	if err := sb.Reset(ctx, true); err != nil {
		t.Fatalf("Reset(true) failed: %v", err)
	}
	stAfter, err := sb.Status(ctx)
	if err != nil {
		t.Fatalf("Status after Reset: %v", err)
	}
	if stAfter.State != "absent" {
		t.Fatalf("expected absent state after Reset(true), got %+v", stAfter)
	}
}

func TestDockerWarmSandboxPython(t *testing.T) {
	requireLiveDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dir := t.TempDir()
	calcPy := "def square(x: int) -> int:\n    return x * x\n"
	testPy := strings.Join([]string{
		"import unittest",
		"from calc import square",
		"",
		"class TestCalc(unittest.TestCase):",
		"    def test_square(self):",
		"        self.assertEqual(square(9), 81)",
		"",
		`if __name__ == "__main__":`,
		"    unittest.main()",
		"",
	}, "\n")

	if err := os.WriteFile(filepath.Join(dir, "calc.py"), []byte(calcPy), 0o644); err != nil {
		t.Fatalf("write calc.py: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test_calc.py"), []byte(testPy), 0o644); err != nil {
		t.Fatalf("write test_calc.py: %v", err)
	}

	syncDeps := false
	cfg := &config.Config{
		Version: 1,
		Mode:    config.ModeDocker,
		Stack:   config.StackPython,
		Sandbox: config.SandboxConfig{
			Image:       "python:3.12-slim",
			IdleTimeout: "10m",
			Network:     "bridge",
			SyncDeps:    &syncDeps,
			CachePaths:  []string{"/root/.cache/pip"},
		},
	}

	sb := sandbox.NewDocker(dir, cfg)
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanCancel()
		_ = sb.Reset(cleanCtx, true)
	})

	if err := sb.EnsureWarm(ctx); err != nil {
		t.Fatalf("EnsureWarm failed: %v", err)
	}
	st1, err := sb.Status(ctx)
	if err != nil {
		t.Fatalf("Status after EnsureWarm: %v", err)
	}
	if st1.State != "running" || st1.ContainerID == "" {
		t.Fatalf("expected running python container, got %+v", st1)
	}

	res, err := sb.Exec(ctx, "", "python -m unittest discover -v", 60*time.Second)
	if err != nil {
		t.Fatalf("Exec python -m unittest failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0 from python -m unittest, got %d:\n%s", res.ExitCode, res.Combined)
	}

	// Verify warm reuse on 2nd EnsureWarm.
	if err := sb.EnsureWarm(ctx); err != nil {
		t.Fatalf("2nd EnsureWarm failed: %v", err)
	}
	st2, err := sb.Status(ctx)
	if err != nil {
		t.Fatalf("Status after 2nd EnsureWarm: %v", err)
	}
	if st2.ContainerID != st1.ContainerID {
		t.Fatalf("expected warm python container reuse (id=%s), got %+v", st1.ContainerID, st2)
	}

	if err := sb.Reset(ctx, true); err != nil {
		t.Fatalf("Reset(true) failed: %v", err)
	}
	stAfter, err := sb.Status(ctx)
	if err != nil {
		t.Fatalf("Status after Reset: %v", err)
	}
	if stAfter.State != "absent" {
		t.Fatalf("expected absent state after Reset(true), got %+v", stAfter)
	}
}
