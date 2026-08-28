// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type finalResponseAdmissionTestBus struct {
	*bus.MessageBus
	publishErr     error
	publishResults []error
	publishCalls   int

	mu           sync.Mutex
	acked        []string
	released     []string
	releaseCause error
	ackErrByID   map[string]error
	publishStart chan struct{}
	publishBlock chan struct{}
	publishOnce  sync.Once
}

type postAcceptBlockingBus struct {
	*bus.MessageBus
	accepted chan struct{}
	release  chan struct{}
}

type coordinatorFeedbackChannel struct {
	fakeChannel
	events chan bus.OutboundMessage
}

func newCoordinatorFeedbackChannel() *coordinatorFeedbackChannel {
	return &coordinatorFeedbackChannel{events: make(chan bus.OutboundMessage, 16)}
}

func (c *coordinatorFeedbackChannel) DeliverText(
	ctx context.Context,
	pending []bus.OutboundMessage,
) channels.DeliveryResult[bus.OutboundMessage] {
	return channels.DeliverSequentially(
		ctx,
		pending,
		func(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
			select {
			case c.events <- msg:
				return []string{"message"}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	)
}

func (c *coordinatorFeedbackChannel) EditMessage(
	ctx context.Context,
	chatID, _ string, content string,
) error {
	select {
	case c.events <- bus.OutboundMessage{ChatID: chatID, Content: content}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func receiveCoordinatorFeedbackChannelMessage(
	t *testing.T,
	channel *coordinatorFeedbackChannel,
	contentSubstring string,
) bus.OutboundMessage {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case msg := <-channel.events:
			if strings.Contains(msg.Content, contentSubstring) {
				return msg
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for channel content containing %q", contentSubstring)
			return bus.OutboundMessage{}
		}
	}
}

func (b *postAcceptBlockingBus) PublishOutbound(ctx context.Context, msg bus.OutboundMessage) error {
	if err := b.MessageBus.PublishOutbound(ctx, msg); err != nil {
		return err
	}
	close(b.accepted)
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type failingRootTurnJournal struct {
	session.SessionStore
	err error
}

func (s *failingRootTurnJournal) AppendTurnMessage(
	_ context.Context,
	_ string,
	msg providers.Message,
) error {
	if msg.Role == "user" {
		return s.err
	}
	return errors.New("unexpected non-root journal append")
}

type countingAdmissionProvider struct {
	calls int
}

func (p *countingAdmissionProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	p.calls++
	return &providers.LLMResponse{Content: "must not run"}, nil
}

func (p *countingAdmissionProvider) GetDefaultModel() string { return "counting" }

func (b *finalResponseAdmissionTestBus) PublishOutbound(
	ctx context.Context,
	msg bus.OutboundMessage,
) error {
	if b.publishStart != nil {
		b.publishOnce.Do(func() { close(b.publishStart) })
	}
	if b.publishBlock != nil {
		select {
		case <-b.publishBlock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.mu.Lock()
	if b.publishCalls < len(b.publishResults) {
		err := b.publishResults[b.publishCalls]
		b.publishCalls++
		b.mu.Unlock()
		if err != nil {
			return err
		}
		return b.MessageBus.PublishOutbound(ctx, msg)
	}
	b.mu.Unlock()
	if b.publishErr != nil {
		return b.publishErr
	}
	return b.MessageBus.PublishOutbound(ctx, msg)
}

func TestOutboundTransactionRejectsDuplicateWhilePublicationIsInFlight(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	installTestOutboundCoordinator(t, al, t.TempDir())
	trackingBus := &finalResponseAdmissionTestBus{
		MessageBus:   msgBus,
		publishStart: make(chan struct{}),
		publishBlock: make(chan struct{}),
	}
	setTestMessageBus(al, trackingBus)
	agent := al.registry.GetDefaultAgent()
	publish := func(ctx context.Context) finalResponseAdmission {
		return al.publishResponseWithContextIfNeeded(
			ctx,
			agent.Workspace,
			agent.ID,
			"telegram",
			"chat-1",
			"session-1",
			"durable final",
			nil,
			finalResponseAlwaysPublish,
		)
	}
	firstResult := make(chan finalResponseAdmission, 1)
	go func() {
		firstResult <- publish(withOutboundTransaction(t.Context(), "spool-in-flight"))
	}()
	select {
	case <-trackingBus.publishStart:
	case <-time.After(time.Second):
		t.Fatal("first publication did not reach the bus")
	}

	duplicate := publish(withOutboundTransaction(t.Context(), "spool-in-flight"))
	if duplicate.permitsInboundAck() || !errors.Is(duplicate.err, errOutboundPublicationInFlight) {
		t.Fatalf("in-flight duplicate admission = %+v", duplicate)
	}
	close(trackingBus.publishBlock)
	if first := <-firstResult; !first.permitsInboundAck() || first.err != nil {
		t.Fatalf("first admission = %+v", first)
	}

	replay := publish(withOutboundTransaction(t.Context(), "spool-in-flight"))
	if !replay.permitsInboundAck() || replay.err != nil {
		t.Fatalf("committed replay admission = %+v", replay)
	}
	select {
	case <-msgBus.OutboundChan():
	default:
		t.Fatal("first publication was not queued")
	}
	select {
	case duplicateMessage := <-msgBus.OutboundChan():
		t.Fatalf("committed replay published again: %+v", duplicateMessage)
	default:
	}
}

func TestOutboundTransactionConsumerCanFinishBeforePublisherReturns(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	installTestOutboundCoordinator(t, al, t.TempDir())
	trackingBus := &postAcceptBlockingBus{
		MessageBus: msgBus,
		accepted:   make(chan struct{}),
		release:    make(chan struct{}),
	}
	setTestMessageBus(al, trackingBus)
	agent := al.registry.GetDefaultAgent()
	result := make(chan finalResponseAdmission, 1)
	go func() {
		result <- al.publishResponseWithContextIfNeeded(
			withOutboundTransaction(t.Context(), "spool-consumer-first"),
			agent.Workspace,
			agent.ID,
			"telegram",
			"chat-1",
			"session-1",
			"durable final",
			nil,
			finalResponseAlwaysPublish,
		)
	}()

	select {
	case <-trackingBus.accepted:
	case <-time.After(time.Second):
		t.Fatal("publication was not accepted by the bus")
	}
	var outbound bus.OutboundMessage
	select {
	case outbound = <-msgBus.OutboundChan():
	case <-time.After(time.Second):
		t.Fatal("consumer did not receive the durable message")
	}
	coordinator := al.outboundCoordinator()
	if err := coordinator.BeginAttempt(outbound.DeliveryID); err != nil {
		t.Fatalf("BeginAttempt() before publisher return error = %v", err)
	}
	if err := coordinator.MarkDelivered(outbound.DeliveryID, outbox.Outcome{}); err != nil {
		t.Fatalf("MarkDelivered() before publisher return error = %v", err)
	}
	close(trackingBus.release)
	if admission := <-result; !admission.permitsInboundAck() || admission.err != nil {
		t.Fatalf("publisher result = %+v", admission)
	}
	intent, err := coordinator.Get(outbound.DeliveryID)
	if err != nil || intent.Status != outbox.StatusDelivered {
		t.Fatalf("delivered intent = %+v, %v", intent, err)
	}
}

func TestSettleInboundAdmissionReleasesAfterAckFailure(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	ackErr := errors.New("root ack failed")
	trackingBus := &finalResponseAdmissionTestBus{
		MessageBus: msgBus,
		ackErrByID: map[string]error{"spool-root-ack": ackErr},
	}
	setTestMessageBus(al, trackingBus)
	msg := bus.InboundMessage{SpoolID: "spool-root-ack"}
	err := al.settleInboundAdmission(
		t.Context(),
		msg,
		finalResponseAdmission{status: finalResponseAdmissionAccepted},
	)
	if !errors.Is(err, ackErr) {
		t.Fatalf("settleInboundAdmission() error = %v, want %v", err, ackErr)
	}
	_, released, cause := trackingBus.ownership()
	if !containsExactly(released, msg.SpoolID) || !errors.Is(cause, ackErr) {
		t.Fatalf("ack failure release = released:%v cause:%v", released, cause)
	}
}

func TestInteractionNoticeReleasesInboundAfterAckFailure(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	ackErr := errors.New("interaction notice ack failed")
	trackingBus := &finalResponseAdmissionTestBus{
		MessageBus: msgBus,
		ackErrByID: map[string]error{"spool-interaction-ack": ackErr},
	}
	setTestMessageBus(al, trackingBus)
	agent := al.registry.GetDefaultAgent()
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "chat-1",
			SenderID: "user-1",
		},
		SpoolID: "spool-interaction-ack",
		Content: "/answer wrong answer",
	}
	newInboundTurnCoordinator(al).consumeExplicitInteractionAnswer(
		t.Context(),
		msg,
		&inboundDispatchTarget{Agent: agent, SessionKey: "session-1"},
		explicitInteractionAnswer{Disposition: explicitInteractionAnswerWrongID},
	)

	_, released, cause := trackingBus.ownership()
	if !containsExactly(released, msg.SpoolID) || !errors.Is(cause, ackErr) {
		t.Fatalf("interaction ack failure = released:%v cause:%v", released, cause)
	}
}

func (b *finalResponseAdmissionTestBus) AckInbound(
	ctx context.Context,
	msg bus.InboundMessage,
) error {
	b.mu.Lock()
	b.acked = append(b.acked, msg.SpoolID)
	err := b.ackErrByID[msg.SpoolID]
	b.mu.Unlock()
	if err != nil {
		return err
	}
	return b.MessageBus.AckInbound(ctx, msg)
}

func TestOutboundTransactionPersistsBeforePublishAndSuppressesSameProcessReplay(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	root := t.TempDir()
	installTestOutboundCoordinator(t, al, root)
	trackingBus := &finalResponseAdmissionTestBus{MessageBus: msgBus}
	setTestMessageBus(al, trackingBus)
	agent := al.registry.GetDefaultAgent()

	ctx := withOutboundTransaction(t.Context(), "spool-durable-final")
	admission := al.publishResponseWithContextIfNeeded(
		ctx,
		agent.Workspace,
		agent.ID,
		"telegram",
		"chat-1",
		"session-1",
		"durable final",
		&bus.InboundContext{Channel: "telegram", ChatID: "chat-1"},
		finalResponseAlwaysPublish,
	)
	if !admission.permitsInboundAck() || admission.err != nil {
		t.Fatalf("durable admission = %+v", admission)
	}

	var outbound bus.OutboundMessage
	select {
	case outbound = <-msgBus.OutboundChan():
	case <-time.After(time.Second):
		t.Fatal("durable final was not published")
	}
	if outbound.DeliveryID == "" {
		t.Fatal("durable final has no delivery ID")
	}
	store, err := outbox.Open(root)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	intent, err := store.Get(outbound.DeliveryID)
	if err != nil || intent.Message == nil || intent.Message.Content != "durable final" {
		t.Fatalf("persisted intent = %+v, %v", intent, err)
	}

	replay := al.publishResponseWithContextIfNeeded(
		withOutboundTransaction(t.Context(), "spool-durable-final"),
		agent.Workspace,
		agent.ID,
		"telegram",
		"chat-1",
		"rotated-session",
		"changed replay payload",
		&bus.InboundContext{Channel: "telegram", ChatID: "chat-1"},
		finalResponseAlwaysPublish,
	)
	if !replay.permitsInboundAck() || replay.err != nil {
		t.Fatalf("replay admission = %+v", replay)
	}
	select {
	case duplicate := <-msgBus.OutboundChan():
		t.Fatalf("same-process replay published duplicate: %+v", duplicate)
	default:
	}
}

func TestBoundOutboundTransactionBindsIdentityBeforeIntentCreation(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	root := t.TempDir()
	installTestOutboundCoordinator(t, al, root)
	coordinator := al.outboundCoordinator()
	agent := al.registry.GetDefaultAgent()
	var boundID string
	ctx := withBoundOutboundTransaction(
		t.Context(),
		"interaction:bind-before-create:final",
		func(deliveryID string) error {
			boundID = deliveryID
			if _, err := coordinator.Get(deliveryID); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("intent existed before binding: %v", err)
			}
			return nil
		},
		nil,
	)
	published, err := al.publishTransactionMessage(ctx, agent.Workspace, bus.OutboundMessage{
		Channel: "telegram", ChatID: "chat-1", SessionKey: "session-1", Content: "bound final",
	})
	if err != nil || !published || boundID == "" {
		t.Fatalf("publication = (%t, %v), bound ID = %q", published, err, boundID)
	}
	intent, err := coordinator.Get(boundID)
	if err != nil || intent.Status != outbox.StatusPending {
		t.Fatalf("intent after binding = (%+v, %v)", intent, err)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.DeliveryID != boundID {
			t.Fatalf("outbound delivery ID = %q, want %q", outbound.DeliveryID, boundID)
		}
	case <-time.After(time.Second):
		t.Fatal("bound final was not published")
	}
}

func TestBoundOutboundTransactionDoesNotCreateIntentWhenBindingFails(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	root := t.TempDir()
	installTestOutboundCoordinator(t, al, root)
	coordinator := al.outboundCoordinator()
	agent := al.registry.GetDefaultAgent()
	bindErr := errors.New("persist interaction binding")
	var boundID string
	ctx := withBoundOutboundTransaction(
		t.Context(),
		"interaction:binding-failure:final",
		func(deliveryID string) error {
			boundID = deliveryID
			return bindErr
		},
		nil,
	)
	published, err := al.publishTransactionMessage(ctx, agent.Workspace, bus.OutboundMessage{
		Channel: "telegram", ChatID: "chat-1", SessionKey: "session-1", Content: "must not publish",
	})
	if published || !errors.Is(err, bindErr) || boundID == "" {
		t.Fatalf("publication = (%t, %v), bound ID = %q", published, err, boundID)
	}
	if _, err = coordinator.Get(boundID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("intent created after failed binding: %v", err)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("failed binding published outbound: %+v", outbound)
	default:
	}
}

func TestBoundOutboundTransactionAbandonsStaleIntentBeforePublication(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	root := t.TempDir()
	installTestOutboundCoordinator(t, al, root)
	coordinator := al.outboundCoordinator()
	agent := al.registry.GetDefaultAgent()
	staleErr := errors.New("interaction final is stale")
	var deliveryID string
	ctx := withBoundOutboundTransaction(
		t.Context(),
		"interaction:stale-final:final",
		func(id string) error {
			deliveryID = id
			return nil
		},
		func(id string) error {
			if _, err := coordinator.Abandon(id, outbox.Outcome{Error: staleErr.Error()}); err != nil {
				return err
			}
			return staleErr
		},
	)
	published, err := al.publishTransactionMessage(ctx, agent.Workspace, bus.OutboundMessage{
		Channel: "telegram", ChatID: "chat-1", SessionKey: "session-1", Content: "stale final",
	})
	if published || !errors.Is(err, staleErr) {
		t.Fatalf("publication = (%t, %v), want stale rejection", published, err)
	}
	intent, err := coordinator.Get(deliveryID)
	if err != nil || intent.Status != outbox.StatusAbandoned {
		t.Fatalf("stale intent = (%+v, %v)", intent, err)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("stale intent reached bus: %+v", outbound)
	default:
	}
}

func TestOutboundTransactionMediaCommitRunsInsideAdmissionBoundary(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	root := t.TempDir()
	installTestOutboundCoordinator(t, al, root)
	agent := al.registry.GetDefaultAgent()
	message := bus.OutboundMediaMessage{
		Channel: "telegram", ChatID: "chat-1", SessionKey: "session-1",
		Parts: []bus.MediaPart{{Type: "image", Ref: "media://screenshot"}},
	}
	identity := outbox.Identity{
		SourceID: "spool-browser-screenshot", Ordinal: 0, Kind: outbox.KindMedia,
		Channel: message.Channel, ChatID: message.ChatID, SessionKey: message.SessionKey,
	}
	deliveryID, err := outbox.DeliveryID(identity)
	if err != nil {
		t.Fatal(err)
	}
	store, err := outbox.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	commitCalls := 0
	commit := func(context.Context) error {
		commitCalls++
		intent, getErr := store.Get(deliveryID)
		if getErr != nil || intent.Status != outbox.StatusPending {
			t.Fatalf("intent at media commit = %+v, %v", intent, getErr)
		}
		select {
		case outbound := <-msgBus.OutboundMediaChan():
			t.Fatalf("media published before artifact claim: %+v", outbound)
		default:
		}
		return nil
	}
	published, err := al.publishTransactionMediaAtBoundary(
		withOutboundTransaction(t.Context(), identity.SourceID), agent.Workspace, message, commit,
	)
	if err != nil || !published || commitCalls != 1 {
		t.Fatalf("first publication = (%t, %v), commit calls = %d", published, err, commitCalls)
	}
	select {
	case <-msgBus.OutboundMediaChan():
	case <-time.After(time.Second):
		t.Fatal("admitted media was not published")
	}

	replayCommits := 0
	published, err = al.publishTransactionMediaAtBoundary(
		withOutboundTransaction(t.Context(), identity.SourceID), agent.Workspace, message,
		func(context.Context) error {
			replayCommits++
			return nil
		},
	)
	if err != nil || published || replayCommits != 1 {
		t.Fatalf("replay = (%t, %v), commit calls = %d", published, err, replayCommits)
	}
	select {
	case duplicate := <-msgBus.OutboundMediaChan():
		t.Fatalf("replay published duplicate: %+v", duplicate)
	default:
	}

	failedIdentity := "spool-browser-claim-failure"
	claimErr := errors.New("claim screenshot delivery")
	published, err = al.publishTransactionMediaAtBoundary(
		withOutboundTransaction(t.Context(), failedIdentity), agent.Workspace, message,
		func(context.Context) error { return claimErr },
	)
	if published || !errors.Is(err, claimErr) {
		t.Fatalf("failed claim publication = (%t, %v)", published, err)
	}
	published, err = al.publishTransactionMediaAtBoundary(
		withOutboundTransaction(t.Context(), failedIdentity), agent.Workspace, message,
		func(context.Context) error { return nil },
	)
	if err != nil || !published {
		t.Fatalf("publication after released claim = (%t, %v)", published, err)
	}
	select {
	case <-msgBus.OutboundMediaChan():
	case <-time.After(time.Second):
		t.Fatal("media was not published after failed claim admission was released")
	}

	crashIdentity := "spool-browser-claimed-before-publish"
	crashCtx := withOutboundTransaction(t.Context(), crashIdentity)
	admission, err := al.admitDurableMedia(crashCtx, agent.Workspace, message)
	if err != nil || !admission.durable || !admission.dispatch {
		t.Fatalf("pre-crash admission = %+v, %v", admission, err)
	}
	artifactClaimed := true
	oldCoordinator := al.outboundCoordinator()
	if closeErr := oldCoordinator.Close(); closeErr != nil {
		t.Fatalf("close pre-crash coordinator: %v", closeErr)
	}
	recoveredCoordinator, err := outbox.OpenCoordinator(root)
	if err != nil {
		t.Fatalf("reopen coordinator after simulated crash: %v", err)
	}
	al.SetOutboundOutbox(recoveredCoordinator)
	recoveryClaims := 0
	published, err = al.publishTransactionMediaAtBoundary(
		withOutboundTransaction(t.Context(), crashIdentity), agent.Workspace, message,
		func(context.Context) error {
			if !artifactClaimed {
				t.Fatal("artifact claim was not retained across simulated crash")
			}
			recoveryClaims++
			return nil
		},
	)
	if err != nil || !published || recoveryClaims != 1 {
		t.Fatalf("post-crash recovery = (%t, %v), claims = %d", published, err, recoveryClaims)
	}
	select {
	case <-msgBus.OutboundMediaChan():
	case <-time.After(time.Second):
		t.Fatal("pending durable media was not published after simulated crash")
	}
}

func TestImplicitImmediateMediaUsesDurableCommitBoundary(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	root := t.TempDir()
	installTestOutboundCoordinator(t, al, root)
	agent := al.registry.GetDefaultAgent()
	const (
		sourceID   = "spool-immediate-media"
		mediaRef   = "media://immediate-media"
		sessionKey = "session-immediate-media"
	)
	ts := &turnState{
		agent: agent, agentID: agent.ID, workspace: agent.Workspace,
		channel: "telegram", chatID: "chat-1", sessionKey: sessionKey,
		opts: turnSpec{Dispatch: DispatchRequest{SessionKey: sessionKey}},
	}
	identity := outbox.Identity{
		SourceID: sourceID, Ordinal: 0, Kind: outbox.KindMedia,
		Channel: ts.channel, ChatID: ts.chatID, SessionKey: sessionKey,
	}
	deliveryID, err := outbox.DeliveryID(identity)
	if err != nil {
		t.Fatal(err)
	}
	store, err := outbox.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	commitCalls := 0
	result := (&toolshared.ToolResult{Media: []string{mediaRef}}).
		WithDeliverable(&taskresult.Deliverable{Artifacts: []taskresult.Artifact{{
			Ref: mediaRef, Kind: "image",
		}}}).
		WithOutboundCommit(func(context.Context) error {
			commitCalls++
			intent, getErr := store.Get(deliveryID)
			if getErr != nil || intent.Status != outbox.StatusPending {
				t.Fatalf("intent at immediate commit = %+v, %v", intent, getErr)
			}
			select {
			case outbound := <-msgBus.OutboundMediaChan():
				t.Fatalf("immediate media published before journal commit: %+v", outbound)
			default:
			}
			return nil
		}).
		WithDeliveryIntent(toolshared.DeliveryImmediateContinue)

	_, outcome, err := al.deliverToolResultToUser(
		withOutboundTransaction(t.Context(), sourceID), ts, result, "image_generate",
	)
	if err != nil || outcome != toolResultDeliveryQueued || commitCalls != 1 {
		t.Fatalf("delivery = (%v, %v), commit calls = %d", outcome, err, commitCalls)
	}
	select {
	case outbound := <-msgBus.OutboundMediaChan():
		if outbound.DeliveryID != deliveryID || len(outbound.Parts) != 1 || outbound.Parts[0].Ref != mediaRef {
			t.Fatalf("durable immediate media = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("durable immediate media was not published")
	}
}

func TestOutboundTransactionRetainsChildFailureAfterSuccessfulRootPublish(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	installTestOutboundCoordinator(t, al, t.TempDir())
	rejection := errors.New("child bus admission rejected")
	setTestMessageBus(al, &finalResponseAdmissionTestBus{
		MessageBus:     msgBus,
		publishResults: []error{rejection, nil},
	})
	agent := al.registry.GetDefaultAgent()
	ctx := withOutboundTransaction(t.Context(), "spool-child-failure")

	child := al.publishResponseWithContextIfNeeded(
		ctx,
		agent.Workspace,
		agent.ID,
		"telegram",
		"chat-1",
		"child-session",
		"child final",
		nil,
		finalResponseAlwaysPublish,
	)
	if child.permitsInboundAck() || !errors.Is(child.err, rejection) {
		t.Fatalf("child admission = %+v", child)
	}
	root := al.publishResponseWithContextIfNeeded(
		ctx,
		agent.Workspace,
		agent.ID,
		"telegram",
		"chat-1",
		"root-session",
		"root final",
		nil,
		finalResponseAlwaysPublish,
	)
	root = transactionAdmission(ctx, root)
	if root.permitsInboundAck() || !errors.Is(root.err, rejection) {
		t.Fatalf("root admission after child failure = %+v", root)
	}
}

func TestProcessMessageSyncDurablyPublishesSystemCompletionOnOriginRoute(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	installTestOutboundCoordinator(t, al, t.TempDir())
	agent := al.registry.GetDefaultAgent()
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "system",
			ChatID:   "telegram:chat-1",
			ChatType: "direct",
			SenderID: "subagent:worker",
		},
		Content: "Task 'worker' completed.\n\nResult:\nfinished",
		SpoolID: "spool-system-completion",
	}

	admission := al.processMessageSync(withOutboundTransaction(t.Context(), msg.SpoolID), msg)
	if !admission.permitsInboundAck() || admission.err != nil {
		t.Fatalf("system completion admission = %+v", admission)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Channel != "telegram" || outbound.ChatID != "chat-1" ||
			outbound.SessionKey != session.BuildMainSessionKey(agent.ID) || outbound.DeliveryID == "" {
			t.Fatalf("system completion outbound = %+v", outbound)
		}
	default:
		t.Fatal("system completion did not publish on origin route")
	}
	select {
	case duplicate := <-msgBus.OutboundChan():
		t.Fatalf("system completion published twice: %+v", duplicate)
	default:
	}
}

func TestProcessMessageSyncPreservesSystemOriginContextOnSynthesisError(t *testing.T) {
	providerErr := errors.New("system synthesis failed")
	al, _, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{errors: []error{providerErr}})
	defer cleanup()
	msgBus := al.bus.(*bus.MessageBus)
	installTestOutboundCoordinator(t, al, t.TempDir())
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:          "system",
			ChatID:           "telegram:chat-1",
			ChatType:         "direct",
			TopicID:          "topic-1",
			MessageID:        "message-1",
			ReplyToMessageID: "reply-1",
			SenderID:         "subagent:worker",
		},
		Content: "Task failed",
		SpoolID: "spool-system-error",
	}

	admission := al.processMessageSync(withOutboundTransaction(t.Context(), msg.SpoolID), msg)
	if !admission.permitsInboundAck() || admission.err != nil {
		t.Fatalf("system error admission = %+v", admission)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Channel != "telegram" || outbound.ChatID != "chat-1" ||
			outbound.Context.TopicID != "topic-1" || outbound.Context.MessageID != "message-1" ||
			outbound.Context.ReplyToMessageID != "reply-1" || outbound.DeliveryID == "" {
			t.Fatalf("system error outbound = %+v", outbound)
		}
	default:
		t.Fatal("system synthesis error was not published on origin route")
	}
}

