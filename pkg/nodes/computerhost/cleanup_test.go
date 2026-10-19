package computerhost

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

type cleanupFailureWorker struct {
	captureWorker
	failing *atomic.Bool
}

func (worker *cleanupFailureWorker) Close() error {
	if worker.failing.Load() {
		return errors.New("PRIVATE_PERSISTENT_WORKER_CLOSE")
	}
	return worker.captureWorker.Close()
}

type cleanupFailureWindow struct {
	windowObserver
	failing *atomic.Bool
}

func (window *cleanupFailureWindow) Close() error {
	if window.failing.Load() {
		return errors.New("PRIVATE_PERSISTENT_OBSERVER_CLOSE")
	}
	return window.windowObserver.Close()
}

func TestNativeHostTerminalStatusRetainsCleanupFailure(t *testing.T) {
	for _, resource := range []string{"worker", "observer"} {
		for _, terminal := range []string{"release", "loss", "expiry"} {
			t.Run(resource+"/"+terminal, func(t *testing.T) {
				host, app := nativeHostFixture(t, func(profile *companion.ComputerProfilePolicy) {
					if terminal == "expiry" {
						profile.LeaseSeconds, profile.ObservationSeconds = 1, 1
					}
				})
				session := openFixture(t, host)
				observeFixture(t, host, session, "before-cleanup")
				entry := host.sessions[session.Binding.SessionID]
				failing := &atomic.Bool{}
				failing.Store(true)
				t.Cleanup(func() { failing.Store(false) })
				if err := acquire(t.Context(), entry.gate); err != nil {
					t.Fatal(err)
				}
				if resource == "worker" {
					entry.driver.worker = &cleanupFailureWorker{captureWorker: entry.driver.worker, failing: failing}
				} else {
					entry.driver.guard = &cleanupFailureWindow{windowObserver: entry.driver.guard, failing: failing}
				}
				relinquish(entry.gate)
				want := computer.ControlLost
				switch terminal {
				case "release":
					want = computer.ControlReleased
					_, err := host.Control(t.Context(), nodes.ComputerHostControlRequest{
						ComputerHostStatusRequest: nodes.ComputerHostStatusRequest{
							Principal: fixturePrincipal(), SessionID: session.Binding.SessionID,
						},
						Generation: session.Binding.Generation, Event: computer.EventRelease,
					})
					if !errors.Is(err, companion.ErrInvocationOutcomeUnknown) {
						t.Fatal("release did not retain its uncertain cleanup", err)
					}
				case "loss":
					if _, err := entry.core.Lose(t.Context(), session.Binding); err != nil {
						t.Fatal(err)
					}
				case "expiry":
					timer := time.NewTimer(time.Until(session.ExpiresAt) + 10*time.Millisecond)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-t.Context().Done():
						t.Fatal(t.Context().Err())
					}
				}
				request := nodes.ComputerHostStatusRequest{
					Principal: fixturePrincipal(), SessionID: session.Binding.SessionID,
				}
				for range 3 {
					status, err := host.Status(t.Context(), request)
					if !errors.Is(err, computer.ErrUnavailable) || strings.Contains(err.Error(), "PRIVATE") {
						t.Errorf("unconfirmed cleanup returned status=%s err=%v", status.Session.Control, err)
					}
					if entry.retired.Load() {
						t.Fatal("unconfirmed cleanup retired its obligation")
					}
				}
				if _, err := host.Observe(t.Context(), nodes.ComputerHostObserveRequest{
					ComputerHostStatusRequest: request, Generation: session.Binding.Generation,
					InvocationID: "after-cleanup", WorkspaceID: "workspace",
				}); err == nil {
					t.Fatal("terminal authority admitted a new capture")
				}
				host.outputs.mu.Lock()
				remaining := len(host.outputs.items)
				host.outputs.mu.Unlock()
				if remaining != 0 {
					t.Fatal("terminal authority retained transferable pixels")
				}
				failing.Store(false)
				status, err := host.Status(t.Context(), request)
				if err != nil || status.Session.Control != want || !entry.retired.Load() ||
					!app.workers[0].closed.Load() || app.starts.Load() != 1 || app.captures.Load() != 1 ||
					!app.alive.Load() {
					t.Fatal("cleanup reconciliation changed control, resource identity or application lifetime", err)
				}
				if _, err = host.Status(context.Background(), request); err != nil {
					t.Fatal("confirmed terminal status was not idempotent", err)
				}
			})
		}
	}
}
