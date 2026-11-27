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
	ProjectDir       string
	BaseRef          string
	OnlyChecks       []string
	FailFast         bool
	ChangedLinesOnly bool
	NoCache          bool
	Fix              bool
	Compact          bool
}

// Run executes diff-aware checks against sb in parallel while preserving configured check order.
func Run(ctx context.Context, cfg *config.Config, sb sandbox.Sandbox, opts Options) (*RunReport, error) {
	started := time.Now()
	absDir, err := filepath.Abs(opts.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve project dir: %w", err)
	}

	changeSet, err := diff.ChangedFilesDetail(ctx, absDir, opts.BaseRef)
	if err != nil {
		return nil, err
	}
	changed := changeSet.Changed
	if changed == nil {
		changed = []string{}
	}
	deleted := changeSet.Deleted

	selected := filterChecks(cfg.Checks, opts.OnlyChecks)
	if len(opts.OnlyChecks) > 0 && len(selected) == 0 {
		return nil, fmt.Errorf("none of requested checks (%s) exist in configuration", strings.Join(opts.OnlyChecks, ", "))
	}

	var memoKey string
	if !opts.NoCache && !opts.Fix {
		checkSpecs := make([]string, len(selected))
		for i, ch := range selected {
			checkSpecs[i] = ch.Name + "|" + ch.Command + "|" + ch.Image + "|" + ch.WorkingDir + "|" + formatCheckEnvSpec(ch.Env)
		}
		memoKey = ComputeMemoKeyWithEnv(
			absDir, sb.ModeName(), opts.BaseRef, opts.OnlyChecks, opts.FailFast, opts.ChangedLinesOnly,
			changed, deleted, checkSpecs,
			DetectToolchainFingerprint(ctx, sb.ModeName(), cfg.Sandbox.Image, cfg.Stack),
		)
		if cachedRep, hit := LoadMemo(memoKey, 15*time.Minute); hit {
			cachedRep.Cached = true
			cachedRep.DurationMs = time.Since(started).Milliseconds()
			if opts.Compact {
				cachedRep.Checks = compactChecks(cachedRep.Checks)
			}
			return cachedRep, nil
		}
	}

	var hunkRanges map[string][]diff.LineRange
	if opts.ChangedLinesOnly && len(changed) > 0 {
		hunkRanges, _ = diff.ChangedLineRanges(ctx, absDir, opts.BaseRef)
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
		res := diff.ResolveWithDeleted(absDir, cfg.Stack, ch, changed, deleted)
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
		waves, waveErr := BuildWaves(selected)
		if waveErr != nil {
			return nil, waveErr
		}

		if err := sb.EnsureWarm(ctx); err != nil {
			return nil, err
		}

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		statusByName := make(map[string]string, len(selected))
		for _, p := range plans {
			if p.resolved.Skip {
				statusByName[p.cfg.Name] = StatusSkipped
			}
		}

		var statusMu sync.Mutex
		for _, wave := range waves {
			if opts.FailFast && runCtx.Err() != nil {
				for _, idx := range wave {
					p := plans[idx]
					if p.resolved.Skip {
						continue
					}
					results[p.index] = CheckResult{
						Name:       p.cfg.Name,
						Status:     StatusSkipped,
						WorkingDir: p.resolved.WorkingDir,
						SkipReason: "cancelled by --fail-fast",
					}
					statusByName[p.cfg.Name] = StatusSkipped
				}
				continue
			}

			var wg sync.WaitGroup
			var sem chan struct{}
			if cfg.MaxParallel > 0 {
				sem = make(chan struct{}, cfg.MaxParallel)
			}
			for _, idx := range wave {
				p := plans[idx]
				if p.resolved.Skip {
					continue
				}
				if dep, failed := FailedDependency(p.cfg, statusByName); failed {
					results[p.index] = CheckResult{
						Name:       p.cfg.Name,
						Status:     StatusSkipped,
						WorkingDir: p.resolved.WorkingDir,
						SkipReason: "dependency failed: " + dep,
					}
					statusByName[p.cfg.Name] = StatusDepFailed
					continue
				}

				wg.Add(1)
				go func(plan plannedCheck) {
					defer wg.Done()
					if sem != nil {
						sem <- struct{}{}
						defer func() { <-sem }()
					}
					cStart := time.Now()
					fixed := false
					if opts.Fix && plan.cfg.FixCommand != "" {
						fixCfg := plan.cfg
						fixCfg.Command = plan.cfg.FixCommand
						fixRes := diff.ResolveWithDeleted(absDir, cfg.Stack, fixCfg, changed, deleted)
						if !fixRes.Skip {
							var (
								fixExecRes sandbox.ExecResult
								fixErr     error
							)
							if envSB, ok := sb.(sandbox.EnvAwareSandbox); ok && (len(plan.cfg.Env) > 0 || plan.cfg.Image != "") {
								fixExecRes, fixErr = envSB.ExecWithEnv(runCtx, fixRes.WorkingDir, plan.cfg.Image, fixRes.Command, plan.cfg.Env, plan.cfg.ParsedTimeout())
							} else if imgSB, ok := sb.(sandbox.ImageAwareSandbox); ok && plan.cfg.Image != "" {
								fixExecRes, fixErr = imgSB.ExecWithImage(runCtx, fixRes.WorkingDir, plan.cfg.Image, fixRes.Command, plan.cfg.ParsedTimeout())
							} else {
								fixExecRes, fixErr = sb.Exec(runCtx, fixRes.WorkingDir, fixRes.Command, plan.cfg.ParsedTimeout())
							}
							totalRawBytes.Add(int64(len(fixExecRes.Combined)))
							if fixErr == nil && !fixExecRes.TimedOut {
								fixed = true
							}
						}
					}
					var (
						execRes sandbox.ExecResult
						execErr error
					)
					if envSB, ok := sb.(sandbox.EnvAwareSandbox); ok && (len(plan.cfg.Env) > 0 || plan.cfg.Image != "") {
						execRes, execErr = envSB.ExecWithEnv(runCtx, plan.resolved.WorkingDir, plan.cfg.Image, plan.resolved.Command, plan.cfg.Env, plan.cfg.ParsedTimeout())
					} else if imgSB, ok := sb.(sandbox.ImageAwareSandbox); ok && plan.cfg.Image != "" {
						execRes, execErr = imgSB.ExecWithImage(runCtx, plan.resolved.WorkingDir, plan.cfg.Image, plan.resolved.Command, plan.cfg.ParsedTimeout())
					} else {
						execRes, execErr = sb.Exec(runCtx, plan.resolved.WorkingDir, plan.resolved.Command, plan.cfg.ParsedTimeout())
					}
					dur := time.Since(cStart).Milliseconds()
					totalRawBytes.Add(int64(len(execRes.Combined)))

					setStatus := func(st string) {
						statusMu.Lock()
						statusByName[plan.cfg.Name] = st
						statusMu.Unlock()
					}

					if execErr != nil {
						results[plan.index] = CheckResult{
							Name:       plan.cfg.Name,
							Status:     StatusFailed,
							Fixed:      fixed,
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
						setStatus(StatusFailed)
						if opts.FailFast {
							cancel()
						}
						return
					}

					if execRes.TimedOut {
						results[plan.index] = CheckResult{
							Name:       plan.cfg.Name,
							Status:     StatusFailed,
							Fixed:      fixed,
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
						setStatus(StatusFailed)
						if opts.FailFast {
							cancel()
						}
						return
					}

					if execRes.ExitCode == 0 {
						results[plan.index] = CheckResult{
							Name:       plan.cfg.Name,
							Status:     StatusPassed,
							Fixed:      fixed,
							WorkingDir: plan.resolved.WorkingDir,
							DurationMs: dur,
						}
						setStatus(StatusPassed)
						return
					}

					parseRoot := absDir
					if plan.resolved.WorkingDir != "" {
						parseRoot = filepath.Join(absDir, filepath.FromSlash(plan.resolved.WorkingDir))
					}
					failures, truncated := parser.Parse(plan.cfg.Parser, plan.resolved.Command, parseRoot, execRes.Combined)
					// Prefix Failure.File with WorkingDir so agents always get repo-root-relative paths
					if plan.resolved.WorkingDir != "" {
						prefix := strings.TrimSuffix(plan.resolved.WorkingDir, "/") + "/"
						for i := range failures {
							if failures[i].File != "" && !strings.HasPrefix(failures[i].File, prefix) {
								failures[i].File = prefix + failures[i].File
							}
						}
					}
					filteredOut := 0
					if opts.ChangedLinesOnly && isLinterCheck(plan.cfg) && len(hunkRanges) > 0 && len(failures) > 0 {
						var kept []Failure
						for _, f := range failures {
							repoRel := f.File
							if plan.resolved.WorkingDir != "" && repoRel != "" && !strings.HasPrefix(repoRel, plan.resolved.WorkingDir+"/") {
								repoRel = plan.resolved.WorkingDir + "/" + repoRel
							}
							if diff.IsTestFile(repoRel) || diff.LineInDiff(hunkRanges, repoRel, f.Line) {
								kept = append(kept, f)
							} else {
								filteredOut++
							}
						}
						failures = kept
						if len(failures) == 0 && filteredOut > 0 {
							results[plan.index] = CheckResult{
								Name:        plan.cfg.Name,
								Status:      StatusPassedWithFiltered,
								Fixed:       fixed,
								WorkingDir:  plan.resolved.WorkingDir,
								DurationMs:  dur,
								FilteredOut: filteredOut,
							}
							setStatus(StatusPassedWithFiltered)
							return
						}
					}
					if len(failures) == 0 {
						failures = []Failure{
							{
								Message:    fmt.Sprintf("command exited with code %d", execRes.ExitCode),
								RawExcerpt: truncateTail(execRes.Combined, 240),
							},
						}
					}
					if strings.Contains(plan.resolved.Command, "npx --no-install") {
						lowerOut := strings.ToLower(execRes.Combined)
						if strings.Contains(lowerOut, "could not determine executable") ||
							strings.Contains(lowerOut, "command not found") ||
							strings.Contains(lowerOut, "is not recognized") ||
							strings.Contains(lowerOut, "enoent") ||
							execRes.ExitCode == 127 {
							scopeDir := plan.resolved.WorkingDir
							if scopeDir == "" {
								scopeDir = "."
							}
							hint := fmt.Sprintf(" (hint: local Node dependencies may not be installed; run 'npm ci' or 'npm install' in %s first)", scopeDir)
							if !strings.Contains(failures[0].Message, "npm ci") {
								failures[0].Message += hint
							}
						}
					}
					results[plan.index] = CheckResult{
						Name:           plan.cfg.Name,
						Status:         StatusFailed,
						Fixed:          fixed,
						WorkingDir:     plan.resolved.WorkingDir,
						DurationMs:     dur,
						Command:        plan.resolved.Command,
						FilteredOut:    filteredOut,
						TruncatedCount: truncated,
						Failures:       failures,
					}
					setStatus(StatusFailed)
					if opts.FailFast {
						cancel()
					}
				}(p)
			}
			wg.Wait()
		}
	}

	var summary Summary
	summary.TotalChecks = len(results)
	anyFiltered := false
	for _, r := range results {
		switch r.Status {
		case StatusPassed:
			summary.Passed++
		case StatusPassedWithFiltered:
			summary.Passed++
			anyFiltered = true
		case StatusFailed:
			summary.Failed++
		case StatusSkipped:
			summary.Skipped++
		}
	}

	overallStatus := StatusPassed
	if summary.Failed > 0 {
		overallStatus = StatusFailed
	} else if anyFiltered {
		overallStatus = StatusPassedWithFiltered
	}

	reportChecks := results
	if opts.Compact {
		reportChecks = compactChecks(results)
	}

	report := &RunReport{
		Status:             overallStatus,
		Mode:               sb.ModeName(),
		ProjectDir:         filepath.ToSlash(absDir),
		DurationMs:         time.Since(started).Milliseconds(),
		FullSuiteTriggered: fullSuite,
		ScopeReason:        scopeReason,
		ChangedFiles:       changed,
		DeletedFiles:       deleted,
		Summary:            summary,
		Checks:             reportChecks,
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

	if !opts.NoCache && !opts.Fix && memoKey != "" && anyActive {
		memoReport := *report
		memoReport.Checks = results
		SaveMemo(memoKey, &memoReport)
	}

	return report, nil
}

func compactChecks(checks []CheckResult) []CheckResult {
	out := make([]CheckResult, 0, len(checks))
	for _, ch := range checks {
		if ch.Status != StatusSkipped {
			out = append(out, ch)
		}
	}
	return out
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

func isLinterCheck(ch config.CheckConfig) bool {
	p := strings.ToLower(strings.TrimSpace(ch.Parser))
	switch p {
	case "eslint", "ruff", "golangci-lint", "go-vet":
		return true
	case "tsc", "mypy", "pyright", "go-test", "vitest", "jest", "pytest", "rust-cargo", "rust-test":
		return false
	}
	cmd := strings.ToLower(ch.Command)
	return strings.Contains(cmd, "eslint") || strings.Contains(cmd, "ruff") || strings.Contains(cmd, "golangci-lint") || strings.Contains(cmd, "go vet")
}

func truncateTail(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return "..." + s[len(s)-maxLen+3:]
}

func formatCheckEnvSpec(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+env[k])
	}
	return strings.Join(parts, ",")
}
