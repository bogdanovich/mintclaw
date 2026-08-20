package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDecideAsyncToolResultDelivery(t *testing.T) {
	tests := []struct {
		name string
		in   *toolshared.ToolResult
		want AsyncDeliveryDecision
	}{
		{
			name: "nil defaults without routing",
			want: AsyncDeliveryDecision{
				DeliveryMode: toolshared.AsyncDeliveryUserAndParent,
			},
		},
		{
			name: "default routes user text and parent content",
			in: &toolshared.ToolResult{
				ForLLM:      "parent text",
				ForUser:     "user text",
				AsyncTaskID: "subagent-9",
			},
			want: AsyncDeliveryDecision{
				TaskID:        "subagent-9",
				DeliveryMode:  toolshared.AsyncDeliveryUserAndParent,
				PublishToUser: true,
				QueueParent:   true,
				ContentLen:    len("parent text"),
				ForUserLen:    len("user text"),
			},
		},
		{
			name: "user only suppresses parent",
			in: (&toolshared.ToolResult{
				ForLLM:  "parent text",
				ForUser: "user text",
			}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly),
			want: AsyncDeliveryDecision{
				DeliveryMode:  toolshared.AsyncDeliveryUserOnly,
				PublishToUser: true,
				QueueParent:   false,
				ParentHandled: true,
				ContentLen:    len("parent text"),
				ForUserLen:    len("user text"),
			},
		},
		{
			name: "parent only suppresses user",
			in: (&toolshared.ToolResult{
				ForLLM:  "parent text",
				ForUser: "user text",
			}).WithAsyncDelivery(toolshared.AsyncDeliveryParentOnly),
			want: AsyncDeliveryDecision{
				DeliveryMode:  toolshared.AsyncDeliveryParentOnly,
				PublishToUser: false,
				QueueParent:   true,
				ContentLen:    len("parent text"),
				ForUserLen:    len("user text"),
			},
		},
		{
			name: "silent suppresses user but not parent",
			in: (&toolshared.ToolResult{
				ForLLM:  "parent text",
				ForUser: "user text",
				Silent:  true,
			}).WithAsyncDelivery(toolshared.AsyncDeliveryUserAndParent),
			want: AsyncDeliveryDecision{
				DeliveryMode:  toolshared.AsyncDeliveryUserAndParent,
				PublishToUser: false,
				QueueParent:   true,
				ContentLen:    len("parent text"),
				ForUserLen:    len("user text"),
			},
		},
		{
			name: "media counts direct and completion media",
			in: (&toolshared.ToolResult{
				ForLLM: "parent text",
				Media:  []string{"media://direct"},
				Completion: &toolshared.CompletionResult{
					Media: []toolshared.CompletionMedia{
						{Ref: "media://completion-1"},
						{Ref: "media://completion-2"},
					},
				},
			}).WithAsyncDelivery(toolshared.AsyncDeliveryParentOnly),
			want: AsyncDeliveryDecision{
				DeliveryMode: toolshared.AsyncDeliveryParentOnly,
				QueueParent:  true,
				ContentLen:   -1,
				MediaCount:   3,
			},
		},
		{
			name: "user only media publishes without user text",
			in: (&toolshared.ToolResult{
				ForLLM: "internal media result",
				Completion: &toolshared.CompletionResult{
					Media: []toolshared.CompletionMedia{{Ref: "media://completion-video"}},
				},
			}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly),
			want: AsyncDeliveryDecision{
				DeliveryMode:  toolshared.AsyncDeliveryUserOnly,
				PublishToUser: true,
				QueueParent:   false,
				ParentHandled: true,
				ContentLen:    -1,
				ForUserLen:    0,
				MediaCount:    1,
			},
		},
		{
			name: "user only durable deliverable publishes even when silent",
			in: (&toolshared.ToolResult{
				ForLLM:      "internal research envelope",
				Silent:      true,
				Deliverable: &toolshared.DeliverableResult{Text: "complete research report"},
			}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly),
			want: AsyncDeliveryDecision{
				DeliveryMode:  toolshared.AsyncDeliveryUserOnly,
				PublishToUser: true,
				QueueParent:   false,
				ParentHandled: true,
				ContentLen:    -1,
				ForUserLen:    len("complete research report"),
			},
		},
		{
			name: "error is surfaced in decision",
			in: (&toolshared.ToolResult{
				ForLLM:  "failed",
				ForUser: "failed",
				IsError: true,
			}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly),
			want: AsyncDeliveryDecision{
				DeliveryMode:  toolshared.AsyncDeliveryUserOnly,
				PublishToUser: true,
				ContentLen:    len("failed"),
				ForUserLen:    len("failed"),
				IsError:       true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.want.ContentLen == -1 {
				tt.want.ContentLen = len(tt.in.ContentForLLM())
			}
			got := decideAsyncToolResultDelivery(tt.in)
			if got != tt.want {
				t.Fatalf("decision = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAsyncToolCompletionDelivery_UsesCurrentConfigForFiltering(t *testing.T) {
	oldCfg := &config.Config{}
	newCfg := &config.Config{
		ModelList: config.SecureModelList{
			&config.ModelConfig{
				ModelName: "current",
				APIKeys:   config.SimpleSecureStrings("sk-new-secret-token"),
			},
		},
	}
	newCfg.Tools.FilterSensitiveData = true
	newCfg.Tools.FilterMinLength = 8

	currentCfg := oldCfg
	var gotInput AsyncCompletionInput
	delivery := &asyncToolCompletionDelivery{
		currentConfig: func() *config.Config {
			return currentCfg
		},
		processCompletion: func(_ context.Context, input AsyncCompletionInput) (string, error) {
			gotInput = input
			return "ok", nil
		},
	}

	currentCfg = newCfg
	delivery.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState: &turnState{
			channel: "telegram",
			chatID:  "chat-1",
		},
		ToolName:     "spawn",
		CompletionID: "completion-1",
		Result: (&toolshared.ToolResult{
			ForLLM: "result includes sk-new-secret-token and should be filtered",
		}).WithAsyncDelivery(toolshared.AsyncDeliveryParentOnly),
	})

	if strings.Contains(gotInput.Content, "sk-new-secret-token") {
		t.Fatalf("async completion content leaked stale secret: %q", gotInput.Content)
	}
	if !strings.Contains(gotInput.Content, "[FILTERED]") {
		t.Fatalf("async completion content was not filtered with current config: %q", gotInput.Content)
	}
}

func TestDeliverAsyncToolCompletion_UserOnlyUpdatesDelivered(t *testing.T) {
	al, msgBus, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "coordinator-user-only"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM:      "internal",
		ForUser:     "user done",
		AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)

	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "spawn",
		CompletionID: "completion-user-only",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	})

	outbound := waitForOutboundMessage(t, msgBus.OutboundChan(), 2*time.Second, func(msg bus.OutboundMessage) bool {
		return msg.Content == "user done"
	})
	if outbound.Context.TopicID != "topic-1" {
		t.Fatalf("TopicID = %q, want topic-1", outbound.Context.TopicID)
	}
	metadata := bus.OutboundMetadataFromMessage(outbound)
	if metadata.OutboundKind != bus.OutboundKindFinal ||
		metadata.MessageKind != bus.OutboundMessageKindFinalReply {
		t.Fatalf("outbound metadata = %+v, want final/final_reply", metadata)
	}
	assertTaskDeliveryStatusForTest(t, al, workspace, taskID, taskregistry.DeliveryDelivered)
	rec, _ := al.taskRegistryForWorkspace(workspace).Get(taskID)
	if rec.LastCompletionID != "completion-user-only" {
		t.Fatalf("LastCompletionID = %q, want completion-user-only", rec.LastCompletionID)
	}
	if rec.DeliveredAt == 0 {
		t.Fatal("DeliveredAt was not set")
	}
	history := ts.agent.Sessions.GetHistory(ts.sessionKey)
	if len(history) != 1 || history[0].Role != "assistant" ||
		!strings.Contains(history[0].Content, "[Background task completion: ") ||
		!strings.Contains(history[0].Content, "task_id: "+taskID) ||
		!strings.Contains(history[0].Content, "This task is no longer running.") {
		t.Fatalf("completion observation = %#v", history)
	}
	events := al.taskRegistryForWorkspace(workspace).ListEvents(taskID)
	assertTaskEventForTest(t, events, taskregistry.EventTaskDeliveryDecision, map[string]string{
		"completion_id": "completion-user-only",
		"source_tool":   "spawn",
		"mode":          string(toolshared.AsyncDeliveryUserOnly),
		"will_user":     "true",
		"will_parent":   "false",
	})
}

