package computerhost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

func TestInputOwnershipProbeOutlivesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	unblock := make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	result := make(chan error, 1)
	go func() {
		result <- probeInputOwnership(ctx, ctx, func(probe context.Context) error {
			close(started)
			<-unblock
			if err := probe.Err(); err != nil {
				t.Error("action cancellation reached the native ownership check", err)
			}
			if _, bounded := probe.Deadline(); !bounded {
				t.Error("ownership check has no native IO bound")
			}
			return nil
		})
	}()
	<-started
	cancel()
	// The synchronous ownership round trip still needs to finish.
	select {
	case <-result:
		t.Fatal("ownership probe returned while native IO remained live")
	default:
	}
	release()
	if err := <-result; !errors.Is(err, computer.ErrStale) {
		t.Fatal("expired action still authorized input after its probe", err)
	}
}

type unresolvedFixtureLease struct {
	done     chan struct{}
	closed   bool
	closeErr error
}

func (lease *unresolvedFixtureLease) Done() <-chan struct{} { return lease.done }
func (lease *unresolvedFixtureLease) Close() error {
	lease.closed = true
	return lease.closeErr
}

func TestInputHostResumesDisconnectedCleanupAfterLeaseLoss(t *testing.T) {
	host, app := nativeHostFixture(t, nil)
	execute := func(context.Context, InputAuthorization) InputDisposition { return InputUnverified }
	if _, err := withInputBackends(host, map[string]InputBackend{
		"fixture": {Revision: [32]byte{1}, Execute: execute},
	}); err != nil {
		t.Fatal(err)
	}
	session := openFixture(t, host)
	entry := host.sessions[session.Binding.SessionID]
	lease := &unresolvedFixtureLease{done: make(chan struct{})}
	if err := acquire(t.Context(), entry.gate); err != nil {
		t.Fatal(err)
	}
	entry.input.lease, entry.input.faulted = lease, true
	relinquish(entry.gate)
	if err := host.Disconnect(t.Context()); !errors.Is(err, computer.ErrUnavailable) {
		t.Fatal("disconnect claimed cleanup with a live uncertain lease", err)
	}
	if app.workers[0].closed.Load() {
		t.Fatal("worker closed while input remained unverified")
	}
	close(lease.done)
	select {
	case <-app.workers[0].done:
	case <-time.After(3 * time.Second):
		t.Fatal("lease ended without resuming disconnected cleanup")
	}
	if err := acquire(t.Context(), entry.gate); err != nil {
		t.Fatal(err)
	}
	defer relinquish(entry.gate)
	if !entry.retired.Load() || !lease.closed || entry.input.lease != nil {
		t.Fatal("cleanup did not finish under the session gate")
	}
}

func TestInputHostRetiresRevokedLeaseAfterCloseFailure(t *testing.T) {
	host, app := nativeHostFixture(t, nil)
	execute := func(context.Context, InputAuthorization) InputDisposition { return InputUnverified }
	if _, err := withInputBackends(host, map[string]InputBackend{
		"fixture": {Revision: [32]byte{1}, Execute: execute},
	}); err != nil {
		t.Fatal(err)
	}
	session := openFixture(t, host)
	entry := host.sessions[session.Binding.SessionID]
	lease := &unresolvedFixtureLease{done: make(chan struct{}), closeErr: computer.ErrUnavailable}

	// A failed native close has revoked authority and cached its error.
	if err := acquire(t.Context(), entry.gate); err != nil {
		t.Fatal(err)
	}
	entry.input.lease, entry.input.faulted = lease, true
	_ = lease.Close()
	close(lease.done)
	relinquish(entry.gate)

	if err := host.Disconnect(t.Context()); err != nil {
		t.Fatal("cached native close failure blocked session cleanup", err)
	}
	if !entry.retired.Load() || !app.workers[0].closed.Load() || entry.driver.guard.Alive(t.Context()) {
		t.Fatal("revoked session did not retire its original capture resources")
	}
}

func TestInputHostUsesOnlyCurrentWorkspaceObservation(t *testing.T) {
	base, _ := nativeHostFixture(t, nil)
	if _, ok := any(base).(companion.ComputerInputCommandHost); ok {
		t.Fatal("ordinary host inferred an input capability")
	}
	execute := func(context.Context, InputAuthorization) InputDisposition {
		t.Error("input executor entered without a native lease")
		return InputUnverified
	}
	backend := InputBackend{Revision: [32]byte{1}, Execute: execute}
	if _, err := withInputBackends(base, map[string]InputBackend{"missing": backend}); err == nil {
		t.Fatal("unconfigured profile admitted")
	}
	if _, err := withInputBackends(base, map[string]InputBackend{"fixture": {Execute: execute}}); err == nil {
		t.Fatal("unpinned backend admitted")
	}
	captureOnly := base.ComputerProfiles()[0].Revision
	host, err := withInputBackends(base, map[string]InputBackend{"fixture": backend})
	if err != nil {
		t.Fatal(err)
	}
	if host.ComputerInputProfiles()[0].Revision == captureOnly {
		t.Fatal("profile revision did not bind the executor")
	}
	session := openFixture(t, base)
	first := observeFixture(t, base, session, "first-capture")
	next := observeFixture(t, base, session, "next-capture")
	request := computer.InputRequest{
		ID: "input", Binding: session.Binding, ObservationID: next.Observation.ID,
		Action: computer.InputAction{Kind: computer.InputScroll, Scroll: &computer.InputScrollSpec{
			Point: &computer.InputPoint{X: 20, Y: 20}, DeltaY: 40,
		}},
	}
	for _, item := range []struct {
		id, workspace string
		want          error
	}{
		{first.Observation.ID, "workspace", computer.ErrStale},
		{next.Observation.ID, "other-workspace", computer.ErrStale},
		{next.Observation.ID, "workspace", computer.ErrDenied}, // no native guard
	} {
		request.ObservationID = item.id
		if _, err := host.Act(t.Context(), nodes.ComputerHostActRequest{
			Request: request, WorkspaceID: item.workspace,
		}); !errors.Is(err, item.want) {
			t.Fatalf("observation checked against wrong authority: got %v, want %v", err, item.want)
		}
	}
	_ = controlFixture(t, base, session, computer.EventHandoff)
	if _, err := host.Act(t.Context(), nodes.ComputerHostActRequest{
		Request: request, WorkspaceID: "workspace",
	}); !errors.Is(err, computer.ErrStale) {
		t.Fatal("pre-handoff evidence remained actionable", err)
	}
}
