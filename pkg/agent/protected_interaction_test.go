package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

const protectedInteractionSentinel = "MINTCLAW_PDF3_INTERACTION_PRIVATE_3d91"

type recordingProtectedAnswerSink struct {
	mu        sync.Mutex
	accepted  []interactions.ProtectedAnswerSinkRequest
	committed []interactions.ProtectedAnswerCommitRequest
	canceled  []interactions.ProtectedAnswerCancelRequest
	discarded []interactions.ProtectedAnswerDiscardRequest
	err       error
	commitErr error
}

func (sink *recordingProtectedAnswerSink) Commit(
	_ context.Context,
	request interactions.ProtectedAnswerCommitRequest,
) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.commitErr != nil {
		return sink.commitErr
	}
	sink.committed = append(sink.committed, request)
	return nil
}

func (*recordingProtectedAnswerSink) Namespace() string { return "document.form.v1" }

func (sink *recordingProtectedAnswerSink) Accept(
	_ context.Context,
	request interactions.ProtectedAnswerSinkRequest,
) (interactions.ProtectedAnswerReceipt, error) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.err != nil {
		return interactions.ProtectedAnswerReceipt{}, sink.err
	}
	sink.accepted = append(sink.accepted, request)
	return interactions.ProtectedAnswerReceipt{
		Reference: "form_value_0123456789abcdef",
		State:     "stored",
	}, nil
}

func (sink *recordingProtectedAnswerSink) Cancel(
	_ context.Context,
	request interactions.ProtectedAnswerCancelRequest,
) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.err != nil {
		return sink.err
	}
	sink.canceled = append(sink.canceled, request)
	return nil
}

func (sink *recordingProtectedAnswerSink) Discard(
	_ context.Context,
	request interactions.ProtectedAnswerDiscardRequest,
) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.err != nil {
		return sink.err
	}
	sink.discarded = append(sink.discarded, request)
	return nil
}

func (sink *recordingProtectedAnswerSink) acceptedRequests() []interactions.ProtectedAnswerSinkRequest {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]interactions.ProtectedAnswerSinkRequest(nil), sink.accepted...)
}

func (sink *recordingProtectedAnswerSink) canceledRequests() []interactions.ProtectedAnswerCancelRequest {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]interactions.ProtectedAnswerCancelRequest(nil), sink.canceled...)
}

func (sink *recordingProtectedAnswerSink) committedRequests() []interactions.ProtectedAnswerCommitRequest {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]interactions.ProtectedAnswerCommitRequest(nil), sink.committed...)
}

func (sink *recordingProtectedAnswerSink) discardedRequests() []interactions.ProtectedAnswerDiscardRequest {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]interactions.ProtectedAnswerDiscardRequest(nil), sink.discarded...)
}

