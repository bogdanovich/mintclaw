package agent

import (
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/reasoning"
)

// ThinkingLevel is the provider-neutral effort selected by the agent runtime.
// A concrete provider profile decides which values a model accepts, and its
// adapter remains responsible for the wire representation.
type ThinkingLevel = reasoning.Effort

const (
	ThinkingOff      = reasoning.EffortOff
	ThinkingMinimal  = reasoning.EffortMinimal
	ThinkingLow      = reasoning.EffortLow
	ThinkingMedium   = reasoning.EffortMedium
	ThinkingHigh     = reasoning.EffortHigh
	ThinkingXHigh    = reasoning.EffortXHigh
	ThinkingMax      = reasoning.EffortMax
	ThinkingAdaptive = reasoning.EffortAdaptive
)

// parseThinkingLevel normalizes a config string to a ThinkingLevel.
// Case-insensitive and whitespace-tolerant for user-facing config values.
// Returns ThinkingOff for unknown or empty values.
func parseThinkingLevel(level string) ThinkingLevel {
	if effort, ok := reasoning.Parse(level); ok {
		return effort
	}
	return ThinkingOff
}

func isConfiguredThinkingLevel(level string) bool {
	_, ok := reasoning.Parse(level)
	return ok
}

type thinkingSettings struct {
	level      ThinkingLevel
	configured bool
}

func thinkingSettingsFromModelConfig(mc *config.ModelConfig) thinkingSettings {
	if mc == nil || !isConfiguredThinkingLevel(mc.ThinkingLevel) {
		return thinkingSettings{}
	}
	return thinkingSettings{
		level:      parseThinkingLevel(mc.ThinkingLevel),
		configured: true,
	}
}

func activeThinkingSettings(
	modelCfg *config.ModelConfig,
	level ThinkingLevel,
	levelConfigured bool,
	levelOverridden bool,
) thinkingSettings {
	if levelOverridden {
		return thinkingSettings{level: level, configured: levelConfigured}
	}
	if settings := thinkingSettingsFromModelConfig(modelCfg); settings.configured {
		return settings
	}
	if modelCfg == nil {
		return thinkingSettings{
			level:      level,
			configured: levelConfigured,
		}
	}
	return thinkingSettings{}
}

func applyThinkingOption(
	opts map[string]any,
	provider providers.LLMProvider,
	settings thinkingSettings,
	warnUnsupported bool,
	agentID string,
) {
	if opts == nil || !settings.configured {
		return
	}
	if settings.level == ThinkingOff {
		opts["thinking_level"] = string(settings.level)
		return
	}
	if providers.Capabilities(provider).Thinking {
		opts["thinking_level"] = string(settings.level)
		return
	}
	if warnUnsupported {
		logger.WarnCF("agent", "thinking_level is set but current provider does not support it, ignoring",
			map[string]any{"agent_id": agentID, "thinking_level": string(settings.level)})
	}
}

func applyTurnThinkingOptions(
	exec *turnExecution,
	llm *LLMIterationState,
	execution effectiveExecutionState,
	provider providers.LLMProvider,
	warnUnsupported bool,
) {
	if exec == nil || llm == nil || llm.llmOpts == nil {
		return
	}
	delete(llm.llmOpts, "thinking_level")
	settings := activeThinkingSettings(
		exec.model.activeModelConfig,
		execution.ThinkingLevel,
		execution.ThinkingLevelConfigured,
		execution.ThinkingLevelOverridden,
	)
	agentID := execution.AgentID
	applyThinkingOption(llm.llmOpts, provider, settings, warnUnsupported, agentID)
	llm.suppressReasoning = shouldSuppressReasoningFor(settings)
}

func shouldSuppressReasoningFor(settings thinkingSettings) bool {
	return settings.configured && settings.level == ThinkingOff
}
