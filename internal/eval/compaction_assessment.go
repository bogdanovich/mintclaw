package eval

import (
	"context"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func gradeCompactionChecks(item Case, trial Trial, report RunReport, workspace string) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	for index, check := range item.Checks {
		if check.Kind != "compaction" {
			continue
		}
		if !readRecoveryOwnedReady(report) || trial.Compaction == nil || trial.Compaction.Code != "observed" {
			checks[index].Code = "native_compaction_unavailable"
			continue
		}
		code := compactionEvidenceCode(trial, item, workspace)
		checks[index] = booleanCheck(check.ID, code == "", []string{"compaction"})
		if code != "" {
			checks[index].Code = code
		}
	}
	return checks
}

func compactionEvidenceCode(trial Trial, item Case, workspace string) string {
	proof := trial.Compaction
	if item.Compaction == nil || item.Validate() != nil || proof == nil || proof.Version != Version ||
		proof.Code != "observed" || !proof.Profile.valid() || len(trial.Events) != 10 ||
		!validTrialConversation(trial, trial.AgentID, workspace) ||
		!validFinalDeliveryCensus(proof.Outbox, proof.OutboxNames, trial, workspace, 5) ||
		proof.MetadataSHA256 != digest([]byte(proof.Metadata)) || len(proof.Metadata) > MaxManifestBytes ||
		proof.HistorySHA256 != digest([]byte(strings.Join(proof.History, "\n"))) ||
		len(strings.Join(proof.History, "\n")) > 2*1024*1024 {
		return "compaction_proof_invalid"
	}
	snapshot, err := memory.InspectJSONLSnapshot(context.Background(), trial.SessionKey,
		[]byte(proof.Metadata), []byte(strings.Join(proof.History, "\n")))
	if err != nil || snapshot.Metadata.Count != 10 || snapshot.Metadata.Skip != 0 || snapshot.Metadata.Summary != "" ||
		len(snapshot.History) != 10 || !slices.Equal(snapshot.Metadata.ClientSessionIDs, []string{trial.SessionID}) {
		return "compaction_history_invalid"
	}
	scope, err := session.DecodeCurrentSessionScope(snapshot.Metadata.Scope)
	if err != nil || scope.AgentID != trial.AgentID || scope.Channel != "mintclaw" ||
		scope.ClientSessionID != trial.SessionID || session.BuildSessionKey(*scope) != trial.SessionKey {
		return "compaction_history_scope"
	}
	turns := map[string]bool{}
	for index, wanted := range []string{
		item.Task, item.Compaction.Correction,
		item.Compaction.Filler, item.Compaction.Filler, item.Compaction.Recall,
	} {
		input, final := trial.Events[2*index], trial.Events[2*index+1]
		if input.Kind != "input" || input.Turn != index+1 || input.Content != wanted || input.Generation != 0 ||
			final.Kind != "final" || final.Turn != index+1 || final.Generation != 0 || final.RequestID != input.RequestID ||
			len(final.TraceScopes) != 1 || !final.TraceScopes[0].Complete() ||
			final.TraceScopes[0].Workspace != workspace || turns[final.TraceScopes[0].TurnID] {
			return "compaction_turn_binding"
		}
		turns[final.TraceScopes[0].TurnID] = true
		user, assistant := snapshot.History[2*index], snapshot.History[2*index+1]
		if user.Role != "user" || user.Content != wanted || len(user.ToolCalls) != 0 || user.ToolCallID != "" ||
			assistant.Role != "assistant" || len(assistant.ToolCalls) != 0 || assistant.ToolCallID != "" ||
			assistant.Content == "" || !strings.HasPrefix(final.Content, assistant.Content) ||
			index == 4 && strings.TrimSpace(assistant.Content) != item.Compaction.Marker {
			return "compaction_behavior_invalid"
		}
		content, found := coreFinalDeliveryContent(proof.Outbox, final.RequestID)
		if !found || content != assistant.Content {
			return "compaction_delivery_mismatch"
		}
	}
	return compactionLogCode(proof, trial)
}
