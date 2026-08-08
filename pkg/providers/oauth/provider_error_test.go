package oauthprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func TestCodexProviderErrorContract(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		errorType  string
		code       string
		message    string
		retryAfter string
		wantKind   providererrors.Kind
	}{
		{
			name: "authentication", status: http.StatusUnauthorized,
			errorType: "invalid_request_error", code: "invalid_api_key", message: "API key rejected",
			wantKind: providererrors.KindAuthentication,
		},
		{
			name: "billing precedes rate status and compatibility text", status: http.StatusTooManyRequests,
			errorType: "insufficient_quota", code: "insufficient_quota",
			message: "maximum context length reached", wantKind: providererrors.KindBilling,
		},
		{
			name: "rate limit", status: http.StatusTooManyRequests,
			errorType: "rate_limit_error", code: "rate_limit_exceeded",
			message: "request rate limited", retryAfter: "9", wantKind: providererrors.KindRateLimit,
		},
		{
			name: "context overflow", status: http.StatusBadRequest,
			errorType: "invalid_request_error", code: "context_length_exceeded",
			message: "request rejected", wantKind: providererrors.KindContextOverflow,
		},
		{
			name: "transient", status: http.StatusServiceUnavailable,
			errorType: "server_error", code: "server_error",
			message: "service unavailable", wantKind: providererrors.KindTransient,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("x-request-id", "req-codex-contract")
				if test.retryAfter != "" {
					w.Header().Set("Retry-After", test.retryAfter)
				}
				w.WriteHeader(test.status)
				_, _ = fmt.Fprintf(
					w,
					`{"error":{"type":%q,"code":%q,"message":%q,"param":null}}`,
					test.errorType,
					test.code,
					test.message,
				)
			}))
			defer server.Close()

			provider := NewCodexProvider("test-token", "acc-123")
			provider.client = createOpenAITestClient(server.URL, "test-token", "acc-123")
			_, err := provider.Chat(
				t.Context(),
				[]Message{{Role: "user", Content: "hello"}},
				nil,
				"gpt-5.3-codex",
				map[string]any{},
			)
			assertCodexProviderError(t, err, test.status, test.wantKind, test.retryAfter != "")
		})
	}
}

func TestNormalizeCodexTransportContract(t *testing.T) {
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
			normalized := normalizeCodexError(test.err)
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

func TestNormalizeCodexCredentialError(t *testing.T) {
	cause := errors.New("private credential diagnostics")
	err := normalizeCodexCredentialError(cause)
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

func TestCodexImageGenerationRequiresAccountIdentity(t *testing.T) {
	provider := NewCodexProvider("test-token", "")
	_, err := provider.GenerateImage(t.Context(), ImageGenerationRequest{Prompt: "test"})
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindAuthentication {
		t.Fatalf("error = %#v, want authentication ProviderError", err)
	}
}

func TestCodexFailedResponseEventContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.failed\n")
		_, _ = fmt.Fprint(
			w,
			`data: {"type":"response.failed","sequence_number":1,"response":{"id":"resp-failed","status":"failed","error":{"code":"server_error","message":"backend unavailable"}}}`+"\n\n",
		)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewCodexProvider("test-token", "acc-123")
	provider.client = createOpenAITestClient(server.URL, "test-token", "acc-123")
	_, err := provider.Chat(
		t.Context(),
		[]Message{{Role: "user", Content: "hello"}},
		nil,
		"gpt-5.3-codex",
		map[string]any{},
	)
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindTransient {
		t.Fatalf("error = %#v, want transient ProviderError", err)
	}
}

func TestCodexImageFailureEventContract(t *testing.T) {
	evt := responses.ResponseStreamEventUnion{
		Type: "response.failed",
		Response: responses.Response{
			Status: responses.ResponseStatusFailed,
			Error: responses.ResponseError{
				Code:    responses.ResponseErrorCodeRateLimitExceeded,
				Message: "request rate limited",
			},
		},
	}
	_, _, err := parseCodexImageEventUnion(evt, "png")
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindRateLimit {
		t.Fatalf("error = %#v, want rate-limit ProviderError", err)
	}
}

func assertCodexProviderError(
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
	if providerErr.RequestID != "req-codex-contract" {
		t.Fatalf("RequestID = %q, want req-codex-contract", providerErr.RequestID)
	}
	if wantRetry && providerErr.RetryAfter != 9*time.Second {
		t.Fatalf("RetryAfter = %s, want 9s", providerErr.RetryAfter)
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatal("ProviderError did not retain OpenAI SDK cause")
	}
}
