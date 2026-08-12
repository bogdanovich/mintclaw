package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/providers/common"
)

type stubNetError struct {
	msg     string
	timeout bool
}

func (e stubNetError) Error() string   { return e.msg }
func (e stubNetError) Timeout() bool   { return e.timeout }
func (e stubNetError) Temporary() bool { return false }

func TestClassifyError_Nil(t *testing.T) {
	result := ClassifyError(nil, "openai", "gpt-4")
	if result != nil {
		t.Errorf("expected nil for nil error, got %+v", result)
	}
}

func TestClassifyError_ContextCanceled(t *testing.T) {
	for _, err := range []error{context.Canceled, fmt.Errorf("provider call: %w", context.Canceled)} {
		result := ClassifyError(err, "openai", "gpt-4")
		if result != nil {
			t.Errorf("expected nil for context.Canceled (user abort), got %+v", result)
		}
	}
}

func TestClassifyError_ContextDeadlineExceeded(t *testing.T) {
	result := ClassifyError(context.DeadlineExceeded, "openai", "gpt-4")
	if result == nil {
		t.Fatal("expected non-nil for deadline exceeded")
	}
	if result.Reason != FailoverTimeout {
		t.Errorf("reason = %q, want timeout", result.Reason)
	}
}

func TestClassifyError_ProviderErrorPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		err        *ProviderError
		wantReason FailoverReason
		wantNil    bool
	}{
		{
			name: "explicit kind precedes conflicting status and message",
			err: &ProviderError{
				Kind: ProviderErrorBilling, HTTPStatus: 401, SafeMessage: "rate limit exceeded",
			},
			wantReason: FailoverBilling,
		},
		{
			name: "status precedes compatibility message",
			err: &ProviderError{
				Kind: ProviderErrorUnknown, HTTPStatus: 401, SafeMessage: "billing balance exhausted",
			},
			wantReason: FailoverAuth,
		},
		{
			name: "structured unknown does not use text fallback",
			err: &ProviderError{
				Kind: ProviderErrorUnknown, SafeMessage: "rate limit exceeded",
			},
			wantNil: true,
		},
		{
			name: "structured cancellation never falls back",
			err: &ProviderError{
				Kind: ProviderErrorCanceled, HTTPStatus: 503, SafeMessage: "server unavailable",
			},
			wantNil: true,
		},
		{
			name: "explicit billing precedes wrapped cancellation",
			err: &ProviderError{
				Kind: ProviderErrorBilling, Cause: context.Canceled,
			},
			wantReason: FailoverBilling,
		},
		{
			name: "explicit cancellation precedes wrapped deadline",
			err: &ProviderError{
				Kind: ProviderErrorCanceled, Cause: context.DeadlineExceeded,
			},
			wantNil: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyError(test.err, "provider", "model")
			if test.wantNil {
				if got != nil {
					t.Fatalf("ClassifyError() = %+v, want nil", got)
				}
				return
			}
			if got == nil || got.Reason != test.wantReason || got.Status != test.err.HTTPStatus {
				t.Fatalf("ClassifyError() = %+v, want reason %q status %d", got, test.wantReason, test.err.HTTPStatus)
			}
		})
	}
}

func TestClassifyError_PreservesProviderErrorMetadata(t *testing.T) {
	providerErr := &ProviderError{
		Kind:        ProviderErrorRateLimit,
		HTTPStatus:  429,
		RetryAfter:  2 * time.Second,
		RequestID:   "req-provider-1",
		SafeMessage: "request rate limited",
		Cause:       errors.New("sdk cause"),
	}
	classified := ClassifyError(fmt.Errorf("chat failed: %w", providerErr), "openai", "gpt-5")
	if classified == nil {
		t.Fatal("ClassifyError() returned nil")
	}
	var got *ProviderError
	if !errors.As(classified, &got) || got != providerErr {
		t.Fatalf("classified error lost ProviderError metadata: %+v", classified)
	}
	if classified.ClassificationSource != ClassificationProviderStructured {
		t.Fatalf("classification source = %q, want provider_structured", classified.ClassificationSource)
	}

	diagnostic := (FallbackAttempt{Error: classified}).Diagnostic(true)
	if diagnostic.ClassificationSource != ClassificationProviderStructured ||
		diagnostic.ProviderErrorKind != string(ProviderErrorRateLimit) ||
		diagnostic.HTTPStatus != 429 || diagnostic.RetryAfter != 2*time.Second ||
		diagnostic.RequestID != "req-provider-1" || diagnostic.Message != "request rate limited" {
		t.Fatalf("structured diagnostic = %#v", diagnostic)
	}
}

