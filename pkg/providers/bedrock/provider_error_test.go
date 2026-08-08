//go:build bedrock

package bedrock

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func TestNormalizeProviderErrorContracts(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		message string
		status  int
		want    providererrors.Kind
	}{
		{name: "authentication", code: "AccessDeniedException", status: 403, want: providererrors.KindAuthentication},
		{name: "billing", code: "SubscriptionRequiredException", status: 403, want: providererrors.KindBilling},
		{name: "rate limit", code: "ThrottlingException", status: 429, want: providererrors.KindRateLimit},
		{
			name:    "context overflow compatibility",
			code:    "ValidationException",
			message: "Input is too long for requested model context window",
			status:  400,
			want:    providererrors.KindContextOverflow,
		},
		{name: "timeout", code: "ModelTimeoutException", status: 408, want: providererrors.KindTimeout},
		{name: "transient", code: "ModelNotReadyException", status: 429, want: providererrors.KindTransient},
		{
			name:    "invalid",
			code:    "ValidationException",
			message: "bad tool schema",
			status:  400,
			want:    providererrors.KindInvalidRequest,
		},
		{name: "status fallback", code: "NewServerFailure", status: 503, want: providererrors.KindTransient},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cause := bedrockResponseError(tt.code, tt.message, tt.status)
			err := normalizeProviderError(cause)
			var providerErr *providererrors.ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error type = %T, want *providererrors.ProviderError", err)
			}
			if providerErr.Kind != tt.want {
				t.Fatalf("Kind = %q, want %q", providerErr.Kind, tt.want)
			}
			if providerErr.HTTPStatus != tt.status {
				t.Fatalf("HTTPStatus = %d, want %d", providerErr.HTTPStatus, tt.status)
			}
			if providerErr.RequestID != "request-123" {
				t.Fatalf("RequestID = %q, want request-123", providerErr.RequestID)
			}
			if providerErr.RetryAfter != 9*time.Second {
				t.Fatalf("RetryAfter = %v, want 9s", providerErr.RetryAfter)
			}
			if !errors.Is(err, cause) {
				t.Fatal("normalized error does not preserve its cause")
			}
		})
	}
}

func TestNormalizeProviderErrorCancellation(t *testing.T) {
	err := normalizeProviderError(context.Canceled)
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindCanceled {
		t.Fatalf("error = %#v, want canceled ProviderError", err)
	}
}

func TestNormalizeProviderErrorSSOCompatibilityFallback(t *testing.T) {
	cause := errors.New("refresh cached SSO token failed")
	err := normalizeProviderError(cause)
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindAuthentication {
		t.Fatalf("error = %#v, want authentication ProviderError", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("normalized error does not preserve its cause")
	}

	nonSSO := normalizeProviderError(errors.New("security token expired"))
	if !errors.As(nonSSO, &providerErr) || providerErr.Kind != providererrors.KindUnknown {
		t.Fatalf("error = %#v, want unknown ProviderError", nonSSO)
	}
}

func TestNormalizeProviderErrorStructuredCodePrecedesStatus(t *testing.T) {
	err := normalizeProviderError(bedrockResponseError("AccountProblem", "throttled", http.StatusTooManyRequests))
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindBilling {
		t.Fatalf("error = %#v, want billing ProviderError", err)
	}
}

func bedrockResponseError(code, message string, status int) error {
	apiErr := &smithy.GenericAPIError{Code: code, Message: message, Fault: smithy.FaultClient}
	response := &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Retry-After": {"9"},
		},
	}
	return &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: response},
			Err:      apiErr,
		},
		RequestID: "request-123",
	}
}
