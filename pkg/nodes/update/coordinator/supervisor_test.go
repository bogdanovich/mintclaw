//go:build linux || darwin

package coordinator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/nodes/update/control"
)

func TestSupervisorRecoversDurableActivationAndCommitsAuthenticatedHealth(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	updateCoordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = updateCoordinator.Close() }()
	request := fixture.stageRequest(now)
	if _, err := updateCoordinator.Stage(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := updateCoordinator.Activate(request.Identity); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(updateCoordinator, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	launched := make(chan State, 1)
	supervisor.launch = func(_ context.Context, state State) (supervisedChild, error) {
		child := newFakeSupervisedChild()
		launched <- state
		child.incoming <- control.Incoming{Health: controlHealth(state, request.Identity.CatalogHash)}
		return child, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	started := <-launched
	if started.Transaction.LaunchAttempts != 1 || started.Transaction.Phase != PhaseActivating {
		t.Fatalf("launched activation state = %#v", started)
	}
	healthy := waitForTransactionPhase(t, updateCoordinator.store, PhaseHealthy)
	if !healthy.Transaction.SuccessorVerified || healthy.Transaction.LaunchAttempts != 1 {
		t.Fatalf("healthy state = %#v", healthy)
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Supervisor.Run() error = %v", err)
	}
}

func TestSupervisorDoesNotActivateDurablyStagedRequestAfterRestart(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	updateCoordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = updateCoordinator.Close() }()
	request := fixture.stageRequest(now)
	staged, err := updateCoordinator.Stage(t.Context(), request)
	if err != nil || staged.Transaction.Phase != PhaseStaged || staged.Transaction.ActivationAttempted {
		t.Fatalf("staged state = %#v, %v", staged, err)
	}
	supervisor, err := NewSupervisor(updateCoordinator, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	launched := make(chan State, 1)
	supervisor.launch = func(_ context.Context, state State) (supervisedChild, error) {
		child := newFakeSupervisedChild()
		launched <- state
		return child, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	started := <-launched
	if started.Transaction.Phase != PhaseStaged || started.Transaction.ActivationAttempted ||
		started.Transaction.LaunchAttempts != 0 || started.Active != staged.Active {
		t.Fatalf("restarted staged state = %#v", started)
	}
	observed, err := updateCoordinator.store.Load()
	if err != nil || observed.Generation != staged.Generation || observed.Transaction.Phase != PhaseStaged {
		t.Fatalf("durable staged state changed = %#v, %v", observed, err)
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Supervisor.Run() error = %v", err)
	}
}

func TestSupervisorStopsOldChildWhenActivationPublicationCannotBeRead(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	updateCoordinator, root := testCoordinator(t, fixture, now)
	defer func() { _ = updateCoordinator.Close() }()
	request := fixture.stageRequest(now)
	if _, err := updateCoordinator.Stage(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, stateFileName)
	defer func() { _ = os.Chmod(statePath, 0o600) }()
	updateCoordinator.store.fault = func(point string) error {
		if point != "state_after_publish" {
			return nil
		}
		if err := os.Chmod(statePath, 0); err != nil {
			t.Fatal(err)
		}
		return unix.EIO
	}
	supervisor, err := NewSupervisor(updateCoordinator, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	launched := make(chan *fakeSupervisedChild, 1)
	supervisor.launch = func(_ context.Context, _ State) (supervisedChild, error) {
		child := newFakeSupervisedChild()
		launched <- child
		child.incoming <- control.Incoming{Request: controlUpdateRequest(request)}
		return child, nil
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(t.Context()) }()
	child := <-launched
	if err = <-done; err == nil {
		t.Fatal("Supervisor.Run() accepted an unreadable activation outcome")
	}
	select {
	case <-child.done:
	default:
		t.Fatal("supervisor kept the old child running after an unknown activation outcome")
	}
	if err = os.Chmod(statePath, 0o600); err != nil {
		t.Fatal(err)
	}
	updateCoordinator.store.fault = nil
	observed, err := updateCoordinator.store.Load()
	if err != nil || observed.Transaction.Phase != PhaseActivating || !observed.Transaction.ActivationAttempted {
		t.Fatalf("published activation state = %#v, %v", observed, err)
	}
}

func TestSupervisorTerminalizesExpiredPreactivationAfterRestart(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	updateCoordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = updateCoordinator.Close() }()
	request := fixture.stageRequest(now)
	staged, err := updateCoordinator.Stage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := staged
	interrupted.Generation++
	interrupted.Transaction.Phase = PhaseDownloading
	interrupted.Transaction.Candidate = nil
	if err = updateCoordinator.store.Commit(staged.Generation, interrupted); err != nil {
		t.Fatal(err)
	}
	updateCoordinator.now = func() time.Time { return request.ExpiresAt }
	supervisor, err := NewSupervisor(updateCoordinator, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	launched := make(chan State, 1)
	supervisor.launch = func(_ context.Context, state State) (supervisedChild, error) {
		child := newFakeSupervisedChild()
		launched <- state
		return child, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	started := <-launched
	if started.Transaction.Phase != PhaseOperatorActionRequired ||
		started.Transaction.FailureCode != "request_expired" || started.Transaction.ActivationAttempted ||
		started.Active != staged.Active {
		t.Fatalf("expired restart state = %#v", started)
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Supervisor.Run() error = %v", err)
	}
}

func TestSupervisorBoundsCandidateAttemptsThenProvesRollback(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	updateCoordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = updateCoordinator.Close() }()
	request := fixture.stageRequest(now)
	if _, err := updateCoordinator.Stage(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := updateCoordinator.Activate(request.Identity); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(updateCoordinator, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	candidateLaunches := 0
	rollbackLaunches := 0
	supervisor.launch = func(_ context.Context, state State) (supervisedChild, error) {
		child := newFakeSupervisedChild()
		mu.Lock()
		defer mu.Unlock()
		if state.Transaction.Phase == PhaseActivating {
			candidateLaunches++
			child.done <- errors.New("candidate exited")
			return child, nil
		}
		rollbackLaunches++
		child.incoming <- control.Incoming{Health: controlHealth(state, request.Identity.CatalogHash)}
		return child, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	rolledBack := waitForTransactionPhase(t, updateCoordinator.store, PhaseRolledBack)
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Supervisor.Run() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if candidateLaunches != MaxLaunchAttempts || rollbackLaunches != 1 ||
		!rolledBack.Transaction.RollbackVerified || rolledBack.Transaction.LaunchAttempts != 1 {
		t.Fatalf(
			"launches candidate=%d rollback=%d, state=%#v",
			candidateLaunches,
			rollbackLaunches,
			rolledBack,
		)
	}
}

func TestSupervisorKeepsVerifiedActivePayloadAfterPreActivationFailure(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	fixture.server.Close()
	updateCoordinator, _ := testCoordinator(t, fixture, now)
	defer func() { _ = updateCoordinator.Close() }()
	if _, err := updateCoordinator.Stage(t.Context(), fixture.stageRequest(now)); err == nil {
		t.Fatal("Stage() unexpectedly succeeded with an unavailable release source")
	}
	failed := waitForTransactionPhase(t, updateCoordinator.store, PhaseOperatorActionRequired)
	if failed.Transaction.ActivationAttempted {
		t.Fatalf("pre-activation failure recorded activation: %#v", failed.Transaction)
	}
	supervisor, err := NewSupervisor(updateCoordinator, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	launched := make(chan State, 1)
	supervisor.launch = func(_ context.Context, state State) (supervisedChild, error) {
		launched <- state
		return newFakeSupervisedChild(), nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	select {
	case state := <-launched:
		if state.Active != failed.Active {
			t.Fatalf("launched payload = %#v, want %#v", state.Active, failed.Active)
		}
	case <-time.After(time.Second):
		t.Fatal("supervisor did not keep the verified active payload running")
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Supervisor.Run() error = %v", err)
	}
}

type fakeSupervisedChild struct {
	incoming chan control.Incoming
	done     chan error
	stopOnce sync.Once
}

func newFakeSupervisedChild() *fakeSupervisedChild {
	return &fakeSupervisedChild{incoming: make(chan control.Incoming, 4), done: make(chan error, 1)}
}

func (child *fakeSupervisedChild) incomingFrames() <-chan control.Incoming { return child.incoming }
func (child *fakeSupervisedChild) completion() <-chan error                { return child.done }
func (child *fakeSupervisedChild) respond(control.Response) error          { return nil }
func (child *fakeSupervisedChild) stop() {
	child.stopOnce.Do(func() {
		select {
		case child.done <- nil:
		default:
		}
	})
}

func controlHealth(state State, catalogHash string) *control.Health {
	return &control.Health{
		SchemaVersion: control.SchemaVersion, Kind: control.KindHealth,
		NodeID: string(state.Installation.NodeID), Version: state.Active.Version,
		Platform: state.Installation.Platform, Architecture: state.Installation.Architecture,
		CatalogHash: catalogHash,
	}
}

func controlUpdateRequest(request StageRequest) *control.Request {
	return &control.Request{
		SchemaVersion: control.SchemaVersion, Kind: control.KindUpdate, RequestID: "request_update",
		Update: &control.UpdateRequest{
			Identity: control.ExecutionIdentity{
				InvocationID: request.Identity.InvocationID, ExecutionID: request.Identity.ExecutionID,
				PlanHash: request.Identity.PlanHash, CatalogHash: request.Identity.CatalogHash,
				AuthorityHash: request.Identity.AuthorityHash,
			},
			Profile: request.Profile, ReleaseAlias: request.ReleaseAlias,
			ExpectedManifestSHA256: request.ExpectedManifestSHA256,
			ExpectedArtifactSHA256: request.ExpectedArtifactSHA256, ExpiresAt: request.ExpiresAt.Unix(),
		},
	}
}

func waitForTransactionPhase(t *testing.T, store *Store, phase Phase) State {
	t.Helper()
	// The supervisor retries on a 1s ticker and bounds candidate launch
	// attempts before falling back, so keep the deadline load-tolerant.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if state.Transaction != nil && state.Transaction.Phase == phase {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("transaction did not reach %s", phase)
	return State{}
}
