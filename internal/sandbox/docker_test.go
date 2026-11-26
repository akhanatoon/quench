package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akhanatoon/quench/internal/config"
)

func TestProjectHashAndLockfileHashAndVolumes(t *testing.T) {
	dir := t.TempDir()
	h1 := ProjectHash8(dir)
	h2 := ProjectHash8(dir)
	if len(h1) != 8 || h1 != h2 {
		t.Fatalf("expected deterministic 8-char hash, got %q vs %q", h1, h2)
	}

	if got := ComputeLockfileHash(dir); got != "" {
		t.Fatalf("expected empty lockfile hash when no lockfiles exist, got %q", got)
	}

	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.24\n"), 0o644)
	lh1 := ComputeLockfileHash(dir)
	_ = os.WriteFile(filepath.Join(dir, "go.sum"), []byte("example.com/pkg v1.0.0 h1:abc=\n"), 0o644)
	lh2 := ComputeLockfileHash(dir)
	if lh1 == "" || lh2 == "" || lh1 == lh2 {
		t.Fatalf("expected lockfile hash to change when go.sum changes: %q vs %q", lh1, lh2)
	}

	cfg := &config.Config{
		Stack: config.StackGo,
		Sandbox: config.SandboxConfig{
			Image:      "golang:1.24-alpine",
			CachePaths: []string{"/go/pkg/mod", "/root/.cache/go-build"},
		},
	}
	ds := NewDocker(dir, cfg)
	if ds.ContainerName() != ContainerPrefix+h1 {
		t.Fatalf("unexpected container name: %s", ds.ContainerName())
	}
	vol := ds.cacheVolumeName("/go/pkg/mod")
	if vol != VolumePrefix+h1+"-go-pkg-mod" {
		t.Fatalf("unexpected volume name: %s", vol)
	}
}

func TestDockerPoolContainerAndVolumeIsolation(t *testing.T) {
	dir := t.TempDir()
	h := ProjectHash8(dir)

	cfg := &config.Config{
		Stack: config.StackPolyglot,
		Sandbox: config.SandboxConfig{
			Image:      "python:3.12-slim",
			CachePaths: []string{"/root/.cache/pip", "/root/.npm"},
		},
		Checks: []config.CheckConfig{
			{Name: "backend-lint", Command: "ruff check .", Scope: config.ScopeProject, Stack: config.StackPython, Image: "python:3.12-slim"},
			{Name: "frontend-typecheck", Command: "npx tsc --noEmit", Scope: config.ScopeProject, Stack: config.StackNode, Image: "node:22-alpine"},
			{Name: "engine-check", Command: "cargo check", Scope: config.ScopeProject, Stack: config.StackRust, Image: "rust:1.85-slim"},
		},
	}

	pool := NewDockerPool(dir, cfg)

	// Empty image and primary image must resolve to the primary container
	primaryEmpty := pool.SandboxForImage("")
	primaryExplicit := pool.SandboxForImage("python:3.12-slim")
	if primaryEmpty != primaryExplicit {
		t.Fatalf("expected empty image and primary image to return the same sandbox instance")
	}
	if primaryEmpty.ContainerName() != ContainerPrefix+h {
		t.Fatalf("expected primary container %s, got %s", ContainerPrefix+h, primaryEmpty.ContainerName())
	}
	if got := primaryEmpty.cacheVolumeName("/root/.cache/pip"); got != VolumePrefix+h+"-root-cache-pip" {
		t.Fatalf("unexpected primary cache volume name: %s", got)
	}

	// Secondary image gets deterministic 4-char hash suffix and isolated cache volumes
	nodeSB := pool.SandboxForImage("node:22-alpine")
	nodeHash := ImageHash4("node:22-alpine")
	if len(nodeHash) != 4 || nodeHash != ImageHash4("NODE:22-ALPINE") {
		t.Fatalf("expected deterministic 4-char image hash, got %q", nodeHash)
	}
	wantNodeContainer := ContainerPrefix + h + "-" + nodeHash
	if nodeSB.ContainerName() != wantNodeContainer {
		t.Fatalf("expected secondary container %s, got %s", wantNodeContainer, nodeSB.ContainerName())
	}
	if nodeSB.configuredImage() != "node:22-alpine" {
		t.Fatalf("expected secondary image node:22-alpine, got %s", nodeSB.configuredImage())
	}
	wantNodeVol := VolumePrefix + h + "-" + nodeHash + "-root-npm"
	if got := nodeSB.cacheVolumeName("/root/.npm"); got != wantNodeVol {
		t.Fatalf("expected isolated secondary cache volume %s, got %s", wantNodeVol, got)
	}
	if primaryEmpty.cacheVolumeName("/root/.npm") == nodeSB.cacheVolumeName("/root/.npm") {
		t.Fatalf("primary and secondary cache volume names must not collide")
	}

	// Same secondary image returns cached instance
	if pool.SandboxForImage("node:22-alpine") != nodeSB {
		t.Fatalf("expected SandboxForImage to reuse existing secondary sandbox instance")
	}

	// AllSandboxes includes primary + all distinct secondary images from cfg.Checks
	all := pool.AllSandboxes()
	if len(all) != 3 {
		t.Fatalf("expected 3 sandboxes in pool (python primary + node + rust), got %d", len(all))
	}
	names := pool.ContainerNames()
	if len(names) != 3 || names[0] != ContainerPrefix+h || names[1] != wantNodeContainer {
		t.Fatalf("unexpected pool container names: %+v", names)
	}
}

func TestDefaultSetupCommandPython(t *testing.T) {
	t.Run("pyproject only", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0o644)
		cmd := defaultSetupCommand(dir, config.StackPython)
		want := `pip install -q --disable-pip-version-check -e ".[dev]" 2>/dev/null || pip install -q --disable-pip-version-check -e . 2>/dev/null || true`
		if cmd != want {
			t.Fatalf("got %q, want %q", cmd, want)
		}
	})

	t.Run("requirements only", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("pytest\n"), 0o644)
		cmd := defaultSetupCommand(dir, config.StackPython)
		want := "pip install -q --disable-pip-version-check -r requirements.txt"
		if cmd != want {
			t.Fatalf("got %q, want %q", cmd, want)
		}
	})

	t.Run("requirements and pyproject runs requirements first", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("pytest\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0o644)
		cmd := defaultSetupCommand(dir, config.StackPython)
		if !strings.HasPrefix(cmd, "pip install -q --disable-pip-version-check -r requirements.txt && ") {
			t.Fatalf("expected requirements.txt first, got %q", cmd)
		}
		if !strings.Contains(cmd, `-e ".[dev]"`) {
			t.Fatalf("expected editable dev install fallback, got %q", cmd)
		}
	})
}

func TestResourceArgs(t *testing.T) {
	if got := resourceArgs(config.SandboxConfig{}); len(got) != 0 {
		t.Fatalf("expected no flags, got %v", got)
	}
	got := strings.Join(resourceArgs(config.SandboxConfig{Memory: "1g"}), " ")
	if got != "--memory 1g --memory-swap 1g" {
		t.Fatalf("memory only: %q", got)
	}
	got = strings.Join(resourceArgs(config.SandboxConfig{CPUs: "2"}), " ")
	if got != "--cpus 2" {
		t.Fatalf("cpus only: %q", got)
	}
	got = strings.Join(resourceArgs(config.SandboxConfig{Memory: "512m", CPUs: "0.5"}), " ")
	if got != "--memory 512m --memory-swap 512m --cpus 0.5" {
		t.Fatalf("both: %q", got)
	}
}
