package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

type finalHandoffAckBus struct {
	*finalResponseAdmissionTestBus
	rootID  string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *finalHandoffAckBus) AckInbound(ctx context.Context, msg bus.InboundMessage) error {
	if msg.SpoolID == b.rootID {
		b.once.Do(func() { close(b.entered) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.finalResponseAdmissionTestBus.AckInbound(ctx, msg)
}

func TestInboundFinalHandoffDoesNotStrandFollowupAfterLastDrain(t *testing.T) {
	t.Run("admitted before route retirement", func(t *testing.T) { testFinalHandoffFollowup(t, false) })
	t.Run("prepared across route retirement", func(t *testing.T) { testFinalHandoffFollowup(t, true) })
}

func testFinalHandoffFollowup(t *testing.T, retired bool) {
	t.Helper()
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{Content: "Root complete", FinishReason: "stop"},
		{Content: "Follow-up complete", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	tracker := &finalHandoffAckBus{
		finalResponseAdmissionTestBus: &finalResponseAdmissionTestBus{MessageBus: al.bus.(*bus.MessageBus)},
		rootID:                        "root-final-handoff", entered: make(chan struct{}), release: make(chan struct{}),
	}
	setTestMessageBus(al, tracker)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(tracker.release) }) }
	defer release()
	root := testInboundMessage(bus.InboundMessage{
		Content: "Start a normal turn", SpoolID: tracker.rootID,
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:final-handoff"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-handoff", ChatType: "direct", SenderID: "owner-handoff",
		},
	})
	target, ok := al.resolveSteeringTarget(root)
	if !ok {
		t.Fatal("root route unavailable")
	}
	coordinator := newInboundTurnCoordinator(al)
	claim, target, claimed := coordinator.claimSession(target)
	if !claimed {
		t.Fatal("root route could not be claimed")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		coordinator.runWorker(t.Context(), root, target, claim)
	}()
	select {
	case <-tracker.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("root did not reach acknowledgement after its last steering drain")
	}
	followup := root
	followup.Content = "Now perform the separate follow-up"
	followup.SpoolID = "followup-final-handoff"
	followup.Context.MessageID = followup.SpoolID
	if retired {
		release()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("root worker did not retire")
		}
		// The ingress target was resolved before retirement; preparation can
		// finish afterward. Admission must retry ownership instead of queueing
		// behind an already retired worker.
		coordinator.handleBusySession(t.Context(), followup, target)
	} else {
		coordinator.handleInbound(t.Context(), followup)
		release()
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("root worker did not release its route")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		provider.mu.Lock()
		calls := provider.callCount
		provider.mu.Unlock()
		acked, released, _ := tracker.ownership()
		if calls == 2 && countMatchingStrings(acked, followup.SpoolID) == 1 {
			if countMatchingStrings(released, followup.SpoolID) != 0 {
				t.Fatal("follow-up was also released after acknowledgement")
			}
			coordinator.wait()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("follow-up stranded: calls=%d queue=%d acked=%v released=%v", calls,
				al.pendingSteeringCountForScope(newRuntimeSessionScope(agent.Workspace, target.SessionKey)),
				acked, released)
		}
		time.Sleep(time.Millisecond)
	}
}

type finalHandoffStopManager struct {
	*interactionChannelManager
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *finalHandoffStopManager) InvokeTypingStop(_, _ string) {
	m.once.Do(func() { close(m.entered) })
	<-m.release
}

func TestResolvedInteractionFinalHandoffStartsSeparateFollowup(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{Content: "Question complete", FinishReason: "stop"},
		{Content: "Separate follow-up complete", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := &finalHandoffStopManager{
		interactionChannelManager: newInteractionChannelManager(),
		entered:                   make(chan struct{}), release: make(chan struct{}),
	}
	installInteractionChannelManager(t, al, manager.interactionChannelManager)
	al.channelManager = manager
	tracker := &finalResponseAdmissionTestBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(manager.release) }) }
	defer release()
	root := testInboundMessage(bus.InboundMessage{
		Content:    "ask a question",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:resolved-handoff"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "resolved-chat", ChatType: "direct", SenderID: "resolved-owner",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, root, "")
	answer := root
	answer.Content = "/answer " + record.ShortID + " continue"
	answer.SpoolID = "answered-before-handoff"
	answer.Context.MessageID = answer.SpoolID
	coordinator := newInboundTurnCoordinator(al)
	claim, target, claimed := coordinator.claimSession(target)
	if !claimed {
		t.Fatal("interaction route unavailable")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		coordinator.runInteractionWorker(t.Context(), answer, target, claim)
	}()
	select {
	case <-manager.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("interaction did not reach final worker teardown")
	}
	current, ok := al.interactionRegistryForWorkspace(agent.Workspace).Get(record.ID)
	if !ok || current.Status != interactions.StatusResolved || len(current.FinalDeliveryIDs) == 0 {
		t.Fatal("worker was blocked before durable interaction resolution")
	}
	followup := root
	followup.Content = "Perform a separate task after that review"
	followup.SpoolID = "separate-after-resolution"
	followup.Context.MessageID = followup.SpoolID
	coordinator.handleInbound(t.Context(), followup)
	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("resolved interaction worker did not drain and retire")
	}
	provider.mu.Lock()
	calls := provider.callCount
	provider.mu.Unlock()
	acked, released, _ := tracker.ownership()
	if calls != 2 || countMatchingStrings(acked, followup.SpoolID) != 1 || len(released) != 0 {
		t.Fatalf("follow-up settlement: calls=%d acked=%v released=%v", calls, acked, released)
	}
	if depth := al.pendingSteeringCountForScope(target.runtimeSessionScope()); depth != 0 {
		t.Fatalf("resolved interaction left %d queued messages", depth)
	}
}

