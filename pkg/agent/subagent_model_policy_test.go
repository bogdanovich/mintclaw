package agent

import (
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/state"
)

func TestMergeSubagentsConfig_AgentOverridesDefaults(t *testing.T) {
	defaults := &config.SubagentsConfig{
		AllowAgents:              []string{"browser"},
		Model:                    &config.AgentModelConfig{Primary: "deepseek", Fallbacks: []string{"glm"}},
		SessionModelOverrideMode: subagentSessionModelOverrideFallbackOnly,
	}
	override := &config.SubagentsConfig{
		AllowAgents:              []string{"media"},
		Model:                    &config.AgentModelConfig{Primary: "gemini-flash-lite", Fallbacks: []string{"kimi"}},
		SessionModelOverrideMode: subagentSessionModelOverrideIgnore,
	}
	got := mergeSubagentsConfig(defaults, override)
	if got == nil {
		t.Fatal("mergeSubagentsConfig() = nil")
	}
	if len(got.AllowAgents) != 1 || got.AllowAgents[0] != "media" {
		t.Fatalf("AllowAgents = %#v, want media override", got.AllowAgents)
	}
	if got.Model == nil || got.Model.Primary != "gemini-flash-lite" {
		t.Fatalf("Model = %#v, want override primary", got.Model)
	}
	if got.SessionModelOverrideMode != subagentSessionModelOverrideIgnore {
		t.Fatalf("SessionModelOverrideMode = %q, want ignore", got.SessionModelOverrideMode)
	}
}

func TestResolveSubagentModelPlan_Ignore(t *testing.T) {
	target := &AgentInstance{
		Model:     "gpt-5.4",
		Fallbacks: []string{"deepseek", "glm"},
		Subagents: &config.SubagentsConfig{
			SessionModelOverrideMode: subagentSessionModelOverrideIgnore,
		},
	}
	got := resolveSubagentModelPlan(target, "gemini-flash-lite", "")
	if got.Primary != "gpt-5.4" {
		t.Fatalf("Primary = %q, want gpt-5.4", got.Primary)
	}
	if len(got.Fallbacks) != 2 || got.Fallbacks[0] != "deepseek" || got.Fallbacks[1] != "glm" {
		t.Fatalf("Fallbacks = %#v, want unchanged", got.Fallbacks)
	}
}

func TestResolveSubagentModelPlan_Inherit(t *testing.T) {
	target := &AgentInstance{
		Model:     "gpt-5.4",
		Fallbacks: []string{"deepseek", "glm"},
		Subagents: &config.SubagentsConfig{
			SessionModelOverrideMode: subagentSessionModelOverrideInherit,
		},
	}
	got := resolveSubagentModelPlan(target, "gemini-flash-lite", "")
	if got.Primary != "gemini-flash-lite" {
		t.Fatalf("Primary = %q, want gemini-flash-lite", got.Primary)
	}
	if len(got.Fallbacks) != 2 || got.Fallbacks[0] != "deepseek" || got.Fallbacks[1] != "glm" {
		t.Fatalf("Fallbacks = %#v, want base fallbacks", got.Fallbacks)
	}
}

func TestResolveSubagentModelPlan_FallbackOnly(t *testing.T) {
	target := &AgentInstance{
		Model:     "gpt-5.4",
		Fallbacks: []string{"deepseek", "glm"},
		Subagents: &config.SubagentsConfig{
			SessionModelOverrideMode: subagentSessionModelOverrideFallbackOnly,
		},
	}
	got := resolveSubagentModelPlan(target, "gemini-flash-lite", "")
	if got.Primary != "gpt-5.4" {
		t.Fatalf("Primary = %q, want gpt-5.4", got.Primary)
	}
	if len(got.Fallbacks) != 3 || got.Fallbacks[0] != "gemini-flash-lite" {
		t.Fatalf("Fallbacks = %#v, want override prepended", got.Fallbacks)
	}
}

