package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

var errComputerOutcomeUnknown = errors.New(
	"computer command outcome is unknown; query status without repeating the action",
)

const computerNodeRequestTimeout = 30 * time.Second

type computerNodeAuthority struct {
	alias          string
	policy         config.ComputerTargetConfig
	nodeRef        string
	nodeID         nodes.ID
	executor       string
	catalogHash    string
	policyRevision string
	profile        nodes.ComputerProfileDescriptor
}

type computerNodeSession struct {
	gate      chan struct{}
	authority computerNodeAuthority
	remote    computer.Session
	deadline  time.Time // Gateway clock, fixed at open; status never extends it.
	uncertain bool
	pending   *computer.Session
	terminal  atomic.Bool
}

// nodeComputerClient consumes only approved first-party commands. It is not
// registered as a public tool by constructing it. The companion remains the
// authority for control transitions; this client pins identity and conservatively
// bounds evidence in the gateway's own clock domain.
type nodeComputerClient struct {
	policy      config.ComputerToolsConfig
	execution   map[string]config.ExecutionTarget
	source      *nodeInvocationSource
	workspaceID string
	openGate    chan struct{}
	opening     map[computerOpenTarget]*computerOpenAttempt // Guarded by openGate.
	mu          sync.Mutex
	sessions    map[string]*computerNodeSession
}

func newNodeComputerClient(cfg *config.Config, runtime *nodeAdmissionRuntime) (*nodeComputerClient, error) {
	if cfg == nil || cfg.ValidateComputerConfig() != nil || !cfg.Tools.Computer.Enabled {
		return nil, computer.ErrDenied
	}
	source, err := newNodeInvocationSource(cfg, runtime)
	if err != nil || source == nil {
		return nil, computer.ErrUnavailable
	}
	encoded, err := json.Marshal(cfg.Tools.Computer)
	if err != nil {
		return nil, computer.ErrInvalid
	}
	var policy config.ComputerToolsConfig
	if json.Unmarshal(encoded, &policy) != nil {
		return nil, computer.ErrInvalid
	}
	return &nodeComputerClient{
		policy: policy, execution: maps.Clone(cfg.Execution.Targets), source: source,
		workspaceID: computerNodeID("workspace", cfg.WorkspacePath()),
		openGate:    make(chan struct{}, 1), opening: make(map[computerOpenTarget]*computerOpenAttempt),
		sessions: make(map[string]*computerNodeSession),
	}, nil
}

func computerNodeID(kind string, values ...string) string {
	encoded, _ := json.Marshal(values)
	digest := sha256.Sum256(encoded)
	return kind + "_" + hex.EncodeToString(digest[:])
}

func (client *nodeComputerClient) authority(
	alias string, principal runtimecap.Principal, pinned *computerNodeAuthority,
) (computerNodeAuthority, error) {
	policy, found := client.policy.Targets[alias]
	if !found || !client.policy.Enabled || !policy.Admits(principal) {
		return computerNodeAuthority{}, computer.ErrDenied
	}
	if _, err := client.source.runtime.invocationHandlerSnapshot(
		client.source.registryPath,
		client.source.generation,
	); err != nil {
		return computerNodeAuthority{}, computer.ErrStale
	}
	execution := client.execution[policy.NodeTarget]
	record, found, err := client.source.Lookup(execution.Node)
	if err != nil || !found || !record.Connected || record.Registration == nil ||
		record.Snapshot.State != nodes.StateConnected || nodes.ValidateProtocolVersion(record.Snapshot.ProtocolVersion) != nil {
		return computerNodeAuthority{}, computer.ErrUnavailable
	}
	authority := computerNodeAuthority{
		alias: alias, policy: policy, nodeRef: execution.Node, nodeID: record.Snapshot.ID,
		executor: record.Snapshot.Executor, catalogHash: record.Snapshot.CatalogHash,
		policyRevision: record.Snapshot.PolicyRevision,
	}
	for _, command := range []string{
		nodes.ComputerCommandTargets, nodes.ComputerCommandOpen, nodes.ComputerCommandStatus,
		nodes.ComputerCommandObserve, nodes.ComputerCommandHandoff, nodes.ComputerCommandRequestResume,
		nodes.ComputerCommandResume, nodes.ComputerCommandRelease,
	} {
		descriptor, approved := computerApprovedDescriptor(record, command)
		if !approved {
			return computerNodeAuthority{}, computer.ErrDenied
		}
		var profile nodes.ComputerProfileDescriptor
		for _, candidate := range descriptor.ComputerProfiles {
			if candidate.Alias == policy.Profile && candidate.Revision == policy.Revision {
				profile = candidate
			}
		}
		if profile.Alias == "" || authority.profile.Alias != "" && authority.profile != profile {
			return computerNodeAuthority{}, computer.ErrDenied
		}
		authority.profile = profile
	}
	if pinned != nil && (pinned.nodeID != authority.nodeID || pinned.executor != authority.executor ||
		pinned.catalogHash != authority.catalogHash || pinned.policyRevision != authority.policyRevision ||
		pinned.profile != authority.profile) {
		return computerNodeAuthority{}, computer.ErrStale
	}
	return authority, nil
}

