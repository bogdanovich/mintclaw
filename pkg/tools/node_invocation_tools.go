package tools

import (
	"context"
	"errors"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func NewNodeInvokeTool(cfg *config.Config, source NodeInvocationSource) *NodeInvokeTool {
	return &NodeInvokeTool{runtime: newNodeInvocationToolRuntime(cfg, source)}
}

func NewNodeStatusTool(cfg *config.Config, source NodeInvocationSource) *NodeStatusTool {
	return &NodeStatusTool{runtime: newNodeInvocationToolRuntime(cfg, source)}
}

func NewNodeCancelTool(cfg *config.Config, source NodeInvocationSource) *NodeCancelTool {
	return &NodeCancelTool{runtime: newNodeInvocationToolRuntime(cfg, source)}
}

// SetEventPublisher injects the runtime event bus used for node invocation audit events.
func (tool *NodeInvokeTool) SetEventPublisher(eventBus runtimeevents.Bus) {
	if tool != nil && tool.runtime != nil {
		tool.runtime.runtimeEvents = eventBus
	}
}

// SetEventPublisher injects the runtime event bus used for node status audit events.
func (tool *NodeStatusTool) SetEventPublisher(eventBus runtimeevents.Bus) {
	if tool != nil && tool.runtime != nil {
		tool.runtime.runtimeEvents = eventBus
	}
}

// SetEventPublisher injects the runtime event bus used for cancellation audit events.
func (tool *NodeCancelTool) SetEventPublisher(eventBus runtimeevents.Bus) {
	if tool != nil && tool.runtime != nil {
		tool.runtime.runtimeEvents = eventBus
	}
}

func newNodeInvocationToolRuntime(
	cfg *config.Config,
	source NodeInvocationSource,
) *nodeInvocationToolRuntime {
	return &nodeInvocationToolRuntime{
		access: newNodeTargetAccess(cfg, source),
		source: source,
	}
}

func (*NodeInvokeTool) Name() string { return "nodes_invoke" }

func (*NodeInvokeTool) Description() string {
	return "Invoke one approved typed command on an operator-configured node target. " +
		"Use nodes describe first to discover visible commands. Never invent target or command names."
}

func (*NodeInvokeTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{
				"type":        "string",
				"description": "Visible target name. Omit only when the agent has a default target.",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Approved versioned command name from nodes describe.",
			},
			"input": map[string]any{
				"type":        "object",
				"description": "Typed command input matching the advertised command schema.",
			},
			"discovery_revision": map[string]any{
				"type":        "string",
				"description": "Opaque revision returned by command-specific nodes describe.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Optional execution timeout from 1 to 3600 seconds.",
			},
			"output_limit_bytes": map[string]any{
				"type":        "integer",
				"description": "Optional bounded result size from 1 to 524288 bytes.",
			},
		},
		"required":             []string{"command", "input", "discovery_revision"},
		"additionalProperties": false,
	}
}

func (tool *NodeInvokeTool) ApprovalArguments(
	ctx context.Context,
	args map[string]any,
) (map[string]any, error) {
	record, err := tool.runtime.prepare(ctx, args)
	if err != nil {
		return nil, err
	}
	approval := map[string]any{
		"target":          record.Target,
		"invocation_id":   record.Plan.InvocationID,
		"node_id":         string(record.Plan.NodeID),
		"command":         record.Plan.Command,
		"risk":            record.Plan.Risk,
		"plan_hash":       record.ExpectedPlanHash,
		"policy_revision": record.Plan.PolicyRevision,
		"expires_at":      record.Plan.ExpiresAt,
	}
	if record.Plan.Update != nil {
		approval["current_version"] = record.Plan.Update.CurrentVersion
		approval["requested_release"] = record.Plan.Update.ReleaseVersion
		approval["platform"] = record.Plan.Update.Platform
		approval["architecture"] = record.Plan.Update.Architecture
	}
	return approval, nil
}

