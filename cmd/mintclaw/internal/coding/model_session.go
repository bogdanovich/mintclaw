package coding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingreviewer "github.com/bogdanovich/mintclaw/pkg/coding/reviewer"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/reasoning"
)

var (
	errCodingModelSessionClosed  = errors.New("coding model session is closed")
	errCodingModelSessionChanged = errors.New("coding model session changed during selection preparation")
)

type codingModelSessionSnapshot struct {
	model             string
	provider          string
	reasoningEffort   string
	reasoningOverride string
	reasoningPinned   bool
	modelPinned       bool
	reviewer          *codingreviewer.Executor
	status            frontend.RuntimeStatus
}

type codingModelSessionConfig struct {
	sourceConfig     *config.Config
	createProvider   func(*config.Config) (providers.LLMProvider, string, error)
	workspace        string
	initial          codingModelSessionSnapshot
	retainedProvider providers.LLMProvider
}

type codingModelSession struct {
	mu               sync.RWMutex
	sourceConfig     *config.Config
	createProvider   func(*config.Config) (providers.LLMProvider, string, error)
	workspace        string
	generation       uint64
	closed           bool
	current          codingModelSessionSnapshot
	retainedProvider providers.LLMProvider
}

type preparedCodingModelSelection struct {
	baseGeneration uint64
	current        codingModelSessionSnapshot
	provider       providers.LLMProvider
	retainProvider bool
}

type persistCodingModelSelection func(
	model string,
	provider string,
	reasoningEffort string,
) (thread.Metadata, error)

func newCodingModelSession(cfg codingModelSessionConfig) *codingModelSession {
	return &codingModelSession{
		sourceConfig:     cfg.sourceConfig,
		createProvider:   cfg.createProvider,
		workspace:        cfg.workspace,
		current:          cloneCodingModelSessionSnapshot(cfg.initial),
		retainedProvider: cfg.retainedProvider,
	}
}

func (session *codingModelSession) snapshot() codingModelSessionSnapshot {
	if session == nil {
		return codingModelSessionSnapshot{}
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	return cloneCodingModelSessionSnapshot(session.current)
}

func (session *codingModelSession) setResumed(resumed bool) frontend.RuntimeStatus {
	if session == nil {
		return frontend.RuntimeStatus{}
	}
	session.mu.Lock()
	session.current.status.Resumed = resumed
	status := cloneCodingRuntimeStatus(session.current.status)
	session.mu.Unlock()
	return status
}

func (session *codingModelSession) selectModel(
	ctx context.Context,
	selection frontend.ModelSelection,
	persist persistCodingModelSelection,
) (thread.Metadata, frontend.RuntimeStatus, bool, error) {
	prepared, err := session.prepareSelection(ctx, selection)
	if err != nil {
		return thread.Metadata{}, frontend.RuntimeStatus{}, false, err
	}
	defer prepared.abort()

	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return thread.Metadata{}, frontend.RuntimeStatus{}, false, errCodingModelSessionClosed
	}
	if session.generation != prepared.baseGeneration {
		session.mu.Unlock()
		return thread.Metadata{}, frontend.RuntimeStatus{}, false, errCodingModelSessionChanged
	}
	if codingModelSelectionUnchanged(session.current, prepared.current) {
		status := cloneCodingRuntimeStatus(session.current.status)
		session.mu.Unlock()
		return thread.Metadata{}, status, false, nil
	}
	if ctx != nil {
		if err = context.Cause(ctx); err != nil {
			session.mu.Unlock()
			return thread.Metadata{}, frontend.RuntimeStatus{}, false, err
		}
	}
	if persist == nil {
		session.mu.Unlock()
		return thread.Metadata{}, frontend.RuntimeStatus{}, false,
			fmt.Errorf("coding model selection persistence is unavailable")
	}
	metadata, err := persist(
		prepared.current.model,
		prepared.current.provider,
		prepared.current.reasoningOverride,
	)
	if err != nil {
		session.mu.Unlock()
		return thread.Metadata{}, frontend.RuntimeStatus{}, false, err
	}

	previousProvider := session.retainedProvider
	session.current = cloneCodingModelSessionSnapshot(prepared.current)
	session.retainedProvider = nil
	if prepared.retainProvider {
		session.retainedProvider = prepared.provider
		prepared.provider = nil
	}
	session.generation++
	status := cloneCodingRuntimeStatus(session.current.status)
	session.mu.Unlock()

	closeStatefulProvider(previousProvider)
	return metadata, status, true, nil
}

