package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

func assertIsolationFrozenControls(t *testing.T, original Trial, item Case, report RunReport, workspace string) {
	t.Helper()
	for _, control := range []struct {
		name string
		edit func(*Trial)
	}{
		{"missing_peer", func(trial *Trial) { trial.Isolation = nil }},
		{"missing_peer_proof", func(trial *Trial) { trial.Isolation.Peer.Steering = nil }},
		{"same_client_session", func(trial *Trial) { trial.Isolation.Peer.SessionID = trial.SessionID }},
		{"same_resolved_scope", func(trial *Trial) { trial.Isolation.Peer.SessionKey = trial.SessionKey }},
		{"not_overlapping", func(trial *Trial) {
			proof := trial.Isolation.Peer.Steering
			start := trial.Steering.Fixture.ReleaseSequence + 1
			proof.Fixture.EnteredSequence = start
			proof.Fixture.ReleaseSequence, proof.Fixture.SettledSequence = start+1, start+2
			proof.Guidance[0].Held.EnteredSequence = start
		}},
		{"foreign_final", func(trial *Trial) {
			for index := range trial.Events {
				if trial.Events[index].Kind == "final" {
					trial.Events[index].Content += " " + item.Steering.Peer.Update
				}
			}
		}},
		{"foreign_history", func(trial *Trial) {
			for index, line := range trial.Steering.History {
				var message map[string]any
				if json.Unmarshal([]byte(line), &message) == nil && message["role"] == "assistant" {
					message["content"] = item.Steering.Updates[0] + " " + item.Steering.Peer.Update
					data, _ := json.Marshal(message)
					trial.Steering.History[index] = string(data)
				}
			}
			trial.Steering.HistorySHA256 = digest([]byte(strings.Join(trial.Steering.History, "\n")))
		}},
		{"swapped_trace", func(trial *Trial) {
			trial.Isolation.Peer.Steering.Trace = trial.Steering.Trace
			trial.Isolation.Peer.Steering.TraceSHA256 = trial.Steering.TraceSHA256
		}},
		{"different_complete_census", func(trial *Trial) {
			trial.Isolation.Peer.Steering.Outbox = nil
			trial.Isolation.Peer.Steering.OutboxNames = nil
		}},
	} {
		t.Run(control.name, func(t *testing.T) {
			data, err := json.Marshal(original)
			var trial Trial
			if err != nil || json.Unmarshal(data, &trial) != nil {
				t.Fatal("clone two-scope native evidence", err)
			}
			control.edit(&trial)
			if validSteeringEvidence(trial, item, workspace, report.ActiveTool.Profile, report.Runtime) {
				t.Fatal("contradictory or missing peer evidence passed")
			}
			for index, check := range item.Checks {
				if check.Kind != "steering_boundary" {
					continue
				}
				checks := gradeSteeringChecks(item, trial, report, workspace)
				if checks[index].Verdict == Pass {
					t.Fatal("stored passing judge overrode missing or contradictory peer proof")
				}
			}
		})
	}
}
