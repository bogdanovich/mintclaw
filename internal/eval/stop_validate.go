package eval

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

type nativeStopShape struct {
	inputs  []Event
	stopped Event
	final   Event
}

func inspectNativeStopShape(trial Trial) (nativeStopShape, bool) {
	shape := nativeStopShape{inputs: []Event{}}
	stage := 0
	requests := map[string]bool{}
	for _, event := range trial.Events {
		switch event.Kind {
		case "input":
			if !validID(event.RequestID) || requests[event.RequestID] ||
				stage == 0 && (len(shape.inputs) >= 3 || event.Turn != 1) ||
				stage == 1 && (len(shape.inputs) != 3 || event.Turn != 2) || stage == 2 {
				return shape, false
			}
			requests[event.RequestID] = true
			shape.inputs = append(shape.inputs, event)
		case "stopped":
			if stage != 0 || len(shape.inputs) != 3 || event.Turn != 1 ||
				event.RequestID != shape.inputs[2].RequestID || shape.inputs[2].Content != "/stop" ||
				!boundedIdentity(event.MessageID, 128) || len(event.TraceScopes) != 0 || event.Usage != nil {
				return shape, false
			}
			shape.stopped, stage = event, 1
		case "final":
			if stage != 1 || len(shape.inputs) != 4 || event.Turn != 2 ||
				event.RequestID != shape.inputs[3].RequestID || len(event.TraceScopes) != 1 {
				return shape, false
			}
			shape.final, stage = event, 2
		case "stream":
			if stage != 1 || len(shape.inputs) != 4 || event.RequestID != shape.inputs[3].RequestID {
				return shape, false
			}
		default:
			return shape, false
		}
	}
	return shape, stage == 2
}

// This narrowly authorizes a trace-less native control receipt, not an LLM
// response or a generic missing-trace exception. It does not recurse via Grade.
func validNativeStopReply(trial Trial, reply Event, workspace string) bool {
	proof := trial.NativeStop
	shape, valid := inspectNativeStopShape(trial)
	if !valid || proof == nil || proof.Version != Version || proof.Code != "observed" ||
		proof.Workspace != workspace || proof.Profile.validate() != nil ||
		proof.Profile.SteeringMode != "one-at-a-time" || proof.Profile.MaxParallelTurns != 1 ||
		reply.ID != shape.stopped.ID || reply.AgentID != trial.AgentID || reply.SessionKey != trial.SessionKey ||
		proof.PrimaryRequestID != shape.inputs[0].RequestID || proof.StopRequestID != shape.inputs[2].RequestID ||
		proof.Guidance.InputEventID != shape.inputs[1].ID ||
		proof.Guidance.ObservedAt.Before(trial.StartedAt) ||
		proof.AbortObservedAt.Before(proof.Guidance.ObservedAt) {
		return false
	}
	enqueue, ok := decodeStopLog(proof.Guidance.Enqueue)
	if !ok || enqueue.Time.Before(trial.StartedAt) || enqueue.Time.After(proof.Guidance.ObservedAt) ||
		enqueue.MessageHash != diagnostictrace.FilteredHash(shape.inputs[1].Content, nil) {
		return false
	}
	binding, valid := inspectNativeStopAbort(proof, trial.AgentID, trial.SessionKey, trial.SessionID)
	return valid && !binding.endedAt.After(proof.AbortObservedAt)
}

func validNativeStopEvidence(trial Trial, item Case, workspace string, profile ActiveToolProfile) bool {
	return nativeStopEvidenceCode(trial, item, workspace, profile) == ""
}

