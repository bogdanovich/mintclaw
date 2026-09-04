package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

func TestFreezeTurnInputOwnsRuntimeSnapshot(t *testing.T) {
	observation := &finalDeliveryObservation{}
	readyCalls := 0
	spec := turnSpec{
		mode: turnModeInteractionContinuation,
		ModelBinding: effectiveModelBinding{Execution: effectiveExecutionState{
			Candidates:         []providers.FallbackCandidate{{Model: "model-1"}},
			CandidateProviders: map[string]providers.LLMProvider{"provider/model-1": nil},
		}},
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-1",
			SessionKey:      "session-1",
			InboundContext: &bus.InboundContext{
				Channel: "telegram",
				Raw:     map[string]string{"thread": "original"},
			},
			SessionScope: &session.SessionScope{Values: map[string]string{"chat": "direct:1"}},
			Media:        []string{"media-1"},
		},
		ForcedSkills: []string{"skill-1"},
		TurnProfile: config.EffectiveTurnProfile{
			AllowedSkills: []string{"allowed-skill"},
			AllowedTools:  []string{"allowed-tool"},
		},
		ObjectiveChecklist: []runtimeObjectiveItem{{
			ID: "objective-1",
			Acceptance: &taskresult.ObjectiveAcceptance{
				RequiredFields: []string{"title"},
			},
		}},
		InitialSteeringMessages: []providers.Message{{
			Role: "user", Content: "original", Media: []string{"steering-media-1"},
		}},
		FinalDeliveryObservation: observation,
		OnTurnReady: func() {
			readyCalls++
		},
	}

	input := freezeTurnInput(spec)
	spec.Dispatch.InboundContext.Raw["thread"] = "mutated"
	spec.Dispatch.SessionScope.Values["chat"] = "mutated"
	spec.Dispatch.Media[0] = "mutated"
	spec.ForcedSkills[0] = "mutated"
	spec.TurnProfile.AllowedSkills[0] = "mutated"
	spec.TurnProfile.AllowedTools[0] = "mutated"
	spec.ModelBinding.Execution.Candidates[0].Model = "mutated"
	delete(spec.ModelBinding.Execution.CandidateProviders, "provider/model-1")
	spec.ObjectiveChecklist[0].Acceptance.RequiredFields[0] = "mutated"
	spec.InitialSteeringMessages[0].Content = "mutated"
	spec.InitialSteeringMessages[0].Media[0] = "mutated"

	if input.Dispatch.InboundContext.Raw["thread"] != "original" ||
		input.Dispatch.SessionScope.Values["chat"] != "direct:1" ||
		input.Dispatch.Media[0] != "media-1" || input.ForcedSkills[0] != "skill-1" ||
		input.TurnProfile.AllowedSkills[0] != "allowed-skill" ||
		input.TurnProfile.AllowedTools[0] != "allowed-tool" ||
		input.ModelBinding.Execution.Candidates[0].Model != "model-1" ||
		len(input.ModelBinding.Execution.CandidateProviders) != 1 ||
		input.ObjectiveChecklist[0].Acceptance.RequiredFields[0] != "title" ||
		input.InitialSteeringMessages[0].Content != "original" ||
		input.InitialSteeringMessages[0].Media[0] != "steering-media-1" {
		t.Fatalf("frozen input changed with request: %#v", input)
	}
	if input.observers.FinalDelivery != observation || input.observers.OnReady == nil {
		t.Fatalf("observation hooks = %#v", input.observers)
	}
	input.observers.OnReady()
	if readyCalls != 1 {
		t.Fatalf("ready calls = %d, want 1", readyCalls)
	}

	runtimeOpts := input.runtimeOptions()
	if runtimeOpts.FinalDeliveryObservation != nil || runtimeOpts.OnTurnReady != nil ||
		runtimeOpts.ApprovalGrant != nil {
		t.Fatalf("runtime options retained caller-owned hooks or mutable grant: %#v", runtimeOpts)
	}
	runtimeOpts.Dispatch.Media[0] = "runtime-mutated"
	runtimeOpts.TurnProfile.AllowedSkills[0] = "runtime-mutated"
	runtimeOpts.TurnProfile.AllowedTools[0] = "runtime-mutated"
	if input.Dispatch.Media[0] != "media-1" || input.TurnProfile.AllowedSkills[0] != "allowed-skill" ||
		input.TurnProfile.AllowedTools[0] != "allowed-tool" {
		t.Fatalf("runtime option mutation reached frozen input: %#v", input)
	}
}

func TestFreezeTurnInputDetachesEmptySessionScopeCollections(t *testing.T) {
	scope := &session.SessionScope{
		Dimensions: []string{},
		Values:     map[string]string{},
	}
	input := freezeTurnInput(turnSpec{Dispatch: DispatchRequest{SessionScope: scope}})

	scope.Dimensions = append(scope.Dimensions, "chat")
	scope.Values["chat"] = "direct:1"

	if input.Dispatch.SessionScope == nil || input.Dispatch.SessionScope.Dimensions == nil ||
		input.Dispatch.SessionScope.Values == nil {
		t.Fatalf("frozen empty scope lost collection shape: %#v", input.Dispatch.SessionScope)
	}
	if len(input.Dispatch.SessionScope.Dimensions) != 0 || len(input.Dispatch.SessionScope.Values) != 0 {
		t.Fatalf("caller mutation reached frozen empty scope: %#v", input.Dispatch.SessionScope)
	}
}

func TestTurnApprovalGrantIsMutableStateNotInput(t *testing.T) {
	grant := &ToolApprovalGrant{InteractionID: "interaction-1", Revision: 2}
	input := freezeTurnInput(turnSpec{ApprovalGrant: grant})
	state := newTurnStateFromInput(&AgentInstance{}, input, grant, turnEventScope{})
	grant.InteractionID = "mutated"

	if got := state.currentApprovalGrant(); got == nil || got.InteractionID != "interaction-1" {
		t.Fatalf("current approval grant = %#v", got)
	}
	state.consumeApprovalGrant()
	if got := state.currentApprovalGrant(); got != nil {
		t.Fatalf("approval grant after consumption = %#v", got)
	}
	if input.runtimeOptions().ApprovalGrant != nil {
		t.Fatal("frozen input unexpectedly owns the mutable approval grant")
	}
}
