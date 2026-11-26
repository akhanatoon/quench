package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	goVersionRe     = regexp.MustCompile(`(?m)^go\s+(1\.\d+)`)
	nodeMajorRe     = regexp.MustCompile(`(\d{2})`)
	pythonVersionRe = regexp.MustCompile(`(3\.\d{1,2})`)
	rustVersionRe   = regexp.MustCompile(`(?m)^rust-version\s*=\s*"(1\.\d+)`)
)

// WorkspaceInfo describes a detected stack rooted at RelDir ("" for repository root).
type WorkspaceInfo struct {
	RelDir string
	Config *Config
}

// DetectConfig builds a zero-config profile by inspecting projectDir and common monorepo subdirectories.
func DetectConfig(projectDir string) (*Config, error) {
	workspaces := DetectWorkspaces(projectDir)
	if len(workspaces) == 0 {
		return nil, fmt.Errorf("could not auto-detect stack in %s (no go.mod, package.json, pyproject.toml, or Cargo.toml found; run 'quench init' or add quench.yaml)", projectDir)
	}
	if len(workspaces) == 1 && workspaces[0].RelDir == "" {
		return workspaces[0].Config, nil
	}

	// Merge multi-workspace / polyglot checks with explicit working_dir
	primary := workspaces[0].Config
	stackSet := make(map[StackType]bool)
	var mergedChecks []CheckConfig
	cacheSeen := make(map[string]bool)
	var mergedCache []string

	for _, ws := range workspaces {
		stackSet[ws.Config.Stack] = true
		for _, cp := range ws.Config.Sandbox.CachePaths {
			if !cacheSeen[cp] {
				cacheSeen[cp] = true
				mergedCache = append(mergedCache, cp)
			}
		}
		prefix := ""
		if ws.RelDir != "" {
			prefix = strings.ReplaceAll(ws.RelDir, "/", "-") + "-"
		}
		for _, ch := range ws.Config.Checks {
			ch.WorkingDir = ws.RelDir
			ch.Stack = ws.Config.Stack
			if prefix != "" {
				ch.Name = prefix + ch.Name
			}
			mergedChecks = append(mergedChecks, ch)
		}
	}

	stack := primary.Stack
	if len(stackSet) > 1 {
		stack = StackPolyglot
	}

	syncDeps := true
	return &Config{
		Version: 1,
		Mode:    ModeDocker,
		Stack:   stack,
		Sandbox: SandboxConfig{
			Image:       primary.Sandbox.Image,
			IdleTimeout: "30m",
			Network:     "bridge",
			SyncDeps:    &syncDeps,
			CachePaths:  mergedCache,
		},
		Checks: mergedChecks,
	}, nil
}

// DetectWorkspaces scans the repository root and common workspace subdirectories for supported stacks.
func DetectWorkspaces(projectDir string) []WorkspaceInfo {
	var found []WorkspaceInfo
	seenDirs := make(map[string]bool)

	tryDir := func(rel string) {
		rel = filepath.ToSlash(filepath.Clean(rel))
		if rel == "." {
			rel = ""
		}
		if seenDirs[rel] {
			return
		}
		full := projectDir
		if rel != "" {
			full = filepath.Join(projectDir, filepath.FromSlash(rel))
		}
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			return
		}
		if cfg := detectSingleDir(full); cfg != nil {
			seenDirs[rel] = true
			found = append(found, WorkspaceInfo{RelDir: rel, Config: cfg})
		}
	}

	// 1. Repository root
	tryDir("")

	// 2. Common direct sub-projects
	directNames := []string{
		"backend", "frontend", "web", "api", "core", "server", "client", "src-tauri", "cli",
	}
	for _, name := range directNames {
		tryDir(name)
	}

	// 3. Monorepo container folders (1 level inside apps/, services/, packages/)
	for _, group := range []string{"apps", "services", "packages"} {
		entries, err := os.ReadDir(filepath.Join(projectDir, group))
		if err != nil {
			continue
		}
		var subNames []string
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				subNames = append(subNames, e.Name())
			}
		}
		sort.Strings(subNames)
		for _, name := range subNames {
			tryDir(group + "/" + name)
		}
	}

	return found
}

func detectSingleDir(dir string) *Config {
	switch {
	case fileExists(filepath.Join(dir, "go.mod")):
		return detectGo(dir)
	case fileExists(filepath.Join(dir, "Cargo.toml")):
		return detectRust(dir)
	case fileExists(filepath.Join(dir, "package.json")):
		return detectNode(dir)
	case fileExists(filepath.Join(dir, "pyproject.toml")) ||
		fileExists(filepath.Join(dir, "requirements.txt")) ||
		fileExists(filepath.Join(dir, "setup.py")):
		return detectPython(dir)
	default:
		return nil
	}
}

