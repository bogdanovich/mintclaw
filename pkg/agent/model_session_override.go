package agent

import (
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/state"
)

type effectiveModelBinding struct {
	RouteSessionKey string
	WorkspaceAgent  *AgentInstance
	Execution       effectiveExecutionState
	Override        state.SessionModelOverride
	// ExactModel records a task-scoped model pin. RouteSessionKey still owns
	// delivery and session state, but exact bindings must not consume or mutate
	// that route's sticky automatic-fallback selection.
	ExactModel string
	cleanup    func()
}

type effectiveExecutionState struct {
	AgentID                 string
	Model                   string
	Provider                providers.LLMProvider
	Candidates              []providers.FallbackCandidate
	CandidateProviders      map[string]providers.LLMProvider
	Router                  *routing.Router
	LightCandidates         []providers.FallbackCandidate
	LightProvider           providers.LLMProvider
	ThinkingLevel           ThinkingLevel
	ThinkingLevelConfigured bool
}

type modelSelectionInspection struct {
	WorkspaceAgent *AgentInstance
	Execution      effectiveExecutionState
	Override       state.SessionModelOverride
}

func cloneEffectiveModelBinding(binding effectiveModelBinding) effectiveModelBinding {
	binding.Execution = cloneEffectiveExecutionState(binding.Execution)
	return binding
}

func cloneEffectiveExecutionState(execution effectiveExecutionState) effectiveExecutionState {
	execution.Candidates = append([]providers.FallbackCandidate(nil), execution.Candidates...)
	execution.CandidateProviders = cloneCandidateProviderMap(execution.CandidateProviders)
	execution.LightCandidates = append([]providers.FallbackCandidate(nil), execution.LightCandidates...)
	return execution
}

func (b effectiveModelBinding) Cleanup() {
	if b.cleanup != nil {
		b.cleanup()
	}
}

func (b effectiveModelBinding) autoFallbackRouteSessionKey() string {
	if strings.TrimSpace(b.ExactModel) != "" {
		return ""
	}
	return strings.TrimSpace(b.RouteSessionKey)
}

func effectiveExecutionStateForAgent(agent *AgentInstance) effectiveExecutionState {
	if agent == nil {
		return effectiveExecutionState{}
	}
	return effectiveExecutionState{
		AgentID:                 agent.ID,
		Model:                   agent.Model,
		Provider:                agent.Provider,
		Candidates:              append([]providers.FallbackCandidate(nil), agent.Candidates...),
		CandidateProviders:      cloneCandidateProviderMap(agent.CandidateProviders),
		Router:                  agent.Router,
		LightCandidates:         append([]providers.FallbackCandidate(nil), agent.LightCandidates...),
		LightProvider:           agent.LightProvider,
		ThinkingLevel:           agent.ThinkingLevel,
		ThinkingLevelConfigured: agent.ThinkingLevelConfigured,
	}
}

func (b effectiveModelBinding) ExecutionState() effectiveExecutionState {
	if b.Execution.Model != "" || b.Execution.Provider != nil || len(b.Execution.Candidates) > 0 {
		return b.Execution
	}
	return effectiveExecutionStateForAgent(b.WorkspaceAgent)
}

func selectionInfoForInspection(
	inspection modelSelectionInspection,
) commands.ModelSelectionInfo {
	return buildModelSelectionInfo(inspection)
}

func buildModelSelectionInfo(
	inspection modelSelectionInspection,
) commands.ModelSelectionInfo {
	if inspection.WorkspaceAgent == nil {
		return commands.ModelSelectionInfo{}
	}
	return buildModelSelectionInfoValues(
		inspection.WorkspaceAgent,
		inspection.Execution,
		normalizeSessionModelOverride(inspection.Override),
	)
}

func normalizeSessionModelOverride(
	override state.SessionModelOverride,
) state.SessionModelOverride {
	override.Model = strings.TrimSpace(override.Model)
	return override
}

func buildModelSelectionInfoValues(
	workspaceAgent *AgentInstance,
	execution effectiveExecutionState,
	override state.SessionModelOverride,
) commands.ModelSelectionInfo {
	info := commands.ModelSelectionInfo{}
	if workspaceAgent != nil {
		info.WorkspaceName = workspaceAgent.Model
		info.WorkspaceProvider = resolvedCandidateProvider(workspaceAgent.Candidates, "")
	}
	info.EffectiveName = resolvedCandidateModelName(
		execution.Candidates,
		strings.TrimSpace(execution.Model),
	)
	info.EffectiveProvider = resolvedCandidateProvider(execution.Candidates, "")
	override = normalizeSessionModelOverride(override)
	if override.Model != "" {
		info.SessionOverride = override.Model
		info.HasSessionOverride = true
	}
	return info
}

