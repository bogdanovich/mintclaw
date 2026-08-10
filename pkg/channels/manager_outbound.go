package channels

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func outboundMessageChannel(msg bus.OutboundMessage) string {
	return msg.Context.Channel
}

func outboundMessageChatID(msg bus.OutboundMessage) string {
	return msg.ChatID
}

func outboundMessageIsToolFeedback(msg bus.OutboundMessage) bool {
	return bus.OutboundMetadataFromMessage(msg).IsToolFeedback()
}

func outboundMessageIsToolCalls(msg bus.OutboundMessage) bool {
	return bus.OutboundMetadataFromMessage(msg).IsToolCalls()
}

func outboundMessageHasAuxiliaryKind(msg bus.OutboundMessage) bool {
	return bus.OutboundMetadataFromMessage(msg).HasAuxiliaryKind()
}

func outboundMessageIsFinal(msg bus.OutboundMessage) bool {
	return bus.OutboundMetadataFromMessage(msg).IsFinal()
}

func outboundMessageBypassesPlaceholderEdit(msg bus.OutboundMessage) bool {
	return bus.OutboundMetadataFromMessage(msg).BypassesPlaceholderEdit()
}

func outboundMessageEditPayload(msg bus.OutboundMessage, content string) map[string]any {
	payload := map[string]any{
		"content": content,
	}
	metadata := bus.OutboundMetadataFromMessage(msg)
	if modelName := metadata.ModelName; modelName != "" {
		payload["model_name"] = modelName
	}
	return payload
}

func (m *Manager) decorateOutboundResponseFooter(msg bus.OutboundMessage) bus.OutboundMessage {
	if m == nil || !m.lifecycle.responseFooterEnabled() {
		return msg
	}
	if !outboundMessageIsFinal(msg) || outboundMessageIsToolFeedback(msg) || outboundMessageIsToolCalls(msg) {
		return msg
	}
	footer := outboundResponseFooter(msg)
	if footer == "" {
		return msg
	}
	msg.Content = appendOutboundResponseFooter(
		msg.Content,
		footer,
		outboundMessageChannel(msg),
	)
	return msg
}

func appendOutboundResponseFooter(content, footer, channel string) string {
	trimmed := strings.TrimRight(content, " \t\r\n")
	if trimmed == "" || strings.HasSuffix(trimmed, footer) {
		return content
	}

	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "telegram":
		footer = `<a name="mintclaw-response-footer"></a><sub>` + footer + "</sub>"
	case "discord":
		footer = "-# " + footer
	}
	return trimmed + "\n\n" + footer
}

func outboundResponseFooter(msg bus.OutboundMessage) string {
	var parts []string
	metadata := bus.OutboundMetadataFromMessage(msg)
	modelName := metadata.ModelName
	defaultModelName := metadata.DefaultModelName
	if modelName != "" && defaultModelName != "" && modelName != defaultModelName {
		parts = append(parts, "model: "+modelName)
	}

	inputTokens := metadata.UsageInputTokens
	outputTokens := metadata.UsageOutputTokens
	totalTokens := metadata.UsageTotalTokens
	if inputTokens > 0 || outputTokens > 0 {
		parts = append(
			parts,
			fmt.Sprintf(
				"tokens: in %s, out %s",
				formatFooterTokenCount(inputTokens),
				formatFooterTokenCount(outputTokens),
			),
		)
	} else if totalTokens > 0 {
		parts = append(parts, fmt.Sprintf("tokens: total %s", formatFooterTokenCount(totalTokens)))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func formatFooterTokenCount(tokens int) string {
	if tokens < 1000 {
		return strconv.Itoa(tokens)
	}
	if tokens < 1_000_000 {
		return formatFooterTokenDecimal(float64(tokens)/1000, "k")
	}
	return formatFooterTokenDecimal(float64(tokens)/1_000_000, "m")
}

func formatFooterTokenDecimal(value float64, suffix string) string {
	truncated := math.Trunc(value*10) / 10
	formatted := strconv.FormatFloat(truncated, 'f', 1, 64)
	formatted = strings.TrimSuffix(formatted, ".0")
	return formatted + suffix
}

func outboundMediaChannel(msg bus.OutboundMediaMessage) string {
	return msg.Context.Channel
}

func outboundMediaChatID(msg bus.OutboundMediaMessage) string {
	return msg.ChatID
}

func candidateChatIDs(raw, resolved string) []string {
	raw = strings.TrimSpace(raw)
	resolved = strings.TrimSpace(resolved)
	if raw == "" || raw == resolved {
		return []string{resolved}
	}
	return []string{resolved, raw}
}

func resolveOutboundChatID(ch Channel, chatID string, outboundCtx *bus.InboundContext) string {
	if resolver, ok := ch.(outboundTargetResolver); ok {
		if resolved := strings.TrimSpace(resolver.ResolveOutboundChatID(chatID, outboundCtx)); resolved != "" {
			return resolved
		}
	}
	return strings.TrimSpace(chatID)
}

func traceScopedDeliveryKey(base string, traceScope runtimeevents.TraceScope) (string, bool) {
	traceScope = runtimeevents.NewTraceScope(traceScope.Workspace, traceScope.TurnID)
	if !traceScope.Complete() {
		return base, false
	}
	return base + "\x00turn\x00" + traceScope.Workspace + "\x00" + traceScope.TurnID, true
}

func primaryTraceScope(scopes []runtimeevents.TraceScope) runtimeevents.TraceScope {
	normalized, err := bus.NormalizeTraceScopes(scopes)
	if err != nil || len(normalized) == 0 {
		return runtimeevents.TraceScope{}
	}
	return normalized[0]
}

func streamSuppressionKey(
	channel, chatID, sessionKey string,
	traceScope runtimeevents.TraceScope,
) string {
	key := streamSuppressionBaseKey(channel, chatID, sessionKey)
	key, _ = traceScopedDeliveryKey(key, traceScope)
	return key
}

func streamSuppressionBaseKey(channel, chatID, sessionKey string) string {
	key := channel + ":" + chatID
	if strings.TrimSpace(sessionKey) != "" {
		key += ":" + sessionKey
	}
	return key
}