func (client *nodeComputerClient) invoke(
	ctx context.Context, authority computerNodeAuthority, principal runtimecap.Principal,
	requestID, command string, input, output any,
) (string, error) {
	if nodes.ID(requestID).Validate() != nil {
		return "", computer.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, computerNodeRequestTimeout)
	defer cancel()
	if _, err := client.authority(authority.alias, principal, &authority); err != nil {
		return "", err
	}
	record, found, err := client.source.Lookup(authority.nodeRef)
	if err != nil || !found || record.Registration == nil {
		return "", computer.ErrUnavailable
	}
	descriptor, approved := computerApprovedDescriptor(record, command)
	if !approved {
		return "", computer.ErrDenied
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", computer.ErrInvalid
	}
	id := client.invocationID(principal, authority.alias, command, requestID)
	callID := computerNodeID("call", id)
	plan, err := nodes.PrepareExecutionPlan(nodes.InvocationRequest{
		InvocationID: id, IdempotencyKey: id, NodeID: authority.nodeID, CatalogHash: authority.catalogHash,
		Command: command, Input: encoded, AgentID: principal.AgentID, ActorID: principal.ActorID,
		SessionID: principal.SessionID, TimeoutSeconds: 30, OutputLimitBytes: 64 * 1024,
	}, descriptor, authority.executor, authority.policyRevision, time.Now(), nodes.MaxExecutionPlanTTL)
	if err != nil {
		return id, computer.ErrDenied
	}
	gatewayPrincipal := client.invocationPrincipal(principal)
	retained, _, err := client.source.PrepareInvocation(
		authority.nodeRef, authority.policy.NodeTarget, callID, gatewayPrincipal, plan, descriptor, true,
		func(current tools.NodeDiscoveryRecord) error {
			if !current.Connected || current.Registration == nil || current.Snapshot.ID != authority.nodeID ||
				current.Snapshot.CatalogHash != authority.catalogHash || current.Snapshot.Executor != authority.executor ||
				current.Snapshot.PolicyRevision != authority.policyRevision {
				return computer.ErrStale
			}
			currentDescriptor, approved := computerApprovedDescriptor(current, command)
			expectedHash, expectedErr := descriptor.Hash()
			currentHash, hashErr := currentDescriptor.Hash()
			if !approved || expectedErr != nil || hashErr != nil || expectedHash != currentHash {
				return computer.ErrStale
			}
			return nil
		},
	)
	if err != nil {
		return id, computer.ErrStale
	}
	previous := retained.Plan
	expectedDescriptor, _ := descriptor.Hash()
	retainedDescriptor, descriptorErr := retained.Descriptor.Hash()
	if descriptorErr != nil || retainedDescriptor != expectedDescriptor || previous.NodeID != plan.NodeID ||
		previous.CatalogHash != plan.CatalogHash || previous.PolicyRevision != plan.PolicyRevision ||
		previous.Executor != plan.Executor || previous.Command != plan.Command ||
		previous.ActorID != plan.ActorID || previous.AgentID != plan.AgentID || previous.SessionID != plan.SessionID ||
		previous.InvocationID != plan.InvocationID || !bytes.Equal(previous.Input, plan.Input) ||
		retained.WorkspaceID != client.workspaceID || retained.ExecutionID != principal.ExecutionID {
		return id, computer.ErrDenied
	}
	if retained.State != nodes.GatewayInvocationPrepared {
		return id, errComputerOutcomeUnknown
	}
	raw, dispatched, err := client.source.DispatchInvocation(ctx, nodes.GatewayInvocationOwner{
		Target: authority.policy.NodeTarget, ToolCallID: callID, AgentID: principal.AgentID,
		ActorID: principal.ActorID, SessionID: principal.SessionID, WorkspaceID: client.workspaceID,
		ExecutionID: principal.ExecutionID,
	}, retained.Plan.InvocationID, retained.ExpectedPlanHash)
	if err != nil {
		return id, computerNodeFailure(err, dispatched)
	}
	validated, err := nodes.ValidateInvocationOutput(descriptor, raw, 64*1024)
	if err != nil || json.Unmarshal(validated, output) != nil {
		if descriptor.Risk == nodes.RiskWrite {
			return id, errComputerOutcomeUnknown
		}
		return id, computer.ErrStale
	}
	if _, err := client.authority(authority.alias, principal, &authority); err != nil {
		if descriptor.Risk == nodes.RiskWrite {
			return id, errComputerOutcomeUnknown
		}
		return id, err
	}
	return id, nil
}

