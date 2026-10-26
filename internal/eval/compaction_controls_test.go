package eval

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/events"
)

func assertCompactionFrozenControls(t *testing.T, original Trial, item Case, report RunReport, workspace string) {
	t.Helper()
	for _, kind := range []string{
		"missing", "profile", "barrier_hash", "log_hash", "missing_pair", "no_progress",
		"interrupted", "wrong_frontier", "foreign_session", "late_compaction", "canonical_old", "scope", "delivery", "history_hash",
	} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(original)
			var trial Trial
			if json.Unmarshal(data, &trial) != nil {
				t.Fatal("clone proof")
			}
			proof := trial.Compaction
			switch kind {
			case "missing":
				trial.Compaction = nil
			case "profile":
				proof.Profile.HistoryMaxTokens = 1
			case "barrier_hash":
				proof.BarrierSHA256 = digest(nil)
			case "log_hash":
				proof.LogSHA256 = digest(nil)
			case "late_compaction":
				proof.Barrier = append([]string{}, proof.Log...)
				proof.BarrierSHA256 = digest([]byte(strings.Join(proof.Barrier, "\n")))
			case "canonical_old":
				var assistant map[string]any
				if json.Unmarshal([]byte(proof.History[9]), &assistant) != nil {
					t.Fatal("native assistant")
				}
				assistant["content"] = item.Compaction.OldMarker
				row, _ := json.Marshal(assistant)
				proof.History[9] = string(row)
				proof.HistorySHA256 = digest([]byte(strings.Join(proof.History, "\n")))
			case "scope":
				trial.Events[9].TraceScopes[0].Workspace = "/foreign/workspace"
			case "delivery":
				proof.Outbox = nil
			case "history_hash":
				proof.HistorySHA256 = digest(nil)
			default:
				mutateCompactionReceipts(t, proof, trial.SessionKey, kind)
			}
			for _, check := range gradeCompactionChecks(item, trial, report, workspace) {
				if check.ID == "native-compaction" && check.Verdict == Pass {
					t.Fatal("invalid compaction accepted", kind)
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
			for _, check := range gradeCompactionChecks(item, original, candidate, workspace) {
				if check.ID == "native-compaction" && check.Verdict != Inconclusive {
					t.Fatal("unknown run became eligible", kind, check)
				}
			}
		})
	}
}

func mutateCompactionReceipts(t *testing.T, proof *CompactionEvidence, key, kind string) {
	t.Helper()
	mutate := func(lines []string) []string {
		result := []string{}
		matched := false
		for _, raw := range lines {
			var record compactionLogEvent
			if json.Unmarshal([]byte(raw), &record) != nil {
				t.Fatal("native receipt")
			}
			if record.SessionKey != key || record.Count != 8 ||
				record.Kind != events.KindAgentContextCompressStart.String() &&
					record.Kind != events.KindAgentContextCompressEnd.String() {
				result = append(result, raw)
				continue
			}
			matched = true
			if kind == "missing_pair" && record.Kind == events.KindAgentContextCompressStart.String() {
				continue
			}
			var value map[string]any
			if json.Unmarshal([]byte(raw), &value) != nil {
				t.Fatal("native receipt fields")
			}
			switch kind {
			case "no_progress":
				value["tokens_saved"] = 0
			case "interrupted":
				if record.Kind == events.KindAgentContextCompressEnd.String() {
					value["status"] = "interrupted"
				}
			case "wrong_frontier":
				value["transcript_count"], value["transcript_revision"] = 4, 4
			case "foreign_session":
				if record.Kind == events.KindAgentContextCompressEnd.String() {
					value["session_key"] = "foreign"
				}
			}
			row, _ := json.Marshal(value)
			result = append(result, string(row))
		}
		if !matched {
			t.Fatal("missing native frontier", kind)
		}
		return result
	}
	proof.Barrier, proof.Log = mutate(proof.Barrier), mutate(proof.Log)
	proof.BarrierSHA256 = digest([]byte(strings.Join(proof.Barrier, "\n")))
	proof.LogSHA256 = digest([]byte(strings.Join(proof.Log, "\n")))
}
