package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type externalWaitTestTool struct {
	state          atomic.Value
	executions     int
	cleanups       int
	executionID    string
	cleanupErr     error
	handoff        bool
	invalidContext atomic.Bool
}

func (*externalWaitTestTool) Name() string               { return "external_test_wait" }
func (*externalWaitTestTool) Description() string        { return "Wait for a trusted external condition" }
func (*externalWaitTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (tool *externalWaitTestTool) Execute(ctx context.Context, _ map[string]any) *toolshared.ToolResult {
	tool.executions++
	tool.executionID = toolshared.ToolExecutionID(ctx)
	result := &toolshared.ToolResult{ForLLM: `{"outcome":"waiting"}`, Control: toolshared.ToolControl{
		Suspension: &interactions.SuspensionRequest{
			Kind: interactions.KindExternalWait, ConditionRef: "opaque_condition_reference", Timeout: 2 * time.Hour,
			PromptSummary: "Approve in the external service; the task continues automatically.",
		},
	}}
	if tool.handoff {
		result.Control.LiveHandoff = &toolshared.LiveResourceHandoff{ResourceKind: "test", ResourceID: "test"}
	}
	return result
}

func (tool *externalWaitTestTool) ResolveExternalWait(ctx context.Context, ref string) toolshared.ExternalWaitState {
	if ref != "opaque_condition_reference" || toolshared.ToolExecutionID(ctx) != tool.executionID ||
		toolshared.ToolInboundContext(ctx).SenderID != "user-1" {
		tool.invalidContext.Store(true)
		return toolshared.ExternalWaitUnavailable
	}
	return tool.state.Load().(toolshared.ExternalWaitState)
}

func (tool *externalWaitTestTool) CleanupTurnWithResult(ctx context.Context) (tools.TurnCleanupResult, error) {
	tool.cleanups++
	if toolshared.ToolExecutionID(ctx) != tool.executionID {
		return tools.TurnCleanupResult{}, errors.New("wrong cleanup owner")
	}
	return tools.TurnCleanupResult{Receipts: []taskresult.Receipt{
		{
			ID:       "external_cleanup",
			Kind:     taskresult.ReceiptKindResourceCleanup,
			Target:   "browser:test",
			Action:   "close",
			Tool:     tool.Name(),
			Summary:  "Test resource closed",
			Metadata: map[string]string{"state": "closed"},
		},
	}}, tool.cleanupErr
}

func startExternalWaitTest(
	t *testing.T,
	tool *externalWaitTestTool,
) (*AgentLoop, *AgentInstance, *sequenceProvider, interactions.Record) {
	t.Helper()
	tool.state.Store(toolshared.ExternalWaitPending)
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{ID: "call-external", Name: tool.Name(), Arguments: map[string]any{}}}},
		{Content: "Continued after external readiness.", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	t.Cleanup(cleanup)
	installInteractionChannelManager(t, al, newInteractionChannelManager())
	agent.Tools.Register(tool)
	inbound := &bus.InboundContext{
		Channel:   "telegram",
		ChatID:    "chat-1",
		SenderID:  "user-1",
		MessageID: "original-message",
	}
	_, status, err := runAgentLoopWithStatusForTest(t.Context(), al, agent, turnSpec{
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-external",
			SessionKey:      "session-external",
			UserMessage:     "perform request",
			InboundContext:  inbound,
		},
		DefaultResponse: defaultResponse,
		EnableSummary:   true,
		SendResponse:    false,
	})
	if err != nil || status != TurnEndStatusSuspended {
		t.Fatalf("initial=%s %v", status, err)
	}
	record, ok := activeInteractionForSession(al.interactionRegistryForWorkspace(agent.Workspace), "session-external")
	if !ok {
		t.Fatal("no durable wait")
	}
	return al, agent, provider, record
}

