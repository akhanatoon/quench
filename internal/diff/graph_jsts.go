package diff

import (
	"encoding/json"
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

	// Matches `import ... from '...'` and `export ... from '...'` (including multiline imports).
	jsFromRe = regexp.MustCompile(`\bfrom\s*['"]([^'"]+)['"]`)
	// Matches side-effect `import '...'`.
	jsSideEffectImportRe = regexp.MustCompile(`(?m)^\s*import\s+['"]([^'"]+)['"]`)
	// Matches CommonJS `require('...')`.
	jsRequireRe = regexp.MustCompile(`\brequire\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	// Matches dynamic `import('...')`.
	jsDynamicImportRe = regexp.MustCompile(`\bimport\s*\(\s*['"]([^'"]+)['"]\s*\)`)
)

type tsPathAlias struct {
	prefix  string
	suffix  string
	hasWild bool
	targets []string
}

type tsPathConfig struct {
	baseURL string
	aliases []tsPathAlias
}

type tsConfigFile struct {
	CompilerOptions struct {
		BaseURL string              `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
}

func loadTSPathConfig(repoDir string) *tsPathConfig {
	var data []byte
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		raw, err := os.ReadFile(filepath.Join(repoDir, name))
		if err == nil {
			data = raw
			break
		}
	}
	if len(data) == 0 {
		return nil
	}

	cleaned := stripJSONC(string(data))
	var parsed tsConfigFile
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return nil
	}
	if len(parsed.CompilerOptions.Paths) == 0 {
		return nil
	}

	baseURL := normalizeRepoRel(parsed.CompilerOptions.BaseURL)
	aliases := make([]tsPathAlias, 0, len(parsed.CompilerOptions.Paths))
	for pattern, targets := range parsed.CompilerOptions.Paths {
		if pattern == "" || len(targets) == 0 {
			continue
		}
		var cleanTargets []string
		for _, t := range targets {
			t = strings.TrimSpace(filepath.ToSlash(t))
			if t != "" {
				cleanTargets = append(cleanTargets, t)
			}
		}
		if len(cleanTargets) == 0 {
			continue
		}
		if idx := strings.IndexByte(pattern, '*'); idx >= 0 {
			aliases = append(aliases, tsPathAlias{
				prefix:  pattern[:idx],
				suffix:  pattern[idx+1:],
				hasWild: true,
				targets: cleanTargets,
			})
		} else {
			aliases = append(aliases, tsPathAlias{
				prefix:  pattern,
				hasWild: false,
				targets: cleanTargets,
			})
		}
	}
	if len(aliases) == 0 {
		return nil
	}

	// Sort exact matches first, then longest prefix+suffix for deterministic specificity.
	sort.Slice(aliases, func(i, j int) bool {
		if aliases[i].hasWild != aliases[j].hasWild {
			return !aliases[i].hasWild
		}
		li := len(aliases[i].prefix) + len(aliases[i].suffix)
		lj := len(aliases[j].prefix) + len(aliases[j].suffix)
		if li != lj {
			return li > lj
		}
		if aliases[i].prefix != aliases[j].prefix {
			return aliases[i].prefix < aliases[j].prefix
		}
		return aliases[i].suffix < aliases[j].suffix
	})

	return &tsPathConfig{
		baseURL: baseURL,
		aliases: aliases,
	}
}

func (c *tsPathConfig) matches(spec string) bool {
	if c == nil || spec == "" {
		return false
	}
	if idx := strings.IndexAny(spec, "?#"); idx >= 0 {
		spec = spec[:idx]
	}
	for _, a := range c.aliases {
		if a.matches(spec) {
			return true
		}
	}
	return false
}

func (a tsPathAlias) matches(spec string) bool {
	if !a.hasWild {
		return spec == a.prefix
	}
	return len(spec) >= len(a.prefix)+len(a.suffix) &&
		strings.HasPrefix(spec, a.prefix) &&
		strings.HasSuffix(spec, a.suffix)
}

func (c *tsPathConfig) resolveCandidates(spec string) []string {
	if c == nil || spec == "" {
		return nil
	}
	var candidates []string
	for _, a := range c.aliases {
		if !a.matches(spec) {
			continue
		}
		mid := ""
		if a.hasWild {
			mid = spec[len(a.prefix) : len(spec)-len(a.suffix)]
		}
		for _, target := range a.targets {
			expanded := target
			if a.hasWild && strings.Contains(target, "*") {
				expanded = strings.Replace(target, "*", mid, 1)
			}
			var joined string
			if c.baseURL != "" {
				joined = path.Clean(path.Join(c.baseURL, expanded))
			} else {
				joined = path.Clean(expanded)
			}
			joined = strings.TrimPrefix(joined, "./")
			if joined == ".." || strings.HasPrefix(joined, "../") {
				continue
			}
			candidates = append(candidates, joined)
		}
	}
	return candidates
}

