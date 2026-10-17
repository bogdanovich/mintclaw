package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

type fixtureComputerHost struct {
	core              *computer.ReadOnlyHost
	profile           nodes.ComputerProfileDescriptor
	observed          int
	opened            int
	disconnected      int
	observeError      error
	mutateObservation func(*nodes.ComputerObservationResult)
	forgeOpenOwner    bool
}

func newFixtureComputerHost(t *testing.T) *fixtureComputerHost {
	t.Helper()
	profile := nodes.ComputerProfileDescriptor{
		Alias: "desktop", Revision: strings.Repeat("a", 64), OS: computer.OSLinux,
		Mode: computer.ModeAttached, Isolation: computer.IsolationNone, Backend: "fixture",
	}
	target := computer.Target{
		Alias: profile.Alias, Placement: computer.PlacementNode, Node: "node_test", OS: profile.OS,
		Mode: profile.Mode, Isolation: profile.Isolation, Backend: profile.Backend,
		Scope: computer.Scope{DesktopID: "desktop", DisplayID: "display", WindowID: "window", WindowGeneration: 7},
	}
	host := &fixtureComputerHost{profile: profile}
	core, err := computer.NewReadOnlyHost(computer.ReadOnlyPolicy{
		WorkerID: "worker", Revision: profile.Revision, Targets: []computer.Target{target},
		LeaseTTL: time.Minute, ObservationTTL: 10 * time.Second,
		Admit: func(principal runtimecap.Principal, _ computer.Target) bool {
			return principal.ActorID == "actor_test" && principal.AgentID == "agent_test"
		},
	}, fixtureComputerCapture{}, computer.NewArbiter())
	if err != nil {
		t.Fatal(err)
	}
	host.core = core
	return host
}

type fixtureComputerCapture struct{}

func (fixtureComputerCapture) Capture(_ context.Context, target computer.Target) (computer.Capture, error) {
	return computer.Capture{
		Scope: target.Scope, Geometry: computer.Geometry{Width: 10, Height: 20, Scale: 1},
		AccessibilityText: "private fixture computer canary",
	}, nil
}

func (host *fixtureComputerHost) ComputerProfiles() []nodes.ComputerProfileDescriptor {
	return []nodes.ComputerProfileDescriptor{host.profile}
}

func (host *fixtureComputerHost) Targets(
	ctx context.Context,
	principal runtimecap.Principal,
) (nodes.ComputerTargetsResult, error) {
	targets, err := host.core.Targets(ctx, principal)
	return nodes.ComputerTargetsResult{Targets: targets, ObservedAt: time.Now()}, err
}

func (host *fixtureComputerHost) Open(
	ctx context.Context,
	request nodes.ComputerHostOpenRequest,
) (nodes.ComputerSessionResult, error) {
	host.opened++
	targets, err := host.core.Targets(ctx, request.Principal)
	if err != nil || len(targets) != 1 || request.Profile != host.profile.Alias ||
		request.ProfileRevision != host.profile.Revision {
		return nodes.ComputerSessionResult{}, computer.ErrDenied
	}
	session, err := host.core.Open(ctx, computer.OpenRequest{Principal: request.Principal, Target: targets[0]})
	if host.forgeOpenOwner {
		session.Binding.Principal.ExecutionID = "different-turn"
	}
	return nodes.ComputerSessionResult{Session: session, ObservedAt: time.Now()}, err
}

func (host *fixtureComputerHost) Status(
	ctx context.Context,
	request nodes.ComputerHostStatusRequest,
) (nodes.ComputerSessionResult, error) {
	session, err := host.core.Status(ctx, request.Principal, request.SessionID)
	return nodes.ComputerSessionResult{Session: session, ObservedAt: time.Now()}, err
}

