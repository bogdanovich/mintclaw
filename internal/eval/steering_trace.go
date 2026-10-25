package eval

import (
	"encoding/json"
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/health"
)

func validSteeringTrace(
	proof *SteeringEvidence, trial Trial, primary Event, binding steeringHistoryBinding, runtime health.RuntimeProbe,
) bool {
	trace, err := diagnostictrace.Decode([]byte(proof.Trace))
	limits := evalModelMetadataCapture()
	if err != nil ||
		trace.Source != (diagnostictrace.Source{Commit: runtime.GitCommit, MintClawVersion: runtime.BuildVersion}) ||
		trace.Metadata.RootTurnID != primary.TraceScopes[0].TurnID ||
		trace.Metadata.AgentID != trial.AgentID ||
		trace.Metadata.SessionHash != diagnostictrace.FilteredHash(trial.SessionKey, nil) ||
		trace.Policy.ContentMode != diagnostictrace.ContentMetadataOnly ||
		trace.Truncation.Incomplete ||
		trace.Truncation.DroppedRecords != 0 ||
		len(trace.Truncation.Reasons) != 0 ||
		len(trace.Truncation.DroppedByKind) != 0 ||
		trace.Outcome == nil ||
		trace.Outcome.Status != "completed" ||
		trace.Limits != (diagnostictrace.AppliedLimits{
			MaxTraceBytes: limits.MaxTraceBytes, MaxRecords: limits.MaxRecords, MaxRecordBytes: limits.MaxRecordBytes,
		}) {
		return false
	}
	interrupts := map[string]int{}
	for index, guidance := range proof.Guidance {
		var native activeToolLogEvent
		if json.Unmarshal([]byte(guidance.Enqueue), &native) != nil {
			return false
		}
		interrupts[native.ID] = index
	}
	args, _ := json.Marshal(map[string]any{"trial_id": binding.args.TrialID, "slot": binding.args.Slot})
	callIndex, resultIndex, calls, results := -1, -1, 0, 0
	received, injected, requests := []int{}, []int{}, []int{}
	counts := []int{}
	seenInterrupts := map[string]bool{}
	for index, record := range trace.Records {
		if record.Sequence != uint64(index+1) {
			return false
		}
		if record.Kind == diagnostictrace.RecordModelRequest {
			requests = append(requests, index)
		}
		switch record.Kind {
		case diagnostictrace.RecordToolCall, diagnostictrace.RecordToolResult,
			diagnostictrace.RecordInterrupt, diagnostictrace.RecordSteeringInjected:
			if record.Scope.AgentID != trial.AgentID || record.Scope.TurnID != trace.Metadata.RootTurnID ||
				record.Scope.SessionHash != trace.Metadata.SessionHash {
				return false
			}
		default:
			continue
		}
		switch record.Kind {
		case diagnostictrace.RecordToolCall, diagnostictrace.RecordToolResult:
			var payload diagnostictrace.ToolPayload
			if json.Unmarshal(record.Data, &payload) != nil || payload.Tool != activeToolNativeName ||
				record.Correlation.ToolCallID != binding.callID || !payload.Executed || payload.IsError ||
				payload.ArgumentsPreview != "" || payload.ResultPreview != "" {
				return false
			}
			if record.Kind == diagnostictrace.RecordToolCall {
				calls++
				callIndex = index
				if payload.Status != "started" || payload.ArgsHash != diagnostictrace.FilteredHash(string(args), nil) {
					return false
				}
			} else {
				results++
				resultIndex = index
				if payload.Status != "completed" ||
					payload.ResultHash != diagnostictrace.FilteredHash(binding.result, nil) {
					return false
				}
			}
		case diagnostictrace.RecordInterrupt:
			position, found := interrupts[record.Origin.ID]
			var payload diagnostictrace.SteeringPayload
			if !found || seenInterrupts[record.Origin.ID] || position != len(received) ||
				json.Unmarshal(
					record.Data,
					&payload,
				) != nil || payload.Status != "steering" || payload.Role != "user" ||
				payload.ContentPreview != "" || payload.QueueDepth < 1 {
				return false
			}
			var event activeToolLogEvent
			if json.Unmarshal([]byte(proof.Guidance[position].Enqueue), &event) != nil ||
				payload.MessageHash != event.MessageHash {
				return false
			}
			seenInterrupts[record.Origin.ID] = true
			received = append(received, index)
		case diagnostictrace.RecordSteeringInjected:
			var payload diagnostictrace.SteeringPayload
			if json.Unmarshal(record.Data, &payload) != nil || payload.Status != "injected" || payload.Count < 1 {
				return false
			}
			injected, counts = append(injected, index), append(counts, payload.Count)
		}
	}
	if calls != 1 || results != 1 || callIndex < 0 || resultIndex <= callIndex ||
		len(received) != len(proof.Guidance) || len(requests) < 2 {
		return false
	}
	for _, index := range received {
		if index <= callIndex || index >= resultIndex {
			return false
		}
	}
	// Native dequeueForTurnLocked coalesces the current sender/authority in
	// both configured modes. Anonymous one-at-a-time polls are a different API.
	if !slices.Equal(counts, []int{len(proof.Guidance)}) {
		return false
	}
	for offset, index := range injected {
		if index <= resultIndex {
			return false
		}
		boundary := len(trace.Records)
		if offset+1 < len(injected) {
			boundary = injected[offset+1]
		}
		if !slices.ContainsFunc(requests, func(request int) bool { return request > index && request < boundary }) {
			return false
		}
	}
	return true
}
