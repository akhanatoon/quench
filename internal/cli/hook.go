package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const (
	hookBlockStart = "# >>> quench hook (managed)"
	hookBlockEnd   = "# <<< quench hook"
)

type hookInstallOptions struct {
	Local            bool
	ChangedLinesOnly bool
	FailFast         bool
}

func newHookCmd() *cobra.Command {
	hookCmd := &cobra.Command{
		Use:   "hook",
		Short: "Install or remove managed git hooks for quench",
	}

	var opts hookInstallOptions
	installCmd := &cobra.Command{
		Use:   "install [pre-commit|pre-push]",
		Short: "Install a managed quench git hook (default: pre-push)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hookName, err := parseHookName(args)
			if err != nil {
				return err
			}
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}
			hookPath, updated, err := installHook(projectDir, hookName, opts)
			if err != nil {
				return err
			}
			action := "Installed"
			if updated {
				action = "Updated"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s quench %s hook at %s\n", action, hookName, hookPath)
			return nil
		},
	}
	installCmd.Flags().BoolVar(&opts.Local, "local", false, "Run checks directly on host instead of Docker sandbox")
	installCmd.Flags().BoolVar(&opts.ChangedLinesOnly, "changed-lines-only", false, "Suppress non-test diagnostics on lines untouched by git diff")
	installCmd.Flags().BoolVar(&opts.FailFast, "fail-fast", true, "Cancel remaining checks on first failure")

	uninstallCmd := &cobra.Command{
		Use:   "uninstall [pre-commit|pre-push]",
		Short: "Remove the managed quench block from a git hook (default: pre-push)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hookName, err := parseHookName(args)
			if err != nil {
				return err
			}
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}
			hookPath, removed, deletedFile, err := uninstallHook(projectDir, hookName)
			if err != nil {
				return err
			}
			switch {
			case !removed:
				fmt.Fprintf(cmd.OutOrStdout(), "No managed quench block found in %s hook (%s)\n", hookName, hookPath)
			case deletedFile:
				fmt.Fprintf(cmd.OutOrStdout(), "Removed quench %s hook file %s\n", hookName, hookPath)
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "Removed quench block from %s hook at %s (preserved custom hook content)\n", hookName, hookPath)
			}
			return nil
		},
	}

	hookCmd.AddCommand(installCmd, uninstallCmd)
	return hookCmd
}

func parseHookName(args []string) (string, error) {
	hookName := "pre-push"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		hookName = strings.TrimSpace(args[0])
	}
	switch hookName {
	case "pre-commit", "pre-push":
		return hookName, nil
	default:
		return "", fmt.Errorf("unsupported git hook %q (expected pre-commit or pre-push)", hookName)
	}
}

func resolveHooksDir(projectDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-path", "hooks")
	cmd.Dir = projectDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("resolve git hooks directory: %s", msg)
	}
	hooksPath := strings.TrimSpace(stdout.String())
	if hooksPath == "" {
		return "", fmt.Errorf("git rev-parse --git-path hooks returned empty path")
	}
	hooksPath = filepath.FromSlash(hooksPath)
	if !filepath.IsAbs(hooksPath) {
		hooksPath = filepath.Join(projectDir, hooksPath)
	}
	return filepath.Clean(hooksPath), nil
}

func buildHookBlock(opts hookInstallOptions) string {
	parts := []string{"quench", "run"}
	if opts.Local {
		parts = append(parts, "--local")
	}
	if opts.ChangedLinesOnly {
		parts = append(parts, "--changed-lines-only")
	}
	if opts.FailFast {
		parts = append(parts, "--fail-fast")
	}
	return hookBlockStart + "\n" + strings.Join(parts, " ") + "\n" + hookBlockEnd
}

