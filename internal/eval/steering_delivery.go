package eval

import (
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func validSteeringDeliveries(proof *SteeringEvidence, trial Trial, workspace string) bool {
	if len(proof.Outbox) == 0 || len(proof.Outbox) > 128 || len(proof.OutboxNames) != len(proof.Outbox) ||
		!slices.IsSorted(proof.OutboxNames) {
		return false
	}
	finals := map[string]Event{}
	for _, event := range trial.Events {
		if event.Kind == "final" {
			if finals[event.RequestID].ID != "" {
				return false
			}
			finals[event.RequestID] = event
		}
	}
	if len(finals) != 2 {
		return false
	}
	total, seen := 0, map[string]bool{}
	for index, row := range proof.Outbox {
		if row.File != proof.OutboxNames[index] || seen[row.File] || !strings.HasSuffix(row.File, ".json") ||
			len(row.JSON) > MaxManifestBytes || row.SHA256 != digest([]byte(row.JSON)) {
			return false
		}
		seen[row.File] = true
		total += len(row.JSON)
		if total > 2*1024*1024 {
			return false
		}
		intent, err := outbox.InspectIntent([]byte(row.JSON), strings.TrimSuffix(row.File, ".json"))
		if err != nil {
			return false
		}
		message := intent.Message
		if intent.Identity.SessionKey != trial.SessionKey || message == nil || !message.Metadata.IsFinalReply() {
			continue
		}
		final := finals[message.Context.MessageID]
		if final.ID == "" || intent.Status != outbox.StatusDelivered || intent.OwnerWorkspace != workspace ||
			intent.Identity.Channel != "mintclaw" || intent.Identity.ChatID != "mintclaw:"+trial.SessionID ||
			message.Context.ClientSessionID != trial.SessionID || message.Context.Channel != "mintclaw" ||
			message.Channel != "mintclaw" || message.ChatID != intent.Identity.ChatID ||
			message.AgentID != trial.AgentID || message.SessionKey != trial.SessionKey ||
			len(intent.PlatformMessageIDs) != 1 || intent.PlatformMessageIDs[0] != final.MessageID ||
			intent.PayloadRetired || intent.Attempts != 1 {
			return false
		}
		delete(finals, message.Context.MessageID)
	}
	return len(finals) == 0
}
