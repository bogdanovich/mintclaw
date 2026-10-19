package agent

import (
	"context"
	"errors"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// Retain only the runtime's resolved outbound capability, not protected page
// content, model artifact references, or resolved filesystem paths.
func retainSuppressedMedia(ts *turnState, cfg *config.Config, result *toolshared.ToolResult) *taskresult.Deliverable {
	if ts == nil || !ts.opts.SuppressToolUserDelivery || result == nil || result.IsError ||
		result.Control.TaskSuspended || result.Delivery.Outbound == nil {
		return nil
	}
	out := result.Delivery.Outbound
	if out.Recovery == nil || len(out.AdditionalRecovery) != 0 || !browserMediaRecovery(*out.Recovery) {
		return nil
	}
	if !routedMediaRecovery(ts, browserMediaWorkspace(ts, cfg), *out.Recovery) {
		return nil
	}
	if _, err := bus.NormalizeOutboundMediaMessage(bus.OutboundMediaMessage{
		Channel: ts.channel, ChatID: ts.chatID, Context: bus.NewOutboundContext(ts.channel, ts.chatID, ""),
		Parts: out.Media, Recovery: out.Recovery,
	}); err != nil {
		return nil
	}
	for _, part := range out.Media {
		if part.Ref == out.Recovery.MediaRef {
			return &taskresult.Deliverable{Artifacts: []taskresult.Artifact{{
				Ref: part.Ref, Kind: part.Type, Filename: part.Filename, ContentType: part.ContentType,
				Recovery: taskresult.CloneArtifactRecovery(out.Recovery),
			}}}
		}
	}
	return nil
}

func browserMediaRecovery(recovery bus.OutboundRecovery) bool {
	return recovery.Kind == bus.OutboundRecoveryBrowserScreenshot ||
		recovery.Kind == bus.OutboundRecoveryBrowserDownload
}

func browserMediaWorkspace(ts *turnState, cfg *config.Config) string {
	if cfg != nil {
		return cfg.WorkspacePath()
	}
	return ts.workspace
}

func routedMediaRecovery(ts *turnState, workspace string, recovery bus.OutboundRecovery) bool {
	if ts == nil {
		return false
	}
	owner, err := nodeFileMediaOwnerForWorkspace(ts, workspace)
	// Delegation changes agent and execution identities, not the authenticated
	// actor, workspace or original channel route. Keep the original spool owner.
	return err == nil && recovery.WorkspaceID == owner.WorkspaceID &&
		recovery.ActorID == owner.ActorID && recovery.RouteID == owner.RouteID
}

type browserMediaDeliverySource interface {
	ClaimArtifactDelivery(context.Context, bus.OutboundRecovery) error
}

func hasRetainedToolMedia(result *toolshared.ToolResult) bool {
	for _, artifact := range toolResultArtifacts(result) {
		if !artifact.Delivered && artifact.Recovery != nil {
			return true
		}
	}
	return false
}

// Bind the same source claims used by direct browser output to the existing
// durable publication boundary. Nothing is claimed when a child is suppressed.
func (al *AgentLoop) bindRetainedMediaDelivery(ts *turnState, result *toolshared.ToolResult) error {
	var recoveries []bus.OutboundRecovery
	for _, artifact := range toolResultArtifacts(result) {
		if artifact.Delivered || artifact.Recovery == nil {
			continue
		}
		recovery := artifact.Recovery
		if recovery.MediaRef != artifact.Ref ||
			!browserMediaRecovery(*recovery) || !routedMediaRecovery(ts, browserMediaWorkspace(ts, al.cfg), *recovery) {
			return errors.New("retained attachment delivery authority is invalid")
		}
		recoveries = append(recoveries, *taskresult.CloneArtifactRecovery(recovery))
	}
	if len(recoveries) == 0 {
		return nil
	}
	if !supportsDurableDeliveryReceipts(al.channelManager) {
		return errors.New("retained attachment delivery requires durable channel receipts")
	}
	capabilities := ts.runtimeCapabilities
	client, ok := capabilities.BrowserClient()
	if !ok {
		capabilities, _ = al.runtimeCapabilitySnapshot()
		client, _ = capabilities.BrowserClient()
	}
	source, ok := client.(browserMediaDeliverySource)
	if !ok {
		return errors.New("retained attachment delivery source is unavailable")
	}
	recovery := taskresult.CloneArtifactRecovery(&recoveries[0])
	out := result.Delivery.Outbound
	message, err := bus.NormalizeOutboundMediaMessage(bus.OutboundMediaMessage{
		Channel: ts.channel, ChatID: ts.chatID, Context: bus.NewOutboundContext(ts.channel, ts.chatID, ""),
		Parts: out.Media, Recovery: recovery, AdditionalRecovery: recoveries[1:],
	})
	if err != nil {
		return err
	}
	out.Recovery = message.Recovery
	out.AdditionalRecovery = message.AdditionalRecovery
	result.Delivery.Commit = func(ctx context.Context) error {
		for _, item := range recoveries {
			if err := source.ClaimArtifactDelivery(ctx, item); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}