func detectGo(projectDir string) *Config {
	ver := "1.24"
	if data, err := os.ReadFile(filepath.Join(projectDir, "go.mod")); err == nil {
		if m := goVersionRe.FindSubmatch(data); len(m) == 2 {
			ver = string(m[1])
		}
	}
	syncDeps := true
	checks := []CheckConfig{
		{
			Name:       "vet",
			Command:    "go vet {{packages}}",
			Scope:      ScopeChangedPackages,
			Stack:      StackGo,
			Parser:     "go-vet",
			Timeout:    "45s",
			Extensions: []string{".go"},
		},
		{
			Name:       "test",
			Command:    "go test -json {{packages}}",
			Scope:      ScopeChangedPackages,
			Stack:      StackGo,
			Parser:     "go-test",
			Timeout:    "120s",
			Extensions: []string{".go"},
		},
	}
	if fileExists(filepath.Join(projectDir, ".golangci.yml")) || fileExists(filepath.Join(projectDir, ".golangci.yaml")) {
		checks = append([]CheckConfig{{
			Name:       "lint",
			Command:    "golangci-lint run {{packages}}",
			Scope:      ScopeChangedPackages,
			Stack:      StackGo,
			Parser:     "golangci-lint",
			Timeout:    "60s",
			Extensions: []string{".go"},
		}}, checks...)
	}

	return &Config{
		Version: 1,
		Mode:    ModeDocker,
		Stack:   StackGo,
		Sandbox: SandboxConfig{
			Image:       fmt.Sprintf("golang:%s-alpine", ver),
			IdleTimeout: "30m",
			Network:     "bridge",
			SyncDeps:    &syncDeps,
			CachePaths: []string{
				"/go/pkg/mod",
				"/root/.cache/go-build",
			},
		},
		Checks: checks,
	}
}

func detectRust(projectDir string) *Config {
	ver := "1.85"
	if data, err := os.ReadFile(filepath.Join(projectDir, "Cargo.toml")); err == nil {
		if m := rustVersionRe.FindSubmatch(data); len(m) == 2 {
			ver = string(m[1])
		}
	}
	syncDeps := true
	return &Config{
		Version: 1,
		Mode:    ModeDocker,
		Stack:   StackRust,
		Sandbox: SandboxConfig{
			Image:       fmt.Sprintf("rust:%s-slim", ver),
			IdleTimeout: "30m",
			Network:     "bridge",
			SyncDeps:    &syncDeps,
			CachePaths: []string{
				"/usr/local/cargo/registry",
				"/workspace/target",
			},
		},
		Checks: []CheckConfig{
			{
				Name:       "check",
				Command:    "cargo check --message-format=json",
				Scope:      ScopeProject,
				Stack:      StackRust,
				Parser:     "rust-cargo",
				Timeout:    "120s",
				Extensions: []string{".rs", ".toml"},
			},
			{
				Name:       "test",
				Command:    "cargo test --no-fail-fast",
				Scope:      ScopeProject,
				Stack:      StackRust,
				Parser:     "rust-test",
				Timeout:    "180s",
				Extensions: []string{".rs", ".toml"},
			},
		},
	}
}

type packageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Engines         struct {
		Node string `json:"node"`
	} `json:"engines"`
}

func (p packageJSON) hasDep(name string) bool {
	if _, ok := p.Dependencies[name]; ok {
		return true
	}
	if _, ok := p.DevDependencies[name]; ok {
		return true
	}
	return false
}