func TestResolveSubagentModelPlan_UsesConfiguredSubagentModel(t *testing.T) {
	target := &AgentInstance{
		Model:     "gpt-5.4",
		Fallbacks: []string{"deepseek", "glm"},
		Subagents: &config.SubagentsConfig{
			Model: &config.AgentModelConfig{
				Primary:   "kimi",
				Fallbacks: []string{"glm"},
			},
			SessionModelOverrideMode: subagentSessionModelOverrideIgnore,
		},
	}
	got := resolveSubagentModelPlan(target, "gemini-flash-lite", "")
	if got.Primary != "kimi" {
		t.Fatalf("Primary = %q, want kimi", got.Primary)
	}
	if len(got.Fallbacks) != 1 || got.Fallbacks[0] != "glm" {
		t.Fatalf("Fallbacks = %#v, want subagents.model fallbacks", got.Fallbacks)
	}
}

func TestResolveSubagentModelPlan_ExplicitModelWins(t *testing.T) {
	target := &AgentInstance{
		Model:     "gpt-5.6-luna",
		Fallbacks: []string{"default-fallback"},
		Subagents: &config.SubagentsConfig{
			Model: &config.AgentModelConfig{
				Primary:   "configured-child",
				Fallbacks: []string{"child-fallback"},
			},
			SessionModelOverrideMode: subagentSessionModelOverrideInherit,
		},
	}

	got := resolveSubagentModelPlan(target, "session-override", "gpt-5.6-sol")
	if got.Primary != "gpt-5.6-sol" || !got.Explicit {
		t.Fatalf("plan = %#v, want explicit gpt-5.6-sol", got)
	}
	if len(got.Fallbacks) != 1 || got.Fallbacks[0] != "child-fallback" {
		t.Fatalf("Fallbacks = %#v, want child policy fallbacks", got.Fallbacks)
	}
}

func TestResolveSubagentModelPlan_NilTarget(t *testing.T) {
	got := resolveSubagentModelPlan(nil, "gemini-flash-lite", "")
	if got.Primary != "" {
		t.Fatalf("Primary = %q, want empty", got.Primary)
	}
	if len(got.Fallbacks) != 0 {
		t.Fatalf("Fallbacks = %#v, want empty", got.Fallbacks)
	}
	if got.Mode != subagentSessionModelOverrideIgnore {
		t.Fatalf("Mode = %q, want ignore", got.Mode)
	}
	if got.ParentOverride != "gemini-flash-lite" {
		t.Fatalf("ParentOverride = %q, want gemini-flash-lite", got.ParentOverride)
	}
}

func TestInheritedSubagentOverride_ReadsParentBinding(t *testing.T) {
	parent := &turnState{
		modelBinding: effectiveModelBinding{
			Override: state.SessionModelOverride{Model: "gemini-flash-lite"},
		},
	}
	if got := inheritedSubagentOverride(parent); got != "gemini-flash-lite" {
		t.Fatalf("inheritedSubagentOverride() = %q, want gemini-flash-lite", got)
	}
}

func TestBuildSubagentChildBinding_ReusesTargetRuntimeWhenPlanMatches(t *testing.T) {
	var al *AgentLoop
	target := &AgentInstance{
		ID:        "main",
		Model:     "test-model",
		Fallbacks: []string{"deepseek"},
	}
	parent := &turnState{
		opts: freezeTurnInput(turnSpec{
			Dispatch: DispatchRequest{
				RouteSessionKey: "route-parent",
			},
		}),
		modelBinding: effectiveModelBinding{
			Override: state.SessionModelOverride{Model: "gemini-flash-lite"},
		},
	}

	got, err := al.buildSubagentChildBinding(parent, target, "")
	if err != nil {
		t.Fatalf("buildSubagentChildBinding() error = %v", err)
	}
	if got.WorkspaceAgent != target {
		t.Fatalf("WorkspaceAgent = %#v, want original target agent", got.WorkspaceAgent)
	}
	if got.Execution.Model != "" {
		t.Fatalf("Execution.Model = %q, want reused runtime binding", got.Execution.Model)
	}
	if got.RouteSessionKey != "route-parent" {
		t.Fatalf("RouteSessionKey = %q, want route-parent", got.RouteSessionKey)
	}
	if got.Override.Model != "gemini-flash-lite" {
		t.Fatalf("Override.Model = %q, want gemini-flash-lite", got.Override.Model)
	}
}

