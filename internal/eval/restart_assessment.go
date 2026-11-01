package eval

import (
	"context"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func gradeRestartChecks(item Case, trial Trial, report RunReport, workspace string) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	for index, check := range item.Checks {
		if check.Kind != "owned_restart" {
			continue
		}
		if len(report.TargetGenerations) < 2 || targetGenerationsVerdict(report) != Pass ||
			trial.Restart == nil || trial.Restart.Code != "observed" {
			checks[index].Code = "native_restart_unavailable"
			continue
		}
		code := restartEvidenceCode(trial, item, report, workspace)
		checks[index] = booleanCheck(check.ID, code == "", []string{"restart"})
		if code != "" {
			checks[index].Code = code
		}
	}
	return checks
}

func restartEvidenceCode(trial Trial, item Case, report RunReport, workspace string) string {
	proof := trial.Restart
	if item.Restart == nil || item.Validate() != nil || proof == nil || proof.Version != Version ||
		proof.Code != "observed" || proof.BeforeGeneration < 1 || proof.AfterGeneration != proof.BeforeGeneration+1 ||
		proof.AfterGeneration > len(report.TargetGenerations) || len(trial.Events) != 4 ||
		!validTrialConversation(trial, trial.AgentID, workspace) ||
		!validCoreFinalDeliveries(proof.Outbox, proof.OutboxNames, trial, workspace) {
		return "restart_proof_invalid"
	}
	for index, history := range []RestartHistoryEvidence{proof.Before, proof.After} {
		count := (index + 1) * 2
		if history.MetadataSHA256 != digest([]byte(history.Metadata)) || len(history.Metadata) > MaxManifestBytes ||
			history.HistorySHA256 != digest([]byte(strings.Join(history.History, "\n"))) ||
			len(strings.Join(history.History, "\n")) > 2*1024*1024 || len(history.History) != count+1 ||
			history.History[count] != "" {
			return "restart_history_invalid"
		}
		snapshot, err := memory.InspectJSONLSnapshot(context.Background(), trial.SessionKey,
			[]byte(history.Metadata), []byte(strings.Join(history.History, "\n")))
		if err != nil || snapshot.Metadata.Count != count || snapshot.Metadata.Skip != 0 ||
			snapshot.Metadata.Summary != "" || len(snapshot.History) != count ||
			!slices.Equal(snapshot.Metadata.ClientSessionIDs, []string{trial.SessionID}) {
			return "restart_history_invalid"
		}
		scope, err := session.DecodeCurrentSessionScope(snapshot.Metadata.Scope)
		if err != nil || scope.AgentID != trial.AgentID || scope.Channel != "mintclaw" ||
			scope.ClientSessionID != trial.SessionID || session.BuildSessionKey(*scope) != trial.SessionKey {
			return "restart_history_scope"
		}
		for turn := range count / 2 {
			input, final := trial.Events[2*turn], trial.Events[2*turn+1]
			wanted, generation := item.Task, proof.BeforeGeneration
			if turn == 1 {
				wanted, generation = item.Restart.Recall, proof.AfterGeneration
			}
			if input.Kind != "input" || input.Turn != turn+1 || input.Content != wanted ||
				input.Generation != generation || final.Generation != generation || final.Kind != "final" ||
				final.Turn != turn+1 || final.RequestID != input.RequestID ||
				len(final.TraceScopes) != 1 || !final.TraceScopes[0].Complete() ||
				final.TraceScopes[0].Workspace != workspace {
				return "restart_turn_binding"
			}
			user, assistant := snapshot.History[2*turn], snapshot.History[2*turn+1]
			if user.Role != "user" || user.Content != wanted || len(user.ToolCalls) != 0 || user.ToolCallID != "" ||
				assistant.Role != "assistant" || len(assistant.ToolCalls) != 0 || assistant.ToolCallID != "" ||
				!strings.HasPrefix(final.Content, assistant.Content) || assistant.Content == "" ||
				turn == 1 && strings.TrimSpace(assistant.Content) != item.Restart.Marker {
				return "restart_behavior_invalid"
			}
			content, found := coreFinalDeliveryContent(proof.Outbox, final.RequestID)
			if !found || content != assistant.Content {
				return "restart_delivery_mismatch"
			}
		}
	}
	if !slices.Equal(proof.Before.History[:2], proof.After.History[:2]) {
		return "restart_history_replayed"
	}
	bindings, valid := nativeModelBindings(trial, workspace)
	models, selected := selectNativeModels(
		report.NativeModels,
		report.Runtime,
		bindings,
		map[string]bool{trial.ID: true},
	)
	if !valid || !selected || models == nil || models.Code != "observed" || len(models.Turns) != 2 {
		return "restart_models_unavailable"
	}
	for _, turn := range models.Turns {
		if len(turn.Calls) != 1 || turn.Generation < 1 || turn.Generation > len(report.TargetGenerations) {
			return "restart_models_binding"
		}
		generation := report.TargetGenerations[turn.Generation-1]
		if generation.Models == nil ||
			!slices.ContainsFunc(generation.Models.Turns, func(candidate NativeModelTurn) bool {
				return candidate.BindingSHA256 == turn.BindingSHA256 && candidate.TraceSHA256 == turn.TraceSHA256 &&
					candidate.TraceID == turn.TraceID && candidate.Generation == turn.Generation
			}) {
			return "restart_models_binding"
		}
	}
	return ""
}
