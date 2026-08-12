package providererrors

import (
	"fmt"
	"strings"
	"time"
)

// Kind is the provider-boundary classification of a failed request.
type Kind string

const (
	KindUnknown         Kind = "unknown"
	KindAuthentication  Kind = "authentication"
	KindBilling         Kind = "billing"
	KindRateLimit       Kind = "rate_limit"
	KindContextOverflow Kind = "context_overflow"
	KindTimeout         Kind = "timeout"
	KindCanceled        Kind = "canceled"
	KindTransient       Kind = "transient"
	KindInvalidRequest  Kind = "invalid_request"
	KindNetwork         Kind = "network"
)

// ProviderError carries adapter-owned failure metadata without exposing the
// underlying SDK or response error in user-facing text.
type ProviderError struct {
	Kind        Kind
	HTTPStatus  int
	RetryAfter  time.Duration
	RequestID   string
	SafeMessage string
	Cause       error

	diagnosticRequestID   string
	diagnosticSafeMessage string
}

const diagnosticLookaheadRunes = 1024

func (e *ProviderError) Error() string {
	if e == nil {
		return "provider request failed"
	}
	return fmt.Sprintf(
		"provider request failed: kind=%s status=%d retry_after=%s request_id=%q message=%q",
		e.Kind.Canonical(),
		e.HTTPStatus,
		e.RetryAfter,
		normalizeSafeText(e.RequestID, 128),
		normalizeSafeText(e.SafeMessage, 240),
	)
}

// Canonical maps zero and unrecognized kinds to KindUnknown so an adapter
// cannot create an unbounded or ambiguous classification value.
func (kind Kind) Canonical() Kind {
	switch kind {
	case KindAuthentication,
		KindBilling,
		KindRateLimit,
		KindContextOverflow,
		KindTimeout,
		KindCanceled,
		KindTransient,
		KindInvalidRequest,
		KindNetwork:
		return kind
	case KindUnknown, "":
		return KindUnknown
	default:
		return KindUnknown
	}
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// DiagnosticRequestID returns bounded lookahead for config-aware redaction.
// Public RequestID remains at its established smaller metadata bound.
func (e *ProviderError) DiagnosticRequestID() string {
	if e == nil {
		return ""
	}
	if e.diagnosticRequestID == "" {
		return e.RequestID
	}
	return e.diagnosticRequestID
}

// DiagnosticSafeMessage returns bounded lookahead for config-aware redaction.
// Public SafeMessage remains safe for ordinary classification and logging.
func (e *ProviderError) DiagnosticSafeMessage() string {
	if e == nil {
		return ""
	}
	if e.diagnosticSafeMessage == "" {
		return e.SafeMessage
	}
	return e.diagnosticSafeMessage
}

// WithRequestID returns a copy with synchronized public and diagnostic views.
func (e *ProviderError) WithRequestID(value string) *ProviderError {
	if e == nil {
		return nil
	}
	copy := *e
	copy.RequestID = normalizeSafeText(value, 128)
	copy.diagnosticRequestID = normalizeSafeText(value, 128+diagnosticLookaheadRunes)
	return &copy
}

// WithSafeMessage returns a copy with synchronized public and diagnostic views.
func (e *ProviderError) WithSafeMessage(value string) *ProviderError {
	if e == nil {
		return nil
	}
	copy := *e
	copy.SafeMessage = normalizeSafeText(value, 240)
	copy.diagnosticSafeMessage = normalizeSafeText(value, 240+diagnosticLookaheadRunes)
	return &copy
}

func normalizeSafeText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
