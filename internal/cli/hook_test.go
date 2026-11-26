package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func initTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, string(out))
	}
	return dir
}

func TestHookInstallIdempotentAndUninstallClean(t *testing.T) {
	repo := initTempGitRepo(t)

	// 1. Fresh install creates executable hook with header + fenced block.
	opts := hookInstallOptions{FailFast: true}
	hookPath, updated, err := installHook(repo, "pre-push", opts)
	if err != nil {
		t.Fatalf("installHook failed: %v", err)
	}
	if updated {
		t.Fatalf("expected updated=false on fresh install")
	}

	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("expected hook file to exist at %s: %v", hookPath, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("expected executable hook permissions, got %o", info.Mode().Perm())
	}

	firstBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	firstContent := string(firstBytes)
	for _, want := range []string{"#!/bin/sh\nset -e\n", hookBlockStart, "quench run --fail-fast", hookBlockEnd} {
		if !strings.Contains(firstContent, want) {
			t.Fatalf("expected hook content to contain %q, got:\n%s", want, firstContent)
		}
	}

	// 2. Re-install with identical options is byte-for-byte idempotent.
	_, updated, err = installHook(repo, "pre-push", opts)
	if err != nil {
		t.Fatalf("second installHook failed: %v", err)
	}
	if !updated {
		t.Fatalf("expected updated=true on re-install")
	}
	secondBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook after re-install: %v", err)
	}
	if string(secondBytes) != firstContent {
		t.Fatalf("expected byte-for-byte idempotent re-install.\nfirst:\n%s\nsecond:\n%s", firstContent, string(secondBytes))
	}

	// 3. Re-install with changed flags replaces block in-place without duplication.
	opts2 := hookInstallOptions{Local: true, ChangedLinesOnly: true, FailFast: false}
	_, updated, err = installHook(repo, "pre-push", opts2)
	if err != nil {
		t.Fatalf("third installHook failed: %v", err)
	}
	if !updated {
		t.Fatalf("expected updated=true when replacing existing block")
	}
	thirdBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook after flag update: %v", err)
	}
	thirdContent := string(thirdBytes)
	if strings.Count(thirdContent, hookBlockStart) != 1 || strings.Count(thirdContent, hookBlockEnd) != 1 {
		t.Fatalf("expected exactly one fenced block, got:\n%s", thirdContent)
	}
	if !strings.Contains(thirdContent, "quench run --local --changed-lines-only\n") {
		t.Fatalf("expected updated flags in hook block, got:\n%s", thirdContent)
	}
	if strings.Contains(thirdContent, "--fail-fast") {
		t.Fatalf("did not expect --fail-fast when disabled, got:\n%s", thirdContent)
	}

	// 4. Uninstall removes the entire hook file cleanly when no custom content remains.
	_, removed, deletedFile, err := uninstallHook(repo, "pre-push")
	if err != nil {
		t.Fatalf("uninstallHook failed: %v", err)
	}
	if !removed || !deletedFile {
		t.Fatalf("expected removed=true, deletedFile=true; got removed=%v, deletedFile=%v", removed, deletedFile)
	}
	if _, statErr := os.Stat(hookPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected hook file %s to be deleted, stat err=%v", hookPath, statErr)
	}
}

func TestHookPreservesExistingCustomContent(t *testing.T) {
	repo := initTempGitRepo(t)
	hooksDir, err := resolveHooksDir(repo)
	if err != nil {
		t.Fatalf("resolveHooksDir failed: %v", err)
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}

	hookPath := filepath.Join(hooksDir, "pre-commit")
	custom := "#!/bin/sh\necho \"custom pre-commit check\"\n"
	if err := os.WriteFile(hookPath, []byte(custom), 0o755); err != nil {
		t.Fatalf("write custom hook: %v", err)
	}

	// Install appends managed block without modifying user lines.
	gotPath, updated, err := installHook(repo, "pre-commit", hookInstallOptions{FailFast: true, Local: true})
	if err != nil {
		t.Fatalf("installHook failed: %v", err)
	}
	if gotPath != hookPath {
		t.Fatalf("expected hook path %s, got %s", hookPath, gotPath)
	}
	if updated {
		t.Fatalf("expected updated=false when appending to custom hook")
	}

	installedBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read installed hook: %v", err)
	}
	installed := string(installedBytes)
	if !strings.Contains(installed, `echo "custom pre-commit check"`) {
		t.Fatalf("custom content lost on install:\n%s", installed)
	}
	if !strings.Contains(installed, hookBlockStart) || !strings.Contains(installed, "quench run --local --fail-fast") {
		t.Fatalf("managed block missing after install:\n%s", installed)
	}

	// Uninstall removes only the managed block and preserves custom content.
	_, removed, deletedFile, err := uninstallHook(repo, "pre-commit")
	if err != nil {
		t.Fatalf("uninstallHook failed: %v", err)
	}
	if !removed || deletedFile {
		t.Fatalf("expected removed=true, deletedFile=false; got removed=%v, deletedFile=%v", removed, deletedFile)
	}

	afterBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook after uninstall: %v", err)
	}
	after := string(afterBytes)
	if after != custom {
		t.Fatalf("expected original custom hook restored.\nwant:\n%q\ngot:\n%q", custom, after)
	}

	// Second uninstall is a clean no-op.
	_, removed, deletedFile, err = uninstallHook(repo, "pre-commit")
	if err != nil {
		t.Fatalf("second uninstallHook failed: %v", err)
	}
	if removed || deletedFile {
		t.Fatalf("expected no-op on second uninstall, got removed=%v deletedFile=%v", removed, deletedFile)
	}
}

