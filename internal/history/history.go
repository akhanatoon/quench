package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/akhanatoon/quench/internal/runner"
)

const (
	DirName  = ".quench"
	FileName = "history.jsonl"
)

var appendMu sync.Mutex

// Record stores non-sensitive aggregate telemetry for a single verification run.
type Record struct {
	Timestamp   time.Time `json:"ts"`
	Source      string    `json:"source"` // "cli" or "mcp"
	Mode        string    `json:"mode"`
	Status      string    `json:"status"`
	DurationMs  int64     `json:"duration_ms"`
	RawBytes    int64     `json:"raw_bytes"`
	ReportBytes int64     `json:"report_bytes"`
	TotalChecks int       `json:"total_checks"`
	Passed      int       `json:"passed"`
	Failed      int       `json:"failed"`
	Skipped     int       `json:"skipped"`
	Cached      bool      `json:"cached"`
}

// Summary aggregates metrics across a window of recorded runs.
type Summary struct {
	Days               int     `json:"days"`
	TotalRuns          int     `json:"total_runs"`
	CLIRuns            int     `json:"cli_runs"`
	MCPRuns            int     `json:"mcp_runs"`
	PassedRuns         int     `json:"passed_runs"`
	FailedRuns         int     `json:"failed_runs"`
	CachedRuns         int     `json:"cached_runs"`
	PassRatePct        float64 `json:"pass_rate_pct"`
	TotalDurationMs    int64   `json:"total_duration_ms"`
	AvgDurationMs      int64   `json:"avg_duration_ms"`
	TotalRawBytes      int64   `json:"total_raw_bytes"`
	TotalReportBytes   int64   `json:"total_report_bytes"`
	ReductionPct       float64 `json:"reduction_pct"`
	EstTokensSaved     int64   `json:"est_tokens_saved"`
	TotalChecksSkipped int     `json:"total_checks_skipped"`
}

// Path returns the per-repository JSONL history path under the user cache directory
// (never polluting the repository working tree or git status).
func Path(projectDir string) string {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	id := hex.EncodeToString(sum[:])[:12]
	if override := strings.TrimSpace(os.Getenv("QUENCH_HISTORY_DIR")); override != "" {
		return filepath.Join(override, id+".jsonl")
	}
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "quench", "history", id+".jsonl")
}

// FromReport converts a RunReport into a lightweight, secret-free telemetry Record.
func FromReport(source string, report *runner.RunReport) Record {
	rec := Record{
		Timestamp: time.Now().UTC(),
		Source:    source,
	}
	if report == nil {
		return rec
	}
	rec.Mode = report.Mode
	rec.Status = report.Status
	rec.DurationMs = report.DurationMs
	rec.TotalChecks = report.Summary.TotalChecks
	rec.Passed = report.Summary.Passed
	rec.Failed = report.Summary.Failed
	rec.Skipped = report.Summary.Skipped
	rec.Cached = report.Cached
	if report.OutputStats != nil {
		rec.RawBytes = int64(report.OutputStats.RawBytes)
		rec.ReportBytes = int64(report.OutputStats.ReportBytes)
	}
	return rec
}

// Append writes one JSONL record to <projectDir>/.quench/history.jsonl.
func Append(projectDir string, rec Record) error {
	if strings.TrimSpace(projectDir) == "" {
		return nil
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	appendMu.Lock()
	defer appendMu.Unlock()

	p := Path(projectDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// Load reads recorded runs from <projectDir>/.quench/history.jsonl.
// When days > 0, only records within the last days*24h are returned.
func Load(projectDir string, days int) ([]Record, error) {
	return loadAt(projectDir, days, time.Now().UTC())
}

func loadAt(projectDir string, days int, now time.Time) ([]Record, error) {
	p := Path(projectDir)
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var cutoff time.Time
	if days > 0 {
		cutoff = now.Add(-time.Duration(days) * 24 * time.Hour)
	}

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if !cutoff.IsZero() && !rec.Timestamp.IsZero() && rec.Timestamp.Before(cutoff) {
			continue
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// Reset removes the local history file if present.
func Reset(projectDir string) error {
	err := os.Remove(Path(projectDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Summarize aggregates records into a Summary.
func Summarize(records []Record, days int) Summary {
	s := Summary{
		Days:      days,
		TotalRuns: len(records),
	}
	if len(records) == 0 {
		return s
	}
	for _, r := range records {
		switch strings.ToLower(r.Source) {
		case "mcp":
			s.MCPRuns++
		default:
			s.CLIRuns++
		}
		if r.Status == runner.StatusFailed {
			s.FailedRuns++
		} else {
			s.PassedRuns++
		}
		if r.Cached {
			s.CachedRuns++
		}
		s.TotalDurationMs += r.DurationMs
		s.TotalRawBytes += r.RawBytes
		s.TotalReportBytes += r.ReportBytes
		s.TotalChecksSkipped += r.Skipped
	}
	s.AvgDurationMs = s.TotalDurationMs / int64(s.TotalRuns)
	s.PassRatePct = round1(float64(s.PassedRuns) * 100 / float64(s.TotalRuns))
	if s.TotalRawBytes > 0 && s.TotalRawBytes > s.TotalReportBytes {
		savedBytes := s.TotalRawBytes - s.TotalReportBytes
		s.ReductionPct = round1(float64(savedBytes) * 100 / float64(s.TotalRawBytes))
		s.EstTokensSaved = savedBytes / 4
	}
	return s
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
