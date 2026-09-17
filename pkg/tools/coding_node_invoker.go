package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

var (
	ErrCodingNodeUnavailable     = errors.New("coding node is unavailable")
	ErrCodingInvocationUncertain = errors.New("coding node invocation outcome is uncertain")
)

const codingTaskStartInvocationTimeout = 60

// CodingNodeOperationError retains only the bounded node failure code. Remote
// messages and causes are intentionally excluded from its public rendering.
type CodingNodeOperationError struct {
	code string
}

func (err *CodingNodeOperationError) Error() string {
	return "coding node operation failed (" + err.code + ")"
}

func CodingNodeOperationErrorCode(err error) (string, bool) {
	var operationErr *CodingNodeOperationError
	if !errors.As(err, &operationErr) {
		return "", false
	}
	return operationErr.code, true
}

// CodingNodeInvocationSource adds transport-only ephemeral content to the
// existing durable invocation source. Coding objectives and steering text are
// digest-bound by the durable plan but deliberately omitted from both gateway
// and companion invocation records.
type CodingNodeInvocationSource interface {
	NodeInvocationSource
	DispatchInvocationEphemeral(
		ctx context.Context,
		owner nodes.GatewayInvocationOwner,
		invocationID string,
		expectedPlanHash string,
		ephemeralInput json.RawMessage,
	) (result json.RawMessage, dispatched bool, err error)
}

// CodingInvocationAuthority is the raw gateway owner identity used to derive
// the hashed principal stored in invocation ledgers. Callers must retain the
// same agent, routed session, actor, and workspace for the task lifetime.
type CodingInvocationAuthority struct {
	AgentID     string
	SessionID   string
	ActorID     string
	Workspace   string
	ExecutionID string
	OperationID string
}

func (authority CodingInvocationAuthority) validate() error {
	for _, value := range []string{
		authority.AgentID,
		authority.SessionID,
		authority.ActorID,
		authority.Workspace,
		authority.ExecutionID,
		authority.OperationID,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return errors.New("coding invocation authority is incomplete")
		}
	}
	return nil
}

// CodingNodeInvoker is the narrow trusted adapter for internal coding
// commands. It deliberately bypasses the generic model command contract while
// retaining target policy, approved descriptor, durable prepare-before-send,
// authenticated WSS, and exact invocation-ledger recovery.
type CodingNodeInvoker struct {
	access *nodeTargetAccess
	source CodingNodeInvocationSource
}

func NewCodingNodeInvoker(
	options NodeToolOptions,
	source CodingNodeInvocationSource,
) *CodingNodeInvoker {
	return &CodingNodeInvoker{
		access: newNodeTargetAccess(options, source),
		source: source,
	}
}