func TestProcessMessageSyncKeepsCancellationRetryable(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{errors: []error{context.Canceled}})
	defer cleanup()
	msgBus := al.bus.(*bus.MessageBus)
	trackingBus := &finalResponseAdmissionTestBus{MessageBus: msgBus}
	setTestMessageBus(al, trackingBus)
	installTestOutboundCoordinator(t, al, t.TempDir())
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:   "system",
			ChatID:    "telegram:chat-1",
			TopicID:   "topic-1",
			MessageID: "message-1",
		},
		Content: "Task canceled",
		SpoolID: "spool-system-canceled",
	}

	ctx := withOutboundTransaction(t.Context(), msg.SpoolID)
	admission := al.processMessageSync(ctx, msg)
	if admission.permitsInboundAck() || !errors.Is(admission.err, context.Canceled) {
		t.Fatalf("system cancellation admission = %+v", admission)
	}
	if err := al.settleInboundAdmission(ctx, msg, admission); !errors.Is(err, context.Canceled) {
		t.Fatalf("settleInboundAdmission() error = %v, want context canceled", err)
	}
	_, released, cause := trackingBus.ownership()
	if !containsExactly(released, msg.SpoolID) || !errors.Is(cause, context.Canceled) {
		t.Fatalf("system cancellation release = released:%v cause:%v", released, cause)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("system cancellation published outbound: %+v", outbound)
	default:
	}
}

