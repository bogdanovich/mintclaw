package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

type ContextOperation string

const (
	ContextList   ContextOperation = "list"
	ContextOpen   ContextOperation = "open"
	ContextSelect ContextOperation = "select"
	ContextClose  ContextOperation = "close"
)

func (operation ContextOperation) validMutation() bool {
	return operation == ContextOpen || operation == ContextSelect || operation == ContextClose
}

type ContextRequest struct {
	Owner             Owner
	RequestID         string
	SessionID         string
	Operation         ContextOperation
	ContextCatalogID  string
	ContextGeneration uint64
	TabID             string
	FrameID           string
}

type ContextPreparation struct {
	Request          ContextRequest
	Invocation       Invocation
	Approval         ApprovalBinding
	RequiresApproval bool
}

type ContextResult struct {
	Catalog     ContextCatalog
	Observation *Observation
	Invocation  *Invocation
}

func (broker *Broker) ListContexts(
	ctx context.Context,
	owner Owner,
	sessionID string,
) (ContextCatalog, error) {
	if owner.Validate() != nil || !validIdentifier(sessionID) {
		return ContextCatalog{}, ErrInvalid
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	session, _, worker, err := broker.contextSessionLocked(ctx, owner, sessionID)
	if err != nil {
		return ContextCatalog{}, err
	}
	return broker.refreshContextCatalogLocked(ctx, session, worker)
}

func (broker *Broker) PrepareContext(
	ctx context.Context,
	request ContextRequest,
) (ContextPreparation, error) {
	if validateContextMutationRequest(request) != nil {
		return ContextPreparation{}, ErrInvalid
	}
	actionHash, err := hashContextRequest(request)
	if err != nil {
		return ContextPreparation{}, err
	}
	invocationID := derivedIdentifier("context_invocation", request.Owner, request.SessionID, request.RequestID)

	broker.mu.Lock()
	defer broker.mu.Unlock()
	existing, existingErr := broker.store.GetInvocation(ctx, invocationID)
	if existingErr == nil && existing.State.Terminal() {
		if existing.Owner != request.Owner || existing.SessionID != request.SessionID ||
			existing.ActionHash != actionHash || existing.Effect != contextOperationEffect(request.Operation) {
			return ContextPreparation{}, ErrConflict
		}
		return contextPreparationView(request, existing, broker.policyRevision), nil
	}
	if existingErr != nil && !errors.Is(existingErr, ErrNotFound) {
		return ContextPreparation{}, existingErr
	}
	session, _, worker, err := broker.contextSessionLocked(ctx, request.Owner, request.SessionID)
	if err != nil {
		return ContextPreparation{}, err
	}
	if _, err = broker.refreshContextCatalogLocked(ctx, session, worker); err != nil {
		return ContextPreparation{}, err
	}
	session, err = broker.store.GetSession(ctx, request.SessionID)
	if err != nil {
		return ContextPreparation{}, err
	}
	if request.Operation != ContextOpen && !contextRequestMatchesSession(session, request) {
		return ContextPreparation{}, ErrStale
	}
	if request.Operation == ContextClose {
		if session.DryRun {
			return ContextPreparation{}, ErrDenied
		}
		if session.ContextAuthority == nil || len(session.ContextAuthority.Tabs) < 2 {
			return ContextPreparation{}, ErrDenied
		}
	}

	invocation, err := broker.store.GetInvocation(ctx, invocationID)
	if err == nil {
		if invocation.Owner != request.Owner || invocation.SessionID != request.SessionID ||
			invocation.ActionHash != actionHash || invocation.Effect != contextOperationEffect(request.Operation) {
			return ContextPreparation{}, ErrConflict
		}
		return contextPreparationView(request, invocation, broker.policyRevision), nil
	}
	if !errors.Is(err, ErrNotFound) {
		return ContextPreparation{}, err
	}
	now := broker.now().UTC()
	invocation = Invocation{
		ID: invocationID, SessionID: request.SessionID, Owner: request.Owner,
		ActionHash: actionHash, Effect: contextOperationEffect(request.Operation),
		State: InvocationPrepared, Revision: 1, CreatedAt: now.UnixNano(), UpdatedAt: now.UnixNano(),
		ExpiresAt: now.Add(time.Duration(broker.config.Limits.Effective().PreparedSeconds) * time.Second).UnixNano(),
	}
	if err = broker.store.CreateInvocation(ctx, invocation); err != nil {
		return ContextPreparation{}, err
	}
	return contextPreparationView(request, invocation, broker.policyRevision), nil
}

func (broker *Broker) ExecuteContext(
	ctx context.Context,
	preparation ContextPreparation,
	approval *ApprovalBinding,
) (ContextResult, error) {
	request := preparation.Request
	if validateContextMutationRequest(request) != nil || preparation.Invocation.ID == "" ||
		preparation.Invocation.ActionHash == "" || !request.Operation.validMutation() {
		return ContextResult{}, ErrInvalid
	}
	expectedHash, err := hashContextRequest(request)
	expectedID := derivedIdentifier("context_invocation", request.Owner, request.SessionID, request.RequestID)
	requiresApproval := request.Operation == ContextClose
	if err != nil || preparation.Invocation.ID != expectedID || preparation.Invocation.ActionHash != expectedHash ||
		preparation.Invocation.SessionID != request.SessionID || preparation.Invocation.Owner != request.Owner ||
		preparation.Invocation.Effect != contextOperationEffect(request.Operation) ||
		preparation.RequiresApproval != requiresApproval ||
		(requiresApproval && preparation.Approval.PolicyRevision != broker.policyRevision) {
		return ContextResult{}, ErrInvalid
	}
	if requiresApproval && !contextApprovalMatches(preparation, approval) {
		return ContextResult{}, ErrApprovalRequired
	}

	broker.mu.Lock()
	defer broker.mu.Unlock()
	storedInvocation, err := broker.store.GetInvocation(ctx, preparation.Invocation.ID)
	if err != nil || storedInvocation.Owner != request.Owner ||
		storedInvocation.ActionHash != preparation.Invocation.ActionHash {
		return ContextResult{}, errors.Join(err, ErrNotFound)
	}
	if storedInvocation.State == InvocationPrepared {
		session, _, worker, preflightErr := broker.contextSessionLocked(ctx, request.Owner, request.SessionID)
		if preflightErr != nil {
			return ContextResult{}, preflightErr
		}
		if _, preflightErr = broker.refreshContextCatalogLocked(ctx, session, worker); preflightErr != nil {
			return ContextResult{}, preflightErr
		}
		session, preflightErr = broker.store.GetSession(ctx, request.SessionID)
		if preflightErr != nil {
			return ContextResult{}, preflightErr
		}
		if request.Operation != ContextOpen && !contextRequestMatchesSession(session, request) {
			return ContextResult{}, ErrStale
		}
	}
	var selectedObservation *Observation
	invocation, executeErr := broker.executePreparedLocked(
		ctx,
		request.Owner,
		preparation.Invocation.ID,
		preparation.Invocation.ActionHash,
		func(executeCtx context.Context) (json.RawMessage, error) {
			session, _, worker, err := broker.contextSessionLocked(executeCtx, request.Owner, request.SessionID)
			if err != nil {
				return nil, err
			}
			if err = broker.ensureContextFreshLocked(executeCtx, session, worker); err != nil {
				return nil, err
			}
			session, err = broker.store.GetSession(executeCtx, request.SessionID)
			if err != nil {
				return nil, err
			}
			if request.Operation != ContextOpen && !contextRequestMatchesSession(session, request) {
				return nil, ErrStale
			}
			var catalog ContextCatalog
			switch request.Operation {
			case ContextOpen:
				catalog, err = worker.OpenTab(executeCtx)
			case ContextSelect:
				var observed DriverObservation
				authority := newContextMutationAuthority(
					*session.ContextAuthority, request.TabID, request.FrameID,
				)
				observed, catalog, err = worker.SelectContext(executeCtx, authority)
				if err == nil {
					var materialized Observation
					materialized, err = broker.persistContextObservationLocked(
						executeCtx, session, catalog, observed,
					)
					if err == nil {
						selectedObservation = &materialized
						persisted, getErr := broker.store.GetSession(executeCtx, session.ID)
						if getErr != nil || persisted.ContextAuthority == nil {
							err = errors.Join(getErr, ErrStale)
						} else {
							catalog = cloneContextCatalog(*persisted.ContextAuthority)
						}
					}
				}
			case ContextClose:
				authority := newContextMutationAuthority(*session.ContextAuthority, request.TabID, "")
				catalog, err = worker.CloseTab(executeCtx, authority)
			}
			if err != nil {
				return nil, err
			}
			if request.Operation != ContextSelect {
				catalog, err = broker.persistContextCatalogLocked(executeCtx, session, catalog)
				if err != nil {
					return nil, err
				}
			}
			return json.Marshal(struct {
				Catalog ContextCatalog `json:"context_catalog"`
			}{Catalog: catalog})
		},
	)
	result := ContextResult{Invocation: &invocation}
	if invocation.State == InvocationUnknown {
		quarantineErr := broker.quarantineWorkerSessionLocked(ctx, request.SessionID, "outcome_unknown")
		return result, errors.Join(executeErr, quarantineErr)
	}
	if executeErr != nil {
		return result, executeErr
	}
	if invocation.State != InvocationSucceeded {
		return result, ErrConflict
	}
	var terminal struct {
		Catalog ContextCatalog `json:"context_catalog"`
	}
	if json.Unmarshal(invocation.TerminalResult, &terminal) != nil || terminal.Catalog.Validate() != nil {
		return result, ErrDriverIncompatible
	}
	result.Catalog = terminal.Catalog
	if request.Operation == ContextSelect && selectedObservation != nil {
		result.Observation = selectedObservation
	}
	return result, nil
}

func validateContextMutationRequest(request ContextRequest) error {
	if request.Owner.Validate() != nil || !validIdentifier(request.RequestID) ||
		!validIdentifier(request.SessionID) || !request.Operation.validMutation() {
		return ErrInvalid
	}
	if request.Operation == ContextOpen {
		if request.ContextCatalogID != "" || request.ContextGeneration != 0 ||
			request.TabID != "" || request.FrameID != "" {
			return ErrInvalid
		}
		return nil
	}
	if !validIdentifier(request.ContextCatalogID) || request.ContextGeneration == 0 ||
		!validIdentifier(request.TabID) || (request.Operation == ContextClose && request.FrameID != "") ||
		(request.FrameID != "" && !validIdentifier(request.FrameID)) {
		return ErrInvalid
	}
	return nil
}

func (broker *Broker) contextSessionLocked(
	ctx context.Context,
	owner Owner,
	sessionID string,
) (Session, *workerSlot, ContextWorker, error) {
	session, err := broker.store.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, nil, nil, err
	}
	if !session.Owner.Equal(owner) {
		return Session{}, nil, nil, ErrNotFound
	}
	if session.State != SessionReady || session.EffectiveController() != ControllerAgent {
		return Session{}, nil, nil, ErrWorkerUnavailable
	}
	if session.PolicyRevision != broker.policyRevision {
		_, finishErr := broker.finishSessionLocked(ctx, session, SessionLost, "policy_changed")
		return Session{}, nil, nil, errors.Join(ErrWorkerUnavailable, finishErr)
	}
	if broker.sessionExpired(session, broker.now().UTC()) {
		_, finishErr := broker.finishSessionLocked(ctx, session, SessionExpired, "")
		return Session{}, nil, nil, errors.Join(ErrWorkerUnavailable, finishErr)
	}
	if session.State != SessionReady {
		return Session{}, nil, nil, ErrWorkerUnavailable
	}
	slot := broker.slots[session.ID]
	if slot == nil || slot.safeFailure != "" {
		return Session{}, nil, nil, ErrWorkerUnavailable
	}
	worker, ok := slot.worker.(ContextWorker)
	if !ok {
		return Session{}, nil, nil, ErrDriverIncompatible
	}
	return session, slot, worker, nil
}

