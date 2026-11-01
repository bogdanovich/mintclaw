package eval

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
)

type nativeModelBinding struct {
	Generation int               `json:"generation,omitempty"`
	TrialID    string            `json:"trial_id"`
	CaseSHA256 string            `json:"case_sha256"`
	AgentID    string            `json:"agent_id"`
	SessionKey string            `json:"session_key"`
	TraceScope events.TraceScope `json:"trace_scope"`
	RequestIDs []string          `json:"request_ids"`
}

func (binding nativeModelBinding) hash() string {
	data, _ := json.Marshal(binding)
	return digest(data)
}

func nativeModelBindings(trial Trial, workspace string) ([]nativeModelBinding, bool) {
	type inputIdentity struct{ Turn, Generation int }
	inputs := map[string]inputIdentity{}
	for _, event := range trial.Events {
		if event.Kind == "input" {
			if !validID(event.RequestID) || event.Turn < 1 || event.Generation < 0 ||
				event.Generation > maxOwnedGatewayGenerations || inputs[event.RequestID].Turn != 0 {
				return nil, false
			}
			inputs[event.RequestID] = inputIdentity{event.Turn, event.Generation}
		}
	}
	type generationScope struct {
		Generation int
		Scope      events.TraceScope
	}
	scopes := map[generationScope][]string{}
	for _, event := range trial.Events {
		if event.Kind == "input" || event.Kind == "stream" || len(event.TraceScopes) == 0 {
			continue
		}
		if event.Turn < 1 || inputs[event.RequestID] != (inputIdentity{event.Turn, event.Generation}) ||
			event.AgentID != trial.AgentID || event.SessionKey == "" || event.SessionKey != trial.SessionKey {
			return nil, false
		}
		for _, scope := range event.TraceScopes {
			if !scope.Complete() || !validID(scope.TurnID) || workspace != "" && scope.Workspace != workspace {
				return nil, false
			}
			key := generationScope{event.Generation, scope}
			if event.Generation < 0 || event.Generation > maxOwnedGatewayGenerations {
				return nil, false
			}
			if !slices.Contains(scopes[key], event.RequestID) {
				scopes[key] = append(scopes[key], event.RequestID)
			}
		}
	}
	result := []nativeModelBinding{}
	for scope, requests := range scopes {
		slices.Sort(requests)
		result = append(result, nativeModelBinding{
			TrialID: trial.ID, CaseSHA256: trial.CaseSHA256, AgentID: trial.AgentID,
			SessionKey: trial.SessionKey, TraceScope: scope.Scope, RequestIDs: requests, Generation: scope.Generation,
		})
	}
	slices.SortFunc(result, func(a, b nativeModelBinding) int { return strings.Compare(a.hash(), b.hash()) })
	return result, true
}

// Validate the frozen projection against retained trial/route evidence, without
// re-opening runtime state. Edited operator-owned reports are not authenticated.
func selectNativeModels(
	original *NativeModelObservation,
	runtime health.RuntimeProbe,
	bindings []nativeModelBinding,
	selected map[string]bool,
) (*NativeModelObservation, bool) {
	if original == nil {
		return nil, true
	}
	if !original.valid(runtime) {
		return nil, false
	}
	if original.Code == "unavailable" || original.Code == "leak_observed" {
		copy := *original
		return &copy, true
	}
	copy := *original
	copy.Turns = nil
	for _, turn := range original.Turns {
		if selected[turn.TrialID] {
			copy.Turns = append(copy.Turns, turn)
		}
	}
	if len(copy.Turns) != len(bindings) {
		return nil, false
	}
	for _, binding := range bindings {
		if !slices.ContainsFunc(copy.Turns, func(turn NativeModelTurn) bool {
			return turn.Generation == binding.Generation && turn.TrialID == binding.TrialID &&
				turn.TraceScope == binding.TraceScope &&
				turn.BindingSHA256 == binding.hash()
		}) {
			return nil, false
		}
	}
	copy.Code = "observed"
	if slices.ContainsFunc(copy.Turns, func(turn NativeModelTurn) bool { return turn.Code != "observed" }) {
		copy.Code = "partial"
	}
	copy.Totals = summarizeNativeModels(copy.Turns)
	return &copy, copy.valid(runtime)
}
