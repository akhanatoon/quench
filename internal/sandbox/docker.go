package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/akhanatoon/quench/internal/config"
)

const (
	ContainerPrefix       = "quench-warm-"
	VolumePrefix          = "quench-cache-"
	WorkspaceVolumePrefix = "quench-ws-"
	lockHashFile          = "/tmp/.quench-lockhash"
	lastUsedFile          = "/tmp/.quench-last-used"
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
	suffix        string
	imageOverride string
	containerName string
	cfg           *config.Config

	syncMu                sync.Mutex
	resolvedWorkspaceMode string
	lastSyncFingerprint   string
}

func NewDocker(projectDir string, cfg *config.Config) *DockerSandbox {
	return NewDockerWithImage(projectDir, cfg, "", "")
}

// NewDockerWithImage constructs a DockerSandbox with an optional image override and deterministic
// name suffix so multiple warm containers per repository coexist without container or cache volume collisions.
func NewDockerWithImage(projectDir string, cfg *config.Config, imageOverride string, suffix string) *DockerSandbox {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	h := ProjectHash8(abs)
	cleanSuffix := strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(suffix), "-"), "-")
	name := ContainerPrefix + h
	if cleanSuffix != "" {
		name = name + "-" + cleanSuffix
	}
	return &DockerSandbox{
		projectDir:    abs,
		hash8:         h,
		suffix:        cleanSuffix,
		imageOverride: strings.TrimSpace(imageOverride),
		containerName: name,
		cfg:           cfg,
	}
}

