package document

import (
	"context"
	"testing"
	"time"
)

func TestExecutionBudgetBoundsConcurrentOperations(t *testing.T) {
	budget := NewExecutionBudget(1, time.Second)
	releaseFirst, failure := budget.acquire(t.Context(), workerOperationInspect)
	if failure.Code != "" || releaseFirst == nil {
		t.Fatalf("first acquire = (%T, %#v)", releaseFirst, failure)
	}

	second := make(chan func(), 1)
	go func() {
		release, queuedFailure := budget.acquire(t.Context(), workerOperationRender)
		if queuedFailure.Code != "" {
			second <- nil
			return
		}
		second <- release
	}()
	waitForExecutionBudget(t, budget, 1, 1)
	select {
	case <-second:
		t.Fatal("second operation acquired saturated budget")
	default:
	}

	releaseFirst()
	select {
	case releaseSecond := <-second:
		if releaseSecond == nil {
			t.Fatal("second operation failed after capacity was released")
		}
		releaseSecond()
	case <-time.After(time.Second):
		t.Fatal("second operation did not acquire released capacity")
	}
	waitForExecutionBudget(t, budget, 0, 0)
}

func TestExecutionBudgetPreservesQueuedAdmissionPriority(t *testing.T) {
	budget := NewExecutionBudget(1, time.Second)
	releaseActive, failure := budget.acquire(t.Context(), workerOperationInspect)
	if failure.Code != "" || releaseActive == nil {
		t.Fatalf("active acquire = (%T, %#v)", releaseActive, failure)
	}

	first := acquireExecutionBudgetAsync(t.Context(), budget, workerOperationExtract)
	waitForExecutionBudget(t, budget, 1, 1)
	second := acquireExecutionBudgetAsync(t.Context(), budget, workerOperationRender)
	waitForExecutionBudget(t, budget, 1, 2)

	releaseActive()
	waitForExecutionBudget(t, budget, 1, 1)
	third := acquireExecutionBudgetAsync(t.Context(), budget, workerOperationInspect)
	waitForExecutionBudget(t, budget, 1, 2)

	releaseFirst := receiveExecutionBudgetLease(t, first, "first queued operation")
	assertExecutionBudgetStillQueued(t, second, "second queued operation")
	assertExecutionBudgetStillQueued(t, third, "new operation")
	releaseFirst()

	releaseSecond := receiveExecutionBudgetLease(t, second, "second queued operation")
	assertExecutionBudgetStillQueued(t, third, "new operation")
	releaseSecond()

	releaseThird := receiveExecutionBudgetLease(t, third, "new operation")
	releaseThird()
	waitForExecutionBudget(t, budget, 0, 0)
}

func TestExecutionBudgetCancellationRemovesQueuedAdmission(t *testing.T) {
	budget := NewExecutionBudget(1, time.Second)
	release, failure := budget.acquire(t.Context(), workerOperationInspect)
	if failure.Code != "" {
		t.Fatalf("first acquire failure = %#v", failure)
	}

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan Failure, 1)
	go func() {
		_, queuedFailure := budget.acquire(ctx, workerOperationExtract)
		result <- queuedFailure
	}()
	waitForExecutionBudget(t, budget, 1, 1)
	cancel()
	select {
	case queuedFailure := <-result:
		if queuedFailure.Code != FailureCanceled {
			t.Fatalf("queued cancellation = %#v", queuedFailure)
		}
	case <-time.After(time.Second):
		t.Fatal("queued cancellation did not return")
	}
	waitForExecutionBudget(t, budget, 1, 0)
	release()
	waitForExecutionBudget(t, budget, 0, 0)
}

func TestExecutionBudgetReturnsTypedQueueTimeout(t *testing.T) {
	budget := NewExecutionBudget(1, 20*time.Millisecond)
	release, failure := budget.acquire(t.Context(), workerOperationInspect)
	if failure.Code != "" {
		t.Fatalf("first acquire failure = %#v", failure)
	}
	defer release()

	_, queuedFailure := budget.acquire(t.Context(), workerOperationRender)
	if queuedFailure.Code != FailureCapacityTimeout ||
		queuedFailure.Message != "document execution capacity wait limit exceeded" {
		t.Fatalf("queue timeout = %#v", queuedFailure)
	}
	waitForExecutionBudget(t, budget, 1, 0)
}

