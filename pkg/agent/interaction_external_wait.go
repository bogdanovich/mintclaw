package agent

import (
	"context"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func (al *AgentLoop) externalWaitState(ctx context.Context, record interactions.Record) toolshared.ExternalWaitState {
	agents := al.GetRegistry()
	if agents == nil {
		return toolshared.ExternalWaitUnavailable
	}
	agent, ok := agents.GetAgent(record.Route.AgentID)
	if !ok || agent == nil || agent.Tools == nil {
		return toolshared.ExternalWaitUnavailable
	}
	capabilities, _ := al.runtimeCapabilitySnapshot()
	toolCtx, cancel, err := interactionOriginToolContext(ctx, agent, record, capabilities)
	if err != nil {
		return toolshared.ExternalWaitUnavailable
	}
	defer cancel()
	// The executing child session owns the tool condition; the independently
	// restored route still identifies the parent conversation for delivery.
	toolCtx = toolshared.WithToolSessionContext(toolCtx, agent.ID, interactionContinuationSessionKey(record), nil)
	return agent.Tools.ResolveExternalWait(toolCtx, record.Origin.ToolName, record.ConditionRef)
}

func (al *AgentLoop) recoverExternalWait(
	ctx context.Context, workspace string, registry *interactions.Registry, record interactions.Record, now time.Time,
) bool {
	outcome := interactions.OutcomeUnavailable
	switch al.externalWaitState(ctx, record) {
	case toolshared.ExternalWaitPending:
		return false
	case toolshared.ExternalWaitReady:
		outcome = interactions.OutcomeReady
	}
	claimed, err := registry.ClaimExternalWait(record.ID, record.Revision, outcome)
	return err == nil && al.recoverClaimedInteractionAt(ctx, workspace, claimed, now)
}

// External waits have no chat approval/question controls. Keep transport
// metadata compatible while retaining durable correlation on ordinary messages.
func interactionFinalMetadata(record interactions.Record) bus.OutboundMetadata {
	metadata := bus.OutboundMetadata{
		InteractionID: record.ID, InteractionShortID: record.ShortID,
	}
	if record.Kind != interactions.KindExternalWait {
		metadata.InteractionKind = string(record.Kind)
		metadata.InteractionControls = bus.OutboundInteractionControlsRemove
	}
	return metadata
}

// A final model result can describe completed effects. Losing an unrelated
// readiness lease must not overwrite it or cause that work to run again.
func externalWaitHasFinalResult(history []providers.Message, record interactions.Record) bool {
	_, _, complete := interactionFinalAfterToolResult(history, record.Origin.ToolCallID)
	return complete
}

func externalWaitTerminalDeliverable(record interactions.Record, content string) *taskresult.Deliverable {
	result := &taskresult.Deliverable{Text: content}
	for _, receipt := range record.OutcomeReceipts {
		if receipt.Kind == taskresult.ReceiptKindResourceCleanup {
			result.LifecycleReceipts = append(
				result.LifecycleReceipts,
				taskresult.CloneReceipts([]taskresult.Receipt{receipt})...)
		}
	}
	return result
}
