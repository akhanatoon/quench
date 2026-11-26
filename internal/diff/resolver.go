package diff

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/akhanatoon/quench/internal/config"
)

type ResolvedCheck struct {
	Command         string
	WorkingDir      string
	Skip            bool
	SkipReason      string
	FullSuite       bool
	FullSuiteReason string
	MatchedFiles    []string
	TargetPackages  []string
	TargetTests     []string
}

var globalCriticalFiles = map[string]bool{
	"quench.yaml":  true,
	".quench.yaml": true,
	"Dockerfile":   true,
}

var stackCriticalFiles = map[config.StackType]map[string]bool{
	config.StackGo: {
		"go.mod":         true,
		"go.sum":         true,
		".golangci.yml":  true,
		".golangci.yaml": true,
	},
	config.StackRust: {
		"Cargo.toml": true,
		"Cargo.lock": true,
	},
	config.StackNode: {
		"package.json":       true,
		"package-lock.json":  true,
		"pnpm-lock.yaml":     true,
		"yarn.lock":          true,
		"bun.lock":           true,
		"bun.lockb":          true,
		"tsconfig.json":      true,
		"tsconfig.base.json": true,
	},
	config.StackPython: {
		"pyproject.toml":   true,
		"requirements.txt": true,
		"uv.lock":          true,
		"poetry.lock":      true,
		"pytest.ini":       true,
		"conftest.py":      true,
		"ruff.toml":        true,
		".ruff.toml":       true,
		"mypy.ini":         true,
	},
}

// CheckCriticalTrigger reports whether any changed file forces a full-suite verification run.
func CheckCriticalTrigger(stack config.StackType, changedFiles []string) (bool, string) {
	stackMap := stackCriticalFiles[stack]
	for _, f := range changedFiles {
		base := path.Base(f)
		if globalCriticalFiles[f] || globalCriticalFiles[base] {
			return true, f
		}
		if stackMap != nil && (stackMap[f] || stackMap[base]) {
			return true, f
		}
	}
	return false, ""
}

// Resolve builds the concrete command to execute (or marks Skip=true) for a check given changedFiles.
func Resolve(repoDir string, stack config.StackType, check config.CheckConfig, changedFiles []string) ResolvedCheck {
	return ResolveWithDeleted(repoDir, stack, check, changedFiles, nil)
}