func TestExecutionBudgetReconfigurationDoesNotRevokeActiveLease(t *testing.T) {
	budget := NewExecutionBudget(2, time.Second)
	releaseFirst, firstFailure := budget.acquire(t.Context(), workerOperationInspect)
	releaseSecond, secondFailure := budget.acquire(t.Context(), workerOperationRender)
	if firstFailure.Code != "" || secondFailure.Code != "" {
		t.Fatalf("initial acquires = (%#v, %#v)", firstFailure, secondFailure)
	}

	budget.Configure(1, 50*time.Millisecond)
	snapshot := budget.Snapshot()
	if snapshot.Capacity != 1 || snapshot.Active != 2 || snapshot.QueueTimeout != 50*time.Millisecond {
		t.Fatalf("reconfigured snapshot = %#v", snapshot)
	}
	releaseFirst()
	if snapshot = budget.Snapshot(); snapshot.Active != 1 {
		t.Fatalf("snapshot after first release = %#v", snapshot)
	}
	releaseSecond()
	waitForExecutionBudget(t, budget, 0, 0)
}

func TestExecutionBudgetNormalizesUnsafeProgrammaticConfig(t *testing.T) {
	budget := NewExecutionBudget(maxConcurrentOperations+1, maxExecutionQueueTimeout+time.Second)
	snapshot := budget.Snapshot()
	if snapshot.Capacity != maxConcurrentOperations || snapshot.QueueTimeout != maxExecutionQueueTimeout {
		t.Fatalf("normalized snapshot = %#v", snapshot)
	}

	budget.Configure(0, 0)
	snapshot = budget.Snapshot()
	if snapshot.Capacity != DefaultMaxConcurrentOperations ||
		snapshot.QueueTimeout != DefaultExecutionQueueTimeout {
		t.Fatalf("defaulted snapshot = %#v", snapshot)
	}
}

func waitForExecutionBudget(t *testing.T, budget *ExecutionBudget, active, waiting int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := budget.Snapshot()
		if snapshot.Active == active && snapshot.Waiting == waiting {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("budget snapshot = %#v, want active=%d waiting=%d", budget.Snapshot(), active, waiting)
}

type executionBudgetAcquireResult struct {
	release func()
	failure Failure
}

func acquireExecutionBudgetAsync(
	ctx context.Context,
	budget *ExecutionBudget,
	operation string,
) <-chan executionBudgetAcquireResult {
	result := make(chan executionBudgetAcquireResult, 1)
	go func() {
		release, failure := budget.acquire(ctx, operation)
		result <- executionBudgetAcquireResult{release: release, failure: failure}
	}()
	return result
}

func receiveExecutionBudgetLease(
	t *testing.T,
	result <-chan executionBudgetAcquireResult,
	label string,
) func() {
	t.Helper()
	select {
	case acquired := <-result:
		if acquired.failure.Code != "" || acquired.release == nil {
			t.Fatalf("%s acquire = (%T, %#v)", label, acquired.release, acquired.failure)
		}
		return acquired.release
	case <-time.After(time.Second):
		t.Fatalf("%s did not acquire capacity", label)
		return nil
	}
}

func assertExecutionBudgetStillQueued(
	t *testing.T,
	result <-chan executionBudgetAcquireResult,
	label string,
) {
	t.Helper()
	select {
	case acquired := <-result:
		if acquired.release != nil {
			acquired.release()
		}
		t.Fatalf("%s bypassed queued admission priority: %#v", label, acquired.failure)
	default:
	}
}

func BenchmarkExecutionBudgetUncontended(b *testing.B) {
	budget := NewExecutionBudget(1, time.Second)
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		release, failure := budget.acquire(ctx, workerOperationInspect)
		if failure.Code != "" {
			b.Fatalf("acquire failure = %#v", failure)
		}
		release()
	}
}
