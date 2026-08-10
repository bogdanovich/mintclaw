package events

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscribeOnceClosesAfterFirstEvent(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	var handled atomic.Uint64
	sub, err := bus.Channel().SubscribeOnce(
		context.Background(),
		SubscribeOptions{Name: "once", Buffer: 2},
		func(context.Context, Event) error {
			handled.Add(1)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("SubscribeOnce failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: KindAgentTurnStart})
	waitForSubscriptionDone(t, sub)
	bus.Publish(context.Background(), Event{Kind: KindAgentTurnEnd})

	if got := handled.Load(); got != 1 {
		t.Fatalf("handled = %d, want 1", got)
	}
	if got := sub.Stats().Handled; got != 1 {
		t.Fatalf("subscription handled = %d, want 1", got)
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	sub, ch, err := bus.Channel().SubscribeChan(context.Background(), SubscribeOptions{Name: "chan"})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel is open, want closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close")
	}
	waitForSubscriptionDone(t, sub)
}

func TestBlockBackpressureCloseUnblocksPublisher(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	sub, _, err := bus.Channel().SubscribeChan(context.Background(), SubscribeOptions{
		Name:         "block-close",
		Buffer:       1,
		Backpressure: Block,
	})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	first := bus.Publish(context.Background(), Event{Kind: Kind("test.first")})
	if first.Delivered != 1 {
		t.Fatalf("first Publish = %+v, want one delivered event", first)
	}

	publishStarted := make(chan struct{})
	publishReturned := make(chan PublishResult, 1)
	go func() {
		close(publishStarted)
		publishReturned <- bus.Publish(context.Background(), Event{Kind: Kind("test.second")})
	}()

	<-publishStarted
	waitForStat(t, func() uint64 {
		return sub.Stats().Received
	}, 2)
	select {
	case result := <-publishReturned:
		t.Fatalf("blocking Publish returned before close: %+v", result)
	default:
	}

	closeReturned := make(chan error, 1)
	go func() {
		closeReturned <- sub.Close()
	}()

	select {
	case err := <-closeReturned:
		if err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Close to unblock")
	}

	select {
	case <-publishReturned:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocking Publish to return after close")
	}
	waitForSubscriptionDone(t, sub)
}

func TestHandlerPanicRecovered(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "panic", Buffer: 1},
		func(context.Context, Event) error {
			panic("boom")
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: KindAgentError})
	waitForStat(t, func() uint64 {
		return sub.Stats().Panicked
	}, 1)
}

func TestLockedHandlerProcessesSequentially(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	var active atomic.Int64
	var maxActive atomic.Int64
	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "locked", Buffer: 8, Concurrency: Locked},
		func(context.Context, Event) error {
			current := active.Add(1)
			for {
				currentMax := maxActive.Load()
				if current <= currentMax || maxActive.CompareAndSwap(currentMax, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			active.Add(-1)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		bus.Publish(context.Background(), Event{Kind: KindAgentLLMDelta})
	}
	waitForStat(t, func() uint64 {
		return sub.Stats().Handled
	}, 5)

	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max active handlers = %d, want 1", got)
	}
}

func TestHandlerTimeoutDoesNotWedgeLockedSubscription(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	releaseFirst := make(chan struct{})
	defer close(releaseFirst)

	var calls atomic.Uint64
	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "timeout", Buffer: 2, Concurrency: Locked, Timeout: 20 * time.Millisecond},
		func(context.Context, Event) error {
			if calls.Add(1) == 1 {
				<-releaseFirst
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: Kind("test.first")})
	waitForStat(t, func() uint64 {
		return sub.Stats().TimedOut
	}, 1)

	bus.Publish(context.Background(), Event{Kind: Kind("test.second")})
	waitForStat(t, func() uint64 {
		return sub.Stats().Handled
	}, 1)

	if got := sub.Stats().Failed; got != 1 {
		t.Fatalf("subscription failed = %d, want timeout failure", got)
	}
}

func waitForSubscriptionDone(t *testing.T, sub Subscription) {
	t.Helper()

	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription to stop")
	}
}

func waitForStat(t *testing.T, stat func() uint64, want uint64) {
	t.Helper()

	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()

	for {
		if got := stat(); got >= want {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("timed out waiting for stat >= %d", want)
		}
	}
}

