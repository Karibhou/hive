package main

import (
	"log/slog"
	"strings"
	"time"

	"github.com/hivecommons/hive/pkg/config"
	"github.com/hivecommons/hive/pkg/dashboard"
	"github.com/hivecommons/hive/pkg/review"
)

// reviewerAutoscale is the reviewer lane's autoscaler (review.reviewers.
// autoscale, hivecommons/hive#14685): how many of the scaled agent's replicas
// the review swarm hands kicks to this cycle.
var reviewerAutoscale review.ReviewerAutoscaler

// reviewerReplicaLimits is DispatchOptions.ReplicaLimits for this cycle: the
// scaled agent capped at the autoscaler's active count, nil when autoscale is
// off so every declared replica takes work as before.
func reviewerReplicaLimits(cfg *config.Config, as *review.ReviewerAutoscaler) map[string]int {
	if cfg == nil || !cfg.Review.Reviewers.Autoscale.Enabled {
		return nil
	}
	ac := cfg.Review.Reviewers.Autoscale
	return map[string]int{ac.EffectiveAgent(): as.Active(ac)}
}

// decideReviewerAutoscale feeds one planned cycle to the autoscaler and logs
// the step when it moves the active count. The new count applies from the
// next cycle's plan.
func decideReviewerAutoscale(cfg *config.Config, as *review.ReviewerAutoscaler, plan review.DispatchPlan, sig review.ReviewerAutoscaleSignals, logger *slog.Logger) review.ReviewerAutoscaleDecision {
	if cfg == nil || !cfg.Review.Reviewers.Autoscale.Enabled {
		return review.ReviewerAutoscaleDecision{}
	}
	ac := cfg.Review.Reviewers.Autoscale
	sig.Waiting = plan.ReviewerStarved
	if sig.Now.IsZero() {
		sig.Now = time.Now().UTC()
	}
	d := as.Decide(ac, sig)
	if d.Changed() && logger != nil {
		logger.Info("reviewer autoscale: "+d.String(),
			"agent", ac.EffectiveAgent(), "action", d.Action, "from", d.From, "to", d.To,
			"backlog", d.Backlog, "oldest_wait", d.OldestWait.Round(time.Second).String(),
			"min", ac.EffectiveMin(), "max", ac.EffectiveMax())
	}
	return d
}

// liveReviewerAutoscaleSignals are the guards read from the running hive:
// the provider spend latch and the pod's cgroup headroom.
func liveReviewerAutoscaleSignals() review.ReviewerAutoscaleSignals {
	cause, _, _, _ := dashboard.InferenceBudgetExceeded()
	return review.ReviewerAutoscaleSignals{
		BudgetLatched: cause != "",
		Headroom:      dashboard.PodHeadroom,
	}
}

// noteReviewerAutoscaleKickFailure records a review kick to one of the scaled
// agent's replicas that failed because the CLI never reached its input
// prompt: saturation, which holds scale-up for a cooldown.
func noteReviewerAutoscaleKickFailure(cfg *config.Config, as *review.ReviewerAutoscaler, agent string, err error, at time.Time) {
	if cfg == nil || !cfg.Review.Reviewers.Autoscale.Enabled || !review.IsPromptNotReadyKickError(err) {
		return
	}
	base := strings.TrimSpace(agent)
	if ac, ok := cfg.Agents[base]; ok && ac.ReplicaOf != "" && !ac.IsReviewHead() {
		base = ac.ReplicaOf
	}
	if base != cfg.Review.Reviewers.Autoscale.EffectiveAgent() {
		return
	}
	as.NoteSaturation(at)
}

// withholdInactiveReviewerReplicas drops the cadence kicks of the scaled
// agent's replicas numbered above the autoscaler's active count, reporting
// each as skipped "(autoscale-inactive)". With autoscale off it returns its
// inputs unchanged.
func withholdInactiveReviewerReplicas(agentsDue, skipped []string, cfg *config.Config, as *review.ReviewerAutoscaler) ([]string, []string) {
	limits := reviewerReplicaLimits(cfg, as)
	if len(limits) == 0 {
		return agentsDue, skipped
	}
	kept := make([]string, 0, len(agentsDue))
	for _, key := range agentsDue {
		name, _ := config.SplitCadenceTargetKey(key)
		ac, ok := cfg.Agents[name]
		if ok && ac.ReplicaOf != "" && !ac.IsReviewHead() {
			if limit, scaled := limits[ac.ReplicaOf]; scaled && ac.ReplicaIndex > limit {
				skipped = append(skipped, key+" (autoscale-inactive)")
				continue
			}
		}
		kept = append(kept, key)
	}
	return kept, skipped
}
