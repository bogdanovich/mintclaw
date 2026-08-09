package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type remoteWorkspaceLocalTool struct {
	calls int
	args  map[string]any
}

func TestRemoteWorkspaceNodeRouterBindsConfiguredRead(t *testing.T) {
	cfg, source := remoteWorkspaceNodeTestSetup(t)
	router, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "read_file")
	if err != nil {
		t.Fatal(err)
	}
	ctx := nodeInvocationTestContext("owner", "workspace-read-1")
	result := router.ExecuteRemoteWorkspace(
		ctx,
		"read_file",
		"project",
		map[string]any{"path": "README.md", "start_line": float64(1), "max_lines": float64(20)},
	)
	payload := decodeNodeResult(t, result)
	if payload["placement"] != "remote" || payload["workspace"] != "project" ||
		payload["workspace_revision"] != "project-workspace-v1" || payload["target"] != "build" ||
		source.dispatchCalls != 1 {
		t.Fatalf("remote workspace result = %#v; dispatch calls = %d", payload, source.dispatchCalls)
	}
	remote := payload["result"].(map[string]any)
	if remote["content"] != "1|hello" || remote["path"] != "README.md" {
		t.Fatalf("remote read payload = %#v", remote)
	}
}

func TestGenericNodeInvokeCannotBypassRemoteWorkspaceAlias(t *testing.T) {
	cfg, source := remoteWorkspaceNodeTestSetup(t)
	router, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "read_file")
	if err != nil {
		t.Fatal(err)
	}
	ctx := nodeInvocationTestContext("owner", "workspace-read-bypass")
	args, err := router.prepareArguments(
		ctx,
		router.byAlias["project"],
		nodes.WorkspaceCommandRead,
		map[string]any{"path": "README.md"},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := NewNodeInvokeTool(cfg, source).Execute(ctx, args)
	if !result.IsError || source.dispatchCalls != 0 {
		t.Fatalf("generic workspace invocation = %#v; dispatch calls = %d", result, source.dispatchCalls)
	}
}

func TestRemoteWorkspaceNodeRouterBindsConfiguredSearch(t *testing.T) {
	cfg, source := remoteWorkspaceNodeTestSetup(t)
	source.dispatchResult = json.RawMessage(
		`{"result":"pkg/a.go:7:match","matches":1,"files_visited":2,"truncated":false}`,
	)
	router, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "search_files")
	if err != nil {
		t.Fatal(err)
	}
	result := router.ExecuteRemoteWorkspace(
		nodeInvocationTestContext("owner", "workspace-search-1"),
		"search_files",
		"project",
		map[string]any{"pattern": "match", "path": "pkg"},
	)
	payload := decodeNodeResult(t, result)
	remote := payload["result"].(map[string]any)
	if remote["matches"] != float64(1) || remote["result"] != "pkg/a.go:7:match" || source.dispatchCalls != 1 {
		t.Fatalf("remote search = %#v; dispatch calls = %d", payload, source.dispatchCalls)
	}
}

