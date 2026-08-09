package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

var ErrRemoteWorkspaceUnavailable = errors.New("remote workspace unavailable")

type remoteWorkspaceNodeBinding struct {
	alias  string
	config config.RemoteWorkspace
}

// RemoteWorkspaceNodeRouter maps read-only local tool shapes onto the hidden
// typed workspace commands. Generic nodes_invoke cannot dispatch those
// commands, so the configured workspace remains the only gateway authority.
type RemoteWorkspaceNodeRouter struct {
	agentID string
	runtime *nodeInvocationToolRuntime
	invoke  *NodeInvokeTool
	byAlias map[string]remoteWorkspaceNodeBinding
	aliases []string
}

func NewRemoteWorkspaceNodeRouter(
	cfg *config.Config,
	source NodeInvocationSource,
	agentID string,
	toolName string,
) (*RemoteWorkspaceNodeRouter, error) {
	if cfg == nil || source == nil || strings.TrimSpace(agentID) == "" {
		return nil, fmt.Errorf("remote workspace node router requires config, source, and agent")
	}
	runtime := newNodeInvocationToolRuntime(cfg, source)
	visible, _ := runtime.access.visibleTargets(agentID)
	byAlias := make(map[string]remoteWorkspaceNodeBinding)
	aliases := make([]string, 0, len(cfg.Execution.RemoteWorkspaces))
	for alias, workspace := range cfg.Execution.RemoteWorkspaces {
		if _, allowed := cfg.RemoteWorkspaceAllows(alias, toolName); !allowed ||
			!slices.Contains(visible, workspace.Target) {
			continue
		}
		byAlias[alias] = remoteWorkspaceNodeBinding{alias: alias, config: workspace}
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	if len(aliases) == 0 {
		return nil, fmt.Errorf("%w: agent %s has no %s grant", ErrRemoteWorkspaceUnavailable, agentID, toolName)
	}
	invoke := &NodeInvokeTool{runtime: runtime}
	return &RemoteWorkspaceNodeRouter{
		agentID: agentID, runtime: runtime, invoke: invoke, byAlias: byAlias, aliases: aliases,
	}, nil
}

func (router *RemoteWorkspaceNodeRouter) WorkspaceAliases() []string {
	return append([]string(nil), router.aliases...)
}

func (router *RemoteWorkspaceNodeRouter) SetEventPublisher(eventBus runtimeevents.Bus) {
	if router != nil && router.runtime != nil {
		router.runtime.runtimeEvents = eventBus
	}
}

func (router *RemoteWorkspaceNodeRouter) ExecuteRemoteWorkspace(
	ctx context.Context,
	toolName string,
	workspaceAlias string,
	toolArgs map[string]any,
) *toolshared.ToolResult {
	binding, ok := router.byAlias[workspaceAlias]
	if !ok {
		return toolshared.ErrorResult("remote workspace is unavailable")
	}
	var command string
	switch toolName {
	case "read_file":
		command = nodes.WorkspaceCommandRead
	case "search_files":
		command = nodes.WorkspaceCommandSearch
	default:
		return toolshared.ErrorResult("tool is not remote-workspace compatible")
	}
	args, err := router.prepareArguments(ctx, binding, command, toolArgs)
	if err != nil {
		return toolshared.ErrorResult("remote workspace authority is unavailable")
	}
	result := router.invoke.execute(ctx, args, true)
	if result == nil || result.IsError {
		return result
	}
	var view nodeInvokeResult
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &view); err != nil || len(view.Result) == 0 {
		return toolshared.ErrorResult("remote workspace result is unavailable")
	}
	var payload any
	if err := json.Unmarshal(view.Result, &payload); err != nil {
		return toolshared.ErrorResult("remote workspace result is malformed")
	}
	return nodeJSONResult(map[string]any{
		"placement":          "remote",
		"workspace":          workspaceAlias,
		"workspace_revision": binding.config.Revision,
		"target":             binding.config.Target,
		"invocation_id":      view.InvocationID,
		"state":              view.State,
		"result":             payload,
	})
}

func (router *RemoteWorkspaceNodeRouter) prepareArguments(
	ctx context.Context,
	binding remoteWorkspaceNodeBinding,
	command string,
	toolArgs map[string]any,
) (map[string]any, error) {
	resolved, err := router.runtime.resolveTarget(router.agentID, binding.config.Target, false)
	if err != nil || resolved.registration == nil || !resolved.available {
		return nil, fmt.Errorf("resolve workspace target")
	}
	descriptor, found := nodeCatalogDescriptor(resolved.snapshot.Catalog, command)
	if !found || descriptor.ModelContract == nil || !nodes.IsWorkspaceCommand(command) {
		return nil, fmt.Errorf("workspace command unavailable")
	}
	fileDescriptor, found := nodeCatalogDescriptor(resolved.snapshot.Catalog, "file.info.v1")
	if !found {
		return nil, fmt.Errorf("workspace file profile unavailable")
	}
	fileDescriptor, found = projectFileDescriptorForTarget(fileDescriptor, resolved.binding.FileProfile)
	if !found || len(fileDescriptor.FileProfiles) != 1 {
		return nil, fmt.Errorf("workspace file profile unavailable")
	}
	profileRevision := fileDescriptor.FileProfiles[0].Revision
	if !slices.Contains(descriptor.ModelContract.Constraints.ProfileAliases, profileRevision) ||
		!slices.Contains(descriptor.ModelContract.Constraints.WorkingScopes, binding.config.WorkingScope) {
		return nil, fmt.Errorf("workspace authority does not intersect node policy")
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
	input := make(map[string]any, len(toolArgs)+3)
	for key, value := range toolArgs {
		input[key] = value
	}
	input["profile_revision"] = profileRevision
	input["workspace_revision"] = binding.config.Revision
	input["working_scope"] = binding.config.WorkingScope
	return map[string]any{
		"target":             binding.config.Target,
		"command":            command,
		"input":              input,
		"discovery_revision": revision,
		"timeout_seconds":    min(30, descriptor.ModelContract.TimeoutSecondsMax),
		"output_limit_bytes": min(nodes.MaxWorkspaceReadBytes, descriptor.ModelContract.OutputBytesMax),
	}, nil
}
