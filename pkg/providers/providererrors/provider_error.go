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
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "provider request failed"
	}
	return fmt.Sprintf(
		"provider request failed: kind=%s status=%d retry_after=%s request_id=%q message=%q",
		effectiveKind(e.Kind),
		e.HTTPStatus,
		e.RetryAfter,
		boundedText(e.RequestID, 128),
		boundedText(e.SafeMessage, 240),
	)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func effectiveKind(kind Kind) Kind {
	if kind == "" {
		return KindUnknown
	}
	return kind
}

func boundedText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
