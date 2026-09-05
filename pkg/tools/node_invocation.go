package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	defaultNodeInvocationTimeout = 30
	defaultNodeInvocationOutput  = 64 * 1024
	defaultNodeStatusAttempts    = 3
)

var defaultNodeStatusRetryDelays = []time.Duration{250 * time.Millisecond, 750 * time.Millisecond}

// ErrNodeDiscoveryStale marks a failed atomic preparation revalidation.
var ErrNodeDiscoveryStale = errors.New("node discovery revision is stale")

var (
	errDiscoveryStale       = ErrNodeDiscoveryStale
	errNodeTargetNotPaired  = errors.New("target is not paired and approved")
	errNodeTargetNotVisible = errors.New("target is not visible to this agent")
)

const (
	nodeDenialTargetUnavailable   = "TARGET_UNAVAILABLE"
	nodeDenialCommandUnavailable  = "COMMAND_UNAVAILABLE"
	nodeDenialReapprovalRequired  = "REAPPROVAL_REQUIRED"
	nodeDenialDiscoveryIncomplete = "DISCOVERY_INCOMPLETE"
	nodeDenialDiscoveryStale      = "DISCOVERY_STALE"
	nodeDenialSchemaInvalid       = "SCHEMA_INVALID"
	nodeDenialConstraintViolation = "CONSTRAINT_VIOLATION"
	nodeDenialApprovalRequired    = "APPROVAL_REQUIRED"
	nodeDenialGatewayCapacity     = "GATEWAY_CAPACITY_EXHAUSTED"

	nodeConstraintInputSchema   = "input_schema"
	nodeConstraintInputSize     = "input_size"
	nodeConstraintExecutable    = "executable_alias"
	nodeConstraintProfile       = "profile_alias"
	nodeConstraintWorkingScope  = "working_scope"
	nodeConstraintEnvironment   = "environment_name"
	nodeConstraintTimeout       = "timeout"
	nodeConstraintOutputLimit   = "output_limit"
	nodeConstraintCommandPolicy = "command_policy"
	nodeConstraintApproval      = "approval"
	nodeConstraintGatewayStore  = "gateway_store"

	nodeActionRefreshDiscovery = "refresh_discovery"
	nodeActionCorrectInput     = "correct_input"
	nodeActionAskOperator      = "ask_operator"
)

type nodeSafeDenialError struct {
	Code       string
	Constraint string
	Action     string
	cause      error
}

func (denial *nodeSafeDenialError) Error() string {
	return "node invocation denied"
}

func (denial *nodeSafeDenialError) Unwrap() error {
	return denial.cause
}

func (denial *nodeSafeDenialError) SafeApprovalDenialResult() *toolshared.ToolResult {
	return nodeDenialToolResult(nodeDenialResult{
		Status:     "denied",
		Code:       denial.Code,
		Constraint: denial.Constraint,
		Action:     denial.Action,
	})
}

func denyNodeInvocation(
	code string,
	constraint string,
	action string,
	cause error,
) error {
	return &nodeSafeDenialError{
		Code: code, Constraint: constraint, Action: action, cause: cause,
	}
}

func denyStaleNodeDiscovery() error {
	return denyNodeInvocation(
		nodeDenialDiscoveryStale,
		nodeConstraintCommandPolicy,
		nodeActionRefreshDiscovery,
		errDiscoveryStale,
	)
}

type NodeInvocationSource interface {
	NodeDiscoverySource
	PrepareInvocation(
		nodeRef string,
		target string,
		toolCallID string,
		principal nodes.GatewayInvocationPrincipal,
		plan nodes.ExecutionPlan,
		descriptor nodes.CommandDescriptor,
		allowCreate bool,
		validate func(NodeDiscoveryRecord) error,
	) (nodes.GatewayInvocationRecord, bool, error)
	LookupInvocationByToolCall(
		principal nodes.GatewayInvocationPrincipal,
		toolCallID string,
	) (nodes.GatewayInvocationRecord, bool, error)
	LookupInvocation(
		principal nodes.GatewayInvocationPrincipal,
		invocationID string,
	) (nodes.GatewayInvocationRecord, bool, error)
	DispatchInvocation(
		ctx context.Context,
		owner nodes.GatewayInvocationOwner,
		invocationID string,
		expectedPlanHash string,
	) (result json.RawMessage, dispatched bool, err error)
	QueryInvocation(
		ctx context.Context,
		principal nodes.GatewayInvocationPrincipal,
		target string,
		nodeID nodes.ID,
		invocationID string,
	) (nodes.InvocationRecord, error)
	CancelInvocation(
		ctx context.Context,
		principal nodes.GatewayInvocationPrincipal,
		target string,
		nodeID nodes.ID,
		invocationID string,
	) (record nodes.InvocationRecord, requested bool, err error)
}

type NodeInvokeTool struct {
	runtime *nodeInvocationToolRuntime
}

func (tool *NodeInvokeTool) approvalBypassOwner() toolshared.Tool { return tool }