func TestSteeringAckFailureRejectsRootSettlement(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	ackErr := errors.New("steering ack failed")
	trackingBus := &finalResponseAdmissionTestBus{
		MessageBus: msgBus,
		ackErrByID: map[string]error{"spool-steering": ackErr},
	}
	setTestMessageBus(al, trackingBus)

	err := al.settleSteeringMessages(
		finalResponseAdmission{status: finalResponseAdmissionAccepted},
		[]providers.Message{{InboundSpoolID: "spool-steering"}},
	)
	if !errors.Is(err, ackErr) {
		t.Fatalf("settleSteeringMessages() error = %v, want %v", err, ackErr)
	}
	_ = al.settleInboundAdmission(
		withOutboundTransaction(t.Context(), "spool-root"),
		bus.InboundMessage{SpoolID: "spool-root"},
		rejectedFinalResponseAdmission(err),
	)
	acked, released, _ := trackingBus.ownership()
	if containsExactly(acked, "spool-root") ||
		!containsExactly(released, "spool-steering", "spool-root") {
		t.Fatalf("ownership after steering ack failure = acked:%v released:%v", acked, released)
	}
}

func (b *finalResponseAdmissionTestBus) ReleaseInbound(
	ctx context.Context,
	msg bus.InboundMessage,
	cause error,
) error {
	b.mu.Lock()
	b.released = append(b.released, msg.SpoolID)
	b.releaseCause = cause
	b.mu.Unlock()
	return b.MessageBus.ReleaseInbound(ctx, msg, cause)
}

