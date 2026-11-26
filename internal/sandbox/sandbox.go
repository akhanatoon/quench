package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Combined string
	TimedOut bool
}

type StatusInfo struct {
	Mode          string   `json:"mode"`
	State         string   `json:"state"` // running | stopped | absent | host
	ContainerName string   `json:"container_name,omitempty"`
	ContainerID   string   `json:"container_id,omitempty"`
	Image         string   `json:"image,omitempty"`
	Uptime        string   `json:"uptime,omitempty"`
	ProjectDir    string   `json:"project_dir"`
	Stack         string   `json:"stack"`
	CacheVolumes  []string `json:"cache_volumes,omitempty"`
	DepsSynced    bool     `json:"deps_synced"`
	LastUsed      string   `json:"last_used,omitempty"`
}

type Sandbox interface {
	ModeName() string
	EnsureWarm(ctx context.Context) error
	Exec(ctx context.Context, workingDir string, command string, timeout time.Duration) (ExecResult, error)
	Status(ctx context.Context) (StatusInfo, error)
	Reset(ctx context.Context, purgeCache bool) error
	Teardown(ctx context.Context) error
}

// ProjectHash8 computes a deterministic 8-hex-character identifier for a project directory.
func ProjectHash8(projectDir string) string {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	norm := filepath.ToSlash(filepath.Clean(abs))
	if runtime.GOOS == "windows" {
		norm = strings.ToLower(norm)
	}
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])[:8]
}
