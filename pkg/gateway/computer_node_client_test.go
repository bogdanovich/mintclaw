package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	nodews "github.com/bogdanovich/mintclaw/pkg/nodes/ws"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

// This is a deterministic remote protocol fixture, not native qualification.
// Plan preparation, gateway dispatch persistence and registry approval are real.
type computerClientTestHandler struct {
	mu              sync.Mutex
	registration    nodes.Registration
	session         computer.Session
	skew            time.Duration
	commands        []string
	observations    int
	loseNextControl bool
	mutate          func(any)
	delay           time.Duration
}

func (*computerClientTestHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}
func (*computerClientTestHandler) Close(context.Context) error                  { return nil }
func (handler *computerClientTestHandler) WithPreparationAuthority(
	_ nodes.ID, _ string, command string,
	operation func(nodes.Registration, nodes.CommandApproval) error,
) (nodes.CommandApproval, error) {
	descriptor, err := handler.registration.ApprovedCommand(command)
	if err != nil {
		return nodes.CommandApproval{}, err
	}
	approval := nodes.CommandApproval{Descriptor: descriptor}
	return approval, operation(handler.registration, approval)
}

func (*computerClientTestHandler) Invocation(context.Context, nodes.ID, string) (nodes.InvocationRecord, error) {
	return nodes.InvocationRecord{}, nodes.ErrGatewayInvocationNotFound
}

func (*computerClientTestHandler) CancelInvocation(context.Context, nodes.ID, string) (nodes.InvocationRecord, error) {
	return nodes.InvocationRecord{}, nodes.ErrGatewayInvocationNotFound
}

