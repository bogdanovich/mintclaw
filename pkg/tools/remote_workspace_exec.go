package tools

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const defaultWorkspaceExecTimeout = 30

const remoteWorkspaceJobInvocationArgument = "job_invocation_id"

// WorkspaceExecTool binds one explicit remote workspace to the existing
// system.exec.v1 or job.start.v1 invocation path. It owns no process or job
// lifecycle of its own.
type WorkspaceExecTool struct {
	router        *RemoteWorkspaceNodeRouter
	sourceFactory func() (NodeInvocationSource, error)
}

func NewWorkspaceExecTool(
	cfg *config.Config,
	source NodeInvocationSource,
	agentID string,
) (*WorkspaceExecTool, error) {
	router, err := NewRemoteWorkspaceNodeRouter(cfg, source, agentID, "workspace_exec")
	if err != nil {
		return nil, err
	}
	router.runtime.eventSource = "workspace_exec"
	return &WorkspaceExecTool{router: router}, nil
}

func (tool *WorkspaceExecTool) SetEventPublisher(eventBus runtimeevents.Bus) {
	if tool != nil && tool.router != nil {
		tool.router.SetEventPublisher(eventBus)
	}
}

// SetInvocationSourceFactory makes generation-bound gateway authority resolve
// at call time, after any config-reload node reconciliation has completed.
func (tool *WorkspaceExecTool) SetInvocationSourceFactory(
	factory func() (NodeInvocationSource, error),
) {
	if tool != nil {
		tool.sourceFactory = factory
	}
}

func (tool *WorkspaceExecTool) routerForCall() (*RemoteWorkspaceNodeRouter, error) {
	if tool == nil || tool.router == nil {
		return nil, ErrRemoteWorkspaceUnavailable
	}
	if tool.sourceFactory == nil {
		return tool.router, nil
	}
	source, err := tool.sourceFactory()
	if err != nil || source == nil {
		return nil, ErrRemoteWorkspaceUnavailable
	}
	router, err := tool.router.withInvocationSource(source)
	if err != nil {
		return nil, err
	}
	return router, nil
}

func (*WorkspaceExecTool) Name() string { return "workspace_exec" }

func (*WorkspaceExecTool) Description() string {
	return "Run one direct-argv command in an explicit operator-configured remote workspace. " +
		"A remote workspace is an execution target, not a MintClaw agent profile, gateway service, or deployment. " +
		"Foreground mode uses system.exec.v1. Job mode starts the existing durable P5a job and returns a stable " +
		"job invocation reference; " +
		"use nodes describe plus nodes_invoke for job status, logs, artifacts, or cancellation. " +
		"This tool accepts no shell text, target, profile, executable path, or cwd, and an uncertain result must be " +
		"recovered with nodes_status rather than replayed."
}

func (tool *WorkspaceExecTool) Parameters() map[string]any {
	aliases := tool.router.WorkspaceAliases()
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			remoteWorkspaceArgument: map[string]any{
				"type":        "string",
				"enum":        aliases,
				"description": "Operator-configured remote execution workspace alias; never an agent profile or service.",
			},
			"executable": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 64,
				"description": "Authenticated executable alias from node discovery, never a path.",
			},
			"args": map[string]any{
				"type": "array", "maxItems": 127,
				"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
				"description": "Bounded direct argument array after the executable alias.",
			},
			"env": map[string]any{
				"type": "object", "maxProperties": 64,
				"additionalProperties": map[string]any{"type": "string", "maxLength": 16384},
				"description":          "Optional environment overrides; node policy allowlists names.",
			},
			"mode": map[string]any{
				"type": "string", "enum": []string{"foreground", "job"},
			},
			"timeout_seconds": map[string]any{
				"type": "integer", "minimum": 1, "maximum": nodes.MaxJobTimeoutSeconds,
				"description": "Foreground or durable-job runtime bound. Defaults to 30 seconds.",
			},
			"artifacts": map[string]any{
				"type": "array", "maxItems": nodes.MaxJobArtifactCount,
				"description": "Job-only declared regular-file artifacts relative to this workspace.",
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"name", "path"},
					"properties": map[string]any{
						"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
						"path": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
					},
				},
			},
		},
		"required":             []string{remoteWorkspaceArgument, "executable", "args", "mode"},
		"additionalProperties": false,
	}
}