func (d *DockerSandbox) configuredImage() string {
	if d.imageOverride != "" {
		return d.imageOverride
	}
	if d.cfg != nil {
		return strings.TrimSpace(d.cfg.Sandbox.Image)
	}
	return ""
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
	Name   string `json:"Name"`
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

	wsMode := d.effectiveWorkspaceMode(ctx)

	info, err := d.inspect(ctx)
	if err == nil && info != nil {
		// Recreate if image or workspace mode changed in config
		existingMode := info.Config.Labels["quench.workspace_mode"]
		modeChanged := (existingMode != "" && existingMode != wsMode) ||
			(existingMode == "" && wsMode == config.WorkspaceModeCopy)
		if desiredImg := d.configuredImage(); (desiredImg != "" && info.Config.Image != desiredImg) || modeChanged {
			_ = d.Teardown(ctx)
			info = nil
		}
	}

	if info == nil {
		if err := d.startNewContainer(ctx, wsMode); err != nil {
			return err
		}
		d.syncMu.Lock()
		d.lastSyncFingerprint = ""
		d.syncMu.Unlock()
	} else if !info.State.Running {
		if _, err := dockerOutput(ctx, 15*time.Second, "start", d.containerName); err != nil {
			return fmt.Errorf("start stopped warm container %s: %w", d.containerName, err)
		}
	}

	if wsMode == config.WorkspaceModeCopy {
		if err := d.syncWorkspaceCopyIfChanged(ctx); err != nil {
			return err
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

func (d *DockerSandbox) buildRunArgs(workspaceMode string) []string {
	image := d.configuredImage()
	if image == "" {
		image = "alpine:3.20"
	}

	hostPath := filepath.ToSlash(d.projectDir)
	workspaceMount := fmt.Sprintf("%s:/workspace", hostPath)
	if workspaceMode == config.WorkspaceModeCopy {
		workspaceMount = fmt.Sprintf("%s:/workspace", d.workspaceVolumeName())
	} else {
		workspaceMode = config.WorkspaceModeBind
	}

	args := []string{
		"run", "-d",
		"--name", d.containerName,
		"--label", "quench.managed=true",
		"--label", "quench.project_hash=" + d.hash8,
		"--label", "quench.project_dir=" + hostPath,
		"--label", "quench.stack=" + string(d.cfg.Stack),
		"--label", "quench.idle_timeout=" + d.cfg.Sandbox.ParsedIdleTimeout().String(),
		"--label", "quench.workspace_mode=" + workspaceMode,
		"-e", "CI=true",
		"-e", "NO_COLOR=1",
		"-e", "FORCE_COLOR=0",
	}
	if strings.TrimSpace(d.cfg.Sandbox.User) != "" {
		args = append(args,
			"-e", "HOME=/tmp",
			"-e", "GOMODCACHE=/tmp/quench-gomod",
			"-e", "GOCACHE=/tmp/quench-gocache",
			"-e", "NPM_CONFIG_CACHE=/tmp/quench-npm",
			"-e", "PIP_CACHE_DIR=/tmp/quench-pip",
			"-e", "XDG_CACHE_HOME=/tmp/quench-cache",
		)
	} else {
		args = append(args,
			"-e", "GOMODCACHE=/go/pkg/mod",
			"-e", "GOCACHE=/root/.cache/go-build",
			"-e", "NPM_CONFIG_CACHE=/root/.npm",
			"-e", "PIP_CACHE_DIR=/root/.cache/pip",
		)
	}
	args = append(args,
		"-v", workspaceMount,
		"-w", "/workspace",
	)
	if d.suffix != "" {
		args = append(args, "--label", "quench.image_suffix="+d.suffix)
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

	args = append(args, securityArgs(d.cfg.Sandbox)...)
	args = append(args, resourceArgs(d.cfg.Sandbox)...)
	args = append(args, image, "sleep", "infinity")
	return args
}

func (d *DockerSandbox) startNewContainer(ctx context.Context, workspaceMode string) error {
	image := d.configuredImage()
	if image == "" {
		image = "alpine:3.20"
	}
	args := d.buildRunArgs(workspaceMode)
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
		setupStack := d.cfg.Stack
		if d.imageOverride != "" || setupStack == config.StackPolyglot {
			setupStack = inferStackFromImage(d.configuredImage())
		}
		setupCmd = defaultSetupCommand(d.projectDir, setupStack)
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

func (d *DockerSandbox) Exec(ctx context.Context, workingDir string, command string, timeout time.Duration) (ExecResult, error) {
	if d.effectiveWorkspaceMode(ctx) == config.WorkspaceModeCopy {
		if err := d.syncWorkspaceCopyIfChanged(ctx); err != nil {
			return ExecResult{}, err
		}
	}
	d.touchLastUsed(ctx)
	if workingDir != "" {
		cleanSub := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(workingDir)), "/")
		command = fmt.Sprintf("cd /workspace/%s && %s", cleanSub, command)
	}
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
		Image:         d.configuredImage(),
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
		_, _ = dockerOutput(ctx, 15*time.Second, "volume", "rm", "-f", d.workspaceVolumeName())
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
	if d.suffix != "" {
		return fmt.Sprintf("%s%s-%s-%s", VolumePrefix, d.hash8, d.suffix, slug)
	}
	return fmt.Sprintf("%s%s-%s", VolumePrefix, d.hash8, slug)
}

// ListManagedContainers returns status information for all quench.managed=true
// containers across all repositories on the active Docker daemon.
func ListManagedContainers(ctx context.Context) ([]StatusInfo, error) {
	if err := CheckDaemon(ctx); err != nil {
		return nil, err
	}
	out, err := dockerOutput(ctx, 10*time.Second, "ps", "-a", "--filter", "label=quench.managed=true", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		name = strings.TrimSpace(name)
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return []StatusInfo{}, nil
	}

	inspectArgs := append([]string{"inspect"}, names...)
	rawInspect, err := dockerOutput(ctx, 10*time.Second, inspectArgs...)
	if err != nil {
		return nil, fmt.Errorf("inspect managed containers: %w", err)
	}
	var items []inspectData
	if err := json.Unmarshal([]byte(rawInspect), &items); err != nil {
		return nil, fmt.Errorf("parse docker inspect output: %w", err)
	}

	results := make([]StatusInfo, 0, len(items))
	for i, item := range items {
		fallback := ""
		if i < len(names) {
			fallback = names[i]
		}
		results = append(results, statusInfoFromInspect(item, fallback))
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].ContainerName < results[j].ContainerName
	})
	return results, nil
}

func statusInfoFromInspect(item inspectData, fallbackName string) StatusInfo {
	name := strings.TrimPrefix(strings.TrimSpace(item.Name), "/")
	if name == "" {
		name = fallbackName
	}
	id := item.ID
	if len(id) >= 12 {
		id = id[:12]
	}
	st := StatusInfo{
		Mode:          "docker-warm",
		State:         "stopped",
		ContainerName: name,
		ContainerID:   id,
		Image:         item.Config.Image,
		ProjectDir:    item.Config.Labels["quench.project_dir"],
		Stack:         item.Config.Labels["quench.stack"],
	}
	if item.State.Running {
		st.State = "running"
		if started, err := time.Parse(time.RFC3339Nano, item.State.StartedAt); err == nil {
			st.Uptime = time.Since(started).Round(time.Second).String()
		}
	}
	return st
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
		hasReq := fileExists(filepath.Join(projectDir, "requirements.txt"))
		hasPyproject := fileExists(filepath.Join(projectDir, "pyproject.toml"))
		switch {
		case hasReq && hasPyproject:
			return `pip install -q --disable-pip-version-check -r requirements.txt && (pip install -q --disable-pip-version-check -e ".[dev]" 2>/dev/null || pip install -q --disable-pip-version-check -e . 2>/dev/null || true)`
		case hasReq:
			return "pip install -q --disable-pip-version-check -r requirements.txt"
		case hasPyproject:
			return `pip install -q --disable-pip-version-check -e ".[dev]" 2>/dev/null || pip install -q --disable-pip-version-check -e . 2>/dev/null || true`
		}
	}
	return ""
}

