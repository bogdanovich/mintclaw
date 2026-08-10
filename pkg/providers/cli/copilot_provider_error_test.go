package cliprovider

import (
	"context"
	"errors"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func TestNormalizeCopilotErrorContracts(t *testing.T) {
	tests := []struct {
		name      string
		errorType string
		status    int64
		message   string
		want      providererrors.Kind
	}{
		{name: "authentication", errorType: "authentication", status: 401, want: providererrors.KindAuthentication},
		{name: "billing", errorType: "quota", status: 403, want: providererrors.KindBilling},
		{name: "rate limit", errorType: "rate_limit", status: 429, want: providererrors.KindRateLimit},
		{name: "context overflow", errorType: "context_window", status: 400, want: providererrors.KindContextOverflow},
		{name: "timeout", errorType: "timeout", status: 408, want: providererrors.KindTimeout},
		{name: "cancellation", errorType: "canceled", status: 499, want: providererrors.KindCanceled},
		{name: "transient", errorType: "service_unavailable", status: 503, want: providererrors.KindTransient},
		{
			name:      "structured type precedes message and status",
			errorType: "subscription",
			status:    429,
			message:   "rate limit exceeded",
			want:      providererrors.KindBilling,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestID := "github-request-123"
			eventType := tt.errorType
			message := tt.message
			event := &copilot.SessionEvent{Data: copilot.Data{
				ErrorType:      &eventType,
				StatusCode:     &tt.status,
				ProviderCallID: &requestID,
				Message:        &message,
			}}
			cause := errors.New("session error")
			err := normalizeCopilotError(cause, event)
			providerErr := assertProviderError(t, err, cause, tt.want)
			if providerErr.HTTPStatus != int(tt.status) {
				t.Fatalf("HTTPStatus = %d, want %d", providerErr.HTTPStatus, tt.status)
			}
			if providerErr.RequestID != requestID {
				t.Fatalf("RequestID = %q, want %q", providerErr.RequestID, requestID)
			}
		})
	}
}

func TestNormalizeCopilotErrorStatusAndCompatibilityFallback(t *testing.T) {
	cause := errors.New("session error")
	message := "credit balance is too low"
	status := int64(429)
	event := &copilot.SessionEvent{Data: copilot.Data{StatusCode: &status, Message: &message}}
	_ = assertProviderError(t, normalizeCopilotError(cause, event), cause, providererrors.KindRateLimit)

	requestID := "github-request-fallback"
	event = &copilot.SessionEvent{Data: copilot.Data{Message: &message, ProviderCallID: &requestID}}
	err := normalizeCopilotError(cause, event)
	providerErr := assertProviderError(t, err, cause, providererrors.KindBilling)
	if providerErr.RequestID != requestID {
		t.Fatalf("RequestID = %q, want %q", providerErr.RequestID, requestID)
	}
	if strings.Contains(err.Error(), message) {
		t.Fatal("ProviderError exposed raw Copilot diagnostics")
	}

	_ = assertProviderError(
		t,
		normalizeCopilotError(context.DeadlineExceeded, event),
		context.DeadlineExceeded,
		providererrors.KindTimeout,
	)
}

func TestNormalizeCopilotErrorStructuredEventPrecedesTransport(t *testing.T) {
	errorType := "subscription"
	status := int64(402)
	requestID := "github-request-structured"
	event := &copilot.SessionEvent{Data: copilot.Data{
		ErrorType:      &errorType,
		StatusCode:     &status,
		ProviderCallID: &requestID,
	}}

	providerErr := assertProviderError(
		t,
		normalizeCopilotError(context.DeadlineExceeded, event),
		context.DeadlineExceeded,
		providererrors.KindBilling,
	)
	if providerErr.HTTPStatus != int(status) {
		t.Fatalf("HTTPStatus = %d, want %d", providerErr.HTTPStatus, status)
	}
	if providerErr.RequestID != requestID {
		t.Fatalf("RequestID = %q, want %q", providerErr.RequestID, requestID)
	}
}

func TestNormalizeCopilotErrorTransportFallbackPreservesRequestID(t *testing.T) {
	requestID := "github-request-transport"
	event := &copilot.SessionEvent{Data: copilot.Data{ProviderCallID: &requestID}}

	providerErr := assertProviderError(
		t,
		normalizeCopilotError(context.DeadlineExceeded, event),
		context.DeadlineExceeded,
		providererrors.KindTimeout,
	)
	if providerErr.RequestID != requestID {
		t.Fatalf("RequestID = %q, want %q", providerErr.RequestID, requestID)
	}
}