func (tool *WorkspaceExecTool) ApprovalArguments(
	ctx context.Context,
	args map[string]any,
) (map[string]any, error) {
	router, err := tool.routerForCall()
	if err != nil {
		return nil, err
	}
	prepared, binding, mode, executable, effectiveTimeout, err := router.prepareWorkspaceExec(ctx, args)
	if err != nil {
		return nil, err
	}
	boundCtx := bindRemoteWorkspaceInvocationIdentity(ctx, binding)
	approval, err := router.invoke.approvalArguments(boundCtx, prepared, false)
	if err != nil {
		return nil, err
	}
	approval[remoteWorkspaceArgument] = binding.alias
	approval["remote_workspace_revision"] = binding.config.Revision
	approval["operation"] = "workspace_exec"
	approval["mode"] = mode
	approval["executable"] = executable
	approval["timeout_seconds"] = effectiveTimeout
	if mode == "job" {
		artifacts, _ := workspaceExecArtifacts(args)
		approval["artifact_count"] = len(artifacts)
	}
	return approval, nil
}

func (tool *WorkspaceExecTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	router, err := tool.routerForCall()
	if err != nil {
		return toolshared.ErrorResult("remote workspace execution authority is unavailable")
	}
	prepared, binding, mode, _, _, err := router.prepareWorkspaceExec(ctx, args)
	if err != nil {
		return toolshared.ErrorResult("remote workspace execution authority is unavailable")
	}
	boundCtx := bindRemoteWorkspaceInvocationIdentity(ctx, binding)
	result := router.invoke.execute(boundCtx, prepared, false)
	return projectWorkspaceExecResult(result, binding, mode)
}

// DescribeRemoteWorkspaceExec projects the already approved foreground and
// durable-job modes for one exact workspace. Modes that require a per-call
// approval or are absent from the current catalog are omitted.
func (router *RemoteWorkspaceNodeRouter) DescribeRemoteWorkspaceExec(
	workspaceAlias string,
) (RemoteWorkspaceOperation, error) {
	binding, ok := router.byAlias[workspaceAlias]
	if !ok {
		return RemoteWorkspaceOperation{}, ErrRemoteWorkspaceUnavailable
	}
	var described RemoteWorkspaceOperation
	modes := make([]string, 0, 2)
	foreground, foregroundErr := router.describeRemoteWorkspaceExecCommand(binding, "system.exec.v1")
	if foregroundErr == nil {
		described = foreground
		modes = append(modes, "foreground")
		described.ExecModes = append(described.ExecModes, remoteWorkspaceExecMode("foreground", foreground))
	}
	if binding.allowJobs {
		job, jobErr := router.describeRemoteWorkspaceExecCommand(binding, nodes.JobCommandStart)
		if jobErr == nil {
			if len(modes) == 0 {
				described = job
			}
			described.Available = described.Available || job.Available
			modes = append(modes, "job")
			described.ExecModes = append(described.ExecModes, remoteWorkspaceExecMode("job", job))
		}
	}
	if len(modes) == 0 {
		return RemoteWorkspaceOperation{}, ErrRemoteWorkspaceUnavailable
	}
	described.Risk = nodes.RiskWrite
	described.ResultKind = "json"
	described.SupportsProgress = false
	described.SupportsCancel = false
	described.Modes = modes
	return described, nil
}

