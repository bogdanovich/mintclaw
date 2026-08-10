package events

import (
	"context"
	"sync"
)

// defaultKeyFunc derives the ordering key for Keyed subscriptions from the
// event scope: session, then turn trace, then chat, then agent identity.
// Events that share a key are handled sequentially while events with
// different keys run concurrently. Events without an identifiable scope share
// the empty key and are handled in subscription order.
func defaultKeyFunc(evt Event) string {
	scope := evt.Scope
	if scope.SessionKey != "" {
		return "session:" + scope.SessionKey
	}
	if trace := scope.TurnTraceScope(); trace.Complete() {
		return "trace:" + trace.Workspace + "\x00" + trace.TurnID
	}
	if scope.ChatID != "" {
		switch {
		case scope.Channel != "":
			return "chat:" + scope.Channel + "\x00" + scope.ChatID
		case scope.Account != "":
			return "chat:" + scope.Account + "\x00" + scope.ChatID
		}
	}
	if scope.AgentID != "" {
		return "agent:" + scope.AgentID
	}
	return ""
}

// keyedDispatcher is the delivery path for Keyed subscriptions. It replaces
// the bounded subscription channel: events are enqueued directly into
// per-key FIFO queues while the total number of queued (not-yet-started)
// events stays within the configured Buffer, honoring the configured
// BackpressurePolicy. Events sharing a key are handled sequentially while
// different keys run concurrently.
type keyedDispatcher struct {
	sub     *eventSubscription
	ctx     context.Context
	keyFunc func(Event) string
	policy  BackpressurePolicy
	limit   int

	mu           sync.Mutex
	nextSeq      uint64
	workers      map[string]*keyedWorker
	pending      int
	onceAccepted bool
	waiting      int
	wakeup       chan struct{}
	closing      chan struct{}
	closed       bool
	wg           sync.WaitGroup

	onceClose sync.Once
}

type queuedEvent struct {
	seq uint64
	evt Event
}

type keyedWorker struct {
	key    string
	queue  []queuedEvent
	active bool
}

func newKeyedDispatcher(sub *eventSubscription, ctx context.Context, keyFunc func(Event) string) *keyedDispatcher {
	if ctx == nil {
		ctx = context.Background()
	}
	if keyFunc == nil {
		keyFunc = defaultKeyFunc
	}
	return &keyedDispatcher{
		sub:     sub,
		ctx:     ctx,
		keyFunc: keyFunc,
		policy:  sub.opts.Backpressure,
		limit:   sub.opts.Buffer,
		workers: make(map[string]*keyedWorker),
		wakeup:  make(chan struct{}),
		closing: make(chan struct{}),
	}
}

// enqueue accepts evt under the subscription backpressure contract and
// returns the delivery outcome. It is the Keyed equivalent of the bounded
// subscription channel: at most limit events are queued, workers start on
// first use, and a full dispatcher applies the configured policy.
func (d *keyedDispatcher) enqueue(ctx context.Context, evt Event, nonBlocking bool) deliveryResult {
	if ctx == nil {
		ctx = context.Background()
	}
	key := d.keyFunc(evt)

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return deliveryResult{closed: true}
	}
	if d.sub.once && d.onceAccepted {
		d.mu.Unlock()
		return deliveryResult{closed: true}
	}
	d.sub.counters.received.Add(1)

	for {
		if d.pending < d.limit {
			d.acceptLocked(key, evt)
			d.mu.Unlock()
			return deliveryResult{delivered: 1}
		}

		if nonBlocking || d.policy != Block {
			if d.policy == DropOldest {
				d.dropOldestLocked()
				d.acceptLocked(key, evt)
				d.mu.Unlock()
				return deliveryResult{delivered: 1, dropped: 1}
			}
			d.sub.counters.dropped.Add(1)
			d.mu.Unlock()
			return deliveryResult{dropped: 1}
		}

		// Block: wait for capacity outside the lock so workers can drain.
		// Every capacity release broadcasts to all waiters so a release can
		// never be coalesced away while slots remain free.
		d.waiting++
		wakeup := d.wakeup
		d.mu.Unlock()
		select {
		case <-wakeup:
		case <-d.sub.closing:
			d.mu.Lock()
			d.waiting--
			d.mu.Unlock()
			return deliveryResult{closed: true}
		case <-d.closing:
			d.mu.Lock()
			d.waiting--
			d.mu.Unlock()
			return deliveryResult{closed: true}
		case <-ctx.Done():
			d.mu.Lock()
			d.waiting--
			d.mu.Unlock()
			d.sub.counters.dropped.Add(1)
			return deliveryResult{dropped: 1, blocked: 1}
		}
		d.mu.Lock()
		d.waiting--
		if d.closed {
			d.mu.Unlock()
			return deliveryResult{closed: true}
		}
		if d.sub.once && d.onceAccepted {
			d.mu.Unlock()
			return deliveryResult{closed: true}
		}
	}
}

