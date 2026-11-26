package sandbox

import (
	"os"
	"path/filepath"
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