// DescribeRemoteWorkspaceJob projects one exact lifecycle operation from the
// target's selected job profile. The model receives a start-invocation
// reference rather than arbitrary job-ID authority.
func (router *RemoteWorkspaceNodeRouter) DescribeRemoteWorkspaceJob(
	workspaceAlias string,
	operation string,
) (RemoteWorkspaceOperation, error) {
	binding, ok := router.byAlias[workspaceAlias]
	if !ok || !binding.allowJobs {
		return RemoteWorkspaceOperation{}, ErrRemoteWorkspaceUnavailable
	}
	command, ok := remoteWorkspaceJobCommand(operation)
	if !ok || command == nodes.JobCommandStart {
		return RemoteWorkspaceOperation{}, ErrRemoteWorkspaceUnavailable
	}
	return router.describeRemoteWorkspaceExecCommand(binding, command)
}

func (router *RemoteWorkspaceNodeRouter) describeRemoteWorkspaceExecCommand(
	binding remoteWorkspaceNodeBinding,
	command string,
) (RemoteWorkspaceOperation, error) {
	resolved, err := router.runtime.resolveTarget(router.agentID, binding.config.Target, false)
	if err != nil || resolved.registration == nil {
		return RemoteWorkspaceOperation{}, ErrRemoteWorkspaceUnavailable
	}
	descriptor, found := visibleNodeCommand(
		resolved.snapshot.Catalog,
		resolved.registration,
		resolved.catalogHash,
		command,
	)
	if !found || descriptor.ModelContract == nil {
		return RemoteWorkspaceOperation{}, ErrRemoteWorkspaceUnavailable
	}
	if nodes.IsJobCommand(command) {
		descriptor, found = nodes.ProjectJobDescriptorForProfile(descriptor, resolved.binding.JobProfile)
		if !found {
			return RemoteWorkspaceOperation{}, ErrRemoteWorkspaceUnavailable
		}
	}
	if descriptor.ModelContract == nil || descriptor.ModelContract.Availability != nodes.ModelAvailable ||
		descriptor.ModelContract.ApprovalMode != "" || resolved.requiresReapproval ||
		!slices.Contains(descriptor.ModelContract.Constraints.WorkingScopes, binding.config.WorkingScope) {
		return RemoteWorkspaceOperation{}, ErrRemoteWorkspaceUnavailable
	}
	return RemoteWorkspaceOperation{
		Target: binding.config.Target, Available: resolved.available, Risk: descriptor.Risk,
		ResultKind:       descriptor.ModelContract.ResultKind,
		SupportsProgress: descriptor.SupportsProgress, SupportsCancel: descriptor.SupportsCancel,
		ExecutableAliases: append(
			[]string(nil),
			descriptor.ModelContract.Constraints.ExecutableAliases...,
		),
		EnvironmentNames:  append([]string(nil), descriptor.ModelContract.Constraints.EnvironmentNames...),
		TimeoutSecondsMax: descriptor.ModelContract.TimeoutSecondsMax,
		ArtifactCountMax:  remoteWorkspaceJobArtifactCount(descriptor),
	}, nil
}

func remoteWorkspaceExecMode(name string, described RemoteWorkspaceOperation) RemoteWorkspaceExecMode {
	return RemoteWorkspaceExecMode{
		Name: name, ExecutableAliases: append([]string(nil), described.ExecutableAliases...),
		EnvironmentNames:  append([]string(nil), described.EnvironmentNames...),
		TimeoutSecondsMax: described.TimeoutSecondsMax, ArtifactCountMax: described.ArtifactCountMax,
	}
}

func remoteWorkspaceJobArtifactCount(descriptor nodes.CommandDescriptor) int {
	if len(descriptor.JobProfiles) != 1 {
		return 0
	}
	return descriptor.JobProfiles[0].ArtifactCountMax
}

// ExecuteRemoteWorkspaceExec reuses the existing direct-argv adapter while
// retaining workspace revision in the durable invocation identity.
func (router *RemoteWorkspaceNodeRouter) ExecuteRemoteWorkspaceExec(
	ctx context.Context,
	workspaceAlias string,
	args map[string]any,
) *toolshared.ToolResult {
	prepared, binding, mode, _, _, err := router.prepareWorkspaceExec(ctx, args)
	if err != nil || binding.alias != workspaceAlias {
		return toolshared.ErrorResult("remote workspace execution authority is unavailable")
	}
	boundCtx := bindRemoteWorkspaceInvocationIdentity(ctx, binding)
	result := router.invoke.execute(boundCtx, prepared, false)
	return projectWorkspaceExecResult(result, binding, mode)
}

