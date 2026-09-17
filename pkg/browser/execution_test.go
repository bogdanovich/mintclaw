package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func executionTestConfig() *config.Config {
	root := admittedBrowserConfig()
	target := root.Tools.Browser.Targets["gateway"]
	target.Driver = config.BrowserDriverPlaywrightLibrary
	target.DriverServer = ""
	target.DriverExecutable = "node"
	profile := target.Profiles["managed"]
	profile.Revision = "managed-execute-v1"
	profile.DryRun = false
	profile.AllowApprovedActions = true
	profile.ApprovalMode = config.BrowserApprovalNone
	profile.PrivilegedExecution = config.BrowserExecutionConfig{Enabled: true}
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target
	return root
}

func TestPrivilegedExecutionBindsSourceBudgetsArtifactsAndNoReplay(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBrokerWithConfig(t, executionTestConfig(), store)
	worker.executionResult = DriverExecutionResult{
		Value:           json.RawMessage(`{"title":"Example"}`),
		Actions:         3,
		NetworkRequests: 1,
		Artifacts: []DriverScreenshot{
			{Data: append(append([]byte(nil), pngSignature...), 'x'), ContentType: "image/png"},
		},
	}
	owner := testOwner()
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	source := `async ({page, artifacts}) => ({title: await page.title(), shot: await artifacts.screenshot()})`
	prepared, err := broker.PrepareExecution(t.Context(), PrepareExecutionRequest{
		Owner: owner, RequestID: "execute_request_1", SessionID: session.ID, TabID: observation.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Source: source, Language: ExecutionJavaScript, DeclaredEffect: EffectRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.RequiresApproval || prepared.Invocation.Execution == nil ||
		prepared.Invocation.Execution.SourceDigest != ExecutionSourceDigest(source) ||
		prepared.Invocation.Execution.Limits != (config.BrowserExecutionConfig{Enabled: true}.Effective()) {
		t.Fatalf("PrepareExecution() = %+v", prepared)
	}
	encoded, err := json.Marshal(prepared.Invocation)
	if err != nil || strings.Contains(string(encoded), source) {
		t.Fatalf("durable invocation leaked source: %s, %v", encoded, err)
	}
	invocation, err := broker.ExecuteExecution(
		t.Context(), owner, prepared.Invocation.ID, source, nil,
		func(_ context.Context, _ Invocation, index int, artifact DriverScreenshot) (RetainedScreenshot, error) {
			if index != 0 || len(artifact.Data) == 0 {
				t.Fatal("artifact sink received wrong artifact")
			}
			return RetainedScreenshot{
				Ref: "artifact_ref", ContentType: "image/png", Size: int64(len(artifact.Data)),
				SHA256: strings.Repeat("a", 64), ExpiresAt: 999,
			}, nil
		},
	)
	if err != nil || invocation.State != InvocationSucceeded || len(worker.executionRequests) != 1 {
		t.Fatalf("ExecuteExecution() = %+v, %v; requests=%d", invocation, err, len(worker.executionRequests))
	}
	var terminal ExecutionResult
	if json.Unmarshal(invocation.TerminalResult, &terminal) != nil || terminal.Status != "completed" ||
		len(terminal.Artifacts) != 1 || terminal.Artifacts[0].Ref != "artifact_ref" {
		t.Fatalf("terminal result = %s", invocation.TerminalResult)
	}
	replayed, err := broker.ExecuteExecution(t.Context(), owner, prepared.Invocation.ID, source, nil, nil)
	if err != nil || replayed.State != InvocationSucceeded || len(worker.executionRequests) != 1 {
		t.Fatalf("terminal replay = %+v, %v; requests=%d", replayed, err, len(worker.executionRequests))
	}
}

func TestPrivilegedExecutionApprovalDigestAndDryRunFailClosed(t *testing.T) {
	root := executionTestConfig()
	target := root.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.ApprovalMode = config.BrowserApprovalModelRequested
	profile.DryRun = true
	profile.AllowApprovedActions = false
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target
	broker, worker, session := openActionTestBrokerWithConfig(t, root, NewMemoryStore())
	owner := testOwner()
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareExecutionRequest{
		Owner: owner, RequestID: "execute_request_approval", SessionID: session.ID,
		TabID: observation.TabID, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Source:             `async () => true`, Language: ExecutionJavaScript,
		DeclaredEffect: EffectExternalCommit, Confirmation: "request",
	}
	prepared, err := broker.PrepareExecution(t.Context(), request)
	if err != nil || !prepared.RequiresApproval {
		t.Fatalf("PrepareExecution() = %+v, %v", prepared, err)
	}
	if _, err = broker.ExecuteExecution(
		t.Context(), owner, prepared.Invocation.ID, request.Source, nil, nil,
	); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("unapproved ExecuteExecution() error = %v", err)
	}
	approval := prepared.Approval
	invocation, err := broker.ExecuteExecution(
		t.Context(), owner, prepared.Invocation.ID, request.Source, &approval, nil,
	)
	if !errors.Is(err, ErrDenied) || invocation.State != InvocationCanceled || len(worker.executionRequests) != 0 {
		t.Fatalf("dry-run execution = %+v, %v; requests=%d", invocation, err, len(worker.executionRequests))
	}
}

func TestPrivilegedExecutionRejectsChangedSourceForSameRequest(t *testing.T) {
	broker, _, session := openActionTestBrokerWithConfig(t, executionTestConfig(), NewMemoryStore())
	owner := testOwner()
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareExecutionRequest{
		Owner: owner, RequestID: "execute_request_conflict", SessionID: session.ID,
		TabID: observation.TabID, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Source:             `async () => 1`, Language: ExecutionJavaScript, DeclaredEffect: EffectRead,
	}
	if _, err = broker.PrepareExecution(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.Source = `async () => 2`
	if _, err = broker.PrepareExecution(t.Context(), request); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed source error = %v", err)
	}
}

func TestPrivilegedExecutionCancellationNeverReplaysAcceptedSource(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBrokerWithConfig(t, executionTestConfig(), store)
	owner := testOwner()
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	source := `async () => await new Promise(() => {})`
	prepared, err := broker.PrepareExecution(t.Context(), PrepareExecutionRequest{
		Owner: owner, RequestID: "execute_request_canceled", SessionID: session.ID,
		TabID: observation.TabID, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration, Source: source,
		Language: ExecutionJavaScript, DeclaredEffect: EffectRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.executionErr = context.Canceled
	invocation, err := broker.ExecuteExecution(
		t.Context(), owner, prepared.Invocation.ID, source, nil, nil,
	)
	if err != nil || invocation.State != InvocationUnknown || invocation.AcceptedAt == 0 ||
		invocation.Diagnostic == nil || invocation.Diagnostic.FailureClass != OutcomeFailureCanceled ||
		len(worker.executionRequests) != 1 {
		t.Fatalf("canceled execution = %+v, %v; requests=%d", invocation, err, len(worker.executionRequests))
	}
	recovered, err := broker.ExecuteExecution(
		t.Context(), owner, prepared.Invocation.ID, source, nil, nil,
	)
	if err != nil || recovered.State != InvocationUnknown || len(worker.executionRequests) != 1 {
		t.Fatalf("recovered execution = %+v, %v; requests=%d", recovered, err, len(worker.executionRequests))
	}
}
