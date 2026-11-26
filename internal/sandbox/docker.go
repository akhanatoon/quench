package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/akhanatoon/quench/internal/config"
)

const (
	ContainerPrefix = "quench-warm-"
	VolumePrefix    = "quench-cache-"
	lockHashFile    = "/tmp/.quench-lockhash"
	lastUsedFile    = "/tmp/.quench-last-used"
)

type DaemonUnavailableError struct {
	Reason string
}

func (e *DaemonUnavailableError) Error() string {
	return fmt.Sprintf("docker daemon unavailable (%s); start Docker or re-run with --local", e.Reason)
}

type DockerSandbox struct {
	projectDir    string
	hash8         string
	containerName string
	cfg           *config.Config
}

func NewDocker(projectDir string, cfg *config.Config) *DockerSandbox {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	h := ProjectHash8(abs)
	return &DockerSandbox{
		projectDir:    abs,
		hash8:         h,
		containerName: ContainerPrefix + h,
		cfg:           cfg,
	}
}

func (d *DockerSandbox) ModeName() string {
	return "docker-warm"
}

func (d *DockerSandbox) ContainerName() string {
	return d.containerName
}

func CheckDaemon(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return &DaemonUnavailableError{Reason: "docker CLI not found in PATH"}
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(tctx, "docker", "info", "--format", "{{.ServerVersion}}")
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return &DaemonUnavailableError{Reason: msg}
	}
	return nil
}

type inspectData struct {
	ID     string `json:"Id"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	NetworkSettings struct {
		Networks map[string]any `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (d *DockerSandbox) inspect(ctx context.Context) (*inspectData, error) {
	out, err := dockerOutput(ctx, 5*time.Second, "inspect", d.containerName)
	if err != nil {
		return nil, err
	}
	var items []inspectData
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) == 0 {
		return nil, fmt.Errorf("parse docker inspect output: %w", err)
	}
	return &items[0], nil
}

func (d *DockerSandbox) EnsureWarm(ctx context.Context) error {
	if err := CheckDaemon(ctx); err != nil {
		return err
	}

	info, err := d.inspect(ctx)
	if err == nil && info != nil {
		// Recreate if image changed in config
		if d.cfg.Sandbox.Image != "" && info.Config.Image != d.cfg.Sandbox.Image {
			_ = d.Teardown(ctx)
			info = nil
		}
	}

	if info == nil {
		if err := d.startNewContainer(ctx); err != nil {
			return err
		}
	} else if !info.State.Running {
		if _, err := dockerOutput(ctx, 15*time.Second, "start", d.containerName); err != nil {
			return fmt.Errorf("start stopped warm container %s: %w", d.containerName, err)
		}
	}

	if d.cfg.Sandbox.ShouldSyncDeps() {
		if err := d.syncDependenciesIfNeeded(ctx); err != nil {
			return err
		}
	}

	// Enforce post-sync network isolation if requested
	if d.cfg.Sandbox.Network == "none" {
		if cur, err := d.inspect(ctx); err == nil && cur != nil {
			if _, hasBridge := cur.NetworkSettings.Networks["bridge"]; hasBridge {
				_, _ = dockerOutput(ctx, 5*time.Second, "network", "disconnect", "bridge", d.containerName)
			}
		}
	}

	d.touchLastUsed(ctx)
	return nil
}

func (d *DockerSandbox) startNewContainer(ctx context.Context) error {
	image := d.cfg.Sandbox.Image
	if image == "" {
		image = "alpine:3.20"
	}

	hostPath := filepath.ToSlash(d.projectDir)
	args := []string{
		"run", "-d",
		"--name", d.containerName,
		"--label", "quench.managed=true",
		"--label", "quench.project_hash=" + d.hash8,
		"--label", "quench.project_dir=" + hostPath,
		"--label", "quench.stack=" + string(d.cfg.Stack),
		"--label", "quench.idle_timeout=" + d.cfg.Sandbox.ParsedIdleTimeout().String(),
		"-e", "CI=true",
		"-e", "NO_COLOR=1",
		"-e", "FORCE_COLOR=0",
		"-e", "GOMODCACHE=/go/pkg/mod",
		"-e", "GOCACHE=/root/.cache/go-build",
		"-e", "NPM_CONFIG_CACHE=/root/.npm",
		"-e", "PIP_CACHE_DIR=/root/.cache/pip",
		"-v", fmt.Sprintf("%s:/workspace", hostPath),
		"-w", "/workspace",
	}

	// Start with bridge during setup if sync_deps is enabled; otherwise honor network directly
	if d.cfg.Sandbox.Network == "none" && !d.cfg.Sandbox.ShouldSyncDeps() {
		args = append(args, "--network", "none")
	} else if d.cfg.Sandbox.Network == "host" {
		args = append(args, "--network", "host")
	}

	for _, cp := range d.cfg.Sandbox.CachePaths {
		volName := d.cacheVolumeName(cp)
		args = append(args, "-v", fmt.Sprintf("%s:%s", volName, cp))
	}

	args = append(args, image, "sleep", "infinity")
	if _, err := dockerOutput(ctx, 5*time.Minute, args...); err != nil {
		return fmt.Errorf("launch warm container %s (%s): %w", d.containerName, image, err)
	}
	return nil
}