func TestExternalWaitSuspendsCleansAndAutomaticallyContinuesOnce(t *testing.T) {
	tool := &externalWaitTestTool{}
	al, agent, provider, record := startExternalWaitTest(t, tool)
	if record.Kind != interactions.KindExternalWait || tool.cleanups != 1 || tool.executionID == "" ||
		len(record.OutcomeReceipts) != 1 || record.OutcomeReceipts[0].Kind != taskresult.ReceiptKindResourceCleanup {
		t.Fatalf("wait=%+v cleanups=%d", record, tool.cleanups)
	}
	now := time.Now().Add(90 * time.Minute)
	al.recoverHumanInteractionsAt(t.Context(), now)
	waiting, _ := al.interactionRegistryForWorkspace(agent.Workspace).Get(record.ID)
	if waiting.Status != interactions.StatusWaiting || provider.callCount != 1 {
		t.Fatal("pending wait continued")
	}
	if _, err := parseInteractionAnswer(waiting, "yes", "chat-answer"); err == nil {
		t.Fatal("chat approved external wait")
	}
	tool.state.Store(toolshared.ExternalWaitReady)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { al.recoverHumanInteractionsAt(t.Context(), now) })
	}
	wg.Wait()
	al.recoverHumanInteractionsAt(t.Context(), now)
	resolved, _ := al.interactionRegistryForWorkspace(agent.Workspace).Get(record.ID)
	if resolved.Status != interactions.StatusResolved || resolved.Outcome != interactions.OutcomeReady ||
		tool.executions != 1 || provider.callCount != 2 || tool.invalidContext.Load() {
		t.Fatalf(
			"resolved=%+v executions=%d calls=%d bad-context=%t",
			resolved,
			tool.executions,
			provider.callCount,
			tool.invalidContext.Load(),
		)
	}
	history, err := agent.Sessions.ReadTurnHistory(t.Context(), "session-external")
	if err != nil || countInteractionToolResults(history, "call-external") != 1 {
		t.Fatalf("history pairing: %v", err)
	}
	for _, msg := range history {
		if msg.Role == "tool" && msg.ToolCallID == "call-external" {
			var payload interactionToolResultPayload
			if err := json.Unmarshal(
				[]byte(msg.Content),
				&payload,
			); err != nil ||
				payload.Outcome != interactions.OutcomeReady {
				t.Fatalf("canonical readiness result: %s %v", msg.Content, err)
			}
		}
	}
	var prompt strings.Builder
	for _, msg := range provider.requests[1] {
		prompt.WriteString(msg.Content)
	}
	projected, err := json.Marshal(provider.requests[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt.String(), "Open fresh resources") ||
		!strings.Contains(prompt.String(), "Recorded outcome: ready.") ||
		strings.Contains(string(projected), "opaque_condition_reference") {
		t.Fatalf("missing readiness/fresh-state guidance: %s", prompt.String())
	}
}

func TestExternalWaitUnavailableExpiryAndCancellationNeverReplay(t *testing.T) {
	for _, scenario := range []string{"missing-tool", "lost-condition", "expired", "canceled", "claimed-then-revoked"} {
		t.Run(scenario, func(t *testing.T) {
			tool := &externalWaitTestTool{}
			al, agent, provider, record := startExternalWaitTest(t, tool)
			registry := al.interactionRegistryForWorkspace(agent.Workspace)
			now := time.Now()
			switch scenario {
			case "missing-tool":
				agent.Tools.Unregister(tool.Name())
			case "lost-condition":
				tool.state.Store(toolshared.ExternalWaitUnavailable)
			case "expired":
				now = now.Add(3 * time.Hour)
			case "canceled":
				if _, err := registry.Cancel(record.ID, record.Revision, "owner_stop"); err != nil {
					t.Fatal(err)
				}
				tool.state.Store(toolshared.ExternalWaitReady)
			case "claimed-then-revoked":
				if _, err := registry.ClaimExternalWait(
					record.ID,
					record.Revision,
					interactions.OutcomeReady,
				); err != nil {
					t.Fatal(err)
				}
				tool.state.Store(toolshared.ExternalWaitUnavailable)
			}
			al.recoverHumanInteractionsAt(t.Context(), now)
			after, _ := registry.Get(record.ID)
			if provider.callCount != 1 || tool.executions != 1 || after.Status == interactions.StatusWaiting ||
				after.Outcome == interactions.OutcomeReady {
				t.Fatalf("after=%+v calls=%d", after, provider.callCount)
			}
		})
	}
}

