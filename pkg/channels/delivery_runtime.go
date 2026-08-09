package channels

import "context"

// DeliveryRuntime owns outbound delivery registration and dispatcher lifetime.
// Manager's mutex protects runtime mutations during channel lifecycle changes.
type DeliveryRuntime struct {
	owners         map[string]*deliveryOwner
	dispatchCancel context.CancelFunc
}

func newDeliveryRuntime() *DeliveryRuntime {
	return &DeliveryRuntime{
		owners: make(map[string]*deliveryOwner),
	}
}

func (r *DeliveryRuntime) ensureInitialized() {
	if r.owners == nil {
		r.owners = make(map[string]*deliveryOwner)
	}
}

func (r *DeliveryRuntime) install(owner *deliveryOwner) {
	if owner == nil || owner.name == "" || owner.Worker() == nil {
		return
	}
	r.ensureInitialized()
	r.owners[owner.name] = owner
}

func (r *DeliveryRuntime) workerCount() int {
	return len(r.owners)
}

func (r *DeliveryRuntime) hasActiveWorker(name string) bool {
	owner := r.owners[name]
	return owner != nil && owner.Worker() != nil
}

func (r *DeliveryRuntime) snapshot() []*deliveryOwner {
	targets := make([]*deliveryOwner, 0, len(r.owners))
	for _, owner := range r.owners {
		targets = append(targets, owner)
	}
	return targets
}

func (r *DeliveryRuntime) owner(name string) *deliveryOwner {
	return r.owners[name]
}

func (r *DeliveryRuntime) removeIfMatches(name string, owner *deliveryOwner) {
	if owner != nil && r.owners[name] == owner {
		delete(r.owners, name)
	}
}

func (r *DeliveryRuntime) startDispatcher(parent context.Context) context.Context {
	r.stopDispatcher()
	dispatchCtx, cancel := context.WithCancel(parent)
	r.dispatchCancel = cancel
	return dispatchCtx
}

func (r *DeliveryRuntime) stopDispatcher() {
	if r == nil || r.dispatchCancel == nil {
		return
	}
	r.dispatchCancel()
	r.dispatchCancel = nil
}

func (r *DeliveryRuntime) dispatcherRunning() bool {
	return r != nil && r.dispatchCancel != nil
}
