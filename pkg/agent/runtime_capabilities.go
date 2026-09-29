package agent

import (
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/identity"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/skills"
)

func (al *AgentLoop) runtimeCapabilityContext() runtimecap.Context {
	kind := runtimecap.KindGateway
	if al.usesCodingProfile() {
		kind = runtimecap.KindCoding
	}
	return runtimecap.NewContext(runtimecap.Inputs{
		Kind:      kind,
		Artifacts: al.mediaStore,
		Delivery:  al.channelManager,
		Browser:   al.runtimeBrowserClient,
	})
}

// CapabilityReport combines the current immutable runner generation's service
// diagnostics with the default agent's final admitted tool/capability report.
// A turn-bound service report additionally marks runtime.principal available
// inside tool execution context.
func (al *AgentLoop) CapabilityReport() runtimecap.Report {
	if al == nil {
		return runtimecap.Report{}
	}
	registry := al.GetRegistry()
	if registry == nil {
		return al.runtimeServiceCapabilityReport()
	}
	return al.capabilityReportForAgent(registry.GetDefaultAgent())
}

func (al *AgentLoop) capabilityReportForAgent(agent *AgentInstance) runtimecap.Report {
	runtimeReport := al.runtimeServiceCapabilityReport()
	if agent == nil || agent.toolComposer == nil {
		return runtimeReport
	}
	admission := agent.toolComposer.CapabilityReport()
	capabilities := append(
		append([]runtimecap.Availability(nil), admission.Capabilities...),
		runtimeReport.Capabilities...,
	)
	return runtimecap.NewAdmissionReport(runtimeReport.Runtime, capabilities, admission.Tools)
}

func (al *AgentLoop) runtimeServiceCapabilityReport() runtimecap.Report {
	if al == nil || al.turns == nil {
		return runtimecap.Report{}
	}
	runner := al.turns.currentRunner()
	if runner == nil || runner.pipeline == nil {
		return runtimecap.Report{}
	}
	return runner.pipeline.RuntimeCapabilities.Report()
}

func (al *AgentLoop) bindSkillCompatibilityEnvironments(registry *AgentRegistry, cfg *config.Config) {
	if al == nil || registry == nil {
		return
	}
	runtimeProduct := skills.SkillRuntimeGateway
	if al.usesCodingProfile() {
		runtimeProduct = skills.SkillRuntimeCoding
	}
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil || agent.ContextBuilder == nil {
			continue
		}
		currentAgent := agent
		agent.ContextBuilder.WithSkillCompatibilityEnvironment(newSkillCompatibilityEnvironment(
			cfg,
			runtimeProduct,
			agent.MCPServerPolicy,
			func() runtimecap.Report { return al.capabilityReportForAgent(currentAgent) },
			currentAgent.capabilityRevision.current,
		))
	}
}

func runtimePrincipalForTurn(ts *turnState, pipeline *Pipeline) runtimecap.Principal {
	if ts == nil || pipeline == nil {
		return runtimecap.Principal{}
	}
	kind := pipeline.RuntimeCapabilities.Kind()
	actorID := strings.TrimSpace(ts.opts.RuntimeActorID)
	if kind == runtimecap.KindGateway {
		actorID = gatewayRuntimeActorID(ts)
	}
	sessionID := strings.TrimSpace(ts.opts.InteractionRouteKey)
	if sessionID == "" {
		sessionID = strings.TrimSpace(ts.opts.Dispatch.RouteSessionKey)
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(ts.sessionKey)
	}
	return runtimecap.Principal{
		Runtime:     kind,
		ActorID:     actorID,
		AgentID:     routing.NormalizeAgentID(ts.agentID),
		SessionID:   sessionID,
		ExecutionID: strings.TrimSpace(effectiveToolExecutionID(ts)),
	}
}

func gatewayRuntimeActorID(ts *turnState) string {
	if ts == nil {
		return ""
	}
	inbound := ts.opts.Dispatch.InboundContext
	if ts.opts.InteractionOriginContext != nil {
		inbound = ts.opts.InteractionOriginContext
	}
	if inbound == nil {
		return ""
	}
	actorID := strings.TrimSpace(inbound.ActorID)
	if actorID == "" {
		actorID = strings.TrimSpace(inbound.SenderID)
	}
	channel := strings.ToLower(strings.TrimSpace(inbound.Channel))
	if channel == "" || actorID == "" {
		return ""
	}
	if platform, platformID, ok := identity.ParseCanonicalID(actorID); ok &&
		strings.EqualFold(strings.TrimSpace(platform), channel) {
		return identity.BuildCanonicalID(channel, platformID)
	}
	return identity.BuildCanonicalID(channel, actorID)
}
