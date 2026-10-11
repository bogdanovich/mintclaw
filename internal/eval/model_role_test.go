package eval

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type roleTestProvider struct {
	response *providers.LLMResponse
	err      error
	calls    int
	mediated bool
}

func (*roleTestProvider) GetDefaultModel() string { return "synthetic-model" }
func (provider *roleTestProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{CallerMediatedTools: provider.mediated, NativeSearch: true}
}

func (provider *roleTestProvider) Chat(
	ctx context.Context, messages []providers.Message, tools []providers.ToolDefinition,
	_ string, options map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls++
	deadline, bounded := ctx.Deadline()
	if len(tools) != 0 || options["native_search"] != false || options["max_tokens"] != 4096 ||
		!bounded || time.Until(deadline) > 3*time.Minute || len(messages) != 2 ||
		messages[0].Role != "system" || messages[1].Role != "user" {
		return nil, errors.New("unsafe role request")
	}
	return provider.response, provider.err
}

func TestModelRoleCallIsToolFreeBoundedAndNonEchoing(t *testing.T) {
	for _, mode := range []string{
		"good", "unknown usage", "negative usage", "tool", "length", "error", "oversize", "agentic",
	} {
		t.Run(mode, func(t *testing.T) {
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: `{"kind":"stop"}`, FinishReason: "stop",
				Usage: &providers.UsageInfo{PromptTokens: 10252, CompletionTokens: 32, TotalTokens: 10284},
			}}
			switch mode {
			case "unknown usage":
				provider.response.Usage = nil
			case "negative usage":
				provider.response.Usage.PromptTokens = -1
			case "tool":
				provider.response.ToolCalls = []providers.ToolCall{{Name: "exec"}}
			case "length":
				provider.response.FinishReason = "length"
			case "error":
				provider.err = errors.New("role-secret-canary must not be echoed")
			case "oversize":
				provider.response.Content = strings.Repeat("x", MaxRoleOutputBytes+1)
			case "agentic":
				provider.mediated = false
			}
			role := &ModelRole{provider: provider, identity: RoleIdentity{Role: "user", Model: "synthetic-model"}}
			output, call, err := role.Call(
				t.Context(),
				"user-v1",
				"Only emit a bounded action.",
				"Synthetic user input.",
			)
			valid := mode == "good" || mode == "unknown usage" || mode == "negative usage"
			if valid && (err != nil || output != provider.response.Content || call.Code != "response_received") ||
				!valid && (err == nil || output != "" || strings.Contains(err.Error(), "role-secret-canary")) {
				t.Fatal("role admitted unsafe output or leaked provider error", call.Code)
			}
			if (mode == "unknown usage" || mode == "negative usage") &&
				(call.Usage != nil || call.UsageSource != "unknown") {
				t.Fatal("missing/invalid usage invented a total")
			}
			if mode == "agentic" && provider.calls != 0 {
				t.Fatal("agentic provider was called")
			}
			if mode == "good" &&
				(call.Usage == nil || call.Usage.PromptTokens != 10252 || !validDigest(call.PromptSHA256)) {
				t.Fatal("provider usage or prompt identity was lost")
			}
		})
	}
}