func nativeStopEvidenceCode(trial Trial, item Case, workspace string, profile ActiveToolProfile) string {
	proof := trial.NativeStop
	shape, valid := inspectNativeStopShape(trial)
	if !valid || proof == nil || item.NativeStop == nil || item.Validate() != nil {
		return "invalid_stop_contract"
	}
	if proof.Profile != profile {
		return "unexpected_stop_profile"
	}
	if !validNativeStopReply(trial, shape.stopped, workspace) ||
		!validTrialConversation(trial, trial.AgentID, workspace) {
		return "invalid_stop_control_scope"
	}
	if proof.MetadataSHA256 != digest([]byte(proof.Metadata)) ||
		proof.HistorySHA256 != digest([]byte(strings.Join(proof.History, "\n"))) {
		return "invalid_stop_capture_digest"
	}
	if shape.inputs[1].Content != nativeStopGuidance(item.NativeStop.QueuedMarker, trial.SessionID) ||
		shape.inputs[3].Content != item.Driver.Steps[0].Action.Text {
		return "unexpected_stop_inputs"
	}
	if !strings.Contains(shape.final.Content, item.NativeStop.ReuseMarker) ||
		strings.Contains(shape.final.Content, item.NativeStop.QueuedMarker) {
		return "wrong_stop_reuse_value"
	}
	encoded, err := json.Marshal(proof)
	if err != nil || len(encoded) > maxSteeringEvidenceBytes || len(proof.Metadata) > MaxManifestBytes {
		return "stop_evidence_limit"
	}
	args, err := json.Marshal(activeToolArgs{TrialID: trial.SessionID, Slot: 1})
	if err != nil || shape.inputs[0].Content != item.Task+"\nEval tool arguments: "+string(args) {
		return "unexpected_stop_task"
	}
	binding, valid := inspectNativeStopAbort(proof, trial.AgentID, trial.SessionKey, trial.SessionID)
	if !valid || shape.final.TraceScopes[0].TurnID == binding.turnID ||
		!validNativeStopReuseLog(proof, trial, shape.final) {
		return "invalid_stop_reuse_log"
	}
	snapshot, err := memory.InspectJSONLSnapshot(context.Background(), trial.SessionKey,
		[]byte(proof.Metadata), []byte(strings.Join(proof.History, "\n")))
	if err != nil || snapshot.Metadata.Key != trial.SessionKey || snapshot.Metadata.Skip != 0 ||
		snapshot.Metadata.Summary != "" || snapshot.Metadata.HistoryDirty || snapshot.Metadata.Count != 5 ||
		len(snapshot.History) != 5 || !slices.Equal(snapshot.Metadata.ClientSessionIDs, []string{trial.SessionID}) {
		return "unexpected_stop_history_scope"
	}
	scope, err := session.DecodeCurrentSessionScope(snapshot.Metadata.Scope)
	if err != nil || scope.AgentID != trial.AgentID || scope.Channel != "mintclaw" ||
		scope.ClientSessionID != trial.SessionID || session.BuildSessionKey(*scope) != trial.SessionKey ||
		scope.Values["chat"] != "" && scope.Values["chat"] != "direct:mintclaw:"+trial.SessionID {
		return "invalid_stop_history_scope"
	}
	history := snapshot.History
	nativeFinal, finalMatched := coreFinalDeliveryContent(proof.Outbox, shape.final.RequestID)
	if !finalMatched {
		return "unexpected_stop_delivery"
	}
	if !strings.Contains(nativeFinal, item.NativeStop.ReuseMarker) ||
		strings.Contains(nativeFinal, item.NativeStop.QueuedMarker) {
		return "wrong_stop_reuse_value"
	}
	if history[0].Role != "user" || history[0].Content != shape.inputs[0].Content ||
		history[1].Role != "assistant" || len(history[1].ToolCalls) != 1 ||
		history[2].Role != "tool" || history[2].ToolCallID != binding.callID ||
		history[2].ToolResultStatus != providers.ToolResultStatusError || history[2].Content == "" ||
		history[3].Role != "user" || history[3].Content != shape.inputs[3].Content ||
		history[4].Role != "assistant" || history[4].Content != nativeFinal ||
		len(history[4].ToolCalls) != 0 {
		return "unpaired_stop_history"
	}
	call := history[1].ToolCalls[0]
	callArgs, err := json.Marshal(call.Arguments)
	if err != nil || call.ID != binding.callID || call.Name != activeToolNativeName {
		return "wrong_stop_tool"
	}
	decodedArgs, err := DecodeManifest[activeToolArgs](callArgs)
	if err != nil || decodedArgs != (activeToolArgs{TrialID: trial.SessionID, Slot: 1}) {
		return "wrong_stop_tool_args"
	}
	for index, message := range history {
		raw, err := json.Marshal(message)
		if err != nil || strings.Contains(string(raw), item.NativeStop.QueuedMarker) ||
			index != 1 && len(message.ToolCalls) != 0 {
			return "queued_stop_input_retained"
		}
	}
	if !validCoreFinalDeliveries(proof.Outbox, proof.OutboxNames, trial, workspace) {
		return "unexpected_stop_delivery"
	}
	return ""
}

func validNativeStopReuseLog(proof *NativeStopEvidence, trial Trial, final Event) bool {
	terminals := 0
	for _, raw := range proof.Log {
		event, valid := decodeStopLog(raw)
		if !valid {
			return false
		}
		if event.TurnID != final.TraceScopes[0].TurnID {
			continue
		}
		if event.AgentID != trial.AgentID || event.SessionKey != trial.SessionKey ||
			event.MessageID != final.RequestID ||
			event.Time.Before(proof.AbortObservedAt) {
			return false
		}
		switch event.Kind {
		case events.KindAgentToolExecStart.String(), events.KindAgentToolExecEnd.String(),
			events.KindAgentSteeringInjected.String():
			return false
		case events.KindAgentTurnEnd.String():
			terminals++
			if event.Status != "completed" || event.LLMCalls != 1 {
				return false
			}
		}
	}
	return terminals == 1
}
