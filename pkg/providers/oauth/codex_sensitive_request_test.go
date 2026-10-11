package oauthprovider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

func TestCodexSensitiveRequestSuppressesUntrustedHTTPErrorDiagnostics(t *testing.T) {
	const sentinel = "MINTCLAW_SENSITIVE_HTTP_ECHO_79ef"
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error("invalid outbound request")
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", sentinel+"_request")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"type": "invalid_request_error", "message": sentinel},
		})
	}))
	defer server.Close()
	provider := NewCodexProvider("synthetic-token", "synthetic-account")
	provider.client = createOpenAITestClient(server.URL, "synthetic-token", "synthetic-account")
	provider.enableWebSearch = true
	previousLevel := logger.GetLevel()
	logger.SetLevel(logger.ERROR)
	defer func() {
		logger.DisableFileLogging()
		logger.SetLevel(previousLevel)
	}()
	for _, sensitive := range []bool{true, false} {
		t.Run(map[bool]string{true: "protected", false: "ordinary"}[sensitive], func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "provider.log")
			if err := logger.EnableFileLogging(logPath); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if sensitive {
				ctx = protocoltypes.WithSensitiveRequest(ctx)
			}
			options := map[string]any{"native_search": false}
			protocoltypes.DisablePromptCache(options)
			_, err := provider.Chat(ctx, []Message{
				{Role: "system", Content: "Inspect a protected synthetic value."},
				{Role: "user", Content: sentinel},
			}, nil, "gpt-6.1-sol", options)
			logger.DisableFileLogging()
			if err == nil {
				t.Fatal("HTTP error unexpectedly succeeded")
			}
			body := <-requests
			if tools, _ := body["tools"].([]any); len(tools) != 0 || body["prompt_cache_key"] != nil ||
				body["sensitive_request"] != nil || body["native_search"] != nil {
				t.Fatal("private request injected search/cache intent or serialized internal controls")
			}
			data, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			logs := string(data)
			if !strings.Contains(logs, "Codex API call failed") || !strings.Contains(logs, "error_kind") ||
				!strings.Contains(logs, `"status_code":400`) {
				t.Fatal("request lost safe failure diagnostics")
			}
			if sensitive {
				if strings.Contains(logs, sentinel) || strings.Contains(logs, "safe_message") ||
					strings.Contains(logs, "request_id") {
					t.Fatal("private provider response leaked into diagnostics")
				}
			} else if !strings.Contains(logs, sentinel) || !strings.Contains(logs, "safe_message") ||
				!strings.Contains(logs, "request_id") {
				t.Fatal("ordinary diagnostics were globally suppressed")
			}
		})
	}
}
