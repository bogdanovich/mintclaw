package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestClientExecutesCodingStartWithEphemeralTextOverAuthenticatedSession(t *testing.T) {
	registry, admission := testGatewayAdmission(t)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	identity := testIdentity(t)
	process := newHostTestProcess()
	backend := &hostTestBackend{processes: []*hostTestProcess{process}}
	host, ledger, catalog := newHostTestFixture(
		t,
		[]codingtask.TaskMode{codingtask.TaskModeInvestigate},
		backend,
	)
	policy := codingTestPolicy(nodes.MaxInvocationOutput)
	commandRuntime, err := NewRuntime(
		identity.ID,
		"test",
		policy,
		ledger,
		WithCodingTaskHost(host),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := testRuntimeClientForServer(t, server, identity, commandRuntime)
	authentication, err := client.Authenticate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Approve(authentication.NodeID, nodes.PairingApproval{
		AllowedCommands: policy.AllowedCommands,
		At:              time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(runContext) }()
	waitForNodeState(t, registry, identity.ID, nodes.StateConnected)

	registration, exists, err := registry.Registration(identity.ID)
	if err != nil || !exists {
		t.Fatalf("Registration() = exists %v, error %v", exists, err)
	}
	descriptor, err := registration.ApprovedCommand(nodes.CodingCommandTaskStart)
	if err != nil {
		t.Fatal(err)
	}
	objective := "Inspect the repository through the authenticated node session."
	input, ephemeral, err := nodes.NewCodingTaskStartInputs(
		"task-transport",
		"generation-transport",
		catalog.List()[0].Alias,
		catalog.List()[0].Revision,
		codingtask.TaskModeInvestigate,
		objective,
		"Return bounded evidence.",
		"turn-transport",
	)
	if err != nil {
		t.Fatal(err)
	}
	rawInput, _ := json.Marshal(input)
	rawEphemeral, _ := json.Marshal(ephemeral)
	plan, err := nodes.PrepareExecutionPlan(nodes.InvocationRequest{
		InvocationID: "inv-coding-transport", IdempotencyKey: "idem-coding-transport",
		NodeID: identity.ID, CatalogHash: registration.Snapshot.CatalogHash,
		Command: descriptor.Name, Input: rawInput,
		AgentID: "agent-test", SessionID: "session-test", ActorID: "actor-test",
		TimeoutSeconds: 10, OutputLimitBytes: nodes.MinCodingTaskOutputBytes,
	}, descriptor, registration.Snapshot.Executor, registration.Snapshot.PolicyRevision, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	output, dispatched, err := admission.Invoke(t.Context(), identity.ID, plan, rawEphemeral, nil)
	if err != nil || !dispatched {
		t.Fatalf("Invoke() = dispatched %v, output %s, error %v", dispatched, output, err)
	}
	var result nodes.CodingTaskResult
	if err = json.Unmarshal(output, &result); err != nil || result.TaskID != input.TaskID ||
		result.State != codingtask.StateRunning || process.startCalls != 1 || process.startText != objective+"\n\nDone criteria:\n"+ephemeral.DoneCriteria {
		t.Fatalf("coding transport result = %s, process %#v, error %v", output, process, err)
	}
	if bytes.Contains(output, []byte(objective)) {
		t.Fatalf("coding transport output exposed objective: %s", output)
	}
	ledger.mu.Lock()
	durable, marshalErr := json.Marshal(invocationLedgerDocument{
		Version: invocationLedgerVersion, Records: ledger.records, CodingTasks: ledger.codingTasks,
	})
	ledger.mu.Unlock()
	if marshalErr != nil || bytes.Contains(durable, []byte(objective)) {
		t.Fatalf("coding transport ledger retained objective: %s, %v", durable, marshalErr)
	}

	cancelRun()
	if runErr := <-runDone; runErr != nil {
		t.Fatal(runErr)
	}
	waitForNodeState(t, registry, identity.ID, nodes.StateDisconnected)
}
