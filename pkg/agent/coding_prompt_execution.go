package agent

import (
	"context"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func codingMessagesForProviderCall(
	ctx context.Context,
	ts *turnState,
	messages []providers.Message,
	candidates []providers.FallbackCandidate,
	modelFallback string,
	providerFallback string,
) []providers.Message {
	if ts != nil && ts.agent != nil && ts.agent.ContextBuilder != nil {
		ts.agent.ContextBuilder.refreshCodingWorkspace(ctx)
	}
	return codingMessagesForCandidate(ts, messages, candidates, modelFallback, providerFallback)
}

func codingMessagesForCandidate(
	ts *turnState,
	messages []providers.Message,
	candidates []providers.FallbackCandidate,
	modelFallback string,
	providerFallback string,
) []providers.Message {
	if ts == nil || ts.agent == nil || ts.agent.ContextBuilder == nil {
		return messages
	}

	context := ts.opts.CodingContext
	context.Model = resolvedCandidateModelName(candidates, modelFallback)
	context.Provider = resolvedCandidateProvider(candidates, providerFallback)
	if strings.TrimSpace(context.Model) == "" {
		context.Model = strings.TrimSpace(modelFallback)
	}
	return ts.agent.ContextBuilder.withCodingExecutionContext(messages, context)
}
