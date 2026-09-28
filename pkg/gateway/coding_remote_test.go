package gateway

import (
	"context"
	"encoding/json"
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

type codingRemoteDiscoverySource struct {
	tools.NodeInvocationSource
	record tools.NodeDiscoveryRecord
}

func (source *codingRemoteDiscoverySource) Lookup(string) (tools.NodeDiscoveryRecord, bool, error) {
	return source.record, true, nil
}

type codingRemoteRetainedSource struct {
	tools.NodeInvocationSource
	record      nodes.GatewayInvocationRecord
	remote      nodes.InvocationRecord
	queryCalls  int
	cancelCalls int
}

func (source *codingRemoteRetainedSource) LookupInvocationByToolCall(
	_ nodes.GatewayInvocationPrincipal,
	_ string,
) (nodes.GatewayInvocationRecord, bool, error) {
	return source.record, true, nil
}

func (source *codingRemoteRetainedSource) LookupInvocation(
	_ nodes.GatewayInvocationPrincipal,
	invocationID string,
) (nodes.GatewayInvocationRecord, bool, error) {
	if invocationID != source.record.Plan.InvocationID {
		return nodes.GatewayInvocationRecord{}, false, nil
	}
	return source.record, true, nil
}

func (source *codingRemoteRetainedSource) QueryInvocation(
	_ context.Context,
	_ nodes.GatewayInvocationPrincipal,
	_ string,
	_ nodes.ID,
	_ string,
) (nodes.InvocationRecord, error) {
	source.queryCalls++
	return source.remote, nil
}

func (source *codingRemoteRetainedSource) CancelInvocation(
	_ context.Context,
	_ nodes.GatewayInvocationPrincipal,
	_ string,
	_ nodes.ID,
	_ string,
) (nodes.InvocationRecord, bool, error) {
	source.cancelCalls++
	return source.remote, true, nil
}

func TestCodingRemoteDiscoveryRequiresExactGrantAndProfile(t *testing.T) {
	cfg := gatewayCodingRemoteTestConfig()
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg },
		now:    func() time.Time { return time.UnixMilli(1234) },
	}
	request := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-one",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     "local-development", GrantRevision: "grant-v1",
		LocalProfile: codingscope.ProfileMutate,
	}
	response := handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Snapshot == nil ||
		response.Snapshot.Grant != request.Grant || response.Snapshot.GrantRevision != request.GrantRevision ||
		response.Snapshot.GeneratedAtUnixMS != 1234 || len(response.Snapshot.Capabilities) != 0 ||
		len(response.Snapshot.TaskScopes) != 0 {
		t.Fatalf("HandleCodingRemote() = %#v", response)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response.Validate() error = %v", err)
	}

	changed := request
	changed.GrantRevision = "old"
	response = handler.HandleCodingRemote(t.Context(), changed)
	if response.Status != codingremote.ResponseDenied || response.Code != "GRANT_CHANGED" ||
		response.Snapshot != nil {
		t.Fatalf("changed grant response = %#v", response)
	}
	denied := request
	denied.LocalProfile = codingscope.ProfileInvestigate
	response = handler.HandleCodingRemote(t.Context(), denied)
	if response.Status != codingremote.ResponseDenied || response.Code != "PROFILE_DENIED" {
		t.Fatalf("profile response = %#v", response)
	}
	missing := request
	missing.Grant = "missing"
	response = handler.HandleCodingRemote(t.Context(), missing)
	if response.Status != codingremote.ResponseDenied || response.Code != "GRANT_UNAVAILABLE" {
		t.Fatalf("missing grant response = %#v", response)
	}
}

func TestCodingRemoteDiscoveryRevisionBindsAuthorityIndependentOfOrdering(t *testing.T) {
	cfg := gatewayCodingRemoteTestConfig()
	grant := cfg.Execution.CodingRemoteGrants["local-development"]
	first := codingRemoteDiscoveryRevision(cfg, "local-development", grant)
	grant.Capabilities = []string{"status", "workspace"}
	grant.LocalProfiles = []codingscope.Profile{codingscope.ProfileMutate}
	grant.Tasks[0].Profiles = []codingscope.Profile{
		codingscope.ProfileMutate,
		codingscope.ProfileInvestigate,
	}
	second := codingRemoteDiscoveryRevision(cfg, "local-development", grant)
	if first != second {
		t.Fatalf("reordered discovery revisions = %q / %q", first, second)
	}
	capability := cfg.Execution.CodingRemoteCapabilities["status"]
	capability.Revision = "cap-v2"
	cfg.Execution.CodingRemoteCapabilities["status"] = capability
	third := codingRemoteDiscoveryRevision(cfg, "local-development", grant)
	if third == second {
		t.Fatal("capability revision did not change discovery revision")
	}
}

