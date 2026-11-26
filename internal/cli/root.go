package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	Version = "0.1.0"
	flagDir string
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "quench",
		Short:         "Warm-sandbox, diff-aware code verification for AI coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}

	root.PersistentFlags().StringVarP(&flagDir, "dir", "C", ".", "Target git repository directory")
	root.AddCommand(
		newInitCmd(),
		newRunCmd(),
		newWatchCmd(),
		newWarmCmd(),
		newHookCmd(),
		newBenchCmd(),
		newMCPCmd(),
		newDoctorCmd(),
	)

	return root
}

func resolveProjectDir() (string, error) {
	target := flagDir
	if target == "" {
		target = "."
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve directory %q: %w", target, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("project directory %q: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path %q is not a directory", abs)
	}
	return abs, nil
}

// Execute runs the CLI and maps outcome to exit codes:
// 0 = success, 1 = check failure, 2 = config/runtime error.
func Execute() int {
	cmd := NewRootCmd()
	if err := cmd.Execute(); err != nil {
		if exitErr, ok := err.(*ExitError); ok {
			if exitErr.Message != "" {
				fmt.Fprintln(os.Stderr, exitErr.Message)
			}
			return exitErr.Code
		}
		fmt.Fprintf(os.Stderr, "quench error: %v\n", err)
		return 2
	}
	return 0
}

type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	return e.Message
}
