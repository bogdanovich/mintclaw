package eval

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

const nativeModelTestCommit = "0123456789012345678901234567890123456789"

func nativeModelTestBinding(workspace string) nativeModelBinding {
	return nativeModelBinding{
		TrialID: "trial-native", CaseSHA256: digest([]byte("case")), AgentID: "main",
		SessionKey: "session:synthetic-secret", TraceScope: events.NewTraceScope(workspace, "turn-native"),
		RequestIDs: []string{"request-native"},
	}
}

func nativeModelTestTrace(t *testing.T, binding nativeModelBinding) diagnostictrace.Trace {
	t.Helper()
	trace, err := makeNativeModelTestTrace(binding)
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

func makeNativeModelTestTrace(binding nativeModelBinding) (diagnostictrace.Trace, error) {
	limits := evalModelMetadataCapture()
	trace := diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-turn-native",
		CreatedAt: time.Now().
			UTC(),
		Source: diagnostictrace.Source{Commit: nativeModelTestCommit, MintClawVersion: "native-test"},
		Policy: diagnostictrace.CapturePolicy{ContentMode: diagnostictrace.ContentMetadataOnly},
		Limits: diagnostictrace.AppliedLimits{
			MaxTraceBytes: limits.MaxTraceBytes,
			MaxRecords:    limits.MaxRecords, MaxRecordBytes: limits.MaxRecordBytes,
		},
		Metadata: diagnostictrace.Metadata{
			RootTurnID: binding.TraceScope.TurnID, AgentID: binding.AgentID,
			SessionHash: diagnostictrace.FilteredHash(binding.SessionKey, nil),
		},
		Outcome: &diagnostictrace.Outcome{Status: "completed"},
	}
	add := func(kind diagnostictrace.RecordKind, attempt string, payload any) {
		data, _ := json.Marshal(payload)
		trace.Records = append(trace.Records, diagnostictrace.Record{
			Kind:     kind,
			Sequence: uint64(len(trace.Records) + 1), OffsetNanos: int64(len(trace.Records)),
			Origin: diagnostictrace.Origin{Kind: "runtime_event", ID: fmt.Sprintf("event-%d", len(trace.Records))},
			Scope: diagnostictrace.Scope{
				AgentID: binding.AgentID, SessionHash: trace.Metadata.SessionHash,
				TurnID: binding.TraceScope.TurnID, Channel: "mintclaw",
			},
			Correlation: diagnostictrace.Correlation{ModelAttemptID: attempt}, Data: data,
		})
	}
	add(diagnostictrace.RecordTurnStart, "", diagnostictrace.TurnPayload{InputHash: digest([]byte("task"))})
	for index, tokens := range []int{10252, 100} {
		attempt := fmt.Sprintf("model-attempt-%d", index)
		hash := digest([]byte(attempt))
		add(diagnostictrace.RecordModelRequest, attempt, diagnostictrace.ModelPayload{
			Provider:          "openai",
			Model:             "requested-model",
			PromptHash:        hash,
			StableSystemHash:  hash,
			DynamicSystemHash: hash,
			ToolSchemaHash:    hash,
			HistoryHash:       hash,
			DynamicTailHash:   hash,
		})
		present := true
		// A tool-call-only response has no content hash; it still has usage.
		add(diagnostictrace.RecordModelResponse, attempt, diagnostictrace.ModelPayload{
			Provider: "gemini", Model: "actual-model", Status: "success", HasProviderUsage: &present,
			PromptTokens: tokens, ResponseTokens: index + 3,
		})
	}
	add(diagnostictrace.RecordTurnEnd, "", diagnostictrace.TurnPayload{Status: "completed"})
	return diagnostictrace.Finalize(trace)
}

func rehashNativeModelTestTrace(t *testing.T, trace *diagnostictrace.Trace) {
	t.Helper()
	for index := range trace.Records {
		record := &trace.Records[index]
		record.Sequence, record.OffsetNanos = uint64(index+1), int64(index)
		record.Origin.ID = fmt.Sprintf("event-%d", index)
		value, err := diagnostictrace.RecordDigest(*record)
		if err != nil {
			t.Fatal(err)
		}
		record.Digest = value
	}
	if err := diagnostictrace.Validate(*trace); err != nil {
		t.Fatal("invalid native fixture", err)
	}
}

func TestNativeModelProjectionSumsEveryToolLoopResponse(t *testing.T) {
	binding := nativeModelTestBinding("/private/workspace")
	trace := nativeModelTestTrace(t, binding)
	turn := projectNativeModelTrace(trace, binding, digest([]byte("raw")), nil)
	totals := summarizeNativeModels([]NativeModelTurn{turn})
	if turn.Code != "observed" || len(turn.Calls) != 2 || totals.Requests != 2 || totals.Responses != 2 ||
		totals.UsageResponses != 2 || totals.InputTokens != 10352 || totals.OutputTokens != 7 ||
		totals.Coverage != "observed_native_responses" || CompactTokens(totals.InputTokens) != "10.3k" ||
		turn.Calls[0].RequestModel != "requested-model" || turn.Calls[0].ResponseModel != "actual-model" {
		t.Fatal("native tool loop was collapsed into its last call or guessed model", turn, totals)
	}
}

