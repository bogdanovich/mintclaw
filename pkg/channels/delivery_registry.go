package channels

// deliveryRegistry owns the active outbound delivery owners. Manager's mutex
// protects registry access while channel lifecycle operations are in progress.
type deliveryRegistry struct {
	owners map[string]*deliveryOwner
}

func newDeliveryRegistry() deliveryRegistry {
	return deliveryRegistry{
		owners: make(map[string]*deliveryOwner),
	}
}

func (r *deliveryRegistry) ensureInitialized() {
	if r.owners == nil {
		r.owners = make(map[string]*deliveryOwner)
	}
}

func (r *deliveryRegistry) install(owner *deliveryOwner) {
	if owner == nil || owner.name == "" || owner.Worker() == nil {
		return
	}
	r.ensureInitialized()
	r.owners[owner.name] = owner
}

func (r *deliveryRegistry) workerCount() int {
	return len(r.owners)
}

func (r *deliveryRegistry) hasActiveWorker(name string) bool {
	owner := r.owners[name]
	return owner != nil && owner.Worker() != nil
}

func (r *deliveryRegistry) snapshot() []*deliveryOwner {
	targets := make([]*deliveryOwner, 0, len(r.owners))
	for _, owner := range r.owners {
		targets = append(targets, owner)
	}
	return targets
}

func (r *deliveryRegistry) owner(name string) *deliveryOwner {
	return r.owners[name]
}

func (r *deliveryRegistry) removeIfMatches(name string, owner *deliveryOwner) {
	if owner != nil && r.owners[name] == owner {
		delete(r.owners, name)
	}
}
