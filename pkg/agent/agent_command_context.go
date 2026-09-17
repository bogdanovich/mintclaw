package agent

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/seahorse"
)

func (al *AgentLoop) bindContextCommandCapabilities(
	rt *commands.Runtime,
	modelBinding effectiveModelBinding,
	opts *turnSpec,
	agent *AgentInstance,
) {
	if agent == nil {
		return
	}
	rt.GetContextStats = func() *commands.ContextStats {
		if opts == nil || agent.Sessions == nil {
			return nil
		}
		resolvedOpts := *opts
		if resolved, err := resolveTurnProfileOptions(al.GetConfig(), resolvedOpts); err != nil {
			logger.WarnCF("agent", "Failed to resolve turn profile for /context stats", map[string]any{
				"error": err.Error(),
			})
		} else {
			resolvedOpts = resolved
		}
		al.applyActiveGoalPrompt(&resolvedOpts)

		sessionKey := resolvedOpts.Dispatch.SessionKey
		storedUsage := computeContextUsage(agent, sessionKey)
		if storedUsage == nil {
			return nil
		}
		storedHistory := agent.Sessions.GetHistory(sessionKey)
		statsAgent := contextStatsAgentForBinding(agent, modelBinding)
		var assembledUsage *bus.ContextUsage
		assembledMessageCount := len(storedHistory)
		assembledFitsBudget := true
		if usage, count, fitsBudget := computeAssembledContextUsage(
			context.Background(),
			al.GetConfig(),
			statsAgent,
			al.contextManager,
			resolvedOpts,
			sessionKey,
		); usage != nil {
			assembledUsage = usage
			assembledMessageCount = count
			assembledFitsBudget = fitsBudget
		} else {
			assembledUsage = storedUsage
		}
		seahorseHeuristicTokens := 0
		if contextManagerDisplayName(al.contextManager) == "seahorse" {
			seahorseHeuristicTokens = int(float64(storedUsage.TotalTokens) * seahorse.ContextThreshold)
		}
		return &commands.ContextStats{
			ContextManager:            contextManagerDisplayName(al.contextManager),
			TotalTokens:               storedUsage.TotalTokens,
			CompressAtTokens:          storedUsage.CompressAtTokens,
			SummarizeAtTokens:         storedUsage.SummarizeAtTokens,
			SummarizeMessageThreshold: agent.SummarizeMessageThreshold,
			SummaryPrefixTokens:       seahorseSummaryPrefixTokens(al.contextManager),
			SeahorseHeuristicTokens:   seahorseHeuristicTokens,
			StoredUsedTokens:          storedUsage.UsedTokens,
			StoredHistoryTokens:       storedUsage.HistoryTokens,
			StoredUsedPercent:         storedUsage.UsedPercent,
			StoredMessageCount:        len(storedHistory),
			AssembledUsedTokens:       assembledUsage.UsedTokens,
			AssembledHistoryTokens:    assembledUsage.HistoryTokens,
			AssembledUsedPercent:      assembledUsage.UsedPercent,
			AssembledMessageCount:     assembledMessageCount,
			AssembledFitsBudget:       assembledFitsBudget,
		}
	}
}

func contextStatsAgentForBinding(
	workspaceAgent *AgentInstance,
	modelBinding effectiveModelBinding,
) *AgentInstance {
	if workspaceAgent == nil {
		return nil
	}
	execution := modelBinding.ExecutionState()
	if execution.Model == "" &&
		execution.Provider == nil &&
		len(execution.Candidates) == 0 &&
		len(execution.CandidateProviders) == 0 &&
		len(execution.LightCandidates) == 0 &&
		execution.LightProvider == nil &&
		!execution.ThinkingLevelConfigured {
		return workspaceAgent
	}

	cloned := *workspaceAgent
	if execution.Model != "" {
		cloned.Model = execution.Model
	}
	if execution.Provider != nil {
		cloned.Provider = execution.Provider
	}
	if len(execution.Candidates) > 0 {
		cloned.Candidates = append([]providers.FallbackCandidate(nil), execution.Candidates...)
	}
	if len(execution.CandidateProviders) > 0 {
		cloned.CandidateProviders = cloneCandidateProviderMap(execution.CandidateProviders)
	}
	if len(execution.LightCandidates) > 0 {
		cloned.LightCandidates = append([]providers.FallbackCandidate(nil), execution.LightCandidates...)
	}
	if execution.LightProvider != nil {
		cloned.LightProvider = execution.LightProvider
	}
	if execution.ThinkingLevelConfigured {
		cloned.ThinkingLevel = execution.ThinkingLevel
		cloned.ThinkingLevelConfigured = true
	}
	return &cloned
}
