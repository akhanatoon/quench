package runner

import (
	"reflect"
	"strings"
	"testing"

	"github.com/akhanatoon/quench/internal/config"
)

func TestBuildWaves_MultiWaveAndParallelSiblings(t *testing.T) {
	// Declared out of topological order to verify deterministic index sorting within each wave.
	checks := []config.CheckConfig{
		{Name: "deploy", DependsOn: []string{"test", "build"}}, // idx 0 -> wave 2
		{Name: "test", DependsOn: []string{"lint"}},            // idx 1 -> wave 1
		{Name: "lint"},                                         // idx 2 -> wave 0
		{Name: "build", DependsOn: []string{"lint", "vet"}},    // idx 3 -> wave 1
		{Name: "vet"},                                          // idx 4 -> wave 0
	}

	waves, err := BuildWaves(checks)
	if err != nil {
		t.Fatalf("BuildWaves failed: %v", err)
	}
	want := [][]int{
		{2, 4}, // lint, vet
		{1, 3}, // test, build
		{0},    // deploy
	}
	if !reflect.DeepEqual(waves, want) {
		t.Fatalf("BuildWaves() = %v, want %v", waves, want)
	}
}

func TestBuildWaves_MissingFilteredDeps(t *testing.T) {
	// Simulates --only test,e2e where "lint" and "typecheck" were filtered out.
	checks := []config.CheckConfig{
		{Name: "test", DependsOn: []string{"lint", "typecheck"}},
		{Name: "e2e", DependsOn: []string{"test", "lint"}},
	}

	waves, err := BuildWaves(checks)
	if err != nil {
		t.Fatalf("BuildWaves failed: %v", err)
	}
	want := [][]int{
		{0}, // test (missing deps ignored)
		{1}, // e2e
	}
	if !reflect.DeepEqual(waves, want) {
		t.Fatalf("BuildWaves() = %v, want %v", waves, want)
	}
}

func TestBuildWaves_CycleDetection(t *testing.T) {
	t.Run("two-node cycle", func(t *testing.T) {
		checks := []config.CheckConfig{
			{Name: "a", DependsOn: []string{"b"}},
			{Name: "b", DependsOn: []string{"a"}},
		}
		_, err := BuildWaves(checks)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cycle") {
			t.Fatalf("expected cycle error, got: %v", err)
		}
	})

	t.Run("three-node cycle with independent node", func(t *testing.T) {
		checks := []config.CheckConfig{
			{Name: "ok"},
			{Name: "a", DependsOn: []string{"c"}},
			{Name: "b", DependsOn: []string{"a"}},
			{Name: "c", DependsOn: []string{"b"}},
		}
		_, err := BuildWaves(checks)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cycle") {
			t.Fatalf("expected cycle error, got: %v", err)
		}
	})

	t.Run("self cycle", func(t *testing.T) {
		checks := []config.CheckConfig{
			{Name: "a", DependsOn: []string{"a"}},
		}
		_, err := BuildWaves(checks)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cycle") {
			t.Fatalf("expected cycle error, got: %v", err)
		}
	})
}

func TestBuildWaves_EmptyAndDuplicateDeps(t *testing.T) {
	if waves, err := BuildWaves(nil); err != nil || waves != nil {
		t.Fatalf("expected (nil, nil) for empty checks, got (%v, %v)", waves, err)
	}

	checks := []config.CheckConfig{
		{Name: "lint"},
		{Name: "test", DependsOn: []string{"lint", "lint"}},
	}
	waves, err := BuildWaves(checks)
	if err != nil {
		t.Fatalf("BuildWaves failed with duplicate dep: %v", err)
	}
	want := [][]int{{0}, {1}}
	if !reflect.DeepEqual(waves, want) {
		t.Fatalf("BuildWaves() = %v, want %v", waves, want)
	}
}

func TestDAGFailedDependency(t *testing.T) {
	ch := config.CheckConfig{
		Name:      "e2e",
		DependsOn: []string{"lint", "build", "test"},
	}

	t.Run("all passed or diff-skipped", func(t *testing.T) {
		status := map[string]string{
			"lint":  StatusPassed,
			"build": StatusSkipped,
			"test":  StatusPassed,
		}
		if dep, failed := FailedDependency(ch, status); failed || dep != "" {
			t.Fatalf("expected no failed dependency, got (%q, %v)", dep, failed)
		}
	})

	t.Run("direct failed dependency", func(t *testing.T) {
		status := map[string]string{
			"lint":  StatusPassed,
			"build": StatusFailed,
			"test":  StatusFailed,
		}
		dep, failed := FailedDependency(ch, status)
		if !failed || dep != "build" {
			t.Fatalf("expected (build, true), got (%q, %v)", dep, failed)
		}
	})

	t.Run("transitive dep-failed dependency", func(t *testing.T) {
		status := map[string]string{
			"lint":  StatusPassed,
			"build": StatusDepFailed,
			"test":  StatusPassed,
		}
		dep, failed := FailedDependency(ch, status)
		if !failed || dep != "build" {
			t.Fatalf("expected (build, true), got (%q, %v)", dep, failed)
		}
	})
}

func TestDAGConfigValidation(t *testing.T) {
	baseCfg := func(checks []config.CheckConfig) *config.Config {
		return &config.Config{
			Version: 1,
			Mode:    config.ModeLocal,
			Checks:  checks,
		}
	}

	t.Run("valid DAG", func(t *testing.T) {
		cfg := baseCfg([]config.CheckConfig{
			{Name: "lint", Command: "echo lint", Scope: config.ScopeProject},
			{Name: "test", Command: "echo test", Scope: config.ScopeProject, DependsOn: []string{"lint"}},
		})
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("expected valid DAG, got: %v", err)
		}
	})

	t.Run("unknown dependency", func(t *testing.T) {
		cfg := baseCfg([]config.CheckConfig{
			{Name: "test", Command: "echo test", Scope: config.ScopeProject, DependsOn: []string{"nonexistent"}},
		})
		if err := config.Validate(cfg); err == nil {
			t.Fatalf("expected error for unknown dependency")
		}
	})

	t.Run("self dependency", func(t *testing.T) {
		cfg := baseCfg([]config.CheckConfig{
			{Name: "test", Command: "echo test", Scope: config.ScopeProject, DependsOn: []string{"test"}},
		})
		err := config.Validate(cfg)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cycle") {
			t.Fatalf("expected cycle error for self dependency, got: %v", err)
		}
	})

	t.Run("multi-node cycle", func(t *testing.T) {
		cfg := baseCfg([]config.CheckConfig{
			{Name: "a", Command: "echo a", Scope: config.ScopeProject, DependsOn: []string{"b"}},
			{Name: "b", Command: "echo b", Scope: config.ScopeProject, DependsOn: []string{"c"}},
			{Name: "c", Command: "echo c", Scope: config.ScopeProject, DependsOn: []string{"a"}},
		})
		err := config.Validate(cfg)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cycle") {
			t.Fatalf("expected cycle error, got: %v", err)
		}
	})
}
