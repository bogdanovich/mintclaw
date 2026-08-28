package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type lifecycleCompactionManager struct {
	started    chan struct{}
	finished   chan struct{}
	calls      atomic.Int32
	background atomic.Bool
}

func (m *lifecycleCompactionManager) Assemble(context.Context, *AssembleRequest) (*AssembleResponse, error) {
	return &AssembleResponse{}, nil
}

func (m *lifecycleCompactionManager) Compact(ctx context.Context, req *CompactRequest) error {
	m.calls.Add(1)
	m.background.Store(req != nil && req.Background)
	select {
	case m.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case m.finished <- struct{}{}:
	default:
	}
	return ctx.Err()
}

func (m *lifecycleCompactionManager) Ingest(context.Context, *IngestRequest) error { return nil }

func (m *lifecycleCompactionManager) Clear(context.Context, *AgentInstance, string) error { return nil }

func TestBackgroundCompactionRunnerDeduplicatesCodingThread(t *testing.T) {
	manager := &lifecycleCompactionManager{started: make(chan struct{}, 2), finished: make(chan struct{}, 2)}
	runner := newBackgroundCompactionRunner(func() ContextManager { return manager })
	agent := &AgentInstance{ID: "main"}
	executionRoot := t.TempDir()
	firstLayout, err := NewCodingRuntimeLayout(
		"thread-1",
		executionRoot,
		t.TempDir(),
		[]string{executionRoot},
	)
	if err != nil {
		t.Fatal(err)
	}
	agent.CodingLayout = firstLayout
	runner.scheduleBackgroundCompaction(agent, "session-a", ContextCompressReasonProactive, 100, "turn")
	select {
	case <-manager.started:
	case <-time.After(time.Second):
		t.Fatal("first compaction did not start")
	}
	if !manager.background.Load() {
		t.Fatal("background compaction request did not carry its execution mode")
	}
	runner.scheduleBackgroundCompaction(agent, "session-b", ContextCompressReasonSummarize, 100, "turn")
	if manager.calls.Load() != 1 {
		t.Fatalf("compaction calls = %d, want one per coding thread", manager.calls.Load())
	}
	if err := runner.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundCompactionRunnerCloseCancelsAndDrains(t *testing.T) {
	manager := &lifecycleCompactionManager{started: make(chan struct{}, 1), finished: make(chan struct{}, 1)}
	runner := newBackgroundCompactionRunner(func() ContextManager { return manager })
	agent := &AgentInstance{ID: "main"}
	runner.scheduleBackgroundCompaction(agent, "session", ContextCompressReasonSummarize, 100, "turn")
	select {
	case <-manager.started:
	case <-time.After(time.Second):
		t.Fatal("compaction did not start")
	}
	if err := runner.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-manager.finished:
	default:
		t.Fatal("Close returned before the compaction worker finished")
	}
	runner.scheduleBackgroundCompaction(agent, "later", ContextCompressReasonSummarize, 100, "turn")
	if manager.calls.Load() != 1 {
		t.Fatalf("closed runner accepted another job: calls=%d", manager.calls.Load())
	}
}
