package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

func assertSkillSelectionFrozenControls(t *testing.T, original Trial, item Case, report RunReport, workspace string) {
	t.Helper()
	for _, kind := range []string{
		"missing", "before", "after", "history_hash", "history_missing", "log_hash", "log_missing",
		"selection_hash", "selection_count", "selection_version", "selection_limit", "control_selected",
		"foreign_log", "duplicate_log", "missing_turn", "canonical_output", "delivery", "scope",
	} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(original)
			var trial Trial
			if json.Unmarshal(data, &trial) != nil {
				t.Fatal("clone native proof")
			}
			proof := trial.SkillSelection
			switch kind {
			case "missing":
				trial.SkillSelection = nil
			case "before":
				proof.Before.SHA256 = digest([]byte("different original skill"))
			case "after":
				proof.After.SHA256 = digest([]byte("modified skill"))
			case "history_hash":
				proof.HistorySHA256 = digest(nil)
			case "history_missing":
				proof.History = proof.History[:4]
				proof.HistorySHA256 = digest([]byte(strings.Join(proof.History, "\n")))
			case "log_hash":
				proof.LogSHA256 = digest(nil)
			case "log_missing":
				proof.Log = []string{}
				proof.LogSHA256 = digest(nil)
			case "canonical_output":
				var assistant map[string]any
				if json.Unmarshal([]byte(proof.History[3]), &assistant) != nil {
					t.Fatal("native assistant")
				}
				assistant["content"] = item.SkillSelection.Value
				row, _ := json.Marshal(assistant)
				proof.History[3] = string(row)
				proof.HistorySHA256 = digest([]byte(strings.Join(proof.History, "\n")))
			case "delivery":
				proof.Outbox = nil
			case "scope":
				trial.Events[3].TraceScopes[0].Workspace = "/foreign/workspace"
			default:
				mutateSkillSelectionLog(t, proof, trial, kind)
			}
			for _, check := range gradeSkillSelectionChecks(item, trial, report, workspace) {
				if check.ID == "native-selection" && check.Verdict == Pass {
					t.Fatal("native skill hard check accepted", kind)
				}
			}
		})
	}
	for _, kind := range []string{"missing_budget", "unknown_budget", "missing_lifecycle", "truncated_log", "private_unknown"} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(report)
			var candidate RunReport
			if json.Unmarshal(data, &candidate) != nil {
				t.Fatal("clone report")
			}
			switch kind {
			case "missing_budget":
				candidate.TargetBudget = nil
			case "unknown_budget":
				candidate.TargetBudget.Code = "receipt_unavailable"
			case "missing_lifecycle":
				candidate.TargetLifecycle = nil
			case "truncated_log":
				candidate.TargetLifecycle.LogTruncated = true
			case "private_unknown":
				candidate.PrivateSurfaces.Code = "coverage_unknown"
			}
			for _, check := range gradeSkillSelectionChecks(item, original, candidate, workspace) {
				if check.ID == "native-selection" && check.Verdict != Inconclusive {
					t.Fatal("run-wide unknown became eligible", kind, check)
				}
			}
		})
	}
}

func mutateSkillSelectionLog(t *testing.T, proof *SkillSelectionEvidence, trial Trial, kind string) {
	t.Helper()
	for index, raw := range proof.Log {
		var event map[string]any
		if json.Unmarshal([]byte(raw), &event) != nil {
			t.Fatal("native log")
		}
		request := trial.Events[2].RequestID
		if kind == "control_selected" {
			request = trial.Events[0].RequestID
		}
		if event["event_kind"] != "agent.turn.end" || event["message_id"] != request {
			continue
		}
		switch kind {
		case "selection_hash":
			event["skill_context_sha256"] = digest([]byte("different revision"))
		case "selection_count":
			event["skill_context_count"] = 0
		case "selection_version":
			event["skill_context_version"] = 2
		case "selection_limit":
			event["skill_context_code"] = "limit"
		case "control_selected":
			event["skill_context_count"] = 1
		case "foreign_log":
			event["session_key"] = "foreign-session"
		case "duplicate_log":
			proof.Log = append(proof.Log, raw)
		case "missing_turn":
			proof.Log = append(proof.Log[:index], proof.Log[index+1:]...)
		}
		if kind != "duplicate_log" && kind != "missing_turn" {
			row, _ := json.Marshal(event)
			proof.Log[index] = string(row)
		}
		proof.LogSHA256 = digest([]byte(strings.Join(proof.Log, "\n")))
		return
	}
	t.Fatal("native selected-turn log not found", kind)
}
