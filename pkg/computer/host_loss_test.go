package computer

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

func TestReadOnlyHostScopeLossRevokesOnlyTypedIdentityFailures(t *testing.T) {
	for _, kind := range []string{"typed", "wrapped", "scope-mismatch", "lookalike", "other"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			host, request := readOnlyFixture(
				t,
				nil,
				captureFunc(func(_ context.Context, target Target) (Capture, error) {
					calls.Add(1)
					switch kind {
					case "typed":
						return Capture{}, ErrStale
					case "wrapped":
						return Capture{}, fmt.Errorf("protected application text: %w", ErrStale)
					case "scope-mismatch":
						target.Scope.WindowGeneration++
						return Capture{Scope: target.Scope, AccessibilityText: "protected application text"}, nil
					case "lookalike":
						return Capture{}, errors.New(ErrStale.Error())
					default:
						return Capture{}, errors.New("protected application text")
					}
				}),
			)
			session, err := host.Open(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			observation, err := host.Observe(t.Context(), session)
			lost := kind != "lookalike" && kind != "other"
			expected := ErrUnavailable
			if lost {
				expected = ErrStale
			}
			if !errors.Is(err, expected) || err.Error() != expected.Error() ||
				observation.AccessibilityText != "" || len(observation.Screenshot) != 0 {
				t.Fatal("capture error or protected payload escaped", err)
			}
			status, err := host.Status(t.Context(), request.Principal, session.Binding.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if lost {
				if status.Control != ControlLost || status.Binding.Generation != session.Binding.Generation+1 {
					t.Fatal("identity loss left the old session live", status)
				}
				_, _ = host.Observe(t.Context(), session)
				if calls.Load() != 1 {
					t.Fatal("old scope was captured again after loss")
				}
				if _, err = host.Open(t.Context(), request); err != nil {
					t.Fatal("lost original lease still blocks fresh trusted admission", err)
				}
			} else if status != session {
				t.Fatal("ordinary capture failure fabricated resource destruction")
			}
		})
	}
}

func TestReadOnlyHostScopeLossSurvivesCancellationAndHandoff(t *testing.T) {
	for _, action := range []string{"cancel", "handoff", "release-and-reopen"} {
		t.Run(action, func(t *testing.T) {
			started, finish := make(chan struct{}), make(chan struct{})
			host, request := readOnlyFixture(t, nil, captureFunc(func(context.Context, Target) (Capture, error) {
				close(started)
				<-finish
				return Capture{}, fmt.Errorf("protected application text: %w", ErrStale)
			}))
			session, err := host.Open(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, observeErr := host.Observe(ctx, session); done <- observeErr }()
			<-started
			var replacement Session
			switch action {
			case "cancel":
				cancel()
			case "handoff":
				_, err = host.Handoff(t.Context(), session.Binding)
			case "release-and-reopen":
				_, err = host.Release(t.Context(), session.Binding)
				if err == nil {
					replacement, err = host.Open(t.Context(), request)
				}
			}
			close(finish)
			if err != nil {
				t.Fatal(err)
			}
			expected := ErrStale
			if action == "cancel" {
				expected = context.Canceled
			}
			if err = <-done; !errors.Is(err, expected) || err.Error() != expected.Error() {
				t.Fatal("in-flight identity loss was not sanitized", err)
			}
			status, err := host.Status(t.Context(), request.Principal, session.Binding.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if action == "release-and-reopen" {
				if status.Control != ControlReleased {
					t.Fatal("late loss overwrote an explicit release")
				}
				status, err = host.Status(t.Context(), request.Principal, replacement.Binding.SessionID)
				if err != nil || status != replacement || len(host.arbiter.leases) != 1 {
					t.Fatal("old identity loss revoked the newer desktop lease", err)
				}
			} else if status.Control != ControlLost || len(host.arbiter.leases) != 0 {
				t.Fatal("cancellation/handoff hid permanent native identity loss")
			}
		})
	}
}