func (d *DockerSandbox) syncDependenciesIfNeeded(ctx context.Context) error {
	desiredHash := ComputeLockfileHash(d.projectDir)
	if desiredHash == "" && d.cfg.Sandbox.SetupCommand == "" {
		return nil
	}

	res, err := d.execRaw(ctx, fmt.Sprintf("cat %s 2>/dev/null || true", lockHashFile), 5*time.Second)
	if err == nil && strings.TrimSpace(res.Stdout) == desiredHash && desiredHash != "" {
		return nil
	}

	setupCmd := d.cfg.Sandbox.SetupCommand
	if setupCmd == "" {
		setupCmd = defaultSetupCommand(d.projectDir, d.cfg.Stack)
	}
	if setupCmd == "" {
		return nil
	}

	// Reconnect bridge temporarily if container was previously air-gapped
	if d.cfg.Sandbox.Network == "none" {
		_, _ = dockerOutput(ctx, 5*time.Second, "network", "connect", "bridge", d.containerName)
	}

	syncRes, err := d.execRaw(ctx, setupCmd, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("dependency sync failed: %w", err)
	}
	if syncRes.ExitCode != 0 {
		return fmt.Errorf("dependency sync command %q failed (exit %d): %s", setupCmd, syncRes.ExitCode, strings.TrimSpace(syncRes.Combined))
	}

	if desiredHash != "" {
		_, _ = d.execRaw(ctx, fmt.Sprintf("printf '%%s' '%s' > %s", desiredHash, lockHashFile), 5*time.Second)
	}
	return nil
}

func (d *DockerSandbox) Exec(ctx context.Context, command string, timeout time.Duration) (ExecResult, error) {
	d.touchLastUsed(ctx)
	return d.execRaw(ctx, command, timeout)
}

func (d *DockerSandbox) execRaw(ctx context.Context, command string, timeout time.Duration) (ExecResult, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Ensure /usr/local/go/bin and localvenv/node_modules/.bin are reachable inside alpine/slim images
	wrapped := "export PATH=\"/workspace/node_modules/.bin:/root/.local/bin:/usr/local/go/bin:$PATH\"; " + command
	cmd := exec.CommandContext(tctx, "docker", "exec", "-i", d.containerName, "sh", "-c", wrapped)
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

func (d *DockerSandbox) Status(ctx context.Context) (StatusInfo, error) {
	vols := make([]string, 0, len(d.cfg.Sandbox.CachePaths))
	for _, cp := range d.cfg.Sandbox.CachePaths {
		vols = append(vols, d.cacheVolumeName(cp))
	}

	st := StatusInfo{
		Mode:          "docker-warm",
		State:         "absent",
		ContainerName: d.containerName,
		Image:         d.cfg.Sandbox.Image,
		ProjectDir:    filepath.ToSlash(d.projectDir),
		Stack:         string(d.cfg.Stack),
		CacheVolumes:  vols,
	}

	if err := CheckDaemon(ctx); err != nil {
		return st, err
	}

	info, err := d.inspect(ctx)
	if err != nil || info == nil {
		return st, nil
	}

	if len(info.ID) >= 12 {
		st.ContainerID = info.ID[:12]
	} else {
		st.ContainerID = info.ID
	}
	st.Image = info.Config.Image
	if info.State.Running {
		st.State = "running"
		if started, pErr := time.Parse(time.RFC3339Nano, info.State.StartedAt); pErr == nil {
			st.Uptime = time.Since(started).Round(time.Second).String()
		}
		if res, lErr := d.execRaw(ctx, fmt.Sprintf("cat %s 2>/dev/null || true", lastUsedFile), 3*time.Second); lErr == nil {
			st.LastUsed = strings.TrimSpace(res.Stdout)
		}
		desired := ComputeLockfileHash(d.projectDir)
		if desired == "" {
			st.DepsSynced = true
		} else if res, hErr := d.execRaw(ctx, fmt.Sprintf("cat %s 2>/dev/null || true", lockHashFile), 3*time.Second); hErr == nil {
			st.DepsSynced = strings.TrimSpace(res.Stdout) == desired
		}
	} else {
		st.State = "stopped"
	}

	return st, nil
}

func (d *DockerSandbox) Reset(ctx context.Context, purgeCache bool) error {
	if err := CheckDaemon(ctx); err != nil {
		return err
	}
	_ = d.Teardown(ctx)
	if purgeCache {
		for _, cp := range d.cfg.Sandbox.CachePaths {
			vol := d.cacheVolumeName(cp)
			_, _ = dockerOutput(ctx, 15*time.Second, "volume", "rm", "-f", vol)
		}
	}
	return nil
}

func (d *DockerSandbox) Teardown(ctx context.Context) error {
	if err := CheckDaemon(ctx); err != nil {
		return err
	}
	_, _ = dockerOutput(ctx, 15*time.Second, "rm", "-f", d.containerName)
	return nil
}

func (d *DockerSandbox) touchLastUsed(ctx context.Context) {
	ts := time.Now().UTC().Format(time.RFC3339)
	_, _ = d.execRaw(ctx, fmt.Sprintf("printf '%%s' '%s' > %s", ts, lastUsedFile), 3*time.Second)
}

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

func (d *DockerSandbox) cacheVolumeName(containerPath string) string {
	slug := strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(containerPath), "-"), "-")
	if slug == "" {
		slug = "cache"
	}
	return fmt.Sprintf("%s%s-%s", VolumePrefix, d.hash8, slug)
}

