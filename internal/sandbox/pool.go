package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/akhanatoon/quench/internal/config"
)

// ImageAwareSandbox extends Sandbox with per-image execution for polyglot repositories.
type ImageAwareSandbox interface {
	Sandbox
	ExecWithImage(ctx context.Context, workingDir string, image string, command string, timeout time.Duration) (ExecResult, error)
}

type poolEntry struct {
	mu     sync.Mutex
	sb     *DockerSandbox
	warmed bool
}

// DockerPool manages a primary warm DockerSandbox plus lazy per-image secondary sandboxes
// for polyglot repositories whose checks target different container images.
type DockerPool struct {
	projectDir string
	cfg        *config.Config
	primary    *poolEntry

	mu        sync.Mutex
	secondary map[string]*poolEntry
}

var (
	_ Sandbox           = (*DockerPool)(nil)
	_ ImageAwareSandbox = (*DockerPool)(nil)
	_ EnvAwareSandbox   = (*DockerPool)(nil)
)

// ImageHash4 computes a deterministic 4-hex-character suffix for a container image name.
func ImageHash4(image string) string {
	norm := strings.ToLower(strings.TrimSpace(image))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])[:4]
}

// NewDockerPool creates a multi-container pool backed by a primary warm container
// (`quench-warm-<hash8>`) and lazily initialized per-image secondary containers
// (`quench-warm-<hash8>-<img4>`).
func NewDockerPool(projectDir string, cfg *config.Config) *DockerPool {
	return &DockerPool{
		projectDir: projectDir,
		cfg:        cfg,
		primary:    &poolEntry{sb: NewDocker(projectDir, cfg)},
		secondary:  make(map[string]*poolEntry),
	}
}

func (p *DockerPool) ModeName() string {
	return p.primary.sb.ModeName()
}

// ContainerName returns the primary warm container name.
func (p *DockerPool) ContainerName() string {
	return p.primary.sb.ContainerName()
}

func (p *DockerPool) primaryImage() string {
	if p.cfg == nil {
		return ""
	}
	return strings.TrimSpace(p.cfg.Sandbox.Image)
}

func (p *DockerPool) entryForImage(image string) *poolEntry {
	img := strings.TrimSpace(image)
	if img == "" || img == p.primaryImage() {
		return p.primary
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.secondary[img]; ok {
		return entry
	}
	entry := &poolEntry{
		sb: NewDockerWithImage(p.projectDir, p.cfg, img, ImageHash4(img)),
	}
	p.secondary[img] = entry
	return entry
}

// SandboxForImage returns the primary or deterministic secondary DockerSandbox for image.
func (p *DockerPool) SandboxForImage(image string) *DockerSandbox {
	return p.entryForImage(image).sb
}

// AllSandboxes returns the primary sandbox followed by all distinct secondary sandboxes
// referenced in cfg.Checks or previously created in the pool.
func (p *DockerPool) AllSandboxes() []*DockerSandbox {
	entries := p.allEntries()
	out := make([]*DockerSandbox, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.sb)
	}
	return out
}

// ContainerNames returns the container names for the primary and all configured/active secondary sandboxes.
func (p *DockerPool) ContainerNames() []string {
	sbs := p.AllSandboxes()
	names := make([]string, 0, len(sbs))
	for _, sb := range sbs {
		names = append(names, sb.ContainerName())
	}
	return names
}

func (p *DockerPool) allEntries() []*poolEntry {
	entries := []*poolEntry{p.primary}
	seen := map[string]bool{p.primaryImage(): true, "": true}

	if p.cfg != nil {
		for _, ch := range p.cfg.Checks {
			img := strings.TrimSpace(ch.Image)
			if img == "" || seen[img] {
				continue
			}
			seen[img] = true
			entries = append(entries, p.entryForImage(img))
		}
	}

	p.mu.Lock()
	for img, entry := range p.secondary {
		if !seen[img] {
			seen[img] = true
			entries = append(entries, entry)
		}
	}
	p.mu.Unlock()

	return entries
}

func (p *DockerPool) ensureEntryWarm(ctx context.Context, entry *poolEntry) error {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.warmed {
		return nil
	}
	if err := entry.sb.EnsureWarm(ctx); err != nil {
		return err
	}
	entry.warmed = true
	return nil
}

// EnsureWarm warms the primary sandbox container. Secondary image containers are lazily warmed on demand.
func (p *DockerPool) EnsureWarm(ctx context.Context) error {
	return p.ensureEntryWarm(ctx, p.primary)
}

func (p *DockerPool) Exec(ctx context.Context, workingDir string, command string, timeout time.Duration) (ExecResult, error) {
	return p.ExecWithImage(ctx, workingDir, "", command, timeout)
}

func (p *DockerPool) ExecWithImage(ctx context.Context, workingDir string, image string, command string, timeout time.Duration) (ExecResult, error) {
	return p.ExecWithEnv(ctx, workingDir, image, command, nil, timeout)
}

func (p *DockerPool) ExecWithEnv(ctx context.Context, workingDir string, image string, command string, env map[string]string, timeout time.Duration) (ExecResult, error) {
	entry := p.entryForImage(image)
	if err := p.ensureEntryWarm(ctx, entry); err != nil {
		return ExecResult{}, err
	}
	return entry.sb.ExecWithEnv(ctx, workingDir, image, command, env, timeout)
}

// Status reports the primary sandbox status along with cache volumes across all pool sandboxes.
func (p *DockerPool) Status(ctx context.Context) (StatusInfo, error) {
	st, err := p.primary.sb.Status(ctx)
	if err != nil {
		return st, err
	}

	entries := p.allEntries()
	if len(entries) > 1 {
		volSeen := make(map[string]bool, len(st.CacheVolumes))
		for _, v := range st.CacheVolumes {
			volSeen[v] = true
		}
		for _, entry := range entries[1:] {
			secStatus, secErr := entry.sb.Status(ctx)
			if secErr != nil {
				continue
			}
			for _, v := range secStatus.CacheVolumes {
				if !volSeen[v] {
					volSeen[v] = true
					st.CacheVolumes = append(st.CacheVolumes, v)
				}
			}
		}
	}
	return st, nil
}

// AllStatuses returns status information for the primary and every secondary sandbox in the pool.
func (p *DockerPool) AllStatuses(ctx context.Context) ([]StatusInfo, error) {
	entries := p.allEntries()
	statuses := make([]StatusInfo, 0, len(entries))
	for i, entry := range entries {
		st, err := entry.sb.Status(ctx)
		if err != nil {
			if i == 0 {
				return append(statuses, st), err
			}
			continue
		}
		statuses = append(statuses, st)
	}
	return statuses, nil
}

// Reset tears down the primary and all secondary containers (and optionally purges their cache volumes).
func (p *DockerPool) Reset(ctx context.Context, purgeCache bool) error {
	if err := CheckDaemon(ctx); err != nil {
		return err
	}
	var firstErr error
	for _, entry := range p.allEntries() {
		entry.mu.Lock()
		entry.warmed = false
		err := entry.sb.Reset(ctx, purgeCache)
		entry.mu.Unlock()
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Teardown stops and removes the primary and all secondary containers in the pool.
func (p *DockerPool) Teardown(ctx context.Context) error {
	if err := CheckDaemon(ctx); err != nil {
		return err
	}
	var firstErr error
	for _, entry := range p.allEntries() {
		entry.mu.Lock()
		entry.warmed = false
		err := entry.sb.Teardown(ctx)
		entry.mu.Unlock()
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