func TestRetiredRouteClaimCannotReleaseReplacementAtSameScope(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "test route ownership",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:replacement-owner"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "replacement-chat", ChatType: "direct", SenderID: "replacement-owner",
		},
	})
	target, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("route unavailable")
	}
	first, _, claimed := al.turns.claimRuntimeRouteSession(target, "pending-first-owner")
	if !claimed {
		t.Fatal("first owner unavailable")
	}
	first.releaseIfOwned()
	second, secondTarget, claimed := al.turns.claimRuntimeRouteSession(target, "pending-second-owner")
	if !claimed {
		t.Fatal("replacement owner unavailable")
	}
	defer second.releaseIfOwned()
	first.releaseIfOwned()
	actual, exists := al.turns.activeRouteSessions.Load(second.routeScope)
	if !exists || actual != second || second.routeTarget != secondTarget {
		t.Fatal("retired claim removed the replacement owner")
	}
}

type finalHandoffPreparationKey struct{}

type finalHandoffPreparationStore struct {
	session.SessionStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (store *finalHandoffPreparationStore) ReadTurnHistory(
	ctx context.Context,
	key string,
) ([]providers.Message, error) {
	if ctx.Value(finalHandoffPreparationKey{}) == true {
		store.once.Do(func() { close(store.entered) })
		select {
		case <-store.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return store.SessionStore.ReadTurnHistory(ctx, key)
}

func TestDeferredInteractionPreparationAcrossRetirementStartsFollowup(t *testing.T) {
	provider := newBlockingInteractionProvider()
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tracker := &finalResponseAdmissionTestBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)
	root := testInboundMessage(bus.InboundMessage{
		Content:    "ask a question",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:deferred-preparation-handoff"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "deferred-chat", ChatType: "direct", SenderID: "deferred-owner",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, root, "")
	store := &finalHandoffPreparationStore{
		SessionStore: agent.Sessions, entered: make(chan struct{}), release: make(chan struct{}),
	}
	agent.Sessions = store
	var releasePreparation, releaseProvider sync.Once
	defer releasePreparation.Do(func() { close(store.release) })
	defer releaseProvider.Do(func() { close(provider.release) })
	answer := root
	answer.Content = "/answer " + record.ShortID + " continue"
	answer.SpoolID = "answer-before-deferred-preparation"
	answer.Context.MessageID = answer.SpoolID
	coordinator := newInboundTurnCoordinator(al)
	claim, target, claimed := coordinator.claimSession(target)
	if !claimed {
		t.Fatal("interaction route unavailable")
	}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		coordinator.runInteractionWorker(t.Context(), answer, target, claim)
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("interaction worker did not reach its provider barrier")
	}
	if !al.hasNonterminalInteraction(agent.Workspace, target.SessionKey) {
		t.Fatal("deferred ingress did not begin while the interaction was nonterminal")
	}
	followup := root
	followup.Content = "Perform the next separate task"
	followup.SpoolID = "prepared-after-interaction-retirement"
	followup.Context.MessageID = followup.SpoolID
	admitted := make(chan error, 1)
	go func() {
		ctx := context.WithValue(t.Context(), finalHandoffPreparationKey{}, true)
		admitted <- coordinator.enqueueDeferredInteractionInbound(ctx, followup, target)
	}()
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("deferred input did not reach its preparation barrier")
	}
	releaseProvider.Do(func() { close(provider.release) })
	select {
	case <-workerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("interaction worker did not retire while preparation was blocked")
	}
	current, exists := al.interactionRegistryForWorkspace(agent.Workspace).Get(record.ID)
	if !exists || current.Status != interactions.StatusResolved || len(current.FinalDeliveryIDs) == 0 {
		t.Fatal("interaction was not durably resolved before deferred preparation completed")
	}
	releasePreparation.Do(func() { close(store.release) })
	select {
	case err := <-admitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prepared input was not admitted")
	}
	coordinator.wait()
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	acked, released, _ := tracker.ownership()
	depth := al.pendingSteeringCountForScope(target.runtimeSessionScope())
	if calls != 2 || countMatchingStrings(acked, followup.SpoolID) != 1 || len(released) != 0 || depth != 0 {
		t.Fatalf("deferred follow-up stranded: calls=%d queue=%d acked=%v released=%v", calls, depth, acked, released)
	}
}