func remoteWorkspaceNodeTestSetup(t *testing.T) (*config.Config, *fakeNodeInvocationSource) {
	t.Helper()
	descriptors, err := nodes.WorkspaceReadDescriptors([]string{"project-v1"}, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	catalog := nodes.CapabilityCatalog{Commands: []nodes.CommandDescriptor{
		nodeFileInfoTestDescriptor("none"),
		descriptors[0],
		descriptors[1],
	}}
	catalogHash := mustCatalogHash(t, catalog)
	snapshot := nodes.Snapshot{
		ID: "private-node-id", State: nodes.StateConnected, Catalog: catalog,
		CatalogHash: catalogHash, Executor: "local", PolicyRevision: "policy-v1",
	}
	discovery := &fakeNodeDiscoverySource{
		byRef: map[string]nodes.Snapshot{"builder-node": snapshot},
		registrations: map[nodes.ID]nodes.Registration{
			snapshot.ID: {
				Snapshot: snapshot, AllowedCommands: []string{
					"file.info.v1", nodes.WorkspaceCommandRead, nodes.WorkspaceCommandSearch,
				},
				ApprovedCatalogHash: catalogHash, ApprovedAt: 1,
			},
		},
		connected: map[nodes.ID]bool{snapshot.ID: true},
	}
	store, err := nodes.NewGatewayInvocationStore(filepath.Join(t.TempDir(), "invocations.json"), 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeNodeInvocationSource{
		fakeNodeDiscoverySource: discovery,
		store:                   store,
		dispatchResult: json.RawMessage(
			`{"path":"README.md","content":"1|hello","size":5,"sha256":"` +
				strings.Repeat("a", 64) + `","truncated":false}`,
		),
	}
	cfg := config.DefaultConfig()
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"build": {Type: "node", Node: "builder-node", FileProfile: "project"},
	}
	cfg.Execution.RemoteWorkspaces = map[string]config.RemoteWorkspace{
		"project": {
			Target: "build", WorkingScope: "project", Revision: "project-workspace-v1",
			Tools: []string{"read_file", "search_files"},
		},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{AllowedTargets: []string{"build"}}
	return cfg, source
}

func (*remoteWorkspaceLocalTool) Name() string        { return "read_file" }
func (*remoteWorkspaceLocalTool) Description() string { return "Read a local file." }
func (*remoteWorkspaceLocalTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

func (tool *remoteWorkspaceLocalTool) Execute(
	_ context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	tool.calls++
	tool.args = args
	return toolshared.NewToolResult("local")
}

type remoteWorkspaceReadSource struct {
	calls     int
	tool      string
	workspace string
	args      map[string]any
}

func (*remoteWorkspaceReadSource) WorkspaceAliases() []string { return []string{"vpn", "mac"} }
func (source *remoteWorkspaceReadSource) ExecuteRemoteWorkspace(
	_ context.Context,
	tool string,
	workspace string,
	args map[string]any,
) *toolshared.ToolResult {
	source.calls++
	source.tool = tool
	source.workspace = workspace
	source.args = args
	return toolshared.NewToolResult("remote")
}

func TestRemoteWorkspaceReadToolRoutesOnlyExplicitAlias(t *testing.T) {
	local := &remoteWorkspaceLocalTool{}
	remote := &remoteWorkspaceReadSource{}
	tool, err := NewRemoteWorkspaceReadTool(local, remote)
	if err != nil {
		t.Fatal(err)
	}

	localResult := tool.Execute(context.Background(), map[string]any{"path": "README.md"})
	if localResult.ContentForLLM() != "local" || local.calls != 1 || remote.calls != 0 {
		t.Fatalf("local result = %#v, local calls = %d, remote calls = %d", localResult, local.calls, remote.calls)
	}
	remoteResult := tool.Execute(context.Background(), map[string]any{
		"path": "README.md", "workspace": "vpn",
	})
	if remoteResult.ContentForLLM() != "remote" || remote.calls != 1 || remote.tool != "read_file" ||
		remote.workspace != "vpn" || remote.args["path"] != "README.md" {
		t.Fatalf("remote result = %#v, source = %#v", remoteResult, remote)
	}
	if _, leaked := remote.args["workspace"]; leaked {
		t.Fatal("remote adapter received workspace as an ordinary tool argument")
	}
}

func TestRemoteWorkspaceReadToolDoesNotFallbackUnknownAlias(t *testing.T) {
	local := &remoteWorkspaceLocalTool{}
	remote := &remoteWorkspaceReadSource{}
	tool, err := NewRemoteWorkspaceReadTool(local, remote)
	if err != nil {
		t.Fatal(err)
	}

	result := tool.Execute(context.Background(), map[string]any{
		"path": "README.md", "workspace": "missing",
	})
	if !result.IsError || local.calls != 0 || remote.calls != 0 {
		t.Fatalf("result = %#v, local calls = %d, remote calls = %d", result, local.calls, remote.calls)
	}
	properties := tool.Parameters()["properties"].(map[string]any)
	workspace := properties["workspace"].(map[string]any)
	aliases := workspace["enum"].([]string)
	if len(aliases) != 2 || aliases[0] != "mac" || aliases[1] != "vpn" {
		t.Fatalf("workspace aliases = %#v", aliases)
	}
	if _, changed := local.Parameters()["properties"].(map[string]any)["workspace"]; changed {
		t.Fatal("decorator mutated local tool parameters")
	}
}

func TestToolLogArgumentsRedactsRemoteWorkspaceFileContent(t *testing.T) {
	arguments := map[string]any{
		"workspace": "private-node", "path": "secret.txt", "pattern": "password",
	}
	got := ToolLogArguments("search_files", arguments)
	if got["redacted"] != true || got["argument_count"] != 3 || len(got) != 2 {
		t.Fatalf("remote workspace arguments = %#v", got)
	}
	if local := ToolLogArguments("search_files", map[string]any{"pattern": "public"}); local["pattern"] != "public" {
		t.Fatalf("local search arguments unexpectedly redacted: %#v", local)
	}
}
