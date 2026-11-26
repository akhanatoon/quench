package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallMCPConfigDryRunAndWrite(t *testing.T) {
	dir := t.TempDir()

	// Dry-run claude should not write file
	target, preview, err := installMCPConfig("claude", dir, false, "quench", true)
	if err != nil {
		t.Fatalf("claude dry-run failed: %v", err)
	}
	if !strings.HasSuffix(target, ".mcp.json") || !strings.Contains(preview, `"quench"`) {
		t.Fatalf("unexpected claude preview: %s\n%s", target, preview)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run should not create file")
	}

	// Real write merges with existing .mcp.json without clobbering other servers
	_ = os.WriteFile(target, []byte(`{"mcpServers":{"other":{"command":"other-mcp"}}}`), 0o644)
	_, written, err := installMCPConfig("claude", dir, false, "quench", false)
	if err != nil {
		t.Fatalf("claude write failed: %v", err)
	}
	if !strings.Contains(written, `"other"`) || !strings.Contains(written, `"quench"`) {
		t.Fatalf("expected merged mcpServers, got:\n%s", written)
	}

	// Codex TOML upsert preserves existing sections
	tomlPath := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(tomlPath, []byte("model = \"gpt-5\"\n"), 0o644)
	_, tomlOut, err := upsertCodexTOML(tomlPath, "quench", false)
	if err != nil {
		t.Fatalf("codex toml upsert failed: %v", err)
	}
	if !strings.Contains(tomlOut, `model = "gpt-5"`) || !strings.Contains(tomlOut, "[mcp_servers.quench]") {
		t.Fatalf("unexpected codex toml:\n%s", tomlOut)
	}
}
