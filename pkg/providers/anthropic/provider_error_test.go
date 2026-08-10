package anthropicprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func TestProviderErrorContract(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		errorType  string
		message    string
		retryAfter string
		wantKind   providererrors.Kind
	}{
		{
			name: "authentication", status: http.StatusUnauthorized,
			errorType: "authentication_error", message: "API key rejected",
			wantKind: providererrors.KindAuthentication,
		},
		{
			name: "billing precedes compatibility text", status: http.StatusBadRequest,
			errorType: "billing_error", message: "prompt is too long but credits are exhausted",
			wantKind: providererrors.KindBilling,
		},
		{
			name: "rate limit", status: http.StatusTooManyRequests,
			errorType: "rate_limit_error", message: "request rate limited", retryAfter: "7",
			wantKind: providererrors.KindRateLimit,
		},
		{
			name: "context compatibility fallback", status: http.StatusBadRequest,
			errorType: "invalid_request_error", message: "prompt is too long for this context window",
			wantKind: providererrors.KindContextOverflow,
		},
		{
			name: "transient", status: 529,
			errorType: "overloaded_error", message: "service overloaded",
			wantKind: providererrors.KindTransient,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Request-Id", "req-anthropic-contract")
				if test.retryAfter != "" {
					w.Header().Set("Retry-After", test.retryAfter)
				}
				w.WriteHeader(test.status)
				_, _ = fmt.Fprintf(
					w,
					`{"type":"error","error":{"type":%q,"message":%q}}`,
					test.errorType,
					test.message,
				)
			}))
			defer server.Close()

			provider := NewProviderWithClient(createAnthropicTestClient(server.URL, "test-token"))
			_, err := provider.Chat(
				t.Context(),
				[]Message{{Role: "user", Content: "hello"}},
				nil,
				"claude-sonnet-4.6",
				map[string]any{},
			)
			assertAnthropicProviderError(t, err, test.status, test.wantKind, test.retryAfter != "")
		})
	}
}

func TestNormalizeAnthropicTransportContract(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind providererrors.Kind
	}{
		{name: "timeout", err: context.DeadlineExceeded, kind: providererrors.KindTimeout},
		{name: "canceled", err: context.Canceled, kind: providererrors.KindCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeAnthropicError(test.err)
			var providerErr *providererrors.ProviderError
			if !errors.As(normalized, &providerErr) || providerErr.Kind != test.kind {
				t.Fatalf("normalized error = %#v, want kind %q", normalized, test.kind)
			}
			if !errors.Is(normalized, test.err) {
				t.Fatal("normalized error did not retain transport cause")
			}
		})
	}
}

func TestNormalizeAnthropicCredentialError(t *testing.T) {
	cause := errors.New("private credential diagnostics")
	err := normalizeAnthropicCredentialError(cause)
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindAuthentication {
		t.Fatalf("error = %#v, want authentication ProviderError", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("credential error did not retain cause")
	}
	if strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("credential error leaked cause: %q", err)
	}
}

func assertAnthropicProviderError(
	t *testing.T,
	err error,
	wantStatus int,
	wantKind providererrors.Kind,
	wantRetry bool,
) {
	t.Helper()
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T, want ProviderError", err)
	}
	if providerErr.Kind != wantKind || providerErr.HTTPStatus != wantStatus {
		t.Fatalf("ProviderError = %#v, want kind %q status %d", providerErr, wantKind, wantStatus)
	}
	if providerErr.RequestID != "req-anthropic-contract" {
		t.Fatalf("RequestID = %q, want req-anthropic-contract", providerErr.RequestID)
	}
	if wantRetry && providerErr.RetryAfter != 7*time.Second {
		t.Fatalf("RetryAfter = %s, want 7s", providerErr.RetryAfter)
	}
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		t.Fatal("ProviderError did not retain Anthropic SDK cause")
	}
}