func (b *finalResponseAdmissionTestBus) ownership() (acked, released []string, cause error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.acked...), append([]string(nil), b.released...), b.releaseCause
}

func TestFinalResponseAdmissionRejectsClosedBus(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	msgBus.Close()

	admission := al.publishResponseWithMetadataAndScopes(
		t.Context(),
		al.registry.GetDefaultAgent().Workspace,
		"main",
		"telegram",
		"chat-1",
		"session-1",
		"final reply",
		&bus.InboundContext{Channel: "telegram", ChatID: "chat-1"},
		finalResponseAlwaysPublish,
		bus.OutboundMetadata{},
		nil,
	)

	if admission.permitsInboundAck() {
		t.Fatalf("closed bus admission = %+v, want rejection", admission)
	}
	if !errors.Is(admission.err, bus.ErrBusClosed) {
		t.Fatalf("closed bus admission error = %v, want %v", admission.err, bus.ErrBusClosed)
	}
}

func TestInboundTurnCoordinatorAcknowledgesAcceptedFinalResponse(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	trackingBus := &finalResponseAdmissionTestBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, trackingBus)
	msg := finalResponseAdmissionInboundMessage("spool-accepted")

	runFinalResponseAdmissionTurn(t, al, msg)

	acked, released, cause := trackingBus.ownership()
	if len(acked) != 1 || acked[0] != msg.SpoolID || len(released) != 0 || cause != nil {
		t.Fatalf("accepted ownership = acked:%v released:%v cause:%v", acked, released, cause)
	}
	select {
	case outbound := <-trackingBus.OutboundChan():
		if outbound.Content != "Hello! How can I help you today?" {
			t.Fatalf("outbound content = %q", outbound.Content)
		}
	default:
		t.Fatal("accepted final response was not queued")
	}
}

