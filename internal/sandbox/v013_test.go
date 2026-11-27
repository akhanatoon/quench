package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/akhanatoon/quench/internal/config"
)

func TestDockerExecArgsWithEnvSorted(t *testing.T) {
	got := dockerExecArgs("quench-warm-abc12345", "go test -race ./...", map[string]string{
		"GOFLAGS":     "-count=1",
		"CGO_ENABLED": "1",
	})
	want := []string{
		"exec", "-i",
		"-e", "CGO_ENABLED=1",
		"-e", "GOFLAGS=-count=1",
		"quench-warm-abc12345", "sh", "-c", "go test -race ./...",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dockerExecArgs mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestLocalSandboxExecWithEnv(t *testing.T) {
	dir := t.TempDir()
	sb := NewLocal(dir, config.StackGo)
	cmd := "echo $QUENCH_TEST_ENV"
	if runtime.GOOS == "windows" {
		cmd = "echo %QUENCH_TEST_ENV%"
	}
	res, err := sb.ExecWithEnv(context.Background(), "", "", cmd, map[string]string{
		"QUENCH_TEST_ENV": "race_enabled_ok",
	}, 10*time.Second)
	if err != nil {
		t.Fatalf("ExecWithEnv error: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "race_enabled_ok") {
		t.Fatalf("expected QUENCH_TEST_ENV in stdout, got exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}
}

func TestFormatDockerHostWarning(t *testing.T) {
	if w := formatDockerHostWarning("", "kiri"); w != "" {
		t.Fatalf("expected empty warning when DOCKER_HOST is unset, got %q", w)
	}
	w := formatDockerHostWarning("tcp://127.0.0.1:2375", "kiri")
	if !strings.Contains(w, "DOCKER_HOST") || !strings.Contains(w, "kiri") {
		t.Fatalf("expected warning mentioning DOCKER_HOST and active context kiri, got %q", w)
	}
	wDefault := formatDockerHostWarning("unix:///tmp/custom.sock", "default")
	if !strings.Contains(wDefault, "DOCKER_HOST") {
		t.Fatalf("expected warning when DOCKER_HOST is set on default context, got %q", wDefault)
	}
}

func TestIsOrphanWorkspace(t *testing.T) {
	existing := t.TempDir()
	if isOrphanWorkspace(existing) {
		t.Fatalf("existing dir %q must not be marked orphan", existing)
	}
	missing := filepath.Join(existing, "deleted-worktree")
	if !isOrphanWorkspace(missing) {
		t.Fatalf("deleted dir %q should be marked orphan", missing)
	}
	if isOrphanWorkspace("") || isOrphanWorkspace("relative/path") {
		t.Fatal("empty or relative paths must not be marked orphan")
	}
	// Ensure a real file is also not treated as missing.
	f := filepath.Join(existing, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isOrphanWorkspace(f) {
		t.Fatal("existing file path should not be marked orphan")
	}
}
