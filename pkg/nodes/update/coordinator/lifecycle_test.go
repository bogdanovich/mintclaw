//go:build linux || darwin

package coordinator

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCoordinatorActivationCommitsIntentBeforeBoundedLaunchAndHealth(t *testing.T) {
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	coordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = coordinator.Close() }()
	request := fixture.stageRequest(now)
	staged, err := coordinator.Stage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := coordinator.Activate(request.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if activated.Transaction.Phase != PhaseActivating || !activated.Transaction.ActivationAttempted ||
		activated.Transaction.Previous == nil || activated.Active != *activated.Transaction.Candidate {
		t.Fatalf("activation state = %#v", activated)
	}
	duplicate, err := coordinator.Activate(request.Identity)
	if err != nil || duplicate.Generation != activated.Generation {
		t.Fatalf("duplicate Activate() = generation %d, %v", duplicate.Generation, err)
	}
	for attempt := 1; attempt <= MaxLaunchAttempts; attempt++ {
		launched, launchErr := coordinator.RecordLaunch(request.Identity)
		if launchErr != nil || launched.Transaction.LaunchAttempts != attempt {
			t.Fatalf("RecordLaunch() attempt %d = %#v, %v", attempt, launched.Transaction, launchErr)
		}
	}
	if _, err = coordinator.RecordLaunch(request.Identity); !errors.Is(err, ErrLaunchLimit) {
		t.Fatalf("fourth RecordLaunch() error = %v", err)
	}
	observation := healthFor(staged, request, now)
	observation.Version = "v9.9.9"
	if _, err = coordinator.CommitHealthy(request.Identity, observation); err == nil {
		t.Fatal("mismatched successor health was accepted")
	}
	observation.Version = activated.Active.Version
	observation.CatalogHash = digestOf([]byte("authenticated-successor-catalog"))
	healthy, err := coordinator.CommitHealthy(request.Identity, observation)
	if err != nil {
		t.Fatal(err)
	}
	if healthy.Transaction.Phase != PhaseHealthy || !healthy.Transaction.SuccessorVerified {
		t.Fatalf("healthy state = %#v", healthy)
	}
}

func TestCoordinatorReconcilesActivationCommitPublicationBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		fault     string
		wantPhase Phase
		wantError error
	}{
		{name: "before publication", fault: "state_before_publish", wantPhase: PhaseStaged, wantError: unix.ENOSPC},
		{name: "after publication", fault: "state_after_publish", wantPhase: PhaseActivating},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
			fixture := newReleaseFixture(t, now, "mintclaw-node")
			defer fixture.server.Close()
			coordinator, _ := testCoordinator(t, fixture, now)
			defer func() { _ = coordinator.Close() }()
			request := fixture.stageRequest(now)
			if _, err := coordinator.Stage(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			coordinator.store.fault = func(point string) error {
				if point == test.fault {
					return unix.ENOSPC
				}
				return nil
			}
			observed, err := coordinator.Activate(request.Identity)
			if !errors.Is(err, test.wantError) || observed.Transaction.Phase != test.wantPhase ||
				(observed.Transaction.ActivationAttempted != (test.wantPhase == PhaseActivating)) {
				t.Fatalf("Activate() at %s = %#v, %v", test.fault, observed.Transaction, err)
			}
		})
	}
}

func TestCoordinatorActivationFailsClosedWhenRequestExpiresAfterStaging(t *testing.T) {
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	current := now
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	coordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = coordinator.Close() }()
	coordinator.now = func() time.Time { return current }
	request := fixture.stageRequest(now)
	if _, err := coordinator.Stage(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	current = request.ExpiresAt
	denied, err := coordinator.Activate(request.Identity)
	if !errors.Is(err, ErrUpdateDenied) || denied.Transaction.Phase != PhaseOperatorActionRequired ||
		denied.Transaction.FailureCode != "request_expired" || denied.Transaction.ActivationAttempted {
		t.Fatalf("expired Activate() = %#v, %v", denied.Transaction, err)
	}
}

func TestCoordinatorActivationFailsClosedWhenAuthorityChangesAfterStaging(t *testing.T) {
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	coordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = coordinator.Close() }()
	request := fixture.stageRequest(now)
	if _, err := coordinator.Stage(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	changed := fixture.authority
	changed.ProfileRevision = "stable-v2"
	changed.AuthorityHash = digestOf([]byte("changed-authority"))
	coordinator.resolver = staticResolver{authority: changed}
	denied, err := coordinator.Activate(request.Identity)
	if !errors.Is(err, ErrUpdateDenied) || denied.Transaction.Phase != PhaseOperatorActionRequired ||
		denied.Transaction.FailureCode != "authority_changed" || denied.Transaction.ActivationAttempted {
		t.Fatalf("changed-authority Activate() = %#v, %v", denied.Transaction, err)
	}
}