func TestFallbackAttemptDiagnosticRedactsHeuristicError(t *testing.T) {
	secret := "sk-secret-that-must-not-appear"
	err := errors.New(
		`received error while streaming: {"type":"error","code":"rate_limit_exceeded",` +
			`"message":"request rate limited","authorization":"Bearer ` + secret + `"}`,
	)
	classified := ClassifyError(err, "openai", "gpt-test")
	if classified == nil || classified.Reason != FailoverRateLimit {
		t.Fatalf("ClassifyError() = %#v, want rate limit", classified)
	}

	diagnostic := (FallbackAttempt{Error: classified}).Diagnostic(true)
	if diagnostic.ClassificationSource != ClassificationMessagePattern {
		t.Fatalf("classification source = %q, want message_pattern", diagnostic.ClassificationSource)
	}
	if !strings.Contains(diagnostic.Message, `"code":"rate_limit_exceeded"`) {
		t.Fatalf("diagnostic message omitted provider code: %q", diagnostic.Message)
	}
	if strings.Contains(diagnostic.Message, secret) || !strings.Contains(diagnostic.Message, "[REDACTED]") {
		t.Fatalf("diagnostic message was not redacted: %q", diagnostic.Message)
	}
}

func TestFallbackAttemptDiagnosticBoundsStructuredMetadata(t *testing.T) {
	secret := "sk-secret-that-must-not-appear"
	providerErr := &ProviderError{
		Kind:        ProviderErrorRateLimit,
		RequestID:   secret,
		SafeMessage: secret + " " + strings.Repeat("界", 100),
	}
	diagnostic := (FallbackAttempt{Error: providerErr}).Diagnostic(true)
	if diagnostic.RequestID != "[REDACTED]" {
		t.Fatalf("request ID = %q, want redacted", diagnostic.RequestID)
	}
	if len(diagnostic.Message) > 240 || !utf8.ValidString(diagnostic.Message) {
		t.Fatalf(
			"diagnostic message is not a valid 240-byte preview: len=%d %q",
			len(diagnostic.Message),
			diagnostic.Message,
		)
	}
	if strings.Contains(diagnostic.Message, secret) {
		t.Fatalf("diagnostic message leaked secret: %q", diagnostic.Message)
	}
}

func TestFallbackAttemptDiagnosticBoundsHeuristicMetadata(t *testing.T) {
	classified := &FailoverError{
		Reason:               FailoverRateLimit,
		ClassificationSource: ClassificationMessagePattern,
		Wrapped:              errors.New(strings.Repeat("界", 100)),
	}
	diagnostic := (FallbackAttempt{Error: classified}).Diagnostic(true)
	if len(diagnostic.Message) > 240 || !utf8.ValidString(diagnostic.Message) {
		t.Fatalf(
			"diagnostic message is not a valid 240-byte preview: len=%d %q",
			len(diagnostic.Message),
			diagnostic.Message,
		)
	}
}

func TestFallbackAttemptDiagnosticNormalizesShortMalformedUTF8(t *testing.T) {
	malformed := string([]byte{'b', 'a', 'd', 0xff})
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "structured",
			err:  &ProviderError{Kind: ProviderErrorRateLimit, SafeMessage: malformed},
		},
		{
			name: "heuristic",
			err: &FailoverError{
				Reason: FailoverRateLimit, ClassificationSource: ClassificationMessagePattern,
				Wrapped: errors.New(malformed),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diagnostic := (FallbackAttempt{Error: test.err}).Diagnostic(true)
			if len(diagnostic.Message) > 240 || !utf8.ValidString(diagnostic.Message) {
				t.Fatalf(
					"diagnostic message is not a valid 240-byte preview: len=%d %q",
					len(diagnostic.Message),
					diagnostic.Message,
				)
			}
		})
	}
}

