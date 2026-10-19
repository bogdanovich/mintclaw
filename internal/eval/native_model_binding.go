package eval

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
)

type nativeModelBinding struct {
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
	inputs := map[string]int{}
	for _, event := range trial.Events {
		if event.Kind == "input" {
			if !validID(event.RequestID) || event.Turn < 1 || inputs[event.RequestID] != 0 {
				return nil, false
			}
			inputs[event.RequestID] = event.Turn
		}
	}
	scopes := map[events.TraceScope][]string{}
	for _, event := range trial.Events {
		if event.Kind == "input" || event.Kind == "stream" || len(event.TraceScopes) == 0 {
			continue
		}
		if event.Turn < 1 || inputs[event.RequestID] != event.Turn ||
			event.AgentID != trial.AgentID || event.SessionKey == "" || event.SessionKey != trial.SessionKey {
			return nil, false
		}
		for _, scope := range event.TraceScopes {
			if !scope.Complete() || !validID(scope.TurnID) || workspace != "" && scope.Workspace != workspace {
				return nil, false
			}
			if !slices.Contains(scopes[scope], event.RequestID) {
				scopes[scope] = append(scopes[scope], event.RequestID)
			}
		}
	}
	result := []nativeModelBinding{}
	for scope, requests := range scopes {
		slices.Sort(requests)
		result = append(result, nativeModelBinding{
			TrialID: trial.ID, CaseSHA256: trial.CaseSHA256, AgentID: trial.AgentID,
			SessionKey: trial.SessionKey, TraceScope: scope, RequestIDs: requests,
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
			return turn.TrialID == binding.TrialID && turn.TraceScope == binding.TraceScope &&
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
