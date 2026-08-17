package bus

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	OutboundMetadataKeyMessageKind        = "message_kind"
	OutboundMetadataKeyToolCalls          = "tool_calls"
	OutboundMetadataKeyOutboundKind       = "outbound_kind"
	OutboundMetadataKeyModelName          = "model_name"
	OutboundMetadataKeyDefaultModel       = "default_model_name"
	OutboundMetadataKeyUsageInput         = "usage_input_tokens"
	OutboundMetadataKeyUsageOutput        = "usage_output_tokens"
	OutboundMetadataKeyUsageTotal         = "usage_total_tokens"
	OutboundMetadataKeyInteraction        = "interaction_kind"
	OutboundMetadataKeyControls           = "interaction_controls"
	OutboundMetadataKeyChoices            = "interaction_choices"
	OutboundMetadataKeyInteractionID      = "interaction_id"
	OutboundMetadataKeyInteractionShortID = "interaction_short_id"
	OutboundMetadataKeyRequestID          = "request_id"

	OutboundMessageKindThought      = "thought"
	OutboundMessageKindToolFeedback = "tool_feedback"
	OutboundMessageKindToolCalls    = "tool_calls"
	OutboundMessageKindFinalReply   = "final_reply"

	OutboundKindFinal   = "final"
	OutboundKindInterim = "interim"

	OutboundInteractionApproval = "approval"
	OutboundInteractionQuestion = "question"

	OutboundInteractionControlsPrompt = "prompt"
	OutboundInteractionControlsRemove = "remove"
)

// OutboundMetadata is the typed form of the cross-package outbound metadata
// stored in InboundContext.Raw for wire/backward compatibility.
type OutboundMetadata struct {
	MessageKind            string
	ToolCalls              string
	OutboundKind           string
	ModelName              string
	DefaultModelName       string
	UsageInputTokens       int
	UsageOutputTokens      int
	UsageTotalTokens       int
	InteractionKind        string
	InteractionControls    string
	InteractionChoicesJSON string
}

func OutboundMetadataFromMessage(msg OutboundMessage) OutboundMetadata {
	return OutboundMetadataFromRaw(msg.Context.Raw)
}

func OutboundMetadataFromContext(ctx InboundContext) OutboundMetadata {
	return OutboundMetadataFromRaw(ctx.Raw)
}

func OutboundMetadataFromRaw(raw map[string]string) OutboundMetadata {
	if len(raw) == 0 {
		return OutboundMetadata{}
	}
	return OutboundMetadata{
		MessageKind:            strings.TrimSpace(raw[OutboundMetadataKeyMessageKind]),
		ToolCalls:              strings.TrimSpace(raw[OutboundMetadataKeyToolCalls]),
		OutboundKind:           strings.TrimSpace(raw[OutboundMetadataKeyOutboundKind]),
		ModelName:              strings.TrimSpace(raw[OutboundMetadataKeyModelName]),
		DefaultModelName:       strings.TrimSpace(raw[OutboundMetadataKeyDefaultModel]),
		UsageInputTokens:       parseOutboundMetadataInt(raw[OutboundMetadataKeyUsageInput]),
		UsageOutputTokens:      parseOutboundMetadataInt(raw[OutboundMetadataKeyUsageOutput]),
		UsageTotalTokens:       parseOutboundMetadataInt(raw[OutboundMetadataKeyUsageTotal]),
		InteractionKind:        strings.TrimSpace(raw[OutboundMetadataKeyInteraction]),
		InteractionControls:    strings.TrimSpace(raw[OutboundMetadataKeyControls]),
		InteractionChoicesJSON: normalizeOutboundInteractionChoices(raw[OutboundMetadataKeyChoices]),
	}
}

func (m OutboundMetadata) ApplyToContext(ctx *InboundContext) {
	if ctx == nil {
		return
	}
	rawCount := len(ctx.Raw)
	if strings.TrimSpace(m.MessageKind) != "" {
		rawCount++
	}
	if strings.TrimSpace(m.ToolCalls) != "" {
		rawCount++
	}
	if strings.TrimSpace(m.OutboundKind) != "" {
		rawCount++
	}
	if strings.TrimSpace(m.ModelName) != "" {
		rawCount++
	}
	if strings.TrimSpace(m.DefaultModelName) != "" {
		rawCount++
	}
	if m.UsageInputTokens > 0 {
		rawCount++
	}
	if m.UsageOutputTokens > 0 {
		rawCount++
	}
	if m.UsageTotalTokens > 0 {
		rawCount++
	}
	if strings.TrimSpace(m.InteractionKind) != "" {
		rawCount++
	}
	if strings.TrimSpace(m.InteractionControls) != "" {
		rawCount++
	}
	if m.InteractionChoicesJSON != "" {
		rawCount++
	}
	if rawCount == 0 {
		return
	}
	if ctx.Raw == nil {
		ctx.Raw = make(map[string]string, rawCount)
	}
	setOutboundMetadataString(ctx.Raw, OutboundMetadataKeyMessageKind, m.MessageKind)
	setOutboundMetadataString(ctx.Raw, OutboundMetadataKeyToolCalls, m.ToolCalls)
	setOutboundMetadataString(ctx.Raw, OutboundMetadataKeyOutboundKind, m.OutboundKind)
	setOutboundMetadataString(ctx.Raw, OutboundMetadataKeyModelName, m.ModelName)
	setOutboundMetadataString(ctx.Raw, OutboundMetadataKeyDefaultModel, m.DefaultModelName)
	setOutboundMetadataInt(ctx.Raw, OutboundMetadataKeyUsageInput, m.UsageInputTokens)
	setOutboundMetadataInt(ctx.Raw, OutboundMetadataKeyUsageOutput, m.UsageOutputTokens)
	setOutboundMetadataInt(ctx.Raw, OutboundMetadataKeyUsageTotal, m.UsageTotalTokens)
	setOutboundMetadataString(ctx.Raw, OutboundMetadataKeyInteraction, m.InteractionKind)
	setOutboundMetadataString(ctx.Raw, OutboundMetadataKeyControls, m.InteractionControls)
	setOutboundMetadataString(ctx.Raw, OutboundMetadataKeyChoices, m.InteractionChoicesJSON)
}

