package review

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hivecommons/hive/pkg/config"
)

// Reviewer autoscaler actions (hivecommons/hive#14685).
const (
	AutoscaleHold = "hold"
	AutoscaleUp   = "up"
	AutoscaleDown = "down"
)

// Reasons a step the pressure called for was not taken. Each is the reason
// field of the decision's log line.
const (
	AutoscaleHeldAtMax     = "at_max"
	AutoscaleHeldAtMin     = "at_min"
	AutoscaleHeldCooldown  = "cooldown"
	AutoscaleHeldSaturated = "saturated"
	AutoscaleHeldBudget    = "provider_budget"
	AutoscaleHeldHeadroom  = "headroom"
)

// ReviewerAutoscaleSignals is what one planner cycle tells the autoscaler.
type ReviewerAutoscaleSignals struct {
	Now time.Time
	// Waiting keys the PR heads that needed a review this cycle and found no
	// free reviewer (DispatchPlan.ReviewerStarved).
	Waiting []string
	// Saturated reports a recent reviewer kick that failed because the CLI
	// did not reach its input prompt: the pod is out of capacity, and adding
	// sessions would make that worse, not better.
	Saturated bool
	// BudgetLatched reports the provider spend latch: a new reviewer could
	// not buy a token either.
	BudgetLatched bool
	// Headroom measures the pod's free CPU cores and memory (MB). It is only
	// called when a step up is otherwise due, because sampling CPU takes a
	// moment. Nil, or a negative value, means unknown, which never blocks.
	Headroom func() (cpuFreeCores, memFreeMB float64)
}

// ReviewerAutoscaleDecision is one cycle's outcome.
type ReviewerAutoscaleDecision struct {
	At         time.Time     `json:"at"`
	Action     string        `json:"action"`
	From       int           `json:"from"`
	To         int           `json:"to"`
	Reason     string        `json:"reason,omitempty"`
	Backlog    int           `json:"backlog"`
	OldestWait time.Duration `json:"oldest_wait"`
}

// Changed reports whether the decision moved the active count.
func (d ReviewerAutoscaleDecision) Changed() bool { return d.From != d.To }

// String is the decision's log line body, e.g. "2→3 reason=backlog=7 oldest=41m".
func (d ReviewerAutoscaleDecision) String() string {
	return fmt.Sprintf("%d→%d reason=%s", d.From, d.To, d.Reason)
}

// ReviewerAutoscaleStatus is the dashboard view of the autoscaler.
type ReviewerAutoscaleStatus struct {
	Agent        string                    `json:"agent"`
	Active       int                       `json:"active"`
	Min          int                       `json:"min"`
	Max          int                       `json:"max"`
	LastDecision ReviewerAutoscaleDecision `json:"last_decision"`
	LastChange   ReviewerAutoscaleDecision `json:"last_change"`
}

// ReviewerAutoscaler holds the reviewer lane's active replica count across
// planner cycles. The zero value is ready; it starts at the configured min.
// State is in memory: a restart starts again from min and scales back up
// within scale_up_cycles if the backlog is still there.
type ReviewerAutoscaler struct {
	mu           sync.Mutex
	active       int
	upStreak     int
	downStreak   int
	lastAction   time.Time
	waitingSince map[string]time.Time
	saturatedAt  time.Time
	last         ReviewerAutoscaleDecision
	lastChange   ReviewerAutoscaleDecision
	min, max     int
	agent        string
}

// Active is the number of the scaled agent's replicas (reviewer,
// reviewer-2, …) the planner may hand review kicks to.
func (s *ReviewerAutoscaler) Active(cfg config.ReviewerAutoscaleConfig) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clampLocked(cfg)
}

func (s *ReviewerAutoscaler) clampLocked(cfg config.ReviewerAutoscaleConfig) int {
	s.min, s.max, s.agent = cfg.EffectiveMin(), cfg.EffectiveMax(), cfg.EffectiveAgent()
	if s.active < s.min {
		s.active = s.min
	}
	if s.active > s.max {
		s.active = s.max
	}
	return s.active
}

// NoteSaturation records a reviewer kick that failed because its CLI did not
// reach the input prompt. Scale-up is held for one cooldown after it.
func (s *ReviewerAutoscaler) NoteSaturation(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.After(s.saturatedAt) {
		s.saturatedAt = at
	}
}

// Status snapshots the autoscaler for the dashboard.
func (s *ReviewerAutoscaler) Status() ReviewerAutoscaleStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ReviewerAutoscaleStatus{Agent: s.agent, Active: s.active, Min: s.min, Max: s.max, LastDecision: s.last, LastChange: s.lastChange}
}