func canonicalModelOverrideValue(cfg *config.Config, raw string) (string, error) {
	modelCfg, err := resolvedSwitchableModelConfig(cfg, strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(modelCfg.ModelName), nil
}

func cloneCandidateProviderMap(
	in map[string]providers.LLMProvider,
) map[string]providers.LLMProvider {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]providers.LLMProvider, len(in))
	for key, provider := range in {
		out[key] = provider
	}
	return out
}

func (m *modelExecutionManager) buildExecutionStateForModel(
	baseAgent *AgentInstance,
	modelName string,
	fallbacks []string,
) (effectiveExecutionState, func(), error) {
	if baseAgent == nil {
		return effectiveExecutionState{}, nil, fmt.Errorf("agent not initialized")
	}
	cfg := m.config()
	selection, err := resolveModelSelection(cfg, modelName, baseAgent.Workspace)
	if err != nil {
		return effectiveExecutionState{}, nil, err
	}

	factory := m.currentProviderFactory()
	overrideProvider, _, err := factory(selection.modelConfig)
	if err != nil {
		return effectiveExecutionState{}, nil, fmt.Errorf("failed to initialize model %q: %w", modelName, err)
	}

	overrideCandidates := resolveModelCandidatesFromSelection(
		cfg,
		&selection,
		fallbacks,
	)
	if len(overrideCandidates) == 0 {
		if stateful, ok := overrideProvider.(providers.StatefulProvider); ok {
			stateful.Close()
		}
		return effectiveExecutionState{}, nil, fmt.Errorf(
			"model %q did not resolve to any provider candidates",
			modelName,
		)
	}

	candidateProviders := cloneCandidateProviderMap(baseAgent.CandidateProviders)
	existingKeys := make(map[string]struct{}, len(candidateProviders))
	for key := range candidateProviders {
		existingKeys[key] = struct{}{}
	}
	if candidateProviders == nil {
		candidateProviders = make(map[string]providers.LLMProvider)
	}
	populateCandidateProvidersFromCandidatesTracked(
		cfg,
		baseAgent.Workspace,
		overrideCandidates[1:],
		candidateProviders,
		nil,
	)
	if len(overrideCandidates) > 0 {
		candidateProviders[candidateProviderKey(overrideCandidates[0])] = overrideProvider
	}

	cleanup := func() {
		overrideClosed := false
		for key, provider := range candidateProviders {
			if _, exists := existingKeys[key]; exists || provider == nil {
				continue
			}
			if provider == overrideProvider {
				overrideClosed = true
			}
			if stateful, ok := provider.(providers.StatefulProvider); ok {
				stateful.Close()
			}
		}
		if !overrideClosed {
			if stateful, ok := overrideProvider.(providers.StatefulProvider); ok {
				stateful.Close()
			}
		}
	}

	return effectiveExecutionState{
		AgentID:                 baseAgent.ID,
		Model:                   modelName,
		Provider:                overrideProvider,
		Candidates:              overrideCandidates,
		CandidateProviders:      candidateProviders,
		ThinkingLevel:           parseThinkingLevel(selection.modelConfig.ThinkingLevel),
		ThinkingLevelConfigured: isConfiguredThinkingLevel(selection.modelConfig.ThinkingLevel),
	}, cleanup, nil
}

func (al *AgentLoop) buildExecutionStateForModel(
	baseAgent *AgentInstance,
	modelName string,
	fallbacks []string,
) (effectiveExecutionState, func(), error) {
	if al == nil || al.modelExecution == nil {
		return effectiveExecutionState{}, nil, fmt.Errorf("model execution manager not initialized")
	}
	return al.modelExecution.buildExecutionStateForModel(baseAgent, modelName, fallbacks)
}

func (al *AgentLoop) buildSessionOverrideExecution(
	baseAgent *AgentInstance,
	modelName string,
) (effectiveExecutionState, func(), error) {
	return al.buildExecutionStateForModel(baseAgent, modelName, baseAgent.Fallbacks)
}

