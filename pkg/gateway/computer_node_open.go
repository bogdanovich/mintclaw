package gateway

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

// Public aliases do not define native ownership: two aliases may select the
// same approved node profile. An unresolved open fences that logical target.
type computerOpenTarget struct {
	node    nodes.ID
	profile string
}

// An attempt owns a cleanup obligation before a native session ID is known.
// It is not a native binding or another control state machine. Only the original
// invocation ledger may resolve it; a timeout or missing record is not proof.
type computerOpenAttempt struct {
	authority    computerNodeAuthority
	principal    runtimecap.Principal
	invocationID string
	started      time.Time
}

func (client *nodeComputerClient) invocationID(
	principal runtimecap.Principal, alias, command, requestID string,
) string {
	identity, _ := json.Marshal(principal)
	return computerNodeID("computer", string(identity), client.workspaceID, alias, command, requestID)
}

func (client *nodeComputerClient) invocationPrincipal(principal runtimecap.Principal) nodes.GatewayInvocationPrincipal {
	return nodes.GatewayInvocationPrincipal{
		AgentID: principal.AgentID, ActorID: principal.ActorID, SessionID: principal.SessionID,
		ExecutionID: principal.ExecutionID, WorkspaceID: client.workspaceID,
	}
}

func computerOpenResult(session computer.Session) (computer.Session, error) {
	if err := session.AgentAuthority(time.Now()); err != nil {
		return computer.Session{}, err
	}
	return session, nil
}

// adoptOpen retains valid original ownership even when its local evidence has
// already expired. Such a resource cannot observe, but still must be released.
// openGate is held by the caller; existing sessions retain their newer state.
func (client *nodeComputerClient) adoptOpen(
	ctx context.Context, attempt *computerOpenAttempt, result nodes.ComputerSessionResult,
) (computer.Session, error) {
	remote, authority := result.Session, attempt.authority
	if remote.Validate() != nil || result.ObservedAt.IsZero() ||
		remote.Binding.Principal != attempt.principal || remote.Binding.Target.Node != string(authority.nodeID) ||
		remote.Binding.Target.Alias != authority.profile.Alias || remote.Binding.PolicyRevision != authority.profile.Revision ||
		remote.Binding.Generation != 1 || remote.Control != computer.ControlAgent ||
		!remote.ExpiresAt.After(result.ObservedAt) {
		return computer.Session{}, errComputerOutcomeUnknown
	}
	client.mu.Lock()
	existing := client.sessions[remote.Binding.SessionID]
	if existing == nil {
		entry := &computerNodeSession{
			gate: make(chan struct{}, 1), authority: authority, remote: remote,
			deadline: computerEvidenceDeadline(
				attempt.started, result.ObservedAt, remote.ExpiresAt, authority.policy.LeaseTTL(),
			),
		}
		projection := entry.projection()
		client.sessions[remote.Binding.SessionID] = entry
		client.mu.Unlock()
		return projection, nil
	}
	client.mu.Unlock()
	if err := computerNodeAcquire(ctx, existing.gate); err != nil {
		return computer.Session{}, err
	}
	defer computerNodeUnlock(existing.gate)
	original := existing.remote.Binding
	original.Generation = remote.Binding.Generation
	if original != remote.Binding || !existing.remote.ExpiresAt.Equal(remote.ExpiresAt) {
		return computer.Session{}, errComputerOutcomeUnknown
	}
	// A replayed open receipt must not reset a handed-off/released session or
	// extend its original local lease. The existing entry remains authoritative.
	return existing.projection(), nil
}

// reconcileOpen is query-only and runs under openGate. It never redispatches an
// open, fabricates a native session ID, or treats unavailable proof as cleanup.
func (client *nodeComputerClient) reconcileOpen(
	ctx context.Context, attempt *computerOpenAttempt,
) (*computer.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	principal := client.invocationPrincipal(attempt.principal)
	retained, found, err := client.source.LookupInvocation(principal, attempt.invocationID)
	if err != nil || !found || retained.Target != attempt.authority.policy.NodeTarget ||
		retained.Plan.NodeID != attempt.authority.nodeID || retained.Plan.Command != nodes.ComputerCommandOpen {
		return nil, errComputerOutcomeUnknown
	}
	var input nodes.ComputerOpenInput
	if json.Unmarshal(retained.Plan.Input, &input) != nil || input.Profile != attempt.authority.profile.Alias ||
		input.ProfileRevision != attempt.authority.profile.Revision {
		return nil, errComputerOutcomeUnknown
	}
	owner, err := input.Principal(retained.Plan)
	if err != nil || owner != attempt.principal {
		return nil, errComputerOutcomeUnknown
	}
	// A repeated tool-call ID can recover an older dispatched plan. Anchor it to
	// the gateway's original durable time too, never to this reconciliation call.
	if original := time.Unix(0, retained.CreatedAt); original.Before(attempt.started) {
		attempt.started = original
	}
	proof, err := client.source.QueryInvocation(
		ctx, principal, retained.Target, retained.Plan.NodeID, retained.Plan.InvocationID,
	)
	if err != nil {
		return nil, errComputerOutcomeUnknown
	}
	key := computerOpenTarget{node: attempt.authority.nodeID, profile: attempt.authority.profile.Alias}
	switch proof.State {
	case nodes.InvocationFailed:
		delete(client.opening, key)
		return nil, nil // Verified definitive failure: no committed resource.
	case nodes.InvocationCanceled:
		if proof.StartedAt == 0 {
			delete(client.opening, key)
			return nil, nil
		}
	case nodes.InvocationSucceeded:
		var result nodes.ComputerSessionResult
		if json.Unmarshal(proof.Result, &result) != nil {
			return nil, errComputerOutcomeUnknown
		}
		session, err := client.adoptOpen(ctx, attempt, result)
		if err != nil {
			return nil, errComputerOutcomeUnknown
		}
		delete(client.opening, key)
		return &session, nil
	}
	return nil, errComputerOutcomeUnknown
}
