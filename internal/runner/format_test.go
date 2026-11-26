package runner

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatGitHub_AllPassed(t *testing.T) {
	report := &RunReport{
		Status:     StatusPassed,
		DurationMs: 145,
		Summary: Summary{
			TotalChecks: 2,
			Passed:      2,
		},
		Checks: []CheckResult{
			{Name: "vet", Status: StatusPassed, DurationMs: 50},
			{Name: "test", Status: StatusPassed, DurationMs: 95},
		},
	}

	got := FormatGitHub(report)
	want := "::notice title=quench::All checks passed in 145ms"
	if got != want {
		t.Fatalf("FormatGitHub() = %q, want %q", got, want)
	}
}

func TestFormatGitHub_ErrorsAndOmittedFields(t *testing.T) {
	report := &RunReport{
		Status:     StatusFailed,
		DurationMs: 210,
		Summary: Summary{
			TotalChecks: 3,
			Failed:      3,
		},
		Checks: []CheckResult{
			{
				Name:   "vet",
				Status: StatusFailed,
				Failures: []Failure{
					{
						File:    `internal\runner\foo.go`,
						Line:    42,
						Column:  7,
						Message: "unreachable code",
					},
					{
						File:    "internal/runner/bar.go",
						Line:    19,
						Column:  0,
						Message: "shadowed var",
					},
					{
						File:    "internal/runner/baz.go",
						Line:    0,
						Column:  0,
						Message: "file-level error",
					},
				},
			},
			{
				Name:   "test",
				Status: StatusFailed,
				Failures: []Failure{
					{
						Message: "package build failed",
					},
				},
			},
			{
				Name:   "lint",
				Status: StatusFailed,
			},
		},
	}

	got := FormatGitHub(report)
	lines := strings.Split(got, "\n")
	wantLines := []string{
		"::error file=internal/runner/foo.go,line=42,col=7,title=quench (vet)::unreachable code",
		"::error file=internal/runner/bar.go,line=19,title=quench (vet)::shadowed var",
		"::error file=internal/runner/baz.go,title=quench (vet)::file-level error",
		"::error title=quench (test)::package build failed",
		"::error title=quench (lint)::check failed",
	}

	if len(lines) != len(wantLines) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(wantLines), got)
	}
	for i := range wantLines {
		if lines[i] != wantLines[i] {
			t.Errorf("line %d:\n got: %q\nwant: %q", i, lines[i], wantLines[i])
		}
	}
}

