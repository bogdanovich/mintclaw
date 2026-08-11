package mintclaw

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/utils"
)

// mintclawConn represents a single WebSocket connection.

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func parseInlineImageMedia(payload map[string]any) ([]string, error) {
	if len(payload) == 0 {
		return nil, nil
	}

	media, err := parseInlineImageValues(payload["media"])
	if err != nil {
		return nil, err
	}

	attachments, err := parseInlineImageAttachments(payload["attachments"])
	if err != nil {
		return nil, err
	}
	media = append(media, attachments...)

	return media, nil
}

func parseInlineImageValues(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	switch values := raw.(type) {
	case []any:
		media := make([]string, 0, len(values))
		for i, item := range values {
			value, err := inlineImageValue(item)
			if err != nil {
				return nil, fmt.Errorf("media[%d]: %w", i, err)
			}
			if err := validateInlineImageDataURL(value); err != nil {
				return nil, fmt.Errorf("media[%d]: %w", i, err)
			}
			media = append(media, value)
		}
		return media, nil
	case []string:
		media := make([]string, 0, len(values))
		for i, value := range values {
			value = strings.TrimSpace(value)
			if err := validateInlineImageDataURL(value); err != nil {
				return nil, fmt.Errorf("media[%d]: %w", i, err)
			}
			media = append(media, value)
		}
		return media, nil
	case string:
		value := strings.TrimSpace(values)
		if err := validateInlineImageDataURL(value); err != nil {
			return nil, err
		}
		return []string{value}, nil
	default:
		return nil, fmt.Errorf("media must be a string or array of strings")
	}
}

func parseInlineImageAttachments(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}

	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("attachments must be an array")
	}

	media := make([]string, 0, len(values))
	for i, item := range values {
		attachment, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("attachments[%d]: attachment must be an object", i)
		}

		attachmentType, _ := attachment["type"].(string)
		attachmentType = strings.ToLower(strings.TrimSpace(attachmentType))
		if attachmentType != "" && attachmentType != "image" {
			continue
		}

		value, err := inlineImageValue(attachment)
		if err != nil {
			if attachmentType == "image" {
				return nil, fmt.Errorf("attachments[%d]: %w", i, err)
			}
			continue
		}
		if !strings.HasPrefix(value, "data:") {
			continue
		}
		if err := validateInlineImageDataURL(value); err != nil {
			return nil, fmt.Errorf("attachments[%d]: %w", i, err)
		}
		media = append(media, value)
	}
	return media, nil
}

func inlineImageValue(item any) (string, error) {
	switch value := item.(type) {
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("image payload is empty")
		}
		return value, nil
	case map[string]any:
		for _, key := range []string{"url", "data_url"} {
			if raw, ok := value[key].(string); ok && strings.TrimSpace(raw) != "" {
				return strings.TrimSpace(raw), nil
			}
		}
		return "", fmt.Errorf("image payload must include url or data_url")
	default:
		return "", fmt.Errorf("image payload must be a string or object")
	}
}

func validateInlineImageDataURL(mediaURL string) error {
	if mediaURL == "" {
		return fmt.Errorf("image payload is empty")
	}
	if !strings.HasPrefix(mediaURL, "data:image/") {
		return fmt.Errorf("only inline image data URLs are supported")
	}

	header, data, found := strings.Cut(mediaURL, ",")
	if !found || strings.TrimSpace(data) == "" {
		return fmt.Errorf("image data URL is malformed")
	}
	if !strings.Contains(header, ";base64") {
		return fmt.Errorf("image data URL must be base64 encoded")
	}
	mimeType, _, _ := strings.Cut(strings.TrimPrefix(header, "data:"), ";")
	if _, ok := allowedInlineImageMIMETypes[mimeType]; !ok {
		return fmt.Errorf("unsupported image format: %s", mimeType)
	}

	data = strings.TrimSpace(data)
	if base64.StdEncoding.DecodedLen(len(data)) > config.DefaultMaxMediaSize {
		return fmt.Errorf("image exceeds %d byte limit", config.DefaultMaxMediaSize)
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return fmt.Errorf("invalid base64 image data")
	}

	return nil
}

