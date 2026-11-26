package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/akhanatoon/quench/internal/mcp"
	"github.com/akhanatoon/quench/internal/runner"
)

func initBrokenGoRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "calc"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/mcpdemo\n\ngo 1.23\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "calc", "calc.go"), []byte("package calc\n\nfunc Sub(a, b int) int { return a + b }\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "calc", "calc_test.go"), []byte("package calc\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {\n\tif got := Sub(10, 3); got != 7 { t.Fatalf(\"want 7, got %d\", got) }\n}\n"), 0o644)

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

	// Modify calc.go in working tree so diff picks it up
	_ = os.WriteFile(filepath.Join(dir, "calc", "calc.go"), []byte("package calc\n\n// modified\nfunc Sub(a, b int) int { return a + b }\n"), 0o644)
	return dir
}

func TestMCPStdioVerifyAndFixFlow(t *testing.T) {
	repo := initBrokenGoRepo(t)

	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := mcp.NewServer(".", "0.1.0")
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx, serverIn, serverOut)
	}()

	reader := bufio.NewReader(clientIn)
	send := func(msg map[string]any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(msg)
		if _, err := clientOut.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var resp map[string]any
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("unmarshal response %q: %v", string(line), err)
		}
		return resp
	}

	// 1. initialize
	initResp := send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"protocolVersion": "2025-03-26"},
	})
	resMap := initResp["result"].(map[string]any)
	if resMap["protocolVersion"] != "2025-03-26" {
		t.Fatalf("expected echoed protocolVersion, got %+v", resMap)
	}

	// 2. notifications/initialized (no response expected)
	notifRaw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	_, _ = clientOut.Write(append(notifRaw, '\n'))

	// 3. tools/list
	listResp := send(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	tools := listResp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("expected 3 MCP tools, got %d", len(tools))
	}

	// 4. tools/call verify_changes -> detect failure via project_dir override
	callFail := send(map[string]any{
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
	})
	content := callFail["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var failReport runner.RunReport
	if err := json.Unmarshal([]byte(content), &failReport); err != nil {
		t.Fatalf("unmarshal RunReport: %v", err)
	}
	if failReport.Status != runner.StatusFailed || failReport.Summary.Failed != 1 {
		t.Fatalf("expected failed report, got %+v", failReport)
	}

	// 5. Agent fixes bug based on MCP report and re-verifies!
	_ = os.WriteFile(filepath.Join(repo, "calc", "calc.go"), []byte("package calc\n\nfunc Sub(a, b int) int { return a - b }\n"), 0o644)
	callPass := send(map[string]any{
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
	})
	passText := callPass["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var passReport runner.RunReport
	_ = json.Unmarshal([]byte(passText), &passReport)
	if passReport.Status != runner.StatusPassed || passReport.Summary.Passed != 2 {
		t.Fatalf("expected passed report after fix, got %+v", passReport)
	}

	_ = clientOut.Close()
	if err := <-errCh; err != nil {
		t.Fatalf("server exited with error: %v", err)
	}
}
