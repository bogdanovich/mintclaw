package bus

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

const (
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

// OutboundMetadata owns the delivery semantics attached to an outbound bus
// message. InboundContext remains limited to inbound addressing and transport
// facts.
type OutboundMetadata struct {
	MessageKind         string          `json:"message_kind,omitempty"`
	ToolCalls           json.RawMessage `json:"tool_calls,omitempty"`
	OutboundKind        string          `json:"outbound_kind,omitempty"`
	ModelName           string          `json:"model_name,omitempty"`
	DefaultModelName    string          `json:"default_model_name,omitempty"`
	UsageInputTokens    int             `json:"usage_input_tokens,omitempty"`
	UsageOutputTokens   int             `json:"usage_output_tokens,omitempty"`
	UsageTotalTokens    int             `json:"usage_total_tokens,omitempty"`
	InteractionKind     string          `json:"interaction_kind,omitempty"`
	InteractionControls string          `json:"interaction_controls,omitempty"`
	Choices             []string        `json:"interaction_choices,omitempty"`
	InteractionID       string          `json:"interaction_id,omitempty"`
	InteractionShortID  string          `json:"interaction_short_id,omitempty"`
	RequestID           string          `json:"request_id,omitempty"`
}

func (m OutboundMetadata) IsZero() bool {
	return m.MessageKind == "" && len(m.ToolCalls) == 0 && m.OutboundKind == "" && m.ModelName == "" &&
		m.DefaultModelName == "" && m.UsageInputTokens == 0 && m.UsageOutputTokens == 0 &&
		m.UsageTotalTokens == 0 && m.InteractionKind == "" && m.InteractionControls == "" &&
		len(m.Choices) == 0 && m.InteractionID == "" && m.InteractionShortID == "" && m.RequestID == ""
}

// NormalizeOutboundMetadata returns one canonical representation for runtime
// construction and durable persistence.
func NormalizeOutboundMetadata(m OutboundMetadata) OutboundMetadata {
	m.MessageKind = strings.TrimSpace(m.MessageKind)
	m.ToolCalls = append(json.RawMessage(nil), bytes.TrimSpace(m.ToolCalls)...)
	m.OutboundKind = strings.TrimSpace(m.OutboundKind)
	m.ModelName = strings.TrimSpace(m.ModelName)
	m.DefaultModelName = strings.TrimSpace(m.DefaultModelName)
	m.InteractionKind = strings.TrimSpace(m.InteractionKind)
	m.InteractionControls = strings.TrimSpace(m.InteractionControls)
	m.InteractionID = strings.TrimSpace(m.InteractionID)
	m.InteractionShortID = strings.TrimSpace(m.InteractionShortID)
	m.RequestID = strings.TrimSpace(m.RequestID)
	if m.UsageInputTokens < 0 {
		m.UsageInputTokens = 0
	}
	if m.UsageOutputTokens < 0 {
		m.UsageOutputTokens = 0
	}
	if m.UsageTotalTokens < 0 {
		m.UsageTotalTokens = 0
	}
	m.Choices = normalizeOutboundInteractionChoices(m.Choices)
	return m
}

// ValidateOutboundMetadata rejects non-canonical persisted metadata. Producers
// normalize before enqueueing; readers accept only the current contract.
func ValidateOutboundMetadata(m OutboundMetadata) error {
	normalized := NormalizeOutboundMetadata(m)
	if m.MessageKind != normalized.MessageKind || !bytes.Equal(m.ToolCalls, normalized.ToolCalls) ||
		m.OutboundKind != normalized.OutboundKind || m.ModelName != normalized.ModelName ||
		m.DefaultModelName != normalized.DefaultModelName || m.UsageInputTokens != normalized.UsageInputTokens ||
		m.UsageOutputTokens != normalized.UsageOutputTokens || m.UsageTotalTokens != normalized.UsageTotalTokens ||
		m.InteractionKind != normalized.InteractionKind ||
		m.InteractionControls != normalized.InteractionControls || m.InteractionID != normalized.InteractionID ||
		m.InteractionShortID != normalized.InteractionShortID || m.RequestID != normalized.RequestID ||
		!equalOutboundInteractionChoices(m.Choices, normalized.Choices) {
		return errors.New("outbound metadata is not canonical")
	}
	if len(m.ToolCalls) > 0 && !json.Valid(m.ToolCalls) {
		return errors.New("outbound tool calls must be valid JSON")
	}
	return nil
}

// Merge applies non-zero delivery semantics from update to m.
func (m OutboundMetadata) Merge(update OutboundMetadata) OutboundMetadata {
	update = NormalizeOutboundMetadata(update)
	if update.MessageKind != "" {
		m.MessageKind = update.MessageKind
	}
	if len(update.ToolCalls) > 0 {
		m.ToolCalls = append(json.RawMessage(nil), update.ToolCalls...)
	}
	if update.OutboundKind != "" {
		m.OutboundKind = update.OutboundKind
	}
	if update.ModelName != "" {
		m.ModelName = update.ModelName
	}
	if update.DefaultModelName != "" {
		m.DefaultModelName = update.DefaultModelName
	}
	if update.UsageInputTokens > 0 {
		m.UsageInputTokens = update.UsageInputTokens
	}
	if update.UsageOutputTokens > 0 {
		m.UsageOutputTokens = update.UsageOutputTokens
	}
	if update.UsageTotalTokens > 0 {
		m.UsageTotalTokens = update.UsageTotalTokens
	}
	if update.InteractionKind != "" {
		m.InteractionKind = update.InteractionKind
	}
	if update.InteractionControls != "" {
		m.InteractionControls = update.InteractionControls
	}
	if len(update.Choices) > 0 {
		m.Choices = append([]string(nil), update.Choices...)
	}
	if update.InteractionID != "" {
		m.InteractionID = update.InteractionID
	}
	if update.InteractionShortID != "" {
		m.InteractionShortID = update.InteractionShortID
	}
	if update.RequestID != "" {
		m.RequestID = update.RequestID
	}
	return NormalizeOutboundMetadata(m)
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
	m.Choices = normalizeOutboundInteractionChoices(choices)
	return m
}

func (m OutboundMetadata) InteractionChoices() []string {
	return append([]string(nil), m.Choices...)
}

func normalizeOutboundInteractionChoices(choices []string) []string {
	if len(choices) == 0 || len(choices) > 3 {
		return nil
	}
	normalized := make([]string, 0, len(choices))
	for _, choice := range choices {
		choice = strings.TrimSpace(choice)
		if choice == "" || len([]rune(choice)) > 64 {
			return nil
		}
		normalized = append(normalized, choice)
	}
	return normalized
}

func equalOutboundInteractionChoices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
