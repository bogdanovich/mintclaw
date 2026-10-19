package eval

import (
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func browserPermissionPrompt(trial Trial, proof *BrowserDenialEvidence) (Event, bool) {
	if proof == nil || proof.Cancellation == nil || !validDigest(proof.PermissionPromptSHA256) {
		return Event{}, false
	}
	var prompt Event
	for _, event := range trial.Events {
		if event.Kind != "question" || event.MessageID != proof.Cancellation.PromptMessageID {
			continue
		}
		if prompt.ID != "" || event.RequestID != proof.Cancellation.PromptRequestID ||
			event.AgentID != trial.AgentID || event.SessionKey != trial.SessionKey ||
			digest([]byte(event.Content)) != proof.PermissionPromptSHA256 {
			return Event{}, false
		}
		prompt = event
	}
	return prompt, prompt.ID != ""
}

func observedBrowserPermissionPrompt(
	trial Trial, proof *BrowserDenialEvidence, intent outbox.Intent, target admittedTarget,
) bool {
	prompt, found := browserPermissionPrompt(trial, proof)
	return found &&
		observedBrowserQuestionPrompt(
			prompt,
			proof.Cancellation.InteractionID,
			proof.Cancellation.ShortID,
			intent,
			target,
		)
}

func observedBrowserQuestionPrompt(
	prompt Event,
	interactionID, shortID string,
	intent outbox.Intent,
	target admittedTarget,
) bool {
	return observedBrowserInteractionPrompt(prompt, interactionID, shortID, "question", intent, target)
}

func observedBrowserInteractionPrompt(
	prompt Event, interactionID, shortID, kind string, intent outbox.Intent, target admittedTarget,
) bool {
	if intent.Message == nil || len(intent.PlatformMessageIDs) != 1 ||
		intent.PlatformMessageIDs[0] != prompt.MessageID {
		return false
	}
	message := intent.Message
	request := message.Metadata.RequestID
	if request == "" {
		request = message.Context.MessageID
	}
	if request != prompt.RequestID || message.Context.MessageID != "" && message.Context.MessageID != prompt.RequestID {
		return false
	}
	projected := channels.DecorateOutboundResponseFooter(
		*message,
		target.config.Agents.Defaults.IsResponseFooterEnabled(),
	)
	return projected.Content == prompt.Content && slices.Equal(message.TraceScopes, prompt.TraceScopes) &&
		message.Metadata.InteractionID == interactionID &&
		message.Metadata.InteractionShortID == shortID && message.Metadata.InteractionKind == kind &&
		message.Metadata.InteractionControls == bus.OutboundInteractionControlsPrompt
}
