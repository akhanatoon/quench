package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheComputeMemoKey_DeterminismAndFileChanges(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	cfgFile := filepath.Join(dir, "quench.yaml")

	if err := os.WriteFile(fileA, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgFile, []byte("stack: go\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	k1 := ComputeMemoKey(dir, "host", "HEAD", []string{"test", "vet"}, false, false, []string{"b.go", "a.go"})
	k2 := ComputeMemoKey(dir, "host", "HEAD", []string{"vet", "test"}, false, false, []string{"a.go", "b.go"})

	if len(k1) != 64 {
		t.Fatalf("expected 64-char SHA256 hex key, got len=%d (%q)", len(k1), k1)
	}
	if k1 != k2 {
		t.Fatalf("expected deterministic key regardless of slice order: %q != %q", k1, k2)
	}

	// Changing a tracked file's content must change the key.
	if err := os.WriteFile(fileA, []byte("package a\n// modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kAfterFileEdit := ComputeMemoKey(dir, "host", "HEAD", []string{"test", "vet"}, false, false, []string{"a.go", "b.go"})
	if kAfterFileEdit == k1 {
		t.Fatalf("expected memo key to change when file content changes")
	}

	// Changing quench.yaml must change the key.
	if err := os.WriteFile(cfgFile, []byte("stack: go\nfail_fast: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kAfterCfgEdit := ComputeMemoKey(dir, "host", "HEAD", []string{"test", "vet"}, false, false, []string{"a.go", "b.go"})
	if kAfterCfgEdit == kAfterFileEdit {
		t.Fatalf("expected memo key to change when quench.yaml changes")
	}

	// Changing options must change the key.
	if kMode := ComputeMemoKey(dir, "docker", "HEAD", []string{"test", "vet"}, false, false, []string{"a.go", "b.go"}); kMode == kAfterCfgEdit {
		t.Errorf("expected memo key to change when mode changes")
	}
	if kBase := ComputeMemoKey(dir, "host", "main", []string{"test", "vet"}, false, false, []string{"a.go", "b.go"}); kBase == kAfterCfgEdit {
		t.Errorf("expected memo key to change when baseRef changes")
	}
	if kFailFast := ComputeMemoKey(dir, "host", "HEAD", []string{"test", "vet"}, true, false, []string{"a.go", "b.go"}); kFailFast == kAfterCfgEdit {
		t.Errorf("expected memo key to change when failFast changes")
	}
	if kChangedLines := ComputeMemoKey(dir, "host", "HEAD", []string{"test", "vet"}, false, true, []string{"a.go", "b.go"}); kChangedLines == kAfterCfgEdit {
		t.Errorf("expected memo key to change when changedLinesOnly changes")
	}
}

func TestCacheComputeMemoKey_LargeFileUsesSizeAndMtime(t *testing.T) {
	dir := t.TempDir()
	bigPath := filepath.Join(dir, "large.bin")
	buf := make([]byte, memoMaxHashFileBytes+1024)
	if err := os.WriteFile(bigPath, buf, 0o644); err != nil {
		t.Fatal(err)
	}

	t1 := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(bigPath, t1, t1); err != nil {
		t.Fatal(err)
	}
	k1 := ComputeMemoKey(dir, "host", "HEAD", nil, false, false, []string{"large.bin"})

	t2 := time.Now().Add(-1 * time.Minute)
	if err := os.Chtimes(bigPath, t2, t2); err != nil {
		t.Fatal(err)
	}
	k2 := ComputeMemoKey(dir, "host", "HEAD", nil, false, false, []string{"large.bin"})

	if k1 == k2 {
		t.Fatalf("expected memo key for >2MB file to change when mtime changes")
	}
}

func TestMemoSaveAndLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := ComputeMemoKey(dir, "host", "HEAD", []string{"vet"}, false, false, nil)
	memoPath := filepath.Join(os.TempDir(), memoDirName, key[:16]+".json")
	t.Cleanup(func() {
		_ = os.Remove(memoPath)
	})

	want := &RunReport{
		Status:     StatusPassed,
		Mode:       "host",
		ProjectDir: filepath.ToSlash(dir),
		DurationMs: 88,
		ChangedFiles: []string{
			"main.go",
		},
		Summary: Summary{
			TotalChecks: 1,
			Passed:      1,
		},
		Checks: []CheckResult{
			{
				Name:       "vet",
				Status:     StatusPassed,
				DurationMs: 88,
			},
		},
	}

	SaveMemo(key, want)

	if _, err := os.Stat(memoPath); err != nil {
		t.Fatalf("expected memo file at %s: %v", memoPath, err)
	}

	got, ok := LoadMemo(key, time.Minute)
	if !ok {
		t.Fatalf("expected LoadMemo cache hit, got miss")
	}
	if got.Status != want.Status || got.DurationMs != want.DurationMs || len(got.Checks) != 1 || got.Checks[0].Name != "vet" {
		t.Fatalf("LoadMemo returned unexpected report: %+v", got)
	}
}

func TestMemoLoad_TTLExpiration(t *testing.T) {
	dir := t.TempDir()
	key := ComputeMemoKey(dir, "host", "HEAD", []string{"test"}, true, false, nil)
	memoPath := filepath.Join(os.TempDir(), memoDirName, key[:16]+".json")
	t.Cleanup(func() {
		_ = os.Remove(memoPath)
	})

	SaveMemo(key, &RunReport{
		Status:     StatusPassed,
		DurationMs: 12,
	})

	expiredTime := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(memoPath, expiredTime, expiredTime); err != nil {
		t.Fatalf("chtimes failed: %v", err)
	}

	if got, ok := LoadMemo(key, 5*time.Minute); ok || got != nil {
		t.Fatalf("expected cache miss for expired TTL, got hit: %+v", got)
	}

	if got, ok := LoadMemo(key, 15*time.Minute); !ok || got == nil {
		t.Fatalf("expected cache hit when TTL exceeds age, got miss")
	}
}

func TestMemoLoad_BestEffortErrors(t *testing.T) {
	if got, ok := LoadMemo("", time.Minute); ok || got != nil {
		t.Errorf("expected miss for empty key")
	}
	if got, ok := LoadMemo("short", time.Minute); ok || got != nil {
		t.Errorf("expected miss for short key")
	}
	if got, ok := LoadMemo("0123456789abcdef0123456789abcdef", 0); ok || got != nil {
		t.Errorf("expected miss for zero TTL")
	}

	// Corrupt JSON file should cleanly miss without error.
	corruptKey := "deadbeef0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	memoPath := filepath.Join(os.TempDir(), memoDirName, corruptKey[:16]+".json")
	if err := os.MkdirAll(filepath.Dir(memoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoPath, []byte("{not-valid-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(memoPath)
	})

	if got, ok := LoadMemo(corruptKey, time.Minute); ok || got != nil {
		t.Errorf("expected miss for corrupt memo JSON, got hit: %+v", got)
	}
}

func useTempMemoDir(t *testing.T) string {
	t.Helper()
	old := memoDir
	memoDir = t.TempDir()
	t.Cleanup(func() { memoDir = old })
	return memoDir
}

func writeMemoFile(t *testing.T, dir, name string, size int, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPruneMemo_Age(t *testing.T) {
	dir := useTempMemoDir(t)
	old := writeMemoFile(t, dir, "old.json", 10, 48*time.Hour)
	fresh := writeMemoFile(t, dir, "fresh.json", 10, time.Minute)
	PruneMemo(24*time.Hour, 100, 1<<20)
	if exists(old) || !exists(fresh) {
		t.Fatalf("age prune wrong: old=%v fresh=%v", exists(old), exists(fresh))
	}
}

func TestPruneMemo_EntryCount(t *testing.T) {
	dir := useTempMemoDir(t)
	var paths []string
	for i := 0; i < 5; i++ { // index 0 is oldest
		paths = append(paths, writeMemoFile(t, dir, "f"+string(rune('a'+i))+".json", 1, time.Duration(5-i)*time.Minute))
	}
	PruneMemo(time.Hour, 2, 1<<20)
	for i, p := range paths {
		if want := i >= 3; exists(p) != want {
			t.Errorf("file %d exists=%v want %v", i, exists(p), want)
		}
	}
}

func TestPruneMemo_Bytes(t *testing.T) {
	dir := useTempMemoDir(t)
	a := writeMemoFile(t, dir, "a.json", 100, 3*time.Minute)
	b := writeMemoFile(t, dir, "b.json", 100, 2*time.Minute)
	c := writeMemoFile(t, dir, "c.json", 100, time.Minute)
	PruneMemo(time.Hour, 100, 200)
	if exists(a) || !exists(b) || !exists(c) {
		t.Fatalf("expected only oldest removed: a=%v b=%v c=%v", exists(a), exists(b), exists(c))
	}
	PruneMemo(time.Hour, 100, 100)
	if exists(b) || !exists(c) {
		t.Fatalf("expected b removed, c kept: b=%v c=%v", exists(b), exists(c))
	}
}

func TestPruneMemo_MissingDir(t *testing.T) {
	old := memoDir
	memoDir = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { memoDir = old })
	PruneMemo(time.Hour, 1, 1) // must not panic
	if exists(memoDir) {
		t.Fatal("PruneMemo must not create the directory")
	}
}