func TestCodingRemoteDiscoveryFailsClosedWhenDisabled(t *testing.T) {
	cfg := gatewayCodingRemoteTestConfig()
	cfg.Gateway.CodingRemote.Enabled = false
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg },
		now:    time.Now,
	}
	response := handler.HandleCodingRemote(t.Context(), codingremote.Request{RequestID: "request-one"})
	if response.Status != codingremote.ResponseUnavailable || response.Code != "BROKER_DISABLED" ||
		response.Snapshot != nil {
		t.Fatalf("disabled response = %#v", response)
	}
}

func TestCodingRemoteDiscoveryProjectsExactExplicitWorkspacePolicy(t *testing.T) {
	profile := nodes.FileProfileDescriptor{
		Alias: "project-files", Revision: "files-v1",
		ReadableRoots: []string{"/private/project"}, WritableRoots: []string{"/private/project"},
		AllowCreate: true, AllowOverwrite: true, MaxFileBytes: 1024,
		Approval: nodes.FileProfileApproval{Metadata: "none", Read: "none", Write: "none"},
	}
	descriptors, err := nodes.WorkspaceDescriptors([]nodes.FileProfileDescriptor{profile}, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	for index := range descriptors {
		contract := *descriptors[index].ModelContract
		contract.Availability = nodes.ModelAvailable
		descriptors[index].ModelContract = &contract
	}
	catalog := nodes.CapabilityCatalog{Commands: descriptors}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := nodes.Snapshot{
		ID: "private-node-id", State: nodes.StateConnected, ProtocolVersion: nodes.ProtocolVersion,
		Catalog: catalog, CatalogHash: catalogHash, Executor: "local", PolicyRevision: "policy-v1",
	}
	registration := nodes.Registration{
		Snapshot: snapshot, ApprovedCatalogHash: catalogHash, ApprovedAt: 1,
		AllowedCommands: []string{nodes.WorkspaceCommandRead, nodes.WorkspaceCommandWrite},
	}
	source := &codingRemoteDiscoverySource{record: tools.NodeDiscoveryRecord{
		Snapshot: snapshot, Registration: &registration, Connected: true,
	}}
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"build": {Type: "node", Node: "builder-node", FileProfile: "project-files"},
	}
	cfg.Execution.RemoteWorkspaces = map[string]config.RemoteWorkspace{
		"project": {
			Target: "build", WorkingScope: "project", Revision: "workspace-v1",
			Tools: []string{"read_file", "write_file"},
		},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{AllowedTargets: []string{"build"}}
	cfg.Execution.CodingRemoteCapabilities = map[string]config.CodingRemoteCapability{
		"build-workspace": {
			Kind: config.CodingRemoteCapabilityWorkspace, Revision: "capability-v1",
			RemoteWorkspace: "project", Operations: []string{"write_file", "read_file"},
		},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Capabilities:  []string{"build-workspace"},
		},
	}
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: func() time.Time { return time.UnixMilli(1234) },
		source: func(*config.Config) (tools.NodeInvocationSource, error) { return source, nil },
	}
	request := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-one",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     "local-development", GrantRevision: "grant-v1", LocalProfile: codingscope.ProfileMutate,
	}
	response := handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Snapshot == nil ||
		len(response.Snapshot.Capabilities) != 1 || len(response.Snapshot.Capabilities[0].Operations) != 2 ||
		response.Snapshot.Capabilities[0].Operations[0].Alias != "read_file" ||
		response.Snapshot.Capabilities[0].Operations[1].Alias != "write_file" {
		t.Fatalf("mutate discovery = %#v", response)
	}
	encoded, err := json.Marshal(response.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-node-id", "/private/project", "builder-node", "project-files"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("discovery exposed private authority %q: %s", private, encoded)
		}
	}
	invoke := request
	invoke.RequestID = "request-stale"
	invoke.Operation = codingremote.OperationCapabilityInvoke
	invoke.Principal = &runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: "coding:11111111-1111-4111-8111-111111111111", ExecutionID: "turn-1",
	}
	invoke.CallID = "call_stale"
	invoke.DiscoveryRevision = "discovery-stale"
	invoke.Capability = "build-workspace"
	invoke.CapabilityRevision = "capability-v1"
	invoke.CapabilityOperation = "read_file"
	invoke.Arguments = json.RawMessage(`{"path":"README.md"}`)
	invoke.InvocationID = codingremote.DeriveInvocationID(invoke)
	response = handler.HandleCodingRemote(t.Context(), invoke)
	if response.Status != codingremote.ResponseDenied || response.Code != "DISCOVERY_STALE" ||
		response.Result != nil {
		t.Fatalf("stale invoke = %#v", response)
	}
	request.LocalProfile = codingscope.ProfileInvestigate
	response = handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Snapshot == nil ||
		len(response.Snapshot.Capabilities) != 1 || len(response.Snapshot.Capabilities[0].Operations) != 1 ||
		response.Snapshot.Capabilities[0].Operations[0].Alias != "read_file" {
		t.Fatalf("investigate discovery = %#v", response)
	}
}