// ResolveWithDeleted builds the concrete command to execute (or marks Skip=true) for a check
// given both existing changedFiles and deletedFiles.
func ResolveWithDeleted(repoDir string, stack config.StackType, check config.CheckConfig, changedFiles []string, deletedFiles []string) ResolvedCheck {
	if len(changedFiles) == 0 && len(deletedFiles) == 0 {
		return ResolvedCheck{
			WorkingDir: check.WorkingDir,
			Skip:       true,
			SkipReason: "no changed files in working tree",
		}
	}

	effectiveStack := stack
	if check.Stack != "" {
		effectiveStack = check.Stack
	}

	workDir := repoDir
	scopedFiles := changedFiles
	scopedDeleted := deletedFiles
	if check.WorkingDir != "" {
		workDir = filepath.Join(repoDir, filepath.FromSlash(check.WorkingDir))
		scopedFiles = filterToWorkingDir(changedFiles, check.WorkingDir)
		scopedDeleted = filterToWorkingDir(deletedFiles, check.WorkingDir)
		// Also check if root quench.yaml changed or was deleted
		for _, f := range changedFiles {
			if globalCriticalFiles[f] {
				return ResolvedCheck{
					Command:         expandFullSuiteCommand(workDir, check.Command),
					WorkingDir:      check.WorkingDir,
					FullSuite:       true,
					FullSuiteReason: "critical file modified: " + f,
					MatchedFiles:    filterByExtensions(scopedFiles, check.Extensions),
				}
			}
		}
		for _, f := range deletedFiles {
			if globalCriticalFiles[f] {
				return ResolvedCheck{
					Command:         expandFullSuiteCommand(workDir, check.Command),
					WorkingDir:      check.WorkingDir,
					FullSuite:       true,
					FullSuiteReason: "critical file deleted: " + f,
					MatchedFiles:    filterByExtensions(scopedFiles, check.Extensions),
				}
			}
		}
		if len(scopedFiles) == 0 && len(scopedDeleted) == 0 {
			return ResolvedCheck{
				WorkingDir: check.WorkingDir,
				Skip:       true,
				SkipReason: "no changed files under " + check.WorkingDir,
			}
		}
	}

	matched := filterByExtensions(scopedFiles, check.Extensions)
	matchedDeleted := filterByExtensions(scopedDeleted, check.Extensions)

	if fullSuite, reason := CheckCriticalTrigger(effectiveStack, scopedFiles); fullSuite {
		cmd := expandFullSuiteCommand(workDir, check.Command)
		return ResolvedCheck{
			Command:         cmd,
			WorkingDir:      check.WorkingDir,
			FullSuite:       true,
			FullSuiteReason: "critical file modified: " + reason,
			MatchedFiles:    matched,
		}
	}
	if fullSuite, reason := CheckCriticalTrigger(effectiveStack, scopedDeleted); fullSuite {
		cmd := expandFullSuiteCommand(workDir, check.Command)
		return ResolvedCheck{
			Command:         cmd,
			WorkingDir:      check.WorkingDir,
			FullSuite:       true,
			FullSuiteReason: "critical file deleted: " + reason,
			MatchedFiles:    matched,
		}
	}

	if len(matched) == 0 && len(matchedDeleted) == 0 {
		return ResolvedCheck{
			WorkingDir: check.WorkingDir,
			Skip:       true,
			SkipReason: "no matching changed files for check extensions",
		}
	}

	switch check.Scope {
	case config.ScopeProject:
		if len(matched) == 0 && len(matchedDeleted) > 0 {
			return ResolvedCheck{
				Command:         expandFullSuiteCommand(workDir, check.Command),
				WorkingDir:      check.WorkingDir,
				FullSuite:       true,
				FullSuiteReason: "deleted source file: " + matchedDeleted[0],
				MatchedFiles:    matched,
			}
		}
		cmd := replacePlaceholders(check.Command, quoteArgs(matched), "./...", ".")
		return ResolvedCheck{
			Command:      cmd,
			WorkingDir:   check.WorkingDir,
			MatchedFiles: matched,
		}

	case config.ScopeChangedPackages:
		for _, f := range matchedDeleted {
			if !strings.HasSuffix(strings.ToLower(f), ".go") {
				continue
			}
			dir := path.Dir(f)
			fullDir := filepath.Join(workDir, filepath.FromSlash(dir))
			if !dirHasGoFiles(fullDir) {
				return ResolvedCheck{
					Command:         expandFullSuiteCommand(workDir, check.Command),
					WorkingDir:      check.WorkingDir,
					FullSuite:       true,
					FullSuiteReason: "deleted Go file: " + f,
					MatchedFiles:    matched,
				}
			}
		}
		allGoFiles := make([]string, 0, len(matched)+len(matchedDeleted))
		allGoFiles = append(allGoFiles, matched...)
		allGoFiles = append(allGoFiles, matchedDeleted...)
		pkgs := resolveGoPackages(workDir, allGoFiles)
		if len(pkgs) == 0 {
			if len(matchedDeleted) > 0 {
				return ResolvedCheck{
					Command:         expandFullSuiteCommand(workDir, check.Command),
					WorkingDir:      check.WorkingDir,
					FullSuite:       true,
					FullSuiteReason: "deleted source file: " + matchedDeleted[0],
					MatchedFiles:    matched,
				}
			}
			return ResolvedCheck{
				WorkingDir: check.WorkingDir,
				Skip:       true,
				SkipReason: "no valid Go packages in changed paths",
			}
		}
		// Expand transitive Go importers so breaking exported signatures is caught
		pkgs = ExpandGoReverseImporters(workDir, pkgs)
		cmd := replacePlaceholders(check.Command, quoteArgs(matched), strings.Join(quoteArgs(pkgs), " "), strings.Join(quoteArgs(pkgs), " "))
		return ResolvedCheck{
			Command:        cmd,
			WorkingDir:     check.WorkingDir,
			MatchedFiles:   matched,
			TargetPackages: pkgs,
		}

	case config.ScopeMappedTests:
		if len(matchedDeleted) > 0 {
			return ResolvedCheck{
				Command:         expandFullSuiteCommand(workDir, check.Command),
				WorkingDir:      check.WorkingDir,
				FullSuite:       true,
				FullSuiteReason: "deleted source file: " + matchedDeleted[0],
				MatchedFiles:    matched,
			}
		}
		tests := resolvePythonTests(workDir, matched)
		if len(tests) == 0 {
			return ResolvedCheck{
				WorkingDir: check.WorkingDir,
				Skip:       true,
				SkipReason: "no mapped tests found for changed files",
			}
		}
		cmd := replacePlaceholders(check.Command, quoteArgs(matched), ".", strings.Join(quoteArgs(tests), " "))
		return ResolvedCheck{
			Command:      cmd,
			WorkingDir:   check.WorkingDir,
			MatchedFiles: matched,
			TargetTests:  tests,
		}

	case config.ScopeChangedFiles:
		fallthrough
	default:
		if len(matchedDeleted) > 0 {
			return ResolvedCheck{
				Command:         expandFullSuiteCommand(workDir, check.Command),
				WorkingDir:      check.WorkingDir,
				FullSuite:       true,
				FullSuiteReason: "deleted source file: " + matchedDeleted[0],
				MatchedFiles:    matched,
			}
		}
		targets := matched
		if effectiveStack == config.StackNode &&
			strings.Contains(strings.ToLower(check.Name), "test") &&
			!strings.Contains(check.Command, "vitest related") &&
			!strings.Contains(check.Command, "--findRelatedTests") {
			targets = filterByExtensions(ExpandJSReverseImporters(workDir, matched), check.Extensions)
		}
		filesArg := strings.Join(quoteArgs(targets), " ")
		cmd := replacePlaceholders(check.Command, quoteArgs(targets), "./...", filesArg)
		return ResolvedCheck{
			Command:      cmd,
			WorkingDir:   check.WorkingDir,
			MatchedFiles: targets,
		}
	}
}

