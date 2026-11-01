package computer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

func TestObserveUntilUsesFreshCapturesAndReturnsOnlyMatchingEvidence(t *testing.T) {
	captures := 0
	host, request := readOnlyFixture(t, nil, captureFunc(func(_ context.Context, target Target) (Capture, error) {
		captures++
		state := "not ready"
		if captures == 3 {
			state = "ready"
		}
		return Capture{
			Scope: target.Scope, Geometry: Geometry{Width: 20, Height: 10, Scale: 1}, AccessibilityText: state,
		}, nil
	}))
	session, err := host.Open(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool)
	result, err := host.ObserveUntil(
		t.Context(),
		session,
		func(ctx context.Context, observation Observation) (bool, error) {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Minute {
				t.Fatal("wait has no bounded deadline")
			}
			if ctx.Err() != nil || observation.Validate(session, time.Now()) != nil || ids[observation.ID] {
				t.Fatal("condition did not receive fresh same-session evidence")
			}
			ids[observation.ID] = true
			return observation.AccessibilityText == "ready", nil
		},
	)
	if err != nil || captures != 3 || len(ids) != 3 || result.AccessibilityText != "ready" {
		t.Fatal("wait did not stop at the independently inspected state", captures, err)
	}
	stored, err := host.Status(t.Context(), request.Principal, session.Binding.SessionID)
	if err != nil || stored != session {
		t.Fatal("read-only wait changed session ownership", err)
	}
}

func TestObserveUntilNeverRetriesCaptureOrConditionErrors(t *testing.T) {
	for _, stage := range []string{"capture", "condition"} {
		t.Run(stage, func(t *testing.T) {
			captures, conditions := 0, 0
			host, request := readOnlyFixture(
				t,
				nil,
				captureFunc(func(_ context.Context, target Target) (Capture, error) {
					captures++
					if stage == "capture" {
						return Capture{}, errors.New("private app details")
					}
					return Capture{
						Scope:             target.Scope,
						Geometry:          Geometry{Width: 1, Height: 1, Scale: 1},
						AccessibilityText: "fixture",
					}, nil
				}),
			)
			session, err := host.Open(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			result, err := host.ObserveUntil(t.Context(), session, func(context.Context, Observation) (bool, error) {
				conditions++
				return true, errors.New("private condition details")
			})
			if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private") ||
				result.ID != "" || captures != 1 || stage == "capture" && conditions != 0 ||
				stage == "condition" && conditions != 1 {
				t.Fatal("failure was retried, exposed private details or returned evidence", captures, conditions, err)
			}
		})
	}
}

func TestObserveUntilRejectsControlPolicyAndExpiryChangesDuringCondition(t *testing.T) {
	for _, change := range []string{"handoff", "release", "policy", "evidence expiry", "session expiry"} {
		for _, matched := range []bool{false, true} {
			t.Run(change+"/"+map[bool]string{false: "not matched", true: "matched"}[matched], func(t *testing.T) {
				host, request := readOnlyFixture(t, nil, nil)
				session, err := host.Open(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				result, err := host.ObserveUntil(
					t.Context(),
					session,
					func(_ context.Context, observation Observation) (bool, error) {
						calls++
						switch change {
						case "handoff":
							_, err = host.Handoff(t.Context(), session.Binding)
						case "release":
							_, err = host.Release(t.Context(), session.Binding)
						case "policy":
							host.policy.Admit = func(runtimecap.Principal, Target) bool { return false }
						case "evidence expiry":
							host.now = func() time.Time { return observation.ExpiresAt }
						case "session expiry":
							host.now = func() time.Time { return session.ExpiresAt }
						}
						if err != nil {
							t.Fatal(err)
						}
						return matched, nil
					},
				)
				if err == nil || result.ID != "" || calls != 1 {
					t.Fatal("condition revived stale authority or evidence", calls, err)
				}
			})
		}
	}
}

func TestObserveUntilCancellationAndTimeoutPreserveTheSession(t *testing.T) {
	for _, phase := range []string{"before capture", "capture", "condition", "between captures", "timeout"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if phase == "timeout" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer deadlineCancel()
			}
			captures := 0
			host, request := readOnlyFixture(
				t,
				nil,
				captureFunc(func(ctx context.Context, target Target) (Capture, error) {
					captures++
					if phase == "capture" {
						cancel()
						<-ctx.Done()
					}
					return Capture{
						Scope:             target.Scope,
						Geometry:          Geometry{Width: 1, Height: 1, Scale: 1},
						AccessibilityText: "fixture",
					}, nil
				}),
			)
			session, err := host.Open(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "before capture" {
				cancel()
			}
			result, err := host.ObserveUntil(ctx, session, func(ctx context.Context, _ Observation) (bool, error) {
				if phase == "condition" {
					cancel()
				}
				if phase == "between captures" {
					timer := time.AfterFunc(10*time.Millisecond, cancel)
					t.Cleanup(func() { timer.Stop() })
				}
				if phase == "condition" {
					<-ctx.Done()
					return true, errors.New("must not replace cancellation")
				}
				return false, nil
			})
			expected := context.Canceled
			if phase == "timeout" {
				expected = context.DeadlineExceeded
			}
			if !errors.Is(err, expected) || result.ID != "" || captures > 1 {
				t.Fatal("cancellation was lost, retried or returned evidence", captures, err)
			}
			stored, statusErr := host.Status(t.Context(), request.Principal, session.Binding.SessionID)
			if statusErr != nil || stored != session {
				t.Fatal("wait cancellation released or revoked the session", statusErr)
			}
		})
	}
}

func TestObserveUntilRejectsInvalidRequestsBeforeCapture(t *testing.T) {
	captures := 0
	host, request := readOnlyFixture(t, nil, captureFunc(func(context.Context, Target) (Capture, error) {
		captures++
		return Capture{}, errors.New("must not capture")
	}))
	session, err := host.Open(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.ObserveUntil(t.Context(), session, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing condition was accepted", err)
	}
	session.Binding.Generation++
	if _, err = host.ObserveUntil(t.Context(), session, func(context.Context, Observation) (bool, error) {
		t.Fatal("condition received stale evidence")
		return true, nil
	}); !errors.Is(err, ErrStale) || captures != 0 {
		t.Fatal("stale request reached capture", captures, err)
	}
}