func TestCodingRemoteStatusRetainsOwnedObservationAfterGrantRevocation(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	invocationID := "node_invocation_1"
	invocationReference := "remote_capability_test_reference"
	source := &codingRemoteRetainedSource{
		record: nodes.GatewayInvocationRecord{
			Target: "build", State: nodes.GatewayInvocationDispatched,
			Plan: nodes.ExecutionPlan{InvocationRequest: nodes.InvocationRequest{
				InvocationID: invocationID, NodeID: "private-node-id", Command: nodes.WorkspaceCommandRead,
			}, Risk: nodes.RiskRead},
		},
		remote: nodes.InvocationRecord{
			InvocationID: invocationID, Command: nodes.WorkspaceCommandRead,
			State: nodes.InvocationSucceeded, Result: json.RawMessage(`{"path":"README.md","content":"ok"}`),
		},
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: time.Now,
		source: func(*config.Config) (tools.NodeInvocationSource, error) { return source, nil },
	}
	request := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "request-status",
		Operation: codingremote.OperationInvocationStatus,
		Grant:     "removed-grant", GrantRevision: "grant-v1", ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "git_worktree:" + strings.Repeat("d", 64), LocalProfile: codingscope.ProfileMutate,
		Principal: &runtimecap.Principal{
			Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
			SessionID: sessionKey, ExecutionID: "turn-2",
		},
		CallID: "call_status", DiscoveryRevision: "discovery-old",
		Capability: "build-workspace", CapabilityRevision: "capability-v1",
		CapabilityOperation: "read_file", InvocationID: invocationReference,
	}
	response := handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Result == nil ||
		response.Result.State != string(nodes.InvocationSucceeded) ||
		response.Result.Operation != "read_file" || response.Result.InvocationID != invocationReference ||
		source.queryCalls != 1 {
		t.Fatalf("retained status = %#v; query calls = %d", response, source.queryCalls)
	}
	mismatched := request
	mismatched.RequestID = "request-status-wrong-operation"
	mismatched.CapabilityOperation = "write_file"
	response = handler.HandleCodingRemote(t.Context(), mismatched)
	if response.Status != codingremote.ResponseDenied || response.Code != "INVOCATION_DENIED" {
		t.Fatalf("mismatched retained status = %#v", response)
	}
	source.remote.State = nodes.InvocationCanceled
	source.remote.Cancellation = &nodes.InvocationCancellation{RequestedAt: 1, TerminationConfirmed: true}
	request.RequestID = "request-cancel"
	request.Operation = codingremote.OperationInvocationCancel
	request.CallID = "call_cancel"
	response = handler.HandleCodingRemote(t.Context(), request)
	if response.Status != codingremote.ResponseOK || response.Result == nil ||
		response.Result.State != string(nodes.InvocationCanceled) ||
		!response.Result.CancellationConfirmed || source.cancelCalls != 1 {
		t.Fatalf("retained cancel = %#v; cancel calls = %d", response, source.cancelCalls)
	}
}

