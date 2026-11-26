package mcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/akhanatoon/quench/internal/runner"
)

func initSubprocessBrokenGoRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calcDir := filepath.Join(dir, "calc")
	if err := os.MkdirAll(calcDir, 0o755); err != nil {
		t.Fatalf("mkdir calc: %v", err)
	}

	goMod := "module example.com/mcpe2e\n\ngo 1.23\n"
	calcBroken := "package calc\n\nfunc Sub(a, b int) int { return a + b }\n"
	calcTest := strings.Join([]string{
		"package calc",
		"",
		`import "testing"`,
		"",
		"func TestSub(t *testing.T) {",
		"\tif got := Sub(10, 3); got != 7 {",
		"\t\tt.Fatalf(\"want 7, got %d\", got)",
		"\t}",
		"}",
		"",
	}, "\n")

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(calcDir, "calc.go"), []byte(calcBroken), 0o644); err != nil {
		t.Fatalf("write calc.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(calcDir, "calc_test.go"), []byte(calcTest), 0o644); err != nil {
		t.Fatalf("write calc_test.go: %v", err)
	}

	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "."}, {"commit", "-m", "init"}} {
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

	// Unstaged edit so diff-aware runner targets ./calc.
	unstaged := "package calc\n\n// unstaged edit\nfunc Sub(a, b int) int { return a + b }\n"
	if err := os.WriteFile(filepath.Join(calcDir, "calc.go"), []byte(unstaged), 0o644); err != nil {
		t.Fatalf("write unstaged calc.go: %v", err)
	}
	return dir
}

