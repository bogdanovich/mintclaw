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

// keyedDispatcher routes events to per-key workers so that events sharing a
// key are handled sequentially while different keys run concurrently. Workers
// exit when their queue is empty and are recreated on the next event, keeping
// per-key FIFO ordering without retaining workers for retired scopes.
type keyedDispatcher struct {
	sub     *eventSubscription
	keyFunc func(Event) string

	mu      sync.Mutex
	workers map[string]*keyedWorker
	closing chan struct{}
	closed  bool
	wg      sync.WaitGroup
}

type keyedWorker struct {
	queue []Event
}

func newKeyedDispatcher(sub *eventSubscription, keyFunc func(Event) string) *keyedDispatcher {
	if keyFunc == nil {
		keyFunc = defaultKeyFunc
	}
	return &keyedDispatcher{
		sub:     sub,
		keyFunc: keyFunc,
		workers: make(map[string]*keyedWorker),
		closing: make(chan struct{}),
	}
}

// dispatch enqueues evt for the worker of its key, starting the worker on
// first use. Backpressure already applied at the subscription queue, so this
// never blocks and never drops.
func (d *keyedDispatcher) dispatch(ctx context.Context, evt Event) {
	key := d.keyFunc(evt)

	d.mu.Lock()
	w := d.workers[key]
	if w == nil {
		w = &keyedWorker{}
		d.workers[key] = w
		d.wg.Add(1)
		go d.runWorker(ctx, key, w)
	}
	w.queue = append(w.queue, evt)
	d.mu.Unlock()
}

func (d *keyedDispatcher) runWorker(ctx context.Context, key string, w *keyedWorker) {
	defer d.wg.Done()

	d.mu.Lock()
	defer d.mu.Unlock()

	for {
		if len(w.queue) == 0 {
			// Idle worker. Removing the worker under the lock is atomic with
			// dispatch: either the worker exits and the next event for this
			// key starts a fresh worker, or an event was appended first and
			// the loop continues.
			delete(d.workers, key)
			return
		}

		evt := w.queue[0]
		w.queue = w.queue[1:]

		d.mu.Unlock()
		d.handle(ctx, evt)
		d.mu.Lock()

		select {
		case <-d.closing:
			d.drainLocked(ctx, key, w)
			return
		default:
		}
	}
}

// drainLocked processes events remaining in w.queue before exiting. The
// caller holds d.mu; handle runs without the lock.
func (d *keyedDispatcher) drainLocked(ctx context.Context, key string, w *keyedWorker) {
	for len(w.queue) > 0 {
		evt := w.queue[0]
		w.queue = w.queue[1:]

		d.mu.Unlock()
		d.handle(ctx, evt)
		d.mu.Lock()
	}
	delete(d.workers, key)
}

func (d *keyedDispatcher) handle(ctx context.Context, evt Event) {
	d.sub.handle(ctx, evt)
}

// shutdown stops dispatching and lets workers drain their queues before
// returning. It is safe to call more than once.
func (d *keyedDispatcher) shutdown() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.closing)
	d.mu.Unlock()
}
