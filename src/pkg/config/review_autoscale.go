package config

import (
	"fmt"
	"strings"
	"time"
)

// Reviewer autoscaler defaults (hivecommons/hive#14685). A zero field in
// review.reviewers.autoscale means the matching default.
const (
	DefaultReviewerAutoscaleAgent           = "reviewer"
	DefaultReviewerAutoscaleMin             = 1
	DefaultReviewerAutoscaleMax             = MaxAgentReplicas
	DefaultReviewerAutoscaleTargetBacklog   = 2
	DefaultReviewerAutoscaleScaleUpCycles   = 2
	DefaultReviewerAutoscaleScaleDownCycles = 5
	DefaultReviewerAutoscaleCooldown        = 5 * time.Minute
	DefaultReviewerAutoscaleMaxWait         = 30 * time.Minute
	DefaultReviewerAutoscaleCPUPerReviewer  = 0.5
	DefaultReviewerAutoscaleMemPerReviewer  = 1024
)

// ReviewReviewersConfig is review.reviewers.
type ReviewReviewersConfig struct {
	Autoscale ReviewerAutoscaleConfig `yaml:"autoscale,omitempty" json:"autoscale,omitempty"`
}

// ReviewerAutoscaleConfig is review.reviewers.autoscale: how many of one
// reviewer agent's replicas the review swarm hands work to, between Min and
// Max, driven by the backlog of PRs waiting for a reviewer.
//
// Enabling it materializes the agent's replicas up to Max through the same
// replica machinery as agents.<name>.replicas (reviewer-2…reviewer-N), so the
// planner, dashboard, kick governor and token cache see ordinary replicas. The
// autoscaler only decides how many of them receive review kicks.
type ReviewerAutoscaleConfig struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Agent is the base reviewer agent this scales. Empty means "reviewer".
	Agent string `yaml:"agent,omitempty" json:"agent,omitempty"`
	Min   int    `yaml:"min,omitempty" json:"min,omitempty"`
	Max   int    `yaml:"max,omitempty" json:"max,omitempty"`
	// TargetBacklogPerReviewer is the waiting-PR count per active reviewer
	// above which the lane is under-provisioned.
	TargetBacklogPerReviewer int `yaml:"target_backlog_per_reviewer,omitempty" json:"target_backlog_per_reviewer,omitempty"`
	// ScaleUpCycles and ScaleDownCycles are how many consecutive planner
	// cycles the pressure must hold before a step up or down.
	ScaleUpCycles   int `yaml:"scale_up_cycles,omitempty" json:"scale_up_cycles,omitempty"`
	ScaleDownCycles int `yaml:"scale_down_cycles,omitempty" json:"scale_down_cycles,omitempty"`
	// Cooldown is the minimum time between two scaling actions (Go duration).
	Cooldown string `yaml:"cooldown,omitempty" json:"cooldown,omitempty"`
	// MaxWait is how long the oldest waiting PR may wait for a reviewer
	// before that alone counts as scale-up pressure (Go duration).
	MaxWait string `yaml:"max_wait,omitempty" json:"max_wait,omitempty"`
	// CPUPerReviewer (cores) and MemPerReviewerMB are the per-reviewer cost
	// estimate the headroom guard checks before a step up.
	CPUPerReviewer   float64 `yaml:"cpu_per_reviewer,omitempty" json:"cpu_per_reviewer,omitempty"`
	MemPerReviewerMB int     `yaml:"mem_per_reviewer_mb,omitempty" json:"mem_per_reviewer_mb,omitempty"`
}

// EffectiveAgent is the scaled base agent name.
func (a ReviewerAutoscaleConfig) EffectiveAgent() string {
	if n := strings.TrimSpace(a.Agent); n != "" {
		return n
	}
	return DefaultReviewerAutoscaleAgent
}

// EffectiveMax is Max, defaulted and clamped to MaxAgentReplicas.
func (a ReviewerAutoscaleConfig) EffectiveMax() int {
	m := a.Max
	if m <= 0 {
		m = DefaultReviewerAutoscaleMax
	}
	if m > MaxAgentReplicas {
		m = MaxAgentReplicas
	}
	return m
}