func (session *codingModelSession) prepareSelection(
	ctx context.Context,
	selection frontend.ModelSelection,
) (*preparedCodingModelSelection, error) {
	if session == nil {
		return nil, errCodingModelSessionClosed
	}
	if ctx != nil {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
	}
	model := strings.TrimSpace(selection.Model)
	if model == "" {
		return nil, fmt.Errorf("coding model is required")
	}
	reasoningEffort := strings.ToLower(strings.TrimSpace(selection.ReasoningEffort))
	if err := validateCodingModelReasoningSelection(session.sourceConfig, model, reasoningEffort); err != nil {
		return nil, err
	}

	session.mu.RLock()
	if session.closed {
		session.mu.RUnlock()
		return nil, errCodingModelSessionClosed
	}
	baseGeneration := session.generation
	status := cloneCodingRuntimeStatus(session.current.status)
	session.mu.RUnlock()

	runtimeCfg, selectedModel, selectedProvider, err := codingRuntimeConfig(
		session.sourceConfig,
		thread.Metadata{Model: model, ReasoningEffort: reasoningEffort},
	)
	if err != nil {
		return nil, err
	}
	if session.createProvider == nil {
		return nil, fmt.Errorf("coding runtime: selected provider factory is unavailable")
	}
	selectedProviderRuntime, providerModel, err := session.createProvider(runtimeCfg)
	if err != nil {
		return nil, fmt.Errorf("coding runtime: create selected provider: %w", err)
	}
	prepared := &preparedCodingModelSelection{
		baseGeneration: baseGeneration,
		provider:       selectedProviderRuntime,
	}
	if providers.Capabilities(selectedProviderRuntime).CallerMediatedTools {
		prepared.current.reviewer, err = codingreviewer.New(
			selectedProviderRuntime,
			providerModel,
			newNativeReviewerToolset(session.workspace),
			codingreviewer.Limits{},
			time.Now,
		)
		if err != nil {
			prepared.abort()
			return nil, fmt.Errorf("coding runtime: initialize selected reviewer: %w", err)
		}
		prepared.retainProvider = true
	}
	selectedConfig, err := selectCodingModelConfig(runtimeCfg, selectedModel, selectedProvider)
	if err != nil {
		prepared.abort()
		return nil, fmt.Errorf("coding runtime: inspect selected model: %w", err)
	}
	effectiveReasoning, reasoningConfigured := canonicalCodingReasoningEffort(selectedConfig.ThinkingLevel)
	if !reasoningConfigured {
		effectiveReasoning = string(reasoning.EffortOff)
	}
	status.Account = codingProviderAccount(selectedProvider, selectedConfig)
	status.ReasoningConfigured = reasoningConfigured
	status.ReasoningEffort = effectiveReasoning
	prepared.current.model = selectedModel
	prepared.current.provider = selectedProvider
	prepared.current.reasoningEffort = effectiveReasoning
	prepared.current.reasoningOverride = reasoningEffort
	prepared.current.reasoningPinned = reasoningEffort != ""
	prepared.current.modelPinned = true
	prepared.current.status = status
	return prepared, nil
}

func validateCodingModelReasoningSelection(
	cfg *config.Config,
	model string,
	reasoningEffort string,
) error {
	if reasoningEffort == "" {
		return nil
	}
	requested, configured := reasoning.Parse(reasoningEffort)
	if !configured {
		return fmt.Errorf("unsupported reasoning effort %q", reasoningEffort)
	}
	for _, option := range codingModelOptions(cfg) {
		if option.Name != model {
			continue
		}
		if !option.ReasoningProfile.Supports(requested) {
			return fmt.Errorf(
				"unsupported reasoning effort %q: not supported by every route of model alias %q",
				reasoningEffort,
				model,
			)
		}
		break
	}
	return nil
}

func codingModelSelectionUnchanged(
	current codingModelSessionSnapshot,
	prepared codingModelSessionSnapshot,
) bool {
	return current.model == prepared.model && current.provider == prepared.provider &&
		current.reasoningEffort == prepared.reasoningEffort &&
		current.reasoningOverride == prepared.reasoningOverride &&
		current.status.ReasoningConfigured == prepared.status.ReasoningConfigured
}

func (prepared *preparedCodingModelSelection) abort() {
	if prepared == nil || prepared.provider == nil {
		return
	}
	closeStatefulProvider(prepared.provider)
	prepared.provider = nil
}

func (session *codingModelSession) close() {
	if session == nil {
		return
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return
	}
	session.closed = true
	session.generation++
	provider := session.retainedProvider
	session.retainedProvider = nil
	session.current.reviewer = nil
	session.mu.Unlock()
	closeStatefulProvider(provider)
}

func closeStatefulProvider(provider providers.LLMProvider) {
	if stateful, ok := provider.(providers.StatefulProvider); ok && stateful != nil {
		stateful.Close()
	}
}

func cloneCodingModelSessionSnapshot(snapshot codingModelSessionSnapshot) codingModelSessionSnapshot {
	snapshot.status = cloneCodingRuntimeStatus(snapshot.status)
	return snapshot
}

func cloneCodingRuntimeStatus(status frontend.RuntimeStatus) frontend.RuntimeStatus {
	status.InstructionSources = append([]frontend.InstructionSource(nil), status.InstructionSources...)
	status.Skills = append([]frontend.SkillSummary(nil), status.Skills...)
	if status.Account != nil {
		account := *status.Account
		status.Account = &account
	}
	status.Models = append([]frontend.ModelOption(nil), status.Models...)
	for index := range status.Models {
		status.Models[index].Providers = append([]string(nil), status.Models[index].Providers...)
		status.Models[index].ReasoningProfile.Options = append(
			[]reasoning.Option(nil),
			status.Models[index].ReasoningProfile.Options...,
		)
	}
	return status
}
