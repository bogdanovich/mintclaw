package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type (
	codingRemoteNodeSourceFactory         func(*config.Config) (tools.NodeInvocationSource, error)
	codingRemoteNodeTransferSourceFactory func(*config.Config) (tools.NodeFileTransferSource, error)
)

func setupCodingRemoteBroker(
	ctx context.Context,
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
	nodeRuntime *nodeAdmissionRuntime,
) (*codingremote.Server, error) {
	if cfg == nil || !cfg.Gateway.CodingRemote.Enabled {
		return nil, nil
	}
	if agentLoop == nil {
		return nil, fmt.Errorf("coding remote broker requires an agent loop")
	}
	server, err := codingremote.StartServer(
		ctx,
		cfg.Gateway.CodingRemote.SocketPath,
		codingRemoteDiscoveryHandler{
			config: agentLoop.GetConfig,
			now:    time.Now,
			events: agentLoop.RuntimeEventBus(),
			source: func(current *config.Config) (tools.NodeInvocationSource, error) {
				return newNodeInvocationSource(current, nodeRuntime)
			},
			transferSource: func(current *config.Config) (tools.NodeFileTransferSource, error) {
				return newNodeFileTransferSource(current, nodeRuntime)
			},
		},
	)
	if err != nil {
		return nil, err
	}
	logger.InfoCF("coding", "Coding remote broker enabled", map[string]any{
		"transport": "same_user_unix",
	})
	return server, nil
}

type codingRemoteDiscoveryHandler struct {
	config         func() *config.Config
	now            func() time.Time
	source         codingRemoteNodeSourceFactory
	transferSource codingRemoteNodeTransferSourceFactory
	events         runtimeevents.Bus
}

func (handler codingRemoteDiscoveryHandler) HandleCodingRemote(
	ctx context.Context,
	request codingremote.Request,
) codingremote.Response {
	response := codingremote.Response{
		Schema: codingremote.SchemaV1, RequestID: request.RequestID,
		Status: codingremote.ResponseDenied, Code: "GRANT_UNAVAILABLE",
		Message: "coding remote grant is unavailable",
	}
	if handler.config == nil || handler.now == nil {
		response.Status = codingremote.ResponseUnavailable
		response.Code = "BROKER_UNAVAILABLE"
		response.Message = "coding remote broker is unavailable"
		return response
	}
	cfg := handler.config()
	if cfg == nil || !cfg.Gateway.CodingRemote.Enabled {
		response.Status = codingremote.ResponseUnavailable
		response.Code = "BROKER_DISABLED"
		response.Message = "coding remote broker is disabled"
		return response
	}
	if request.Operation == codingremote.OperationInvocationStatus ||
		request.Operation == codingremote.OperationInvocationCancel {
		if request.Principal == nil {
			response.Code = "IDENTITY_DENIED"
			response.Message = "turn-bound coding identity is denied"
			return response
		}
		return handler.observeRetainedInvocation(ctx, cfg, request)
	}
	grant, exists := cfg.Execution.CodingRemoteGrants[request.Grant]
	if !exists {
		return response
	}
	if grant.Revision != request.GrantRevision {
		response.Code = "GRANT_CHANGED"
		response.Message = "coding remote grant revision changed"
		return response
	}
	if !slices.Contains(grant.LocalProfiles, request.LocalProfile) {
		response.Code = "PROFILE_DENIED"
		response.Message = "local coding profile is not granted"
		return response
	}
	snapshot, snapshotErr := handler.capabilitySnapshot(ctx, cfg, request, grant)
	if snapshotErr != nil {
		response.Status = codingremote.ResponseUnavailable
		response.Code = "BROKER_UNAVAILABLE"
		response.Message = "coding remote broker is unavailable"
		return response
	}
	if request.Operation == codingremote.OperationCapabilitiesList {
		return codingremote.Response{
			Schema: codingremote.SchemaV1, RequestID: request.RequestID,
			Status: codingremote.ResponseOK, Snapshot: &snapshot,
		}
	}
	if request.Principal == nil || request.Principal.AgentID != grant.Agent {
		response.Code = "IDENTITY_DENIED"
		response.Message = "turn-bound coding identity is denied"
		return response
	}
	if request.DiscoveryRevision != snapshot.DiscoveryRevision {
		response.Code = "DISCOVERY_STALE"
		response.Message = "coding remote discovery is stale"
		return response
	}
	capability, found := codingRemoteCapability(snapshot, request.Capability)
	if !found || capability.Revision != request.CapabilityRevision {
		response.Code = "CAPABILITY_CHANGED"
		response.Message = "coding remote capability changed"
		return response
	}
	if request.Operation == codingremote.OperationArtifactDescribe ||
		request.Operation == codingremote.OperationArtifactFetch {
		return handler.executeArtifact(ctx, cfg, request, grant, capability)
	}
	return handler.executeCapability(ctx, cfg, request, grant, capability)
}

