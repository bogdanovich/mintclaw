package channels

import "testing"

func TestDeliveryRegistryInstallAndSnapshot(t *testing.T) {
	registry := newDeliveryRegistry()
	owner := newDeliveryOwner("test", &mockChannel{}, "test")

	registry.install(owner)

	if registry.workerCount() != 1 || !registry.hasActiveWorker("test") {
		t.Fatalf("installed registry state = %+v", registry)
	}
	if got := registry.owner("test"); got != owner {
		t.Fatalf("owner() = %p, want %p", got, owner)
	}
	targets := registry.snapshot()
	if len(targets) != 1 || targets[0] != owner {
		t.Fatalf("snapshot() = %+v", targets)
	}
}

func TestDeliveryRegistryDoesNotInventMissingOwner(t *testing.T) {
	registry := newDeliveryRegistry()

	if owner := registry.owner("missing"); owner != nil {
		t.Fatalf("owner() = %+v, want nil", owner)
	}
}

func TestDeliveryRegistryRejectsIncompleteOwner(t *testing.T) {
	registry := newDeliveryRegistry()

	registry.install(&deliveryOwner{name: "missing-worker"})
	registry.install(&deliveryOwner{worker: &channelWorker{}})

	if registry.workerCount() != 0 {
		t.Fatalf("workerCount() = %d, want 0", registry.workerCount())
	}
}

func TestDeliveryRegistryConditionalRemovePreservesReplacement(t *testing.T) {
	registry := newDeliveryRegistry()
	oldOwner := newDeliveryOwner("test", &mockChannel{}, "test")
	newOwner := newDeliveryOwner("test", &mockChannel{}, "test")
	registry.install(newOwner)

	registry.removeIfMatches("test", oldOwner)

	if registry.owner("test") != newOwner {
		t.Fatal("conditional remove deleted replacement delivery state")
	}

	registry.removeIfMatches("test", newOwner)
	if registry.workerCount() != 0 || registry.owner("test") != nil {
		t.Fatalf("matching remove left registry state = %+v", registry)
	}
}
