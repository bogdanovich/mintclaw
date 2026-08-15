package coding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/coding/controller"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend/agentadapter"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
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
	return newNativeCodingRuntimeDependencies()
}

func newNativeCodingRuntimeDependencies() nativeCodingTurnRunner {
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
	runtime, err := openNativeCodingRuntime(r, request, nil)
	if err != nil {
		return codingTurnOutcome{}, err
	}
	turnErr := runtime.RunTurn(ctx, request.Prompt)
	return runtime.outcome(), errors.Join(turnErr, runtime.Close())
}

type nativeCodingRuntime struct {
	loop            *agent.AgentLoop
	messageBus      *bus.MessageBus
	eventBus        runtimeevents.Bus
	sessions        session.SessionStore
	readTurnHistory func(context.Context, session.SessionStore, string) ([]providers.Message, error)
	metadata        thread.Metadata
	model           string
	provider        string
	streaming       bool
	mu              sync.Mutex
	lastOutcome     codingTurnOutcome
	closeOnce       sync.Once
	closeErr        error
}

func openNativeCodingRuntime(
	r nativeCodingTurnRunner,
	request codingTurnRequest,
	projector *frontend.Projector,
) (*nativeCodingRuntime, error) {
	layout, err := runtimeLayoutFor(request.Store, request.Metadata)
	if err != nil {
		return nil, err
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("coding runtime: load config: %w", err)
	}
	runtimeCfg, modelName, providerName, err := codingRuntimeConfig(cfg, request.Metadata)
	if err != nil {
		return nil, err
	}
	provider, _, err := r.createProvider(runtimeCfg)
	if err != nil {
		return nil, fmt.Errorf("coding runtime: create provider: %w", err)
	}
	profile, err := agent.NewRuntimeProfile(agent.RuntimeProfileBinding{AgentID: "main", Layout: layout})
	if err != nil {
		return nil, err
	}
	messageBus := bus.NewMessageBus()
	baseEventBus := runtimeevents.NewBus()
	var eventBus runtimeevents.Bus = baseEventBus
	if projector != nil {
		eventBus, err = agentadapter.WrapBus(baseEventBus, projector, request.Metadata.SessionKey)
		if err != nil {
			messageBus.Close()
			_ = baseEventBus.Close()
			return nil, err
		}
		messageBus.SetStreamDelegate(frontend.NewStreamDelegate(projector, request.Metadata.SessionKey))
	}
	loop, err := agent.NewAgentLoopWithRuntimeProfile(
		runtimeCfg,
		messageBus,
		provider,
		profile,
		agent.WithRuntimeEvents(eventBus),
	)
	if err != nil {
		messageBus.Close()
		_ = baseEventBus.Close()
		return nil, fmt.Errorf("coding runtime: initialize agent: %w", err)
	}
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
	return &nativeCodingRuntime{
		loop:            loop,
		messageBus:      messageBus,
		eventBus:        baseEventBus,
		sessions:        loop.GetRegistry().GetDefaultAgent().Sessions,
		readTurnHistory: readTurnHistory,
		metadata:        request.Metadata,
		model:           modelName,
		provider:        providerName,
		streaming:       projector != nil,
	}, nil
}

func (r *nativeCodingRuntime) RunTurn(ctx context.Context, prompt string) error {
	beforeHistory, err := r.readTurnHistory(ctx, r.sessions, r.metadata.SessionKey)
	if err != nil {
		return fmt.Errorf("coding runtime: read history before turn: %w", err)
	}
	response, turnErr := r.loop.ProcessDirectWithOptions(
		ctx,
		prompt,
		r.metadata.SessionKey,
		"coding",
		r.metadata.ThreadID,
		agent.DirectTurnOptions{
			SuppressBackgroundCompaction: true,
			EnableStreaming:              r.streaming,
		},
	)
	after, historyErr := r.readTurnHistory(
		context.WithoutCancel(ctx),
		r.sessions,
		r.metadata.SessionKey,
	)
	promptStored := historyErr == nil && acceptedPromptAfter(after, len(beforeHistory), prompt)
	outcome := codingTurnOutcome{
		Model:        r.model,
		Provider:     r.provider,
		Response:     response,
		PromptStored: promptStored,
	}
	r.mu.Lock()
	r.lastOutcome = outcome
	r.mu.Unlock()
	hardCanceled := errors.Is(context.Cause(ctx), controller.ErrHardCanceled)
	if historyErr != nil {
		return &thread.IndeterminatePromptError{
			ThreadID: r.metadata.ThreadID,
			Err: errors.Join(
				turnErr,
				fmt.Errorf("coding runtime: confirm history after turn: %w", historyErr),
			),
		}
	}
	if hardCanceled && !promptStored {
		return turnErr
	}
	if !promptStored {
		return &thread.IndeterminatePromptError{
			ThreadID: r.metadata.ThreadID,
			Err: errors.Join(
				turnErr,
				fmt.Errorf("coding runtime: confirmed history does not contain the admitted prompt"),
			),
		}
	}
	return turnErr
}