func TestFallbackAttemptDiagnosticMetadataOnlyDoesNotMaterializeMessage(t *testing.T) {
	providerErr := &ProviderError{
		Kind:        ProviderErrorRateLimit,
		HTTPStatus:  429,
		RequestID:   "req-safe-id",
		SafeMessage: "provider text must not enter the event bus",
	}
	diagnostic := (FallbackAttempt{Error: providerErr}).Diagnostic(false)
	if diagnostic.Message != "" {
		t.Fatalf("metadata-only diagnostic message = %q, want empty", diagnostic.Message)
	}
	if diagnostic.ProviderErrorKind != string(ProviderErrorRateLimit) ||
		diagnostic.HTTPStatus != 429 || diagnostic.RequestID != "req-safe-id" {
		t.Fatalf("metadata-only structured fields = %#v", diagnostic)
	}
}

func TestFallbackAttemptDiagnosticUsesCanonicalBoundedRedaction(t *testing.T) {
	secrets := []string{
		"Basic dXNlcjpzdXBlcnNlY3JldA==",
		"PASSWORD=super-secret-value",
		"github_pat_1234567890abcdefghijklmnop",
		"xox" + "b-12345678-abcdefghijklmnop",
		"AKIA1234567890ABCDEF",
		"eyJabcdefgh.ijklmnop.qrstuvwx",
		"https://admin:hunter2@example.com/path?token=secret-value",
		"-----BEGIN PRIVATE KEY-----\nsecret-key-material\n-----END PRIVATE KEY-----",
	}
	for _, secret := range secrets {
		diagnostic := (FallbackAttempt{Error: &ProviderError{
			Kind: ProviderErrorRateLimit, SafeMessage: "failure " + secret,
		}}).Diagnostic(true)
		if strings.Contains(diagnostic.Message, secret) {
			t.Fatalf("diagnostic leaked %q in %q", secret, diagnostic.Message)
		}
		if len(diagnostic.Message) > 240 || !utf8.ValidString(diagnostic.Message) {
			t.Fatalf("invalid bounded diagnostic: len=%d %q", len(diagnostic.Message), diagnostic.Message)
		}
	}

	oversized := strings.Repeat("x", 1<<20)
	diagnostic := (FallbackAttempt{Error: &ProviderError{
		Kind: ProviderErrorRateLimit, SafeMessage: oversized,
	}}).Diagnostic(true)
	if len(diagnostic.Message) > 240 || !strings.Contains(diagnostic.Message, "TRUNCATED") {
		t.Fatalf("oversized diagnostic was not pre-bounded: len=%d %q", len(diagnostic.Message), diagnostic.Message)
	}
}

func TestClassifyError_StatusCodes(t *testing.T) {
	tests := []struct {
		status int
		reason FailoverReason
	}{
		{401, FailoverAuth},
		{403, FailoverAuth},
		{402, FailoverBilling},
		{408, FailoverTimeout},
		{429, FailoverRateLimit},
		{400, FailoverFormat},
		{500, FailoverTimeout},
		{502, FailoverTimeout},
		{503, FailoverTimeout},
		{521, FailoverTimeout},
		{522, FailoverTimeout},
		{523, FailoverTimeout},
		{524, FailoverTimeout},
		{529, FailoverTimeout},
	}

	for _, tt := range tests {
		err := fmt.Errorf("API error: status: %d something went wrong", tt.status)
		result := ClassifyError(err, "test", "model")
		if result == nil {
			t.Errorf("status %d: expected non-nil", tt.status)
			continue
		}
		if result.Reason != tt.reason {
			t.Errorf("status %d: reason = %q, want %q", tt.status, result.Reason, tt.reason)
		}
	}
}

