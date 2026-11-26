package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/akhanatoon/quench/internal/config"
	"github.com/akhanatoon/quench/internal/runner"
	"github.com/akhanatoon/quench/internal/sandbox"
	"github.com/spf13/cobra"
)

type BenchModeResult struct {
	Mode      string  `json:"mode"`
	Runs      int     `json:"runs"`
	MinMs     int64   `json:"min_ms"`
	AvgMs     int64   `json:"avg_ms"`
	MaxMs     int64   `json:"max_ms"`
	SamplesMs []int64 `json:"samples_ms"`
	Status    string  `json:"status"`
	Cached    bool    `json:"cached,omitempty"`
}

type BenchReport struct {
	ProjectDir      string            `json:"project_dir"`
	Stack           string            `json:"stack"`
	Runs            int               `json:"runs"`
	LocalOnly       bool              `json:"local_only"`
	DockerAvailable bool              `json:"docker_available"`
	Results         []BenchModeResult `json:"results"`
}

func newBenchCmd() *cobra.Command {
	var (
		runs      int
		localOnly bool
		jsonOut   bool
	)

	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Benchmark verification latency across local and warm Docker modes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runs < 1 {
				return fmt.Errorf("--runs must be >= 1 (got %d)", runs)
			}

			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}

			cfg, _, err := config.Load(projectDir)
			if err != nil {
				return err
			}

			ctx := context.Background()
			report := BenchReport{
				ProjectDir: filepath.ToSlash(projectDir),
				Stack:      string(cfg.Stack),
				Runs:       runs,
				LocalOnly:  localOnly,
			}

			localSB := sandbox.NewLocal(projectDir, cfg.Stack)

			localUncached, err := benchmarkMode(ctx, cfg, localSB, projectDir, "local (uncached)", runs, true)
			if err != nil {
				return err
			}
			report.Results = append(report.Results, localUncached)

			localCached, err := benchmarkMode(ctx, cfg, localSB, projectDir, "local (memo-cached)", runs, false)
			if err != nil {
				return err
			}
			report.Results = append(report.Results, localCached)

			if !localOnly && sandbox.CheckDaemon(ctx) == nil {
				report.DockerAvailable = true
				dockerPool := sandbox.NewDockerPool(projectDir, cfg)
				if err := dockerPool.EnsureWarm(ctx); err != nil {
					return fmt.Errorf("pre-warm docker sandbox: %w", err)
				}

				dockerUncached, err := benchmarkMode(ctx, cfg, dockerPool, projectDir, "docker-warm (uncached)", runs, true)
				if err != nil {
					return err
				}
				report.Results = append(report.Results, dockerUncached)

				dockerCached, err := benchmarkMode(ctx, cfg, dockerPool, projectDir, "docker-warm (memo-cached)", runs, false)
				if err != nil {
					return err
				}
				report.Results = append(report.Results, dockerCached)
			}

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}

			fmt.Fprint(cmd.OutOrStdout(), formatBenchMarkdown(report))
			return nil
		},
	}

	cmd.Flags().IntVar(&runs, "runs", 3, "Number of timed iterations per mode")
	cmd.Flags().BoolVar(&localOnly, "local-only", false, "Only benchmark local execution (skip Docker warm sandbox)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit benchmark report as JSON")

	return cmd
}

func benchmarkMode(
	ctx context.Context,
	cfg *config.Config,
	sb sandbox.Sandbox,
	projectDir string,
	label string,
	runs int,
	noCache bool,
) (BenchModeResult, error) {
	opts := runner.Options{
		ProjectDir: projectDir,
		NoCache:    noCache,
	}

	// Prime the memo cache before timing memo-cached runs.
	if !noCache {
		if _, err := runner.Run(ctx, cfg, sb, opts); err != nil {
			return BenchModeResult{}, fmt.Errorf("%s warmup: %w", label, err)
		}
	}

	samples := make([]int64, 0, runs)
	lastStatus := ""
	anyCached := false

	for i := 0; i < runs; i++ {
		started := time.Now()
		rep, err := runner.Run(ctx, cfg, sb, opts)
		if err != nil {
			return BenchModeResult{}, fmt.Errorf("%s run %d/%d: %w", label, i+1, runs, err)
		}
		samples = append(samples, time.Since(started).Milliseconds())
		lastStatus = rep.Status
		if rep.Cached {
			anyCached = true
		}
	}

	minMs, avgMs, maxMs := computeBenchStats(samples)
	return BenchModeResult{
		Mode:      label,
		Runs:      runs,
		MinMs:     minMs,
		AvgMs:     avgMs,
		MaxMs:     maxMs,
		SamplesMs: samples,
		Status:    lastStatus,
		Cached:    anyCached,
	}, nil
}

func computeBenchStats(samples []int64) (minMs int64, avgMs int64, maxMs int64) {
	if len(samples) == 0 {
		return 0, 0, 0
	}
	minMs = samples[0]
	maxMs = samples[0]
	var sum int64
	for _, s := range samples {
		if s < minMs {
			minMs = s
		}
		if s > maxMs {
			maxMs = s
		}
		sum += s
	}
	avgMs = int64(math.Round(float64(sum) / float64(len(samples))))
	return minMs, avgMs, maxMs
}

func formatBenchMarkdown(rep BenchReport) string {
	var b strings.Builder
	repoName := filepath.Base(filepath.FromSlash(rep.ProjectDir))
	if repoName == "" || repoName == "." || repoName == "/" {
		repoName = rep.ProjectDir
	}
	fmt.Fprintf(&b, "### Quench Benchmark (`%s` · `%s` · %d runs)\n\n", repoName, rep.Stack, rep.Runs)
	b.WriteString("| Mode | Runs | Min (ms) | Avg (ms) | Max (ms) | Status |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | --- |\n")
	for _, r := range rep.Results {
		status := r.Status
		if status == "" {
			status = "unknown"
		}
		if r.Cached {
			status += " (cached)"
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %s |\n",
			r.Mode, r.Runs, r.MinMs, r.AvgMs, r.MaxMs, status)
	}
	if !rep.LocalOnly && !rep.DockerAvailable {
		b.WriteString("\n> Docker daemon unavailable; skipped `docker-warm` benchmarks.\n")
	}
	return b.String()
}