func TestFormatGitHub_Escaping(t *testing.T) {
	report := &RunReport{
		Status: StatusFailed,
		Checks: []CheckResult{
			{
				Name:   "lint:go,strict",
				Status: StatusFailed,
				Failures: []Failure{
					{
						File:    "dir/a,b:c%1.go",
						Line:    3,
						Message: "100% failed\r\nline 2\nline 3",
					},
				},
			},
		},
	}

	got := FormatGitHub(report)
	want := "::error file=dir/a%2Cb%3Ac%251.go,line=3,title=quench (lint%3Ago%2Cstrict)::100%25 failed%0D%0Aline 2%0Aline 3"
	if got != want {
		t.Fatalf("FormatGitHub() escaping mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatSARIF_SchemaAndResults(t *testing.T) {
	report := &RunReport{
		Status:     StatusFailed,
		DurationMs: 320,
		Checks: []CheckResult{
			{
				Name:   "eslint",
				Status: StatusFailed,
				Failures: []Failure{
					{
						File:    `src\app.ts`,
						Line:    10,
						Column:  5,
						Symbol:  "no-unused-vars",
						Message: "'x' is assigned a value but never used",
					},
				},
			},
			{
				Name:   "go-vet",
				Status: StatusFailed,
				Failures: []Failure{
					{
						File:    "main.go",
						Line:    25,
						Message: "printf format mismatch",
					},
					{
						Message: "generic failure without location",
					},
				},
			},
		},
	}

	raw, err := FormatSARIF(report)
	if err != nil {
		t.Fatalf("FormatSARIF() returned error: %v", err)
	}

	var parsed struct {
		Schema  string `json:"$schema"`
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name           string `json:"name"`
					InformationURI string `json:"informationUri"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID  string `json:"ruleId"`
				Level   string `json:"level"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region *struct {
							StartLine   int `json:"startLine"`
							StartColumn int `json:"startColumn"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}

	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("invalid SARIF JSON: %v\n%s", err, string(raw))
	}

	if parsed.Schema != "https://json.schemastore.org/sarif-2.1.0.json" {
		t.Errorf("$schema = %q, want %q", parsed.Schema, "https://json.schemastore.org/sarif-2.1.0.json")
	}
	if parsed.Version != "2.1.0" {
		t.Errorf("version = %q, want %q", parsed.Version, "2.1.0")
	}
	if len(parsed.Runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(parsed.Runs))
	}

	driver := parsed.Runs[0].Tool.Driver
	if driver.Name != "quench" {
		t.Errorf("driver.name = %q, want %q", driver.Name, "quench")
	}
	if driver.InformationURI != "https://github.com/akhanatoon/quench" {
		t.Errorf("driver.informationUri = %q, want %q", driver.InformationURI, "https://github.com/akhanatoon/quench")
	}

	results := parsed.Runs[0].Results
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}

	// Result 0: uses f.Symbol as ruleId, has uri + startLine + startColumn
	r0 := results[0]
	if r0.RuleID != "no-unused-vars" {
		t.Errorf("results[0].ruleId = %q, want %q", r0.RuleID, "no-unused-vars")
	}
	if r0.Level != "error" {
		t.Errorf("results[0].level = %q, want %q", r0.Level, "error")
	}
	if r0.Message.Text != "'x' is assigned a value but never used" {
		t.Errorf("results[0].message.text = %q", r0.Message.Text)
	}
	if len(r0.Locations) != 1 {
		t.Fatalf("len(results[0].locations) = %d, want 1", len(r0.Locations))
	}
	loc0 := r0.Locations[0].PhysicalLocation
	if loc0.ArtifactLocation.URI != "src/app.ts" {
		t.Errorf("results[0].uri = %q, want %q", loc0.ArtifactLocation.URI, "src/app.ts")
	}
	if loc0.Region == nil || loc0.Region.StartLine != 10 || loc0.Region.StartColumn != 5 {
		t.Errorf("results[0].region = %+v, want startLine=10 startColumn=5", loc0.Region)
	}

	// Result 1: falls back to check.Name as ruleId, has startLine only
	r1 := results[1]
	if r1.RuleID != "go-vet" {
		t.Errorf("results[1].ruleId = %q, want %q", r1.RuleID, "go-vet")
	}
	if len(r1.Locations) != 1 || r1.Locations[0].PhysicalLocation.ArtifactLocation.URI != "main.go" {
		t.Fatalf("results[1].locations unexpected: %+v", r1.Locations)
	}
	if r1.Locations[0].PhysicalLocation.Region == nil || r1.Locations[0].PhysicalLocation.Region.StartLine != 25 {
		t.Errorf("results[1].region = %+v, want startLine=25", r1.Locations[0].PhysicalLocation.Region)
	}

	// Result 2: no file -> no locations
	r2 := results[2]
	if len(r2.Locations) != 0 {
		t.Errorf("results[2].locations = %+v, want empty", r2.Locations)
	}
}

func TestFormatSARIF_AllPassedEmptyResults(t *testing.T) {
	report := &RunReport{
		Status: StatusPassed,
		Checks: []CheckResult{
			{Name: "test", Status: StatusPassed},
		},
	}

	raw, err := FormatSARIF(report)
	if err != nil {
		t.Fatalf("FormatSARIF() error: %v", err)
	}

	if !strings.Contains(string(raw), `"results": []`) {
		t.Fatalf("expected empty JSON array for results, got:\n%s", string(raw))
	}
}
