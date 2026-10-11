package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

type captureFunc func(context.Context, Target) (Capture, error)

func (fn captureFunc) Capture(ctx context.Context, target Target) (Capture, error) {
	return fn(ctx, target)
}

func readOnlyFixture(t *testing.T, arbiter *Arbiter, driver CaptureDriver) (*ReadOnlyHost, OpenRequest) {
	t.Helper()
	request := OpenRequest{
		Principal: runtimecap.Principal{
			Runtime:     runtimecap.KindGateway,
			ActorID:     "actor",
			AgentID:     "agent",
			SessionID:   "thread",
			ExecutionID: "turn",
		},
		Target: Target{
			Alias: "fixture", Placement: PlacementGateway, OS: OSLinux, Mode: ModeAttached,
			Isolation: IsolationNone, Backend: "fixture",
			Scope: Scope{DesktopID: "desktop", DisplayID: "display", WindowID: "window", WindowGeneration: 1},
		},
	}
	if driver == nil {
		driver = captureFunc(func(_ context.Context, target Target) (Capture, error) {
			return Capture{
				Scope: target.Scope, Geometry: Geometry{Width: 200, Height: 100, Scale: 1},
				AccessibilityText: "private fixture text",
			}, nil
		})
	}
	if arbiter == nil {
		arbiter = NewArbiter()
	}
	host, err := NewReadOnlyHost(ReadOnlyPolicy{
		WorkerID: "worker", Revision: "policy-v1", Targets: []Target{request.Target},
		LeaseTTL: time.Minute, ObservationTTL: 10 * time.Second,
		Admit: func(principal runtimecap.Principal, _ Target) bool { return principal.ActorID == "actor" },
	}, driver, arbiter)
	if err != nil {
		t.Fatal(err)
	}
	return host, request
}

