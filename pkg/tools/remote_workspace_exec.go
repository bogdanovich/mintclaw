package tools

import (
	"context"
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

// WorkspaceExecTool binds one explicit remote workspace to the existing
// system.exec.v1 or job.start.v1 invocation path. It owns no process or job
// lifecycle of its own.
type WorkspaceExecTool struct {
	router *RemoteWorkspaceNodeRouter
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

func (*WorkspaceExecTool) Name() string { return "workspace_exec" }

func (*WorkspaceExecTool) Description() string {
	return "Run one direct-argv command in an explicit operator-configured remote workspace. " +
		"Foreground mode uses system.exec.v1. Job mode starts the existing durable P5a job and returns a stable job ID; " +
		"use nodes describe plus nodes_invoke for job status, logs, artifacts, or cancellation. " +
		"This tool accepts no shell text, target, profile, executable path, or cwd, and an uncertain result must be " +
		"recovered with nodes_status rather than replayed."
}

func (tool *WorkspaceExecTool) Parameters() map[string]any {
	aliases := tool.router.WorkspaceAliases()
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workspace": map[string]any{
				"type": "string", "enum": aliases,
				"description": "Operator-configured remote workspace alias.",
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
		"required":             []string{"workspace", "executable", "args", "mode"},
		"additionalProperties": false,
	}
}

func (tool *WorkspaceExecTool) ApprovalArguments(
	ctx context.Context,
	args map[string]any,
) (map[string]any, error) {
	prepared, binding, mode, executable, err := tool.router.prepareWorkspaceExec(ctx, args)
	if err != nil {
		return nil, err
	}
	boundCtx := bindRemoteWorkspaceInvocationIdentity(ctx, binding)
	approval, err := tool.router.invoke.approvalArguments(boundCtx, prepared, false)
	if err != nil {
		return nil, err
	}
	approval["workspace"] = binding.alias
	approval["workspace_revision"] = binding.config.Revision
	approval["operation"] = "workspace_exec"
	approval["mode"] = mode
	approval["executable"] = executable
	if timeout, exists := args["timeout_seconds"]; exists {
		approval["timeout_seconds"] = timeout
	} else {
		approval["timeout_seconds"] = defaultWorkspaceExecTimeout
	}
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
	prepared, binding, mode, _, err := tool.router.prepareWorkspaceExec(ctx, args)
	if err != nil {
		return toolshared.ErrorResult("remote workspace execution authority is unavailable")
	}
	boundCtx := bindRemoteWorkspaceInvocationIdentity(ctx, binding)
	result := tool.router.invoke.execute(boundCtx, prepared, false)
	return projectWorkspaceExecResult(result, binding, mode)
}

func (*WorkspaceExecTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (router *RemoteWorkspaceNodeRouter) prepareWorkspaceExec(
	ctx context.Context,
	toolArgs map[string]any,
) (map[string]any, remoteWorkspaceNodeBinding, string, string, error) {
	for name := range toolArgs {
		switch name {
		case "workspace", "executable", "args", "env", "mode", "timeout_seconds", "artifacts":
		default:
			return nil, remoteWorkspaceNodeBinding{}, "", "", ErrRemoteWorkspaceUnavailable
		}
	}
	workspace, ok := toolArgs["workspace"].(string)
	if !ok || workspace == "" || strings.TrimSpace(workspace) != workspace {
		return nil, remoteWorkspaceNodeBinding{}, "", "", ErrRemoteWorkspaceUnavailable
	}
	binding, ok := router.byAlias[workspace]
	if !ok {
		return nil, remoteWorkspaceNodeBinding{}, "", "", ErrRemoteWorkspaceUnavailable
	}
	mode, ok := toolArgs["mode"].(string)
	if !ok || (mode != "foreground" && mode != "job") {
		return nil, remoteWorkspaceNodeBinding{}, "", "", ErrRemoteWorkspaceUnavailable
	}
	command := "system.exec.v1"
	if mode == "job" {
		if !binding.allowJobs {
			return nil, remoteWorkspaceNodeBinding{}, "", "", ErrRemoteWorkspaceUnavailable
		}
		command = nodes.JobCommandStart
	}
	executable, ok := toolArgs["executable"].(string)
	if !ok || executable == "" || strings.TrimSpace(executable) != executable {
		return nil, remoteWorkspaceNodeBinding{}, "", "", ErrRemoteWorkspaceUnavailable
	}
	argv, ok := workspaceExecArgv(executable, toolArgs["args"])
	if !ok {
		return nil, remoteWorkspaceNodeBinding{}, "", "", ErrRemoteWorkspaceUnavailable
	}
	environment, ok := workspaceExecEnvironment(toolArgs)
	if !ok {
		return nil, remoteWorkspaceNodeBinding{}, "", "", ErrRemoteWorkspaceUnavailable
	}
	timeout := any(float64(defaultWorkspaceExecTimeout))
	if configured, exists := toolArgs["timeout_seconds"]; exists {
		timeout = configured
	}
	input := map[string]any{
		"argv": argv, "cwd": binding.config.WorkingScope, "env": environment,
		"timeout_seconds": timeout,
	}
	artifacts, artifactsOK := workspaceExecArtifacts(toolArgs)
	if !artifactsOK {
		return nil, remoteWorkspaceNodeBinding{}, "", "", ErrRemoteWorkspaceUnavailable
	}
	if mode == "foreground" {
		if _, supplied := toolArgs["artifacts"]; supplied {
			return nil, remoteWorkspaceNodeBinding{}, "", "", ErrRemoteWorkspaceUnavailable
		}
	} else {
		input["artifacts"] = artifacts
	}
	prepared, err := router.prepareWorkspaceExecInvocation(
		binding,
		command,
		input,
		timeout,
	)
	if err != nil {
		return nil, remoteWorkspaceNodeBinding{}, "", "", err
	}
	return prepared, binding, mode, executable, nil
}

func (router *RemoteWorkspaceNodeRouter) prepareWorkspaceExecInvocation(
	binding remoteWorkspaceNodeBinding,
	command string,
	input map[string]any,
	timeout any,
) (map[string]any, error) {
	resolved, err := router.runtime.resolveTarget(router.agentID, binding.config.Target, false)
	if err != nil || resolved.registration == nil || !resolved.available {
		return nil, ErrRemoteWorkspaceUnavailable
	}
	descriptor, found := nodeCatalogDescriptor(resolved.snapshot.Catalog, command)
	if !found || descriptor.ModelContract == nil {
		return nil, ErrRemoteWorkspaceUnavailable
	}
	if command == nodes.JobCommandStart {
		descriptor, found = nodes.ProjectJobDescriptorForProfile(descriptor, resolved.binding.JobProfile)
		if !found {
			return nil, ErrRemoteWorkspaceUnavailable
		}
	}
	constraints := descriptor.ModelContract.Constraints
	if !slices.Contains(constraints.WorkingScopes, binding.config.WorkingScope) {
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
	invocationTimeout := timeout
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
	}, nil
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
		"placement": "remote", "workspace": binding.alias,
		"workspace_revision": binding.config.Revision, "target": binding.config.Target,
		"mode": mode,
	}
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
	base["invocation_id"] = view.InvocationID
	base["state"] = view.State
	base["result"] = payload
	if job, ok := payload.(map[string]any); ok && mode == "job" {
		if jobID, exists := job["job_id"]; exists {
			base["job_id"] = jobID
		}
	}
	return nodeJSONResult(base)
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