// GarbageCollect removes managed warm containers whose idle time exceeds their configured idle_timeout.
func GarbageCollect(ctx context.Context, forceAll bool) ([]string, error) {
	if err := CheckDaemon(ctx); err != nil {
		return nil, err
	}
	out, err := dockerOutput(ctx, 10*time.Second, "ps", "-a", "--filter", "label=quench.managed=true", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if forceAll {
			if _, rmErr := dockerOutput(ctx, 15*time.Second, "rm", "-f", name); rmErr == nil {
				removed = append(removed, name)
			}
			continue
		}

		rawInspect, iErr := dockerOutput(ctx, 5*time.Second, "inspect", name)
		if iErr != nil {
			continue
		}
		var items []inspectData
		if json.Unmarshal([]byte(rawInspect), &items) != nil || len(items) == 0 {
			continue
		}
		item := items[0]
		idleDur := 30 * time.Minute
		if rawIdle := item.Config.Labels["quench.idle_timeout"]; rawIdle != "" {
			if parsed, pErr := time.ParseDuration(rawIdle); pErr == nil && parsed > 0 {
				idleDur = parsed
			}
		}

		var lastActive time.Time
		if item.State.Running {
			if tsOut, eErr := dockerOutput(ctx, 3*time.Second, "exec", name, "sh", "-c", "cat "+lastUsedFile+" 2>/dev/null || true"); eErr == nil {
				if parsed, pErr := time.Parse(time.RFC3339, strings.TrimSpace(tsOut)); pErr == nil {
					lastActive = parsed
				}
			}
		}
		if lastActive.IsZero() {
			if parsed, pErr := time.Parse(time.RFC3339Nano, item.State.StartedAt); pErr == nil {
				lastActive = parsed
			}
		}
		if !lastActive.IsZero() && time.Since(lastActive) >= idleDur {
			if _, rmErr := dockerOutput(ctx, 15*time.Second, "rm", "-f", name); rmErr == nil {
				removed = append(removed, name)
			}
		}
	}
	return removed, nil
}

// ComputeLockfileHash hashes existing dependency lockfiles/manifests in projectDir.
func ComputeLockfileHash(projectDir string) string {
	candidates := []string{
		"go.mod",
		"go.sum",
		"package.json",
		"package-lock.json",
		"pnpm-lock.yaml",
		"yarn.lock",
		"bun.lock",
		"bun.lockb",
		"pyproject.toml",
		"requirements.txt",
		"uv.lock",
		"poetry.lock",
	}
	sort.Strings(candidates)
	h := sha256.New()
	found := false
	for _, name := range candidates {
		data, err := os.ReadFile(filepath.Join(projectDir, name))
		if err != nil {
			continue
		}
		found = true
		_, _ = h.Write([]byte(name + "\x00"))
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
	}
	if !found {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func defaultSetupCommand(projectDir string, stack config.StackType) string {
	switch stack {
	case config.StackGo:
		if fileExists(filepath.Join(projectDir, "go.mod")) {
			return "go mod download"
		}
	case config.StackNode:
		switch {
		case fileExists(filepath.Join(projectDir, "pnpm-lock.yaml")):
			return "corepack enable >/dev/null 2>&1 || true; pnpm install --frozen-lockfile || pnpm install"
		case fileExists(filepath.Join(projectDir, "yarn.lock")):
			return "corepack enable >/dev/null 2>&1 || true; yarn install"
		case fileExists(filepath.Join(projectDir, "package-lock.json")):
			return "npm ci --no-audit --no-fund || npm install --no-audit --no-fund"
		case fileExists(filepath.Join(projectDir, "package.json")):
			return "npm install --no-audit --no-fund"
		}
	case config.StackPython:
		if fileExists(filepath.Join(projectDir, "requirements.txt")) {
			return "pip install -q --disable-pip-version-check -r requirements.txt"
		}
		if fileExists(filepath.Join(projectDir, "pyproject.toml")) {
			return "pip install -q --disable-pip-version-check -e . 2>/dev/null || true"
		}
	}
	return ""
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func dockerOutput(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, "docker", args...)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}
