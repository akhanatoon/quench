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
		baseRef          string
		jsonOut          bool
		format           string
		forceLocal       bool
		onlyChecks       []string
		failFast         bool
		changedLinesOnly bool
		noCache          bool
		fix              bool
		compact          bool
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
				sb = sandbox.NewDockerPool(projectDir, cfg)
				sandbox.BestEffortGC()
			}

			report, err := runner.Run(context.Background(), cfg, sb, runner.Options{
				ProjectDir:       projectDir,
				BaseRef:          baseRef,
				OnlyChecks:       onlyChecks,
				FailFast:         failFast,
				ChangedLinesOnly: changedLinesOnly,
				NoCache:          noCache,
				Fix:              fix,
				Compact:          compact,
			})
			if err != nil {
				return err
			}

			outFmt := strings.ToLower(strings.TrimSpace(format))
			if jsonOut {
				outFmt = "json"
			}
			switch outFmt {
			case "json":
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if encErr := enc.Encode(report); encErr != nil {
					return encErr
				}
			case "sarif":
				sarifBytes, sErr := runner.FormatSARIF(report)
				if sErr != nil {
					return sErr
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(sarifBytes))
			case "github":
				fmt.Fprintln(cmd.OutOrStdout(), runner.FormatGitHub(report))
			case "", "text":
				printHumanReport(cmd, report)
			default:
				return fmt.Errorf("unsupported --format %q (expected text, json, sarif, or github)", format)
			}

			if report.Status == runner.StatusFailed {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&baseRef, "base", "", "Compare against git base ref (e.g. main, HEAD~1)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit token-lean JSON report (shorthand for --format json)")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, sarif, or github")
	cmd.Flags().BoolVar(&forceLocal, "local", false, "Run checks directly on host instead of Docker sandbox")
	cmd.Flags().StringSliceVar(&onlyChecks, "only", nil, "Comma-separated check names to run (e.g. lint,test)")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "Cancel remaining checks on first failure")
	cmd.Flags().BoolVar(&changedLinesOnly, "changed-lines-only", false, "Suppress non-test diagnostics on lines untouched by git diff")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "Bypass result memoization cache")
	cmd.Flags().BoolVar(&fix, "fix", false, "Run configured auto-fix commands before verification")
	cmd.Flags().BoolVar(&compact, "compact", false, "Omit skipped checks from report output")

	return cmd
}

func printHumanReport(cmd *cobra.Command, r *runner.RunReport) {
	out := cmd.OutOrStdout()
	extra := ""
	if r.Cached {
		extra += " | cached"
	}
	if r.OutputStats != nil && r.OutputStats.ReductionPct > 0 {
		extra += fmt.Sprintf(" | %.1f%% log reduction", r.OutputStats.ReductionPct)
	}
	fmt.Fprintf(out, "quench [%s] — %s in %dms (%d changed files%s)\n",
		r.Mode, strings.ToUpper(r.Status), r.DurationMs, len(r.ChangedFiles), extra)
	if r.FullSuiteTriggered && r.ScopeReason != "" {
		fmt.Fprintf(out, "  note: full suite triggered (%s)\n", r.ScopeReason)
	}
	shownSkipped := 0
	for _, ch := range r.Checks {
		fixTag := ""
		if ch.Fixed {
			fixTag = " (fixed)"
		}
		switch ch.Status {
		case runner.StatusPassed:
			fmt.Fprintf(out, "  [PASS] %-12s (%dms)%s\n", ch.Name, ch.DurationMs, fixTag)
		case runner.StatusPassedWithFiltered:
			fmt.Fprintf(out, "  [PASS*] %-11s (%dms, %d legacy lint issues outside diff filtered)%s\n", ch.Name, ch.DurationMs, ch.FilteredOut, fixTag)
		case runner.StatusSkipped:
			shownSkipped++
			fmt.Fprintf(out, "  [SKIP] %-12s (%s)\n", ch.Name, ch.SkipReason)
		case runner.StatusFailed:
			fmt.Fprintf(out, "  [FAIL] %-12s (%dms)%s\n", ch.Name, ch.DurationMs, fixTag)
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
	if omitted := r.Summary.Skipped - shownSkipped; omitted > 0 {
		fmt.Fprintf(out, "  (%d skipped checks omitted; use without --compact to show)\n", omitted)
	}
}
