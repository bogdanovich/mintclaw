package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/memory"
)

// PrepareCodingSession reconciles disposable context state with the canonical
// thread before a resumed frontend accepts another turn.
func (al *AgentLoop) PrepareCodingSession(
	ctx context.Context,
	sessionKey string,
) (memory.HistoryRevision, error) {
	if al == nil || al.contextManager == nil {
		return memory.HistoryRevision{}, fmt.Errorf("coding context manager is unavailable")
	}
	sessionKey = strings.TrimSpace(sessionKey)
	agent, _, err := al.codingRuntimeTargetForSession(sessionKey)
	if err != nil {
		return memory.HistoryRevision{}, err
	}
	reconciler, ok := al.contextManager.(interface {
		prepareCodingSession(context.Context, *AgentInstance, string) (memory.HistoryRevision, error)
	})
	if !ok {
		return memory.HistoryRevision{}, fmt.Errorf("coding context manager cannot reconcile durable history")
	}
	return reconciler.prepareCodingSession(ctx, agent, sessionKey)
}

// CompactCodingSession performs explicit foreground compaction for the one
// coding owner admitted by sessionKey. An active turn must finish or be
// interrupted first so history mutation remains single-writer.
func (al *AgentLoop) CompactCodingSession(ctx context.Context, sessionKey string) error {
	if al == nil || al.contextManager == nil {
		return fmt.Errorf("coding context manager is unavailable")
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if active, ambiguous := al.turns.uniqueActiveTurnForSession(sessionKey); ambiguous {
		return fmt.Errorf("session %s is active in multiple workspaces", sessionKey)
	} else if active != nil {
		return fmt.Errorf("session %s has an active turn", sessionKey)
	}
	agent, _, err := al.codingRuntimeTargetForSession(sessionKey)
	if err != nil {
		return err
	}
	budget := agent.ContextWindow - agent.MaxTokens
	if budget <= 0 {
		budget = agent.ContextWindow
	}
	if budget <= 0 {
		return fmt.Errorf("coding runtime agent %q has no context budget", agent.ID)
	}
	return al.contextManager.Compact(ctx, &CompactRequest{
		Agent:      agent,
		SessionKey: sessionKey,
		Workspace:  agent.Workspace,
		Reason:     ContextCompressReasonManual,
		Budget:     budget,
	})
}
