package interactions

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

func externalWaitFixture(t *testing.T) (*Registry, *testClock, string, Record) {
	t.Helper()
	registry, clock, path := newTestRegistry(t)
	request := validCreate(clock, "interaction_external111", "session-external")
	request.Kind = KindExternalWait
	request.Questions = nil
	request.ConditionRef = "non_authorizing_test_reference"
	request.Origin.ExecutionID = "execution-original"
	request.Origin.ExecutionContext = &bus.InboundContext{Channel: "telegram", SenderID: "user-1"}
	request.ExpiresAt = clock.Now().Add(2 * time.Hour)
	record, err := registry.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.BindPromptDelivery(record.ID, record.Revision, "outbox-external")
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkWaiting(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return registry, clock, path, record
}

func TestExternalWaitRejectsChatAnswersAndClaimsOnceAfterDelay(t *testing.T) {
	registry, clock, path, record := externalWaitFixture(t)
	for _, outcome := range []Outcome{OutcomeAnswered, OutcomeAllowed, OutcomeDenied, OutcomeReady} {
		if _, err := registry.ClaimAnswer(record.ID, record.Revision, Answer{Text: "allow_once"}, outcome); err == nil {
			t.Fatalf("chat claimed external condition with %s", outcome)
		}
	}
	clock.Advance(90 * time.Minute)
	var claimed atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := registry.ClaimExternalWait(record.ID, record.Revision, OutcomeReady); err == nil {
				claimed.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("claim: %v", err)
			}
		})
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("claims=%d", claimed.Load())
	}
	reloaded := NewRegistryWithOptions(path, Options{Now: clock.Now})
	if err := reloaded.LastLoadError(); err != nil {
		t.Fatal(err)
	}
	ready, _ := reloaded.Get(record.ID)
	if ready.Outcome != OutcomeReady || ready.Status != StatusClaimed || ready.Answer.Text != "" {
		t.Fatalf("ready=%+v", ready)
	}
	invalid, err := reloaded.InvalidateExternalWait(ready.ID, ready.Revision)
	if err != nil || invalid.Outcome != OutcomeUnavailable {
		t.Fatalf("invalidate=%+v %v", invalid, err)
	}
}

func TestExternalWaitTerminalAndExpiryFenceLateCompletion(t *testing.T) {
	for _, terminal := range []string{"cancel", "fail", "expire"} {
		t.Run(terminal, func(t *testing.T) {
			registry, clock, _, record := externalWaitFixture(t)
			var err error
			switch terminal {
			case "cancel":
				record, err = registry.Cancel(record.ID, record.Revision, "owner_stop")
			case "fail":
				record, err = registry.Fail(record.ID, record.Revision, "lost", "condition lost")
			case "expire":
				clock.Advance(2 * time.Hour)
			}
			if err != nil {
				t.Fatal(err)
			}
			ready, err := registry.ClaimExternalWait(record.ID, record.Revision, OutcomeReady)
			if terminal == "expire" {
				if err != nil || ready.Outcome != OutcomeTimedOut {
					t.Fatalf("expired=%+v %v", ready, err)
				}
			} else if err == nil {
				t.Fatal("late event revived terminal interaction")
			}
		})
	}
}

func TestExternalWaitValidationRejectsCapabilitiesAndForeignBindings(t *testing.T) {
	registry, clock, _, record := externalWaitFixture(t)
	for _, value := range []string{"", "https://phone/#request=secret", "has space"} {
		request := validCreate(clock, "interaction_external222", "other-session")
		request.Kind = KindExternalWait
		request.Origin = record.Origin
		request.Questions = nil
		request.ConditionRef = value
		if _, err := registry.Create(request); err == nil {
			t.Fatalf("accepted reference %q", value)
		}
	}
	request := validCreate(clock, "interaction_external333", "other-session")
	request.ConditionRef = "opaque"
	if _, err := registry.Create(request); err == nil {
		t.Fatal("ordinary question accepted condition")
	}
}
