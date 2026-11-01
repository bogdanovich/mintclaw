package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func sourceFieldsTestFacts(item Case) *diagnostictrace.DocumentFieldsPayload {
	return &diagnostictrace.DocumentFieldsPayload{
		Operation: "fields", SourceSHA256: item.Fixtures[0].SHA256,
		SchemaFields: item.PDF.FieldCount, ReturnedFields: item.PDF.SourceFields.MinReturnedFields,
		RequiredFields: item.PDF.SourceFields.RequiredFields, ValuesPresent: item.PDF.SourceFields.ValuesPresent,
		DefaultsPresent: item.PDF.SourceFields.DefaultsPresent,
	}
}

func sourceFieldsTrace(
	t *testing.T,
	binding nativeModelBinding,
	item Case,
	includeDocument bool,
) diagnostictrace.Trace {
	t.Helper()
	trace := nativeModelTestTrace(t, binding)
	trace.Outcome.Status = "suspended"
	end := trace.Records[len(trace.Records)-1]
	end.Data, _ = json.Marshal(diagnostictrace.TurnPayload{Status: "suspended"})
	trace.Records = trace.Records[:len(trace.Records)-1]
	if includeDocument {
		for index, kind := range []diagnostictrace.RecordKind{diagnostictrace.RecordToolCall, diagnostictrace.RecordToolResult} {
			payload := diagnostictrace.ToolPayload{
				Tool:     "document",
				Executed: true,
				Status:   "started",
				ArgsHash: digest([]byte("args")),
			}
			if index == 1 {
				payload.Status, payload.ArgsHash = "completed", ""
				payload.ResultHash, payload.DocumentFields = digest([]byte("result")), sourceFieldsTestFacts(item)
			}
			data, _ := json.Marshal(payload)
			origin := "source-call"
			if index == 1 {
				origin = "source-result"
			}
			trace.Records = append(trace.Records, diagnostictrace.Record{
				Kind:   kind,
				Scope:  trace.Records[0].Scope,
				Data:   data,
				Origin: diagnostictrace.Origin{Kind: "runtime_event", ID: origin},
				Correlation: diagnostictrace.Correlation{
					ToolCallID:         "document-call",
					ToolCallGeneration: "document-generation",
				},
			})
		}
	}
	trace.Records = append(trace.Records, end)
	return finalizeSourceTrace(t, trace)
}

