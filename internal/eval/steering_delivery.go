package eval

import (
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func validSteeringDeliveries(proof *SteeringEvidence, trial Trial, workspace string) bool {
	return validCoreFinalDeliveries(proof.Outbox, proof.OutboxNames, trial, workspace)
}

func validCoreFinalDeliveries(rows []SteeringOutboxEvidence, names []string, trial Trial, workspace string) bool {
	return validCoreDeliveryCensus(rows, names, trial, workspace, 2)
}

func validCoreDeliveryCensus(
	rows []SteeringOutboxEvidence, names []string, trial Trial, workspace string, expected int,
) bool {
	if len(rows) == 0 || len(rows) > 128 || len(names) != len(rows) || !slices.IsSorted(names) {
		return false
	}
	finals := map[string]Event{}
	for _, event := range trial.Events {
		if event.Kind == "final" || event.Kind == "stopped" {
			if finals[event.RequestID].ID != "" {
				return false
			}
			finals[event.RequestID] = event
		}
	}
	if len(finals) != expected || expected < 1 || expected > 3 {
		return false
	}
	total, seen := 0, map[string]bool{}
	for index, row := range rows {
		if row.File != names[index] || seen[row.File] || !strings.HasSuffix(row.File, ".json") ||
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
		if intent.Identity.SessionKey != trial.SessionKey || message == nil ||
			!message.Metadata.IsFinalReply() && !message.Metadata.IsFinal() {
			continue
		}
		final := finals[message.Context.MessageID]
		if final.ID == "" || (final.Kind == "final") != message.Metadata.IsFinalReply() ||
			final.Kind == "stopped" && len(message.TraceScopes) != 0 ||
			intent.Status != outbox.StatusDelivered || intent.OwnerWorkspace != workspace ||
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

// Outbox content precedes channel footer rendering; it is not wire text.
func coreFinalDeliveryContent(rows []SteeringOutboxEvidence, requestID string) (string, bool) {
	content, matched := "", false
	for _, row := range rows {
		intent, err := outbox.InspectIntent([]byte(row.JSON), strings.TrimSuffix(row.File, ".json"))
		if err != nil {
			return "", false
		}
		if intent.Message == nil || intent.Message.Context.MessageID != requestID {
			continue
		}
		if matched {
			return "", false
		}
		content, matched = intent.Message.Content, true
	}
	return content, matched
}
