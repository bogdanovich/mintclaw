package providererrors

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFromHTTPResponse(t *testing.T) {
	cause := errors.New("raw response")
	tests := []struct {
		name           string
		status         int
		header         http.Header
		body           string
		wantKind       Kind
		wantRetryAfter time.Duration
		wantRequestID  string
		wantMessage    string
	}{
		{
			name:           "openai billing identifier precedes rate status",
			status:         http.StatusTooManyRequests,
			header:         http.Header{"Retry-After": {"12"}, "X-Request-Id": {"req-openai"}},
			body:           `{"error":{"message":"Quota exhausted","type":"insufficient_quota","code":"insufficient_quota"}}`,
			wantKind:       KindBilling,
			wantRetryAfter: 12 * time.Second,
			wantRequestID:  "req-openai",
			wantMessage:    "Quota exhausted",
		},
		{
			name:          "anthropic body request id",
			status:        http.StatusBadRequest,
			body:          `{"type":"error","error":{"type":"billing_error","message":"Credit balance is too low"},"request_id":"req-anthropic"}`,
			wantKind:      KindBilling,
			wantRequestID: "req-anthropic",
			wantMessage:   "Credit balance is too low",
		},
		{
			name:        "gemini structured context reason",
			status:      http.StatusBadRequest,
			body:        `{"error":{"code":400,"message":"Prompt rejected","status":"INVALID_ARGUMENT","details":[{"reason":"context_length_exceeded"}]}}`,
			wantKind:    KindContextOverflow,
			wantMessage: "Prompt rejected",
		},
		{
			name:        "gemini invalid API key reason precedes generic status",
			status:      http.StatusBadRequest,
			body:        `{"error":{"code":400,"message":"API key rejected","status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}`,
			wantKind:    KindAuthentication,
			wantMessage: "API key rejected",
		},
		{
			name:        "message text cannot override status",
			status:      http.StatusTooManyRequests,
			body:        `{"error":{"message":"billing balance exhausted"}}`,
			wantKind:    KindRateLimit,
			wantMessage: "billing balance exhausted",
		},
		{
			name:           "antigravity quota reset metadata",
			status:         http.StatusTooManyRequests,
			body:           `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"metadata":{"quotaResetDelay":"7s"}}]}}`,
			wantKind:       KindRateLimit,
			wantRetryAfter: 7 * time.Second,
			wantMessage:    "Too Many Requests",
		},
		{
			name:           "gemini standard retry info",
			status:         http.StatusTooManyRequests,
			body:           `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"13s"}]}}`,
			wantKind:       KindRateLimit,
			wantRetryAfter: 13 * time.Second,
			wantMessage:    "Too Many Requests",
		},
		{
			name:        "malformed transient response",
			status:      http.StatusServiceUnavailable,
			body:        `<html>unavailable</html>`,
			wantKind:    KindTransient,
			wantMessage: "Service Unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := FromHTTPResponse(test.status, test.header, []byte(test.body), cause)
			if got.Kind != test.wantKind || got.HTTPStatus != test.status {
				t.Fatalf(
					"FromHTTPResponse() = kind %q status %d, want %q %d",
					got.Kind,
					got.HTTPStatus,
					test.wantKind,
					test.status,
				)
			}
			if got.RetryAfter != test.wantRetryAfter || got.RequestID != test.wantRequestID {
				t.Fatalf(
					"metadata = retry %s request %q, want %s %q",
					got.RetryAfter,
					got.RequestID,
					test.wantRetryAfter,
					test.wantRequestID,
				)
			}
			if got.SafeMessage != test.wantMessage {
				t.Fatalf("SafeMessage = %q, want %q", got.SafeMessage, test.wantMessage)
			}
			if !errors.Is(got, cause) {
				t.Fatal("normalized error did not preserve cause")
			}
		})
	}
}

func TestFromHTTPResponseBoundsSafeMetadata(t *testing.T) {
	message := strings.Repeat("界", 300) + "\nsecret"
	body, err := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	providerErr := FromHTTPResponse(
		http.StatusBadRequest,
		http.Header{"X-Request-Id": {"request\nidentifier"}},
		body,
		nil,
	)

	if strings.ContainsAny(providerErr.SafeMessage, "\r\n") || len([]rune(providerErr.SafeMessage)) > 243 {
		t.Fatalf("SafeMessage is not bounded and single-line: %q", providerErr.SafeMessage)
	}
	if providerErr.RequestID != "request identifier" {
		t.Fatalf("RequestID = %q, want normalized value", providerErr.RequestID)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	value := now.Add(15 * time.Second).Format(http.TimeFormat)
	if got := parseRetryAfter(value, now); got != 15*time.Second {
		t.Fatalf("parseRetryAfter() = %s, want 15s", got)
	}
}
