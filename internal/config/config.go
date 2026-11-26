package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ConfigFileName    = "quench.yaml"
	AltConfigFileName = ".quench.yaml"

	ModeDocker = "docker"
	ModeLocal  = "local"

	WorkspaceModeAuto = "auto"
	WorkspaceModeBind = "bind"
	WorkspaceModeCopy = "copy"

	ScopeProject         = "project"
	ScopeChangedFiles    = "changed_files"
	ScopeChangedPackages = "changed_packages"
	ScopeMappedTests     = "mapped_tests"
)

type StackType string

const (
	StackGo       StackType = "go"
	StackNode     StackType = "node"
	StackPython   StackType = "python"
	StackRust     StackType = "rust"
	StackPolyglot StackType = "polyglot"
	StackGeneric  StackType = "generic"
)

type Config struct {
	Version int           `yaml:"version" json:"version"`
	Mode    string        `yaml:"mode" json:"mode"`
	Stack   StackType     `yaml:"stack,omitempty" json:"stack"`
	Sandbox SandboxConfig `yaml:"sandbox" json:"sandbox"`
	Checks  []CheckConfig `yaml:"checks" json:"checks"`

	// MaxParallel caps concurrently executing checks within a wave (0 = unlimited).
	MaxParallel int `yaml:"max_parallel,omitempty" json:"max_parallel,omitempty"`
}

type SandboxConfig struct {
	Image         string   `yaml:"image" json:"image"`
	IdleTimeout   string   `yaml:"idle_timeout,omitempty" json:"idle_timeout,omitempty"`
	Network       string   `yaml:"network,omitempty" json:"network,omitempty"`
	SyncDeps      *bool    `yaml:"sync_deps,omitempty" json:"sync_deps,omitempty"`
	SetupCommand  string   `yaml:"setup_command,omitempty" json:"setup_command,omitempty"`
	CachePaths    []string `yaml:"cache_paths,omitempty" json:"cache_paths,omitempty"`
	Memory        string   `yaml:"memory,omitempty" json:"memory,omitempty"`
	CPUs          string   `yaml:"cpus,omitempty" json:"cpus,omitempty"`
	WorkspaceMode string   `yaml:"workspace_mode,omitempty" json:"workspace_mode,omitempty"`
	User          string   `yaml:"user,omitempty" json:"user,omitempty"`
}

var memoryPattern = regexp.MustCompile(`(?i)^[0-9]+(b|k|m|g)?$`)

func (s SandboxConfig) ShouldSyncDeps() bool {
	if s.SyncDeps == nil {
		return true
	}
	return *s.SyncDeps
}

func (s SandboxConfig) ParsedIdleTimeout() time.Duration {
	if s.IdleTimeout == "" {
		return 30 * time.Minute
	}
	d, err := time.ParseDuration(s.IdleTimeout)
	if err != nil || d <= 0 {
		return 30 * time.Minute
	}
	return d
}

type CheckConfig struct {
	Name       string    `yaml:"name" json:"name"`
	Command    string    `yaml:"command" json:"command"`
	Scope      string    `yaml:"scope" json:"scope"`
	Stack      StackType `yaml:"stack,omitempty" json:"stack,omitempty"`
	Image      string    `yaml:"image,omitempty" json:"image,omitempty"`
	WorkingDir string    `yaml:"working_dir,omitempty" json:"working_dir,omitempty"`
	Parser     string    `yaml:"parser,omitempty" json:"parser,omitempty"`
	Timeout    string    `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Extensions []string  `yaml:"extensions,omitempty" json:"extensions,omitempty"`
	DependsOn  []string  `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
}

func (c CheckConfig) ParsedTimeout() time.Duration {
	if c.Timeout == "" {
		return 60 * time.Second
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil || d <= 0 {
		return 60 * time.Second
	}
	return d
}

// Load reads quench.yaml (or .quench.yaml) if present; otherwise zero-config detects the project stack(s).
// Returns (config, loadedFromFile, error).
func Load(projectDir string) (*Config, bool, error) {
	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, false, fmt.Errorf("resolve project dir: %w", err)
	}

	for _, name := range []string{ConfigFileName, AltConfigFileName} {
		cfgPath := filepath.Join(absDir, name)
		data, readErr := os.ReadFile(cfgPath)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return nil, false, fmt.Errorf("read %s: %w", name, readErr)
		}

		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, true, fmt.Errorf("parse %s: %w", name, err)
		}
		applyDefaults(&cfg, absDir)
		if err := Validate(&cfg); err != nil {
			return nil, true, fmt.Errorf("invalid %s: %w", name, err)
		}
		return &cfg, true, nil
	}

	detected, err := DetectConfig(absDir)
	if err != nil {
		return nil, false, err
	}
	return detected, false, nil
}

