package dashboard

import (
	"testing"

	"github.com/hivecommons/hive/pkg/config"
)

// TestApplyPackClearsPackModelOnGatewayAgent covers #9945: an agent moved onto
// a model gateway kept the pack's copilot model (`claude-sonnet-4-6`), which
// outranks the gateway's default_model and 403s as not entitled. The pack
// apply that runs on every restart re-seeded it even after the operator
// deleted the `model:` line. It must clear a pack-owned model instead.
func TestApplyPackClearsPackModelOnGatewayAgent(t *testing.T) {
	tests := []struct {
		name     string
		backend  string
		gateways []config.GatewayConfig
	}{
		{"litellm backend", "litellm", nil},
		{"named gateway backend", "cb", []config.GatewayConfig{{
			Name: "cb", Kind: config.GatewayKindLiteLLM, Endpoint: "https://litellm.example.test", DefaultModel: "rits/zai-org/glm-5-3",
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newFullServer(t)
			enableScanner(t, srv)
			srv.deps.Config.Governor.Gateways = tt.gateways
			sc := srv.deps.Config.Agents["scanner"]
			sc.Backend = tt.backend
			sc.BackendOwner = config.FieldOwnerOperator
			sc.Model = "claude-sonnet-4-6"
			sc.ModelOwner = config.FieldOwnerPack
			srv.deps.Config.Agents["scanner"] = sc

			// Twice: the second apply must not re-seed the pack model.
			for i := 0; i < 2; i++ {
				if _, err := srv.ApplyPack(3); err != nil {
					t.Fatalf("ApplyPack(3) #%d: %v", i, err)
				}
				got := srv.deps.Config.Agents["scanner"]
				if got.Backend != tt.backend {
					t.Fatalf("apply #%d: backend = %q, want %q", i, got.Backend, tt.backend)
				}
				if got.Model != "" {
					t.Errorf("apply #%d: model = %q, want \"\" so the gateway default_model applies", i, got.Model)
				}
			}
		})
	}
}

// TestApplyPackKeepsOperatorModelOnGatewayAgent: a model the operator picked
// for a gateway agent is theirs; the pack apply never clears it.
func TestApplyPackKeepsOperatorModelOnGatewayAgent(t *testing.T) {
	srv := newFullServer(t)
	enableScanner(t, srv)
	sc := srv.deps.Config.Agents["scanner"]
	sc.Backend = "litellm"
	sc.BackendOwner = config.FieldOwnerOperator
	sc.Model = "rits/zai-org/glm-5-3"
	sc.ModelOwner = config.FieldOwnerOperator
	srv.deps.Config.Agents["scanner"] = sc

	if _, err := srv.ApplyPack(3); err != nil {
		t.Fatalf("ApplyPack(3): %v", err)
	}
	if got := srv.deps.Config.Agents["scanner"].Model; got != "rits/zai-org/glm-5-3" {
		t.Errorf("operator gateway model = %q, want rits/zai-org/glm-5-3", got)
	}
}

// TestApplyPackSeedsModelOnPackBackend: an agent still on the pack's own
// backend keeps receiving the pack model.
func TestApplyPackSeedsModelOnPackBackend(t *testing.T) {
	srv := newFullServer(t)
	enableScanner(t, srv)
	sc := srv.deps.Config.Agents["scanner"]
	sc.Backend = "copilot"
	sc.BackendOwner = config.FieldOwnerPack
	sc.Model = ""
	sc.ModelOwner = config.FieldOwnerPack
	srv.deps.Config.Agents["scanner"] = sc
	if _, err := srv.ApplyPack(3); err != nil {
		t.Fatalf("ApplyPack(3): %v", err)
	}
	got := srv.deps.Config.Agents["scanner"]
	if got.Backend != "copilot" || got.Model != "claude-sonnet-4-6" {
		t.Errorf("scanner on pack backend: backend=%q model=%q, want copilot with the pack model claude-sonnet-4-6", got.Backend, got.Model)
	}
}

// TestApplyPackModelOnKeptOrReplacedBackend covers #13288: a hand-set CLI
// backend that is kept must not receive the pack's model id.
func TestApplyPackModelOnKeptOrReplacedBackend(t *testing.T) {
	tests := []struct {
		name      string
		backend   string
		model     string
		wantModel string
	}{
		{"kept codex keeps its model", "codex", "gpt-5-codex", "gpt-5-codex"},
		{"kept codex without model is not seeded", "codex", "", ""},
		{"pack backend is seeded", "copilot", "", "claude-sonnet-4-6"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newFullServer(t)
			enableScanner(t, srv)
			sc := srv.deps.Config.Agents["scanner"]
			sc.Backend = tt.backend
			sc.BackendOwner = config.FieldOwnerOperator
			sc.Model = tt.model
			sc.ModelOwner = config.FieldOwnerPack
			srv.deps.Config.Agents["scanner"] = sc
			if _, err := srv.ApplyPack(3); err != nil {
				t.Fatalf("ApplyPack(3): %v", err)
			}
			got := srv.deps.Config.Agents["scanner"]
			if got.Backend != tt.backend || got.Model != tt.wantModel {
				t.Errorf("backend=%q model=%q, want %q / %q", got.Backend, got.Model, tt.backend, tt.wantModel)
			}
		})
	}
}

