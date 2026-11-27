package history

import (
	"os"
	"testing"
	"time"

	"github.com/akhanatoon/quench/internal/runner"
)

func TestAppendLoadSummarizeAndReset(t *testing.T) {
	t.Setenv("QUENCH_HISTORY_DIR", t.TempDir())
	tmp := t.TempDir()
	now := time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC)

	records := []Record{
		{
			Timestamp:   now.Add(-10 * 24 * time.Hour), // outside 7-day window
			Source:      "cli",
			Mode:        "local",
			Status:      runner.StatusPassed,
			DurationMs:  5000,
			RawBytes:    50000,
			ReportBytes: 1000,
			TotalChecks: 4,
			Passed:      2,
			Skipped:     2,
		},
		{
			Timestamp:   now.Add(-2 * time.Hour),
			Source:      "cli",
			Mode:        "docker-warm",
			Status:      runner.StatusPassed,
			DurationMs:  4000,
			RawBytes:    40000,
			ReportBytes: 800,
			TotalChecks: 4,
			Passed:      1,
			Skipped:     3,
		},
		{
			Timestamp:   now.Add(-1 * time.Hour),
			Source:      "mcp",
			Mode:        "local",
			Status:      runner.StatusFailed,
			DurationMs:  2000,
			RawBytes:    20000,
			ReportBytes: 1200,
			TotalChecks: 4,
			Failed:      1,
			Skipped:     3,
			Cached:      true,
		},
	}

	for _, r := range records {
		if err := Append(tmp, r); err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}

	// Inject a corrupt line to verify resilience.
	f, err := os.OpenFile(Path(tmp), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{corrupt json line\n")
	_ = f.Close()

	all, err := loadAt(tmp, 0, now)
	if err != nil {
		t.Fatalf("loadAt(all) failed: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 records, got %d", len(all))
	}

	recent, err := loadAt(tmp, 7, now)
	if err != nil {
		t.Fatalf("loadAt(7) failed: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent records, got %d", len(recent))
	}

	sum := Summarize(recent, 7)
	if sum.TotalRuns != 2 || sum.CLIRuns != 1 || sum.MCPRuns != 1 {
		t.Fatalf("unexpected run counts: %+v", sum)
	}
	if sum.PassedRuns != 1 || sum.FailedRuns != 1 || sum.CachedRuns != 1 {
		t.Fatalf("unexpected status/cache counts: %+v", sum)
	}
	if sum.PassRatePct != 50.0 {
		t.Fatalf("expected PassRatePct=50.0, got %.1f", sum.PassRatePct)
	}
	if sum.TotalRawBytes != 60000 || sum.TotalReportBytes != 2000 {
		t.Fatalf("unexpected byte totals: %+v", sum)
	}
	if sum.ReductionPct != 96.7 {
		t.Fatalf("expected ReductionPct=96.7, got %.1f", sum.ReductionPct)
	}
	if sum.EstTokensSaved != (60000-2000)/4 {
		t.Fatalf("unexpected EstTokensSaved: %d", sum.EstTokensSaved)
	}
	if sum.TotalChecksSkipped != 6 {
		t.Fatalf("expected TotalChecksSkipped=6, got %d", sum.TotalChecksSkipped)
	}

	if err := Reset(tmp); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}
	afterReset, err := loadAt(tmp, 0, now)
	if err != nil {
		t.Fatalf("loadAt after reset failed: %v", err)
	}
	if len(afterReset) != 0 {
		t.Fatalf("expected 0 records after reset, got %d", len(afterReset))
	}
}
