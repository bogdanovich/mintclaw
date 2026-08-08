//go:build linux || darwin

package coordinator

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes/update/control"
)

const (
	DefaultHealthTimeout = 90 * time.Second
	steadyRestartDelay   = time.Second
)

var (
	errActivationRequested = errors.New("node update activation requested")
	errHealthCommitted     = errors.New("node update health committed")
)

type Supervisor struct {
	coordinator   *Coordinator
	healthTimeout time.Duration
	launch        func(context.Context, State) (supervisedChild, error)
}

type supervisedChild interface {
	incomingFrames() <-chan control.Incoming
	completion() <-chan error
	respond(control.Response) error
	stop()
}

func NewSupervisor(coordinator *Coordinator, healthTimeout time.Duration) (*Supervisor, error) {
	if coordinator == nil || coordinator.store == nil || healthTimeout < time.Second || healthTimeout > 10*time.Minute {
		return nil, errors.New("supervisor requires a coordinator and bounded health timeout")
	}
	return &Supervisor{
		coordinator: coordinator, healthTimeout: healthTimeout,
		launch: func(ctx context.Context, state State) (supervisedChild, error) {
			return coordinator.store.launchSelected(ctx, state)
		},
	}, nil
}

func (supervisor *Supervisor) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := supervisor.coordinator.store.Load()
		if err != nil {
			return err
		}
		if state.Transaction != nil {
			switch state.Transaction.Phase {
			case PhasePrepared, PhaseDownloading, PhaseVerified, PhaseStaged:
				if !state.Transaction.Canceled &&
					supervisor.coordinator.now().UTC().Unix() >= state.Transaction.ExpiresAt {
					if _, err = supervisor.coordinator.RequireOperatorAction(
						state.Transaction.Identity,
						"request_expired",
					); err != nil {
						return err
					}
					continue
				}
			case PhaseActivating, PhaseRollingBack:
				if err = supervisor.runUpdateAttempt(ctx, state); err != nil {
					return err
				}
				continue
			case PhaseUnknown:
				<-ctx.Done()
				return ctx.Err()
			case PhaseOperatorActionRequired:
				if state.Transaction.ActivationAttempted {
					<-ctx.Done()
					return ctx.Err()
				}
			}
		}
		child, err := supervisor.launch(ctx, state)
		if err != nil {
			return err
		}
		err = supervisor.serveChild(ctx, child, false, state)
		child.stop()
		if err != nil && !errors.Is(err, errActivationRequested) && !errors.Is(err, context.Canceled) {
			if waitErr := waitContext(ctx, steadyRestartDelay); waitErr != nil {
				return waitErr
			}
		}
	}
}

func (supervisor *Supervisor) runUpdateAttempt(ctx context.Context, state State) error {
	transaction := state.Transaction
	if transaction.LaunchAttempts >= MaxLaunchAttempts {
		if transaction.Phase == PhaseActivating {
			_, err := supervisor.coordinator.BeginRollback(transaction.Identity, "successor_unhealthy")
			return err
		}
		_, err := supervisor.coordinator.RequireOperatorAction(transaction.Identity, "rollback_unproven")
		return err
	}
	state, err := supervisor.coordinator.RecordLaunch(transaction.Identity)
	if err != nil {
		return err
	}
	child, err := supervisor.launch(ctx, state)
	if err != nil {
		return supervisor.recordAttemptFailure(transaction.Identity, transaction.Phase, "payload_start_failed")
	}
	err = supervisor.serveChild(ctx, child, true, state)
	if errors.Is(err, errHealthCommitted) {
		err = supervisor.serveChild(ctx, child, false, state)
		child.stop()
		if errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}
	child.stop()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, errActivationRequested) {
		return errors.New("candidate requested a nested update before health was proven")
	}
	return supervisor.recordAttemptFailure(transaction.Identity, transaction.Phase, "health_unproven")
}

func (supervisor *Supervisor) recordAttemptFailure(
	identity ExecutionIdentity,
	phase Phase,
	code string,
) error {
	state, err := supervisor.coordinator.store.Load()
	if err != nil {
		return err
	}
	if state.Transaction == nil || !sameExecution(state.Transaction.Identity, identity) {
		return ErrTransactionConflict
	}
	if state.Transaction.LaunchAttempts < MaxLaunchAttempts {
		return nil
	}
	if phase == PhaseActivating {
		_, err = supervisor.coordinator.BeginRollback(identity, code)
		return err
	}
	_, err = supervisor.coordinator.RequireOperatorAction(identity, "rollback_unproven")
	return err
}