func TestDeliverAsyncToolCompletion_ParentOnlyUpdatesSessionQueued(t *testing.T) {
	al, msgBus, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "parent synthesized")
	taskID := "coordinator-parent-only"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM:      "parent data",
		ForUser:     "do not send",
		AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryParentOnly)

	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "delegate",
		CompletionID: "completion-parent-only",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	})

	waitForOutboundMessage(t, msgBus.OutboundChan(), 2*time.Second, func(msg bus.OutboundMessage) bool {
		return msg.Content == "parent synthesized"
	})
	assertTaskDeliveryStatusForTest(t, al, workspace, taskID, taskregistry.DeliverySessionQueued)
	assertNoSyntheticAsyncCompletionInbound(t, msgBus)
}

func TestDeliverAsyncToolCompletion_UserAndParentDeliversBothOnce(t *testing.T) {
	al, msgBus, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "parent synthesized")
	taskID := "coordinator-user-and-parent"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM:      "parent data",
		ForUser:     "user visible",
		AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserAndParent)
	req := AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "spawn",
		CompletionID: "same-user-and-parent-completion",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	}

	al.deliverAsyncToolCompletion(req)
	userOutbound := waitForOutboundMessage(t, msgBus.OutboundChan(), 2*time.Second, func(msg bus.OutboundMessage) bool {
		return msg.Content == "user visible"
	})
	if metadata := bus.OutboundMetadataFromMessage(userOutbound); metadata.OutboundKind == bus.OutboundKindFinal {
		t.Fatalf("user_and_parent outbound metadata = %+v, must not be terminal", metadata)
	}
	waitForOutboundMessage(t, msgBus.OutboundChan(), 2*time.Second, func(msg bus.OutboundMessage) bool {
		return msg.Content == "parent synthesized"
	})
	assertTaskDeliveryStatusForTest(t, al, workspace, taskID, taskregistry.DeliveryDelivered)
	assertNoSyntheticAsyncCompletionInbound(t, msgBus)

	reloaded, reloadedBus, reloadedTS, _ := newDeliveryCoordinatorTestRuntimeWithWorkspace(
		t,
		workspace,
		"parent duplicate",
	)
	req.TurnState = reloadedTS
	reloaded.deliverAsyncToolCompletion(req)
	assertNoOutboundMessage(t, reloadedBus, "duplicate user_and_parent delivery")
	assertTaskDeliveryStatusForTest(t, reloaded, workspace, taskID, taskregistry.DeliveryDelivered)
}

