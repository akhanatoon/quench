package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akhanatoon/quench/internal/config"
	"github.com/akhanatoon/quench/internal/diff"
	"github.com/akhanatoon/quench/internal/parser"
	"github.com/akhanatoon/quench/internal/sandbox"
)

type Options struct {
	ProjectDir string
	BaseRef    string
	OnlyChecks []string
	FailFast   bool
}

// Run executes diff-aware checks against sb in parallel while preserving configured check order.
func Run(ctx context.Context, cfg *config.Config, sb sandbox.Sandbox, opts Options) (*RunReport, error) {
	started := time.Now()
	absDir, err := filepath.Abs(opts.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve project dir: %w", err)
	}

	changed, err := diff.ChangedFiles(ctx, absDir, opts.BaseRef)
	if err != nil {
		return nil, err
	}
	if changed == nil {
		changed = []string{}
	}

	selected := filterChecks(cfg.Checks, opts.OnlyChecks)
	if len(opts.OnlyChecks) > 0 && len(selected) == 0 {
		return nil, fmt.Errorf("none of requested checks (%s) exist in configuration", strings.Join(opts.OnlyChecks, ", "))
	}

	type plannedCheck struct {
		index    int
		cfg      config.CheckConfig
		resolved diff.ResolvedCheck
	}

	plans := make([]plannedCheck, len(selected))
	results := make([]CheckResult, len(selected))
	anyActive := false
	fullSuite := false
	var scopeReason string

	for i, ch := range selected {
		res := diff.Resolve(absDir, cfg.Stack, ch, changed)
		plans[i] = plannedCheck{index: i, cfg: ch, resolved: res}
		if res.FullSuite {
			fullSuite = true
			if scopeReason == "" {
				scopeReason = res.FullSuiteReason
			}
		}
		if res.Skip {
			results[i] = CheckResult{
				Name:       ch.Name,
				Status:     StatusSkipped,
				WorkingDir: ch.WorkingDir,
				SkipReason: res.SkipReason,
			}
		} else {
			anyActive = true
		}
	}

	var totalRawBytes atomic.Int64

	if anyActive {
		if err := sb.EnsureWarm(ctx); err != nil {
			return nil, err
		}

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		var wg sync.WaitGroup
		for _, p := range plans {
			if p.resolved.Skip {
				continue
			}
			wg.Add(1)
			go func(plan plannedCheck) {
				defer wg.Done()
				cStart := time.Now()
				execRes, execErr := sb.Exec(runCtx, plan.resolved.WorkingDir, plan.resolved.Command, plan.cfg.ParsedTimeout())
				dur := time.Since(cStart).Milliseconds()
				totalRawBytes.Add(int64(len(execRes.Combined)))

				if execErr != nil {
					results[plan.index] = CheckResult{
						Name:       plan.cfg.Name,
						Status:     StatusFailed,
						WorkingDir: plan.resolved.WorkingDir,
						DurationMs: dur,
						Command:    plan.resolved.Command,
						Failures: []Failure{
							{
								Message:    fmt.Sprintf("execution error: %v", execErr),
								RawExcerpt: execErr.Error(),
							},
						},
					}
					if opts.FailFast {
						cancel()
					}
					return
				}

				if execRes.TimedOut {
					results[plan.index] = CheckResult{
						Name:       plan.cfg.Name,
						Status:     StatusFailed,
						WorkingDir: plan.resolved.WorkingDir,
						DurationMs: dur,
						Command:    plan.resolved.Command,
						Failures: []Failure{
							{
								Message:    fmt.Sprintf("check timed out after %s", plan.cfg.ParsedTimeout()),
								RawExcerpt: truncateTail(execRes.Combined, 240),
							},
						},
					}
					if opts.FailFast {
						cancel()
					}
					return
				}

				if execRes.ExitCode == 0 {
					results[plan.index] = CheckResult{
						Name:       plan.cfg.Name,
						Status:     StatusPassed,
						WorkingDir: plan.resolved.WorkingDir,
						DurationMs: dur,
					}
					return
				}

				parseRoot := absDir
				if plan.resolved.WorkingDir != "" {
					parseRoot = filepath.Join(absDir, filepath.FromSlash(plan.resolved.WorkingDir))
				}
				failures, truncated := parser.Parse(plan.cfg.Parser, plan.resolved.Command, parseRoot, execRes.Combined)
				if len(failures) == 0 {
					failures = []Failure{
						{
							Message:    fmt.Sprintf("command exited with code %d", execRes.ExitCode),
							RawExcerpt: truncateTail(execRes.Combined, 240),
						},
					}
				}
				results[plan.index] = CheckResult{
					Name:           plan.cfg.Name,
					Status:         StatusFailed,
					WorkingDir:     plan.resolved.WorkingDir,
					DurationMs:     dur,
					Command:        plan.resolved.Command,
					TruncatedCount: truncated,
					Failures:       failures,
				}
				if opts.FailFast {
					cancel()
				}
			}(p)
		}
		wg.Wait()
	}

	var summary Summary
	summary.TotalChecks = len(results)
	for _, r := range results {
		switch r.Status {
		case StatusPassed:
			summary.Passed++
		case StatusFailed:
			summary.Failed++
		case StatusSkipped:
			summary.Skipped++
		}
	}

	overallStatus := StatusPassed
	if summary.Failed > 0 {
		overallStatus = StatusFailed
	}

	report := &RunReport{
		Status:             overallStatus,
		Mode:               sb.ModeName(),
		ProjectDir:         filepath.ToSlash(absDir),
		DurationMs:         time.Since(started).Milliseconds(),
		FullSuiteTriggered: fullSuite,
		ScopeReason:        scopeReason,
		ChangedFiles:       changed,
		Summary:            summary,
		Checks:             results,
	}

	rawBytes := int(totalRawBytes.Load())
	if rawBytes > 0 {
		encoded, _ := json.Marshal(report)
		repBytes := len(encoded)
		pct := 0.0
		if rawBytes > repBytes {
			pct = math.Round((1.0-float64(repBytes)/float64(rawBytes))*1000) / 10
		}
		report.OutputStats = &OutputStats{
			RawBytes:     rawBytes,
			ReportBytes:  repBytes,
			ReductionPct: pct,
		}
	}

	return report, nil
}

func filterChecks(all []config.CheckConfig, only []string) []config.CheckConfig {
	if len(only) == 0 {
		return all
	}
	norm := make([]string, 0, len(only))
	for _, o := range only {
		for _, part := range strings.Split(o, ",") {
			t := strings.TrimSpace(part)
			if t != "" {
				norm = append(norm, strings.ToLower(t))
			}
		}
	}
	if len(norm) == 0 {
		return all
	}
	var out []config.CheckConfig
	for _, ch := range all {
		if slices.Contains(norm, strings.ToLower(ch.Name)) {
			out = append(out, ch)
		}
	}
	return out
}

func truncateTail(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return "..." + s[len(s)-maxLen+3:]
}
