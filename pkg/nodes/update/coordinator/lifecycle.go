//go:build linux || darwin

package coordinator

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"
)

var (
	ErrNotStaged                = errors.New("node update candidate is not staged")
	ErrActivationTooLate        = errors.New("node update activation has already begun")
	ErrActivationOutcomeUnknown = errors.New("node update activation outcome is unknown")
	ErrLaunchLimit              = errors.New("node update launch attempt limit reached")
)

// Activate durably selects the verified candidate before a supervisor may
// launch it. Repeating the call observes the existing activation; it never
// creates another activation intent.
func (coordinator *Coordinator) Activate(identity ExecutionIdentity) (State, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, err := coordinator.store.Load()
	if err != nil {
		return State{}, err
	}
	if state.Transaction == nil || !sameExecution(state.Transaction.Identity, identity) {
		return State{}, ErrTransactionConflict
	}
	if state.Transaction.Canceled {
		return state, ErrActivationTooLate
	}
	switch state.Transaction.Phase {
	case PhaseStaged:
		if state.Transaction.Candidate == nil {
			return State{}, ErrNotStaged
		}
		if coordinator.now().UTC().Unix() >= state.Transaction.ExpiresAt {
			return coordinator.denyActivation(state, "request_expired")
		}
		authority, resolveErr := coordinator.resolver.ResolveUpdateRelease(
			context.Background(),
			state.Transaction.Profile,
			state.Transaction.ReleaseAlias,
		)
		if resolveErr != nil || authority.Validate() != nil ||
			authority.Profile != state.Transaction.Profile ||
			authority.ProfileRevision != state.Transaction.ProfileRevision ||
			authority.ReleaseAlias != state.Transaction.ReleaseAlias ||
			authority.Tag != state.Transaction.RequestedRelease ||
			authority.AuthorityHash != state.Transaction.Identity.AuthorityHash {
			return coordinator.denyActivation(state, "authority_changed")
		}
		if err = coordinator.store.verifyPayload(*state.Transaction.Candidate, state.Installation); err != nil {
			return coordinator.requireOperatorAction(state, "candidate_changed")
		}
		previous := state.Active
		if err = coordinator.store.verifyPayload(previous, state.Installation); err != nil {
			return coordinator.requireOperatorAction(state, "previous_changed")
		}
		state.Generation++
		state.Active = *state.Transaction.Candidate
		state.Transaction.Previous = &previous
		state.Transaction.Phase = PhaseActivating
		state.Transaction.ActivationAttempted = true
		state.Transaction.UpdatedAt = coordinator.transitionTime(*state.Transaction)
		if err = coordinator.store.Commit(state.Generation-1, state); err != nil {
			return coordinator.reconcileActivationCommit(state, err)
		}
		return state, nil
	case PhaseActivating, PhaseHealthy, PhaseRollingBack, PhaseRolledBack,
		PhaseUnknown, PhaseOperatorActionRequired:
		return state, nil
	default:
		return State{}, ErrNotStaged
	}
}

func (coordinator *Coordinator) reconcileActivationCommit(expected State, commitErr error) (State, error) {
	observed, loadErr := coordinator.store.Load()
	if loadErr != nil || observed.Transaction == nil || expected.Transaction == nil ||
		!sameExecution(observed.Transaction.Identity, expected.Transaction.Identity) ||
		observed.Transaction.RequestHash != expected.Transaction.RequestHash {
		return State{}, errors.Join(ErrActivationOutcomeUnknown, commitErr, loadErr)
	}
	if observed.Generation == expected.Generation && observed.Transaction.Phase == PhaseActivating &&
		observed.Transaction.ActivationAttempted && observed.Active == expected.Active &&
		samePayload(observed.Transaction.Candidate, expected.Transaction.Candidate) &&
		samePayload(observed.Transaction.Previous, expected.Transaction.Previous) {
		return observed, nil
	}
	if observed.Generation+1 == expected.Generation && observed.Transaction.Phase == PhaseStaged &&
		!observed.Transaction.ActivationAttempted && expected.Transaction.Previous != nil &&
		observed.Active == *expected.Transaction.Previous &&
		samePayload(observed.Transaction.Candidate, expected.Transaction.Candidate) {
		return observed, commitErr
	}
	return State{}, errors.Join(ErrActivationOutcomeUnknown, commitErr)
}

func samePayload(left *Payload, right *Payload) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func (coordinator *Coordinator) denyActivation(state State, code string) (State, error) {
	denied, err := coordinator.requireOperatorAction(state, code)
	if err != nil {
		return State{}, err
	}
	return denied, ErrUpdateDenied
}