func TestInboundTurnCoordinatorReleasesRootJournalFailuresBeforeLLM(t *testing.T) {
	for _, stage := range []string{"append", "flush", "rename", "fsync"} {
		t.Run(stage, func(t *testing.T) {
			provider := &countingAdmissionProvider{}
			al, agent, cleanup := newTurnCoordTestLoop(t, provider)
			defer cleanup()
			events, closeEvents := subscribeRuntimeEventsForTest(
				t, al, 8, runtimeevents.KindAgentTurnEnd,
			)
			defer closeEvents()
			journalErr := errors.New("injected " + stage + " failure")
			agent.Sessions = &failingRootTurnJournal{
				SessionStore: session.NewMemoryStore(),
				err:          journalErr,
			}
			trackingBus := &finalResponseAdmissionTestBus{MessageBus: al.bus.(*bus.MessageBus)}
			setTestMessageBus(al, trackingBus)
			msg := finalResponseAdmissionInboundMessage("spool-root-" + stage)

			runFinalResponseAdmissionTurn(t, al, msg)

			acked, released, cause := trackingBus.ownership()
			if len(acked) != 0 || len(released) != 1 || released[0] != msg.SpoolID {
				t.Fatalf("journal failure ownership = acked:%v released:%v", acked, released)
			}
			if !errors.Is(cause, journalErr) {
				t.Fatalf("release cause = %v, want %v", cause, journalErr)
			}
			if provider.calls != 0 {
				t.Fatalf("failure executed provider %d times", provider.calls)
			}
			event := waitForRuntimeEvent(t, events, time.Second, func(event runtimeevents.Event) bool {
				return event.Kind == runtimeevents.KindAgentTurnEnd
			})
			payload, ok := event.Payload.(TurnEndPayload)
			if !ok || payload.Status != TurnEndStatusError {
				t.Fatalf("turn end = %#v, want error", event.Payload)
			}
		})
	}
}

func TestInboundTurnCoordinatorReleasesRejectedFinalResponse(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "queue rejection", err: errors.New("outbound queue rejected")},
		{name: "canceled admission", err: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			al, _, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
			defer cleanup()
			trackingBus := &finalResponseAdmissionTestBus{
				MessageBus: al.bus.(*bus.MessageBus),
				publishErr: tt.err,
			}
			setTestMessageBus(al, trackingBus)
			msg := finalResponseAdmissionInboundMessage("spool-rejected")

			runFinalResponseAdmissionTurn(t, al, msg)

			acked, released, cause := trackingBus.ownership()
			if len(acked) != 0 || len(released) != 1 || released[0] != msg.SpoolID {
				t.Fatalf("rejected ownership = acked:%v released:%v", acked, released)
			}
			if !errors.Is(cause, tt.err) {
				t.Fatalf("release cause = %v, want %v", cause, tt.err)
			}
		})
	}
}

func TestInboundTurnCoordinatorReleasesOriginalAndSteeringAfterAggregateRejection(t *testing.T) {
	rejection := errors.New("aggregate outbound rejected")
	al, _, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	trackingBus := &finalResponseAdmissionTestBus{
		MessageBus: al.bus.(*bus.MessageBus),
		publishErr: rejection,
	}
	setTestMessageBus(al, trackingBus)
	msg := finalResponseAdmissionInboundMessage("spool-original")
	coordinator, target, claim := prepareFinalResponseAdmissionTurn(t, al, msg, "spool-steering")

	coordinator.runWorker(t.Context(), msg, target, claim)

	acked, released, cause := trackingBus.ownership()
	if len(acked) != 0 || !containsExactly(released, "spool-original", "spool-steering") {
		t.Fatalf("rejected aggregate ownership = acked:%v released:%v", acked, released)
	}
	if !errors.Is(cause, rejection) {
		t.Fatalf("release cause = %v, want %v", cause, rejection)
	}
}

func TestInboundTurnCoordinatorReleasesActiveTurnSteeringAfterAggregateRejection(t *testing.T) {
	rejection := errors.New("aggregate outbound rejected")
	al, _, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	trackingBus := &finalResponseAdmissionTestBus{
		MessageBus: al.bus.(*bus.MessageBus),
		publishErr: rejection,
	}
	setTestMessageBus(al, trackingBus)
	msg := finalResponseAdmissionInboundMessage("spool-original")
	coordinator, target, claim := prepareFinalResponseAdmissionTurnForSender(
		t,
		al,
		msg,
		"spool-steering",
		msg.Context.SenderID,
	)

	coordinator.runWorker(t.Context(), msg, target, claim)

	acked, released, cause := trackingBus.ownership()
	if len(acked) != 0 || !containsExactly(released, "spool-original", "spool-steering") {
		t.Fatalf("rejected active-turn aggregate ownership = acked:%v released:%v", acked, released)
	}
	if !errors.Is(cause, rejection) {
		t.Fatalf("release cause = %v, want %v", cause, rejection)
	}
}

