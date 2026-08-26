package agent

import (
	"context"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/logger"
)

type backgroundCompactionRunner struct {
	contextManager func() ContextManager
	running        sync.Map
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.Mutex
	closed         bool
	workers        sync.WaitGroup
}

func newBackgroundCompactionRunner(contextManager func() ContextManager) *backgroundCompactionRunner {
	ctx, cancel := context.WithCancel(context.Background())
	return &backgroundCompactionRunner{contextManager: contextManager, ctx: ctx, cancel: cancel}
}

func (r *backgroundCompactionRunner) scheduleBackgroundCompaction(
	agent *AgentInstance,
	sessionKey string,
	reason ContextCompressReason,
	budget int,
	messageKind string,
) {
	contextManager := r.currentContextManager()
	if contextManager == nil || agent == nil || sessionKey == "" {
		return
	}
	key := agent.ID + ":" + sessionKey
	if threadID := agent.CodingLayout.ThreadID(); threadID != "" {
		key = "coding:" + threadID
	}
	if _, loaded := r.running.LoadOrStore(key, struct{}{}); loaded {
		logger.DebugCF("agent", "Background context compaction already running", map[string]any{
			"agent_id":     agent.ID,
			"session_key":  sessionKey,
			"reason":       reason,
			"message_kind": messageKind,
		})
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.running.Delete(key)
		return
	}
	r.workers.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.workers.Done()
		defer r.running.Delete(key)

		compactCtx, cancel := context.WithTimeout(r.ctx, 2*time.Minute)
		defer cancel()

		startedAt := time.Now()
		logger.DebugCF("agent", "Background context compaction started", map[string]any{
			"agent_id":     agent.ID,
			"session_key":  sessionKey,
			"reason":       reason,
			"budget":       budget,
			"message_kind": messageKind,
		})
		if err := contextManager.Compact(
			compactCtx,
			&CompactRequest{
				Agent:      agent,
				SessionKey: sessionKey,
				Workspace:  agent.Workspace,
				Reason:     reason,
				Budget:     budget,
			},
		); err != nil {
			logger.WarnCF("agent", "Background context compaction failed", map[string]any{
				"agent_id":     agent.ID,
				"session_key":  sessionKey,
				"reason":       reason,
				"message_kind": messageKind,
				"duration_ms":  time.Since(startedAt).Milliseconds(),
				"error":        err.Error(),
			})
			return
		}
		logger.InfoCF("agent", "Background context compaction completed", map[string]any{
			"agent_id":     agent.ID,
			"session_key":  sessionKey,
			"reason":       reason,
			"message_kind": messageKind,
			"duration_ms":  time.Since(startedAt).Milliseconds(),
		})
	}()
}

// Close cancels all derived background compaction and waits for workers to
// release the context manager before its dependencies are closed.
func (r *backgroundCompactionRunner) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		r.cancel()
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *backgroundCompactionRunner) currentContextManager() ContextManager {
	if r == nil || r.contextManager == nil {
		return nil
	}
	return r.contextManager()
}
