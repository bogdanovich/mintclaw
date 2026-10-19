package eval

import (
	"context"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
)

const pdfSourceFieldsScope = "native_question_turn_fields_v1"

// Frozen projections are not authenticated operator-owned reports. Their native
// byte/source/scope bindings are retained for offline checks, never reopened.
type PDFSourceFieldsEvidence struct {
	Version          int                    `json:"version"`
	Scope            string                 `json:"scope"`
	Code             string                 `json:"code"`
	Source           diagnostictrace.Source `json:"source"`
	BindingSHA256    string                 `json:"binding_sha256,omitempty"`
	TraceScope       events.TraceScope      `json:"trace_scope"`
	TraceID          string                 `json:"trace_id,omitempty"`
	TraceSHA256      string                 `json:"trace_sha256,omitempty"`
	SessionHash      string                 `json:"session_hash,omitempty"`
	RecordCount      int                    `json:"record_count"`
	TerminalSequence uint64                 `json:"terminal_sequence"`
	DocumentResults  int                    `json:"document_results"`
	UnknownResults   int                    `json:"unknown_results"`
	ErrorResults     int                    `json:"error_results"`
	Reads            []PDFSourceFieldRead   `json:"reads,omitempty"`
}

type PDFSourceFieldRead struct {
	CallID         string                                `json:"call_id"`
	Generation     string                                `json:"generation"`
	CallSequence   uint64                                `json:"call_sequence"`
	ResultSequence uint64                                `json:"result_sequence"`
	ResultSHA256   string                                `json:"result_sha256"`
	Fields         diagnostictrace.DocumentFieldsPayload `json:"fields"`
}

func pdfSourceBinding(trial Trial, workspace string) (nativeModelBinding, bool) {
	cancellation := trial.Cancellation
	if cancellation == nil && trial.PDF != nil && trial.PDF.Cancellation != nil {
		cancellation = &trial.PDF.Cancellation.Question
	}
	if cancellation == nil || cancellation.Workspace != workspace || cancellation.AgentID != trial.AgentID ||
		cancellation.SessionKey != trial.SessionKey || !validID(cancellation.OriginTurnID) ||
		!validID(trial.ID) || !validDigest(trial.CaseSHA256) {
		return nativeModelBinding{}, false
	}
	bindings, valid := nativeModelBindings(trial, workspace)
	if !valid {
		return nativeModelBinding{}, false
	}
	var selected nativeModelBinding
	count := 0
	for _, binding := range bindings {
		if binding.TraceScope.TurnID == cancellation.OriginTurnID {
			selected, count = binding, count+1
		}
	}
	return selected, count == 1
}

func unavailablePDFSourceFields() *PDFSourceFieldsEvidence {
	return &PDFSourceFieldsEvidence{Version: Version, Scope: pdfSourceFieldsScope, Code: "unavailable"}
}

func observePDFSourceFields(
	ctx context.Context, target admittedTarget, lifecycle *TargetLifecycle, surfaces *PrivateSurfaceObservation,
	trial Trial, facts map[string]string, filter *strings.Replacer,
) *PDFSourceFieldsEvidence {
	binding, valid := pdfSourceBinding(trial, target.preset.ExpectedWorkspace)
	if !valid {
		return unavailablePDFSourceFields()
	}
	result := unavailablePDFSourceFields()
	seen := false
	var replace func(string) string
	if filter != nil {
		replace = filter.Replace
	}
	reason := visitBoundNativeTraces(ctx, target, lifecycle, surfaces, []nativeModelBinding{binding}, facts, filter,
		func(_ int, trace diagnostictrace.Trace, sha string) {
			if seen {
				result = unavailablePDFSourceFields()
				return
			}
			seen = true
			result = projectPDFSourceFields(trace, binding, sha, replace)
		})
	if reason != "" {
		result = unavailablePDFSourceFields()
		if reason == "raw_private_evidence" {
			result.Code = "leak_observed"
		}
	}
	return result
}

