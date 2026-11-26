package diff

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestChangedLineRangesUnstagedStagedUntrackedAndBaseRef(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")

	// Base commit on main
	writeFile(t, dir, "pkg/a.go", strings.Join([]string{
		"package pkg", // 1
		"",            // 2
		"func A() {",  // 3
		"	x := 1",     // 4
		"	y := 2",     // 5
		"	_ = x + y",  // 6
		"}",           // 7
	}, "\n")+"\n")

	writeFile(t, dir, "pkg/b.go", strings.Join([]string{
		"package pkg", // 1
		"",            // 2
		"func B() {",  // 3
		"	a := 1",     // 4
		"	b := 2",     // 5
		"	c := 3",     // 6
		"	_ = a+b+c",  // 7
		"}",           // 8
	}, "\n")+"\n")

	writeFile(t, dir, "pkg/c.go", strings.Join([]string{
		"package pkg", // 1
		"",            // 2
		"func C() {",  // 3
		"	v := 10",    // 4
		"	_ = v",      // 5
		"}",           // 6
	}, "\n")+"\n")

	writeFile(t, dir, "pkg/del.go", strings.Join([]string{
		"package pkg", // 1
		"// line 2",   // 2
		"// line 3",   // 3
		"// line 4",   // 4
	}, "\n")+"\n")

	writeFile(t, dir, "pkg/clean.go", "package pkg\n\nfunc Clean() {}\n")

	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	// Feature branch commit modifying lines 4-5 of pkg/a.go
	runGit(t, dir, "checkout", "-b", "feat/hunks")
	writeFile(t, dir, "pkg/a.go", strings.Join([]string{
		"package pkg", // 1
		"",            // 2
		"func A() {",  // 3
		"	x := 10",    // 4 (changed on branch)
		"	y := 20",    // 5 (changed on branch)
		"	_ = x + y",  // 6
		"}",           // 7
	}, "\n")+"\n")
	runGit(t, dir, "add", "pkg/a.go")
	runGit(t, dir, "commit", "-m", "update A")

	// Unstaged edit: single line (line 5) in pkg/b.go
	writeFile(t, dir, "pkg/b.go", strings.Join([]string{
		"package pkg", // 1
		"",            // 2
		"func B() {",  // 3
		"	a := 1",     // 4
		"	b := 200",   // 5 (unstaged single-line change)
		"	c := 3",     // 6
		"	_ = a+b+c",  // 7
		"}",           // 8
	}, "\n")+"\n")

	// Staged edit: multi-line insertion/modification at lines 4-6 in pkg/c.go
	writeFile(t, dir, "pkg/c.go", strings.Join([]string{
		"package pkg", // 1
		"",            // 2
		"func C() {",  // 3
		"	v := 100",   // 4 (staged)
		"	w := 200",   // 5 (staged)
		"	_ = v + w",  // 6 (staged)
		"}",           // 7
	}, "\n")+"\n")
	runGit(t, dir, "add", "pkg/c.go")

	// Pure deletion in pkg/del.go (delete lines 2 and 3) -> count == 0, ignored
	writeFile(t, dir, "pkg/del.go", strings.Join([]string{
		"package pkg", // 1
		"// line 4",   // 2
	}, "\n")+"\n")

	// Untracked file
	writeFile(t, dir, "pkg/new_untracked.go", "package pkg\n\nfunc New() int {\n\treturn 42\n}\n")

	ctx := context.Background()

	// 1. Without baseRef: pkg/a.go should NOT be in ranges
	wtRanges, err := ChangedLineRanges(ctx, dir, "")
	if err != nil {
		t.Fatalf("ChangedLineRanges(no base) failed: %v", err)
	}

	if LineInDiff(wtRanges, "pkg/a.go", 4) {
		t.Fatalf("expected pkg/a.go:4 to be false without baseRef, ranges=%v", wtRanges["pkg/a.go"])
	}

	// Unstaged single-line change at pkg/b.go:5
	if LineInDiff(wtRanges, "pkg/b.go", 4) {
		t.Fatalf("expected pkg/b.go:4 false")
	}
	if !LineInDiff(wtRanges, "pkg/b.go", 5) {
		t.Fatalf("expected pkg/b.go:5 true")
	}
	if LineInDiff(wtRanges, "pkg/b.go", 6) {
		t.Fatalf("expected pkg/b.go:6 false")
	}
	// Path normalization check
	if !LineInDiff(wtRanges, `./pkg\b.go`, 5) {
		t.Fatalf("expected normalized path ./pkg\\b.go:5 true")
	}

	// Staged multi-line change at pkg/c.go:4-6
	if LineInDiff(wtRanges, "pkg/c.go", 3) {
		t.Fatalf("expected pkg/c.go:3 false")
	}
	for ln := 4; ln <= 6; ln++ {
		if !LineInDiff(wtRanges, "pkg/c.go", ln) {
			t.Fatalf("expected pkg/c.go:%d true, ranges=%v", ln, wtRanges["pkg/c.go"])
		}
	}
	if LineInDiff(wtRanges, "pkg/c.go", 7) {
		t.Fatalf("expected pkg/c.go:7 false")
	}

	// Pure deletion should produce no ranges
	if len(wtRanges["pkg/del.go"]) != 0 {
		t.Fatalf("expected pure deletion pkg/del.go to have no ranges, got %v", wtRanges["pkg/del.go"])
	}
	if LineInDiff(wtRanges, "pkg/del.go", 1) {
		t.Fatalf("expected pkg/del.go:1 false for pure deletion")
	}

	// Untracked file should match any positive line
	if !LineInDiff(wtRanges, "pkg/new_untracked.go", 1) || !LineInDiff(wtRanges, "pkg/new_untracked.go", 50000) {
		t.Fatalf("expected untracked file lines to be in diff, ranges=%v", wtRanges["pkg/new_untracked.go"])
	}

	// Clean file: positive lines false, line <= 0 always true
	if LineInDiff(wtRanges, "pkg/clean.go", 1) {
		t.Fatalf("expected clean file line 1 to be false")
	}
	if !LineInDiff(wtRanges, "pkg/clean.go", 0) || !LineInDiff(wtRanges, "pkg/clean.go", -1) {
		t.Fatalf("expected line <= 0 to always return true")
	}

	// 2. With --base main: pkg/a.go lines 4-5 MUST be included
	branchRanges, err := ChangedLineRanges(ctx, dir, "main")
	if err != nil {
		t.Fatalf("ChangedLineRanges(base=main) failed: %v", err)
	}
	if LineInDiff(branchRanges, "pkg/a.go", 3) {
		t.Fatalf("expected pkg/a.go:3 false")
	}
	if !LineInDiff(branchRanges, "pkg/a.go", 4) || !LineInDiff(branchRanges, "pkg/a.go", 5) {
		t.Fatalf("expected pkg/a.go:4-5 true with base=main, ranges=%v", branchRanges["pkg/a.go"])
	}
	if LineInDiff(branchRanges, "pkg/a.go", 6) {
		t.Fatalf("expected pkg/a.go:6 false")
	}
	// Working tree changes remain present alongside baseRef changes
	if !LineInDiff(branchRanges, "pkg/b.go", 5) || !LineInDiff(branchRanges, "pkg/c.go", 5) {
		t.Fatalf("expected working tree and staged hunks still present with base=main")
	}
}

