package cli

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/akhanatoon/quench/internal/mcp"
	"github.com/spf13/cobra"
)

func newMCPCmd() *cobra.Command {
	var allowDirs []string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start the stdio Model Context Protocol (MCP) server for coding agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			allowed := make([]string, 0, len(allowDirs)+1)
			allowed = append(allowed, allowDirs...)
			if envDirs := strings.TrimSpace(os.Getenv("QUENCH_MCP_ALLOW_DIRS")); envDirs != "" {
				for _, d := range filepath.SplitList(envDirs) {
					if d = strings.TrimSpace(d); d != "" {
						allowed = append(allowed, d)
					}
				}
			}

			srv := mcp.NewServerWithAllowedRoots(projectDir, Version, allowed)
			return srv.Serve(ctx, os.Stdin, os.Stdout)
		},
	}
	cmd.Flags().StringSliceVar(&allowDirs, "allow-dir", nil, "Additional trusted workspace directories allowed for MCP project_dir overrides")
	cmd.AddCommand(newMCPInstallCmd())
	return cmd
}