func inferStackFromImage(image string) config.StackType {
	lower := strings.ToLower(strings.TrimSpace(image))
	switch {
	case strings.HasPrefix(lower, "golang:") || strings.Contains(lower, "/golang:"):
		return config.StackGo
	case strings.HasPrefix(lower, "node:") || strings.Contains(lower, "/node:"):
		return config.StackNode
	case strings.HasPrefix(lower, "python:") || strings.Contains(lower, "/python:"):
		return config.StackPython
	case strings.HasPrefix(lower, "rust:") || strings.Contains(lower, "/rust:"):
		return config.StackRust
	default:
		return config.StackGeneric
	}
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

// securityArgs returns the docker run flags that harden container execution
// (dropping all Linux capabilities, preventing privilege escalation, and optional non-root user).
// It is pure so it can be unit-tested without Docker.
func securityArgs(s config.SandboxConfig) []string {
	out := []string{
		"--security-opt", "no-new-privileges:true",
		"--cap-drop", "ALL",
	}
	if u := strings.TrimSpace(s.User); u != "" {
		if strings.EqualFold(u, "nonroot") {
			u = "65532:65532"
		}
		out = append(out, "--user", u)
	}
	return out
}

// IsRemoteDockerContext reports whether the active Docker daemon is remote
// (either via DOCKER_HOST or the active docker context endpoint).
func IsRemoteDockerContext(ctx context.Context) bool {
	if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); host != "" {
		return isRemoteDockerHost(host)
	}
	out, err := dockerOutput(ctx, 3*time.Second, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		return false
	}
	return isRemoteDockerHost(strings.TrimSpace(out))
}

func isRemoteDockerHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	lower := strings.ToLower(h)
	if strings.HasPrefix(lower, "unix://") || strings.HasPrefix(lower, "npipe://") {
		return false
	}
	if strings.HasPrefix(lower, "ssh://") {
		return true
	}
	if strings.HasPrefix(lower, "tcp://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(h)
		if err != nil {
			return false
		}
		hostname := strings.ToLower(strings.TrimSpace(u.Hostname()))
		if hostname == "" || hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" {
			return false
		}
		if ip := net.ParseIP(hostname); ip != nil && ip.IsLoopback() {
			return false
		}
		return true
	}
	return false
}

