package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/akhanatoon/quench/internal/config"
)

type LocalSandbox struct {
	projectDir string
	stack      config.StackType
}

func NewLocal(projectDir string, stack config.StackType) *LocalSandbox {
	return &LocalSandbox{
		projectDir: projectDir,
		stack:      stack,
	}
}

func (l *LocalSandbox) ModeName() string {
	return "local"
}

func (l *LocalSandbox) EnsureWarm(ctx context.Context) error {
	return nil
}

func (l *LocalSandbox) Exec(ctx context.Context, workingDir string, command string, timeout time.Duration) (ExecResult, error) {
	return l.ExecWithEnv(ctx, workingDir, "", command, nil, timeout)
}

func (l *LocalSandbox) ExecWithEnv(ctx context.Context, workingDir string, _ string, command string, env map[string]string, timeout time.Duration) (ExecResult, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(tctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(tctx, "sh", "-c", command)
	}
	cmd.Dir = l.projectDir
	if workingDir != "" {
		cmd.Dir = filepath.Join(l.projectDir, filepath.FromSlash(workingDir))
	}
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		merged := append([]string{}, os.Environ()...)
		for _, k := range keys {
			merged = append(merged, k+"="+env[k])
		}
		cmd.Env = merged
	}
	cmd.WaitDelay = 2 * time.Second

	var stdout, stderr, combined bytes.Buffer
	cmd.Stdout = &multiWriter{a: &stdout, b: &combined}
	cmd.Stderr = &multiWriter{a: &stderr, b: &combined}

	err := cmd.Run()
	if errors.Is(tctx.Err(), context.DeadlineExceeded) {
		return ExecResult{
			ExitCode: 124,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			Combined: combined.String(),
			TimedOut: true,
		}, nil
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return ExecResult{}, err
		}
	}

	return ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Combined: combined.String(),
	}, nil
}

func (l *LocalSandbox) Status(ctx context.Context) (StatusInfo, error) {
	return StatusInfo{
		Mode:       "local",
		State:      "host",
		ProjectDir: l.projectDir,
		Stack:      string(l.stack),
		DepsSynced: true,
	}, nil
}

func (l *LocalSandbox) Reset(ctx context.Context, purgeCache bool) error {
	return nil
}

func (l *LocalSandbox) Teardown(ctx context.Context) error {
	return nil
}

type multiWriter struct {
	a *bytes.Buffer
	b *bytes.Buffer
}

func (m *multiWriter) Write(p []byte) (int, error) {
	_, _ = m.a.Write(p)
	return m.b.Write(p)
}
