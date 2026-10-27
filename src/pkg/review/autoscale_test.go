package review

import (
	"errors"
	"testing"
	"time"

	"github.com/hivecommons/hive/pkg/config"
)

// Decision-table tests for the reviewer autoscaler (hivecommons/hive#14685).
// Every cycle is driven through an injected clock; nothing sleeps.

var autoscaleT0 = time.Date(2026, 10, 27, 19, 20, 0, 0, time.UTC)

func autoscaleCfg() config.ReviewerAutoscaleConfig {
	return config.ReviewerAutoscaleConfig{Enabled: true, Min: 2, Max: 4}
}

func waitingKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = ReviewerStarvedKey("acme/hive", i+1, "sha")
	}
	return out
}

// run feeds cycles one minute apart starting at start and returns the last
// decision.
func runCycles(s *ReviewerAutoscaler, cfg config.ReviewerAutoscaleConfig, start time.Time, cycles int, sig ReviewerAutoscaleSignals) (ReviewerAutoscaleDecision, time.Time) {
	var d ReviewerAutoscaleDecision
	at := start
	for i := 0; i < cycles; i++ {
		sig.Now = at
		d = s.Decide(cfg, sig)
		at = at.Add(time.Minute)
	}
	return d, at
}

func TestReviewerAutoscaleStartsAtMin(t *testing.T) {
	var s ReviewerAutoscaler
	if got := s.Active(autoscaleCfg()); got != 2 {
		t.Fatalf("Active = %d, want min 2", got)
	}
}

func TestReviewerAutoscaleDecisionTable(t *testing.T) {
	cases := []struct {
		name       string
		cycles     int
		sig        ReviewerAutoscaleSignals
		wantAction string
		wantTo     int
		wantReason string
	}{
		{name: "one cycle of backlog is not sustained", cycles: 1, sig: ReviewerAutoscaleSignals{Waiting: waitingKeys(7)}, wantAction: AutoscaleHold, wantTo: 2},
		{name: "backlog above target for two cycles scales up", cycles: 2, sig: ReviewerAutoscaleSignals{Waiting: waitingKeys(7)}, wantAction: AutoscaleUp, wantTo: 3, wantReason: "backlog=7 oldest=1m0s"},
		{name: "backlog at target holds", cycles: 3, sig: ReviewerAutoscaleSignals{Waiting: waitingKeys(4)}, wantAction: AutoscaleHold, wantTo: 2},
		{name: "saturation holds scale-up", cycles: 2, sig: ReviewerAutoscaleSignals{Waiting: waitingKeys(7), Saturated: true}, wantAction: AutoscaleHold, wantTo: 2, wantReason: AutoscaleHeldSaturated + " backlog=7 oldest=1m0s"},
		{name: "provider budget latch holds scale-up", cycles: 2, sig: ReviewerAutoscaleSignals{Waiting: waitingKeys(7), BudgetLatched: true}, wantAction: AutoscaleHold, wantTo: 2, wantReason: AutoscaleHeldBudget + " backlog=7 oldest=1m0s"},
		{name: "no cpu headroom holds scale-up", cycles: 2, sig: ReviewerAutoscaleSignals{Waiting: waitingKeys(7), Headroom: func() (float64, float64) { return 0.2, 8000 }}, wantAction: AutoscaleHold, wantTo: 2, wantReason: AutoscaleHeldHeadroom + " backlog=7 oldest=1m0s"},
		{name: "no memory headroom holds scale-up", cycles: 2, sig: ReviewerAutoscaleSignals{Waiting: waitingKeys(7), Headroom: func() (float64, float64) { return 4, 512 }}, wantAction: AutoscaleHold, wantTo: 2, wantReason: AutoscaleHeldHeadroom + " backlog=7 oldest=1m0s"},
		{name: "unknown headroom never blocks", cycles: 2, sig: ReviewerAutoscaleSignals{Waiting: waitingKeys(7), Headroom: func() (float64, float64) { return -1, -1 }}, wantAction: AutoscaleUp, wantTo: 3},
		{name: "idle below one per reviewer for five cycles stays at min", cycles: 5, sig: ReviewerAutoscaleSignals{Waiting: waitingKeys(1)}, wantAction: AutoscaleHold, wantTo: 2, wantReason: AutoscaleHeldAtMin + " backlog=1 oldest=4m0s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s ReviewerAutoscaler
			d, _ := runCycles(&s, autoscaleCfg(), autoscaleT0, tc.cycles, tc.sig)
			if d.Action != tc.wantAction || d.To != tc.wantTo {
				t.Fatalf("decision = %+v, want action %s to %d", d, tc.wantAction, tc.wantTo)
			}
			if tc.wantReason != "" && d.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", d.Reason, tc.wantReason)
			}
			if got := s.Active(autoscaleCfg()); got != tc.wantTo {
				t.Fatalf("Active = %d, want %d", got, tc.wantTo)
			}
		})
	}
}