func TestHumanInteractionRuntimePersistsProtectedBinding(t *testing.T) {
	messageBus := bus.NewMessageBus()
	manager := newInteractionChannelManager()
	al := &AgentLoop{cfg: config.DefaultConfig(), bus: messageBus, channelManager: manager}
	attachInteractionOutbox(t, al, messageBus, manager)
	workspace := t.TempDir()
	request := testToolSuspensionRequest(workspace)
	request.Prompt.Questions = []interactions.Question{{
		ID: "field_value", Question: "What value should be used?",
	}}
	request.Prompt.ProtectedAnswer = &interactions.ProtectedAnswerBinding{
		Namespace: "document.form.v1", Token: "opaque-runtime-binding",
	}
	disposition, err := (&humanInteractionRuntime{al: al, coordinator: &al.interactions}).SuspendToolCall(
		t.Context(),
		request,
	)
	if err != nil || !disposition.Durable {
		t.Fatalf("SuspendToolCall() = (%#v, %v)", disposition, err)
	}
	record, ok := al.interactionRegistryForWorkspace(workspace).Get(disposition.InteractionID)
	if !ok || record.ProtectedAnswer == nil || record.ProtectedAnswer.Token != "opaque-runtime-binding" ||
		record.Status != interactions.StatusWaiting {
		t.Fatalf("protected suspension record = %#v, found=%t", record, ok)
	}
	select {
	case outbound := <-manager.sent:
		if !outbound.Metadata.IsQuestionPrompt() {
			t.Fatalf("protected prompt metadata = %#v", outbound.Metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("protected prompt was not delivered")
	}
}

func TestProtectedAnswerIdempotencyPrefersStablePlatformMessageIdentity(t *testing.T) {
	message := bus.InboundMessage{
		SpoolID: "delivery-attempt-1",
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", TopicID: "topic-1",
			MessageID: "platform-message-1",
		},
	}
	first, err := protectedAnswerIdempotencyKey("interaction-1", message)
	if err != nil {
		t.Fatal(err)
	}
	message.SpoolID = "delivery-attempt-2"
	replayed, err := protectedAnswerIdempotencyKey("interaction-1", message)
	if err != nil || replayed != first {
		t.Fatalf("stable message replay key = %q, %v; want %q", replayed, err, first)
	}
	message.Context.MessageID = ""
	fallback, err := protectedAnswerIdempotencyKey("interaction-1", message)
	if err != nil || fallback == first {
		t.Fatalf("spool fallback key = %q, %v", fallback, err)
	}
}

func TestProtectedInteractionAnswerSurvivesCoordinatorRestartWithoutPlaintext(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &simpleConvProvider{}, func(cfg *config.Config) {
		cfg.Channels = config.ChannelsConfig{
			"telegram": &config.Channel{Enabled: true, Type: config.ChannelTelegram},
		}
	})
	al := fixture.Loop
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "start protected form",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:protected-answer"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
	})
	record, target := prepareWaitingProtectedInteraction(t, al, fixture.Agent, msg)

	// Replace process-owned interaction state while retaining the durable
	// workspace snapshot, then register a freshly constructed sink.
	al.interactions = newInteractionCoordinator(t.TempDir())
	sink := &recordingProtectedAnswerSink{}
	if err := al.interactions.registerProtectedAnswerSink(sink); err != nil {
		t.Fatal(err)
	}
	reloaded, ok := al.interactionRegistryForWorkspace(fixture.Agent.Workspace).Get(record.ID)
	if !ok || reloaded.Status != interactions.StatusWaiting || reloaded.ProtectedAnswer == nil {
		t.Fatalf("reloaded protected interaction = %#v, found=%t", reloaded, ok)
	}

	answer := msg
	answer.Content = protectedInteractionSentinel
	answer.SpoolID = "spool-protected-answer"
	answer.Context.MessageID = "message-protected-answer"
	answer.Context.ReplyToMessageID = "prompt-message"
	answer.Context.Interaction.Response = protectedInteractionSentinel
	command, err := newAnswerInteractionCommand(answer, target)
	if err != nil {
		t.Fatal(err)
	}
	result, err := newInteractionService(al).Answer(t.Context(), command)
	if err != nil || result.Ownership != interactionInboundClaimed || !result.Effects.AnswerPersisted {
		t.Fatalf("protected Answer() = (%#v, %v)", result, err)
	}
	requests := sink.acceptedRequests()
	if len(requests) != 1 || requests[0].Text != protectedInteractionSentinel ||
		requests[0].Intent != interactions.ProtectedAnswerValue ||
		!strings.HasPrefix(requests[0].IdempotencyKey, "protected_answer_") {
		t.Fatalf("protected sink requests = %#v", requests)
	}
	commits := sink.committedRequests()
	if len(commits) != 1 || commits[0].Receipt.Reference != "form_value_0123456789abcdef" ||
		commits[0].InteractionID != record.ID {
		t.Fatalf("protected sink commits = %#v", commits)
	}
	resolved, ok := al.interactionRegistryForWorkspace(fixture.Agent.Workspace).Get(record.ID)
	if !ok || resolved.Status != interactions.StatusResolved || resolved.Answer == nil ||
		resolved.Answer.Protected == nil || resolved.Answer.Text != "" || len(resolved.Answer.Values) != 0 {
		t.Fatalf("resolved protected interaction = %#v, found=%t", resolved, ok)
	}
	assertProtectedSentinelAbsentFromInteractionStateAndHistory(
		t,
		fixture.Agent.Workspace,
		fixture.Agent.Sessions.GetHistory(target.SessionKey),
		protectedInteractionSentinel,
	)
}

