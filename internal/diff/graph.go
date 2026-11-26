package diff

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var goModPathRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)

// ExpandGoReverseImporters scans Go imports in repoDir using go/parser (ImportsOnly)
// and computes the full transitive reverse-importer closure of the changed packages via BFS.
func ExpandGoReverseImporters(repoDir string, changedPkgs []string) []string {
	if len(changedPkgs) == 0 {
		return nil
	}
	modPath := readGoModPath(repoDir)
	if modPath == "" {
		return changedPkgs
	}

	// Map: imported pkg ("./internal/config") -> set of importer pkgs ("./internal/runner")
	revGraph := make(map[string]map[string]struct{})
	fset := token.NewFileSet()

	_ = filepath.WalkDir(repoDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".") && name != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".go") {
			return nil
		}

		fileAST, parseErr := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil
		}
		relDir, relErr := filepath.Rel(repoDir, filepath.Dir(p))
		if relErr != nil {
			return nil
		}
		importerPkg := toGoPkgPath(filepath.ToSlash(relDir))

		for _, imp := range fileAST.Imports {
			impPath := strings.Trim(imp.Path.Value, `"`)
			if impPath != modPath && !strings.HasPrefix(impPath, modPath+"/") {
				continue
			}
			targetRel := strings.TrimPrefix(impPath, modPath)
			targetRel = strings.TrimPrefix(targetRel, "/")
			targetPkg := toGoPkgPath(targetRel)
			if targetPkg == importerPkg {
				continue
			}
			if revGraph[targetPkg] == nil {
				revGraph[targetPkg] = make(map[string]struct{})
			}
			revGraph[targetPkg][importerPkg] = struct{}{}
		}
		return nil
	})

	visited := make(map[string]struct{}, len(changedPkgs))
	queue := make([]string, 0, len(changedPkgs))
	for _, pkg := range changedPkgs {
		if _, ok := visited[pkg]; !ok {
			visited[pkg] = struct{}{}
			queue = append(queue, pkg)
		}
	}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for importer := range revGraph[curr] {
			if _, ok := visited[importer]; !ok {
				visited[importer] = struct{}{}
				queue = append(queue, importer)
			}
		}
	}

	out := make([]string, 0, len(visited))
	for pkg := range visited {
		out = append(out, pkg)
	}
	sort.Strings(out)
	return out
}

func readGoModPath(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	if m := goModPathRe.FindSubmatch(data); len(m) == 2 {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

func toGoPkgPath(relSlash string) string {
	relSlash = strings.TrimPrefix(path.Clean(relSlash), "./")
	if relSlash == "" || relSlash == "." {
		return "."
	}
	return "./" + relSlash
}

// FindPythonReverseTests scans test files under repoDir for imports referencing changed Python modules.
func FindPythonReverseTests(repoDir string, changedPyFiles []string) []string {
	var stems []string
	for _, f := range changedPyFiles {
		base := path.Base(f)
		if isPythonTestFile(base) || base == "__init__.py" {
			continue
		}
		stem := strings.TrimSuffix(base, path.Ext(base))
		if len(stem) >= 3 {
			stems = append(stems, stem)
		}
	}
	if len(stems) == 0 {
		return nil
	}

	var matched []string
	testsRoot := filepath.Join(repoDir, "tests")
	if info, err := os.Stat(testsRoot); err != nil || !info.IsDir() {
		return nil
	}

	_ = filepath.WalkDir(testsRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isPythonTestFile(d.Name()) {
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}
		text := string(data)
		for _, stem := range stems {
			if strings.Contains(text, "import "+stem) || strings.Contains(text, "."+stem+" ") || strings.Contains(text, "from "+stem+" ") {
				if rel, rErr := filepath.Rel(repoDir, p); rErr == nil {
					matched = append(matched, filepath.ToSlash(rel))
				}
				break
			}
		}
		return nil
	})
	sort.Strings(matched)
	return matched
}
