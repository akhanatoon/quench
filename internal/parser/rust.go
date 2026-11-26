package parser

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var (
	rustcHeaderRe = regexp.MustCompile(`^error(?:\[(E\d+)\])?:\s*(.+)$`)
	rustcArrowRe  = regexp.MustCompile(`^\s*-->\s*([^\s:]+):(\d+):(\d+)\s*$`)
	rustPanicRe   = regexp.MustCompile(`thread\s+'([^']+)'\s+panicked\s+at\s+([^\s:]+):(\d+):(\d+):?`)
	rustTestResRe = regexp.MustCompile(`^test\s+([^\s]+)\s+\.\.\.\s+FAILED$`)
)

type cargoJSONMsg struct {
	Reason  string `json:"reason"`
	Message struct {
		Message string `json:"message"`
		Level   string `json:"level"`
		Code    *struct {
			Code string `json:"code"`
		} `json:"code"`
		Spans []struct {
			FileName    string `json:"file_name"`
			LineStart   int    `json:"line_start"`
			ColumnStart int    `json:"column_start"`
			IsPrimary   bool   `json:"is_primary"`
		} `json:"spans"`
	} `json:"message"`
}

func parseRustCargo(output string) []Failure {
	var failures []Failure
	parsedJSON := false

	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var msg cargoJSONMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.Reason != "compiler-message" {
			continue
		}
		parsedJSON = true
		if msg.Message.Level != "error" {
			continue
		}
		var file string
		var ln, col int
		for _, sp := range msg.Message.Spans {
			if sp.IsPrimary || file == "" {
				file = sp.FileName
				ln = sp.LineStart
				col = sp.ColumnStart
				if sp.IsPrimary {
					break
				}
			}
		}
		sym := ""
		if msg.Message.Code != nil {
			sym = msg.Message.Code.Code
		}
		failures = append(failures, Failure{
			File:       file,
			Line:       ln,
			Column:     col,
			Symbol:     sym,
			Message:    strings.TrimSpace(msg.Message.Message),
			RawExcerpt: strings.TrimSpace(msg.Message.Message),
		})
	}

	if parsedJSON && len(failures) > 0 {
		return failures
	}
	return parsePlainRustc(output)
}

func parsePlainRustc(output string) []Failure {
	var failures []Failure
	var pendingCode, pendingMsg string

	for _, raw := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(raw)
		if m := rustcHeaderRe.FindStringSubmatch(trimmed); len(m) == 3 {
			pendingCode = m[1]
			pendingMsg = strings.TrimSpace(m[2])
			continue
		}
		if m := rustcArrowRe.FindStringSubmatch(raw); len(m) == 4 && pendingMsg != "" {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			failures = append(failures, Failure{
				File:       m[1],
				Line:       ln,
				Column:     col,
				Symbol:     pendingCode,
				Message:    pendingMsg,
				RawExcerpt: pendingMsg,
			})
			pendingCode = ""
			pendingMsg = ""
		}
	}
	return failures
}

func parseRustTest(output string) []Failure {
	var failures []Failure
	lines := strings.Split(output, "\n")
	seenTests := make(map[string]bool)

	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if m := rustPanicRe.FindStringSubmatch(trimmed); len(m) == 5 {
			sym := m[1]
			ln, _ := strconv.Atoi(m[3])
			col, _ := strconv.Atoi(m[4])
			msg := "assertion failed"
			if i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				if next != "" && !strings.HasPrefix(next, "note:") {
					msg = next
				}
			}
			seenTests[sym] = true
			failures = append(failures, Failure{
				File:       m[2],
				Line:       ln,
				Column:     col,
				Symbol:     sym,
				Message:    msg,
				RawExcerpt: trimmed + " " + msg,
			})
		}
	}

	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if m := rustTestResRe.FindStringSubmatch(trimmed); len(m) == 2 {
			if !seenTests[m[1]] {
				failures = append(failures, Failure{
					Symbol:     m[1],
					Message:    "cargo test failed",
					RawExcerpt: trimmed,
				})
			}
		}
	}

	if len(failures) == 0 {
		return parsePlainRustc(output)
	}
	return failures
}
