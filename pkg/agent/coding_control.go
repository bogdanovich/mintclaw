package agent

import (
	"context"
	"fmt"
	"strings"
)

type codingContextPreparer interface {
	prepareCodingSession(context.Context, *AgentInstance, string) error
	publishCodingRetrievalTools() error
}

func (al *AgentLoop) prepareCodingContext(ctx context.Context) error {
	reconciler, ok := al.contextManager.(codingContextPreparer)
	if !ok {
		return nil
	}
	for _, agentID := range al.registry.ListAgentIDs() {
		agent, found := al.registry.GetAgent(agentID)
		if !found || agent == nil {
			continue
		}
		layout, found := al.codingProfile.AgentLayout(agentID)
		if !found {
			return fmt.Errorf("coding context has no admitted layout for agent %q", agentID)
		}
		if err := reconciler.prepareCodingSession(ctx, agent, "coding:"+layout.ThreadID()); err != nil {
			return err
		}
	}
	return reconciler.publishCodingRetrievalTools()
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
		Background: false,
	})
}
