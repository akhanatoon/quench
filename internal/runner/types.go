package runner

import "github.com/akhanatoon/quench/internal/parser"

type Failure = parser.Failure

const (
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

type CheckResult struct {
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	DurationMs     int64     `json:"duration_ms"`
	Command        string    `json:"command,omitempty"`
	SkipReason     string    `json:"skip_reason,omitempty"`
	TruncatedCount int       `json:"truncated_count,omitempty"`
	Failures       []Failure `json:"failures,omitempty"`
}

type Summary struct {
	TotalChecks int `json:"total_checks"`
	Passed      int `json:"passed"`
	Failed      int `json:"failed"`
	Skipped     int `json:"skipped"`
}

type RunReport struct {
	Status             string        `json:"status"`
	Mode               string        `json:"mode"`
	ProjectDir         string        `json:"project_dir"`
	DurationMs         int64         `json:"duration_ms"`
	FullSuiteTriggered bool          `json:"full_suite_triggered,omitempty"`
	ScopeReason        string        `json:"scope_reason,omitempty"`
	ChangedFiles       []string      `json:"changed_files"`
	Summary            Summary       `json:"summary"`
	Checks             []CheckResult `json:"checks"`
}