// acceptLocked records the one event accepted by a Keyed SubscribeOnce,
// starts the worker for its key, and enqueues the event.
func (d *keyedDispatcher) acceptLocked(key string, evt Event) {
	if d.sub.once {
		d.onceAccepted = true
	}
	w := d.workers[key]
	if w == nil {
		w = &keyedWorker{key: key}
		d.workers[key] = w
		d.wg.Add(1)
		go d.runWorker(key, w)
	}
	d.nextSeq++
	w.queue = append(w.queue, queuedEvent{seq: d.nextSeq, evt: evt})
	d.pending++
	if d.sub.once {
		d.onceClose.Do(func() { go func() { _ = d.sub.Close() }() })
	}
}

// dropOldestLocked drops the earliest queued event and frees its slot. The
// victim is the queue head with the smallest acceptance sequence, preserving
// FIFO for the remaining accepted events. The worker stays mapped until its
// goroutine exits so a retired worker can never remove a replacement.
func (d *keyedDispatcher) dropOldestLocked() {
	var oldest *keyedWorker
	for _, w := range d.workers {
		if len(w.queue) == 0 {
			continue
		}
		if oldest == nil || w.queue[0].seq < oldest.queue[0].seq {
			oldest = w
		}
	}
	if oldest == nil {
		return
	}
	oldest.queue = oldest.queue[1:]
	d.pending--
	d.sub.counters.dropped.Add(1)
	d.capacityFreedLocked()
}

func (d *keyedDispatcher) runWorker(key string, w *keyedWorker) {
	defer d.wg.Done()

	for {
		d.mu.Lock()
		if len(w.queue) == 0 {
			if d.workers[key] == w {
				delete(d.workers, key)
			}
			d.mu.Unlock()
			return
		}

		evt := w.queue[0].evt
		w.queue = w.queue[1:]
		d.pending--
		w.active = true
		d.capacityFreedLocked()
		d.mu.Unlock()

		d.handle(evt)

		d.mu.Lock()
		w.active = false
		closing := false
		select {
		case <-d.closing:
			closing = true
		default:
		}
		if closing {
			for len(w.queue) > 0 {
				evt := w.queue[0].evt
				w.queue = w.queue[1:]
				d.pending--
				d.capacityFreedLocked()
				d.mu.Unlock()
				d.handle(evt)
				d.mu.Lock()
			}
			if d.workers[key] == w {
				delete(d.workers, key)
			}
			d.mu.Unlock()
			return
		}
		d.mu.Unlock()
	}
}

func (d *keyedDispatcher) handle(evt Event) {
	d.sub.handle(d.ctx, evt)
}

// capacityFreedLocked wakes every waiter blocked on capacity so each one can
// recheck whether a slot is now available. The caller holds d.mu.
func (d *keyedDispatcher) capacityFreedLocked() {
	if d.waiting == 0 {
		return
	}
	close(d.wakeup)
	d.wakeup = make(chan struct{})
}

// shutdown stops accepting events and lets workers drain their queues before
// returning. It is safe to call more than once.
func (d *keyedDispatcher) shutdown() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.closing)
	d.capacityFreedLocked()
	d.mu.Unlock()
}