func TestDeliverAsyncToolCompletion_SkipsDuplicateUserDelivery(t *testing.T) {
	al, msgBus, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "coordinator-duplicate-user"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM:      "internal",
		ForUser:     "user once",
		AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	req := AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "spawn",
		CompletionID: "same-completion",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	}

	al.deliverAsyncToolCompletion(req)
	waitForOutboundMessage(t, msgBus.OutboundChan(), 2*time.Second, func(msg bus.OutboundMessage) bool {
		return msg.Content == "user once"
	})
	al.deliverAsyncToolCompletion(req)
	assertNoOutboundMessage(t, msgBus, "duplicate user delivery")
	history := ts.agent.Sessions.GetHistory(ts.sessionKey)
	if len(history) != 1 {
		t.Fatalf("completion observation count = %d, want 1", len(history))
	}
}

func TestDeliverAsyncToolCompletion_RecordsBlockedObjectiveAsTerminal(t *testing.T) {
	al, msgBus, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "coordinator-blocked-objective"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM:      "Task could not be completed: checklist missing",
		ForUser:     "Task could not be completed: checklist missing",
		AsyncTaskID: taskID,
		Completion: &toolshared.CompletionResult{ObjectiveOutcome: &toolshared.ObjectiveOutcome{
			Status:       toolshared.ObjectiveOutcomeBlocked,
			MissingItems: []string{"checklist"},
		}},
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)

	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "spawn",
		CompletionID: "blocked-completion",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	})
	waitForOutboundMessage(t, msgBus.OutboundChan(), 2*time.Second, func(msg bus.OutboundMessage) bool {
		return strings.Contains(msg.Content, "checklist missing")
	})
	history := ts.agent.Sessions.GetHistory(ts.sessionKey)
	if len(history) != 1 || !strings.Contains(history[0].Content, "state: blocked") ||
		!strings.Contains(history[0].Content, "This task is no longer running.") {
		t.Fatalf("blocked completion observation = %#v", history)
	}
}

