package agent

import (
	"context"
	"fmt"
	"strings"

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
	) ([]providers.Attachment, toolResultDeliveryOutcome, error)
}

func requiresTerminalDeliverySettlement(ts *turnState, result *toolshared.ToolResult) bool {
	return ts != nil && !ts.opts.SuppressToolUserDelivery &&
		isFinalHandledDelivery(result) && hasToolResultDeliveryPayload(result)
}

func hasToolResultDeliveryPayload(result *toolshared.ToolResult) bool {
	if result == nil {
		return false
	}
	if result.Outbound != nil {
		return strings.TrimSpace(result.Outbound.Text) != "" || len(result.Outbound.Media) > 0
	}
	if len(toolResultMediaRefs(result)) > 0 {
		return true
	}
	if strings.TrimSpace(toolResultUserText(result)) == "" {
		return false
	}
	return !result.Silent || result.Deliverable != nil || result.AsyncDelivery == toolshared.AsyncDeliveryUserOnly
}

func (al *AgentLoop) syncToolResultDelivery() *syncToolResultDelivery {
	if al == nil {
		return nil
	}
	return &syncToolResultDelivery{deliverToUser: al.deliverToolResultToUser}
}

func normalizeToolResultForSyncDelivery(ts *turnState, result *toolshared.ToolResult) *toolshared.ToolResult {
	if result == nil {
		return toolshared.ErrorResult("nil tool result")
	}
	if ts != nil && ts.opts.SuppressToolUserDelivery {
		result.ResponseHandled = false
		result.ImmediateDelivery = false
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

	if !ts.opts.SuppressToolUserDelivery && result.ImmediateDelivery {
		if len(deliveredToolResultMediaRefs(result)) > 0 && !hasOutboundTransaction(ctx) {
			err := fmt.Errorf("durable outbound transaction is required for immediate media delivery")
			return nil, wrapToolDeliveryError(result, err.Error(), err)
		}
		if d == nil || d.deliverToUser == nil {
			return nil, toolshared.ErrorResult("tool result delivery is not initialized")
		}
		_, outcome, err := d.deliverToUser(ctx, ts, result, toolName)
		if err != nil {
			return nil, wrapToolDeliveryError(result, fmt.Sprintf("failed to deliver attachment: %v", err), err)
		}
		if outcome != toolResultDeliveryNone {
			markToolResultMediaDelivered(result, deliveredToolResultMediaRefs(result))
		}
	}

	if !ts.opts.SuppressToolUserDelivery && result.ResponseHandled {
		if d == nil || d.deliverToUser == nil {
			return nil, toolshared.ErrorResult("tool result delivery is not initialized")
		}
		attachments, outcome, err := d.deliverToUser(ctx, ts, result, toolName)
		if err != nil {
			return nil, wrapToolDeliveryError(result, fmt.Sprintf("failed to deliver attachment: %v", err), err)
		}
		if outcome != toolResultDeliveryNone {
			markToolResultMediaDelivered(result, deliveredToolResultMediaRefs(result))
		}
		if outcome != toolResultDeliveryDirect && len(toolResultMediaRefs(result)) > 0 {
			result.ResponseHandled = false
		}
		if outcome == toolResultDeliveryDirect {
			return attachments, result
		}
	}

	return nil, result
}

func confirmToolResultOutbound(result *toolshared.ToolResult) {
	if result == nil || result.ConfirmOutbound == nil {
		return
	}
	confirm := result.ConfirmOutbound
	result.ConfirmOutbound = nil
	confirm()
}

func commitToolResultOutbound(ctx context.Context, result *toolshared.ToolResult) error {
	if result == nil || result.CommitOutbound == nil {
		return nil
	}
	commit := result.CommitOutbound
	result.CommitOutbound = nil
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