func applyDefaults(cfg *Config, projectDir string) {
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeDocker
	}
	detected, _ := DetectConfig(projectDir)
	if cfg.Stack == "" {
		if detected != nil {
			cfg.Stack = detected.Stack
		} else {
			cfg.Stack = StackGeneric
		}
	}
	if cfg.Sandbox.Image == "" && detected != nil {
		cfg.Sandbox.Image = detected.Sandbox.Image
	}
	if cfg.Sandbox.IdleTimeout == "" {
		cfg.Sandbox.IdleTimeout = "30m"
	}
	if cfg.Sandbox.Network == "" {
		cfg.Sandbox.Network = "bridge"
	}
	if cfg.Sandbox.WorkspaceMode == "" {
		cfg.Sandbox.WorkspaceMode = WorkspaceModeAuto
	}
	if len(cfg.Sandbox.CachePaths) == 0 && detected != nil {
		cfg.Sandbox.CachePaths = detected.Sandbox.CachePaths
	}
	if len(cfg.Checks) == 0 && detected != nil {
		cfg.Checks = detected.Checks
	}
	for i := range cfg.Checks {
		if cfg.Checks[i].Scope == "" {
			cfg.Checks[i].Scope = ScopeProject
		}
		if cfg.Checks[i].Timeout == "" {
			cfg.Checks[i].Timeout = "60s"
		}
		if cfg.Checks[i].WorkingDir != "" {
			cfg.Checks[i].WorkingDir = filepath.ToSlash(filepath.Clean(cfg.Checks[i].WorkingDir))
			if cfg.Checks[i].WorkingDir == "." {
				cfg.Checks[i].WorkingDir = ""
			}
		}
	}
}

func Validate(cfg *Config) error {
	if cfg.Mode != ModeDocker && cfg.Mode != ModeLocal {
		return fmt.Errorf("unsupported mode %q (expected %q or %q)", cfg.Mode, ModeDocker, ModeLocal)
	}
	if cfg.Sandbox.IdleTimeout != "" {
		if _, err := time.ParseDuration(cfg.Sandbox.IdleTimeout); err != nil {
			return fmt.Errorf("invalid sandbox.idle_timeout %q: %w", cfg.Sandbox.IdleTimeout, err)
		}
	}
	if cfg.Sandbox.Network != "" && cfg.Sandbox.Network != "bridge" && cfg.Sandbox.Network != "none" && cfg.Sandbox.Network != "host" {
		return fmt.Errorf("unsupported sandbox.network %q (expected bridge, none, or host)", cfg.Sandbox.Network)
	}
	if cfg.Sandbox.WorkspaceMode != "" &&
		cfg.Sandbox.WorkspaceMode != WorkspaceModeAuto &&
		cfg.Sandbox.WorkspaceMode != WorkspaceModeBind &&
		cfg.Sandbox.WorkspaceMode != WorkspaceModeCopy {
		return fmt.Errorf("unsupported sandbox.workspace_mode %q (expected auto, bind, or copy)", cfg.Sandbox.WorkspaceMode)
	}
	if cfg.Sandbox.Memory != "" && !memoryPattern.MatchString(cfg.Sandbox.Memory) {
		return fmt.Errorf("invalid sandbox.memory %q (expected e.g. 512m or 1g)", cfg.Sandbox.Memory)
	}
	if cfg.Sandbox.CPUs != "" {
		v, err := strconv.ParseFloat(cfg.Sandbox.CPUs, 64)
		if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("invalid sandbox.cpus %q (expected a positive number)", cfg.Sandbox.CPUs)
		}
	}
	if cfg.MaxParallel < 0 {
		return fmt.Errorf("invalid max_parallel %d (must be >= 0)", cfg.MaxParallel)
	}
	if len(cfg.Checks) == 0 {
		return errors.New("at least one check must be defined")
	}

	validScopes := []string{ScopeProject, ScopeChangedFiles, ScopeChangedPackages, ScopeMappedTests}
	seen := make(map[string]bool, len(cfg.Checks))
	for _, ch := range cfg.Checks {
		if ch.Name == "" {
			return errors.New("check name cannot be empty")
		}
		if seen[ch.Name] {
			return fmt.Errorf("duplicate check name %q", ch.Name)
		}
		seen[ch.Name] = true
		if ch.Command == "" {
			return fmt.Errorf("check %q command cannot be empty", ch.Name)
		}
		if !slices.Contains(validScopes, ch.Scope) {
			return fmt.Errorf("check %q has invalid scope %q", ch.Name, ch.Scope)
		}
		if ch.Timeout != "" {
			if _, err := time.ParseDuration(ch.Timeout); err != nil {
				return fmt.Errorf("check %q has invalid timeout %q: %w", ch.Name, ch.Timeout, err)
			}
		}
	}

	depsByName := make(map[string][]string, len(cfg.Checks))
	for _, ch := range cfg.Checks {
		for _, dep := range ch.DependsOn {
			if dep == "" {
				return fmt.Errorf("check %q has empty depends_on entry", ch.Name)
			}
			if dep == ch.Name {
				return fmt.Errorf("check %q cannot depend on itself (dependency cycle)", ch.Name)
			}
			if !seen[dep] {
				return fmt.Errorf("check %q depends_on unknown check %q", ch.Name, dep)
			}
		}
		depsByName[ch.Name] = ch.DependsOn
	}

	// 3-color DFS cycle detection (0=unvisited, 1=visiting, 2=visited).
	const (
		colorWhite = 0
		colorGray  = 1
		colorBlack = 2
	)
	color := make(map[string]int, len(cfg.Checks))
	var visit func(name string) error
	visit = func(name string) error {
		color[name] = colorGray
		for _, dep := range depsByName[name] {
			switch color[dep] {
			case colorGray:
				return fmt.Errorf("dependency cycle detected: %q -> %q", name, dep)
			case colorWhite:
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		color[name] = colorBlack
		return nil
	}
	for _, ch := range cfg.Checks {
		if color[ch.Name] == colorWhite {
			if err := visit(ch.Name); err != nil {
				return err
			}
		}
	}

	return nil
}

func MarshalYAML(cfg *Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}
