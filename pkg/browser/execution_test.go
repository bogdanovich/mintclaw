package browser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
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

func TestPrivilegedExecutionSeparatesSourceAndActionBudgets(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "slow_control_plane"
		if canceled {
			name = "caller_deadline"
		}
		t.Run(name, func(t *testing.T) {
			root := executionTestConfig()
			root.Tools.Browser.Limits.ActionSeconds = 10
			target := root.Tools.Browser.Targets["gateway"]
			profile := target.Profiles["managed"]
			profile.PrivilegedExecution.RuntimeSeconds = 1
			target.Profiles["managed"] = profile
			root.Tools.Browser.Targets["gateway"] = target
			store := NewMemoryStore()
			broker, worker, session := openActionTestBrokerWithConfig(t, root, store)
			owner := testOwner()
			observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			source := `async () => await new Promise(() => {})`
			prepared, err := broker.PrepareExecution(t.Context(), PrepareExecutionRequest{
				Owner: owner, RequestID: "delayed_read", SessionID: session.ID, TabID: observed.TabID,
				SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
				Source: source, Language: ExecutionJavaScript, DeclaredEffect: EffectRead,
			})
			if err != nil {
				t.Fatal(err)
			}
			worker.executionFunc = func(ctx context.Context, request DriverExecutionRequest) (DriverExecutionResult, error) {
				if request.Limits.RuntimeSeconds != 1 {
					t.Fatalf("source runtime budget changed: %+v", request.Limits)
				}
				// Six seconds of preflight/transport followed by one second of
				// source execution fits the ten-second action budget, but not
				// the old source-runtime-plus-five-second outer deadline.
				timer := time.NewTimer(7 * time.Second)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return DriverExecutionResult{}, ctx.Err()
				case <-timer.C:
					return DriverExecutionResult{}, errors.Join(ErrExecutionSettled, ErrExecutionTimeout)
				}
			}
			ctx := t.Context()
			if canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			result, executeErr := broker.ExecuteExecution(ctx, owner, prepared.Invocation.ID, source, nil, nil)
			stored, getErr := store.GetSession(t.Context(), session.ID)
			if getErr != nil || len(worker.executionRequests) != 1 {
				t.Fatalf("session=%+v, %v; dispatches=%d", stored, getErr, len(worker.executionRequests))
			}
			if canceled {
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) || result.State != InvocationUnknown ||
					stored.State != SessionLost {
					t.Fatalf("caller deadline result=%+v, %v; session=%+v", result, executeErr, stored)
				}
				return
			}
			if executeErr != nil || result.State != InvocationFailed || result.SafeFailure != "execution_timeout" ||
				stored.State != SessionReady {
				t.Fatalf("delayed settlement result=%+v, %v; session=%+v", result, executeErr, stored)
			}
			if _, err = broker.Observe(t.Context(), owner, session.ID, session.TabID); err != nil {
				t.Fatalf("same-session observation: %v", err)
			}
			if _, err = broker.ExecuteExecution(
				t.Context(),
				owner,
				prepared.Invocation.ID,
				source,
				nil,
				nil,
			); err != nil ||
				len(worker.executionRequests) != 1 {
				t.Fatalf("settled source replay: %v; dispatches=%d", err, len(worker.executionRequests))
			}
			if _, err = broker.Close(t.Context(), owner, session.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
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

func TestPrivilegedExecutionSettledFailurePreservesSessionWithoutReplay(t *testing.T) {
	for _, failure := range []struct {
		name       string
		err        error
		wantState  InvocationState
		wantReason string
	}{
		{"rejected", errors.Join(ErrExecutionSettled, ErrDriverRejected), InvocationFailed, "execution_rejected"},
		{
			"timeout", errors.Join(ErrExecutionSettled, ErrExecutionTimeout, ErrDriverRejected),
			InvocationFailed, "execution_timeout",
		},
		{"unconfirmed rejection", ErrDriverRejected, InvocationUnknown, "outcome_unknown"},
		{"unconfirmed timeout", ErrExecutionTimeout, InvocationUnknown, "outcome_unknown"},
		{
			"transport loss", errors.Join(ErrExecutionSettled, ErrDriverRejected, ErrWorkerUnavailable),
			InvocationUnknown, "outcome_unknown",
		},
		{"cancellation", errors.Join(ErrExecutionSettled, context.Canceled), InvocationUnknown, "outcome_unknown"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			store := NewMemoryStore()
			broker, worker, session := openActionTestBrokerWithConfig(t, executionTestConfig(), store)
			owner := testOwner()
			observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			source := `async ({page}) => page.locator('#missing').innerText()`
			prepared, err := broker.PrepareExecution(t.Context(), PrepareExecutionRequest{
				Owner: owner, RequestID: "settled_execution", SessionID: session.ID, TabID: observed.TabID,
				SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
				Source: source, Language: ExecutionJavaScript, DeclaredEffect: EffectRead,
			})
			if err != nil {
				t.Fatal(err)
			}
			worker.executionErr = failure.err
			invocation, err := broker.ExecuteExecution(t.Context(), owner, prepared.Invocation.ID, source, nil, nil)
			if err != nil || invocation.State != failure.wantState || invocation.SafeFailure != failure.wantReason {
				t.Fatalf("execution = %+v, %v", invocation, err)
			}
			recovered, err := broker.ExecuteExecution(t.Context(), owner, prepared.Invocation.ID, source, nil, nil)
			if err != nil || recovered.State != failure.wantState || len(worker.executionRequests) != 1 {
				t.Fatalf("recovery = %+v, %v; dispatches=%d", recovered, err, len(worker.executionRequests))
			}
			persisted, err := store.GetSession(t.Context(), session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if failure.wantState == InvocationUnknown {
				if persisted.State != SessionLost {
					t.Fatalf("ambiguous session = %+v", persisted)
				}
				return
			}
			if persisted.State != SessionReady || persisted.SnapshotID != "" {
				t.Fatalf("settled failure lost session or retained stale authority: %+v", persisted)
			}
			if _, err = broker.Observe(t.Context(), owner, session.ID, session.TabID); err != nil {
				t.Fatalf("same-session observation: %v", err)
			}
		})
	}
}

func TestSettledExecutionFailureDoesNotOverrideNetworkOrTransportFailure(t *testing.T) {
	for _, boundaryErr := range []error{ErrDenied, ErrDriverIncompatible, ErrStale, ErrWorkerUnavailable} {
		if got := SettledExecutionFailure(errors.Join(ErrExecutionSettled, ErrDriverRejected, boundaryErr)); got != "" {
			t.Fatalf("boundary error %v was classified as settled: %q", boundaryErr, got)
		}
	}
}

func TestPrivilegedExecutionRestrictedPolicyBindsAllowAskAndDeny(t *testing.T) {
	for _, test := range []struct {
		decision     string
		wantApproval bool
		wantDenied   bool
	}{
		{decision: browserpolicy.DecisionAllow},
		{decision: browserpolicy.DecisionAsk, wantApproval: true},
		{decision: browserpolicy.DecisionDeny, wantDenied: true},
	} {
		t.Run(test.decision, func(t *testing.T) {
			root := restrictedExecutionTestConfig(browserpolicy.Policy{DefaultDecision: test.decision})
			broker, worker, session := openActionTestBrokerWithConfig(t, root, NewMemoryStore())
			worker.executionResult = DriverExecutionResult{Value: json.RawMessage(`{"ok":true}`)}
			owner := testOwner()
			observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			source := `async ({page}) => ({title: await page.title()})`
			prepared, err := broker.PrepareExecution(t.Context(), PrepareExecutionRequest{
				Owner: owner, RequestID: "execute_restricted_" + test.decision,
				SessionID: session.ID, TabID: observed.TabID, SnapshotID: observed.SnapshotID,
				SnapshotGeneration: observed.SnapshotGeneration, Source: source,
				Language: ExecutionJavaScript, DeclaredEffect: EffectRead,
			})
			if test.wantDenied {
				if !errors.Is(err, ErrDenied) || len(worker.executionRequests) != 0 {
					t.Fatalf("PrepareExecution() = %+v, %v; requests=%d", prepared, err, len(worker.executionRequests))
				}
				return
			}
			binding := prepared.Invocation.Execution
			if err != nil || prepared.RequiresApproval != test.wantApproval || binding == nil ||
				binding.PolicyEffect != EffectRead || binding.RestrictedDecision != test.decision ||
				binding.LocalRestrictedDecision != test.decision || !validDigest(binding.RestrictedPolicyRevision) {
				t.Fatalf("PrepareExecution() = %+v, %v", prepared, err)
			}
			var approval *ExecutionApprovalBinding
			if prepared.RequiresApproval {
				approval = &prepared.Approval
			}
			invocation, err := broker.ExecuteExecution(
				t.Context(), owner, prepared.Invocation.ID, source, approval, nil,
			)
			if err != nil || invocation.State != InvocationSucceeded || len(worker.executionRequests) != 1 {
				t.Fatalf("ExecuteExecution() = %+v, %v; requests=%+v", invocation, err, worker.executionRequests)
			}
			request := worker.executionRequests[0]
			if request.NetworkMode != config.BrowserNetworkExactOrigins ||
				!reflect.DeepEqual(request.AllowedOrigins, []string{"https://example.com"}) ||
				request.CapabilityMode != browserpolicy.CapabilityRestricted {
				t.Fatalf("driver authority = %+v", request)
			}
		})
	}
}

func TestPrivilegedExecutionRestrictedHookRevalidatesAfterAcceptance(t *testing.T) {
	statePath := t.TempDir() + "/hook-output.json"
	writeBrowserPolicyHookOutput(t, statePath, `{"decision":"allow"}`)
	root := restrictedExecutionTestConfig(browserpolicy.Policy{
		DefaultDecision: browserpolicy.DecisionAllow,
		Hook: &browserpolicy.Hook{
			Command:   []string{os.Args[0], "-test.run=TestBrokerRestrictedPolicyHookProcess", "--", statePath},
			TimeoutMS: 1000,
		},
	})
	store := &acceptedMutationStore{MemoryStore: NewMemoryStore()}
	store.onAccepted = func() {
		writeBrowserPolicyHookOutput(t, statePath, `{"decision":"deny","reason":"changed"}`)
	}
	broker, worker, session := openActionTestBrokerWithConfig(t, root, store)
	owner := testOwner()
	observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	source := `async ({page}) => page.title()`
	prepared, err := broker.PrepareExecution(t.Context(), PrepareExecutionRequest{
		Owner: owner, RequestID: "execute_restricted_hook", SessionID: session.ID,
		TabID: observed.TabID, SnapshotID: observed.SnapshotID,
		SnapshotGeneration: observed.SnapshotGeneration, Source: source,
		Language: ExecutionJavaScript, DeclaredEffect: EffectRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := broker.ExecuteExecution(
		t.Context(), owner, prepared.Invocation.ID, source, nil, nil,
	)
	if !errors.Is(err, ErrDenied) || invocation.State != InvocationFailed ||
		invocation.SafeFailure != "policy_denied" || len(worker.executionRequests) != 0 {
		t.Fatalf("ExecuteExecution() = %+v, %v; requests=%+v", invocation, err, worker.executionRequests)
	}
}

func restrictedExecutionTestConfig(policy browserpolicy.Policy) *config.Config {
	root := executionTestConfig()
	target := root.Tools.Browser.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.CapabilityMode = config.BrowserCapabilityRestricted
	profile.ApprovalMode = config.BrowserApprovalPolicy
	profile.Policy = &policy
	target.Profiles["managed"] = profile
	root.Tools.Browser.Targets["gateway"] = target
	return root
}
