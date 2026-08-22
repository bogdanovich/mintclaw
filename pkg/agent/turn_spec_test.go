package agent

import "github.com/bogdanovich/mintclaw/pkg/bus"

func testDispatchRequest(sessionKey, channel, chatID, userMessage string) DispatchRequest {
	dispatch := DispatchRequest{
		SessionKey:  sessionKey,
		UserMessage: userMessage,
	}
	if channel != "" || chatID != "" {
		dispatch.InboundContext = &bus.InboundContext{
			Channel: channel,
			ChatID:  chatID,
		}
	}
	return dispatch
}
