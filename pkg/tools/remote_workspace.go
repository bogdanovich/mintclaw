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
	ApprovalArgumentsRemoteWorkspace(
		context.Context,
		string,
		string,
		map[string]any,
	) (map[string]any, error)
	ExecuteRemoteWorkspace(
		context.Context,
		string,
		string,
		map[string]any,
	) *toolshared.ToolResult
}

func (tool *RemoteWorkspaceReadTool) ApprovalArguments(
	ctx context.Context,
	args map[string]any,
) (map[string]any, error) {
	workspace, remoteArgs, remote, err := tool.routeArguments(args)
	if err != nil {
		return nil, err
	}
	if !remote {
		if provider, ok := tool.local.(ApprovalArgumentsProvider); ok {
			return provider.ApprovalArguments(ctx, remoteArgs)
		}
		return remoteArgs, nil
	}
	return tool.remote.ApprovalArgumentsRemoteWorkspace(
		ctx,
		tool.Name(),
		workspace,
		tool.withReadMode(remoteArgs),
	)
}

// RemoteWorkspaceReadTool preserves one agent-specific local tool and routes
// only calls that explicitly carry a configured workspace alias.
type RemoteWorkspaceReadTool struct {
	local    toolshared.Tool
	remote   RemoteWorkspaceReadSource
	aliases  []string
	lineRead bool
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
	return &RemoteWorkspaceReadTool{
		local: local, remote: remote, aliases: aliases,
		lineRead: local.Name() == "read_file" && toolHasParameter(local, "start_line"),
	}, nil
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
	workspace, routedArgs, remote, err := tool.routeArguments(args)
	if err != nil {
		return toolshared.ErrorResult("remote workspace is unavailable")
	}
	if !remote {
		return tool.local.Execute(ctx, routedArgs)
	}
	return tool.remote.ExecuteRemoteWorkspace(ctx, tool.Name(), workspace, tool.withReadMode(routedArgs))
}

func (tool *RemoteWorkspaceReadTool) routeArguments(
	args map[string]any,
) (string, map[string]any, bool, error) {
	raw, exists := args["workspace"]
	if !exists {
		return "", args, false, nil
	}
	workspace, ok := raw.(string)
	workspace = strings.TrimSpace(workspace)
	routed := cloneToolArguments(args)
	delete(routed, "workspace")
	if !ok || workspace == "" {
		return "", routed, false, nil
	}
	if !slices.Contains(tool.aliases, workspace) {
		return "", nil, false, ErrRemoteWorkspaceUnavailable
	}
	return workspace, routed, true, nil
}

func (tool *RemoteWorkspaceReadTool) withReadMode(args map[string]any) map[string]any {
	if tool.lineRead {
		if _, hasStart := args["start_line"]; !hasStart {
			if _, hasLimit := args["max_lines"]; !hasLimit {
				args["start_line"] = float64(1)
			}
		}
	}
	return args
}

func toolHasParameter(tool toolshared.Tool, name string) bool {
	properties, _ := tool.Parameters()["properties"].(map[string]any)
	_, exists := properties[name]
	return exists
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
