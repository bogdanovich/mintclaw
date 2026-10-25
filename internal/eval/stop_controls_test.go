package eval

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/events"
)

func assertNativeStopFrozenControls(t *testing.T, original Trial, item Case, report RunReport, workspace string) {
	t.Helper()
	for _, kind := range []string{
		"missing", "deadline", "closed", "released", "release_sequence", "stop_is_primary", "foreign_scope",
		"stale_observation", "bad_log_hash", "missing_abort", "not_aborted", "extra_abort_call",
		"reuse_not_completed", "extra_reuse_call", "wrong_owner_request", "history_digest", "queued_in_history",
		"unpaired_result", "extra_final", "missing_delivery", "delivery_hash", "footer_only_value",
	} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(original)
			var trial Trial
			if json.Unmarshal(data, &trial) != nil {
				t.Fatal("clone stop evidence")
			}
			proof := trial.NativeStop
			switch kind {
			case "missing":
				trial.NativeStop = nil
			case "deadline":
				proof.Fixture.Cancellation = "deadline_exceeded"
			case "closed", "released":
				proof.Fixture.State = kind
			case "release_sequence":
				proof.Fixture.ReleaseSequence = 2
			case "stop_is_primary":
				proof.StopRequestID = proof.PrimaryRequestID
			case "foreign_scope":
				trial.SessionKey = "foreign-scope"
			case "stale_observation":
				proof.AbortObservedAt = trial.StartedAt.Add(-1)
			case "bad_log_hash":
				proof.LogSHA256 = digest([]byte("wrong log"))
			case "missing_abort", "not_aborted", "extra_abort_call", "reuse_not_completed", "extra_reuse_call",
				"wrong_owner_request":
				lines := []string{}
				for _, raw := range proof.Log {
					event, valid := decodeStopLog(raw)
					if !valid {
						t.Fatal("invalid positive log")
					}
					if event.Kind == events.KindAgentTurnEnd.String() {
						if event.Status == "aborted" {
							switch kind {
							case "missing_abort":
								continue
							case "not_aborted":
								event.Status = "completed"
							case "extra_abort_call":
								event.LLMCalls++
							case "wrong_owner_request":
								event.MessageID = proof.StopRequestID
							}
						} else {
							if kind == "reuse_not_completed" {
								event.Status = "error"
							}
							if kind == "extra_reuse_call" {
								event.LLMCalls++
							}
						}
						encoded, _ := json.Marshal(event)
						raw = string(encoded)
					}
					lines = append(lines, raw)
				}
				proof.Log = lines
				proof.LogSHA256 = digest([]byte(strings.Join(lines, "\n")))
			case "history_digest":
				proof.HistorySHA256 = digest([]byte("wrong history"))
			case "queued_in_history":
				var message map[string]any
				_ = json.Unmarshal([]byte(proof.History[1]), &message)
				message["content"] = item.NativeStop.QueuedMarker
				encoded, _ := json.Marshal(message)
				proof.History[1] = string(encoded)
				proof.HistorySHA256 = digest([]byte(strings.Join(proof.History, "\n")))
			case "unpaired_result":
				var message map[string]any
				_ = json.Unmarshal([]byte(proof.History[2]), &message)
				message["tool_call_id"] = "foreign-call"
				encoded, _ := json.Marshal(message)
				proof.History[2] = string(encoded)
				proof.HistorySHA256 = digest([]byte(strings.Join(proof.History, "\n")))
			case "extra_final":
				extra := trial.Events[len(trial.Events)-1]
				extra.ID = "extra-final"
				trial.Events = append(trial.Events, extra)
			case "missing_delivery":
				proof.Outbox = proof.Outbox[:1]
				proof.OutboxNames = proof.OutboxNames[:1]
			case "delivery_hash":
				proof.Outbox[0].SHA256 = digest([]byte("different delivery"))
			case "footer_only_value":
				var message map[string]any
				_ = json.Unmarshal([]byte(proof.History[4]), &message)
				message["content"] = "wrong-native-reply"
				encoded, _ := json.Marshal(message)
				proof.History[4] = string(encoded)
				proof.HistorySHA256 = digest([]byte(strings.Join(proof.History, "\n")))
				for index, row := range proof.Outbox {
					var record map[string]any
					_ = json.Unmarshal([]byte(row.JSON), &record)
					body := record["message"].(map[string]any)
					ctx := body["context"].(map[string]any)
					if ctx["message_id"] == lastOutcome(trial.Events).RequestID {
						body["content"] = "wrong-native-reply"
						encoded, _ := json.Marshal(record)
						proof.Outbox[index].JSON = string(encoded)
						proof.Outbox[index].SHA256 = digest(encoded)
					}
				}
			}
			if validNativeStopEvidence(trial, item, workspace, report.ActiveTool.Profile) {
				t.Fatal("missing or contradictory native stop evidence passed", kind)
			}
			trial.Judge = nil
			assessCoreConversation(t.Context(), nil, item, &trial, report, workspace)
			if trial.Verdict == Pass || trial.EvidenceEligible || trial.Judge != nil {
				t.Fatal("failed stop proof reached judging")
			}
		})
	}
	for _, kind := range []string{
		"missing_budget", "unknown_budget", "missing_lifecycle", "truncated_log", "private_unknown", "host_unknown",
	} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(report)
			var broken RunReport
			if json.Unmarshal(data, &broken) != nil {
				t.Fatal("clone run proof")
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
			case "host_unknown":
				broken.ActiveTool.Code = "shutdown_unknown"
			}
			trial := original
			trial.Judge = nil
			assessCoreConversation(t.Context(), nil, item, &trial, broken, workspace)
			if trial.Verdict == Pass || trial.EvidenceEligible || trial.Judge != nil {
				t.Fatal("missing owned/global evidence reached judging")
			}
		})
	}
}