func TestKeyedPreservesOrderWithinScope(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	var mu sync.Mutex
	got := map[string][]int{}
	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "keyed-order", Buffer: 16, Concurrency: Keyed},
		func(_ context.Context, evt Event) error {
			mu.Lock()
			got[evt.Scope.SessionKey] = append(got[evt.Scope.SessionKey], evt.Payload.(int))
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	for i := 0; i < 6; i++ {
		bus.Publish(context.Background(), Event{Kind: KindAgentLLMDelta, Scope: Scope{SessionKey: "s1"}, Payload: i})
		bus.Publish(context.Background(), Event{Kind: KindAgentLLMDelta, Scope: Scope{SessionKey: "s2"}, Payload: i})
	}
	waitForStat(t, func() uint64 { return sub.Stats().Handled }, 12)

	want := []int{0, 1, 2, 3, 4, 5}
	for _, key := range []string{"s1", "s2"} {
		if !slices.Equal(got[key], want) {
			t.Fatalf("scope %q handled order = %v, want %v", key, got[key], want)
		}
	}
}

func TestKeyedOrdersPerScopeAndRunsConcurrentlyAcrossScopes(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	var (
		stateMu         sync.Mutex
		perKeyActive    = map[string]int{}
		maxPerKeyActive int
		globalActive    atomic.Int64
		maxGlobalActive atomic.Int64
	)

	track := func(delta int, key string) {
		if delta > 0 {
			g := globalActive.Add(1)
			for {
				cur := maxGlobalActive.Load()
				if g <= cur || maxGlobalActive.CompareAndSwap(cur, g) {
					break
				}
			}
		} else {
			globalActive.Add(-1)
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		perKeyActive[key] += delta
		if delta > 0 && perKeyActive[key] > maxPerKeyActive {
			maxPerKeyActive = perKeyActive[key]
		}
	}

	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "keyed", Buffer: 16, Concurrency: Keyed},
		func(_ context.Context, evt Event) error {
			key := evt.Scope.SessionKey
			track(1, key)
			time.Sleep(5 * time.Millisecond)
			track(-1, key)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	for i := 0; i < 4; i++ {
		bus.Publish(context.Background(), Event{Kind: KindAgentLLMDelta, Scope: Scope{SessionKey: "s1"}})
		bus.Publish(context.Background(), Event{Kind: KindAgentLLMDelta, Scope: Scope{SessionKey: "s2"}})
	}
	waitForStat(t, func() uint64 { return sub.Stats().Handled }, 8)

	if got := maxPerKeyActive; got != 1 {
		t.Fatalf("max active handlers per scope = %d, want 1 (per-scope ordering)", got)
	}
	if got := maxGlobalActive.Load(); got < 2 {
		t.Fatalf("max active handlers across scopes = %d, want >= 2 (cross-scope concurrency)", got)
	}
}

func TestKeyedHonorsCustomKeyFunc(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	var mu sync.Mutex
	got := map[string][]string{}
	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{
			Name:        "keyed-custom",
			Buffer:      16,
			Concurrency: Keyed,
			KeyFunc:     func(evt Event) string { return string(evt.Kind) },
		},
		func(_ context.Context, evt Event) error {
			mu.Lock()
			got[string(evt.Kind)] = append(got[string(evt.Kind)], evt.Scope.SessionKey)
			mu.Unlock()
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	for i := 0; i < 3; i++ {
		bus.Publish(context.Background(), Event{Kind: Kind("k1"), Scope: Scope{SessionKey: "s1"}})
		bus.Publish(context.Background(), Event{Kind: Kind("k1"), Scope: Scope{SessionKey: "s2"}})
		bus.Publish(context.Background(), Event{Kind: Kind("k2"), Scope: Scope{SessionKey: "s3"}})
	}
	waitForStat(t, func() uint64 { return sub.Stats().Handled }, 9)

	wantK1 := []string{"s1", "s2", "s1", "s2", "s1", "s2"}
	if !slices.Equal(got["k1"], wantK1) {
		t.Fatalf("k1 handled order = %v, want %v", got["k1"], wantK1)
	}
	wantK2 := []string{"s3", "s3", "s3"}
	if !slices.Equal(got["k2"], wantK2) {
		t.Fatalf("k2 handled order = %v, want %v", got["k2"], wantK2)
	}
}

func TestKeyedDefaultKeyFunc(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		evt  Event
		want string
	}{
		{name: "session", evt: Event{Scope: Scope{SessionKey: "k"}}, want: "session:k"},
		{
			name: "trace",
			evt:  Event{Scope: Scope{TraceScope: TraceScope{Workspace: "w", TurnID: "t"}}},
			want: "trace:w\x00t",
		},
		{name: "chat-channel", evt: Event{Scope: Scope{Channel: "c", ChatID: "1"}}, want: "chat:c\x001"},
		{name: "chat-account", evt: Event{Scope: Scope{Account: "a", ChatID: "1"}}, want: "chat:a\x001"},
		{name: "agent", evt: Event{Scope: Scope{AgentID: "a1"}}, want: "agent:a1"},
		{name: "empty", evt: Event{}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultKeyFunc(tc.evt); got != tc.want {
				t.Fatalf("defaultKeyFunc = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKeyedDropNewestBoundsBacklog(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	started := make(chan struct{})
	release := make(chan struct{})

	var handledMu sync.Mutex
	handled := []int{}
	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "keyed-drop-newest", Buffer: 1, Concurrency: Keyed, Backpressure: DropNewest},
		func(_ context.Context, evt Event) error {
			if evt.Payload.(int) == 1 {
				close(started)
				<-release
			}
			handledMu.Lock()
			handled = append(handled, evt.Payload.(int))
			handledMu.Unlock()
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: 1})
	<-started
	for i := 2; i <= 6; i++ {
		bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: i})
	}
	close(release)
	waitForStat(t, func() uint64 { return sub.Stats().Handled }, 2)

	handledMu.Lock()
	defer handledMu.Unlock()
	if !slices.Equal(handled, []int{1, 2}) {
		t.Fatalf("handled = %v, want [1 2]", handled)
	}
	if got := sub.Stats().Dropped; got != 4 {
		t.Fatalf("dropped = %d, want 4", got)
	}
	if got := sub.Stats().Received; got != 6 {
		t.Fatalf("received = %d, want 6", got)
	}
}

func TestKeyedDropOldestKeepsNewest(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	started := make(chan struct{})
	release := make(chan struct{})

	var handledMu sync.Mutex
	handled := []int{}
	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "keyed-drop-oldest", Buffer: 1, Concurrency: Keyed, Backpressure: DropOldest},
		func(_ context.Context, evt Event) error {
			if evt.Payload.(int) == 1 {
				close(started)
				<-release
			}
			handledMu.Lock()
			handled = append(handled, evt.Payload.(int))
			handledMu.Unlock()
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: 1})
	<-started
	for i := 2; i <= 6; i++ {
		bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: i})
	}
	close(release)
	waitForStat(t, func() uint64 { return sub.Stats().Handled }, 2)

	handledMu.Lock()
	defer handledMu.Unlock()
	if !slices.Equal(handled, []int{1, 6}) {
		t.Fatalf("handled = %v, want [1 6]", handled)
	}
	if got := sub.Stats().Dropped; got != 4 {
		t.Fatalf("dropped = %d, want 4", got)
	}
}

