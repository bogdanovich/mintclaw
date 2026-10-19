package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestTraceModelResponseRetainsProviderUsagePresence(t *testing.T) {
	for _, test := range []struct {
		name          string
		known         bool
		input, output int
	}{
		{"unavailable", false, 0, 0},
		{"observed-zero", true, 0, 0},
		{"observed-nonzero", true, 10252, 17},
		{"do-not-infer-from-counts", false, 12, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			record, _, ok := runtimeEventRecord(
				traceCaptureSettings{contentMode: diagnostictrace.ContentMetadataOnly},
				&activeTraceCapture{startedAt: started, turnID: "usage-turn"},
				runtimeevents.Event{
					ID: "usage-response", Kind: runtimeevents.KindAgentLLMResponse, Time: started,
					Correlation: runtimeevents.Correlation{ModelAttemptID: "usage-attempt"},
					Payload: LLMResponsePayload{
						Provider: "test", Model: "test", HasProviderUsage: test.known,
						PromptTokens: test.input, CompletionTokens: test.output,
					},
				},
			)
			if !ok || record.Kind != diagnostictrace.RecordModelResponse ||
				record.Correlation.ModelAttemptID != "usage-attempt" {
				t.Fatal("native response projection unavailable")
			}
			var payload diagnostictrace.ModelPayload
			if json.Unmarshal(record.Data, &payload) != nil || payload.HasProviderUsage == nil ||
				*payload.HasProviderUsage != test.known || payload.PromptTokens != test.input ||
				payload.ResponseTokens != test.output {
				t.Fatal("provider usage presence was dropped or inferred from counters")
			}
		})
	}
}
