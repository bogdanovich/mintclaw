package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestQueuedInboundContinuationPreservesAuthorityOutsideSessionDimensions(t *testing.T) {
	for _, dimensions := range [][]string{nil, {"chat"}} {
		name := "shared_session"
		if len(dimensions) != 0 {
			name = "chat_session"
		}
		t.Run(name, func(t *testing.T) {
			provider := &sequenceProvider{responses: []*providers.LLMResponse{
				{ToolCalls: []providers.ToolCall{{ID: "initial-probe", Name: "approval_context"}}},
				{Content: "ready"},
				{ToolCalls: []providers.ToolCall{{ID: "continued-probe", Name: "approval_context"}}},
				{Content: "continued"},
			}}
			fixture := newAgentLoopTestFixture(t, provider, func(cfg *config.Config) {
				cfg.Session.Dimensions = dimensions
			})
			probe := &approvalContextTool{}
			fixture.Agent.Tools.Register(probe)
			initial := bus.NormalizeInboundMessage(bus.InboundMessage{
				Context: bus.InboundContext{
					Channel: "telegram", Account: "primary", ChatID: "pdf-chat", ChatType: "direct",
					TopicID: "pdf-topic", SpaceID: "team-a", SpaceType: "team",
					SenderID: "pdf-operator", ActorID: "pdf-operator", MessageID: "initial-message",
				},
				Content: "capture initial authority", SpoolID: "initial-source",
			})
			if _, err := fixture.Loop.processMessage(t.Context(), initial); err != nil {
				t.Fatal(err)
			}
			if probe.executions != 1 || probe.inbound.TopicID != initial.Context.TopicID {
				t.Fatalf("initial tool authority = %#v, executions %d", probe.inbound, probe.executions)
			}
			followup := initial
			followup.Content = "capture continued authority"
			followup.Context.MessageID = "followup-message"
			followup.Context.Raw = map[string]string{"thread_ts": "native-original-thread"}
			followup.Context.ReplyHandles = map[string]string{"telegram": "native-original-reply"}
			followup.SpoolID = "followup-source"
			target, err := fixture.Loop.resolveInboundDispatchTarget(followup)
			if err != nil {
				t.Fatal(err)
			}
			claim, claimed := fixture.Loop.turns.claimRuntimeSession(
				target.runtimeSessionScope(),
				"previous-final-handoff",
			)
			if !claimed {
				t.Fatal("could not hold previous final handoff")
			}
			t.Cleanup(claim.releaseIfOwned)
			coordinator := newInboundTurnCoordinator(fixture.Loop)
			if err = coordinator.admitPreparedInbound(t.Context(), followup, target); err != nil {
				t.Fatal(err)
			}
			followup.Context.Raw["thread_ts"] = "mutated"
			followup.Context.ReplyHandles["telegram"] = "mutated"
			claim.releaseIfOwned()
			_, err = fixture.Loop.Continue(
				t.Context(),
				fixture.Agent.Workspace,
				target.SessionKey,
				followup.Context.Channel,
				followup.Context.ChatID,
			)
			if err != nil {
				t.Fatal(err)
			}
			if probe.executions != 2 {
				t.Fatalf("tool executions = %d, want 2", probe.executions)
			}
			got := probe.inbound
			if got.Raw["thread_ts"] != "native-original-thread" ||
				got.ReplyHandles["telegram"] != "native-original-reply" {
				t.Fatal("queued execution lost or borrowed mutable reply metadata")
			}
			if !reflect.DeepEqual([]string{
				got.Channel, got.Account, got.ChatID, got.ChatType, got.TopicID, got.SpaceType, got.SpaceID,
				got.SenderID, got.ActorID, got.MessageID,
			}, []string{
				followup.Context.Channel, followup.Context.Account, followup.Context.ChatID, followup.Context.ChatType,
				followup.Context.TopicID, followup.Context.SpaceType, followup.Context.SpaceID,
				followup.Context.SenderID, followup.Context.ActorID, followup.Context.MessageID,
			}) {
				t.Fatalf("queued tool authority = %#v, want native followup %#v", got, followup.Context)
			}
		})
	}
}

