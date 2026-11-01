package eval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func isolationDefinition(t *testing.T) LoadedCase {
	t.Helper()
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	items, err := Select(corpus, Selection{IDs: []string{"general.session.isolation"}})
	if err != nil || len(items) != 1 {
		t.Fatal("isolation definition unavailable", err)
	}
	return items[0]
}

func TestIsolationScopeMaterializationPreservesSourceAndInputCeiling(t *testing.T) {
	item := isolationDefinition(t).Case
	before, _ := json.Marshal(item)
	main, peer := isolationMainCase(item), isolationPeerCase(item)
	for _, scope := range []Case{main, peer} {
		if scope.Validate() != nil || scope.ID != item.ID || scope.Limits.MaxUserTurns != 3 ||
			scope.Steering.Peer != nil || scope.Steering.Scenario != "boundary" {
			t.Fatal("scope materialization changed source identity or widened input bounds")
		}
	}
	if main.Limits.MaxUserTurns+peer.Limits.MaxUserTurns != item.Limits.MaxUserTurns ||
		main.Task != item.Task || strings.Contains(peer.Task, item.Steering.InitialMarker) ||
		!strings.Contains(peer.Task, item.Steering.Peer.InitialMarker) ||
		peer.Checks[1].Expected != item.Steering.Peer.Update {
		t.Fatal("per-scope task/checks did not stay source-derived")
	}
	after, _ := json.Marshal(item)
	if string(before) != string(after) {
		t.Fatal("peer materialization mutated the source contract")
	}
	for _, kind := range []string{"missing_peer", "overlap", "same_peer_markers", "task_knows_peer", "all_mode"} {
		t.Run(kind, func(t *testing.T) {
			var invalid Case
			if json.Unmarshal(before, &invalid) != nil {
				t.Fatal("clone")
			}
			switch kind {
			case "missing_peer":
				invalid.Steering.Peer = nil
			case "overlap":
				invalid.Steering.Peer.InitialMarker = invalid.Steering.InitialMarker + "-peer"
			case "same_peer_markers":
				invalid.Steering.Peer.Update = invalid.Steering.Peer.InitialMarker
			case "task_knows_peer":
				invalid.Task += " " + invalid.Steering.Peer.Update
			case "all_mode":
				invalid.Steering.Modes = []string{"all"}
			}
			if !errors.Is(invalid.Validate(), ErrInvalidCase) {
				t.Fatal("ambiguous or widened isolation contract admitted")
			}
		})
	}
}

func TestIsolationPinsExistingNativeTwoTurnCapacityBeforeWork(t *testing.T) {
	item := isolationDefinition(t)
	for _, kind := range []string{"positive", "missing_pin", "wrong_agent", "wrong_default", "unbounded"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			enableTestActiveTool(fixture)
			fixture.preset.ActiveTool.MaxParallelTurns = 2
			fixture.cfg.Agents.List[0].MaxParallelTurns = 2
			fixture.cfg.Agents.Defaults.MaxParallelTurns = 2
			switch kind {
			case "missing_pin":
				fixture.preset.ActiveTool.MaxParallelTurns = 0
			case "wrong_agent":
				fixture.cfg.Agents.List[0].MaxParallelTurns = 1
			case "wrong_default":
				fixture.cfg.Agents.Defaults.MaxParallelTurns = 1
			case "unbounded":
				fixture.preset.ActiveTool.MaxParallelTurns = 3
				fixture.cfg.Agents.List[0].MaxParallelTurns = 3
				fixture.cfg.Agents.Defaults.MaxParallelTurns = 3
			}
			err := validateConversationProfile(fixture.cfg, fixture.preset, fixture.workspace)
			wantPolicy := kind == "positive" || kind == "missing_pin"
			if (err == nil) != wantPolicy {
				t.Fatal("native capacity pin mismatch", err)
			}
			if kind == "missing_pin" {
				_, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
					Home: fixture.home, OutputParent: fixture.output, StartTarget: true, IncludePending: true,
				})
				if !errors.Is(err, ErrUnsafeTarget) || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
					t.Fatal("legacy unpinned capacity executed isolation", err)
				}
			}
		})
	}
}

func TestIsolationJudgeProjectionIsVisibleOnlyAndNamespaced(t *testing.T) {
	trial := Trial{
		Events: []Event{{ID: "event-000001", Kind: "input", Turn: 1, Content: "scope-a-visible"}},
		Isolation: &IsolationEvidence{Version: Version, Peer: IsolationScopeEvidence{
			Events:   []Event{{ID: "event-000001", Kind: "final", Turn: 1, Content: "scope-b-visible"}},
			Steering: &SteeringEvidence{Trace: "raw-proof-not-for-judge"},
		}},
	}
	projected := steeringJudgeEvents(trial)
	visible, err := visibleConversation(projected, nil)
	data, _ := json.Marshal(visible)
	if err != nil || len(visible) != 2 || !strings.Contains(string(data), "scope a") ||
		!strings.Contains(string(data), "scope b") || strings.Contains(string(data), "raw-proof-not-for-judge") ||
		visible[0].ID == visible[1].ID || trial.Events[0].ID != "event-000001" ||
		trial.Isolation.Peer.Events[0].ID != "event-000001" {
		t.Fatal("judge projection lost scope, mutated raw events or exposed proof", err)
	}
}
