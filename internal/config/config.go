package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ConfigFileName    = "quench.yaml"
	AltConfigFileName = ".quench.yaml"

	ModeDocker = "docker"
	ModeLocal  = "local"

	ScopeProject         = "project"
	ScopeChangedFiles    = "changed_files"
	ScopeChangedPackages = "changed_packages"
	ScopeMappedTests     = "mapped_tests"
)

type StackType string

const (
	StackGo      StackType = "go"
	StackNode    StackType = "node"
	StackPython  StackType = "python"
	StackGeneric StackType = "generic"
)

type Config struct {
	Version int           `yaml:"version" json:"version"`
	Mode    string        `yaml:"mode" json:"mode"`
	Stack   StackType     `yaml:"stack,omitempty" json:"stack"`
	Sandbox SandboxConfig `yaml:"sandbox" json:"sandbox"`
	Checks  []CheckConfig `yaml:"checks" json:"checks"`
}

type SandboxConfig struct {
	Image        string   `yaml:"image" json:"image"`
	IdleTimeout  string   `yaml:"idle_timeout,omitempty" json:"idle_timeout,omitempty"`
	Network      string   `yaml:"network,omitempty" json:"network,omitempty"`
	SyncDeps     *bool    `yaml:"sync_deps,omitempty" json:"sync_deps,omitempty"`
	SetupCommand string   `yaml:"setup_command,omitempty" json:"setup_command,omitempty"`
	CachePaths   []string `yaml:"cache_paths,omitempty" json:"cache_paths,omitempty"`
}

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
	Name       string   `yaml:"name" json:"name"`
	Command    string   `yaml:"command" json:"command"`
	Scope      string   `yaml:"scope" json:"scope"`
	Parser     string   `yaml:"parser,omitempty" json:"parser,omitempty"`
	Timeout    string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Extensions []string `yaml:"extensions,omitempty" json:"extensions,omitempty"`
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

// Load reads quench.yaml (or .quench.yaml) if present; otherwise zero-config detects the project stack.
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
	return nil
}

func MarshalYAML(cfg *Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}
