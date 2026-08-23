package channels

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestDeliverSequentiallyCollectsMessageIDs(t *testing.T) {
	t.Parallel()

	var delivered []string
	result := DeliverSequentially(
		t.Context(),
		[]string{"first", "second"},
		func(_ context.Context, payload string) ([]string, error) {
			delivered = append(delivered, payload)
			return []string{"id-" + payload}, nil
		},
	)

	if !result.Delivered() {
		t.Fatalf("DeliverSequentially() result = %#v, want complete delivery", result)
	}
	if !slices.Equal(delivered, []string{"first", "second"}) {
		t.Fatalf("delivered payloads = %v, want [first second]", delivered)
	}
	if !slices.Equal(result.MessageIDs, []string{"id-first", "id-second"}) {
		t.Fatalf("message IDs = %v, want [id-first id-second]", result.MessageIDs)
	}
}

func TestDeliverSequentiallyReturnsKnownRemainder(t *testing.T) {
	t.Parallel()

	result := DeliverSequentially(
		t.Context(),
		[]string{"first", "second", "third"},
		func(_ context.Context, payload string) ([]string, error) {
			if payload == "second" {
				return nil, ErrSendFailed
			}
			return []string{"id-" + payload}, nil
		},
	)

	if result.Status != DeliveryPartial || !slices.Equal(result.MessageIDs, []string{"id-first"}) {
		t.Fatalf("result = %#v, want first payload confirmed", result)
	}
	if !slices.Equal(result.Remaining, []string{"second", "third"}) {
		t.Fatalf("remaining payloads = %v, want [second third]", result.Remaining)
	}
}

func TestDeliverSequentiallyDoesNotReplayPartiallyDeliveredPayload(t *testing.T) {
	t.Parallel()

	result := DeliverSequentially(t.Context(), []string{"payload"}, func(context.Context, string) ([]string, error) {
		return []string{"id-partial"}, ErrTemporary
	})

	if result.Status != DeliveryPartial || !result.Ambiguous() {
		t.Fatalf("result = %#v, want ambiguous partial delivery", result)
	}
	if result.Remaining != nil {
		t.Fatalf("remaining payloads = %v, want unknown remainder", result.Remaining)
	}
}

func TestDeliverSequentiallyRejectsEmptyPayload(t *testing.T) {
	t.Parallel()

	result := DeliverSequentially(t.Context(), []string(nil), func(context.Context, string) ([]string, error) {
		t.Fatal("delivery callback called for empty payload")
		return nil, nil
	})

	if result.Err == nil || !result.DefinitelyNotSent() {
		t.Fatalf("result = %#v, want definite empty-payload rejection", result)
	}
}

func TestDeliverWithRetryResumesKnownRemainder(t *testing.T) {
	t.Parallel()

	var attempts [][]string
	result := DeliverWithRetry(
		t.Context(),
		[]string{"first", "second"},
		DeliveryRetryPolicy{MaxRetries: 1, RetryAmbiguous: true},
		func(_ context.Context, pending []string) DeliveryResult[string] {
			attempts = append(attempts, append([]string(nil), pending...))
			if len(attempts) == 1 {
				return FailedDelivery(
					[]string{"id-1"},
					[]string{"second"},
					0,
					errors.New("second chunk failed"),
				)
			}
			return SuccessfulDelivery[string]([]string{"id-2"})
		},
		nil,
	)

	if !result.Delivered() {
		t.Fatalf("DeliverWithRetry() result = %#v, want complete delivery", result)
	}
	if !slices.Equal(result.MessageIDs, []string{"id-1", "id-2"}) {
		t.Fatalf("message IDs = %v, want [id-1 id-2]", result.MessageIDs)
	}
	if len(attempts) != 2 || !slices.Equal(attempts[1], []string{"second"}) {
		t.Fatalf("attempt payloads = %v, want second attempt to contain only remainder", attempts)
	}
}

func TestDeliverWithRetryStopsOnPartialResultWithoutRemainder(t *testing.T) {
	t.Parallel()

	calls := 0
	result := DeliverWithRetry(
		t.Context(),
		[]string{"payload"},
		DeliveryRetryPolicy{MaxRetries: 3, RetryAmbiguous: true},
		func(_ context.Context, _ []string) DeliveryResult[string] {
			calls++
			return FailedDelivery[string](
				[]string{"id-1"},
				nil,
				0,
				errors.New("unknown remainder"),
			)
		},
		nil,
	)

	if calls != 1 {
		t.Fatalf("delivery calls = %d, want 1", calls)
	}
	if result.Status != DeliveryPartial || !result.Ambiguous() {
		t.Fatalf("result = %#v, want ambiguous partial delivery", result)
	}
}

