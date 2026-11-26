package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
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

func TestSecurityArgs(t *testing.T) {
	got := strings.Join(securityArgs(config.SandboxConfig{}), " ")
	want := "--security-opt no-new-privileges:true --cap-drop ALL"
	if got != want {
		t.Fatalf("default securityArgs = %q, want %q", got, want)
	}

	got = strings.Join(securityArgs(config.SandboxConfig{User: "1000:1000"}), " ")
	want = "--security-opt no-new-privileges:true --cap-drop ALL --user 1000:1000"
	if got != want {
		t.Fatalf("non-root securityArgs = %q, want %q", got, want)
	}

	got = strings.Join(securityArgs(config.SandboxConfig{User: "   "}), " ")
	want = "--security-opt no-new-privileges:true --cap-drop ALL"
	if got != want {
		t.Fatalf("blank user securityArgs = %q, want %q", got, want)
	}
}

func TestRemoteDockerHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"", false},
		{"unix:///var/run/docker.sock", false},
		{"npipe:////./pipe/docker_engine", false},
		{"tcp://localhost:2375", false},
		{"tcp://127.0.0.1:2375", false},
		{"tcp://[::1]:2375", false},
		{"https://127.0.0.1:2376", false},
		{"ssh://kiri", true},
		{"ssh://user@192.168.1.50", true},
		{"SSH://build-box", true},
		{"tcp://10.0.0.5:2375", true},
		{"https://docker.example.com:2376", true},
	}
	for _, tc := range cases {
		if got := isRemoteDockerHost(tc.host); got != tc.want {
			t.Errorf("isRemoteDockerHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestRemoteDockerContextEnv(t *testing.T) {
	t.Setenv("DOCKER_HOST", "ssh://kiri")
	if !IsRemoteDockerContext(context.Background()) {
		t.Fatalf("expected IsRemoteDockerContext=true for DOCKER_HOST=ssh://kiri")
	}

	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	if IsRemoteDockerContext(context.Background()) {
		t.Fatalf("expected IsRemoteDockerContext=false for unix socket")
	}

	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	if IsRemoteDockerContext(context.Background()) {
		t.Fatalf("expected IsRemoteDockerContext=false for local tcp loopback")
	}
}

func TestRemoteCopyModeContainerArgs(t *testing.T) {
	dir := t.TempDir()
	h := ProjectHash8(dir)
	hostPath := filepath.ToSlash(dir)

	cfg := &config.Config{
		Stack: config.StackGo,
		Sandbox: config.SandboxConfig{
			Image:         "golang:1.24-alpine",
			WorkspaceMode: config.WorkspaceModeCopy,
			User:          "1000:1000",
			CachePaths:    []string{"/go/pkg/mod"},
		},
	}
	ds := NewDocker(dir, cfg)

	copyArgs := ds.buildRunArgs(config.WorkspaceModeCopy)
	joinedCopy := strings.Join(copyArgs, " ")

	// Must NOT contain host bind mount for /workspace
	if strings.Contains(joinedCopy, hostPath+":/workspace") {
		t.Fatalf("copy mode must not bind-mount host path, got args: %s", joinedCopy)
	}
	// Must contain named workspace volume quench-ws-<hash8>:/workspace
	wantVolMount := "-v " + WorkspaceVolumePrefix + h + ":/workspace"
	if !strings.Contains(joinedCopy, wantVolMount) {
		t.Fatalf("copy mode missing workspace volume mount %q in: %s", wantVolMount, joinedCopy)
	}
	if !strings.Contains(joinedCopy, "--security-opt no-new-privileges:true") ||
		!strings.Contains(joinedCopy, "--cap-drop ALL") ||
		!strings.Contains(joinedCopy, "--user 1000:1000") {
		t.Fatalf("copy mode missing security flags in: %s", joinedCopy)
	}

	// Bind mode still mounts host path
	bindArgs := ds.buildRunArgs(config.WorkspaceModeBind)
	joinedBind := strings.Join(bindArgs, " ")
	if !strings.Contains(joinedBind, "-v "+hostPath+":/workspace") {
		t.Fatalf("bind mode missing host bind mount in: %s", joinedBind)
	}
}

func TestRemoteCopyTarSkipsBuildDirsAndPreservesCachePaths(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	for _, skip := range []string{".git", "node_modules", "target", "dist", ".venv", "__pycache__"} {
		_ = os.MkdirAll(filepath.Join(dir, skip), 0o755)
		_ = os.WriteFile(filepath.Join(dir, skip, "ignored.txt"), []byte("nope"), 0o644)
	}

	var buf bytes.Buffer
	if err := writeWorkspaceTar(dir, &buf); err != nil {
		t.Fatalf("writeWorkspaceTar failed: %v", err)
	}

	tr := tar.NewReader(&buf)
	found := make(map[string]bool)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}
		found[hdr.Name] = true
	}
	if !found["main.go"] {
		t.Fatalf("expected main.go in tar archive, got: %+v", found)
	}
	for name := range found {
		for skip := range copySkipDirs {
			if name == skip || strings.HasPrefix(name, skip+"/") {
				t.Fatalf("expected %q to be skipped from tar archive", name)
			}
		}
	}

	cleanCmd := workspaceCopyCleanCmd([]string{"/root/.npm", "/workspace/node_modules"})
	if !strings.Contains(cleanCmd, "'/workspace/node_modules'") {
		t.Fatalf("expected clean command to preserve /workspace/node_modules, got: %s", cleanCmd)
	}
	if !strings.Contains(cleanCmd, "tar -xf - -C /workspace") {
		t.Fatalf("expected clean command to extract tar into /workspace, got: %s", cleanCmd)
	}
}
