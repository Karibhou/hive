package github

import (
	"regexp"
	"strings"
)

var (
	dedupeTaskLine       = regexp.MustCompile(`(?mi)^\s*[-*+]\s+\[ \]`)
	dedupeNearCounts     = regexp.MustCompile(`(?i)\b([0-9]+)\s+near[- ]duplicate\s+pairs?\b`)
	dedupeLinkCounts     = regexp.MustCompile(`(?i)\b([0-9]+)\s+missing\s+PR\s*(?:↔|<->|/)\s*issue\s+links?\b`)
	dedupeResolvedCounts = regexp.MustCompile(`(?i)\b([0-9]+)\s+(?:items?\s+(?:that\s+appear\s+)?(?:already\s+)?resolved|resolved\s+(?:items?|candidates?))\b`)
)

// emptyDedupeAdvisory only refuses bodies with explicit zero counts for the
// two principal finding classes. Unknown formats remain eligible for filing.
func emptyDedupeAdvisory(body string) bool {
	_, verdict, ok := parseDedupeChecklist(body)
	if !ok || verdict != dedupeVerdictNo || dedupeTaskLine.MatchString(body) {
		return false
	}
	summary := body
	if i := strings.Index(strings.ToLower(summary), "## summary"); i >= 0 {
		summary = summary[i+len("## summary"):]
		if j := strings.Index(summary, "\n## "); j >= 0 {
			summary = summary[:j]
		}
	} else {
		return false
	}
	for _, pattern := range []*regexp.Regexp{dedupeNearCounts, dedupeLinkCounts} {
		counts := pattern.FindAllStringSubmatch(summary, -1)
		if len(counts) == 0 {
			return false
		}
		for _, count := range counts {
			if count[1] != "0" {
				return false
			}
		}
	}
	for _, count := range dedupeResolvedCounts.FindAllStringSubmatch(summary, -1) {
		if count[1] != "0" {
			return false
		}
	}
	return true
}