func (host *fixtureComputerHost) Control(
	ctx context.Context,
	request nodes.ComputerHostControlRequest,
) (nodes.ComputerSessionResult, error) {
	status, err := host.Status(ctx, request.ComputerHostStatusRequest)
	if err != nil {
		return nodes.ComputerSessionResult{}, err
	}
	if status.Session.Binding.Generation != request.Generation {
		return nodes.ComputerSessionResult{}, computer.ErrStale
	}
	var session computer.Session
	switch request.Event {
	case computer.EventHandoff:
		session, err = host.core.Handoff(ctx, status.Session.Binding)
	case computer.EventRequestResume:
		session, err = host.core.RequestResume(ctx, status.Session.Binding)
	case computer.EventResume:
		session, err = host.core.Resume(ctx, status.Session.Binding)
	case computer.EventRelease:
		session, err = host.core.Release(ctx, status.Session.Binding)
	default:
		return nodes.ComputerSessionResult{}, computer.ErrInvalid
	}
	return nodes.ComputerSessionResult{Session: session, ObservedAt: time.Now()}, err
}

func (host *fixtureComputerHost) Observe(
	ctx context.Context,
	request nodes.ComputerHostObserveRequest,
) (nodes.ComputerObservationResult, error) {
	host.observed++
	if host.observeError != nil {
		return nodes.ComputerObservationResult{}, host.observeError
	}
	status, err := host.Status(ctx, request.ComputerHostStatusRequest)
	if err != nil {
		return nodes.ComputerObservationResult{}, err
	}
	if status.Session.Binding.Generation != request.Generation {
		return nodes.ComputerObservationResult{}, computer.ErrStale
	}
	observation, err := host.core.Observe(ctx, status.Session)
	if err != nil {
		return nodes.ComputerObservationResult{}, err
	}
	text := observation.AccessibilityText
	observation.AccessibilityText = ""
	result := nodes.ComputerObservationResult{
		Session: status.Session, Observation: observation, AccessibilityText: text, ObservedAt: time.Now(),
	}
	if host.mutateObservation != nil {
		host.mutateObservation(&result)
	}
	return result, nil
}
func (host *fixtureComputerHost) Disconnect(context.Context) error { host.disconnected++; return nil }

func fixtureComputerRuntime(
	t *testing.T,
	host ComputerCommandHost,
	ledger *InvocationLedger,
) (*Runtime, func(string, any) nodes.ExecutionPlan) {
	t.Helper()
	descriptors, err := nodes.ComputerCommandDescriptors(host.ComputerProfiles())
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	for _, descriptor := range descriptors {
		commands = append(commands, descriptor.Name)
	}
	policy := testRuntimePolicy(commands)
	policy.MaximumRisk = nodes.RiskWrite
	runtime, err := NewRuntime("node_test", "test", policy, ledger, WithComputerHost(host))
	if err != nil {
		t.Fatal(err)
	}
	sequence := 0
	return runtime, func(command string, input any) nodes.ExecutionPlan {
		t.Helper()
		sequence++
		var descriptor nodes.CommandDescriptor
		catalog := runtime.Catalog()
		for _, candidate := range catalog.Commands {
			if candidate.Name == command {
				descriptor = candidate
			}
		}
		hash, hashErr := catalog.Hash()
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		raw, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		plan, planErr := nodes.PrepareExecutionPlan(nodes.InvocationRequest{
			InvocationID: fmt.Sprintf(
				"computer-%d",
				sequence,
			),
			IdempotencyKey:   fmt.Sprintf("computer-idem-%d", sequence),
			NodeID:           runtime.nodeID,
			CatalogHash:      hash,
			Command:          command,
			Input:            raw,
			ActorID:          "actor_test",
			AgentID:          "agent_test",
			SessionID:        "session_test",
			TimeoutSeconds:   5,
			OutputLimitBytes: 32 * 1024,
		}, descriptor, LocalExecutor, policy.Revision, time.Now(), time.Minute)
		if planErr != nil {
			t.Fatal(planErr)
		}
		return plan
	}
}

func computerTestTurn() nodes.ComputerTurn {
	return nodes.ComputerTurn{Runtime: runtimecap.KindGateway, ExecutionID: "original-turn"}
}

func TestComputerRuntimeDormantWithoutExplicitHost(t *testing.T) {
	runtime, err := NewRuntime("node_test", "test", testRuntimePolicy(nil), newMemoryInvocationLedger())
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range runtime.Catalog().Commands {
		if nodes.IsComputerCommand(descriptor.Name) {
			t.Fatal("computer capability registered by default")
		}
	}
	if _, err = NewRuntime(
		"node_test",
		"test",
		testRuntimePolicy(nil),
		newMemoryInvocationLedger(),
		WithComputerHost(nil),
	); err == nil {
		t.Fatal("nil opt-in host admitted")
	}
}

