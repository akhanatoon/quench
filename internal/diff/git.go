package diff

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ChangeSet contains both existing changed files and deleted files relative to the repo root.
type ChangeSet struct {
	Changed []string
	Deleted []string
}

// ChangedFiles returns existing relative file paths (slash-separated) that changed in the working tree,
// index, untracked set, and optionally relative to baseRef (...HEAD).
func ChangedFiles(ctx context.Context, repoDir string, baseRef string) ([]string, error) {
	cs, err := ChangedFilesDetail(ctx, repoDir, baseRef)
	if err != nil {
		return nil, err
	}
	return cs.Changed, nil
}

// ChangedFilesDetail returns both existing changed files and deleted files (slash-separated relative paths)
// across the working tree, index, untracked set, and optionally relative to baseRef (...HEAD).
func ChangedFilesDetail(ctx context.Context, repoDir string, baseRef string) (ChangeSet, error) {
	absDir, err := filepath.Abs(repoDir)
	if err != nil {
		return ChangeSet{}, fmt.Errorf("resolve repo dir: %w", err)
	}

	if _, err := gitLines(ctx, absDir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return ChangeSet{}, fmt.Errorf("not a git repository (%s): %w", absDir, err)
	}

	seen := make(map[string]struct{})
	addLines := func(lines []string) {
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
			seen[rel] = struct{}{}
		}
	}

	deletedSeen := make(map[string]struct{})
	addDeletedLines := func(lines []string) {
		for _, raw := range lines {
			rel := normalizeRelPath(raw)
			if rel == "" {
				continue
			}
			full := filepath.Join(absDir, filepath.FromSlash(rel))
			if _, statErr := os.Stat(full); statErr == nil {
				continue
			}
			deletedSeen[rel] = struct{}{}
		}
	}

	// 1. Unstaged tracked changes (excluding deleted) + deleted files
	if lines, err := gitLines(ctx, absDir, "-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=ACMRTUXB"); err == nil {
		addLines(lines)
	}
	if lines, err := gitLines(ctx, absDir, "-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=D"); err == nil {
		addDeletedLines(lines)
	}

	// 2. Staged changes against HEAD (or index if unborn HEAD) + staged deletions
	if lines, err := gitLines(ctx, absDir, "-c", "core.quotePath=false", "diff", "--cached", "--name-only", "--diff-filter=ACMRTUXB"); err == nil {
		addLines(lines)
	} else if lines, lsErr := gitLines(ctx, absDir, "-c", "core.quotePath=false", "ls-files", "--stage"); lsErr == nil {
		for _, l := range lines {
			if idx := strings.IndexByte(l, '\t'); idx >= 0 && idx+1 < len(l) {
				addLines([]string{l[idx+1:]})
			}
		}
	}
	if lines, err := gitLines(ctx, absDir, "-c", "core.quotePath=false", "diff", "--cached", "--name-only", "--diff-filter=D"); err == nil {
		addDeletedLines(lines)
	}

	// 3. Untracked non-ignored files
	if lines, err := gitLines(ctx, absDir, "-c", "core.quotePath=false", "ls-files", "--others", "--exclude-standard"); err == nil {
		addLines(lines)
	}

	// 4. Optional baseRef comparison (committed branch diff + working tree)
	if strings.TrimSpace(baseRef) != "" {
		ref := strings.TrimSpace(baseRef)
		lines, err := gitLines(ctx, absDir, "-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=ACMRTUXB", ref+"...HEAD")
		if err != nil {
			// Fallback to direct diff against baseRef if merge-base fails (e.g. shallow history)
			lines, err = gitLines(ctx, absDir, "-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=ACMRTUXB", ref)
			if err != nil {
				return ChangeSet{}, fmt.Errorf("git diff against base ref %q failed: %w", ref, err)
			}
		}
		addLines(lines)

		delLines, delErr := gitLines(ctx, absDir, "-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=D", ref+"...HEAD")
		if delErr != nil {
			delLines, delErr = gitLines(ctx, absDir, "-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=D", ref)
		}
		if delErr == nil {
			addDeletedLines(delLines)
		}
	}

	changed := make([]string, 0, len(seen))
	for f := range seen {
		changed = append(changed, f)
	}
	sort.Strings(changed)

	deleted := make([]string, 0, len(deletedSeen))
	for f := range deletedSeen {
		deleted = append(deleted, f)
	}
	sort.Strings(deleted)

	return ChangeSet{
		Changed: changed,
		Deleted: deleted,
	}, nil
}

func gitLines(ctx context.Context, dir string, args ...string) ([]string, error) {
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
		return nil, fmt.Errorf("%s", msg)
	}
	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return nil, nil
	}
	lines := strings.Split(raw, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return lines, nil
}

func normalizeRelPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"`)
	p = strings.ReplaceAll(filepath.ToSlash(p), "\\", "/")
	p = strings.TrimPrefix(p, "./")
	return p
}