// ExecuteRemoteWorkspaceJob resolves the opaque job only through the exact
// workspace_exec invocation that produced it, then dispatches one typed job
// lifecycle command. Neither a raw job ID nor a node identity is accepted.
func (router *RemoteWorkspaceNodeRouter) ExecuteRemoteWorkspaceJob(
	ctx context.Context,
	startCtx context.Context,
	workspaceAlias string,
	operation string,
	args map[string]any,
) *toolshared.ToolResult {
	binding, ok := router.byAlias[workspaceAlias]
	if !ok || !binding.allowJobs {
		return toolshared.ErrorResult("remote workspace job authority is unavailable")
	}
	command, ok := remoteWorkspaceJobCommand(operation)
	if !ok || command == nodes.JobCommandStart {
		return toolshared.ErrorResult("remote workspace job operation is unavailable")
	}
	jobInvocationID, ok := args[remoteWorkspaceJobInvocationArgument].(string)
	if !ok || strings.TrimSpace(jobInvocationID) != jobInvocationID || jobInvocationID == "" {
		return toolshared.ErrorResult("remote workspace job authority is unavailable")
	}
	job, err := router.resolveRemoteWorkspaceJob(startCtx, binding)
	if err != nil {
		return toolshared.ErrorResult("remote workspace job authority is unavailable")
	}
	input, err := remoteWorkspaceJobInput(operation, args, job.id)
	if err != nil {
		return toolshared.ErrorResult("remote workspace job arguments are invalid")
	}
	prepared, err := router.prepareRemoteWorkspaceJobInvocation(binding, job, command, input)
	if err != nil {
		return toolshared.ErrorResult("remote workspace job authority is unavailable")
	}
	boundCtx := bindRemoteWorkspaceInvocationIdentity(ctx, binding)
	result := router.invoke.execute(boundCtx, prepared, false)
	return projectRemoteWorkspaceJobResult(result, binding, operation, jobInvocationID)
}

// LookupRemoteWorkspaceInvocationByCurrentCall resolves an invocation whose
// durable identity includes this exact workspace alias and revision.
func (router *RemoteWorkspaceNodeRouter) LookupRemoteWorkspaceInvocationByCurrentCall(
	ctx context.Context,
	workspaceAlias string,
) (nodes.GatewayInvocationRecord, bool, error) {
	binding, ok := router.byAlias[workspaceAlias]
	if !ok || router.runtime == nil {
		return nodes.GatewayInvocationRecord{}, false, ErrRemoteWorkspaceUnavailable
	}
	return LookupNodeInvocationByRemoteWorkspaceCurrentCall(
		ctx,
		router.runtime.source,
		binding.alias,
		binding.config.Revision,
	)
}

// LookupNodeInvocationByRemoteWorkspaceCurrentCall is the revocation-safe
// recovery seam for a still-configured workspace capability. It requires the
// exact operator-owned alias and revision that participated in preparation.
func LookupNodeInvocationByRemoteWorkspaceCurrentCall(
	ctx context.Context,
	source NodeInvocationSource,
	workspaceAlias string,
	workspaceRevision string,
) (nodes.GatewayInvocationRecord, bool, error) {
	if strings.TrimSpace(workspaceAlias) == "" || strings.TrimSpace(workspaceRevision) == "" {
		return nodes.GatewayInvocationRecord{}, false, ErrRemoteWorkspaceUnavailable
	}
	return LookupNodeInvocationByCurrentCall(
		withNodeInvocationWorkspace(ctx, workspaceAlias, workspaceRevision),
		source,
	)
}

type remoteWorkspaceJobAuthority struct {
	id         string
	target     string
	nodeID     nodes.ID
	jobProfile string
}

