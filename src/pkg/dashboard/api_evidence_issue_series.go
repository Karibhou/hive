package dashboard

import (
	"path/filepath"
	"strings"

	"github.com/hivecommons/hive/pkg/evidence"
)

type evidenceIssueSeriesWeek struct {
	Repo string
	evidence.IssueWeek
}

// loadEvidenceIssueSeries reads the weekly files written by backfill-issues.
// Missing or malformed files leave the existing audit fallback available.
func loadEvidenceIssueSeries(root string) []evidenceIssueSeriesWeek {
	paths, err := filepath.Glob(filepath.Join(root, "*", "*", "issues", "*.json"))
	if err != nil {
		return nil
	}
	var out []evidenceIssueSeriesWeek
	for _, path := range paths {
		repoDir := filepath.Dir(filepath.Dir(path))
		repo := filepath.Base(filepath.Dir(repoDir)) + "/" + filepath.Base(repoDir)
		week := strings.TrimSuffix(filepath.Base(path), ".json")
		w, err := evidence.ReadIssueWeek(root, repo, week)
		if err != nil || w.Events == nil {
			continue
		}
		valid := true
		for _, event := range w.Events {
			if event.At.IsZero() || evidenceWeekKey(event.At) != w.Week || event.At.Before(w.CoveredFrom) || !event.At.Before(w.CoveredTo) {
				valid = false
			}
			switch event.Action {
			case "opened":
				if event.Kind != evidence.AuthorHuman && event.Kind != evidence.AuthorAgent && event.Kind != evidence.AuthorBot {
					valid = false
				}
			case "closed":
			default:
				valid = false
			}
		}
		if valid {
			out = append(out, evidenceIssueSeriesWeek{Repo: repo, IssueWeek: w})
		}
	}
	return out
}
