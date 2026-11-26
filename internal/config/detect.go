package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	goVersionRe     = regexp.MustCompile(`(?m)^go\s+(1\.\d+)`)
	nodeMajorRe     = regexp.MustCompile(`(\d{2})`)
	pythonVersionRe = regexp.MustCompile(`(3\.\d{1,2})`)
)

// DetectConfig builds a zero-config profile by inspecting manifest files in projectDir.
func DetectConfig(projectDir string) (*Config, error) {
	switch {
	case fileExists(filepath.Join(projectDir, "go.mod")):
		return detectGo(projectDir), nil
	case fileExists(filepath.Join(projectDir, "package.json")):
		return detectNode(projectDir), nil
	case fileExists(filepath.Join(projectDir, "pyproject.toml")) ||
		fileExists(filepath.Join(projectDir, "requirements.txt")) ||
		fileExists(filepath.Join(projectDir, "setup.py")):
		return detectPython(projectDir), nil
	default:
		return nil, fmt.Errorf("could not auto-detect stack in %s (no go.mod, package.json, or pyproject.toml found; run 'quench init' or add quench.yaml)", projectDir)
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
			Parser:     "go-vet",
			Timeout:    "45s",
			Extensions: []string{".go"},
		},
		{
			Name:       "test",
			Command:    "go test -json {{packages}}",
			Scope:      ScopeChangedPackages,
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
			Parser:     "jest",
			Timeout:    "90s",
			Extensions: jsExts,
		})
	case useVitest || (len(pkg.Dependencies) == 0 && len(pkg.DevDependencies) == 0):
		checks = append(checks, CheckConfig{
			Name:       "test",
			Command:    runner + " vitest related --run {{files}}",
			Scope:      ScopeChangedFiles,
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
			Parser:     "mypy",
			Timeout:    "60s",
			Extensions: []string{".py"},
		})
	}
	checks = append(checks, CheckConfig{
		Name:       "test",
		Command:    "pytest -q {{tests}}",
		Scope:      ScopeMappedTests,
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
