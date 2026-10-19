package diagnostictrace

import (
	"encoding/json"
	"testing"
)

func TestModelUsagePresencePreservesLegacyUnknownAndObservedZero(t *testing.T) {
	for _, test := range []struct {
		name, input string
		known       *bool
	}{
		{"legacy-missing", `{"status":"success"}`, nil},
		{"legacy-counts", `{"prompt_tokens":12,"response_tokens":4}`, nil},
		{"unavailable", `{"has_provider_usage":false}`, boolPointer(false)},
		{"observed-zero", `{"has_provider_usage":true}`, boolPointer(true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var payload ModelPayload
			if err := json.Unmarshal([]byte(test.input), &payload); err != nil {
				t.Fatal(err)
			}
			assertPresence := func(known *bool) {
				t.Helper()
				if (known == nil) != (test.known == nil) ||
					known != nil && *known != *test.known {
					t.Fatal("unknown, unavailable and observed-zero usage were conflated")
				}
			}
			assertPresence(payload.HasProviderUsage)
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(encoded, &fields) != nil {
				t.Fatal("encoded payload unavailable")
			}
			if _, present := fields["has_provider_usage"]; present != (test.known != nil) {
				t.Fatal("explicit unavailable usage was omitted or legacy evidence was invented")
			}
			var roundTrip ModelPayload
			if json.Unmarshal(encoded, &roundTrip) != nil {
				t.Fatal("round trip unavailable")
			}
			assertPresence(roundTrip.HasProviderUsage)
		})
	}
}

func boolPointer(value bool) *bool { return &value }
