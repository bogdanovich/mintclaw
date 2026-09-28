package tools

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestWorkspaceExecForegroundBindsWorkspaceAuthority(t *testing.T) {
	cfg, source := workspaceExecTestSetup(t, false)
	tool, err := NewWorkspaceExecTool(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	ctx := nodeInvocationTestContext("owner", "workspace-exec-foreground")
	result := tool.Execute(ctx, map[string]any{
		"remote_workspace": "project", "executable": "go", "args": []any{"test", "./pkg/..."},
		"env": map[string]any{"PATH": "/usr/bin"}, "mode": "foreground", "timeout_seconds": float64(45),
	})
	payload := decodeNodeResult(t, result)
	if payload["placement"] != "remote" || payload["remote_workspace"] != "project" ||
		payload["remote_workspace_revision"] != "project-workspace-v1" || payload["target"] != "build" ||
		payload["mode"] != "foreground" || source.prepareCalls != 1 || source.dispatchCalls != 1 {
		t.Fatalf(
			"workspace exec result = %#v; prepare=%d dispatch=%d",
			payload,
			source.prepareCalls,
			source.dispatchCalls,
		)
	}
	prepared := mustFakeGatewayInvocation(t, source, ctx, payload["invocation_id"].(string))
	if prepared.Plan.Command != "system.exec.v1" || prepared.Plan.TimeoutSeconds != 45 {
		t.Fatalf("workspace exec plan = %#v", prepared.Plan)
	}
	var input map[string]any
	if err := json.Unmarshal(prepared.Plan.Input, &input); err != nil {
		t.Fatal(err)
	}
	argv := input["argv"].([]any)
	if input["cwd"] != "project" || len(argv) != 3 || argv[0] != "go" || argv[2] != "./pkg/..." {
		t.Fatalf("workspace exec input = %#v", input)
	}
}

func TestWorkspaceExecBoundsOmittedTimeoutToCommandMaximum(t *testing.T) {
	cfg, source := workspaceExecTestSetup(t, false)
	workspaceExecSetTimeoutMaximum(source, "system.exec.v1", 12)
	tool, err := NewWorkspaceExecTool(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	ctx := nodeInvocationTestContext("owner", "workspace-exec-short-timeout")
	args := map[string]any{
		"remote_workspace": "project", "executable": "go", "args": []any{"version"}, "mode": "foreground",
	}
	approval, err := tool.ApprovalArguments(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if approval["timeout_seconds"] != float64(12) {
		t.Fatalf("effective approval timeout = %#v", approval["timeout_seconds"])
	}
	result := tool.Execute(ctx, args)
	payload := decodeNodeResult(t, result)
	prepared := mustFakeGatewayInvocation(t, source, ctx, payload["invocation_id"].(string))
	if prepared.Plan.TimeoutSeconds != 12 {
		t.Fatalf("effective execution timeout = %d", prepared.Plan.TimeoutSeconds)
	}
	var input map[string]any
	if err := json.Unmarshal(prepared.Plan.Input, &input); err != nil {
		t.Fatal(err)
	}
	if input["timeout_seconds"] != float64(12) {
		t.Fatalf("effective command timeout = %#v", input["timeout_seconds"])
	}
	explicit := cloneToolArguments(args)
	explicit["timeout_seconds"] = float64(30)
	denied := tool.Execute(nodeInvocationTestContext("owner", "workspace-exec-long-timeout"), explicit)
	if !denied.IsError || source.dispatchCalls != 1 {
		t.Fatalf("out-of-range explicit timeout = %#v; dispatch=%d", denied, source.dispatchCalls)
	}
}

func TestWorkspaceExecResolvesGenerationBoundSourcePerCall(t *testing.T) {
	cfg, stale := workspaceExecTestSetup(t, false)
	_, current := workspaceExecTestSetup(t, false)
	tool, err := NewWorkspaceExecTool(cfg, stale, "main")
	if err != nil {
		t.Fatal(err)
	}
	factoryCalls := 0
	tool.SetInvocationSourceFactory(func() (NodeInvocationSource, error) {
		factoryCalls++
		return current, nil
	})
	workspace := cfg.Execution.RemoteWorkspaces["project"]
	workspace.Target = "replacement"
	workspace.WorkingScope = "widened"
	workspace.Revision = "mutated-v2"
	cfg.Execution.RemoteWorkspaces["project"] = workspace
	cfg.Execution.Targets["build"] = config.ExecutionTarget{Type: "node", Node: "replacement-node"}
	cfg.Agents.Defaults.TargetPolicy.AllowedTargets = []string{"replacement"}
	result := tool.Execute(nodeInvocationTestContext("owner", "workspace-exec-fresh-source"), map[string]any{
		"remote_workspace": "project", "executable": "go", "args": []any{"version"}, "mode": "foreground",
	})
	payload := decodeNodeResult(t, result)
	if result.IsError || payload["target"] != "build" ||
		payload["remote_workspace_revision"] != "project-workspace-v1" ||
		factoryCalls != 1 || current.dispatchCalls != 1 || stale.dispatchCalls != 0 {
		t.Fatalf(
			"fresh source result = %#v; factory=%d current=%d stale=%d",
			result,
			factoryCalls,
			current.dispatchCalls,
			stale.dispatchCalls,
		)
	}
}

func TestWorkspaceExecJobUsesExistingP5aStart(t *testing.T) {
	cfg, source := workspaceExecTestSetup(t, true)
	source.dispatchResult = json.RawMessage(`{"job_id":"job_123","state":"running"}`)
	tool, err := NewWorkspaceExecTool(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	ctx := nodeInvocationTestContext("owner", "workspace-exec-job")
	result := tool.Execute(ctx, map[string]any{
		"remote_workspace": "project", "executable": "go", "args": []any{"test", "./..."},
		"mode": "job", "timeout_seconds": float64(600),
		"artifacts": []any{map[string]any{"name": "report", "path": "out/report.json"}},
	})
	payload := decodeNodeResult(t, result)
	jobInvocationID, _ := payload[remoteWorkspaceJobInvocationArgument].(string)
	projected, _ := json.Marshal(payload)
	if payload["mode"] != "job" || jobInvocationID == "" ||
		jobInvocationID != payload["invocation_id"] || strings.Contains(string(projected), `"job_id"`) ||
		source.dispatchCalls != 1 {
		t.Fatalf("workspace job result = %#v; dispatch=%d", payload, source.dispatchCalls)
	}
	prepared := mustFakeGatewayInvocation(t, source, ctx, payload["invocation_id"].(string))
	if prepared.Plan.Command != nodes.JobCommandStart || prepared.Plan.JobProfile != "builds" ||
		prepared.Plan.TimeoutSeconds != defaultWorkspaceExecTimeout {
		t.Fatalf("workspace job plan = %#v", prepared.Plan)
	}
	var input map[string]any
	if err := json.Unmarshal(prepared.Plan.Input, &input); err != nil {
		t.Fatal(err)
	}
	if input["timeout_seconds"] != float64(600) || len(input["artifacts"].([]any)) != 1 {
		t.Fatalf("workspace job input = %#v", input)
	}
}

func TestRemoteWorkspaceJobLifecycleBindsProducingInvocation(t *testing.T) {
	const jobID = "job_0123456789abcdef0123456789abcdef"
	cfg, source := workspaceExecTestSetup(t, true)
	startRouter, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "workspace_exec")
	if err != nil {
		t.Fatal(err)
	}
	described, err := startRouter.DescribeRemoteWorkspaceExec("project")
	if err != nil || !slices.Equal(described.Modes, []string{"foreground", "job"}) {
		t.Fatalf("workspace exec description = %#v, %v", described, err)
	}
	startCtx := nodeInvocationTestContext("owner", "coding-job-start")
	source.dispatchResult = json.RawMessage(`{"job_id":"` + jobID + `","state":"running"}`)
	start := startRouter.ExecuteRemoteWorkspaceExec(startCtx, "project", map[string]any{
		"remote_workspace": "project", "executable": "go", "args": []any{"test", "./..."}, "mode": "job",
	})
	startResult := decodeNodeResult(t, start)
	startInvocationID, _ := startResult["invocation_id"].(string)
	projectedStart, _ := json.Marshal(startResult)
	if startInvocationID == "" || startResult[remoteWorkspaceJobInvocationArgument] != startInvocationID ||
		strings.Contains(string(projectedStart), `"job_id"`) {
		t.Fatalf("workspace job start = %#v", startResult)
	}
	source.remote = nodes.InvocationRecord{
		InvocationID: startInvocationID, Command: nodes.JobCommandStart,
		State: nodes.InvocationSucceeded, Result: json.RawMessage(`{"job_id":"` + jobID + `","state":"running"}`),
	}
	source.dispatchResult = json.RawMessage(`{"job_id":"` + jobID + `","state":"running"}`)
	statusRouter, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "job_status")
	if err != nil {
		t.Fatal(err)
	}
	described, err = statusRouter.DescribeRemoteWorkspaceJob("project", "job_status")
	if err != nil || described.Risk != nodes.RiskRead {
		t.Fatalf("job status description = %#v, %v", described, err)
	}
	statusCtx := nodeInvocationTestContext("owner", "coding-job-status")
	status := statusRouter.ExecuteRemoteWorkspaceJob(
		statusCtx,
		startCtx,
		"project",
		"job_status",
		map[string]any{remoteWorkspaceJobInvocationArgument: "remote_capability_job_start"},
	)
	statusResult := decodeNodeResult(t, status)
	projectedStatus, _ := json.Marshal(statusResult)
	if statusResult["operation"] != "job_status" ||
		statusResult[remoteWorkspaceJobInvocationArgument] != "remote_capability_job_start" ||
		strings.Contains(string(projectedStatus), `"job_id"`) {
		t.Fatalf("job status result = %#v", statusResult)
	}
	prepared := mustFakeGatewayInvocation(t, source, statusCtx, statusResult["invocation_id"].(string))
	if prepared.Plan.Command != nodes.JobCommandStatus || prepared.Plan.JobProfile != "builds" {
		t.Fatalf("job status plan = %#v", prepared.Plan)
	}
	var input map[string]any
	if err = json.Unmarshal(prepared.Plan.Input, &input); err != nil || input["job_id"] != jobID {
		t.Fatalf("job status input = %#v, %v", input, err)
	}
	denied := statusRouter.ExecuteRemoteWorkspaceJob(
		nodeInvocationTestContext("owner", "coding-job-cross-owner"),
		nodeInvocationTestContext("other-owner", "coding-job-start"),
		"project",
		"job_status",
		map[string]any{remoteWorkspaceJobInvocationArgument: "remote_capability_job_start"},
	)
	if !denied.IsError || source.prepareCalls != 2 || source.dispatchCalls != 2 {
		t.Fatalf(
			"cross-owner job status = %#v; prepare=%d dispatch=%d",
			denied,
			source.prepareCalls,
			source.dispatchCalls,
		)
	}
}

func TestRemoteWorkspaceJobLifecycleRejectsRemappedStartAuthority(t *testing.T) {
	const jobID = "job_0123456789abcdef0123456789abcdef"
	tests := []struct {
		name  string
		remap func(*testing.T, *config.Config, *fakeNodeInvocationSource)
	}{
		{
			name: "node",
			remap: func(t *testing.T, cfg *config.Config, source *fakeNodeInvocationSource) {
				t.Helper()
				snapshot := source.byRef["builder-node"]
				snapshot.ID = "replacement-private-node-id"
				source.byRef["replacement-node"] = snapshot
				registration := source.registrations["private-node-id"]
				registration.Snapshot = snapshot
				source.registrations[snapshot.ID] = registration
				source.connected[snapshot.ID] = true
				binding := cfg.Execution.Targets["build"]
				binding.Node = "replacement-node"
				cfg.Execution.Targets["build"] = binding
			},
		},
		{
			name: "job profile",
			remap: func(t *testing.T, cfg *config.Config, source *fakeNodeInvocationSource) {
				t.Helper()
				binding := cfg.Execution.Targets["build"]
				binding.JobProfile = "tests"
				cfg.Execution.Targets["build"] = binding
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, source := workspaceExecTestSetupWithJobProfiles(t, "builds", "tests")
			startRouter, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "workspace_exec")
			if err != nil {
				t.Fatal(err)
			}
			startCtx := nodeInvocationTestContext("owner", "coding-job-remap-start")
			source.dispatchResult = json.RawMessage(`{"job_id":"` + jobID + `","state":"running"}`)
			start := startRouter.ExecuteRemoteWorkspaceExec(startCtx, "project", map[string]any{
				"remote_workspace": "project", "executable": "go", "args": []any{"test", "./..."},
				"mode": "job",
			})
			startResult := decodeNodeResult(t, start)
			startInvocationID, _ := startResult["invocation_id"].(string)
			if startInvocationID == "" {
				t.Fatalf("workspace job start = %#v", startResult)
			}
			source.remote = nodes.InvocationRecord{
				InvocationID: startInvocationID, Command: nodes.JobCommandStart,
				State: nodes.InvocationSucceeded, Result: json.RawMessage(`{"job_id":"` + jobID + `"}`),
			}
			test.remap(t, cfg, source)
			statusRouter, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "job_status")
			if err != nil {
				t.Fatal(err)
			}
			status := statusRouter.ExecuteRemoteWorkspaceJob(
				nodeInvocationTestContext("owner", "coding-job-remap-status"),
				startCtx,
				"project",
				"job_status",
				map[string]any{remoteWorkspaceJobInvocationArgument: "remote_capability_job_start"},
			)
			if !status.IsError || source.prepareCalls != 1 || source.dispatchCalls != 1 {
				t.Fatalf(
					"remapped lifecycle status = %#v; prepare=%d dispatch=%d",
					status,
					source.prepareCalls,
					source.dispatchCalls,
				)
			}
		})
	}
}

