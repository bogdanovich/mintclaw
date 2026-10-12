package secretvalues

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNestedJSONStringFieldsAreGuardedWithoutLosingIntegerPrecision(t *testing.T) {
	const value = "nested-record-fixture-A\"B"
	release, err := Register([]byte(value))
	if err != nil {
		t.Fatal("credential registration unavailable")
	}
	defer release()
	for layers := 1; layers <= 4; layers++ {
		payload, err := json.Marshal(map[string]string{"detail": value})
		if err != nil {
			t.Fatal("nested record fixture unavailable")
		}
		for range layers - 1 {
			payload, _ = json.Marshal(map[string]string{"nested": string(payload)})
		}
		record, _ := json.Marshal(map[string]any{
			"alias": "ebay", "response": string(payload), "sequence": json.Number("9007199254740993"),
		})
		guarded := RedactRecord(record)
		var root map[string]json.RawMessage
		if json.Unmarshal(guarded, &root) != nil || string(root["sequence"]) != "9007199254740993" {
			t.Fatal("record guard damaged JSON or integer precision")
		}
		var body string
		if json.Unmarshal(root["response"], &body) != nil {
			t.Fatal("nested response envelope invalid")
		}
		for depth := 0; depth < layers; depth++ {
			var nested map[string]string
			if json.Unmarshal([]byte(body), &nested) != nil {
				t.Fatal("guarded nested JSON is invalid")
			}
			if depth == layers-1 {
				if nested["detail"] != Marker {
					t.Fatal("nested JSON response exposed a registered value")
				}
			} else {
				body = nested["nested"]
			}
		}
	}
}

func TestUninspectedOverDepthLogPayloadIsSuppressed(t *testing.T) {
	release, err := Register([]byte("depth-bounded-fixture"))
	if err != nil {
		t.Fatal("credential registration unavailable")
	}
	defer release()
	payload := `{"detail":"depth-bounded-fixture"}`
	for range 20 {
		encoded, _ := json.Marshal(map[string]string{"nested": payload})
		payload = string(encoded)
	}
	guarded := RedactRecord([]byte(payload))
	if !json.Valid(guarded) || !strings.Contains(string(guarded), "REDACTED") &&
		!strings.Contains(string(guarded), "log record redacted") {
		t.Fatal("over-depth record was not suppressed safely")
	}
}
