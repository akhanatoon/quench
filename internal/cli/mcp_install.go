package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newMCPInstallCmd() *cobra.Command {
	var (
		client  string
		global  bool
		dryRun  bool
		binPath string
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Register quench as an MCP server for Claude Code, Codex, Cursor, or OpenCode",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}
			binary := strings.TrimSpace(binPath)
			if binary == "" {
				binary = "quench"
			}
			targetPath, preview, err := installMCPConfig(strings.ToLower(strings.TrimSpace(client)), projectDir, global, binary, dryRun)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dryRun {
				fmt.Fprintf(out, "[dry-run] Would write %s:\n%s\n", targetPath, preview)
				return nil
			}
			fmt.Fprintf(out, "Registered quench MCP server in %s\n", targetPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&client, "client", "claude", "Target client: claude, codex, cursor, or opencode")
	cmd.Flags().BoolVar(&global, "global", false, "Write to user-level global config instead of project config")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview config changes without writing to disk")
	cmd.Flags().StringVar(&binPath, "binary", "quench", "Command or absolute path to quench binary")

	return cmd
}

func installMCPConfig(client, projectDir string, global bool, binary string, dryRun bool) (string, string, error) {
	home, _ := os.UserHomeDir()

	switch client {
	case "claude":
		target := filepath.Join(projectDir, ".mcp.json")
		if global && home != "" {
			target = filepath.Join(home, ".claude.json")
		}
		return upsertJSONMCPServer(target, "mcpServers", map[string]any{
			"command": binary,
			"args":    []string{"mcp"},
		}, dryRun)

	case "cursor":
		target := filepath.Join(projectDir, ".cursor", "mcp.json")
		if global && home != "" {
			target = filepath.Join(home, ".cursor", "mcp.json")
		}
		return upsertJSONMCPServer(target, "mcpServers", map[string]any{
			"command": binary,
			"args":    []string{"mcp"},
		}, dryRun)

	case "opencode":
		target := filepath.Join(projectDir, "opencode.json")
		if global && home != "" {
			target = filepath.Join(home, ".config", "opencode", "opencode.json")
		}
		return upsertJSONMCPServer(target, "mcp", map[string]any{
			"type":    "local",
			"command": []string{binary, "mcp"},
			"enabled": true,
		}, dryRun)

	case "codex":
		if home == "" {
			return "", "", fmt.Errorf("cannot determine home directory for codex config")
		}
		target := filepath.Join(home, ".codex", "config.toml")
		return upsertCodexTOML(target, binary, dryRun)

	default:
		return "", "", fmt.Errorf("unsupported client %q (expected claude, codex, cursor, or opencode)", client)
	}
}

func upsertJSONMCPServer(targetPath, rootKey string, entry map[string]any, dryRun bool) (string, string, error) {
	doc := make(map[string]any)
	if raw, err := os.ReadFile(targetPath); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		if uErr := json.Unmarshal(raw, &doc); uErr != nil {
			return "", "", fmt.Errorf("parse existing %s: %w", targetPath, uErr)
		}
	}

	servers, ok := doc[rootKey].(map[string]any)
	if !ok || servers == nil {
		servers = make(map[string]any)
	}
	servers["quench"] = entry
	doc[rootKey] = servers

	formatted, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", "", err
	}
	formatted = append(formatted, '\n')

	if !dryRun {
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return "", "", err
		}
		if err := os.WriteFile(targetPath, formatted, 0o644); err != nil {
			return "", "", err
		}
	}
	return targetPath, string(formatted), nil
}

func upsertCodexTOML(targetPath, binary string, dryRun bool) (string, string, error) {
	existing := ""
	if raw, err := os.ReadFile(targetPath); err == nil {
		existing = string(raw)
	}

	section := fmt.Sprintf("[mcp_servers.quench]\ncommand = %q\nargs = [\"mcp\"]\n", binary)
	var updated string
	if idx := strings.Index(existing, "[mcp_servers.quench]"); idx >= 0 {
		// Replace existing [mcp_servers.quench] block up to next [section] or EOF
		rest := existing[idx+len("[mcp_servers.quench]"):]
		nextIdx := strings.Index(rest, "\n[")
		if nextIdx >= 0 {
			updated = existing[:idx] + section + rest[nextIdx+1:]
		} else {
			updated = existing[:idx] + section
		}
	} else {
		sep := ""
		if len(existing) > 0 && !strings.HasSuffix(existing, "\n\n") {
			if strings.HasSuffix(existing, "\n") {
				sep = "\n"
			} else {
				sep = "\n\n"
			}
		}
		updated = existing + sep + section
	}

	if !dryRun {
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return "", "", err
		}
		if err := os.WriteFile(targetPath, []byte(updated), 0o644); err != nil {
			return "", "", err
		}
	}
	return targetPath, updated, nil
}