func (broker *Broker) refreshContextCatalogLocked(
	ctx context.Context,
	session Session,
	worker ContextWorker,
) (ContextCatalog, error) {
	catalog, err := worker.ContextCatalog(ctx)
	if err != nil {
		return ContextCatalog{}, err
	}
	return broker.persistContextCatalogLocked(ctx, session, catalog)
}

func (broker *Broker) ensureContextFreshLocked(
	ctx context.Context,
	session Session,
	worker ContextWorker,
) error {
	live, err := worker.ContextCatalog(ctx)
	if err != nil {
		return err
	}
	live = broker.applyContextFramePolicy(ctx, session, live)
	_, changed, err := normalizeContextCatalog(session.ContextAuthority, live)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	_, persistErr := broker.persistContextCatalogLocked(ctx, session, live)
	return errors.Join(ErrStale, persistErr)
}

func (broker *Broker) persistContextCatalogLocked(
	ctx context.Context,
	session Session,
	driverCatalog ContextCatalog,
) (ContextCatalog, error) {
	driverCatalog = broker.applyContextFramePolicy(ctx, session, driverCatalog)
	catalog, changed, err := normalizeContextCatalog(session.ContextAuthority, driverCatalog)
	if err != nil {
		return ContextCatalog{}, err
	}
	if !changed {
		return catalog, nil
	}
	session.TabID = catalog.SelectedTabID
	session.FrameID = catalog.SelectedFrameID
	session.ContextAuthority = &catalog
	clearSessionSnapshot(&session)
	session.Revision++
	session.UpdatedAt = timestampAtLeast(broker.now().UTC().UnixNano(), session.UpdatedAt)
	session.LastActivityAt = session.UpdatedAt
	if err = broker.store.UpdateSession(ctx, session.Revision-1, session); err != nil {
		return ContextCatalog{}, err
	}
	if slot := broker.slots[session.ID]; slot != nil {
		slot.refs, slot.inputs, slot.uploads, slot.navigationID = nil, nil, nil, ""
	}
	return catalog, nil
}