func TestCodingRemoteCapabilityExecutionIdentityBindsExactCapabilityOperation(t *testing.T) {
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	request := codingremote.Request{
		ThreadID: threadID, SessionKey: sessionKey,
		ProjectKey: "git_worktree:" + strings.Repeat("e", 64),
		Grant:      "local-development", GrantRevision: "grant-v1",
		DiscoveryRevision: "discovery-v1", Capability: "build-workspace",
		CapabilityRevision: "capability-v1", CapabilityOperation: "read_file",
		CallID: "call_invoke", InvocationID: "remote_capability_reference",
		Principal: &runtimecap.Principal{
			Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
			SessionID: sessionKey, ExecutionID: "turn-1",
		},
	}
	executionID := func(candidate codingremote.Request) string {
		ctx := codingRemoteExecutionContext(t.Context(), candidate, "main")
		return toolshared.ToolExecutionID(ctx)
	}
	baseline := executionID(request)
	if got := toolshared.ToolCallID(
		codingRemoteExecutionContext(t.Context(), request, "main"),
	); got != request.InvocationID {
		t.Fatalf("invoke tool-call identity = %q, want %q", got, request.InvocationID)
	}
	continued := request
	continued.CallID = "call_status"
	continued.DiscoveryRevision = "discovery-v2"
	continued.Principal = &runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-2",
	}
	if got := executionID(continued); got != baseline {
		t.Fatalf("continued capability execution ID = %q, want %q", got, baseline)
	}
	if got := toolshared.ToolCallID(
		codingRemoteExecutionContext(t.Context(), continued, "main"),
	); got != request.InvocationID {
		t.Fatalf("continued tool-call identity = %q, want %q", got, request.InvocationID)
	}
	for name, mutate := range map[string]func(*codingremote.Request){
		"grant revision": func(value *codingremote.Request) { value.GrantRevision = "grant-v2" },
		"capability":     func(value *codingremote.Request) { value.Capability = "other-workspace" },
		"capability revision": func(value *codingremote.Request) {
			value.CapabilityRevision = "capability-v2"
		},
		"operation": func(value *codingremote.Request) { value.CapabilityOperation = "write_file" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request
			mutate(&candidate)
			if got := executionID(candidate); got == baseline {
				t.Fatalf("authority mutation retained execution ID %q", got)
			}
		})
	}
}

func TestCodingRemoteInvokePreservesUncertainInvocationForNoReplayRecovery(t *testing.T) {
	request := codingremote.Request{
		Grant: "local-development", GrantRevision: "grant-v1", DiscoveryRevision: "discovery-v1",
		Capability: "build-workspace", CapabilityRevision: "capability-v1",
		InvocationID: "remote_capability_uncertain_reference",
	}
	capability := codingremote.CapabilityDescriptor{Target: "build"}
	operation := codingremote.OperationDescriptor{Alias: "write_file", Risk: codingremote.RiskWrite}
	toolResult := toolshared.ErrorResult(`{
		"error":"node dispatch outcome is uncertain",
		"error_code":"DISPATCH_UNCERTAIN",
		"invocation":{
			"invocation_id":"invocation_1",
			"target":"build",
			"command":"workspace.write.v1",
			"gateway_state":"dispatched",
			"state":"unknown",
			"error_code":"DISPATCH_UNCERTAIN",
			"recovery_action":"Call nodes_status with this invocation ID; do not replay the write."
		}
	}`)
	result, err := codingRemoteInvokeResult(request, capability, operation, "invocation_1", toolResult)
	if err != nil {
		t.Fatal(err)
	}
	if result.InvocationID != request.InvocationID || result.State != "unknown" ||
		result.ErrorCode != "DISPATCH_UNCERTAIN" ||
		!strings.Contains(result.RecoveryAction, "remote_capability status") ||
		strings.Contains(result.RecoveryAction, "nodes_status") ||
		!strings.Contains(result.RecoveryAction, "do not replay") {
		t.Fatalf("uncertain invocation = %#v", result)
	}
}

func gatewayCodingRemoteTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Gateway.CodingRemote.Enabled = true
	cfg.Execution.CodingRemoteCapabilities = map[string]config.CodingRemoteCapability{
		"workspace": {Revision: "cap-v1"},
		"status":    {Revision: "cap-v1"},
	}
	cfg.Execution.RemoteCodingScopes = map[string]config.RemoteCodingScope{
		"mintclaw-dev": {Revision: "scope-v1"},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main", LocalProfiles: []codingscope.Profile{codingscope.ProfileMutate},
			Capabilities: []string{"workspace", "status"},
			Tasks: []config.CodingRemoteTaskGrant{{
				Scope:    "mintclaw-dev",
				Profiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			}},
		},
	}
	return cfg
}
