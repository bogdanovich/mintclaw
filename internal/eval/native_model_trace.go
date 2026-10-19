package eval

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

func projectNativeModelTrace(
	trace diagnostictrace.Trace, binding nativeModelBinding, sha string, filter func(string) string,
) NativeModelTurn {
	result := NativeModelTurn{
		TrialID: binding.TrialID, TraceScope: binding.TraceScope,
		BindingSHA256: binding.hash(), Code: "invalid_trace",
	}
	session := diagnostictrace.FilteredHash(binding.SessionKey, filter)
	if trace.Metadata.RootTurnID != binding.TraceScope.TurnID || trace.Metadata.AgentID != binding.AgentID ||
		trace.Metadata.SessionHash != session || trace.Truncation.Incomplete || trace.Truncation.DroppedRecords != 0 ||
		len(trace.Truncation.Reasons) != 0 || len(trace.Truncation.DroppedByKind) != 0 || trace.Outcome == nil ||
		!slices.Contains([]string{"completed", "error", "aborted", "suspended"}, trace.Outcome.Status) {
		return result
	}
	start, end, terminal := 0, 0, false
	calls, attempts := []NativeModelCall{}, map[string]int{}
	retries := 0
	for index, record := range trace.Records {
		if record.Sequence != uint64(index+1) {
			return result
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(record.Data, &fields) != nil {
			return result
		}
		for key, value := range fields {
			if strings.HasSuffix(key, "_preview") && string(value) != `""` {
				return result
			}
		}
		if (strings.HasPrefix(string(record.Kind), "model.") || record.Kind == diagnostictrace.RecordTurnStart ||
			record.Kind == diagnostictrace.RecordTurnEnd) &&
			(record.Origin.Kind != "runtime_event" || record.Origin.ID == "" ||
				record.Scope.AgentID != binding.AgentID || record.Scope.SessionHash != session ||
				record.Scope.TurnID != binding.TraceScope.TurnID || record.Scope.Channel != "mintclaw") {
			return result
		}
		switch record.Kind {
		case diagnostictrace.RecordTurnStart:
			start++
			if index != 0 {
				return result
			}
		case diagnostictrace.RecordTurnEnd:
			end++
			terminal = true
			payload, err := decodeDocument[diagnostictrace.TurnPayload](record.Data, 16*1024)
			if err != nil || payload.Status != trace.Outcome.Status {
				return result
			}
		case diagnostictrace.RecordModelRetry:
			position, exists := attempts[record.Correlation.ModelAttemptID]
			payload, err := decodeDocument[diagnostictrace.ModelPayload](record.Data, 16*1024)
			if terminal || !exists || calls[position].UsageState != "not_returned" || calls[position].RetryObserved ||
				err != nil || payload.Status != "retry" || payload.Attempt < 1 {
				return result
			}
			calls[position].RetryObserved = true
			retries++
		case diagnostictrace.RecordModelFallbackAttempt:
			if terminal {
				return result
			}
			retries++
		case diagnostictrace.RecordModelRequest, diagnostictrace.RecordModelResponse:
			if terminal || !validID(record.Correlation.ModelAttemptID) {
				return result
			}
			payload, err := decodeDocument[diagnostictrace.ModelPayload](record.Data, 16*1024)
			if err != nil {
				return result
			}
			id := record.Correlation.ModelAttemptID
			if record.Kind == diagnostictrace.RecordModelRequest {
				if _, duplicate := attempts[id]; duplicate {
					return result
				}
				attempts[id] = len(calls)
				call := NativeModelCall{
					AttemptID: id, RequestProvider: payload.Provider, RequestModel: payload.Model,
					UsageState: "not_returned", Inputs: NativeModelInputs{
						Prompt: payload.PromptHash, StableSystem: payload.StableSystemHash,
						DynamicSystem: payload.DynamicSystemHash, Tools: payload.ToolSchemaHash,
						History: payload.HistoryHash, DynamicTail: payload.DynamicTailHash,
					},
				}
				if !call.valid() {
					return result
				}
				calls = append(calls, call)
				continue
			}
			position, exists := attempts[id]
			if !exists || calls[position].UsageState != "not_returned" || payload.Status != "success" ||
				payload.PromptTokens < 0 || payload.ResponseTokens < 0 {
				return result
			}
			call := &calls[position]
			call.ResponseProvider, call.ResponseModel = payload.Provider, payload.Model
			call.ResponseSHA256, call.UsageState = payload.ResponseHash, "legacy_unknown"
			if payload.HasProviderUsage != nil {
				call.UsageState = "unavailable"
				if *payload.HasProviderUsage {
					call.UsageState = "observed"
					call.Usage = &NativeModelUsage{int64(payload.PromptTokens), int64(payload.ResponseTokens)}
				} else if payload.PromptTokens != 0 || payload.ResponseTokens != 0 {
					return result
				}
			}
			if !call.valid() {
				return result
			}
		}
	}
	if start != 1 || end != 1 {
		return result
	}
	if trace.Outcome.Status != "error" && trace.Outcome.Status != "aborted" {
		for _, call := range calls {
			if call.UsageState == "not_returned" && !call.RetryObserved {
				return result
			}
		}
	}
	result.Code, result.TraceID, result.TraceSHA256 = "observed", trace.TraceID, sha
	result.SessionHash, result.Calls, result.RetryFallbackRecords = session, calls, retries
	return result
}
