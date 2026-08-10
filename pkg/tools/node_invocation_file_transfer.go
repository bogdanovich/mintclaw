package tools

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func (runtime *nodeInvocationToolRuntime) fileTransferStatus(
	ctx context.Context,
	record nodes.GatewayInvocationRecord,
	principal nodes.GatewayInvocationPrincipal,
	available bool,
) *toolshared.ToolResult {
	result := NodeFileTransferResult{
		TransferID:     record.Plan.InvocationID,
		State:          string(nodes.InvocationUnknown),
		PolicyRevision: record.Plan.PolicyRevision,
	}
	if !available {
		result.Code = "NODE_UNAVAILABLE"
		result.RecoveryAction = "Retry nodes_status after the target reconnects; do not replay the transfer."
		runtime.publishInvocationEvent(
			ctx,
			NodeInvocationObservationUncertain,
			"nodes_status",
			record,
			result.State,
			result.Code,
		)
		return nodeJSONResult(result)
	}
	source, ok := runtime.source.(NodeFileTransferSource)
	if !ok {
		result.Code = "STATUS_UNAVAILABLE"
		result.RecoveryAction = "Retry nodes_status; do not replay the transfer."
		runtime.publishInvocationEvent(
			ctx,
			NodeInvocationObservationUncertain,
			"nodes_status",
			record,
			result.State,
			result.Code,
		)
		return nodeJSONResult(result)
	}
	remote, err := source.QueryFileTransfer(ctx, principal, record)
	if err != nil {
		result.Code = "STATUS_UNAVAILABLE"
		result.RecoveryAction = "Retry nodes_status; do not replay the transfer."
		runtime.publishInvocationEvent(
			ctx,
			NodeInvocationObservationUncertain,
			"nodes_status",
			record,
			result.State,
			result.Code,
		)
		return nodeJSONResult(result)
	}
	remote.TransferID = record.Plan.InvocationID
	remote.PolicyRevision = record.Plan.PolicyRevision
	runtime.publishInvocationEvent(
		ctx,
		NodeInvocationObservationStatus,
		"nodes_status",
		record,
		remote.State,
		remote.Code,
	)
	return nodeJSONResult(remote)
}

func (runtime *nodeInvocationToolRuntime) cancelFileTransfer(
	ctx context.Context,
	record nodes.GatewayInvocationRecord,
	principal nodes.GatewayInvocationPrincipal,
) *toolshared.ToolResult {
	view := nodeCancelResult{
		InvocationID: record.Plan.InvocationID,
		Target:       record.Target,
		Command:      record.Plan.Command,
	}
	source, ok := runtime.source.(NodeFileTransferSource)
	if !ok {
		view.Status = "denied"
		view.ErrorCode = "CANCEL_DENIED"
		runtime.publishInvocationEvent(
			ctx,
			NodeInvocationObservationCancel,
			"nodes_cancel",
			record,
			view.Status,
			view.ErrorCode,
		)
		return nodeJSONResult(view)
	}
	result, requested, err := source.CancelFileTransfer(ctx, principal, record)
	if err != nil {
		if requested {
			view.Status = "unknown"
			view.ErrorCode = "CANCEL_OUTCOME_UNKNOWN"
			view.RecoveryAction = "Call nodes_status; do not replay cancellation or the transfer."
		} else {
			view.Status = "denied"
			view.ErrorCode = "CANCEL_DENIED"
		}
		observation := NodeInvocationObservationCancel
		if requested {
			observation = NodeInvocationObservationUncertain
		}
		runtime.publishInvocationEvent(
			ctx,
			observation,
			"nodes_cancel",
			record,
			view.Status,
			view.ErrorCode,
		)
		return nodeJSONResult(view)
	}
	view.OriginalState = result.State
	switch {
	case result.RecoveryAction == "already_committed" || result.State == "committed":
		view.Status = "already_committed"
	case result.State == "canceled":
		view.Status = "canceled"
	case result.State == "cancel_requested":
		view.Status = "cancel_requested"
	case result.State == "denied":
		view.Status = "denied"
		view.ErrorCode = result.Code
	case result.State == "unknown":
		view.Status = "unknown"
		view.ErrorCode = "CANCEL_OUTCOME_UNKNOWN"
		view.RecoveryAction = "Call nodes_status; do not replay cancellation or the transfer."
	default:
		view.Status = "already_terminal"
	}
	runtime.publishInvocationEvent(
		ctx,
		NodeInvocationObservationCancel,
		"nodes_cancel",
		record,
		view.Status,
		view.ErrorCode,
	)
	return nodeJSONResult(view)
}