func (coordinator *Coordinator) Cancel(identity ExecutionIdentity) (State, error) {
	if err := identity.Validate(); err != nil {
		return State{}, err
	}
	var operationDone <-chan struct{}
	coordinator.operationMu.Lock()
	if coordinator.operation != nil && sameExecution(coordinator.operation.identity, identity) {
		coordinator.operation.cancel()
		operationDone = coordinator.operation.done
	}
	coordinator.operationMu.Unlock()
	if operationDone != nil {
		<-operationDone
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, err := coordinator.store.Load()
	if err != nil {
		return State{}, err
	}
	if state.Transaction == nil || !sameExecution(state.Transaction.Identity, identity) {
		return State{}, ErrTransactionConflict
	}
	if state.Transaction.Canceled {
		return state, nil
	}
	switch state.Transaction.Phase {
	case PhasePrepared, PhaseDownloading, PhaseVerified, PhaseStaged:
		return coordinator.cancelStage(state)
	default:
		return state, ErrActivationTooLate
	}
}

// RecordLaunch commits an attempt before starting the selected payload. A
// crash after this boundary consumes the attempt rather than hiding it.
func (coordinator *Coordinator) RecordLaunch(identity ExecutionIdentity) (State, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, err := coordinator.store.Load()
	if err != nil {
		return State{}, err
	}
	if state.Transaction == nil || !sameExecution(state.Transaction.Identity, identity) {
		return State{}, ErrTransactionConflict
	}
	if state.Transaction.Phase != PhaseActivating && state.Transaction.Phase != PhaseRollingBack {
		return State{}, ErrActivationTooLate
	}
	if state.Transaction.LaunchAttempts >= MaxLaunchAttempts {
		return state, ErrLaunchLimit
	}
	if err = coordinator.store.verifyPayload(state.Active, state.Installation); err != nil {
		return coordinator.requireOperatorAction(state, "selected_payload_changed")
	}
	state.Generation++
	state.Transaction.LaunchAttempts++
	state.Transaction.UpdatedAt = coordinator.transitionTime(*state.Transaction)
	if err = coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (coordinator *Coordinator) CommitHealthy(
	identity ExecutionIdentity,
	observation HealthObservation,
) (State, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, err := coordinator.store.Load()
	if err != nil {
		return State{}, err
	}
	if state.Transaction == nil || !sameExecution(state.Transaction.Identity, identity) ||
		state.Transaction.Phase != PhaseActivating {
		return State{}, ErrTransactionConflict
	}
	if err = observation.Validate(state.Installation, state.Active); err != nil {
		return State{}, err
	}
	state.Generation++
	state.Transaction.Phase = PhaseHealthy
	state.Transaction.SuccessorVerified = true
	state.Transaction.FailureCode = ""
	state.Transaction.UpdatedAt = coordinator.transitionTime(*state.Transaction)
	if err = coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (coordinator *Coordinator) BeginRollback(identity ExecutionIdentity, code string) (State, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, err := coordinator.store.Load()
	if err != nil {
		return State{}, err
	}
	if state.Transaction == nil || !sameExecution(state.Transaction.Identity, identity) {
		return State{}, ErrTransactionConflict
	}
	if state.Transaction.Phase == PhaseRollingBack || state.Transaction.Phase == PhaseRolledBack ||
		state.Transaction.Phase == PhaseOperatorActionRequired || state.Transaction.Phase == PhaseUnknown {
		return state, nil
	}
	if state.Transaction.Phase != PhaseActivating || state.Transaction.Previous == nil {
		return State{}, ErrActivationTooLate
	}
	if !boundedNamePattern.MatchString(code) {
		return State{}, errors.New("invalid rollback reason")
	}
	if err = coordinator.store.verifyPayload(*state.Transaction.Previous, state.Installation); err != nil {
		return coordinator.requireOperatorAction(state, "rollback_payload_changed")
	}
	state.Generation++
	state.Active = *state.Transaction.Previous
	state.Transaction.Phase = PhaseRollingBack
	state.Transaction.RollbackAttempted = true
	state.Transaction.LaunchAttempts = 0
	state.Transaction.FailureCode = code
	state.Transaction.UpdatedAt = coordinator.transitionTime(*state.Transaction)
	if err = coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (coordinator *Coordinator) CommitRolledBack(
	identity ExecutionIdentity,
	observation HealthObservation,
) (State, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, err := coordinator.store.Load()
	if err != nil {
		return State{}, err
	}
	if state.Transaction == nil || !sameExecution(state.Transaction.Identity, identity) ||
		state.Transaction.Phase != PhaseRollingBack {
		return State{}, ErrTransactionConflict
	}
	if err = observation.Validate(state.Installation, state.Active); err != nil {
		return State{}, err
	}
	state.Generation++
	state.Transaction.Phase = PhaseRolledBack
	state.Transaction.RollbackVerified = true
	state.Transaction.UpdatedAt = coordinator.transitionTime(*state.Transaction)
	if err = coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (coordinator *Coordinator) RequireOperatorAction(identity ExecutionIdentity, code string) (State, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, err := coordinator.store.Load()
	if err != nil {
		return State{}, err
	}
	if state.Transaction == nil || !sameExecution(state.Transaction.Identity, identity) {
		return State{}, ErrTransactionConflict
	}
	return coordinator.requireOperatorAction(state, code)
}

func (coordinator *Coordinator) requireOperatorAction(state State, code string) (State, error) {
	if !boundedNamePattern.MatchString(code) {
		return State{}, errors.New("invalid operator-action reason")
	}
	state.Generation++
	state.Transaction.Phase = PhaseOperatorActionRequired
	state.Transaction.FailureCode = code
	state.Transaction.UpdatedAt = coordinator.transitionTime(*state.Transaction)
	if err := coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (coordinator *Coordinator) transitionTime(transaction Transaction) int64 {
	now := coordinator.now().UTC().Unix()
	if now < transaction.AcceptedAt {
		return transaction.AcceptedAt
	}
	return min(now, transaction.ExpiresAt)
}

type HealthObservation struct {
	NodeID       string
	Version      string
	Platform     string
	Architecture string
	CatalogHash  string
	ObservedAt   time.Time
}

func (observation HealthObservation) Validate(
	installation Installation,
	payload Payload,
) error {
	if observation.NodeID != string(installation.NodeID) || observation.Version != payload.Version ||
		observation.Platform != installation.Platform || observation.Architecture != installation.Architecture ||
		!isDigest(observation.CatalogHash, sha256.Size) || observation.ObservedAt.IsZero() {
		return errors.New("authenticated successor health does not match the expected payload")
	}
	return nil
}
