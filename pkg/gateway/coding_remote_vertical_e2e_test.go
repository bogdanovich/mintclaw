//go:build (linux || darwin) && integration

package gateway

import (
	"context"
	"encoding/json"
	"errors"
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
			Operations:      []string{"read_file", "write_file"},
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
