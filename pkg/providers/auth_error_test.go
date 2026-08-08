package providers

import (
	"errors"
	"fmt"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers/common"
)

func TestClassifyError_HTTPErrorStatus(t *testing.T) {
	err := fmt.Errorf("provider request: %w", &common.HTTPError{
		StatusCode:  httpStatusUnauthorized,
		BodyPreview: `{"error":"unauthorized"}`,
		ContentType: "application/json",
		APIBase:     "https://api.example.com",
	})

	result := ClassifyError(err, "openai", "gpt-4")
	if result == nil {
		t.Fatal("expected classified error")
	}
	if result.Reason != FailoverAuth {
		t.Fatalf("reason = %q, want %q", result.Reason, FailoverAuth)
	}
	if result.Status != httpStatusUnauthorized {
		t.Fatalf("status = %d, want %d", result.Status, httpStatusUnauthorized)
	}
}

func TestClassifyAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want AuthErrorKind
	}{
		{
			name: "invalid api key",
			err: errors.New(
				`API request failed: Status: 401 Body: {"error":{"message":"Incorrect API key provided"}}`,
			),
			want: AuthErrorInvalidAPIKey,
		},
		{
			name: "missing api key",
			err:  errors.New("API key not configured"),
			want: AuthErrorMissingAPIKey,
		},
		{
			name: "expired token",
			err:  errors.New("oauth token refresh failed: token has expired"),
			want: AuthErrorExpiredToken,
		},
		{
			name: "structured generic auth",
			err: &common.HTTPError{
				StatusCode:  httpStatusUnauthorized,
				BodyPreview: `{"error":"unauthorized"}`,
				ContentType: "application/json",
				APIBase:     "https://api.example.com",
			},
			want: AuthErrorGeneric,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ClassifyAuthError(tt.err)
			if !ok {
				t.Fatal("expected auth classification")
			}
			if got != tt.want {
				t.Fatalf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyAuthError_ProviderErrorPrecedence(t *testing.T) {
	if kind, ok := ClassifyAuthError(&ProviderError{
		Kind: ProviderErrorBilling, HTTPStatus: 401, SafeMessage: "unauthorized",
	}); ok {
		t.Fatalf("billing ProviderError classified as auth: %q", kind)
	}

	kind, ok := ClassifyAuthError(&ProviderError{
		Kind: ProviderErrorAuthentication, HTTPStatus: 429, SafeMessage: "invalid api key",
	})
	if !ok || kind != AuthErrorInvalidAPIKey {
		t.Fatalf("auth ProviderError = (%q, %v), want invalid_api_key", kind, ok)
	}

	kind, ok = ClassifyAuthError(&ProviderError{HTTPStatus: 401})
	if !ok || kind != AuthErrorGeneric {
		t.Fatalf("zero-kind auth ProviderError = (%q, %v), want generic auth", kind, ok)
	}
}

func TestClassifyAuthError_FallbackExhaustedAllAuth(t *testing.T) {
	err := &FallbackExhaustedError{
		Attempts: []FallbackAttempt{
			{
				Reason: FailoverAuth,
				Error: &FailoverError{
					Reason:  FailoverAuth,
					Wrapped: errors.New("invalid api key"),
				},
			},
			{
				Reason: FailoverAuth,
				Error: &FailoverError{
					Reason:  FailoverAuth,
					Wrapped: errors.New("unauthorized"),
				},
			},
		},
	}

	got, ok := ClassifyAuthError(err)
	if !ok {
		t.Fatal("expected auth classification")
	}
	if got != AuthErrorInvalidAPIKey {
		t.Fatalf("kind = %q, want %q", got, AuthErrorInvalidAPIKey)
	}
}

func TestClassifyAuthError_FallbackExhaustedMixedFailures(t *testing.T) {
	err := &FallbackExhaustedError{
		Attempts: []FallbackAttempt{
			{
				Reason: FailoverAuth,
				Error: &FailoverError{
					Reason:  FailoverAuth,
					Wrapped: errors.New("invalid api key"),
				},
			},
			{
				Reason: FailoverRateLimit,
				Error: &FailoverError{
					Reason:  FailoverRateLimit,
					Wrapped: errors.New("rate limit exceeded"),
				},
			},
		},
	}

	if got, ok := ClassifyAuthError(err); ok {
		t.Fatalf("kind = %q, want no auth classification for mixed failures", got)
	}
}

func TestClassifyAuthError_FallbackAttemptProviderErrorPrecedence(t *testing.T) {
	err := &FallbackExhaustedError{Attempts: []FallbackAttempt{{
		Reason: FailoverAuth,
		Error: &ProviderError{
			Kind: ProviderErrorBilling, HTTPStatus: 401, SafeMessage: "unauthorized",
		},
	}}}
	if kind, ok := ClassifyAuthError(err); ok {
		t.Fatalf("billing ProviderError classified as auth from stale attempt reason: %q", kind)
	}
}

func TestClassifyAuthError_OuterProviderErrorPrecedesNestedFallback(t *testing.T) {
	nested := &FallbackExhaustedError{Attempts: []FallbackAttempt{{
		Reason: FailoverAuth,
		Error:  &ProviderError{Kind: ProviderErrorAuthentication},
	}}}
	err := &ProviderError{Kind: ProviderErrorBilling, Cause: nested}
	if kind, ok := ClassifyAuthError(err); ok {
		t.Fatalf("outer billing ProviderError classified from nested auth aggregate: %q", kind)
	}
}

func TestAttemptIsAuthFailure_ZeroKindUsesStructuredStatus(t *testing.T) {
	if !attemptIsAuthFailure(FallbackAttempt{Error: &ProviderError{HTTPStatus: 401}}) {
		t.Fatal("zero-kind ProviderError with 401 status was not classified as auth")
	}
}

const httpStatusUnauthorized = 401