func TestModelRoleRefusesInvalidInputBeforeProviderCall(t *testing.T) {
	provider := &roleTestProvider{mediated: true}
	role := &ModelRole{provider: provider}
	for _, input := range []string{"", strings.Repeat("x", MaxInputBytes), "invalid\x00input"} {
		if _, _, err := role.Call(t.Context(), "user-v1", "system", input); !errors.Is(err, ErrModelRole) {
			t.Fatal("unbounded input admitted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := role.Call(ctx, "user-v1", "system", "input"); !errors.Is(err, ErrModelRole) || provider.calls != 0 {
		t.Fatal("canceled or invalid input reached a provider")
	}
}

func TestModelRoleUsageIsAnIndependentObservation(t *testing.T) {
	read, write := 12, 4
	provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
		Content: `{"kind":"stop"}`, FinishReason: "stop", Usage: &providers.UsageInfo{
			PromptTokens: 10252, CompletionTokens: 3, TotalTokens: 10255,
			CacheReadInputTokens: &read, CacheWriteInputTokens: &write,
		},
	}}
	role := &ModelRole{provider: provider, identity: RoleIdentity{Role: "user"}}
	_, call, err := role.Call(t.Context(), "user-v1", "Only return JSON.", "Synthetic input.")
	read, write = 99, 88
	provider.response.Usage.PromptTokens = 5
	if err != nil || call.Usage == nil || call.Usage.PromptTokens != 10252 ||
		*call.Usage.CacheReadInputTokens != 12 || *call.Usage.CacheWriteInputTokens != 4 {
		t.Fatal("recorded usage was aliased to mutable provider response", err)
	}
}

func TestModelRoleUsesNativeHTTPProviderWithoutTools(t *testing.T) {
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": `{"kind":"stop"}`}, "finish_reason": "stop",
		}}})
	}))
	defer server.Close()
	cfg := &config.Config{ModelList: config.SecureModelList{{
		ModelName: "eval-role", Provider: "openai", Model: "synthetic-model", APIBase: server.URL, Enabled: true,
		APIKeys: config.SimpleSecureStrings("role-secret-canary"),
	}}}
	user, err := NewModelRole(cfg, "eval-role", "user")
	if err != nil {
		t.Fatal(err)
	}
	defer user.Close()
	judge, err := NewModelRole(cfg, "eval-role", "judge")
	if err != nil {
		t.Fatal(err)
	}
	defer judge.Close()
	if user.provider == judge.provider || user.identity.Role == judge.identity.Role ||
		user.identity.ConfigSHA256 != judge.identity.ConfigSHA256 || !validDigest(user.identity.ConfigSHA256) {
		t.Fatal("roles shared one provider or lost pinned configuration identity")
	}
	for _, role := range []*ModelRole{user, judge} {
		output, call, callErr := role.Call(t.Context(), "test-v1", "Only return JSON.", "Synthetic permitted evidence.")
		if callErr != nil || output == "" || call.Usage != nil || call.UsageSource != "unknown" {
			t.Fatal("native role call failed or missing usage was fabricated", callErr)
		}
		request := <-requests
		if _, hasTools := request["tools"]; hasTools || request["model"] != "synthetic-model" ||
			request["max_tokens"] != float64(4096) {
			t.Fatal("native HTTP request gained tools or changed the declared bounds")
		}
		encoded, encodeErr := json.Marshal(call)
		if encodeErr != nil || strings.Contains(string(encoded), "role-secret-canary") ||
			strings.Contains(string(encoded), "Synthetic permitted") {
			t.Fatal("role metrics leaked credentials or prompts")
		}
	}
}

func TestModelRoleRejectsAgenticExtraBodyAndAmbiguousRouteBeforeConstruction(t *testing.T) {
	for _, mode := range []string{"cli", "extra", "duplicate", "null", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{ModelList: config.SecureModelList{
				{
					ModelName: "eval-role",
					Provider:  "openai",
					Model:     "synthetic-model",
					APIBase:   "http://127.0.0.1:1",
					Enabled:   true,
				},
			}}
			switch mode {
			case "cli":
				cfg.ModelList[0].Provider = "codex-cli"
			case "extra":
				cfg.ModelList[0].ExtraBody = map[string]any{"tools": []any{map[string]string{"type": "web_search"}}}
			case "duplicate":
				cfg.ModelList = append(cfg.ModelList, cfg.ModelList[0])
			case "null":
				cfg.ModelList = config.SecureModelList{nil}
			case "disabled":
				cfg.ModelList[0].Enabled = false
			}
			if _, err := NewModelRole(cfg, "eval-role", "user"); !errors.Is(err, ErrModelRole) {
				t.Fatal("unsafe role constructed")
			}
		})
	}
}