func TestReadOnlyHostLifecycle(t *testing.T) {
	ctx := context.Background()
	host, request := readOnlyFixture(t, nil, nil)
	targets, err := host.Targets(ctx, request.Principal)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets = %v, %v", targets, err)
	}
	session, err := host.Open(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := host.Observe(ctx, session)
	if err != nil || observation.Validate(session, time.Now()) != nil {
		t.Fatalf("observe = %v, %v", observation, err)
	}
	encoded, err := json.Marshal(observation)
	if err != nil || strings.Contains(string(encoded), "private fixture") ||
		strings.Contains(fmt.Sprintf("%#v", observation), "private fixture") {
		t.Fatal("protected observation escaped")
	}
	human, err := host.Handoff(ctx, session.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Observe(ctx, session); !errors.Is(err, ErrStale) {
		t.Fatalf("old generation = %v", err)
	}
	if _, err = host.Observe(ctx, human); !errors.Is(err, ErrDenied) {
		t.Fatalf("human observe = %v", err)
	}
	pending, err := host.RequestResume(ctx, human.Binding)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := host.Resume(ctx, pending.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if err = observation.Validate(resumed, time.Now()); !errors.Is(err, ErrStale) {
		t.Fatalf("pre-handoff evidence = %v", err)
	}
	if _, err = host.Observe(ctx, resumed); err != nil {
		t.Fatal(err)
	}
	released, err := host.Release(ctx, resumed.Binding)
	if err != nil || released.Control != ControlReleased {
		t.Fatalf("release = %v, %v", released, err)
	}
	if again, releaseErr := host.Release(ctx, released.Binding); releaseErr != nil || again != released {
		t.Fatalf("idempotent release = %v, %v", again, releaseErr)
	}
	if _, err = host.Observe(ctx, released); !errors.Is(err, ErrDenied) {
		t.Fatalf("released observe = %v", err)
	}
	if _, err = host.Open(ctx, request); err != nil {
		t.Fatal("release did not free desktop", err)
	}
}

func TestReadOnlyHostRejectsForgedAuthority(t *testing.T) {
	host, request := readOnlyFixture(t, nil, nil)
	ctx := context.Background()
	session, err := host.Open(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Binding){
		"owner":      func(b *Binding) { b.Principal.ExecutionID = "other-turn" },
		"worker":     func(b *Binding) { b.WorkerID = "other-worker" },
		"policy":     func(b *Binding) { b.PolicyRevision = "other-policy" },
		"scope":      func(b *Binding) { b.Target.Scope.WindowGeneration++ },
		"generation": func(b *Binding) { b.Generation++ },
	} {
		t.Run(name, func(t *testing.T) {
			forged := session
			mutate(&forged.Binding)
			if _, err := host.Observe(ctx, forged); err == nil {
				t.Fatal("forged observe admitted")
			}
			if _, err := host.Release(ctx, forged.Binding); err == nil {
				t.Fatal("forged release admitted")
			}
		})
	}
	request.Target.Scope.WindowGeneration++
	if _, err = host.Open(ctx, request); !errors.Is(err, ErrDenied) {
		t.Fatal("model-authored target admitted", err)
	}
	// Caller-controlled expiry/control are ignored in favor of stored state.
	session.ExpiresAt = time.Now().Add(time.Hour)
	session.Control = ControlHuman
	if _, err = host.Observe(ctx, session); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyHostArbitratesProfilesAndDisplays(t *testing.T) {
	arbiter := NewArbiter()
	first, request := readOnlyFixture(t, arbiter, nil)
	second, other := readOnlyFixture(t, arbiter, nil)
	second.policy.WorkerID = "other-worker"
	other.Target.Scope.DisplayID = "another-display"
	second.policy.Targets = []Target{other.Target}
	ctx := context.Background()
	session, err := first.Open(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.Open(ctx, other); !errors.Is(err, ErrBusy) {
		t.Fatalf("same desktop different display = %v", err)
	}
	human, err := first.Handoff(ctx, session.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.Open(ctx, other); !errors.Is(err, ErrBusy) {
		t.Fatal("human ownership was stolen", err)
	}
	if _, err = first.Release(ctx, human.Binding); err != nil {
		t.Fatal(err)
	}
	if _, err = second.Open(ctx, other); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyHostCancelsAndDiscardsInflightCapture(t *testing.T) {
	for _, event := range []ControlEvent{EventHandoff, EventRelease} {
		t.Run(string(event), func(t *testing.T) {
			started, canceled := make(chan struct{}), make(chan struct{})
			driver := captureFunc(func(ctx context.Context, target Target) (Capture, error) {
				close(started)
				<-ctx.Done()
				close(canceled)
				// Simulate a driver returning data despite cancellation.
				return Capture{
					Scope: target.Scope, Geometry: Geometry{Width: 1, Height: 1, Scale: 1},
					AccessibilityText: "must not escape",
				}, nil
			})
			host, request := readOnlyFixture(t, nil, driver)
			session, err := host.Open(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := host.Observe(context.Background(), session); done <- err }()
			<-started
			if _, err = host.transition(context.Background(), session.Binding, event); err != nil {
				t.Fatal(err)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("capture was not canceled")
			}
			if err = <-done; !errors.Is(err, ErrStale) {
				t.Fatal("canceled payload accepted", err)
			}
		})
	}
}

func TestReadOnlyHostExpiryAndPolicyRevocation(t *testing.T) {
	host, request := readOnlyFixture(t, nil, nil)
	ctx := context.Background()
	now := time.Now()
	host.now = func() time.Time { return now }
	session, err := host.Open(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	now = session.ExpiresAt
	status, err := host.Status(ctx, request.Principal, session.Binding.SessionID)
	if err != nil || status.Control != ControlLost {
		t.Fatalf("expired status = %v, %v", status, err)
	}
	if _, err = host.Observe(ctx, session); err == nil {
		t.Fatal("expired authority accepted")
	}
	if _, err = host.Open(ctx, request); err != nil {
		t.Fatal("expired lease still blocks desktop", err)
	}
	host, request = readOnlyFixture(t, nil, nil)
	session, err = host.Open(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	host.policy.Admit = func(runtimecap.Principal, Target) bool { return false }
	if _, err = host.Status(ctx, request.Principal, session.Binding.SessionID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked policy accepted", err)
	}
	if len(host.arbiter.leases) != 0 {
		t.Fatal("revoked policy left ownership claimed")
	}
}

func TestReadOnlyHostCaptureValidationAndPrivacy(t *testing.T) {
	for name, capture := range map[string]Capture{
		"wrong-scope": {Geometry: Geometry{Width: 1, Height: 1, Scale: 1}, AccessibilityText: "secret"},
		"bad-image":   {Geometry: Geometry{Width: 1, Height: 1, Scale: 1}, Screenshot: []byte("secret")},
		"oversize-text": {
			Geometry:          Geometry{Width: 1, Height: 1, Scale: 1},
			AccessibilityText: strings.Repeat("x", MaxAccessibilityBytes+1),
		},
	} {
		t.Run(name, func(t *testing.T) {
			host, request := readOnlyFixture(
				t,
				nil,
				captureFunc(func(_ context.Context, target Target) (Capture, error) {
					if name != "wrong-scope" {
						capture.Scope = target.Scope
					}
					return capture, nil
				}),
			)
			session, err := host.Open(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			observation, err := host.Observe(context.Background(), session)
			if err == nil || observation.AccessibilityText != "" || len(observation.Screenshot) != 0 {
				t.Fatal("invalid capture escaped")
			}
		})
	}
	host, request := readOnlyFixture(t, nil, captureFunc(func(context.Context, Target) (Capture, error) {
		return Capture{}, errors.New("protected application text")
	}))
	session, err := host.Open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Observe(context.Background(), session); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("driver error leaked: %v", err)
	}
}

func TestReadOnlyHostSlowCaptureCannotRefreshFreshness(t *testing.T) {
	host, request := readOnlyFixture(t, nil, nil)
	now := time.Now()
	host.now = func() time.Time { return now }
	host.driver = captureFunc(func(_ context.Context, target Target) (Capture, error) {
		now = now.Add(11 * time.Second)
		return Capture{
			Scope: target.Scope, Geometry: Geometry{Width: 1, Height: 1, Scale: 1},
			AccessibilityText: "old text",
		}, nil
	})
	session, err := host.Open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Observe(context.Background(), session); !errors.Is(err, ErrExpired) {
		t.Fatal("slow capture refreshed freshness", err)
	}
}

func TestReadOnlyHostCanceledRequestsDoNotLaunchCapture(t *testing.T) {
	var captures atomic.Int32
	host, request := readOnlyFixture(t, nil, captureFunc(func(context.Context, Target) (Capture, error) {
		captures.Add(1)
		return Capture{}, nil
	}))
	session, err := host.Open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = host.Open(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = host.Observe(ctx, session); !errors.Is(err, context.Canceled) || captures.Load() != 0 {
		t.Fatal("canceled capture launched", err)
	}
}

func TestReadOnlyHostConcurrentOpen(t *testing.T) {
	host, request := readOnlyFixture(t, nil, nil)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := host.Open(context.Background(), request); err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, ErrBusy) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("admitted %d owners", admitted.Load())
	}
}

func TestReadOnlyHostReleasedSessionChurnDoesNotBlockAdmission(t *testing.T) {
	host, request := readOnlyFixture(t, nil, nil)
	now := time.Now()
	host.now = func() time.Time { return now }
	host.policy.LeaseTTL = time.Hour
	var first Session
	for i := range 256 {
		session, err := host.Open(t.Context(), request)
		if err != nil {
			t.Fatalf("open after %d completed sessions: %v", i, err)
		}
		if i == 0 {
			first = session
		}
		if _, err = host.Release(t.Context(), session.Binding); err != nil {
			t.Fatal(err)
		}
		if len(host.sessions) > 128 || len(host.arbiter.leases) != 0 {
			t.Fatal("retained state is unbounded or completed session still owns desktop")
		}
		now = now.Add(time.Millisecond)
	}
	if _, err := host.Status(t.Context(), request.Principal, first.Binding.SessionID); err == nil {
		t.Fatal("evicted terminal session still has authority")
	}
	if _, err := host.Open(t.Context(), request); err != nil {
		t.Fatal("terminal tombstones blocked subsequent work", err)
	}
}
