package runner_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akhanatoon/quench/internal/config"
	"github.com/akhanatoon/quench/internal/runner"
	"github.com/akhanatoon/quench/internal/sandbox"
)

type concurrencySandbox struct {
	cur, peak atomic.Int32
	mu        sync.Mutex
	calls     int
}

func (c *concurrencySandbox) ModeName() string                  { return "fake" }
func (c *concurrencySandbox) EnsureWarm(context.Context) error  { return nil }
func (c *concurrencySandbox) Teardown(context.Context) error    { return nil }
func (c *concurrencySandbox) Reset(context.Context, bool) error { return nil }
func (c *concurrencySandbox) Status(context.Context) (sandbox.StatusInfo, error) {
	return sandbox.StatusInfo{Mode: "fake"}, nil
}
func (c *concurrencySandbox) Exec(_ context.Context, _ string, _ string, _ time.Duration) (sandbox.ExecResult, error) {
	n := c.cur.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(40 * time.Millisecond)
	c.cur.Add(-1)
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return sandbox.ExecResult{}, nil
}

func runParallel(t *testing.T, maxParallel int) (peak int32, calls int) {
	t.Helper()
	cfg := &config.Config{Version: 1, Mode: config.ModeLocal, MaxParallel: maxParallel}
	for i := 0; i < 6; i++ {
		cfg.Checks = append(cfg.Checks, config.CheckConfig{
			Name: fmt.Sprintf("c%d", i), Command: "true", Scope: config.ScopeProject, Timeout: "10s",
		})
	}
	sb := &concurrencySandbox{}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := runner.Run(context.Background(), cfg, sb, runner.Options{ProjectDir: dir, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Passed != 6 {
		t.Fatalf("expected 6 passed, got %+v", rep.Summary)
	}
	return sb.peak.Load(), sb.calls
}

func TestMaxParallelLimitsConcurrency(t *testing.T) {
	for _, limit := range []int{1, 2, 3} {
		peak, calls := runParallel(t, limit)
		if calls != 6 {
			t.Fatalf("limit %d: expected 6 calls, got %d", limit, calls)
		}
		if int(peak) > limit {
			t.Fatalf("limit %d exceeded: peak %d", limit, peak)
		}
		if limit > 1 && int(peak) < 2 {
			t.Fatalf("limit %d: expected some concurrency, peak %d", limit, peak)
		}
	}
}

func TestMaxParallelZeroIsUnlimited(t *testing.T) {
	peak, _ := runParallel(t, 0)
	if peak < 4 {
		t.Fatalf("expected near-full concurrency with no limit, peak %d", peak)
	}
}