// EffectiveMin is Min, defaulted and clamped to [1, EffectiveMax].
func (a ReviewerAutoscaleConfig) EffectiveMin() int {
	m := a.Min
	if m <= 0 {
		m = DefaultReviewerAutoscaleMin
	}
	if mx := a.EffectiveMax(); m > mx {
		m = mx
	}
	return m
}

func (a ReviewerAutoscaleConfig) EffectiveTargetBacklogPerReviewer() int {
	if a.TargetBacklogPerReviewer > 0 {
		return a.TargetBacklogPerReviewer
	}
	return DefaultReviewerAutoscaleTargetBacklog
}

func (a ReviewerAutoscaleConfig) EffectiveScaleUpCycles() int {
	if a.ScaleUpCycles > 0 {
		return a.ScaleUpCycles
	}
	return DefaultReviewerAutoscaleScaleUpCycles
}

func (a ReviewerAutoscaleConfig) EffectiveScaleDownCycles() int {
	if a.ScaleDownCycles > 0 {
		return a.ScaleDownCycles
	}
	return DefaultReviewerAutoscaleScaleDownCycles
}

func (a ReviewerAutoscaleConfig) EffectiveCooldown() time.Duration {
	return parseAutoscaleDuration(a.Cooldown, DefaultReviewerAutoscaleCooldown)
}

func (a ReviewerAutoscaleConfig) EffectiveMaxWait() time.Duration {
	return parseAutoscaleDuration(a.MaxWait, DefaultReviewerAutoscaleMaxWait)
}

func (a ReviewerAutoscaleConfig) EffectiveCPUPerReviewer() float64 {
	if a.CPUPerReviewer > 0 {
		return a.CPUPerReviewer
	}
	return DefaultReviewerAutoscaleCPUPerReviewer
}

func (a ReviewerAutoscaleConfig) EffectiveMemPerReviewerMB() int {
	if a.MemPerReviewerMB > 0 {
		return a.MemPerReviewerMB
	}
	return DefaultReviewerAutoscaleMemPerReviewer
}

func parseAutoscaleDuration(raw string, def time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return def
	}
	return d
}

// expandedReplicas is how many replicas ExpandAgentReplicas materializes for
// agent name: the declared count, raised to the autoscaler's max for the
// scaled agent.
func (a ReviewerAutoscaleConfig) expandedReplicas(name string, declared int) int {
	if !a.Enabled || name != a.EffectiveAgent() {
		return declared
	}
	if mx := a.EffectiveMax(); mx > declared {
		return mx
	}
	return declared
}

// Validate rejects a review.reviewers.autoscale block that cannot mean what
// it says.
func (a ReviewerAutoscaleConfig) Validate() error {
	if a.Min < 0 || a.Max < 0 {
		return fmt.Errorf("review.reviewers.autoscale: min and max must not be negative")
	}
	if a.Max > MaxAgentReplicas {
		return fmt.Errorf("review.reviewers.autoscale: max must be at most %d", MaxAgentReplicas)
	}
	if a.Min > 0 && a.Max > 0 && a.Min > a.Max {
		return fmt.Errorf("review.reviewers.autoscale: min (%d) must not exceed max (%d)", a.Min, a.Max)
	}
	if a.TargetBacklogPerReviewer < 0 || a.ScaleUpCycles < 0 || a.ScaleDownCycles < 0 {
		return fmt.Errorf("review.reviewers.autoscale: target_backlog_per_reviewer, scale_up_cycles and scale_down_cycles must not be negative")
	}
	if a.CPUPerReviewer < 0 || a.MemPerReviewerMB < 0 {
		return fmt.Errorf("review.reviewers.autoscale: cpu_per_reviewer and mem_per_reviewer_mb must not be negative")
	}
	for _, f := range []struct{ name, raw string }{{"cooldown", a.Cooldown}, {"max_wait", a.MaxWait}} {
		raw := strings.TrimSpace(f.raw)
		if raw == "" {
			continue
		}
		if d, err := time.ParseDuration(raw); err != nil || d < 0 {
			return fmt.Errorf("review.reviewers.autoscale.%s: %q is not a non-negative duration", f.name, raw)
		}
	}
	return nil
}