// A small backlog that has waited past max_wait is scale-up pressure on its
// own: the oldest PR is the stage-dwell signal from the issue.
func TestReviewerAutoscaleOldestWaitScalesUp(t *testing.T) {
	cfg := autoscaleCfg()
	cfg.MaxWait = "30m"
	var s ReviewerAutoscaler
	waiting := waitingKeys(2)
	s.Decide(cfg, ReviewerAutoscaleSignals{Now: autoscaleT0, Waiting: waiting})
	d := s.Decide(cfg, ReviewerAutoscaleSignals{Now: autoscaleT0.Add(29 * time.Minute), Waiting: waiting})
	if d.Changed() {
		t.Fatalf("scaled before max_wait: %+v", d)
	}
	d = s.Decide(cfg, ReviewerAutoscaleSignals{Now: autoscaleT0.Add(31 * time.Minute), Waiting: waiting})
	if d.Changed() {
		t.Fatalf("first over-age cycle must not act alone (scale_up_cycles=2): %+v", d)
	}
	d = s.Decide(cfg, ReviewerAutoscaleSignals{Now: autoscaleT0.Add(32 * time.Minute), Waiting: waiting})
	if d.Action != AutoscaleUp || d.To != 3 || d.OldestWait != 32*time.Minute {
		t.Fatalf("decision = %+v, want up to 3 with oldest 32m", d)
	}
	if d.String() != "2→3 reason=backlog=2 oldest=32m0s" {
		t.Fatalf("log line = %q", d.String())
	}
}

// A head that stops waiting is forgotten, so its age does not linger.
func TestReviewerAutoscaleForgetsServedPRs(t *testing.T) {
	var s ReviewerAutoscaler
	cfg := autoscaleCfg()
	s.Decide(cfg, ReviewerAutoscaleSignals{Now: autoscaleT0, Waiting: waitingKeys(1)})
	s.Decide(cfg, ReviewerAutoscaleSignals{Now: autoscaleT0.Add(10 * time.Minute)})
	d := s.Decide(cfg, ReviewerAutoscaleSignals{Now: autoscaleT0.Add(20 * time.Minute), Waiting: waitingKeys(1)})
	if d.OldestWait != 0 {
		t.Fatalf("oldest wait = %s, want 0 for a head seen waiting again", d.OldestWait)
	}
}

func TestReviewerAutoscaleCooldownAndMax(t *testing.T) {
	cfg := autoscaleCfg()
	var s ReviewerAutoscaler
	busy := ReviewerAutoscaleSignals{Waiting: waitingKeys(20)}
	d, at := runCycles(&s, cfg, autoscaleT0, 2, busy)
	if d.To != 3 {
		t.Fatalf("first step = %+v, want 3", d)
	}
	// Pressure keeps holding, but the next step waits for the 5m cooldown.
	d, at = runCycles(&s, cfg, at, 3, busy)
	if d.Changed() || d.Reason != AutoscaleHeldCooldown+" backlog=20 oldest=4m0s" {
		t.Fatalf("inside cooldown = %+v, want held cooldown", d)
	}
	d, at = runCycles(&s, cfg, at, 2, busy)
	if d.Action != AutoscaleUp || d.To != 4 {
		t.Fatalf("after cooldown = %+v, want up to 4", d)
	}
	d, _ = runCycles(&s, cfg, at.Add(10*time.Minute), 2, busy)
	if d.Changed() || d.To != 4 || d.Reason != AutoscaleHeldAtMax+" backlog=20 oldest=18m0s" {
		t.Fatalf("at max = %+v, want held at_max 4", d)
	}
}

