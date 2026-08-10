package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// RemoteWorkspaceSource executes one compatible tool against an explicit
// operator-configured remote workspace. It is deliberately narrower than a
// generic tool proxy.
type RemoteWorkspaceSource interface {
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

func (tool *RemoteWorkspaceTool) ApprovalArguments(
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
		tool.normalizeRemoteArguments(remoteArgs),
	)
}

// RemoteWorkspaceTool preserves one agent-specific local tool and routes
// only calls that explicitly carry a configured workspace alias.
type RemoteWorkspaceTool struct {
	local    toolshared.Tool
	remote   RemoteWorkspaceSource
	aliases  []string
	lineRead bool
}

func NewRemoteWorkspaceTool(
	local toolshared.Tool,
	remote RemoteWorkspaceSource,
) (*RemoteWorkspaceTool, error) {
	if local == nil || remote == nil {
		return nil, fmt.Errorf("local tool and remote workspace source are required")
	}
	if !slices.Contains([]string{"read_file", "search_files", "write_file", "apply_patch"}, local.Name()) {
		return nil, fmt.Errorf("tool %q is not remote-workspace compatible", local.Name())
	}
	aliases := append([]string(nil), remote.WorkspaceAliases()...)
	slices.Sort(aliases)
	aliases = slices.Compact(aliases)
	if len(aliases) == 0 {
		return nil, fmt.Errorf("remote workspace source has no aliases")
	}
	return &RemoteWorkspaceTool{
		local: local, remote: remote, aliases: aliases,
		lineRead: local.Name() == "read_file" && toolHasParameter(local, "start_line"),
	}, nil
}

func (tool *RemoteWorkspaceTool) Name() string { return tool.local.Name() }

func (tool *RemoteWorkspaceTool) Description() string {
	return tool.local.Description() +
		" Omit workspace for the current gateway-local workspace, or pass one configured remote workspace alias. " +
		"A failed remote call never falls back to the local host."
}

func (tool *RemoteWorkspaceTool) Parameters() map[string]any {
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
	if tool.Name() == "write_file" {
		properties["expected_sha256"] = map[string]any{
			"type":        "string",
			"minLength":   64,
			"maxLength":   64,
			"pattern":     "^[A-Fa-f0-9]{64}$",
			"description": "Optional remote replace precondition from a preceding remote read. Requires overwrite=true.",
		}
	}
	return parameters
}

func (tool *RemoteWorkspaceTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	workspace, routedArgs, remote, err := tool.routeArguments(args)
	if err != nil {
		return toolshared.ErrorResult("remote workspace is unavailable")
	}
	if !remote {
		if _, exists := routedArgs["expected_sha256"]; exists {
			return toolshared.ErrorResult("expected_sha256 is available only with a remote workspace")
		}
		return tool.local.Execute(ctx, routedArgs)
	}
	return tool.remote.ExecuteRemoteWorkspace(ctx, tool.Name(), workspace, tool.normalizeRemoteArguments(routedArgs))
}

func (tool *RemoteWorkspaceTool) routeArguments(
	args map[string]any,
) (string, map[string]any, bool, error) {
	raw, exists := args["workspace"]
	if !exists {
		if _, remoteOnly := args["expected_sha256"]; remoteOnly {
			return "", nil, false, ErrRemoteWorkspaceUnavailable
		}
		return "", args, false, nil
	}
	workspace, ok := raw.(string)
	workspace = strings.TrimSpace(workspace)
	routed := cloneToolArguments(args)
	delete(routed, "workspace")
	if !ok || workspace == "" {
		if _, remoteOnly := routed["expected_sha256"]; remoteOnly {
			return "", nil, false, ErrRemoteWorkspaceUnavailable
		}
		return "", routed, false, nil
	}
	if !slices.Contains(tool.aliases, workspace) {
		return "", nil, false, ErrRemoteWorkspaceUnavailable
	}
	return workspace, routed, true, nil
}

func (tool *RemoteWorkspaceTool) normalizeRemoteArguments(args map[string]any) map[string]any {
	if tool.lineRead {
		if _, hasStart := args["start_line"]; !hasStart {
			if _, hasLimit := args["max_lines"]; !hasLimit {
				args["start_line"] = float64(1)
			}
		}
	}
	if tool.Name() == "write_file" {
		if _, exists := args["overwrite"]; !exists {
			args["overwrite"] = false
		}
	}
	return args
}

func toolHasParameter(tool toolshared.Tool, name string) bool {
	properties, _ := tool.Parameters()["properties"].(map[string]any)
	_, exists := properties[name]
	return exists
}

func (tool *RemoteWorkspaceTool) ToolLoopSemantics() loopguard.Semantics {
	if tool.Name() == "write_file" || tool.Name() == "apply_patch" {
		return loopguard.SemanticsMutating
	}
	return loopguard.SemanticsReadOnlyIdempotent
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