func (router *RemoteWorkspaceNodeRouter) resolveRemoteWorkspaceJob(
	startCtx context.Context,
	binding remoteWorkspaceNodeBinding,
) (remoteWorkspaceJobAuthority, error) {
	boundCtx := bindRemoteWorkspaceInvocationIdentity(startCtx, binding)
	retained, found, err := LookupNodeInvocationByCurrentCall(boundCtx, router.runtime.source)
	if err != nil || !found || retained.Target != binding.config.Target ||
		retained.Plan.Command != nodes.JobCommandStart || retained.Plan.NodeID.Validate() != nil ||
		strings.TrimSpace(retained.Plan.JobProfile) == "" {
		return remoteWorkspaceJobAuthority{}, ErrRemoteWorkspaceUnavailable
	}
	var startInput struct {
		CWD string `json:"cwd"`
	}
	if json.Unmarshal(retained.Plan.Input, &startInput) != nil || startInput.CWD != binding.config.WorkingScope {
		return remoteWorkspaceJobAuthority{}, ErrRemoteWorkspaceUnavailable
	}
	record, principal, snapshot, _, err := router.runtime.visibleInvocation(
		boundCtx,
		map[string]any{"invocation_id": retained.Plan.InvocationID},
	)
	if err != nil || record.Target != retained.Target || record.Plan.Command != nodes.JobCommandStart ||
		record.Plan.NodeID != retained.Plan.NodeID || record.Plan.JobProfile != retained.Plan.JobProfile {
		return remoteWorkspaceJobAuthority{}, ErrRemoteWorkspaceUnavailable
	}
	remote, _, err := router.runtime.queryInvocationStatus(
		boundCtx,
		principal,
		record.Target,
		snapshot.ID,
		record.Plan.InvocationID,
	)
	if err != nil || remote.State != nodes.InvocationSucceeded {
		return remoteWorkspaceJobAuthority{}, ErrRemoteWorkspaceUnavailable
	}
	var result struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal(remote.Result, &result) != nil || !validRemoteWorkspaceJobID(result.JobID) {
		return remoteWorkspaceJobAuthority{}, ErrRemoteWorkspaceUnavailable
	}
	return remoteWorkspaceJobAuthority{
		id: result.JobID, target: record.Target, nodeID: record.Plan.NodeID, jobProfile: record.Plan.JobProfile,
	}, nil
}

func (router *RemoteWorkspaceNodeRouter) prepareRemoteWorkspaceJobInvocation(
	binding remoteWorkspaceNodeBinding,
	job remoteWorkspaceJobAuthority,
	command string,
	input map[string]any,
) (map[string]any, error) {
	resolved, err := router.runtime.resolveTarget(router.agentID, binding.config.Target, false)
	if err != nil || resolved.registration == nil || !resolved.available ||
		job.target != binding.config.Target || resolved.snapshot.ID != job.nodeID ||
		resolved.binding.JobProfile != job.jobProfile {
		return nil, ErrRemoteWorkspaceUnavailable
	}
	descriptor, found := nodeCatalogDescriptor(resolved.snapshot.Catalog, command)
	if !found || descriptor.ModelContract == nil {
		return nil, ErrRemoteWorkspaceUnavailable
	}
	descriptor, found = nodes.ProjectJobDescriptorForProfile(descriptor, job.jobProfile)
	if !found || descriptor.ModelContract == nil || descriptor.ModelContract.Availability != nodes.ModelAvailable ||
		descriptor.ModelContract.ApprovalMode != "" || resolved.requiresReapproval ||
		!slices.Contains(descriptor.ModelContract.Constraints.WorkingScopes, binding.config.WorkingScope) {
		return nil, ErrRemoteWorkspaceUnavailable
	}
	revision, err := router.runtime.access.discoveryRevision(
		router.agentID,
		resolved.name,
		command,
		resolved.snapshot,
		*resolved.registration,
		descriptor,
		resolved.available,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"target": binding.config.Target, "command": command, "input": input,
		"discovery_revision": revision,
		"timeout_seconds":    min(defaultWorkspaceExecTimeout, descriptor.ModelContract.TimeoutSecondsMax),
		"output_limit_bytes": min(defaultNodeInvocationOutput, descriptor.ModelContract.OutputBytesMax),
	}, nil
}

