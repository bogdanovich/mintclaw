package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestProviderFallbackDisablesDeepSeekThinkingForForeignToolHistory(t *testing.T) {
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "rate limit exceeded", "type": "rate_limit_error"},
		})
	}))
	defer primaryServer.Close()

	fallbackCalls := 0
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode DeepSeek fallback request: %v", err)
		}
		thinking, ok := body["thinking"].(map[string]any)
		if !ok || thinking["type"] != "disabled" {
			t.Fatalf("DeepSeek fallback thinking = %#v, want disabled", body["thinking"])
		}
		requestMessages, ok := body["messages"].([]any)
		if !ok {
			t.Fatalf("DeepSeek fallback messages = %T, want []any", body["messages"])
		}
		for index, raw := range requestMessages {
			message, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("DeepSeek fallback messages[%d] = %T", index, raw)
			}
			if _, exists := message["reasoning_content"]; exists {
				t.Fatalf("DeepSeek fallback messages[%d] fabricated reasoning_content", index)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": "review complete"}, "finish_reason": "stop",
			}},
		})
	}))
	defer fallbackServer.Close()

	makeProvider := func(modelName, providerName, model, apiBase string) providers.LLMProvider {
		t.Helper()
		modelConfig := &config.ModelConfig{
			ModelName: modelName,
			Provider:  providerName,
			Model:     model,
			APIBase:   apiBase,
			Enabled:   true,
		}
		modelConfig.SetAPIKey("test-key")
		provider, _, err := providers.CreateProviderFromConfig(modelConfig)
		if err != nil {
			t.Fatalf("create %s provider: %v", providerName, err)
		}
		return provider
	}
	primary := makeProvider("primary", "openrouter", "primary-model", primaryServer.URL)
	fallback := makeProvider("deepseek-fallback", "deepseek", "deepseek-v4-flash", fallbackServer.URL)
	candidates := []providers.FallbackCandidate{
		{Provider: "openrouter", Model: "primary-model", IdentityKey: "model_name:primary"},
		{Provider: "deepseek", Model: "deepseek-v4-flash", IdentityKey: "model_name:deepseek-fallback"},
	}
	messages := []providers.Message{
		{Role: "user", Content: "Review the pull request"},
		{
			Role:    "assistant",
			Content: "I will inspect the diff.",
			ToolCalls: []providers.ToolCall{{
				ID: "call_diff", Type: "function", Name: "exec",
				Arguments: map[string]any{"command": "git diff"},
			}},
		},
		{Role: "tool", ToolCallID: "call_diff", Content: "diff output"},
	}
	tools := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: "exec", Description: "Execute a command",
		},
	}}

	chain := providers.NewFallbackChain(providers.NewCooldownTracker(), nil)
	result, err := chain.ExecuteCandidate(
		context.Background(),
		candidates,
		func(ctx context.Context, candidate providers.FallbackCandidate) (*providers.LLMResponse, error) {
			selected := primary
			if candidate.Provider == "deepseek" {
				selected = fallback
			}
			return selected.Chat(
				ctx,
				messages,
				tools,
				candidate.Model,
				map[string]any{"thinking_level": "high"},
			)
		},
	)
	if err != nil {
		t.Fatalf("ExecuteCandidate() error = %v", err)
	}
	if result.Provider != "deepseek" || result.Response.Content != "review complete" {
		t.Fatalf("fallback result = %#v, want successful DeepSeek response", result)
	}
	if fallbackCalls != 1 {
		t.Fatalf("DeepSeek fallback calls = %d, want 1", fallbackCalls)
	}
}