func TestInboundTurnCoordinatorSettlesOriginalAndSteeringAdmissionsIndependently(t *testing.T) {
	rejection := errors.New("outbound rejected")
	tests := []struct {
		name           string
		publishResults []error
		wantAcked      []string
		wantReleased   []string
	}{
		{
			name:           "rejected original error and accepted continuation",
			publishResults: []error{rejection, nil},
			wantAcked:      []string{"spool-steering"},
			wantReleased:   []string{"spool-original"},
		},
		{
			name:           "accepted original error and rejected continuation",
			publishResults: []error{nil, rejection},
			wantAcked:      []string{"spool-original"},
			wantReleased:   []string{"spool-steering"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &sequenceProvider{
				errors: []error{errors.New("provider unavailable")},
				responses: []*providers.LLMResponse{
					nil,
					{Content: "continuation response", FinishReason: "stop"},
				},
			}
			al, _, cleanup := newTurnCoordTestLoop(t, provider)
			defer cleanup()
			trackingBus := &finalResponseAdmissionTestBus{
				MessageBus:     al.bus.(*bus.MessageBus),
				publishResults: append([]error(nil), tt.publishResults...),
			}
			setTestMessageBus(al, trackingBus)
			msg := finalResponseAdmissionInboundMessage("spool-original")
			coordinator, target, claim := prepareFinalResponseAdmissionTurn(t, al, msg, "spool-steering")

			coordinator.runWorker(t.Context(), msg, target, claim)

			acked, released, _ := trackingBus.ownership()
			if !containsExactly(acked, tt.wantAcked...) || !containsExactly(released, tt.wantReleased...) {
				t.Fatalf("ownership = acked:%v released:%v", acked, released)
			}
		})
	}
}

func TestInboundTurnCoordinatorSettlesHandledNoOutputIndependently(t *testing.T) {
	rejection := errors.New("aggregate outbound rejected")
	handledResponse := &providers.LLMResponse{
		Content: "Delivering the result now.",
		ToolCalls: []providers.ToolCall{{
			ID:        "call-handled-user",
			Type:      "function",
			Name:      "handled_user_tool",
			Arguments: map[string]any{},
		}},
	}
	textResponse := &providers.LLMResponse{Content: "aggregate text", FinishReason: "stop"}
	handledTerminalResponse := &providers.LLMResponse{}
	tests := []struct {
		name         string
		responses    []*providers.LLMResponse
		steeringID   string
		wantAcked    []string
		wantReleased []string
	}{
		{
			name:         "handled original does not depend on continuation aggregate",
			responses:    []*providers.LLMResponse{handledResponse, handledTerminalResponse, textResponse},
			steeringID:   "user-2",
			wantAcked:    []string{"spool-original"},
			wantReleased: []string{"spool-steering"},
		},
		{
			name:         "handled steering does not depend on original aggregate",
			responses:    []*providers.LLMResponse{textResponse, handledResponse, handledTerminalResponse},
			steeringID:   "user-2",
			wantAcked:    []string{"spool-steering"},
			wantReleased: []string{"spool-original"},
		},
		{
			name:       "handled active-turn steering does not wait for aggregate admission",
			responses:  []*providers.LLMResponse{handledResponse, handledTerminalResponse},
			steeringID: "user-1",
			wantAcked:  []string{"spool-original", "spool-steering"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &sequenceProvider{responses: tt.responses}
			al, agent, cleanup := newTurnCoordTestLoop(t, provider)
			defer cleanup()
			agent.Tools.Register(&handledUserTool{})
			trackingBus := &finalResponseAdmissionTestBus{
				MessageBus:     al.bus.(*bus.MessageBus),
				publishResults: []error{rejection},
			}
			setTestMessageBus(al, trackingBus)
			store := media.NewFileMediaStore()
			al.SetMediaStore(store)
			al.SetChannelManager(newStartedTestChannelManager(
				t,
				trackingBus.MessageBus,
				store,
				"telegram",
				&fakeMediaChannel{fakeChannel: fakeChannel{id: "rid-telegram"}},
			))
			msg := finalResponseAdmissionInboundMessage("spool-original")
			coordinator, target, claim := prepareFinalResponseAdmissionTurnForSender(
				t,
				al,
				msg,
				"spool-steering",
				tt.steeringID,
			)

			coordinator.runWorker(t.Context(), msg, target, claim)

			acked, released, _ := trackingBus.ownership()
			if !containsExactly(acked, tt.wantAcked...) || !containsExactly(released, tt.wantReleased...) {
				t.Fatalf("ownership = acked:%v released:%v", acked, released)
			}
		})
	}
}

func finalResponseAdmissionInboundMessage(spoolID string) bus.InboundMessage {
	return testInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "chat-1",
			SenderID: "user-1",
		},
		SpoolID: spoolID,
		Content: "hello",
	})
}

func runFinalResponseAdmissionTurn(t *testing.T, al *AgentLoop, msg bus.InboundMessage) {
	t.Helper()
	coordinator, target, claim := prepareFinalResponseAdmissionTurn(t, al, msg, "")
	coordinator.runWorker(t.Context(), msg, target, claim)
}

func prepareFinalResponseAdmissionTurn(
	t *testing.T,
	al *AgentLoop,
	msg bus.InboundMessage,
	steeringSpoolID string,
) (*inboundTurnCoordinator, *inboundDispatchTarget, *runtimeSessionClaim) {
	return prepareFinalResponseAdmissionTurnForSender(t, al, msg, steeringSpoolID, "user-2")
}

func prepareFinalResponseAdmissionTurnForSender(
	t *testing.T,
	al *AgentLoop,
	msg bus.InboundMessage,
	steeringSpoolID string,
	steeringSenderID string,
) (*inboundTurnCoordinator, *inboundDispatchTarget, *runtimeSessionClaim) {
	t.Helper()
	coordinator := newInboundTurnCoordinator(al)
	target, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("resolveSteeringTarget() rejected test inbound")
	}
	if steeringSpoolID != "" {
		err := al.enqueueSteeringMessageWithSender(
			target.runtimeSessionScope(),
			target.Agent.ID,
			steeringSenderID,
			providers.Message{
				Role:           "user",
				Content:        "queued steering",
				InboundSpoolID: steeringSpoolID,
			},
		)
		if err != nil {
			t.Fatalf("enqueueSteeringMessageWithSender() error = %v", err)
		}
	}
	claim, active, claimed := coordinator.claimSession(target)
	if !claimed {
		t.Fatalf("claimSession() failed with active target %+v", active)
	}
	return coordinator, target, claim
}

func containsExactly(values []string, wants ...string) bool {
	if len(values) != len(wants) {
		return false
	}
	remaining := make(map[string]int, len(wants))
	for _, want := range wants {
		remaining[want]++
	}
	for _, value := range values {
		if remaining[value] == 0 {
			return false
		}
		remaining[value]--
	}
	return true
}

