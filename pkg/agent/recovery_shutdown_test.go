package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

type recoveryShutdownSink struct {
	recordingProtectedAnswerSink
	started     chan context.Context
	release     chan struct{}
	closed      chan struct{}
	releaseOnce sync.Once
	closeOnce   sync.Once
}

func (sink *recoveryShutdownSink) Discard(
	ctx context.Context, request interactions.ProtectedAnswerDiscardRequest,
) error {
	sink.started <- ctx
	<-sink.release
	return sink.recordingProtectedAnswerSink.Discard(ctx, request)
}

func (sink *recoveryShutdownSink) Close() { sink.closeOnce.Do(func() { close(sink.closed) }) }

func (sink *recoveryShutdownSink) unblock() { sink.releaseOnce.Do(func() { close(sink.release) }) }

func TestCloseContextDrainsProtectedRecoveryBeforeClosingSink(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &protectedInteractionContinuationTestProvider{})
	al := fixture.Loop
	sink := &recoveryShutdownSink{
		started: make(chan context.Context, 1), release: make(chan struct{}), closed: make(chan struct{}),
	}
	if err := al.interactions.registerProtectedAnswerSink(sink); err != nil {
		t.Fatal(err)
	}
	msg := testInboundMessage(bus.InboundMessage{
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:recovery-close"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
	})
	prepareWaitingProtectedInteraction(t, al, fixture.Agent, msg)
	recoveryDone := make(chan struct{})
	go func() {
		defer close(recoveryDone)
		al.RecoverHumanInteractions(t.Context())
	}()
	t.Cleanup(func() {
		sink.unblock()
		<-recoveryDone
	})
	var recoveryCtx context.Context
	select {
	case recoveryCtx = <-sink.started:
	case <-time.After(2 * time.Second):
		t.Fatal("native protected recovery did not reach its sink")
	}
	closeCtx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	err := al.CloseContext(closeCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("CloseContext returned before protected recovery drained: %v", err)
	}
	select {
	case <-sink.closed:
		t.Error("protected sink closed while its recovery operation was active")
	default:
	}
	if recoveryCtx.Err() == nil {
		t.Error("runtime close did not cancel its admitted recovery context")
	}
	sink.unblock()
	<-recoveryDone
	retryCtx, retryCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer retryCancel()
	if err = al.CloseContext(retryCtx); err != nil {
		t.Fatal("CloseContext retry did not finish drained teardown", err)
	}
	select {
	case <-sink.closed:
	default:
		t.Fatal("drained native protected sink was not closed")
	}
}

func TestClosedLoopRefusesLateInteractionRecoveryBeforeRegistryMutation(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &protectedInteractionContinuationTestProvider{})
	al := fixture.Loop
	msg := testInboundMessage(bus.InboundMessage{
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:late-recovery-close"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
	})
	record, _ := prepareWaitingProtectedInteraction(t, al, fixture.Agent, msg)
	registry := al.interactionRegistryForWorkspace(fixture.Agent.Workspace)
	if err := al.CloseContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := al.recoverHumanInteractionsAt(t.Context(), time.Now().Add(2*time.Hour)); got != 0 {
		t.Error("closed loop admitted late interaction recovery", got)
	}
	after, ok := registry.Get(record.ID)
	if !ok || after.Revision != record.Revision || after.Status != record.Status {
		t.Fatal("late recovery changed native registry state after teardown")
	}
}

func TestCloseContextDrainsUnansweredSessionRecovery(t *testing.T) {
	provider := newRunLifecycleBlockingProvider()
	fixture := newAgentLoopTestFixture(t, provider)
	al := fixture.Loop
	key := session.BuildOpaqueSessionKey("recovery|agent=main|channel=telegram|chat=-100123|topic=42")
	metadata, ok := fixture.Agent.Sessions.(session.MetadataAwareSessionStore)
	if !ok {
		t.Fatal("native session store does not support recovery metadata")
	}
	metadata.EnsureSessionMetadata(key, &session.SessionScope{
		Version: session.ScopeVersion, AgentID: fixture.Agent.ID, Channel: "telegram",
		Dimensions: []string{"chat"}, Values: map[string]string{"chat": "group:-100123/42"},
	})
	fixture.Agent.Sessions.AddFullMessage(key, providers.Message{Role: "user", Content: "continue this test task"})
	recoveryDone := make(chan struct{})
	go func() {
		defer close(recoveryDone)
		al.RecoverUnansweredSessions(t.Context())
	}()
	t.Cleanup(func() {
		provider.unblock()
		<-recoveryDone
	})
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("native unanswered recovery did not reach its provider")
	}
	closeCtx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	err := al.CloseContext(closeCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("CloseContext returned before unanswered recovery drained: %v", err)
	}
	provider.unblock()
	<-recoveryDone
	retryCtx, retryCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer retryCancel()
	if err = al.CloseContext(retryCtx); err != nil {
		t.Fatal("CloseContext retry did not drain unanswered recovery", err)
	}
	if got := al.RecoverUnansweredSessions(t.Context()); got != 0 {
		t.Fatal("closed loop admitted late unanswered recovery", got)
	}
}

func TestRecoveryEnrollmentIsSealedBeforeShutdownWait(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &simpleMockProvider{response: "unused"})
	al := fixture.Loop
	workCtx, finish, admitted := al.beginRecovery(t.Context())
	if !admitted {
		t.Fatal("open runtime refused recovery enrollment")
	}
	t.Cleanup(finish)
	closeCtx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	err := al.CloseContext(closeCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || workCtx.Err() == nil {
		t.Fatal("shutdown did not wait for and cancel admitted recovery", err)
	}
	if _, lateFinish, late := al.beginRecovery(t.Context()); late {
		lateFinish()
		t.Fatal("shutdown wait admitted new recovery work")
	}
	finish()
	finish()
	if err = al.CloseContext(t.Context()); err != nil {
		t.Fatal("idempotent recovery retirement did not release shutdown", err)
	}
}
