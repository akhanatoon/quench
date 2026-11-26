package diff

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LineRange represents an inclusive 1-based line range [Start, End] in a file.
type LineRange struct {
	Start int
	End   int
}

const untrackedMaxLine = 1 << 30

var hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// ChangedLineRanges returns a map of normalized relative file paths to the
// 1-based inclusive line ranges modified in the working tree (unstaged),
// index (staged), untracked files, and optionally relative to baseRef (...HEAD).
func ChangedLineRanges(ctx context.Context, repoDir string, baseRef string) (map[string][]LineRange, error) {
	absDir, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("resolve repo dir: %w", err)
	}

	if _, err := gitLines(ctx, absDir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, fmt.Errorf("not a git repository (%s): %w", absDir, err)
	}

	result := make(map[string][]LineRange)

	// 1. Unstaged tracked changes
	if out, err := gitDiffOutput(ctx, absDir, "-c", "core.quotePath=false", "diff", "-U0", "--no-color"); err == nil && out != "" {
		parseUnifiedDiffHunks(out, result)
	}

	// 2. Staged changes
	if out, err := gitDiffOutput(ctx, absDir, "-c", "core.quotePath=false", "diff", "--cached", "-U0", "--no-color"); err == nil && out != "" {
		parseUnifiedDiffHunks(out, result)
	}

	// 3. Optional baseRef comparison (committed branch diff)
	if ref := strings.TrimSpace(baseRef); ref != "" {
		out, err := gitDiffOutput(ctx, absDir, "-c", "core.quotePath=false", "diff", "-U0", "--no-color", ref+"...HEAD")
		if err != nil {
			out, err = gitDiffOutput(ctx, absDir, "-c", "core.quotePath=false", "diff", "-U0", "--no-color", ref)
			if err != nil {
				return nil, fmt.Errorf("git diff against base ref %q failed: %w", ref, err)
			}
		}
		if out != "" {
			parseUnifiedDiffHunks(out, result)
		}
	}

	// 4. Untracked non-ignored files: treat all lines as changed
	if lines, err := gitLines(ctx, absDir, "-c", "core.quotePath=false", "ls-files", "--others", "--exclude-standard"); err == nil {
		for _, raw := range lines {
			rel := normalizeRelPath(raw)
			if rel == "" {
				continue
			}
			full := filepath.Join(absDir, filepath.FromSlash(rel))
			info, statErr := os.Stat(full)
			if statErr != nil || info.IsDir() {
				continue
			}
			result[rel] = append(result[rel], LineRange{Start: 1, End: untrackedMaxLine})
		}
	}

	for file, ranges := range result {
		result[file] = mergeLineRanges(ranges)
	}

	return result, nil
}

// LineInDiff reports whether line in file falls within any changed LineRange.
// If line <= 0 (file-level or unlocated error), it always returns true.
func LineInDiff(ranges map[string][]LineRange, file string, line int) bool {
	if line <= 0 {
		return true
	}
	norm := normalizeRelPath(file)
	if norm == "" {
		return false
	}
	fileRanges, ok := ranges[norm]
	if !ok {
		return false
	}
	for _, r := range fileRanges {
		if r.Start <= line && line <= r.End {
			return true
		}
	}
	return false
}

// IsTestFile reports whether file matches common test file naming conventions
// for Go, Python, TypeScript/JavaScript, and Rust.
func IsTestFile(file string) bool {
	norm := normalizeRelPath(file)
	if norm == "" {
		return false
	}
	base := strings.ToLower(path.Base(norm))
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasSuffix(base, "_test.py"):
		return true
	case strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"):
		return true
	case strings.HasSuffix(base, ".test.ts"),
		strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".test.js"),
		strings.HasSuffix(base, ".test.jsx"),
		strings.HasSuffix(base, ".spec.ts"),
		strings.HasSuffix(base, ".spec.tsx"),
		strings.HasSuffix(base, ".spec.js"),
		strings.HasSuffix(base, ".spec.jsx"):
		return true
	case strings.HasSuffix(base, "_test.rs"):
		return true
	default:
		return false
	}
}

func parseUnifiedDiffHunks(diffText string, out map[string][]LineRange) {
	var currentFile string
	lines := strings.Split(diffText, "\n")
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		if strings.HasPrefix(line, "+++ ") {
			target := strings.TrimPrefix(line, "+++ ")
			if target == "/dev/null" {
				currentFile = ""
				continue
			}
			if strings.HasPrefix(target, "b/") {
				target = strings.TrimPrefix(target, "b/")
			}
			// Strip optional trailing tab (present on some diff generators for filenames with spaces)
			if idx := strings.IndexByte(target, '\t'); idx >= 0 {
				target = target[:idx]
			}
			currentFile = normalizeRelPath(target)
			continue
		}

		if currentFile == "" || !strings.HasPrefix(line, "@@ ") {
			continue
		}

		m := hunkHeaderRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		start, err := strconv.Atoi(m[1])
		if err != nil || start <= 0 {
			continue
		}
		count := 1
		if m[2] != "" {
			c, err := strconv.Atoi(m[2])
			if err != nil {
				continue
			}
			count = c
		}
		if count == 0 {
			// Pure deletion hunk: no added/modified lines in the new file.
			continue
		}
		out[currentFile] = append(out[currentFile], LineRange{
			Start: start,
			End:   start + count - 1,
		})
	}
}

func mergeLineRanges(ranges []LineRange) []LineRange {
	if len(ranges) <= 1 {
		return ranges
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start != ranges[j].Start {
			return ranges[i].Start < ranges[j].Start
		}
		return ranges[i].End < ranges[j].End
	})
	merged := make([]LineRange, 0, len(ranges))
	cur := ranges[0]
	for _, next := range ranges[1:] {
		if next.Start <= cur.End+1 {
			if next.End > cur.End {
				cur.End = next.End
			}
		} else {
			merged = append(merged, cur)
			cur = next
		}
	}
	merged = append(merged, cur)
	return merged
}

func gitDiffOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}