func TestReviewerAutoscaleScalesDownAfterSustainedIdle(t *testing.T) {
	cfg := autoscaleCfg()
	var s ReviewerAutoscaler
	_, at := runCycles(&s, cfg, autoscaleT0, 2, ReviewerAutoscaleSignals{Waiting: waitingKeys(20)})
	if s.Active(cfg) != 3 {
		t.Fatal("setup: expected 3 active")
	}
	at = at.Add(10 * time.Minute) // past the cooldown
	// Four idle cycles are not enough (scale_down_cycles=5).
	d, at := runCycles(&s, cfg, at, 4, ReviewerAutoscaleSignals{Waiting: waitingKeys(2)})
	if d.Changed() {
		t.Fatalf("scaled down after 4 cycles: %+v", d)
	}
	// Backlog of 3 at 3 reviewers is not below one each: it resets the streak.
	runCycles(&s, cfg, at, 1, ReviewerAutoscaleSignals{Waiting: waitingKeys(3)})
	d, at = runCycles(&s, cfg, at.Add(time.Minute), 4, ReviewerAutoscaleSignals{})
	if d.Changed() {
		t.Fatalf("streak was not reset by a busy cycle: %+v", d)
	}
	d, _ = runCycles(&s, cfg, at, 1, ReviewerAutoscaleSignals{})
	if d.Action != AutoscaleDown || d.From != 3 || d.To != 2 {
		t.Fatalf("decision = %+v, want down 3→2", d)
	}
	if st := s.Status(); st.Active != 2 || st.LastChange.Action != AutoscaleDown || st.Min != 2 || st.Max != 4 {
		t.Fatalf("status = %+v", st)
	}
}

// A kick that failed with "did not reach input prompt" holds scale-up for one
// cooldown, then stops counting.
func TestReviewerAutoscaleNoteSaturationExpires(t *testing.T) {
	cfg := autoscaleCfg()
	var s ReviewerAutoscaler
	s.NoteSaturation(autoscaleT0)
	busy := ReviewerAutoscaleSignals{Waiting: waitingKeys(20)}
	d, _ := runCycles(&s, cfg, autoscaleT0.Add(time.Minute), 2, busy)
	if d.Changed() || d.Reason[:len(AutoscaleHeldSaturated)] != AutoscaleHeldSaturated {
		t.Fatalf("within cooldown of saturation = %+v, want held saturated", d)
	}
	d, _ = runCycles(&s, cfg, autoscaleT0.Add(6*time.Minute), 1, busy)
	if d.Action != AutoscaleUp {
		t.Fatalf("after saturation cooldown = %+v, want up", d)
	}
}

// Lowering max in config clamps the active count on the next read.
func TestReviewerAutoscaleClampsToConfigChange(t *testing.T) {
	cfg := autoscaleCfg()
	var s ReviewerAutoscaler
	runCycles(&s, cfg, autoscaleT0, 2, ReviewerAutoscaleSignals{Waiting: waitingKeys(20)})
	cfg.Max = 2
	if got := s.Active(cfg); got != 2 {
		t.Fatalf("Active after max lowered = %d, want 2", got)
	}
}

func TestIsPromptNotReadyKickError(t *testing.T) {
	if !IsPromptNotReadyKickError(errors.New("agent reviewer-2 CLI did not reach input prompt: busy")) {
		t.Fatal("prompt-not-ready error not recognized")
	}
	if IsPromptNotReadyKickError(errors.New("tmux: no server running")) || IsPromptNotReadyKickError(nil) {
		t.Fatal("unrelated error recognized as saturation")
	}
}

