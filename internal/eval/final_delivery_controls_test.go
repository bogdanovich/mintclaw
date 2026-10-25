package eval

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func assertFinalDeliveryFrozenControls(t *testing.T, original Trial, item Case, report RunReport, workspace string) {
	t.Helper()
	clone := func() Trial {
		data, _ := json.Marshal(original)
		var trial Trial
		if json.Unmarshal(data, &trial) != nil {
			t.Fatal("clone native delivery proof")
		}
		return trial
	}
	// Only the revision parser is synthesized here. This is not a claim that
	// the native gateway streamed: the unchanged native CLI campaign says not.
	t.Run("synthetic-stream-revision-parser", func(t *testing.T) {
		trial := clone()
		definition, _ := json.Marshal(item)
		var streamed Case
		if json.Unmarshal(definition, &streamed) != nil {
			t.Fatal("clone definition")
		}
		streamed.FinalDelivery.Streaming = true
		first := slices.IndexFunc(trial.Events, func(event Event) bool { return event.Kind == "final" })
		chunk := trial.Events[first]
		chunk.ID, chunk.Kind, chunk.Usage, chunk.Content = "synthetic-stream-event", "stream", nil, "Synthetic"
		trial.Events[first].WireType = protocol.TypeMessageUpdate
		trial.Events = slices.Insert(trial.Events, first, chunk)
		if code := finalDeliveryEvidenceCode(trial, streamed, workspace); code != "" {
			t.Fatal("same-message revision rejected", code)
		}
		trial.Events[first+1].MessageID = "second-physical-message"
		if finalDeliveryEvidenceCode(trial, streamed, workspace) == "" {
			t.Fatal("stream replaced by second final message passed")
		}
	})
	for _, kind := range []string{
		"missing", "settlement", "census", "extra_intent", "history_digest", "history_missing",
		"foreign_session", "duplicate_final", "missing_create", "lost_wire_type", "wrong_content",
		"wrong_platform", "wrong_trace", "pending_delivery", "retired_payload", "multiple_attempts",
	} {
		t.Run(kind, func(t *testing.T) {
			trial := clone()
			proof := trial.FinalDelivery
			switch kind {
			case "missing":
				trial.FinalDelivery = nil
			case "settlement":
				proof.Settled = proof.Settled[:1]
			case "census":
				proof.OutboxNames = proof.OutboxNames[:1]
			case "extra_intent":
				proof.Outbox = append(proof.Outbox, proof.Outbox[0])
				proof.OutboxNames = append(proof.OutboxNames, proof.OutboxNames[0])
			case "history_digest":
				proof.HistorySHA256 = digest(nil)
			case "history_missing":
				proof.History = proof.History[1:]
				proof.HistorySHA256 = digest([]byte(strings.Join(proof.History, "\n")))
			case "foreign_session":
				trial.SessionID = "foreign-session"
			case "duplicate_final", "missing_create", "lost_wire_type":
				first := slices.IndexFunc(trial.Events, func(event Event) bool { return event.Kind == "final" })
				switch kind {
				case "duplicate_final":
					duplicate := trial.Events[first]
					duplicate.ID = "duplicate-final-event"
					trial.Events = slices.Insert(trial.Events, first+1, duplicate)
				case "missing_create":
					trial.Events[first].WireType = protocol.TypeMessageUpdate
				default:
					trial.Events[first].WireType = ""
				}
			default:
				own := slices.IndexFunc(proof.Outbox, func(row SteeringOutboxEvidence) bool {
					return row.File == proof.Settled[0].File
				})
				if own < 0 {
					t.Fatal("own native delivery receipt missing")
				}
				row := &proof.Outbox[own]
				intent, err := outbox.InspectIntent([]byte(row.JSON), strings.TrimSuffix(row.File, ".json"))
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "wrong_content":
					intent.Message.Content = "Unrelated canonical text."
				case "wrong_platform":
					intent.PlatformMessageIDs = []string{"wrong-message"}
				case "wrong_trace":
					intent.Message.TraceScopes[0].TurnID = "foreign-turn"
				case "pending_delivery":
					intent.Status = outbox.StatusPending
				case "retired_payload":
					intent.PayloadRetired = true
				case "multiple_attempts":
					intent.Attempts = 2
				}
				data, _ := json.Marshal(intent)
				row.JSON, row.SHA256 = string(data), digest(data)
				for index := range proof.Settled {
					if proof.Settled[index].File == row.File {
						proof.Settled[index] = *row
					}
				}
			}
			if finalDeliveryEvidenceCode(trial, item, workspace) == "" {
				t.Fatal("contradictory/missing native delivery proof passed")
			}
			for index, check := range item.Checks {
				if check.Kind == "final_delivery" &&
					gradeFinalDeliveryChecks(item, trial, report, workspace)[index].Verdict == Pass {
					t.Fatal("passing judge overrode hard delivery check")
				}
			}
		})
	}
	for _, kind := range []string{"missing_budget", "unknown_budget", "missing_lifecycle", "truncated_log", "private_unknown"} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(report)
			var broken RunReport
			if json.Unmarshal(data, &broken) != nil {
				t.Fatal("clone run")
			}
			switch kind {
			case "missing_budget":
				broken.TargetBudget = nil
			case "unknown_budget":
				broken.TargetBudget.Code = "unavailable"
			case "missing_lifecycle":
				broken.TargetLifecycle = nil
			case "truncated_log":
				broken.TargetLifecycle.LogTruncated = true
			case "private_unknown":
				broken.PrivateSurfaces.Code = "unavailable"
			}
			trial := clone()
			trial.Judge = nil
			assessCoreConversation(t.Context(), nil, item, &trial, broken, workspace)
			if trial.Verdict == Pass || trial.EvidenceEligible || trial.Judge != nil {
				t.Fatal("missing global proof reached judging")
			}
		})
	}
}