func TestProtectedInteractionProjectsButtonAndVoiceWithoutHistoryLeak(t *testing.T) {
	t.Run("skip button", func(t *testing.T) {
		fixture := newAgentLoopTestFixture(t, &simpleConvProvider{})
		al := fixture.Loop
		manager := newInteractionChannelManager()
		installInteractionChannelManager(t, al, manager)
		sink := &recordingProtectedAnswerSink{}
		if err := al.interactions.registerProtectedAnswerSink(sink); err != nil {
			t.Fatal(err)
		}
		msg := testInboundMessage(bus.InboundMessage{
			SessionKey: session.BuildOpaqueSessionKey("agent:main:test:protected-skip"),
			Context: bus.InboundContext{
				Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
			},
		})
		_, target := prepareWaitingProtectedInteraction(t, al, fixture.Agent, msg)
		answer := msg
		answer.Content = interactions.ProtectedAnswerSkipLabel
		answer.SpoolID = "spool-protected-skip"
		answer.Context.MessageID = "message-protected-skip"
		answer.Context.Interaction.Response = interactions.ProtectedAnswerSkipLabel
		command, _ := newAnswerInteractionCommand(answer, target)
		if _, err := newInteractionService(al).Answer(t.Context(), command); err != nil {
			t.Fatal(err)
		}
		requests := sink.acceptedRequests()
		if len(requests) != 1 || requests[0].Intent != interactions.ProtectedAnswerSkip || requests[0].Text != "" {
			t.Fatalf("skip request = %#v", requests)
		}
	})

	t.Run("voice", func(t *testing.T) {
		fixture := newAgentLoopTestFixture(t, &simpleConvProvider{}, func(cfg *config.Config) {
			cfg.Channels = config.ChannelsConfig{
				"telegram": &config.Channel{Enabled: true, Type: config.ChannelTelegram},
			}
		})
		al := fixture.Loop
		manager := newInteractionChannelManager()
		installInteractionChannelManager(t, al, manager)
		sink := &recordingProtectedAnswerSink{}
		if err := al.interactions.registerProtectedAnswerSink(sink); err != nil {
			t.Fatal(err)
		}
		store := media.NewFileMediaStore()
		audioPath := filepath.Join(t.TempDir(), "voice.ogg")
		if err := os.WriteFile(audioPath, []byte("fake audio"), 0o600); err != nil {
			t.Fatal(err)
		}
		ref, err := store.Store(audioPath, media.MediaMeta{
			Filename: "voice.ogg", ContentType: "audio/ogg", CleanupPolicy: media.CleanupPolicyForgetOnly,
		}, "scope-protected-voice")
		if err != nil {
			t.Fatal(err)
		}
		al.SetMediaStore(store)
		al.SetTranscriber(&fixedTranscriber{text: protectedInteractionSentinel})
		msg := testInboundMessage(bus.InboundMessage{
			SessionKey: session.BuildOpaqueSessionKey("agent:main:test:protected-voice"),
			Context: bus.InboundContext{
				Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
			},
		})
		_, target := prepareWaitingProtectedInteraction(t, al, fixture.Agent, msg)
		answer := msg
		answer.Content = "[voice]"
		answer.Media = []string{ref}
		answer.SpoolID = "spool-protected-voice"
		answer.Context.MessageID = "message-protected-voice"
		answer.Context.Interaction.Response = "[voice]"
		command, _ := newAnswerInteractionCommand(answer, target)
		if _, err := newInteractionService(al).Answer(t.Context(), command); err != nil {
			t.Fatal(err)
		}
		requests := sink.acceptedRequests()
		if len(requests) != 1 || !strings.Contains(requests[0].Text, protectedInteractionSentinel) {
			t.Fatalf("voice request = %#v", requests)
		}
		assertProtectedSentinelAbsentFromInteractionStateAndHistory(
			t,
			fixture.Agent.Workspace,
			fixture.Agent.Sessions.GetHistory(target.SessionKey),
			protectedInteractionSentinel,
		)
	})
}

