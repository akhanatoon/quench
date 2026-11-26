package cli

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/akhanatoon/quench/internal/config"
	"github.com/akhanatoon/quench/internal/runner"
	"github.com/akhanatoon/quench/internal/sandbox"
	"github.com/spf13/cobra"
)

const watchPollInterval = 250 * time.Millisecond

var watchIgnoredDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"target":       {},
	"dist":         {},
	"vendor":       {},
	".venv":        {},
	"__pycache__":  {},
}

func newWatchCmd() *cobra.Command {
	var (
		baseRef    string
		forceLocal bool
		onlyChecks []string
		failFast   bool
		debounce   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch for file changes and re-run diff-aware verification checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}

			cfg, _, err := config.Load(projectDir)
			if err != nil {
				return err
			}

			if debounce < 0 {
				debounce = 0
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
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "[%s] watch: monitoring %s (mode=%s, debounce=%s, ctrl+c to stop)\n",
				watchNow(), projectDir, mode, debounce)

			runOnce := func(reason string) error {
				fmt.Fprintf(out, "[%s] watch: %s\n", watchNow(), reason)
				report, runErr := runner.Run(ctx, cfg, sb, runner.Options{
					ProjectDir: projectDir,
					BaseRef:    baseRef,
					OnlyChecks: onlyChecks,
					FailFast:   failFast,
				})
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if runErr != nil {
					fmt.Fprintf(out, "[%s] watch: error: %v\n", watchNow(), runErr)
					return nil
				}
				printHumanReport(cmd, report)
				return nil
			}

			lastRunFP, err := computeTreeFingerprint(projectDir)
			if err != nil {
				return fmt.Errorf("scan project tree: %w", err)
			}

			if err := runOnce("initial verification"); err != nil {
				fmt.Fprintf(out, "[%s] watch: stopped\n", watchNow())
				return nil
			}

			ticker := time.NewTicker(watchPollInterval)
			defer ticker.Stop()

			var (
				pending      bool
				pendingFP    uint64
				pendingSince time.Time
			)

			for {
				select {
				case <-ctx.Done():
					fmt.Fprintf(out, "[%s] watch: stopped\n", watchNow())
					return nil
				case <-ticker.C:
					fp, fpErr := computeTreeFingerprint(projectDir)
					if fpErr != nil {
						continue
					}

					if fp == lastRunFP {
						pending = false
						continue
					}

					if !pending || fp != pendingFP {
						pending = true
						pendingFP = fp
						pendingSince = time.Now()
					}

					if time.Since(pendingSince) < debounce {
						continue
					}

					pending = false
					lastRunFP = fp
					if err := runOnce("change detected, re-running checks"); err != nil {
						fmt.Fprintf(out, "[%s] watch: stopped\n", watchNow())
						return nil
					}
				}
			}
		},
	}

	cmd.Flags().StringVar(&baseRef, "base", "", "Compare against git base ref (e.g. main, HEAD~1)")
	cmd.Flags().BoolVar(&forceLocal, "local", false, "Run checks directly on host instead of Docker sandbox")
	cmd.Flags().StringSliceVar(&onlyChecks, "only", nil, "Comma-separated check names to run (e.g. lint,test)")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "Cancel remaining checks on first failure")
	cmd.Flags().DurationVar(&debounce, "debounce", 350*time.Millisecond, "Debounce interval before re-running after file changes")

	return cmd
}

func watchNow() string {
	return time.Now().Format("15:04:05")
}

// computeTreeFingerprint walks rootDir and hashes relative file paths, mtimes,
// and sizes for non-ignored source files.
func computeTreeFingerprint(rootDir string) (uint64, error) {
	h := fnv.New64a()
	var buf [16]byte

	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Skip transient unreadable entries (e.g. atomic editor temp files).
			return nil
		}
		if d.IsDir() {
			if path != rootDir {
				if _, skip := watchIgnoredDirs[d.Name()]; skip {
					return filepath.SkipDir
				}
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			// File may have been deleted/renamed mid-walk; ignore transiently.
			return nil
		}

		rel, err := filepath.Rel(rootDir, path)
		if err != nil {
			rel = path
		}
		_, _ = h.Write([]byte(filepath.ToSlash(rel)))
		_, _ = h.Write([]byte{0})
		binary.LittleEndian.PutUint64(buf[0:8], uint64(info.ModTime().UnixNano()))
		binary.LittleEndian.PutUint64(buf[8:16], uint64(info.Size()))
		_, _ = h.Write(buf[:])
		return nil
	})
	if err != nil {
		return 0, err
	}
	return h.Sum64(), nil
}