func TestKeyedBlockBackpressureBlocksPublisher(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	started := make(chan struct{})
	release := make(chan struct{})

	var handledMu sync.Mutex
	handled := []int{}
	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "keyed-block", Buffer: 1, Concurrency: Keyed, Backpressure: Block},
		func(_ context.Context, evt Event) error {
			if evt.Payload.(int) == 1 {
				close(started)
				<-release
			}
			handledMu.Lock()
			handled = append(handled, evt.Payload.(int))
			handledMu.Unlock()
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: 1})
	<-started
	if result := bus.Publish(
		context.Background(),
		Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: 2},
	); result.Delivered != 1 {
		t.Fatalf("second Publish = %+v, want one delivered event", result)
	}

	publishReturned := make(chan PublishResult, 1)
	go func() {
		publishReturned <- bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: 3})
	}()

	select {
	case result := <-publishReturned:
		t.Fatalf("blocking Publish returned before release: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case result := <-publishReturned:
		if result.Delivered != 1 {
			t.Fatalf("blocked Publish = %+v, want one delivered event", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked Publish to return")
	}
	waitForStat(t, func() uint64 { return sub.Stats().Handled }, 3)

	handledMu.Lock()
	defer handledMu.Unlock()
	if !slices.Equal(handled, []int{1, 2, 3}) {
		t.Fatalf("handled = %v, want [1 2 3]", handled)
	}
}

func TestKeyedBlockBackpressureHonorsContext(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	started := make(chan struct{})
	release := make(chan struct{})

	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "keyed-block-ctx", Buffer: 1, Concurrency: Keyed, Backpressure: Block},
		func(_ context.Context, evt Event) error {
			if evt.Payload.(int) == 1 {
				close(started)
				<-release
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: 1})
	<-started
	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: 2})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := bus.Publish(ctx, Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: 3})
	if result.Blocked != 1 {
		t.Fatalf("Publish = %+v, want blocked result", result)
	}
	close(release)
	waitForStat(t, func() uint64 { return sub.Stats().Handled }, 2)
}

