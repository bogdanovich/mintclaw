package eval

import "github.com/bogdanovich/mintclaw/pkg/diagnostictrace"

func projectPDFSourceFields(
	trace diagnostictrace.Trace, binding nativeModelBinding, sha string, filter func(string) string,
) *PDFSourceFieldsEvidence {
	// Reuse the native complete-turn/model-shape validation; document call
	// ownership and result semantics are additionally checked below.
	turn := projectNativeModelTrace(trace, binding, sha, filter)
	if turn.Code != "observed" || trace.Outcome.Status != "suspended" {
		return unavailablePDFSourceFields()
	}
	result := &PDFSourceFieldsEvidence{
		Version: Version, Scope: pdfSourceFieldsScope, Code: "observed", Source: trace.Source,
		BindingSHA256: binding.hash(), TraceScope: binding.TraceScope,
		TraceID: trace.TraceID, TraceSHA256: sha, SessionHash: turn.SessionHash, RecordCount: len(trace.Records),
	}
	type call struct {
		sequence  uint64
		completed bool
	}
	calls := map[pdfSourceCallIdentity]call{}
	terminal := false
	for _, record := range trace.Records {
		if record.Kind == diagnostictrace.RecordTurnEnd {
			terminal = true
			result.TerminalSequence = record.Sequence
		}
		if record.Kind != diagnostictrace.RecordToolCall && record.Kind != diagnostictrace.RecordToolResult {
			continue
		}
		payload, err := decodeDocument[diagnostictrace.ToolPayload](record.Data, 16*1024)
		if err != nil {
			return unavailablePDFSourceFields()
		}
		if payload.Tool != "document" {
			if payload.DocumentFields != nil {
				return unavailablePDFSourceFields()
			}
			continue
		}
		if terminal || record.Origin.Kind != "runtime_event" || record.Origin.ID == "" ||
			record.Scope.AgentID != binding.AgentID || record.Scope.SessionHash != turn.SessionHash ||
			record.Scope.TurnID != binding.TraceScope.TurnID || record.Scope.Channel != "mintclaw" ||
			!boundedIdentity(
				record.Correlation.ToolCallID,
				256,
			) || !boundedIdentity(record.Correlation.ToolCallGeneration, 256) ||
			!payload.Executed || payload.ArgumentsPreview != "" || payload.ResultPreview != "" {
			return unavailablePDFSourceFields()
		}
		id, generation := record.Correlation.ToolCallID, record.Correlation.ToolCallGeneration
		identity := pdfSourceCallIdentity{id, generation}
		if record.Kind == diagnostictrace.RecordToolCall {
			if _, duplicate := calls[identity]; duplicate || payload.Status != "started" ||
				payload.DocumentFields != nil ||
				!validDigest(payload.ArgsHash) {
				return unavailablePDFSourceFields()
			}
			calls[identity] = call{sequence: record.Sequence}
			continue
		}
		origin, found := calls[identity]
		if !found || origin.completed || payload.Status != "completed" {
			return unavailablePDFSourceFields()
		}
		origin.completed = true
		calls[identity] = origin
		result.DocumentResults++
		if payload.DocumentFields == nil {
			if payload.IsError {
				result.ErrorResults++
			} else {
				result.UnknownResults++
			}
			continue
		}
		if payload.IsError || !validDigest(payload.ResultHash) || !payload.DocumentFields.Valid() {
			return unavailablePDFSourceFields()
		}
		result.Reads = append(result.Reads, PDFSourceFieldRead{
			CallID: id, Generation: generation, CallSequence: origin.sequence, ResultSequence: record.Sequence,
			ResultSHA256: payload.ResultHash, Fields: *payload.DocumentFields,
		})
	}
	for _, call := range calls {
		if !call.completed {
			return unavailablePDFSourceFields()
		}
	}
	return result
}