func (broker *Broker) applyContextFramePolicy(
	ctx context.Context,
	session Session,
	catalog ContextCatalog,
) ContextCatalog {
	projected := cloneContextCatalog(catalog)
	for tabIndex := range projected.Tabs {
		for frameIndex := range projected.Tabs[tabIndex].Frames {
			frame := &projected.Tabs[tabIndex].Frames[frameIndex]
			if frame.Availability != FrameReady ||
				(broker.originAllowed(session, frame.Origin) &&
					broker.originNetworkAllowed(ctx, session, frame.Origin)) {
				continue
			}
			frame.Availability = FrameUnavailable
			frame.SafeFailure = "frame_policy_denied"
			if projected.SelectedFrameID == frame.ID {
				projected.SelectedFrameID = ""
			}
		}
	}
	return projected
}

func normalizeContextCatalog(
	current *ContextCatalog,
	driver ContextCatalog,
) (ContextCatalog, bool, error) {
	if err := driver.Validate(); err != nil {
		return ContextCatalog{}, false, errors.Join(ErrDriverIncompatible, err)
	}
	next := cloneContextCatalog(driver)
	if current == nil {
		next.Generation = 1
		return next, true, next.Validate()
	}
	if current.ID != next.ID {
		return ContextCatalog{}, false, ErrDriverIncompatible
	}
	next.Generation = current.Generation
	if reflect.DeepEqual(*current, next) {
		return cloneContextCatalog(*current), false, nil
	}
	next.Generation++
	return next, true, next.Validate()
}