func TestClassifyError_RepresentativeProviderBodies(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		err      error
		reason   FailoverReason
		status   int
	}{
		{
			name:     "openai quota exceeded",
			provider: "openai",
			model:    "gpt-4o",
			err: errors.New(`API request failed:
  Status: 429
  Body:   {"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`),
			reason: FailoverRateLimit,
			status: 429,
		},
		{
			name:     "codex openai compatible server error",
			provider: "openai",
			model:    "codex-mini-latest",
			err: errors.New(`API request failed:
  Body:   {"error":{"message":"The server had an error while processing your request.","type":"server_error","code":"server_error"}}`),
			reason: FailoverTimeout,
		},
		{
			name:     "openrouter auth expired",
			provider: "openrouter",
			model:    "anthropic/claude-sonnet-4",
			err: &common.HTTPError{
				StatusCode:  401,
				BodyPreview: `{"error":{"message":"OAuth token has expired. Please re-authenticate.","code":401}}`,
			},
			reason: FailoverAuth,
			status: 401,
		},
		{
			name:     "anthropic billing credits",
			provider: "anthropic",
			model:    "claude-sonnet-4",
			err: errors.New(
				`{"type":"error","error":{"type":"billing_error","message":"Your credit balance is too low. Please visit Plans & Billing."}}`,
			),
			reason: FailoverBilling,
		},
		{
			name:     "gemini resource exhausted",
			provider: "gemini",
			model:    "gemini-2.5-pro",
			err: errors.New(
				`rpc error: code = ResourceExhausted desc = Quota exceeded for quota metric 'Generate requests' and limit 'GenerateRequestsPerMinute'`,
			),
			reason: FailoverRateLimit,
		},
		{
			name:     "openai context overflow inside bad request",
			provider: "openai",
			model:    "gpt-4o",
			err: &common.HTTPError{
				StatusCode:  400,
				BodyPreview: `{"error":{"message":"This model's maximum context length is 128000 tokens. Please reduce your prompt.","type":"invalid_request_error","code":"context_length_exceeded"}}`,
			},
			reason: FailoverContextOverflow,
			status: 400,
		},
		{
			name:     "anthropic unsupported image format",
			provider: "anthropic",
			model:    "claude-sonnet-4",
			err: errors.New(
				`{"type":"error","error":{"type":"invalid_request_error","message":"unsupported image format: image/tiff"}}`,
			),
			reason: FailoverFormat,
		},
		{
			name:     "network reset",
			provider: "openai",
			model:    "gpt-4o",
			err: errors.New(
				`Post "https://api.openai.com/v1/responses": read tcp 10.0.0.1:12345->104.18.33.45:443: read: connection reset by peer`,
			),
			reason: FailoverNetwork,
		},
		{
			name:     "timeout",
			provider: "openrouter",
			model:    "openai/gpt-4o",
			err: errors.New(
				`Post "https://openrouter.ai/api/v1/chat/completions": context deadline exceeded`,
			),
			reason: FailoverTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClassifyError(tt.err, tt.provider, tt.model)
			if result == nil {
				t.Fatal("expected non-nil")
			}
			if result.Reason != tt.reason {
				t.Fatalf("reason = %q, want %q", result.Reason, tt.reason)
			}
			if result.Status != tt.status {
				t.Fatalf("status = %d, want %d", result.Status, tt.status)
			}
			if result.Provider != tt.provider {
				t.Fatalf("provider = %q, want %q", result.Provider, tt.provider)
			}
			if result.Model != tt.model {
				t.Fatalf("model = %q, want %q", result.Model, tt.model)
			}
		})
	}
}

func TestClassifyError_RateLimitPatterns(t *testing.T) {
	patterns := []string{
		"rate limit exceeded",
		"rate_limit reached",
		"too many requests",
		"exceeded your current quota",
		"resource has been exhausted",
		"resource_exhausted",
		"quota exceeded",
		"usage limit reached",
	}

	for _, msg := range patterns {
		err := errors.New(msg)
		result := ClassifyError(err, "openai", "gpt-4")
		if result == nil {
			t.Errorf("pattern %q: expected non-nil", msg)
			continue
		}
		if result.Reason != FailoverRateLimit {
			t.Errorf("pattern %q: reason = %q, want rate_limit", msg, result.Reason)
		}
	}
}

func TestClassifyError_OverloadedPatterns(t *testing.T) {
	patterns := []string{
		"overloaded_error",
		`{"type": "overloaded_error"}`,
		"server is overloaded",
	}

	for _, msg := range patterns {
		err := errors.New(msg)
		result := ClassifyError(err, "anthropic", "claude")
		if result == nil {
			t.Errorf("pattern %q: expected non-nil", msg)
			continue
		}
		// Overloaded is treated as rate_limit
		if result.Reason != FailoverRateLimit {
			t.Errorf("pattern %q: reason = %q, want rate_limit", msg, result.Reason)
		}
	}
}

