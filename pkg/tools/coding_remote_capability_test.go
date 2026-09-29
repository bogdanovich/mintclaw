package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type fakeCodingRemoteBroker struct {
	snapshot       codingremote.CapabilitySnapshot
	result         codingremote.CapabilityResult
	discoverErr    error
	executeErr     error
	discoverCalls  []codingremote.Request
	executionCalls []codingremote.Request
	executeFunc    func(codingremote.Request) (codingremote.CapabilityResult, error)
	artifact       codingremote.ArtifactResult
	artifactErr    error
	artifactCalls  []codingremote.Request
	artifactFunc   func(codingremote.Request) (codingremote.ArtifactResult, error)
}

func (broker *fakeCodingRemoteBroker) Artifact(
	_ context.Context,
	request codingremote.Request,
) (codingremote.ArtifactResult, error) {
	broker.artifactCalls = append(broker.artifactCalls, request)
	if broker.artifactFunc != nil {
		return broker.artifactFunc(request)
	}
	return broker.artifact, broker.artifactErr
}

type fakeCodingRemoteArtifactStore struct {
	data  []byte
	meta  CodingRemoteArtifactMetadata
	calls int
}

func (store *fakeCodingRemoteArtifactStore) ImportRemoteArtifact(
	_ context.Context,
	path string,
	meta CodingRemoteArtifactMetadata,
) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	store.calls++
	store.data = data
	store.meta = meta
	return "media://coding-attachment/imported", nil
}

func (broker *fakeCodingRemoteBroker) Discover(
	_ context.Context,
	request codingremote.Request,
) (codingremote.CapabilitySnapshot, error) {
	broker.discoverCalls = append(broker.discoverCalls, request)
	return broker.snapshot, broker.discoverErr
}

func (broker *fakeCodingRemoteBroker) Execute(
	_ context.Context,
	request codingremote.Request,
) (codingremote.CapabilityResult, error) {
	broker.executionCalls = append(broker.executionCalls, request)
	if broker.executeFunc != nil {
		return broker.executeFunc(request)
	}
	result := broker.result
	if request.InvocationID != "" {
		result.InvocationID = request.InvocationID
	}
	return result, broker.executeErr
}

func TestCodingRemoteCapabilityToolReportsConfiguredBrokerOutage(t *testing.T) {
	threadID := uuid.NewString()
	broker := &fakeCodingRemoteBroker{discoverErr: errors.New("socket unavailable")}
	tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: "coding:" + threadID,
		ProjectKey: "directory:" + strings.Repeat("a", 64), LocalProfile: codingscope.ProfileMutate,
	}, codingremote.CapabilitySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	parameters := tool.Parameters()
	properties := parameters["properties"].(map[string]any)
	if _, exists := properties["capability"].(map[string]any)["enum"]; exists {
		t.Fatalf("unavailable tool published an empty capability enum: %#v", parameters)
	}
	result := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if result == nil || !result.IsError || !strings.Contains(result.ContentForLLM(), "BROKER_UNAVAILABLE") ||
		len(broker.discoverCalls) != 1 || broker.discoverCalls[0].GrantRevision != "grant-v1" {
		t.Fatalf("configured outage result = %#v; calls = %#v", result, broker.discoverCalls)
	}
}

func TestCodingRemoteCapabilityToolRetainsPreboundInvocationAfterLostResponse(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	snapshot := codingRemoteToolTestSnapshot()
	broker := &fakeCodingRemoteBroker{
		snapshot:   snapshot,
		executeErr: errors.New("response lost after request write"),
		result: codingremote.CapabilityResult{
			Grant: "local-development", GrantRevision: "grant-v1",
			DiscoveryRevision: "discovery-v1", Capability: "build-workspace",
			CapabilityRevision: "capability-v1", Operation: "write_file",
			Target: "laptop", Risk: codingremote.RiskWrite, State: "succeeded",
		},
	}
	tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "git_worktree:" + strings.Repeat("f", 64), LocalProfile: codingscope.ProfileMutate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-execution-lost-response",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
	ctx = toolshared.WithToolCallID(ctx, "provider-lost-response-1")
	uncertain := tool.Execute(ctx, map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "write_file",
		"input": map[string]any{"path": "result.md", "content": "done", "overwrite": false},
	})
	if uncertain == nil || !uncertain.IsError {
		t.Fatalf("lost response result = %#v", uncertain)
	}
	uncertainResult := decodeCodingRemoteToolResult(t, uncertain)
	if uncertainResult.State != "unknown" || uncertainResult.ErrorCode != "BROKER_UNAVAILABLE" ||
		uncertainResult.InvocationID == "" ||
		!strings.Contains(uncertainResult.RecoveryAction, "do not replay") ||
		len(broker.executionCalls) != 1 ||
		broker.executionCalls[0].InvocationID != uncertainResult.InvocationID {
		t.Fatalf("uncertain result = %#v; calls = %#v", uncertainResult, broker.executionCalls)
	}

	broker.executeErr = nil
	ctx = toolshared.WithToolCallID(ctx, "provider-status-after-loss")
	status := tool.Execute(ctx, map[string]any{
		"action": "status", "capability": "build-workspace",
		"invocation_id": uncertainResult.InvocationID,
	})
	if status == nil || status.IsError || len(broker.executionCalls) != 2 ||
		broker.executionCalls[1].Operation != codingremote.OperationInvocationStatus ||
		broker.executionCalls[1].InvocationID != uncertainResult.InvocationID {
		t.Fatalf("status after lost response = %#v; calls = %#v", status, broker.executionCalls)
	}
}