func TestExternalWaitRecoveryPreservesCachedFinalAfterConditionLoss(t *testing.T) {
	tool := &externalWaitTestTool{}
	al, agent, provider, record := startExternalWaitTest(t, tool)
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	ready, err := registry.ClaimExternalWait(record.ID, record.Revision, interactions.OutcomeReady)
	if err != nil {
		t.Fatal(err)
	}
	if err = al.ensureInteractionToolResult(t.Context(), agent, ready); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.MarkResuming(ready.ID, ready.Revision); err != nil {
		t.Fatal(err)
	}
	if err = agent.Sessions.AppendTurnMessage(t.Context(), "session-external", providers.Message{
		Role: "assistant", Content: "Previously completed result.",
	}); err != nil {
		t.Fatal(err)
	}
	al.interactions.registries.Delete(agent.Workspace)
	tool.state.Store(toolshared.ExternalWaitUnavailable)
	al.RecoverHumanInteractions(t.Context())
	reloaded := al.interactionRegistryForWorkspace(agent.Workspace)
	after, _ := reloaded.Get(record.ID)
	if after.Status != interactions.StatusResolved || after.Outcome != interactions.OutcomeReady ||
		provider.callCount != 1 {
		t.Fatalf("cached result overwritten or replayed: %+v calls=%d", after, provider.callCount)
	}
}

type externalWaitWithoutResolver struct{ toolshared.Tool }

func TestExternalWaitRejectsUnsafeSuspension(t *testing.T) {
	for _, scenario := range []string{"cleanup-failed", "live-handoff", "missing-resolver"} {
		t.Run(scenario, func(t *testing.T) {
			tool := &externalWaitTestTool{}
			tool.state.Store(toolshared.ExternalWaitPending)
			var registered toolshared.Tool = tool
			switch scenario {
			case "cleanup-failed":
				tool.cleanupErr = errors.New("resource not closed")
			case "live-handoff":
				tool.handoff = true
			case "missing-resolver":
				registered = &externalWaitWithoutResolver{Tool: tool}
			}
			provider := &sequenceProvider{responses: []*providers.LLMResponse{
				{
					ToolCalls: []providers.ToolCall{
						{ID: "call-external-unsafe", Name: tool.Name(), Arguments: map[string]any{}},
					},
				},
				{Content: "Unable to wait safely.", FinishReason: "stop"},
			}}
			al, agent, cleanup := newTurnCoordTestLoop(t, provider)
			defer cleanup()
			installInteractionChannelManager(t, al, newInteractionChannelManager())
			agent.Tools.Register(registered)
			spec := makeTestTurnSpec("session-unsafe-wait")
			spec.Dispatch.InboundContext = &bus.InboundContext{Channel: "cli", ChatID: "test-chat", SenderID: "user-1"}
			_, status, err := runAgentLoopWithStatusForTest(t.Context(), al, agent, spec)
			if err != nil || status == TurnEndStatusSuspended ||
				len(al.interactionRegistryForWorkspace(agent.Workspace).ListNonterminal()) != 0 {
				t.Fatalf("unsafe suspension admitted: %s %v", status, err)
			}
			if scenario == "cleanup-failed" && tool.cleanups == 0 {
				t.Fatal("cleanup not attempted")
			}
		})
	}
}

func TestExternalWaitHasNoChatApprovalControls(t *testing.T) {
	record := interactions.Record{
		ID: "interaction_wait", ShortID: "wait", Kind: interactions.KindExternalWait,
		PromptSummary: "Waiting for phone approval.", Route: interactions.Route{Channel: "telegram", ChatID: "chat-1"},
	}
	message := interactionPromptMessage(record)
	if err := bus.ValidateOutboundMetadata(message.Metadata); err != nil {
		t.Fatal(err)
	}
	if message.Metadata.IsApprovalPrompt() || message.Metadata.IsQuestionPrompt() ||
		strings.Contains(renderInteractionPrompt(record), "/answer") {
		t.Fatal("external wait offered chat approval")
	}
	metadata := interactionFinalMetadata(record)
	if err := bus.ValidateOutboundMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.InteractionKind != "" || metadata.InteractionControls != "" {
		t.Fatal("external wait acquired chat controls")
	}
}