func (tool *NodeInvokeTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	record, err := tool.runtime.prepare(ctx, args)
	if err != nil {
		var denial *nodeSafeDenialError
		if errors.As(err, &denial) {
			return nodeDenialToolResult(nodeDenialResult{
				Status:     "denied",
				Code:       denial.Code,
				Constraint: denial.Constraint,
				Action:     denial.Action,
			})
		}
		if errors.Is(err, errDiscoveryStale) {
			return nodeDenialToolResult(nodeDenialResult{
				Status:     "denied",
				Code:       nodeDenialDiscoveryStale,
				Constraint: nodeConstraintCommandPolicy,
				Action:     nodeActionRefreshDiscovery,
			})
		}
		return nodeDenialToolResult(nodeDenialResult{
			Status:     "denied",
			Code:       nodeDenialCommandUnavailable,
			Constraint: nodeConstraintCommandPolicy,
			Action:     nodeActionRefreshDiscovery,
		})
	}
	requiresApproval := record.Plan.Command == "shell.exec.v1" ||
		(record.Descriptor.ModelContract != nil &&
			record.Descriptor.ModelContract.ApprovalMode == "each_command")
	if requiresApproval &&
		!toolshared.ToolApprovalContinuation(ctx) &&
		!toolshared.ToolApprovalBypass(ctx) {
		return nodeDenialToolResult(nodeDenialResult{
			Status:     "denied",
			Code:       nodeDenialApprovalRequired,
			Constraint: nodeConstraintApproval,
			Action:     nodeActionAskOperator,
		})
	}
	owner := nodes.GatewayInvocationOwner{
		Target:      record.Target,
		AgentID:     record.Plan.AgentID,
		SessionID:   record.Plan.SessionID,
		ActorID:     record.Plan.ActorID,
		ToolCallID:  record.ToolCallID,
		WorkspaceID: record.WorkspaceID,
		ExecutionID: record.ExecutionID,
	}
	result, dispatched, err := tool.runtime.source.DispatchInvocation(
		ctx,
		owner,
		record.Plan.InvocationID,
		record.ExpectedPlanHash,
	)
	if err != nil {
		if errorCode, remoteRejection := nodes.InvocationDispatchErrorCode(err); remoteRejection &&
			errorCode != nodes.InvocationDispatchUnknown {
			state := "rejected"
			message := "the node rejected the invocation"
			switch errorCode {
			case nodes.InvocationDispatchExecutionFailed:
				state = string(nodes.InvocationFailed)
				message = "the node reported a terminal invocation failure"
			case nodes.InvocationDispatchCanceled:
				state = string(nodes.InvocationCanceled)
				message = "the node reported the invocation canceled"
			}
			if dispatched {
				tool.runtime.publishInvocationEvent(
					ctx,
					NodeInvocationObservationDispatched,
					"nodes_invoke",
					record,
					string(nodes.GatewayInvocationDispatched),
					"",
				)
			}
			tool.runtime.publishInvocationEvent(
				ctx,
				NodeInvocationObservationRejected,
				"nodes_invoke",
				record,
				state,
				errorCode,
			)
			view := nodeInvokeResult{
				InvocationID: record.Plan.InvocationID,
				Target:       record.Target,
				Command:      record.Plan.Command,
				Risk:         record.Plan.Risk,
				GatewayState: nodes.GatewayInvocationDispatched,
				State:        state,
				ErrorCode:    errorCode,
			}
			return nodeInvocationError(errorCode, message, &view)
		}
		if errors.Is(err, nodes.ErrGatewayInvocationDispatched) || dispatched {
			if dispatched {
				tool.runtime.publishInvocationEvent(
					ctx,
					NodeInvocationObservationDispatched,
					"nodes_invoke",
					record,
					string(nodes.GatewayInvocationDispatched),
					"",
				)
			}
			tool.runtime.publishInvocationEvent(
				ctx,
				NodeInvocationObservationUncertain,
				"nodes_invoke",
				record,
				string(nodes.InvocationUnknown),
				"DISPATCH_UNCERTAIN",
			)
			view := nodeInvokeResult{
				InvocationID:   record.Plan.InvocationID,
				Target:         record.Target,
				Command:        record.Plan.Command,
				Risk:           record.Plan.Risk,
				GatewayState:   nodes.GatewayInvocationDispatched,
				State:          string(nodes.InvocationUnknown),
				ErrorCode:      "DISPATCH_UNCERTAIN",
				RecoveryAction: "Call nodes_status with this invocation_id; do not replay the command.",
			}
			return nodeInvocationError(
				"DISPATCH_UNCERTAIN",
				"the invocation outcome is uncertain",
				&view,
			)
		}
		view := nodeInvokeResult{
			InvocationID: record.Plan.InvocationID,
			Target:       record.Target,
			Command:      record.Plan.Command,
			Risk:         record.Plan.Risk,
			GatewayState: nodes.GatewayInvocationPrepared,
			State:        "not_dispatched",
			ErrorCode:    "DISPATCH_DENIED",
		}
		return nodeInvocationError(
			"DISPATCH_DENIED",
			"the gateway rejected dispatch before contacting the node",
			&view,
		)
	}
	tool.runtime.publishInvocationEvent(
		ctx,
		NodeInvocationObservationDispatched,
		"nodes_invoke",
		record,
		string(nodes.GatewayInvocationDispatched),
		"",
	)
	tool.runtime.publishInvocationEvent(
		ctx,
		NodeInvocationObservationCompleted,
		"nodes_invoke",
		record,
		string(nodes.InvocationSucceeded),
		"",
		result,
	)
	return nodeJSONResult(nodeInvokeResult{
		InvocationID: record.Plan.InvocationID,
		Target:       record.Target,
		Command:      record.Plan.Command,
		Risk:         record.Plan.Risk,
		GatewayState: nodes.GatewayInvocationDispatched,
		State:        string(nodes.InvocationSucceeded),
		Result:       result,
	})
}

