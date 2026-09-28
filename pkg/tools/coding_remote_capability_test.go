package tools

import (
	"context"
	"encoding/json"
	"errors"
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

func TestCodingRemoteCapabilityToolRejectsUnlinkedInvocationObservation(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	snapshot := codingRemoteToolTestSnapshot()
	broker := &fakeCodingRemoteBroker{snapshot: snapshot}
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
		!strings.Contains(result.ContentForLLM(), "INVOCATION_UNAVAILABLE") ||
		len(broker.executionCalls) != 0 {
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

func decodeCodingRemoteToolResult(t *testing.T, result *toolshared.ToolResult) codingremote.CapabilityResult {
	t.Helper()
	var decoded codingremote.CapabilityResult
	if result == nil || json.Unmarshal([]byte(result.ContentForLLM()), &decoded) != nil {
		t.Fatalf("decode coding remote tool result = %#v", result)
	}
	return decoded
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
					Alias: "write_file", Risk: codingremote.RiskWrite,
					InputSchema: json.RawMessage(`{"type":"object"}`), ResultKind: "workspace_write",
					SupportsCancel: true,
				},
			},
		}},
		TaskScopes: []codingremote.TaskScopeDescriptor{},
	}
}
