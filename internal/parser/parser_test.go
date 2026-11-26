package parser

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseGoTestJSONAndTruncation(t *testing.T) {
	var lines []string
	// 12 failing tests to verify 10-failure truncation
	for i := 1; i <= 12; i++ {
		lines = append(lines,
			fmt.Sprintf(`{"Action":"run","Package":"github.com/acme/app/internal/diff","Test":"TestCase%02d"}`, i),
			fmt.Sprintf(`{"Action":"output","Package":"github.com/acme/app/internal/diff","Test":"TestCase%02d","Output":"    git_test.go:%d: expected ok for case %d\n"}`, i, 10+i, i),
			fmt.Sprintf(`{"Action":"fail","Package":"github.com/acme/app/internal/diff","Test":"TestCase%02d"}`, i),
		)
	}
	// Passing test should be ignored completely
	lines = append(lines,
		`{"Action":"run","Package":"github.com/acme/app/internal/diff","Test":"TestPass"}`,
		`{"Action":"pass","Package":"github.com/acme/app/internal/diff","Test":"TestPass"}`,
	)

	failures, truncated := Parse("go-test", "go test -json ./...", "", strings.Join(lines, "\n"))
	if len(failures) != 10 {
		t.Fatalf("expected 10 kept failures, got %d", len(failures))
	}
	if truncated != 2 {
		t.Fatalf("expected truncated_count=2, got %d", truncated)
	}
	if failures[0].File != "internal/diff/git_test.go" || failures[0].Line != 11 || failures[0].Symbol != "TestCase01" {
		t.Fatalf("unexpected first failure: %+v", failures[0])
	}
	if failures[0].SuggestedScope != "internal/diff" {
		t.Fatalf("expected suggested_scope=internal/diff, got %q", failures[0].SuggestedScope)
	}
}

func TestParseNodeTSCAndESLintAndVitest(t *testing.T) {
	tscOut := "src/calc.ts(18,9): error TS2322: Type 'string' is not assignable to type 'number'."
	f1, _ := Parse("tsc", "npx tsc --noEmit", "", tscOut)
	if len(f1) != 1 || f1[0].File != "src/calc.ts" || f1[0].Line != 18 || f1[0].Symbol != "TS2322" {
		t.Fatalf("unexpected tsc parse: %+v", f1)
	}

	eslintOut := "src/calc.ts\n  7:3  error  'unused' is defined but never used  @typescript-eslint/no-unused-vars\n"
	f2, _ := Parse("eslint", "npx eslint src/calc.ts", "", eslintOut)
	if len(f2) != 1 || f2[0].File != "src/calc.ts" || f2[0].Line != 7 || f2[0].Symbol != "@typescript-eslint/no-unused-vars" {
		t.Fatalf("unexpected eslint parse: %+v", f2)
	}

	vitestOut := " FAIL  src/calc.test.ts > adds numbers\nAssertionError: expected 5 to be 4\n ❯ src/calc.test.ts:9:18\n"
	f3, _ := Parse("vitest", "npx vitest related --run src/calc.ts", "", vitestOut)
	if len(f3) != 1 || f3[0].File != "src/calc.test.ts" || f3[0].Line != 9 {
		t.Fatalf("unexpected vitest parse: %+v", f3)
	}
}

func TestParsePythonRuffMypyAndPytest(t *testing.T) {
	ruffOut := "app/service.py:4:8: F401 [*] `sys` imported but unused\n"
	f1, _ := Parse("ruff", "ruff check app/service.py", "", ruffOut)
	if len(f1) != 1 || f1[0].File != "app/service.py" || f1[0].Line != 4 || f1[0].Symbol != "F401" {
		t.Fatalf("unexpected ruff parse: %+v", f1)
	}

	mypyOut := "app/service.py:12: error: Incompatible return value type (got \"str\", expected \"int\")  [return-value]\n"
	f2, _ := Parse("mypy", "mypy app/service.py", "", mypyOut)
	if len(f2) != 1 || f2[0].File != "app/service.py" || f2[0].Line != 12 {
		t.Fatalf("unexpected mypy parse: %+v", f2)
	}

	pytestOut := `F
================================== FAILURES ===================================
_________________________________ test_divide _________________________________
    def test_divide():
>       assert divide(10, 2) == 99
E       assert 5 == 99

tests/test_service.py:8: AssertionError
=========================== short test summary info ===========================
FAILED tests/test_service.py::test_divide - AssertionError: assert 5 == 99
1 failed in 0.04s
`
	f3, _ := Parse("pytest", "pytest -q tests/test_service.py", "", pytestOut)
	if len(f3) != 1 || f3[0].File != "tests/test_service.py" || f3[0].Line != 8 || f3[0].Symbol != "test_divide" {
		t.Fatalf("unexpected pytest parse: %+v", f3)
	}
}

func TestParseRustCargoJSONAndRustTest(t *testing.T) {
	cargoJSON := `{"reason":"compiler-message","message":{"message":"mismatched types","code":{"code":"E0308"},"level":"error","spans":[{"file_name":"src/lib.rs","line_start":14,"column_start":12,"is_primary":true}]}}`
	f1, _ := Parse("rust-cargo", "cargo check --message-format=json", "", cargoJSON)
	if len(f1) != 1 || f1[0].File != "src/lib.rs" || f1[0].Line != 14 || f1[0].Symbol != "E0308" {
		t.Fatalf("unexpected cargo JSON parse: %+v", f1)
	}

	rustTestOut := `running 1 test
test math::tests::test_sum ... FAILED

failures:

---- math::tests::test_sum stdout ----
thread 'math::tests::test_sum' panicked at src/math.rs:22:9:
assertion left == right failed: left: 4, right: 5
`
	f2, _ := Parse("rust-test", "cargo test --no-fail-fast", "", rustTestOut)
	if len(f2) != 1 || f2[0].File != "src/math.rs" || f2[0].Line != 22 || f2[0].Symbol != "math::tests::test_sum" {
		t.Fatalf("unexpected rust test parse: %+v", f2)
	}
}

func TestParseGoVetExePrefixAndGo124FailedBuildJSON(t *testing.T) {
	vetWin := "vet.exe: internal\\usage\\usage_test.go:12:12: undefined: EvaluateQuota\n"
	f1, _ := Parse("go-vet", "go vet ./...", "", vetWin)
	if len(f1) != 1 || f1[0].File != "internal/usage/usage_test.go" || f1[0].Line != 12 || f1[0].Column != 12 {
		t.Fatalf("expected structured go-vet failure from vet.exe prefix, got: %+v", f1)
	}

	go124BuildFail := strings.Join([]string{
		`{"Action":"build-output","ImportPath":"github.com/veriyum/veripod/services/metering/internal/usage","Output":"internal/usage/usage_test.go:12:12: undefined: EvaluateQuota\n"}`,
		`{"Action":"build-fail","ImportPath":"github.com/veriyum/veripod/services/metering/internal/usage"}`,
		`{"Action":"fail","Package":"github.com/veriyum/veripod/services/metering/internal/usage","FailedBuild":"github.com/veriyum/veripod/services/metering/internal/usage"}`,
	}, "\n")
	f2, _ := Parse("go-test", "go test -json ./...", "", go124BuildFail)
	if len(f2) != 1 || f2[0].File != "internal/usage/usage_test.go" || f2[0].Line != 12 || !strings.Contains(f2[0].Message, "undefined: EvaluateQuota") {
		t.Fatalf("expected structured Go 1.24 FailedBuild diagnostic, got: %+v", f2)
	}
}
