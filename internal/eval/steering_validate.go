package eval

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

const maxSteeringEvidenceBytes = 3 * 1024 * 1024

type steeringHistoryBinding struct {
	callID string
	args   activeToolArgs
	result string
}

func validSteeringEvidence(
	trial Trial,
	item Case,
	workspace string,
	profile ActiveToolProfile,
	runtime health.RuntimeProbe,
) bool {
	proof := trial.Steering
	if proof == nil || item.Steering == nil || proof.Version != Version || proof.Code != "observed" ||
		proof.Profile != profile || profile.validate() != nil || !slices.Contains(item.Steering.Modes, profile.SteeringMode) ||
		proof.MetadataSHA256 != digest([]byte(proof.Metadata)) ||
		proof.HistorySHA256 != digest([]byte(strings.Join(proof.History, "\n"))) ||
		proof.TraceSHA256 != digest([]byte(proof.Trace)) || len(proof.Guidance) != len(item.Steering.Updates) {
		return false
	}
	encoded, err := json.Marshal(proof)
	if err != nil || len(encoded) > maxSteeringEvidenceBytes {
		return false
	}
	fixture := proof.Fixture
	if fixture.TrialID != trial.SessionID || fixture.SessionID != trial.SessionID || fixture.Slot != 1 ||
		fixture.State != "released" || fixture.EnteredSequence < 1 ||
		fixture.ReleaseSequence <= fixture.EnteredSequence || fixture.SettledSequence <= fixture.ReleaseSequence {
		return false
	}
	primary := Event{}
	inputs := map[string]Event{}
	for _, event := range trial.Events {
		if event.Kind == "input" {
			if inputs[event.ID].ID != "" {
				return false
			}
			inputs[event.ID] = event
		}
		if event.Kind == "final" && event.Turn == 1 {
			if primary.ID != "" {
				return false
			}
			primary = event
		}
	}
	last := lastOutcome(trial.Events)
	marker := item.Steering.Updates[len(item.Steering.Updates)-1]
	if primary.ID == "" || primary.RequestID != proof.PrimaryRequestID || len(primary.TraceScopes) != 1 ||
		primary.AgentID != trial.AgentID || primary.SessionKey != trial.SessionKey ||
		primary.TraceScopes[0].Workspace != workspace || !primary.TraceScopes[0].Complete() ||
		last.Kind != "final" || last.Turn != 2 || last.AgentID != trial.AgentID || last.SessionKey != trial.SessionKey ||
		!strings.Contains(primary.Content, marker) || !strings.Contains(last.Content, marker) ||
		strings.Contains(primary.Content, item.Steering.InitialMarker) ||
		strings.Contains(last.Content, item.Steering.InitialMarker) || len(inputs) != len(item.Steering.Updates)+2 {
		return false
	}
	seen := map[string]bool{}
	for index, guidance := range proof.Guidance {
		input := inputs[guidance.InputEventID]
		held := guidance.Held
		if input.Kind != "input" || input.Turn != 1 ||
			input.Content != steeringGuidance(item.Steering.Updates[index], fixture.TrialID) ||
			guidance.SHA256 != digest([]byte(guidance.Enqueue)) || guidance.ObservedAt.Before(trial.StartedAt) ||
			held.TrialID != fixture.TrialID || held.SessionID != fixture.SessionID || held.Slot != fixture.Slot ||
			held.State != "active" || held.EnteredSequence != fixture.EnteredSequence ||
			held.ReleaseSequence != 0 || held.SettledSequence != 0 {
			return false
		}
		var event activeToolLogEvent
		if json.Unmarshal([]byte(guidance.Enqueue), &event) != nil || !validID(event.ID) || seen[event.ID] ||
			event.Kind != "agent.interrupt.received" || event.InterruptKind != "steering" || event.Role != "user" ||
			event.QueueDepth < 1 || event.AgentID != trial.AgentID || event.SessionKey != trial.SessionKey ||
			event.TurnID != primary.TraceScopes[0].TurnID || event.Time.IsZero() ||
			event.MessageHash != diagnostictrace.FilteredHash(input.Content, nil) {
			return false
		}
		seen[event.ID] = true
	}
	binding, valid := inspectSteeringHistory(proof, trial, item)
	if !valid {
		return false
	}
	return validSteeringTrace(proof, trial, primary, binding, runtime) &&
		validSteeringDeliveries(proof, trial, workspace)
}

