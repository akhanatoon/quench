package parser

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	tscParenRe          = regexp.MustCompile(`^([^\s(:]+)\((\d+),(\d+)\):\s*error\s+(TS\d+):\s*(.+)$`)
	tscColonRe          = regexp.MustCompile(`^([^\s:]+):(\d+):(\d+)\s*-\s*error\s+(TS\d+):\s*(.+)$`)
	eslintStylishLineRe = regexp.MustCompile(`^\s+(\d+):(\d+)\s+(?:error|warning)\s+(.+?)\s{2,}([@a-zA-Z0-9_/-]+)\s*$`)
	eslintCompactRe     = regexp.MustCompile(`^([^\s:]+):(\d+):(\d+):\s*(.+)$`)
	vitestFailHeaderRe  = regexp.MustCompile(`^\s*(?:FAIL|❯)\s+([^\s]+\.(?:test|spec)\.[jt]sx?)\s*(?:>\s*(.+))?$`)
	jestBulletRe        = regexp.MustCompile(`^\s*●\s+(.+)$`)
	jsStackLocRe        = regexp.MustCompile(`([a-zA-Z0-9_./\\-]+\.[jt]sx?):(\d+):(\d+)`)
)

func parseTSC(output string) []Failure {
	var failures []Failure
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if m := tscParenRe.FindStringSubmatch(line); len(m) == 6 {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			failures = append(failures, Failure{
				File:       m[1],
				Line:       ln,
				Column:     col,
				Symbol:     m[4],
				Message:    strings.TrimSpace(m[5]),
				RawExcerpt: line,
			})
			continue
		}
		if m := tscColonRe.FindStringSubmatch(line); len(m) == 6 {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			failures = append(failures, Failure{
				File:       m[1],
				Line:       ln,
				Column:     col,
				Symbol:     m[4],
				Message:    strings.TrimSpace(m[5]),
				RawExcerpt: line,
			})
		}
	}
	return failures
}

func parseESLint(output string) []Failure {
	var failures []Failure
	var currentFile string

	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "✖") {
			continue
		}

		if m := eslintStylishLineRe.FindStringSubmatch(line); len(m) == 5 && currentFile != "" {
			ln, _ := strconv.Atoi(m[1])
			col, _ := strconv.Atoi(m[2])
			failures = append(failures, Failure{
				File:       currentFile,
				Line:       ln,
				Column:     col,
				Symbol:     m[4],
				Message:    strings.TrimSpace(m[3]),
				RawExcerpt: trimmed,
			})
			continue
		}

		if m := eslintCompactRe.FindStringSubmatch(trimmed); len(m) == 5 {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			failures = append(failures, Failure{
				File:       m[1],
				Line:       ln,
				Column:     col,
				Message:    strings.TrimSpace(m[4]),
				RawExcerpt: trimmed,
			})
			continue
		}

		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && looksLikeSourceFile(trimmed) {
			currentFile = trimmed
		}
	}
	return failures
}

func parseVitestOrJest(output string) []Failure {
	var failures []Failure
	lines := strings.Split(output, "\n")

	var curFile, curSymbol, curMsg string
	flush := func() {
		if curMsg != "" || (curFile != "" && curSymbol != "") {
			msg := curMsg
			if msg == "" {
				msg = "test failed"
			}
			failures = append(failures, Failure{
				File:       curFile,
				Symbol:     curSymbol,
				Message:    msg,
				RawExcerpt: msg,
			})
		}
		curSymbol = ""
		curMsg = ""
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if m := vitestFailHeaderRe.FindStringSubmatch(trimmed); len(m) == 3 {
			flush()
			curFile = m[1]
			curSymbol = strings.TrimSpace(m[2])
			continue
		}
		if m := jestBulletRe.FindStringSubmatch(trimmed); len(m) == 2 {
			flush()
			curSymbol = strings.TrimSpace(m[1])
			continue
		}
		if strings.HasPrefix(trimmed, "AssertionError:") || strings.HasPrefix(trimmed, "Error:") ||
			strings.HasPrefix(trimmed, "TypeError:") || strings.HasPrefix(trimmed, "expect(") {
			if curMsg == "" {
				curMsg = trimmed
			}
			continue
		}
		if m := jsStackLocRe.FindStringSubmatch(trimmed); len(m) == 4 && !strings.Contains(m[1], "node_modules") {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			msg := curMsg
			if msg == "" {
				msg = "test assertion failed"
			}
			failures = append(failures, Failure{
				File:       m[1],
				Line:       ln,
				Column:     col,
				Symbol:     curSymbol,
				Message:    msg,
				RawExcerpt: msg,
			})
			curSymbol = ""
			curMsg = ""
		}
	}
	flush()
	return failures
}

func looksLikeSourceFile(s string) bool {
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs"} {
		if strings.HasSuffix(strings.ToLower(s), ext) {
			return true
		}
	}
	return false
}
