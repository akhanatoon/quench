package parser

import (
	"regexp"
	"strconv"
	"strings"
)

var genericFileLineRe = regexp.MustCompile(`^([a-zA-Z0-9_./\\-]+\.[a-zA-Z0-9]+):(\d+)(?::(\d+))?:\s*(.+)$`)

func parseGeneric(output string) []Failure {
	var structured []Failure
	var tailLines []string

	for _, raw := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if m := genericFileLineRe.FindStringSubmatch(trimmed); len(m) == 5 {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			structured = append(structured, Failure{
				File:       m[1],
				Line:       ln,
				Column:     col,
				Message:    strings.TrimSpace(m[4]),
				RawExcerpt: trimmed,
			})
			continue
		}
		tailLines = append(tailLines, trimmed)
	}

	if len(structured) > 0 {
		return structured
	}
	if len(tailLines) == 0 {
		return nil
	}

	start := 0
	if len(tailLines) > 5 {
		start = len(tailLines) - 5
	}
	excerpt := strings.Join(tailLines[start:], " | ")
	return []Failure{
		{
			Message:    tailLines[len(tailLines)-1],
			RawExcerpt: excerpt,
		},
	}
}