func TestAsyncTaskCompletionObservationIsVisibleToNextRequest(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: workspace, ModelName: "test-model", MaxTokens: 4096,
	}}}
	provider := &recordingProvider{}
	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, provider)
	agent := al.registry.GetDefaultAgent()
	sessionKey := session.BuildOpaqueSessionKey("agent:main:test:repeat-request")
	ts := &turnState{
		agent: agent, agentID: agent.ID, workspace: workspace, channel: "telegram", chatID: "chat-1",
		sessionKey: sessionKey,
		opts: processOptions{Dispatch: DispatchRequest{SessionKey: sessionKey, InboundContext: &bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct",
		}}},
	}
	taskID := "repeat-request-task"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM: "Task could not be completed: checklist missing", ForUser: "checklist missing", AsyncTaskID: taskID,
		Completion: &toolshared.CompletionResult{ObjectiveOutcome: &toolshared.ObjectiveOutcome{
			Status: toolshared.ObjectiveOutcomeBlocked,
		}},
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState: ts, ToolName: "spawn", CompletionID: "repeat-completion", Result: result,
	})
	waitForOutboundMessage(t, msgBus.OutboundChan(), 2*time.Second, func(msg bus.OutboundMessage) bool {
		return msg.Content == "checklist missing"
	})

	if _, err := al.runAgentLoop(t.Context(), agent, processOptions{Dispatch: DispatchRequest{
		SessionKey: sessionKey, UserMessage: "Run the Craigslist check again.", InboundContext: &bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
	}, DefaultResponse: "default", SendResponse: false}); err != nil {
		t.Fatalf("next request failed: %v", err)
	}
	var prompt strings.Builder
	for _, message := range provider.lastMessages {
		prompt.WriteString(message.Content)
		prompt.WriteByte('\n')
	}
	if !strings.Contains(prompt.String(), "state: blocked") ||
		!strings.Contains(prompt.String(), "This task is no longer running.") ||
		!strings.Contains(prompt.String(), "a terminal task does not satisfy a new request") {
		t.Fatalf("next request prompt omitted terminal observation: %s", prompt.String())
	}
}

func TestAsyncTaskCompletionObservationUsesRequesterAgentSession(t *testing.T) {
	ownerWorkspace := t.TempDir()
	childWorkspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "test-model"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true, Workspace: ownerWorkspace},
		{ID: "browser", Workspace: childWorkspace},
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "ok"})
	owner := al.registry.GetDefaultAgent()
	child, ok := al.registry.GetAgent("browser")
	if owner == nil || !ok {
		t.Fatal("expected owner and browser agents")
	}
	taskID := "cross-agent-completion"
	upsertAsyncTaskForTest(t, al, ownerWorkspace, taskID)
	if err := al.taskRegistryForWorkspace(ownerWorkspace).Update(taskID, func(record *taskregistry.Record) {
		record.OwnerKey = owner.ID
		record.RequesterSessionKey = "owner-session"
		record.AgentID = child.ID
	}); err != nil {
		t.Fatal(err)
	}
	ts := &turnState{
		agent: child, agentID: child.ID, workspace: ownerWorkspace, sessionKey: "child-session",
		opts: processOptions{Dispatch: DispatchRequest{SessionKey: "child-session"}},
	}
	result := (&toolshared.ToolResult{
		ForLLM: "browser finished", ForUser: "browser finished", AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState: ts, ToolName: "spawn", CompletionID: "cross-agent-result", Result: result,
	})

	ownerHistory := owner.Sessions.GetHistory("owner-session")
	if len(ownerHistory) != 1 || !strings.Contains(ownerHistory[0].Content, "browser finished") {
		t.Fatalf("owner history = %#v", ownerHistory)
	}
	if childHistory := child.Sessions.GetHistory("child-session"); len(childHistory) != 0 {
		t.Fatalf("child history = %#v, want empty", childHistory)
	}
}

func TestAsyncTaskCompletionObservationFiltersSensitiveContent(t *testing.T) {
	al, _, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	al.cfg = &config.Config{
		ModelList: config.SecureModelList{&config.ModelConfig{
			ModelName: "secret-model",
			APIKeys:   config.SecureStrings{config.NewSecureString("sk-secret-value-123")},
		}},
		Tools: config.ToolsConfig{FilterSensitiveData: true, FilterMinLength: 8},
	}
	if filtered := al.cfg.FilterSensitiveData("sk-secret-value-123"); filtered != "[FILTERED]" {
		t.Fatalf("test config did not enable filtering: %q", filtered)
	}
	taskID := "filtered-completion"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM: "result has sk-secret-value-123", ForUser: "done", AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState: ts, ToolName: "spawn", CompletionID: "filtered-result", Result: result,
	})

	history := ts.agent.Sessions.GetHistory(ts.sessionKey)
	if len(history) != 1 || strings.Contains(history[0].Content, "sk-secret-value-123") ||
		!strings.Contains(history[0].Content, "[FILTERED]") {
		t.Fatalf("filtered history = %#v", history)
	}
}

func TestAsyncTaskCompletionObservationSkipsNoHistoryTurn(t *testing.T) {
	al, _, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	ts.opts.NoHistory = true
	taskID := "stateless-completion"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM: "must stay stateless", ForUser: "done", AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState: ts, ToolName: "spawn", CompletionID: "stateless-result", Result: result,
	})
	if history := ts.agent.Sessions.GetHistory(ts.sessionKey); len(history) != 0 {
		t.Fatalf("stateless history = %#v, want empty", history)
	}
}