func TestNativeSteeringKeepsDifferentAuthoritiesQueued(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &simpleMockProvider{response: "unused"})
	scope := newRuntimeSessionScope(fixture.Agent.Workspace, "shared-native-scope")
	first := bus.NormalizeInboundContext(bus.InboundContext{
		Channel: "telegram", Account: "primary", ChatID: "chat", ChatType: "group", TopicID: "topic-a",
		SenderID: "operator", ActorID: "actor-a", SpaceID: "space-a", SpaceType: "team",
		Raw:          map[string]string{"private": "private-native-context-canary"},
		ReplyHandles: map[string]string{"private": "private-native-context-canary"},
		Interaction:  bus.InboundInteractionProjection{Response: "private-native-context-canary"},
	})
	for _, dimension := range []string{"topic", "space", "actor", "chat"} {
		other := first
		switch dimension {
		case "topic":
			other.TopicID = "topic-b"
		case "space":
			other.SpaceID = "space-b"
		case "actor":
			other.ActorID = "actor-b"
		case "chat":
			other.ChatID = "other-chat"
		}
		if err := fixture.Loop.enqueueInboundSteering(scope, fixture.Agent.ID, bus.InboundMessage{
			Context: other, Content: dimension,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.Loop.enqueueInboundSteering(scope, fixture.Agent.ID, bus.InboundMessage{
		Context: first, Content: "matching",
	}); err != nil {
		t.Fatal(err)
	}
	selected := fixture.Loop.steering.dequeueSteeringMessagesForTurn(scope, first.SenderID, &first)
	if len(selected) != 1 || selected[0].msg.Content != "matching" {
		t.Fatalf("active turn consumed another authority: %#v", selected)
	}
	fixture.Loop.steering.returnSteeringMessagesForTurn(scope, selected, &first)
	first.TopicID = "mutated-after-return"
	for _, expected := range []string{"matching", "topic", "space", "actor", "chat"} {
		batch := fixture.Loop.steering.dequeueScopeForContinuationBatch(scope, nil)
		if len(batch.entries) != 1 || batch.entries[0].msg.Content != expected || batch.inbound == nil {
			t.Fatalf("authority cohort %q = %#v", expected, batch)
		}
		if batch.inbound.Raw["private"] != "private-native-context-canary" ||
			batch.inbound.ReplyHandles["private"] != "private-native-context-canary" ||
			batch.inbound.Interaction.Response != "private-native-context-canary" {
			t.Fatal("native execution metadata was changed")
		}
		encoded, err := json.Marshal(batch.entries[0].msg)
		if err != nil || strings.Contains(string(encoded), "private-native-context-canary") {
			t.Fatal("native context leaked into provider or canonical message data")
		}
		if expected == "matching" && batch.inbound.TopicID != "topic-a" {
			t.Fatal("returned queue authority was not frozen")
		}
	}
}

func TestReturnedSenderlessSteeringKeepsOneAtATimePolicyWithNativeContext(t *testing.T) {
	queue := newSteeringQueue(SteeringOneAtATime)
	scope := testSessionScope("senderless-native-context")
	inbound := bus.NormalizeInboundContext(bus.InboundContext{
		Channel: "telegram", ChatID: "chat", ChatType: "direct", SenderID: "owner",
	})
	queue.returnSteeringMessagesForTurn(scope, steeringEntriesFromMessages([]providers.Message{
		{Role: "user", Content: "first"}, {Role: "user", Content: "second"},
	}), &inbound)
	for _, expected := range []string{"first", "second"} {
		batch := queue.dequeueScopeForContinuationBatch(scope, &inbound)
		if len(batch.entries) != 1 || batch.entries[0].msg.Content != expected {
			t.Fatalf("one-at-a-time policy changed: %#v", batch)
		}
	}
}

func TestDeferredNativeSteeringPublishesEachDeliveryTargetSeparately(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{ID: "a-probe", Name: "approval_context"}}},
		{Content: "answer-a"},
		{ToolCalls: []providers.ToolCall{{ID: "b-probe", Name: "approval_context"}}},
		{Content: "answer-b"},
		{ToolCalls: []providers.ToolCall{{ID: "c-probe", Name: "approval_context"}}},
		{Content: "answer-c"},
	}}
	fixture := newAgentLoopTestFixture(t, provider)
	probe := &approvalContextTool{}
	fixture.Agent.Tools.Register(probe)
	scope := newRuntimeSessionScope(fixture.Agent.Workspace, "shared-delivery-scope")
	first := bus.NormalizeInboundContext(bus.InboundContext{
		Channel: "telegram", ChatID: "chat-a", ChatType: "direct", SenderID: "operator", ActorID: "operator",
	})
	for _, chat := range []string{"chat-a", "chat-b", "chat-c"} {
		inbound := first
		inbound.ChatID, inbound.MessageID = chat, chat+"-message"
		if err := fixture.Loop.enqueueInboundSteering(scope, fixture.Agent.ID, bus.InboundMessage{
			Context: inbound, Content: "input for " + chat,
		}); err != nil {
			t.Fatal(err)
		}
	}
	response, err := fixture.Loop.drainQueuedSteeringContinuations(t.Context(), &continuationTarget{
		Workspace: scope.workspace, SessionKey: scope.sessionKey, AgentID: fixture.Agent.ID,
		Channel: first.Channel, ChatID: first.ChatID, inbound: &first,
	})
	if err != nil || response != "answer-a" || probe.inbound.ChatID != "chat-a" {
		t.Fatalf("original target continuation = %q, %#v, %v", response, probe.inbound, err)
	}
	if depth := fixture.Loop.pendingSteeringCountForScope(scope); depth != 2 {
		t.Fatalf("other delivery targets consumed by original aggregate: depth %d", depth)
	}
	if err = fixture.Loop.drainDeferredInteractionIngress(t.Context(), scope.workspace, interactions.Route{
		AgentID: fixture.Agent.ID, SessionKey: scope.sessionKey, Channel: first.Channel, ChatID: first.ChatID,
	}, first); err != nil {
		t.Fatal(err)
	}
	for _, chat := range []string{"chat-b", "chat-c"} {
		select {
		case outbound := <-fixture.Bus.OutboundChan():
			if outbound.Context.ChatID != chat || outbound.Context.MessageID != chat+"-message" ||
				outbound.Content != "answer-"+chat[len(chat)-1:] {
				t.Fatalf("deferred response borrowed another target: %#v", outbound)
			}
		default:
			t.Fatalf("missing deferred response for %s", chat)
		}
	}
	if probe.executions != 3 || probe.inbound.ChatID != "chat-c" ||
		fixture.Loop.pendingSteeringCountForScope(scope) != 0 {
		t.Fatalf("deferred authorities incomplete: executions %d, last %#v", probe.executions, probe.inbound)
	}
}