func TestAcquireTurnCapacityDoesNotHoldAdmissionWhileWaitingForWorker(t *testing.T) {
	al := &AgentLoop{
		workerSem: make(chan struct{}, 1),
		turns: &turnRuntime{
			admissions: &agentTurnAdmissionController{
				limits:  map[string]int{"agent-a": 1},
				active:  make(map[string]int),
				changed: make(chan struct{}),
			},
		},
	}
	al.workerSem <- struct{}{}
	coordinator := newInboundTurnCoordinator(al)
	al.turns.admissions.mu.Lock()
	admissionReleased := al.turns.admissions.changed
	al.turns.admissions.mu.Unlock()

	capacityDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		_, release, err := coordinator.acquireTurnCapacity(ctx, "agent-a")
		if err == nil {
			release()
		}
		capacityDone <- err
	}()
	select {
	case <-admissionReleased:
	case <-time.After(time.Second):
		t.Fatal("queued turn retained agent admission while waiting for worker")
	}

	// The queued inbound turn must release agent-a while the only worker is
	// occupied, allowing the running worker to delegate to agent-a.
	delegateCtx, delegateCancel := context.WithTimeout(context.Background(), time.Second)
	_, releaseDelegate, err := al.turns.acquireAgentTurn(delegateCtx, "agent-a")
	delegateCancel()
	if err != nil {
		t.Fatalf("delegate acquireAgentTurn() error = %v", err)
	}
	releaseDelegate()

	<-al.workerSem
	select {
	case err = <-capacityDone:
		if err != nil {
			t.Fatalf("acquireTurnCapacity() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for capacity acquisition")
	}
}

func TestInboundTurnCoordinatorHandlesSubagentsBeforeSessionClaim(t *testing.T) {
	for _, tt := range []struct {
		name        string
		active      bool
		wantReply   string
		wantActive  int
		wantInterim bool
	}{
		{name: "idle", wantReply: "No active tasks running in this session."},
		{name: "active", active: true, wantReply: "turn-active", wantActive: 1, wantInterim: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			al, _, msgBus, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			agent := al.registry.GetDefaultAgent()
			if agent == nil {
				t.Fatal("default agent is unavailable")
			}

			sessionKey := session.BuildOpaqueSessionKey("coordinator-subagents-" + tt.name)
			scope := newRuntimeSessionScope(agent.Workspace, sessionKey)
			if tt.active {
				al.turns.activeTurnStates.Store(scope, &turnState{
					turnID:       "turn-active",
					agentID:      agent.ID,
					workspace:    agent.Workspace,
					sessionKey:   sessionKey,
					phase:        TurnPhaseRunning,
					childTurnIDs: []string{"child-active"},
				})
				defer al.turns.activeTurnStates.Delete(scope)
			}

			msg := bus.NormalizeInboundMessage(bus.InboundMessage{
				Context:    bus.InboundContext{Channel: "cli", ChatID: "direct", SenderID: "owner"},
				Content:    "/subagents",
				SessionKey: sessionKey,
			})
			newInboundTurnCoordinator(al).handleInbound(t.Context(), msg)

			outbound := receiveCoordinatorCommandReply(t, msgBus)
			if !strings.Contains(outbound.Content, tt.wantReply) {
				t.Fatalf("/subagents reply = %q, want substring %q", outbound.Content, tt.wantReply)
			}
			metadata := outbound.Metadata
			if metadata.IsInterim() != tt.wantInterim || metadata.IsFinal() == tt.wantInterim {
				t.Fatalf(
					"/subagents outbound metadata = %#v, want interim=%v",
					metadata,
					tt.wantInterim,
				)
			}
			if got := al.ActiveTurnCount(); got != tt.wantActive {
				t.Fatalf("active turns after /subagents = %d, want %d", got, tt.wantActive)
			}
			if got := al.pendingSteeringCountForScope(scope); got != 0 {
				t.Fatalf("queued steering after /subagents = %d, want 0", got)
			}
		})
	}
}

func TestInboundTurnCoordinatorActiveSubagentsDoesNotFinalizeToolFeedback(t *testing.T) {
	al, cfg, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	channel := newCoordinatorFeedbackChannel()
	manager := newStartedTestChannelManagerWithConfig(t, cfg, msgBus, nil, "telegram", channel)
	al.SetChannelManager(manager)

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is unavailable")
	}
	sessionKey := session.BuildOpaqueSessionKey("coordinator-subagents-feedback")
	scope := newRuntimeSessionScope(agent.Workspace, sessionKey)
	activeTurn := &turnState{
		agent:        agent,
		turnID:       "turn-active",
		agentID:      agent.ID,
		workspace:    agent.Workspace,
		sessionKey:   sessionKey,
		phase:        TurnPhaseRunning,
		channel:      "telegram",
		chatID:       "direct",
		childTurnIDs: []string{"child-active"},
		opts: turnSpec{Dispatch: DispatchRequest{
			SessionKey: sessionKey,
			InboundContext: &bus.InboundContext{
				Channel: "telegram",
				ChatID:  "direct",
			},
		}},
	}
	al.turns.activeTurnStates.Store(scope, activeTurn)
	defer al.turns.activeTurnStates.Delete(scope)

	publishFeedback := func(content string) {
		t.Helper()
		msg := outboundMessageForTurnWithOptions(
			activeTurn,
			content,
			outboundTurnMessageOptions{kind: bus.OutboundMessageKindToolFeedback},
		)
		if err := al.bus.PublishOutbound(t.Context(), msg); err != nil {
			t.Fatalf("PublishOutbound(%q) error = %v", content, err)
		}
	}

	publishFeedback("before-inspection")
	receiveCoordinatorFeedbackChannelMessage(t, channel, "before-inspection")

	msg := bus.NormalizeInboundMessage(bus.InboundMessage{
		Context:    bus.InboundContext{Channel: "telegram", ChatID: "direct", SenderID: "owner"},
		Content:    "/subagents",
		SessionKey: sessionKey,
	})
	newInboundTurnCoordinator(al).handleInbound(t.Context(), msg)
	inspection := receiveCoordinatorFeedbackChannelMessage(t, channel, "turn-active")
	metadata := inspection.Metadata
	if !metadata.IsInterim() || metadata.IsFinal() {
		t.Fatalf("active /subagents metadata = %#v, want interim", metadata)
	}

	publishFeedback("after-inspection")
	receiveCoordinatorFeedbackChannelMessage(t, channel, "after-inspection")
}

func TestInboundTurnCoordinatorSubagentsUsesRoutedWorkspace(t *testing.T) {
	firstWorkspace := t.TempDir()
	secondWorkspace := t.TempDir()
	cfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{
			Workspace:         firstWorkspace,
			ModelName:         "test-model",
			MaxTokens:         4096,
			MaxToolIterations: 10,
			ContextManager:    "none",
		},
		List: []config.AgentConfig{
			{ID: "first", Default: true},
			{ID: "second", Workspace: secondWorkspace},
		},
		Dispatch: &config.DispatchConfig{Rules: []config.DispatchRule{{
			Name: "second-channel", Agent: "second",
			When: config.DispatchSelector{Channel: "second"},
		}}},
	}}
	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, &mockProvider{})
	defer al.Close()

	first, firstOK := al.registry.GetAgent("first")
	second, secondOK := al.registry.GetAgent("second")
	if !firstOK || !secondOK {
		t.Fatal("routed agents are unavailable")
	}
	sessionKey := session.BuildOpaqueSessionKey("coordinator-subagents-shared")
	firstScope := newRuntimeSessionScope(first.Workspace, sessionKey)
	secondScope := newRuntimeSessionScope(second.Workspace, sessionKey)
	al.turns.activeTurnStates.Store(firstScope, &turnState{
		turnID: "turn-first", agentID: first.ID, workspace: first.Workspace,
		sessionKey: sessionKey, phase: TurnPhaseRunning,
	})
	al.turns.activeTurnStates.Store(secondScope, &turnState{
		turnID: "turn-second", agentID: second.ID, workspace: second.Workspace,
		sessionKey: sessionKey, phase: TurnPhaseRunning,
	})
	defer al.turns.activeTurnStates.Delete(firstScope)
	defer al.turns.activeTurnStates.Delete(secondScope)

	msg := bus.NormalizeInboundMessage(bus.InboundMessage{
		Context:    bus.InboundContext{Channel: "second", ChatID: "direct", SenderID: "owner"},
		Content:    "/subagents",
		SessionKey: sessionKey,
	})
	newInboundTurnCoordinator(al).handleInbound(t.Context(), msg)

	outbound := receiveCoordinatorCommandReply(t, msgBus)
	if !strings.Contains(outbound.Content, "turn-second") || strings.Contains(outbound.Content, "turn-first") {
		t.Fatalf("/subagents routed reply = %q, want only second workspace turn", outbound.Content)
	}
	if got := al.pendingSteeringCountForScope(firstScope); got != 0 {
		t.Fatalf("first workspace queued steering = %d, want 0", got)
	}
	if got := al.pendingSteeringCountForScope(secondScope); got != 0 {
		t.Fatalf("second workspace queued steering = %d, want 0", got)
	}
}