func TestAsyncTaskCompletionObservationUsesDurableNoHistoryPolicy(t *testing.T) {
	al, _, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "resumed-stateless-completion"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	if err := al.taskRegistryForWorkspace(workspace).Update(taskID, func(record *taskregistry.Record) {
		record.HistoryDisabled = true
	}); err != nil {
		t.Fatal(err)
	}
	result := (&toolshared.ToolResult{
		ForLLM: "must remain stateless after resume", ForUser: "done", AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState: ts, ToolName: "spawn", CompletionID: "resumed-stateless-result", Result: result,
	})
	if history := ts.agent.Sessions.GetHistory(ts.sessionKey); len(history) != 0 {
		t.Fatalf("resumed stateless history = %#v, want empty", history)
	}
}

func TestAsyncTaskCompletionObservationSkipsLegacyRecordWithoutHistoryPolicy(t *testing.T) {
	al, _, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "legacy-completion"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	if err := al.taskRegistryForWorkspace(workspace).Update(taskID, func(record *taskregistry.Record) {
		record.HistoryPolicyKnown = false
		record.HistoryDisabled = false
		record.OwnerKey = ""
		record.RequesterSessionKey = ""
	}); err != nil {
		t.Fatal(err)
	}
	result := (&toolshared.ToolResult{
		ForLLM: "legacy result must not enter history", AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	if err := al.recordAsyncTaskCompletionObservation(t.Context(), ts, "legacy-result", result); err != nil {
		t.Fatal(err)
	}
	if history := ts.agent.Sessions.GetHistory(ts.sessionKey); len(history) != 0 {
		t.Fatalf("legacy fallback history = %#v, want empty", history)
	}
}

func TestAsyncTaskCompletionObservationFailsClosedForMissingOwner(t *testing.T) {
	al, _, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "missing-owner-completion"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	if err := al.taskRegistryForWorkspace(workspace).Update(taskID, func(record *taskregistry.Record) {
		record.RequesterSessionKey = "requester-session"
		record.OwnerKey = "removed-agent"
	}); err != nil {
		t.Fatal(err)
	}
	result := (&toolshared.ToolResult{
		ForLLM: "must not enter child history", AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	err := al.recordAsyncTaskCompletionObservation(t.Context(), ts, "missing-owner-result", result)
	if err == nil || !strings.Contains(err.Error(), "requester owner") {
		t.Fatalf("error = %v, want unresolved requester owner", err)
	}
	if history := ts.agent.Sessions.GetHistory("requester-session"); len(history) != 0 {
		t.Fatalf("fallback child history = %#v, want empty", history)
	}
}

func TestAsyncTaskCompletionObservationBoundsAndSanitizesIdentifiers(t *testing.T) {
	al, _, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "task\nforged_state: running" + strings.Repeat("x", 3000)
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM: strings.Repeat("result", 1000), AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	if err := al.recordAsyncTaskCompletionObservation(
		t.Context(), ts, "completion\nforged: active"+strings.Repeat("y", 3000), result,
	); err != nil {
		t.Fatal(err)
	}
	history := ts.agent.Sessions.GetHistory(ts.sessionKey)
	if len(history) != 1 {
		t.Fatalf("history = %#v", history)
	}
	if got := len([]rune(history[0].Content)); got > asyncTaskObservationResultLimit {
		t.Fatalf("observation length = %d, limit = %d", got, asyncTaskObservationResultLimit)
	}
	if strings.Contains(history[0].Content, "\nforged_state:") ||
		strings.Contains(history[0].Content, "\nforged:") {
		t.Fatalf("identifier injected observation fields: %q", history[0].Content)
	}
}

func TestToolExecutionContextCarriesNoHistoryPolicy(t *testing.T) {
	ts := &turnState{
		agent: &AgentInstance{ID: "main"}, sessionKey: "session-1", workspace: t.TempDir(),
		opts: processOptions{NoHistory: true},
	}
	ctx := toolExecutionContextForTurn(t.Context(), ts)
	if !toolshared.ToolHistoryDisabled(ctx) {
		t.Fatal("tool context omitted NoHistory policy")
	}
}

func TestAsyncTaskCompletionObservationRetriesAfterDeliveryHandled(t *testing.T) {
	attempts := 0
	delivery := &asyncToolCompletionDelivery{
		recordCompletionObservation: func(context.Context, *turnState, string, *toolshared.ToolResult) error {
			attempts++
			if attempts == 1 {
				return errors.New("transient history failure")
			}
			return nil
		},
		asyncTaskDeliveryAlreadyHandled: func(string, string, string) bool { return true },
	}
	req := AsyncDeliveryRequest{
		TurnState: &turnState{workspace: t.TempDir()}, ToolName: "spawn", CompletionID: "retry-result",
		Result: (&toolshared.ToolResult{ForLLM: "done", AsyncTaskID: "retry-task"}).
			WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly),
	}
	delivery.deliverAsyncToolCompletion(req)
	delivery.deliverAsyncToolCompletion(req)
	if attempts != 2 {
		t.Fatalf("observation attempts = %d, want 2", attempts)
	}
}

func TestAsyncTaskCompletionObservationWaitsForImmediateMultiToolResults(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "run both tools"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{
			{ID: "spawn-call", Name: "spawn"},
			{ID: "sibling-call", Name: "read_file"},
		}},
	}
	marker := asyncTaskObservationMarker("fast-task", "fast-completion")
	observation := marker + "\nstate: succeeded"
	deferred, changed, err := appendAsyncTaskCompletionObservation(history, marker, observation)
	if !errors.Is(err, errAsyncTaskObservationToolBlockOpen) || changed || len(deferred) != len(history) {
		t.Fatalf("open block mutation = (%d, %v, %v), want unchanged deferred history", len(deferred), changed, err)
	}
	history = append(history,
		providers.Message{Role: "tool", ToolCallID: "spawn-call", Content: "accepted"},
		providers.Message{Role: "tool", ToolCallID: "sibling-call", Content: "read"},
	)
	completed, changed, err := appendAsyncTaskCompletionObservation(history, marker, observation)
	if err != nil || !changed || len(completed) != len(history)+1 {
		t.Fatalf("closed block mutation = (%d, %v, %v)", len(completed), changed, err)
	}
	sanitized := sanitizeHistoryForProvider(completed)
	if len(sanitized) != len(completed) {
		t.Fatalf("provider sanitizer dropped multi-tool history: got %#v", sanitized)
	}
	if sanitized[1].Role != "assistant" || len(sanitized[1].ToolCalls) != 2 ||
		sanitized[2].ToolCallID != "spawn-call" || sanitized[3].ToolCallID != "sibling-call" ||
		!strings.HasPrefix(sanitized[4].Content, marker) {
		t.Fatalf("multi-tool block order = %#v", sanitized)
	}
}

func TestDeliverAsyncToolCompletion_SkipsDuplicateParentDeliveryAfterReload(t *testing.T) {
	al, msgBus, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "parent once")
	taskID := "coordinator-duplicate-parent"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM:      "parent data",
		ForUser:     "do not send",
		AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryParentOnly)
	req := AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "delegate",
		CompletionID: "same-parent-completion",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	}

	al.deliverAsyncToolCompletion(req)
	waitForOutboundMessage(t, msgBus.OutboundChan(), 2*time.Second, func(msg bus.OutboundMessage) bool {
		return msg.Content == "parent once"
	})

	reloaded, reloadedBus, reloadedTS, _ := newDeliveryCoordinatorTestRuntimeWithWorkspace(
		t,
		workspace,
		"parent duplicate",
	)
	req.TurnState = reloadedTS
	reloaded.deliverAsyncToolCompletion(req)
	assertNoOutboundMessage(t, reloadedBus, "duplicate parent delivery")
	assertTaskDeliveryStatusForTest(t, reloaded, workspace, taskID, taskregistry.DeliverySessionQueued)
}

