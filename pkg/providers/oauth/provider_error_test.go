package oauthprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

func TestCodexMalformedStreamIsTransientAndDoesNotReturnPartialToolCalls(t *testing.T) {
	for _, malformed := range []string{`{"type":"response.completed","response":`, `{"type":invalid}`} {
		t.Run(malformed, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if calls.Add(1) == 1 {
					_, _ = fmt.Fprint(w, "event: response.output_item.done\n"+
						`data: {"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"renew","arguments":"{}"}}`+"\n\n")
					_, _ = fmt.Fprint(w, "event: response.completed\ndata: "+malformed+"\n\n")
					return
				}
				_, _ = fmt.Fprint(w, "event: response.completed\n"+
					`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp-ok","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"verified result","annotations":[]}]}]}}`+"\n\n")
			}))
			defer server.Close()
			provider := NewCodexProvider("test-token", "acc-123")
			provider.client = createOpenAITestClient(server.URL, "test-token", "acc-123")
			messages := []Message{{Role: "user", Content: "Report the already-completed action."}}
			response, err := provider.Chat(t.Context(), messages, nil, "gpt-5.3-codex", nil)
			var providerErr *providererrors.ProviderError
			var syntaxErr *json.SyntaxError
			if response != nil || !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindTransient ||
				!errors.As(err, &syntaxErr) {
				t.Fatalf("malformed stream = %#v, %T %v", response, err, err)
			}
			if strings.Contains(err.Error(), malformed) || strings.Contains(err.Error(), "call_1") {
				t.Fatal("safe provider error exposed response contents")
			}
			response, err = provider.Chat(t.Context(), messages, nil, "gpt-5.3-codex", nil)
			if err != nil || response == nil || response.Content != "verified result" ||
				len(response.ToolCalls) != 0 || calls.Load() != 2 {
				t.Fatalf("subsequent response = %#v, %v; requests=%d", response, err, calls.Load())
			}
		})
	}
}

func TestCodexProtocolClassificationPreservesCancellationAndUnknownErrors(t *testing.T) {
	parseErr := json.Unmarshal([]byte(`{"private_response":`), new(any))
	wrapped := fmt.Errorf("SDK decode: %w", parseErr)
	var providerErr *providererrors.ProviderError
	if err := normalizeCodexError(wrapped); !errors.As(err, &providerErr) ||
		providerErr.Kind != providererrors.KindTransient || !errors.Is(err, parseErr) {
		t.Fatalf("wrapped syntax error = %v", err)
	}
	if err := normalizeCodexError(errors.Join(context.Canceled, parseErr)); !errors.As(err, &providerErr) ||
		providerErr.Kind != providererrors.KindCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parse = %v", err)
	}
	unknown := errors.New("unexpected end of JSON input")
	if err := normalizeCodexError(unknown); errors.As(err, &providerErr) || !errors.Is(err, unknown) {
		t.Fatalf("untyped local error was incorrectly made retryable: %v", err)
	}
}

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

func TestCodexImageGenerationPreservesTopLevelDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			t.Errorf("path = %q, want /images/generations", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"detail":"image model is not available for this account"}`)
	}))
	defer server.Close()

	provider := NewCodexProvider("test-token", "acc-123")
	provider.client = createOpenAITestClient(server.URL, "test-token", "acc-123")
	_, err := provider.GenerateImage(t.Context(), ImageGenerationRequest{
		Prompt: "test",
		Model:  "gpt-image-2",
		Count:  1,
	})
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T, want ProviderError", err)
	}
	if providerErr.Kind != providererrors.KindInvalidRequest || providerErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("ProviderError = %#v, want invalid_request status 400", providerErr)
	}
	if providerErr.SafeMessage != "image model is not available for this account" {
		t.Fatalf("SafeMessage = %q, want backend detail", providerErr.SafeMessage)
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

func TestCodexIncompleteResponseDoesNotReturnPartialText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(
			w,
			"event: response.output_text.delta\n"+
				`data: {"type":"response.output_text.delta","sequence_number":1,"delta":"partial"}`+"\n\n",
		)
		_, _ = fmt.Fprint(w, "event: response.incomplete\n")
		_, _ = fmt.Fprint(
			w,
			`data: {"type":"response.incomplete","sequence_number":2,"response":{"id":"resp-incomplete","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`+"\n\n",
		)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewCodexProvider("test-token", "acc-123")
	provider.client = createOpenAITestClient(server.URL, "test-token", "acc-123")
	resp, err := provider.Chat(
		t.Context(),
		[]Message{{Role: "user", Content: "hello"}},
		nil,
		"gpt-5.3-codex",
		map[string]any{},
	)
	if resp != nil {
		t.Fatalf("response = %#v, want nil for incomplete terminal", resp)
	}
	var providerErr *providererrors.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindInvalidRequest {
		t.Fatalf("error = %#v, want invalid-request ProviderError", err)
	}
}

func TestCodexTopLevelErrorEventPreservesClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: error\n")
		_, _ = fmt.Fprint(
			w,
			`data: {"type":"error","sequence_number":1,"code":"rate_limit_exceeded","message":"request rate limited","param":null}`+"\n\n",
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
	if !errors.As(err, &providerErr) || providerErr.Kind != providererrors.KindRateLimit {
		t.Fatalf("error = %#v, want rate-limit ProviderError", err)
	}
	if providerErr.SafeMessage != "request rate limited" {
		t.Fatalf("SafeMessage = %q, want provider event message", providerErr.SafeMessage)
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
