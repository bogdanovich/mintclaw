package agent

import (
	"context"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func (p *Pipeline) nativeSearchEnabled(
	profile config.EffectiveTurnProfile,
	provider providers.LLMProvider,
) bool {
	if p == nil || p.Cfg == nil || !p.Cfg.Tools.IsToolEnabled("web") || !p.Cfg.Tools.Web.PreferNative {
		return false
	}
	return turnProfileToolAllowed(profile, "web_search") && providers.Capabilities(provider).NativeSearch
}

type contextRetrySleeper struct{}

func (contextRetrySleeper) Sleep(ctx context.Context, delay time.Duration) error {
	return sleepWithContext(ctx, delay)
}

func (p *Pipeline) llmRetrySettings() (int, int) {
	maxRetries := 2
	backoffSecs := 2
	if p == nil || p.Cfg == nil {
		return maxRetries, backoffSecs
	}
	if configuredRetries := p.Cfg.Agents.Defaults.MaxLLMRetries; configuredRetries > 0 {
		maxRetries = configuredRetries
	}
	if configuredBackoff := p.Cfg.Agents.Defaults.LLMRetryBackoffSecs; configuredBackoff > 0 {
		backoffSecs = configuredBackoff
	}
	return maxRetries, backoffSecs
}

func (p *Pipeline) sleepBeforeLLMRetry(ctx context.Context, delay time.Duration) error {
	if p == nil || p.retrySleeper == nil {
		return sleepWithContext(ctx, delay)
	}
	return p.retrySleeper.Sleep(ctx, delay)
}

func (p *Pipeline) maxMediaSize() int {
	if p == nil || p.Cfg == nil {
		return config.DefaultMaxMediaSize
	}
	return p.Cfg.Agents.Defaults.GetMaxMediaSize()
}

func (p *Pipeline) shouldFinalizeAfterToolLoop(exec *turnExecution, llm *LLMIterationState) bool {
	if p == nil {
		return false
	}
	return shouldFinalizeAfterToolLoopWithRenderConfig(p.Cfg, exec, llm)
}

func (p *Pipeline) filterToolContentForLLM(content string) string {
	if p == nil || p.Cfg == nil || !p.Cfg.Tools.IsFilterSensitiveDataEnabled() {
		return content
	}
	return p.Cfg.FilterSensitiveData(content)
}

func (p *Pipeline) filterPendingResultForLLM(content string) string {
	return p.filterToolContentForLLM(content)
}

func pipelineDefaultProvider(cfg *config.Config) string {
	provider := "openai"
	if cfg != nil {
		if configured := strings.TrimSpace(cfg.Agents.Defaults.Provider); configured != "" {
			provider = configured
		}
	}
	return effectiveDefaultProvider(provider)
}

func (p *Pipeline) modelCandidates(
	primary string,
	fallbacks []string,
) []providers.FallbackCandidate {
	if p == nil {
		return nil
	}
	return resolveModelCandidates(p.Cfg, pipelineDefaultProvider(p.Cfg), primary, fallbacks)
}

func (p *Pipeline) activeModelConfig(
	workspace string,
	candidates []providers.FallbackCandidate,
	activeModel string,
) *config.ModelConfig {
	if p == nil {
		return nil
	}
	return resolveActiveModelConfig(
		p.Cfg,
		workspace,
		candidates,
		activeModel,
		pipelineDefaultProvider(p.Cfg),
	)
}

func (p *Pipeline) buildTurnMessages(
	ts *turnState,
	history []providers.Message,
	summary string,
	currentMessage string,
	media []string,
	activeSkills []string,
) []providers.Message {
	return p.buildTurnMessagesWithProtectedTurnBoundary(
		ts, history, summary, currentMessage, media, activeSkills, 0,
	)
}

func (p *Pipeline) buildTurnMessagesWithProtectedTurnBoundary(
	ts *turnState,
	history []providers.Message,
	summary string,
	currentMessage string,
	media []string,
	activeSkills []string,
	protectedTurnTailCount int,
) []providers.Message {
	if p == nil || ts == nil || ts.agent == nil || ts.agent.ContextBuilder == nil {
		return nil
	}
	req := promptBuildRequestForTurn(ts, history, summary, currentMessage, media, p.Cfg)
	req.ActiveSkills = append([]string(nil), activeSkills...)
	messages := ts.agent.ContextBuilder.BuildMessagesFromPrompt(req)
	if p.Context.TerminalTasks != nil {
		terminalContext := p.Context.TerminalTasks.terminalTaskContextForTurn(ts)
		if len(terminalContext) > 0 {
			currentTurnStart := promptCurrentTurnStart(messages, currentMessage, media)
			if protectedTurnTailCount > 0 {
				currentTurnStart = normalizeCurrentTurnStart(messages, len(messages)-protectedTurnTailCount)
			}
			withTerminalContext := make([]providers.Message, 0, len(messages)+len(terminalContext))
			withTerminalContext = append(withTerminalContext, messages[:currentTurnStart]...)
			withTerminalContext = append(withTerminalContext, terminalContext...)
			withTerminalContext = append(withTerminalContext, messages[currentTurnStart:]...)
			messages = withTerminalContext
		}
	}
	return projectNodeFileMediaAttachments(messages, ts, media, p.Context.MediaResolver)
}