func TestDeliverAsyncToolCompletion_SkipsDuplicateMediaAfterReload(t *testing.T) {
	al, msgBus, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "coordinator-duplicate-media"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM:      "internal media result",
		AsyncTaskID: taskID,
		Completion: &toolshared.CompletionResult{
			Text: "https://example.com/reel",
			Media: []toolshared.CompletionMedia{{
				Ref:         "media://video-1",
				Type:        "video",
				Filename:    "source.mp4",
				ContentType: "video/mp4",
			}},
		},
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)
	req := AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "spawn",
		CompletionID: "same-media-completion",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	}

	al.deliverAsyncToolCompletion(req)
	media := waitForOutboundMediaMessage(t, msgBus.OutboundMediaChan(), 2*time.Second)
	if len(media.Parts) != 1 || media.Parts[0].Ref != "media://video-1" {
		t.Fatalf("media parts = %+v, want media://video-1", media.Parts)
	}
	metadata := bus.OutboundMetadataFromContext(media.Context)
	if metadata.OutboundKind != bus.OutboundKindFinal ||
		metadata.MessageKind != bus.OutboundMessageKindFinalReply {
		t.Fatalf("media outbound metadata = %+v, want final/final_reply", metadata)
	}
	assertTaskDeliveryStatusForTest(t, al, workspace, taskID, taskregistry.DeliveryDelivered)

	reloaded, reloadedBus, reloadedTS, _ := newDeliveryCoordinatorTestRuntimeWithWorkspace(
		t,
		workspace,
		"duplicate should not synthesize",
	)
	req.TurnState = reloadedTS
	reloaded.deliverAsyncToolCompletion(req)
	assertNoOutboundMediaMessage(t, reloadedBus, "duplicate media delivery after reload")
	assertNoOutboundMessage(t, reloadedBus, "duplicate media parent synthesis after reload")
	assertTaskDeliveryStatusForTest(t, reloaded, workspace, taskID, taskregistry.DeliveryDelivered)
}

