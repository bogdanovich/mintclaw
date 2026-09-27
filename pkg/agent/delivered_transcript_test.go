package agent

import (
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func TestDeliveredProactiveTranscriptAppearsInNextTurnPrompt(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	provider := &recordingProvider{}
	messageBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, messageBus, provider)
	channel := &fakeChannel{id: "telegram"}
	al.SetChannelManager(newStartedTestChannelManagerWithConfig(
		t,
		cfg,
		messageBus,
		media.NewFileMediaStore(),
		"telegram",
		channel,
		channels.WithDeliveredTranscriptProjector(al),
	))
	target := bus.InboundContext{
		Channel:  "telegram",
		ChatID:   "chat-1",
		ChatType: "direct",
		SenderID: "telegram:user-1",
	}
	const reminder = "Напоминание: верни кроссовки в Whole Foods."

	projection, _, err := al.resolveOutboundTranscriptTarget(t.Context(), "", target, reminder, nil)
	if err != nil {
		t.Fatalf("resolveOutboundTranscriptTarget() error = %v", err)
	}
	if projection == nil {
		t.Fatal("expected proactive transcript projection")
	}
	if err = al.PublishProactiveMessage(t.Context(), target, reminder); err != nil {
		t.Fatalf("PublishProactiveMessage() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		history := al.GetRegistry().GetDefaultAgent().Sessions.GetHistory(projection.SessionKey)
		if len(history) == 1 && history[0].Role == "assistant" && history[0].Content == reminder {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivered reminder was not projected into history: %#v", history)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = al.processMessage(t.Context(), bus.InboundMessage{
		Context: target,
		Content: "Вернул",
	}); err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}

	foundReminder := false
	for _, message := range provider.lastMessages {
		if message.Role == "assistant" && message.Content == reminder {
			foundReminder = true
			break
		}
	}
	if !foundReminder {
		t.Fatalf("next-turn prompt omitted delivered reminder: %#v", provider.lastMessages)
	}
}

func TestPublishProactiveMessageOwnsDurableTranscriptReceipt(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	messageBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, messageBus, &recordingProvider{})
	coordinator, err := outbox.OpenCoordinator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	al.SetOutboundOutbox(coordinator)
	target := bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "telegram:user-1",
	}
	if err = al.PublishProactiveMessage(t.Context(), target, "reminder"); err != nil {
		t.Fatal(err)
	}
	select {
	case outbound := <-messageBus.OutboundChan():
		if outbound.DeliveryID == "" || outbound.Transcript == nil ||
			outbound.Transcript.DeliveryID != outbound.DeliveryID {
			t.Fatalf("durable proactive outbound = %#v", outbound)
		}
		intent, getErr := coordinator.Get(outbound.DeliveryID)
		if getErr != nil || intent.Status != outbox.StatusPending || !intent.RequiresTranscriptProjection() {
			t.Fatalf("durable proactive intent = %#v, %v", intent, getErr)
		}
	case <-time.After(time.Second):
		t.Fatal("proactive message was not published")
	}
}

func TestOutboundTranscriptProjectionSkipsCanonicalSameSession(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	messageBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, messageBus, &recordingProvider{})
	target := bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "telegram:user-1",
	}

	initial, _, err := al.resolveOutboundTranscriptTarget(t.Context(), "", target, "reply", nil)
	if err != nil || initial == nil {
		t.Fatalf("initial projection = %#v, %v", initial, err)
	}
	duplicate, _, err := al.resolveOutboundTranscriptTarget(
		t.Context(),
		initial.SessionKey,
		target,
		"reply",
		nil,
	)
	if err != nil {
		t.Fatalf("same-session target error = %v", err)
	}
	if duplicate != nil {
		t.Fatalf("same-session outbound requested duplicate projection: %#v", duplicate)
	}
}