func TestCodingRemoteCapabilityToolBindsTrustedInvocationAuthority(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	snapshot := codingRemoteToolTestSnapshot()
	broker := &fakeCodingRemoteBroker{
		snapshot: snapshot,
		result: codingremote.CapabilityResult{
			Grant: "local-development", GrantRevision: "grant-v1",
			DiscoveryRevision: "discovery-v1", Capability: "build-workspace",
			CapabilityRevision: "capability-v1", Operation: "write_file",
			InvocationID: "invocation_1", Target: "laptop", Risk: codingremote.RiskWrite,
			State: "succeeded", Result: json.RawMessage(`{"path":"docs/result.md","action":"create"}`),
			Changes: []codingremote.ChangeReceipt{{Path: "docs/result.md", Action: "create"}},
		},
	}
	tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "git_worktree:" + strings.Repeat("d", 64), LocalProfile: codingscope.ProfileMutate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-execution-1",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
	ctx = toolshared.WithToolCallID(ctx, "provider-call-1")
	result := tool.Execute(ctx, map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "write_file",
		"input": map[string]any{"path": "docs/result.md", "content": "ok", "overwrite": false},
	})
	if result == nil || result.IsError {
		t.Fatalf("Execute() = %#v", result)
	}
	if len(broker.executionCalls) != 1 {
		t.Fatalf("execution calls = %d, want 1", len(broker.executionCalls))
	}
	request := broker.executionCalls[0]
	if request.Principal == nil || *request.Principal != principal || request.CallID == "provider-call-1" ||
		!strings.HasPrefix(request.CallID, "call_") || request.Operation != codingremote.OperationCapabilityInvoke ||
		request.DiscoveryRevision != snapshot.DiscoveryRevision || request.CapabilityRevision != "capability-v1" ||
		request.InvocationID != codingremote.DeriveInvocationID(request) {
		t.Fatalf("trusted execution authority = %#v", request)
	}
	if strings.Contains(string(request.Arguments), "provider-call-1") {
		t.Fatalf("provider call identity leaked into model arguments: %s", request.Arguments)
	}
	if len(result.WriteAudit) != 1 || result.WriteAudit[0].Target != "remote:build-workspace/docs/result.md" ||
		result.WriteAudit[0].Metadata["placement"] != "remote" {
		t.Fatalf("write audit = %#v", result.WriteAudit)
	}
}

func TestCodingRemoteCapabilityToolRequiresTurnBoundIdentity(t *testing.T) {
	threadID := uuid.NewString()
	snapshot := codingRemoteToolTestSnapshot()
	broker := &fakeCodingRemoteBroker{snapshot: snapshot}
	tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: "coding:" + threadID,
		ProjectKey: "directory:" + strings.Repeat("a", 64), LocalProfile: codingscope.ProfileInvestigate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(context.Background(), map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "read_file",
		"input": map[string]any{"path": "README.md"},
	})
	if result == nil || !result.IsError || !strings.Contains(result.ContentForLLM(), "IDENTITY_UNAVAILABLE") {
		t.Fatalf("Execute() without principal = %#v", result)
	}
	if len(broker.executionCalls) != 0 {
		t.Fatalf("broker executions = %d, want 0", len(broker.executionCalls))
	}
}

