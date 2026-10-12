package diagnostictrace

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
)

func TestRuntimeCredentialsAreRemovedBeforeTracePreviewTruncation(t *testing.T) {
	value := strings.Repeat("trace-canary-", 128)
	release, err := secretvalues.Register([]byte(value))
	if err != nil {
		t.Fatal("credential guard unavailable")
	}
	defer release()
	preview := (Redactor{}).RedactText("detail="+value, 32)
	if strings.Contains(preview, "trace-canary") {
		t.Fatal("bounded trace preview exposed a secret prefix")
	}
}

func TestRuntimeCredentialsAreRemovedFromUnclassifiedTraceData(t *testing.T) {
	release, err := secretvalues.Register([]byte("571928"))
	if err != nil {
		t.Fatal("credential guard unavailable")
	}
	defer release()
	redactor := Redactor{Filter: func(string) string { return "571928" }}
	if strings.Contains(redactor.RedactText("ordinary", 128), "571928") {
		t.Fatal("trace filter reintroduced a registered value")
	}
	for _, value := range []any{
		map[string]any{"alias": "ebay", "detail": "571928"},
		map[string]any{"detail": 571928},
	} {
		encoded := (Redactor{}).RedactJSON(value, 1024)
		if !json.Valid([]byte(encoded)) || strings.Contains(encoded, "571928") {
			t.Fatal("unclassified trace data exposed a credential")
		}
	}
}

func TestNestedJSONTraceTextIsGuardedBeforePreviewTruncation(t *testing.T) {
	const value = "nested-trace-fixture-A\"B"
	release, err := secretvalues.Register([]byte(value))
	if err != nil {
		t.Fatal("credential guard unavailable")
	}
	defer release()
	payload, _ := json.Marshal(map[string]string{"detail": value})
	for range 3 {
		payload, _ = json.Marshal(map[string]string{"nested": string(payload)})
	}
	for _, maximum := range []int{64, 4096} {
		preview := (Redactor{}).RedactText(string(payload), maximum)
		if strings.Contains(preview, "nested-trace-fixture") {
			t.Fatal("nested JSON trace preview exposed credential content")
		}
	}
}