func TestClassifyError_BillingPatterns(t *testing.T) {
	patterns := []string{
		"payment required",
		"insufficient credits",
		"credit balance too low",
		"plans & billing page",
		"insufficient balance",
	}

	for _, msg := range patterns {
		err := errors.New(msg)
		result := ClassifyError(err, "openai", "gpt-4")
		if result == nil {
			t.Errorf("pattern %q: expected non-nil", msg)
			continue
		}
		if result.Reason != FailoverBilling {
			t.Errorf("pattern %q: reason = %q, want billing", msg, result.Reason)
		}
	}
}

func TestClassifyError_TimeoutPatterns(t *testing.T) {
	patterns := []string{
		"request timeout",
		"connection timed out",
		"deadline exceeded",
		"context deadline exceeded",
	}

	for _, msg := range patterns {
		err := errors.New(msg)
		result := ClassifyError(err, "openai", "gpt-4")
		if result == nil {
			t.Errorf("pattern %q: expected non-nil", msg)
			continue
		}
		if result.Reason != FailoverTimeout {
			t.Errorf("pattern %q: reason = %q, want timeout", msg, result.Reason)
		}
	}
}

func TestClassifyError_NetworkPatterns(t *testing.T) {
	patterns := []string{
		`failed to send request: Post "https://example.com": tls: bad record MAC`,
		"read tcp 10.20.0.1:61279->172.65.90.20:443: read: connection reset by peer",
		"failed to send request: dial tcp 203.0.113.10:443: connect: connection refused",
		"tls handshake failure",
		"x509: certificate has expired or is not yet valid",
		"read tcp 127.0.0.1:443: read: unexpected EOF",
		"lookup api.example.com: no such host",
	}

	for _, msg := range patterns {
		err := errors.New(msg)
		result := ClassifyError(err, "openai", "gpt-4")
		if result == nil {
			t.Errorf("pattern %q: expected non-nil", msg)
			continue
		}
		if result.Reason != FailoverNetwork {
			t.Errorf("pattern %q: reason = %q, want network", msg, result.Reason)
		}
	}
}

func TestClassifyError_NetworkTypes(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "wrapped EOF",
			err: &url.Error{
				Op:  "Post",
				URL: "https://example.com",
				Err: io.EOF,
			},
		},
		{
			name: "dns error",
			err: &net.DNSError{
				Err:  "no such host",
				Name: "api.example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClassifyError(tt.err, "openai", "gpt-4")
			if result == nil {
				t.Fatal("expected non-nil")
			}
			if result.Reason != FailoverNetwork {
				t.Fatalf("reason = %q, want network", result.Reason)
			}
		})
	}
}

func TestClassifyError_TimeoutNetworkTypes(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "wrapped syscall timeout",
			err:  fmt.Errorf("dial tcp: %w", syscall.ETIMEDOUT),
		},
		{
			name: "net error timeout",
			err: &url.Error{
				Op:  "Post",
				URL: "https://example.com",
				Err: stubNetError{msg: "i/o timeout", timeout: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClassifyError(tt.err, "openai", "gpt-4")
			if result == nil {
				t.Fatal("expected non-nil")
			}
			if result.Reason != FailoverTimeout {
				t.Fatalf("reason = %q, want timeout", result.Reason)
			}
		})
	}
}

func TestClassifyError_TimeoutPatternsWinOverNetworkContext(t *testing.T) {
	patterns := []string{
		`failed to send request: Post "https://example.com": dial tcp 203.0.113.10:443: i/o timeout`,
		`read tcp 10.20.0.1:61279->172.65.90.20:443: i/o timeout`,
	}

	for _, msg := range patterns {
		err := errors.New(msg)
		result := ClassifyError(err, "openai", "gpt-4")
		if result == nil {
			t.Errorf("pattern %q: expected non-nil", msg)
			continue
		}
		if result.Reason != FailoverTimeout {
			t.Errorf("pattern %q: reason = %q, want timeout", msg, result.Reason)
		}
	}
}

