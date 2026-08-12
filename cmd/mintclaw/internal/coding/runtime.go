package coding

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

type codingTurnRequest struct {
	Store    *thread.Store
	Lease    *thread.Lease
	Metadata thread.Metadata
	Prompt   string
}

type codingTurnOutcome struct {
	Model        string
	Provider     string
	Response     string
	PromptStored bool
}

type codingTurnRunner interface {
	Run(context.Context, codingTurnRequest) (codingTurnOutcome, error)
}

type codingTurnRunnerFunc func(context.Context, codingTurnRequest) (codingTurnOutcome, error)

func (f codingTurnRunnerFunc) Run(
	ctx context.Context,
	request codingTurnRequest,
) (codingTurnOutcome, error) {
	return f(ctx, request)
}

type nativeCodingTurnRunner struct {
	loadConfig      func() (*config.Config, error)
	createProvider  func(*config.Config) (providers.LLMProvider, string, error)
	readTurnHistory func(context.Context, session.SessionStore, string) ([]providers.Message, error)
}

func newNativeCodingTurnRunner() codingTurnRunner {
	return nativeCodingTurnRunner{
		loadConfig:     internal.LoadConfig,
		createProvider: providers.CreateProvider,
		readTurnHistory: func(
			ctx context.Context,
			store session.SessionStore,
			sessionKey string,
		) ([]providers.Message, error) {
			return store.ReadTurnHistory(ctx, sessionKey)
		},
	}
}

func resolveNativeCodingModel(model string) (string, string, error) {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return "", "", fmt.Errorf("coding runtime: load config: %w", err)
	}
	_, modelName, providerName, err := codingRuntimeConfig(cfg, thread.Metadata{Model: strings.TrimSpace(model)})
	return modelName, providerName, err
}

func (r nativeCodingTurnRunner) Run(
	ctx context.Context,
	request codingTurnRequest,
) (codingTurnOutcome, error) {
	layout, err := runtimeLayoutFor(request.Store, request.Metadata)
	if err != nil {
		return codingTurnOutcome{}, err
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return codingTurnOutcome{}, fmt.Errorf("coding runtime: load config: %w", err)
	}
	runtimeCfg, modelName, providerName, err := codingRuntimeConfig(cfg, request.Metadata)
	if err != nil {
		return codingTurnOutcome{}, err
	}
	provider, _, err := r.createProvider(runtimeCfg)
	if err != nil {
		return codingTurnOutcome{Model: modelName, Provider: providerName},
			fmt.Errorf("coding runtime: create provider: %w", err)
	}
	profile, err := agent.NewRuntimeProfile(agent.RuntimeProfileBinding{AgentID: "main", Layout: layout})
	if err != nil {
		return codingTurnOutcome{Model: modelName, Provider: providerName}, err
	}
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	loop, err := agent.NewAgentLoopWithRuntimeProfile(runtimeCfg, messageBus, provider, profile)
	if err != nil {
		return codingTurnOutcome{Model: modelName, Provider: providerName},
			fmt.Errorf("coding runtime: initialize agent: %w", err)
	}
	defer loop.Close()
	sessions := loop.GetRegistry().GetDefaultAgent().Sessions
	readTurnHistory := r.readTurnHistory
	if readTurnHistory == nil {
		readTurnHistory = func(
			readCtx context.Context,
			store session.SessionStore,
			sessionKey string,
		) ([]providers.Message, error) {
			return store.ReadTurnHistory(readCtx, sessionKey)
		}
	}
	beforeHistory, err := readTurnHistory(ctx, sessions, request.Metadata.SessionKey)
	if err != nil {
		return codingTurnOutcome{Model: modelName, Provider: providerName},
			fmt.Errorf("coding runtime: read history before turn: %w", err)
	}
	response, turnErr := loop.ProcessDirectWithOptions(
		ctx,
		request.Prompt,
		request.Metadata.SessionKey,
		"coding",
		request.Metadata.ThreadID,
		agent.DirectTurnOptions{SuppressBackgroundCompaction: true},
	)
	after, historyErr := readTurnHistory(
		context.WithoutCancel(ctx),
		sessions,
		request.Metadata.SessionKey,
	)
	promptStored := historyErr == nil && acceptedPromptAfter(after, len(beforeHistory), request.Prompt)
	outcome := codingTurnOutcome{
		Model:        modelName,
		Provider:     providerName,
		Response:     response,
		PromptStored: promptStored,
	}
	if historyErr != nil {
		return outcome, &thread.IndeterminatePromptError{
			ThreadID: request.Metadata.ThreadID,
			Err: errors.Join(
				turnErr,
				fmt.Errorf("coding runtime: confirm history after turn: %w", historyErr),
			),
		}
	}
	if !promptStored {
		return outcome, &thread.IndeterminatePromptError{
			ThreadID: request.Metadata.ThreadID,
			Err: errors.Join(
				turnErr,
				fmt.Errorf("coding runtime: confirmed history does not contain the admitted prompt"),
			),
		}
	}
	return outcome, turnErr
}