func TestCodingRemoteCapabilityToolStatusObservesWithoutReplay(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	snapshot := codingRemoteToolTestSnapshot()
	broker := &fakeCodingRemoteBroker{
		snapshot: snapshot,
		result: codingremote.CapabilityResult{
			Grant: "local-development", GrantRevision: "grant-v1",
			DiscoveryRevision: "discovery-v1", Capability: "build-workspace",
			CapabilityRevision: "capability-v1", Operation: "read_file",
			InvocationID: "invocation_1", Target: "laptop", Risk: codingremote.RiskRead,
			State: "running",
		},
	}
	tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "directory:" + strings.Repeat("b", 64), LocalProfile: codingscope.ProfileInvestigate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-execution-2",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
	ctx = toolshared.WithToolCallID(ctx, "provider-invoke-1")
	invoked := tool.Execute(ctx, map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "read_file",
		"input": map[string]any{"path": "README.md"},
	})
	if invoked == nil || invoked.IsError {
		t.Fatalf("invoke = %#v", invoked)
	}
	invocationID := decodeCodingRemoteToolResult(t, invoked).InvocationID
	broker.snapshot = codingremote.CapabilitySnapshot{
		Schema: codingremote.SchemaV1, Grant: "local-development", GrantRevision: "grant-v1",
		DiscoveryRevision: "discovery-v2", GeneratedAtUnixMS: 2,
		Capabilities: []codingremote.CapabilityDescriptor{}, TaskScopes: []codingremote.TaskScopeDescriptor{},
	}
	broker.result.DiscoveryRevision = "discovery-v2"
	if listed := tool.Execute(context.Background(), map[string]any{"action": "list"}); listed == nil || listed.IsError {
		t.Fatalf("list after policy revocation = %#v", listed)
	}
	ctx = toolshared.WithToolCallID(ctx, "provider-status-1")
	wrongCapability := tool.Execute(ctx, map[string]any{
		"action": "status", "capability": "other-workspace", "invocation_id": invocationID,
	})
	if wrongCapability == nil || !wrongCapability.IsError ||
		!strings.Contains(wrongCapability.ContentForLLM(), "INVOCATION_UNAVAILABLE") ||
		len(broker.executionCalls) != 1 {
		t.Fatalf("cross-capability status = %#v; calls = %#v", wrongCapability, broker.executionCalls)
	}
	result := tool.Execute(ctx, map[string]any{
		"action": "status", "capability": "build-workspace", "invocation_id": invocationID,
	})
	if result == nil || result.IsError {
		t.Fatalf("status = %#v", result)
	}
	if len(broker.executionCalls) != 2 ||
		broker.executionCalls[1].Operation != codingremote.OperationInvocationStatus ||
		broker.executionCalls[1].InvocationID != invocationID ||
		broker.executionCalls[1].CapabilityRevision != "capability-v1" ||
		broker.executionCalls[1].DiscoveryRevision != "discovery-v2" ||
		len(broker.executionCalls[1].Arguments) != 0 ||
		broker.executionCalls[1].CapabilityOperation != "read_file" {
		t.Fatalf("status request replayed work: %#v", broker.executionCalls)
	}
}

func TestCodingRemoteCapabilityToolFailsClosedForUnknownRecoveredInvocation(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	snapshot := codingRemoteToolTestSnapshot()
	broker := &fakeCodingRemoteBroker{
		snapshot: snapshot,
		executeErr: &codingremote.BrokerError{
			Status: codingremote.ResponseDenied, Code: "INVOCATION_DENIED",
			Message: "coding remote invocation is denied",
		},
	}
	tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "directory:" + strings.Repeat("e", 64), LocalProfile: codingscope.ProfileMutate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-execution-4",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
	ctx = toolshared.WithToolCallID(ctx, "provider-status-unlinked")
	result := tool.Execute(ctx, map[string]any{
		"action": "status", "capability": "build-workspace", "invocation_id": "invocation_unlinked",
	})
	if result == nil || !result.IsError ||
		!strings.Contains(result.ContentForLLM(), "INVOCATION_DENIED") ||
		len(broker.executionCalls) != 1 ||
		broker.executionCalls[0].CapabilityOperation != codingremote.BrowserReceiptRecoveryOperation ||
		broker.executionCalls[0].CapabilityRevision != codingremote.BrowserReceiptRecoveryRevision {
		t.Fatalf("unlinked status = %#v; calls = %#v", result, broker.executionCalls)
	}
}

