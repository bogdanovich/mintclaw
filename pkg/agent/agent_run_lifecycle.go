package agent

import (
	"context"
	"errors"
	"sync"
)

var (
	errAgentLoopAlreadyRunning = errors.New("agent loop is already running")
	errAgentLoopClosing        = errors.New("agent loop is closing")
)

// agentLoopRunLifecycle serializes Run ownership with resource teardown.
// CloseContext seals the lifecycle before it waits, so a late Run cannot start
// against stores that are already being closed.
type agentLoopRunLifecycle struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	closing bool
}

func (al *AgentLoop) beginRun(parent context.Context) (context.Context, chan struct{}, error) {
	al.runLife.mu.Lock()
	defer al.runLife.mu.Unlock()
	if al.runLife.closing {
		return nil, nil, errAgentLoopClosing
	}
	if al.runLife.done != nil {
		return nil, nil, errAgentLoopAlreadyRunning
	}
	runCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	al.runLife.cancel = cancel
	al.runLife.done = done
	return runCtx, done, nil
}

func (al *AgentLoop) finishRun(done chan struct{}) {
	al.runLife.mu.Lock()
	defer al.runLife.mu.Unlock()
	if al.runLife.done != done {
		return
	}
	al.runLife.cancel = nil
	al.runLife.done = nil
	close(done)
}

func (al *AgentLoop) stopRun() {
	al.running.Store(false)
	al.runLife.mu.Lock()
	cancel := al.runLife.cancel
	al.runLife.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (al *AgentLoop) stopAndWaitRun(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	al.runLife.mu.Lock()
	al.runLife.closing = true
	cancel := al.runLife.cancel
	done := al.runLife.done
	al.runLife.mu.Unlock()
	al.running.Store(false)
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