func computerNodeFailure(err error, dispatched bool) error {
	code, typed := nodes.InvocationDispatchErrorCode(err)
	if typed {
		switch code {
		case nodes.InvocationDispatchCommandDenied:
			return computer.ErrDenied
		case nodes.InvocationDispatchComputerStale:
			return computer.ErrStale
		case nodes.InvocationDispatchComputerBusy:
			return computer.ErrBusy
		case nodes.InvocationDispatchComputerExpired:
			return computer.ErrExpired
		case nodes.InvocationDispatchComputerUnavailable:
			return computer.ErrUnavailable
		}
	}
	if dispatched || errors.Is(err, nodes.ErrGatewayInvocationDispatched) {
		return errComputerOutcomeUnknown
	}
	return computer.ErrUnavailable
}

func (client *nodeComputerClient) Targets(
	ctx context.Context, principal runtimecap.Principal, requestID string,
) ([]computer.NamedTarget, error) {
	aliases := slices.Sorted(maps.Keys(client.policy.Targets))
	targets := []computer.NamedTarget{}
	for _, alias := range aliases {
		if !client.policy.Targets[alias].Admits(principal) {
			continue
		}
		authority, err := client.authority(alias, principal, nil)
		if err != nil {
			return nil, err
		}
		var result nodes.ComputerTargetsResult
		if _, err := client.invoke(ctx, authority, principal, requestID, nodes.ComputerCommandTargets,
			nodes.ComputerTargetsInput{ComputerTurn: computerNodeTurn(principal)}, &result); err != nil {
			return nil, err
		}
		for _, target := range result.Targets {
			if target.Alias == authority.profile.Alias && target.Node == string(authority.nodeID) {
				targets = append(targets, computer.NamedTarget{Alias: alias, Target: target})
			}
		}
	}
	return targets, nil
}

func (client *nodeComputerClient) Open(
	ctx context.Context, principal runtimecap.Principal, alias, requestID string,
) (computer.Session, error) {
	start := time.Now()
	if err := computerNodeAcquire(ctx, client.openGate); err != nil {
		return computer.Session{}, err
	}
	defer computerNodeUnlock(client.openGate)
	authority, err := client.authority(alias, principal, nil)
	if err != nil {
		return computer.Session{}, err
	}
	key := computerOpenTarget{node: authority.nodeID, profile: authority.profile.Alias}
	if previous := client.opening[key]; previous != nil {
		recovered, reconcileErr := client.reconcileOpen(ctx, previous)
		if reconcileErr != nil {
			return computer.Session{}, errComputerOutcomeUnknown
		}
		if recovered != nil && !recovered.Control.Terminal() {
			return computer.Session{}, computer.ErrBusy
		}
	}
	client.mu.Lock()
	leased := false
	for id, entry := range client.sessions {
		if !entry.terminal.Load() && entry.authority.nodeID == authority.nodeID &&
			entry.authority.profile.Alias == authority.profile.Alias {
			leased = true
		}
		if len(client.sessions)+len(client.opening) >= 128 && entry.terminal.Load() {
			delete(client.sessions, id)
		}
	}
	full := len(client.sessions)+len(client.opening) >= 128
	client.mu.Unlock()
	if full || leased {
		return computer.Session{}, computer.ErrBusy
	}
	attempt := &computerOpenAttempt{
		authority: authority, principal: principal, started: start,
		invocationID: client.invocationID(principal, alias, nodes.ComputerCommandOpen, requestID),
	}
	client.opening[key] = attempt
	var result nodes.ComputerSessionResult
	_, err = client.invoke(ctx, authority, principal, requestID, nodes.ComputerCommandOpen, nodes.ComputerOpenInput{
		ComputerTurn: computerNodeTurn(
			principal,
		),
		Profile:         authority.profile.Alias,
		ProfileRevision: authority.profile.Revision,
	}, &result)
	if err != nil {
		if !errors.Is(err, errComputerOutcomeUnknown) {
			delete(client.opening, key)
			return computer.Session{}, err
		}
		recovered, reconcileErr := client.reconcileOpen(ctx, attempt)
		if reconcileErr != nil {
			return computer.Session{}, errComputerOutcomeUnknown
		}
		if recovered == nil {
			return computer.Session{}, computer.ErrUnavailable
		}
		return computerOpenResult(*recovered)
	}
	opened, err := client.adoptOpen(ctx, attempt, result)
	if err != nil {
		return computer.Session{}, errComputerOutcomeUnknown
	}
	delete(client.opening, key)
	return computerOpenResult(opened)
}