func (handler codingRemoteDiscoveryHandler) executeArtifact(
	ctx context.Context,
	cfg *config.Config,
	request codingremote.Request,
	grant config.CodingRemoteClientGrant,
	descriptor codingremote.CapabilityDescriptor,
) codingremote.Response {
	denied := func(code, message string) codingremote.Response {
		return codingremote.Response{
			Schema: codingremote.SchemaV1, RequestID: request.RequestID,
			Status: codingremote.ResponseDenied, Code: code, Message: message,
		}
	}
	configured, exists := cfg.Execution.CodingRemoteCapabilities[request.Capability]
	if !exists || configured.Kind != config.CodingRemoteCapabilityWorkspace || handler.transferSource == nil ||
		request.CapabilityOperation != "workspace_exec" {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	if _, found := codingRemoteOperation(descriptor, "workspace_exec"); !found {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	source, err := handler.transferSource(cfg)
	if err != nil || source == nil {
		return codingremote.Response{
			Schema: codingremote.SchemaV1, RequestID: request.RequestID,
			Status: codingremote.ResponseUnavailable, Code: "BROKER_UNAVAILABLE",
			Message: "coding remote broker is unavailable",
		}
	}
	router, err := tools.NewRemoteWorkspaceArtifactRouter(cfg, source, grant.Agent)
	if err != nil {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	executionCtx := codingRemoteExecutionContext(ctx, request, grant.Agent)
	var artifact tools.RemoteWorkspaceArtifact
	var chunk tools.NodeDownloadedArtifactChunk
	if request.Operation == codingremote.OperationArtifactDescribe {
		artifact, err = router.Describe(
			executionCtx,
			executionCtx,
			configured.RemoteWorkspace,
			request.InvocationID,
			request.ArtifactRef,
		)
	} else {
		artifact, chunk, err = router.FetchRange(
			executionCtx,
			executionCtx,
			configured.RemoteWorkspace,
			request.InvocationID,
			request.ArtifactRef,
			request.Offset,
			request.LimitBytes,
		)
	}
	if err != nil || artifact.Target != descriptor.Target ||
		artifact.Size > codingremote.MaxFetchedArtifactBytes {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	result := codingremote.ArtifactResult{
		Grant: request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision: request.DiscoveryRevision,
		Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
		InvocationID: request.InvocationID, Target: artifact.Target,
		ArtifactRef: artifact.Ref, Name: artifact.Name, State: artifact.State,
		Size: artifact.Size, SHA256: artifact.SHA256, ContentType: artifact.ContentType,
	}
	if request.Operation == codingremote.OperationArtifactFetch {
		if chunk.Offset != request.Offset || chunk.Size != artifact.Size ||
			chunk.SHA256 != artifact.SHA256 || len(chunk.Data) == 0 {
			return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
		}
		result.Offset = chunk.Offset
		result.NextOffset = chunk.Offset + int64(len(chunk.Data))
		result.EOF = chunk.EOF
		result.DataBase64 = base64.StdEncoding.EncodeToString(chunk.Data)
	}
	if err := result.Validate(); err != nil {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	return codingremote.Response{
		Schema: codingremote.SchemaV1, RequestID: request.RequestID,
		Status: codingremote.ResponseOK, Artifact: &result,
	}
}

func (handler codingRemoteDiscoveryHandler) capabilitySnapshot(
	ctx context.Context,
	cfg *config.Config,
	request codingremote.Request,
	grant config.CodingRemoteClientGrant,
) (codingremote.CapabilitySnapshot, error) {
	capabilities := make([]codingremote.CapabilityDescriptor, 0, len(grant.Capabilities))
	if handler.source != nil {
		source, err := handler.source(cfg)
		if err != nil || source == nil {
			return codingremote.CapabilitySnapshot{}, errors.New("coding remote node source is unavailable")
		}
		aliases := append([]string(nil), grant.Capabilities...)
		sort.Strings(aliases)
		for _, alias := range aliases {
			configured, exists := cfg.Execution.CodingRemoteCapabilities[alias]
			if !exists {
				continue
			}
			operations := append([]string(nil), configured.Operations...)
			sort.Strings(operations)
			projected := make([]codingremote.OperationDescriptor, 0, len(operations))
			availability := codingremote.AvailabilityOffline
			var target string
			var kind codingremote.CapabilityKind
			switch configured.Kind {
			case config.CodingRemoteCapabilityWorkspace:
				workspace, workspaceExists := cfg.Execution.RemoteWorkspaces[configured.RemoteWorkspace]
				if !workspaceExists {
					continue
				}
				target = workspace.Target
				kind = codingremote.CapabilityRemoteWorkspace
				for _, operationAlias := range operations {
					if !codingRemoteWorkspaceOperationSupported(operationAlias) {
						continue
					}
					router, routerErr := tools.NewRemoteWorkspaceNodeRouter(
						cfg,
						source,
						grant.Agent,
						operationAlias,
					)
					if routerErr != nil {
						continue
					}
					described, describeErr := codingRemoteDescribeWorkspaceOperation(
						router,
						configured.RemoteWorkspace,
						operationAlias,
					)
					if describeErr != nil {
						continue
					}
					risk, riskOK := codingRemoteRisk(described.Risk)
					if !riskOK || request.LocalProfile.ReadOnly() && risk == codingremote.RiskWrite {
						continue
					}
					schema := codingRemoteWorkspaceInputSchema(operationAlias, described)
					if len(schema) == 0 {
						continue
					}
					projected = append(projected, codingremote.OperationDescriptor{
						Alias: operationAlias, Risk: risk, InputSchema: schema,
						ResultKind:       described.ResultKind,
						SupportsProgress: described.SupportsProgress,
						SupportsCancel:   described.SupportsCancel,
					})
					if described.Available {
						availability = codingremote.AvailabilityAvailable
					}
				}
			case config.CodingRemoteCapabilityNode:
				router, routerErr := tools.NewRemoteServiceNodeRouter(cfg, source, grant.Agent)
				if routerErr != nil {
					continue
				}
				target = configured.Target
				kind = codingremote.CapabilityNodeCommand
				for _, command := range operations {
					operationAlias := codingRemoteServiceAlias(command)
					if operationAlias == "" {
						continue
					}
					described, describeErr := router.Describe(configured.Target, command)
					if describeErr != nil {
						continue
					}
					risk, riskOK := codingRemoteRisk(described.Risk)
					if !riskOK || request.LocalProfile.ReadOnly() && risk == codingremote.RiskWrite {
						continue
					}
					projected = append(projected, codingremote.OperationDescriptor{
						Alias: operationAlias, Risk: risk, InputSchema: described.InputSchema,
						ResultKind:       described.ResultKind,
						SupportsProgress: described.SupportsProgress,
						SupportsCancel:   described.SupportsCancel,
					})
					if described.Available {
						availability = codingremote.AvailabilityAvailable
					}
				}
			default:
				continue
			}
			if len(projected) == 0 {
				continue
			}
			sort.Slice(projected, func(left, right int) bool {
				return projected[left].Alias < projected[right].Alias
			})
			capabilities = append(capabilities, codingremote.CapabilityDescriptor{
				Alias: alias, Revision: configured.Revision, Target: target,
				Kind: kind, Availability: availability, Operations: projected,
			})
		}
	}
	snapshot := codingremote.CapabilitySnapshot{
		Schema: codingremote.SchemaV1, Grant: request.Grant, GrantRevision: grant.Revision,
		GeneratedAtUnixMS: handler.now().UnixMilli(), Capabilities: capabilities,
		TaskScopes: []codingremote.TaskScopeDescriptor{},
	}
	snapshot.DiscoveryRevision = codingRemoteSnapshotRevision(cfg, request.Grant, grant, capabilities)
	if err := snapshot.Validate(); err != nil {
		return codingremote.CapabilitySnapshot{}, err
	}
	return snapshot, nil
}

func (handler codingRemoteDiscoveryHandler) executeCapability(
	ctx context.Context,
	cfg *config.Config,
	request codingremote.Request,
	grant config.CodingRemoteClientGrant,
	descriptor codingremote.CapabilityDescriptor,
) codingremote.Response {
	denied := func(code, message string) codingremote.Response {
		return codingremote.Response{
			Schema: codingremote.SchemaV1, RequestID: request.RequestID,
			Status: codingremote.ResponseDenied, Code: code, Message: message,
		}
	}
	configured, exists := cfg.Execution.CodingRemoteCapabilities[request.Capability]
	if !exists || handler.source == nil || !codingRemoteCapabilityKindMatches(configured.Kind, descriptor.Kind) {
		return denied("CAPABILITY_UNAVAILABLE", "coding remote capability is unavailable")
	}
	source, err := handler.source(cfg)
	if err != nil || source == nil {
		return codingremote.Response{
			Schema: codingremote.SchemaV1, RequestID: request.RequestID,
			Status: codingremote.ResponseUnavailable, Code: "BROKER_UNAVAILABLE",
			Message: "coding remote broker is unavailable",
		}
	}
	executionCtx := codingRemoteExecutionContext(ctx, request, grant.Agent)
	var result codingremote.CapabilityResult
	switch request.Operation {
	case codingremote.OperationCapabilityInvoke:
		operation, found := codingRemoteOperation(descriptor, request.CapabilityOperation)
		if !found {
			return denied("OPERATION_UNAVAILABLE", "coding remote operation is unavailable")
		}
		if descriptor.Availability != codingremote.AvailabilityAvailable {
			return codingremote.Response{
				Schema: codingremote.SchemaV1, RequestID: request.RequestID,
				Status: codingremote.ResponseUnavailable, Code: "TARGET_OFFLINE",
				Message: "coding remote target is offline",
			}
		}
		var arguments map[string]any
		if err = json.Unmarshal(request.Arguments, &arguments); err != nil {
			return denied("INVALID_ARGUMENTS", "coding remote arguments are invalid")
		}
		var toolResult *toolshared.ToolResult
		var retained nodes.GatewayInvocationRecord
		var retainedFound bool
		var retainedErr error
		switch configured.Kind {
		case config.CodingRemoteCapabilityWorkspace:
			router, routerErr := tools.NewRemoteWorkspaceNodeRouter(
				cfg,
				source,
				grant.Agent,
				request.CapabilityOperation,
			)
			if routerErr != nil {
				return denied("OPERATION_UNAVAILABLE", "coding remote operation is unavailable")
			}
			router.SetEventPublisher(handler.events)
			toolResult = codingRemoteExecuteWorkspaceOperation(
				ctx,
				router,
				executionCtx,
				request,
				grant.Agent,
				configured.RemoteWorkspace,
				arguments,
			)
			if codingRemoteWorkspaceBoundOperation(request.CapabilityOperation) {
				retained, retainedFound, retainedErr = router.LookupRemoteWorkspaceInvocationByCurrentCall(
					executionCtx,
					configured.RemoteWorkspace,
				)
			} else {
				retained, retainedFound, retainedErr = tools.LookupNodeInvocationByCurrentCall(executionCtx, source)
			}
		case config.CodingRemoteCapabilityNode:
			command := codingRemoteServiceCommand(request.CapabilityOperation)
			if command == "" || !slices.Contains(configured.Operations, command) {
				return denied("OPERATION_UNAVAILABLE", "coding remote operation is unavailable")
			}
			router, routerErr := tools.NewRemoteServiceNodeRouter(cfg, source, grant.Agent)
			if routerErr != nil {
				return denied("OPERATION_UNAVAILABLE", "coding remote operation is unavailable")
			}
			router.SetEventPublisher(handler.events)
			toolResult = router.Execute(executionCtx, configured.Target, command, arguments)
			retained, retainedFound, retainedErr = tools.LookupNodeInvocationByCurrentCall(executionCtx, source)
		default:
			return denied("CAPABILITY_UNAVAILABLE", "coding remote capability is unavailable")
		}
		if retainedErr != nil {
			return codingremote.Response{
				Schema: codingremote.SchemaV1, RequestID: request.RequestID,
				Status: codingremote.ResponseUnavailable, Code: "INVOCATION_UNCERTAIN",
				Message: "coding remote invocation outcome is uncertain",
			}
		}
		if !retainedFound || retained.Target != descriptor.Target ||
			codingRemoteOperationAlias(retained.Plan.Command) != request.CapabilityOperation {
			return denied("INVOCATION_DENIED", "coding remote invocation is denied")
		}
		result, err = codingRemoteInvokeResult(
			request,
			descriptor,
			operation,
			retained.Plan.InvocationID,
			toolResult,
		)
		if err != nil {
			result = codingRemoteUncertainResult(request, descriptor, operation)
			err = nil
		}
	default:
		return denied("OPERATION_UNAVAILABLE", "coding remote operation is unavailable")
	}
	if err != nil {
		return denied("INVOCATION_DENIED", "coding remote invocation is denied")
	}
	return codingremote.Response{
		Schema: codingremote.SchemaV1, RequestID: request.RequestID,
		Status: codingremote.ResponseOK, Result: &result,
	}
}

func codingRemoteDescribeWorkspaceOperation(
	router *tools.RemoteWorkspaceNodeRouter,
	workspace string,
	operation string,
) (tools.RemoteWorkspaceOperation, error) {
	switch operation {
	case "read_file", "search_files", "write_file", "apply_patch":
		return router.DescribeRemoteWorkspace(workspace, operation)
	case "workspace_exec":
		return router.DescribeRemoteWorkspaceExec(workspace)
	case "job_status", "job_logs", "job_artifacts", "job_cancel":
		return router.DescribeRemoteWorkspaceJob(workspace, operation)
	default:
		return tools.RemoteWorkspaceOperation{}, tools.ErrRemoteWorkspaceUnavailable
	}
}

func codingRemoteExecuteWorkspaceOperation(
	ctx context.Context,
	router *tools.RemoteWorkspaceNodeRouter,
	executionCtx context.Context,
	request codingremote.Request,
	agentID string,
	workspace string,
	arguments map[string]any,
) *toolshared.ToolResult {
	switch request.CapabilityOperation {
	case "read_file", "search_files", "write_file", "apply_patch":
		return router.ExecuteRemoteWorkspace(executionCtx, request.CapabilityOperation, workspace, arguments)
	case "workspace_exec":
		arguments = cloneCodingRemoteArguments(arguments)
		arguments["remote_workspace"] = workspace
		return router.ExecuteRemoteWorkspaceExec(executionCtx, workspace, arguments)
	case "job_status", "job_logs", "job_artifacts", "job_cancel":
		jobInvocationID, _ := arguments["job_invocation_id"].(string)
		startRequest := request
		startRequest.CapabilityOperation = "workspace_exec"
		startRequest.InvocationID = strings.TrimSpace(jobInvocationID)
		startCtx := codingRemoteExecutionContext(ctx, startRequest, agentID)
		return router.ExecuteRemoteWorkspaceJob(
			executionCtx,
			startCtx,
			workspace,
			request.CapabilityOperation,
			arguments,
		)
	default:
		return toolshared.ErrorResult("remote workspace operation is unavailable")
	}
}

func cloneCodingRemoteArguments(arguments map[string]any) map[string]any {
	cloned := make(map[string]any, len(arguments)+1)
	for name, value := range arguments {
		cloned[name] = value
	}
	return cloned
}

func (handler codingRemoteDiscoveryHandler) observeRetainedInvocation(
	ctx context.Context,
	cfg *config.Config,
	request codingremote.Request,
) codingremote.Response {
	denied := func(code, message string) codingremote.Response {
		return codingremote.Response{
			Schema: codingremote.SchemaV1, RequestID: request.RequestID,
			Status: codingremote.ResponseDenied, Code: code, Message: message,
		}
	}
	if handler.source == nil {
		return denied("INVOCATION_DENIED", "coding remote invocation is denied")
	}
	source, err := handler.source(cfg)
	if err != nil || source == nil {
		return codingremote.Response{
			Schema: codingremote.SchemaV1, RequestID: request.RequestID,
			Status: codingremote.ResponseUnavailable, Code: "BROKER_UNAVAILABLE",
			Message: "coding remote broker is unavailable",
		}
	}
	executionCtx := codingRemoteExecutionContext(ctx, request, request.Principal.AgentID)
	var retained nodes.GatewayInvocationRecord
	var found bool
	if codingRemoteWorkspaceBoundOperation(request.CapabilityOperation) {
		capability, capabilityFound := cfg.Execution.CodingRemoteCapabilities[request.Capability]
		workspace, workspaceFound := cfg.Execution.RemoteWorkspaces[capability.RemoteWorkspace]
		if !capabilityFound || capability.Kind != config.CodingRemoteCapabilityWorkspace || !workspaceFound {
			return denied("INVOCATION_DENIED", "coding remote invocation is denied")
		}
		retained, found, err = tools.LookupNodeInvocationByRemoteWorkspaceCurrentCall(
			executionCtx,
			source,
			capability.RemoteWorkspace,
			workspace.Revision,
		)
	} else {
		retained, found, err = tools.LookupNodeInvocationByCurrentCall(executionCtx, source)
	}
	if err != nil || !found || codingRemoteOperationAlias(retained.Plan.Command) != request.CapabilityOperation {
		return denied("INVOCATION_DENIED", "coding remote invocation is denied")
	}
	nodeInvocationID := retained.Plan.InvocationID
	var toolResult *toolshared.ToolResult
	cancel := request.Operation == codingremote.OperationInvocationCancel
	if cancel {
		tool := tools.NewNodeCancelTool(tools.NewNodeToolOptions(cfg), source)
		tool.SetEventPublisher(handler.events)
		toolResult = tool.Execute(executionCtx, map[string]any{"invocation_id": nodeInvocationID})
	} else {
		tool := tools.NewNodeStatusTool(tools.NewNodeToolOptions(cfg), source)
		tool.SetEventPublisher(handler.events)
		toolResult = tool.Execute(executionCtx, map[string]any{"invocation_id": nodeInvocationID})
	}
	result, err := codingRemoteRetainedObservedResult(request, nodeInvocationID, toolResult, cancel)
	if err != nil {
		return denied("INVOCATION_DENIED", "coding remote invocation is denied")
	}
	return codingremote.Response{
		Schema: codingremote.SchemaV1, RequestID: request.RequestID,
		Status: codingremote.ResponseOK, Result: &result,
	}
}

func codingRemoteExecutionContext(
	ctx context.Context,
	request codingremote.Request,
	agentID string,
) context.Context {
	principal := *request.Principal
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx = toolshared.WithRuntimeCapabilities(ctx, runtime)
	ctx = toolshared.WithToolSessionContext(ctx, agentID, request.SessionKey, nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, request.SessionKey)
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "coding", SenderID: principal.ActorID, ActorID: principal.ActorID,
	})
	toolCallID := request.CallID
	if request.InvocationID != "" {
		toolCallID = request.InvocationID
	}
	ctx = toolshared.WithToolCallID(ctx, toolCallID)
	return toolshared.WithToolExecutionIdentity(
		ctx,
		request.ProjectKey,
		codingRemoteCapabilityExecutionID(request),
	)
}

func codingRemoteCapabilityExecutionID(request codingremote.Request) string {
	digest := sha256.New()
	for _, value := range []string{
		"mintclaw:coding-remote-capability-execution:v1",
		request.ThreadID,
		request.Grant,
		request.GrantRevision,
		request.Capability,
		request.CapabilityRevision,
		request.CapabilityOperation,
	} {
		_, _ = fmt.Fprintf(digest, "%d:", len(value))
		_, _ = digest.Write([]byte(value))
	}
	return "remote_capability_" + hex.EncodeToString(digest.Sum(nil))
}

func codingRemoteCapability(
	snapshot codingremote.CapabilitySnapshot,
	alias string,
) (codingremote.CapabilityDescriptor, bool) {
	index, found := slices.BinarySearchFunc(
		snapshot.Capabilities,
		alias,
		func(value codingremote.CapabilityDescriptor, want string) int {
			return strings.Compare(value.Alias, want)
		},
	)
	if !found {
		return codingremote.CapabilityDescriptor{}, false
	}
	return snapshot.Capabilities[index], true
}

func codingRemoteOperation(
	capability codingremote.CapabilityDescriptor,
	alias string,
) (codingremote.OperationDescriptor, bool) {
	index, found := slices.BinarySearchFunc(
		capability.Operations,
		alias,
		func(value codingremote.OperationDescriptor, want string) int {
			return strings.Compare(value.Alias, want)
		},
	)
	if !found {
		return codingremote.OperationDescriptor{}, false
	}
	return capability.Operations[index], true
}

type codingRemoteNodeResult struct {
	Placement       string                        `json:"placement"`
	RemoteWorkspace string                        `json:"remote_workspace"`
	InvocationID    string                        `json:"invocation_id"`
	JobInvocationID string                        `json:"job_invocation_id"`
	Target          string                        `json:"target"`
	Command         string                        `json:"command"`
	GatewayState    nodes.GatewayInvocationState  `json:"gateway_state"`
	State           string                        `json:"state"`
	Status          string                        `json:"status"`
	OriginalState   string                        `json:"original_state"`
	Result          json.RawMessage               `json:"result"`
	Failure         *nodes.InvocationFailure      `json:"failure"`
	Cancellation    *nodes.InvocationCancellation `json:"cancellation"`
	ErrorCode       string                        `json:"error_code"`
	RecoveryAction  string                        `json:"recovery_action"`
}

func codingRemoteInvokeResult(
	request codingremote.Request,
	capability codingremote.CapabilityDescriptor,
	operation codingremote.OperationDescriptor,
	nodeInvocationID string,
	toolResult *toolshared.ToolResult,
) (codingremote.CapabilityResult, error) {
	if toolResult == nil {
		return codingremote.CapabilityResult{}, errors.New("remote workspace returned no result")
	}
	var wire codingRemoteNodeResult
	raw := []byte(toolResult.ContentForLLM())
	if err := json.Unmarshal(raw, &wire); err != nil {
		return codingremote.CapabilityResult{}, errors.New("remote workspace result is unavailable")
	}
	if wire.InvocationID == "" {
		var uncertain struct {
			ErrorCode  string                  `json:"error_code"`
			Invocation *codingRemoteNodeResult `json:"invocation"`
		}
		if json.Unmarshal(raw, &uncertain) != nil || uncertain.Invocation == nil {
			return codingremote.CapabilityResult{}, errors.New("remote workspace result is unavailable")
		}
		wire = *uncertain.Invocation
		if wire.ErrorCode == "" {
			wire.ErrorCode = uncertain.ErrorCode
		}
	}
	if wire.InvocationID != nodeInvocationID || wire.Target != capability.Target {
		return codingremote.CapabilityResult{}, errors.New("remote workspace result is unavailable")
	}
	if wire.State == "" {
		wire.State = string(wire.GatewayState)
	}
	wire.InvocationID = request.InvocationID
	result := codingRemoteResultBase(request, capability, operation, wire)
	result.Changes = codingRemoteChanges(operation.Alias, wire.Result)
	if err := result.Validate(); err != nil {
		return codingremote.CapabilityResult{}, err
	}
	return result, nil
}

func codingRemoteRetainedObservedResult(
	request codingremote.Request,
	nodeInvocationID string,
	toolResult *toolshared.ToolResult,
	cancel bool,
) (codingremote.CapabilityResult, error) {
	if toolResult == nil {
		return codingremote.CapabilityResult{}, errors.New("remote invocation returned no result")
	}
	var wire codingRemoteNodeResult
	if err := json.Unmarshal([]byte(toolResult.ContentForLLM()), &wire); err != nil ||
		wire.InvocationID != nodeInvocationID || !codingremote.ValidAlias(wire.Target) {
		return codingremote.CapabilityResult{}, errors.New("remote invocation result is unavailable")
	}
	operationAlias := codingRemoteOperationAlias(wire.Command)
	if operationAlias == "" || operationAlias != request.CapabilityOperation {
		return codingremote.CapabilityResult{}, errors.New("remote invocation is outside the capability")
	}
	risk := codingremote.RiskRead
	if operationAlias == "write_file" || operationAlias == "apply_patch" ||
		operationAlias == "workspace_exec" || operationAlias == "job_cancel" ||
		operationAlias == "service_action" {
		risk = codingremote.RiskWrite
	}
	capability := codingremote.CapabilityDescriptor{Target: wire.Target}
	operation := codingremote.OperationDescriptor{Alias: operationAlias, Risk: risk}
	if cancel {
		wire.State = wire.Status
	}
	if wire.State == "" {
		wire.State = string(wire.GatewayState)
	}
	if wire.ErrorCode == "" && wire.Failure != nil {
		wire.ErrorCode = wire.Failure.Code
	}
	wire.InvocationID = request.InvocationID
	result := codingRemoteResultBase(request, capability, operation, wire)
	result.Changes = codingRemoteChanges(operationAlias, wire.Result)
	if wire.Cancellation != nil {
		result.CancellationConfirmed = wire.Cancellation.TerminationConfirmed
	}
	if err := result.Validate(); err != nil {
		return codingremote.CapabilityResult{}, err
	}
	return result, nil
}

func codingRemoteUncertainResult(
	request codingremote.Request,
	capability codingremote.CapabilityDescriptor,
	operation codingremote.OperationDescriptor,
) codingremote.CapabilityResult {
	return codingremote.CapabilityResult{
		Grant: request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision: request.DiscoveryRevision,
		Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
		Operation: operation.Alias, InvocationID: request.InvocationID,
		Target: capability.Target, Risk: operation.Risk, State: "unknown",
		ErrorCode:      "INVOCATION_UNCERTAIN",
		RecoveryAction: "Call remote_capability status with this invocation_id; do not replay the operation.",
	}
}

func codingRemoteResultBase(
	request codingremote.Request,
	capability codingremote.CapabilityDescriptor,
	operation codingremote.OperationDescriptor,
	wire codingRemoteNodeResult,
) codingremote.CapabilityResult {
	result := codingremote.CapabilityResult{
		Grant: request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision: request.DiscoveryRevision,
		Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
		Operation: operation.Alias, InvocationID: wire.InvocationID, Target: capability.Target,
		Risk: operation.Risk, State: strings.ToLower(strings.TrimSpace(wire.State)),
		Result: codingRemoteSafeResult(wire.Result), ErrorCode: wire.ErrorCode,
		RecoveryAction: codingRemoteRecoveryAction(wire.RecoveryAction),
	}
	if operation.Alias == "workspace_exec" &&
		(wire.JobInvocationID != "" || wire.Command == nodes.JobCommandStart) {
		result.JobInvocationID = wire.InvocationID
	} else if strings.HasPrefix(operation.Alias, "job_") && wire.JobInvocationID != "" {
		result.JobInvocationID = wire.JobInvocationID
	}
	return result
}

func codingRemoteSafeResult(payload json.RawMessage) json.RawMessage {
	if len(payload) == 0 || !bytes.Contains(payload, []byte(`"job_id"`)) {
		return append(json.RawMessage(nil), payload...)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return append(json.RawMessage(nil), payload...)
	}
	stripCodingRemoteJobIDs(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return append(json.RawMessage(nil), payload...)
	}
	return encoded
}

func stripCodingRemoteJobIDs(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "job_id")
		for _, child := range typed {
			stripCodingRemoteJobIDs(child)
		}
	case []any:
		for _, child := range typed {
			stripCodingRemoteJobIDs(child)
		}
	}
}

func codingRemoteRecoveryAction(value string) string {
	return strings.NewReplacer(
		"nodes_status", "remote_capability status",
		"nodes_cancel", "remote_capability cancel",
	).Replace(value)
}

func codingRemoteChanges(operation string, payload json.RawMessage) []codingremote.ChangeReceipt {
	byPath := make(map[string]codingremote.ChangeReceipt)
	retain := func(path, action string) {
		receipt := codingremote.ChangeReceipt{Path: path, Action: action}
		if receipt.Validate() == nil {
			byPath[path] = receipt
		}
	}
	switch operation {
	case "write_file":
		var mutation struct {
			Path   string `json:"path"`
			Action string `json:"action"`
		}
		if json.Unmarshal(payload, &mutation) == nil && mutation.Path != "" && mutation.Action != "" {
			retain(mutation.Path, mutation.Action)
		}
	case "apply_patch":
		var patch struct {
			Committed []struct {
				Path   string `json:"path"`
				Action string `json:"action"`
			} `json:"committed"`
		}
		if json.Unmarshal(payload, &patch) == nil {
			for _, mutation := range patch.Committed {
				retain(mutation.Path, mutation.Action)
			}
		}
	}
	changes := make([]codingremote.ChangeReceipt, 0, len(byPath))
	for _, receipt := range byPath {
		changes = append(changes, receipt)
	}
	sort.Slice(changes, func(left, right int) bool { return changes[left].Path < changes[right].Path })
	return changes
}

func codingRemoteWorkspaceOperationSupported(operation string) bool {
	switch operation {
	case "read_file", "search_files", "write_file", "apply_patch", "workspace_exec",
		"job_status", "job_logs", "job_artifacts", "job_cancel":
		return true
	default:
		return false
	}
}

func codingRemoteWorkspaceAlias(command string) string {
	switch command {
	case nodes.WorkspaceCommandRead:
		return "read_file"
	case nodes.WorkspaceCommandSearch:
		return "search_files"
	case nodes.WorkspaceCommandWrite:
		return "write_file"
	case nodes.WorkspaceCommandPatch:
		return "apply_patch"
	case "system.exec.v1", nodes.JobCommandStart:
		return "workspace_exec"
	case nodes.JobCommandStatus:
		return "job_status"
	case nodes.JobCommandLogs:
		return "job_logs"
	case nodes.JobCommandArtifacts:
		return "job_artifacts"
	case nodes.JobCommandCancel:
		return "job_cancel"
	default:
		return ""
	}
}

func codingRemoteServiceAlias(command string) string {
	switch command {
	case "service.status.v1":
		return "service_status"
	case "service.logs.v1":
		return "service_logs"
	case "service.action.v1":
		return "service_action"
	default:
		return ""
	}
}

func codingRemoteCapabilityKindMatches(
	configured config.CodingRemoteCapabilityKind,
	projected codingremote.CapabilityKind,
) bool {
	switch configured {
	case config.CodingRemoteCapabilityWorkspace:
		return projected == codingremote.CapabilityRemoteWorkspace
	case config.CodingRemoteCapabilityNode:
		return projected == codingremote.CapabilityNodeCommand
	default:
		return false
	}
}

func codingRemoteServiceCommand(operation string) string {
	switch operation {
	case "service_status":
		return "service.status.v1"
	case "service_logs":
		return "service.logs.v1"
	case "service_action":
		return "service.action.v1"
	default:
		return ""
	}
}

func codingRemoteOperationAlias(command string) string {
	if operation := codingRemoteWorkspaceAlias(command); operation != "" {
		return operation
	}
	return codingRemoteServiceAlias(command)
}

func codingRemoteRisk(risk nodes.Risk) (codingremote.Risk, bool) {
	switch risk {
	case nodes.RiskRead:
		return codingremote.RiskRead, true
	case nodes.RiskWrite:
		return codingremote.RiskWrite, true
	default:
		return "", false
	}
}

func codingRemoteWorkspaceInputSchema(
	operation string,
	described tools.RemoteWorkspaceOperation,
) json.RawMessage {
	const path = `{"type":"string","minLength":1,"maxLength":4096}`
	switch operation {
	case "read_file":
		return json.RawMessage(
			`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":` + path + `,"offset":{"type":"integer","minimum":0},"length":{"type":"integer","minimum":1,"maximum":524288},"start_line":{"type":"integer","minimum":1},"max_lines":{"type":"integer","minimum":1,"maximum":5000}}}`,
		)
	case "search_files":
		return json.RawMessage(
			`{"type":"object","additionalProperties":false,"required":["pattern"],"properties":{"pattern":{"type":"string","minLength":1,"maxLength":2048},"target":{"type":"string","enum":["content","files"]},"path":{"type":"string","maxLength":4096},"file_glob":{"type":"string","maxLength":256},"output_mode":{"type":"string","enum":["content","files_only","count"]},"context":{"type":"integer","minimum":0,"maximum":10},"limit":{"type":"integer","minimum":1,"maximum":500},"include_ignored":{"type":"boolean"}}}`,
		)
	case "write_file":
		return json.RawMessage(
			`{"type":"object","additionalProperties":false,"required":["path","content","overwrite"],"properties":{"path":` + path + `,"content":{"type":"string","maxLength":262144},"overwrite":{"type":"boolean"},"expected_sha256":{"type":"string","minLength":64,"maxLength":64,"pattern":"^[A-Fa-f0-9]{64}$"}}}`,
		)
	case "apply_patch":
		return json.RawMessage(
			`{"type":"object","additionalProperties":false,"required":["input"],"properties":{"input":{"type":"string","minLength":1,"maxLength":262144}}}`,
		)
	case "workspace_exec":
		if len(described.ExecModes) == 0 {
			return nil
		}
		branches := make([]any, 0, len(described.ExecModes))
		for _, mode := range described.ExecModes {
			branch := codingRemoteWorkspaceExecModeSchema(mode)
			if branch == nil {
				return nil
			}
			branches = append(branches, branch)
		}
		schema := map[string]any{"type": "object", "oneOf": branches}
		encoded, _ := json.Marshal(schema)
		return encoded
	case "job_status", "job_artifacts", "job_cancel":
		return json.RawMessage(
			`{"type":"object","additionalProperties":false,"required":["job_invocation_id"],"properties":{"job_invocation_id":{"type":"string","minLength":1,"maxLength":128}}}`,
		)
	case "job_logs":
		return json.RawMessage(
			`{"type":"object","additionalProperties":false,"required":["job_invocation_id","stream","cursor","limit_bytes"],"properties":{"job_invocation_id":{"type":"string","minLength":1,"maxLength":128},"stream":{"type":"string","enum":["stderr","stdout"]},"cursor":{"type":"integer","minimum":0,"maximum":67108864},"limit_bytes":{"type":"integer","minimum":1,"maximum":65536}}}`,
		)
	default:
		return nil
	}
}

func codingRemoteWorkspaceExecModeSchema(mode tools.RemoteWorkspaceExecMode) map[string]any {
	if len(mode.ExecutableAliases) == 0 || mode.TimeoutSecondsMax < 1 ||
		(mode.Name != "foreground" && mode.Name != "job") {
		return nil
	}
	environment := map[string]any{
		"type": "object", "maxProperties": len(mode.EnvironmentNames),
		"additionalProperties": map[string]any{"type": "string", "maxLength": 16384},
	}
	if len(mode.EnvironmentNames) > 0 {
		environment["propertyNames"] = map[string]any{"enum": mode.EnvironmentNames}
	}
	properties := map[string]any{
		"executable": map[string]any{"type": "string", "enum": mode.ExecutableAliases},
		"args": map[string]any{
			"type": "array", "maxItems": 127,
			"items": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		},
		"env": environment, "mode": map[string]any{"type": "string", "enum": []string{mode.Name}},
		"timeout_seconds": map[string]any{
			"type": "integer", "minimum": 1, "maximum": mode.TimeoutSecondsMax,
		},
	}
	if mode.Name == "job" && mode.ArtifactCountMax > 0 {
		properties["artifacts"] = map[string]any{
			"type": "array", "maxItems": mode.ArtifactCountMax,
			"items": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"name", "path"},
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
					"path": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
				},
			},
		}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"executable", "args", "mode"}, "properties": properties,
	}
}

