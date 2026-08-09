package agent

import "sync"

type agentInstanceResources struct {
	mu       sync.Mutex
	users    int
	retired  bool
	finalize func() error
}

func newAgentInstanceResources(finalize func() error) *agentInstanceResources {
	return &agentInstanceResources{finalize: finalize}
}

func (r *agentInstanceResources) acquire() (func(), bool) {
	if r == nil {
		return func() {}, true
	}
	r.mu.Lock()
	if r.retired {
		r.mu.Unlock()
		return nil, false
	}
	r.users++
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(r.release)
	}, true
}

func (r *agentInstanceResources) retire() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.retired = true
	finalize := r.takeFinalizerLocked()
	r.mu.Unlock()
	if finalize != nil {
		return finalize()
	}
	return nil
}

func (r *agentInstanceResources) release() {
	r.mu.Lock()
	if r.users > 0 {
		r.users--
	}
	finalize := r.takeFinalizerLocked()
	r.mu.Unlock()
	if finalize != nil {
		_ = finalize()
	}
}

func (r *agentInstanceResources) takeFinalizerLocked() func() error {
	if !r.retired || r.users != 0 || r.finalize == nil {
		return nil
	}
	finalize := r.finalize
	r.finalize = nil
	return finalize
}