func (entry *computerNodeSession) projection() computer.Session {
	result := entry.remote
	result.ExpiresAt = entry.deadline
	return result
}

func (client *nodeComputerClient) locked(
	ctx context.Context, principal runtimecap.Principal, sessionID string,
) (*computerNodeSession, error) {
	client.mu.Lock()
	entry := client.sessions[sessionID]
	client.mu.Unlock()
	if entry == nil {
		return nil, computer.ErrDenied
	}
	if err := computerNodeAcquire(ctx, entry.gate); err != nil {
		return nil, err
	}
	if principal != entry.remote.Binding.Principal {
		computerNodeUnlock(entry.gate)
		return nil, computer.ErrDenied
	}
	return entry, nil
}

func (entry *computerNodeSession) accept(result nodes.ComputerSessionResult) error {
	next, previous := result.Session, entry.remote
	immutable := next.Binding
	immutable.Generation = previous.Binding.Generation
	if immutable != previous.Binding || !next.ExpiresAt.Equal(previous.ExpiresAt) ||
		next.Binding.Generation < previous.Binding.Generation ||
		previous.Control.Terminal() && (next.Control != previous.Control || next.Binding != previous.Binding) {
		return computer.ErrStale
	}
	if next.Binding.Generation == previous.Binding.Generation {
		if next.Control != previous.Control {
			return computer.ErrStale
		}
	} else if next.Binding.Generation-previous.Binding.Generation != 1 {
		return computer.ErrStale
	} else if next.Control != computer.ControlLost &&
		(entry.pending == nil || !sameComputerRemoteSession(next, *entry.pending)) {
		return computer.ErrStale
	}
	// An unchanged status does not prove an ambiguous in-flight transition
	// cannot still commit. Do not grant another mutation based on that race.
	if !entry.uncertain || next.Binding.Generation != previous.Binding.Generation || next.Control.Terminal() {
		entry.uncertain, entry.pending = false, nil
	}
	entry.remote = next
	entry.terminal.Store(next.Control.Terminal())
	return nil
}

func (client *nodeComputerClient) Status(
	ctx context.Context, principal runtimecap.Principal, sessionID, requestID string,
) (computer.Session, error) {
	entry, err := client.locked(ctx, principal, sessionID)
	if err != nil {
		return computer.Session{}, err
	}
	defer computerNodeUnlock(entry.gate)
	return client.status(ctx, entry, requestID)
}

func (client *nodeComputerClient) status(
	ctx context.Context, entry *computerNodeSession, requestID string,
) (computer.Session, error) {
	var result nodes.ComputerSessionResult
	_, err := client.invoke(
		ctx,
		entry.authority,
		entry.remote.Binding.Principal,
		requestID,
		nodes.ComputerCommandStatus,
		nodes.ComputerStatusInput{
			ComputerTurn: computerNodeTurn(entry.remote.Binding.Principal),
			SessionID:    entry.remote.Binding.SessionID,
		},
		&result,
	)
	if err != nil {
		return computer.Session{}, err
	}
	if err := entry.accept(result); err != nil {
		return computer.Session{}, err
	}
	return entry.projection(), nil
}

