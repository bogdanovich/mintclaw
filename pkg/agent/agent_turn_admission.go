package agent

import (
	"context"
	"fmt"
	"sync"
)

type agentTurnAdmissionsKey struct{}

type agentTurnAdmissionLease struct {
	mu      sync.Mutex
	refs    int
	release func()
}

func newAgentTurnAdmissionLease(release func()) *agentTurnAdmissionLease {
	return &agentTurnAdmissionLease{refs: 1, release: release}
}

func (l *agentTurnAdmissionLease) retain() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refs == 0 {
		return false
	}
	l.refs++
	return true
}

func (l *agentTurnAdmissionLease) releaseRef() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.refs == 0 {
		l.mu.Unlock()
		return
	}
	l.refs--
	if l.refs > 0 {
		l.mu.Unlock()
		return
	}
	release := l.release
	l.release = nil
	l.mu.Unlock()
	if release != nil {
		release()
	}
}

type agentTurnAdmissionController struct {
	mu      sync.Mutex
	limits  map[string]int
	active  map[string]int
	pauses  int
	changed chan struct{}
}

func newAgentTurnAdmissionController(registry *AgentRegistry) *agentTurnAdmissionController {
	controller := &agentTurnAdmissionController{
		limits:  make(map[string]int),
		active:  make(map[string]int),
		changed: make(chan struct{}),
	}
	controller.update(registry)
	return controller
}

func agentTurnLimits(registry *AgentRegistry) map[string]int {
	limits := make(map[string]int)
	if registry == nil {
		return limits
	}
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil || agent.MaxParallelTurns <= 0 {
			continue
		}
		limits[agentID] = agent.MaxParallelTurns
	}
	return limits
}

func (c *agentTurnAdmissionController) update(registry *AgentRegistry) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.limits = agentTurnLimits(registry)
	c.notifyLocked()
	c.mu.Unlock()
}

func (c *agentTurnAdmissionController) acquire(ctx context.Context, agentID string) (func(), error) {
	return c.acquireObserved(ctx, agentID, nil)
}

func (c *agentTurnAdmissionController) acquireObserved(
	ctx context.Context,
	agentID string,
	onWait func(active, limit int),
) (func(), error) {
	waitingReported := false
	for {
		c.mu.Lock()
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		limit := c.limits[agentID]
		if c.pauses == 0 && (limit <= 0 || c.active[agentID] < limit) {
			c.active[agentID]++
			c.mu.Unlock()
			return func() { c.release(agentID) }, nil
		}
		active := c.active[agentID]
		changed := c.changed
		c.mu.Unlock()
		if !waitingReported && onWait != nil {
			waitingReported = true
			onWait(active, limit)
		}

		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c *agentTurnAdmissionController) pause(ctx context.Context) (func(), error) {
	if c == nil {
		return func() {}, nil
	}
	c.mu.Lock()
	c.pauses++
	c.notifyLocked()
	for len(c.active) > 0 {
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			c.mu.Lock()
			c.pauses--
			c.notifyLocked()
			c.mu.Unlock()
			return nil, ctx.Err()
		}
		c.mu.Lock()
	}
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.pauses--
			c.notifyLocked()
			c.mu.Unlock()
		})
	}, nil
}

// QuiesceTurns blocks new turn admission and waits for admitted turns to drain.
func (al *AgentLoop) QuiesceTurns(ctx context.Context) (func(), error) {
	if al == nil {
		return nil, fmt.Errorf("agent loop is nil")
	}
	if al.turns == nil || al.turns.admissions == nil {
		return func() {}, nil
	}
	return al.turns.admissions.pause(ctx)
}

func (al *AgentLoop) currentAgentGeneration(agent *AgentInstance) (*AgentInstance, bool, error) {
	if agent == nil || agent.ownerRegistry == nil {
		return agent, false, nil
	}
	registry := al.GetRegistry()
	if registry == agent.ownerRegistry {
		return agent, false, nil
	}
	current, ok := registry.GetAgent(agent.ID)
	if !ok || current == nil {
		return nil, false, fmt.Errorf("agent %q is unavailable after config reload", agent.ID)
	}
	return current, true, nil
}

func (c *agentTurnAdmissionController) release(agentID string) {
	c.mu.Lock()
	if c.active[agentID] <= 1 {
		delete(c.active, agentID)
	} else {
		c.active[agentID]--
	}
	c.notifyLocked()
	c.mu.Unlock()
}

func (c *agentTurnAdmissionController) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func inheritAgentTurnAdmissions(dst context.Context, src context.Context) (context.Context, func()) {
	admissions, ok := src.Value(agentTurnAdmissionsKey{}).(map[string]*agentTurnAdmissionLease)
	if !ok || len(admissions) == 0 {
		return dst, func() {}
	}

	inherited := make(map[string]*agentTurnAdmissionLease, len(admissions))
	retained := make([]*agentTurnAdmissionLease, 0, len(admissions))
	for agentID, lease := range admissions {
		if lease == nil || !lease.retain() {
			continue
		}
		inherited[agentID] = lease
		retained = append(retained, lease)
	}
	if len(inherited) == 0 {
		return dst, func() {}
	}
	return context.WithValue(dst, agentTurnAdmissionsKey{}, inherited), func() {
		for _, lease := range retained {
			lease.releaseRef()
		}
	}
}

func cloneAgentTurnAdmissions(ctx context.Context) map[string]*agentTurnAdmissionLease {
	cloned := make(map[string]*agentTurnAdmissionLease)
	if ctx == nil {
		return cloned
	}
	if inherited, ok := ctx.Value(agentTurnAdmissionsKey{}).(map[string]*agentTurnAdmissionLease); ok {
		for agentID, lease := range inherited {
			cloned[agentID] = lease
		}
	}
	return cloned
}