func TestBuildSubagentChildBinding_PreservesTargetRoutingStateOnRebuild(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: t.TempDir(),
				ModelName: "test-model",
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "test-model",
				Provider:  "openai",
				Model:     "test-model",
				APIKeys:   config.SimpleSecureStrings("test-key"),
				APIBase:   "https://example.invalid/v1",
				Enabled:   true,
			},
			{
				ModelName: "gemini-flash-lite",
				Provider:  "openai",
				Model:     "gemini-flash-lite",
				APIKeys:   config.SimpleSecureStrings("test-key"),
				APIBase:   "https://example.invalid/v1",
				Enabled:   true,
			},
			{
				ModelName: "light-model",
				Provider:  "openai",
				Model:     "light-model",
				APIKeys:   config.SimpleSecureStrings("test-key"),
				APIBase:   "https://example.invalid/v1",
				Enabled:   true,
			},
			{
				ModelName: "gpt-5.6-sol",
				Provider:  "openai",
				Model:     "gpt-5.6-sol",
				APIKeys:   config.SimpleSecureStrings("test-key"),
				APIBase:   "https://example.invalid/v1",
				Enabled:   true,
			},
		},
	}
	al := &AgentLoop{cfg: cfg}
	al.modelExecution = &modelExecutionManager{configProvider: al.GetConfig}
	router := routing.New(routing.RouterConfig{LightModel: "light-model", Threshold: 1})
	target := &AgentInstance{
		ID:            "child",
		Model:         "test-model",
		Fallbacks:     []string{"light-model"},
		Router:        router,
		LightProvider: &turnProfileCaptureProvider{},
		LightCandidates: []providers.FallbackCandidate{
			{Provider: "openai", Model: "light-model"},
		},
		Subagents: &config.SubagentsConfig{
			SessionModelOverrideMode: subagentSessionModelOverrideInherit,
		},
	}
	parent := &turnState{
		modelBinding: effectiveModelBinding{
			Override: state.SessionModelOverride{Model: "gemini-flash-lite"},
		},
	}

	got, err := al.buildSubagentChildBinding(parent, target, "")
	if err != nil {
		t.Fatalf("buildSubagentChildBinding() error = %v", err)
	}
	execution := got.ExecutionState()
	if execution.Router != router {
		t.Fatalf("ExecutionState().Router = %#v, want preserved router", execution.Router)
	}
	if len(execution.LightCandidates) != 1 || execution.LightCandidates[0].Model != "light-model" {
		t.Fatalf("ExecutionState().LightCandidates = %#v, want preserved light candidates", execution.LightCandidates)
	}
	if execution.LightProvider == nil {
		t.Fatal("ExecutionState().LightProvider = nil, want preserved provider")
	}
	got.Cleanup()

	explicit, err := al.buildSubagentChildBinding(parent, target, "gpt-5.6-sol")
	if err != nil {
		t.Fatalf("explicit buildSubagentChildBinding() error = %v", err)
	}
	defer explicit.Cleanup()
	explicitExecution := explicit.ExecutionState()
	if explicitExecution.Model != "gpt-5.6-sol" || len(explicitExecution.Candidates) == 0 ||
		explicitExecution.Candidates[0].DisplayName != "gpt-5.6-sol" {
		t.Fatalf("explicit execution = %#v, want gpt-5.6-sol primary", explicitExecution)
	}
	if explicitExecution.Router != nil || len(explicitExecution.LightCandidates) != 0 ||
		explicitExecution.LightProvider != nil {
		t.Fatalf("explicit execution retained automatic light-model routing: %#v", explicitExecution)
	}
	if explicit.Override.Model != "gemini-flash-lite" {
		t.Fatalf("explicit binding lost parent override metadata: %#v", explicit.Override)
	}
	if target.Model != "test-model" {
		t.Fatalf("explicit child selection mutated parent agent model to %q", target.Model)
	}
	if invalid, invalidErr := al.buildSubagentChildBinding(parent, target, "missing-model"); invalidErr == nil {
		invalid.Cleanup()
		t.Fatal("unknown explicit model silently fell back to the target or session model")
	}
}