func finalizeSourceTrace(t *testing.T, trace diagnostictrace.Trace) diagnostictrace.Trace {
	t.Helper()
	for index := range trace.Records {
		trace.Records[index].Sequence, trace.Records[index].OffsetNanos = uint64(index+1), int64(index)
	}
	result, err := diagnostictrace.Finalize(trace)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sourceFieldsReaderFixture(t *testing.T) (admittedTarget, nativeModelBinding, Trial, LoadedCase, string) {
	t.Helper()
	target, binding, store := nativeModelReaderFixture(t)
	item := loadPDFTestCase(t, "pdf.empty.required")
	binding.CaseSHA256 = item.Digest
	trial := Trial{
		Version: Version, ID: binding.TrialID, CaseID: item.Case.ID, CaseSHA256: item.Digest,
		AgentID: binding.AgentID, SessionKey: binding.SessionKey,
		PDF: &PDFTrialEvidence{Version: Version, SourceSHA256: item.Case.Fixtures[0].SHA256},
		Cancellation: &QuestionCancellationEvidence{
			Workspace: binding.TraceScope.Workspace, AgentID: binding.AgentID,
			SessionKey: binding.SessionKey, OriginTurnID: binding.TraceScope.TurnID,
		},
		Events: []Event{
			{Kind: "input", Turn: 1, RequestID: binding.RequestIDs[0]},
			{
				Kind: "question", Turn: 1, RequestID: binding.RequestIDs[0], AgentID: binding.AgentID,
				SessionKey: binding.SessionKey, TraceScopes: []events.TraceScope{binding.TraceScope},
			},
		},
	}
	return target, binding, trial, item, store
}

func TestPDFSourceFieldsRequiresExactNativeReadNotProse(t *testing.T) {
	for _, mode := range []string{
		"complete", "opaque-call-id", "reused-call-id", "no-document", "wrong-source", "partial-window", "wrong-count", "unknown-metadata", "error",
		"foreign-session", "foreign-call-scope", "generation", "result-before-call", "duplicate-result",
		"missing-count", "truncated", "completed-not-suspended", "mode", "ambiguous", "spaced-id",
	} {
		t.Run(mode, func(t *testing.T) {
			target, binding, trial, item, store := sourceFieldsReaderFixture(t)
			trace := sourceFieldsTrace(t, binding, item.Case, mode != "no-document")
			if mode != "no-document" {
				result := &trace.Records[len(trace.Records)-2]
				var payload diagnostictrace.ToolPayload
				if json.Unmarshal(result.Data, &payload) != nil {
					t.Fatal("result fixture unavailable")
				}
				switch mode {
				case "opaque-call-id":
					trace.Records[len(trace.Records)-3].Correlation.ToolCallID = "call_Pdf:AbC_1"
					result.Correlation.ToolCallID = "call_Pdf:AbC_1"
				case "spaced-id":
					result.Correlation.ToolCallID = "spaced identity"
				case "wrong-source":
					payload.DocumentFields.SourceSHA256 = strings.Repeat("b", 64)
				case "partial-window":
					payload.DocumentFields.Operation, payload.DocumentFields.ReturnedFields = "form.discover", 1
				case "wrong-count":
					payload.DocumentFields.ValuesPresent--
				case "unknown-metadata":
					payload.DocumentFields = nil
				case "error":
					payload.DocumentFields, payload.IsError = nil, true
				case "foreign-session":
					trace.Metadata.SessionHash = digest([]byte("foreign-session"))
				case "foreign-call-scope":
					result.Scope.TurnID = "foreign-turn"
				case "generation":
					result.Correlation.ToolCallGeneration = "foreign-generation"
				case "truncated":
					trace.Truncation.Incomplete = true
				case "completed-not-suspended":
					trace.Outcome.Status = "completed"
					trace.Records[len(trace.Records)-1].Data, _ = json.Marshal(
						diagnostictrace.TurnPayload{Status: "completed"},
					)
				}
				result.Data, _ = json.Marshal(payload)
				if mode == "missing-count" {
					var object map[string]any
					_ = json.Unmarshal(result.Data, &object)
					delete(object["document_fields"].(map[string]any), "values_present")
					result.Data, _ = json.Marshal(object)
				}
				if mode == "result-before-call" {
					i := len(trace.Records) - 2
					trace.Records[i-1], trace.Records[i] = trace.Records[i], trace.Records[i-1]
				}
				if mode == "duplicate-result" {
					duplicate := *result
					duplicate.Origin.ID = "duplicate-origin"
					trace.Records = append(
						trace.Records[:len(trace.Records)-1],
						duplicate,
						trace.Records[len(trace.Records)-1],
					)
				}
				if mode == "reused-call-id" {
					call, completed := trace.Records[len(trace.Records)-3], *result
					call.Correlation.ToolCallGeneration, completed.Correlation.ToolCallGeneration = "next-generation", "next-generation"
					call.Origin.ID, completed.Origin.ID = "reused-call-origin", "reused-result-origin"
					end := trace.Records[len(trace.Records)-1]
					trace.Records = append(trace.Records[:len(trace.Records)-1], call, completed, end)
				}
			}
			trace = finalizeSourceTrace(t, trace)
			name := writeNativeModelTestTrace(t, store, trace)
			if mode == "mode" {
				if os.Chmod(name, 0o644) != nil {
					t.Fatal("mode control unavailable")
				}
			}
			if mode == "ambiguous" {
				trace.TraceID = "trace-second"
				writeNativeModelTestTrace(t, store, trace)
			}
			trial.PDF.SourceFields = observePDFSourceFields(t.Context(), target, nativeModelTestLifecycle(),
				nativeModelTestSurfaces(), trial, nil, nil)
			check := gradePDFSourceFields("read", item.Case, trial)
			want := Inconclusive
			switch mode {
			case "complete", "opaque-call-id", "reused-call-id":
				want = Pass
			case "no-document", "wrong-source", "partial-window", "wrong-count", "error":
				want = Fail
			}
			if check.Verdict != want {
				t.Fatalf("%s source check = %#v, proof = %#v", mode, check, trial.PDF.SourceFields)
			}
		})
	}
}

func TestPDFSourceFieldsRawLeakDominatesEarlierInvalidTrace(t *testing.T) {
	target, _, trial, _, store := sourceFieldsReaderFixture(t)
	if os.WriteFile(filepath.Join(store, "trace-a.json"), []byte(`{"incomplete":`), 0o600) != nil ||
		os.WriteFile(filepath.Join(store, "trace-z.json"),
			[]byte(`{"discarded":"\u0050DF_SOURCE_CANARY","discarded":"public"}`), 0o600) != nil {
		t.Fatal("raw control unavailable")
	}
	proof := observePDFSourceFields(t.Context(), target, nativeModelTestLifecycle(), nativeModelTestSurfaces(),
		trial, map[string]string{"fact": "PDF_SOURCE_CANARY"}, nil)
	data, _ := json.Marshal(proof)
	if proof.Code != "leak_observed" || strings.Contains(string(data), "PDF_SOURCE_CANARY") {
		t.Fatal("raw private leak hidden by uncertainty/projection", proof)
	}
}

func TestPDFSourceFieldsFrozenProofRejectsOmittedZeroCounts(t *testing.T) {
	fields := sourceFieldsTestFacts(loadPDFTestCase(t, "pdf.empty.required").Case)
	fields.ValuesPresent = 0
	data, _ := json.Marshal(PDFSourceFieldRead{Fields: *fields})
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	delete(object["fields"].(map[string]any), "values_present")
	data, _ = json.Marshal(object)
	if _, err := decodeDocument[PDFSourceFieldRead](data, 4096); err == nil {
		t.Fatal("omitted count silently became observed zero")
	}
}

func TestPDFSourceFieldsRunRequiresExplicitCaptureBeforeEffects(t *testing.T) {
	item := loadPDFTestCase(t, "pdf.empty.required")
	fixture := newAdmissionFixture(t)
	enableAdmissionPDF(t, fixture)
	for _, owned := range []bool{false, true} {
		report, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
			Home: fixture.home, StartTarget: owned, OutputParent: fixture.output, IncludePending: true,
		})
		if !errors.Is(err, ErrUnsafeTarget) || report.ReportDir != "" ||
			fixture.tasks.Load() != 0 || fixture.probes.Load() != 0 {
			t.Fatal("source check silently enabled capture or attempted target work", report, err)
		}
	}
	entries, err := os.ReadDir(fixture.output)
	if err != nil || len(entries) != 0 {
		t.Fatal("source capture refusal allocated evidence/profile state", err)
	}
}