func TestDeliverWithRetryHonorsAmbiguousPolicy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		retryAmbiguous bool
		wantCalls      int
	}{
		{name: "disabled", retryAmbiguous: false, wantCalls: 1},
		{name: "enabled", retryAmbiguous: true, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			result := DeliverWithRetry(
				t.Context(),
				[]string{"payload"},
				DeliveryRetryPolicy{MaxRetries: 1, RetryAmbiguous: tc.retryAmbiguous},
				func(_ context.Context, _ []string) DeliveryResult[string] {
					calls++
					if calls == 1 {
						return FailedDelivery[string](nil, nil, 0, errors.New("transport timeout"))
					}
					return SuccessfulDelivery[string]([]string{"id-1"})
				},
				nil,
			)

			if calls != tc.wantCalls {
				t.Fatalf("delivery calls = %d, want %d", calls, tc.wantCalls)
			}
			if tc.retryAmbiguous != result.Delivered() {
				t.Fatalf("delivered = %v, want %v", result.Delivered(), tc.retryAmbiguous)
			}
		})
	}
}

func TestDeliverWithRetryPreservesAmbiguityBeforeDefiniteRejection(t *testing.T) {
	t.Parallel()

	calls := 0
	result := DeliverWithRetry(
		t.Context(),
		[]string{"payload"},
		DeliveryRetryPolicy{MaxRetries: 1, RetryAmbiguous: true},
		func(_ context.Context, _ []string) DeliveryResult[string] {
			calls++
			if calls == 1 {
				return FailedDelivery[string](nil, nil, 0, ErrTemporary)
			}
			return FailedDelivery[string](nil, nil, 0, ErrSendFailed)
		},
		nil,
	)

	if calls != 2 {
		t.Fatalf("delivery calls = %d, want 2", calls)
	}
	if !result.Ambiguous() || result.DefinitelyNotSent() {
		t.Fatalf("result = %#v, want sticky ambiguous failure", result)
	}
}

func TestDeliverWithRetryHonorsRetryAfterAndCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	attempted := make(chan struct{})
	resultCh := make(chan DeliveryResult[string], 1)
	go func() {
		resultCh <- DeliverWithRetry(
			ctx,
			[]string{"payload"},
			DeliveryRetryPolicy{MaxRetries: 1, RetryAmbiguous: true},
			func(_ context.Context, _ []string) DeliveryResult[string] {
				close(attempted)
				return FailedDelivery[string](nil, nil, time.Hour, ErrRateLimit)
			},
			nil,
		)
	}()

	<-attempted
	cancel()
	result := <-resultCh
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", result.Err)
	}
	if result.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", result.Attempts)
	}
	if result.RetryAt.IsZero() {
		t.Fatal("retry deadline was not anchored")
	}
}

func TestDeliverWithRetryAnchorsRetryDeadlineBeforeWaiting(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	started := time.Now().UTC()
	result := DeliverWithRetry(
		ctx,
		[]string{"payload"},
		DeliveryRetryPolicy{MaxRetries: 1},
		func(_ context.Context, pending []string) DeliveryResult[string] {
			return FailedDelivery(nil, pending, 200*time.Millisecond, ErrRateLimit)
		},
		nil,
	)

	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", result.Err)
	}
	if result.RetryAt.Before(started.Add(150*time.Millisecond)) ||
		result.RetryAt.After(started.Add(300*time.Millisecond)) {
		t.Fatalf(
			"retry deadline = %v, want adapter-time deadline near %v",
			result.RetryAt,
			started.Add(200*time.Millisecond),
		)
	}
	if remaining := time.Until(result.RetryAt); remaining > 150*time.Millisecond {
		t.Fatalf("retry deadline retained %v after waiting, want at most 150ms", remaining)
	}
}

func TestDeliverWithRetryRejectsEmptyPayload(t *testing.T) {
	t.Parallel()

	result := DeliverWithRetry(
		t.Context(),
		[]string(nil),
		DeliveryRetryPolicy{},
		func(_ context.Context, _ []string) DeliveryResult[string] {
			t.Fatal("delivery callback called for empty payload")
			return DeliveryResult[string]{}
		},
		nil,
	)

	if result.Err == nil || !result.DefinitelyNotSent() {
		t.Fatalf("result = %#v, want definite empty-payload rejection", result)
	}
}

func TestDeliverWithRetryRejectsIncompleteResultWithoutError(t *testing.T) {
	t.Parallel()

	result := DeliverWithRetry(
		t.Context(),
		[]string{"payload"},
		DeliveryRetryPolicy{},
		func(_ context.Context, _ []string) DeliveryResult[string] {
			return DeliveryResult[string]{}
		},
		nil,
	)

	if result.Err == nil || result.Delivered() {
		t.Fatalf("result = %#v, want invalid result failure", result)
	}
}