func (client *nodeComputerClient) Control(
	ctx context.Context, principal runtimecap.Principal, sessionID, requestID string, event computer.ControlEvent,
) (computer.Session, error) {
	entry, err := client.locked(ctx, principal, sessionID)
	if err != nil {
		return computer.Session{}, err
	}
	defer computerNodeUnlock(entry.gate)
	if entry.uncertain {
		return computer.Session{}, errComputerOutcomeUnknown
	}
	if event != computer.EventRelease && !time.Now().Before(entry.deadline) {
		return computer.Session{}, computer.ErrExpired
	}
	command := map[computer.ControlEvent]string{
		computer.EventHandoff: nodes.ComputerCommandHandoff, computer.EventRequestResume: nodes.ComputerCommandRequestResume,
		computer.EventResume: nodes.ComputerCommandResume, computer.EventRelease: nodes.ComputerCommandRelease,
	}[event]
	if command == "" {
		return computer.Session{}, computer.ErrDenied
	}
	expected, err := entry.remote.Transition(event, entry.remote.Binding.Generation)
	if err != nil {
		return computer.Session{}, err
	}
	var result nodes.ComputerSessionResult
	_, err = client.invoke(ctx, entry.authority, principal, requestID, command, nodes.ComputerControlInput{
		ComputerTurn: computerNodeTurn(principal), SessionID: sessionID, Generation: entry.remote.Binding.Generation,
	}, &result)
	entry.pending = &expected
	if err != nil {
		entry.uncertain = errors.Is(err, errComputerOutcomeUnknown)
		return computer.Session{}, err
	}
	if !sameComputerRemoteSession(result.Session, expected) || entry.accept(result) != nil {
		entry.uncertain = true
		return computer.Session{}, errComputerOutcomeUnknown
	}
	return entry.projection(), nil
}

func (client *nodeComputerClient) Observe(
	ctx context.Context, principal runtimecap.Principal, sessionID, requestID string,
) (computer.Observation, error) {
	start := time.Now()
	entry, err := client.locked(ctx, principal, sessionID)
	if err != nil {
		return computer.Observation{}, err
	}
	defer computerNodeUnlock(entry.gate)
	if entry.uncertain {
		return computer.Observation{}, errComputerOutcomeUnknown
	}
	if authorityErr := entry.projection().AgentAuthority(time.Now()); authorityErr != nil {
		return computer.Observation{}, authorityErr
	}
	var result nodes.ComputerObservationResult
	invocationID, err := client.invoke(ctx, entry.authority, principal, requestID, nodes.ComputerCommandObserve,
		nodes.ComputerObserveInput{ComputerControlInput: nodes.ComputerControlInput{
			ComputerTurn: computerNodeTurn(
				principal,
			),
			SessionID:  sessionID,
			Generation: entry.remote.Binding.Generation,
		}, WorkspaceID: client.workspaceID}, &result)
	if err != nil {
		return computer.Observation{}, err
	}
	if result.Validate([]nodes.ComputerProfileDescriptor{entry.authority.profile}) != nil ||
		result.Session.Binding != entry.remote.Binding || !result.Session.ExpiresAt.Equal(entry.remote.ExpiresAt) {
		return computer.Observation{}, computer.ErrStale
	}
	deadline := minComputerDeadline(entry.deadline, computerEvidenceDeadline(
		start, result.ObservedAt, result.Observation.ExpiresAt, entry.authority.policy.ObservationTTL(),
	))
	if !time.Now().Before(deadline) {
		return computer.Observation{}, computer.ErrExpired
	}
	observation := result.Observation
	observation.CapturedAt, observation.ExpiresAt = start, deadline
	observation.AccessibilityText = result.AccessibilityText
	if result.Output != nil {
		if result.Output.InvocationID != invocationID || result.Output.WorkspaceID != client.workspaceID {
			return computer.Observation{}, computer.ErrStale
		}
		transfer, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		observation.Screenshot, err = client.receiveImage(transfer, entry, *result.Output)
		if err != nil {
			return computer.Observation{}, err
		}
	}
	if err := observation.Validate(entry.projection(), time.Now()); err != nil {
		clear(observation.Screenshot)
		return computer.Observation{}, computer.ErrStale
	}
	return observation, nil
}

func computerNodeTurn(principal runtimecap.Principal) nodes.ComputerTurn {
	return nodes.ComputerTurn{Runtime: principal.Runtime, ExecutionID: principal.ExecutionID}
}

