//go:build (linux || darwin) && integration

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type codingRemoteLostResponseClient struct {
	delegate    codingremote.BrokerClient
	dropNext    bool
	invokeCalls int
}

func (client *codingRemoteLostResponseClient) Discover(
	ctx context.Context,
	request codingremote.Request,
) (codingremote.CapabilitySnapshot, error) {
	return client.delegate.Discover(ctx, request)
}

func (client *codingRemoteLostResponseClient) Execute(
	ctx context.Context,
	request codingremote.Request,
) (codingremote.CapabilityResult, error) {
	result, err := client.delegate.Execute(ctx, request)
	if request.Operation == codingremote.OperationCapabilityInvoke {
		client.invokeCalls++
		if client.dropNext && err == nil {
			client.dropNext = false
			return codingremote.CapabilityResult{}, errors.New("simulated IPC response loss")
		}
	}
	return result, err
}

func TestCodingRemoteCapabilityVerticalSliceRealProcess(t *testing.T) {
	gatewayWorkspace := t.TempDir()
	remoteRoot := canonicalVerticalSlicePath(t, filepath.Join(t.TempDir(), "project"))
	if err := os.Mkdir(remoteRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteRoot, "README.md"), []byte("remote-original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "workspace-fixture")
	writeRemoteWorkspaceFixture(t, fixture)
	cfg := remoteWorkspaceVerticalSliceConfig(gatewayWorkspace)
	cfg.Gateway.CodingRemote.Enabled = true
	cfg.Execution.CodingRemoteCapabilities = map[string]config.CodingRemoteCapability{
		"build-workspace": {
			Revision: "capability-v1", Kind: config.CodingRemoteCapabilityWorkspace,
			RemoteWorkspace: remoteWorkspaceAlias,
			Operations: []string{
				"read_file", "write_file", "workspace_exec",
				"job_status", "job_logs", "job_artifacts", "job_cancel",
			},
		},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileMutate},
			Capabilities:  []string{"build-workspace"},
		},
	}

	registry, admission, runtimeState := newNodeJobVerticalSliceRuntime(t, gatewayWorkspace)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	defer closeNodeJobVerticalSliceRuntime(t, runtimeState)
	binaryPath := buildVerticalSliceCompanion(t, t.TempDir())
	companionConfig := remoteWorkspaceVerticalSliceCompanionConfig(
		t,
		server,
		filepath.Join(t.TempDir(), "state"),
		remoteRoot,
		fixture,
	)
	configPath := filepath.Join(t.TempDir(), "companion.json")
	writeVerticalSliceConfig(t, configPath, companionConfig)
	process := startVerticalSliceCompanion(t, binaryPath, configPath)
	defer process.stop(t)
	pending := waitForVerticalSliceNodeState(t, registry, nodes.StatePendingPairing)
	if _, err := registry.Approve(pending.ID, nodes.PairingApproval{
		Aliases: []nodes.Alias{remoteWorkspaceTarget},
		AllowedCommands: []string{
			nodes.WorkspaceCommandRead,
			nodes.WorkspaceCommandWrite,
			"system.exec.v1",
			nodes.JobCommandStart,
			nodes.JobCommandStatus,
			nodes.JobCommandLogs,
			nodes.JobCommandArtifacts,
			nodes.JobCommandCancel,
		},
		At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)

	socketDirectory, err := os.MkdirTemp("/tmp", "mintclaw-cr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupErr := os.RemoveAll(socketDirectory); cleanupErr != nil {
			t.Errorf("remove broker directory: %v", cleanupErr)
		}
	})
	socketDirectory, err = filepath.EvalSymlinks(socketDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(socketDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDirectory, "broker.sock")
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: time.Now,
		source: func(current *config.Config) (tools.NodeInvocationSource, error) {
			return newNodeInvocationSource(current, runtimeState)
		},
	}
	broker, err := codingremote.StartServer(t.Context(), socketPath, handler)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if closeErr := broker.Close(closeCtx); closeErr != nil {
			t.Errorf("close coding remote broker: %v", closeErr)
		}
	})
	client, err := codingremote.NewClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	projectKey := "directory:" + strings.Repeat("d", 64)
	discovery, err := client.Discover(t.Context(), codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request_discovery",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey, ProjectKey: projectKey,
		LocalProfile: codingscope.ProfileMutate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Capabilities) != 1 || discovery.Capabilities[0].Availability !=
		codingremote.AvailabilityAvailable {
		t.Fatalf("discovery = %#v", discovery)
	}
	remoteTool, err := tools.NewCodingRemoteCapabilityTool(
		client,
		tools.CodingRemoteToolAuthority{
			Grant: "local-development", GrantRevision: "grant-v1",
			ThreadID: threadID, SessionKey: sessionKey, ProjectKey: projectKey,
			LocalProfile: codingscope.ProfileMutate,
		},
		discovery,
	)
	if err != nil {
		t.Fatal(err)
	}
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-1",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	toolContext := func(callID string) context.Context {
		ctx := toolshared.WithRuntimeCapabilities(t.Context(), runtime)
		return toolshared.WithToolCallID(ctx, callID)
	}
	read := remoteTool.Execute(toolContext("provider-read-1"), map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "read_file",
		"input": map[string]any{"path": "README.md"},
	})
	readResult := decodeCodingRemoteCapabilityResult(t, read)
	if readResult.State != string(nodes.InvocationSucceeded) || readResult.InvocationID == "" ||
		!strings.Contains(string(readResult.Result), "remote-original") {
		t.Fatalf("remote read = %#v", readResult)
	}
	write := remoteTool.Execute(toolContext("provider-write-1"), map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "write_file",
		"input": map[string]any{
			"path": "created.txt", "content": "remote-created\n", "overwrite": false,
		},
	})
	writeResult := decodeCodingRemoteCapabilityResult(t, write)
	if writeResult.State != string(nodes.InvocationSucceeded) || len(writeResult.Changes) != 1 ||
		writeResult.Changes[0].Path != "created.txt" {
		t.Fatalf("remote write = %#v", writeResult)
	}
	foreground := remoteTool.Execute(toolContext("provider-foreground-1"), map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "workspace_exec",
		"input": map[string]any{
			"executable": "fixture", "args": []any{"foreground"}, "mode": "foreground",
			"timeout_seconds": float64(5),
		},
	})
	foregroundResult := decodeCodingRemoteCapabilityResult(t, foreground)
	foregroundPayload := decodeCodingRemotePayload(t, foregroundResult)
	if foregroundPayload["stdout"] != "foreground-ok\n" {
		t.Fatalf("remote foreground = %#v", foregroundResult)
	}
	jobStart := remoteTool.Execute(toolContext("provider-job-start-1"), map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "workspace_exec",
		"input": map[string]any{
			"executable": "fixture", "args": []any{"coding-job"}, "mode": "job",
			"timeout_seconds": float64(10),
			"artifacts":       []any{map[string]any{"name": "report", "path": "coding-artifact.txt"}},
		},
	})
	jobStartResult := decodeCodingRemoteCapabilityResult(t, jobStart)
	if jobStartResult.JobInvocationID != jobStartResult.InvocationID ||
		strings.Contains(string(jobStartResult.Result), `"job_id"`) {
		t.Fatalf("remote job start authority projection = %#v", jobStartResult)
	}
	jobStartStatus := remoteTool.Execute(toolContext("provider-job-start-status-1"), map[string]any{
		"action": "status", "capability": "build-workspace", "invocation_id": jobStartResult.InvocationID,
	})
	jobStartStatusResult := decodeCodingRemoteCapabilityResult(t, jobStartStatus)
	if jobStartStatusResult.JobInvocationID != jobStartResult.InvocationID ||
		strings.Contains(string(jobStartStatusResult.Result), `"job_id"`) {
		t.Fatalf("remote job start status authority projection = %#v", jobStartStatusResult)
	}
	jobStatusResult := waitForCodingRemoteJobState(
		t,
		remoteTool,
		toolContext,
		jobStartResult.JobInvocationID,
		"succeeded",
	)
	if jobStatusResult.InvocationID == jobStartResult.InvocationID {
		t.Fatal("job status reused the start invocation instead of a typed lifecycle invocation")
	}
	logs := remoteTool.Execute(toolContext("provider-job-logs-1"), map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "job_logs",
		"input": map[string]any{
			"job_invocation_id": jobStartResult.JobInvocationID,
			"stream":            "stdout", "cursor": float64(0), "limit_bytes": float64(4096),
		},
	})
	logsResult := decodeCodingRemoteCapabilityResult(t, logs)
	if payload := decodeCodingRemotePayload(t, logsResult); payload["data"] != "coding-stdout\n" {
		t.Fatalf("remote job logs = %#v", logsResult)
	}
	artifacts := remoteTool.Execute(toolContext("provider-job-artifacts-1"), map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "job_artifacts",
		"input": map[string]any{"job_invocation_id": jobStartResult.JobInvocationID},
	})
	artifactsResult := decodeCodingRemoteCapabilityResult(t, artifacts)
	artifactsPayload := decodeCodingRemotePayload(t, artifactsResult)
	artifactList, _ := artifactsPayload["artifacts"].([]any)
	if len(artifactList) != 1 {
		t.Fatalf("remote job artifacts = %#v", artifactsResult)
	}
	artifact, _ := artifactList[0].(map[string]any)
	if artifact["name"] != "report" || artifact["state"] != "available" || artifact["artifact_ref"] == "" ||
		strings.Contains(string(artifactsResult.Result), remoteRoot) ||
		strings.Contains(string(artifactsResult.Result), "coding-artifact.txt") {
		t.Fatalf("remote job artifact ownership projection = %#v", artifactsResult)
	}
	cancelStart := remoteTool.Execute(toolContext("provider-job-cancel-start-1"), map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "workspace_exec",
		"input": map[string]any{
			"executable": "fixture", "args": []any{"coding-cancel"}, "mode": "job",
			"timeout_seconds": float64(20),
		},
	})
	cancelStartResult := decodeCodingRemoteCapabilityResult(t, cancelStart)
	if cancelStartResult.JobInvocationID != cancelStartResult.InvocationID ||
		strings.Contains(string(cancelStartResult.Result), `"job_id"`) {
		t.Fatalf("remote cancel job start authority projection = %#v", cancelStartResult)
	}
	waitForNodeJobFile(t, filepath.Join(remoteRoot, "coding-cancel.started"))
	lostCancelClient := &codingRemoteLostResponseClient{delegate: client, dropNext: true}
	lostCancelTool, err := tools.NewCodingRemoteCapabilityTool(
		lostCancelClient,
		tools.CodingRemoteToolAuthority{
			Grant: "local-development", GrantRevision: "grant-v1",
			ThreadID: threadID, SessionKey: sessionKey, ProjectKey: projectKey,
			LocalProfile: codingscope.ProfileMutate,
		},
		discovery,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancel := lostCancelTool.Execute(toolContext("provider-job-cancel-1"), map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "job_cancel",
		"input": map[string]any{"job_invocation_id": cancelStartResult.JobInvocationID},
	})
	if cancel == nil || !cancel.IsError || lostCancelClient.invokeCalls != 1 {
		t.Fatalf("lost cancel response = %#v; invokes=%d", cancel, lostCancelClient.invokeCalls)
	}
	var uncertainCancel codingremote.CapabilityResult
	if err = json.Unmarshal([]byte(cancel.ContentForLLM()), &uncertainCancel); err != nil ||
		uncertainCancel.State != "unknown" || uncertainCancel.InvocationID == "" {
		t.Fatalf("lost cancel projection = %#v, %v", uncertainCancel, err)
	}
	cancelStatus := lostCancelTool.Execute(toolContext("provider-job-cancel-status-1"), map[string]any{
		"action": "status", "capability": "build-workspace", "invocation_id": uncertainCancel.InvocationID,
	})
	cancelStatusResult := decodeCodingRemoteCapabilityResult(t, cancelStatus)
	if cancelStatusResult.State != string(nodes.InvocationSucceeded) || lostCancelClient.invokeCalls != 1 {
		t.Fatalf("lost cancel recovery = %#v; invokes=%d", cancelStatusResult, lostCancelClient.invokeCalls)
	}
	waitForCodingRemoteJobState(
		t,
		remoteTool,
		toolContext,
		cancelStartResult.JobInvocationID,
		"canceled",
	)
	_, err = client.Execute(t.Context(), codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request_wrong_operation_status",
		Operation: codingremote.OperationInvocationStatus,
		Grant:     "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey, ProjectKey: projectKey,
		LocalProfile: codingscope.ProfileMutate,
		Principal:    &principal, CallID: "call_wrong_operation_status",
		DiscoveryRevision: discovery.DiscoveryRevision, Capability: "build-workspace",
		CapabilityRevision: "capability-v1", CapabilityOperation: "read_file",
		InvocationID:   writeResult.InvocationID,
		DeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(),
	})
	var brokerErr *codingremote.BrokerError
	if !errors.As(err, &brokerErr) || brokerErr.Code != "INVOCATION_DENIED" {
		t.Fatalf("cross-operation status error = %v", err)
	}
	status := remoteTool.Execute(toolContext("provider-status-1"), map[string]any{
		"action": "status", "capability": "build-workspace",
		"invocation_id": writeResult.InvocationID,
	})
	statusResult := decodeCodingRemoteCapabilityResult(t, status)
	if statusResult.InvocationID != writeResult.InvocationID ||
		statusResult.State != string(nodes.InvocationSucceeded) {
		t.Fatalf("remote status = %#v", statusResult)
	}
	lostClient := &codingRemoteLostResponseClient{delegate: client, dropNext: true}
	lostTool, err := tools.NewCodingRemoteCapabilityTool(
		lostClient,
		tools.CodingRemoteToolAuthority{
			Grant: "local-development", GrantRevision: "grant-v1",
			ThreadID: threadID, SessionKey: sessionKey, ProjectKey: projectKey,
			LocalProfile: codingscope.ProfileMutate,
		},
		discovery,
	)
	if err != nil {
		t.Fatal(err)
	}
	lost := lostTool.Execute(toolContext("provider-write-lost-response"), map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "write_file",
		"input": map[string]any{
			"path": "lost-response.txt", "content": "accepted-once\n", "overwrite": false,
		},
	})
	if lost == nil || !lost.IsError {
		t.Fatalf("lost response result = %#v", lost)
	}
	var lostResult codingremote.CapabilityResult
	if err = json.Unmarshal([]byte(lost.ContentForLLM()), &lostResult); err != nil ||
		lostResult.State != "unknown" || lostResult.InvocationID == "" ||
		!strings.Contains(lostResult.RecoveryAction, "do not replay") || lostClient.invokeCalls != 1 {
		t.Fatalf("lost response projection = %#v; err=%v; invokes=%d", lostResult, err, lostClient.invokeCalls)
	}
	lostStatus := lostTool.Execute(toolContext("provider-status-lost-response"), map[string]any{
		"action": "status", "capability": "build-workspace",
		"invocation_id": lostResult.InvocationID,
	})
	lostStatusResult := decodeCodingRemoteCapabilityResult(t, lostStatus)
	if lostStatusResult.InvocationID != lostResult.InvocationID ||
		lostStatusResult.State != string(nodes.InvocationSucceeded) || lostClient.invokeCalls != 1 {
		t.Fatalf("lost response recovery = %#v; invokes=%d", lostStatusResult, lostClient.invokeCalls)
	}
	content, err := os.ReadFile(filepath.Join(remoteRoot, "created.txt"))
	if err != nil || string(content) != "remote-created\n" {
		t.Fatalf("remote mutation content = %q, %v", content, err)
	}
	if _, err = os.Stat(filepath.Join(gatewayWorkspace, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("remote mutation appeared in gateway workspace: %v", err)
	}
	lostContent, err := os.ReadFile(filepath.Join(remoteRoot, "lost-response.txt"))
	if err != nil || string(lostContent) != "accepted-once\n" {
		t.Fatalf("lost-response mutation content = %q, %v", lostContent, err)
	}
}