func (tool *NodeInvokeTool) approvalBypassesTarget(target string) bool {
	return tool != nil && tool.runtime != nil && tool.runtime.access != nil &&
		tool.runtime.access.bypassesApproval(target)
}

type NodeStatusTool struct {
	runtime *nodeInvocationToolRuntime
}

type NodeCancelTool struct {
	runtime *nodeInvocationToolRuntime
}

type nodeInvocationToolRuntime struct {
	access        *nodeTargetAccess
	source        NodeInvocationSource
	runtimeEvents runtimeevents.Bus
	eventSource   string
}

type resolvedNodeTarget struct {
	name               string
	binding            config.ExecutionTarget
	snapshot           nodes.Snapshot
	registration       *nodes.Registration
	available          bool
	requiresReapproval bool
}

type nodeDenialResult struct {
	Status     string `json:"status"`
	Code       string `json:"code"`
	Constraint string `json:"constraint"`
	Action     string `json:"action"`
}

type nodeInvokeResult struct {
	InvocationID   string                       `json:"invocation_id"`
	Target         string                       `json:"target"`
	Command        string                       `json:"command"`
	Risk           nodes.Risk                   `json:"risk"`
	GatewayState   nodes.GatewayInvocationState `json:"gateway_state"`
	State          string                       `json:"state"`
	Result         json.RawMessage              `json:"result,omitempty"`
	ErrorCode      string                       `json:"error_code,omitempty"`
	RecoveryAction string                       `json:"recovery_action,omitempty"`
}

type nodeStatusResult struct {
	InvocationID   string                        `json:"invocation_id"`
	Target         string                        `json:"target"`
	Command        string                        `json:"command"`
	Risk           nodes.Risk                    `json:"risk"`
	GatewayState   nodes.GatewayInvocationState  `json:"gateway_state"`
	State          string                        `json:"state"`
	NodeAvailable  bool                          `json:"node_available"`
	AcceptedAt     int64                         `json:"accepted_at,omitempty"`
	UpdatedAt      int64                         `json:"updated_at,omitempty"`
	CompletedAt    int64                         `json:"completed_at,omitempty"`
	Result         json.RawMessage               `json:"result,omitempty"`
	Failure        *nodes.InvocationFailure      `json:"failure,omitempty"`
	Cancellation   *nodes.InvocationCancellation `json:"cancellation,omitempty"`
	ErrorCode      string                        `json:"error_code,omitempty"`
	RecoveryAction string                        `json:"recovery_action,omitempty"`
	StatusAttempts int                           `json:"status_attempts,omitempty"`
}

type nodeCancelResult struct {
	InvocationID   string                        `json:"invocation_id"`
	Target         string                        `json:"target,omitempty"`
	Command        string                        `json:"command,omitempty"`
	Status         string                        `json:"status"`
	OriginalState  string                        `json:"original_state,omitempty"`
	Cancellation   *nodes.InvocationCancellation `json:"cancellation,omitempty"`
	ErrorCode      string                        `json:"error_code,omitempty"`
	RecoveryAction string                        `json:"recovery_action,omitempty"`
}

const (
	NodeInvocationObservationPrepared   = "prepared"
	NodeInvocationObservationDispatched = "dispatched"
	NodeInvocationObservationCompleted  = "completed"
	NodeInvocationObservationStatus     = "status"
	NodeInvocationObservationUncertain  = "uncertain"
	NodeInvocationObservationRejected   = "rejected"
	NodeInvocationObservationCancel     = "cancel"
)

// NodeInvocationEventPayload is a redacted, passive invocation snapshot
// published to the runtime event bus. Concurrent observations are not a
// transaction log and may arrive out of order. Command input, output, node
// identity, and plan authority are intentionally excluded.
type NodeInvocationEventPayload struct {
	Observation       string                       `json:"observation"`
	InvocationID      string                       `json:"invocation_id"`
	Target            string                       `json:"target"`
	Workspace         string                       `json:"workspace,omitempty"`
	WorkspaceRevision string                       `json:"workspace_revision,omitempty"`
	Command           string                       `json:"command"`
	Risk              nodes.Risk                   `json:"risk"`
	GatewayState      nodes.GatewayInvocationState `json:"gateway_state"`
	State             string                       `json:"state"`
	ErrorCode         string                       `json:"error_code,omitempty"`
	Service           string                       `json:"service,omitempty"`
	Action            nodes.ServiceAction          `json:"action,omitempty"`
	LogEntries        int                          `json:"log_entries,omitempty"`
	JobProfile        string                       `json:"job_profile,omitempty"`
	JobID             string                       `json:"job_id,omitempty"`
	JobState          string                       `json:"job_state,omitempty"`
	JobLogStream      string                       `json:"job_log_stream,omitempty"`
	JobLogBytes       int                          `json:"job_log_bytes,omitempty"`
	JobLogCursor      int64                        `json:"job_log_cursor,omitempty"`
	ArtifactCount     int                          `json:"artifact_count,omitempty"`
	CancelDisposition string                       `json:"cancel_disposition,omitempty"`
}
