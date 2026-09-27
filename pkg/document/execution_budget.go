package document

import (
	"context"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/logger"
)

const (
	DefaultMaxConcurrentOperations = 1
	DefaultExecutionQueueTimeout   = 30 * time.Second
	maxConcurrentOperations        = 16
	maxExecutionQueueTimeout       = 5 * time.Minute
)

type executionBudgetContextKey struct{}

// ExecutionBudget owns admission for document worker processes in one
// MintClaw runtime. Reconfiguration affects new admissions without revoking
// operations that already hold capacity.
type ExecutionBudget struct {
	mu           sync.Mutex
	capacity     int
	active       int
	queueTimeout time.Duration
	waiters      []*executionWaiter
}

type executionWaiter struct {
	ctx     context.Context
	ready   chan struct{}
	granted bool
}

// ExecutionBudgetSnapshot is a content-free diagnostic view of document
// process pressure.
type ExecutionBudgetSnapshot struct {
	Capacity     int           `json:"capacity"`
	Active       int           `json:"active"`
	Waiting      int           `json:"waiting"`
	QueueTimeout time.Duration `json:"-"`
}

var defaultExecutionBudget = NewExecutionBudget(
	DefaultMaxConcurrentOperations,
	DefaultExecutionQueueTimeout,
)

func NewExecutionBudget(capacity int, queueTimeout time.Duration) *ExecutionBudget {
	capacity, queueTimeout = normalizeExecutionBudget(capacity, queueTimeout)
	return &ExecutionBudget{
		capacity:     capacity,
		queueTimeout: queueTimeout,
	}
}

// Configure changes admission for future acquisitions. Existing holders keep
// their leases; reducing capacity blocks new work until active use drains.
func (b *ExecutionBudget) Configure(capacity int, queueTimeout time.Duration) {
	if b == nil {
		return
	}
	capacity, queueTimeout = normalizeExecutionBudget(capacity, queueTimeout)
	b.mu.Lock()
	if b.capacity != capacity || b.queueTimeout != queueTimeout {
		b.capacity = capacity
		b.queueTimeout = queueTimeout
		b.grantWaitersLocked()
	}
	b.mu.Unlock()
}

func (b *ExecutionBudget) Snapshot() ExecutionBudgetSnapshot {
	if b == nil {
		return ExecutionBudgetSnapshot{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

// WithExecutionBudget binds one runtime-owned document budget to all worker
// launches reached through ctx.
func WithExecutionBudget(ctx context.Context, budget *ExecutionBudget) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if budget == nil {
		return ctx
	}
	return context.WithValue(ctx, executionBudgetContextKey{}, budget)
}

func executionBudgetFromContext(ctx context.Context) *ExecutionBudget {
	if ctx != nil {
		if budget, ok := ctx.Value(executionBudgetContextKey{}).(*ExecutionBudget); ok && budget != nil {
			return budget
		}
	}
	return defaultExecutionBudget
}

func (b *ExecutionBudget) acquire(ctx context.Context, operation string) (func(), Failure) {
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	if err := ctx.Err(); err != nil {
		b.mu.Unlock()
		return nil, Failure{Code: FailureCanceled, Message: "document worker was canceled"}
	}
	if b.active < b.capacity && len(b.waiters) == 0 {
		b.active++
		b.mu.Unlock()
		return b.releaseFunc(), Failure{}
	}
	waiter := &executionWaiter{ctx: ctx, ready: make(chan struct{})}
	b.waiters = append(b.waiters, waiter)
	timeout := b.queueTimeout
	snapshot := b.snapshotLocked()
	b.mu.Unlock()

	logger.WarnCF("document", "Document execution capacity saturated", map[string]any{
		"operation":        operation,
		"active":           snapshot.Active,
		"capacity":         snapshot.Capacity,
		"waiting":          snapshot.Waiting,
		"queue_timeout_ms": timeout.Milliseconds(),
	})

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		b.cancelWaiter(waiter)
		return nil, Failure{Code: FailureCanceled, Message: "document worker was canceled"}
	case <-timer.C:
		granted, timedOut := b.resolveTimedOutWaiter(waiter)
		if granted {
			return b.releaseFunc(), Failure{}
		}
		logger.WarnCF("document", "Document execution capacity wait timed out", map[string]any{
			"operation":        operation,
			"active":           timedOut.Active,
			"capacity":         timedOut.Capacity,
			"waiting":          timedOut.Waiting,
			"queue_timeout_ms": timeout.Milliseconds(),
		})
		return nil, Failure{
			Code:    FailureCapacityTimeout,
			Message: "document execution capacity wait limit exceeded",
		}
	case <-waiter.ready:
		if ctx.Err() != nil {
			b.cancelWaiter(waiter)
			return nil, Failure{Code: FailureCanceled, Message: "document worker was canceled"}
		}
		return b.releaseFunc(), Failure{}
	}
}

func (b *ExecutionBudget) cancelWaiter(waiter *executionWaiter) {
	b.mu.Lock()
	if waiter.granted {
		waiter.granted = false
		if b.active > 0 {
			b.active--
		}
	} else {
		b.removeQueuedWaiterLocked(waiter)
	}
	b.grantWaitersLocked()
	b.mu.Unlock()
}

func (b *ExecutionBudget) resolveTimedOutWaiter(waiter *executionWaiter) (bool, ExecutionBudgetSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if waiter.granted {
		return true, b.snapshotLocked()
	}
	b.removeQueuedWaiterLocked(waiter)
	b.grantWaitersLocked()
	return false, b.snapshotLocked()
}

func (b *ExecutionBudget) removeQueuedWaiterLocked(waiter *executionWaiter) {
	for index, queued := range b.waiters {
		if queued != waiter {
			continue
		}
		copy(b.waiters[index:], b.waiters[index+1:])
		b.waiters[len(b.waiters)-1] = nil
		b.waiters = b.waiters[:len(b.waiters)-1]
		return
	}
}

func (b *ExecutionBudget) grantWaitersLocked() {
	for b.active < b.capacity && len(b.waiters) > 0 {
		waiter := b.waiters[0]
		copy(b.waiters, b.waiters[1:])
		b.waiters[len(b.waiters)-1] = nil
		b.waiters = b.waiters[:len(b.waiters)-1]
		if waiter.ctx.Err() != nil {
			continue
		}
		waiter.granted = true
		b.active++
		close(waiter.ready)
	}
}

func (b *ExecutionBudget) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			if b.active > 0 {
				b.active--
			}
			b.grantWaitersLocked()
			b.mu.Unlock()
		})
	}
}

func (b *ExecutionBudget) snapshotLocked() ExecutionBudgetSnapshot {
	return ExecutionBudgetSnapshot{
		Capacity:     b.capacity,
		Active:       b.active,
		Waiting:      len(b.waiters),
		QueueTimeout: b.queueTimeout,
	}
}

func normalizeExecutionBudget(capacity int, queueTimeout time.Duration) (int, time.Duration) {
	if capacity <= 0 {
		capacity = DefaultMaxConcurrentOperations
	} else if capacity > maxConcurrentOperations {
		capacity = maxConcurrentOperations
	}
	if queueTimeout <= 0 {
		queueTimeout = DefaultExecutionQueueTimeout
	} else if queueTimeout > maxExecutionQueueTimeout {
		queueTimeout = maxExecutionQueueTimeout
	}
	return capacity, queueTimeout
}
