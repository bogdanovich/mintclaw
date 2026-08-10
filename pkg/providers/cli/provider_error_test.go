package cliprovider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func TestNormalizeCLIErrorCompatibilityContracts(t *testing.T) {
	tests := []struct {
		name       string
		diagnostic string
		want       providererrors.Kind
	}{
		{
			name:       "authentication",
			diagnostic: "authentication failed: token expired",
			want:       providererrors.KindAuthentication,
		},
		{name: "billing", diagnostic: "credit balance is too low", want: providererrors.KindBilling},
		{name: "rate limit", diagnostic: "rate limit exceeded", want: providererrors.KindRateLimit},
		{
			name:       "context overflow",
			diagnostic: "prompt is too long for context window",
			want:       providererrors.KindContextOverflow,
		},
		{name: "timeout", diagnostic: "request timed out", want: providererrors.KindTimeout},
		{name: "cancellation", diagnostic: "operation canceled", want: providererrors.KindCanceled},
		{name: "transient", diagnostic: "service unavailable", want: providererrors.KindTransient},
		{name: "unknown", diagnostic: "command failed", want: providererrors.KindUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cause := errors.New("process exited")
			err := normalizeCLIError(cause, tt.diagnostic+" secret-token-123")
			_ = assertProviderError(t, err, cause, tt.want)
			if strings.Contains(err.Error(), "secret-token-123") {
				t.Fatal("ProviderError exposed raw CLI diagnostics")
			}
		})
	}
}

func TestNormalizeCLIErrorStructuredAndTransportPrecedence(t *testing.T) {
	existing := providererrors.FromStructuredError(
		providererrors.KindBilling,
		0,
		nil,
		"",
		"credits unavailable",
		errors.New("billing"),
	)
	if got := normalizeCLIError(
		existing,
		"rate limit exceeded",
	); got != existing { //nolint:errorlint // exact top-level identity is the contract under test
		t.Fatalf("normalizeCLIError() = %#v, want existing structured error", got)
	}

	err := normalizeCLIError(context.DeadlineExceeded, "rate limit exceeded")
	_ = assertProviderError(t, err, context.DeadlineExceeded, providererrors.KindTimeout)
}

func TestNormalizeCodedCLIErrorCodePrecedesText(t *testing.T) {
	cause := errors.New("CLI event failure")
	err := normalizeCodedCLIError("insufficient_quota", "rate limit exceeded", cause)
	_ = assertProviderError(t, err, cause, providererrors.KindBilling)
}

func assertProviderError(t *testing.T, err, cause error, want providererrors.Kind) *providererrors.ProviderError {
	t.Helper()
	providerErr := assertProviderErrorKind(t, err, want)
	if !errors.Is(err, cause) {
		t.Fatal("normalized error does not preserve its cause")
	}
	return providerErr
}

func assertProviderErrorKind(t *testing.T, err error, want providererrors.Kind) *providererrors.ProviderError {
	t.Helper()
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error type = %T, want *providererrors.ProviderError", err)
	}
	if providerErr.Kind != want {
		t.Fatalf("Kind = %q, want %q", providerErr.Kind, want)
	}
	return providerErr
}