func TestParseUnifiedDiffHunksEdgeCases(t *testing.T) {
	sample := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"index 1111111..2222222 100644",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -3 +3 @@ func single()",
		"-old",
		"+new",
		"@@ -10,2 +10,0 @@ func pureDelete()",
		"-del1",
		"-del2",
		"@@ -20,0 +18,3 @@ func pureInsert()",
		"+ins1",
		"+ins2",
		"+ins3",
		"diff --git a/gone.go b/gone.go",
		"deleted file mode 100644",
		"--- a/gone.go",
		"+++ /dev/null",
		"@@ -1,5 +0,0 @@",
		"-package gone",
	}, "\n")

	got := make(map[string][]LineRange)
	parseUnifiedDiffHunks(sample, got)

	want := map[string][]LineRange{
		"foo.go": {
			{Start: 3, End: 3},
			{Start: 18, End: 20},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseUnifiedDiffHunks mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestIsTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"internal/diff/hunks_test.go", true},
		{"internal/diff/hunks.go", false},
		{"tests/test_billing.py", true},
		{"tests/billing_test.py", true},
		{"src/billing.py", false},
		{"web/Button.test.ts", true},
		{"web/Button.test.tsx", true},
		{"web/Button.test.js", true},
		{"web/Button.test.jsx", true},
		{"web/Button.spec.ts", true},
		{"web/Button.spec.tsx", true},
		{"web/Button.spec.js", true},
		{"web/Button.spec.jsx", true},
		{"web/Button.tsx", false},
		{"crates/core/src/parser_test.rs", true},
		{"crates/core/src/parser.rs", false},
		{`internal\diff\hunks_test.go`, true},
		{"", false},
	}

	for _, tc := range cases {
		if got := IsTestFile(tc.path); got != tc.want {
			t.Errorf("IsTestFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