func TestCodingRemoteCapabilityToolCancelKeepsOriginalInvocationIdentity(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	snapshot := codingRemoteToolTestSnapshot()
	broker := &fakeCodingRemoteBroker{
		snapshot: snapshot,
		result: codingremote.CapabilityResult{
			Grant: "local-development", GrantRevision: "grant-v1",
			DiscoveryRevision: "discovery-v1", Capability: "build-workspace",
			CapabilityRevision: "capability-v1", Operation: "write_file",
			InvocationID: "invocation_1", Target: "laptop", Risk: codingremote.RiskWrite,
			State: "canceled", CancellationConfirmed: true,
		},
	}
	tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "directory:" + strings.Repeat("c", 64), LocalProfile: codingscope.ProfileMutate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-execution-3",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
	ctx = toolshared.WithToolCallID(ctx, "provider-invoke-1")
	invoked := tool.Execute(ctx, map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "write_file",
		"input": map[string]any{"path": "README.md", "content": "updated", "overwrite": true},
	})
	if invoked == nil || invoked.IsError {
		t.Fatalf("invoke = %#v", invoked)
	}
	invocationID := decodeCodingRemoteToolResult(t, invoked).InvocationID
	ctx = toolshared.WithToolCallID(ctx, "provider-cancel-1")
	result := tool.Execute(ctx, map[string]any{
		"action": "cancel", "capability": "build-workspace", "invocation_id": invocationID,
	})
	if result == nil || result.IsError || len(broker.executionCalls) != 2 ||
		broker.executionCalls[1].Operation != codingremote.OperationInvocationCancel ||
		broker.executionCalls[1].InvocationID != invocationID ||
		broker.executionCalls[1].CapabilityOperation != "write_file" ||
		result.Observation == nil || result.Observation.Command == nil ||
		!result.Observation.Command.Canceled {
		t.Fatalf("cancel result = %#v; calls = %#v", result, broker.executionCalls)
	}
}

func TestCodingRemoteCapabilityToolFetchesOwnedArtifactIntoThreadStore(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	snapshot := codingRemoteToolTestSnapshot()
	data := []byte(strings.Repeat("remote-artifact\n", 30000))
	digest := sha256.Sum256(data)
	digestText := hex.EncodeToString(digest[:])
	broker := &fakeCodingRemoteBroker{
		snapshot: snapshot,
		result: codingremote.CapabilityResult{
			Grant: "local-development", GrantRevision: "grant-v1",
			DiscoveryRevision: "discovery-v1", Capability: "build-workspace",
			CapabilityRevision: "capability-v1", Operation: "workspace_exec",
			Target: "laptop", Risk: codingremote.RiskWrite, State: "succeeded",
		},
	}
	broker.artifactFunc = func(request codingremote.Request) (codingremote.ArtifactResult, error) {
		result := codingremote.ArtifactResult{
			Grant: request.Grant, GrantRevision: request.GrantRevision,
			DiscoveryRevision: request.DiscoveryRevision,
			Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
			InvocationID: request.InvocationID, Target: "laptop", ArtifactRef: request.ArtifactRef,
			Name: "report.txt", State: "available", Size: int64(len(data)), SHA256: digestText,
			ContentType: "text/plain",
		}
		if request.Operation == codingremote.OperationArtifactFetch {
			end := min(len(data), int(request.Offset)+request.LimitBytes)
			chunk := data[int(request.Offset):end]
			result.Offset = request.Offset
			result.NextOffset = int64(end)
			result.EOF = end == len(data)
			result.DataBase64 = base64.StdEncoding.EncodeToString(chunk)
		}
		return result, nil
	}
	tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "directory:" + strings.Repeat("f", 64), LocalProfile: codingscope.ProfileMutate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeCodingRemoteArtifactStore{}
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-execution-artifact",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
	ctx = toolshared.WithToolCallID(ctx, "provider-job-start")
	started := tool.Execute(ctx, map[string]any{
		"action": "invoke", "capability": "build-workspace", "operation": "workspace_exec",
		"input": map[string]any{"executable": "go", "args": []any{"test", "./..."}, "mode": "job"},
	})
	if started == nil || started.IsError {
		t.Fatalf("job start = %#v", started)
	}
	invocationID := decodeCodingRemoteToolResult(t, started).InvocationID
	// Reconstruct the tool as a resumed coding runtime would. The process-local
	// invocation map is intentionally empty; the broker owns durable recovery.
	resumedTool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "directory:" + strings.Repeat("f", 64), LocalProfile: codingscope.ProfileMutate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	resumedTool.SetArtifactStore(store)
	ctx = toolshared.WithToolCallID(ctx, "provider-artifact-fetch")
	fetched := resumedTool.Execute(ctx, map[string]any{
		"action": "artifact_fetch", "capability": "build-workspace",
		"invocation_id": invocationID, "artifact_ref": "jobart_0123456789abcdef0123456789abcdef",
	})
	if fetched == nil || fetched.IsError || store.calls != 1 || string(store.data) != string(data) ||
		store.meta.Filename != "report.txt" || store.meta.SHA256 != digestText ||
		!strings.Contains(fetched.ContentForLLM(), "media://coding-attachment/imported") {
		t.Fatalf("artifact fetch = %#v; store = %#v", fetched, store)
	}
	if len(broker.artifactCalls) != 3 ||
		broker.artifactCalls[0].Operation != codingremote.OperationArtifactDescribe ||
		broker.artifactCalls[1].Offset != 0 ||
		broker.artifactCalls[2].Offset != int64(codingremote.MaxArtifactChunkBytes) {
		t.Fatalf("artifact calls = %#v", broker.artifactCalls)
	}
	corruptStore := &fakeCodingRemoteArtifactStore{}
	resumedTool.SetArtifactStore(corruptStore)
	broker.artifactCalls = nil
	broker.artifactFunc = func(request codingremote.Request) (codingremote.ArtifactResult, error) {
		result := codingremote.ArtifactResult{
			Grant: request.Grant, GrantRevision: request.GrantRevision,
			DiscoveryRevision: request.DiscoveryRevision,
			Capability:        request.Capability, CapabilityRevision: request.CapabilityRevision,
			InvocationID: request.InvocationID, Target: "laptop", ArtifactRef: request.ArtifactRef,
			Name: "report.txt", State: "available", Size: int64(len(data)), SHA256: digestText,
			ContentType: "text/plain",
		}
		if request.Operation == codingremote.OperationArtifactFetch {
			end := min(len(data), int(request.Offset)+request.LimitBytes)
			chunk := append([]byte(nil), data[int(request.Offset):end]...)
			chunk[0] ^= 0xff
			result.Offset = request.Offset
			result.NextOffset = int64(end)
			result.EOF = end == len(data)
			result.DataBase64 = base64.StdEncoding.EncodeToString(chunk)
		}
		return result, nil
	}
	ctx = toolshared.WithToolCallID(ctx, "provider-artifact-fetch-corrupt")
	corrupt := resumedTool.Execute(ctx, map[string]any{
		"action": "artifact_fetch", "capability": "build-workspace",
		"invocation_id": invocationID, "artifact_ref": "jobart_0123456789abcdef0123456789abcdef",
	})
	if corrupt == nil || !corrupt.IsError ||
		!strings.Contains(corrupt.ContentForLLM(), "ARTIFACT_FETCH_FAILED") || corruptStore.calls != 0 {
		t.Fatalf("corrupt artifact fetch = %#v; store=%#v", corrupt, corruptStore)
	}
}