func TestClassifyError_NetworkPatternsWinOverAuthExpired(t *testing.T) {
	err := errors.New(
		`Post "https://example.com": tls: failed to verify certificate: x509: certificate has expired or is not yet valid`,
	)
	result := ClassifyError(err, "openai", "gpt-4")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result.Reason != FailoverNetwork {
		t.Fatalf("reason = %q, want network", result.Reason)
	}
}

func TestClassifyError_AuthPatterns(t *testing.T) {
	patterns := []string{
		"invalid api key",
		"invalid_api_key",
		"incorrect api key",
		"invalid token",
		"authentication failed",
		"re-authenticate",
		"oauth token refresh failed",
		"unauthorized access",
		"forbidden",
		"access denied",
		"expired",
		"token has expired",
		"no credentials found",
		"no api key found",
	}

	for _, msg := range patterns {
		err := errors.New(msg)
		result := ClassifyError(err, "openai", "gpt-4")
		if result == nil {
			t.Errorf("pattern %q: expected non-nil", msg)
			continue
		}
		if result.Reason != FailoverAuth {
			t.Errorf("pattern %q: reason = %q, want auth", msg, result.Reason)
		}
	}
}

func TestClassifyError_FormatPatterns(t *testing.T) {
	patterns := []string{
		"string should match pattern",
		"tool_use.id is required",
		"invalid tool_use_id",
		"messages.1.content.1.tool_use.id must be valid",
		"invalid request format",
	}

	for _, msg := range patterns {
		err := errors.New(msg)
		result := ClassifyError(err, "anthropic", "claude")
		if result == nil {
			t.Errorf("pattern %q: expected non-nil", msg)
			continue
		}
		if result.Reason != FailoverFormat {
			t.Errorf("pattern %q: reason = %q, want format", msg, result.Reason)
		}
	}
}

func TestClassifyError_ImageDimensionError(t *testing.T) {
	err := errors.New("image dimensions exceed max allowed 2048x2048")
	result := ClassifyError(err, "openai", "gpt-4o")
	if result == nil {
		t.Fatal("expected non-nil for image dimension error")
	}
	if result.Reason != FailoverFormat {
		t.Errorf("reason = %q, want format", result.Reason)
	}
	if result.IsRetriable() {
		t.Error("image dimension error should not be retriable")
	}
}

func TestClassifyError_ContextOverflowPatterns(t *testing.T) {
	patterns := []string{
		"context_length_exceeded",
		"context_window_exceeded",
		"maximum context length",
		"token limit",
		"too many tokens",
		"prompt is too long",
		"request too large",
	}

	for _, msg := range patterns {
		err := errors.New(msg)
		result := ClassifyError(err, "openai", "gpt-4")
		if result == nil {
			t.Errorf("pattern %q: expected non-nil", msg)
			continue
		}
		if result.Reason != FailoverContextOverflow {
			t.Errorf("pattern %q: reason = %q, want context_overflow", msg, result.Reason)
		}
	}
}

func TestClassifyError_ImageSizeError(t *testing.T) {
	err := errors.New("image exceeds 20 mb limit")
	result := ClassifyError(err, "openai", "gpt-4o")
	if result == nil {
		t.Fatal("expected non-nil for image size error")
	}
	if result.Reason != FailoverFormat {
		t.Errorf("reason = %q, want format", result.Reason)
	}
}

func TestClassifyError_UnknownError(t *testing.T) {
	err := errors.New("some completely random error")
	result := ClassifyError(err, "openai", "gpt-4")
	if result != nil {
		t.Errorf("expected nil for unknown error, got %+v", result)
	}
}

func TestClassifyError_UnknownProviderBodyDoesNotFallback(t *testing.T) {
	err := &common.HTTPError{
		StatusCode:  418,
		BodyPreview: `{"error":{"message":"model brewed an unexpected response","type":"teapot"}}`,
	}
	result := ClassifyError(err, "openrouter", "unknown/model")
	if result != nil {
		t.Fatalf("expected nil for unknown provider body, got %+v", result)
	}
}