func decodeCodingRemotePayload(
	t *testing.T,
	result codingremote.CapabilityResult,
) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(result.Result, &payload); err != nil {
		t.Fatalf("decode coding remote payload: %v; result=%#v", err, result)
	}
	return payload
}

func waitForCodingRemoteJobState(
	t *testing.T,
	remoteTool *tools.CodingRemoteCapabilityTool,
	toolContext func(string) context.Context,
	jobInvocationID string,
	want string,
) codingremote.CapabilityResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		status := remoteTool.Execute(
			toolContext(fmt.Sprintf("provider-job-status-%s-%d", want, attempt)),
			map[string]any{
				"action": "invoke", "capability": "build-workspace", "operation": "job_status",
				"input": map[string]any{"job_invocation_id": jobInvocationID},
			},
		)
		result := decodeCodingRemoteCapabilityResult(t, status)
		if decodeCodingRemotePayload(t, result)["state"] == want {
			return result
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("remote job %s did not reach state %q", jobInvocationID, want)
	return codingremote.CapabilityResult{}
}

func decodeCodingRemoteCapabilityResult(
	t *testing.T,
	result *toolshared.ToolResult,
) codingremote.CapabilityResult {
	t.Helper()
	if result == nil || result.IsError {
		t.Fatalf("remote capability result = %#v", result)
	}
	var decoded codingremote.CapabilityResult
	if err := json.Unmarshal([]byte(result.ContentForLLM()), &decoded); err != nil {
		t.Fatalf("decode remote capability result: %v; content=%q", err, result.ContentForLLM())
	}
	return decoded
}