func TestCodingRemoteCapabilityToolDelegatesRetainedArtifactOwnershipToBroker(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	snapshot := codingRemoteToolTestSnapshot()
	broker := &fakeCodingRemoteBroker{
		snapshot: snapshot,
		artifactErr: &codingremote.BrokerError{
			Status: codingremote.ResponseDenied, Code: "ARTIFACT_UNAVAILABLE",
			Message: "coding remote artifact is unavailable",
		},
	}
	tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1", ThreadID: threadID,
		SessionKey: sessionKey, ProjectKey: "directory:" + strings.Repeat("a", 64),
		LocalProfile: codingscope.ProfileMutate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-execution-artifact-denied",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
	ctx = toolshared.WithToolCallID(ctx, "provider-artifact-denied")
	result := tool.Execute(ctx, map[string]any{
		"action": "artifact_describe", "capability": "build-workspace",
		"invocation_id": "remote_capability_unowned", "artifact_ref": "jobart_unowned",
	})
	if result == nil || !result.IsError || !strings.Contains(result.ContentForLLM(), "ARTIFACT_UNAVAILABLE") ||
		len(broker.artifactCalls) != 1 ||
		broker.artifactCalls[0].Operation != codingremote.OperationArtifactDescribe {
		t.Fatalf("unowned artifact = %#v; calls = %#v", result, broker.artifactCalls)
	}
}

func decodeCodingRemoteToolResult(t *testing.T, result *toolshared.ToolResult) codingremote.CapabilityResult {
	t.Helper()
	var decoded codingremote.CapabilityResult
	if result == nil || json.Unmarshal([]byte(result.ContentForLLM()), &decoded) != nil {
		t.Fatalf("decode coding remote tool result = %#v", result)
	}
	return decoded
}