func TestProtectedInteractionStoreFailureKeepsQuestionWaiting(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &simpleConvProvider{})
	al := fixture.Loop
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	sink := &recordingProtectedAnswerSink{err: errors.New("private backend detail")}
	if err := al.interactions.registerProtectedAnswerSink(sink); err != nil {
		t.Fatal(err)
	}
	msg := testInboundMessage(bus.InboundMessage{
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:protected-failure"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
	})
	record, target := prepareWaitingProtectedInteraction(t, al, fixture.Agent, msg)
	answer := msg
	answer.Content = protectedInteractionSentinel
	answer.SpoolID = "spool-protected-failure"
	answer.Context.MessageID = "message-protected-failure"
	command, _ := newAnswerInteractionCommand(answer, target)
	result, err := newInteractionService(al).Answer(t.Context(), command)
	if err != nil || result.Ownership != interactionInboundCallerOwned ||
		result.Effects != (interactionAnswerEffects{}) {
		t.Fatalf("failed protected Answer() = (%#v, %v)", result, err)
	}
	current, _ := al.interactionRegistryForWorkspace(fixture.Agent.Workspace).Get(record.ID)
	if current.Status != interactions.StatusWaiting || current.Answer != nil {
		t.Fatalf("failed sink mutated interaction = %#v", current)
	}
	select {
	case outbound := <-manager.sent:
		if strings.Contains(outbound.Content, "private backend detail") ||
			!strings.Contains(outbound.Content, "could not be stored") {
			t.Fatalf("protected failure notice = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("protected failure notice was not delivered")
	}
}

func TestProtectedInteractionLosingReplayDoesNotDiscardWinningValue(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &simpleConvProvider{})
	al := fixture.Loop
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	sink := &recordingProtectedAnswerSink{}
	if err := al.interactions.registerProtectedAnswerSink(sink); err != nil {
		t.Fatal(err)
	}
	msg := testInboundMessage(bus.InboundMessage{
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:protected-losing-replay"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
	})
	record, target := prepareWaitingProtectedInteraction(t, al, fixture.Agent, msg)
	receipt := interactions.ProtectedAnswerReceipt{
		Reference: "form_value_0123456789abcdef",
		State:     "stored",
	}
	registry := al.interactionRegistryForWorkspace(fixture.Agent.Workspace)
	if _, err := registry.ClaimProtectedAnswer(record.ID, record.Revision, interactions.Answer{
		MessageID: "message-protected-replay", ReceivedAt: time.Now().UnixMilli(), Protected: &receipt,
	}); err != nil {
		t.Fatal(err)
	}
	answer := msg
	answer.Content = protectedInteractionSentinel
	answer.SpoolID = "spool-protected-replay"
	answer.Context.MessageID = "message-protected-replay"
	command, err := newAnswerInteractionCommand(answer, target)
	if err != nil {
		t.Fatal(err)
	}
	result, err := newInteractionService(al).acceptProtectedAnswer(
		t.Context(),
		command,
		registry,
		record,
		interactions.Answer{Text: protectedInteractionSentinel, ReceivedAt: time.Now().UnixMilli()},
		answerInteractionResult{Ownership: interactionInboundCallerOwned},
	)
	if err != nil || result.Ownership != interactionInboundClaimed || !result.Effects.AnswerPersisted {
		t.Fatalf("losing protected replay = (%#v, %v)", result, err)
	}
	if discarded := sink.discardedRequests(); len(discarded) != 0 {
		t.Fatalf("losing replay discarded the winning staged value: %#v", discarded)
	}
	if commits := sink.committedRequests(); len(commits) != 1 || commits[0].Receipt != receipt {
		t.Fatalf("losing replay commits = %#v", commits)
	}
	claimed, ok := registry.Get(record.ID)
	if !ok || claimed.Status != interactions.StatusClaimed || !protectedAnswerReceiptMatches(claimed, receipt) {
		t.Fatalf("winning protected claim = %#v, found=%t", claimed, ok)
	}
}