func TestDeliverAsyncToolCompletion_MediaDeliveryFailureRecordsFailed(t *testing.T) {
	al, _, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "coordinator-media-failed"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM:      "internal media result",
		AsyncTaskID: taskID,
		Completion: &toolshared.CompletionResult{
			Media: []toolshared.CompletionMedia{{
				Ref:         "media://video-fail",
				Type:        "video",
				Filename:    "source.mp4",
				ContentType: "video/mp4",
			}},
		},
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)

	al.bus = failingMessageBus{}
	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "spawn",
		CompletionID: "failed-media-completion",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	})

	rec, ok := al.taskRegistryForWorkspace(workspace).Get(taskID)
	if !ok {
		t.Fatal("expected task")
	}
	if rec.DeliveryStatus != taskregistry.DeliveryFailed {
		t.Fatalf("DeliveryStatus = %q, want failed", rec.DeliveryStatus)
	}
	if rec.LastCompletionID != "failed-media-completion" {
		t.Fatalf("LastCompletionID = %q, want failed-media-completion", rec.LastCompletionID)
	}
	if !strings.Contains(rec.DeliveryError, "publish failed") {
		t.Fatalf("DeliveryError = %q, want publish failed", rec.DeliveryError)
	}
}

func newDeliveryCoordinatorTestRuntime(
	t *testing.T,
	response string,
) (*AgentLoop, *bus.MessageBus, *turnState, string) {
	t.Helper()
	workspace := t.TempDir()
	return newDeliveryCoordinatorTestRuntimeWithWorkspace(t, workspace, response)
}

func newDeliveryCoordinatorTestRuntimeWithWorkspace(
	t *testing.T,
	workspace string,
	response string,
) (*AgentLoop, *bus.MessageBus, *turnState, string) {
	t.Helper()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "test-model",
				MaxTokens: 4096,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, &simpleMockProvider{response: response})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	inbound := &bus.InboundContext{
		Channel:  "telegram",
		ChatID:   "chat-1",
		ChatType: "direct",
		TopicID:  "topic-1",
		SenderID: "user-1",
	}
	ts := &turnState{
		agent:      agent,
		agentID:    agent.ID,
		workspace:  workspace,
		channel:    "telegram",
		chatID:     "chat-1",
		sessionKey: "session-1",
		opts: processOptions{
			Dispatch: DispatchRequest{
				SessionKey:     "session-1",
				InboundContext: inbound,
			},
		},
		scope: al.newTurnEventScope(agent.ID, agent.Workspace, "session-1", &TurnContext{Inbound: inbound}),
	}
	return al, msgBus, ts, workspace
}

func assertNoOutboundMessage(t *testing.T, msgBus *bus.MessageBus, context string) {
	t.Helper()
	select {
	case msg := <-msgBus.OutboundChan():
		t.Fatalf("unexpected outbound during %s: %+v", context, msg)
	case <-time.After(150 * time.Millisecond):
	}
}

func waitForOutboundMediaMessage(
	t *testing.T,
	ch <-chan bus.OutboundMediaMessage,
	timeout time.Duration,
) bus.OutboundMediaMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatal("timeout waiting for outbound media message")
	}
	return bus.OutboundMediaMessage{}
}

func assertNoOutboundMediaMessage(t *testing.T, msgBus *bus.MessageBus, context string) {
	t.Helper()
	select {
	case msg := <-msgBus.OutboundMediaChan():
		t.Fatalf("unexpected outbound media during %s: %+v", context, msg)
	case <-time.After(150 * time.Millisecond):
	}
}

func assertTaskEventForTest(
	t *testing.T,
	events []taskregistry.TaskEvent,
	eventType taskregistry.EventType,
	payload map[string]string,
) {
	t.Helper()
	for _, evt := range events {
		if evt.Type != eventType {
			continue
		}
		for key, want := range payload {
			if got := evt.Payload[key]; got != want {
				t.Fatalf("event %s payload[%s] = %q, want %q; event=%+v", eventType, key, got, want, evt)
			}
		}
		return
	}
	t.Fatalf("event %s not found in %+v", eventType, events)
}

type failingMessageBus struct{}

func (failingMessageBus) PublishInbound(context.Context, bus.InboundMessage) error {
	return errors.New("publish failed")
}

