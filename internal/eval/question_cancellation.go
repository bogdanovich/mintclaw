package eval

import "github.com/bogdanovich/mintclaw/pkg/bus"

// Regrading uses operator-owned retained evidence, never current native state.
func validQuestionCancellation(trial Trial, reply Event, workspace string) bool {
	return validInteractionCancellation(trial.Cancellation, trial, reply, workspace, "question")
}

func validInteractionCancellation(
	proof *InteractionCancellationEvidence, trial Trial, reply Event, workspace, kind string,
) bool {
	if proof == nil || proof.Version != Version || proof.AgentID != trial.AgentID ||
		proof.SessionKey != trial.SessionKey || proof.Workspace != workspace ||
		!validID(proof.InteractionID) || !boundedIdentity(proof.ShortID, 64) ||
		!validID(proof.PromptDeliveryID) || !boundedIdentity(proof.PromptMessageID, 128) ||
		!boundedIdentity(proof.ReplyMessageID, 128) || proof.ReplyMessageID != reply.MessageID ||
		!validID(proof.OriginTurnID) || !validID(proof.PromptRequestID) || !validID(proof.CancelRequestID) ||
		proof.CancelRequestID != reply.RequestID || proof.WaitingRevision <= 0 ||
		proof.TerminalRevision <= proof.WaitingRevision || proof.ResolvedAt < trial.StartedAt.UnixMilli() ||
		reply.Controls != bus.OutboundInteractionControlsRemove || reply.InteractionKind != kind {
		return false
	}
	promptMatched, inputMatched, cancellations := false, false, 0
	for _, event := range trial.Events {
		if event.Kind == "canceled" {
			cancellations++
		}
		if event.Kind == kind && event.InteractionID == proof.InteractionID &&
			event.InteractionShortID == proof.ShortID && event.MessageID == proof.PromptMessageID &&
			event.RequestID == proof.PromptRequestID && event.Turn < reply.Turn &&
			event.AgentID == proof.AgentID && event.SessionKey == proof.SessionKey {
			for _, scope := range event.TraceScopes {
				promptMatched = promptMatched || scope.Complete() && scope.Workspace == workspace &&
					scope.TurnID == proof.OriginTurnID
			}
		}
		inputMatched = inputMatched || event.Kind == "input" && event.RequestID == proof.CancelRequestID &&
			event.Turn == reply.Turn && event.Content == bus.InboundInteractionCancelLabel
	}
	return cancellations == 1 && promptMatched && inputMatched
}
