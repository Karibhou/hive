package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hivecommons/hive/pkg/config"
	"github.com/hivecommons/hive/pkg/review"
)

// Wiring for review.reviewers.autoscale (hivecommons/hive#14685).

func autoscaleWiringConfig(enabled bool) *config.Config {
	cfg := &config.Config{}
	cfg.Agents = map[string]config.AgentConfig{
		"reviewer": {Enabled: true, Replicas: 1},
		"scanner":  {Enabled: true, Replicas: 1},
	}
	cfg.Review.Reviewers.Autoscale = config.ReviewerAutoscaleConfig{Enabled: enabled, Min: 1, Max: 3, Cooldown: "1m"}
	if err := cfg.ExpandAgentReplicas(); err != nil {
		panic(err)
	}
	return cfg
}

func TestReviewerReplicaLimitsOffIsNil(t *testing.T) {
	var as review.ReviewerAutoscaler
	if got := reviewerReplicaLimits(autoscaleWiringConfig(false), &as); got != nil {
		t.Fatalf("limits with autoscale off = %v, want nil", got)
	}
	if got := reviewerReplicaLimits(autoscaleWiringConfig(true), &as); got["reviewer"] != 1 || len(got) != 1 {
		t.Fatalf("limits = %v, want reviewer:1", got)
	}
}

// Inactive replicas get no cadence kick, are reported as skipped, and every
// other agent (and repo-scoped cadence key) passes through.
func TestWithholdInactiveReviewerReplicas(t *testing.T) {
	cfg := autoscaleWiringConfig(true)
	var as review.ReviewerAutoscaler
	due := []string{"reviewer", "reviewer-2", config.CadenceTargetKey("reviewer-3", "acme/hive"), "scanner"}
	kept, skipped := withholdInactiveReviewerReplicas(due, nil, cfg, &as)
	if strings.Join(kept, ",") != "reviewer,scanner" {
		t.Fatalf("kept = %v, want reviewer and scanner", kept)
	}
	if len(skipped) != 2 || !strings.HasSuffix(skipped[0], "(autoscale-inactive)") {
		t.Fatalf("skipped = %v", skipped)
	}

	kept, skipped = withholdInactiveReviewerReplicas(due, nil, autoscaleWiringConfig(false), &as)
	if len(kept) != len(due) || len(skipped) != 0 {
		t.Fatalf("autoscale off: kept %v skipped %v, want everything kept", kept, skipped)
	}
}

func TestDecideReviewerAutoscaleScalesFromPlanBacklog(t *testing.T) {
	cfg := autoscaleWiringConfig(true)
	var as review.ReviewerAutoscaler
	plan := review.DispatchPlan{ReviewerStarved: []string{"a#1@x", "a#2@x", "a#3@x"}}
	t0 := time.Date(2026, 10, 27, 20, 0, 0, 0, time.UTC)
	decideReviewerAutoscale(cfg, &as, plan, review.ReviewerAutoscaleSignals{Now: t0}, nil)
	d := decideReviewerAutoscale(cfg, &as, plan, review.ReviewerAutoscaleSignals{Now: t0.Add(time.Minute)}, nil)
	if d.Action != review.AutoscaleUp || d.To != 2 {
		t.Fatalf("decision = %+v, want up to 2", d)
	}
	if got := reviewerReplicaLimits(cfg, &as)["reviewer"]; got != 2 {
		t.Fatalf("next cycle limit = %d, want 2", got)
	}
	if d := decideReviewerAutoscale(autoscaleWiringConfig(false), &as, plan, review.ReviewerAutoscaleSignals{Now: t0}, nil); d.Action != "" {
		t.Fatalf("autoscale off decided %+v", d)
	}
}

// Only a prompt-not-ready failure on the scaled agent's own replicas counts
// as saturation.
func TestNoteReviewerAutoscaleKickFailure(t *testing.T) {
	cfg := autoscaleWiringConfig(true)
	t0 := time.Date(2026, 10, 27, 20, 0, 0, 0, time.UTC)
	busy := review.DispatchPlan{ReviewerStarved: []string{"a#1@x", "a#2@x", "a#3@x"}}
	scaleUp := func(as *review.ReviewerAutoscaler) review.ReviewerAutoscaleDecision {
		decideReviewerAutoscale(cfg, as, busy, review.ReviewerAutoscaleSignals{Now: t0.Add(10 * time.Second)}, nil)
		return decideReviewerAutoscale(cfg, as, busy, review.ReviewerAutoscaleSignals{Now: t0.Add(20 * time.Second)}, nil)
	}
	promptErr := errors.New("agent reviewer-2 CLI did not reach input prompt")

	var saturated review.ReviewerAutoscaler
	noteReviewerAutoscaleKickFailure(cfg, &saturated, "reviewer-2", promptErr, t0)
	if d := scaleUp(&saturated); d.Changed() {
		t.Fatalf("scaled up right after saturation: %+v", d)
	}

	for name, tc := range map[string]struct {
		agent string
		err   error
	}{
		"other agent":   {"scanner", promptErr},
		"other failure": {"reviewer-2", errors.New("tmux: no server running")},
	} {
		var as review.ReviewerAutoscaler
		noteReviewerAutoscaleKickFailure(cfg, &as, tc.agent, tc.err, t0)
		if d := scaleUp(&as); !d.Changed() {
			t.Fatalf("%s: counted as saturation, decision %+v", name, d)
		}
	}
}
