package eval

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/internal/evalfixture"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func approvalDeniedEvidenceCode(trial Trial, item Case, workspace string) string {
	proof := trial.ApprovalDenied
	if proof == nil || item.ApprovalDenied == nil || item.Validate() != nil || proof.Version != Version ||
		proof.Code != "observed" || proof.Workspace != workspace ||
		!approvalFixtureMatches(proof.Before) || !approvalFixtureMatches(proof.After) ||
		!validTrialConversation(trial, trial.AgentID, workspace) ||
		!validID(proof.DenialRequestID) || proof.DenialAt < trial.StartedAt.UnixMilli() ||
		proof.ObservedAt < proof.DenialAt || !validDigest(proof.KeySHA256) {
		return "approval_denied_scope_invalid"
	}
	encoded, err := json.Marshal(proof)
	if err != nil || len(encoded) > 6*1024*1024 {
		return "approval_denied_bounds_invalid"
	}
	before, waiting := frozenQuestion(proof.Waiting)
	current, resolved := frozenQuestion(proof.Resolved)
	if !waiting || !resolved || !validApprovalDeniedRecords(proof, before, current, trial) {
		return "approval_denied_record_invalid"
	}
	inputs, prompts, finals := []Event{}, []Event{}, []Event{}
	for _, event := range trial.Events {
		switch event.Kind {
		case "input":
			inputs = append(inputs, event)
		case "approval":
			prompts = append(prompts, event)
		case "final":
			finals = append(finals, event)
		case "stream":
		default:
			return "approval_denied_event_invalid"
		}
	}
	if len(inputs) != 2 || len(prompts) != 1 || len(finals) != 1 {
		return "approval_denied_event_invalid"
	}
	prompt, final := prompts[0], finals[0]
	if inputs[0].Turn != 1 ||
		inputs[0].Content != item.Task ||
		inputs[0].RequestID != before.Origin.ExecutionContext.MessageID ||
		inputs[1].Turn != 2 ||
		inputs[1].Content != "Deny" ||
		inputs[1].RequestID != proof.DenialRequestID ||
		prompt.Turn != 1 ||
		prompt.RequestID != inputs[0].RequestID ||
		prompt.InteractionID != before.ID ||
		prompt.InteractionShortID != before.ShortID ||
		prompt.InteractionKind != "approval" ||
		prompt.Controls != bus.OutboundInteractionControlsPrompt ||
		prompt.MessageID != current.Answer.ResponseMessageID ||
		final.Turn != 2 ||
		final.RequestID != proof.DenialRequestID ||
		final.AgentID != trial.AgentID ||
		final.SessionKey != trial.SessionKey ||
		!slices.Contains(prompt.TraceScopes, events.NewTraceScope(workspace, before.Origin.TurnID)) {
		return "approval_denied_event_binding_invalid"
	}
	content, historyValid := approvalDeniedHistory(proof, trial, item, before)
	if !historyValid || !strings.HasPrefix(final.Content, content) {
		return "approval_denied_history_invalid"
	}
	if !validApprovalDeniedDeliveries(proof, trial, before, current, prompt, final, content) {
		return "approval_denied_delivery_invalid"
	}
	if !validApprovalDeniedLog(proof, trial, before) {
		return "approval_denied_execution_invalid"
	}
	return ""
}

func validApprovalDeniedRecords(
	proof *ApprovalDeniedEvidence, before, current interactions.Record, trial Trial,
) bool {
	route := before.Route
	return before.Kind == interactions.KindApproval && before.Status == interactions.StatusWaiting &&
		before.Answer == nil && before.Outcome == "" && before.ResumeTries == 0 && before.ApprovalConsumedAt == 0 &&
		before.ProtectedAnswer == nil && len(before.Questions) == 0 && before.ApprovalAction != "" &&
		before.CreatedAt >= trial.StartedAt.UnixMilli() && before.UpdatedAt <= proof.DenialAt &&
		before.ExpiresAt > proof.DenialAt && validID(before.Origin.TurnID) &&
		boundedIdentity(before.Origin.ToolCallID, 128) && before.Origin.ToolName == "write_file" &&
		before.Origin.ArgumentHash != "" && before.Origin.TaskID == "" && before.Origin.ExecutionContext != nil &&
		(before.Origin.ContinuationSessionKey == "" || before.Origin.ContinuationSessionKey == trial.SessionKey) &&
		route.AgentID == trial.AgentID && route.SessionKey == trial.SessionKey &&
		(route.RouteSessionKey == "" || route.RouteSessionKey == trial.SessionKey) &&
		route.Channel == "mintclaw" && route.ChatID == "mintclaw:"+trial.SessionID &&
		route.ChatType == "direct" && route.SenderID == "mintclaw-user" && route.AccountID == "" &&
		route.TopicID == "" && route.SpaceID == "" && route.SpaceType == "" &&
		current.ID == before.ID && current.ShortID == before.ShortID && current.CreatedAt == before.CreatedAt &&
		current.PromptDeliveryID == before.PromptDeliveryID && current.Route == route &&
		reflect.DeepEqual(current.Origin, before.Origin) && current.ApprovalAction == before.ApprovalAction &&
		current.Kind == interactions.KindApproval && current.Status == interactions.StatusResolved &&
		current.Outcome == interactions.OutcomeDenied && current.ProtectedAnswer == nil && len(current.Questions) == 0 &&
		current.Revision > before.Revision && current.ResumeTries == 1 && current.ResumeError == "" &&
		current.FailureCode == "" && current.ApprovalConsumedAt == 0 && len(current.OutcomeReceipts) == 0 &&
		current.ResolvedAt >= proof.DenialAt && current.LastResumeAt >= proof.DenialAt &&
		current.LastResumeAt <= current.ResolvedAt && current.ResolvedAt <= proof.ObservedAt &&
		current.UpdatedAt <= proof.ObservedAt && current.Answer != nil && !current.Answer.Superseded &&
		current.Answer.Protected == nil && current.Answer.Choice == "" && current.Answer.Text == "deny" &&
		len(current.Answer.Values) == 0 && len(current.Answer.Media) == 0 &&
		current.Answer.MessageID == proof.DenialRequestID && current.Answer.ReceivedAt >= proof.DenialAt &&
		current.Answer.ReceivedAt <= current.ResolvedAt
}

