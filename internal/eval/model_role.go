package eval

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const MaxRoleOutputBytes = 16 * 1024

var ErrModelRole = errors.New("eval: tools-disabled model role unavailable or invalid")

type RoleIdentity struct {
	Role         string `json:"role"`
	Alias        string `json:"alias"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	ModelSource  string `json:"model_source"`
	ConfigSHA256 string `json:"config_sha256"`
	Generation   string `json:"generation"`
}

type RoleCall struct {
	Identity      RoleIdentity             `json:"identity"`
	PromptVersion string                   `json:"prompt_version"`
	PromptSHA256  string                   `json:"prompt_sha256"`
	DurationMS    int64                    `json:"duration_ms"`
	Attempted     bool                     `json:"attempted"`
	Code          string                   `json:"code"`
	ActionKind    string                   `json:"action_kind,omitempty"`
	UsageSource   string                   `json:"usage_source"`
	Usage         *providers.UsageInfo     `json:"usage,omitempty"`
	Boundary      *RoleBoundaryObservation `json:"boundary,omitempty"`
}

// ModelRole never runs an agent or a tool. Its caller owns admission, permitted
// evidence projection, and the number of calls. Provider-internal attempts are
// not observable here; returned usage is not a claim of all-attempt accounting.
type ModelRole struct {
	identity RoleIdentity
	provider providers.LLMProvider
	filter   *strings.Replacer
	retained *privateFactRetention
	close    sync.Once
}

func NewModelRole(cfg *config.Config, alias, role string) (*ModelRole, error) {
	if cfg == nil || !validID(alias) || !slices.Contains([]string{"user", "judge"}, role) ||
		len(cfg.ModelList) == 0 {
		return nil, ErrModelRole
	}
	matched := 0
	for _, candidate := range cfg.ModelList {
		if candidate == nil || candidate.Validate() != nil {
			return nil, ErrModelRole
		}
		if candidate.Enabled && candidate.ModelName == alias {
			matched++
		}
	}
	if matched != 1 {
		return nil, ErrModelRole
	}
	model, err := cfg.GetModelConfig(alias)
	// Refuse agentic transports before construction, not after a CLI bridge has
	// already started. The provider's descriptor remains a second required gate.
	if err != nil || !slices.Contains([]string{"openai", "anthropic", "gemini"}, model.Provider) ||
		len(model.ExtraBody) != 0 || !boundedText(model.Model, 512) {
		return nil, ErrModelRole
	}
	public, err := config.ProjectPublicConfig(&config.Config{ModelList: config.SecureModelList{model}})
	if err != nil {
		return nil, ErrModelRole
	}
	data, err := json.Marshal(public.ModelList[0])
	if err != nil || len(data) > MaxManifestBytes {
		return nil, ErrModelRole
	}
	provider, nativeModel, err := providers.CreateProviderFromConfig(model)
	if err != nil {
		return nil, ErrModelRole
	}
	if !providers.Capabilities(provider).CallerMediatedTools {
		closeRoleProvider(provider)
		return nil, ErrModelRole
	}
	return &ModelRole{
		identity: RoleIdentity{
			Role: role, Alias: alias, Provider: model.Provider, Model: nativeModel,
			ModelSource: "configured_request", ConfigSHA256: digest(data), Generation: "eval-role-generation-v1",
		},
		provider: provider, filter: cfg.SensitiveDataReplacer(),
	}, nil
}

func (role *ModelRole) Close() { role.close.Do(func() { closeRoleProvider(role.provider) }) }

func closeRoleProvider(provider providers.LLMProvider) {
	if stateful, ok := provider.(providers.StatefulProvider); ok {
		stateful.Close()
	}
}

func (role *ModelRole) Call(
	ctx context.Context,
	promptVersion, system, input string,
) (output string, call RoleCall, callErr error) {
	return role.call(ctx, promptVersion, system, input, nil)
}

func (role *ModelRole) call(
	ctx context.Context, promptVersion, system, input string, facts map[string]string,
) (output string, call RoleCall, callErr error) {
	started := time.Now()
	call = RoleCall{
		Code:        "invalid_input",
		UsageSource: "unknown",
		Boundary: &RoleBoundaryObservation{
			Scope: roleBoundaryScope, FactScope: "credentials_only", Input: "not_observed", Output: "not_observed",
		},
	}
	if role.identity.Role == "user" || role.identity.Role == "judge" {
		call.Identity.Role = role.identity.Role
	}
	if len(facts) != 0 {
		call.Boundary.FactScope = "call_facts"
	}
	if role.retained != nil {
		call.Boundary.FactScope = "run_retained_facts"
		if role.retained.generated {
			call.Boundary.FactScope = generatedFactGuardScope
		}
	}
	defer func() { call.DurationMS = time.Since(started).Milliseconds() }()
	if !validID(promptVersion) || !boundedText(system, MaxRoleOutputBytes) ||
		!boundedText(
			input,
			MaxInputBytes,
		) || len(system)+len(input) > MaxInputBytes || ctx == nil || ctx.Err() != nil ||
		!providers.Capabilities(role.provider).CallerMediatedTools {
		return "", call, ErrModelRole
	}
	messages := []providers.Message{{Role: "system", Content: system}, {Role: "user", Content: input}}
	options := providers.CallerMediatedToolsOptions(map[string]any{"max_tokens": 4096})
	request := struct {
		Identity RoleIdentity
		Prompt   string
		Messages []providers.Message
		Options  map[string]any
	}{role.identity, promptVersion, messages, options}
	if err := role.inspectBoundary(ctx, request, facts); err != nil {
		call.Boundary.Input = roleBoundaryCode(err)
		call.Code = "input_evidence_unavailable"
		if errors.Is(err, ErrPrivateEvidence) {
			call.Code = "private_input"
		}
		return "", call, err
	}
	call.Boundary.Input = "observed"
	call.Identity, call.PromptVersion = role.identity, promptVersion
	data, err := json.Marshal(messages)
	if err != nil {
		return "", call, ErrModelRole
	}
	call.PromptSHA256 = digest(data)
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	call.Attempted = true
	response, err := role.provider.Chat(callCtx, messages, nil, role.identity.Model, options)
	var boundaryErr error
	call.Boundary.Output = "not_returned"
	if response != nil {
		// Inspect late/partial responses before projection, even after provider
		// errors or conversation deadlines, under a separate cleanup budget.
		inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 30*time.Second)
		boundaryErr = role.inspectBoundary(inspectCtx, response, facts)
		inspectCancel()
		call.Boundary.Output = roleBoundaryCode(boundaryErr)
	}
	if response != nil && validRoleUsage(response.Usage) {
		usage := *response.Usage
		if usage.CacheReadInputTokens != nil {
			value := *usage.CacheReadInputTokens
			usage.CacheReadInputTokens = &value
		}
		if usage.CacheWriteInputTokens != nil {
			value := *usage.CacheWriteInputTokens
			usage.CacheWriteInputTokens = &value
		}
		call.Usage, call.UsageSource = &usage, "provider_response"
	}
	if boundaryErr != nil {
		call.Code = "output_evidence_unavailable"
		if errors.Is(boundaryErr, ErrPrivateEvidence) {
			call.Code = "private_output"
		}
		return "", call, boundaryErr
	}
	if err != nil || callCtx.Err() != nil {
		call.Code = "provider_unavailable"
		return "", call, ErrModelRole
	}
	if response == nil || len(response.ToolCalls) != 0 ||
		!boundedText(response.Content, MaxRoleOutputBytes) ||
		response.FinishReason != "stop" && response.FinishReason != "end_turn" {
		call.Code = "invalid_output"
		return "", call, ErrModelRole
	}
	call.Code = "response_received"
	return response.Content, call, nil
}

func validRoleUsage(usage *providers.UsageInfo) bool {
	return usage != nil && usage.PromptTokens >= 0 && usage.CompletionTokens >= 0 && usage.TotalTokens >= 0 &&
		(usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens >= 0) &&
		(usage.CacheWriteInputTokens == nil || *usage.CacheWriteInputTokens >= 0)
}
