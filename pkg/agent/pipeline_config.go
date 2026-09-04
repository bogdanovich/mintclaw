package agent

import (
	"context"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// pipelineTurnPolicy is the immutable turn-policy snapshot for one runtime
// generation. AgentLoop replaces the owning turnRunner when configuration is
// reloaded; turns already admitted to the old generation keep these values.
type pipelineTurnPolicy struct {
	nativeWebEnabled      bool
	preferNativeSearch    bool
	maxLLMRetries         int
	llmRetryBackoffSecs   int
	maxMediaSize          int
	finalTurnRender       bool
	filterSensitiveData   bool
	filterMinLength       int
	sensitiveDataReplacer *strings.Replacer
}

func newPipelineTurnPolicy(cfg *config.Config) pipelineTurnPolicy {
	policy := pipelineTurnPolicy{
		maxLLMRetries:       2,
		llmRetryBackoffSecs: 2,
		maxMediaSize:        config.DefaultMaxMediaSize,
	}
	if cfg == nil {
		return policy
	}

	policy.nativeWebEnabled = cfg.Tools.IsToolEnabled("web")
	policy.preferNativeSearch = cfg.Tools.Web.PreferNative
	if cfg.Agents.Defaults.MaxLLMRetries > 0 {
		policy.maxLLMRetries = cfg.Agents.Defaults.MaxLLMRetries
	}
	if cfg.Agents.Defaults.LLMRetryBackoffSecs > 0 {
		policy.llmRetryBackoffSecs = cfg.Agents.Defaults.LLMRetryBackoffSecs
	}
	policy.maxMediaSize = cfg.Agents.Defaults.GetMaxMediaSize()
	policy.finalTurnRender = cfg.Agents.Defaults.UseFinalTurnRender()
	policy.filterSensitiveData = cfg.Tools.IsFilterSensitiveDataEnabled()
	policy.filterMinLength = cfg.Tools.GetFilterMinLength()
	if policy.filterSensitiveData {
		policy.sensitiveDataReplacer = cfg.SensitiveDataReplacer()
	}
	return policy
}

func (p *Pipeline) nativeSearchEnabled(
	profile config.EffectiveTurnProfile,
	provider providers.LLMProvider,
) bool {
	if p == nil || !p.turnPolicy.nativeWebEnabled || !p.turnPolicy.preferNativeSearch {
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
	if p == nil {
		return maxRetries, backoffSecs
	}
	if p.turnPolicy.maxLLMRetries > 0 {
		maxRetries = p.turnPolicy.maxLLMRetries
	}
	if p.turnPolicy.llmRetryBackoffSecs > 0 {
		backoffSecs = p.turnPolicy.llmRetryBackoffSecs
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
	if p == nil || p.turnPolicy.maxMediaSize <= 0 {
		return config.DefaultMaxMediaSize
	}
	return p.turnPolicy.maxMediaSize
}

func (p *Pipeline) shouldFinalizeAfterToolLoop(exec *turnExecution, llm *LLMIterationState) bool {
	if p == nil {
		return false
	}
	return shouldFinalizeAfterToolLoopWithRenderPolicy(p.turnPolicy.finalTurnRender, exec, llm)
}

func (p *Pipeline) filterToolContentForLLM(content string) string {
	if p == nil || !p.turnPolicy.filterSensitiveData || len(content) < p.turnPolicy.filterMinLength ||
		p.turnPolicy.sensitiveDataReplacer == nil {
		return content
	}
	return p.turnPolicy.sensitiveDataReplacer.Replace(content)
}

func (p *Pipeline) filterPendingResultForLLM(content string) string {
	return p.filterToolContentForLLM(content)
}

func (p *Pipeline) modelCandidates(
	primary string,
	fallbacks []string,
) []providers.FallbackCandidate {
	if p == nil {
		return nil
	}
	return resolveModelCandidates(p.Cfg, primary, fallbacks)
}

func (p *Pipeline) activeModelConfig(
	workspace string,
	candidates []providers.FallbackCandidate,
	activeModel string,
) *config.ModelConfig {
	if p == nil {
		return nil
	}
	return resolveActiveModelConfig(p.Cfg, workspace, candidates, activeModel)
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