// Decide runs one cycle of the autoscaler.
//
// Scale-up pressure is a backlog above target_backlog_per_reviewer per active
// reviewer, or a PR that has waited longer than max_wait; it must hold for
// scale_up_cycles consecutive cycles. Scale-down pressure is fewer waiting PRs
// than active reviewers (below one each), held for scale_down_cycles. The gap
// between the two thresholds and the streaks are the hysteresis; cooldown
// spaces any two steps.
//
// A step down removes the highest-numbered replica from new dispatch. It is
// never interrupted: a turn already in flight finishes and its verdict is
// recorded as usual, so the replica drains rather than dies.
func (s *ReviewerAutoscaler) Decide(cfg config.ReviewerAutoscaleConfig, sig ReviewerAutoscaleSignals) ReviewerAutoscaleDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.clampLocked(cfg)
	now := sig.Now
	oldest := s.observeWaitingLocked(sig.Waiting, now)
	backlog := len(sig.Waiting)
	d := ReviewerAutoscaleDecision{At: now, Action: AutoscaleHold, From: active, To: active, Backlog: backlog, OldestWait: oldest}
	pressure := fmt.Sprintf("backlog=%d oldest=%s", backlog, formatWait(oldest))

	up := backlog > active*cfg.EffectiveTargetBacklogPerReviewer() ||
		(backlog > 0 && oldest >= cfg.EffectiveMaxWait())
	down := !up && backlog < active
	switch {
	case up:
		s.upStreak++
		s.downStreak = 0
	case down:
		s.downStreak++
		s.upStreak = 0
	default:
		s.upStreak, s.downStreak = 0, 0
	}
	coolingDown := !s.lastAction.IsZero() && now.Sub(s.lastAction) < cfg.EffectiveCooldown()
	saturated := sig.Saturated || (!s.saturatedAt.IsZero() && now.Sub(s.saturatedAt) < cfg.EffectiveCooldown())

	switch {
	case up && s.upStreak >= cfg.EffectiveScaleUpCycles():
		held := ""
		switch {
		case active >= s.max:
			held = AutoscaleHeldAtMax
		case coolingDown:
			held = AutoscaleHeldCooldown
		case saturated:
			held = AutoscaleHeldSaturated
		case sig.BudgetLatched:
			held = AutoscaleHeldBudget
		case !headroomFor(cfg, sig):
			held = AutoscaleHeldHeadroom
		}
		if held != "" {
			d.Reason = held + " " + pressure
			break
		}
		d.Action, d.To, d.Reason = AutoscaleUp, active+1, pressure
	case down && s.downStreak >= cfg.EffectiveScaleDownCycles():
		held := ""
		switch {
		case active <= s.min:
			held = AutoscaleHeldAtMin
		case coolingDown:
			held = AutoscaleHeldCooldown
		}
		if held != "" {
			d.Reason = held + " " + pressure
			break
		}
		d.Action, d.To, d.Reason = AutoscaleDown, active-1, pressure
	default:
		d.Reason = pressure
	}
	if d.Changed() {
		s.active = d.To
		s.lastAction = now
		s.upStreak, s.downStreak = 0, 0
		s.lastChange = d
	}
	s.last = d
	return d
}

// observeWaitingLocked keeps the first time each waiting PR head was seen
// waiting, forgets heads no longer waiting, and returns the oldest wait.
func (s *ReviewerAutoscaler) observeWaitingLocked(keys []string, now time.Time) time.Duration {
	seen := make(map[string]time.Time, len(keys))
	var oldest time.Duration
	for _, k := range keys {
		since, ok := s.waitingSince[k]
		if !ok {
			since = now
		}
		seen[k] = since
		if w := now.Sub(since); w > oldest {
			oldest = w
		}
	}
	s.waitingSince = seen
	return oldest
}

// headroomFor reports whether the pod can afford one more reviewer session.
func headroomFor(cfg config.ReviewerAutoscaleConfig, sig ReviewerAutoscaleSignals) bool {
	if sig.Headroom == nil {
		return true
	}
	cpuFree, memFree := sig.Headroom()
	if cpuFree >= 0 && cpuFree < cfg.EffectiveCPUPerReviewer() {
		return false
	}
	if memFree >= 0 && memFree < float64(cfg.EffectiveMemPerReviewerMB()) {
		return false
	}
	return true
}

func formatWait(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.Round(time.Minute).String()
}

// ReviewerStarvedKey keys a PR head in DispatchPlan.ReviewerStarved.
func ReviewerStarvedKey(repo string, number int, headSHA string) string {
	return fmt.Sprintf("%s#%d@%s", strings.ToLower(strings.TrimSpace(repo)), number, strings.TrimSpace(headSHA))
}

// IsPromptNotReadyKickError reports a kick that failed because the agent's
// CLI never reached its input prompt (crux/agent kickPromptNotReadyErr), the
// autoscaler's saturation signal.
func IsPromptNotReadyKickError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "did not reach input prompt")
}
