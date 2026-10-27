package config

import (
	"strings"
	"testing"
	"time"
)

// review.reviewers.autoscale (hivecommons/hive#14685).

func autoscaleTestConfig(as ReviewerAutoscaleConfig) *Config {
	return &Config{
		AgentRuntimeConfig: AgentRuntimeConfig{Agents: map[string]AgentConfig{
			"reviewer": {ID: "reviewer", Backend: "claude", Enabled: true, Replicas: 2},
			"scanner":  {ID: "scanner", Backend: "claude", Enabled: true, Replicas: 1},
		}},
		Review: ReviewConfig{Reviewers: ReviewReviewersConfig{Autoscale: as}},
	}
}

// Enabling autoscale materializes the scaled agent's replicas up to max
// through the ordinary replica machinery, and leaves other agents alone.
func TestExpandAgentReplicasMaterializesAutoscaleMax(t *testing.T) {
	cfg := autoscaleTestConfig(ReviewerAutoscaleConfig{Enabled: true, Min: 2, Max: 4})
	if err := cfg.ExpandAgentReplicas(); err != nil {
		t.Fatalf("ExpandAgentReplicas() error = %v", err)
	}
	for i, name := range []string{"reviewer", "reviewer-2", "reviewer-3", "reviewer-4"} {
		got, ok := cfg.Agents[name]
		if !ok {
			t.Fatalf("missing replica %s", name)
		}
		if got.ReplicaIndex != i+1 || got.ReplicaCount != 4 {
			t.Fatalf("%s metadata = index %d count %d, want %d/4", name, got.ReplicaIndex, got.ReplicaCount, i+1)
		}
	}
	if _, ok := cfg.Agents["reviewer-5"]; ok {
		t.Fatal("expanded past max")
	}
	if _, ok := cfg.Agents["scanner-2"]; ok {
		t.Fatal("unscaled agent was expanded")
	}
	// The declared replicas stay what the operator wrote, so a save does not
	// persist the autoscaler's max as a static count.
	if got := cfg.Agents["reviewer"].Replicas; got != 2 {
		t.Fatalf("declared replicas = %d, want 2 kept", got)
	}
}

func TestExpandAgentReplicasAutoscaleOffIsUnchanged(t *testing.T) {
	cfg := autoscaleTestConfig(ReviewerAutoscaleConfig{Enabled: false, Max: 4})
	if err := cfg.ExpandAgentReplicas(); err != nil {
		t.Fatalf("ExpandAgentReplicas() error = %v", err)
	}
	if _, ok := cfg.Agents["reviewer-3"]; ok {
		t.Fatal("disabled autoscale expanded replicas")
	}
	if got := cfg.Agents["reviewer-2"].ReplicaCount; got != 2 {
		t.Fatalf("ReplicaCount = %d, want 2", got)
	}
}

// A max below the declared replicas never removes a declared replica.
func TestExpandAgentReplicasAutoscaleNeverShrinksDeclared(t *testing.T) {
	cfg := autoscaleTestConfig(ReviewerAutoscaleConfig{Enabled: true, Max: 1})
	if err := cfg.ExpandAgentReplicas(); err != nil {
		t.Fatalf("ExpandAgentReplicas() error = %v", err)
	}
	if _, ok := cfg.Agents["reviewer-2"]; !ok {
		t.Fatal("declared replica reviewer-2 was dropped")
	}
}

func TestReviewerAutoscaleDefaults(t *testing.T) {
	var a ReviewerAutoscaleConfig
	if a.EffectiveAgent() != "reviewer" || a.EffectiveMin() != 1 || a.EffectiveMax() != MaxAgentReplicas {
		t.Fatalf("agent/min/max defaults = %s/%d/%d", a.EffectiveAgent(), a.EffectiveMin(), a.EffectiveMax())
	}
	if a.EffectiveTargetBacklogPerReviewer() != 2 || a.EffectiveScaleUpCycles() != 2 || a.EffectiveScaleDownCycles() != 5 {
		t.Fatal("cycle/target defaults wrong")
	}
	if a.EffectiveCooldown() != 5*time.Minute || a.EffectiveMaxWait() != 30*time.Minute {
		t.Fatalf("cooldown/max_wait = %s/%s", a.EffectiveCooldown(), a.EffectiveMaxWait())
	}
	if a.EffectiveCPUPerReviewer() != 0.5 || a.EffectiveMemPerReviewerMB() != 1024 {
		t.Fatal("cost defaults wrong")
	}
	a = ReviewerAutoscaleConfig{Min: 4, Max: 3, Cooldown: "90s", Agent: " cr "}
	if a.EffectiveMin() != 3 || a.EffectiveCooldown() != 90*time.Second || a.EffectiveAgent() != "cr" {
		t.Fatalf("min clamp/cooldown/agent = %d/%s/%q", a.EffectiveMin(), a.EffectiveCooldown(), a.EffectiveAgent())
	}
}

func TestReviewerAutoscaleValidate(t *testing.T) {
	ok := []ReviewerAutoscaleConfig{
		{},
		{Enabled: true, Min: 2, Max: 4, Cooldown: "5m", MaxWait: "45m", CPUPerReviewer: 1, MemPerReviewerMB: 2048},
	}
	for _, a := range ok {
		if err := a.Validate(); err != nil {
			t.Fatalf("Validate(%+v) = %v", a, err)
		}
	}
	bad := map[string]ReviewerAutoscaleConfig{
		"max":      {Max: MaxAgentReplicas + 1},
		"min (4)":  {Min: 4, Max: 2},
		"negative": {Min: -1},
		"cooldown": {Cooldown: "soon"},
		"max_wait": {MaxWait: "-1m"},
		"cpu_per":  {CPUPerReviewer: -1},
		"scale_up": {ScaleUpCycles: -1},
	}
	for want, a := range bad {
		err := a.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate(%+v) = %v, want error mentioning %q", a, err, want)
		}
	}
}