func TestCodingRemoteCapabilityToolPreservesBrowserDurabilityBoundaries(t *testing.T) {
	threadID := uuid.NewString()
	snapshot := codingRemoteBrowserToolTestSnapshot()
	tool, err := NewCodingRemoteCapabilityTool(&fakeCodingRemoteBroker{}, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: "coding:" + threadID,
		ProjectKey: "directory:" + strings.Repeat("b", 64), LocalProfile: codingscope.ProfileMutate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	secret := "browser-fill-secret"
	args := map[string]any{
		"action": "invoke", "capability": "browser", "operation": "browser_act",
		"input": map[string]any{
			"browser_session_id": "browser_1", "tab_id": "tab_1",
			"snapshot_id": "snapshot_1", "snapshot_generation": 1,
			"action": map[string]any{"kind": "fill", "ref": "ref_1", "value": secret},
		},
	}
	durable, err := tool.DurableArguments(args)
	encoded, marshalErr := json.Marshal(durable)
	if err != nil || marshalErr != nil || strings.Contains(string(encoded), secret) ||
		!strings.Contains(string(encoded), browserProtectedInputRedaction) ||
		!tool.ProtectedDurableArguments(args) || !tool.ProtectedDurableResult(args) {
		t.Fatalf(
			"browser durable projection = %s, err=%v/%v protected=%v/%v",
			encoded,
			err,
			marshalErr,
			tool.ProtectedDurableArguments(args),
			tool.ProtectedDurableResult(args),
		)
	}
	if tool.ProtectedDurableResult(map[string]any{
		"action": "invoke", "capability": "browser", "operation": "browser_open",
	}) {
		t.Fatal("opaque browser session receipt was unexpectedly protected")
	}

	invocationID := "remote_capability_browser_observe"
	if !tool.retainInvocation(invocationID, codingRemoteInvocationLink{
		Capability: "browser", CapabilityRevision: "browser-capability-v1",
		Operation: "browser_observe", Target: "companion-browser", Risk: codingremote.RiskRead,
		Kind: codingremote.CapabilityBrowserProfile,
	}) {
		t.Fatal("failed to retain browser invocation link")
	}
	tool.mu.Lock()
	tool.snapshot.Capabilities = nil
	tool.mu.Unlock()
	if !tool.ProtectedDurableResult(map[string]any{
		"action": "status", "capability": "browser", "invocation_id": invocationID,
	}) {
		t.Fatal("retained browser status lost protected-result classification after revocation")
	}
}

func TestCodingRemoteCapabilityToolRecoversBrowserReceiptIndependentOfCurrentDiscovery(t *testing.T) {
	cases := []struct {
		name     string
		snapshot func() codingremote.CapabilitySnapshot
	}{
		{
			name: "capability removed",
			snapshot: func() codingremote.CapabilitySnapshot {
				snapshot := codingRemoteBrowserToolTestSnapshot()
				snapshot.Capabilities = nil
				return snapshot
			},
		},
		{
			name: "alias reused by workspace",
			snapshot: func() codingremote.CapabilitySnapshot {
				snapshot := codingRemoteToolTestSnapshot()
				snapshot.Capabilities[0].Alias = "browser"
				snapshot.Capabilities[0].Revision = "workspace-capability-v2"
				return snapshot
			},
		},
		{
			name: "browser target rebound",
			snapshot: func() codingremote.CapabilitySnapshot {
				snapshot := codingRemoteBrowserToolTestSnapshot()
				snapshot.Capabilities[0].Revision = "browser-capability-v2"
				snapshot.Capabilities[0].Target = "replacement-browser"
				return snapshot
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			threadID := uuid.NewString()
			sessionKey := "coding:" + threadID
			snapshot := testCase.snapshot()
			snapshot.DiscoveryRevision = "discovery-reconfigured-v2"
			broker := &fakeCodingRemoteBroker{
				snapshot: snapshot,
				result: codingremote.CapabilityResult{
					Grant: "local-development", GrantRevision: "grant-v1",
					DiscoveryRevision: snapshot.DiscoveryRevision, Capability: "browser",
					CapabilityRevision: "browser-capability-v1", Operation: "browser_observe",
					Target: "companion-browser", Risk: codingremote.RiskRead, State: "succeeded",
					Result: json.RawMessage(`{"status":"ok"}`),
				},
			}
			tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
				Grant: "local-development", GrantRevision: "grant-v1",
				ThreadID: threadID, SessionKey: sessionKey,
				ProjectKey: "directory:" + strings.Repeat("c", 64), LocalProfile: codingscope.ProfileMutate,
			}, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			principal := runtimecap.Principal{
				Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
				SessionID: sessionKey, ExecutionID: "turn-browser-recovery",
			}
			runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
			ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
			ctx = toolshared.WithToolCallID(ctx, "provider-browser-status-after-restart")
			args := map[string]any{
				"action": "status", "capability": "browser",
				"invocation_id": "remote_capability_browser_restart",
			}
			if !tool.ProtectedDurableResult(args) {
				t.Fatal("unlinked browser status was not conservatively protected")
			}
			result := tool.Execute(ctx, args)
			if result == nil || result.IsError || len(broker.executionCalls) != 1 {
				t.Fatalf("browser receipt recovery = %#v; calls = %#v", result, broker.executionCalls)
			}
			request := broker.executionCalls[0]
			if request.Validate() != nil || request.Operation != codingremote.OperationInvocationStatus ||
				request.CapabilityOperation != codingremote.BrowserReceiptRecoveryOperation ||
				request.CapabilityRevision != codingremote.BrowserReceiptRecoveryRevision ||
				request.InvocationID != "remote_capability_browser_restart" {
				t.Fatalf("browser receipt recovery request = %#v", request)
			}
			recovered := decodeCodingRemoteToolResult(t, result)
			link, linked := tool.invocationLink(recovered.InvocationID)
			if recovered.Operation != "browser_observe" || !linked ||
				link.Operation != "browser_observe" || link.Kind != codingremote.CapabilityBrowserProfile {
				t.Fatalf("recovered browser receipt = %#v; link = %#v, %v", recovered, link, linked)
			}
		})
	}
}

