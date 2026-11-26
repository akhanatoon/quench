package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/akhanatoon/quench/internal/config"
	"github.com/akhanatoon/quench/internal/sandbox"
	"github.com/spf13/cobra"
)

func newWarmCmd() *cobra.Command {
	warm := &cobra.Command{
		Use:   "warm",
		Short: "Manage warm Docker sandboxes (up, status, down, gc)",
	}

	var jsonOut bool
	warm.PersistentFlags().BoolVar(&jsonOut, "json", false, "Output status as JSON")

	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Start and pre-warm the project's Docker sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}
			cfg, _, err := config.Load(projectDir)
			if err != nil {
				return err
			}
			sb := sandbox.NewDockerPool(projectDir, cfg)
			if err := sb.EnsureWarm(context.Background()); err != nil {
				return err
			}
			st, _ := sb.Status(context.Background())
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Warm sandbox ready: %s (%s, state=%s)\n", st.ContainerName, st.Image, st.State)
			return nil
		},
	}

	var statusAll bool
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Inspect warm sandbox state, image, uptime, and cache volumes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if statusAll {
				containers, err := sandbox.ListManagedContainers(context.Background())
				if err != nil {
					return err
				}
				if jsonOut {
					if containers == nil {
						containers = []sandbox.StatusInfo{}
					}
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					return enc.Encode(containers)
				}
				if len(containers) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No quench-managed warm containers found.")
					return nil
				}
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tSTATE\tSTACK\tIMAGE\tPROJECT DIR")
				for _, c := range containers {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
						c.ContainerName, c.State, c.Stack, c.Image, c.ProjectDir)
				}
				return tw.Flush()
			}
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}
			cfg, _, err := config.Load(projectDir)
			if err != nil {
				return err
			}
			sb := sandbox.NewDockerPool(projectDir, cfg)
			st, err := sb.Status(context.Background())
			if err != nil && !jsonOut {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Container : %s\nState     : %s\nImage     : %s\nStack     : %s\nUptime    : %s\nSynced    : %v\n",
				st.ContainerName, st.State, st.Image, st.Stack, st.Uptime, st.DepsSynced)
			if allSt, allErr := sb.AllStatuses(context.Background()); allErr == nil && len(allSt) > 1 {
				for _, sec := range allSt[1:] {
					fmt.Fprintf(cmd.OutOrStdout(), "Secondary : %s (%s, state=%s)\n", sec.ContainerName, sec.Image, sec.State)
				}
			}
			return nil
		},
	}
	statusCmd.Flags().BoolVarP(&statusAll, "all", "a", false, "List all quench-managed warm containers across all repositories")

	var purgeCache bool
	downCmd := &cobra.Command{
		Use:   "down",
		Short: "Stop and remove the warm sandbox container",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}
			cfg, _, err := config.Load(projectDir)
			if err != nil {
				return err
			}
			sb := sandbox.NewDockerPool(projectDir, cfg)
			names := strings.Join(sb.ContainerNames(), ", ")
			if purgeCache {
				if err := sb.Reset(context.Background(), true); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Removed warm container(s) %s and purged cache volumes.\n", names)
				return nil
			}
			if err := sb.Teardown(context.Background()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed warm container(s) %s.\n", names)
			return nil
		},
	}
	downCmd.Flags().BoolVar(&purgeCache, "purge-cache", false, "Also delete named dependency cache volumes")

	var gcAll bool
	gcCmd := &cobra.Command{
		Use:   "gc",
		Short: "Remove idle warm containers exceeding their idle_timeout",
		RunE: func(cmd *cobra.Command, args []string) error {
			removed, err := sandbox.GarbageCollect(context.Background(), gcAll)
			if err != nil {
				return err
			}
			if len(removed) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No idle warm containers to clean up.")
				return nil
			}
			for _, name := range removed {
				fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", name)
			}
			return nil
		},
	}
	gcCmd.Flags().BoolVar(&gcAll, "all", false, "Remove all quench warm containers regardless of idle time")

	warm.AddCommand(upCmd, statusCmd, downCmd, gcCmd)
	return warm
}
