package companion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

const protectedComputerCanary = "private input Привет 世界 e\u0301 👩‍💻 literal \\n and \\t"

// This counter host verifies runtime/ledger contracts, not native input. Its
// stored evidence comes from the real read-only host, never from the wire.
type fixtureComputerInputHost struct {
	*fixtureComputerHost
	evidence computer.InputEvidence
	calls    atomic.Int32
	act      func(context.Context, nodes.ComputerHostActRequest) (nodes.ComputerActResult, error)
}

func (host *fixtureComputerInputHost) ComputerInputProfiles() []nodes.ComputerProfileDescriptor {
	return host.ComputerProfiles()
}

func (host *fixtureComputerInputHost) Act(
	ctx context.Context, request nodes.ComputerHostActRequest,
) (nodes.ComputerActResult, error) {
	status, err := host.core.Status(ctx, request.Request.Binding.Principal, request.Request.Binding.SessionID)
	if err != nil {
		return nodes.ComputerActResult{}, err
	}
	if err = request.Request.Validate(status, host.evidence, time.Now()); err != nil {
		return nodes.ComputerActResult{}, err
	}
	host.calls.Add(1)
	if host.act != nil {
		return host.act(ctx, request)
	}
	return host.postAction(ctx, request)
}

func (host *fixtureComputerInputHost) postAction(
	ctx context.Context, request nodes.ComputerHostActRequest,
) (nodes.ComputerActResult, error) {
	observation, err := host.Observe(ctx, nodes.ComputerHostObserveRequest{
		ComputerHostStatusRequest: nodes.ComputerHostStatusRequest{
			Principal: request.Request.Binding.Principal, SessionID: request.Request.Binding.SessionID,
		},
		Generation:   request.Request.Binding.Generation,
		InvocationID: request.Request.ID, WorkspaceID: request.WorkspaceID,
	})
	// A real post-action AX tree can echo entered text. It must stay live-only.
	observation.AccessibilityText = string(request.Request.Text)
	return nodes.ComputerActResult{ActionID: request.Request.ID, Observation: observation}, err
}

