package parser

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const MaxUniqueFailures = 10

type Failure struct {
	File           string `json:"file,omitempty"`
	Line           int    `json:"line,omitempty"`
	Column         int    `json:"column,omitempty"`
	Symbol         string `json:"symbol,omitempty"`
	Message        string `json:"message"`
	SuggestedScope string `json:"suggested_scope,omitempty"`
	RawExcerpt     string `json:"raw_excerpt,omitempty"`
}

var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// Parse dispatches raw command output to the right stack/tool parser and normalizes + caps failures.
func Parse(parserName string, command string, projectDir string, output string) ([]Failure, int) {
	cleanOut := StripANSI(output)
	kind := resolveParserKind(parserName, command)

	var raw []Failure
	switch kind {
	case "go-test":
		raw = parseGoTest(cleanOut, projectDir)
	case "go-vet", "golangci-lint":
		raw = parseGoVetOrLint(cleanOut)
	case "tsc":
		raw = parseTSC(cleanOut)
	case "eslint":
		raw = parseESLint(cleanOut)
	case "vitest", "jest":
		raw = parseVitestOrJest(cleanOut)
	case "ruff":
		raw = parseRuff(cleanOut)
	case "mypy", "pyright":
		raw = parseMypy(cleanOut)
	case "pytest":
		raw = parsePytest(cleanOut)
	case "rust-cargo", "cargo-check", "clippy":
		raw = parseRustCargo(cleanOut)
	case "rust-test", "cargo-test":
		raw = parseRustTest(cleanOut)
	default:
		raw = parseGeneric(cleanOut)
	}

	if len(raw) == 0 && strings.TrimSpace(cleanOut) != "" {
		raw = parseGeneric(cleanOut)
	}

	return FinalizeFailures(raw, projectDir)
}

func resolveParserKind(explicit string, command string) string {
	explicit = strings.ToLower(strings.TrimSpace(explicit))
	if explicit != "" && explicit != "auto" {
		return explicit
	}
	cmd := strings.ToLower(command)
	switch {
	case strings.Contains(cmd, "go test"):
		return "go-test"
	case strings.Contains(cmd, "golangci-lint"):
		return "golangci-lint"
	case strings.Contains(cmd, "go vet"):
		return "go-vet"
	case strings.Contains(cmd, "tsc"):
		return "tsc"
	case strings.Contains(cmd, "eslint"):
		return "eslint"
	case strings.Contains(cmd, "vitest"):
		return "vitest"
	case strings.Contains(cmd, "jest"):
		return "jest"
	case strings.Contains(cmd, "ruff"):
		return "ruff"
	case strings.Contains(cmd, "mypy") || strings.Contains(cmd, "pyright"):
		return "mypy"
	case strings.Contains(cmd, "pytest"):
		return "pytest"
	case strings.Contains(cmd, "cargo test"):
		return "rust-test"
	case strings.Contains(cmd, "cargo check") || strings.Contains(cmd, "cargo clippy"):
		return "rust-cargo"
	default:
		return "generic"
	}
}

func StripANSI(s string) string {
	return ansiEscapeRe.ReplaceAllString(s, "")
}

// FinalizeFailures normalizes paths, populates suggested_scope, deduplicates, and enforces MaxUniqueFailures.
func FinalizeFailures(in []Failure, projectDir string) ([]Failure, int) {
	normProj := ""
	if projectDir != "" {
		if abs, err := filepath.Abs(projectDir); err == nil {
			normProj = strings.ToLower(filepath.ToSlash(abs))
		}
	}

	type dedupKey struct {
		file string
		line int
		sym  string
		msg  string
	}
	seen := make(map[dedupKey]bool, len(in))
	unique := make([]Failure, 0, len(in))

	for _, f := range in {
		f.File = normalizeFailurePath(f.File, normProj)
		f.Message = compactText(f.Message, 240)
		f.RawExcerpt = compactText(f.RawExcerpt, 280)
		if f.Message == "" {
			continue
		}
		if f.SuggestedScope == "" && f.File != "" {
			dir := path.Dir(f.File)
			if dir == "." || dir == "" {
				f.SuggestedScope = "."
			} else {
				f.SuggestedScope = dir
			}
		}

		k := dedupKey{
			file: f.File,
			line: f.Line,
			sym:  f.Symbol,
			msg:  f.Message,
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		unique = append(unique, f)
	}

	if len(unique) <= MaxUniqueFailures {
		return unique, 0
	}
	truncated := len(unique) - MaxUniqueFailures
	return unique[:MaxUniqueFailures], truncated
}

func normalizeFailurePath(p string, normProjLower string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(filepath.ToSlash(p), "\\", "/")
	p = strings.TrimPrefix(p, "/workspace/")
	if normProjLower != "" && strings.HasPrefix(strings.ToLower(p), normProjLower+"/") {
		p = p[len(normProjLower)+1:]
	}
	p = strings.TrimPrefix(p, "./")
	return p
}

func compactText(s string, maxLen int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r", ""))
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