// stripJSONC removes // line comments, /* ... */ block comments, and trailing
// commas before '}' or ']' while preserving JSON string literals verbatim.
func stripJSONC(src string) string {
	noComments := stripJSONCComments(src)
	return stripJSONCTrailingCommas(noComments)
}

func stripJSONCComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	inString := false
	escaped := false

	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inString {
			b.WriteByte(ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			b.WriteByte(ch)
			continue
		}

		if ch == '/' && i+1 < len(src) {
			next := src[i+1]
			if next == '/' {
				i += 2
				for i < len(src) && src[i] != '\n' {
					i++
				}
				if i < len(src) && src[i] == '\n' {
					b.WriteByte('\n')
				}
				continue
			}
			if next == '*' {
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					if src[i] == '\n' {
						b.WriteByte('\n')
					}
					i++
				}
				i++ // skip closing '/'
				continue
			}
		}

		b.WriteByte(ch)
	}
	return b.String()
}

func stripJSONCTrailingCommas(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	inString := false
	escaped := false

	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inString {
			b.WriteByte(ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			b.WriteByte(ch)
			continue
		}

		if ch == ',' {
			j := i + 1
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r' || src[j] == '\n') {
				j++
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				continue
			}
		}

		b.WriteByte(ch)
	}
	return b.String()
}

func isRelativeJSSpecifier(spec string) bool {
	return spec == "." || spec == ".." ||
		strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../")
}

// ExpandJSReverseImporters scans JS/TS source files in repoDir, extracts relative
// and tsconfig/jsconfig path-alias import/export/require specifiers, resolves them
// to on-disk paths, and returns the sorted, deduplicated transitive closure of
// changedFiles plus all JS/TS importers.
func ExpandJSReverseImporters(repoDir string, changedFiles []string) []string {
	if len(changedFiles) == 0 {
		return nil
	}

	var seeds []string
	seenSeeds := make(map[string]struct{}, len(changedFiles))
	for _, f := range changedFiles {
		norm := normalizeRepoRel(f)
		if norm == "" {
			continue
		}
		if _, ok := seenSeeds[norm]; !ok {
			seenSeeds[norm] = struct{}{}
			seeds = append(seeds, norm)
		}
	}
	if len(seeds) == 0 {
		return nil
	}

	pathCfg := loadTSPathConfig(repoDir)

	// Build reverse adjacency graph: targetRel -> set of importerRel
	revGraph := make(map[string]map[string]struct{})
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

		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}

		importerDir := path.Dir(importerRel)
		for _, spec := range extractJSSpecifiers(string(data), pathCfg) {
			target := resolveJSSpecifierWithPaths(repoDir, importerDir, spec, pathCfg)
			if target == "" || target == importerRel {
				continue
			}
			if revGraph[target] == nil {
				revGraph[target] = make(map[string]struct{})
			}
			revGraph[target][importerRel] = struct{}{}
		}
		return nil
	})

	// Transitive BFS closure (cycle-safe)
	resultSet := make(map[string]struct{}, len(seeds))
	queue := make([]string, 0, len(seeds))
	for _, s := range seeds {
		resultSet[s] = struct{}{}
		queue = append(queue, s)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for importer := range revGraph[cur] {
			if _, visited := resultSet[importer]; !visited {
				resultSet[importer] = struct{}{}
				queue = append(queue, importer)
			}
		}
	}

	out := make([]string, 0, len(resultSet))
	for f := range resultSet {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func extractJSRelativeSpecifiers(src string) []string {
	return extractJSSpecifiers(src, nil)
}

func extractJSSpecifiers(src string, pathCfg *tsPathConfig) []string {
	seen := make(map[string]struct{})
	var specs []string
	for _, re := range []*regexp.Regexp{jsFromRe, jsSideEffectImportRe, jsRequireRe, jsDynamicImportRe} {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			if len(m) < 2 {
				continue
			}
			spec := m[1]
			if !isRelativeJSSpecifier(spec) && !pathCfg.matches(spec) {
				continue
			}
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
	return resolveJSSpecifierWithPaths(repoDir, importerDirRel, spec, nil)
}

func resolveJSSpecifierWithPaths(repoDir, importerDirRel, spec string, pathCfg *tsPathConfig) string {
	// Strip query or hash fragments if present (e.g. ./foo.js?raw).
	if idx := strings.IndexAny(spec, "?#"); idx >= 0 {
		spec = spec[:idx]
	}
	if spec == "" {
		return ""
	}

	if isRelativeJSSpecifier(spec) {
		base := path.Clean(path.Join(importerDirRel, spec))
		base = strings.TrimPrefix(base, "./")
		if base == ".." || strings.HasPrefix(base, "../") {
			return ""
		}
		return resolveJSBasePath(repoDir, base)
	}

	if pathCfg != nil {
		for _, base := range pathCfg.resolveCandidates(spec) {
			if resolved := resolveJSBasePath(repoDir, base); resolved != "" {
				return resolved
			}
		}
	}

	return ""
}

func resolveJSBasePath(repoDir, base string) string {
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