func TestNativeModelProjectionPreservesUsagePresence(t *testing.T) {
	for _, mode := range []string{"legacy", "unavailable", "zero", "missing"} {
		t.Run(mode, func(t *testing.T) {
			binding := nativeModelTestBinding("/private/workspace")
			trace := nativeModelTestTrace(t, binding)
			for _, index := range []int{2, 4} {
				var payload diagnostictrace.ModelPayload
				_ = json.Unmarshal(trace.Records[index].Data, &payload)
				switch mode {
				case "legacy":
					payload.HasProviderUsage = nil
				case "unavailable":
					absent := false
					payload.HasProviderUsage = &absent
					payload.PromptTokens, payload.ResponseTokens = 0, 0
				case "zero":
					payload.PromptTokens, payload.ResponseTokens = 0, 0
				}
				trace.Records[index].Data, _ = json.Marshal(payload)
			}
			if mode == "missing" {
				trace.Records = slices.Delete(trace.Records, 4, 5)
				trace.Outcome.Status = "error"
				trace.Records[len(trace.Records)-1].Data = []byte(`{"status":"error"}`)
			}
			rehashNativeModelTestTrace(t, &trace)
			turn := projectNativeModelTrace(trace, binding, digest([]byte("raw")), nil)
			totals := summarizeNativeModels([]NativeModelTurn{turn})
			if turn.Code != "observed" || totals.Coverage != map[string]string{
				"legacy": "unknown", "unavailable": "unknown", "zero": "observed_native_responses",
				"missing": "partial_native_responses",
			}[mode] || mode == "zero" && (totals.UsageResponses != 2 || totals.InputTokens != 0) ||
				mode == "legacy" && turn.Calls[0].UsageState != "legacy_unknown" ||
				mode == "unavailable" && turn.Calls[0].UsageState != "unavailable" ||
				mode == "missing" && turn.Calls[1].UsageState != "not_returned" {
				t.Fatal("absence was converted into observed zero or complete usage", mode, turn, totals)
			}
		})
	}
}

func TestNativeModelProjectionRejectsIncompleteOrForeignRecords(t *testing.T) {
	for _, mode := range []string{
		"session", "agent", "turn", "channel", "duplicate-request", "duplicate-response",
		"orphan-response", "truncated", "drop", "preview", "negative", "end", "missing-response", "gap",
	} {
		t.Run(mode, func(t *testing.T) {
			binding := nativeModelTestBinding("/private/workspace")
			trace := nativeModelTestTrace(t, binding)
			switch mode {
			case "session":
				trace.Records[1].Scope.SessionHash = strings.Repeat("b", 64)
			case "agent":
				trace.Metadata.AgentID = "foreign"
			case "turn":
				trace.Records[1].Scope.TurnID = "foreign"
			case "channel":
				trace.Records[1].Scope.Channel = "telegram"
			case "duplicate-request":
				trace.Records[3].Correlation.ModelAttemptID = trace.Records[1].Correlation.ModelAttemptID
			case "duplicate-response":
				trace.Records[4].Correlation.ModelAttemptID = trace.Records[2].Correlation.ModelAttemptID
			case "orphan-response":
				trace.Records = slices.Delete(trace.Records, 1, 2)
			case "truncated":
				trace.Truncation.Incomplete = true
			case "drop":
				trace.Truncation.DroppedRecords = 1
			case "preview", "negative":
				var payload diagnostictrace.ModelPayload
				_ = json.Unmarshal(trace.Records[2].Data, &payload)
				if mode == "preview" {
					payload.ResponsePreview = "unadmitted content"
				} else {
					payload.PromptTokens = -1
				}
				trace.Records[2].Data, _ = json.Marshal(payload)
			case "end":
				trace.Records = trace.Records[:len(trace.Records)-1]
			case "missing-response":
				trace.Records = slices.Delete(trace.Records, 4, 5)
			}
			rehashNativeModelTestTrace(t, &trace)
			if mode == "gap" {
				for index := range trace.Records {
					trace.Records[index].Sequence++
					trace.Records[index].Digest, _ = diagnostictrace.RecordDigest(trace.Records[index])
				}
				if diagnostictrace.Validate(trace) != nil {
					t.Fatal("gap fixture must be structurally native-valid")
				}
			}
			if turn := projectNativeModelTrace(
				trace,
				binding,
				digest([]byte("raw")),
				nil,
			); turn.Code != "invalid_trace" ||
				len(turn.Calls) != 0 {
				t.Fatal("incomplete or mismatched native records qualified", mode, turn)
			}
		})
	}
}

func TestNativeModelTotalsOverflowDoesNotInventUsage(t *testing.T) {
	turn := NativeModelTurn{Code: "observed", Calls: []NativeModelCall{
		{UsageState: "observed", Usage: &NativeModelUsage{InputTokens: math.MaxInt64}},
		{UsageState: "observed", Usage: &NativeModelUsage{InputTokens: 1}},
	}}
	totals := summarizeNativeModels([]NativeModelTurn{turn})
	if totals.Coverage != "overflow" || totals.InputTokens != 0 || totals.OutputTokens != 0 ||
		totals.UsageResponses != 2 {
		t.Fatal(totals)
	}
}
