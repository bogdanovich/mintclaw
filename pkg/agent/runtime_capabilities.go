package agent

import (
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/identity"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
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

// CapabilityReport returns the current immutable runner generation's
// construction-time service diagnostics. A turn-bound report additionally
// marks runtime.principal available inside tool execution context.
func (al *AgentLoop) CapabilityReport() runtimecap.Report {
	if al == nil || al.turns == nil {
		return runtimecap.Report{}
	}
	runner := al.turns.currentRunner()
	if runner == nil || runner.pipeline == nil {
		return runtimecap.Report{}
	}
	return runner.pipeline.RuntimeCapabilities.Report()
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