func TestReturnedNativeSteeringPreservesFollowupContext(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &simpleMockProvider{response: "unused"})
	scope := newRuntimeSessionScope(fixture.Agent.Workspace, "native-suspension-return")
	root := bus.NormalizeInboundContext(bus.InboundContext{
		Channel: "telegram", ChatID: "chat", ChatType: "direct", SenderID: "owner", ActorID: "owner",
		MessageID: "root-event", SourceRef: "root-source", ReplyToMessageID: "root-reply",
		ReplyHandles: map[string]string{"native": "root-handle"}, Raw: map[string]string{"event": "root-raw"},
	})
	followup := *cloneInboundContext(&root)
	followup.MessageID, followup.SourceRef, followup.ReplyToMessageID = "followup-event", "followup-source", "followup-reply"
	followup.ReplyHandles["native"], followup.Raw["event"] = "followup-handle", "followup-raw"
	expected := cloneInboundContext(&followup)
	if err := fixture.Loop.enqueueInboundSteering(scope, fixture.Agent.ID, bus.InboundMessage{
		Context: followup, Content: "unspooled native followup",
	}); err != nil {
		t.Fatal(err)
	}
	spec := makeTestTurnSpec(scope.sessionKey)
	spec.Dispatch.InboundContext = &root
	ts := newTurnState(fixture.Agent, spec, turnEventScope{})
	pipeline := newTestPipeline(fixture.Loop)
	exec := newTurnExecution(fixture.Agent, ts.opts, nil, nil, nil)
	selected := pipeline.dequeueSteeringMessagesForTurn(ts)
	if len(selected) != 1 {
		t.Fatalf("selected = %#v", selected)
	}
	exec.pendingInputs.AppendSteeringEntries(selected...)
	followup.ReplyHandles["native"], followup.Raw["event"] = "mutated-handle", "mutated-raw"
	runner := &toolLoopRunner{p: pipeline, ts: ts, exec: exec}
	runner.transferPendingSteeringOwnership()
	if exec.pendingInputs.Len() != 0 {
		t.Fatal("returned input remained turn-owned")
	}
	resumed := fixture.Loop.steering.dequeueScopeForContinuationBatch(scope, nil)
	if len(resumed.entries) != 1 || !reflect.DeepEqual(resumed.inbound, expected) {
		t.Fatalf("returned native context = %#v, want %#v", resumed.inbound, expected)
	}
	encoded, err := json.Marshal(resumed.entries[0].msg)
	if err != nil || strings.Contains(string(encoded), "followup-handle") ||
		strings.Contains(string(encoded), "followup-raw") {
		t.Fatal("private native metadata entered provider or canonical data")
	}
}

func steeringEntriesFromMessages(messages []providers.Message) []steeringEntry {
	entries := make([]steeringEntry, 0, len(messages))
	for _, msg := range messages {
		entries = append(entries, steeringEntry{msg: msg, senderID: strings.TrimSpace(msg.SteeringSenderID)})
	}
	return entries
}
