package eval

import (
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func validApprovalDeniedDeliveries(
	proof *ApprovalDeniedEvidence,
	trial Trial,
	before, current interactions.Record,
	prompt, final Event,
	content string,
) bool {
	if len(proof.Outbox) == 0 || len(proof.Outbox) > 128 || len(proof.Outbox) != len(proof.OutboxNames) ||
		!slices.IsSorted(proof.OutboxNames) || len(current.FinalDeliveryIDs) != 1 {
		return false
	}
	seen, total, prompts, finals := map[string]bool{}, 0, 0, 0
	for index, row := range proof.Outbox {
		if row.File != proof.OutboxNames[index] || seen[row.File] || !strings.HasSuffix(row.File, ".json") ||
			len(row.JSON) > MaxManifestBytes || row.SHA256 != digest([]byte(row.JSON)) {
			return false
		}
		seen[row.File], total = true, total+len(row.JSON)
		if total > 2*1024*1024 {
			return false
		}
		intent, err := outbox.InspectIntent([]byte(row.JSON), strings.TrimSuffix(row.File, ".json"))
		if err != nil {
			return false
		}
		message := intent.Message
		if intent.Identity.SessionKey != trial.SessionKey &&
			(message == nil || message.SessionKey != trial.SessionKey && message.Context.ClientSessionID != trial.SessionID) {
			continue
		}
		if message == nil || intent.Status != outbox.StatusDelivered || intent.OwnerWorkspace != proof.Workspace ||
			intent.Identity.Channel != "mintclaw" || intent.Identity.ChatID != "mintclaw:"+trial.SessionID ||
			intent.Identity.SessionKey != trial.SessionKey || message.AgentID != trial.AgentID ||
			message.SessionKey != trial.SessionKey ||
			message.Channel != "mintclaw" || message.ChatID != intent.Identity.ChatID ||
			intent.PayloadRetired || intent.Attempts != 1 ||
			len(intent.PlatformMessageIDs) != 1 {
			return false
		}
		switch intent.ID {
		case before.PromptDeliveryID:
			if !message.Metadata.IsApprovalPrompt() || message.Metadata.InteractionID != before.ID ||
				message.Metadata.InteractionShortID != before.ShortID || message.Metadata.RequestID != prompt.RequestID ||
				intent.PlatformMessageIDs[0] != prompt.MessageID {
				return false
			}
			prompts++
		case current.FinalDeliveryIDs[0]:
			if !message.Metadata.IsFinalReply() || !message.Metadata.IsFinal() ||
				message.Context.ClientSessionID != trial.SessionID || message.Context.Channel != "mintclaw" ||
				message.Metadata.InteractionControls != bus.OutboundInteractionControlsRemove ||
				message.Context.MessageID != final.RequestID || intent.PlatformMessageIDs[0] != final.MessageID ||
				message.Content != content {
				return false
			}
			finals++
		default:
			return false
		}
	}
	return prompts == 1 && finals == 1
}