func TestPDFSourceFieldsUnsupportedCapabilityStillSkips(t *testing.T) {
	fixture := newAdmissionFixture(t)
	result, err := runAdmissionFixture(t, fixture, loadPDFTestCase(t, "pdf.empty.required"))
	if !errors.Is(err, ErrNotPassed) || result.Verdict != Skipped || result.Coverage[Skipped] != 1 ||
		fixture.tasks.Load() != 0 || fixture.probes.Load() != 1 {
		t.Fatal("unsupported source case required capture or reached a target", err)
	}
}

func TestPDFSourceFieldsCaseContractRequiresPairedBoundedExpectations(t *testing.T) {
	original := loadPDFTestCase(t, "pdf.empty.required").Case
	data, _ := json.Marshal(original)
	if original.Validate() != nil {
		t.Fatal("new empty-fields case invalid")
	}
	for _, mode := range []string{"no-check", "no-expectation", "zero-window", "large-window", "negative", "large-count", "private"} {
		t.Run(mode, func(t *testing.T) {
			var item Case
			if json.Unmarshal(data, &item) != nil {
				t.Fatal("case copy unavailable")
			}
			switch mode {
			case "no-check":
				item.Checks = item.Checks[:len(item.Checks)-1]
			case "no-expectation":
				item.PDF.SourceFields = nil
			case "zero-window":
				item.PDF.SourceFields.MinReturnedFields = 0
			case "large-window":
				item.PDF.SourceFields.MinReturnedFields = item.PDF.FieldCount + 1
			case "negative":
				item.PDF.SourceFields.ValuesPresent = -1
			case "large-count":
				item.PDF.SourceFields.DefaultsPresent = item.PDF.FieldCount + 1
			case "private":
				item.Driver.Facts = map[string]string{"fact": "private-value"}
			}
			if item.Validate() == nil {
				t.Fatal("unsupported/unbounded source contract admitted", mode)
			}
		})
	}
}

func TestPDFSourceFieldsHardFailureSurvivesPositiveJudge(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(map[bool]string{true: "read", false: "no-read"}[present], func(t *testing.T) {
			trial, item, prepared, surfaces := finalizedNoWriteFixture(t)
			item.Case.PDF.SourceFields = &PDFSourceFieldsExpectation{
				MinReturnedFields: item.Case.PDF.FieldCount, RequiredFields: 3, ValuesPresent: 3, DefaultsPresent: 4,
			}
			item.Case.Checks = append(item.Case.Checks, Check{ID: "source-fields", Kind: "pdf_source_fields"})
			encoded, _ := json.Marshal(item.Case)
			item.Digest = digest(encoded)
			trial.CaseSHA256 = item.Digest
			binding, valid := pdfSourceBinding(trial, prepared.workspace)
			if !valid {
				t.Fatal("native cancellation did not retain a source turn binding")
			}
			trace := sourceFieldsTrace(t, binding, item.Case, present)
			trial.PDF.SourceFields = projectPDFSourceFields(trace, binding, digest([]byte("native-bytes")), nil)
			if !trial.PDF.SourceFields.valid(trial, prepared.workspace) {
				t.Fatal("complete source proof invalid", trial.PDF.SourceFields)
			}
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: adaptiveJudgePass, FinishReason: "stop",
			}}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			assessFinalizedPDF(
				t.Context(),
				judge,
				item.Case,
				&trial,
				prepared.workspace,
				cleanPDFLifecycle(),
				surfaces,
				nil,
			)
			want := Fail
			if present {
				want = Pass
			}
			if trial.Verdict != want || provider.calls != 1 || !trial.EvidenceEligible ||
				gradeFinalizedPDFChecks(item.Case, trial)[len(item.Case.Checks)-1].Verdict != want {
				t.Fatal("positive judge overrode missing read or frozen check changed", trial.Code, trial.Verdict)
			}
		})
	}
}