func TestExternalWaitDelegatedTaskRetainsOwnerAcrossDelayAndReload(t *testing.T) {
	for _, scenario := range []string{"ready", "unavailable", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			tool := &externalWaitTestTool{}
			tool.state.Store(toolshared.ExternalWaitPending)
			provider := &sequenceProvider{responses: []*providers.LLMResponse{
				{
					ToolCalls: []providers.ToolCall{
						{ID: "call-task-external", Name: tool.Name(), Arguments: map[string]any{}},
					},
				},
				{Content: "Delegated request continued.", FinishReason: "stop"},
			}}
			al, agent, cleanup := newTurnCoordTestLoop(t, provider)
			defer cleanup()
			installInteractionChannelManager(t, al, newInteractionChannelManager())
			agent.Tools.Register(tool)
			const taskID = "external-wait-task"
			const ownerSession = "owner-external-session"
			const childSession = "child-external-session"
			tasks := al.taskRegistryForWorkspace(agent.Workspace)
			if err := tasks.Upsert(taskregistry.Record{
				TaskID:              taskID,
				Runtime:             taskregistry.RuntimeDelegate,
				TaskKind:            "delegate",
				Task:                "perform delegated request",
				Status:              taskregistry.StatusRunning,
				TimeoutSeconds:      60,
				DeliveryStatus:      taskregistry.DeliveryPending,
				DeliveryMode:        string(toolshared.AsyncDeliveryParentOnly),
				Channel:             "telegram",
				ChatID:              "chat-1",
				RequesterSessionKey: ownerSession,
			}); err != nil {
				t.Fatal(err)
			}
			taskBefore, _ := tasks.Get(taskID)
			inbound := &bus.InboundContext{
				Channel:   "telegram",
				ChatID:    "chat-1",
				SenderID:  "user-1",
				MessageID: "original-task-message",
			}
			executionCtx, cancel := context.WithCancel(t.Context())
			_, status, err := runAgentLoopWithStatusForTest(executionCtx, al, agent, turnSpec{
				Dispatch: DispatchRequest{
					RouteSessionKey: "owner-external-route", SessionKey: childSession,
					UserMessage: "perform delegated request", InboundContext: inbound,
				},
				TaskID: taskID, InteractionWorkspace: agent.Workspace, InteractionSessionKey: ownerSession,
				InteractionRouteKey: "owner-external-route", DefaultResponse: defaultResponse, SendResponse: false,
			})
			cancel()
			if err != nil || status != TurnEndStatusSuspended {
				t.Fatalf("delegate initial=%s %v", status, err)
			}
			registry := al.interactionRegistryForWorkspace(agent.Workspace)
			record, ok := activeInteractionForSession(registry, ownerSession)
			if !ok || record.Origin.TaskID != taskID || record.Origin.ContinuationSessionKey != childSession {
				t.Fatalf("delegate ownership=%+v found=%t", record, ok)
			}
			// Reconstruct both durable registries, preserving the task that is owned by
			// the interaction rather than treating the old short execution as lost.
			al.interactions.registries.Delete(agent.Workspace)
			al.tasks.registries.Delete(agent.Workspace)
			tasks = al.taskRegistryForWorkspace(agent.Workspace)
			now := time.Now().Add(90 * time.Minute)
			al.recoverHumanInteractionsAt(t.Context(), now)
			pending, _ := tasks.Get(taskID)
			if pending.Status != taskregistry.StatusRunning || pending.GenerationID != taskBefore.GenerationID ||
				provider.callCount != 1 {
				t.Fatalf("task did not remain durably waiting=%+v calls=%d", pending, provider.callCount)
			}
			expectedStatus := taskregistry.StatusSucceeded
			switch scenario {
			case "ready":
				tool.state.Store(toolshared.ExternalWaitReady)
			case "unavailable":
				tool.state.Store(toolshared.ExternalWaitUnavailable)
				expectedStatus = taskregistry.StatusFailed
			case "expired":
				now = now.Add(time.Hour)
				expectedStatus = taskregistry.StatusTimedOut
			}
			al.recoverHumanInteractionsAt(t.Context(), now)
			after, _ := tasks.Get(taskID)
			if after.Status != expectedStatus || after.DeliveryStatus != taskregistry.DeliverySessionQueued ||
				after.GenerationID != taskBefore.GenerationID || after.RequesterSessionKey != ownerSession || tool.executions != 1 {
				t.Fatalf("delegate continuation=%+v executions=%d", after, tool.executions)
			}
		})
	}
}