func autoscaleFleet() []AgentCapability {
	r2 := replica("reviewer-2", "reviewer")
	r2.ReplicaIndex = 2
	r3 := replica("reviewer-3", "reviewer")
	r3.ReplicaIndex = 3
	base := reviewer("reviewer")
	base.ReplicaIndex = 1
	return []AgentCapability{base, r2, r3}
}

func autoscaleDispatchOpts(limits map[string]int) DispatchOptions {
	return DispatchOptions{
		RequireApproval: true, FanOut: true, CombinedPerspectives: true,
		MaxParallelReviews: 10, ProjectOrg: "acme",
		Agents: autoscaleFleet(), ReplicaLimits: limits,
		Now: autoscaleT0,
	}
}

// Replicas above the autoscaler's active count take no new review kick, and
// the PR left without a reviewer is reported as the backlog.
func TestPlanDispatchHonorsReplicaLimits(t *testing.T) {
	prs := []PullRequest{dispatchPRNum(1, "sha1"), dispatchPRNum(2, "sha2"), dispatchPRNum(3, "sha3")}
	plan := PlanDispatch(prs, Artifact{}, DispatchState{}, autoscaleDispatchOpts(map[string]int{"reviewer": 2}))
	agents := map[string]bool{}
	for _, k := range plan.ReviewKicks {
		agents[k.Agent] = true
	}
	if len(plan.ReviewKicks) != 2 || !agents["reviewer"] || !agents["reviewer-2"] || agents["reviewer-3"] {
		t.Fatalf("kicks = %+v, want reviewer and reviewer-2 only", plan.ReviewKicks)
	}
	want := ReviewerStarvedKey("acme/hive", 3, "sha3")
	if len(plan.ReviewerStarved) != 1 || plan.ReviewerStarved[0] != want {
		t.Fatalf("ReviewerStarved = %v, want [%s]", plan.ReviewerStarved, want)
	}

	plan = PlanDispatch(prs, Artifact{}, DispatchState{}, autoscaleDispatchOpts(nil))
	if len(plan.ReviewKicks) != 3 || len(plan.ReviewerStarved) != 0 {
		t.Fatalf("unlimited plan = %d kicks, starved %v; want 3 kicks, none starved", len(plan.ReviewKicks), plan.ReviewerStarved)
	}
}

// A scaled-in replica keeps the turn it is already running: its pending entry
// is neither pruned nor redispatched, so the verdict still lands.
func TestPlanDispatchDrainsScaledInReplica(t *testing.T) {
	pending := []PendingReview{{Repo: "acme/hive", Number: 1, HeadSHA: "sha1", Perspective: PerspectiveCorrectness, Agent: "reviewer-3", Dispatched: autoscaleT0.Add(-5 * time.Minute)}}
	opts := autoscaleDispatchOpts(map[string]int{"reviewer": 2})
	plan := PlanDispatch([]PullRequest{dispatchPRNum(1, "sha1")}, Artifact{}, DispatchState{Pending: pending}, opts)
	kept := false
	for _, p := range plan.State.Pending {
		if p.Agent == "reviewer-3" && p.Number == 1 {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("draining replica's in-flight entry was dropped: %+v", plan.State.Pending)
	}
	for _, k := range plan.ReviewKicks {
		if k.Agent == "reviewer-3" {
			t.Fatalf("scaled-in replica got a new kick: %+v", k)
		}
	}
}

// Review heads carry ReplicaOf too, but are their own lanes: the limit must
// not hide them.
func TestOverReplicaLimitIgnoresReviewHeads(t *testing.T) {
	head := headReviewer("reviewer-sonnet-3", "reviewer-sonnet", "claude", "m")
	head.ReplicaOf = "reviewer"
	head.ReplicaIndex = 3
	if overReplicaLimit(head, map[string]int{"reviewer": 1}) {
		t.Fatal("review head was limited")
	}
	plain := replica("reviewer-3", "reviewer")
	plain.ReplicaIndex = 3
	if !overReplicaLimit(plain, map[string]int{"reviewer": 2}) {
		t.Fatal("plain replica 3 not limited at 2")
	}
	if overReplicaLimit(plain, map[string]int{"other": 1}) {
		t.Fatal("replica of an unscaled base was limited")
	}
}
