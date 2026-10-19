package eval

import (
	"github.com/bogdanovich/mintclaw/pkg/browseraction"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

// This projection correlates a native producer with one delivered question.
// It does not authenticate tokens, supply values, or authorize browser effects.
func projectBrowserInputTarget(
	trace diagnostictrace.Trace, binding nativeModelBinding, record interactions.Record,
	sha string, filter func(string) string,
) (browseraction.InputTargetObservation, bool) {
	turn := projectNativeModelTrace(trace, binding, sha, filter)
	if turn.Code != "observed" || trace.Outcome.Status != "suspended" || record.ProtectedAnswer == nil {
		return browseraction.InputTargetObservation{}, false
	}
	type callIdentity struct{ id, generation string }
	calls := map[callIdentity]bool{}
	var selected browseraction.InputTargetObservation
	terminal := false
	originCalls := 0
	for _, event := range trace.Records {
		if event.Kind == diagnostictrace.RecordTurnEnd {
			terminal = true
		}
		if event.Kind != diagnostictrace.RecordToolCall && event.Kind != diagnostictrace.RecordToolResult {
			continue
		}
		payload, err := decodeDocument[diagnostictrace.ToolPayload](event.Data, 16*1024)
		if err != nil || payload.BrowserInputTarget != nil && payload.Tool != "browser_input" {
			return browseraction.InputTargetObservation{}, false
		}
		if payload.Tool != "browser_input" {
			continue
		}
		if terminal || event.Origin.Kind != "runtime_event" || event.Origin.ID == "" ||
			event.Scope.AgentID != binding.AgentID || event.Scope.SessionHash != turn.SessionHash ||
			event.Scope.TurnID != binding.TraceScope.TurnID || event.Scope.Channel != "mintclaw" ||
			!boundedIdentity(event.Correlation.ToolCallID, 256) ||
			!boundedIdentity(event.Correlation.ToolCallGeneration, 256) || !payload.Executed ||
			payload.ArgumentsPreview != "" || payload.ResultPreview != "" {
			return browseraction.InputTargetObservation{}, false
		}
		identity := callIdentity{event.Correlation.ToolCallID, event.Correlation.ToolCallGeneration}
		if event.Kind == diagnostictrace.RecordToolCall {
			if identity.id == record.Origin.ToolCallID {
				originCalls++
			}
			if _, duplicate := calls[identity]; duplicate || payload.Status != "started" ||
				payload.BrowserInputTarget != nil || !validDigest(payload.ArgsHash) || originCalls > 1 {
				return browseraction.InputTargetObservation{}, false
			}
			calls[identity] = false
			continue
		}
		completed, found := calls[identity]
		if !found || completed {
			return browseraction.InputTargetObservation{}, false
		}
		calls[identity] = true
		if payload.BrowserInputTarget == nil {
			if payload.Status != "completed" {
				return browseraction.InputTargetObservation{}, false
			}
			continue
		}
		proof := payload.BrowserInputTarget
		if payload.Status != "completed" || payload.IsError || !proof.Valid() ||
			proof.InteractionID != record.ID || identity.id != record.Origin.ToolCallID ||
			!proof.Target.MatchesToken(record.ProtectedAnswer.Token) || selected.Version != "" {
			return browseraction.InputTargetObservation{}, false
		}
		selected = proof.Target
	}
	for _, completed := range calls {
		if !completed {
			return browseraction.InputTargetObservation{}, false
		}
	}
	return selected, selected.Valid()
}
