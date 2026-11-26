package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/akhanatoon/quench/internal/config"
	"github.com/akhanatoon/quench/internal/diff"
	"github.com/akhanatoon/quench/internal/sandbox"
	"github.com/spf13/cobra"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
}

type doctorReport struct {
	ProjectDir string        `json:"project_dir"`
	Ready      bool          `json:"ready"`
	Checks     []doctorCheck `json:"checks"`
}

func newDoctorCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose git, Docker daemon, detected stacks, local toolchains, and warm sandbox health",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := resolveProjectDir()
			if err != nil {
				return err
			}
			rep := runDoctor(context.Background(), projectDir)
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "quench doctor (%s)\n", rep.ProjectDir)
			for _, c := range rep.Checks {
				badge := "[ OK ]"
				switch c.Status {
				case "warn":
					badge = "[WARN]"
				case "fail":
					badge = "[FAIL]"
				}
				fmt.Fprintf(out, "  %s %-16s %s\n", badge, c.Name, c.Detail)
			}
			if !rep.Ready {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output diagnostics as JSON")
	return cmd
}

func runDoctor(ctx context.Context, projectDir string) doctorReport {
	rep := doctorReport{
		ProjectDir: filepath.ToSlash(projectDir),
		Ready:      true,
	}

	// 1. Git CLI + Repo check
	if gitPath, err := exec.LookPath("git"); err != nil {
		rep.Ready = false
		rep.Checks = append(rep.Checks, doctorCheck{Name: "git", Status: "fail", Detail: "git CLI not found in PATH"})
	} else {
		files, diffErr := diff.ChangedFiles(ctx, projectDir, "")
		if diffErr != nil {
			rep.Ready = false
			rep.Checks = append(rep.Checks, doctorCheck{Name: "git", Status: "fail", Detail: fmt.Sprintf("found %s, but %s is not a git repo", gitPath, projectDir)})
		} else {
			rep.Checks = append(rep.Checks, doctorCheck{Name: "git", Status: "ok", Detail: fmt.Sprintf("repository ok (%d active changed files)", len(files))})
		}
	}

	// 2. Stack / Config detection
	cfg, fromFile, cfgErr := config.Load(projectDir)
	if cfgErr != nil {
		rep.Ready = false
		rep.Checks = append(rep.Checks, doctorCheck{Name: "config", Status: "fail", Detail: cfgErr.Error()})
	} else {
		src := "zero-config"
		if fromFile {
			src = "quench.yaml"
		}
		ws := config.DetectWorkspaces(projectDir)
		var wsNames []string
		for _, w := range ws {
			loc := w.RelDir
			if loc == "" {
				loc = "."
			}
			wsNames = append(wsNames, fmt.Sprintf("%s(%s)", loc, w.Config.Stack))
		}
		detail := fmt.Sprintf("%s | stack=%s | image=%s | %d checks", src, cfg.Stack, cfg.Sandbox.Image, len(cfg.Checks))
		if len(wsNames) > 1 {
			detail += " | workspaces=" + strings.Join(wsNames, ", ")
		}
		rep.Checks = append(rep.Checks, doctorCheck{Name: "config", Status: "ok", Detail: detail})
	}

	// 3. Docker Daemon + Warm Sandbox status
	if dErr := sandbox.CheckDaemon(ctx); dErr != nil {
		rep.Checks = append(rep.Checks, doctorCheck{
			Name:   "docker",
			Status: "warn",
			Detail: dErr.Error() + " (local mode still works)",
		})
	} else if cfg != nil {
		ds := sandbox.NewDocker(projectDir, cfg)
		st, _ := ds.Status(ctx)
		rep.Checks = append(rep.Checks, doctorCheck{
			Name:   "docker",
			Status: "ok",
			Detail: fmt.Sprintf("daemon reachable | container=%s state=%s synced=%v", st.ContainerName, st.State, st.DepsSynced),
		})
	} else {
		rep.Checks = append(rep.Checks, doctorCheck{Name: "docker", Status: "ok", Detail: "daemon reachable"})
	}

	// 4. Local host toolchains
	tools := []string{"go", "node", "python", "cargo", "ruff", "pytest"}
	var foundTools []string
	for _, t := range tools {
		if _, err := exec.LookPath(t); err == nil {
			foundTools = append(foundTools, t)
		}
	}
	if len(foundTools) == 0 {
		rep.Checks = append(rep.Checks, doctorCheck{Name: "local-tools", Status: "warn", Detail: "no local runtimes detected on host (Docker sandbox recommended)"})
	} else {
		rep.Checks = append(rep.Checks, doctorCheck{Name: "local-tools", Status: "ok", Detail: "available: " + strings.Join(foundTools, ", ")})
	}

	return rep
}