func (evidence *PDFSourceFieldsEvidence) valid(trial Trial, workspace string) bool {
	binding, valid := pdfSourceBinding(trial, workspace)
	if !valid || evidence == nil || evidence.Version != Version || evidence.Scope != pdfSourceFieldsScope ||
		evidence.Code != "observed" || evidence.BindingSHA256 != binding.hash() ||
		evidence.TraceScope != binding.TraceScope || !validID(evidence.TraceID) ||
		!validDigest(evidence.TraceSHA256) || !validDigest(evidence.SessionHash) ||
		!nativeModelSourceKnown(health.RuntimeProbe{
			GitCommit: evidence.Source.Commit, BuildVersion: evidence.Source.MintClawVersion,
		}) || evidence.RecordCount < 2 || evidence.RecordCount > evalModelMetadataCapture().MaxRecords ||
		evidence.TerminalSequence < 2 || evidence.TerminalSequence > uint64(evidence.RecordCount) ||
		evidence.DocumentResults < 0 || evidence.DocumentResults > evidence.RecordCount/2 ||
		evidence.UnknownResults < 0 || evidence.ErrorResults < 0 ||
		evidence.UnknownResults > evidence.DocumentResults || evidence.ErrorResults > evidence.DocumentResults ||
		len(evidence.Reads) > evidence.DocumentResults ||
		evidence.UnknownResults+evidence.ErrorResults+len(evidence.Reads) != evidence.DocumentResults {
		return false
	}
	seen, sequences := map[string]bool{}, map[uint64]bool{}
	var previous uint64
	for _, read := range evidence.Reads {
		if !boundedIdentity(read.CallID, 256) || !boundedIdentity(read.Generation, 256) || seen[read.CallID] ||
			!validDigest(read.ResultSHA256) || read.CallSequence <= 1 || read.CallSequence >= read.ResultSequence ||
			read.ResultSequence >= evidence.TerminalSequence || read.ResultSequence <= previous ||
			sequences[read.CallSequence] || sequences[read.ResultSequence] {
			return false
		}
		if !read.Fields.Valid() {
			return false
		}
		previous = read.ResultSequence
		seen[read.CallID], sequences[read.CallSequence], sequences[read.ResultSequence] = true, true, true
	}
	return true
}

func pdfSourceFieldsRuntimeMatches(evidence *PDFSourceFieldsEvidence, runtime health.RuntimeProbe) bool {
	return evidence != nil && evidence.Source == (diagnostictrace.Source{
		Commit: runtime.GitCommit, MintClawVersion: runtime.BuildVersion,
	})
}

func gradePDFSourceFields(id string, item Case, trial Trial) CheckResult {
	result := CheckResult{ID: id, Verdict: Inconclusive, Code: "source_fields_unavailable", EvidenceIDs: []string{}}
	if item.PDF == nil || item.PDF.SourceFields == nil || trial.PDF == nil ||
		trial.PDF.SourceFields == nil ||
		!trial.PDF.SourceFields.valid(trial, trial.PDF.SourceFields.TraceScope.Workspace) {
		return result
	}
	proof, expected := trial.PDF.SourceFields, item.PDF.SourceFields
	result.EvidenceIDs = []string{"pdf.source_fields"}
	for _, read := range proof.Reads {
		fields := read.Fields
		if fields.SourceSHA256 == item.Fixtures[0].SHA256 && fields.SchemaFields == item.PDF.FieldCount &&
			fields.ReturnedFields >= expected.MinReturnedFields && fields.RequiredFields == expected.RequiredFields &&
			fields.ValuesPresent == expected.ValuesPresent && fields.DefaultsPresent == expected.DefaultsPresent {
			return booleanCheck(id, true, result.EvidenceIDs)
		}
	}
	if proof.UnknownResults != 0 {
		return result
	}
	return booleanCheck(id, false, result.EvidenceIDs)
}
