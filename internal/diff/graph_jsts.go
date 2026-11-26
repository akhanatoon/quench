package diff

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	jsSkipDirs = map[string]struct{}{
		".git":         {},
		"node_modules": {},
		"dist":         {},
		"build":        {},
		".next":        {},
		"coverage":     {},
		"vendor":       {},
	}

	jsScanExts = map[string]struct{}{
		".ts":  {},
		".tsx": {},
		".js":  {},
		".jsx": {},
		".mts": {},
		".cts": {},
		".mjs": {},
		".cjs": {},
	}

	jsResolveExts = []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs"}

	jsIndexFiles = []string{
		"index.ts",
		"index.tsx",
		"index.js",
		"index.jsx",
		"index.mts",
		"index.mjs",
	}

	// Matches `import ... from './...'` and `export ... from './...'` (including multiline imports).
	jsFromRe = regexp.MustCompile(`\bfrom\s*['"](\.{1,2}(?:/[^'"]*)?)['"]`)
	// Matches side-effect `import './...'`.
	jsSideEffectImportRe = regexp.MustCompile(`(?m)^\s*import\s+['"](\.{1,2}(?:/[^'"]*)?)['"]`)
	// Matches CommonJS `require('./...')`.
	jsRequireRe = regexp.MustCompile(`\brequire\s*\(\s*['"](\.{1,2}(?:/[^'"]*)?)['"]\s*\)`)
	// Matches dynamic `import('./...')`.
	jsDynamicImportRe = regexp.MustCompile(`\bimport\s*\(\s*['"](\.{1,2}(?:/[^'"]*)?)['"]\s*\)`)
)

// ExpandJSReverseImporters scans JS/TS source files in repoDir, extracts relative
// import/export/require specifiers, resolves them to on-disk paths, and returns the
// sorted, deduplicated union of changedFiles plus any 1-hop direct JS/TS importers.
func ExpandJSReverseImporters(repoDir string, changedFiles []string) []string {
	if len(changedFiles) == 0 {
		return nil
	}

	changedSet := make(map[string]struct{}, len(changedFiles))
	resultSet := make(map[string]struct{}, len(changedFiles))
	for _, f := range changedFiles {
		norm := normalizeRepoRel(f)
		if norm == "" {
			continue
		}
		changedSet[norm] = struct{}{}
		resultSet[norm] = struct{}{}
	}
	if len(changedSet) == 0 {
		return nil
	}

	_ = filepath.WalkDir(repoDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != repoDir {
				if _, skip := jsSkipDirs[d.Name()]; skip {
					return filepath.SkipDir
				}
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(d.Name()))
		if _, ok := jsScanExts[ext]; !ok {
			return nil
		}

		rel, relErr := filepath.Rel(repoDir, p)
		if relErr != nil {
			return nil
		}
		importerRel := normalizeRepoRel(rel)
		if importerRel == "" {
			return nil
		}
		if _, already := resultSet[importerRel]; already {
			return nil
		}

		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}

		importerDir := path.Dir(importerRel)
		for _, spec := range extractJSRelativeSpecifiers(string(data)) {
			target := resolveJSSpecifier(repoDir, importerDir, spec)
			if target == "" {
				continue
			}
			if _, hit := changedSet[target]; hit {
				resultSet[importerRel] = struct{}{}
				break
			}
		}
		return nil
	})

	out := make([]string, 0, len(resultSet))
	for f := range resultSet {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func extractJSRelativeSpecifiers(src string) []string {
	seen := make(map[string]struct{})
	var specs []string
	for _, re := range []*regexp.Regexp{jsFromRe, jsSideEffectImportRe, jsRequireRe, jsDynamicImportRe} {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			if len(m) < 2 {
				continue
			}
			spec := m[1]
			if _, ok := seen[spec]; ok {
				continue
			}
			seen[spec] = struct{}{}
			specs = append(specs, spec)
		}
	}
	return specs
}

func resolveJSSpecifier(repoDir, importerDirRel, spec string) string {
	// Strip query or hash fragments if present (e.g. ./foo.js?raw).
	if idx := strings.IndexAny(spec, "?#"); idx >= 0 {
		spec = spec[:idx]
	}
	if spec == "" {
		return ""
	}

	base := path.Clean(path.Join(importerDirRel, spec))
	base = strings.TrimPrefix(base, "./")
	if base == ".." || strings.HasPrefix(base, "../") {
		return ""
	}

	// 1. Exact file match on disk.
	if base != "" && base != "." && isRegularRepoFile(repoDir, base) {
		return base
	}

	// 2. TypeScript ESM extension remapping (e.g. import './foo.js' -> foo.ts / foo.tsx).
	if ext := strings.ToLower(path.Ext(base)); ext != "" {
		stem := strings.TrimSuffix(base, path.Ext(base))
		var tsAlts []string
		switch ext {
		case ".js":
			tsAlts = []string{".ts", ".tsx", ".mts", ".cts"}
		case ".jsx":
			tsAlts = []string{".tsx"}
		case ".mjs":
			tsAlts = []string{".mts"}
		case ".cjs":
			tsAlts = []string{".cts"}
		}
		for _, alt := range tsAlts {
			cand := stem + alt
			if isRegularRepoFile(repoDir, cand) {
				return cand
			}
		}
	}

	// 3. Extensionless file candidates (+ .ts/.tsx/.js/.jsx/.mts/.cts/.mjs/.cjs).
	if base != "" && base != "." {
		for _, ext := range jsResolveExts {
			cand := base + ext
			if isRegularRepoFile(repoDir, cand) {
				return cand
			}
		}
	}

	// 4. Directory barrel index files (/index.ts, /index.tsx, /index.js, /index.jsx, ...).
	for _, idxFile := range jsIndexFiles {
		cand := idxFile
		if base != "" && base != "." {
			cand = path.Join(base, idxFile)
		}
		if isRegularRepoFile(repoDir, cand) {
			return cand
		}
	}

	return ""
}

func isRegularRepoFile(repoDir, relSlash string) bool {
	info, err := os.Stat(filepath.Join(repoDir, filepath.FromSlash(relSlash)))
	return err == nil && !info.IsDir()
}

func normalizeRepoRel(p string) string {
	s := strings.TrimSpace(filepath.ToSlash(p))
	if s == "" {
		return ""
	}
	s = path.Clean(s)
	s = strings.TrimPrefix(s, "./")
	if s == "." {
		return ""
	}
	return s
}