func TestCoordinatorCancellationIsDurableBeforeActivationAndTooLateAfter(t *testing.T) {
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	coordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = coordinator.Close() }()
	request := fixture.stageRequest(now)
	_, err := coordinator.Stage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	changedIdentity := request.Identity
	changedIdentity.PlanHash = digestOf([]byte("changed-plan"))
	if _, err = coordinator.Activate(changedIdentity); !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("Activate() with changed identity error = %v", err)
	}
	if _, err = coordinator.Cancel(changedIdentity); !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("Cancel() with changed identity error = %v", err)
	}
	canceled, err := coordinator.Cancel(request.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if !canceled.Transaction.Canceled || canceled.Transaction.FailureCode != "canceled" ||
		canceled.Transaction.Phase != PhaseStaged || canceled.Transaction.ActivationAttempted {
		t.Fatalf("canceled state = %#v", canceled)
	}
	duplicate, err := coordinator.Cancel(request.Identity)
	if err != nil || duplicate.Generation != canceled.Generation {
		t.Fatalf("duplicate Cancel() = generation %d, %v", duplicate.Generation, err)
	}
	if _, err = coordinator.Activate(request.Identity); !errors.Is(err, ErrActivationTooLate) {
		t.Fatalf("Activate() after cancel error = %v", err)
	}

	second := fixture.stageRequest(now)
	second.Identity.InvocationID = "invocation_second"
	second.Identity.ExecutionID = "execution_second"
	second.Identity.PlanHash = digestOf([]byte("second-plan"))
	staged, err := coordinator.Stage(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.Activate(second.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.Cancel(second.Identity); !errors.Is(err, ErrActivationTooLate) {
		t.Fatalf("Cancel() after activation error = %v", err)
	}
	if staged.Transaction.Canceled {
		t.Fatal("new execution inherited prior cancellation")
	}
}

func TestCoordinatorCancellationInterruptsInFlightDownloadAndCommitsTruth(t *testing.T) {
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	coordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = coordinator.Close() }()
	started := make(chan struct{})
	coordinator.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	request := fixture.stageRequest(now)
	stageDone := make(chan State, 1)
	stageErrors := make(chan error, 1)
	go func() {
		state, err := coordinator.Stage(t.Context(), request)
		stageDone <- state
		stageErrors <- err
	}()
	<-started
	canceled, err := coordinator.Cancel(request.Identity)
	if err != nil {
		t.Fatal(err)
	}
	staged := <-stageDone
	if err = <-stageErrors; err != nil {
		t.Fatalf("canceled Stage() error = %v", err)
	}
	if !canceled.Transaction.Canceled || !staged.Transaction.Canceled || canceled.Generation != staged.Generation {
		t.Fatalf("cancellation states = %#v, %#v", canceled, staged)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestCoordinatorRollbackSelectsAndProvesOnlyPreviousPayload(t *testing.T) {
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	coordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = coordinator.Close() }()
	request := fixture.stageRequest(now)
	staged, err := coordinator.Stage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.Activate(request.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.RecordLaunch(request.Identity); err != nil {
		t.Fatal(err)
	}
	rollingBack, err := coordinator.BeginRollback(request.Identity, "successor_timeout")
	if err != nil {
		t.Fatal(err)
	}
	if rollingBack.Transaction.Phase != PhaseRollingBack || !rollingBack.Transaction.RollbackAttempted ||
		rollingBack.Transaction.Previous == nil || rollingBack.Active != *rollingBack.Transaction.Previous ||
		rollingBack.Transaction.LaunchAttempts != 0 {
		t.Fatalf("rollback state = %#v", rollingBack)
	}
	if _, err = coordinator.RecordLaunch(request.Identity); err != nil {
		t.Fatal(err)
	}
	observation := healthFor(staged, request, now)
	observation.Version = rollingBack.Active.Version
	rolledBack, err := coordinator.CommitRolledBack(request.Identity, observation)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Transaction.Phase != PhaseRolledBack || !rolledBack.Transaction.RollbackVerified ||
		rolledBack.Active != staged.Active {
		t.Fatalf("rolled-back state = %#v", rolledBack)
	}
}

func healthFor(state State, request StageRequest, now time.Time) HealthObservation {
	return HealthObservation{
		NodeID: string(state.Installation.NodeID), Version: state.Active.Version,
		Platform: state.Installation.Platform, Architecture: state.Installation.Architecture,
		CatalogHash: request.Identity.CatalogHash, ObservedAt: now,
	}
}