func (broker *Broker) persistContextObservationLocked(
	ctx context.Context,
	session Session,
	driverCatalog ContextCatalog,
	driverObservation DriverObservation,
) (Observation, error) {
	catalog, err := broker.persistContextCatalogLocked(ctx, session, driverCatalog)
	if err != nil {
		return Observation{}, err
	}
	session, err = broker.store.GetSession(ctx, session.ID)
	if err != nil || session.ContextAuthority == nil || session.ContextAuthority.Generation != catalog.Generation {
		return Observation{}, errors.Join(err, ErrStale)
	}
	return broker.persistDriverObservationLocked(ctx, session, driverObservation)
}

func contextRequestMatchesSession(session Session, request ContextRequest) bool {
	if session.ContextAuthority == nil || session.ContextAuthority.ID != request.ContextCatalogID ||
		session.ContextAuthority.Generation != request.ContextGeneration {
		return false
	}
	for _, tab := range session.ContextAuthority.Tabs {
		if tab.ID != request.TabID {
			continue
		}
		if request.Operation != ContextSelect || request.FrameID == "" {
			return true
		}
		for _, frame := range tab.Frames {
			if frame.ID == request.FrameID && frame.Availability == FrameReady {
				return true
			}
		}
		return false
	}
	return false
}

func contextOperationEffect(operation ContextOperation) Effect {
	switch operation {
	case ContextOpen:
		return EffectLocalEdit
	case ContextSelect:
		return EffectRead
	default:
		return EffectUnknown
	}
}

func hashContextRequest(request ContextRequest) (string, error) {
	encoded, err := json.Marshal(struct {
		Owner             Owner            `json:"owner"`
		SessionID         string           `json:"browser_session_id"`
		Operation         ContextOperation `json:"operation"`
		ContextCatalogID  string           `json:"context_catalog_id,omitempty"`
		ContextGeneration uint64           `json:"context_generation,omitempty"`
		TabID             string           `json:"tab_id,omitempty"`
		FrameID           string           `json:"frame_id,omitempty"`
	}{
		Owner: request.Owner, SessionID: request.SessionID, Operation: request.Operation,
		ContextCatalogID: request.ContextCatalogID, ContextGeneration: request.ContextGeneration,
		TabID: request.TabID, FrameID: request.FrameID,
	})
	if err != nil {
		return "", fmt.Errorf("hash browser context request: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func contextPreparationView(
	request ContextRequest,
	invocation Invocation,
	policyRevision string,
) ContextPreparation {
	return ContextPreparation{
		Request: request, Invocation: invocation,
		Approval: ApprovalBinding{
			PreparedActionID: invocation.ID, ActionHash: invocation.ActionHash,
			PolicyRevision: policyRevision, ExpiresAt: invocation.ExpiresAt,
		},
		RequiresApproval: request.Operation == ContextClose,
	}
}

func contextApprovalMatches(preparation ContextPreparation, approval *ApprovalBinding) bool {
	return approval != nil && approval.PreparedActionID == preparation.Invocation.ID &&
		approval.ActionHash == preparation.Invocation.ActionHash &&
		approval.PolicyRevision == preparation.Approval.PolicyRevision &&
		approval.ExpiresAt == preparation.Invocation.ExpiresAt
}