func newFixtureComputerInputHost(t *testing.T) *fixtureComputerInputHost {
	t.Helper()
	host := &fixtureComputerInputHost{fixtureComputerHost: newFixtureComputerHost(t)}
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindGateway, ActorID: "actor_test", AgentID: "agent_test",
		SessionID: "session_test", ExecutionID: "original-turn",
	}
	opened, err := host.Open(t.Context(), nodes.ComputerHostOpenRequest{
		Principal: principal, Profile: host.profile.Alias, ProfileRevision: host.profile.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := host.core.Observe(t.Context(), opened.Session)
	if err != nil {
		t.Fatal(err)
	}
	host.evidence, err = computer.InputEvidenceFromObservation(observation, opened.Session, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func fixtureComputerInputRuntime(
	t *testing.T, host *fixtureComputerInputHost, ledger *InvocationLedger, options ...RuntimeOption,
) *Runtime {
	t.Helper()
	descriptors, err := nodes.ComputerCommandDescriptors(host.ComputerProfiles())
	if err != nil {
		t.Fatal(err)
	}
	commands := []string{nodes.ComputerCommandAct}
	for _, descriptor := range descriptors {
		commands = append(commands, descriptor.Name)
	}
	policy := testRuntimePolicy(commands)
	policy.MaximumRisk = nodes.RiskWrite
	options = append([]RuntimeOption{WithComputerInputHost(host)}, options...)
	runtime, err := NewRuntime("node_test", "test", policy, ledger, options...)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func fixtureComputerInputPlan(
	t *testing.T, runtime *Runtime, host *fixtureComputerInputHost,
) (nodes.ExecutionPlan, json.RawMessage) {
	t.Helper()
	input, ephemeral, err := nodes.NewComputerActInputs(computer.InputRequest{
		ID: "action-1", Binding: host.evidence.Binding, ObservationID: host.evidence.ID,
		Action: computer.InputAction{
			Kind: computer.InputInsertText, Text: &computer.InputTextSpec{ElementRef: "field-1"},
		},
		Text: []byte(protectedComputerCanary),
	}, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	payload, _ := json.Marshal(ephemeral)
	catalog := runtime.Catalog()
	hash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	var descriptor nodes.CommandDescriptor
	for _, candidate := range catalog.Commands {
		if candidate.Name == nodes.ComputerCommandAct {
			descriptor = candidate
		}
	}
	plan, err := nodes.PrepareExecutionPlan(nodes.InvocationRequest{
		InvocationID: input.Request.ID, IdempotencyKey: "action-idem-1", NodeID: "node_test", CatalogHash: hash,
		Command: nodes.ComputerCommandAct, Input: raw, ActorID: "actor_test", AgentID: "agent_test",
		SessionID: "session_test", TimeoutSeconds: 5, OutputLimitBytes: 32 * 1024,
	}, descriptor, LocalExecutor, runtime.policy.Revision, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return plan, payload
}

func TestComputerInputRuntimeRequiresSeparateOptIn(t *testing.T) {
	host := newFixtureComputerInputHost(t)
	readOnly, _ := fixtureComputerRuntime(t, host, newMemoryInvocationLedger())
	downgraded := fixtureComputerInputRuntime(t, host, newMemoryInvocationLedger(), WithComputerHost(host))
	for _, runtime := range []*Runtime{readOnly, downgraded} {
		for _, descriptor := range runtime.Catalog().Commands {
			if descriptor.Name == nodes.ComputerCommandAct {
				t.Fatal("read-only composition inferred input capability from host methods")
			}
		}
	}
	input := fixtureComputerInputRuntime(t, host, newMemoryInvocationLedger())
	count := 0
	for _, descriptor := range input.Catalog().Commands {
		if descriptor.Name == nodes.ComputerCommandAct {
			count++
			if descriptor.Risk != nodes.RiskWrite || descriptor.ModelContract != nil {
				t.Fatal("input exposed an unqualified generic model tool")
			}
		}
	}
	if count != 1 || input.computerHost != host {
		t.Fatal("input did not share exactly one lifecycle owner")
	}
	if err := input.computerHost.Disconnect(t.Context()); err != nil || host.disconnected != 1 {
		t.Fatal("shared host disconnect failed", err)
	}
}

func TestComputerInputRuntimeRejectsPayloadBeforeLedgerAcceptance(t *testing.T) {
	for _, payload := range []json.RawMessage{nil, json.RawMessage(`{"text":"wrong"}`), json.RawMessage(`{"Text":"wrong"}`)} {
		host := newFixtureComputerInputHost(t)
		ledger := newMemoryInvocationLedger()
		runtime := fixtureComputerInputRuntime(t, host, ledger)
		plan, _ := fixtureComputerInputPlan(t, runtime, host)
		if _, err := runtime.InvokeWithEphemeral(t.Context(), plan, payload); err == nil {
			t.Fatal("unbound payload admitted")
		}
		if _, found, err := ledger.Lookup(plan.InvocationID); err != nil || found || host.calls.Load() != 0 {
			t.Fatal("bad payload reached acceptance or host", err)
		}
	}
}

func TestComputerInputRuntimeHostRevalidatesStoredEvidenceAndHumanControl(t *testing.T) {
	for _, kind := range []string{"handoff", "expired-evidence", "different-window-incarnation"} {
		t.Run(kind, func(t *testing.T) {
			host := newFixtureComputerInputHost(t)
			runtime := fixtureComputerInputRuntime(t, host, newMemoryInvocationLedger())
			plan, payload := fixtureComputerInputPlan(t, runtime, host)
			switch kind {
			case "handoff":
				if _, err := host.core.Handoff(t.Context(), host.evidence.Binding); err != nil {
					t.Fatal(err)
				}
			case "expired-evidence":
				host.evidence.ExpiresAt = time.Now().Add(-time.Second)
			case "different-window-incarnation":
				host.evidence.Binding.Target.Scope.WindowGeneration++
			}
			if _, err := runtime.InvokeWithEphemeral(t.Context(), plan, payload); err == nil || host.calls.Load() != 0 {
				t.Fatal("transport commitment bypassed the host's own current authority", err)
			}
		})
	}
}

func TestComputerInputRuntimeLiveOnlyResultAndDurableNoReplay(t *testing.T) {
	host := newFixtureComputerInputHost(t)
	path := filepath.Join(t.TempDir(), "invocations.json")
	ledger, err := NewFileInvocationLedger(path, DefaultInvocationLedgerLimit, DefaultInvocationLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.Close)
	runtime := fixtureComputerInputRuntime(t, host, ledger)
	plan, payload := fixtureComputerInputPlan(t, runtime, host)
	raw, err := runtime.InvokeWithEphemeral(t.Context(), plan, payload)
	var result nodes.ComputerActResult
	if err != nil || json.Unmarshal(raw, &result) != nil ||
		result.Observation.AccessibilityText != protectedComputerCanary {
		t.Fatal("live post-action evidence unavailable", err)
	}
	for _, data := range [][]byte{plan.Input, mustReadComputerLedger(t, path)} {
		if strings.Contains(string(data), "private input") ||
			strings.Contains(string(data), base64.StdEncoding.EncodeToString([]byte(protectedComputerCanary))) {
			t.Fatal("protected text or post-action AX echo persisted")
		}
	}
	ledger.Close()
	reloaded, err := NewFileInvocationLedger(path, DefaultInvocationLedgerLimit, DefaultInvocationLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.Close)
	after := fixtureComputerInputRuntime(t, host, reloaded)
	for range 2 {
		raw, err = after.Invoke(t.Context(), plan)
		if err != nil || string(raw) != `{"protected_result":true}` || host.calls.Load() != 1 {
			t.Fatal("durable replay executed input or reconstructed private evidence", err)
		}
	}
}

func mustReadComputerLedger(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestComputerInputRuntimeUnknownEffectsAreNotReplayed(t *testing.T) {
	for _, kind := range []string{
		"host-error", "context-only", "wrong-action", "same-observation", "foreign-binding", "unknown-wins",
		"unsolicited-cleanup", "old-capture",
	} {
		t.Run(kind, func(t *testing.T) {
			host := newFixtureComputerInputHost(t)
			host.act = func(ctx context.Context, request nodes.ComputerHostActRequest) (nodes.ComputerActResult, error) {
				result, err := host.postAction(ctx, request)
				switch kind {
				case "host-error":
					err = errors.New(protectedComputerCanary)
				case "context-only":
					err = context.Canceled
				case "unsolicited-cleanup":
					err = ErrComputerInputCancellationConfirmed
				case "wrong-action":
					result.ActionID = "other-action"
				case "same-observation":
					result.Observation.Observation.ID = request.Request.ObservationID
				case "old-capture":
					result.Observation.Observation.CapturedAt = host.evidence.CapturedAt
				case "foreign-binding":
					result.Observation.Session.Binding.Principal.ExecutionID = "other-turn"
				case "unknown-wins":
					err = errors.Join(
						ErrInvocationOutcomeUnknown,
						ErrComputerInputCancellationConfirmed,
						computer.ErrStale,
					)
				}
				return result, err
			}
			ledger := newMemoryInvocationLedger()
			runtime := fixtureComputerInputRuntime(t, host, ledger)
			plan, payload := fixtureComputerInputPlan(t, runtime, host)
			for range 2 {
				if _, err := runtime.InvokeWithEphemeral(
					t.Context(),
					plan,
					payload,
				); !errors.Is(
					err,
					ErrInvocationOutcomeUnknown,
				) ||
					strings.Contains(err.Error(), protectedComputerCanary) {
					t.Fatal("uncertain effects became known or leaked private detail", err)
				}
			}
			record, found, err := ledger.Lookup(plan.InvocationID)
			if err != nil || !found || record.State != nodes.InvocationUnknown || host.calls.Load() != 1 {
				t.Fatal("unknown effect lost durable no-replay state", err)
			}
		})
	}
}

func TestComputerInputRuntimeRestartDistinguishesAcceptedFromRunning(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "running"}[running], func(t *testing.T) {
			host := newFixtureComputerInputHost(t)
			path := filepath.Join(t.TempDir(), "invocations.json")
			ledger, err := NewFileInvocationLedger(path, DefaultInvocationLedgerLimit, DefaultInvocationLedgerBytes)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(ledger.Close)
			runtime := fixtureComputerInputRuntime(t, host, ledger)
			plan, payload := fixtureComputerInputPlan(t, runtime, host)
			if _, _, err = ledger.Accept(plan); err != nil {
				t.Fatal(err)
			}
			if running {
				if _, err = ledger.MarkRunning(plan.InvocationID); err != nil {
					t.Fatal(err)
				}
			}
			ledger.Close()
			reloaded, err := NewFileInvocationLedger(path, DefaultInvocationLedgerLimit, DefaultInvocationLedgerBytes)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reloaded.Close)
			after := fixtureComputerInputRuntime(t, host, reloaded)
			if !running {
				if _, err = after.Invoke(t.Context(), plan); err == nil || host.calls.Load() != 0 {
					t.Fatal("accepted restart manufactured missing protected text", err)
				}
			}
			_, err = after.InvokeWithEphemeral(t.Context(), plan, payload)
			if running {
				if !errors.Is(err, ErrInvocationOutcomeUnknown) || host.calls.Load() != 0 {
					t.Fatal("running restart retried unknown effects", err)
				}
			} else if err != nil || host.calls.Load() != 1 {
				t.Fatal("accepted invocation did not resume once with matching payload", err)
			}
		})
	}
}

func TestComputerInputRuntimeCancellationRequiresIndependentCleanupProof(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "context-only", true: "confirmed-cleanup"}[confirmed], func(t *testing.T) {
			host := newFixtureComputerInputHost(t)
			started := make(chan struct{})
			host.act = func(ctx context.Context, _ nodes.ComputerHostActRequest) (nodes.ComputerActResult, error) {
				close(started)
				<-ctx.Done()
				if confirmed {
					return nodes.ComputerActResult{}, ErrComputerInputCancellationConfirmed
				}
				return nodes.ComputerActResult{}, ctx.Err()
			}
			ledger := newMemoryInvocationLedger()
			runtime := fixtureComputerInputRuntime(t, host, ledger)
			plan, payload := fixtureComputerInputPlan(t, runtime, host)
			done := make(chan error, 1)
			go func() { _, err := runtime.InvokeWithEphemeral(t.Context(), plan, payload); done <- err }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("input did not start")
			}
			if _, err := runtime.Cancel(nodes.InvocationCancelRequest{InvocationID: plan.InvocationID}); err != nil {
				t.Fatal(err)
			}
			wantErr, wantState := ErrInvocationOutcomeUnknown, nodes.InvocationUnknown
			if confirmed {
				wantErr, wantState = ErrInvocationCanceled, nodes.InvocationCanceled
			}
			if err := <-done; !errors.Is(err, wantErr) {
				t.Fatal("cancellation misclassified", err)
			}
			record, found, err := ledger.Lookup(plan.InvocationID)
			if err != nil || !found || record.State != wantState || host.calls.Load() != 1 {
				t.Fatal("cancellation state was not retained", err)
			}
			if _, err = runtime.Invoke(t.Context(), plan); !errors.Is(err, wantErr) || host.calls.Load() != 1 {
				t.Fatal("canceled/uncertain invocation was reexecuted", err)
			}
		})
	}
}
