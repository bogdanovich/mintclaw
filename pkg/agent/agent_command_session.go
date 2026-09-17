package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/commands"
)

func (al *AgentLoop) bindSessionCommandCapabilities(
	rt *commands.Runtime,
	ctx context.Context,
	opts *turnSpec,
	agent *AgentInstance,
) {
	rt.GetEnabledChannels = func() []string {
		if al.channelManager == nil {
			return nil
		}
		return al.channelManager.GetEnabledChannels()
	}
	rt.GetCurrentTurn = func() *commands.TurnInfo {
		if opts == nil || agent == nil {
			return nil
		}
		info := al.GetActiveTurnByScope(agent.Workspace, opts.Dispatch.SessionKey)
		if info == nil {
			return nil
		}
		return &commands.TurnInfo{
			TurnID:       info.TurnID,
			ParentTurnID: info.ParentTurnID,
			Depth:        info.Depth,
			ChildTurnIDs: append([]string(nil), info.ChildTurnIDs...),
		}
	}
	rt.SwitchChannel = func(value string) error {
		if al.channelManager == nil {
			return fmt.Errorf("channel manager not initialized")
		}
		if _, exists := al.channelManager.GetChannel(value); !exists && value != "cli" {
			return fmt.Errorf("channel '%s' not found or not enabled", value)
		}
		return nil
	}
	rt.StopActiveTurn = func() (commands.StopResult, error) {
		if opts == nil {
			return commands.StopResult{}, fmt.Errorf("turn specification not available")
		}
		if agent == nil {
			return commands.StopResult{}, fmt.Errorf("workspace agent not available")
		}
		return al.stopActiveTurnForScope(newRuntimeSessionScope(agent.Workspace, opts.Dispatch.SessionKey))
	}
	rt.ReloadConfig = func() error {
		if al.reloadFunc == nil {
			return fmt.Errorf("reload not configured")
		}
		return al.reloadFunc()
	}

	if agent == nil {
		return
	}
	if agent.ContextBuilder != nil {
		rt.ListSkillNames = agent.ContextBuilder.ListSkillNames
	}
	rt.ClearHistory = func() error {
		if opts == nil {
			return fmt.Errorf("turn specification not available")
		}
		// /clear can arrive before any turn has persisted session scope
		// metadata (runAgentLoop records it per turn), so record it here to
		// let the ContextManager resolve which agent owns the session.
		ensureSessionMetadata(
			agent.Sessions,
			opts.Dispatch.SessionKey,
			opts.Dispatch.SessionScope,
		)
		return al.contextManager.Clear(ctx, agent, opts.Dispatch.SessionKey)
	}
	rt.ResetSession = func(clearOverride bool) (string, error) {
		if opts == nil {
			return "", fmt.Errorf("turn specification not available")
		}
		routeSessionKey := commandRouteSessionKey(opts)
		if routeSessionKey == "" {
			return "", fmt.Errorf("route session key not available")
		}
		baseSessionKey := strings.TrimSpace(opts.Dispatch.BaseSessionKey)
		if baseSessionKey == "" {
			baseSessionKey = strings.TrimSpace(opts.Dispatch.SessionKey)
		}
		if baseSessionKey == "" {
			baseSessionKey = routeSessionKey
		}
		if clearOverride {
			if err := al.clearSessionModelOverride(routeSessionKey); err != nil {
				return "", err
			}
			if err := al.clearAutoModelSelection(routeSessionKey); err != nil {
				return "", err
			}
			if err := al.clearSessionOverride(baseSessionKey); err != nil {
				return "", err
			}
			return "", al.clearSessionGoal(routeSessionKey)
		}

		nextSessionKey := buildResetSessionKey(agent.ID, baseSessionKey)
		if nextSessionKey == "" {
			return "", fmt.Errorf("failed to allocate reset session key")
		}
		if err := al.clearSessionModelOverride(routeSessionKey); err != nil {
			return "", err
		}
		if err := al.clearAutoModelSelection(routeSessionKey); err != nil {
			return "", err
		}
		if err := al.setSessionOverride(baseSessionKey, nextSessionKey); err != nil {
			return "", err
		}
		return nextSessionKey, nil
	}
	rt.StartFreshSession = func() (string, error) {
		routeSessionKey := commandRouteSessionKey(opts)
		if routeSessionKey == "" {
			return "", fmt.Errorf("route session key not available")
		}
		if err := al.clearSessionGoal(routeSessionKey); err != nil {
			return "", err
		}
		return rt.ResetSession(false)
	}

	rt.GetToolFeedback = func() (bool, string) {
		enabledByConfig := al.cfg != nil && al.cfg.Agents.Defaults.IsToolFeedbackEnabled()
		routeSessionKey := commandRouteSessionKey(opts)
		if routeSessionKey == "" {
			return enabledByConfig, "config default"
		}
		if enabled, ok := al.getToolFeedbackOverride(routeSessionKey); ok {
			return enabled, "conversation override"
		}
		return enabledByConfig, "config default"
	}
	rt.SetToolFeedback = func(mode string) (bool, string, error) {
		routeSessionKey := commandRouteSessionKey(opts)
		if routeSessionKey == "" {
			return false, "", fmt.Errorf("route session key not available")
		}

		switch strings.ToLower(strings.TrimSpace(mode)) {
		case "on":
			if err := al.setToolFeedbackOverride(routeSessionKey, true); err != nil {
				return false, "", err
			}
			return true, "conversation override", nil
		case "off":
			if err := al.setToolFeedbackOverride(routeSessionKey, false); err != nil {
				return false, "", err
			}
			return false, "conversation override", nil
		case "default":
			if err := al.clearToolFeedbackOverride(routeSessionKey); err != nil {
				return false, "", err
			}
			enabled := al.cfg != nil && al.cfg.Agents.Defaults.IsToolFeedbackEnabled()
			return enabled, "config default", nil
		default:
			return false, "", fmt.Errorf("unsupported mode %q", mode)
		}
	}
	rt.AskSideQuestion = func(ctx context.Context, question string) (string, error) {
		return al.askSideQuestion(ctx, agent, opts, question)
	}
}