func resolveWorkspaceMode(ctx context.Context, configured string) string {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case config.WorkspaceModeBind:
		return config.WorkspaceModeBind
	case config.WorkspaceModeCopy:
		return config.WorkspaceModeCopy
	default:
		if IsRemoteDockerContext(ctx) {
			return config.WorkspaceModeCopy
		}
		return config.WorkspaceModeBind
	}
}

func (d *DockerSandbox) effectiveWorkspaceMode(ctx context.Context) string {
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	if d.resolvedWorkspaceMode != "" {
		return d.resolvedWorkspaceMode
	}
	configured := ""
	if d.cfg != nil {
		configured = d.cfg.Sandbox.WorkspaceMode
	}
	d.resolvedWorkspaceMode = resolveWorkspaceMode(ctx, configured)
	return d.resolvedWorkspaceMode
}

func (d *DockerSandbox) workspaceVolumeName() string {
	return WorkspaceVolumePrefix + d.hash8
}

var copySkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"target":       true,
	"dist":         true,
	".venv":        true,
	"__pycache__":  true,
}

func (d *DockerSandbox) copyExclude() []string {
	if d.cfg == nil {
		return nil
	}
	return d.cfg.Sandbox.CopyExclude
}

func (d *DockerSandbox) syncWorkspaceCopyIfChanged(ctx context.Context) error {
	fp := workspaceFingerprint(d.projectDir, d.copyExclude()...)
	d.syncMu.Lock()
	alreadySynced := fp != "" && fp == d.lastSyncFingerprint
	d.syncMu.Unlock()
	if alreadySynced {
		return nil
	}
	if err := d.syncWorkspaceCopy(ctx); err != nil {
		return err
	}
	d.syncMu.Lock()
	d.lastSyncFingerprint = fp
	d.syncMu.Unlock()
	return nil
}

func (d *DockerSandbox) syncWorkspaceCopy(ctx context.Context) error {
	tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var cachePaths []string
	if d.cfg != nil {
		cachePaths = d.cfg.Sandbox.CachePaths
	}
	cleanCmd := workspaceCopyCleanCmd(cachePaths)
	excludes := d.copyExclude()

	pr, pw := io.Pipe()
	tarErrCh := make(chan error, 1)
	go func() {
		err := writeWorkspaceTar(d.projectDir, pw, excludes...)
		_ = pw.CloseWithError(err)
		tarErrCh <- err
	}()

	cmd := exec.CommandContext(tctx, "docker", "exec", "-i", d.containerName, "sh", "-c", cleanCmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = pr
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	_ = pr.Close()
	tarErr := <-tarErrCh

	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = runErr.Error()
		}
		return fmt.Errorf("sync workspace copy to %s: %s", d.containerName, msg)
	}
	if tarErr != nil {
		return fmt.Errorf("archive workspace %s: %w", d.projectDir, tarErr)
	}
	return nil
}

func workspaceCopyCleanCmd(cachePaths []string) string {
	preserveSet := make(map[string]bool)
	var preserve []string
	for _, cp := range cachePaths {
		clean := strings.TrimSpace(cp)
		if !strings.HasPrefix(clean, "/workspace/") {
			continue
		}
		rest := strings.TrimPrefix(clean, "/workspace/")
		seg := strings.SplitN(rest, "/", 2)[0]
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		entry := "/workspace/" + seg
		if !preserveSet[entry] {
			preserveSet[entry] = true
			preserve = append(preserve, entry)
		}
	}
	sort.Strings(preserve)

	var sb strings.Builder
	sb.WriteString(`for p in /workspace/* /workspace/.[!.]* /workspace/..?*; do [ -e "$p" ] || [ -L "$p" ] || continue; `)
	if len(preserve) > 0 {
		sb.WriteString(`case "$p" in `)
		for i, p := range preserve {
			if i > 0 {
				sb.WriteString("|")
			}
			sb.WriteString("'")
			sb.WriteString(strings.ReplaceAll(p, "'", `'\''`))
			sb.WriteString("'")
		}
		sb.WriteString(`) continue ;; esac; `)
	}
	sb.WriteString(`rm -rf "$p" 2>/dev/null || true; done; tar -xf - -C /workspace`)
	return sb.String()
}