func filterToWorkingDir(files []string, workingDir string) []string {
	prefix := strings.TrimSuffix(filepath.ToSlash(workingDir), "/") + "/"
	var out []string
	for _, f := range files {
		if strings.HasPrefix(f, prefix) {
			out = append(out, strings.TrimPrefix(f, prefix))
		}
	}
	return out
}

func filterByExtensions(files []string, exts []string) []string {
	if len(exts) == 0 {
		out := make([]string, len(files))
		copy(out, files)
		return out
	}
	extSet := make(map[string]bool, len(exts))
	for _, e := range exts {
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		extSet[strings.ToLower(e)] = true
	}
	var out []string
	for _, f := range files {
		if extSet[strings.ToLower(path.Ext(f))] {
			out = append(out, f)
		}
	}
	return out
}

func resolveGoPackages(repoDir string, goFiles []string) []string {
	seen := make(map[string]struct{})
	for _, f := range goFiles {
		if !strings.HasSuffix(strings.ToLower(f), ".go") {
			continue
		}
		dir := path.Dir(f)
		fullDir := filepath.Join(repoDir, filepath.FromSlash(dir))
		if !dirHasGoFiles(fullDir) {
			continue
		}
		pkg := "./" + dir
		if dir == "." || dir == "" {
			pkg = "."
		}
		seen[pkg] = struct{}{}
	}
	pkgs := make([]string, 0, len(seen))
	for p := range seen {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	return pkgs
}

func dirHasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".go") {
			return true
		}
	}
	return false
}