func receiveCoordinatorCommandReply(t *testing.T, msgBus *bus.MessageBus) bus.OutboundMessage {
	t.Helper()
	select {
	case outbound := <-msgBus.OutboundChan():
		return outbound
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for coordinator command reply")
		return bus.OutboundMessage{}
	}
}

func coordinatorTestTarget(routeScopeKey, sessionKey string) *inboundDispatchTarget {
	return &inboundDispatchTarget{
		Agent:         &AgentInstance{ID: "main", Workspace: "/test/main"},
		RouteClaimKey: runtimeRouteClaimKey(routeScopeKey, ""),
		Allocation: session.Allocation{
			RouteScopeKey: routeScopeKey,
		},
		SessionKey: sessionKey,
	}
}

func TestInboundTurnCoordinatorClaimSessionSerializesSession(t *testing.T) {
	al := &AgentLoop{turns: newTurnRuntime(nil, nil)}
	coord := newInboundTurnCoordinator(al)

	firstTarget := coordinatorTestTarget("route-1", "session-1")
	claim, _, claimed := coord.claimSession(firstTarget)
	if !claimed {
		t.Fatal("expected first claim to succeed")
	}
	if claim == nil || claim.placeholder == nil {
		t.Fatal("expected claim with placeholder")
	}
	if claim.scope.sessionKey != "session-1" {
		t.Fatalf("claim session key = %q, want session-1", claim.scope.sessionKey)
	}
	if !isPendingTurnState(claim.placeholder) {
		t.Fatalf("placeholder turn id = %q, want pending turn", claim.placeholder.turnID)
	}
	if got := al.turns.activeTurnState(firstTarget.runtimeSessionScope()); got != claim.placeholder {
		t.Fatalf("active turn = %p, want placeholder %p", got, claim.placeholder)
	}

	second, activeTarget, claimed := coord.claimSession(coordinatorTestTarget("route-1", "session-2"))
	if claimed {
		t.Fatalf("expected second claim to fail, got placeholder %p", second)
	}
	if activeTarget.SessionKey != "session-1" {
		t.Fatalf("active session key = %q, want session-1", activeTarget.SessionKey)
	}
	if activeTarget != firstTarget {
		t.Fatal("route claim did not retain the original dispatch target")
	}
	if got := al.turns.activeTurnState(firstTarget.runtimeSessionScope()); got != claim.placeholder {
		t.Fatalf("active turn changed after rejected claim: got %p, want %p", got, claim.placeholder)
	}
}

func TestInboundTurnCoordinatorCleanupOnlyClearsOwnedPlaceholder(t *testing.T) {
	al := &AgentLoop{turns: newTurnRuntime(nil, nil)}
	coord := newInboundTurnCoordinator(al)

	first, _, claimed := coord.claimSession(coordinatorTestTarget("route-1", "session-1"))
	if !claimed {
		t.Fatal("expected first claim")
	}

	replacement := &turnState{
		turnID:     makePendingTurnID("session-1", al.turns.nextSequence()),
		workspace:  first.scope.workspace,
		sessionKey: first.scope.sessionKey,
		phase:      TurnPhaseSetup,
	}
	al.turns.activeTurnStates.Store(first.scope, replacement)

	first.releaseIfOwned()
	if got := al.turns.activeTurnState(first.scope); got != replacement {
		t.Fatalf("cleanup removed unowned placeholder: got %p, want replacement %p", got, replacement)
	}

	replacementClaim := &runtimeSessionClaim{
		runtime:     al.turns,
		scope:       first.scope,
		placeholder: replacement,
	}
	replacementClaim.releaseIfOwned()
	if got := al.turns.activeTurnState(first.scope); got != nil {
		t.Fatalf("cleanup left owned placeholder active: got %p", got)
	}
}

func TestInboundTurnCoordinatorPinsFollowUpAcrossCalendarBoundary(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	cfg.Session.Lifecycle = &config.SessionLifecycleConfig{
		Strategy: "calendar",
		Period:   "day",
		Timezone: "UTC",
	}
	now := time.Date(2026, 7, 17, 23, 59, 0, 0, time.UTC)
	al.sessionNow = func() time.Time { return now }
	msg := bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "chat-1",
			ChatType: "direct",
			SenderID: "telegram:42",
		},
		Content: "first",
	})

	initial, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("resolveSteeringTarget() failed for initial message")
	}
	coord := newInboundTurnCoordinator(al)
	claim, _, claimed := coord.claimSession(initial)
	if !claimed {
		t.Fatal("initial route claim failed")
	}
	defer claim.releaseIfOwned()

	now = now.Add(2 * time.Minute)
	followUp, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("resolveSteeringTarget() failed for follow-up")
	}
	if followUp != initial || followUp.SessionKey != initial.SessionKey {
		t.Fatal("follow-up escaped the active epoch at calendar boundary")
	}
}

func TestInboundTurnCoordinatorFollowUpExtendsIdleEpoch(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	cfg.Session.Lifecycle = &config.SessionLifecycleConfig{
		Strategy:           "idle",
		IdleTimeoutMinutes: 30,
	}
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	al.sessionNow = func() time.Time { return now }
	msg := bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "chat-1",
			ChatType: "direct",
			SenderID: "telegram:42",
		},
		Content: "first",
	})

	initial, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("resolveSteeringTarget() failed for initial message")
	}
	coord := newInboundTurnCoordinator(al)
	claim, _, claimed := coord.claimSession(initial)
	if !claimed {
		t.Fatal("initial route claim failed")
	}

	now = now.Add(20 * time.Minute)
	followUp, ok := al.resolveSteeringTarget(msg)
	if !ok || followUp.SessionKey != initial.SessionKey {
		t.Fatal("follow-up did not remain in the active idle epoch")
	}
	claim.releaseIfOwned()

	now = now.Add(20 * time.Minute)
	next, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("resolveSteeringTarget() failed after active turn")
	}
	if next.SessionKey != initial.SessionKey {
		t.Fatal("idle epoch rotated relative to initial activity instead of follow-up activity")
	}
}