func codingRemoteWorkspaceBoundOperation(operation string) bool {
	return operation == "workspace_exec" || strings.HasPrefix(operation, "job_")
}

func codingRemoteSnapshotRevision(
	cfg *config.Config,
	grantAlias string,
	grant config.CodingRemoteClientGrant,
	capabilities []codingremote.CapabilityDescriptor,
) string {
	base := codingRemoteDiscoveryRevision(cfg, grantAlias, grant)
	encoded, _ := json.Marshal(capabilities)
	digest := sha256.New()
	_, _ = digest.Write([]byte(base))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(encoded)
	return "discovery_" + hex.EncodeToString(digest.Sum(nil))
}

func codingRemoteDiscoveryRevision(
	cfg *config.Config,
	grantAlias string,
	grant config.CodingRemoteClientGrant,
) string {
	digest := sha256.New()
	_, _ = fmt.Fprintf(
		digest,
		"mintclaw:coding-remote-discovery:v1\x00%s\x00%s\x00%s\n",
		grantAlias,
		grant.Revision,
		grant.Agent,
	)
	profiles := profileStrings(grant.LocalProfiles)
	sort.Strings(profiles)
	for _, profile := range profiles {
		_, _ = fmt.Fprintf(digest, "local:%s\n", profile)
	}
	capabilities := append([]string(nil), grant.Capabilities...)
	sort.Strings(capabilities)
	for _, alias := range capabilities {
		capability := cfg.Execution.CodingRemoteCapabilities[alias]
		_, _ = fmt.Fprintf(digest, "capability:%s:%s\n", alias, capability.Revision)
	}
	tasks := append([]config.CodingRemoteTaskGrant(nil), grant.Tasks...)
	sort.Slice(tasks, func(left, right int) bool { return tasks[left].Scope < tasks[right].Scope })
	for _, task := range tasks {
		scope := cfg.Execution.RemoteCodingScopes[task.Scope]
		_, _ = fmt.Fprintf(digest, "task:%s:%s\n", task.Scope, scope.Revision)
		profiles = profileStrings(task.Profiles)
		sort.Strings(profiles)
		for _, profile := range profiles {
			_, _ = fmt.Fprintf(digest, "task-profile:%s\n", profile)
		}
	}
	return "discovery_" + hex.EncodeToString(digest.Sum(nil))
}

func profileStrings[T ~string](profiles []T) []string {
	result := make([]string, len(profiles))
	for index, profile := range profiles {
		result[index] = string(profile)
	}
	return result
}