func writeWorkspaceTar(projectDir string, w io.Writer, excludes ...string) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(projectDir, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(projectDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if entry.IsDir() {
			if copySkipDirs[entry.Name()] || matchesCopyExclude(entry.Name(), relSlash, excludes) {
				return filepath.SkipDir
			}
		} else {
			if shouldSkipCopyFile(entry.Name()) || matchesCopyExclude(entry.Name(), relSlash, excludes) {
				return nil
			}
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		tarName := relSlash
		mode := info.Mode()

		if mode&fs.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return nil
			}
			hdr, err := tar.FileInfoHeader(info, target)
			if err != nil {
				return err
			}
			hdr.Name = tarName
			return tw.WriteHeader(hdr)
		}

		if info.IsDir() {
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = tarName + "/"
			return tw.WriteHeader(hdr)
		}

		if !mode.IsRegular() {
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = tarName
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := tw.Close(); err == nil {
		err = closeErr
	}
	return err
}

func matchesCopyExclude(name, relSlash string, excludes []string) bool {
	for _, raw := range excludes {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		pSlash := strings.TrimPrefix(filepath.ToSlash(p), "./")
		pTrim := strings.TrimSuffix(pSlash, "/")
		for _, pat := range []string{pSlash, pTrim} {
			if pat == "" {
				continue
			}
			if ok, _ := path.Match(pat, name); ok {
				return true
			}
			if ok, _ := path.Match(pat, relSlash); ok {
				return true
			}
			if ok, _ := filepath.Match(pat, name); ok {
				return true
			}
			if ok, _ := filepath.Match(pat, relSlash); ok {
				return true
			}
		}
	}
	return false
}

func shouldSkipCopyFile(name string) bool {
	lower := strings.ToLower(name)
	if lower == ".env" || strings.HasPrefix(lower, ".env.") {
		switch lower {
		case ".env.example", ".env.sample", ".env.template":
			return false
		default:
			return true
		}
	}
	for _, ext := range []string{".key", ".pem", ".p12", ".pfx"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return lower == "id_rsa" || lower == "id_ed25519"
}

func workspaceFingerprint(projectDir string, excludes ...string) string {
	h := sha256.New()
	err := filepath.WalkDir(projectDir, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(projectDir, p)
		if err != nil || rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if entry.IsDir() {
			if copySkipDirs[entry.Name()] || matchesCopyExclude(entry.Name(), relSlash, excludes) {
				return filepath.SkipDir
			}
		} else {
			if shouldSkipCopyFile(entry.Name()) || matchesCopyExclude(entry.Name(), relSlash, excludes) {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\n",
			relSlash,
			info.Size(),
			info.ModTime().UnixNano(),
			uint32(info.Mode()),
		)
		return nil
	})
	if err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// resourceArgs returns the docker run flags that enforce configured memory/CPU limits.
// It is pure so it can be unit-tested without Docker.
func resourceArgs(s config.SandboxConfig) []string {
	var out []string
	if s.Memory != "" {
		// memory-swap equal to memory disables swap so the limit cannot be bypassed.
		out = append(out, "--memory", s.Memory, "--memory-swap", s.Memory)
	}
	if s.CPUs != "" {
		out = append(out, "--cpus", s.CPUs)
	}
	return out
}

// BestEffortGC launches GarbageCollect(ctx, false) in a background goroutine with a
// 3s timeout. All errors are ignored; it never blocks or fails the caller.
func BestEffortGC() {
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = GarbageCollect(ctx, false)
	}()
}
