package openai_compat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

func TestSensitiveStreamSuppressesUntrustedToolDiagnostics(t *testing.T) {
	const sentinel = "MINTCLAW_SENSITIVE_TOOL_ECHO_859d"
	payload, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{
				"tool_calls": []any{map[string]any{
					"index": 0, "id": "synthetic", "type": "function",
					"function": map[string]any{"name": sentinel, "arguments": "{"},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sse := "data: " + string(payload) + "\n\ndata: [DONE]\n\n"
	previousLevel := logger.GetLevel()
	logger.SetLevel(logger.WARN)
	defer func() {
		logger.DisableFileLogging()
		logger.SetLevel(previousLevel)
	}()
	for _, sensitive := range []bool{true, false} {
		t.Run(map[bool]string{true: "protected", false: "ordinary"}[sensitive], func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "stream.log")
			if err := logger.EnableFileLogging(logPath); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if sensitive {
				ctx = protocoltypes.WithSensitiveRequest(ctx)
			}
			response, parseErr := parseStreamResponse(ctx, io.NopCloser(strings.NewReader(sse)), nil)
			logger.DisableFileLogging()
			if parseErr != nil || response == nil || len(response.ToolCalls) != 1 {
				t.Fatal("provider parsing changed instead of scoping diagnostics")
			}
			data, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			logs := string(data)
			if !strings.Contains(logs, "failed to decode tool call arguments") {
				t.Fatal("safe warning disappeared")
			}
			if sensitive {
				if strings.Contains(logs, sentinel) || strings.Contains(logs, `"error"`) ||
					strings.Contains(logs, `"tool"`) {
					t.Fatal("untrusted tool diagnostics escaped the sensitive request")
				}
			} else if !strings.Contains(logs, sentinel) || !strings.Contains(logs, `"error"`) {
				t.Fatal("ordinary tool diagnostics were globally suppressed")
			}
		})
	}
}

func TestSensitiveRequestsRefuseOpaqueBodyOverridesBeforeSending(t *testing.T) {
	const sentinel = "MINTCLAW_SENSITIVE_OVERRIDE_PRIVATE_48a2"
	requests := make(chan map[string]any, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error("invalid outbound JSON")
		}
		requests <- body
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	overrides := []map[string]any{
		{"tools": []any{map[string]any{"type": "web_search_preview"}}, "prompt_cache_key": "ordinary-lineage"},
		{"messages": []any{map[string]any{"role": "user", "content": "unrelated history"}}, "model": "other-model"},
		{"vendor_search": true, "vendor_cache": "unknown-extension"},
		{"temperature": 0.7},
	}
	for _, extra := range overrides {
		for _, stream := range []bool{false, true} {
			provider := NewProvider("synthetic-key", server.URL, "", WithExtraBody(extra))
			messages := []Message{{Role: "system", Content: "Private audit"}, {Role: "user", Content: sentinel}}
			options := map[string]any{"native_search": false}
			protocoltypes.DisablePromptCache(options)
			call := provider.Chat
			if stream {
				call = func(ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any) (*LLMResponse, error) {
					return provider.ChatStreamEvents(ctx, messages, tools, model, options, nil)
				}
			}
			response, err := call(
				protocoltypes.WithSensitiveRequest(t.Context()),
				messages,
				nil,
				"gpt-6.1-sol",
				options,
			)
			if response != nil || !errors.Is(err, protocoltypes.ErrSensitiveRequestUnsupported) ||
				strings.Contains(err.Error(), sentinel) {
				t.Fatal("opaque overrides did not fail closed with a value-free refusal")
			}
			select {
			case <-requests:
				t.Fatal("sensitive request was sent before rejecting body overrides")
			default:
			}
			if response, err := call(
				t.Context(),
				messages,
				nil,
				"gpt-6.1-sol",
				options,
			); err != nil || response == nil ||
				response.Content != "ok" {
				t.Fatalf("ordinary override call changed: %v", err)
			}
			body := <-requests
			for key := range extra {
				if _, present := body[key]; !present {
					t.Fatalf("ordinary override %s was removed", key)
				}
			}
		}
	}
	// The refusal is a configuration boundary, not a blanket block on the
	// compatibility provider. A no-override private request remains supported.
	provider := NewProvider("synthetic-key", server.URL, "")
	options := map[string]any{"native_search": false}
	protocoltypes.DisablePromptCache(options)
	response, err := provider.Chat(protocoltypes.WithSensitiveRequest(t.Context()), []Message{
		{Role: "system", Content: "Private audit"}, {Role: "user", Content: sentinel},
	}, nil, "gpt-6.1-sol", options)
	if err != nil || response == nil {
		t.Fatalf("no-override private lane refused: %v", err)
	}
	body := <-requests
	if body["tools"] != nil || body["prompt_cache_key"] != nil || body["sensitive_request"] != nil {
		t.Fatal("private outbound controls changed")
	}
}