func TestExactChildBindingIsolatesLightAndStickyRouting(t *testing.T) {
	provider := &stickyFallbackProvider{}
	al, target, cleanup := newTurnCoordFallbackTestLoop(t, provider)
	defer cleanup()
	al.providerFactory = func(modelConfig *config.ModelConfig) (providers.LLMProvider, string, error) {
		return provider, modelConfig.Model, nil
	}
	target.Router = routing.New(routing.RouterConfig{LightModel: "fallback-model", Threshold: 1})
	target.LightCandidates = []providers.FallbackCandidate{target.Candidates[1]}
	target.LightProvider = provider
	parent := &turnState{opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{
		RouteSessionKey: "route-parent",
	}})}

	binding, err := al.buildSubagentChildBinding(parent, target, target.Model)
	if err != nil {
		t.Fatalf("buildSubagentChildBinding() error = %v", err)
	}
	defer binding.Cleanup()
	execution := binding.ExecutionState()
	if execution.Model != target.Model || binding.ExactModel != target.Model {
		t.Fatalf("exact same-model binding = %#v, execution = %#v", binding, execution)
	}
	if execution.Router != nil || len(execution.LightCandidates) != 0 || execution.LightProvider != nil {
		t.Fatalf("exact same-model binding retained light routing: %#v", execution)
	}
	if binding.RouteSessionKey != "route-parent" || binding.autoFallbackRouteSessionKey() != "" {
		t.Fatalf("exact binding route isolation = %#v", binding)
	}

	err = al.setAutoModelSelection("route-parent", state.AutoModelSelection{
		SelectedProvider: execution.Candidates[0].Provider,
		SelectedModel:    execution.Candidates[0].Model,
		ActiveProvider:   execution.Candidates[1].Provider,
		ActiveModel:      execution.Candidates[1].Model,
		Reason:           string(providers.FailoverRateLimit),
		ExpiresAt:        time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := al.selectCandidates(execution, "", nil, binding.autoFallbackRouteSessionKey())
	if selection.activeCandidates[0].StableKey() != execution.Candidates[0].StableKey() {
		t.Fatalf("exact binding consumed parent sticky fallback: %#v", selection.activeCandidates)
	}
	if err = al.clearAutoModelSelection("route-parent"); err != nil {
		t.Fatal(err)
	}
	al.modelExecution.updateAutoFallbackSelection(
		binding.autoFallbackRouteSessionKey(),
		selection.selectedCandidates,
		&providers.FallbackResult{
			Provider: execution.Candidates[1].Provider,
			Model:    execution.Candidates[1].Model,
			Attempts: []providers.FallbackAttempt{{Reason: providers.FailoverRateLimit}},
		},
		false,
	)
	if _, ok := al.getAutoModelSelection("route-parent"); ok {
		t.Fatal("exact child mutated the parent sticky fallback selection")
	}
}

func TestBindResumedInteractionModel_RebuildsExactSameModel(t *testing.T) {
	provider := &simpleConvProvider{}
	al, target, cleanup := newTurnCoordFallbackTestLoop(t, provider)
	defer cleanup()
	al.providerFactory = func(modelConfig *config.ModelConfig) (providers.LLMProvider, string, error) {
		return provider, modelConfig.Model, nil
	}
	target.Router = routing.New(routing.RouterConfig{LightModel: "fallback-model", Threshold: 1})
	target.LightCandidates = []providers.FallbackCandidate{target.Candidates[1]}
	target.LightProvider = provider

	binding := al.bindResumedInteractionModel("route-parent", target, target.Model)
	defer binding.Cleanup()
	execution := binding.ExecutionState()
	if execution.Model != target.Model || binding.ExactModel != target.Model {
		t.Fatalf("resumed exact binding = %#v, execution = %#v", binding, execution)
	}
	if execution.Router != nil || len(execution.LightCandidates) != 0 || execution.LightProvider != nil {
		t.Fatalf("resumed exact binding retained light routing: %#v", execution)
	}
	if binding.RouteSessionKey != "route-parent" || binding.autoFallbackRouteSessionKey() != "" {
		t.Fatalf("resumed exact binding route isolation = %#v", binding)
	}
}