func TestProtectedInteractionRecoveryCommitsClaimedAnswerBeforeResume(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &simpleConvProvider{})
	al := fixture.Loop
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	sink := &recordingProtectedAnswerSink{commitErr: errors.New("private commit backend detail")}
	if err := al.interactions.registerProtectedAnswerSink(sink); err != nil {
		t.Fatal(err)
	}
	msg := testInboundMessage(bus.InboundMessage{
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:protected-commit-recovery"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
	})
	record, target := prepareWaitingProtectedInteraction(t, al, fixture.Agent, msg)
	answer := msg
	answer.Content = protectedInteractionSentinel
	answer.SpoolID = "spool-protected-commit-recovery"
	answer.Context.MessageID = "message-protected-commit-recovery"
	command, _ := newAnswerInteractionCommand(answer, target)
	result, err := newInteractionService(al).Answer(t.Context(), command)
	if err == nil || strings.Contains(err.Error(), "private commit backend detail") ||
		result.Ownership != interactionInboundClaimed || !result.Effects.AnswerPersisted {
		t.Fatalf("protected commit failure = (%#v, %v)", result, err)
	}
	claimed, ok := al.interactionRegistryForWorkspace(fixture.Agent.Workspace).Get(record.ID)
	if !ok || claimed.Status != interactions.StatusClaimed || claimed.Answer == nil ||
		claimed.Answer.Protected == nil {
		t.Fatalf("claimed protected answer = %#v, found=%t", claimed, ok)
	}
	sink.mu.Lock()
	sink.commitErr = nil
	sink.mu.Unlock()
	al.RecoverHumanInteractions(t.Context())
	if commits := sink.committedRequests(); len(commits) != 1 || commits[0].InteractionID != record.ID {
		t.Fatalf("recovered protected commits = %#v", commits)
	}
}

func TestProtectedInteractionCancelCallsDomainSinkBeforeTerminalState(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &simpleConvProvider{})
	al := fixture.Loop
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	sink := &recordingProtectedAnswerSink{}
	if err := al.interactions.registerProtectedAnswerSink(sink); err != nil {
		t.Fatal(err)
	}
	msg := testInboundMessage(bus.InboundMessage{
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:protected-cancel"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
	})
	record, target := prepareWaitingProtectedInteraction(t, al, fixture.Agent, msg)
	cancel := msg
	cancel.Content = "/stop"
	cancel.SpoolID = "spool-protected-cancel"
	cancel.Context.MessageID = "message-protected-cancel"
	command, ok := newCancelInteractionCommand(cancel, target)
	if !ok {
		t.Fatal("protected cancellation command was not recognized")
	}
	result, err := newInteractionService(al).Cancel(t.Context(), command)
	if err != nil || !result.Canceled || !result.Effects.CancellationCompleted {
		t.Fatalf("protected Cancel() = (%#v, %v)", result, err)
	}
	requests := sink.canceledRequests()
	if len(requests) != 1 || requests[0].InteractionID != record.ID ||
		!strings.HasPrefix(requests[0].IdempotencyKey, "protected_answer_") {
		t.Fatalf("protected cancel requests = %#v", requests)
	}
	terminal, _ := al.interactionRegistryForWorkspace(fixture.Agent.Workspace).Get(record.ID)
	if terminal.Status != interactions.StatusCancelled {
		t.Fatalf("terminal protected interaction = %#v", terminal)
	}
}

