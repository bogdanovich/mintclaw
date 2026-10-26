package eval

import (
	"context"
	"slices"
	"strings"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func finalDeliveryReceipt(row SteeringOutboxEvidence, final Event, trial Trial, workspace string) bool {
	intent, err := outbox.InspectIntent([]byte(row.JSON), strings.TrimSuffix(row.File, ".json"))
	if err != nil || row.SHA256 != digest([]byte(row.JSON)) || intent.Message == nil {
		return false
	}
	message := intent.Message
	return intent.Identity.SessionKey == trial.SessionKey && intent.OwnerWorkspace == workspace &&
		intent.Status == outbox.StatusDelivered && !intent.PayloadRetired && intent.Attempts == 1 &&
		message.Context.MessageID == final.RequestID && message.Context.ClientSessionID == trial.SessionID &&
		message.Context.Channel == "mintclaw" && message.Channel == "mintclaw" &&
		message.ChatID == "mintclaw:"+trial.SessionID && message.AgentID == trial.AgentID &&
		message.SessionKey == trial.SessionKey && message.Metadata.IsFinalReply() && message.Metadata.IsFinal() &&
		len(intent.PlatformMessageIDs) == 1 && intent.PlatformMessageIDs[0] == final.MessageID &&
		len(final.TraceScopes) == 1 && slices.Equal(message.TraceScopes, final.TraceScopes) &&
		boundedText(message.Content, MaxInputBytes) && strings.HasPrefix(final.Content, message.Content)
}

func validFinalDeliveryCensus(
	rows []SteeringOutboxEvidence, names []string, trial Trial, workspace string, expected int,
) bool {
	if !validCoreDeliveryCensus(rows, names, trial, workspace, expected) {
		return false
	}
	scoped := 0
	for _, row := range rows {
		intent, err := outbox.InspectIntent([]byte(row.JSON), strings.TrimSuffix(row.File, ".json"))
		if err != nil {
			return false
		}
		message := intent.Message
		if intent.Identity.SessionKey != trial.SessionKey &&
			(message == nil || message.Context.ClientSessionID != trial.SessionID) {
			continue
		}
		if intent.Identity.SessionKey != trial.SessionKey || message == nil ||
			!message.Metadata.IsFinalReply() || !message.Metadata.IsFinal() {
			return false
		}
		scoped++
	}
	return scoped == expected
}

func finalDeliveryEvidenceCode(trial Trial, item Case, workspace string) string {
	proof := trial.FinalDelivery
	if proof == nil || item.FinalDelivery == nil || item.Validate() != nil || proof.Version != Version ||
		proof.Code != "observed" || len(proof.Settled) != 2 ||
		proof.MetadataSHA256 != digest([]byte(proof.Metadata)) ||
		proof.HistorySHA256 != digest([]byte(strings.Join(proof.History, "\n"))) ||
		len(proof.Metadata) > MaxManifestBytes || len(strings.Join(proof.History, "\n")) > 2*1024*1024 ||
		!validTrialConversation(trial, trial.AgentID, workspace) ||
		!validFinalDeliveryCensus(proof.Outbox, proof.OutboxNames, trial, workspace, 2) {
		return "final_delivery_proof_invalid"
	}
	if code := finalDeliveryWireCode(trial, item, workspace); code != "" {
		return code
	}
	finals, code := finalDeliveryRevisions(trial, item, workspace)
	if code != "" {
		return code
	}
	snapshot, err := memory.InspectJSONLSnapshot(context.Background(), trial.SessionKey,
		[]byte(proof.Metadata), []byte(strings.Join(proof.History, "\n")))
	if err != nil || snapshot.Metadata.Skip != 0 || snapshot.Metadata.Summary != "" ||
		snapshot.Metadata.Count != 4 || len(snapshot.History) != 4 ||
		!slices.Equal(snapshot.Metadata.ClientSessionIDs, []string{trial.SessionID}) {
		return "final_delivery_history_invalid"
	}
	scope, err := session.DecodeCurrentSessionScope(snapshot.Metadata.Scope)
	if err != nil || scope.AgentID != trial.AgentID || scope.Channel != "mintclaw" ||
		scope.ClientSessionID != trial.SessionID || session.BuildSessionKey(*scope) != trial.SessionKey ||
		scope.Values["chat"] != "" && scope.Values["chat"] != "direct:mintclaw:"+trial.SessionID {
		return "final_delivery_history_scope"
	}
	inputs := []string{item.Task, item.Driver.Steps[0].Action.Text}
	for index, final := range finals {
		user, assistant := snapshot.History[2*index], snapshot.History[2*index+1]
		if user.Role != "user" || user.Content != inputs[index] || len(user.ToolCalls) != 0 ||
			assistant.Role != "assistant" || len(assistant.ToolCalls) != 0 || assistant.Content == "" ||
			user.ToolCallID != "" || assistant.ToolCallID != "" ||
			index == 1 && !strings.Contains(assistant.Content, item.FinalDelivery.Marker) {
			return "final_delivery_history_content"
		}
		row := proof.Settled[index]
		if !slices.Contains(proof.OutboxNames, row.File) || !finalDeliveryReceipt(row, final, trial, workspace) {
			return "final_delivery_settlement_invalid"
		}
		found := false
		for _, retained := range proof.Outbox {
			if retained.File == row.File {
				found = retained == row
			}
		}
		content, matched := coreFinalDeliveryContent([]SteeringOutboxEvidence{row}, final.RequestID)
		if !found || !matched || content != assistant.Content {
			return "final_delivery_canonical_mismatch"
		}
	}
	return ""
}

func finalDeliveryRevisions(trial Trial, item Case, workspace string) ([]Event, string) {
	inputs := []string{item.Task, item.Driver.Steps[0].Action.Text}
	phase, revisions, streams := -1, 0, 0
	request, message, trace, priorRequest, priorMessage, priorTrace := "", "", "", "", "", ""
	finals := []Event{}
	for _, event := range trial.Events {
		if event.Kind == "input" {
			if phase >= 0 && len(finals) != phase+1 || phase >= 1 {
				return nil, "final_delivery_input_order"
			}
			phase++
			if event.Content != inputs[phase] || !validID(event.RequestID) || event.RequestID == priorRequest ||
				event.Turn != phase+1 || event.WireType != "" {
				return nil, "final_delivery_input_mismatch"
			}
			priorRequest, request, message, trace, revisions, streams = event.RequestID, event.RequestID, "", "", 0, 0
			continue
		}
		if phase < 0 || len(finals) > phase || event.Turn != phase+1 || event.RequestID != request ||
			event.Kind != "stream" && event.Kind != "final" || event.AgentID != trial.AgentID ||
			event.SessionKey != trial.SessionKey || !validID(event.MessageID) ||
			len(event.TraceScopes) != 1 || !event.TraceScopes[0].Complete() ||
			event.TraceScopes[0].Workspace != workspace || event.TraceScopes[0].TurnID == priorTrace {
			return nil, "final_delivery_revision_scope"
		}
		if revisions == 0 {
			if event.WireType != protocol.TypeMessageCreate || event.MessageID == priorMessage {
				return nil, "final_delivery_missing_create"
			}
			message = event.MessageID
			trace = event.TraceScopes[0].TurnID
		} else if event.WireType != protocol.TypeMessageUpdate || event.MessageID != message ||
			event.TraceScopes[0].TurnID != trace {
			return nil, "final_delivery_revision_identity"
		}
		revisions++
		if event.Kind == "stream" {
			if !item.FinalDelivery.Streaming {
				return nil, "final_delivery_unexpected_stream"
			}
			streams++
		} else {
			if phase == 0 && item.FinalDelivery.Streaming && streams == 0 {
				return nil, "final_delivery_stream_unavailable"
			}
			finals = append(finals, event)
			priorMessage, priorTrace = message, event.TraceScopes[0].TurnID
		}
	}
	if len(finals) != 2 {
		return nil, "final_delivery_terminal_missing"
	}
	return finals, ""
}