func TestDeliveredTranscriptProjectionIsIdempotentByDeliveryID(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &recordingProvider{})
	target := bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "telegram:user-1",
	}
	projection, agent, err := al.resolveOutboundTranscriptTarget(t.Context(), "", target, "reminder", nil)
	if err != nil || projection == nil || agent == nil {
		t.Fatalf("projection = %#v, agent = %#v, error = %v", projection, agent, err)
	}
	projection.DeliveryID = "out_11111111111111111111111111111111"
	if err = al.ProjectDeliveredTranscript(t.Context(), *projection); err != nil {
		t.Fatal(err)
	}
	if err = al.ProjectDeliveredTranscript(t.Context(), *projection); err != nil {
		t.Fatalf("idempotent projection error = %v", err)
	}
	history := agent.Sessions.GetHistory(projection.SessionKey)
	if len(history) != 1 || history[0].Content != "reminder" ||
		history[0].OutboundDeliveryID != projection.DeliveryID {
		t.Fatalf("canonical history = %#v", history)
	}
}

func TestRecoveredDeliveredTranscriptProjectsAndSettlesAfterRestart(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &recordingProvider{})
	target := bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "telegram:user-1",
	}
	projection, agent, err := al.resolveOutboundTranscriptTarget(t.Context(), "", target, "reminder", nil)
	if err != nil || projection == nil || agent == nil {
		t.Fatalf("projection = %#v, agent = %#v, error = %v", projection, agent, err)
	}

	outboxRoot := t.TempDir()
	first, err := outbox.OpenCoordinator(outboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := first.AdmitMessage(agent.Workspace, outbox.Identity{
		SourceID: "restart-transcript", Channel: target.Channel, ChatID: target.ChatID,
		SessionKey: projection.SessionKey,
	}, bus.OutboundMessage{Content: "reminder", Transcript: projection})
	if err != nil {
		t.Fatal(err)
	}
	if err = first.PrepareAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = first.CommitAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = first.BeginAttempt(admission.Intent.ID); err != nil {
		t.Fatal(err)
	}
	if err = first.MarkDelivered(admission.Intent.ID, outbox.Outcome{}); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := outbox.OpenCoordinator(outboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	al.SetOutboundOutbox(second)
	recovered, err := second.Recover()
	if err != nil || len(recovered) != 1 || !recovered[0].Settle {
		t.Fatalf("Recover() = %#v, %v", recovered, err)
	}
	if err = al.SettleRecoveredOutboundAdmission(t.Context(), recovered[0]); err != nil {
		t.Fatal(err)
	}
	history := agent.Sessions.GetHistory(projection.SessionKey)
	if len(history) != 1 || history[0].Content != "reminder" ||
		history[0].OutboundDeliveryID != admission.Intent.ID {
		t.Fatalf("canonical history = %#v", history)
	}
	intent, err := second.Get(admission.Intent.ID)
	if err != nil || !intent.TranscriptProjected {
		t.Fatalf("settled intent = %#v, %v", intent, err)
	}
}

func TestScheduledSourceSessionProjectsIntoRoutedConversation(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	messageBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, messageBus, &recordingProvider{})
	target := bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "telegram:user-1",
	}
	targetProjection, agent, err := al.resolveOutboundTranscriptTarget(t.Context(), "", target, "reply", nil)
	if err != nil || targetProjection == nil || agent == nil {
		t.Fatalf("target projection = %#v, agent = %#v, error = %v", targetProjection, agent, err)
	}
	sourceSessionKey := session.BuildOpaqueSessionKey("cron|job=one|run=one")
	ensureSessionMetadata(
		agent.Sessions,
		sourceSessionKey,
		sessionScopeFromOutboundScope(targetProjection.Scope),
	)

	projection := al.transcriptProjectionFromSourceSession(
		agent,
		sourceSessionKey,
		"scheduled response",
		nil,
	)
	if projection == nil || projection.SessionKey != targetProjection.SessionKey {
		t.Fatalf("scheduled projection = %#v, target = %#v", projection, targetProjection)
	}

	al.PublishResponseIfNeeded(
		t.Context(),
		agent.Workspace,
		agent.ID,
		target.Channel,
		target.ChatID,
		sourceSessionKey,
		"scheduled response",
	)
	select {
	case outbound := <-messageBus.OutboundChan():
		if outbound.Transcript == nil || outbound.Transcript.SessionKey != targetProjection.SessionKey {
			t.Fatalf("scheduled outbound transcript = %#v", outbound.Transcript)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduled response was not published")
	}
}
