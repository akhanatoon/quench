package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	memoDirName          = "quench-memo"
	memoMaxHashFileBytes = 2 * 1024 * 1024 // 2 MiB
)

// ComputeMemoKey computes a deterministic SHA256 hex key incorporating all run options
// plus the content hash (or size+mtime when >2MB) of each changed file and quench.yaml if present.
func ComputeMemoKey(
	projectDir string,
	mode string,
	baseRef string,
	onlyChecks []string,
	failFast bool,
	changedLinesOnly bool,
	changedFiles []string,
) string {
	h := sha256.New()

	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		absDir = filepath.Clean(projectDir)
	}

	writeField(h, "dir", filepath.ToSlash(absDir))
	writeField(h, "mode", mode)
	writeField(h, "base", baseRef)
	writeField(h, "failFast", strconv.FormatBool(failFast))
	writeField(h, "changedLinesOnly", strconv.FormatBool(changedLinesOnly))

	sortedChecks := make([]string, len(onlyChecks))
	copy(sortedChecks, onlyChecks)
	slices.Sort(sortedChecks)
	for _, c := range sortedChecks {
		writeField(h, "only", c)
	}

	// Include quench.yaml if present in projectDir.
	hashFileInto(h, "config:quench.yaml", filepath.Join(absDir, "quench.yaml"))

	// Normalize, sort, and deduplicate changed files for deterministic hashing.
	normFiles := make([]string, 0, len(changedFiles))
	for _, f := range changedFiles {
		normFiles = append(normFiles, filepath.ToSlash(filepath.Clean(f)))
	}
	slices.Sort(normFiles)
	normFiles = slices.Compact(normFiles)

	for _, rel := range normFiles {
		fullPath := rel
		if !filepath.IsAbs(fullPath) {
			fullPath = filepath.Join(absDir, filepath.FromSlash(rel))
		}
		hashFileInto(h, "file:"+rel, fullPath)
	}

	return hex.EncodeToString(h.Sum(nil))
}

func writeField(h hash.Hash, label, value string) {
	_, _ = io.WriteString(h, label)
	_, _ = io.WriteString(h, "=")
	_, _ = io.WriteString(h, strconv.Itoa(len(value)))
	_, _ = io.WriteString(h, ":")
	_, _ = io.WriteString(h, value)
	_, _ = io.WriteString(h, "\x00")
}

func hashFileInto(h hash.Hash, label, fullPath string) {
	info, err := os.Stat(fullPath)
	if err != nil {
		writeField(h, label, "missing")
		return
	}
	if info.IsDir() {
		writeField(h, label, fmt.Sprintf("dir:%d", info.ModTime().UnixNano()))
		return
	}
	if info.Size() > memoMaxHashFileBytes {
		writeField(h, label, fmt.Sprintf("meta:%d:%d", info.Size(), info.ModTime().UnixNano()))
		return
	}

	f, err := os.Open(fullPath)
	if err != nil {
		writeField(h, label, fmt.Sprintf("meta:%d:%d", info.Size(), info.ModTime().UnixNano()))
		return
	}
	defer f.Close()

	fh := sha256.New()
	if _, err := io.Copy(fh, f); err != nil {
		writeField(h, label, fmt.Sprintf("meta:%d:%d", info.Size(), info.ModTime().UnixNano()))
		return
	}

	writeField(h, label, fmt.Sprintf("sha256:%d:%s", info.Size(), hex.EncodeToString(fh.Sum(nil))))
}

func memoFilePath(key string) (string, bool) {
	if len(key) < 16 {
		return "", false
	}
	prefix := key[:16]
	if strings.ContainsAny(prefix, `/\:*?"<>|`) {
		return "", false
	}
	return filepath.Join(memoDir, prefix+".json"), true
}

// memoDir is the memo cache directory; overridable in tests.
var memoDir = filepath.Join(os.TempDir(), memoDirName)

const (
	memoPruneMaxAge     = 24 * time.Hour
	memoPruneMaxEntries = 200
	memoPruneMaxBytes   = 10 * 1024 * 1024
)

var memoPruneOnce sync.Once

// PruneMemo removes stale memo files: first those older than maxAge, then the
// oldest (by mtime) until at most maxEntries files and maxBytes total remain.
// A non-positive limit is treated as unlimited. Best-effort: errors are ignored.
func PruneMemo(maxAge time.Duration, maxEntries int, maxBytes int64) {
	defer func() { _ = recover() }()

	dirEntries, err := os.ReadDir(memoDir)
	if err != nil {
		return
	}

	type memoFile struct {
		path  string
		mtime time.Time
		size  int64
	}
	var files []memoFile
	var total int64
	now := time.Now()
	for _, e := range dirEntries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(memoDir, e.Name())
		if maxAge > 0 && now.Sub(info.ModTime()) > maxAge {
			_ = os.Remove(p)
			continue
		}
		files = append(files, memoFile{p, info.ModTime(), info.Size()})
		total += info.Size()
	}

	sort.SliceStable(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	count := len(files)
	for _, f := range files {
		overCount := maxEntries > 0 && count > maxEntries
		overBytes := maxBytes > 0 && total > maxBytes
		if !overCount && !overBytes {
			break
		}
		_ = os.Remove(f.path)
		count--
		total -= f.size
	}
}

// LoadMemo loads a cached RunReport if the entry exists and is within ttl.
// Disk and parse errors are ignored (best-effort cache).
func LoadMemo(key string, ttl time.Duration) (*RunReport, bool) {
	if ttl <= 0 {
		return nil, false
	}
	path, ok := memoFilePath(key)
	if !ok {
		return nil, false
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if time.Since(info.ModTime()) > ttl {
		return nil, false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	var report RunReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, false
	}
	return &report, true
}

// SaveMemo persists a RunReport to the temporary memoization directory.
// Errors are silently ignored (best-effort cache).
func SaveMemo(key string, report *RunReport) {
	if report == nil {
		return
	}
	path, ok := memoFilePath(key)
	if !ok {
		return
	}

	memoPruneOnce.Do(func() {
		PruneMemo(memoPruneMaxAge, memoPruneMaxEntries, memoPruneMaxBytes)
	})

	data, err := json.Marshal(report)
	if err != nil {
		return
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}

	tmp, err := os.CreateTemp(dir, "memo-*.tmp")
	if err != nil {
		_ = os.WriteFile(path, data, 0o644)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		_ = os.WriteFile(path, data, 0o644)
	}
}