func codingRuntimeConfig(
	cfg *config.Config,
	metadata thread.Metadata,
) (*config.Config, string, string, error) {
	if cfg == nil {
		return nil, "", "", fmt.Errorf("coding runtime: config is required")
	}
	runtimeCfg := *cfg
	runtimeCfg.Agents = cfg.Agents
	runtimeCfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	runtimeCfg.Agents.Dispatch = nil
	runtimeCfg.Agents.Defaults.Routing = nil
	runtimeCfg.Agents.Defaults.ModelFallbacks = nil
	// Coding continuation depends on budget-aware assembly of canonical history.
	// It always owns a disposable Seahorse index under this thread's StateRoot;
	// personal-agent context mode and custom database paths are not inherited.
	runtimeCfg.Agents.Defaults.ContextManager = "seahorse"
	runtimeCfg.Agents.Defaults.ContextManagerConfig = nil
	modelName := strings.TrimSpace(metadata.Model)
	if modelName == "" {
		modelName = strings.TrimSpace(runtimeCfg.Agents.Defaults.GetModelName())
	}
	if modelName == "" {
		return nil, "", "", fmt.Errorf("coding runtime: model is required")
	}
	persistedProvider := providers.NormalizeProvider(strings.TrimSpace(metadata.Provider))
	modelCfg, err := selectCodingModelConfig(cfg, modelName, persistedProvider)
	if err != nil {
		return nil, "", "", fmt.Errorf("coding runtime: select model %q: %w", modelName, err)
	}
	selected := cloneModelConfig(modelCfg)
	if persistedProvider != "" {
		_, canonicalModelID := providers.ExtractProtocol(selected)
		selected.Model = canonicalModelID
		selected.Provider = persistedProvider
	}
	runtimeCfg.Agents.Defaults.ModelName = modelName
	runtimeCfg.ModelList = config.SecureModelList{selected}
	for _, candidate := range cfg.ModelList {
		if candidate == nil || candidate.ModelName == modelName {
			continue
		}
		runtimeCfg.ModelList = append(runtimeCfg.ModelList, cloneModelConfig(candidate))
	}
	providerName, _ := providers.ExtractProtocol(selected)
	providerName = providers.NormalizeProvider(providerName)
	if providerName == "" {
		return nil, "", "", fmt.Errorf("coding runtime: provider is required for model %q", modelName)
	}
	runtimeCfg.Agents.Defaults.Provider = providerName
	return &runtimeCfg, modelName, providerName, nil
}

func selectCodingModelConfig(
	cfg *config.Config,
	modelName string,
	persistedProvider string,
) (*config.ModelConfig, error) {
	for _, candidate := range cfg.ModelList {
		if candidate == nil || candidate.ModelName != modelName ||
			!candidate.IsEffectivelyEnabled() || candidate.IsVirtual() {
			continue
		}
		if persistedProvider == "" {
			return candidate, nil
		}
		providerName, _ := providers.ExtractProtocol(candidate)
		if providers.NormalizeProvider(providerName) == persistedProvider {
			return candidate, nil
		}
	}
	if persistedProvider == "" {
		return nil, fmt.Errorf("model not found in model_list or providers")
	}
	return nil, fmt.Errorf("provider %q has no configured entry for this model alias", persistedProvider)
}

func cloneModelConfig(model *config.ModelConfig) *config.ModelConfig {
	if model == nil {
		return nil
	}
	cloned := *model
	cloned.APIKeys = append(config.SecureStrings(nil), model.APIKeys...)
	cloned.Fallbacks = append([]string(nil), model.Fallbacks...)
	if model.ExtraBody != nil {
		cloned.ExtraBody = make(map[string]any, len(model.ExtraBody))
		for key, value := range model.ExtraBody {
			cloned.ExtraBody[key] = value
		}
	}
	if model.CustomHeaders != nil {
		cloned.CustomHeaders = make(map[string]string, len(model.CustomHeaders))
		for key, value := range model.CustomHeaders {
			cloned.CustomHeaders[key] = value
		}
	}
	return &cloned
}

func acceptedPromptAfter(history []providers.Message, before int, prompt string) bool {
	if before < 0 || before > len(history) {
		before = 0
	}
	for _, message := range history[before:] {
		if message.Role == "user" && message.Content == prompt {
			return true
		}
	}
	return false
}