// resolvePythonTests maps changed .py files to their matching test files, reverse-importing tests, or test directories.
func resolvePythonTests(repoDir string, pyFiles []string) []string {
	seen := make(map[string]struct{})
	for _, f := range pyFiles {
		base := path.Base(f)
		if isPythonTestFile(base) {
			if relFileExists(repoDir, f) {
				seen[f] = struct{}{}
			}
			continue
		}
		if base == "__init__.py" {
			dir := strings.TrimPrefix(path.Dir(f), "src/")
			for _, cand := range []string{
				path.Join("tests", dir),
				"tests",
			} {
				if relDirHasPyTests(repoDir, cand) {
					seen[cand] = struct{}{}
					break
				}
			}
			continue
		}

		stem := strings.TrimSuffix(base, path.Ext(base))
		dir := path.Dir(f)
		strippedDir := strings.TrimPrefix(dir, "src/")
		if strippedDir == "src" {
			strippedDir = "."
		}

		candidates := []string{
			cleanSlash(path.Join(dir, "test_"+stem+".py")),
			cleanSlash(path.Join(dir, stem+"_test.py")),
			cleanSlash(path.Join("tests", strippedDir, "test_"+stem+".py")),
			cleanSlash(path.Join("tests", strippedDir, stem+"_test.py")),
			cleanSlash(path.Join("tests", "test_"+stem+".py")),
			cleanSlash(path.Join("tests", stem+"_test.py")),
		}

		matchedAny := false
		for _, cand := range candidates {
			if relFileExists(repoDir, cand) {
				seen[cand] = struct{}{}
				matchedAny = true
			}
		}

		// Also include any test file under tests/ that directly imports this module
		for _, rev := range FindPythonReverseTests(repoDir, []string{f}) {
			seen[rev] = struct{}{}
			matchedAny = true
		}

		if matchedAny {
			continue
		}

		dirCandidates := []string{
			cleanSlash(path.Join("tests", strippedDir)),
			cleanSlash(dir),
			"tests",
		}
		for _, dc := range dirCandidates {
			if relDirHasPyTests(repoDir, dc) {
				seen[dc] = struct{}{}
				break
			}
		}
	}

	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func isPythonTestFile(base string) bool {
	lower := strings.ToLower(base)
	return (strings.HasPrefix(lower, "test_") && strings.HasSuffix(lower, ".py")) ||
		strings.HasSuffix(lower, "_test.py")
}

func relFileExists(repoDir, rel string) bool {
	info, err := os.Stat(filepath.Join(repoDir, filepath.FromSlash(rel)))
	return err == nil && !info.IsDir()
}

func relDirHasPyTests(repoDir, rel string) bool {
	full := filepath.Join(repoDir, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil || !info.IsDir() {
		return false
	}
	var found bool
	_ = filepath.WalkDir(full, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || found {
			return walkErr
		}
		if !d.IsDir() && isPythonTestFile(d.Name()) {
			found = true
		}
		return nil
	})
	return found
}

func cleanSlash(p string) string {
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "./")
	return p
}

func expandFullSuiteCommand(repoDir string, cmd string) string {
	if strings.Contains(cmd, "vitest related") {
		cmd = strings.Replace(cmd, "vitest related", "vitest", 1)
		cmd = strings.ReplaceAll(cmd, "{{files}}", "")
		return strings.TrimSpace(cmd)
	}
	if strings.Contains(cmd, "--findRelatedTests") {
		cmd = strings.Replace(cmd, "--findRelatedTests", "", 1)
		cmd = strings.ReplaceAll(cmd, "{{files}}", "")
		return strings.Join(strings.Fields(cmd), " ")
	}
	testTarget := "."
	if info, err := os.Stat(filepath.Join(repoDir, "tests")); err == nil && info.IsDir() {
		testTarget = "tests"
	}
	out := strings.ReplaceAll(cmd, "{{packages}}", "./...")
	out = strings.ReplaceAll(out, "{{files}}", ".")
	out = strings.ReplaceAll(out, "{{tests}}", testTarget)
	return strings.TrimSpace(out)
}

func replacePlaceholders(cmd string, quotedFiles []string, pkgStr string, testsStr string) string {
	filesStr := strings.Join(quotedFiles, " ")
	out := strings.ReplaceAll(cmd, "{{files}}", filesStr)
	out = strings.ReplaceAll(out, "{{packages}}", pkgStr)
	out = strings.ReplaceAll(out, "{{tests}}", testsStr)
	return strings.TrimSpace(out)
}

var safeShellArgRe = regexp.MustCompile(`^[a-zA-Z0-9_./:@+-]+$`)

// shellQuote safely quotes an argument for POSIX shells, stripping NUL/CR/LF
// and single-quoting any value that contains characters outside safe ASCII path chars.
func shellQuote(s string) string {
	s = strings.NewReplacer("\x00", "", "\r", "", "\n", "").Replace(s)
	if safeShellArgRe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func quoteArgs(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = shellQuote(s)
	}
	return out
}
