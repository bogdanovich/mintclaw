package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type syncToolResultDelivery struct {
	deliverToUser func(
		ctx context.Context,
		ts *turnState,
		result *toolshared.ToolResult,
		toolName string,
		traceScopes []runtimeevents.TraceScope,
		metadata bus.OutboundMetadata,
	) ([]providers.Attachment, toolResultDeliveryOutcome, error)
}

func requiresTerminalDeliverySettlement(ts *turnState, result *toolshared.ToolResult) bool {
	return ts != nil && result != nil && !result.Control.TaskSuspended && !ts.opts.SuppressToolUserDelivery &&
		isFinalHandledDelivery(result) && hasToolResultDeliveryPayload(result)
}

func hasToolResultDeliveryPayload(result *toolshared.ToolResult) bool {
	if result == nil {
		return false
	}
	if result.Delivery.Outbound != nil {
		return strings.TrimSpace(result.Delivery.Outbound.Text) != "" || len(result.Delivery.Outbound.Media) > 0
	}
	if len(toolResultMediaRefs(result)) > 0 {
		return true
	}
	if strings.TrimSpace(toolResultUserText(result)) == "" {
		return false
	}
	return result.Delivery.Intent != toolshared.DeliverySilent || result.Deliverable != nil ||
		result.Delivery.AsyncMode == toolshared.AsyncDeliveryUserOnly
}

func normalizeToolResultForSyncDelivery(ts *turnState, result *toolshared.ToolResult) *toolshared.ToolResult {
	if result == nil {
		return toolshared.ErrorResult("nil tool result")
	}
	if result.Control.TaskSuspended {
		result.ForUser = ""
		result.Media = nil
		result.Deliverable = nil
		result.Delivery = toolshared.ToolDelivery{Intent: toolshared.DeliveryFinalHandled}
	} else if ts != nil && ts.opts.SuppressToolUserDelivery {
		result.Delivery.Intent = toolshared.DeliverySilent
	}
	return result
}

func (d *syncToolResultDelivery) applySyncToolResultDelivery(
	ctx context.Context,
	ts *turnState,
	result *toolshared.ToolResult,
	toolName string,
) ([]providers.Attachment, *toolshared.ToolResult) {
	result = normalizeToolResultForSyncDelivery(ts, result)
	if result.Control.TaskSuspended {
		return nil, result
	}

	if !ts.opts.SuppressToolUserDelivery && result.Delivery.IsImmediate() {
		if len(deliveredToolResultMediaRefs(result)) > 0 && !hasOutboundTransaction(ctx) {
			err := fmt.Errorf("durable outbound transaction is required for immediate media delivery")
			return nil, wrapToolDeliveryError(result, err.Error(), err)
		}
		if d == nil || d.deliverToUser == nil {
			return nil, toolshared.ErrorResult("tool result delivery is not initialized")
		}
		_, outcome, err := d.deliverToUser(ctx, ts, result, toolName, nil, bus.OutboundMetadata{})
		if err != nil {
			return nil, wrapToolDeliveryError(result, fmt.Sprintf("failed to deliver attachment: %v", err), err)
		}
		if outcome != toolResultDeliveryNone {
			markToolResultMediaDelivered(result, deliveredToolResultMediaRefs(result))
		}
	}

	if !ts.opts.SuppressToolUserDelivery && result.Delivery.IsFinalHandled() {
		if d == nil || d.deliverToUser == nil {
			return nil, toolshared.ErrorResult("tool result delivery is not initialized")
		}
		attachments, outcome, err := d.deliverToUser(
			ctx, ts, result, toolName, nil, bus.OutboundMetadata{},
		)
		if err != nil {
			return nil, wrapToolDeliveryError(result, fmt.Sprintf("failed to deliver attachment: %v", err), err)
		}
		if outcome != toolResultDeliveryNone {
			markToolResultMediaDelivered(result, deliveredToolResultMediaRefs(result))
		}
		if outcome != toolResultDeliveryDirect && len(toolResultMediaRefs(result)) > 0 {
			result.Delivery.Intent = toolshared.DeliveryDefault
		}
		if outcome == toolResultDeliveryDirect {
			return attachments, result
		}
	}

	return nil, result
}

func confirmToolResultOutbound(result *toolshared.ToolResult) {
	if result == nil || result.Delivery.Confirm == nil {
		return
	}
	confirm := result.Delivery.Confirm
	result.Delivery.Confirm = nil
	confirm()
}

func commitToolResultOutbound(ctx context.Context, result *toolshared.ToolResult) error {
	if result == nil || result.Delivery.Commit == nil {
		return nil
	}
	commit := result.Delivery.Commit
	result.Delivery.Commit = nil
	return commit(ctx)
}

func wrapToolDeliveryError(
	original *toolshared.ToolResult,
	message string,
	err error,
) *toolshared.ToolResult {
	wrapped := toolshared.ErrorResult(message).WithError(err)
	if original == nil {
		return wrapped
	}
	wrapped.Deliverable = taskresult.CloneDeliverable(original.Deliverable)
	if len(original.WriteAudit) > 0 {
		wrapped.WriteAudit = append(wrapped.WriteAudit, original.WriteAudit...)
	}
	return wrapped
}