func installHook(projectDir string, hookName string, opts hookInstallOptions) (hookPath string, updated bool, err error) {
	hooksDir, err := resolveHooksDir(projectDir)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", false, fmt.Errorf("create hooks directory %s: %w", hooksDir, err)
	}

	hookPath = filepath.Join(hooksDir, hookName)
	var existing string
	if data, readErr := os.ReadFile(hookPath); readErr == nil {
		existing = string(data)
	} else if !os.IsNotExist(readErr) {
		return "", false, fmt.Errorf("read existing hook %s: %w", hookPath, readErr)
	}

	content, replaced := upsertHookBlock(existing, buildHookBlock(opts))
	if err := os.WriteFile(hookPath, []byte(content), 0o755); err != nil {
		return "", false, fmt.Errorf("write hook %s: %w", hookPath, err)
	}
	if err := os.Chmod(hookPath, 0o755); err != nil {
		return "", false, fmt.Errorf("chmod hook %s: %w", hookPath, err)
	}
	return hookPath, replaced, nil
}

func uninstallHook(projectDir string, hookName string) (hookPath string, removed bool, deletedFile bool, err error) {
	hooksDir, err := resolveHooksDir(projectDir)
	if err != nil {
		return "", false, false, err
	}
	hookPath = filepath.Join(hooksDir, hookName)
	data, readErr := os.ReadFile(hookPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return hookPath, false, false, nil
		}
		return "", false, false, fmt.Errorf("read hook %s: %w", hookPath, readErr)
	}

	remaining, found, shouldDelete := removeHookBlock(string(data))
	if !found {
		return hookPath, false, false, nil
	}
	if shouldDelete {
		if err := os.Remove(hookPath); err != nil {
			return "", false, false, fmt.Errorf("remove hook %s: %w", hookPath, err)
		}
		return hookPath, true, true, nil
	}
	if err := os.WriteFile(hookPath, []byte(remaining), 0o755); err != nil {
		return "", false, false, fmt.Errorf("update hook %s: %w", hookPath, err)
	}
	if err := os.Chmod(hookPath, 0o755); err != nil {
		return "", false, false, fmt.Errorf("chmod hook %s: %w", hookPath, err)
	}
	return hookPath, true, false, nil
}

func upsertHookBlock(existing string, block string) (content string, replaced bool) {
	norm := strings.ReplaceAll(existing, "\r\n", "\n")
	if strings.TrimSpace(norm) == "" {
		return "#!/bin/sh\nset -e\n\n" + block + "\n", false
	}

	lines := splitLines(norm)
	blockLines := strings.Split(block, "\n")

	if startIdx, endIdx, ok := findHookBlock(lines); ok {
		out := make([]string, 0, len(lines)-(endIdx-startIdx+1)+len(blockLines))
		out = append(out, lines[:startIdx]...)
		out = append(out, blockLines...)
		out = append(out, lines[endIdx+1:]...)
		return strings.Join(out, "\n") + "\n", true
	}

	out := make([]string, 0, len(lines)+1+len(blockLines))
	out = append(out, lines...)
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
		out = append(out, "")
	}
	out = append(out, blockLines...)
	return strings.Join(out, "\n") + "\n", false
}

func removeHookBlock(existing string) (remaining string, removed bool, shouldDelete bool) {
	norm := strings.ReplaceAll(existing, "\r\n", "\n")
	lines := splitLines(norm)
	startIdx, endIdx, ok := findHookBlock(lines)
	if !ok {
		return existing, false, false
	}

	before := append([]string(nil), lines[:startIdx]...)
	after := append([]string(nil), lines[endIdx+1:]...)

	// Trim a single blank separator line that was added immediately before the managed block.
	if len(before) > 0 && strings.TrimSpace(before[len(before)-1]) == "" {
		before = before[:len(before)-1]
	}

	out := make([]string, 0, len(before)+len(after))
	out = append(out, before...)
	out = append(out, after...)

	if isBoilerplateOnly(out) {
		return "", true, true
	}
	return strings.Join(out, "\n") + "\n", true, false
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func findHookBlock(lines []string) (startIdx int, endIdx int, ok bool) {
	startIdx = -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if startIdx == -1 {
			if trimmed == hookBlockStart {
				startIdx = i
			}
		} else if trimmed == hookBlockEnd {
			return startIdx, i, true
		}
	}
	return -1, -1, false
}

func isBoilerplateOnly(lines []string) bool {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#!") || trimmed == "set -e" {
			continue
		}
		return false
	}
	return true
}
