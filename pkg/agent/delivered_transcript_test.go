package agent

import (
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
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
