package eval

import (
	"os"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/events"
)

func TestRestartNativeBindingsKeepCollidingTurnScopesSeparate(t *testing.T) {
	scope := events.TraceScope{Workspace: "/synthetic/workspace", TurnID: "main-turn-2"}
	trial := Trial{ID: "trial", CaseSHA256: digest(nil), AgentID: "main", SessionKey: "session", Events: []Event{
		{Kind: "input", RequestID: "first", Turn: 1, Generation: 1},
		{
			Kind:        "final",
			RequestID:   "first",
			Turn:        1,
			Generation:  1,
			AgentID:     "main",
			SessionKey:  "session",
			TraceScopes: []events.TraceScope{scope},
		},
		{Kind: "input", RequestID: "second", Turn: 2, Generation: 2},
		{
			Kind:        "final",
			RequestID:   "second",
			Turn:        2,
			Generation:  2,
			AgentID:     "main",
			SessionKey:  "session",
			TraceScopes: []events.TraceScope{scope},
		},
	}}
	bindings, valid := nativeModelBindings(trial, scope.Workspace)
	if !valid || len(bindings) != 2 || bindings[0].hash() == bindings[1].hash() ||
		bindings[0].Generation == bindings[1].Generation {
		t.Fatal("different processes collapsed into one native turn")
	}
	usage := newTargetUsage()
	for index := range trial.Events {
		if trial.Events[index].Kind == "final" {
			trial.Events[index].Usage = &TokenUsage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9}
		}
	}
	usage.addTrial(trial)
	if !usage.valid() || usage.Coverage != "returned_finals_only" || usage.TotalTokens != 18 {
		t.Fatal("restart scope collision lost usage", usage)
	}
	trial.Events[3].Generation = 1
	if _, valid := nativeModelBindings(trial, scope.Workspace); valid {
		t.Fatal("native model binding crossed generations")
	}
	usage = newTargetUsage()
	usage.addTrial(trial)
	if usage.Finals != 1 || usage.TotalTokens != 9 {
		t.Fatal("cross-generation reply was counted", usage)
	}
}

func TestRestartTraceReaderRefusesChangedOrMissingPredecessor(t *testing.T) {
	for _, kind := range []string{"unchanged", "changed", "missing"} {
		t.Run(kind, func(t *testing.T) {
			target, binding, store := nativeModelReaderFixture(t)
			old := nativeModelTestTrace(t, binding)
			old.TraceID = "predecessor-trace"
			path := writeNativeModelTestTrace(t, store, old)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			target.priorTraces = map[string]string{old.TraceID: digest(data)}
			if kind == "changed" {
				target.priorTraces[old.TraceID] = digest([]byte("changed"))
			}
			if kind == "missing" && os.Remove(path) != nil {
				t.Fatal("remove own predecessor")
			}
			current := nativeModelTestTrace(t, binding)
			current.TraceID = "successor-trace"
			writeNativeModelTestTrace(t, store, current)
			binding.Generation = 2
			models := observeNativeModels(t.Context(), target, nativeModelTestLifecycle(), nativeModelTestSurfaces(),
				[]nativeModelBinding{binding}, nil, nil)
			if kind == "unchanged" {
				if models.Code != "observed" || len(models.Turns) != 1 || models.Turns[0].TraceID != current.TraceID ||
					models.Turns[0].Generation != 2 {
					t.Fatal("old trace was reused/ambiguous", models.Code)
				}
			} else if models.Code == "observed" {
				t.Fatal("uncertain predecessor accepted", kind)
			}
		})
	}
}

func TestRestartPrivacyFailureSurvivesIncompleteGenerationEvidence(t *testing.T) {
	for _, kind := range []string{"surface", "budget", "models"} {
		t.Run(kind, func(t *testing.T) {
			generation := ownedGatewayGeneration{}
			switch kind {
			case "surface":
				generation.Surfaces = &PrivateSurfaceObservation{LeakObserved: true}
			case "budget":
				generation.Budget = &TargetBudgetObservation{Code: "private_receipt"}
			case "models":
				generation.Models = &NativeModelObservation{Code: "leak_observed"}
			}
			if targetGenerationsVerdict(RunReport{TargetGenerations: []ownedGatewayGeneration{generation}}) != Fail {
				t.Fatal("known private evidence downgraded to unknown", kind)
			}
		})
	}
}
