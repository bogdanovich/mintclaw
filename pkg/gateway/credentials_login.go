package gateway

import (
	"context"
	"errors"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type credentialNotifier func(context.Context, string) error

// The existing outbound channel carries only a generic notice and a locator
// requiring the owner's passkey. It carries no alias, provider ID or bearer
// approval, and the route always comes from authenticated inbound context.
func credentialNotificationPublisher(messageBus *bus.MessageBus) credentialNotifier {
	return func(ctx context.Context, approvalURL string) error {
		inbound := toolshared.ToolInboundContext(ctx)
		if messageBus == nil || inbound.Channel == "" || inbound.ChatID == "" {
			return errCredentialsUnavailable
		}
		outbound := bus.NewOutboundContext(inbound.Channel, inbound.ChatID, inbound.MessageID)
		outbound.TopicID, outbound.Account = inbound.TopicID, inbound.Account
		return messageBus.PublishOutbound(ctx, bus.OutboundMessage{
			Context: outbound, SessionKey: toolshared.ToolRouteSessionKey(ctx),
			Content: "MintClaw needs account access. Review and approve with your phone passkey: " + approvalURL,
		})
	}
}

func (source *gatewayBrowserToolSource) CredentialLoginAvailable() bool {
	return source != nil && source.services != nil && source.services.Credentials != nil &&
		source.services.Credentials.loginEnabled &&
		(source.services.Credentials.consumeLogin != nil || source.services.Credentials.consumeWebsiteLogin != nil) &&
		len(source.credentialProfiles) > 0
}

func browserCredentialProfiles(policy config.BrowserToolsConfig) map[string]struct{} {
	profiles := make(map[string]struct{})
	for targetName, target := range policy.Targets {
		if !target.Enabled || target.EffectivePlacement() != config.BrowserPlacementGateway ||
			target.EffectiveProvider() != config.BrowserProviderLocal || target.Driver != config.BrowserDriverPlaywrightLibrary {
			continue
		}
		for name, profile := range target.Profiles {
			if profile.Enabled && profile.Mode == config.BrowserProfileEphemeral && !profile.DryRun &&
				profile.AllowApprovedActions && profile.CapabilityMode == config.BrowserCapabilityFullAccess {
				profiles[gatewayBrowserProfileKey(targetName, name)] = struct{}{}
			}
		}
	}
	return profiles
}

// LoginCredential derives authority from runtime context and current browser
// state. Phone waiting holds neither the browser lock nor any credential value.
func (source *gatewayBrowserToolSource) LoginCredential(ctx context.Context,
	selection browser.CredentialLoginRequest,
) browser.CredentialLoginResult {
	denied := browser.CredentialLoginResult{Outcome: "denied"}
	if !source.CredentialLoginAvailable() || ctx == nil || ctx.Err() != nil || selection.Owner.Validate() != nil {
		return denied
	}
	principal, ok := source.credentialPrincipal(ctx, selection.Owner)
	if !ok {
		return denied
	}
	session, err := withGatewayBrowserBroker(ctx, source,
		func(ctx context.Context, broker *browser.Broker) (browser.Session, error) {
			return broker.Status(ctx, selection.Owner, selection.SessionID)
		})
	if err != nil || session.FrameID != "" || session.TabID != selection.TabID ||
		session.SnapshotID != selection.SnapshotID || session.SnapshotGeneration != selection.SnapshotGeneration {
		return denied
	}
	if _, supported := source.credentialProfiles[gatewayBrowserProfileKey(session.Target, session.Profile)]; !supported {
		return denied
	}
	runtime := source.services.Credentials
	if selection.Fields.Step != "" {
		return source.loginDiscoveredCredential(ctx, selection, session, principal)
	}
	scope, flow, err := runtime.controller.LoginRequest(ctx, principal, selection.Account, session.SnapshotOrigin)
	if err != nil {
		return denied
	}
	_, waiting, err := runtime.credentialLeaseOrApproval(ctx, scope)
	if errors.Is(err, errCredentialApprovalRequired) {
		return browser.CredentialLoginResult{
			Alias: scope.Account, Outcome: "approval_required",
			ApprovalWait: &browser.CredentialApprovalWait{Reference: waiting.Reference, Deadline: waiting.Deadline},
		}
	} else if err != nil {
		return denied
	}
	lease, consume, err := runtime.prepareLogin(ctx, scope)
	if err != nil {
		return denied
	}
	selection.Principal, selection.PolicyRevision = principal, scope.PolicyRevision
	selection.Fields = browser.CredentialFields{
		UsernameRef: selection.Fields.UsernameRef, PasswordRef: selection.Fields.PasswordRef,
		SubmitRef:  selection.Fields.SubmitRef,
		SuccessURL: flow.SuccessURL, SuccessMarkerID: flow.SuccessMarkerID,
		HandoffURL: flow.HandoffURL, HandoffMarkerID: flow.HandoffMarkerID,
	}
	result, err := withGatewayBrowserBroker(ctx, source,
		func(ctx context.Context, broker *browser.Broker) (browser.CredentialLoginResult, error) {
			return broker.LoginCredential(ctx, selection, lease, consume), nil
		})
	if err != nil {
		return denied
	}
	return result
}

func (source *gatewayBrowserToolSource) credentialPrincipal(
	ctx context.Context,
	owner browser.Owner,
) (credentialbroker.Principal, bool) {
	if !source.CredentialLoginAvailable() || ctx == nil || ctx.Err() != nil || owner.Validate() != nil {
		return credentialbroker.Principal{}, false
	}
	agentID := strings.TrimSpace(toolshared.ToolAgentID(ctx))
	if runtime, ok := toolshared.RuntimeCapabilities(ctx); ok && runtime.Kind() == runtimecap.KindCoding {
		principal, bound := runtime.Principal()
		if !bound || principal.Validate() != nil {
			return credentialbroker.Principal{}, false
		}
		agentID = principal.AgentID
	}
	if agentID == "" {
		return credentialbroker.Principal{}, false
	}
	agentID = routing.NormalizeAgentID(agentID)
	if browser.OpaqueAgentID(agentID) != owner.AgentID {
		return credentialbroker.Principal{}, false
	}
	runtime := source.services.Credentials
	return credentialbroker.Principal{
		OwnerID: runtime.ownerID,
		ActorID: owner.ActorID,
		AgentID: agentID,
		HostID:  runtime.hostID,
	}, true
}

func (source *gatewayBrowserToolSource) CredentialApprovalState(
	ctx context.Context,
	owner browser.Owner,
	reference string,
) toolshared.ExternalWaitState {
	principal, ok := source.credentialPrincipal(ctx, owner)
	if !ok {
		return toolshared.ExternalWaitUnavailable
	}
	switch source.services.Credentials.controller.ResolveWait(ctx, principal, reference) {
	case intent.ApprovalPending:
		return toolshared.ExternalWaitPending
	case intent.GrantActive:
		return toolshared.ExternalWaitReady
	default:
		return toolshared.ExternalWaitUnavailable
	}
}