func TestRemoteWorkspaceJobDiscoveryOmitsPerCallApproval(t *testing.T) {
	cfg, source := workspaceExecTestSetup(t, true)
	workspaceExecRequireJobApproval(t, source, nodes.JobCommandStart)
	workspaceExecRequireJobApproval(t, source, nodes.JobCommandCancel)
	startRouter, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "workspace_exec")
	if err != nil {
		t.Fatal(err)
	}
	described, err := startRouter.DescribeRemoteWorkspaceExec("project")
	if err != nil || !slices.Equal(described.Modes, []string{"foreground"}) {
		t.Fatalf("approval-bound start description = %#v, %v", described, err)
	}
	cancelRouter, err := NewRemoteWorkspaceNodeRouter(cfg, source, "main", "job_cancel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cancelRouter.DescribeRemoteWorkspaceJob("project", "job_cancel"); err == nil {
		t.Fatal("approval-bound job cancellation was advertised")
	}
}

func TestWorkspaceExecJobRequiresSeparateJobsGrant(t *testing.T) {
	cfg, source := workspaceExecTestSetup(t, false)
	tool, err := NewWorkspaceExecTool(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(nodeInvocationTestContext("owner", "workspace-exec-no-jobs"), map[string]any{
		"remote_workspace": "project", "executable": "go", "args": []any{}, "mode": "job",
	})
	if !result.IsError || source.prepareCalls != 0 || source.dispatchCalls != 0 {
		t.Fatalf(
			"ungranted workspace job = %#v; prepare=%d dispatch=%d",
			result,
			source.prepareCalls,
			source.dispatchCalls,
		)
	}
}

func TestWorkspaceExecApprovalCannotResumeAfterWorkspaceRevisionChange(t *testing.T) {
	cfg, source := workspaceExecTestSetup(t, false)
	workspaceExecSetApprovalMode(source, "system.exec.v1", "each_command")
	tool, err := NewWorkspaceExecTool(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	ctx := nodeInvocationTestContext("owner", "workspace-exec-approval")
	args := map[string]any{
		"remote_workspace": "project", "executable": "go", "args": []any{"version"}, "mode": "foreground",
	}
	approval, err := tool.ApprovalArguments(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if approval["remote_workspace_revision"] != "project-workspace-v1" || source.prepareCalls != 1 {
		t.Fatalf("workspace approval = %#v; prepare=%d", approval, source.prepareCalls)
	}

	workspace := cfg.Execution.RemoteWorkspaces["project"]
	workspace.Revision = "project-workspace-v2"
	cfg.Execution.RemoteWorkspaces["project"] = workspace
	changed, err := NewWorkspaceExecTool(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	result := changed.Execute(toolshared.WithToolApprovalContinuation(ctx, true), args)
	if !result.IsError || source.prepareCalls != 1 || source.dispatchCalls != 0 {
		t.Fatalf(
			"changed workspace continuation = %#v; prepare=%d dispatch=%d",
			result,
			source.prepareCalls,
			source.dispatchCalls,
		)
	}
	retry := changed.Execute(ctx, args)
	if !retry.IsError || source.prepareCalls != 1 || source.dispatchCalls != 0 {
		t.Fatalf(
			"changed workspace retry = %#v; prepare=%d dispatch=%d",
			retry,
			source.prepareCalls,
			source.dispatchCalls,
		)
	}
}

func TestWorkspaceExecReportsPostDispatchUncertaintyWithoutReplay(t *testing.T) {
	cfg, source := workspaceExecTestSetup(t, false)
	source.dispatchErr = errTestTransportClosed
	tool, err := NewWorkspaceExecTool(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	ctx := nodeInvocationTestContext("owner", "workspace-exec-unknown")
	args := map[string]any{
		"remote_workspace": "project", "executable": "go", "args": []any{"version"}, "mode": "foreground",
	}
	first := tool.Execute(ctx, args)
	second := tool.Execute(ctx, args)
	if !first.IsError || !strings.Contains(first.ContentForLLM(), "DISPATCH_UNCERTAIN") ||
		!second.IsError || source.prepareCalls != 1 || source.dispatchCalls != 1 {
		t.Fatalf(
			"uncertain results = %#v / %#v; prepare=%d dispatch=%d",
			first,
			second,
			source.prepareCalls,
			source.dispatchCalls,
		)
	}
}

func TestToolLogArgumentsRedactsWorkspaceExecContent(t *testing.T) {
	const secret = "workspace-exec-secret"
	got := ToolLogArguments("workspace_exec", map[string]any{
		"remote_workspace": "private", "executable": "go", "args": []any{"--token=" + secret},
		"env": map[string]any{"TOKEN": secret}, "mode": "job",
	})
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if got["redacted"] != true || strings.Contains(string(encoded), secret) ||
		strings.Contains(string(encoded), "private") {
		t.Fatalf("workspace exec log arguments = %s", encoded)
	}
}

func TestWorkspaceExecSchemaDisambiguatesAgentProfiles(t *testing.T) {
	cfg, source := workspaceExecTestSetup(t, false)
	tool, err := NewWorkspaceExecTool(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	properties := tool.Parameters()["properties"].(map[string]any)
	if properties["remote_workspace"] == nil || properties["workspace"] != nil {
		t.Fatalf("workspace exec schema = %#v", properties)
	}
	if !strings.Contains(tool.Description(), "not a MintClaw agent profile") {
		t.Fatalf("workspace exec description = %q", tool.Description())
	}
	result := tool.Execute(nodeInvocationTestContext("owner", "workspace-exec-legacy-selector"), map[string]any{
		"workspace": "project", "executable": "go", "args": []any{"version"}, "mode": "foreground",
	})
	if !result.IsError || source.prepareCalls != 0 || source.dispatchCalls != 0 {
		t.Fatalf(
			"legacy selector result = %#v; prepare=%d dispatch=%d",
			result,
			source.prepareCalls,
			source.dispatchCalls,
		)
	}
}

func TestWorkspaceExecEventsExposePlacementWithoutArguments(t *testing.T) {
	const secret = "workspace-event-secret"
	cfg, source := workspaceExecTestSetup(t, false)
	eventBus := &recordingNodeEventBus{}
	tool, err := NewWorkspaceExecTool(cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	tool.SetEventPublisher(eventBus)
	result := tool.Execute(nodeInvocationTestContext("owner", "workspace-exec-event"), map[string]any{
		"remote_workspace": "project", "executable": "go", "args": []any{"--token=" + secret},
		"env": map[string]any{"PATH": secret}, "mode": "foreground",
	})
	if result.IsError {
		t.Fatalf("workspace exec result = %#v", result)
	}
	events := eventBus.snapshot()
	if len(events) != 3 {
		t.Fatalf("workspace exec events = %#v", events)
	}
	for _, event := range events {
		payload, ok := event.Payload.(NodeInvocationEventPayload)
		if !ok || event.Source.Name != "workspace_exec" || payload.Workspace != "project" ||
			payload.WorkspaceRevision != "project-workspace-v1" || payload.Target != "build" {
			t.Fatalf("workspace exec event = %#v", event)
		}
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "--token") {
			t.Fatalf("workspace exec event leaked arguments: %s", encoded)
		}
	}
}

var errTestTransportClosed = &workspaceExecTestError{"transport closed"}

type workspaceExecTestError struct{ message string }

func (err *workspaceExecTestError) Error() string { return err.message }

func workspaceExecTestSetup(t *testing.T, allowJobs bool) (*config.Config, *fakeNodeInvocationSource) {
	return workspaceExecTestSetupWithOptions(t, allowJobs, "builds")
}

func workspaceExecTestSetupWithJobProfiles(
	t *testing.T,
	profileAliases ...string,
) (*config.Config, *fakeNodeInvocationSource) {
	return workspaceExecTestSetupWithOptions(t, true, profileAliases...)
}

func workspaceExecTestSetupWithOptions(
	t *testing.T,
	allowJobs bool,
	profileAliases ...string,
) (*config.Config, *fakeNodeInvocationSource) {
	t.Helper()
	system := workspaceExecSystemDescriptor(t)
	jobProfiles := make([]nodes.JobProfileDescriptor, 0, len(profileAliases))
	for _, alias := range profileAliases {
		jobProfile := nodeJobProjectionProfile(alias, alias+"-v1")
		jobProfile.WorkingScopes = []string{"project"}
		jobProfile.Approval.Start = "none"
		jobProfiles = append(jobProfiles, jobProfile)
	}
	jobDescriptors, err := nodes.JobCommandDescriptors(jobProfiles)
	if err != nil {
		t.Fatal(err)
	}
	commands := append([]nodes.CommandDescriptor{system}, jobDescriptors...)
	catalog := nodes.CapabilityCatalog{Commands: commands}
	catalogHash := mustCatalogHash(t, catalog)
	snapshot := nodes.Snapshot{
		ProtocolVersion: nodes.ProtocolVersion,
		ID:              "private-node-id", State: nodes.StateConnected, Catalog: catalog,
		CatalogHash: catalogHash, Executor: "local", PolicyRevision: "policy-v1",
	}
	discovery := &fakeNodeDiscoverySource{
		byRef: map[string]nodes.Snapshot{"builder-node": snapshot},
		registrations: map[nodes.ID]nodes.Registration{snapshot.ID: {
			Snapshot: snapshot, AllowedCommands: []string{
				system.Name,
				nodes.JobCommandStart,
				nodes.JobCommandStatus,
				nodes.JobCommandLogs,
				nodes.JobCommandArtifacts,
				nodes.JobCommandCancel,
			},
			ApprovedCatalogHash: catalogHash, ApprovedAt: 1,
		}},
		connected: map[nodes.ID]bool{snapshot.ID: true},
	}
	store, err := nodes.NewGatewayInvocationStore(
		filepath.Join(t.TempDir(), "state", "invocations.db"),
		1024*1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeNodeInvocationSource{fakeNodeDiscoverySource: discovery, store: store}
	cfg := config.DefaultConfig()
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"build": {Type: "node", Node: "builder-node", JobProfile: profileAliases[0]},
	}
	tools := []string{"workspace_exec"}
	if allowJobs {
		tools = append(tools, "jobs")
	}
	cfg.Execution.RemoteWorkspaces = map[string]config.RemoteWorkspace{
		"project": {
			Target: "build", WorkingScope: "project", Revision: "project-workspace-v1", Tools: tools,
		},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{AllowedTargets: []string{"build"}}
	return cfg, source
}

func workspaceExecSystemDescriptor(t *testing.T) nodes.CommandDescriptor {
	t.Helper()
	contract := &nodes.CommandModelContract{
		Availability: nodes.ModelAvailable, TimeoutSecondsMax: 120, OutputBytesMax: 4096, ResultKind: "json",
		AuthorityDigest: strings.Repeat("b", 64),
		Constraints: nodes.CommandModelConstraints{
			ExecutableAliases: []string{"go"}, WorkingScopes: []string{"project"},
			EnvironmentNames: []string{"PATH"},
		},
		Guidance: []string{}, Examples: []json.RawMessage{},
	}
	return nodes.CommandDescriptor{
		Name: "system.exec.v1",
		InputSchema: json.RawMessage(
			`{"type":"object","required":["argv","cwd","timeout_seconds","env"],"properties":{"argv":{"type":"array","minItems":1,"maxItems":128,"items":{"type":"string","minLength":1,"maxLength":4096}},"cwd":{"type":"string","minLength":1,"maxLength":4096},"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600},"env":{"type":"object","maxProperties":64,"additionalProperties":{"type":"string","maxLength":16384}}},"additionalProperties":false}`,
		),
		OutputSchema: json.RawMessage(`{"type":"object"}`), Risk: nodes.RiskWrite, ModelContract: contract,
	}
}

func workspaceExecSetApprovalMode(source *fakeNodeInvocationSource, command string, mode string) {
	snapshot := source.byRef["builder-node"]
	for index := range snapshot.Catalog.Commands {
		if snapshot.Catalog.Commands[index].Name == command {
			snapshot.Catalog.Commands[index].ModelContract.ApprovalMode = mode
		}
	}
	snapshot.CatalogHash, _ = snapshot.Catalog.Hash()
	source.byRef["builder-node"] = snapshot
	registration := source.registrations[snapshot.ID]
	registration.Snapshot = snapshot
	registration.ApprovedCatalogHash = snapshot.CatalogHash
	source.registrations[snapshot.ID] = registration
}

func workspaceExecRequireJobApproval(t *testing.T, source *fakeNodeInvocationSource, command string) {
	t.Helper()
	snapshot := source.byRef["builder-node"]
	for index := range snapshot.Catalog.Commands {
		descriptor := &snapshot.Catalog.Commands[index]
		if descriptor.Name != command || len(descriptor.JobProfiles) != 1 {
			continue
		}
		switch command {
		case nodes.JobCommandStart:
			descriptor.JobProfiles[0].Approval.Start = "required"
		case nodes.JobCommandCancel:
			descriptor.JobProfiles[0].Approval.Cancel = "required"
		default:
			descriptor.JobProfiles[0].Approval.Read = "required"
		}
		descriptor.ModelContract.ApprovalMode = "each_command"
	}
	var err error
	snapshot.CatalogHash, err = snapshot.Catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	source.byRef["builder-node"] = snapshot
	registration := source.registrations[snapshot.ID]
	registration.Snapshot = snapshot
	registration.ApprovedCatalogHash = snapshot.CatalogHash
	source.registrations[snapshot.ID] = registration
}

func workspaceExecSetTimeoutMaximum(source *fakeNodeInvocationSource, command string, maximum int) {
	snapshot := source.byRef["builder-node"]
	for index := range snapshot.Catalog.Commands {
		if snapshot.Catalog.Commands[index].Name == command {
			snapshot.Catalog.Commands[index].ModelContract.TimeoutSecondsMax = maximum
		}
	}
	snapshot.CatalogHash, _ = snapshot.Catalog.Hash()
	source.byRef["builder-node"] = snapshot
	registration := source.registrations[snapshot.ID]
	registration.Snapshot = snapshot
	registration.ApprovedCatalogHash = snapshot.CatalogHash
	source.registrations[snapshot.ID] = registration
}