func remoteWorkspaceJobCommand(operation string) (string, bool) {
	switch operation {
	case "workspace_exec":
		return nodes.JobCommandStart, true
	case "job_status":
		return nodes.JobCommandStatus, true
	case "job_logs":
		return nodes.JobCommandLogs, true
	case "job_artifacts":
		return nodes.JobCommandArtifacts, true
	case "job_cancel":
		return nodes.JobCommandCancel, true
	default:
		return "", false
	}
}

func remoteWorkspaceJobInput(operation string, args map[string]any, jobID string) (map[string]any, error) {
	allowed := map[string]struct{}{remoteWorkspaceJobInvocationArgument: {}}
	input := map[string]any{"job_id": jobID}
	if operation == "job_logs" {
		for _, name := range []string{"stream", "cursor", "limit_bytes"} {
			allowed[name] = struct{}{}
			value, exists := args[name]
			if !exists {
				return nil, ErrRemoteWorkspaceUnavailable
			}
			input[name] = value
		}
	}
	for name := range args {
		if _, ok := allowed[name]; !ok {
			return nil, ErrRemoteWorkspaceUnavailable
		}
	}
	return input, nil
}

func validRemoteWorkspaceJobID(value string) bool {
	if len(value) != 36 || !strings.HasPrefix(value, "job_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "job_"))
	return err == nil
}

func (*WorkspaceExecTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (router *RemoteWorkspaceNodeRouter) prepareWorkspaceExec(
	ctx context.Context,
	toolArgs map[string]any,
) (map[string]any, remoteWorkspaceNodeBinding, string, string, any, error) {
	for name := range toolArgs {
		switch name {
		case remoteWorkspaceArgument, "executable", "args", "env", "mode", "timeout_seconds", "artifacts":
		default:
			return nil, remoteWorkspaceNodeBinding{}, "", "", nil, ErrRemoteWorkspaceUnavailable
		}
	}
	workspace, ok := toolArgs[remoteWorkspaceArgument].(string)
	if !ok || workspace == "" || strings.TrimSpace(workspace) != workspace {
		return nil, remoteWorkspaceNodeBinding{}, "", "", nil, ErrRemoteWorkspaceUnavailable
	}
	binding, ok := router.byAlias[workspace]
	if !ok {
		return nil, remoteWorkspaceNodeBinding{}, "", "", nil, ErrRemoteWorkspaceUnavailable
	}
	mode, ok := toolArgs["mode"].(string)
	if !ok || (mode != "foreground" && mode != "job") {
		return nil, remoteWorkspaceNodeBinding{}, "", "", nil, ErrRemoteWorkspaceUnavailable
	}
	command := "system.exec.v1"
	if mode == "job" {
		if !binding.allowJobs {
			return nil, remoteWorkspaceNodeBinding{}, "", "", nil, ErrRemoteWorkspaceUnavailable
		}
		command = nodes.JobCommandStart
	}
	executable, ok := toolArgs["executable"].(string)
	if !ok || executable == "" || strings.TrimSpace(executable) != executable {
		return nil, remoteWorkspaceNodeBinding{}, "", "", nil, ErrRemoteWorkspaceUnavailable
	}
	argv, ok := workspaceExecArgv(executable, toolArgs["args"])
	if !ok {
		return nil, remoteWorkspaceNodeBinding{}, "", "", nil, ErrRemoteWorkspaceUnavailable
	}
	environment, ok := workspaceExecEnvironment(toolArgs)
	if !ok {
		return nil, remoteWorkspaceNodeBinding{}, "", "", nil, ErrRemoteWorkspaceUnavailable
	}
	input := map[string]any{
		"argv": argv, "cwd": binding.config.WorkingScope, "env": environment,
	}
	artifacts, artifactsOK := workspaceExecArtifacts(toolArgs)
	if !artifactsOK {
		return nil, remoteWorkspaceNodeBinding{}, "", "", nil, ErrRemoteWorkspaceUnavailable
	}
	if mode == "foreground" {
		if _, supplied := toolArgs["artifacts"]; supplied {
			return nil, remoteWorkspaceNodeBinding{}, "", "", nil, ErrRemoteWorkspaceUnavailable
		}
	} else {
		input["artifacts"] = artifacts
	}
	requestedTimeout, timeoutSupplied := toolArgs["timeout_seconds"]
	prepared, effectiveTimeout, err := router.prepareWorkspaceExecInvocation(
		binding,
		command,
		input,
		requestedTimeout,
		timeoutSupplied,
	)
	if err != nil {
		return nil, remoteWorkspaceNodeBinding{}, "", "", nil, err
	}
	return prepared, binding, mode, executable, effectiveTimeout, nil
}

func (router *RemoteWorkspaceNodeRouter) prepareWorkspaceExecInvocation(
	binding remoteWorkspaceNodeBinding,
	command string,
	input map[string]any,
	requestedTimeout any,
	timeoutSupplied bool,
) (map[string]any, any, error) {
	resolved, err := router.runtime.resolveTarget(router.agentID, binding.config.Target, false)
	if err != nil || resolved.registration == nil || !resolved.available {
		return nil, nil, ErrRemoteWorkspaceUnavailable
	}
	descriptor, found := nodeCatalogDescriptor(resolved.snapshot.Catalog, command)
	if !found || descriptor.ModelContract == nil {
		return nil, nil, ErrRemoteWorkspaceUnavailable
	}
	if command == nodes.JobCommandStart {
		descriptor, found = nodes.ProjectJobDescriptorForProfile(descriptor, resolved.binding.JobProfile)
		if !found {
			return nil, nil, ErrRemoteWorkspaceUnavailable
		}
	}
	constraints := descriptor.ModelContract.Constraints
	if !slices.Contains(constraints.WorkingScopes, binding.config.WorkingScope) {
		return nil, nil, ErrRemoteWorkspaceUnavailable
	}
	effectiveTimeout := requestedTimeout
	if !timeoutSupplied {
		effectiveTimeout = float64(min(defaultWorkspaceExecTimeout, descriptor.ModelContract.TimeoutSecondsMax))
	}
	input["timeout_seconds"] = effectiveTimeout
	revision, err := router.runtime.access.discoveryRevision(
		router.agentID,
		resolved.name,
		command,
		resolved.snapshot,
		*resolved.registration,
		descriptor,
		resolved.available,
	)
	if err != nil {
		return nil, nil, err
	}
	invocationTimeout := effectiveTimeout
	if command == nodes.JobCommandStart {
		invocationTimeout = float64(min(defaultWorkspaceExecTimeout, descriptor.ModelContract.TimeoutSecondsMax))
	}
	return map[string]any{
		"target":             binding.config.Target,
		"command":            command,
		"input":              input,
		"discovery_revision": revision,
		"timeout_seconds":    invocationTimeout,
		"output_limit_bytes": min(defaultNodeInvocationOutput, descriptor.ModelContract.OutputBytesMax),
	}, effectiveTimeout, nil
}

func bindRemoteWorkspaceInvocationIdentity(
	ctx context.Context,
	binding remoteWorkspaceNodeBinding,
) context.Context {
	return withNodeInvocationWorkspace(ctx, binding.alias, binding.config.Revision)
}

func workspaceExecArgv(executable string, raw any) ([]any, bool) {
	arguments, ok := raw.([]any)
	if !ok || len(arguments) > 127 {
		return nil, false
	}
	argv := make([]any, 1, len(arguments)+1)
	argv[0] = executable
	argv = append(argv, arguments...)
	return argv, true
}

func workspaceExecEnvironment(args map[string]any) (map[string]any, bool) {
	raw, exists := args["env"]
	if !exists {
		return map[string]any{}, true
	}
	environment, ok := raw.(map[string]any)
	return environment, ok
}

func workspaceExecArtifacts(args map[string]any) ([]any, bool) {
	raw, exists := args["artifacts"]
	if !exists {
		return []any{}, true
	}
	artifacts, ok := raw.([]any)
	return artifacts, ok
}

func projectWorkspaceExecResult(
	result *toolshared.ToolResult,
	binding remoteWorkspaceNodeBinding,
	mode string,
) *toolshared.ToolResult {
	base := map[string]any{
		"placement": "remote", remoteWorkspaceArgument: binding.alias,
		"remote_workspace_revision": binding.config.Revision, "target": binding.config.Target,
		"mode": mode,
	}
	return projectRemoteWorkspaceInvocationResult(result, base, mode == "job")
}

func projectRemoteWorkspaceJobResult(
	result *toolshared.ToolResult,
	binding remoteWorkspaceNodeBinding,
	operation string,
	jobInvocationID string,
) *toolshared.ToolResult {
	base := map[string]any{
		"placement": "remote", remoteWorkspaceArgument: binding.alias,
		"remote_workspace_revision": binding.config.Revision, "target": binding.config.Target,
		"operation": operation, remoteWorkspaceJobInvocationArgument: jobInvocationID,
	}
	return projectRemoteWorkspaceInvocationResult(result, base, false)
}

func projectRemoteWorkspaceInvocationResult(
	result *toolshared.ToolResult,
	base map[string]any,
	jobStart bool,
) *toolshared.ToolResult {
	if result == nil {
		return workspaceExecErrorResult(base, "RESULT_UNAVAILABLE", "remote workspace result is unavailable")
	}
	if result.IsError {
		var failure map[string]any
		if err := json.Unmarshal([]byte(result.ContentForLLM()), &failure); err != nil {
			return workspaceExecErrorResult(base, "EXECUTION_FAILED", "remote workspace execution failed")
		}
		for key, value := range failure {
			base[key] = value
		}
		stripRemoteWorkspaceJobIDs(base)
		if invocation, ok := failure["invocation"].(map[string]any); ok {
			if value, exists := invocation["invocation_id"]; exists {
				base["invocation_id"] = value
			}
			if value, exists := invocation["state"]; exists {
				base["state"] = value
			}
		}
		encoded, err := json.Marshal(base)
		if err != nil {
			return toolshared.ErrorResult("remote workspace execution failed")
		}
		return toolshared.ErrorResult(string(encoded))
	}
	var view nodeInvokeResult
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &view); err != nil || len(view.Result) == 0 {
		return workspaceExecErrorResult(base, "RESULT_MALFORMED", "remote workspace result is malformed")
	}
	var payload any
	if err := json.Unmarshal(view.Result, &payload); err != nil {
		return workspaceExecErrorResult(base, "RESULT_MALFORMED", "remote workspace result is malformed")
	}
	stripRemoteWorkspaceJobIDs(payload)
	base["invocation_id"] = view.InvocationID
	base["state"] = view.State
	base["result"] = payload
	if jobStart {
		base[remoteWorkspaceJobInvocationArgument] = view.InvocationID
	}
	return nodeJSONResult(base)
}

func stripRemoteWorkspaceJobIDs(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "job_id")
		for _, child := range typed {
			stripRemoteWorkspaceJobIDs(child)
		}
	case []any:
		for _, child := range typed {
			stripRemoteWorkspaceJobIDs(child)
		}
	}
}

func workspaceExecErrorResult(base map[string]any, code string, message string) *toolshared.ToolResult {
	base["error_code"] = code
	base["error"] = message
	encoded, err := json.Marshal(base)
	if err != nil {
		return toolshared.ErrorResult(fmt.Sprintf("remote workspace execution failed: %s", code))
	}
	return toolshared.ErrorResult(string(encoded))
}
