package runner

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	sarifSchemaURL      = "https://json.schemastore.org/sarif-2.1.0.json"
	sarifVersion        = "2.1.0"
	sarifToolName       = "quench"
	sarifInformationURI = "https://github.com/akhanatoon/quench"
)

// FormatGitHub renders a RunReport as native GitHub Actions workflow command annotations.
func FormatGitHub(r *RunReport) string {
	if r == nil {
		return ""
	}

	var lines []string
	for _, ch := range r.Checks {
		if len(ch.Failures) == 0 {
			if ch.Status == StatusFailed {
				title := escapeGitHubProperty(fmt.Sprintf("quench (%s)", ch.Name))
				lines = append(lines, fmt.Sprintf("::error title=%s::check failed", title))
			}
			continue
		}

		for _, f := range ch.Failures {
			var props []string
			if f.File != "" {
				props = append(props, "file="+escapeGitHubProperty(normalizeSlashPath(f.File)))
			}
			if f.Line > 0 {
				props = append(props, fmt.Sprintf("line=%d", f.Line))
			}
			if f.Column > 0 {
				props = append(props, fmt.Sprintf("col=%d", f.Column))
			}
			props = append(props, "title="+escapeGitHubProperty(fmt.Sprintf("quench (%s)", ch.Name)))

			msg := failureMessage(f)
			lines = append(lines, fmt.Sprintf("::error %s::%s", strings.Join(props, ","), escapeGitHubData(msg)))
		}
	}

	if len(lines) == 0 && r.Status == StatusPassedWithFiltered {
		totalFiltered := 0
		for _, ch := range r.Checks {
			totalFiltered += ch.FilteredOut
		}
		return fmt.Sprintf("::warning title=quench::Checks passed in %dms (%d legacy lint issues outside git diff filtered)", r.DurationMs, totalFiltered)
	}
	if len(lines) == 0 && r.Status != StatusFailed {
		return fmt.Sprintf("::notice title=quench::All checks passed in %dms", r.DurationMs)
	}

	return strings.Join(lines, "\n")
}

func normalizeSlashPath(p string) string {
	return strings.ReplaceAll(filepath.ToSlash(p), "\\", "/")
}

func escapeGitHubData(s string) string {
	r := strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
	)
	return r.Replace(s)
}

func escapeGitHubProperty(s string) string {
	r := strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
		":", "%3A",
		",", "%2C",
	)
	return r.Replace(s)
}

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string `json:"name"`
	InformationURI string `json:"informationUri"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations,omitempty"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           *sarifRegion          `json:"region,omitempty"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine,omitempty"`
	StartColumn int `json:"startColumn,omitempty"`
}

// FormatSARIF renders a RunReport as a SARIF 2.1.0 JSON document.
func FormatSARIF(r *RunReport) ([]byte, error) {
	results := make([]sarifResult, 0)

	if r != nil {
		for _, ch := range r.Checks {
			for _, f := range ch.Failures {
				ruleID := f.Symbol
				if ruleID == "" {
					ruleID = ch.Name
				}
				if ruleID == "" {
					ruleID = sarifToolName
				}

				res := sarifResult{
					RuleID:  ruleID,
					Level:   "error",
					Message: sarifMessage{Text: failureMessage(f)},
				}

				if f.File != "" {
					phys := sarifPhysicalLocation{
						ArtifactLocation: sarifArtifactLocation{
							URI: normalizeSlashPath(f.File),
						},
					}
					if f.Line > 0 || f.Column > 0 {
						reg := &sarifRegion{}
						if f.Line > 0 {
							reg.StartLine = f.Line
						}
						if f.Column > 0 {
							reg.StartColumn = f.Column
						}
						phys.Region = reg
					}
					res.Locations = []sarifLocation{{PhysicalLocation: phys}}
				}

				results = append(results, res)
			}
		}
	}

	log := sarifLog{
		Schema:  sarifSchemaURL,
		Version: sarifVersion,
		Runs: []sarifRun{
			{
				Tool: sarifTool{
					Driver: sarifDriver{
						Name:           sarifToolName,
						InformationURI: sarifInformationURI,
					},
				},
				Results: results,
			},
		},
	}

	return json.MarshalIndent(log, "", "  ")
}

func failureMessage(f Failure) string {
	if strings.TrimSpace(f.Message) != "" {
		return f.Message
	}
	if strings.TrimSpace(f.RawExcerpt) != "" {
		return f.RawExcerpt
	}
	return "check failed"
}
