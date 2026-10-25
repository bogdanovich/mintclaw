package eval

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func assertSteeringFrozenControls(t *testing.T, original Trial, item Case, report RunReport, workspace string) {
	t.Helper()
	for _, control := range []struct {
		name string
		edit func(*Trial)
	}{
		{"missing", func(trial *Trial) { trial.Steering = nil }},
		{"canceled_read", func(trial *Trial) { trial.Steering.Fixture.State = "canceled" }},
		{"premature_settlement", func(trial *Trial) {
			trial.Steering.Fixture.SettledSequence = trial.Steering.Fixture.ReleaseSequence
		}},
		{"not_held", func(trial *Trial) { trial.Steering.Guidance[0].Held.ReleaseSequence = 2 }},
		{"wrong_enqueue_scope", func(trial *Trial) {
			proof := &trial.Steering.Guidance[0]
			var native map[string]any
			if json.Unmarshal([]byte(proof.Enqueue), &native) != nil {
				t.Fatal("native enqueue fixture")
			}
			native["session_key"] = "different-session"
			data, _ := json.Marshal(native)
			proof.Enqueue, proof.SHA256 = string(data), digest(data)
		}},
		{"unpaired_result", func(trial *Trial) {
			for index, line := range trial.Steering.History {
				var message map[string]any
				if json.Unmarshal([]byte(line), &message) == nil && message["role"] == "tool" {
					message["tool_call_id"] = "orphaned-call"
					data, _ := json.Marshal(message)
					trial.Steering.History[index] = string(data)
				}
			}
			trial.Steering.HistorySHA256 = digest([]byte(strings.Join(trial.Steering.History, "\n")))
		}},
		{"trace_error", func(trial *Trial) {
			trace, err := diagnostictrace.Decode([]byte(trial.Steering.Trace))
			if err != nil {
				t.Fatal(err)
			}
			trace.Outcome.Status = "error"
			trace, err = diagnostictrace.Finalize(trace)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(trace)
			trial.Steering.Trace, trial.Steering.TraceSHA256 = string(data), digest(data)
		}},
		{"wrong_mode", func(trial *Trial) { trial.Steering.Profile.SteeringMode = "anonymous" }},
		{"missing_delivery", func(trial *Trial) {
			trial.Steering.Outbox, trial.Steering.OutboxNames = nil, nil
		}},
		{"duplicate_final", func(trial *Trial) {
			for _, row := range trial.Steering.Outbox {
				intent, err := outbox.InspectIntent([]byte(row.JSON), strings.TrimSuffix(row.File, ".json"))
				if err != nil || intent.Message == nil || !intent.Message.Metadata.IsFinalReply() {
					continue
				}
				intent.Identity.Ordinal++
				id, err := outbox.DeliveryID(intent.Identity)
				if err != nil {
					t.Fatal(err)
				}
				intent.ID, intent.Message.DeliveryID = id, id
				data, _ := json.Marshal(intent)
				if _, err := outbox.InspectIntent(data, id); err != nil {
					t.Fatal("duplicate must remain a native-valid intent", err)
				}
				trial.Steering.Outbox = append(trial.Steering.Outbox,
					SteeringOutboxEvidence{File: id + ".json", JSON: string(data), SHA256: digest(data)})
				slices.SortFunc(trial.Steering.Outbox, func(a, b SteeringOutboxEvidence) int {
					return strings.Compare(a.File, b.File)
				})
				trial.Steering.OutboxNames = nil
				for _, copy := range trial.Steering.Outbox {
					trial.Steering.OutboxNames = append(trial.Steering.OutboxNames, copy.File)
				}
				return
			}
			t.Fatal("native final fixture missing")
		}},
		{"stale_final", func(trial *Trial) {
			for index := range trial.Events {
				if trial.Events[index].Kind == "final" && trial.Events[index].Turn == 1 {
					trial.Events[index].Content = item.Steering.InitialMarker
				}
			}
		}},
	} {
		t.Run(control.name, func(t *testing.T) {
			data, err := json.Marshal(original)
			var trial Trial
			if err != nil || json.Unmarshal(data, &trial) != nil {
				t.Fatal("clone native fixture", err)
			}
			control.edit(&trial)
			if validSteeringEvidence(trial, item, workspace, report.ActiveTool.Profile, report.Runtime) {
				t.Fatal("frozen native contradiction became passing")
			}
		})
	}
	for _, kind := range []string{"missing_host", "log_truncated", "host_unknown"} {
		t.Run(kind, func(t *testing.T) {
			copyReport := report
			host, lifecycle := *report.ActiveTool, *report.TargetLifecycle
			copyReport.ActiveTool, copyReport.TargetLifecycle = &host, &lifecycle
			switch kind {
			case "missing_host":
				copyReport.ActiveTool = nil
			case "log_truncated":
				lifecycle.LogTruncated = true
			case "host_unknown":
				host.Code = "shutdown_unknown"
			}
			checks := gradeSteeringChecks(item, original, copyReport, workspace)
			for index, check := range item.Checks {
				if check.Kind == "steering_boundary" && checks[index].Verdict != Inconclusive {
					t.Fatal("judge/pass trial overrode missing global native prerequisites")
				}
			}
		})
	}
}
