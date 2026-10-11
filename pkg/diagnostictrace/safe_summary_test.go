package diagnostictrace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSummarizeOmitsRichUnknownAndPathLikeValues(t *testing.T) {
	secretPath := "/private/workspace/customer-secret"
	trace := baseTrace(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	trace.Source = Source{
		MintClawVersion: secretPath,
		Commit:          strings.Repeat("a", 40),
	}
	trace.Policy = CapturePolicy{ContentMode: ContentRedacted, Redactor: "mintclaw.config_filter.v1"}
	trace.Metadata = Metadata{
		RootTurnID:  "main-turn-1",
		SessionHash: strings.Repeat("b", 64),
		AgentID:     secretPath,
		ProjectHash: strings.Repeat("c", 64),
	}
	trace.Records[0].Scope = Scope{
		TurnID:      "main-turn-1",
		TargetAlias: secretPath,
	}
	trace.Records[0].Correlation = Correlation{
		ToolCallID: "call-safe-1",
		EventID:    "evt-safe-1",
	}
	trace.Records[0].Data = json.RawMessage(`{
        "status":"completed",
        "input_hash":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
        "input_len":42,
        "executed":false,
        "input_preview":"private prompt",
        "unknown":"private value",
        "reason":"/private/reason"
    }`)
	trace.Outcome = &Outcome{
		Status:      "completed",
		ContentHash: strings.Repeat("e", 64),
		ContentLen:  12,
	}
	trace, err := Finalize(trace)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	summary, err := Summarize(trace)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(encoded)
	for _, forbidden := range []string{secretPath, "private prompt", "private value", "/private/reason", "input_preview"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("safe summary leaked %q: %s", forbidden, text)
		}
	}
	for _, expected := range []string{"completed", "call-safe-1", strings.Repeat("d", 64)} {
		if !strings.Contains(text, expected) {
			t.Fatalf("safe summary lacks %q: %s", expected, text)
		}
	}
	if summary.Projection.UnsafeFieldsOmitted < 5 || !summary.Projection.RichPreviewsOmitted {
		t.Fatalf("projection = %#v", summary.Projection)
	}
	if executed, ok := summary.Records[0].Facts["executed"].(bool); !ok || executed {
		t.Fatalf("executed fact = %#v", summary.Records[0].Facts["executed"])
	}
}

func TestSummarizeRejectsInvalidTrace(t *testing.T) {
	trace := baseTrace(time.Now().UTC())
	trace.TraceID = "../escape"
	if _, err := Summarize(trace); err == nil {
		t.Fatal("Summarize() accepted an invalid trace")
	}
}

func TestSummarizeRestrictsRuntimeCodesMoreThanNames(t *testing.T) {
	trace := baseTrace(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	trace.Records[0].Data = json.RawMessage(`{
        "provider":"OpenAI",
        "model":"gpt-5.6-sol",
        "status":"COMPLETED",
        "reason":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "error_code":"safe_error",
        "message_preview":"private content"
    }`)
	trace, err := Finalize(trace)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	summary, err := Summarize(trace)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	facts := summary.Records[0].Facts
	if facts["provider"] != "OpenAI" || facts["model"] != "gpt-5.6-sol" || facts["error_code"] != "safe_error" {
		t.Fatalf("safe facts = %#v", facts)
	}
	for _, omitted := range []string{"status", "reason", "message_preview"} {
		if _, ok := facts[omitted]; ok {
			t.Fatalf("unsafe fact %q was retained: %#v", omitted, facts)
		}
	}
	if summary.Projection.UnsafeFieldsOmitted != 3 {
		t.Fatalf("unsafe fields omitted = %d, want 3", summary.Projection.UnsafeFieldsOmitted)
	}
}

func TestSummarizeOmitsNegativeCounters(t *testing.T) {
	trace := baseTrace(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	trace.Records[0].Correlation.InteractionRevision = -1
	trace.Records[0].Data = json.RawMessage(`{"status":"completed","input_len":-2}`)
	trace.Outcome = &Outcome{Status: "completed", ContentLen: -3}
	trace.Truncation = Truncation{
		DroppedRecords: -4,
		DroppedByKind:  map[RecordKind]int{RecordTurnStart: -5},
	}
	trace, err := Finalize(trace)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	summary, err := Summarize(trace)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summary.Records[0].Correlation.InteractionRevision != 0 ||
		summary.Outcome.ContentLen != 0 || summary.Truncation.DroppedRecords != 0 ||
		len(summary.Truncation.DroppedByKind) != 0 {
		t.Fatalf("negative counters survived: %#v", summary)
	}
	if _, ok := summary.Records[0].Facts["input_len"]; ok {
		t.Fatalf("negative record counter survived: %#v", summary.Records[0].Facts)
	}
	if summary.Projection.UnsafeFieldsOmitted != 5 {
		t.Fatalf("unsafe fields omitted = %d, want 5", summary.Projection.UnsafeFieldsOmitted)
	}
}
