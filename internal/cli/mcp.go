package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/akhanatoon/quench/internal/mcp"
	"github.com/spf13/cobra"
)

func newMCPCmd() *cobra.Command {
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

			srv := mcp.NewServer(projectDir, Version)
			return srv.Serve(ctx, os.Stdin, os.Stdout)
		},
	}
	cmd.AddCommand(newMCPInstallCmd())
	return cmd
}