func TestComputerRuntimeLifecycleAndLiveOnlyObservationLedger(t *testing.T) {
	host := newFixtureComputerHost(t)
	path := filepath.Join(t.TempDir(), "invocations.json")
	ledger, err := NewFileInvocationLedger(path, DefaultInvocationLedgerLimit, DefaultInvocationLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.Close)
	runtime, plan := fixtureComputerRuntime(t, host, ledger)
	turn := computerTestTurn()
	raw, err := runtime.Invoke(t.Context(), plan(nodes.ComputerCommandOpen, nodes.ComputerOpenInput{
		ComputerTurn: turn, Profile: host.profile.Alias, ProfileRevision: host.profile.Revision,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var opened nodes.ComputerSessionResult
	if json.Unmarshal(raw, &opened) != nil {
		t.Fatal("invalid open result")
	}
	session := opened.Session
	observe := func() nodes.ExecutionPlan {
		return plan(nodes.ComputerCommandObserve, nodes.ComputerObserveInput{
			ComputerControlInput: nodes.ComputerControlInput{
				ComputerTurn: turn,
				SessionID:    session.Binding.SessionID,
				Generation:   session.Binding.Generation,
			},
			WorkspaceID: "workspace",
		})
	}
	first := observe()
	raw, err = runtime.Invoke(t.Context(), first)
	if err != nil || !strings.Contains(string(raw), "private fixture computer canary") {
		t.Fatal("live observation missing", err)
	}
	var originalObservation nodes.ComputerObservationResult
	if json.Unmarshal(raw, &originalObservation) != nil {
		t.Fatal("invalid original observation")
	}
	retained, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(retained), "private fixture computer canary") {
		t.Fatal("private observation persisted", err)
	}
	raw, err = runtime.Invoke(t.Context(), first)
	if err != nil || string(raw) != `{"protected_result":true}` || host.observed != 1 {
		t.Fatal("replay recaptured or retained page data", err)
	}
	for _, command := range []string{nodes.ComputerCommandHandoff, nodes.ComputerCommandRequestResume, nodes.ComputerCommandResume} {
		raw, err = runtime.Invoke(t.Context(), plan(command, nodes.ComputerControlInput{
			ComputerTurn: turn, SessionID: session.Binding.SessionID, Generation: session.Binding.Generation,
		}))
		if err != nil || json.Unmarshal(raw, &opened) != nil {
			t.Fatal(command, err)
		}
		session = opened.Session
	}
	raw, err = runtime.Invoke(t.Context(), observe())
	var resumedObservation nodes.ComputerObservationResult
	if err != nil || host.observed != 2 || json.Unmarshal(raw, &resumedObservation) != nil ||
		resumedObservation.Observation.ID == originalObservation.Observation.ID ||
		resumedObservation.Observation.Binding.Target != originalObservation.Observation.Binding.Target ||
		resumedObservation.Observation.Binding.WorkerID != originalObservation.Observation.Binding.WorkerID ||
		resumedObservation.Observation.Binding.SessionID != originalObservation.Observation.Binding.SessionID ||
		resumedObservation.Observation.Binding.Generation != session.Binding.Generation {
		t.Fatal("fresh same-session observe after resume failed", err)
	}
	_, err = runtime.Invoke(t.Context(), plan(nodes.ComputerCommandRelease, nodes.ComputerControlInput{
		ComputerTurn: turn, SessionID: session.Binding.SessionID, Generation: session.Binding.Generation,
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range runtime.Catalog().Commands {
		if nodes.IsComputerCommand(descriptor.Name) && descriptor.ModelContract != nil {
			t.Fatal("generic computer model contract manufactured")
		}
	}
}

func TestComputerRuntimeSanitizesHostFailuresAndRejectsWrongTurnResults(t *testing.T) {
	for _, kind := range []string{"error", "wrong-turn", "invalid-text"} {
		t.Run(kind, func(t *testing.T) {
			host := newFixtureComputerHost(t)
			runtime, plan := fixtureComputerRuntime(t, host, newMemoryInvocationLedger())
			turn := computerTestTurn()
			raw, err := runtime.Invoke(t.Context(), plan(nodes.ComputerCommandOpen, nodes.ComputerOpenInput{
				ComputerTurn: turn, Profile: host.profile.Alias, ProfileRevision: host.profile.Revision,
			}))
			if err != nil {
				t.Fatal(err)
			}
			var opened nodes.ComputerSessionResult
			if json.Unmarshal(raw, &opened) != nil {
				t.Fatal("invalid open result")
			}
			switch kind {
			case "error":
				host.observeError = errors.New("private error canary")
			case "wrong-turn":
				host.mutateObservation = func(result *nodes.ComputerObservationResult) {
					result.Session.Binding.Principal.ExecutionID = "other-turn"
				}
			case "invalid-text":
				host.mutateObservation = func(result *nodes.ComputerObservationResult) { result.AccessibilityText = string([]byte{0xff}) }
			}
			_, err = runtime.Invoke(t.Context(), plan(nodes.ComputerCommandObserve, nodes.ComputerObserveInput{
				ComputerControlInput: nodes.ComputerControlInput{
					ComputerTurn: turn,
					SessionID:    opened.Session.Binding.SessionID,
					Generation:   1,
				},
				WorkspaceID: "workspace",
			}))
			if err == nil || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "\xff") {
				t.Fatal("private error or forged observation escaped", err)
			}
		})
	}
}

func TestComputerRuntimeDoesNotReexecuteUncertainOpen(t *testing.T) {
	host := newFixtureComputerHost(t)
	host.forgeOpenOwner = true
	ledger := newMemoryInvocationLedger()
	runtime, plan := fixtureComputerRuntime(t, host, ledger)
	open := plan(nodes.ComputerCommandOpen, nodes.ComputerOpenInput{
		ComputerTurn: computerTestTurn(), Profile: host.profile.Alias, ProfileRevision: host.profile.Revision,
	})
	for range 2 {
		if _, err := runtime.Invoke(t.Context(), open); !errors.Is(err, ErrInvocationOutcomeUnknown) {
			t.Fatal("unconfirmed ownership change was not retained as uncertain", err)
		}
	}
	if host.opened != 1 {
		t.Fatal("uncertain open was reexecuted")
	}
}

func TestComputerRuntimePreservesLocalPolicyAndFullTurnOwnership(t *testing.T) {
	host := newFixtureComputerHost(t)
	runtime, plan := fixtureComputerRuntime(t, host, newMemoryInvocationLedger())
	turn := computerTestTurn()
	open := plan(nodes.ComputerCommandOpen, nodes.ComputerOpenInput{
		ComputerTurn: turn, Profile: host.profile.Alias, ProfileRevision: host.profile.Revision,
	})
	raw, err := runtime.Invoke(t.Context(), open)
	if err != nil {
		t.Fatal(err)
	}
	var result nodes.ComputerSessionResult
	if json.Unmarshal(raw, &result) != nil {
		t.Fatal("invalid open result")
	}
	turn.ExecutionID = "different-turn"
	if _, err = runtime.Invoke(t.Context(), plan(nodes.ComputerCommandStatus, nodes.ComputerStatusInput{
		ComputerTurn: turn, SessionID: result.Session.Binding.SessionID,
	})); !errors.Is(err, nodes.ErrCommandDenied) {
		t.Fatal("another turn acquired original turn's computer session", err)
	}
	turn = computerTestTurn()
	observe := plan(nodes.ComputerCommandObserve, nodes.ComputerObserveInput{
		ComputerControlInput: nodes.ComputerControlInput{
			ComputerTurn: turn,
			SessionID:    result.Session.Binding.SessionID,
			Generation:   1,
		},
		WorkspaceID: "workspace",
	})
	runtime.policy.AllowedCommands = nil
	if _, err = runtime.Invoke(t.Context(), observe); !errors.Is(err, nodes.ErrCommandDenied) || host.observed != 0 {
		t.Fatal("computer authority bypassed local node command policy", err)
	}
	catalog := runtime.Catalog()
	for index := range catalog.Commands {
		if len(catalog.Commands[index].ComputerProfiles) != 0 {
			catalog.Commands[index].ComputerProfiles[0].Revision = "forged"
		}
	}
	if err = runtime.Catalog().Validate(); err != nil {
		t.Fatal("caller mutated internal computer catalog", err)
	}
}