func inspectSteeringHistory(proof *SteeringEvidence, trial Trial, item Case) (steeringHistoryBinding, bool) {
	var binding steeringHistoryBinding
	if len(proof.Metadata) > MaxManifestBytes || len(proof.History) > memory.MaxJSONLInspectionRecords+1 {
		return binding, false
	}
	history := []byte(strings.Join(proof.History, "\n"))
	if len(history)+len(proof.Metadata) > memory.MaxJSONLInspectionBytes {
		return binding, false
	}
	snapshot, err := memory.InspectJSONLSnapshot(
		context.Background(),
		trial.SessionKey,
		[]byte(proof.Metadata),
		history,
	)
	if err != nil || snapshot.Metadata.Key != trial.SessionKey || snapshot.Metadata.HistoryDirty ||
		snapshot.Metadata.Skip != 0 || snapshot.Metadata.Summary != "" ||
		snapshot.Metadata.Count != len(snapshot.History) ||
		!slices.Equal(snapshot.Metadata.ClientSessionIDs, []string{trial.SessionID}) {
		return binding, false
	}
	scope, err := session.DecodeCurrentSessionScope(snapshot.Metadata.Scope)
	if err != nil || scope.AgentID != trial.AgentID || scope.Channel != "mintclaw" ||
		scope.ClientSessionID != trial.SessionID || session.BuildSessionKey(*scope) != trial.SessionKey ||
		scope.Values["chat"] != "" && scope.Values["chat"] != "direct:mintclaw:"+trial.SessionID {
		return binding, false
	}
	calls, results, callIndex, resultIndex := 0, 0, -1, -1
	users := []string{}
	updates := []int{}
	for index, message := range snapshot.History {
		for _, call := range message.ToolCalls {
			calls++
			if message.Role != "assistant" || call.Name != activeToolNativeName || !boundedIdentity(call.ID, 128) {
				return binding, false
			}
			raw, err := json.Marshal(call.Arguments)
			if err != nil {
				return binding, false
			}
			args, err := DecodeManifest[activeToolArgs](raw)
			if err != nil || args != (activeToolArgs{TrialID: trial.SessionID, Slot: 1}) {
				return binding, false
			}
			binding.callID, binding.args, callIndex = call.ID, args, index
		}
		if message.Role == "tool" {
			results++
			if binding.callID == "" || message.ToolCallID != binding.callID ||
				message.ToolResultStatus != providers.ToolResultStatusSuccess {
				return binding, false
			}
			binding.result, resultIndex = message.Content, index
		}
		if message.Role == "user" {
			users = append(users, message.Content)
			for _, marker := range item.Steering.Updates {
				if message.Content == steeringGuidance(marker, trial.SessionID) {
					updates = append(updates, index)
				}
			}
		}
	}
	// This smoke case deliberately emits one held read. Native agent tests cover
	// larger emitted batches; an extra/replayed call is not silently ignored.
	if calls != 1 || results != 1 || callIndex < 0 || resultIndex <= callIndex ||
		len(updates) != len(item.Steering.Updates) || len(users) != len(item.Steering.Updates)+2 {
		return binding, false
	}
	initial, followup := "", ""
	for _, event := range trial.Events {
		if event.Kind == "input" && event.RequestID == proof.PrimaryRequestID {
			initial = event.Content
		}
		if event.Kind == "input" && event.Turn == 2 {
			followup = event.Content
		}
	}
	if initial == "" || followup == "" || users[0] != initial || users[len(users)-1] != followup {
		return binding, false
	}
	for index, marker := range item.Steering.Updates {
		if updates[index] <= resultIndex || users[index+1] != steeringGuidance(marker, trial.SessionID) {
			return binding, false
		}
	}
	return binding, true
}
