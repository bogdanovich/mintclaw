package interactions

import (
	"context"
	"sync"
)

type observerDelivery struct {
	mu             sync.Mutex
	observer       Observer
	active         bool
	closed         bool
	notifying      bool
	pending        []*queuedObservation
	unsubscribed   bool
	resyncRequired bool
	work           int
	idle           chan struct{}
}

func newObserverDelivery(observer Observer, active bool) *observerDelivery {
	return &observerDelivery{observer: observer, active: active}
}

func (d *observerDelivery) enqueue(observation *queuedObservation) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		d.finishObservation(observation)
		return
	}
	d.pending = append(d.pending, observation)
	drain := d.active && !d.notifying
	if drain {
		d.notifying = true
	}
	d.mu.Unlock()
	if drain {
		d.drain()
	}
}

func (d *observerDelivery) activate() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed || d.active {
		d.mu.Unlock()
		return
	}
	d.active = true
	drain := len(d.pending) > 0 && !d.notifying
	if drain {
		d.notifying = true
	}
	d.mu.Unlock()
	if drain {
		d.drain()
	}
}

func (d *observerDelivery) unsubscribe() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	d.unsubscribed = true
	var pending []*queuedObservation
	if !d.active {
		d.closed = true
		d.resyncRequired = d.resyncRequired || d.work > 0
		pending = d.pending
		d.pending = nil
		d.notifying = false
	}
	resync := d.resyncRequired
	d.mu.Unlock()
	for _, observation := range pending {
		d.finishObservation(observation)
	}
	return resync
}

func (d *observerDelivery) subscribed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.unsubscribed
}

func (d *observerDelivery) beginAdmission() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.unsubscribed {
		return false
	}
	d.addWorkLocked(1)
	return true
}

func (d *observerDelivery) addWork(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addWorkLocked(n)
}

func (d *observerDelivery) addWorkLocked(n int) {
	if d.work == 0 && n > 0 {
		d.idle = make(chan struct{})
	}
	d.work += n
}

func (d *observerDelivery) finishWork(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.work -= n
	if d.work == 0 && n > 0 {
		close(d.idle)
	}
}

func (d *observerDelivery) finishObservation(observation *queuedObservation) {
	observation.done()
	d.finishWork(1)
}

func (d *observerDelivery) wait(ctx context.Context) (int, bool, error) {
	d.mu.Lock()
	if d.work == 0 {
		resync := d.resyncRequired
		d.mu.Unlock()
		return 0, resync, nil
	}
	idle := d.idle
	d.mu.Unlock()
	var err error
	select {
	case <-idle:
	case <-ctx.Done():
		err = ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.work == 0 {
		err = nil
	}
	return d.work, d.resyncRequired, err
}

func (d *observerDelivery) drain() {
	for {
		observation, observer, ok := d.claimNext()
		if !ok {
			return
		}
		notifyObserver(
			observer,
			EventObservation{
				Event:  cloneEvent(observation.observation.Event),
				Record: cloneRecord(observation.observation.Record),
			},
		)
		d.finishObservation(observation)
	}
}

func (d *observerDelivery) claimNext() (*queuedObservation, Observer, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || !d.active || len(d.pending) == 0 {
		d.notifying = false
		return nil, nil, false
	}
	observation := d.pending[0]
	d.pending[0] = nil
	d.pending = d.pending[1:]
	return observation, d.observer, true
}