func (al *AgentLoop) bindResumedInteractionModel(
	routeSessionKey string,
	baseAgent *AgentInstance,
	modelName string,
) effectiveModelBinding {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" || baseAgent == nil {
		return al.bindEffectiveModel(routeSessionKey, baseAgent)
	}

	execution, cleanup, err := al.buildExecutionStateForModel(baseAgent, modelName, baseAgent.Fallbacks)
	if err != nil {
		logger.WarnCF("agent", "Falling back to current model for interaction continuation", map[string]any{
			"agent_id":           baseAgent.ID,
			"session_key":        strings.TrimSpace(routeSessionKey),
			"continuation_model": modelName,
			"error":              err.Error(),
		})
		return al.bindEffectiveModel(routeSessionKey, baseAgent)
	}
	return effectiveModelBinding{
		RouteSessionKey: strings.TrimSpace(routeSessionKey),
		WorkspaceAgent:  baseAgent,
		Execution:       execution,
		ExactModel:      modelName,
		cleanup:         cleanup,
	}
}

func (al *AgentLoop) bindEffectiveModel(
	routeSessionKey string,
	baseAgent *AgentInstance,
) effectiveModelBinding {
	binding := effectiveModelBinding{
		RouteSessionKey: strings.TrimSpace(routeSessionKey),
		WorkspaceAgent:  baseAgent,
	}
	if binding.RouteSessionKey == "" || baseAgent == nil {
		return binding
	}

	override, ok := al.getSessionModelOverride(binding.RouteSessionKey)
	if !ok {
		return binding
	}
	override = normalizeSessionModelOverride(override)
	if override.Model == "" {
		return binding
	}

	binding.Override = override
	if override.Model == baseAgent.Model {
		return binding
	}

	execution, cleanup, err := al.buildSessionOverrideExecution(baseAgent, override.Model)
	if err != nil {
		logger.WarnCF("agent", "Clearing invalid session model override",
			map[string]any{
				"agent_id":       baseAgent.ID,
				"session_key":    binding.RouteSessionKey,
				"override":       override.Model,
				"override_error": err.Error(),
			})
		_ = al.clearSessionModelOverride(binding.RouteSessionKey)
		binding.Override = state.SessionModelOverride{}
		return binding
	}

	binding.Execution = execution
	binding.cleanup = cleanup
	return binding
}

func (al *AgentLoop) rebindModelAfterGenerationChange(
	previous effectiveModelBinding,
	baseAgent *AgentInstance,
) effectiveModelBinding {
	if exactModel := strings.TrimSpace(previous.ExactModel); exactModel != "" {
		return al.bindResumedInteractionModel(previous.RouteSessionKey, baseAgent, exactModel)
	}
	return al.bindEffectiveModel(previous.RouteSessionKey, baseAgent)
}

func (al *AgentLoop) buildModelSelectionInspection(
	cfg *config.Config,
	binding effectiveModelBinding,
) modelSelectionInspection {
	inspection := modelSelectionInspection{
		WorkspaceAgent: binding.WorkspaceAgent,
		Execution:      binding.ExecutionState(),
		Override:       normalizeSessionModelOverride(binding.Override),
	}
	workspaceAgent := inspection.WorkspaceAgent
	if workspaceAgent == nil {
		return inspection
	}
	if strings.TrimSpace(binding.ExactModel) != "" {
		inspection.Override = state.SessionModelOverride{}
		return inspection
	}

	workspaceSelection := effectiveExecutionStateForAgent(workspaceAgent)
	if binding.RouteSessionKey != "" {
		override, _ := al.getSessionModelOverride(binding.RouteSessionKey)
		inspection.Override = normalizeSessionModelOverride(override)
	}
	if inspection.Override.Model != "" {
		if inspection.Override.Model == strings.TrimSpace(workspaceAgent.Model) {
			inspection.Execution = workspaceSelection
			return inspection
		}
		if inspection.Override.Model != strings.TrimSpace(binding.Override.Model) ||
			inspection.Execution.Model == "" ||
			len(inspection.Execution.Candidates) == 0 {
			inspection.Execution.Model = inspection.Override.Model
			if cfg != nil {
				inspection.Execution.Candidates = resolveModelCandidates(
					cfg,
					inspection.Override.Model,
					workspaceAgent.Fallbacks,
				)
			}
		}
		return inspection
	}
	if inspection.Override.Model == "" {
		executionDecision := al.previewStickyAutoFallback(modelSelectionDecision{
			selectedCandidates: append([]providers.FallbackCandidate(nil), workspaceSelection.Candidates...),
			activeCandidates:   append([]providers.FallbackCandidate(nil), workspaceSelection.Candidates...),
			model:              resolvedCandidateModel(workspaceSelection.Candidates, workspaceSelection.Model),
		}, binding.autoFallbackRouteSessionKey())
		workspaceSelection.Candidates = executionDecision.activeCandidates
		workspaceSelection.Model = executionDecision.model
		inspection.Execution = workspaceSelection
	}
	return inspection
}