func TestClassifyError_ProviderModelPropagation(t *testing.T) {
	err := errors.New("rate limit exceeded")
	result := ClassifyError(err, "my-provider", "my-model")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result.Provider != "my-provider" {
		t.Errorf("provider = %q, want my-provider", result.Provider)
	}
	if result.Model != "my-model" {
		t.Errorf("model = %q, want my-model", result.Model)
	}
}

func TestFailoverError_IsRetriable(t *testing.T) {
	tests := []struct {
		reason    FailoverReason
		retriable bool
	}{
		{FailoverAuth, false},
		{FailoverRateLimit, true},
		{FailoverBilling, true},
		{FailoverNetwork, true},
		{FailoverTimeout, true},
		{FailoverOverloaded, true},
		{FailoverFormat, false},
		{FailoverContextOverflow, false},
		{FailoverUnknown, true},
	}

	for _, tt := range tests {
		fe := &FailoverError{Reason: tt.reason}
		if fe.IsRetriable() != tt.retriable {
			t.Errorf("IsRetriable(%q) = %v, want %v", tt.reason, fe.IsRetriable(), tt.retriable)
		}
	}
}

func TestFailoverError_ErrorString(t *testing.T) {
	longRaw := strings.Join([]string{
		"too many requests",
		"Authorization: Bearer secret-token-123",
		"api_key=sk-proj-openaiProjectKeyShouldNotLeak1234567890",
		`"access_token":"sk-ant-api03-anthropicKeyShouldNotLeak1234567890"`,
		"provider_key=gsk_groqKeyShouldNotLeak1234567890",
		"google_key=AIzaGoogleKeyShouldNotLeak1234567890",
	}, " ") + strings.Repeat("x", 300)
	fe := &FailoverError{
		Reason:   FailoverRateLimit,
		Provider: "openai",
		Model:    "gpt-4",
		Status:   429,
		Wrapped:  errors.New(longRaw),
	}
	s := fe.Error()
	for _, want := range []string{
		"provider=openai",
		"model=gpt-4",
		"status=429",
		"classification=rate_limit",
		"raw_error=",
		"too many requests",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("error string %q missing %q", s, want)
		}
	}
	if strings.Contains(s, "\n") {
		t.Fatalf("error string should normalize whitespace, got %q", s)
	}
	for _, leaked := range []string{
		"secret-token-123",
		"sk-proj-openaiProjectKeyShouldNotLeak1234567890",
		"sk-ant-api03-anthropicKeyShouldNotLeak1234567890",
		"gsk_groqKeyShouldNotLeak1234567890",
		"AIzaGoogleKeyShouldNotLeak1234567890",
	} {
		if strings.Contains(s, leaked) {
			t.Fatalf("error string should redact %q, got %q", leaked, s)
		}
	}
	if len(s) > 360 {
		t.Fatalf("error string should include only a bounded raw preview, len=%d: %q", len(s), s)
	}
}

func TestFailoverError_Unwrap(t *testing.T) {
	inner := errors.New("inner error")
	fe := &FailoverError{Reason: FailoverTimeout, Wrapped: inner}
	if fe.Unwrap() != inner { //nolint:errorlint // direct Unwrap identity is the contract under test
		t.Error("Unwrap should return wrapped error")
	}
}

func TestExtractHTTPStatus(t *testing.T) {
	tests := []struct {
		msg  string
		want int
	}{
		{"status: 429 rate limited", 429},
		{"status 401 unauthorized", 401},
		{"http/1.1 502 bad gateway", 502},
		{"error 429", 429},
		{"no status code here", 0},
		{"random number 12345", 0},
	}

	for _, tt := range tests {
		got := extractHTTPStatus(tt.msg)
		if got != tt.want {
			t.Errorf("extractHTTPStatus(%q) = %d, want %d", tt.msg, got, tt.want)
		}
	}
}

func TestIsImageDimensionError(t *testing.T) {
	if !IsImageDimensionError("image dimensions exceed max 4096x4096") {
		t.Error("should match image dimensions exceed max")
	}
	if IsImageDimensionError("normal error message") {
		t.Error("should not match normal error")
	}
}

func TestIsImageSizeError(t *testing.T) {
	if !IsImageSizeError("image exceeds 20 mb") {
		t.Error("should match image exceeds mb")
	}
	if IsImageSizeError("normal error message") {
		t.Error("should not match normal error")
	}
}
