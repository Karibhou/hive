package github

import (
	"os"
	"strings"
	"testing"
)

func TestEmptyDedupeAdvisory(t *testing.T) {
	data, err := os.ReadFile("testdata/dedupe-15381.txt")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	cases := []struct {
		name, body string
		want       bool
	}{
		{"reported body", body, true},
		{"unchecked task", body + "\n- [ ] #1 close duplicate\n", false},
		{"duplicate", strings.Replace(body, "0 near-duplicate pairs", "1 near-duplicate pairs", 1), false},
		{"missing link", strings.Replace(body, "0 missing PR↔issue links", "2 missing PR↔issue links", 1), false},
		{"resolved", strings.Replace(body, "0 near-duplicate pairs", "0 near-duplicate pairs, 1 resolved candidate", 1), false},
		{"unknown count", strings.Replace(body, "0 near-duplicate pairs", "no near-duplicate pairs", 1), false},
		{"decision", strings.Replace(body, "Decision required: no", "Decision required: yes", 1), false},
		{"other issue", strings.Replace(body, "hive:dedupe-checklist", "other", 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := emptyDedupeAdvisory(tc.body); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