func approvalDeniedHistory(
	proof *ApprovalDeniedEvidence, trial Trial, item Case, record interactions.Record,
) (string, bool) {
	if len(proof.Metadata) > MaxManifestBytes || proof.MetadataSHA256 != digest([]byte(proof.Metadata)) ||
		proof.HistorySHA256 != digest([]byte(strings.Join(proof.History, "\n"))) ||
		len(proof.History) < 4 || len(proof.History) > 5 || len(proof.History) == 5 && proof.History[4] != "" {
		return "", false
	}
	snapshot, err := memory.InspectJSONLSnapshot(context.Background(), trial.SessionKey,
		[]byte(proof.Metadata), []byte(strings.Join(proof.History, "\n")))
	if err != nil || snapshot.Metadata.Skip != 0 || snapshot.Metadata.Summary != "" || snapshot.Metadata.HistoryDirty ||
		snapshot.Metadata.Count != 4 || len(snapshot.History) != 4 ||
		!slices.Equal(snapshot.Metadata.ClientSessionIDs, []string{trial.SessionID}) ||
		snapshot.Metadata.UpdatedAt.UnixMilli() > proof.ObservedAt {
		return "", false
	}
	scope, err := session.DecodeCurrentSessionScope(snapshot.Metadata.Scope)
	if err != nil || scope.AgentID != trial.AgentID || scope.Channel != "mintclaw" ||
		scope.ClientSessionID != trial.SessionID || session.BuildSessionKey(*scope) != trial.SessionKey ||
		scope.Values["chat"] != "" && scope.Values["chat"] != "direct:mintclaw:"+trial.SessionID {
		return "", false
	}
	history := snapshot.History
	if history[0].Role != "user" || history[0].Content != item.Task || len(history[0].ToolCalls) != 0 ||
		history[1].Role != "assistant" || len(history[1].ToolCalls) != 1 ||
		history[2].Role != "tool" || history[2].ToolCallID != record.Origin.ToolCallID ||
		history[2].ToolResultStatus != providers.ToolResultStatusSuccess || len(history[2].ToolCalls) != 0 ||
		history[3].Role != "assistant" || len(history[3].ToolCalls) != 0 || !boundedText(history[3].Content, MaxInputBytes) {
		return "", false
	}
	call := history[1].ToolCalls[0]
	if call.ID != record.Origin.ToolCallID || call.Name != "write_file" || len(call.Arguments) != 3 ||
		call.Arguments["path"] != evalfixture.ApprovalPath || call.Arguments["content"] != evalfixture.ApprovalAfter ||
		call.Arguments["overwrite"] != true {
		return "", false
	}
	payload, err := DecodeManifest[struct {
		InteractionID string `json:"interaction_id"`
		Outcome       string `json:"outcome"`
		Text          string `json:"text"`
	}]([]byte(history[2].Content))
	return history[3].Content, err == nil && payload.InteractionID == record.ID &&
		payload.Outcome == "denied" && payload.Text == "deny"
}

func validApprovalDeniedLog(proof *ApprovalDeniedEvidence, trial Trial, before interactions.Record) bool {
	if len(proof.Log) == 0 || len(proof.Log) > 4096 ||
		proof.LogSHA256 != digest([]byte(strings.Join(proof.Log, "\n"))) {
		return false
	}
	seen, suspended, completed, suspensionEnds := map[string]bool{}, 0, 0, 0
	for _, raw := range proof.Log {
		event, valid := decodeStopLog(raw)
		if !valid || seen[event.ID] {
			return false
		}
		seen[event.ID] = true
		if event.SessionKey != trial.SessionKey {
			continue
		}
		if event.AgentID != trial.AgentID || event.Time.Before(trial.StartedAt) ||
			event.Time.After(time.UnixMilli(proof.ObservedAt)) || event.Kind == events.KindAgentToolExecStart.String() {
			return false
		}
		if event.Kind == events.KindAgentToolExecEnd.String() {
			// Native approval suspension emits exec_end without exec_start.
			// This exact suspension marker is not an executed operation.
			if event.TraceID != "turn.tool.suspended" || event.TurnID != before.Origin.TurnID ||
				event.ToolCallID != before.Origin.ToolCallID || event.Tool != "write_file" || event.IsError ||
				event.MessageID != before.Origin.ExecutionContext.MessageID {
				return false
			}
			suspensionEnds++
		}
		if event.Kind == events.KindAgentTurnEnd.String() {
			switch event.Status {
			case "suspended":
				if event.TurnID != before.Origin.TurnID ||
					event.MessageID != before.Origin.ExecutionContext.MessageID ||
					event.LLMCalls != 1 {
					return false
				}
				suspended++
			case "completed":
				if event.TurnID == before.Origin.TurnID || event.MessageID != proof.DenialRequestID ||
					event.LLMCalls != 1 {
					return false
				}
				completed++
			default:
				return false
			}
		}
	}
	return suspended == 1 && completed == 1 && suspensionEnds == 1
}