func (m OutboundMetadata) IsToolFeedback() bool {
	return strings.EqualFold(m.MessageKind, OutboundMessageKindToolFeedback)
}

func (m OutboundMetadata) IsThought() bool {
	return strings.EqualFold(m.MessageKind, OutboundMessageKindThought)
}

func (m OutboundMetadata) IsToolCalls() bool {
	return strings.EqualFold(m.MessageKind, OutboundMessageKindToolCalls)
}

func (m OutboundMetadata) IsFinalReply() bool {
	return strings.EqualFold(m.MessageKind, OutboundMessageKindFinalReply)
}

func (m OutboundMetadata) HasAuxiliaryKind() bool {
	return strings.TrimSpace(m.MessageKind) != ""
}

func (m OutboundMetadata) IsFinal() bool {
	return strings.EqualFold(m.OutboundKind, OutboundKindFinal)
}

func (m OutboundMetadata) IsInterim() bool {
	return strings.EqualFold(m.OutboundKind, OutboundKindInterim)
}

func (m OutboundMetadata) BypassesPlaceholderEdit() bool {
	return m.IsThought() || m.IsToolCalls() || m.IsFinalReply() || m.IsApprovalPrompt() || m.IsQuestionPrompt()
}

func (m OutboundMetadata) IsApprovalPrompt() bool {
	return strings.EqualFold(m.InteractionKind, OutboundInteractionApproval) &&
		strings.EqualFold(m.InteractionControls, OutboundInteractionControlsPrompt)
}

func (m OutboundMetadata) IsQuestionPrompt() bool {
	return strings.EqualFold(m.InteractionKind, OutboundInteractionQuestion) &&
		strings.EqualFold(m.InteractionControls, OutboundInteractionControlsPrompt)
}

func (m OutboundMetadata) RemovesInteractionControls() bool {
	return (strings.EqualFold(m.InteractionKind, OutboundInteractionApproval) ||
		strings.EqualFold(m.InteractionKind, OutboundInteractionQuestion)) &&
		strings.EqualFold(m.InteractionControls, OutboundInteractionControlsRemove)
}

func (m OutboundMetadata) WithInteractionChoices(choices []string) OutboundMetadata {
	m.InteractionChoicesJSON = encodeOutboundInteractionChoices(choices)
	return m
}

func (m OutboundMetadata) InteractionChoices() []string {
	return parseOutboundInteractionChoices(m.InteractionChoicesJSON)
}

func encodeOutboundInteractionChoices(choices []string) string {
	if len(choices) == 0 || len(choices) > 3 {
		return ""
	}
	normalized := make([]string, 0, len(choices))
	for _, choice := range choices {
		choice = strings.TrimSpace(choice)
		if choice == "" || len([]rune(choice)) > 64 {
			return ""
		}
		normalized = append(normalized, choice)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func normalizeOutboundInteractionChoices(encoded string) string {
	return encodeOutboundInteractionChoices(parseOutboundInteractionChoices(encoded))
}

func parseOutboundInteractionChoices(encoded string) []string {
	if strings.TrimSpace(encoded) == "" {
		return nil
	}
	var choices []string
	if err := json.Unmarshal([]byte(encoded), &choices); err != nil || len(choices) == 0 || len(choices) > 3 {
		return nil
	}
	for index, choice := range choices {
		choice = strings.TrimSpace(choice)
		if choice == "" || len([]rune(choice)) > 64 {
			return nil
		}
		choices[index] = choice
	}
	return choices
}

func parseOutboundMetadataInt(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func setOutboundMetadataString(raw map[string]string, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	raw[key] = value
}

func setOutboundMetadataInt(raw map[string]string, key string, value int) {
	if value <= 0 {
		return
	}
	raw[key] = strconv.Itoa(value)
}