// Lookup and preparation supply a registry-validated snapshot. Check exact
// operator approval against that snapshot once, rather than revalidating the
// entire catalog for each command in the closed family.
func computerApprovedDescriptor(record tools.NodeDiscoveryRecord, command string) (nodes.CommandDescriptor, bool) {
	registration := record.Registration
	if registration == nil || registration.ApprovedAt <= 0 || !nodes.IsComputerCommand(command) ||
		registration.ApprovedCatalogHash != record.Snapshot.CatalogHash ||
		!slices.Contains(registration.AllowedCommands, command) {
		return nodes.CommandDescriptor{}, false
	}
	for _, descriptor := range record.Snapshot.Catalog.Commands {
		if descriptor.Name == command && descriptor.ModelContract == nil {
			return descriptor, true
		}
	}
	return nodes.CommandDescriptor{}, false
}

func sameComputerRemoteSession(first, second computer.Session) bool {
	return first.Binding == second.Binding && first.Control == second.Control && first.ExpiresAt.Equal(second.ExpiresAt)
}

func minComputerDeadline(first, second time.Time) time.Time {
	if second.Before(first) {
		return second
	}
	return first
}

func computerEvidenceDeadline(start, remoteObserved, remoteExpiry time.Time, ceiling time.Duration) time.Time {
	return start.Add(min(ceiling, remoteExpiry.Sub(remoteObserved)))
}

func computerNodeAcquire(ctx context.Context, gate chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func computerNodeUnlock(gate chan struct{}) { <-gate }

func (client *nodeComputerClient) OwnedSessions(
	ctx context.Context, principal runtimecap.Principal,
) ([]computer.Session, error) {
	if principal.Validate() != nil {
		return nil, computer.ErrDenied
	}
	if err := computerNodeAcquire(ctx, client.openGate); err != nil {
		return nil, err
	}
	var reconciliationErr error
	for _, attempt := range client.opening {
		if attempt.principal == principal {
			_, err := client.reconcileOpen(ctx, attempt)
			reconciliationErr = errors.Join(reconciliationErr, err)
		}
	}
	computerNodeUnlock(client.openGate)
	client.mu.Lock()
	entries := make([]*computerNodeSession, 0, len(client.sessions))
	for _, entry := range client.sessions {
		entries = append(entries, entry)
	}
	client.mu.Unlock()
	sessions := []computer.Session{}
	for _, entry := range entries {
		if err := computerNodeAcquire(ctx, entry.gate); err != nil {
			return sessions, errors.Join(reconciliationErr, err)
		}
		if entry.remote.Binding.Principal == principal {
			sessions = append(sessions, entry.projection())
		}
		computerNodeUnlock(entry.gate)
	}
	slices.SortFunc(sessions, func(first, second computer.Session) int {
		return bytes.Compare([]byte(first.Binding.SessionID), []byte(second.Binding.SessionID))
	})
	return sessions, reconciliationErr
}

// Shutdown runs while the gateway generation holds exclusive ownership. It
// releases automation only; it never sends application input or replacement open.
func (client *nodeComputerClient) Shutdown(ctx context.Context) error {
	if err := computerNodeAcquire(ctx, client.openGate); err != nil {
		return err
	}
	var cleanupErr error
	for _, attempt := range client.opening {
		_, err := client.reconcileOpen(ctx, attempt)
		cleanupErr = errors.Join(cleanupErr, err)
	}
	computerNodeUnlock(client.openGate)
	client.mu.Lock()
	entries := make([]*computerNodeSession, 0, len(client.sessions))
	for _, entry := range client.sessions {
		entries = append(entries, entry)
	}
	client.mu.Unlock()
	for _, entry := range entries {
		if err := computerNodeAcquire(ctx, entry.gate); err != nil {
			return errors.Join(cleanupErr, err)
		}
		principal, id, terminal := entry.remote.Binding.Principal, entry.remote.Binding.SessionID, entry.remote.Control.Terminal()
		computerNodeUnlock(entry.gate)
		if terminal {
			continue
		}
		_, err := client.Control(ctx, principal, id, uuid.NewString(), computer.EventRelease)
		if err != nil {
			status, statusErr := client.Status(ctx, principal, id, uuid.NewString())
			if statusErr != nil || !status.Control.Terminal() {
				cleanupErr = errors.Join(cleanupErr, computer.ErrUnavailable)
			}
		}
	}
	return cleanupErr
}
