package channels

import (
	"context"
	"testing"
)

func TestDeliveryRuntimeInstallAndSnapshot(t *testing.T) {
	runtime := newDeliveryRuntime(nil)
	owner := newDeliveryOwner("test", &mockChannel{}, "test")

	runtime.install(owner)

	if runtime.workerCount() != 1 || !runtime.hasActiveWorker("test") {
		t.Fatalf("installed runtime state = %+v", runtime)
	}
	if got := runtime.owner("test"); got != owner {
		t.Fatalf("owner() = %p, want %p", got, owner)
	}
	targets := runtime.snapshot()
	if len(targets) != 1 || targets[0] != owner {
		t.Fatalf("snapshot() = %+v", targets)
	}
}

func TestDeliveryRuntimeDoesNotInventMissingOwner(t *testing.T) {
	runtime := newDeliveryRuntime(nil)

	if owner := runtime.owner("missing"); owner != nil {
		t.Fatalf("owner() = %+v, want nil", owner)
	}
}

func TestDeliveryRuntimeRejectsIncompleteOwner(t *testing.T) {
	runtime := newDeliveryRuntime(nil)

	runtime.install(&deliveryOwner{name: "missing-worker"})
	runtime.install(&deliveryOwner{worker: &channelWorker{}})

	if runtime.workerCount() != 0 {
		t.Fatalf("workerCount() = %d, want 0", runtime.workerCount())
	}
}

func TestDeliveryRuntimeConditionalRemovePreservesReplacement(t *testing.T) {
	runtime := newDeliveryRuntime(nil)
	oldOwner := newDeliveryOwner("test", &mockChannel{}, "test")
	newOwner := newDeliveryOwner("test", &mockChannel{}, "test")
	runtime.install(newOwner)

	runtime.removeIfMatches("test", oldOwner)

	if runtime.owner("test") != newOwner {
		t.Fatal("conditional remove deleted replacement delivery state")
	}

	runtime.removeIfMatches("test", newOwner)
	if runtime.workerCount() != 0 || runtime.owner("test") != nil {
		t.Fatalf("matching remove left runtime state = %+v", runtime)
	}
}

func TestDeliveryRuntimeDispatcherLifecycle(t *testing.T) {
	runtime := newDeliveryRuntime(nil)
	first := runtime.startDispatcher(context.Background())
	second := runtime.startDispatcher(context.Background())

	select {
	case <-first.Done():
	default:
		t.Fatal("replaced dispatcher context remained active")
	}
	if !runtime.dispatcherRunning() {
		t.Fatal("replacement dispatcher is not running")
	}

	runtime.stopDispatcher()
	runtime.stopDispatcher()
	select {
	case <-second.Done():
	default:
		t.Fatal("stopped dispatcher context remained active")
	}
	if runtime.dispatcherRunning() {
		t.Fatal("dispatcher remained registered after stop")
	}
}
