package holdguard

import "testing"

// #14534: only the merge of the base into the requested head is hygiene.
func TestIsHygieneMerge(t *testing.T) {
	tests := []struct {
		name      string
		head      Commit
		requested string
		base      string
		want      bool
	}{
		{"merge of base into requested head", Commit{SHA: "m", Parents: []string{"h", "b"}}, "h", "b", true},
		{"single-parent push on the requested head", Commit{SHA: "m", Parents: []string{"h"}}, "h", "b", false},
		{"merge of a non-base commit", Commit{SHA: "m", Parents: []string{"h", "x"}}, "h", "b", false},
		{"merge into another head", Commit{SHA: "m", Parents: []string{"x", "b"}}, "h", "b", false},
		{"parents swapped", Commit{SHA: "m", Parents: []string{"b", "h"}}, "h", "b", false},
		{"octopus merge", Commit{SHA: "m", Parents: []string{"h", "b", "x"}}, "h", "b", false},
		{"no parent information", Commit{SHA: "m"}, "h", "b", false},
		{"unknown base", Commit{SHA: "m", Parents: []string{"h", ""}}, "h", "", false},
		{"unknown requested head", Commit{SHA: "m", Parents: []string{"", "b"}}, "", "b", false},
		{"empty head SHA", Commit{Parents: []string{"h", "b"}}, "h", "b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsHygieneMerge(tt.head, tt.requested, tt.base); got != tt.want {
				t.Fatalf("IsHygieneMerge(%+v, %q, %q) = %v, want %v", tt.head, tt.requested, tt.base, got, tt.want)
			}
		})
	}
}
