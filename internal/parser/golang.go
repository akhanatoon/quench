package parser

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	goTestLocRe   = regexp.MustCompile(`^\s*([^\s:]+\.go):(\d+):\s*(.+)$`)
	goDiagRe      = regexp.MustCompile(`^([^\s:]+\.go):(\d+)(?::(\d+))?:\s*(.+)$`)
	goVetPrefixRe = regexp.MustCompile(`^(?:go\s+)?vet(?:\.exe)?:\s*`)
	goModuleRe    = regexp.MustCompile(`(?m)^module\s+(\S+)`)
)

type goTestEvent struct {
	Action      string `json:"Action"`
	Package     string `json:"Package"`
	ImportPath  string `json:"ImportPath"`
	FailedBuild string `json:"FailedBuild"`
	Test        string `json:"Test"`
	Output      string `json:"Output"`
}

func readModulePath(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return ""
	}
	if m := goModuleRe.FindSubmatch(data); len(m) == 2 {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

func parseGoTest(output string, projectDir string) []Failure {
	modPath := readModulePath(projectDir)
	type testKey struct {
		pkg  string
		test string
	}
	buffers := make(map[testKey][]string)
	pkgBuffers := make(map[string][]string)

	var failures []Failure
	parsedAnyJSON := false

	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev goTestEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Action == "" {
			continue
		}
		parsedAnyJSON = true

		switch ev.Action {
		case "output", "build-output":
			if ev.Test != "" {
				k := testKey{pkg: ev.Package, test: ev.Test}
				buffers[k] = append(buffers[k], ev.Output)
			} else {
				key := ev.Package
				if key == "" {
					key = ev.ImportPath
				}
				pkgBuffers[key] = append(pkgBuffers[key], ev.Output)
			}
		case "fail", "build-fail":
			if ev.Test != "" {
				k := testKey{pkg: ev.Package, test: ev.Test}
				failures = append(failures, extractGoTestFailure(ev.Package, modPath, ev.Test, buffers[k]))
			} else if ev.Action == "build-fail" || ev.FailedBuild != "" {
				key := ev.ImportPath
				if key == "" {
					key = ev.FailedBuild
				}
				if key == "" {
					key = ev.Package
				}
				joined := strings.Join(pkgBuffers[key], "")
				diags := parseGoVetOrLint(joined)
				if len(diags) > 0 {
					failures = append(failures, diags...)
				} else if msg := firstMeaningfulGoLine(pkgBuffers[key]); msg != "" {
					failures = append(failures, Failure{
						Symbol:         shortGoPkgScope(key, modPath),
						Message:        msg,
						SuggestedScope: shortGoPkgScope(key, modPath),
						RawExcerpt:     msg,
					})
				}
			}
		}
	}

	if len(failures) == 0 {
		if parsedAnyJSON {
			for pkg, outs := range pkgBuffers {
				joined := strings.Join(outs, "")
				diags := parseGoVetOrLint(joined)
				if len(diags) > 0 {
					failures = append(failures, diags...)
				} else if strings.Contains(joined, "FAIL") || strings.Contains(joined, "panic:") {
					msg := firstMeaningfulGoLine(outs)
					if msg != "" {
						failures = append(failures, Failure{
							Symbol:         pkg,
							Message:        msg,
							SuggestedScope: shortGoPkgScope(pkg, modPath),
							RawExcerpt:     msg,
						})
					}
				}
			}
		} else {
			failures = parsePlainGoTest(output)
		}
	}

	return failures
}

func extractGoTestFailure(pkg string, modPath string, testName string, outs []string) Failure {
	scope := shortGoPkgScope(pkg, modPath)
	for _, raw := range outs {
		trimmed := strings.TrimRight(raw, "\r\n")
		if m := goTestLocRe.FindStringSubmatch(trimmed); len(m) == 4 {
			ln, _ := strconv.Atoi(m[2])
			file := m[1]
			if !strings.Contains(file, "/") && scope != "" && scope != "." {
				file = path.Join(scope, file)
			}
			return Failure{
				File:           file,
				Line:           ln,
				Symbol:         testName,
				Message:        strings.TrimSpace(m[3]),
				SuggestedScope: scope,
				RawExcerpt:     strings.TrimSpace(trimmed),
			}
		}
	}

	msg := firstMeaningfulGoLine(outs)
	if msg == "" {
		msg = "test failed"
	}
	return Failure{
		Symbol:         testName,
		Message:        msg,
		SuggestedScope: scope,
		RawExcerpt:     msg,
	}
}

func parsePlainGoTest(output string) []Failure {
	var failures []Failure
	var currentTest string
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--- FAIL: ") {
			rest := strings.TrimPrefix(trimmed, "--- FAIL: ")
			if idx := strings.IndexByte(rest, ' '); idx > 0 {
				currentTest = rest[:idx]
			} else {
				currentTest = rest
			}
			continue
		}
		if m := goTestLocRe.FindStringSubmatch(line); len(m) == 4 {
			ln, _ := strconv.Atoi(m[2])
			failures = append(failures, Failure{
				File:       m[1],
				Line:       ln,
				Symbol:     currentTest,
				Message:    strings.TrimSpace(m[3]),
				RawExcerpt: trimmed,
			})
			continue
		}
		if m := goDiagRe.FindStringSubmatch(trimmed); len(m) == 5 {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			failures = append(failures, Failure{
				File:       m[1],
				Line:       ln,
				Column:     col,
				Message:    strings.TrimSpace(m[4]),
				RawExcerpt: trimmed,
			})
		}
	}
	return failures
}

func parseGoVetOrLint(output string) []Failure {
	var failures []Failure
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip Windows/Linux "vet.exe: " or "vet: " prefix so file:line:col matches cleanly
		cleaned := goVetPrefixRe.ReplaceAllString(line, "")
		if m := goDiagRe.FindStringSubmatch(cleaned); len(m) == 5 {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			failures = append(failures, Failure{
				File:       m[1],
				Line:       ln,
				Column:     col,
				Message:    strings.TrimSpace(m[4]),
				RawExcerpt: line,
			})
		}
	}
	return failures
}

func shortGoPkgScope(pkg string, modPath string) string {
	pkg = strings.TrimSpace(pkg)
	if modPath != "" {
		if pkg == modPath {
			return "."
		}
		if strings.HasPrefix(pkg, modPath+"/") {
			return strings.TrimPrefix(pkg, modPath+"/")
		}
	}
	parts := strings.Split(pkg, "/")
	if len(parts) >= 3 && strings.Contains(parts[0], ".") {
		if len(parts) == 3 {
			return "."
		}
		return strings.Join(parts[3:], "/")
	}
	if len(parts) >= 2 && strings.Contains(parts[0], ".") {
		if len(parts) == 2 {
			return "."
		}
		return strings.Join(parts[2:], "/")
	}
	return pkg
}

func firstMeaningfulGoLine(outs []string) string {
	for _, raw := range outs {
		s := strings.TrimSpace(raw)
		if s == "" || strings.HasPrefix(s, "=== RUN") || strings.HasPrefix(s, "=== PAUSE") ||
			strings.HasPrefix(s, "=== CONT") || strings.HasPrefix(s, "--- FAIL:") ||
			strings.HasPrefix(s, "FAIL") || s == "exit status 1" {
			continue
		}
		return s
	}
	return ""
}