func (handler *computerClientTestHandler) Invoke(
	ctx context.Context, _ nodes.ID, plan nodes.ExecutionPlan, _ json.RawMessage, commit func() error,
) (json.RawMessage, bool, error) {
	if err := commit(); err != nil {
		return nil, false, err
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.commands = append(handler.commands, plan.Command)
	if handler.delay > 0 {
		timer := time.NewTimer(handler.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, true, ctx.Err()
		}
	}
	var turn nodes.ComputerTurn
	if err := json.Unmarshal(plan.Input, &turn); err != nil {
		return nil, true, err
	}
	principal, err := turn.Principal(plan)
	if err != nil {
		return nil, true, err
	}
	profile := handler.registration.Snapshot.Catalog.Commands[0].ComputerProfiles[0]
	target := computer.Target{
		Alias:     profile.Alias,
		Placement: computer.PlacementNode,
		Node:      string(handler.registration.Snapshot.ID),
		OS:        computer.OSLinux,
		Mode:      computer.ModeAttached,
		Isolation: computer.IsolationNone,
		Backend:   "fixture",
		Scope: computer.Scope{
			DesktopID:        "fixture-desktop",
			DisplayID:        "fixture-display",
			WindowID:         "fixture-window",
			WindowGeneration: 1,
		},
	}
	now := time.Now().Add(handler.skew)
	var result any
	switch plan.Command {
	case nodes.ComputerCommandTargets:
		result = &nodes.ComputerTargetsResult{Targets: []computer.Target{target}, ObservedAt: now}
	case nodes.ComputerCommandOpen:
		handler.session = computer.Session{
			Binding: computer.Binding{
				Principal: principal, SessionID: "fixture-session",
				Target: target, WorkerID: "fixture-worker", PolicyRevision: profile.Revision, Generation: 1,
			},
			Control: computer.ControlAgent, ExpiresAt: now.Add(5 * time.Minute),
		}
		result = &nodes.ComputerSessionResult{Session: handler.session, ObservedAt: now}
	case nodes.ComputerCommandStatus:
		result = &nodes.ComputerSessionResult{Session: handler.session, ObservedAt: now}
	case nodes.ComputerCommandObserve:
		handler.observations++
		result = &nodes.ComputerObservationResult{
			Session: handler.session, ObservedAt: now,
			Observation: computer.Observation{
				ID:      computerNodeID("observation", plan.InvocationID),
				Binding: handler.session.Binding,
				Geometry: computer.Geometry{
					Width:  2,
					Height: 2,
					Scale:  1,
				},
				CapturedAt: now,
				ExpiresAt:  now.Add(20 * time.Second),
			},
			AccessibilityText: "private-fixture-canary",
		}
	default:
		if handler.loseNextControl {
			handler.loseNextControl = false
			return nil, true, nodes.NewInvocationDispatchError(
				nodes.InvocationDispatchUnknown,
				errors.New("private-provider-canary"),
			)
		}
		event := map[string]computer.ControlEvent{
			nodes.ComputerCommandHandoff:       computer.EventHandoff,
			nodes.ComputerCommandRequestResume: computer.EventRequestResume, nodes.ComputerCommandResume: computer.EventResume,
			nodes.ComputerCommandRelease: computer.EventRelease,
		}[plan.Command]
		next, err := handler.session.Transition(event, handler.session.Binding.Generation)
		if err != nil {
			return nil, true, err
		}
		handler.session = next
		result = &nodes.ComputerSessionResult{Session: next, ObservedAt: now}
	}
	if handler.mutate != nil {
		handler.mutate(result)
	}
	raw, err := json.Marshal(result)
	return raw, true, err
}

func computerClientPrincipal() runtimecap.Principal {
	return runtimecap.Principal{
		Runtime: runtimecap.KindGateway, ActorID: "telegram:owner", AgentID: "browser",
		SessionID: "thread", ExecutionID: "turn",
	}
}

func computerClientFixture(t *testing.T, skew time.Duration) (*nodeComputerClient, *computerClientTestHandler) {
	t.Helper()
	client, handler, _ := computerClientFixtureConfig(t, skew)
	return client, handler
}

func computerClientFixtureConfig(
	t *testing.T,
	skew time.Duration,
) (*nodeComputerClient, *computerClientTestHandler, *config.Config) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := nodes.DeriveID(public)
	if err != nil {
		t.Fatal(err)
	}
	profile := nodes.ComputerProfileDescriptor{
		Alias: "selected", Revision: strings.Repeat("a", 64), OS: computer.OSLinux,
		Mode: computer.ModeAttached, Isolation: computer.IsolationNone, Backend: "fixture",
	}
	descriptors, err := nodes.ComputerCommandDescriptors([]nodes.ComputerProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	catalog := nodes.CapabilityCatalog{Commands: descriptors}
	hash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	registry, err := nodes.NewFileRegistry(nodes.RegistryPath(workspace), 8)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := nodes.Snapshot{
		ID: id, State: nodes.StatePendingPairing, ProtocolVersion: nodes.ProtocolVersion,
		Platform: "linux", Architecture: "amd64", SoftwareVersion: "fixture", CatalogHash: hash, Catalog: catalog,
		Executor: "local", PolicyRevision: "fixture-policy", LastSeenAt: time.Now().Unix(),
	}
	if err := registry.UpsertPending(nodes.PendingPairing{
		Node: snapshot, PublicKey: public,
		KeyAlgorithm: nodes.KeyAlgorithmEd25519, RequestedRole: "companion", RequestedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	commands := make([]string, len(descriptors))
	for i, descriptor := range descriptors {
		commands[i] = descriptor.Name
	}
	if _, err := registry.Approve(id, nodes.PairingApproval{
		Aliases:         []nodes.Alias{"native-companion"},
		AllowedCommands: commands, At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot.State = nodes.StateConnected
	if err := registry.Upsert(snapshot); err != nil {
		t.Fatal(err)
	}
	registration, _, err := registry.Registration(id)
	if err != nil {
		t.Fatal(err)
	}
	hub := nodews.NewSessionHub()
	release, err := hub.Claim(id, &testNodeConnection{}, nil, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = release() })
	handler := &computerClientTestHandler{registration: registration, skew: skew}
	runtime := &nodeAdmissionRuntime{
		registry: registry, registryPath: nodes.RegistryPath(workspace), sessions: hub,
		handler: handler, generation: 1, mounted: true,
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{"native": {Type: "node", Node: "native-companion"}}
	cfg.Tools.Computer = config.ComputerToolsConfig{Enabled: true, Targets: map[string]config.ComputerTargetConfig{
		"desktop": {
			NodeTarget: "native",
			Profile:    "selected",
			Revision:   profile.Revision,
			Actors: []string{
				"telegram:owner",
			},
			Agents:   []string{"browser"},
			Runtimes: []runtimecap.Kind{runtimecap.KindGateway},
			// Use the normal lease for the schema/persistence-heavy lifecycle.
			// Expiry is tested explicitly without depending on machine speed.
			LeaseSeconds:       0,
			ObservationSeconds: 5,
		},
	}}
	client, err := newNodeComputerClient(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	return client, handler, cfg
}

func openComputerClientFixture(t *testing.T, client *nodeComputerClient) computer.Session {
	t.Helper()
	session, err := client.Open(t.Context(), computerClientPrincipal(), "desktop", "open")
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestComputerNodeClientSameResourceAndGatewayClock(t *testing.T) {
	for _, skew := range []time.Duration{-36 * time.Hour, 0, 36 * time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			client, handler := computerClientFixture(t, skew)
			principal := computerClientPrincipal()
			targets, err := client.Targets(t.Context(), principal, "targets")
			if err != nil || len(targets) != 1 || targets[0].Alias != "desktop" {
				t.Fatal("explicit discovery failed", err)
			}
			start := time.Now()
			opened := openComputerClientFixture(t, client)
			if !opened.ExpiresAt.After(start) ||
				opened.ExpiresAt.After(time.Now().Add(client.policy.Targets["desktop"].LeaseTTL())) {
				t.Fatal("remote wall clock extended gateway lease")
			}
			first, err := client.Observe(t.Context(), principal, opened.Binding.SessionID, "observe-first")
			if err != nil || first.Validate(opened, time.Now()) != nil ||
				first.AccessibilityText != "private-fixture-canary" {
				t.Fatal("fresh protected observation failed", err)
			}
			for i, event := range []computer.ControlEvent{computer.EventHandoff, computer.EventRequestResume, computer.EventResume} {
				key := []string{"handoff", "request-resume", "resume"}[i]
				session, err := client.Control(t.Context(), principal, opened.Binding.SessionID, key, event)
				if err != nil || !session.ExpiresAt.Equal(opened.ExpiresAt) {
					t.Fatal("same-resource control failed", err)
				}
			}
			second, err := client.Observe(t.Context(), principal, opened.Binding.SessionID, "observe-second")
			if err != nil || first.ID == second.ID || second.Binding.WorkerID != first.Binding.WorkerID ||
				second.Binding.Target != first.Binding.Target || handler.observations != 2 {
				t.Fatal("fresh resume rebound resource", err)
			}
			status, err := client.Status(t.Context(), principal, opened.Binding.SessionID, "status")
			if err != nil || !status.ExpiresAt.Equal(opened.ExpiresAt) {
				t.Fatal("status refreshed gateway lease", err)
			}
			released, err := client.Control(
				t.Context(),
				principal,
				opened.Binding.SessionID,
				"release",
				computer.EventRelease,
			)
			if err != nil || released.Control != computer.ControlReleased {
				t.Fatal("release failed", err)
			}
		})
	}
}

func TestComputerEvidenceDeadlineAnchorsRequestStartNotRemoteOrResponseClock(t *testing.T) {
	start := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, skew := range []time.Duration{-36 * time.Hour, 0, 36 * time.Hour} {
		remote := start.Add(skew)
		if deadline := computerEvidenceDeadline(
			start,
			remote,
			remote.Add(time.Minute),
			5*time.Second,
		); !deadline.Equal(
			start.Add(5 * time.Second),
		) {
			t.Fatal("remote skew changed local budget")
		}
		if deadline := computerEvidenceDeadline(
			start,
			remote,
			remote.Add(2*time.Second),
			5*time.Second,
		); !deadline.Equal(
			start.Add(2 * time.Second),
		) {
			t.Fatal("short remote lease was extended")
		}
		if deadline := computerEvidenceDeadline(
			start,
			remote,
			remote.Add(-time.Second),
			5*time.Second,
		); !deadline.Before(
			start,
		) {
			t.Fatal("expired remote evidence acquired a fresh budget")
		}
	}
}

func TestComputerNodeClientUnknownControlCannotBeReplayedAfterUnchangedStatus(t *testing.T) {
	client, handler := computerClientFixture(t, 0)
	principal := computerClientPrincipal()
	opened := openComputerClientFixture(t, client)
	handler.loseNextControl = true
	_, err := client.Control(t.Context(), principal, opened.Binding.SessionID, "handoff", computer.EventHandoff)
	if !errors.Is(err, errComputerOutcomeUnknown) || strings.Contains(err.Error(), "private-provider-canary") {
		t.Fatal("uncertain control leaked or became retryable", err)
	}
	if _, err := client.Status(
		t.Context(),
		principal,
		opened.Binding.SessionID,
		"status-before-late-commit",
	); err != nil {
		t.Fatal(err)
	}
	count := len(handler.commands)
	if _, err := client.Control(
		t.Context(),
		principal,
		opened.Binding.SessionID,
		"another-handoff",
		computer.EventHandoff,
	); !errors.Is(err, errComputerOutcomeUnknown) ||
		len(handler.commands) != count {
		t.Fatal("unchanged status permitted repeated mutation", err)
	}
	handler.session, err = handler.session.Transition(computer.EventHandoff, handler.session.Binding.Generation)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(t.Context(), principal, opened.Binding.SessionID, "status-after-late-commit")
	if err != nil || status.Control != computer.ControlHuman {
		t.Fatal("status did not reconcile exact pending transition", err)
	}
	if _, err := client.Control(
		t.Context(),
		principal,
		opened.Binding.SessionID,
		"request-resume",
		computer.EventRequestResume,
	); err != nil {
		t.Fatal(err)
	}
}

func TestComputerNodeClientRejectsNewOwnerAndReplacementEvidence(t *testing.T) {
	client, handler := computerClientFixture(t, 0)
	opened := openComputerClientFixture(t, client)
	for _, change := range []func(*runtimecap.Principal){
		func(p *runtimecap.Principal) { p.ActorID = "other" }, func(p *runtimecap.Principal) { p.AgentID = "other" },
		func(p *runtimecap.Principal) { p.SessionID = "other" }, func(p *runtimecap.Principal) { p.ExecutionID = "other" },
		func(p *runtimecap.Principal) { p.Runtime = runtimecap.KindCoding },
	} {
		forged := computerClientPrincipal()
		change(&forged)
		count := len(handler.commands)
		if _, err := client.Status(
			t.Context(),
			forged,
			opened.Binding.SessionID,
			"forged",
		); err == nil ||
			len(handler.commands) != count {
			t.Fatal("new principal acquired old native session")
		}
	}
	handler.mutate = func(raw any) {
		if result, ok := raw.(*nodes.ComputerObservationResult); ok {
			result.Session.Binding.WorkerID = "replacement-worker"
			result.Observation.Binding.WorkerID = "replacement-worker"
		}
	}
	if _, err := client.Observe(
		t.Context(),
		computerClientPrincipal(),
		opened.Binding.SessionID,
		"replacement",
	); err == nil ||
		strings.Contains(err.Error(), "private-fixture-canary") {
		t.Fatal("replacement evidence admitted or protected payload leaked", err)
	}
}

func TestComputerNodeClientCannotExtendLocalLeaseOrCaptureAfterExpiry(t *testing.T) {
	client, handler := computerClientFixture(t, 36*time.Hour)
	opened := openComputerClientFixture(t, client)
	entry := client.sessions[opened.Binding.SessionID]
	entry.deadline = time.Now().Add(-time.Second)
	count := len(handler.commands)
	if _, err := client.Observe(
		t.Context(),
		computerClientPrincipal(),
		opened.Binding.SessionID,
		"expired",
	); !errors.Is(err, computer.ErrExpired) ||
		len(handler.commands) != count {
		t.Fatal("expired local lease reached capture", err)
	}
	if _, err := client.Control(
		t.Context(),
		computerClientPrincipal(),
		opened.Binding.SessionID,
		"release-expired",
		computer.EventRelease,
	); err != nil {
		t.Fatal("expiry prevented bounded authority release", err)
	}
}

func TestComputerNodeClientStatusRejectsUnrequestedControlButAcceptsNativeLoss(t *testing.T) {
	client, handler := computerClientFixture(t, 0)
	principal := computerClientPrincipal()
	opened := openComputerClientFixture(t, client)
	human, err := handler.session.Transition(computer.EventHandoff, handler.session.Binding.Generation)
	if err != nil {
		t.Fatal(err)
	}
	handler.session = human
	if _, err := client.Status(
		t.Context(),
		principal,
		opened.Binding.SessionID,
		"unsolicited-handoff",
	); !errors.Is(
		err,
		computer.ErrStale,
	) {
		t.Fatal("status accepted an unsolicited ownership transition", err)
	}
	lost, err := opened.Transition(computer.EventLose, opened.Binding.Generation)
	if err != nil {
		t.Fatal(err)
	}
	// The wire retains the remote clock domain, not the gateway projection.
	lost.ExpiresAt = handler.session.ExpiresAt
	handler.session = lost
	status, err := client.Status(t.Context(), principal, opened.Binding.SessionID, "native-loss")
	if err != nil || status.Control != computer.ControlLost || status.Binding.WorkerID != opened.Binding.WorkerID ||
		status.Binding.Target != opened.Binding.Target {
		t.Fatal("native loss did not revoke the original resource", err)
	}
	count := len(handler.commands)
	if _, err := client.Observe(
		t.Context(),
		principal,
		opened.Binding.SessionID,
		"observe-lost",
	); err == nil ||
		len(handler.commands) != count {
		t.Fatal("lost session dispatched another capture", err)
	}
}