func codingRemoteBrowserToolTestSnapshot() codingremote.CapabilitySnapshot {
	return codingremote.CapabilitySnapshot{
		Schema: codingremote.SchemaV1, Grant: "local-development", GrantRevision: "grant-v1",
		DiscoveryRevision: "discovery-browser-v1", GeneratedAtUnixMS: 1,
		Capabilities: []codingremote.CapabilityDescriptor{{
			Alias: "browser", Revision: "browser-capability-v1", Target: "companion-browser",
			Kind: codingremote.CapabilityBrowserProfile, Availability: codingremote.AvailabilityAvailable,
			Operations: []codingremote.OperationDescriptor{
				{
					Alias: "browser_act", Risk: codingremote.RiskWrite,
					InputSchema: json.RawMessage(`{"type":"object"}`), ResultKind: "browser_action",
				},
				{
					Alias: "browser_capture", Risk: codingremote.RiskRead,
					InputSchema: json.RawMessage(`{"type":"object"}`), ResultKind: "browser_artifact",
				},
				{
					Alias: "browser_observe", Risk: codingremote.RiskRead,
					InputSchema: json.RawMessage(`{"type":"object"}`), ResultKind: "browser_observation",
				},
				{
					Alias: "browser_open", Risk: codingremote.RiskWrite,
					InputSchema: json.RawMessage(`{"type":"object"}`), ResultKind: "browser_session",
				},
			},
		}},
		TaskScopes: []codingremote.TaskScopeDescriptor{},
	}
}