func TestMCPRealSubprocessStdio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Build the real quench binary into a temp directory.
	binName := "quench-test"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(t.TempDir(), binName)
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, "../../cmd/quench")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/quench failed: %v\n%s", err, string(out))
	}

	repo := initSubprocessBrokenGoRepo(t)

	// Spawn the compiled binary as a real OS subprocess over stdin/stdout pipes.
	proc := exec.CommandContext(ctx, binPath, "mcp", "--allow-dir", repo)
	stdin, err := proc.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := proc.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	proc.Stderr = &stderr

	if err := proc.Start(); err != nil {
		t.Fatalf("start quench mcp subprocess: %v", err)
	}

	reader := bufio.NewReader(stdout)

	writeFramed := func(msg map[string]any, delim string) {
		t.Helper()
		raw, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		if _, err := io.WriteString(stdin, string(raw)+delim); err != nil {
			t.Fatalf("write request to subprocess stdin: %v (stderr=%q)", err, stderr.String())
		}
	}

	readJSONLine := func() map[string]any {
		t.Helper()
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read response from subprocess stdout: %v (stderr=%q)", err, stderr.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
			t.Fatalf("stdout emitted non-JSON line %q: %v (stderr=%q)", string(line), err, stderr.String())
		}
		return resp
	}

	// 1. initialize over Windows CRLF (\r\n)
	writeFramed(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"protocolVersion": "2025-03-26"},
	}, "\r\n")

	initResp := readJSONLine()
	if gotID, ok := initResp["id"].(float64); !ok || gotID != 1 {
		t.Fatalf("initialize: expected id=1, got %+v", initResp)
	}
	initResult, ok := initResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize: expected result object, got %+v", initResp)
	}
	if initResult["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize: expected protocolVersion 2025-03-26, got %+v", initResult["protocolVersion"])
	}
	serverInfo, _ := initResult["serverInfo"].(map[string]any)
	if serverInfo["name"] != "quench" {
		t.Fatalf("initialize: expected serverInfo.name=quench, got %+v", serverInfo)
	}

	// 2. notifications/initialized over CRLF (must NOT produce any response)
	writeFramed(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}, "\r\n")

	// 3. tools/list over Unix LF (\n)
	// Receiving id=2 as the very next response also proves notifications/initialized emitted no stray reply.
	writeFramed(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	}, "\n")

	listResp := readJSONLine()
	if gotID, ok := listResp["id"].(float64); !ok || gotID != 2 {
		t.Fatalf("tools/list: expected id=2 (no stray notification response), got %+v", listResp)
	}
	listResult, ok := listResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list: expected result object, got %+v", listResp)
	}
	rawTools, ok := listResult["tools"].([]any)
	if !ok || len(rawTools) != 3 {
		t.Fatalf("tools/list: expected 3 tools, got %+v", listResult["tools"])
	}
	expectedTools := map[string]bool{
		"verify_changes": false,
		"sandbox_status": false,
		"sandbox_reset":  false,
	}
	for _, item := range rawTools {
		toolMap, _ := item.(map[string]any)
		name, _ := toolMap["name"].(string)
		if _, known := expectedTools[name]; !known {
			t.Fatalf("tools/list: unexpected tool %q", name)
		}
		expectedTools[name] = true
	}
	for name, seen := range expectedTools {
		if !seen {
			t.Fatalf("tools/list: missing expected tool %q", name)
		}
	}

	// 4. tools/call verify_changes (broken repo) over CRLF
	writeFramed(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "verify_changes",
			"arguments": map[string]any{
				"project_dir": repo,
				"mode":        "local",
			},
		},
	}, "\r\n")

	failResp := readJSONLine()
	if gotID, ok := failResp["id"].(float64); !ok || gotID != 3 {
		t.Fatalf("verify_changes (broken): expected id=3, got %+v", failResp)
	}
	failToolRes, ok := failResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("verify_changes (broken): expected result object, got %+v", failResp)
	}
	if isErr, _ := failToolRes["isError"].(bool); isErr {
		t.Fatalf("verify_changes (broken): unexpected tool error: %+v", failToolRes)
	}
	failContent, _ := failToolRes["content"].([]any)
	if len(failContent) == 0 {
		t.Fatalf("verify_changes (broken): empty content: %+v", failToolRes)
	}
	failText, _ := failContent[0].(map[string]any)["text"].(string)

	var failReport runner.RunReport
	if err := json.Unmarshal([]byte(failText), &failReport); err != nil {
		t.Fatalf("unmarshal failed RunReport: %v\nraw=%s", err, failText)
	}
	if failReport.Status != runner.StatusFailed {
		t.Fatalf("expected status=%q, got %q (report=%+v)", runner.StatusFailed, failReport.Status, failReport)
	}

	foundExactFailure := false
	for _, ch := range failReport.Checks {
		if ch.Name != "test" || ch.Status != runner.StatusFailed {
			continue
		}
		for _, f := range ch.Failures {
			if f.File == "calc/calc_test.go" && f.Line == 7 && f.Symbol == "TestSub" {
				foundExactFailure = true
				break
			}
		}
	}
	if !foundExactFailure {
		t.Fatalf("expected failure at calc/calc_test.go:7 symbol TestSub, got checks=%+v", failReport.Checks)
	}

	// 5. Fix calc/calc.go and call verify_changes again over LF
	fixedCalc := "package calc\n\nfunc Sub(a, b int) int { return a - b }\n"
	if err := os.WriteFile(filepath.Join(repo, "calc", "calc.go"), []byte(fixedCalc), 0o644); err != nil {
		t.Fatalf("write fixed calc.go: %v", err)
	}

	writeFramed(map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "verify_changes",
			"arguments": map[string]any{
				"project_dir": repo,
				"mode":        "local",
			},
		},
	}, "\n")

	passResp := readJSONLine()
	if gotID, ok := passResp["id"].(float64); !ok || gotID != 4 {
		t.Fatalf("verify_changes (fixed): expected id=4, got %+v", passResp)
	}
	passToolRes, ok := passResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("verify_changes (fixed): expected result object, got %+v", passResp)
	}
	if isErr, _ := passToolRes["isError"].(bool); isErr {
		t.Fatalf("verify_changes (fixed): unexpected tool error: %+v", passToolRes)
	}
	passContent, _ := passToolRes["content"].([]any)
	if len(passContent) == 0 {
		t.Fatalf("verify_changes (fixed): empty content: %+v", passToolRes)
	}
	passText, _ := passContent[0].(map[string]any)["text"].(string)

	var passReport runner.RunReport
	if err := json.Unmarshal([]byte(passText), &passReport); err != nil {
		t.Fatalf("unmarshal passed RunReport: %v\nraw=%s", err, passText)
	}
	if passReport.Status != runner.StatusPassed {
		t.Fatalf("expected status=%q after fix, got %q (report=%+v)", runner.StatusPassed, passReport.Status, passReport)
	}
	if passReport.OutputStats == nil {
		t.Fatalf("expected output_stats to be populated on passed report, got nil (raw=%s)", passText)
	}
	if passReport.OutputStats.RawBytes <= 0 || passReport.OutputStats.ReportBytes <= 0 {
		t.Fatalf("expected positive output_stats byte counts, got %+v", passReport.OutputStats)
	}

	// Close stdin and verify ZERO stray output on stdout and clean exit code 0.
	if err := stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	trailing, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read trailing stdout: %v", err)
	}
	if len(trailing) != 0 {
		t.Fatalf("expected zero stray bytes on stdout after EOF, got %q", string(trailing))
	}

	if err := proc.Wait(); err != nil {
		t.Fatalf("expected clean exit code 0, got error: %v (stderr=%q)", err, stderr.String())
	}
}
