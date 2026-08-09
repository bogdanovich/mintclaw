package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// RemoteWorkspaceReadSource executes one read-only tool against an explicit
// operator-configured remote workspace. It is deliberately narrower than a
// generic tool proxy.
type RemoteWorkspaceReadSource interface {
	WorkspaceAliases() []string
	ExecuteRemoteWorkspace(
		context.Context,
		string,
		string,
		map[string]any,
	) *toolshared.ToolResult
}

// RemoteWorkspaceReadTool preserves one agent-specific local tool and routes
// only calls that explicitly carry a configured workspace alias.
type RemoteWorkspaceReadTool struct {
	local   toolshared.Tool
	remote  RemoteWorkspaceReadSource
	aliases []string
}

func NewRemoteWorkspaceReadTool(
	local toolshared.Tool,
	remote RemoteWorkspaceReadSource,
) (*RemoteWorkspaceReadTool, error) {
	if local == nil || remote == nil {
		return nil, fmt.Errorf("local tool and remote workspace source are required")
	}
	if local.Name() != "read_file" && local.Name() != "search_files" {
		return nil, fmt.Errorf("tool %q is not remote-workspace read compatible", local.Name())
	}
	aliases := append([]string(nil), remote.WorkspaceAliases()...)
	slices.Sort(aliases)
	aliases = slices.Compact(aliases)
	if len(aliases) == 0 {
		return nil, fmt.Errorf("remote workspace source has no aliases")
	}
	return &RemoteWorkspaceReadTool{local: local, remote: remote, aliases: aliases}, nil
}

func (tool *RemoteWorkspaceReadTool) Name() string { return tool.local.Name() }

func (tool *RemoteWorkspaceReadTool) Description() string {
	return tool.local.Description() +
		" Omit workspace for the current gateway-local workspace, or pass one configured remote workspace alias. " +
		"A failed remote call never falls back to the local host."
}

func (tool *RemoteWorkspaceReadTool) Parameters() map[string]any {
	parameters := cloneToolParameters(tool.local.Parameters())
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		properties = make(map[string]any)
		parameters["properties"] = properties
	}
	properties["workspace"] = map[string]any{
		"type":        "string",
		"enum":        append([]string(nil), tool.aliases...),
		"description": "Optional operator-configured remote workspace alias. Omit for gateway-local execution.",
	}
	return parameters
}

func (tool *RemoteWorkspaceReadTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	raw, exists := args["workspace"]
	if !exists {
		return tool.local.Execute(ctx, args)
	}
	workspace, ok := raw.(string)
	workspace = strings.TrimSpace(workspace)
	if !ok || workspace == "" {
		localArgs := cloneToolArguments(args)
		delete(localArgs, "workspace")
		return tool.local.Execute(ctx, localArgs)
	}
	if !slices.Contains(tool.aliases, workspace) {
		return toolshared.ErrorResult("remote workspace is unavailable")
	}
	remoteArgs := cloneToolArguments(args)
	delete(remoteArgs, "workspace")
	return tool.remote.ExecuteRemoteWorkspace(ctx, tool.Name(), workspace, remoteArgs)
}

func (*RemoteWorkspaceReadTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

func (*RemoteWorkspaceReadTool) ToolSteeringSafety(map[string]any) toolshared.SteeringSafety {
	return toolshared.SteeringSafetyReadOnly
}

func cloneToolParameters(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneToolParameterValue(value)
	}
	return cloned
}

func cloneToolParameterValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneToolParameters(typed)
	case []string:
		return append([]string(nil), typed...)
	case []any:
		cloned := make([]any, len(typed))
		for index := range typed {
			cloned[index] = cloneToolParameterValue(typed[index])
		}
		return cloned
	default:
		return value
	}
}

func cloneToolArguments(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
