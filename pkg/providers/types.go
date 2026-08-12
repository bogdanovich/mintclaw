package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	providercapabilities "github.com/bogdanovich/mintclaw/pkg/providers/capabilities"
	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

type (
	ToolCall                    = protocoltypes.ToolCall
	FunctionCall                = protocoltypes.FunctionCall
	LLMResponse                 = protocoltypes.LLMResponse
	StreamChunk                 = protocoltypes.StreamChunk
	UsageInfo                   = protocoltypes.UsageInfo
	Message                     = protocoltypes.Message
	ToolResultStatus            = protocoltypes.ToolResultStatus
	ToolExecution               = protocoltypes.ToolExecution
	ToolDefinition              = protocoltypes.ToolDefinition
	ToolFunctionDefinition      = protocoltypes.ToolFunctionDefinition
	ExtraContent                = protocoltypes.ExtraContent
	GoogleExtra                 = protocoltypes.GoogleExtra
	ContentBlock                = protocoltypes.ContentBlock
	CacheControl                = protocoltypes.CacheControl
	Attachment                  = protocoltypes.Attachment
	ImageGenerationRequest      = protocoltypes.ImageGenerationRequest
	GeneratedImage              = protocoltypes.GeneratedImage
	ImageGenerationResponse     = protocoltypes.ImageGenerationResponse
	ProviderCapabilities        = providercapabilities.ProviderCapabilities
	StreamingCapabilities       = providercapabilities.StreamingCapabilities
	ImageGenerationCapabilities = providercapabilities.ImageGenerationCapabilities
	ToolSchemaLimits            = providercapabilities.ToolSchemaLimits
)

const (
	ToolResultStatusSuccess     = protocoltypes.ToolResultStatusSuccess
	ToolResultStatusError       = protocoltypes.ToolResultStatusError
	ToolResultStatusUnresolved  = protocoltypes.ToolResultStatusUnresolved
	ToolResultStatusInterrupted = protocoltypes.ToolResultStatusInterrupted
	ToolResultStatusUnknown     = protocoltypes.ToolResultStatusUnknown
)

type LLMProvider interface {
	Chat(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
	) (*LLMResponse, error)
	GetDefaultModel() string
}

type StatefulProvider interface {
	LLMProvider
	Close()
}

// StreamingProvider is an optional interface for providers that support token streaming.
// onChunk receives the accumulated text so far (not individual deltas).
// The returned LLMResponse is the same complete response for compatibility with tool-call handling.
type StreamingProvider interface {
	ChatStream(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
		onChunk func(accumulated string),
	) (*LLMResponse, error)
}

type StreamingEventProvider interface {
	ChatStreamEvents(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
		onChunk func(StreamChunk),
	) (*LLMResponse, error)
}

// CapabilityProvider exposes one authoritative provider feature descriptor.
type CapabilityProvider interface {
	Capabilities() ProviderCapabilities
}

// ImageGenerationCapable is the legacy provider-owned image generation
// contract. New providers should also implement CapabilityProvider; the
// descriptor then takes precedence over these compatibility methods.
type ImageGenerationCapable interface {
	SupportsImageGeneration() bool
	ImageGenerationProviderID() string
	DefaultImageGenerationModel() string
	GenerateImage(ctx context.Context, req ImageGenerationRequest) (*ImageGenerationResponse, error)
}

// ImageGenerationProvider is the descriptor-based image generation contract.
type ImageGenerationProvider interface {
	CapabilityProvider
	GenerateImage(ctx context.Context, req ImageGenerationRequest) (*ImageGenerationResponse, error)
}

// FailoverReason classifies why an LLM request failed for fallback decisions.
type FailoverReason string

const (
	FailoverAuth            FailoverReason = "auth"
	FailoverRateLimit       FailoverReason = "rate_limit"
	FailoverBilling         FailoverReason = "billing"
	FailoverNetwork         FailoverReason = "network"
	FailoverTimeout         FailoverReason = "timeout"
	FailoverFormat          FailoverReason = "format"
	FailoverContextOverflow FailoverReason = "context_overflow"
	FailoverOverloaded      FailoverReason = "overloaded"
	FailoverUnknown         FailoverReason = "unknown"
)

// ErrorClassificationSource identifies the evidence used to classify a
// provider failure. Values are persisted in diagnostic traces.
type ErrorClassificationSource string

const (
	ClassificationProviderStructured ErrorClassificationSource = "provider_structured"
	ClassificationContextDeadline    ErrorClassificationSource = "context_deadline"
	ClassificationTransportError     ErrorClassificationSource = "transport_error"
	ClassificationHTTPStatus         ErrorClassificationSource = "http_status"
	ClassificationStatusText         ErrorClassificationSource = "status_text"
	ClassificationMessagePattern     ErrorClassificationSource = "message_pattern"
	ClassificationLocalControl       ErrorClassificationSource = "local_control"
	ClassificationUnclassified       ErrorClassificationSource = "unclassified"
)

// FailoverError wraps an LLM provider error with classification metadata.
type FailoverError struct {
	Reason               FailoverReason
	Provider             string
	Model                string
	Status               int
	ClassificationSource ErrorClassificationSource
	Wrapped              error
}

func (e *FailoverError) Error() string {
	return fmt.Sprintf("failover: provider=%s model=%s status=%d classification=%s raw_error=%q",
		e.Provider, e.Model, e.Status, e.Reason, errorPreview(e.Wrapped))
}

func (e *FailoverError) Unwrap() error {
	return e.Wrapped
}

func errorPreview(err error) string {
	if err == nil {
		return ""
	}
	return failureMetadataPreview(err.Error(), nil)
}

func failureMetadataPreview(value string, filter func(string) string) string {
	const maxPreviewBytes = 240
	redactor := diagnostictrace.Redactor{Filter: filter}
	preview := redactor.RedactText(value, maxPreviewBytes)
	preview = strings.ToValidUTF8(preview, "\uFFFD")
	preview = strings.Join(strings.Fields(preview), " ")
	return redactor.RedactText(preview, maxPreviewBytes)
}

// IsRetriable returns true if this error should trigger fallback to next candidate.
// Non-retriable:
//   - Auth errors: the active credentials are invalid and should be surfaced
//     directly so users can re-authenticate instead of silently switching models.
//   - Format/context errors: retrying another provider with the same request is
//     unlikely to succeed and can mask the real issue.
func (e *FailoverError) IsRetriable() bool {
	return e.Reason != FailoverAuth &&
		e.Reason != FailoverFormat &&
		e.Reason != FailoverContextOverflow
}

// ModelConfig holds primary model and fallback list.
type ModelConfig struct {
	Primary   string
	Fallbacks []string
}
