package browser

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

type Observation struct {
	SessionID          string             `json:"browser_session_id"`
	TabID              string             `json:"tab_id"`
	FrameID            string             `json:"frame_id,omitempty"`
	ContextCatalogID   string             `json:"context_catalog_id,omitempty"`
	ContextGeneration  uint64             `json:"context_generation,omitempty"`
	SnapshotID         string             `json:"snapshot_id"`
	SnapshotGeneration uint64             `json:"snapshot_generation"`
	URL                string             `json:"url"`
	Origin             string             `json:"origin"`
	Title              string             `json:"title,omitempty"`
	Snapshot           string             `json:"snapshot"`
	PendingDialog      *DialogObservation `json:"pending_dialog,omitempty"`
	Truncated          bool               `json:"truncated"`
	PageStateHash      string             `json:"page_state_hash"`
}

type DialogObservation struct {
	ID      string `json:"dialog_id,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

type PrepareActionRequest struct {
	Owner              Owner
	RequestID          string
	SessionID          string
	TabID              string
	FrameID            string
	ContextCatalogID   string
	ContextGeneration  uint64
	SnapshotID         string
	SnapshotGeneration uint64
	Action             Action
	Upload             *UploadBinding
}

type Preparation struct {
	Action           PreparedAction
	Approval         ApprovalBinding
	RequiresApproval bool
}

type ObserveRequest struct {
	Owner             Owner
	SessionID         string
	TabID             string
	FrameID           string
	ContextCatalogID  string
	ContextGeneration uint64
}

func (broker *Broker) Observe(ctx context.Context, owner Owner, sessionID, tabID string) (Observation, error) {
	request := ObserveRequest{Owner: owner, SessionID: sessionID, TabID: tabID}
	if err := owner.Validate(); err != nil {
		return Observation{}, err
	}
	if !validIdentifier(sessionID) || !validIdentifier(tabID) {
		return Observation{}, fmt.Errorf("%w: malformed observation identity", ErrInvalid)
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	session, _, worker, err := broker.actionSessionLocked(ctx, owner, sessionID, tabID)
	if err != nil {
		return Observation{}, err
	}
	if session.ContextAuthority != nil {
		request.FrameID = session.FrameID
		request.ContextCatalogID = session.ContextAuthority.ID
		request.ContextGeneration = session.ContextAuthority.Generation
	}
	return broker.observeBoundSessionLocked(ctx, request, session, worker)
}

func (broker *Broker) ObserveContext(ctx context.Context, request ObserveRequest) (Observation, error) {
	if err := request.Owner.Validate(); err != nil {
		return Observation{}, err
	}
	if !validIdentifier(request.SessionID) || !validIdentifier(request.TabID) ||
		!validContextBinding(request.FrameID, request.ContextCatalogID, request.ContextGeneration) {
		return Observation{}, fmt.Errorf("%w: malformed observation identity", ErrInvalid)
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	session, _, worker, err := broker.actionSessionLocked(
		ctx, request.Owner, request.SessionID, request.TabID,
	)
	if err != nil {
		return Observation{}, err
	}
	return broker.observeBoundSessionLocked(ctx, request, session, worker)
}

func (broker *Broker) observeBoundSessionLocked(
	ctx context.Context,
	request ObserveRequest,
	session Session,
	worker ActionWorker,
) (Observation, error) {
	if !sessionMatchesContextBinding(
		session, request.FrameID, request.ContextCatalogID, request.ContextGeneration,
	) {
		return Observation{}, ErrStale
	}
	if session.ContextAuthority == nil {
		driverObservation, navigationID, observeErr := observeWithNavigationCheck(ctx, worker)
		if observeErr != nil {
			return Observation{}, broker.handleObservationErrorLocked(ctx, session, observeErr)
		}
		return broker.persistDriverObservationLocked(ctx, session, driverObservation, navigationID)
	}
	contextWorker, ok := worker.(ContextWorker)
	if !ok {
		if session.FrameID != "" {
			return Observation{}, ErrDriverIncompatible
		}
		driverObservation, navigationID, observeErr := observeWithNavigationCheck(ctx, worker)
		if observeErr != nil {
			return Observation{}, broker.handleObservationErrorLocked(ctx, session, observeErr)
		}
		return broker.persistDriverObservationLocked(ctx, session, driverObservation, navigationID)
	}
	return broker.observeSelectedContextLocked(ctx, session, contextWorker)
}

func (broker *Broker) observeSelectedContextLocked(
	ctx context.Context,
	session Session,
	worker ContextWorker,
) (Observation, error) {
	before := cloneContextCatalog(*session.ContextAuthority)
	live, err := worker.ContextCatalog(ctx)
	if err != nil {
		return Observation{}, broker.handleObservationErrorLocked(ctx, session, err)
	}
	live = broker.applyContextFramePolicy(ctx, session, live)
	normalized, changed, err := normalizeContextCatalog(session.ContextAuthority, live)
	if err != nil {
		return Observation{}, err
	}
	if changed {
		_, persistErr := broker.persistContextCatalogLocked(ctx, session, normalized)
		return Observation{}, errors.Join(ErrStale, persistErr)
	}
	var driverObservation DriverObservation
	var navigationID string
	if session.FrameID == "" {
		driverObservation, navigationID, err = observeWithNavigationCheck(ctx, worker)
	} else {
		identityWorker, ok := worker.(ContextSelectionIdentityWorker)
		authority := newContextMutationAuthority(*session.ContextAuthority, session.TabID, session.FrameID)
		if ok {
			driverObservation, live, navigationID, err = identityWorker.SelectContextWithNavigationIdentity(
				ctx,
				authority,
			)
		} else {
			// Remote context workers retain their existing observation path
			// until companion screenshot transport supplies an equivalent
			// private document authority. An empty identity keeps capture
			// fail-closed without regressing ordinary frame observation.
			driverObservation, live, err = worker.SelectContext(ctx, authority)
		}
	}
	if err != nil {
		return Observation{}, broker.handleObservationErrorLocked(ctx, session, err)
	}
	if session.FrameID == "" {
		live, err = worker.ContextCatalog(ctx)
		if err != nil {
			return Observation{}, broker.handleObservationErrorLocked(ctx, session, err)
		}
	}
	live = broker.applyContextFramePolicy(ctx, session, live)
	normalized, changed, err = normalizeContextCatalog(session.ContextAuthority, live)
	if err != nil {
		return Observation{}, err
	}
	if changed || !reflect.DeepEqual(before, normalized) {
		_, persistErr := broker.persistContextCatalogLocked(ctx, session, normalized)
		return Observation{}, errors.Join(ErrStale, persistErr)
	}
	return broker.persistDriverObservationLocked(ctx, session, driverObservation, navigationID)
}

func (broker *Broker) handleObservationErrorLocked(ctx context.Context, session Session, err error) error {
	if !errors.Is(err, ErrWorkerLost) {
		return err
	}
	quarantineErr := broker.quarantineWorkerSessionLocked(ctx, session.ID, "worker_lost")
	return errors.Join(err, quarantineErr)
}

func (broker *Broker) persistDriverObservationLocked(
	ctx context.Context,
	session Session,
	driverObservation DriverObservation,
	navigationID ...string,
) (Observation, error) {
	var err error
	if err = validateBlankObservation(driverObservation, ""); err != nil {
		return Observation{}, err
	}
	if !broker.originAllowed(session, driverObservation.Origin) {
		return Observation{}, ErrDenied
	}
	if !broker.originNetworkAllowed(ctx, session, driverObservation.Origin) {
		return Observation{}, broker.quarantineNetworkDeniedLocked(ctx, session)
	}
	snapshotID, err := randomOpaqueID("snapshot")
	if err != nil {
		return Observation{}, fmt.Errorf("generate browser snapshot ID: %w", err)
	}
	refs := make(map[string]DriverElement, len(driverObservation.Elements))
	refPositions := make(map[string]uint32, len(driverObservation.Elements))
	visibleSnapshot := driverObservation.Snapshot
	for index, element := range driverObservation.Elements {
		ref := stableElementRef(snapshotID, element.Target)
		refs[ref] = element
		refPositions[ref] = uint32(index + 1)
		visibleSnapshot = strings.ReplaceAll(visibleSnapshot, "[ref="+element.Target+"]", "[ref="+ref+"]")
	}
	now := broker.now().UTC().UnixNano()
	pageStateHash, err := browserPageStateHash(driverObservation)
	if err != nil {
		return Observation{}, err
	}
	session.SnapshotGeneration++
	session.SnapshotID = snapshotID
	session.SnapshotOrigin = driverObservation.Origin
	session.PageStateHash = pageStateHash
	session.Revision++
	session.UpdatedAt = timestampAtLeast(now, session.UpdatedAt)
	session.LastActivityAt = session.UpdatedAt
	if err = broker.store.UpdateSession(ctx, session.Revision-1, session); err != nil {
		return Observation{}, err
	}
	slot := broker.slots[session.ID]
	if slot == nil {
		return Observation{}, ErrWorkerUnavailable
	}
	slot.refs = refs
	slot.refPositions = refPositions
	slot.inputs = nil
	slot.uploads = nil
	slot.navigationID = ""
	if len(navigationID) > 0 {
		slot.navigationID = navigationID[0]
	}
	pendingDialog := cloneDialogObservation(driverObservation.PendingDialog)
	if pendingDialog != nil {
		pendingDialog.ID = stableDialogRef(snapshotID, pendingDialog.Type, pendingDialog.Message)
	}
	observation := Observation{
		SessionID: session.ID, TabID: session.TabID, FrameID: session.FrameID, SnapshotID: snapshotID,
		SnapshotGeneration: session.SnapshotGeneration, URL: driverObservation.URL,
		Origin: driverObservation.Origin, Title: driverObservation.Title, Snapshot: visibleSnapshot,
		PendingDialog: pendingDialog,
		Truncated:     driverObservation.Truncated,
		PageStateHash: pageStateHash,
	}
	if session.ContextAuthority != nil {
		observation.ContextCatalogID = session.ContextAuthority.ID
		observation.ContextGeneration = session.ContextAuthority.Generation
	}
	return observation, nil
}

func (broker *Broker) PrepareAction(ctx context.Context, request PrepareActionRequest) (Preparation, error) {
	if err := request.Owner.Validate(); err != nil {
		return Preparation{}, err
	}
	if !validIdentifier(request.RequestID) || !validIdentifier(request.SessionID) ||
		!validIdentifier(request.TabID) || !validIdentifier(request.SnapshotID) ||
		request.SnapshotGeneration == 0 ||
		!validContextBinding(request.FrameID, request.ContextCatalogID, request.ContextGeneration) ||
		request.Action.Validate(broker.config.Limits.Effective().TextInputBytes) != nil ||
		(request.Action.Kind == ActionFill && request.Action.Value == "") ||
		(request.Action.Kind == ActionSelect && request.Action.Value == "") ||
		(request.Action.Kind == ActionDialog && !validIdentifier(request.Action.DialogID)) ||
		(artifactInputAction(request.Action.Kind) &&
			(request.Upload == nil || request.Upload.Ref != request.Action.ArtifactRef)) ||
		(!artifactInputAction(request.Action.Kind) && request.Upload != nil) {
		return Preparation{}, fmt.Errorf("%w: malformed action preparation", ErrInvalid)
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	session, slot, worker, err := broker.actionSessionLocked(
		ctx, request.Owner, request.SessionID, request.TabID,
	)
	if err != nil {
		return Preparation{}, err
	}
	if session.SnapshotID != request.SnapshotID ||
		session.SnapshotGeneration != request.SnapshotGeneration ||
		!sessionMatchesContextBinding(
			session,
			request.FrameID,
			request.ContextCatalogID,
			request.ContextGeneration,
		) {
		return Preparation{}, ErrStale
	}
	if session.ContextAuthority != nil {
		contextWorker, ok := worker.(ContextWorker)
		if !ok && session.FrameID != "" {
			return Preparation{}, ErrDriverIncompatible
		}
		if ok {
			err = broker.ensureContextFreshLocked(ctx, session, contextWorker)
		}
		if err != nil {
			return Preparation{}, err
		}
	}
	if session.FrameID != "" {
		return Preparation{}, ErrDriverIncompatible
	}
	boundAction, inputDigest, inputBytes, err := broker.bindActionInput(request.Action)
	if err != nil {
		return Preparation{}, err
	}
	preparedID := derivedIdentifier("prepared", request.Owner, request.SessionID, request.RequestID)
	if existing, getErr := broker.store.GetPreparedAction(ctx, preparedID); getErr == nil {
		if broker.now().UTC().UnixNano() >= existing.ExpiresAt {
			return Preparation{}, ErrStale
		}
		if existing.Owner != request.Owner || existing.SessionID != request.SessionID ||
			existing.TabID != request.TabID || existing.SnapshotID != request.SnapshotID ||
			existing.FrameID != request.FrameID || existing.ContextCatalogID != request.ContextCatalogID ||
			existing.ContextGeneration != request.ContextGeneration ||
			existing.SnapshotGeneration != request.SnapshotGeneration || existing.Action != boundAction ||
			existing.InputDigest != inputDigest || existing.InputBytes != inputBytes {
			return Preparation{}, ErrConflict
		}
		if artifactInputAction(request.Action.Kind) && (request.Upload == nil ||
			existing.ArtifactSHA256 != request.Upload.SHA256 || existing.ArtifactBytes != request.Upload.Size ||
			existing.ArtifactFilename != request.Upload.Filename ||
			existing.ArtifactContentType != request.Upload.ContentType) {
			return Preparation{}, ErrConflict
		}
		broker.rememberActionInput(slot, existing, request.Action.Value)
		broker.rememberUpload(slot, existing, request.Upload)
		return preparationView(existing), nil
	} else if !errors.Is(getErr, ErrNotFound) {
		return Preparation{}, getErr
	}
	prepared, err := broker.resolvePreparedActionLocked(
		ctx, session, slot, worker, request, boundAction, inputDigest, inputBytes,
	)
	if err != nil {
		return Preparation{}, err
	}
	prepared.ProgressSignature, err = browserActionProgressSignature(session.PageStateHash, prepared)
	if err != nil {
		return Preparation{}, err
	}
	if actionRequiresApproval(prepared.Effect) && session.ProgressSignature == prepared.ProgressSignature &&
		session.ProgressCount >= 2 {
		return Preparation{}, ErrNoProgress
	}
	prepared.ID = preparedID
	prepared.RequestID = request.RequestID
	prepared.ActionHash, err = hashPreparedAction(prepared)
	if err != nil {
		return Preparation{}, err
	}
	invocation := Invocation{
		ID:               derivedIdentifier("invocation", request.Owner, request.SessionID, request.RequestID),
		PreparedActionID: prepared.ID, SessionID: session.ID, Owner: request.Owner,
		ActionHash: prepared.ActionHash, Effect: prepared.Effect, State: InvocationPrepared,
		Revision: 1, CreatedAt: prepared.CreatedAt, UpdatedAt: prepared.CreatedAt,
		ExpiresAt: prepared.ExpiresAt,
	}
	if err = broker.store.CreatePreparation(ctx, prepared, invocation); err != nil {
		return Preparation{}, err
	}
	broker.rememberActionInput(slot, prepared, request.Action.Value)
	broker.rememberUpload(slot, prepared, request.Upload)
	return preparationView(prepared), nil
}

func (broker *Broker) ExecuteAction(
	ctx context.Context,
	owner Owner,
	preparedID string,
	approval *ApprovalBinding,
) (Invocation, error) {
	return broker.ExecuteActionWithDownloadSink(ctx, owner, preparedID, approval, nil)
}

func (broker *Broker) ExecuteActionWithDownloadSink(
	ctx context.Context,
	owner Owner,
	preparedID string,
	approval *ApprovalBinding,
	sink DownloadSink,
) (Invocation, error) {
	if err := owner.Validate(); err != nil {
		return Invocation{}, err
	}
	if !validIdentifier(preparedID) {
		return Invocation{}, fmt.Errorf("%w: malformed prepared action ID", ErrInvalid)
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	prepared, err := broker.store.GetPreparedAction(ctx, preparedID)
	if err != nil {
		return Invocation{}, err
	}
	if prepared.Owner != owner {
		return Invocation{}, ErrNotFound
	}
	invocationID := derivedIdentifier("invocation", owner, prepared.SessionID, prepared.RequestID)
	currentInvocation, err := broker.store.GetInvocation(ctx, invocationID)
	if err != nil {
		return Invocation{}, err
	}
	if currentInvocation.State.Terminal() {
		currentInvocation = diagnoseRecoveredOutcome(currentInvocation)
		if currentInvocation.State == InvocationUnknown {
			return currentInvocation, broker.finalizeActionInvocationLocked(
				ctx, prepared.SessionID, currentInvocation, "",
			)
		}
		return currentInvocation, nil
	}
	if currentInvocation.State == InvocationAccepted {
		invocation, executeErr := broker.executePreparedLocked(
			ctx,
			owner,
			invocationID,
			prepared.ActionHash,
			func(context.Context) (json.RawMessage, error) {
				return nil, errors.New("accepted browser action cannot be replayed")
			},
		)
		return invocation, errors.Join(
			executeErr,
			broker.finalizeActionInvocationLocked(ctx, prepared.SessionID, invocation, ""),
		)
	}
	if broker.now().UTC().UnixNano() >= prepared.ExpiresAt {
		expired, completeErr := broker.completeInvocationLocked(
			ctx, currentInvocation, InvocationCanceled, nil, "invocation_expired",
		)
		return expired, errors.Join(ErrStale, completeErr)
	}
	requiresApproval := actionRequiresApproval(prepared.Effect)
	if requiresApproval && !approvalMatches(prepared, approval) {
		return Invocation{}, ErrApprovalRequired
	}
	session, slot, worker, err := broker.actionSessionLocked(
		ctx, owner, prepared.SessionID, prepared.TabID,
	)
	if err != nil {
		return Invocation{}, err
	}
	if err = broker.validateActionInput(slot, prepared); err != nil {
		return Invocation{}, err
	}
	if err = broker.revalidatePreparedLocked(ctx, session, slot, worker, prepared); err != nil {
		if errors.Is(err, ErrDenied) {
			delete(slot.inputs, prepared.ID)
			denied, completeErr := broker.completeInvocationLocked(
				ctx, currentInvocation, InvocationFailed, nil, "policy_denied",
			)
			return denied, errors.Join(ErrDenied, completeErr)
		}
		return Invocation{}, err
	}
	if dryRunDeniesAction(prepared) {
		denied, completeErr := broker.completeInvocationLocked(
			ctx, currentInvocation, InvocationCanceled, nil, "dry_run_denied",
		)
		return denied, errors.Join(ErrDenied, completeErr)
	}
	driverAction, err := broker.driverActionForPrepared(slot, prepared)
	if err != nil {
		return Invocation{}, err
	}
	workerPreparedAction := WorkerPreparedAction{
		InvocationID: invocationID, Prepared: prepared, DriverAction: driverAction,
	}
	preparedWorker, preparedDispatch := worker.(PreparedActionWorker)
	preparedDispatch = preparedDispatch && preparedWorker.SupportsPreparedAction(prepared.Action.Kind)
	if artifactInputAction(prepared.Action.Kind) && preparedDispatch {
		stager, ok := worker.(PreparedActionStager)
		if !ok {
			return currentInvocation, ErrDriverIncompatible
		}
		if stageErr := stager.StagePreparedAction(ctx, workerPreparedAction); stageErr != nil {
			return currentInvocation, stageErr
		}
	}
	invocation, executeErr := broker.executePreparedLocked(
		ctx,
		owner,
		invocationID,
		prepared.ActionHash,
		func(executeCtx context.Context) (json.RawMessage, error) {
			if artifactInputAction(prepared.Action.Kind) || prepared.Action.Kind == ActionDownload {
				if artifactInputAction(prepared.Action.Kind) && preparedDispatch {
					if executeErr := preparedWorker.ExecutePrepared(
						executeCtx,
						workerPreparedAction,
					); executeErr != nil {
						return nil, executeErr
					}
					return json.RawMessage(`{"status":"completed"}`), nil
				}
				transferWorker, ok := worker.(TransferWorker)
				if !ok {
					return nil, ErrDriverIncompatible
				}
				if artifactInputAction(prepared.Action.Kind) {
					checkedUpload, ok := worker.(NavigationCheckedUploadWorker)
					if !ok || slot.navigationID == "" {
						return nil, ErrDriverIncompatible
					}
					if executeErr := checkedUpload.UploadAfterNavigationCheck(
						executeCtx,
						slot.navigationID,
						driverAction,
					); executeErr != nil {
						return nil, executeErr
					}
					return json.RawMessage(`{"status":"completed"}`), nil
				}
				if sink == nil {
					return nil, ErrDriverIncompatible
				}
				download, executeErr := transferWorker.Download(
					executeCtx, driverAction, int64(broker.config.Limits.Effective().DownloadBytes),
				)
				if executeErr != nil {
					return nil, executeErr
				}
				return sink(executeCtx, prepared, download)
			}
			if preparedDispatch {
				if executeErr := preparedWorker.ExecutePrepared(executeCtx, workerPreparedAction); executeErr != nil {
					return nil, executeErr
				}
				return json.RawMessage(`{"status":"completed"}`), nil
			}
			if checkedWorker, ok := worker.(NavigationCheckedActionWorker); ok &&
				navigationCheckedAction(prepared.Action.Kind) {
				if slot.navigationID == "" {
					return nil, ErrStale
				}
				if executeErr := checkedWorker.ExecuteAfterNavigationCheck(
					executeCtx,
					slot.navigationID,
					driverAction,
				); executeErr != nil {
					return nil, executeErr
				}
				return json.RawMessage(`{"status":"completed"}`), nil
			}
			if executeErr := worker.Execute(executeCtx, driverAction); executeErr != nil {
				return nil, executeErr
			}
			return json.RawMessage(`{"status":"completed"}`), nil
		},
	)
	progressSignature := ""
	if invocation.State == InvocationSucceeded && actionRequiresApproval(prepared.Effect) {
		progressSignature = prepared.ProgressSignature
	}
	postActionErr := broker.finalizeActionInvocationLocked(
		ctx, prepared.SessionID, invocation, progressSignature,
	)
	return invocation, errors.Join(executeErr, postActionErr)
}

func (broker *Broker) finalizeActionInvocationLocked(
	ctx context.Context,
	sessionID string,
	invocation Invocation,
	progressSignature string,
) error {
	finalizationCtx, cancelFinalization := context.WithTimeout(
		context.WithoutCancel(ctx),
		time.Duration(broker.config.Limits.Effective().ActionSeconds)*time.Second,
	)
	defer cancelFinalization()
	var finalizationErr error
	if invocation.State == InvocationAccepted || invocation.State.Terminal() {
		finalizationErr = broker.invalidateSnapshotLocked(finalizationCtx, sessionID, progressSignature)
	}
	if invocation.State != InvocationUnknown {
		return finalizationErr
	}
	safeFailure := invocation.SafeFailure
	if safeFailure == "" {
		safeFailure = "outcome_unknown"
	}
	return errors.Join(
		finalizationErr,
		broker.quarantineWorkerSessionLocked(finalizationCtx, sessionID, safeFailure),
	)
}

func (broker *Broker) resolvePreparedActionLocked(
	ctx context.Context,
	session Session,
	slot *workerSlot,
	worker ActionWorker,
	request PrepareActionRequest,
	boundAction Action,
	inputDigest string,
	inputBytes int,
) (PreparedAction, error) {
	now := broker.now().UTC()
	prepared := PreparedAction{
		SessionID: session.ID, Owner: session.Owner, Target: session.Target, Profile: session.Profile,
		ControllerGeneration: session.ControllerGeneration, TabID: session.TabID,
		FrameID: session.FrameID, ContextCatalogID: request.ContextCatalogID,
		ContextGeneration: request.ContextGeneration,
		SnapshotID:        session.SnapshotID, SnapshotGeneration: session.SnapshotGeneration,
		CurrentOrigin: session.SnapshotOrigin, Action: boundAction,
		InputDigest: inputDigest, InputBytes: inputBytes, DryRun: session.DryRun,
		PolicyRevision: session.PolicyRevision, CatalogRevision: worker.CatalogRevision(),
		CreatedAt: now.UnixNano(),
		ExpiresAt: now.Add(time.Duration(broker.config.Limits.Effective().PreparedSeconds) * time.Second).UnixNano(),
	}
	if remote, ok := worker.(PreparedActionWorker); ok && !remote.SupportsPreparedAction(request.Action.Kind) {
		return PreparedAction{}, ErrDenied
	}
	if prepared.CurrentOrigin == initialBlankOrigin && request.Action.Kind != ActionNavigate {
		return PreparedAction{}, ErrDenied
	}
	if !validDigest(prepared.CatalogRevision) {
		return PreparedAction{}, ErrDriverIncompatible
	}
	if !broker.originNetworkAllowed(ctx, session, prepared.CurrentOrigin) {
		return PreparedAction{}, broker.quarantineNetworkDeniedLocked(ctx, session)
	}
	switch request.Action.Kind {
	case ActionNavigate:
		if remote, ok := worker.(PreparedActionWorker); !ok || !remote.SupportsPreparedAction(ActionNavigate) {
			observation, observeErr := worker.Observe(ctx)
			if observeErr != nil {
				return PreparedAction{}, observeErr
			}
			if blankErr := validateBlankObservation(observation, session.SnapshotOrigin); blankErr != nil {
				return PreparedAction{}, blankErr
			}
			if observation.Origin != session.SnapshotOrigin {
				return PreparedAction{}, ErrStale
			}
		}
		normalized, err := normalizeDriverNavigationURL(request.Action.URL)
		if err != nil {
			return PreparedAction{}, err
		}
		destination, err := originFromURL(normalized)
		if err != nil || !broker.originAllowed(session, destination) {
			return PreparedAction{}, ErrDenied
		}
		if !broker.originNetworkAllowed(ctx, session, destination) {
			return PreparedAction{}, broker.quarantineNetworkDeniedLocked(ctx, session)
		}
		prepared.Action.URL = normalized
		prepared.DestinationOrigin = destination
		prepared.Effect = EffectNavigation
	case ActionClick, ActionFill, ActionSelect, ActionCheck, ActionUncheck, ActionHover,
		ActionFileChooser, ActionUpload, ActionDownload:
		element, ok := slot.refs[request.Action.Ref]
		if !ok {
			return PreparedAction{}, ErrStale
		}
		position, positionOK := slot.refPositions[request.Action.Ref]
		if !positionOK || position == 0 {
			return PreparedAction{}, ErrStale
		}
		resolved, origin, resolveErr := worker.Resolve(ctx, element.Target)
		if resolveErr != nil {
			return PreparedAction{}, resolveErr
		}
		if resolved != element || origin != session.SnapshotOrigin {
			return PreparedAction{}, ErrStale
		}
		element = resolved
		prepared.ElementRole = element.Role
		prepared.ElementName = element.Name
		prepared.ElementPosition = position
		switch request.Action.Kind {
		case ActionCheck, ActionUncheck:
			if !checkableElementRole(request.Action.Kind, element.Role) {
				return PreparedAction{}, ErrDenied
			}
			prepared.Effect = EffectLocalEdit
		case ActionHover:
			prepared.Effect = EffectRead
		case ActionFileChooser, ActionUpload:
			if element.Role != "button" || request.Upload == nil || request.Upload.Size < 1 ||
				request.Upload.Size > int64(broker.config.Limits.Effective().UploadBytes) ||
				!validDigest(request.Upload.SHA256) || request.Upload.Path == "" || request.Upload.Filename == "" ||
				request.Upload.ContentType == "" {
				return PreparedAction{}, ErrDenied
			}
			prepared.ArtifactSHA256 = request.Upload.SHA256
			prepared.ArtifactBytes = request.Upload.Size
			prepared.ArtifactFilename = request.Upload.Filename
			prepared.ArtifactContentType = request.Upload.ContentType
			prepared.Effect = EffectLocalEdit
		case ActionDownload:
			prepared.Effect = classifyClickEffect(element)
		case ActionFill:
			if !ordinaryFillElement(element.Role, element.Name, broker.sensitiveFieldTerms(session)) {
				return PreparedAction{}, ErrDenied
			}
			prepared.Effect = EffectLocalEdit
		case ActionSelect:
			if element.Role != "combobox" {
				return PreparedAction{}, ErrDenied
			}
			prepared.Effect = EffectLocalEdit
		default:
			prepared.Effect = classifyClickEffect(element)
		}
	case ActionDrag:
		source, sourceOK := slot.refs[request.Action.SourceRef]
		destination, destinationOK := slot.refs[request.Action.DestinationRef]
		sourcePosition, sourcePositionOK := slot.refPositions[request.Action.SourceRef]
		destinationPosition, destinationPositionOK := slot.refPositions[request.Action.DestinationRef]
		if !sourceOK || !destinationOK || !sourcePositionOK || !destinationPositionOK ||
			sourcePosition == 0 || destinationPosition == 0 || source.Target == destination.Target {
			return PreparedAction{}, ErrStale
		}
		resolvedSource, resolvedDestination, origin, navigationID, validationDelegated, resolveErr := resolveDragBindings(
			ctx,
			worker,
			source,
			destination,
		)
		if resolveErr != nil {
			return PreparedAction{}, resolveErr
		}
		if resolvedSource != source || resolvedDestination != destination ||
			origin != session.SnapshotOrigin ||
			(!validationDelegated && slot.navigationID != "" && navigationID != slot.navigationID) ||
			resolvedSource.Target == resolvedDestination.Target {
			return PreparedAction{}, ErrStale
		}
		prepared.ElementRole = resolvedSource.Role
		prepared.ElementName = resolvedSource.Name
		prepared.ElementPosition = sourcePosition
		prepared.DestinationElementRole = resolvedDestination.Role
		prepared.DestinationElementName = resolvedDestination.Name
		prepared.DestinationElementPosition = destinationPosition
		prepared.Effect = EffectUnknown
	case ActionPress, ActionScroll:
		observation, observeErr := worker.Observe(ctx)
		if observeErr != nil {
			return PreparedAction{}, observeErr
		}
		if blankErr := validateBlankObservation(observation, session.SnapshotOrigin); blankErr != nil {
			return PreparedAction{}, blankErr
		}
		if observation.Origin != session.SnapshotOrigin {
			return PreparedAction{}, ErrStale
		}
		if request.Action.Kind == ActionPress {
			// A page-global key event can run arbitrary same-origin handlers. Until
			// the driver can bind a press to a revalidated element, its effect is
			// unknown even when the key itself is allowlisted.
			prepared.Effect = EffectUnknown
		} else {
			prepared.Effect = EffectRead
		}
	case ActionDialog:
		observation, observeErr := worker.Observe(ctx)
		if observeErr != nil {
			return PreparedAction{}, observeErr
		}
		if blankErr := validateBlankObservation(observation, session.SnapshotOrigin); blankErr != nil {
			return PreparedAction{}, blankErr
		}
		if observation.Origin != session.SnapshotOrigin || observation.PendingDialog == nil ||
			request.Action.DialogID != stableDialogRef(
				session.SnapshotID,
				observation.PendingDialog.Type,
				observation.PendingDialog.Message,
			) {
			return PreparedAction{}, ErrStale
		}
		prepared.DialogType = observation.PendingDialog.Type
		prepared.DialogMessageDigest = dialogMessageDigest(
			observation.PendingDialog.Type,
			observation.PendingDialog.Message,
		)
		prepared.DialogMessageBytes = len(observation.PendingDialog.Message)
		if request.Action.PromptProvided && prepared.DialogType != "prompt" {
			return PreparedAction{}, ErrDenied
		}
		prepared.Effect = classifyDialogEffect(request.Action.Decision)
	default:
		return PreparedAction{}, ErrInvalid
	}
	return prepared, nil
}

func (broker *Broker) revalidatePreparedLocked(
	ctx context.Context,
	session Session,
	slot *workerSlot,
	worker ActionWorker,
	prepared PreparedAction,
) error {
	if session.ContextAuthority != nil {
		contextWorker, ok := worker.(ContextWorker)
		if !ok && session.FrameID != "" {
			return ErrDriverIncompatible
		}
		if ok {
			if err := broker.ensureContextFreshLocked(ctx, session, contextWorker); err != nil {
				return err
			}
		}
	}
	if prepared.FrameID != "" {
		return ErrDriverIncompatible
	}
	if broker.now().UTC().UnixNano() >= prepared.ExpiresAt || session.PolicyRevision != prepared.PolicyRevision ||
		session.Target != prepared.Target || session.Profile != prepared.Profile ||
		session.ControllerGeneration != prepared.ControllerGeneration || session.TabID != prepared.TabID ||
		!sessionMatchesContextBinding(
			session,
			prepared.FrameID,
			prepared.ContextCatalogID,
			prepared.ContextGeneration,
		) ||
		session.SnapshotID != prepared.SnapshotID || session.SnapshotGeneration != prepared.SnapshotGeneration ||
		session.SnapshotOrigin != prepared.CurrentOrigin || worker.CatalogRevision() != prepared.CatalogRevision {
		return ErrStale
	}
	if !broker.originNetworkAllowed(ctx, session, prepared.CurrentOrigin) ||
		(prepared.DestinationOrigin != "" &&
			!broker.originNetworkAllowed(ctx, session, prepared.DestinationOrigin)) {
		return broker.quarantineNetworkDeniedLocked(ctx, session)
	}
	if prepared.Action.Kind == ActionNavigate || prepared.Action.Kind == ActionPress ||
		prepared.Action.Kind == ActionScroll {
		if remote, ok := worker.(PreparedActionWorker); ok && remote.SupportsPreparedAction(prepared.Action.Kind) {
			return nil
		}
		observation, err := worker.Observe(ctx)
		if err != nil {
			return err
		}
		if err = validateBlankObservation(observation, prepared.CurrentOrigin); err != nil {
			return err
		}
		if observation.Origin != prepared.CurrentOrigin {
			return ErrStale
		}
		return nil
	}
	if prepared.Action.Kind == ActionDialog {
		observation, err := worker.Observe(ctx)
		if err != nil {
			return err
		}
		if err = validateBlankObservation(observation, prepared.CurrentOrigin); err != nil {
			return err
		}
		if observation.Origin != prepared.CurrentOrigin || observation.PendingDialog == nil ||
			prepared.Action.DialogID != stableDialogRef(
				prepared.SnapshotID,
				observation.PendingDialog.Type,
				observation.PendingDialog.Message,
			) ||
			observation.PendingDialog.Type != prepared.DialogType ||
			dialogMessageDigest(
				observation.PendingDialog.Type,
				observation.PendingDialog.Message,
			) != prepared.DialogMessageDigest ||
			len(observation.PendingDialog.Message) != prepared.DialogMessageBytes {
			return ErrStale
		}
		return nil
	}
	if prepared.Action.Kind == ActionDrag {
		source, sourceOK := slot.refs[prepared.Action.SourceRef]
		destination, destinationOK := slot.refs[prepared.Action.DestinationRef]
		if !sourceOK || !destinationOK || source.Target == destination.Target {
			return ErrStale
		}
		resolvedSource, resolvedDestination, origin, navigationID, validationDelegated, err := resolveDragBindings(
			ctx,
			worker,
			source,
			destination,
		)
		if err != nil {
			return err
		}
		if origin != prepared.CurrentOrigin ||
			(!validationDelegated && slot.navigationID != "" && navigationID != slot.navigationID) ||
			resolvedSource != source || resolvedDestination != destination ||
			resolvedSource.Target == resolvedDestination.Target ||
			resolvedSource.Role != prepared.ElementRole || resolvedSource.Name != prepared.ElementName ||
			resolvedDestination.Role != prepared.DestinationElementRole ||
			resolvedDestination.Name != prepared.DestinationElementName {
			return ErrStale
		}
		return nil
	}
	element, ok := slot.refs[prepared.Action.Ref]
	if !ok {
		return ErrStale
	}
	resolved, origin, err := worker.Resolve(ctx, element.Target)
	if err != nil {
		return err
	}
	if origin != prepared.CurrentOrigin || resolved != element ||
		resolved.Role != prepared.ElementRole || resolved.Name != prepared.ElementName {
		return ErrStale
	}
	if prepared.Action.Kind == ActionFill {
		if remote, ok := worker.(PreparedActionWorker); ok && remote.SupportsPreparedAction(ActionFill) {
			return nil
		}
		authorizer, ok := worker.(ProtectedFillWorker)
		if !ok || slot.navigationID == "" {
			return ErrDriverIncompatible
		}
		if err = authorizer.AuthorizeFill(ctx, slot.navigationID, resolved.Target); err != nil {
			return err
		}
	}
	return nil
}

func (broker *Broker) actionSessionLocked(
	ctx context.Context,
	owner Owner,
	sessionID string,
	tabID string,
) (Session, *workerSlot, ActionWorker, error) {
	session, err := broker.store.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, nil, nil, err
	}
	if !session.Owner.Equal(owner) {
		return Session{}, nil, nil, ErrNotFound
	}
	if session.State != SessionReady || session.EffectiveController() != ControllerAgent || session.TabID != tabID {
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
	slot := broker.slots[session.ID]
	if slot == nil {
		return Session{}, nil, nil, ErrWorkerUnavailable
	}
	if slot.safeFailure != "" {
		return Session{}, nil, nil, ErrWorkerUnavailable
	}
	worker, ok := slot.worker.(ActionWorker)
	if !ok {
		return Session{}, nil, nil, ErrDriverIncompatible
	}
	return session, slot, worker, nil
}

func (broker *Broker) quarantineWorkerSessionLocked(
	ctx context.Context,
	sessionID string,
	safeFailure string,
) error {
	limits := broker.config.Limits.Effective()
	quarantineCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		time.Duration(limits.ActionSeconds)*time.Second,
	)
	defer cancel()
	session, err := broker.store.GetSession(quarantineCtx, sessionID)
	if err != nil || session.State.Terminal() {
		return err
	}
	_, err = broker.finishSessionLocked(
		quarantineCtx,
		session,
		SessionLost,
		safeFailure,
	)
	return err
}

func (broker *Broker) invalidateSnapshotLocked(
	ctx context.Context,
	sessionID string,
	progressSignature string,
) error {
	session, err := broker.store.GetSession(ctx, sessionID)
	if err != nil || session.State != SessionReady {
		return err
	}
	current := session
	slot := broker.slots[sessionID]
	if slot != nil {
		// Live references become invalid at dispatch and must never depend on a
		// later durable write succeeding.
		slot.refs = nil
		slot.refPositions = nil
		slot.inputs = nil
		slot.uploads = nil
		slot.navigationID = ""
	}
	session.SnapshotID = ""
	session.SnapshotOrigin = ""
	session.PageStateHash = ""
	if progressSignature != "" {
		if session.ProgressSignature == progressSignature {
			session.ProgressCount++
		} else {
			session.ProgressSignature = progressSignature
			session.ProgressCount = 1
		}
	}
	session.Revision++
	session.UpdatedAt = timestampAtLeast(broker.now().UTC().UnixNano(), session.UpdatedAt)
	session.LastActivityAt = session.UpdatedAt
	if err = broker.store.UpdateSession(ctx, session.Revision-1, session); err != nil {
		if slot != nil {
			slot.safeFailure = "snapshot_invalidation_failed"
		}
		if fileutil.IsCommittedWriteError(err) {
			persisted, getErr := broker.store.GetSession(context.WithoutCancel(ctx), session.ID)
			if getErr != nil {
				return errors.Join(ErrSnapshotInvalidation, err, getErr)
			}
			current = persisted
		}
		limits := broker.config.Limits.Effective()
		quarantineCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			time.Duration(limits.ActionSeconds)*time.Second,
		)
		defer cancel()
		_, quarantineErr := broker.finishSessionLocked(
			quarantineCtx,
			current,
			SessionLost,
			"snapshot_invalidation_failed",
		)
		return errors.Join(ErrSnapshotInvalidation, err, quarantineErr)
	}
	return nil
}

func observeWithNavigationCheck(
	ctx context.Context,
	worker ActionWorker,
) (DriverObservation, string, error) {
	if bound, ok := worker.(BoundObservationWorker); ok {
		observation, identity, err := bound.ObserveWithNavigationIdentity(ctx)
		if err != nil {
			return DriverObservation{}, "", err
		}
		return observation, identity, nil
	}
	checkedWorker, ok := worker.(NavigationIdentityWorker)
	if !ok {
		observation, err := worker.Observe(ctx)
		return observation, "", err
	}
	before, err := checkedWorker.NavigationIdentity(ctx)
	if err != nil {
		return DriverObservation{}, "", err
	}
	observation, err := worker.Observe(ctx)
	if err != nil {
		return DriverObservation{}, "", err
	}
	after, err := checkedWorker.NavigationIdentity(ctx)
	if err != nil {
		return DriverObservation{}, "", err
	}
	if before == "" || before != after {
		return DriverObservation{}, "", ErrStale
	}
	return observation, after, nil
}

func resolveDragFromFreshObservation(
	ctx context.Context,
	worker ActionWorker,
	source DriverElement,
	destination DriverElement,
) (DriverElement, DriverElement, string, string, error) {
	observation, navigationID, err := observeWithNavigationCheck(ctx, worker)
	if err != nil {
		return DriverElement{}, DriverElement{}, "", "", err
	}
	var resolvedSource DriverElement
	var resolvedDestination DriverElement
	for _, element := range observation.Elements {
		switch element.Target {
		case source.Target:
			resolvedSource = element
		case destination.Target:
			resolvedDestination = element
		}
	}
	if resolvedSource.Target == "" || resolvedDestination.Target == "" {
		return DriverElement{}, DriverElement{}, "", "", ErrStale
	}
	return resolvedSource, resolvedDestination, observation.Origin, navigationID, nil
}

func resolveDragBindings(
	ctx context.Context,
	worker ActionWorker,
	source DriverElement,
	destination DriverElement,
) (DriverElement, DriverElement, string, string, bool, error) {
	if remote, ok := worker.(PreparedActionWorker); ok && remote.SupportsPreparedAction(ActionDrag) {
		resolvedSource, sourceOrigin, err := worker.Resolve(ctx, source.Target)
		if err != nil {
			return DriverElement{}, DriverElement{}, "", "", true, err
		}
		resolvedDestination, destinationOrigin, err := worker.Resolve(ctx, destination.Target)
		if err != nil {
			return DriverElement{}, DriverElement{}, "", "", true, err
		}
		if sourceOrigin == "" || sourceOrigin != destinationOrigin {
			return DriverElement{}, DriverElement{}, "", "", true, ErrStale
		}
		// Remote observations expose snapshot-bound node-broker references. A
		// second outer observation would rotate those references before the
		// prepared action reaches the node. Bind the current metadata here and
		// leave the mandatory live revalidation to ExecutePrepared on the
		// remote broker, which owns the original references and driver state.
		return resolvedSource, resolvedDestination, sourceOrigin, "", true, nil
	}
	resolvedSource, resolvedDestination, origin, navigationID, err := resolveDragFromFreshObservation(
		ctx,
		worker,
		source,
		destination,
	)
	return resolvedSource, resolvedDestination, origin, navigationID, false, err
}

func navigationCheckedAction(kind ActionKind) bool {
	switch kind {
	case ActionClick, ActionFill, ActionSelect, ActionCheck, ActionUncheck, ActionHover, ActionDrag, ActionPress,
		ActionScroll:
		return true
	default:
		return false
	}
}

func preparationView(prepared PreparedAction) Preparation {
	return Preparation{
		Action: prepared,
		Approval: ApprovalBinding{
			PreparedActionID: prepared.ID, ActionHash: prepared.ActionHash,
			PolicyRevision: prepared.PolicyRevision, ExpiresAt: prepared.ExpiresAt,
		},
		RequiresApproval: actionRequiresApproval(prepared.Effect),
	}
}

func approvalMatches(prepared PreparedAction, approval *ApprovalBinding) bool {
	return approval != nil && approval.PreparedActionID == prepared.ID &&
		approval.ActionHash == prepared.ActionHash && approval.PolicyRevision == prepared.PolicyRevision &&
		approval.ExpiresAt == prepared.ExpiresAt
}

func actionRequiresApproval(effect Effect) bool {
	return effect == EffectExternalCommit || effect == EffectUnknown
}

func dryRunDeniesAction(prepared PreparedAction) bool {
	if !prepared.DryRun {
		return false
	}
	if prepared.Effect == EffectExternalCommit {
		return true
	}
	// A B2 download remains approval-bound when the page element cannot prove
	// read-only semantics. Unlike another unknown page action, however, the
	// typed download path establishes a one-file expectation before dispatch
	// and retains only the bounded result. Allow that exact approved operation
	// without weakening dry-run for clicks, key presses, dialogs, or submits.
	return prepared.Effect == EffectUnknown && prepared.Action.Kind != ActionDownload
}

func classifyClickEffect(element DriverElement) Effect {
	switch element.Role {
	case "button":
		return EffectExternalCommit
	default:
		// Accessibility role alone cannot prove that a click is a side-effect-free
		// navigation. A later adapter may lower this only after resolving a plain
		// destination with no submit or script semantics.
		return EffectUnknown
	}
}

func classifyDialogEffect(decision string) Effect {
	if decision == "dismiss" {
		return EffectRead
	}
	return EffectExternalCommit
}

func validDialogType(dialogType string) bool {
	switch dialogType {
	case "alert", "beforeunload", "confirm", "prompt":
		return true
	default:
		return false
	}
}

func cloneDialogObservation(dialog *DialogObservation) *DialogObservation {
	if dialog == nil {
		return nil
	}
	cloned := *dialog
	return &cloned
}

func editableElementRole(role string) bool {
	return role == "textbox" || role == "searchbox" || role == "combobox"
}

func ordinaryFillElement(role, name string, sensitiveTerms ...[]string) bool {
	var configured []string
	if len(sensitiveTerms) > 0 {
		configured = sensitiveTerms[0]
	}
	return browserpolicy.OrdinaryFillField(role, name, configured)
}

func (broker *Broker) sensitiveFieldTerms(session Session) []string {
	target, ok := broker.config.Targets[session.Target]
	if !ok {
		return nil
	}
	return target.Profiles[session.Profile].SensitiveFields
}

func (broker *Broker) driverActionForPrepared(
	slot *workerSlot,
	prepared PreparedAction,
) (DriverAction, error) {
	switch prepared.Action.Kind {
	case ActionNavigate:
		return DriverAction{Kind: DriverNavigate, URL: prepared.Action.URL}, nil
	case ActionClick, ActionFill, ActionSelect, ActionCheck, ActionUncheck, ActionHover,
		ActionFileChooser, ActionUpload, ActionDownload:
		element, ok := slot.refs[prepared.Action.Ref]
		if !ok {
			return DriverAction{}, ErrStale
		}
		kind := DriverClick
		value := ""
		if prepared.Action.Kind == ActionFill || prepared.Action.Kind == ActionSelect {
			kind = DriverFill
			if prepared.Action.Kind == ActionSelect {
				kind = DriverSelect
			}
			var ok bool
			value, ok = slot.inputs[prepared.ID]
			if !ok || !broker.actionInputMatches(prepared, value) {
				return DriverAction{}, ErrStale
			}
		}
		switch prepared.Action.Kind {
		case ActionCheck:
			kind = DriverCheck
		case ActionUncheck:
			kind = DriverUncheck
		case ActionHover:
			kind = DriverHover
		case ActionFileChooser, ActionUpload:
			kind = DriverUpload
			binding, exists := slot.uploads[prepared.ID]
			if !exists || binding.Ref != prepared.Action.ArtifactRef || binding.SHA256 != prepared.ArtifactSHA256 ||
				binding.Size != prepared.ArtifactBytes || binding.Filename != prepared.ArtifactFilename ||
				binding.ContentType != prepared.ArtifactContentType || binding.Path == "" {
				return DriverAction{}, ErrStale
			}
			value = binding.Path
		case ActionDownload:
			kind = DriverDownloadAction
		}
		return DriverAction{
			Kind: kind, Target: element.Target, Element: element.Name, Value: value,
			ArtifactSHA256: prepared.ArtifactSHA256, ArtifactBytes: prepared.ArtifactBytes,
			ArtifactFilename: prepared.ArtifactFilename, ArtifactContentType: prepared.ArtifactContentType,
		}, nil
	case ActionDrag:
		source, sourceOK := slot.refs[prepared.Action.SourceRef]
		destination, destinationOK := slot.refs[prepared.Action.DestinationRef]
		if !sourceOK || !destinationOK || source.Target == destination.Target {
			return DriverAction{}, ErrStale
		}
		return DriverAction{
			Kind: DriverDrag, Target: source.Target, Element: source.Name,
			DestinationTarget: destination.Target, DestinationElement: destination.Name,
		}, nil
	case ActionPress:
		return DriverAction{Kind: DriverPress, Key: prepared.Action.Key}, nil
	case ActionScroll:
		return DriverAction{
			Kind: DriverScroll, Direction: prepared.Action.Direction, Amount: prepared.Action.Amount,
		}, nil
	case ActionDialog:
		value := ""
		if prepared.Action.PromptProvided {
			var ok bool
			value, ok = slot.inputs[prepared.ID]
			if !ok || !broker.actionInputMatches(prepared, value) {
				return DriverAction{}, ErrStale
			}
		}
		return DriverAction{
			Kind: DriverDialog, Accept: prepared.Action.Decision == "accept", Value: value,
			PromptProvided: prepared.Action.PromptProvided,
		}, nil
	default:
		return DriverAction{}, ErrInvalid
	}
}

func (broker *Broker) rememberUpload(slot *workerSlot, prepared PreparedAction, binding *UploadBinding) {
	if !artifactInputAction(prepared.Action.Kind) || binding == nil {
		return
	}
	if slot.uploads == nil {
		slot.uploads = make(map[string]UploadBinding)
	}
	slot.uploads[prepared.ID] = *binding
}

func artifactInputAction(kind ActionKind) bool {
	return kind == ActionFileChooser || kind == ActionUpload
}

func (broker *Broker) bindActionInput(action Action) (Action, string, int, error) {
	if !actionHasSensitiveInput(action) {
		return action, "", 0, nil
	}
	if len(broker.bindingKey) != sha256.Size {
		return Action{}, "", 0, ErrInvalid
	}
	mac := hmac.New(sha256.New, broker.bindingKey)
	_, _ = mac.Write([]byte(action.Value))
	digest := hex.EncodeToString(mac.Sum(nil))
	bound := action
	bound.Value = ""
	return bound, digest, len(action.Value), nil
}

func (broker *Broker) actionInputMatches(prepared PreparedAction, value string) bool {
	_, digest, size, err := broker.bindActionInput(Action{
		Kind: prepared.Action.Kind, Decision: prepared.Action.Decision, Value: value,
		PromptProvided: prepared.Action.PromptProvided,
	})
	return err == nil && size == prepared.InputBytes && hmac.Equal(
		[]byte(digest), []byte(prepared.InputDigest),
	)
}

func (broker *Broker) validateActionInput(slot *workerSlot, prepared PreparedAction) error {
	if prepared.InputDigest == "" {
		return nil
	}
	value, ok := slot.inputs[prepared.ID]
	if !ok || !broker.actionInputMatches(prepared, value) {
		return ErrStale
	}
	return nil
}

func (broker *Broker) rememberActionInput(
	slot *workerSlot,
	prepared PreparedAction,
	value string,
) {
	if prepared.InputDigest == "" {
		return
	}
	if slot.inputs == nil {
		slot.inputs = make(map[string]string)
	}
	slot.inputs[prepared.ID] = value
}

func actionHasSensitiveInput(action Action) bool {
	return action.Kind == ActionFill || action.Kind == ActionSelect ||
		(action.Kind == ActionDialog && action.PromptProvided)
}

func (broker *Broker) originAllowed(session Session, origin string) bool {
	if origin == initialBlankOrigin {
		return true
	}
	target, ok := broker.config.Targets[session.Target]
	if !ok {
		return false
	}
	profile, ok := target.Profiles[session.Profile]
	if !ok {
		return false
	}
	if profile.EffectiveNetworkMode() == config.BrowserNetworkPublicWeb {
		normalized, err := config.NormalizeBrowserOrigin(origin)
		return err == nil && normalized == origin
	}
	if profile.EffectiveNetworkMode() == config.BrowserNetworkAnyHTTP {
		normalized, err := config.NormalizeBrowserHTTPOrigin(origin)
		return err == nil && normalized == origin
	}
	for _, allowed := range profile.AllowedOrigins {
		normalized, err := config.NormalizeBrowserOrigin(allowed)
		if err == nil && normalized == origin {
			return true
		}
	}
	return false
}

func (broker *Broker) originNetworkAllowed(ctx context.Context, session Session, origin string) bool {
	if origin == initialBlankOrigin {
		return true
	}
	target, ok := broker.config.Targets[session.Target]
	if !ok {
		return false
	}
	profile, ok := target.Profiles[session.Profile]
	if !ok {
		return false
	}
	anyHTTP := profile.EffectiveNetworkMode() == config.BrowserNetworkAnyHTTP
	normalized, err := config.NormalizeBrowserOrigin(origin)
	if anyHTTP {
		normalized, err = config.NormalizeBrowserHTTPOrigin(origin)
	}
	if err != nil || normalized != origin {
		return false
	}
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Hostname() == "" || broker.lookupIP == nil {
		return false
	}
	host := parsed.Hostname()
	if address, addressErr := netip.ParseAddr(host); addressErr == nil {
		return anyHTTP || config.IsPublicBrowserIP(net.IP(address.AsSlice()))
	}
	addresses, err := broker.lookupIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 || len(addresses) > 32 {
		return false
	}
	for _, address := range addresses {
		if address == nil || (address.To4() == nil && address.To16() == nil) ||
			(!anyHTTP && !config.IsPublicBrowserIP(address)) {
			return false
		}
	}
	return true
}

func validInitialBlankObservation(observation DriverObservation) bool {
	return observation.URL == initialBlankOrigin && observation.Origin == initialBlankOrigin &&
		observation.Title == "" && observation.Snapshot == "" && len(observation.Elements) == 0 &&
		observation.PendingDialog == nil && !observation.Truncated
}

func validateBlankObservation(observation DriverObservation, expectedOrigin string) error {
	if observation.URL != initialBlankOrigin && observation.Origin != initialBlankOrigin &&
		expectedOrigin != initialBlankOrigin {
		return nil
	}
	if !validInitialBlankObservation(observation) {
		return ErrDriverIncompatible
	}
	return nil
}

func (broker *Broker) quarantineNetworkDeniedLocked(ctx context.Context, session Session) error {
	_, finishErr := broker.finishSessionLocked(ctx, session, SessionLost, "network_denied")
	return errors.Join(ErrDenied, finishErr)
}

func originFromURL(raw string) (string, error) {
	parsed, err := normalizeDriverNavigationURL(raw)
	if err != nil {
		return "", err
	}
	_, origin, err := sanitizeObservedURL(parsed)
	return origin, err
}

func stableElementRef(snapshotID, target string) string {
	digest := sha256.Sum256([]byte(snapshotID + "\x00" + target))
	return "ref_" + hex.EncodeToString(digest[:16])
}

func stableDialogRef(snapshotID, dialogType, message string) string {
	digest := sha256.Sum256([]byte(snapshotID + "\x00" + dialogType + "\x00" + message))
	return "dialog_" + hex.EncodeToString(digest[:16])
}

func dialogMessageDigest(dialogType, message string) string {
	digest := sha256.Sum256([]byte("mintclaw.browser.dialog-message.v1\x00" + dialogType + "\x00" + message))
	return hex.EncodeToString(digest[:])
}

func derivedIdentifier(prefix string, owner Owner, values ...string) string {
	payload := strings.Join([]string{
		owner.ActorID, owner.AgentID, owner.SessionKey, owner.ExecutionID,
	}, "\x00") + "\x00" + strings.Join(values, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return prefix + "_" + hex.EncodeToString(digest[:16])
}

func hashPreparedAction(prepared PreparedAction) (string, error) {
	prepared.ActionHash = ""
	encoded, err := json.Marshal(prepared)
	if err != nil {
		return "", fmt.Errorf("encode prepared browser action: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func browserPageStateHash(observation DriverObservation) (string, error) {
	snapshot := observation.Snapshot
	for _, element := range observation.Elements {
		if element.Target != "" {
			snapshot = strings.ReplaceAll(snapshot, "[ref="+element.Target+"]", "[ref]")
		}
	}
	payload := struct {
		URL           string
		Origin        string
		Title         string
		Snapshot      string
		PendingDialog *DialogObservation
		Truncated     bool
	}{
		URL: observation.URL, Origin: observation.Origin, Title: observation.Title,
		Snapshot: snapshot, PendingDialog: observation.PendingDialog, Truncated: observation.Truncated,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode browser page state: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func browserActionProgressSignature(pageStateHash string, prepared PreparedAction) (string, error) {
	if !validDigest(pageStateHash) {
		return "", nil
	}
	action := prepared.Action
	action.Ref = ""
	action.SourceRef = ""
	action.DestinationRef = ""
	action.DialogID = ""
	action.ArtifactRef = ""
	payload := struct {
		PageStateHash       string
		Action              Action
		InputDigest         string
		ArtifactSHA256      string
		ElementRole         string
		ElementName         string
		ElementPosition     uint32
		DestinationRole     string
		DestinationName     string
		DestinationPosition uint32
		DialogMessageDigest string
	}{
		PageStateHash: pageStateHash, Action: action, InputDigest: prepared.InputDigest,
		ArtifactSHA256: prepared.ArtifactSHA256, ElementRole: prepared.ElementRole,
		ElementName: prepared.ElementName, ElementPosition: prepared.ElementPosition,
		DestinationRole:     prepared.DestinationElementRole,
		DestinationName:     prepared.DestinationElementName,
		DestinationPosition: prepared.DestinationElementPosition,
		DialogMessageDigest: prepared.DialogMessageDigest,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode browser action progress: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