func (invoker *CodingNodeInvoker) Invoke(
	ctx context.Context,
	authority CodingInvocationAuthority,
	target string,
	command string,
	input any,
	ephemeral any,
) (json.RawMessage, error) {
	if invoker == nil || invoker.access == nil || invoker.source == nil ||
		authority.validate() != nil || !nodes.IsCodingCommand(command) {
		return nil, ErrCodingNodeUnavailable
	}
	resolved, err := (&nodeInvocationToolRuntime{
		access: invoker.access,
		source: invoker.source,
	}).resolveTarget(authority.AgentID, target, true)
	if err != nil || resolved.registration == nil || resolved.requiresReapproval {
		return nil, fmt.Errorf("%w: target authority is unavailable", ErrCodingNodeUnavailable)
	}
	descriptor, advertised := nodeCatalogDescriptor(resolved.snapshot.Catalog, command)
	if !advertised || descriptor.ModelContract != nil {
		return nil, fmt.Errorf("%w: coding command is unavailable", ErrCodingNodeUnavailable)
	}
	approved, err := resolved.registration.ApprovedCommand(command)
	if err != nil || !sameCodingDescriptor(descriptor, approved) {
		return nil, fmt.Errorf("%w: coding command approval changed", ErrCodingNodeUnavailable)
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode coding command input: %w", err)
	}
	principal := codingInvocationPrincipal(authority)
	invocationID := stableNodeInvocationID(
		"coding_inv",
		authority.AgentID,
		authority.SessionID,
		authority.ActorID,
		authority.ExecutionID,
		authority.OperationID,
		command,
	)
	toolCallID := stableNodeInvocationID(
		"coding_call",
		authority.ExecutionID,
		authority.OperationID,
		command,
	)
	timeoutSeconds := defaultNodeInvocationTimeout
	if command == nodes.CodingCommandTaskStart {
		timeoutSeconds = codingTaskStartInvocationTimeout
	}
	request := nodes.InvocationRequest{
		InvocationID:     invocationID,
		IdempotencyKey:   stableNodeInvocationID("coding_idem", invocationID),
		NodeID:           resolved.snapshot.ID,
		CatalogHash:      resolved.snapshot.CatalogHash,
		Command:          command,
		Input:            inputJSON,
		AgentID:          principal.AgentID,
		SessionID:        principal.SessionID,
		ActorID:          principal.ActorID,
		TimeoutSeconds:   timeoutSeconds,
		OutputLimitBytes: nodes.MinCodingTaskOutputBytes,
	}
	profile := nodes.ExecutionProfile{
		Executor:       resolved.snapshot.Executor,
		PolicyRevision: resolved.snapshot.PolicyRevision,
	}
	plan, err := nodes.PrepareExecutionPlan(
		request,
		descriptor,
		profile.Executor,
		profile.PolicyRevision,
		time.Now(),
		nodes.MaxExecutionPlanTTL,
	)
	if err != nil {
		return nil, fmt.Errorf("prepare coding command: %w", err)
	}
	record, created, err := invoker.source.PrepareInvocation(
		resolved.binding.Node,
		resolved.name,
		toolCallID,
		principal,
		plan,
		descriptor,
		true,
		func(current NodeDiscoveryRecord) error {
			if current.Registration == nil || !current.Connected ||
				current.Snapshot.ID != resolved.snapshot.ID ||
				current.Snapshot.CatalogHash != resolved.snapshot.CatalogHash ||
				current.Snapshot.PolicyRevision != resolved.snapshot.PolicyRevision {
				return ErrNodeDiscoveryStale
			}
			currentDescriptor, ok := nodeCatalogDescriptor(current.Snapshot.Catalog, command)
			if !ok || !sameCodingDescriptor(currentDescriptor, descriptor) {
				return ErrNodeDiscoveryStale
			}
			currentApproved, approvalErr := current.Registration.ApprovedCommand(command)
			if approvalErr != nil || !sameCodingDescriptor(currentApproved, descriptor) {
				return ErrNodeDiscoveryStale
			}
			return nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("%w: prepare coding invocation", ErrCodingNodeUnavailable)
	}
	if !created {
		if validationErr := validateRetainedNodeInvocation(
			record,
			resolved.name,
			request,
			descriptor,
			profile,
		); validationErr != nil {
			return nil, fmt.Errorf(
				"coding invocation identity conflicts with retained authority: %w",
				validationErr,
			)
		}
	}
	if record.State == nodes.GatewayInvocationDispatched {
		return invoker.recoverResult(ctx, principal, record)
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
	var result json.RawMessage
	var dispatched bool
	if ephemeral != nil {
		ephemeralJSON, marshalErr := json.Marshal(ephemeral)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode coding command content: %w", marshalErr)
		}
		result, dispatched, err = invoker.source.DispatchInvocationEphemeral(
			ctx,
			owner,
			record.Plan.InvocationID,
			record.ExpectedPlanHash,
			ephemeralJSON,
		)
	} else {
		result, dispatched, err = invoker.source.DispatchInvocation(
			ctx,
			owner,
			record.Plan.InvocationID,
			record.ExpectedPlanHash,
		)
	}
	if err == nil {
		return result, nil
	}
	if code, remote := nodes.InvocationDispatchErrorCode(err); remote {
		return nil, &CodingNodeOperationError{code: code}
	}
	if dispatched || errors.Is(err, nodes.ErrGatewayInvocationDispatched) {
		result, recoveryErr := invoker.recoverResult(ctx, principal, record)
		if recoveryErr == nil {
			return result, nil
		}
		if _, definitive := CodingNodeOperationErrorCode(recoveryErr); definitive {
			return nil, recoveryErr
		}
		return nil, errors.Join(ErrCodingInvocationUncertain, recoveryErr)
	}
	return nil, fmt.Errorf("coding invocation was not dispatched: %w", err)
}

func (invoker *CodingNodeInvoker) recoverResult(
	ctx context.Context,
	principal nodes.GatewayInvocationPrincipal,
	record nodes.GatewayInvocationRecord,
) (json.RawMessage, error) {
	remote, err := invoker.source.QueryInvocation(
		ctx,
		principal,
		record.Target,
		record.Plan.NodeID,
		record.Plan.InvocationID,
	)
	if err != nil {
		return nil, err
	}
	if remote.State == nodes.InvocationSucceeded {
		return remote.Result, nil
	}
	if !remote.State.Terminal() {
		return nil, ErrCodingInvocationUncertain
	}
	if remote.Failure != nil {
		return nil, &CodingNodeOperationError{code: remote.Failure.Code}
	}
	return nil, fmt.Errorf("coding invocation ended in state %s", remote.State)
}

func codingInvocationPrincipal(authority CodingInvocationAuthority) nodes.GatewayInvocationPrincipal {
	return nodes.GatewayInvocationPrincipal{
		AgentID:     stableNodeInvocationID("agent", authority.AgentID),
		SessionID:   stableNodeInvocationID("session", authority.SessionID),
		ActorID:     stableNodeInvocationID("actor", authority.ActorID),
		WorkspaceID: stableNodeInvocationID("workspace", authority.Workspace),
		ExecutionID: stableNodeInvocationID(
			"execution_scope",
			authority.Workspace,
			authority.ExecutionID,
		),
	}
}

func sameCodingDescriptor(left, right nodes.CommandDescriptor) bool {
	leftHash, leftErr := left.Hash()
	rightHash, rightErr := right.Hash()
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}