func TestKeyedSubscribeOnceHandlesSingleEvent(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	var handled atomic.Uint64
	sub, err := bus.Channel().SubscribeOnce(
		context.Background(),
		SubscribeOptions{Name: "keyed-once", Buffer: 16, Concurrency: Keyed},
		func(context.Context, Event) error {
			handled.Add(1)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("SubscribeOnce failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}})
		}()
	}
	wg.Wait()
	waitForSubscriptionDone(t, sub)

	if got := handled.Load(); got != 1 {
		t.Fatalf("handled = %d, want 1", got)
	}
}

func TestKeyedDropOldestEvictsOldestQueuedEventAcrossWorkers(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	startedA := make(chan struct{})
	startedB := make(chan struct{})
	release := make(chan struct{})

	var handledMu sync.Mutex
	handled := []int{}
	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "keyed-drop-oldest-cross", Buffer: 2, Concurrency: Keyed, Backpressure: DropOldest},
		func(_ context.Context, evt Event) error {
			payload := evt.Payload.(int)
			if payload <= 2 {
				if payload == 1 {
					close(startedA)
				} else {
					close(startedB)
				}
				<-release
			}
			handledMu.Lock()
			handled = append(handled, payload)
			handledMu.Unlock()
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "a"}, Payload: 1})
	<-startedA
	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "b"}, Payload: 2})
	<-startedB
	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "b"}, Payload: 3})
	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "a"}, Payload: 4})
	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "b"}, Payload: 5})

	close(release)
	waitForStat(t, func() uint64 { return sub.Stats().Handled }, 4)

	handledMu.Lock()
	defer handledMu.Unlock()
	got := append([]int(nil), handled...)
	slices.Sort(got)
	if !slices.Equal(got, []int{1, 2, 4, 5}) {
		t.Fatalf("handled = %v, want [1 2 4 5] (event 3 evicted)", got)
	}
	if dropped := sub.Stats().Dropped; dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
}

func TestKeyedHandlerSeesSubscriptionContext(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	ctx, cancel := context.WithCancel(context.Background())
	handlerStarted := make(chan struct{})
	handlerObservedCancel := make(chan struct{})

	sub, err := bus.Channel().Subscribe(
		ctx,
		SubscribeOptions{Name: "keyed-ctx", Buffer: 2, Concurrency: Keyed},
		func(hctx context.Context, evt Event) error {
			if evt.Payload.(int) == 1 {
				close(handlerStarted)
				<-hctx.Done()
				close(handlerObservedCancel)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}, Payload: 1})
	<-handlerStarted
	cancel()
	waitForSubscriptionDone(t, sub)

	select {
	case <-handlerObservedCancel:
	case <-time.After(time.Second):
		t.Fatal("keyed handler did not observe subscription cancellation")
	}
}

func TestKeyedBlockWakesEveryWaiterWithCapacity(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	startedA := make(chan struct{})
	startedB := make(chan struct{})
	first := make(chan struct{})
	later := make(chan struct{})

	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "keyed-block-liveness", Buffer: 2, Concurrency: Keyed, Backpressure: Block},
		func(_ context.Context, evt Event) error {
			switch evt.Payload.(int) {
			case 1:
				close(startedA)
				<-first
			case 2:
				close(startedB)
				<-first
			default:
				<-later
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "a"}, Payload: 1})
	<-startedA
	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "b"}, Payload: 2})
	<-startedB
	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "a"}, Payload: 3})
	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "b"}, Payload: 4})

	resultW1 := make(chan PublishResult, 1)
	resultW2 := make(chan PublishResult, 1)
	go func() {
		resultW1 <- bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "a"}, Payload: 5})
	}()
	go func() {
		resultW2 <- bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "b"}, Payload: 6})
	}()

	// Both publishers must be blocked on capacity: pending == limit and both
	// workers are pinned on the first gate, so no slot can free.
	waitForStat(t, func() uint64 { return sub.Stats().Received }, 6)

	close(first)
	close(later)

	for i, ch := range []chan PublishResult{resultW1, resultW2} {
		select {
		case result := <-ch:
			if result.Delivered != 1 {
				t.Fatalf("waiter %d Publish = %+v, want one delivered event", i+1, result)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiter %d remained blocked despite available capacity", i+1)
		}
	}
	waitForStat(t, func() uint64 { return sub.Stats().Handled }, 6)
}

func TestKeyedCloseStopsAcceptanceSynchronously(t *testing.T) {
	t.Parallel()

	bus := NewBus()
	defer closeBus(t, bus)

	sub, err := bus.Channel().Subscribe(
		context.Background(),
		SubscribeOptions{Name: "keyed-close", Buffer: 2, Concurrency: Keyed},
		func(context.Context, Event) error { return nil },
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	bus.Publish(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}})
	waitForStat(t, func() uint64 { return sub.Stats().Handled }, 1)

	if err := sub.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	waitForSubscriptionDone(t, sub)

	es := sub.(*eventSubscription)
	result := es.enqueue(context.Background(), Event{Kind: Kind("k"), Scope: Scope{SessionKey: "s"}}, false)
	if !result.closed {
		t.Fatalf("enqueue after Close = %+v, want closed result", result)
	}
	if got := es.counters.received.Load(); got != 1 {
		t.Fatalf("received after close = %d, want 1 (post-close enqueue must not count)", got)
	}
}
