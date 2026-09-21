package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

type fakeCodingNodeInvocationSource struct {
	*fakeNodeInvocationSource
	ephemeral        json.RawMessage
	ephemeralCalls   int
	dispatchedBefore bool
}

func TestToolLogArgumentsRedactsCodingTaskContent(t *testing.T) {
	const secret = "private objective and sk-secret-value-1234567890"
	got := ToolLogArguments("coding_task", map[string]any{
		"action": "start", "task_id": "coding-one", "scope": "mintclaw",
		"profile": "investigate", "objective": secret, "done_criteria": secret,
		"text": secret,
	})
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || got["action"] != "start" ||
		got["scope"] != "mintclaw" || got["redacted"] != true {
		t.Fatalf("coding task log projection = %s", encoded)
	}
}

func (source *fakeCodingNodeInvocationSource) DispatchInvocationEphemeral(
	ctx context.Context,
	owner nodes.GatewayInvocationOwner,
	invocationID string,
	expectedPlanHash string,
	ephemeralInput json.RawMessage,
) (json.RawMessage, bool, error) {
	source.ephemeralCalls++
	source.ephemeral = append(json.RawMessage(nil), ephemeralInput...)
	principal := nodes.GatewayInvocationPrincipal{
		AgentID: owner.AgentID, SessionID: owner.SessionID, ActorID: owner.ActorID,
		WorkspaceID: owner.WorkspaceID, ExecutionID: owner.ExecutionID,
	}
	if _, found, _ := source.store.Lookup(principal, invocationID); found {
		source.dispatchedBefore = true
	}
	return source.DispatchInvocation(ctx, owner, invocationID, expectedPlanHash)
}

