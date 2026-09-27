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
	waiting      int
	queueTimeout time.Duration
	changed      chan struct{}
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
		changed:      make(chan struct{}),
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
		b.notifyLocked()
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
	if b.active < b.capacity {
		b.active++
		b.mu.Unlock()
		return b.releaseFunc(), Failure{}
	}
	b.waiting++
	timeout := b.queueTimeout
	snapshot := b.snapshotLocked()
	changed := b.changed
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
	for {
		select {
		case <-ctx.Done():
			b.removeWaiter()
			return nil, Failure{Code: FailureCanceled, Message: "document worker was canceled"}
		case <-timer.C:
			timedOut := b.removeWaiter()
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
		case <-changed:
			b.mu.Lock()
			if err := ctx.Err(); err != nil {
				b.waiting--
				b.notifyLocked()
				b.mu.Unlock()
				return nil, Failure{Code: FailureCanceled, Message: "document worker was canceled"}
			}
			if b.active < b.capacity {
				b.waiting--
				b.active++
				b.mu.Unlock()
				return b.releaseFunc(), Failure{}
			}
			changed = b.changed
			b.mu.Unlock()
		}
	}
}

func (b *ExecutionBudget) removeWaiter() ExecutionBudgetSnapshot {
	b.mu.Lock()
	if b.waiting > 0 {
		b.waiting--
	}
	snapshot := b.snapshotLocked()
	b.notifyLocked()
	b.mu.Unlock()
	return snapshot
}

func (b *ExecutionBudget) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			if b.active > 0 {
				b.active--
			}
			b.notifyLocked()
			b.mu.Unlock()
		})
	}
}

func (b *ExecutionBudget) snapshotLocked() ExecutionBudgetSnapshot {
	return ExecutionBudgetSnapshot{
		Capacity:     b.capacity,
		Active:       b.active,
		Waiting:      b.waiting,
		QueueTimeout: b.queueTimeout,
	}
}

func (b *ExecutionBudget) notifyLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
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
