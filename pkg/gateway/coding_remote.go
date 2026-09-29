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
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
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
	codingRemoteBrowserSourceFactory      func(*config.Config) (tools.BrowserToolSource, error)
	codingRemoteTaskCoordinator           interface {
		Start(context.Context, agent.RemoteCodingTaskStart) (agent.RemoteCodingTaskView, error)
		Status(context.Context, agent.RemoteCodingTaskControl) (agent.RemoteCodingTaskView, error)
		Steer(context.Context, agent.RemoteCodingTaskControl) (agent.RemoteCodingTaskView, error)
		Answer(context.Context, agent.RemoteCodingTaskControl) (agent.RemoteCodingTaskView, error)
		Cancel(context.Context, agent.RemoteCodingTaskControl) (agent.RemoteCodingTaskView, error)
	}
)

func setupCodingRemoteBroker(
	ctx context.Context,
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
	nodeRuntime *nodeAdmissionRuntime,
	runningServices *services,
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
			tasks:  agentLoop.RemoteCodingTaskCoordinator(),
			source: func(current *config.Config) (tools.NodeInvocationSource, error) {
				return newNodeInvocationSource(current, nodeRuntime)
			},
			transferSource: func(current *config.Config) (tools.NodeFileTransferSource, error) {
				return newNodeFileTransferSource(current, nodeRuntime)
			},
			browserSource: func(current *config.Config) (tools.BrowserToolSource, error) {
				return newGatewayBrowserToolSource(current, runningServices)
			},
			browserInvocations: newCodingRemoteBrowserInvocationStore(),
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
	config             func() *config.Config
	now                func() time.Time
	source             codingRemoteNodeSourceFactory
	transferSource     codingRemoteNodeTransferSourceFactory
	browserSource      codingRemoteBrowserSourceFactory
	browserInvocations *codingRemoteBrowserInvocationStore
	events             runtimeevents.Bus
	tasks              codingRemoteTaskCoordinator
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
		if result, found, authorized := handler.browserInvocations.lookup(request); found {
			if !authorized {
				response.Code = "INVOCATION_DENIED"
				response.Message = "coding remote invocation is denied"
				return response
			}
			if request.Operation == codingremote.OperationInvocationCancel {
				response.Code = "CANCEL_UNSUPPORTED"
				response.Message = "coding remote browser invocation does not support cancellation"
				return response
			}
			return codingremote.Response{
				Schema: codingremote.SchemaV1, RequestID: request.RequestID,
				Status: codingremote.ResponseOK, Result: &result,
			}
		}
		if request.Operation == codingremote.OperationInvocationStatus &&
			request.CapabilityOperation == codingremote.BrowserReceiptRecoveryOperation {
			response.Code = "INVOCATION_DENIED"
			response.Message = "coding remote invocation is denied"
			return response
		}
		return handler.observeRetainedInvocation(ctx, cfg, request)
	}
	if request.Operation == codingremote.OperationTaskStatus ||
		request.Operation == codingremote.OperationTaskCancel {
		return handler.controlCodingTask(ctx, request)
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
	if codingRemoteTaskOperation(request.Operation) {
		if request.Operation == codingremote.OperationTaskStart ||
			request.Operation == codingremote.OperationTaskSteer ||
			request.Operation == codingremote.OperationTaskAnswer {
			descriptor, found := codingRemoteTaskScope(snapshot, request.TaskScope)
			if !found || descriptor.Revision != request.TaskScopeRevision ||
				!slices.Contains(descriptor.Profiles, request.TaskProfile) {
				response.Code = "TASK_SCOPE_CHANGED"
				response.Message = "coding remote task scope changed"
				return response
			}
		}
		return handler.controlCodingTask(ctx, request)
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
	if !exists || !codingRemoteCapabilityKindMatches(configured.Kind, descriptor.Kind) {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	if _, found := codingRemoteOperation(descriptor, request.CapabilityOperation); !found {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	if configured.Kind == config.CodingRemoteCapabilityBrowser {
		return handler.executeBrowserArtifact(ctx, cfg, request, grant, descriptor, denied)
	}
	if configured.Kind != config.CodingRemoteCapabilityWorkspace || handler.transferSource == nil ||
		request.CapabilityOperation != "workspace_exec" {
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

func (handler codingRemoteDiscoveryHandler) executeBrowserArtifact(
	ctx context.Context,
	cfg *config.Config,
	request codingremote.Request,
	grant config.CodingRemoteClientGrant,
	descriptor codingremote.CapabilityDescriptor,
	denied func(string, string) codingremote.Response,
) codingremote.Response {
	if handler.browserSource == nil || handler.browserInvocations == nil ||
		(request.CapabilityOperation != "browser_capture" && request.CapabilityOperation != "browser_act") {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	receipt, found, authorized := handler.browserInvocations.artifactReceipt(request)
	if found && !authorized {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	var retainedReceipt *codingRemoteBrowserArtifactReceipt
	if found {
		retainedReceipt = &receipt
	}
	source, err := handler.browserSource(cfg)
	if err != nil || source == nil {
		return codingremote.Response{
			Schema: codingremote.SchemaV1, RequestID: request.RequestID,
			Status: codingremote.ResponseUnavailable, Code: "BROKER_UNAVAILABLE",
			Message: "coding remote broker is unavailable",
		}
	}
	artifactSource, ok := source.(codingRemoteBrowserArtifactSource)
	if !ok {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	executionCtx := codingRemoteBrowserExecutionContext(
		codingRemoteExecutionContext(ctx, request, grant.Agent),
		request,
	)
	fetch := request.Operation == codingremote.OperationArtifactFetch
	expectedKind := "download"
	if request.CapabilityOperation == "browser_capture" {
		expectedKind = "screenshot"
	}
	record, data, err := artifactSource.codingRemoteBrowserArtifact(
		executionCtx,
		retainedReceipt,
		request.ArtifactRef,
		expectedKind,
		descriptor.Target,
		request.Offset,
		request.LimitBytes,
		fetch,
	)
	if err != nil || record.Spec.DeclaredSize > codingremote.MaxFetchedArtifactBytes {
		return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
	}
	result := codingremote.ArtifactResult{
		Grant: request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision: request.DiscoveryRevision,
		Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
		InvocationID: request.InvocationID, Target: descriptor.Target,
		ArtifactRef: record.Ref, Name: record.Spec.Filename, State: "available",
		Size: record.Spec.DeclaredSize, SHA256: record.Spec.SHA256, ContentType: record.Spec.ContentType,
	}
	if fetch {
		if len(data) == 0 {
			return denied("ARTIFACT_UNAVAILABLE", "coding remote artifact is unavailable")
		}
		result.Offset = request.Offset
		result.NextOffset = request.Offset + int64(len(data))
		result.EOF = result.NextOffset == result.Size
		result.DataBase64 = base64.StdEncoding.EncodeToString(data)
	}
	if result.Validate() != nil {
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
	var source tools.NodeInvocationSource
	if handler.source != nil {
		var err error
		source, err = handler.source(cfg)
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
			revision := configured.Revision
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
			case config.CodingRemoteCapabilityBrowser:
				if handler.browserSource == nil {
					continue
				}
				browserSource, sourceErr := handler.browserSource(cfg)
				if sourceErr != nil || browserSource == nil {
					continue
				}
				router, routerErr := tools.NewRemoteBrowserProfileRouter(
					cfg,
					browserSource,
					grant.Agent,
					configured.Target,
					configured.BrowserProfile,
				)
				if routerErr != nil {
					continue
				}
				target = configured.Target
				kind = codingremote.CapabilityBrowserProfile
				revision, routerErr = codingRemoteBrowserCapabilityRevision(cfg, configured)
				if routerErr != nil {
					continue
				}
				for _, operationAlias := range operations {
					described, describeErr := router.Describe(ctx, operationAlias)
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
				Alias: alias, Revision: revision, Target: target,
				Kind: kind, Availability: availability, Operations: projected,
			})
		}
	}
	taskScopes := codingRemoteTaskScopes(cfg, grant, source)
	snapshot := codingremote.CapabilitySnapshot{
		Schema: codingremote.SchemaV1, Grant: request.Grant, GrantRevision: grant.Revision,
		GeneratedAtUnixMS: handler.now().UnixMilli(), Capabilities: capabilities,
		TaskScopes: taskScopes,
	}
	snapshot.DiscoveryRevision = codingRemoteSnapshotRevision(
		cfg,
		request.Grant,
		grant,
		capabilities,
		taskScopes,
	)
	if err := snapshot.Validate(); err != nil {
		return codingremote.CapabilitySnapshot{}, err
	}
	return snapshot, nil
}

func codingRemoteTaskScopes(
	cfg *config.Config,
	grant config.CodingRemoteClientGrant,
	source tools.NodeInvocationSource,
) []codingremote.TaskScopeDescriptor {
	if cfg == nil || source == nil || len(grant.Tasks) == 0 {
		return []codingremote.TaskScopeDescriptor{}
	}
	tasks := append([]config.CodingRemoteTaskGrant(nil), grant.Tasks...)
	sort.Slice(tasks, func(left, right int) bool { return tasks[left].Scope < tasks[right].Scope })
	result := make([]codingremote.TaskScopeDescriptor, 0, len(tasks))
	for _, task := range tasks {
		scope, exists := cfg.Execution.RemoteCodingScopes[task.Scope]
		if !exists {
			continue
		}
		target, exists := cfg.Execution.Targets[scope.Target]
		if !exists || strings.TrimSpace(target.Node) == "" {
			continue
		}
		record, found, err := source.Lookup(target.Node)
		if err != nil || !found || !codingRemoteTaskCatalogApproved(record) {
			continue
		}
		availability := codingremote.AvailabilityOffline
		if record.Connected && record.Snapshot.State == nodes.StateConnected {
			availability = codingremote.AvailabilityAvailable
		}
		profiles := append([]codingscope.Profile(nil), task.Profiles...)
		sort.Slice(profiles, func(left, right int) bool { return profiles[left] < profiles[right] })
		result = append(result, codingremote.TaskScopeDescriptor{
			Alias: task.Scope, Revision: scope.Revision, Target: scope.Target,
			Profiles: profiles, Availability: availability,
		})
	}
	return result
}

func codingRemoteTaskOperation(operation codingremote.Operation) bool {
	switch operation {
	case codingremote.OperationTaskStart, codingremote.OperationTaskStatus,
		codingremote.OperationTaskSteer, codingremote.OperationTaskAnswer,
		codingremote.OperationTaskCancel:
		return true
	default:
		return false
	}
}

func codingRemoteTaskScope(
	snapshot codingremote.CapabilitySnapshot,
	alias string,
) (codingremote.TaskScopeDescriptor, bool) {
	index, found := slices.BinarySearchFunc(
		snapshot.TaskScopes,
		alias,
		func(value codingremote.TaskScopeDescriptor, wanted string) int {
			return strings.Compare(value.Alias, wanted)
		},
	)
	if !found {
		return codingremote.TaskScopeDescriptor{}, false
	}
	return snapshot.TaskScopes[index], true
}

func (handler codingRemoteDiscoveryHandler) controlCodingTask(
	ctx context.Context,
	request codingremote.Request,
) codingremote.Response {
	denied := func(code, message string) codingremote.Response {
		return codingremote.Response{
			Schema: codingremote.SchemaV1, RequestID: request.RequestID,
			Status: codingremote.ResponseDenied, Code: code, Message: message,
		}
	}
	if handler.tasks == nil || request.Principal == nil {
		return codingremote.Response{
			Schema: codingremote.SchemaV1, RequestID: request.RequestID,
			Status: codingremote.ResponseUnavailable, Code: "TASK_COORDINATOR_UNAVAILABLE",
			Message: "coding remote task coordinator is unavailable",
		}
	}
	authority := agent.RemoteCodingTaskAuthority{
		AgentID: request.Principal.AgentID,
		Grant:   request.Grant, GrantRevision: request.GrantRevision,
		DiscoveryRevision: request.DiscoveryRevision,
		ThreadID:          request.ThreadID, SessionKey: request.SessionKey,
		ProjectKey: request.ProjectKey, LocalProfile: request.LocalProfile,
		Principal: *request.Principal, CallID: request.CallID,
	}
	control := agent.RemoteCodingTaskControl{
		Authority: authority, TaskID: request.TaskID,
		Scope: request.TaskScope, ScopeRevision: request.TaskScopeRevision,
		Profile: request.TaskProfile, Text: request.TaskText,
		QuestionID: request.TaskQuestionID, QuestionRevision: request.TaskQuestionRevision,
		AnswerID: request.TaskAnswerID,
	}
	var view agent.RemoteCodingTaskView
	var err error
	switch request.Operation {
	case codingremote.OperationTaskStart:
		view, err = handler.tasks.Start(ctx, agent.RemoteCodingTaskStart{
			Authority: authority, TaskID: request.TaskID,
			Scope: request.TaskScope, ScopeRevision: request.TaskScopeRevision,
			Profile: request.TaskProfile, Objective: request.TaskObjective,
			DoneCriteria: request.TaskDoneCriteria,
		})
	case codingremote.OperationTaskStatus:
		view, err = handler.tasks.Status(ctx, control)
	case codingremote.OperationTaskSteer:
		view, err = handler.tasks.Steer(ctx, control)
	case codingremote.OperationTaskAnswer:
		view, err = handler.tasks.Answer(ctx, control)
	case codingremote.OperationTaskCancel:
		view, err = handler.tasks.Cancel(ctx, control)
	default:
		return denied("TASK_OPERATION_UNAVAILABLE", "coding remote task operation is unavailable")
	}
	if err != nil {
		if errors.Is(err, agent.ErrRemoteCodingTaskUnavailable) {
			return codingremote.Response{
				Schema: codingremote.SchemaV1, RequestID: request.RequestID,
				Status: codingremote.ResponseUnavailable, Code: "TASK_UNAVAILABLE",
				Message: "coding remote task operation is unavailable",
			}
		}
		return denied("TASK_DENIED", "coding remote task operation is denied")
	}
	result := codingRemoteTaskResult(view)
	if result.Validate() != nil {
		return denied("TASK_RESULT_UNAVAILABLE", "coding remote task result is unavailable")
	}
	return codingremote.Response{
		Schema: codingremote.SchemaV1, RequestID: request.RequestID,
		Status: codingremote.ResponseOK, Task: &result,
	}
}

func codingRemoteTaskResult(view agent.RemoteCodingTaskView) codingremote.TaskResult {
	result := codingremote.TaskResult{
		Grant: view.Grant, GrantRevision: view.GrantRevision,
		DiscoveryRevision: view.DiscoveryRevision,
		TaskID:            view.TaskID, GenerationID: view.GenerationID,
		Scope: view.Scope, Target: view.Target, Profile: view.Profile,
		Status: view.Status, NodeState: view.NodeState,
		ThreadID: view.ThreadID, WorkerGenerationID: view.WorkerGenerationID,
		Activity: view.Activity, Progress: view.Progress,
		Branch: view.Branch, HandoffID: view.HandoffID,
		FailureCode: view.FailureCode, TerminalSummary: view.TerminalSummary,
	}
	if view.Question != nil {
		result.Question = &codingremote.TaskQuestion{
			ID: view.Question.ID, Revision: view.Question.Revision,
			Prompt: view.Question.Prompt,
		}
		for _, option := range view.Question.Options {
			result.Question.Options = append(result.Question.Options, codingremote.TaskQuestionOption{
				ID: option.ID, Label: option.Label, Description: option.Description,
			})
		}
	}
	return result
}

func codingRemoteTaskCatalogApproved(record tools.NodeDiscoveryRecord) bool {
	registration := record.Registration
	snapshot := record.Snapshot
	if registration == nil || registration.RevokedAt != 0 || registration.ApprovedAt <= 0 ||
		snapshot.State == nodes.StateRevoked || snapshot.Validate() != nil ||
		registration.ApprovedCatalogHash == "" ||
		registration.ApprovedCatalogHash != snapshot.CatalogHash {
		return false
	}
	allowed := make(map[string]struct{}, len(registration.AllowedCommands))
	for _, command := range registration.AllowedCommands {
		allowed[command] = struct{}{}
	}
	required := map[string]bool{
		nodes.CodingCommandScopes:     false,
		nodes.CodingCommandTaskStart:  false,
		nodes.CodingCommandTaskStatus: false,
		nodes.CodingCommandTaskSteer:  false,
		nodes.CodingCommandTaskCancel: false,
	}
	for _, descriptor := range snapshot.Catalog.Commands {
		if _, needed := required[descriptor.Name]; !needed {
			continue
		}
		if _, approved := allowed[descriptor.Name]; !approved || descriptor.Validate() != nil {
			return false
		}
		required[descriptor.Name] = true
	}
	for _, approved := range required {
		if !approved {
			return false
		}
	}
	return true
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
	if !exists || !codingRemoteCapabilityKindMatches(configured.Kind, descriptor.Kind) {
		return denied("CAPABILITY_UNAVAILABLE", "coding remote capability is unavailable")
	}
	var source tools.NodeInvocationSource
	var err error
	if configured.Kind != config.CodingRemoteCapabilityBrowser {
		if handler.source == nil {
			return denied("CAPABILITY_UNAVAILABLE", "coding remote capability is unavailable")
		}
		source, err = handler.source(cfg)
		if err != nil || source == nil {
			return codingremote.Response{
				Schema: codingremote.SchemaV1, RequestID: request.RequestID,
				Status: codingremote.ResponseUnavailable, Code: "BROKER_UNAVAILABLE",
				Message: "coding remote broker is unavailable",
			}
		}
	}
	executionCtx := codingRemoteExecutionContext(ctx, request, grant.Agent)
	if configured.Kind == config.CodingRemoteCapabilityBrowser {
		executionCtx = codingRemoteBrowserExecutionContext(executionCtx, request)
	}
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
		case config.CodingRemoteCapabilityBrowser:
			if handler.browserSource == nil || handler.browserInvocations == nil {
				return denied("CAPABILITY_UNAVAILABLE", "coding remote capability is unavailable")
			}
			browserSource, sourceErr := handler.browserSource(cfg)
			if sourceErr != nil || browserSource == nil {
				return codingremote.Response{
					Schema: codingremote.SchemaV1, RequestID: request.RequestID,
					Status: codingremote.ResponseUnavailable, Code: "BROKER_UNAVAILABLE",
					Message: "coding remote broker is unavailable",
				}
			}
			router, routerErr := tools.NewRemoteBrowserProfileRouter(
				cfg,
				browserSource,
				grant.Agent,
				configured.Target,
				configured.BrowserProfile,
			)
			if routerErr != nil || !slices.Contains(configured.Operations, request.CapabilityOperation) {
				return denied("OPERATION_UNAVAILABLE", "coding remote operation is unavailable")
			}
			reserved, reservation := handler.browserInvocations.reserve(
				request,
				codingRemoteBrowserRunningResult(request, descriptor, operation),
			)
			switch reservation {
			case codingRemoteBrowserReservationExisting:
				return codingremote.Response{
					Schema: codingremote.SchemaV1, RequestID: request.RequestID,
					Status: codingremote.ResponseOK, Result: &reserved,
				}
			case codingRemoteBrowserReservationDenied:
				return denied("INVOCATION_DENIED", "coding remote invocation is denied")
			case codingRemoteBrowserReservationFull:
				return codingremote.Response{
					Schema: codingremote.SchemaV1, RequestID: request.RequestID,
					Status: codingremote.ResponseUnavailable, Code: "INVOCATION_CAPACITY",
					Message: "coding remote browser receipt capacity is exhausted",
				}
			case codingRemoteBrowserReservationClaimed:
			default:
				return denied("INVOCATION_DENIED", "coding remote invocation is denied")
			}
			toolResult = router.Execute(executionCtx, request.CapabilityOperation, arguments)
			browserResult, resultErr := codingRemoteBrowserInvokeResult(request, descriptor, operation, toolResult)
			if resultErr != nil {
				browserResult = codingRemoteBrowserUncertainResult(request, descriptor, operation)
			}
			if !handler.browserInvocations.complete(request, browserResult) {
				return codingremote.Response{
					Schema: codingremote.SchemaV1, RequestID: request.RequestID,
					Status: codingremote.ResponseUnavailable, Code: "INVOCATION_UNCERTAIN",
					Message: "coding remote invocation outcome is uncertain",
				}
			}
			return codingremote.Response{
				Schema: codingremote.SchemaV1, RequestID: request.RequestID,
				Status: codingremote.ResponseOK, Result: &browserResult,
			}
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

func codingRemoteBrowserExecutionContext(
	ctx context.Context,
	request codingremote.Request,
) context.Context {
	principal := *request.Principal
	principal.ExecutionID = codingRemoteBrowserExecutionID(request)
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx = toolshared.WithRuntimeCapabilities(ctx, runtime)
	return toolshared.WithToolExecutionIdentity(ctx, request.ProjectKey, principal.ExecutionID)
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

func codingRemoteBrowserExecutionID(request codingremote.Request) string {
	digest := sha256.New()
	for _, value := range []string{
		"mintclaw:coding-remote-browser-execution:v1",
		request.ThreadID,
		request.Grant,
		request.GrantRevision,
		request.Capability,
		request.CapabilityRevision,
	} {
		_, _ = fmt.Fprintf(digest, "%d:", len(value))
		_, _ = digest.Write([]byte(value))
	}
	return "remote_browser_" + hex.EncodeToString(digest.Sum(nil))
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
	case config.CodingRemoteCapabilityBrowser:
		return projected == codingremote.CapabilityBrowserProfile
	default:
		return false
	}
}

func codingRemoteBrowserCapabilityRevision(
	cfg *config.Config,
	capability config.CodingRemoteCapability,
) (string, error) {
	if cfg == nil || capability.Kind != config.CodingRemoteCapabilityBrowser {
		return "", tools.ErrRemoteBrowserUnavailable
	}
	target, targetFound := cfg.Tools.Browser.Targets[capability.Target]
	profile, profileFound := target.Profiles[capability.BrowserProfile]
	policyRevision, err := cfg.Tools.Browser.PolicyRevision()
	if !targetFound || !profileFound || err != nil {
		return "", tools.ErrRemoteBrowserUnavailable
	}
	digest := sha256.New()
	for _, value := range []string{
		"mintclaw:coding-remote-browser-capability:v1",
		capability.Revision,
		capability.Target,
		capability.BrowserProfile,
		profile.Revision,
		policyRevision,
	} {
		_, _ = fmt.Fprintf(digest, "%d:", len(value))
		_, _ = digest.Write([]byte(value))
	}
	return "browser_" + hex.EncodeToString(digest.Sum(nil)), nil
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
	taskScopes []codingremote.TaskScopeDescriptor,
) string {
	base := codingRemoteDiscoveryRevision(cfg, grantAlias, grant)
	encoded, _ := json.Marshal(struct {
		Capabilities []codingremote.CapabilityDescriptor `json:"capabilities"`
		TaskScopes   []codingremote.TaskScopeDescriptor  `json:"task_scopes"`
	}{Capabilities: capabilities, TaskScopes: taskScopes})
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
