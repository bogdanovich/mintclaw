package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	fstools "github.com/bogdanovich/mintclaw/pkg/tools/fs"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type remoteWorkspaceLocalTool struct {
	calls int
	args  map[string]any
	name  string
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

func TestRemoteWorkspaceNodeRouterPreservesReadApprovalAndPreparedPlan(t *testing.T) {
	cfg, source := remoteWorkspaceNodeTestSetupWithApproval(t, "required")
	router, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "read_file")
	if err != nil {
		t.Fatal(err)
	}
	ctx := nodeInvocationTestContext("owner", "workspace-read-approval")
	toolArgs := map[string]any{"path": "README.md", "start_line": float64(1)}
	approval, err := router.ApprovalArgumentsRemoteWorkspace(
		ctx,
		"read_file",
		"project",
		toolArgs,
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared := mustFakeGatewayInvocation(t, source, ctx, approval["invocation_id"].(string))
	if prepared.Descriptor.ModelContract == nil ||
		prepared.Descriptor.ModelContract.ApprovalMode != "each_command" ||
		len(prepared.Descriptor.FileProfiles) != 1 ||
		prepared.Descriptor.FileProfiles[0].Approval.Read != "required" {
		t.Fatalf("prepared workspace approval authority = %#v", prepared.Descriptor)
	}
	unapproved := router.ExecuteRemoteWorkspace(ctx, "read_file", "project", toolArgs)
	if !unapproved.IsError || !strings.Contains(unapproved.ContentForLLM(), nodeDenialApprovalRequired) ||
		source.dispatchCalls != 0 {
		t.Fatalf("unapproved workspace read = %#v; dispatches = %d", unapproved, source.dispatchCalls)
	}
	approved := router.ExecuteRemoteWorkspace(
		toolshared.WithToolApprovalContinuation(ctx, true),
		"read_file",
		"project",
		toolArgs,
	)
	payload := decodeNodeResult(t, approved)
	if payload["invocation_id"] != approval["invocation_id"] || source.dispatchCalls != 1 || source.prepareCalls != 1 {
		t.Fatalf(
			"approved workspace read = %#v; prepares = %d, dispatches = %d",
			payload,
			source.prepareCalls,
			source.dispatchCalls,
		)
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

func TestRemoteWorkspaceNodeRouterBindsWriteApprovalAndExactPlan(t *testing.T) {
	cfg, source := remoteWorkspaceNodeTestSetupWithApproval(t, "required")
	source.dispatchResult = json.RawMessage(
		`{"path":"README.md","action":"replace","size":4,"sha256":"` + strings.Repeat("b", 64) + `"}`,
	)
	router, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "write_file")
	if err != nil {
		t.Fatal(err)
	}
	ctx := nodeInvocationTestContext("owner", "workspace-write-approval")
	toolArgs := map[string]any{
		"path": "README.md", "content": "new\n", "overwrite": true,
		"expected_sha256": strings.Repeat("a", 64),
	}
	approval, err := router.ApprovalArgumentsRemoteWorkspace(ctx, "write_file", "project", toolArgs)
	if err != nil {
		t.Fatal(err)
	}
	if approval["workspace"] != "project" || approval["path"] != "README.md" ||
		approval["publication"] != "replace" || approval["content_bytes"] != 4 ||
		approval["content"] != nil || approval["input"] != nil {
		t.Fatalf("safe workspace approval = %#v", approval)
	}
	prepared := mustFakeGatewayInvocation(t, source, ctx, approval["invocation_id"].(string))
	if prepared.Plan.Command != nodes.WorkspaceCommandWrite || len(prepared.Descriptor.FileProfiles) != 1 ||
		prepared.Descriptor.FileProfiles[0].Approval.Write != "required" ||
		!strings.Contains(string(prepared.Plan.Input), strings.Repeat("a", 64)) {
		t.Fatalf("prepared workspace write = %#v", prepared)
	}
	result := router.ExecuteRemoteWorkspace(
		toolshared.WithToolApprovalContinuation(ctx, true), "write_file", "project", toolArgs,
	)
	payload := decodeNodeResult(t, result)
	if payload["invocation_id"] != approval["invocation_id"] || source.prepareCalls != 1 ||
		source.dispatchCalls != 1 {
		t.Fatalf("approved write = %#v; prepares=%d dispatches=%d", payload, source.prepareCalls, source.dispatchCalls)
	}
}

func TestRemoteWorkspaceNodeRouterMapsPatch(t *testing.T) {
	cfg, source := remoteWorkspaceNodeTestSetup(t)
	source.dispatchResult = json.RawMessage(
		`{"state":"completed","committed":[{"path":"a.txt","action":"add","size":2,"sha256":"` +
			strings.Repeat("c", 64) + `"}]}`,
	)
	router, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "apply_patch")
	if err != nil {
		t.Fatal(err)
	}
	ctx := nodeInvocationTestContext("owner", "workspace-patch-1")
	result := router.ExecuteRemoteWorkspace(
		ctx,
		"apply_patch",
		"project",
		map[string]any{"input": "*** Begin Patch\n*** Add File: a.txt\n+x\n*** End Patch"},
	)
	payload := decodeNodeResult(t, result)
	prepared := mustFakeGatewayInvocation(t, source, ctx, payload["invocation_id"].(string))
	if payload["placement"] != "remote" || prepared.Plan.Command != nodes.WorkspaceCommandPatch ||
		source.dispatchCalls != 1 {
		t.Fatalf("remote patch = %#v; plan = %#v", payload, prepared.Plan)
	}
}

func remoteWorkspaceNodeTestSetup(t *testing.T) (*config.Config, *fakeNodeInvocationSource) {
	return remoteWorkspaceNodeTestSetupWithApproval(t, "none")
}

func remoteWorkspaceNodeTestSetupWithApproval(
	t *testing.T,
	readApproval string,
) (*config.Config, *fakeNodeInvocationSource) {
	t.Helper()
	fileInfo := nodeFileInfoTestDescriptor("none")
	fileInfo.FileProfiles[0].Approval.Read = readApproval
	fileInfo.FileProfiles[0].Approval.Write = readApproval
	fileInfo.FileProfiles[0].WritableRoots = []string{"/srv/project"}
	fileInfo.FileProfiles[0].AllowCreate = true
	fileInfo.FileProfiles[0].AllowOverwrite = true
	descriptors, err := nodes.WorkspaceDescriptors(fileInfo.FileProfiles, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	catalog := nodes.CapabilityCatalog{Commands: []nodes.CommandDescriptor{
		fileInfo,
		descriptors[0],
		descriptors[1],
		descriptors[2],
		descriptors[3],
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
					nodes.WorkspaceCommandWrite, nodes.WorkspaceCommandPatch,
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
			Tools: []string{"read_file", "search_files", "write_file", "apply_patch"},
		},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{AllowedTargets: []string{"build"}}
	return cfg, source
}

func (tool *remoteWorkspaceLocalTool) Name() string {
	if tool.name != "" {
		return tool.name
	}
	return "read_file"
}
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

func (source *remoteWorkspaceReadSource) ApprovalArgumentsRemoteWorkspace(
	_ context.Context,
	tool string,
	workspace string,
	args map[string]any,
) (map[string]any, error) {
	source.calls++
	source.tool = tool
	source.workspace = workspace
	source.args = args
	return map[string]any{"workspace": workspace, "path": args["path"]}, nil
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
	tool, err := NewRemoteWorkspaceTool(local, remote)
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

func TestRemoteWorkspaceReadToolPreservesLineModeForPathOnlyCall(t *testing.T) {
	remote := &remoteWorkspaceReadSource{}
	local := fstools.NewReadFileLinesTool(t.TempDir(), false, fstools.MaxReadFileSize)
	tool, err := NewRemoteWorkspaceTool(local, remote)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(context.Background(), map[string]any{
		"path": "README.md", "workspace": "vpn",
	})
	if result.IsError || remote.calls != 1 || remote.args["start_line"] != float64(1) {
		t.Fatalf("remote line read result = %#v; source = %#v", result, remote)
	}
	if _, sentOffset := remote.args["offset"]; sentOffset {
		t.Fatalf("line-mode call gained byte offset: %#v", remote.args)
	}
}

func TestRemoteWorkspaceReadToolDoesNotFallbackUnknownAlias(t *testing.T) {
	local := &remoteWorkspaceLocalTool{}
	remote := &remoteWorkspaceReadSource{}
	tool, err := NewRemoteWorkspaceTool(local, remote)
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

func TestRemoteWorkspaceMutationToolIsExplicitAndNeverLeaksRemotePreconditionLocally(t *testing.T) {
	local := &remoteWorkspaceLocalTool{name: "write_file"}
	remote := &remoteWorkspaceReadSource{}
	tool, err := NewRemoteWorkspaceTool(local, remote)
	if err != nil {
		t.Fatal(err)
	}
	properties := tool.Parameters()["properties"].(map[string]any)
	if properties["workspace"] == nil || properties["expected_sha256"] == nil ||
		tool.ToolLoopSemantics() != loopguard.SemanticsMutating {
		t.Fatalf("remote write schema = %#v", properties)
	}
	localResult := tool.Execute(context.Background(), map[string]any{
		"path": "a.txt", "expected_sha256": strings.Repeat("a", 64),
	})
	if !localResult.IsError || local.calls != 0 || remote.calls != 0 {
		t.Fatalf("local precondition result = %#v, local=%d remote=%d", localResult, local.calls, remote.calls)
	}
	remoteResult := tool.Execute(context.Background(), map[string]any{
		"path": "a.txt", "content": "new", "workspace": "vpn",
	})
	if remoteResult.IsError || remote.calls != 1 || remote.args["overwrite"] != false {
		t.Fatalf("remote write = %#v, source = %#v", remoteResult, remote)
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