func detectNode(projectDir string) *Config {
	var pkg packageJSON
	if data, err := os.ReadFile(filepath.Join(projectDir, "package.json")); err == nil {
		_ = json.Unmarshal(data, &pkg)
	}

	nodeMajor := "22"
	for _, vf := range []string{".nvmrc", ".node-version"} {
		if raw, err := os.ReadFile(filepath.Join(projectDir, vf)); err == nil {
			if m := nodeMajorRe.FindStringSubmatch(strings.TrimSpace(string(raw))); len(m) == 2 {
				nodeMajor = m[1]
				break
			}
		}
	}
	if nodeMajor == "22" && pkg.Engines.Node != "" {
		if m := nodeMajorRe.FindStringSubmatch(pkg.Engines.Node); len(m) == 2 {
			nodeMajor = m[1]
		}
	}

	runner := "npx"
	switch {
	case fileExists(filepath.Join(projectDir, "pnpm-lock.yaml")):
		runner = "pnpm exec"
	case fileExists(filepath.Join(projectDir, "yarn.lock")):
		runner = "yarn"
	case fileExists(filepath.Join(projectDir, "bun.lock")) || fileExists(filepath.Join(projectDir, "bun.lockb")):
		runner = "bunx"
	}

	hasTS := fileExists(filepath.Join(projectDir, "tsconfig.json")) || pkg.hasDep("typescript")
	jsExts := []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs"}
	var checks []CheckConfig

	if hasTS {
		checks = append(checks, CheckConfig{
			Name:       "typecheck",
			Command:    runner + " tsc --noEmit",
			Scope:      ScopeProject,
			Stack:      StackNode,
			Parser:     "tsc",
			Timeout:    "45s",
			Extensions: []string{".ts", ".tsx", ".mts", ".cts"},
		})
	}

	hasESLintCfg := hasAnyGlob(projectDir, "eslint.config.*", ".eslintrc*")
	if pkg.hasDep("eslint") || hasESLintCfg || (len(pkg.Dependencies) == 0 && len(pkg.DevDependencies) == 0) {
		checks = append(checks, CheckConfig{
			Name:       "lint",
			Command:    runner + " eslint {{files}}",
			Scope:      ScopeChangedFiles,
			Stack:      StackNode,
			Parser:     "eslint",
			Timeout:    "45s",
			Extensions: jsExts,
		})
	}

	useJest := pkg.hasDep("jest") || hasAnyGlob(projectDir, "jest.config.*")
	useVitest := pkg.hasDep("vitest") || hasAnyGlob(projectDir, "vitest.config.*")
	switch {
	case useJest && !useVitest:
		checks = append(checks, CheckConfig{
			Name:       "test",
			Command:    runner + " jest --findRelatedTests {{files}} --passWithNoTests",
			Scope:      ScopeChangedFiles,
			Stack:      StackNode,
			Parser:     "jest",
			Timeout:    "90s",
			Extensions: jsExts,
		})
	case useVitest || (len(pkg.Dependencies) == 0 && len(pkg.DevDependencies) == 0):
		checks = append(checks, CheckConfig{
			Name:       "test",
			Command:    runner + " vitest related --run {{files}}",
			Scope:      ScopeChangedFiles,
			Stack:      StackNode,
			Parser:     "vitest",
			Timeout:    "90s",
			Extensions: jsExts,
		})
	}

	if len(checks) == 0 {
		checks = append(checks, CheckConfig{
			Name:       "test",
			Command:    "npm test",
			Scope:      ScopeProject,
			Stack:      StackNode,
			Parser:     "generic",
			Timeout:    "90s",
			Extensions: jsExts,
		})
	}

	syncDeps := true
	return &Config{
		Version: 1,
		Mode:    ModeDocker,
		Stack:   StackNode,
		Sandbox: SandboxConfig{
			Image:       fmt.Sprintf("node:%s-alpine", nodeMajor),
			IdleTimeout: "30m",
			Network:     "bridge",
			SyncDeps:    &syncDeps,
			CachePaths: []string{
				"/root/.npm",
				"/workspace/node_modules",
			},
		},
		Checks: checks,
	}
}

func detectPython(projectDir string) *Config {
	pyVer := "3.12"
	if raw, err := os.ReadFile(filepath.Join(projectDir, ".python-version")); err == nil {
		if m := pythonVersionRe.FindStringSubmatch(strings.TrimSpace(string(raw))); len(m) == 2 {
			pyVer = m[1]
		}
	}
	pyprojectRaw, _ := os.ReadFile(filepath.Join(projectDir, "pyproject.toml"))
	pyprojectText := string(pyprojectRaw)
	if pyVer == "3.12" && pyprojectText != "" {
		for _, line := range strings.Split(pyprojectText, "\n") {
			if strings.Contains(line, "requires-python") {
				if m := pythonVersionRe.FindStringSubmatch(line); len(m) == 2 {
					pyVer = m[1]
					break
				}
			}
		}
	}

	checks := []CheckConfig{
		{
			Name:       "lint",
			Command:    "ruff check {{files}}",
			Scope:      ScopeChangedFiles,
			Stack:      StackPython,
			Parser:     "ruff",
			Timeout:    "30s",
			Extensions: []string{".py"},
		},
	}
	if strings.Contains(pyprojectText, "[tool.mypy]") || fileExists(filepath.Join(projectDir, "mypy.ini")) {
		checks = append(checks, CheckConfig{
			Name:       "typecheck",
			Command:    "mypy {{files}}",
			Scope:      ScopeChangedFiles,
			Stack:      StackPython,
			Parser:     "mypy",
			Timeout:    "60s",
			Extensions: []string{".py"},
		})
	}
	checks = append(checks, CheckConfig{
		Name:       "test",
		Command:    "pytest -q {{tests}}",
		Scope:      ScopeMappedTests,
		Stack:      StackPython,
		Parser:     "pytest",
		Timeout:    "90s",
		Extensions: []string{".py"},
	})

	syncDeps := true
	return &Config{
		Version: 1,
		Mode:    ModeDocker,
		Stack:   StackPython,
		Sandbox: SandboxConfig{
			Image:       fmt.Sprintf("python:%s-slim", pyVer),
			IdleTimeout: "30m",
			Network:     "bridge",
			SyncDeps:    &syncDeps,
			CachePaths: []string{
				"/root/.cache/pip",
				"/root/.cache/uv",
			},
		},
		Checks: checks,
	}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func hasAnyGlob(dir string, patterns ...string) bool {
	for _, pat := range patterns {
		matches, err := filepath.Glob(filepath.Join(dir, pat))
		if err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}