func TestCodingRemoteBrowserCapabilityClientUsesBoundDiscoveryAndReceipts(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	snapshot := codingRemoteBrowserToolTestSnapshot()
	broker := &fakeCodingRemoteBroker{snapshot: snapshot, result: codingremote.CapabilityResult{
		Grant: "local-development", GrantRevision: "grant-v1",
		DiscoveryRevision: "discovery-browser-v1", Capability: "browser",
		CapabilityRevision: "browser-capability-v1", Operation: "browser_observe",
		Target: "companion-browser", Risk: codingremote.RiskRead, State: "succeeded",
		Result: json.RawMessage(`{"browser_session_id":"browser_1","snapshot":"page"}`),
	}}
	tool, err := NewCodingRemoteCapabilityTool(broker, CodingRemoteToolAuthority{
		Grant: "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "git_worktree:" + strings.Repeat("b", 64), LocalProfile: codingscope.ProfileMutate,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewCodingRemoteBrowserCapabilityClient(tool, []string{"browser"})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := client.BrowserCapabilities()
	if !client.Available() || len(capabilities) != 1 || capabilities[0].Alias != "browser" ||
		capabilities[0].Target != "companion-browser" || len(capabilities[0].Operations) != 4 {
		t.Fatalf("browser capabilities = %#v", capabilities)
	}
	capabilities[0].Operations[0].InputSchema[0] = '['
	if refreshed := client.BrowserCapabilities(); refreshed[0].Operations[0].InputSchema[0] != '{' {
		t.Fatal("browser capability schema was not cloned")
	}

	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-browser-client",
	}
	runtime := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(context.Background(), runtime)
	ctx = toolshared.WithToolCallID(ctx, "provider-browser-observe")
	invoked := client.InvokeBrowser(ctx, "browser", "browser_observe", map[string]any{
		"browser_session_id": "browser_1",
	})
	result := decodeCodingRemoteToolResult(t, invoked)
	if invoked.IsError || result.InvocationID == "" || len(broker.executionCalls) != 1 ||
		broker.executionCalls[0].Capability != "browser" ||
		broker.executionCalls[0].CapabilityOperation != "browser_observe" {
		t.Fatalf("browser invocation = %#v; calls = %#v", result, broker.executionCalls)
	}

	ctx = toolshared.WithToolCallID(ctx, "provider-browser-status")
	status := client.BrowserInvocationStatus(ctx, "browser", result.InvocationID)
	if status == nil || status.IsError || len(broker.executionCalls) != 2 ||
		broker.executionCalls[1].Operation != codingremote.OperationInvocationStatus ||
		broker.executionCalls[1].InvocationID != result.InvocationID {
		t.Fatalf("browser status = %#v; calls = %#v", status, broker.executionCalls)
	}

	denied := client.InvokeBrowser(ctx, "workspace", "read_file", map[string]any{})
	if denied == nil || !denied.IsError || len(broker.executionCalls) != 2 {
		t.Fatalf("non-browser invocation = %#v; calls = %#v", denied, broker.executionCalls)
	}

	data := []byte("verified browser capture")
	digest := sha256.Sum256(data)
	digestText := hex.EncodeToString(digest[:])
	artifactRef := "transfer-artifact://capture_0123456789abcdef"
	broker.executeFunc = func(request codingremote.Request) (codingremote.CapabilityResult, error) {
		return codingremote.CapabilityResult{
			Grant: request.Grant, GrantRevision: request.GrantRevision,
			DiscoveryRevision: request.DiscoveryRevision, Capability: request.Capability,
			CapabilityRevision: request.CapabilityRevision, Operation: request.CapabilityOperation,
			InvocationID: request.InvocationID, Target: "companion-browser",
			Risk: codingremote.RiskRead, State: "succeeded",
			Result: json.RawMessage(`{"artifact":{"ref":"transfer-artifact://capture_0123456789abcdef"}}`),
		}, nil
	}
	captured := client.InvokeBrowser(ctx, "browser", "browser_capture", map[string]any{
		"browser_session_id": "browser_1",
	})
	captureResult := decodeCodingRemoteToolResult(t, captured)
	store := &fakeCodingRemoteArtifactStore{}
	tool.SetArtifactStore(store)
	broker.artifactFunc = func(request codingremote.Request) (codingremote.ArtifactResult, error) {
		artifact := codingremote.ArtifactResult{
			Grant: request.Grant, GrantRevision: request.GrantRevision,
			DiscoveryRevision: request.DiscoveryRevision, Capability: request.Capability,
			CapabilityRevision: request.CapabilityRevision, InvocationID: request.InvocationID,
			Target: "companion-browser", ArtifactRef: request.ArtifactRef, Name: "browser-screenshot.png",
			State: "available", Size: int64(len(data)), SHA256: digestText, ContentType: "image/png",
		}
		if request.Operation == codingremote.OperationArtifactFetch {
			artifact.Offset = request.Offset
			artifact.NextOffset = int64(len(data))
			artifact.EOF = true
			artifact.DataBase64 = base64.StdEncoding.EncodeToString(data)
		}
		return artifact, nil
	}
	ctx = toolshared.WithToolCallID(ctx, "provider-browser-artifact")
	imported := client.ImportBrowserArtifact(
		ctx,
		"browser",
		"browser_capture",
		captureResult.InvocationID,
		artifactRef,
	)
	if imported.IsError || store.calls != 1 || string(store.data) != string(data) ||
		len(broker.artifactCalls) != 2 ||
		broker.artifactCalls[0].CapabilityOperation != "browser_capture" ||
		!strings.Contains(imported.ContentForLLM(), "media://coding-attachment/imported") {
		t.Fatalf("browser artifact import = %#v; store=%#v calls=%#v", imported, store, broker.artifactCalls)
	}
}

func codingRemoteToolTestSnapshot() codingremote.CapabilitySnapshot {
	return codingremote.CapabilitySnapshot{
		Schema: codingremote.SchemaV1, Grant: "local-development", GrantRevision: "grant-v1",
		DiscoveryRevision: "discovery-v1", GeneratedAtUnixMS: 1,
		Capabilities: []codingremote.CapabilityDescriptor{{
			Alias: "build-workspace", Revision: "capability-v1", Target: "laptop",
			Kind: codingremote.CapabilityRemoteWorkspace, Availability: codingremote.AvailabilityAvailable,
			Operations: []codingremote.OperationDescriptor{
				{
					Alias: "read_file", Risk: codingremote.RiskRead,
					InputSchema: json.RawMessage(`{"type":"object"}`), ResultKind: "workspace_read",
				},
				{
					Alias: "workspace_exec", Risk: codingremote.RiskWrite,
					InputSchema: json.RawMessage(`{"type":"object"}`), ResultKind: "workspace_exec",
				},
				{
					Alias: "write_file", Risk: codingremote.RiskWrite,
					InputSchema: json.RawMessage(`{"type":"object"}`), ResultKind: "workspace_write",
					SupportsCancel: true,
				},
			},
		}},
		TaskScopes: []codingremote.TaskScopeDescriptor{},
	}
}