func (*NodeInvokeTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (*NodeStatusTool) Name() string { return "nodes_status" }

func (*NodeStatusTool) Description() string {
	return "Inspect one invocation owned by this agent, conversation, and actor. " +
		"This recovery query never retries or replays the command."
}

func (*NodeStatusTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"invocation_id": map[string]any{
				"type":        "string",
				"description": "Invocation ID returned by nodes_invoke.",
			},
		},
		"required":             []string{"invocation_id"},
		"additionalProperties": false,
	}
}

func (tool *NodeStatusTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	record, principal, snapshot, available, err := tool.runtime.visibleInvocation(ctx, args)
	if err != nil {
		return nodeInvocationError("INVOCATION_UNAVAILABLE", err.Error(), nil)
	}
	view := gatewayStatusResult(record, available)
	if record.State == nodes.GatewayInvocationPrepared {
		view.State = string(nodes.GatewayInvocationPrepared)
		return nodeJSONResult(view)
	}
	if isNodeFileTransferCommand(record.Plan.Command) {
		return tool.runtime.fileTransferStatus(ctx, record, principal, available)
	}
	remote, attempts, err := tool.runtime.queryInvocationStatus(
		ctx,
		principal,
		record.Target,
		snapshot.ID,
		record.Plan.InvocationID,
	)
	if err != nil {
		view.State = string(nodes.InvocationUnknown)
		view.ErrorCode, view.RecoveryAction = nodeInvocationStatusFailure(err)
		view.StatusAttempts = attempts
		tool.runtime.publishInvocationEvent(
			ctx,
			NodeInvocationObservationUncertain,
			"nodes_status",
			record,
			view.State,
			view.ErrorCode,
		)
		return nodeJSONResult(view)
	}
	if remote.State.Terminal() {
		errorCode := ""
		if remote.Failure != nil {
			errorCode = remote.Failure.Code
		}
		tool.runtime.publishInvocationEvent(
			ctx,
			NodeInvocationObservationStatus,
			"nodes_status",
			record,
			string(remote.State),
			errorCode,
			remote.Result,
		)
	}
	view = remoteStatusResult(record, remote, available)
	view.StatusAttempts = attempts
	return nodeJSONResult(view)
}

func (runtime *nodeInvocationToolRuntime) queryInvocationStatus(
	ctx context.Context,
	principal nodes.GatewayInvocationPrincipal,
	target string,
	nodeID nodes.ID,
	invocationID string,
) (nodes.InvocationRecord, int, error) {
	for attempt := 1; attempt <= defaultNodeStatusAttempts; attempt++ {
		remote, err := runtime.source.QueryInvocation(ctx, principal, target, nodeID, invocationID)
		if err == nil {
			return remote, attempt, nil
		}
		if attempt == defaultNodeStatusAttempts || !retryableNodeInvocationStatusError(err) {
			return nodes.InvocationRecord{}, attempt, err
		}
		delay := defaultNodeStatusRetryDelays[attempt-1]
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nodes.InvocationRecord{}, attempt, nodeInvocationQueryContextError(ctx.Err())
		case <-timer.C:
		}
	}
	return nodes.InvocationRecord{}, defaultNodeStatusAttempts, nodes.NewInvocationQueryError(
		nodes.InvocationQueryTransportUnavailable,
		nil,
	)
}

func nodeInvocationQueryContextError(err error) error {
	code := nodes.InvocationQueryCanceled
	if errors.Is(err, context.DeadlineExceeded) {
		code = nodes.InvocationQueryTimeout
	}
	return nodes.NewInvocationQueryError(code, err)
}

func retryableNodeInvocationStatusError(err error) bool {
	code, classified := nodes.InvocationQueryErrorCode(err)
	if !classified {
		return false
	}
	switch code {
	case nodes.InvocationQueryNotFound,
		nodes.InvocationQueryNodeUnavailable,
		nodes.InvocationQueryTransportUnavailable:
		return true
	default:
		return false
	}
}