func TestProtectedInteractionCancelFailureLeavesRecoveryFence(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &simpleConvProvider{})
	al := fixture.Loop
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	sink := &recordingProtectedAnswerSink{err: errors.New("private cancel backend detail")}
	if err := al.interactions.registerProtectedAnswerSink(sink); err != nil {
		t.Fatal(err)
	}
	msg := testInboundMessage(bus.InboundMessage{
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:protected-cancel-recovery"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
	})
	record, target := prepareWaitingProtectedInteraction(t, al, fixture.Agent, msg)
	cancel := msg
	cancel.Content = "/stop"
	cancel.SpoolID = "spool-protected-cancel-recovery"
	cancel.Context.MessageID = "message-protected-cancel-recovery"
	command, ok := newCancelInteractionCommand(cancel, target)
	if !ok {
		t.Fatal("protected cancellation command was not recognized")
	}
	result, err := newInteractionService(al).Cancel(t.Context(), command)
	if err == nil || strings.Contains(err.Error(), "private cancel backend detail") ||
		!result.Effects.CancellationFenced {
		t.Fatalf("protected cancel failure = (%#v, %v)", result, err)
	}
	fenced, ok := al.interactionRegistryForWorkspace(fixture.Agent.Workspace).Get(record.ID)
	if !ok || fenced.Status != interactions.StatusCanceling {
		t.Fatalf("protected cancellation fence = %#v, found=%t", fenced, ok)
	}
	sink.mu.Lock()
	sink.err = nil
	sink.mu.Unlock()
	al.recoverCancelingInteraction(
		t.Context(),
		fixture.Agent.Workspace,
		al.interactionRegistryForWorkspace(fixture.Agent.Workspace),
		fenced,
	)
	if requests := sink.canceledRequests(); len(requests) != 1 || requests[0].InteractionID != record.ID {
		t.Fatalf("recovered protected cancellation requests = %#v", requests)
	}
	recovered, ok := al.interactionRegistryForWorkspace(fixture.Agent.Workspace).Get(record.ID)
	if !ok || (recovered.Status != interactions.StatusCanceling &&
		recovered.Status != interactions.StatusCancelled) {
		t.Fatalf("recovered protected cancellation fence = %#v, found=%t", recovered, ok)
	}
}

func prepareWaitingProtectedInteraction(
	t *testing.T,
	al *AgentLoop,
	agent *AgentInstance,
	msg bus.InboundMessage,
) (interactions.Record, *inboundDispatchTarget) {
	t.Helper()
	target, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("failed to resolve protected interaction target")
	}
	route := interactions.Route{
		AgentID: agent.ID, SessionKey: target.SessionKey, RouteSessionKey: target.Allocation.RouteScopeKey,
		Channel: msg.Context.Channel, AccountID: msg.Context.Account, ChatID: msg.Context.ChatID,
		ChatType: msg.Context.ChatType, TopicID: msg.Context.TopicID, SenderID: msg.Context.SenderID,
	}
	origin := interactions.Origin{
		TurnID: "turn-protected", ToolCallID: "call-protected", ToolName: "document",
	}
	agent.Sessions.AddFullMessage(target.SessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: origin.ToolCallID, Name: origin.ToolName, Arguments: map[string]any{},
		}},
	})
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: interactions.KindQuestion, Route: route, Origin: origin,
		Questions: []interactions.Question{{
			ID: "field_value", Question: "What value should be used?", Options: []interactions.Option{
				{Label: interactions.ProtectedAnswerSkipLabel},
				{Label: interactions.ProtectedAnswerNotApplicableLabel},
			},
		}},
		ProtectedAnswer: &interactions.ProtectedAnswerBinding{
			Namespace: "document.form.v1", Token: "opaque-test-binding",
		},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return markTestInteractionWaiting(t, registry, record), target
}

func assertProtectedSentinelAbsentFromInteractionStateAndHistory(
	t *testing.T,
	workspace string,
	history []providers.Message,
	sentinel string,
) {
	t.Helper()
	data, err := os.ReadFile(interactions.WorkspaceStorePath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sentinel) {
		t.Fatalf("interaction state leaked protected sentinel: %s", data)
	}
	for _, message := range history {
		if strings.Contains(message.Content, sentinel) {
			t.Fatalf("session history leaked protected sentinel: %#v", history)
		}
	}
}