// gatewayHive is the #14678 shape: an L2 hive whose operator moved the agents
// it already had onto one inference gateway before the pack grew `reviewer`.
func gatewayHive(t *testing.T, backends ...string) *Server {
	t.Helper()
	srv := newFullServer(t)
	for i, name := range []string{"scanner", "quality", "guide"} {
		srv.deps.Config.Agents[name] = config.AgentConfig{
			ID: name, Role: name, Enabled: true,
			Backend: backends[i%len(backends)], BackendOwner: config.FieldOwnerOperator,
		}
	}
	return srv
}

// TestApplyPackCreatesAgentOnHiveGateway covers #14678: the L2 `reviewer`,
// added to a hive whose agents all run on a LiteLLM gateway, was created on
// the pack's copilot backend with a copilot model id.
func TestApplyPackCreatesAgentOnHiveGateway(t *testing.T) {
	srv := gatewayHive(t, "litellm")
	if _, err := srv.ApplyPack(2); err != nil {
		t.Fatalf("ApplyPack(2): %v", err)
	}
	got, ok := srv.deps.Config.Agents["reviewer"]
	if !ok {
		t.Fatal("L2 pack did not create `reviewer`")
	}
	if got.Backend != "litellm" || got.Model != "" {
		t.Errorf("created reviewer backend=%q model=%q, want litellm with no model so the gateway default_model applies", got.Backend, got.Model)
	}
	if got.BackendIsOperatorOwned() {
		t.Error("created reviewer backend is operator-owned; the operator never picked it")
	}
}

// TestApplyPackMovesPackOwnedAgentOntoHiveGateway: a hive that already got
// the copilot reviewer before the fix is repaired on its next restart.
func TestApplyPackMovesPackOwnedAgentOntoHiveGateway(t *testing.T) {
	srv := gatewayHive(t, "litellm")
	srv.deps.Config.Agents["reviewer"] = config.AgentConfig{
		ID: "reviewer", Role: "reviewer", Enabled: true,
		Backend: "copilot", BackendOwner: config.FieldOwnerPack,
		Model: "claude-fable-5", ModelOwner: config.FieldOwnerPack,
	}
	for i := 0; i < 2; i++ {
		if _, err := srv.ApplyPack(2); err != nil {
			t.Fatalf("ApplyPack(2) #%d: %v", i, err)
		}
		got := srv.deps.Config.Agents["reviewer"]
		if got.Backend != "litellm" || got.Model != "" {
			t.Errorf("apply #%d: reviewer backend=%q model=%q, want litellm with no model", i, got.Backend, got.Model)
		}
	}
}

// TestApplyPackKeepsPackBackendWhenGatewayNotShared: the pack backend stays
// whenever "the hive's gateway" is not a single, unambiguous choice, and an
// operator's own copilot pick is never moved.
func TestApplyPackKeepsPackBackendWhenGatewayNotShared(t *testing.T) {
	tests := []struct {
		name     string
		backends []string
		reviewer *config.AgentConfig
	}{
		{"agents disagree on the gateway", []string{"litellm", "vllm"}, nil},
		{"no agent on a gateway", []string{"claude"}, nil},
		{"operator picked copilot", []string{"litellm"}, &config.AgentConfig{
			ID: "reviewer", Role: "reviewer", Enabled: true,
			Backend: "copilot", BackendOwner: config.FieldOwnerOperator,
		}},
		{"legacy unowned copilot", []string{"litellm"}, &config.AgentConfig{
			ID: "reviewer", Role: "reviewer", Enabled: true, Backend: "copilot",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := gatewayHive(t, tt.backends...)
			if tt.reviewer != nil {
				srv.deps.Config.Agents["reviewer"] = *tt.reviewer
			}
			if _, err := srv.ApplyPack(2); err != nil {
				t.Fatalf("ApplyPack(2): %v", err)
			}
			if got := srv.deps.Config.Agents["reviewer"].Backend; got != "copilot" {
				t.Errorf("reviewer backend = %q, want the pack's copilot", got)
			}
		})
	}
}