func nodeInvocationStatusFailure(err error) (string, string) {
	code, classified := nodes.InvocationQueryErrorCode(err)
	if !classified {
		code = nodes.InvocationQueryTransportUnavailable
	}
	switch code {
	case nodes.InvocationQueryNotFound:
		return code, "The node did not observe the invocation after bounded status polling; do not replay automatically."
	case nodes.InvocationQueryLedgerUnavailable:
		return code, "Inspect the node invocation ledger, then retry nodes_status; do not replay the original command."
	case nodes.InvocationQueryNodeUnavailable:
		return code, "Retry nodes_status after the target reconnects; do not replay the original command."
	case nodes.InvocationQueryTimeout:
		return code, "Retry nodes_status with sufficient time; do not replay the original command."
	case nodes.InvocationQueryCanceled:
		return code, "The status query was canceled; do not replay the original command."
	case nodes.InvocationQueryRejected:
		return code, "The node rejected the status query; inspect node policy and ledger health."
	default:
		return nodes.InvocationQueryTransportUnavailable,
			"Retry nodes_status after transport recovery; do not replay the original command."
	}
}

func (*NodeStatusTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

func (*NodeCancelTool) Name() string { return "nodes_cancel" }

func (*NodeCancelTool) Description() string {
	return "Request cancellation of one running node invocation owned by this exact execution scope. " +
		"Cancellation is idempotent and never replays the original command."
}

func (*NodeCancelTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"invocation_id": map[string]any{
				"type":        "string",
				"description": "Invocation ID returned by nodes_invoke.",
			},
		},
		"required":             []string{"invocation_id"},
		"additionalProperties": false,
	}
}

func (tool *NodeCancelTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	record, principal, snapshot, available, err := tool.runtime.visibleInvocation(ctx, args)
	if err != nil {
		return nodeJSONResult(nodeCancelResult{Status: "denied", ErrorCode: "CANCEL_DENIED"})
	}
	view := nodeCancelResult{
		InvocationID: record.Plan.InvocationID,
		Target:       record.Target,
		Command:      record.Plan.Command,
	}
	if record.State == nodes.GatewayInvocationPrepared {
		view.Status = "already_terminal"
		view.OriginalState = "not_dispatched"
		return nodeJSONResult(view)
	}
	if isNodeFileTransferCommand(record.Plan.Command) {
		if !available {
			view.Status = "unknown"
			view.ErrorCode = "NODE_UNAVAILABLE"
			view.RecoveryAction = "Call nodes_status after the target reconnects; do not replay cancellation or the transfer."
			return nodeJSONResult(view)
		}
		return tool.runtime.cancelFileTransfer(ctx, record, principal)
	}
	remote, _, err := tool.runtime.source.CancelInvocation(
		ctx,
		principal,
		record.Target,
		snapshot.ID,
		record.Plan.InvocationID,
	)
	if err != nil {
		if errors.Is(err, nodes.ErrGatewayInvocationConflict) ||
			errors.Is(err, nodes.ErrGatewayInvocationNotFound) ||
			errors.Is(err, nodes.ErrGatewayInvocationNotDispatched) {
			return nodeJSONResult(nodeCancelResult{Status: "denied", ErrorCode: "CANCEL_DENIED"})
		}
		view.Status = "unknown"
		view.ErrorCode = "CANCEL_OUTCOME_UNKNOWN"
		view.RecoveryAction = "Call nodes_status; do not replay cancellation or the original command."
		tool.runtime.publishInvocationEvent(
			ctx,
			NodeInvocationObservationUncertain,
			"nodes_cancel",
			record,
			view.Status,
			view.ErrorCode,
		)
		return nodeJSONResult(view)
	}
	view.OriginalState = string(remote.State)
	view.Cancellation = remote.Cancellation
	switch {
	case remote.State == nodes.InvocationCanceled &&
		remote.Cancellation != nil &&
		remote.Cancellation.TerminationConfirmed:
		view.Status = "canceled"
	case remote.State.Terminal():
		view.Status = "already_terminal"
	case remote.Cancellation != nil:
		view.Status = "cancel_requested"
	default:
		view.Status = "unknown"
		view.ErrorCode = "CANCEL_OUTCOME_UNKNOWN"
		view.RecoveryAction = "Call nodes_status; do not replay cancellation or the original command."
	}
	tool.runtime.publishInvocationEvent(
		ctx,
		NodeInvocationObservationCancel,
		"nodes_cancel",
		record,
		view.Status,
		view.ErrorCode,
	)
	return nodeJSONResult(view)
}

func (*NodeCancelTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}
