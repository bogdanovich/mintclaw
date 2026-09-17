// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/commands"
)

func (al *AgentLoop) handleCommand(
	ctx context.Context,
	msg bus.InboundMessage,
	modelBinding effectiveModelBinding,
	opts *turnSpec,
) (string, bool) {
	normalizeTurnSpecInPlace(opts)

	if !commands.HasCommandPrefix(msg.Content) {
		return "", false
	}

	if matched, handled, reply := al.applyExplicitSkillCommand(
		msg.Content,
		modelBinding.WorkspaceAgent,
		opts,
	); matched {
		return reply, handled
	}

	if al.cmdRegistry == nil {
		return "", false
	}

	rt := al.buildCommandsRuntime(ctx, modelBinding, opts)
	executor := commands.NewExecutor(al.cmdRegistry, rt)

	var commandReply string
	result := executor.Execute(ctx, commands.Request{
		Channel:  msg.Context.Channel,
		ChatID:   msg.Context.ChatID,
		SenderID: msg.Context.SenderID,
		Text:     msg.Content,
		Reply: func(text string) error {
			commandReply = text
			return nil
		},
	})

	switch result.Outcome {
	case commands.OutcomeHandled:
		if result.Err != nil {
			return mapCommandError(result), true
		}
		if commandReply != "" {
			return commandReply, true
		}
		return "", true
	default: // OutcomePassthrough — let the message fall through to LLM
		return "", false
	}
}

func (al *AgentLoop) applyExplicitSkillCommand(
	raw string,
	agent *AgentInstance,
	opts *turnSpec,
) (matched bool, handled bool, reply string) {
	normalizeTurnSpecInPlace(opts)

	cmdName, ok := commands.CommandName(raw)
	if !ok || cmdName != "use" {
		return false, false, ""
	}

	if agent == nil || agent.ContextBuilder == nil {
		return true, true, commandsUnavailableSkillMessage()
	}

	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) < 2 {
		return true, true, buildUseCommandHelp(agent)
	}

	arg := strings.TrimSpace(parts[1])
	if strings.EqualFold(arg, "clear") || strings.EqualFold(arg, "off") {
		if opts != nil {
			al.clearPendingSkills(newRuntimeSessionScope(agent.Workspace, opts.Dispatch.SessionKey))
		}
		return true, true, "Cleared pending skill override."
	}

	skillName, ok := agent.ContextBuilder.ResolveSkillName(arg)
	if !ok {
		return true, true, fmt.Sprintf("Unknown skill: %s\nUse /list skills to see installed skills.", arg)
	}

	if len(parts) < 3 {
		if opts == nil || strings.TrimSpace(opts.Dispatch.SessionKey) == "" {
			return true, true, commandsUnavailableSkillMessage()
		}
		al.setPendingSkills(
			newRuntimeSessionScope(agent.Workspace, opts.Dispatch.SessionKey), []string{skillName},
		)
		return true, true, fmt.Sprintf(
			"Skill %q is armed for your next message. Send your next prompt normally, or use /use clear to cancel.",
			skillName,
		)
	}

	message := strings.TrimSpace(strings.Join(parts[2:], " "))
	if message == "" {
		return true, true, buildUseCommandHelp(agent)
	}

	if opts != nil {
		opts.ForcedSkills = append(opts.ForcedSkills, skillName)
		opts.Dispatch.UserMessage = message
		if opts.Dispatch.InboundContext != nil {
			opts.Dispatch.InboundContext.Relation = standaloneInboundMessageRelation(
				message,
				opts.Dispatch.Media,
			)
		}
	}

	return true, false, ""
}

func (al *AgentLoop) buildCommandsRuntime(
	ctx context.Context,
	modelBinding effectiveModelBinding,
	opts *turnSpec,
) *commands.Runtime {
	normalizeTurnSpecInPlace(opts)

	registry := al.GetRegistry()
	cfg := al.GetConfig()
	agent := modelBinding.WorkspaceAgent
	rt := &commands.Runtime{
		ListAgentIDs:    registry.ListAgentIDs,
		ListDefinitions: al.cmdRegistry.Definitions,
	}
	al.bindMCPCommandCapabilities(rt, cfg)
	al.bindGoalCommandCapabilities(rt, modelBinding, opts)
	al.bindModelCommandCapabilities(rt, cfg, modelBinding, opts, agent)
	al.bindSessionCommandCapabilities(rt, ctx, opts, agent)
	al.bindContextCommandCapabilities(rt, modelBinding, opts, agent)
	return rt
}

func commandRouteSessionKey(opts *turnSpec) string {
	if opts == nil {
		return ""
	}
	return strings.TrimSpace(opts.Dispatch.RouteSessionKey)
}

func (al *AgentLoop) setPendingSkills(scope runtimeSessionScope, skillNames []string) {
	if !scope.complete() || len(skillNames) == 0 {
		return
	}

	filtered := make([]string, 0, len(skillNames))
	for _, name := range skillNames {
		name = strings.TrimSpace(name)
		if name != "" {
			filtered = append(filtered, name)
		}
	}
	if len(filtered) == 0 {
		return
	}

	al.pendingSkills.Store(scope, filtered)
}

func (al *AgentLoop) takePendingSkills(scope runtimeSessionScope) []string {
	if !scope.complete() {
		return nil
	}

	value, ok := al.pendingSkills.LoadAndDelete(scope)
	if !ok {
		return nil
	}

	skills, ok := value.([]string)
	if !ok {
		return nil
	}

	return append([]string(nil), skills...)
}

func (al *AgentLoop) clearPendingSkills(scope runtimeSessionScope) {
	if !scope.complete() {
		return
	}
	al.pendingSkills.Delete(scope)
}
