package parser

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	ruffConciseRe   = regexp.MustCompile(`^([^\s:]+\.pyi?):(\d+):(\d+):\s*([A-Z]+\d+)\s+(?:\[\*\]\s+)?(.+)$`)
	ruffFullRuleRe  = regexp.MustCompile(`^([A-Z]{1,6}\d+)\s+(?:\[\*\]\s+)?(.+)$`)
	ruffFullArrowRe = regexp.MustCompile(`^\s*-->\s*([^\s:]+\.pyi?):(\d+):(\d+)\s*$`)
	mypyRe          = regexp.MustCompile(`^([^\s:]+\.pyi?):(\d+)(?::(\d+))?:\s*error:\s*(.+)$`)
	pyrightRe       = regexp.MustCompile(`^\s*([^\s:]+\.pyi?):(\d+):(\d+)\s*-\s*error:\s*(.+)$`)
	pytestSummaryRe = regexp.MustCompile(`^FAILED\s+([^\s:]+\.py)::([^\s]+)(?:\s+-\s+(.+))?$`)
	pytestLocRe     = regexp.MustCompile(`^([^\s:]+\.py):(\d+):\s*([A-Za-z0-9_.]*(?:Error|Exception|Failed).*)$`)
)

func parseRuff(output string) []Failure {
	var failures []Failure
	var pendingRule, pendingMsg string

	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if m := ruffConciseRe.FindStringSubmatch(line); len(m) == 6 {
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
			pendingRule = ""
			continue
		}
		if m := ruffFullArrowRe.FindStringSubmatch(raw); len(m) == 4 && pendingRule != "" {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			failures = append(failures, Failure{
				File:       m[1],
				Line:       ln,
				Column:     col,
				Symbol:     pendingRule,
				Message:    pendingMsg,
				RawExcerpt: pendingRule + " " + pendingMsg,
			})
			pendingRule = ""
			pendingMsg = ""
			continue
		}
		if m := ruffFullRuleRe.FindStringSubmatch(line); len(m) == 3 {
			pendingRule = m[1]
			pendingMsg = strings.TrimSpace(m[2])
		}
	}
	return failures
}

func parseMypy(output string) []Failure {
	var failures []Failure
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if m := mypyRe.FindStringSubmatch(trimmed); len(m) == 5 {
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
		if m := pyrightRe.FindStringSubmatch(line); len(m) == 5 {
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

func parsePytest(output string) []Failure {
	type locInfo struct {
		line    int
		excerpt string
	}
	locsByFile := make(map[string]locInfo)
	var lastErrorLine string

	lines := strings.Split(output, "\n")
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "E       ") || strings.HasPrefix(trimmed, "E   ") {
			if lastErrorLine == "" {
				lastErrorLine = strings.TrimSpace(strings.TrimPrefix(trimmed, "E"))
			}
			continue
		}
		if m := pytestLocRe.FindStringSubmatch(trimmed); len(m) == 4 {
			ln, _ := strconv.Atoi(m[2])
			msg := lastErrorLine
			if msg == "" {
				msg = m[3]
			}
			locsByFile[m[1]] = locInfo{line: ln, excerpt: msg}
			lastErrorLine = ""
		}
	}

	var failures []Failure
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if m := pytestSummaryRe.FindStringSubmatch(trimmed); len(m) == 4 {
			file := m[1]
			sym := m[2]
			msg := strings.TrimSpace(m[3])
			loc := locsByFile[file]
			if msg == "" {
				msg = loc.excerpt
			}
			if msg == "" {
				msg = "pytest failure"
			}
			failures = append(failures, Failure{
				File:       file,
				Line:       loc.line,
				Symbol:     sym,
				Message:    msg,
				RawExcerpt: trimmed,
			})
		}
	}

	if len(failures) == 0 {
		for file, loc := range locsByFile {
			failures = append(failures, Failure{
				File:       file,
				Line:       loc.line,
				Message:    loc.excerpt,
				RawExcerpt: loc.excerpt,
			})
		}
	}
	return failures
}
