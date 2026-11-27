package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolDefinitionsIncludeConfigAndTimeout(t *testing.T) {
	defs := toolDefinitions()
	var verify map[string]any
	for _, d := range defs {
		if d["name"] == "verify_changes" {
			verify = d
			break
		}
	}
	if verify == nil {
		t.Fatal("verify_changes tool definition missing")
	}
	schema := verify["inputSchema"].(map[string]any)
	props := schema["properties"].(map[string]any)
	if _, ok := props["config"]; !ok {
		t.Fatal("verify_changes schema missing 'config' property")
	}
	if _, ok := props["timeout"]; !ok {
		t.Fatal("verify_changes schema missing 'timeout' property")
	}
}

func TestVerifyChangesInvalidTimeoutReturnsToolError(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp, "0.1.4-test")

	for _, bad := range []string{"not-a-duration", "0s", "-5s"} {
		argsJSON, _ := json.Marshal(map[string]any{
			"project_dir": tmp,
			"mode":        "local",
			"timeout":     bad,
		})
		res, rpcErr := srv.dispatchTool(context.Background(), callToolParams{
			Name:      "verify_changes",
			Arguments: argsJSON,
		})
		if rpcErr != nil {
			t.Fatalf("timeout=%q unexpected rpcErr: %+v", bad, rpcErr)
		}
		if res == nil || !res.IsError {
			t.Fatalf("timeout=%q expected tool IsError=true, got %+v", bad, res)
		}
		if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "invalid timeout") {
			t.Fatalf("timeout=%q expected invalid timeout message, got %+v", bad, res.Content)
		}
	}
}

func TestVerifyChangesConfigFromGitRefAndTimeout(t *testing.T) {
	tmp := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmp
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}

	runGit("init")
	cfgYAML := `version: 1
mode: local
stack: generic
checks:
  - name: smoke
    command: "echo ok"
    scope: project
    timeout: 5s
`
	if err := os.WriteFile(filepath.Join(tmp, "quench.yaml"), []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "hello.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "initial")

	// Remove working tree quench.yaml so only HEAD:quench.yaml exists
	if err := os.Remove(filepath.Join(tmp, "quench.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "hello.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(tmp, "0.1.4-test")
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params: mustMarshalRaw(map[string]any{
			"name": "verify_changes",
			"arguments": map[string]any{
				"project_dir": tmp,
				"mode":        "local",
				"config":      "HEAD:quench.yaml",
				"timeout":     "30s",
				"no_cache":    true,
			},
		}),
	}

	var out bytes.Buffer
	line, _ := json.Marshal(req)
	if err := srv.Serve(context.Background(), bytes.NewReader(append(line, '\n')), &out); err != nil {
		t.Fatalf("Serve failed: %v", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, out.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	if isErr, _ := resMap["isError"].(bool); isErr {
		t.Fatalf("expected successful verify_changes, got error: %s", out.String())
	}
	content := resMap["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"status":"passed"`) {
		t.Fatalf("expected passed report, got: %s", text)
	}
}

func mustMarshalRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return json.RawMessage(b)
}