func TestCodingNodeInvokerDurablyPreparesBeforeEphemeralDispatch(t *testing.T) {
	source, descriptor := newFakeCodingNodeSource(t, nodes.CodingCommandTaskStart)
	result := nodes.CodingTaskResult{
		TaskID: "coding-task-one", TaskGenerationID: uuid.NewString(),
		ScopeAlias: "mintclaw", ScopeRevision: "project-v1",
		Profile: codingtask.TaskModeInvestigate, ThreadID: uuid.NewString(),
		ThreadOpenMode: "new", WorkerGenerationID: uuid.NewString(),
		State: codingtask.StateRunning, Revision: 1,
	}
	source.dispatchResult = mustJSONRaw(t, result)
	input, ephemeral, err := nodes.NewCodingTaskStartInputs(
		result.TaskID,
		result.TaskGenerationID,
		result.ScopeAlias,
		result.ScopeRevision,
		result.Profile,
		"Investigate a private regression without exposing this prompt.",
		"Return a bounded root-cause report.",
		"start-"+result.TaskGenerationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	authority := codingNodeTestAuthority("start")
	raw, err := NewCodingNodeInvoker(
		NewNodeToolOptions(nodeDiscoveryTestConfig()),
		source,
	).Invoke(t.Context(), authority, "build", descriptor.Name, input, ephemeral)
	if err != nil {
		t.Fatal(err)
	}
	if !source.dispatchedBefore || source.prepareCalls != 1 || source.dispatchCalls != 1 ||
		source.ephemeralCalls != 1 || len(raw) == 0 {
		t.Fatalf(
			"coding invocation order = prepared %d, ephemeral %d, dispatched %d, durable-before-send %v",
			source.prepareCalls,
			source.ephemeralCalls,
			source.dispatchCalls,
			source.dispatchedBefore,
		)
	}
	if !strings.Contains(string(source.ephemeral), "private regression") {
		t.Fatalf("ephemeral dispatch content = %s", source.ephemeral)
	}
	principal := codingInvocationPrincipal(authority)
	toolCallID := stableNodeInvocationID(
		"coding_call",
		authority.ExecutionID,
		authority.OperationID,
		descriptor.Name,
	)
	record, found, err := source.store.ByToolCall(principal, toolCallID)
	if err != nil || !found {
		t.Fatalf("prepared invocation = %#v, %v, %v", record, found, err)
	}
	if record.Plan.TimeoutSeconds != codingTaskStartInvocationTimeout {
		t.Fatalf(
			"start invocation timeout = %d, want %d",
			record.Plan.TimeoutSeconds,
			codingTaskStartInvocationTimeout,
		)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private regression", "bounded root-cause report"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("durable invocation leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestCodingNodeInvokerRecoversDispatchedInvocationWithoutReplay(t *testing.T) {
	source, descriptor := newFakeCodingNodeSource(t, nodes.CodingCommandTaskStatus)
	source.dispatchErr = errors.New("acknowledgement was lost")
	source.remote = nodes.InvocationRecord{
		State:  nodes.InvocationSucceeded,
		Result: json.RawMessage(`{"task_id":"coding-task-one"}`),
	}
	authority := codingNodeTestAuthority("status-one")
	invoker := NewCodingNodeInvoker(NewNodeToolOptions(nodeDiscoveryTestConfig()), source)
	input := nodes.CodingTaskIdentityInput{
		TaskID: "coding-task-one", TaskGenerationID: uuid.NewString(),
	}
	first, err := invoker.Invoke(t.Context(), authority, "build", descriptor.Name, input, nil)
	if err != nil || len(first) == 0 {
		t.Fatalf("first Invoke() = %s, %v", first, err)
	}
	second, err := invoker.Invoke(t.Context(), authority, "build", descriptor.Name, input, nil)
	if err != nil || len(second) == 0 {
		t.Fatalf("second Invoke() = %s, %v", second, err)
	}
	if source.prepareCalls != 1 || source.dispatchCalls != 1 || source.queryCalls != 2 {
		t.Fatalf(
			"recovery calls = prepare %d, dispatch %d, query %d",
			source.prepareCalls,
			source.dispatchCalls,
			source.queryCalls,
		)
	}
}

func TestCodingNodeInvokerKeepsControlOperationsAtDefaultTimeout(t *testing.T) {
	source, descriptor := newFakeCodingNodeSource(t, nodes.CodingCommandTaskStatus)
	source.dispatchResult = mustJSONRaw(t, nodes.CodingTaskResult{
		TaskID: "coding-task-one", TaskGenerationID: uuid.NewString(),
		ScopeAlias: "mintclaw", ScopeRevision: "project-v1",
		Profile: codingtask.TaskModeInvestigate, ThreadID: uuid.NewString(),
		ThreadOpenMode: "new", WorkerGenerationID: uuid.NewString(),
		State: codingtask.StateRunning, Revision: 1,
	})
	authority := codingNodeTestAuthority("status-timeout")
	input := nodes.CodingTaskIdentityInput{
		TaskID: "coding-task-one", TaskGenerationID: uuid.NewString(),
	}
	if _, err := NewCodingNodeInvoker(
		NewNodeToolOptions(nodeDiscoveryTestConfig()),
		source,
	).Invoke(t.Context(), authority, "build", descriptor.Name, input, nil); err != nil {
		t.Fatal(err)
	}
	principal := codingInvocationPrincipal(authority)
	toolCallID := stableNodeInvocationID(
		"coding_call",
		authority.ExecutionID,
		authority.OperationID,
		descriptor.Name,
	)
	record, found, err := source.store.ByToolCall(principal, toolCallID)
	if err != nil || !found {
		t.Fatalf("prepared invocation = %#v, %v, %v", record, found, err)
	}
	if record.Plan.TimeoutSeconds != defaultNodeInvocationTimeout {
		t.Fatalf(
			"status invocation timeout = %d, want %d",
			record.Plan.TimeoutSeconds,
			defaultNodeInvocationTimeout,
		)
	}
}

func TestCodingNodeInvokerRejectsGenericOrModelVisibleCommands(t *testing.T) {
	source, _ := newFakeCodingNodeSource(t, nodes.CodingCommandTaskStatus)
	invoker := NewCodingNodeInvoker(NewNodeToolOptions(nodeDiscoveryTestConfig()), source)
	if _, err := invoker.Invoke(
		t.Context(),
		codingNodeTestAuthority("generic"),
		"build",
		"system.exec.v1",
		map[string]any{},
		nil,
	); !errors.Is(err, ErrCodingNodeUnavailable) {
		t.Fatalf("generic Invoke() error = %v", err)
	}
}

func TestCodingNodeInvokerRetainsOnlySafeRemoteFailureCode(t *testing.T) {
	source, descriptor := newFakeCodingNodeSource(t, nodes.CodingCommandTaskStatus)
	source.dispatchErr = nodes.NewInvocationDispatchError(
		nodes.InvocationDispatchCodingScopeStale,
		errors.New("private repository root and credential"),
	)
	source.remote = nodes.InvocationRecord{
		State: nodes.InvocationFailed,
		Failure: &nodes.InvocationFailure{
			Code: nodes.InvocationDispatchCodingScopeStale,
		},
	}
	_, err := NewCodingNodeInvoker(
		NewNodeToolOptions(nodeDiscoveryTestConfig()),
		source,
	).Invoke(
		t.Context(),
		codingNodeTestAuthority("status-stale"),
		"build",
		descriptor.Name,
		nodes.CodingTaskIdentityInput{
			TaskID: "coding-task-one", TaskGenerationID: uuid.NewString(),
		},
		nil,
	)
	code, classified := CodingNodeOperationErrorCode(err)
	if !classified || code != nodes.InvocationDispatchCodingScopeStale ||
		strings.Contains(err.Error(), "private repository") {
		t.Fatalf("coding operation error = %v, code %q, classified %v", err, code, classified)
	}
}

func newFakeCodingNodeSource(
	t *testing.T,
	commandName string,
) (*fakeCodingNodeInvocationSource, nodes.CommandDescriptor) {
	t.Helper()
	descriptors, err := nodes.CodingCommandDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	var descriptor nodes.CommandDescriptor
	for _, candidate := range descriptors {
		if candidate.Name == commandName {
			descriptor = candidate
			break
		}
	}
	if descriptor.Name == "" {
		t.Fatalf("coding descriptor %q not found", commandName)
	}
	base := newFakeNodeInvocationSource(t)
	catalog := nodes.CapabilityCatalog{Commands: []nodes.CommandDescriptor{descriptor}}
	catalogHash := mustCatalogHash(t, catalog)
	snapshot := base.byRef["builder-node"]
	snapshot.Catalog = catalog
	snapshot.CatalogHash = catalogHash
	base.byRef["builder-node"] = snapshot
	base.registrations[snapshot.ID] = nodes.Registration{
		Snapshot: snapshot, AllowedCommands: []string{descriptor.Name},
		ApprovedCatalogHash: catalogHash, ApprovedAt: 1,
	}
	return &fakeCodingNodeInvocationSource{fakeNodeInvocationSource: base}, descriptor
}

func codingNodeTestAuthority(operation string) CodingInvocationAuthority {
	return CodingInvocationAuthority{
		AgentID: "main", SessionID: "telegram-route", ActorID: "owner-42",
		Workspace: "/gateway/workspace", ExecutionID: "coding-task-generation",
		OperationID: operation,
	}
}

func mustJSONRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
