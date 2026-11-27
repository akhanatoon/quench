package cli

import (
	"encoding/json"
	"fmt"

	"github.com/akhanatoon/quench/internal/history"
	"github.com/spf13/cobra"
)

func newStatsCmd() *cobra.Command {
	var (
		days    int
		jsonOut bool
		reset   bool
	)

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show local verification run metrics and estimated token savings",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}
			if reset {
				if err := history.Reset(projectDir); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Cleared local quench run history.")
				return nil
			}
			if days < 0 {
				return fmt.Errorf("invalid --days %d (expected >= 0)", days)
			}

			records, err := history.Load(projectDir, days)
			if err != nil {
				return err
			}
			summary := history.Summarize(records, days)

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(summary)
			}

			out := cmd.OutOrStdout()
			windowLabel := fmt.Sprintf("last %d days", days)
			if days == 0 {
				windowLabel = "all time"
			}
			fmt.Fprintf(out, "quench stats (%s)\n", windowLabel)
			if summary.TotalRuns == 0 {
				fmt.Fprintln(out, "  No verification runs recorded yet.")
				return nil
			}
			fmt.Fprintf(out, "  Runs:          %d total (%d cli, %d mcp | %d passed, %d failed, %d cached | %.1f%% pass)\n",
				summary.TotalRuns, summary.CLIRuns, summary.MCPRuns, summary.PassedRuns, summary.FailedRuns, summary.CachedRuns, summary.PassRatePct)
			fmt.Fprintf(out, "  Duration:      %dms total (avg %dms/run)\n", summary.TotalDurationMs, summary.AvgDurationMs)
			fmt.Fprintf(out, "  Output volume: %s raw -> %s report (%.1f%% reduction, ~%d tokens saved)\n",
				formatBytes(summary.TotalRawBytes), formatBytes(summary.TotalReportBytes), summary.ReductionPct, summary.EstTokensSaved)
			fmt.Fprintf(out, "  Checks skipped:%d unrelated checks avoided\n", summary.TotalChecksSkipped)
			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 7, "Include runs from the last N days (0 = all time)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit stats summary as JSON")
	cmd.Flags().BoolVar(&reset, "reset", false, "Clear local run history for this repository")

	return cmd
}

func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
