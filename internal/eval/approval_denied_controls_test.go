package eval

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func rewriteApprovalRecord(t *testing.T, encoded *string, mutate func(*interactions.Record)) {
	t.Helper()
	var record interactions.Record
	if json.Unmarshal([]byte(*encoded), &record) != nil {
		t.Fatal("decode native control record")
	}
	mutate(&record)
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	*encoded = string(raw)
}

func assertApprovalDeniedFrozenControls(t *testing.T, original Trial, item Case, report RunReport, workspace string) {
	t.Helper()
	for _, kind := range []string{
		"missing", "unobserved", "missing_key_binding", "changed_file", "extra_file", "foreign_workspace",
		"wrong_request", "question_not_approval", "cancel_not_deny", "allowed", "consumed", "superseded",
		"stale", "wrong_prompt", "changed_origin", "history_hash", "unpaired_result", "fake_result",
		"missing_history", "missing_log", "execution_started", "not_suspended", "extra_model_call", "not_resolved",
		"duplicate_final", "missing_delivery", "delivery_hash",
	} {
		t.Run(kind, func(t *testing.T) {
			raw, _ := json.Marshal(original)
			var trial Trial
			if json.Unmarshal(raw, &trial) != nil {
				t.Fatal("clone native approval evidence")
			}
			proof := trial.ApprovalDenied
			switch kind {
			case "missing":
				trial.ApprovalDenied = nil
			case "unobserved":
				proof.Code = "post_stop_evidence_unavailable"
			case "missing_key_binding":
				proof.KeySHA256 = ""
			case "changed_file":
				proof.After.SHA256 = digest([]byte("changed"))
			case "extra_file":
				proof.After.Entries = append(proof.After.Entries, "other.txt")
			case "foreign_workspace":
				proof.Workspace += "-foreign"
			case "wrong_request":
				proof.DenialRequestID = trial.Events[0].RequestID
			case "question_not_approval":
				rewriteApprovalRecord(
					t,
					&proof.Resolved,
					func(r *interactions.Record) { r.Kind = interactions.KindQuestion },
				)
			case "cancel_not_deny":
				rewriteApprovalRecord(
					t,
					&proof.Resolved,
					func(r *interactions.Record) { r.Status = interactions.StatusCancelled },
				)
			case "allowed":
				rewriteApprovalRecord(
					t,
					&proof.Resolved,
					func(r *interactions.Record) { r.Outcome = interactions.OutcomeAllowed },
				)
			case "consumed":
				rewriteApprovalRecord(
					t,
					&proof.Resolved,
					func(r *interactions.Record) { r.ApprovalConsumedAt = proof.DenialAt },
				)
			case "superseded":
				rewriteApprovalRecord(t, &proof.Resolved, func(r *interactions.Record) { r.Answer.Superseded = true })
			case "stale":
				proof.DenialAt = trial.StartedAt.Add(-1).UnixMilli()
			case "wrong_prompt":
				rewriteApprovalRecord(
					t,
					&proof.Resolved,
					func(r *interactions.Record) { r.Answer.ResponseMessageID = "foreign-prompt" },
				)
			case "changed_origin":
				rewriteApprovalRecord(
					t,
					&proof.Resolved,
					func(r *interactions.Record) { r.Origin.ToolCallID += "-foreign" },
				)
			case "not_resolved":
				rewriteApprovalRecord(
					t,
					&proof.Resolved,
					func(r *interactions.Record) { r.Status = interactions.StatusResuming },
				)
			case "history_hash":
				proof.HistorySHA256 = digest([]byte("other"))
			case "unpaired_result", "fake_result":
				var message map[string]any
				if json.Unmarshal([]byte(proof.History[2]), &message) != nil {
					t.Fatal("decode native result")
				}
				if kind == "unpaired_result" {
					message["tool_call_id"] = "foreign-call"
				} else {
					message["content"] = `{"interaction_id":"foreign","outcome":"denied","text":"deny"}`
				}
				encoded, _ := json.Marshal(message)
				proof.History[2] = string(encoded)
				proof.HistorySHA256 = digest([]byte(strings.Join(proof.History, "\n")))
			case "missing_history":
				proof.History = nil
			case "missing_log":
				proof.Log = nil
			case "not_suspended", "extra_model_call":
				for index, raw := range proof.Log {
					event, ok := decodeStopLog(raw)
					if !ok {
						t.Fatal("decode native positive log")
					}
					if kind == "not_suspended" && event.Kind == events.KindAgentToolExecEnd.String() {
						event.TraceID = "turn.tool.executed"
					}
					if kind == "extra_model_call" && event.Status == "completed" {
						event.LLMCalls++
					}
					encoded, _ := json.Marshal(event)
					proof.Log[index] = string(encoded)
				}
				proof.LogSHA256 = digest([]byte(strings.Join(proof.Log, "\n")))
			case "execution_started":
				event := activeToolLogEvent{
					ID: "forbidden-execution", Kind: events.KindAgentToolExecStart.String(),
					Time: trial.StartedAt, AgentID: trial.AgentID, SessionKey: trial.SessionKey, Tool: "write_file",
				}
				encoded, _ := json.Marshal(event)
				proof.Log = append(proof.Log, string(encoded))
				proof.LogSHA256 = digest([]byte(strings.Join(proof.Log, "\n")))
			case "duplicate_final":
				event := trial.Events[len(trial.Events)-1]
				event.ID = "duplicate-final"
				trial.Events = append(trial.Events, event)
			case "missing_delivery":
				proof.Outbox = proof.Outbox[:1]
				proof.OutboxNames = proof.OutboxNames[:1]
			case "delivery_hash":
				proof.Outbox[0].SHA256 = digest([]byte("other"))
			}
			if approvalDeniedEvidenceCode(trial, item, workspace) == "" {
				t.Fatal("invalid native denial passed")
			}
			trial.Judge = nil
			assessCoreConversation(t.Context(), nil, item, &trial, report, workspace)
			if trial.Verdict == Pass || trial.Judge != nil {
				t.Fatal("invalid denial reached judge")
			}
		})
	}
	for _, kind := range []string{"no_budget", "unknown_budget", "no_census", "truncated_log", "forced_exit"} {
		t.Run(kind, func(t *testing.T) {
			raw, _ := json.Marshal(report)
			var changed RunReport
			if json.Unmarshal(raw, &changed) != nil {
				t.Fatal("clone owned global gates")
			}
			switch kind {
			case "no_budget":
				changed.TargetBudget = nil
			case "unknown_budget":
				changed.TargetBudget.Code = "unavailable"
			case "no_census":
				changed.PrivateSurfaces = nil
			case "truncated_log":
				changed.TargetLifecycle.LogTruncated = true
			case "forced_exit":
				changed.TargetLifecycle.Forced = true
			}
			trial := original
			trial.Judge = nil
			assessCoreConversation(t.Context(), nil, item, &trial, changed, workspace)
			if trial.Verdict == Pass || trial.Judge != nil {
				t.Fatal("unknown ownership/budget reached judge")
			}
		})
	}
}