func (supervisor *Supervisor) serveChild(
	ctx context.Context,
	child supervisedChild,
	expectHealth bool,
	state State,
) error {
	type requestResult struct {
		activated bool
		err       error
	}
	requestContext, cancelRequests := context.WithCancel(ctx)
	requestResults := make(chan requestResult, 4)
	var requests sync.WaitGroup
	defer func() {
		cancelRequests()
		requests.Wait()
	}()
	activeRequests := 0
	var timeout <-chan time.Time
	var timer *time.Timer
	if expectHealth {
		timer = time.NewTimer(supervisor.healthTimeout)
		timeout = timer.C
		defer timer.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-child.completion():
			if err == nil {
				return errors.New("companion payload exited before shutdown")
			}
			return err
		case <-timeout:
			return errors.New("companion health observation timed out")
		case result := <-requestResults:
			activeRequests--
			if result.err != nil {
				return result.err
			}
			if result.activated {
				return errActivationRequested
			}
		case incoming, open := <-child.incomingFrames():
			if !open {
				return errors.New("companion control connection closed")
			}
			if incoming.Request != nil {
				if activeRequests >= cap(requestResults) {
					response := control.Response{
						SchemaVersion: control.SchemaVersion, RequestID: incoming.Request.RequestID,
						Observation: control.Observation{Phase: "unknown"}, ErrorCode: "request_busy",
					}
					if err := child.respond(response); err != nil {
						return err
					}
					continue
				}
				activeRequests++
				request := *incoming.Request
				requests.Add(1)
				go func() {
					defer requests.Done()
					activated, err := supervisor.handleRequest(requestContext, child, request)
					requestResults <- requestResult{activated: activated, err: err}
				}()
				continue
			}
			if incoming.Health == nil || !expectHealth || state.Transaction == nil {
				continue
			}
			health := HealthObservation{
				NodeID: incoming.Health.NodeID, Version: incoming.Health.Version,
				Platform: incoming.Health.Platform, Architecture: incoming.Health.Architecture,
				CatalogHash: incoming.Health.CatalogHash, ObservedAt: time.Now().UTC(),
			}
			if state.Transaction.Phase == PhaseActivating {
				_, err := supervisor.coordinator.CommitHealthy(state.Transaction.Identity, health)
				if err != nil {
					return err
				}
				return errHealthCommitted
			}
			_, err := supervisor.coordinator.CommitRolledBack(state.Transaction.Identity, health)
			if err != nil {
				return err
			}
			return errHealthCommitted
		}
	}
}

func (supervisor *Supervisor) handleRequest(
	ctx context.Context,
	child supervisedChild,
	request control.Request,
) (bool, error) {
	response := control.Response{SchemaVersion: control.SchemaVersion, RequestID: request.RequestID}
	activated := false
	switch request.Kind {
	case control.KindUpdate:
		stageRequest := fromControlUpdate(*request.Update)
		state, err := supervisor.coordinator.Stage(ctx, stageRequest)
		if err == nil && state.Transaction != nil && state.Transaction.Phase == PhaseStaged {
			state, err = supervisor.coordinator.Activate(stageRequest.Identity)
		}
		response.Observation = observationFromState(state)
		response.ErrorCode = safeCoordinatorError(err)
		activated = err == nil && state.Transaction != nil && state.Transaction.Phase == PhaseActivating
	case control.KindStatus:
		state, err := supervisor.status(fromControlIdentity(*request.Identity))
		response.Observation = observationFromState(state)
		response.ErrorCode = safeCoordinatorError(err)
	case control.KindCancel:
		state, err := supervisor.coordinator.Cancel(fromControlIdentity(*request.Identity))
		response.Observation = observationFromState(state)
		response.ErrorCode = safeCoordinatorError(err)
	default:
		response.Observation = control.Observation{Phase: "unknown"}
		response.ErrorCode = "request_invalid"
	}
	if err := child.respond(response); err != nil {
		return false, err
	}
	return activated, nil
}

func (supervisor *Supervisor) status(identity ExecutionIdentity) (State, error) {
	state, err := supervisor.coordinator.store.Load()
	if err != nil {
		return State{}, err
	}
	if state.Transaction == nil || !sameExecution(state.Transaction.Identity, identity) {
		return State{}, ErrTransactionConflict
	}
	return state, nil
}

func fromControlUpdate(request control.UpdateRequest) StageRequest {
	return StageRequest{
		Identity: fromControlIdentity(request.Identity), Profile: request.Profile, ReleaseAlias: request.ReleaseAlias,
		ExpectedManifestSHA256: request.ExpectedManifestSHA256,
		ExpectedArtifactSHA256: request.ExpectedArtifactSHA256,
		ExpiresAt:              time.Unix(request.ExpiresAt, 0).UTC(),
	}
}

func fromControlIdentity(identity control.ExecutionIdentity) ExecutionIdentity {
	return ExecutionIdentity{
		InvocationID: identity.InvocationID, ExecutionID: identity.ExecutionID,
		PlanHash: identity.PlanHash, CatalogHash: identity.CatalogHash, AuthorityHash: identity.AuthorityHash,
	}
}

func observationFromState(state State) control.Observation {
	if state.Transaction == nil {
		return control.Observation{Phase: "unknown", InstalledVersion: state.Active.Version}
	}
	observation := control.Observation{
		Phase: string(state.Transaction.Phase), RequestedRelease: state.Transaction.RequestedRelease,
		InstalledVersion: state.Active.Version, ActivationAttempted: state.Transaction.ActivationAttempted,
		SuccessorVerified: state.Transaction.SuccessorVerified, RollbackAttempted: state.Transaction.RollbackAttempted,
		RollbackVerified: state.Transaction.RollbackVerified, FailureCode: state.Transaction.FailureCode,
	}
	if state.Transaction.Canceled {
		observation.Phase = "canceled"
	}
	if state.Transaction.Previous != nil {
		observation.PreviousRelease = state.Transaction.Previous.Release
	}
	return observation
}

func safeCoordinatorError(err error) string {
	if err == nil {
		return ""
	}
	var stageError *StageError
	if errors.As(err, &stageError) {
		return stageError.Code
	}
	switch {
	case errors.Is(err, ErrTransactionConflict):
		return "identity_conflict"
	case errors.Is(err, ErrTransactionBusy):
		return "update_busy"
	case errors.Is(err, ErrUpdateDenied):
		return "update_denied"
	case errors.Is(err, ErrNotStaged):
		return "candidate_not_staged"
	case errors.Is(err, ErrActivationTooLate):
		return "activation_too_late"
	default:
		return "state_unknown"
	}
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