func (failingMessageBus) AckInbound(context.Context, bus.InboundMessage) error {
	return nil
}

func (failingMessageBus) ReleaseInbound(context.Context, bus.InboundMessage, error) error {
	return nil
}

func (failingMessageBus) PendingInboundSpool(context.Context) ([]bus.InboundMessage, error) {
	return nil, nil
}

func (failingMessageBus) PublishObserved(context.Context, bus.ObservedMessage) error {
	return errors.New("publish failed")
}

func (failingMessageBus) PublishOutbound(context.Context, bus.OutboundMessage) error {
	return errors.New("publish failed")
}

func (failingMessageBus) PublishOutboundMedia(context.Context, bus.OutboundMediaMessage) error {
	return errors.New("publish failed")
}

func (failingMessageBus) GetStreamer(
	context.Context,
	string,
	string,
	string,
	string,
	runtimeevents.TraceScope,
) (bus.Streamer, bool) {
	return nil, false
}

func (failingMessageBus) InboundChan() <-chan bus.InboundMessage {
	return nil
}

func (failingMessageBus) ObservedChan() <-chan bus.ObservedMessage {
	return nil
}

func TestDeliverAsyncToolCompletion_FailedDeliveryRecordsCompletionError(t *testing.T) {
	al, _, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "coordinator-failed"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM:      "internal",
		ForUser:     "user fail",
		AsyncTaskID: taskID,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)

	al.bus = failingMessageBus{}
	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "spawn",
		CompletionID: "failed-completion",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	})

	rec, ok := al.taskRegistryForWorkspace(workspace).Get(taskID)
	if !ok {
		t.Fatal("expected task")
	}
	if rec.DeliveryStatus != taskregistry.DeliveryFailed {
		t.Fatalf("DeliveryStatus = %q, want failed", rec.DeliveryStatus)
	}
	if rec.LastCompletionID != "failed-completion" {
		t.Fatalf("LastCompletionID = %q, want failed-completion", rec.LastCompletionID)
	}
	if !strings.Contains(rec.DeliveryError, "publish failed") {
		t.Fatalf("DeliveryError = %q, want publish failed", rec.DeliveryError)
	}
}

func TestDeliverAsyncToolCompletion_UserOnlyDurableDeliverableIsDelivered(t *testing.T) {
	al, msgBus, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "coordinator-durable-report"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	fullReport := strings.Repeat("research result ", 700)
	result := (&toolshared.ToolResult{
		ForLLM:      "internal research envelope",
		Silent:      true,
		AsyncTaskID: taskID,
		Deliverable: &toolshared.DeliverableResult{Text: fullReport},
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)

	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "spawn",
		CompletionID: "durable-report-completion",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	})

	waitForOutboundMessage(t, msgBus.OutboundChan(), 2*time.Second, func(msg bus.OutboundMessage) bool {
		return msg.Content == fullReport
	})
	assertTaskDeliveryStatusForTest(t, al, workspace, taskID, taskregistry.DeliveryDelivered)
}

func TestDeliverAsyncToolCompletion_ErrorDeliveryUpdatesTaskStatus(t *testing.T) {
	al, msgBus, ts, workspace := newDeliveryCoordinatorTestRuntime(t, "ok")
	taskID := "coordinator-error-delivered"
	upsertAsyncTaskForTest(t, al, workspace, taskID)
	result := (&toolshared.ToolResult{
		ForLLM:      "internal error",
		ForUser:     "user error",
		AsyncTaskID: taskID,
		IsError:     true,
	}).WithAsyncDelivery(toolshared.AsyncDeliveryUserOnly)

	al.deliverAsyncToolCompletion(AsyncDeliveryRequest{
		TurnState:    ts,
		ToolName:     "spawn",
		CompletionID: "error-completion",
		Result:       result,
		Decision:     decideAsyncToolResultDelivery(result),
	})

	errorOutbound := waitForOutboundMessage(
		t,
		msgBus.OutboundChan(),
		2*time.Second,
		func(msg bus.OutboundMessage) bool {
			return msg.Content == "user error"
		},
	)
	metadata := bus.OutboundMetadataFromMessage(errorOutbound)
	if metadata.OutboundKind != bus.OutboundKindFinal ||
		metadata.MessageKind != bus.OutboundMessageKindFinalReply {
		t.Fatalf("error outbound metadata = %+v, want final/final_reply", metadata)
	}
	assertTaskDeliveryStatusForTest(t, al, workspace, taskID, taskregistry.DeliveryDelivered)
	rec, _ := al.taskRegistryForWorkspace(workspace).Get(taskID)
	if rec.LastCompletionID != "error-completion" {
		t.Fatalf("LastCompletionID = %q, want error-completion", rec.LastCompletionID)
	}
}
