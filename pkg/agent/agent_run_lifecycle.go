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

// Run and recovery must retire before their shared stores are closed.
// Enrollment and shutdown sealing use the same lock, preventing late work.
type agentLoopRunLifecycle struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	closing bool

	recoveryWorkers sync.WaitGroup
	recoveryContext context.Context
	recoveryCancel  context.CancelFunc
	recoveryDone    chan struct{}
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

func (al *AgentLoop) beginRecovery(parent context.Context) (context.Context, func(), bool) {
	if parent == nil {
		parent = context.Background()
	}
	lifecycle := &al.runLife
	lifecycle.mu.Lock()
	if lifecycle.closing || parent.Err() != nil {
		lifecycle.mu.Unlock()
		return nil, nil, false
	}
	if lifecycle.recoveryContext == nil {
		closingCtx, cancel := context.WithCancel(context.Background())
		lifecycle.recoveryContext, lifecycle.recoveryCancel = closingCtx, cancel
	}
	closing := lifecycle.recoveryContext
	lifecycle.recoveryWorkers.Add(1)
	lifecycle.mu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(closing, cancel)
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			stop()
			cancel()
			lifecycle.recoveryWorkers.Done()
		})
	}, true
}

func (al *AgentLoop) stopAndWaitRun(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	al.runLife.mu.Lock()
	al.runLife.closing = true
	cancel := al.runLife.cancel
	done := al.runLife.done
	cancelRecovery := al.runLife.recoveryCancel
	if al.runLife.recoveryDone == nil {
		al.runLife.recoveryDone = make(chan struct{})
		go func(drained chan struct{}) {
			al.runLife.recoveryWorkers.Wait()
			close(drained)
		}(al.runLife.recoveryDone)
	}
	recoveryDone := al.runLife.recoveryDone
	al.runLife.mu.Unlock()
	al.running.Store(false)
	if cancel != nil {
		cancel()
	}
	if cancelRecovery != nil {
		cancelRecovery()
	}
	for _, drained := range []chan struct{}{done, recoveryDone} {
		if drained == nil {
			continue
		}
		select {
		case <-drained:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