func TestHookCmdCLIAndValidation(t *testing.T) {
	repo := initTempGitRepo(t)
	prevDir := flagDir
	flagDir = repo
	t.Cleanup(func() { flagDir = prevDir })

	// Invalid hook name should error.
	cmd := newHookCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"install", "post-merge"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected error for unsupported hook name")
	}

	// Default install (pre-push) via CLI command.
	var out bytes.Buffer
	cmd = newHookCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"install", "--local"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("hook install command failed: %v", err)
	}
	if !strings.Contains(out.String(), "Installed quench pre-push hook") {
		t.Fatalf("unexpected install output: %s", out.String())
	}

	// Default uninstall via CLI command.
	out.Reset()
	cmd = newHookCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"uninstall"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("hook uninstall command failed: %v", err)
	}
	if !strings.Contains(out.String(), "Removed quench pre-push hook file") {
		t.Fatalf("unexpected uninstall output: %s", out.String())
	}
}

func TestBenchStatsFormattingAndLocalOnlyCmd(t *testing.T) {
	minMs, avgMs, maxMs := computeBenchStats([]int64{10, 20, 31})
	if minMs != 10 || avgMs != 20 || maxMs != 31 {
		t.Fatalf("unexpected stats: min=%d avg=%d max=%d", minMs, avgMs, maxMs)
	}
	minMs, avgMs, maxMs = computeBenchStats(nil)
	if minMs != 0 || avgMs != 0 || maxMs != 0 {
		t.Fatalf("expected zeros for empty samples, got %d %d %d", minMs, avgMs, maxMs)
	}

	md := formatBenchMarkdown(BenchReport{
		ProjectDir:      "/tmp/demo-repo",
		Stack:           "go",
		Runs:            3,
		LocalOnly:       false,
		DockerAvailable: false,
		Results: []BenchModeResult{
			{Mode: "local (uncached)", Runs: 3, MinMs: 100, AvgMs: 120, MaxMs: 140, Status: "passed"},
			{Mode: "local (memo-cached)", Runs: 3, MinMs: 1, AvgMs: 2, MaxMs: 3, Status: "passed", Cached: true},
		},
	})
	for _, want := range []string{
		"### Quench Benchmark (`demo-repo` · `go` · 3 runs)",
		"| local (uncached) | 3 | 100 | 120 | 140 | passed |",
		"| local (memo-cached) | 3 | 1 | 2 | 3 | passed (cached) |",
		"Docker daemon unavailable",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("expected markdown to contain %q, got:\n%s", want, md)
		}
	}

	// Run `quench bench --local-only --runs 1 --json` on a temp git repo with a fast check.
	repo := initTempGitRepo(t)
	quenchCfg := `version: 1
mode: local
stack: generic
checks:
  - name: smoke
    command: "echo ok"
    scope: project
    timeout: 10s
`
	if err := os.WriteFile(filepath.Join(repo, ".quench.yaml"), []byte(quenchCfg), 0o644); err != nil {
		t.Fatalf("write .quench.yaml: %v", err)
	}

	prevDir := flagDir
	flagDir = repo
	t.Cleanup(func() { flagDir = prevDir })

	// Invalid --runs should fail.
	badCmd := newBenchCmd()
	badCmd.SilenceUsage = true
	badCmd.SilenceErrors = true
	badCmd.SetArgs([]string{"--runs", "0"})
	if err := badCmd.Execute(); err == nil {
		t.Fatalf("expected error for --runs 0")
	}

	var out bytes.Buffer
	benchCmd := newBenchCmd()
	benchCmd.SetOut(&out)
	benchCmd.SetArgs([]string{"--local-only", "--runs", "1", "--json"})
	if err := benchCmd.Execute(); err != nil {
		t.Fatalf("bench --local-only failed: %v", err)
	}

	var parsed BenchReport
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("decode bench JSON: %v\nraw:\n%s", err, out.String())
	}
	if len(parsed.Results) != 2 {
		t.Fatalf("expected 2 local results, got %d: %+v", len(parsed.Results), parsed.Results)
	}
	if parsed.Results[0].Mode != "local (uncached)" || parsed.Results[1].Mode != "local (memo-cached)" {
		t.Fatalf("unexpected benchmark modes: %+v", parsed.Results)
	}
	if !parsed.Results[1].Cached {
		t.Fatalf("expected memo-cached run to report Cached=true, got %+v", parsed.Results[1])
	}
}