func (r *nativeCodingRuntime) Interrupt(_ context.Context) error {
	return r.loop.InterruptGracefulSession(r.metadata.SessionKey, "finish the current work and summarize")
}

func (r *nativeCodingRuntime) HardCancel(_ context.Context) error {
	return r.loop.HardAbort(r.metadata.SessionKey)
}

func (r *nativeCodingRuntime) Compact(ctx context.Context) error {
	return r.loop.CompactCodingSession(ctx, r.metadata.SessionKey)
}

func (r *nativeCodingRuntime) Close() error {
	r.closeOnce.Do(func() {
		r.closeErr = errors.Join(r.loop.CloseContext(context.Background()), r.eventBus.Close())
		r.messageBus.Close()
	})
	return r.closeErr
}

func (r *nativeCodingRuntime) outcome() codingTurnOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastOutcome
}

type nativeControllerRuntime struct {
	*nativeCodingRuntime
	store     *thread.Store
	lease     *thread.Lease
	projector *frontend.Projector
	now       func() time.Time
}

var _ controller.Runtime = (*nativeControllerRuntime)(nil)

func (r *nativeControllerRuntime) RunTurn(ctx context.Context, prompt string) error {
	turnErr := r.nativeCodingRuntime.RunTurn(ctx, prompt)
	outcome := r.outcome()
	if !outcome.PromptStored {
		return turnErr
	}
	_, preview, displayErr := thread.DisplayFromRequest(prompt)
	if displayErr == nil {
		r.metadata.Preview = preview
	}
	r.metadata.Model = r.model
	r.metadata.Provider = r.provider
	r.metadata.UpdatedAt = r.now().UTC()
	saveErr := r.store.Save(r.metadata)
	projectionErr := agentadapter.ProjectThreadMetadata(r.projector, r.metadata)
	return errors.Join(turnErr, displayErr, saveErr, projectionErr)
}

func (r *nativeControllerRuntime) Close() error {
	return errors.Join(r.nativeCodingRuntime.Close(), r.lease.Release())
}

func newNativeCodingControllerWithDependencies(
	request codingTurnRequest,
	resumed bool,
	limits frontend.ProjectionLimits,
	dependencies nativeCodingTurnRunner,
	now func() time.Time,
) (frontend.Controller, error) {
	if request.Store == nil || request.Lease == nil {
		return nil, fmt.Errorf("coding controller requires a store and thread lease")
	}
	if err := request.Store.ValidateLease(request.Lease, request.Metadata.ThreadID); err != nil {
		return nil, fmt.Errorf("coding controller requires an active thread lease: %w", err)
	}
	projector, err := frontend.NewProjector(request.Metadata.ThreadID, limits)
	if err != nil {
		return nil, err
	}
	projector.Open(resumed)
	if projectionErr := agentadapter.ProjectThreadMetadata(projector, request.Metadata); projectionErr != nil {
		return nil, projectionErr
	}
	native, err := openNativeCodingRuntime(dependencies, request, projector)
	if err != nil {
		return nil, err
	}
	runtime := &nativeControllerRuntime{
		nativeCodingRuntime: native,
		store:               request.Store,
		lease:               request.Lease,
		projector:           projector,
		now:                 now,
	}
	result, err := controller.New(projector, runtime)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	return result, nil
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
