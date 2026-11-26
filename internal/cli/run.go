package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/akhanatoon/quench/internal/config"
	"github.com/akhanatoon/quench/internal/runner"
	"github.com/akhanatoon/quench/internal/sandbox"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var (
		baseRef    string
		jsonOut    bool
		forceLocal bool
		onlyChecks []string
		failFast   bool
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run diff-aware verification checks in a warm sandbox or locally",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}

			cfg, _, err := config.Load(projectDir)
			if err != nil {
				return err
			}

			mode := cfg.Mode
			if forceLocal {
				mode = config.ModeLocal
			}

			var sb sandbox.Sandbox
			if mode == config.ModeLocal {
				sb = sandbox.NewLocal(projectDir, cfg.Stack)
			} else {
				sb = sandbox.NewDocker(projectDir, cfg)
			}

			report, err := runner.Run(context.Background(), cfg, sb, runner.Options{
				ProjectDir: projectDir,
				BaseRef:    baseRef,
				OnlyChecks: onlyChecks,
				FailFast:   failFast,
			})
			if err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if encErr := enc.Encode(report); encErr != nil {
					return encErr
				}
			} else {
				printHumanReport(cmd, report)
			}

			if report.Status == runner.StatusFailed {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&baseRef, "base", "", "Compare against git base ref (e.g. main, HEAD~1)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit token-lean JSON report")
	cmd.Flags().BoolVar(&forceLocal, "local", false, "Run checks directly on host instead of Docker sandbox")
	cmd.Flags().StringSliceVar(&onlyChecks, "only", nil, "Comma-separated check names to run (e.g. lint,test)")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "Cancel remaining checks on first failure")

	return cmd
}

func printHumanReport(cmd *cobra.Command, r *runner.RunReport) {
	out := cmd.OutOrStdout()
	extra := ""
	if r.OutputStats != nil && r.OutputStats.ReductionPct > 0 {
		extra = fmt.Sprintf(" | %.1f%% log reduction", r.OutputStats.ReductionPct)
	}
	fmt.Fprintf(out, "quench [%s] — %s in %dms (%d changed files%s)\n",
		r.Mode, strings.ToUpper(r.Status), r.DurationMs, len(r.ChangedFiles), extra)
	if r.FullSuiteTriggered && r.ScopeReason != "" {
		fmt.Fprintf(out, "  note: full suite triggered (%s)\n", r.ScopeReason)
	}
	for _, ch := range r.Checks {
		switch ch.Status {
		case runner.StatusPassed:
			fmt.Fprintf(out, "  [PASS] %-12s (%dms)\n", ch.Name, ch.DurationMs)
		case runner.StatusSkipped:
			fmt.Fprintf(out, "  [SKIP] %-12s (%s)\n", ch.Name, ch.SkipReason)
		case runner.StatusFailed:
			fmt.Fprintf(out, "  [FAIL] %-12s (%dms)\n", ch.Name, ch.DurationMs)
			for _, f := range ch.Failures {
				loc := f.File
				if loc != "" && f.Line > 0 {
					loc = fmt.Sprintf("%s:%d", f.File, f.Line)
				}
				if loc != "" && f.Symbol != "" {
					fmt.Fprintf(out, "         • %s (%s): %s\n", loc, f.Symbol, f.Message)
				} else if loc != "" {
					fmt.Fprintf(out, "         • %s: %s\n", loc, f.Message)
				} else {
					fmt.Fprintf(out, "         • %s\n", f.Message)
				}
			}
			if ch.TruncatedCount > 0 {
				fmt.Fprintf(out, "         … +%d more failures truncated\n", ch.TruncatedCount)
			}
		}
	}
}