// setContextUsagePayload adds context window usage stats to a mintclaw payload.
func setContextUsagePayload(payload map[string]any, u *bus.ContextUsage) {
	if u == nil {
		return
	}
	payload["context_usage"] = map[string]any{
		"used_tokens":         u.UsedTokens,
		"total_tokens":        u.TotalTokens,
		"history_tokens":      u.HistoryTokens,
		"compress_at_tokens":  u.CompressAtTokens,
		"summarize_at_tokens": u.SummarizeAtTokens,
		"used_percent":        u.UsedPercent,
	}
}

// setTurnUsagePayload attaches real per-turn LLM token usage to the payload.
// Input and output are kept separate (billed at different rates); total is a
// convenience sum. Omitted entirely when both counts are zero.
func setTurnUsagePayload(payload map[string]any, inputTokens, outputTokens int) {
	if inputTokens <= 0 && outputTokens <= 0 {
		return
	}
	payload[PayloadKeyUsage] = map[string]any{
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"total_tokens":  inputTokens + outputTokens,
	}
}

func setOutboundIdentityPayload(payload map[string]any, msg bus.OutboundMessage) {
	if strings.TrimSpace(msg.AgentID) != "" {
		payload[PayloadKeyAgentID] = strings.TrimSpace(msg.AgentID)
	}
	if strings.TrimSpace(msg.SessionKey) != "" {
		payload[PayloadKeySessionKey] = strings.TrimSpace(msg.SessionKey)
	}
	requestID := strings.TrimSpace(msg.Context.Raw[bus.OutboundMetadataKeyRequestID])
	if requestID == "" {
		requestID = strings.TrimSpace(msg.Context.MessageID)
	}
	if requestID == "" {
		requestID = strings.TrimSpace(msg.ReplyToMessageID)
	}
	if requestID == "" {
		requestID = strings.TrimSpace(msg.Context.ReplyToMessageID)
	}
	if requestID != "" {
		payload["request_id"] = requestID
	}
	if len(msg.TraceScopes) > 0 {
		payload[PayloadKeyTraceScopes] = msg.TraceScopes
	}
	if interactionID := strings.TrimSpace(msg.Context.Raw[PayloadKeyInteractionID]); interactionID != "" {
		payload[PayloadKeyInteractionID] = interactionID
	}
	if shortID := strings.TrimSpace(msg.Context.Raw[PayloadKeyInteractionShortID]); shortID != "" {
		payload[PayloadKeyInteractionShortID] = shortID
	}
}

func setStreamingIdentityPayload(
	payload map[string]any,
	sessionKey string,
	traceScope runtimeevents.TraceScope,
) {
	if strings.TrimSpace(sessionKey) != "" {
		payload[PayloadKeySessionKey] = strings.TrimSpace(sessionKey)
	}
	if traceScope.Complete() {
		payload[PayloadKeyTraceScopes] = []runtimeevents.TraceScope{traceScope}
	}
}

func setStreamingAgentPayload(payload map[string]any, agentID string) {
	if strings.TrimSpace(agentID) != "" {
		payload[PayloadKeyAgentID] = strings.TrimSpace(agentID)
	}
}

func setStreamingRequestPayload(payload map[string]any, requestID string) {
	if strings.TrimSpace(requestID) != "" {
		payload[bus.OutboundMetadataKeyRequestID] = strings.TrimSpace(requestID)
	}
}

func setOutboundControlPayload(payload map[string]any, metadata bus.OutboundMetadata) {
	if strings.TrimSpace(metadata.OutboundKind) != "" {
		payload[PayloadKeyOutbound] = metadata.OutboundKind
	}
	if metadata.IsFinal() {
		payload[PayloadKeyFinal] = true
	}
	if strings.TrimSpace(metadata.InteractionKind) != "" {
		payload[PayloadKeyInteraction] = metadata.InteractionKind
	}
	if strings.TrimSpace(metadata.InteractionControls) != "" {
		payload[PayloadKeyControls] = metadata.InteractionControls
	}
}

func mintclawToolCallsPayload(msg bus.OutboundMessage) ([]utils.VisibleToolCall, bool) {
	raw := strings.TrimSpace(msg.Context.Raw[PayloadKeyToolCalls])
	if raw == "" {
		return nil, false
	}

	var toolCalls []utils.VisibleToolCall
	if err := json.Unmarshal([]byte(raw), &toolCalls); err != nil || len(toolCalls) == 0 {
		return nil, false
	}
	return toolCalls, true
}
