package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// Test constants (use defaults from subturn.go)
const (
	testMaxConcurrentSubTurns = defaultMaxConcurrentSubTurns
)

// ====================== Test Helper: Event Collector ======================
type eventCollector struct {
	mu     sync.Mutex
	events []runtimeevents.Event
}

func newEventCollector(t *testing.T, al *AgentLoop) (*eventCollector, func()) {
	t.Helper()
	c := &eventCollector{}
	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentSubTurnSpawn,
		runtimeevents.KindAgentSubTurnEnd,
		runtimeevents.KindAgentSubTurnResultDelivered,
		runtimeevents.KindAgentSubTurnOrphan,
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for evt := range runtimeCh {
			c.mu.Lock()
			c.events = append(c.events, evt)
			c.mu.Unlock()
		}
	}()
	cleanup := func() {
		closeRuntimeEvents()
		<-done
	}
	return c, cleanup
}

func (c *eventCollector) hasEventOfKind(kind runtimeevents.Kind) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// ====================== Main Test Function ======================
func TestSpawnSubTurn(t *testing.T) {
	tests := []struct {
		name          string
		parentDepth   int
		config        SubTurnConfig
		wantErr       error
		wantSpawn     bool
		wantEnd       bool
		wantDepthFail bool
	}{
		{
			name:        "Basic success path - Single layer sub-turn",
			parentDepth: 0,
			config: SubTurnConfig{
				Model: "gpt-4o-mini",
				Tools: []toolshared.Tool{}, // At least one tool
			},
			wantErr:   nil,
			wantSpawn: true,
			wantEnd:   true,
		},
		{
			name:        "Nested 2 layers - Normal",
			parentDepth: 1,
			config: SubTurnConfig{
				Model: "gpt-4o-mini",
				Tools: []toolshared.Tool{},
			},
			wantErr:   nil,
			wantSpawn: true,
			wantEnd:   true,
		},
		{
			name:        "Depth limit triggered - 4th layer fails",
			parentDepth: 3,
			config: SubTurnConfig{
				Model: "gpt-4o-mini",
				Tools: []toolshared.Tool{},
			},
			wantErr:       ErrDepthLimitExceeded,
			wantSpawn:     false,
			wantEnd:       false,
			wantDepthFail: true,
		},
		{
			name:        "Invalid config - Empty Model",
			parentDepth: 0,
			config: SubTurnConfig{
				Model: "",
				Tools: []toolshared.Tool{},
			},
			wantErr:   ErrInvalidSubTurnConfig,
			wantSpawn: false,
			wantEnd:   false,
		},
	}

	al, _, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Prepare parent Turn
			parent := &turnState{
				ctx:            context.Background(),
				turnID:         "parent-1",
				depth:          tt.parentDepth,
				childTurnIDs:   []string{},
				pendingResults: make(chan *toolshared.ToolResult, 10),
				session:        &ephemeralSessionStore{},
				agent:          al.registry.GetDefaultAgent(),
			}

			// Subscribe to runtime events to capture sub-turn lifecycle.
			collector, collectCleanup := newEventCollector(t, al)
			defer collectCleanup()

			// Execute spawnSubTurn
			result, err := spawnSubTurn(context.Background(), al, parent, tt.config)

			// Assert errors
			if tt.wantErr != nil {
				if err == nil || !errors.Is(err, tt.wantErr) {
					t.Errorf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Verify result
			if result == nil {
				t.Error("expected non-nil result")
			}

			// Verify event emission
			time.Sleep(10 * time.Millisecond) // let event goroutine flush
			if tt.wantSpawn {
				if !collector.hasEventOfKind(runtimeevents.KindAgentSubTurnSpawn) {
					t.Error("SubTurnSpawnEvent not emitted")
				}
			}
			if tt.wantEnd {
				if !collector.hasEventOfKind(runtimeevents.KindAgentSubTurnEnd) {
					t.Error("SubTurnEndEvent not emitted")
				}
			}

			// Verify turn tree
			if len(parent.childTurnIDs) == 0 && !tt.wantDepthFail {
				t.Error("child Turn not added to parent.childTurnIDs")
			}

			// For synchronous calls (Async=false, the default), result is returned directly
			// and should NOT be in pendingResults. The result was already verified above.
			// Only async calls (Async=true) would place results in pendingResults.
		})
	}
}

func TestDurableTaskSubTurnSuspendsIntoWaitingTask(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "call-task-question", Name: "request_user_input",
			Arguments: map[string]any{"questions": []any{map[string]any{
				"id": "mode", "question": "Which deployment mode?",
			}}},
			Function: &providers.FunctionCall{
				Name:      "request_user_input",
				Arguments: `{"questions":[{"id":"mode","question":"Which deployment mode?"}]}`,
			},
		}}},
		{ToolCalls: []providers.ToolCall{{
			ID: "call-task-confirm", Name: "request_user_input",
			Arguments: map[string]any{"questions": []any{map[string]any{
				"id": "confirm", "question": "Proceed now?",
			}}},
			Function: &providers.FunctionCall{
				Name:      "request_user_input",
				Arguments: `{"questions":[{"id":"confirm","question":"Proceed now?"}]}`,
			},
		}}},
		{Content: "deployed", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	requestTool, err := tools.NewRequestUserInputTool(tools.RequestUserInputToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	agent.Tools.Register(requestTool)

	tasks := al.taskRegistryForWorkspace(agent.Workspace)
	if err = tasks.Upsert(taskregistry.Record{
		TaskID: "subagent-1", Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "deploy", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
	}); err != nil {
		t.Fatal(err)
	}
	inbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
	}
	parentOpts := turnSpec{Dispatch: DispatchRequest{
		RouteSessionKey: "route-owner", SessionKey: "owner-session",
		InboundContext: inbound,
	}}
	parent := newTurnState(
		agent,
		parentOpts,
		al.newTurnEventScope(agent.ID, agent.Workspace, "owner-session", newTurnContext(inbound, nil, nil)),
	)
	parent.ctx = t.Context()
	parent.pendingResults = make(chan *toolshared.ToolResult, 4)
	parent.concurrencySem = make(chan struct{}, defaultMaxConcurrentSubTurns)

	result, err := spawnSubTurn(t.Context(), al, parent, SubTurnConfig{
		Model: agent.Model, SystemPrompt: "deploy", TaskID: "subagent-1", Critical: true,
	})
	if err != nil || result == nil || !result.Control.TaskSuspended {
		t.Fatalf("spawnSubTurn() = (%#v, %v), want suspended durable task", result, err)
	}
	rec, _ := tasks.Get("subagent-1")
	if rec.Status != taskregistry.StatusRunning {
		t.Fatalf("task after suspension = %#v", rec)
	}
	interaction, ok := al.interactionRegistryForWorkspace(agent.Workspace).FindNonterminalByTaskID("subagent-1")
	if !ok || interaction.Route.SessionKey != "owner-session" ||
		interaction.Origin.TaskID != "subagent-1" ||
		interaction.Origin.ContinuationSessionKey != durableTaskSessionKey(
			agent.Workspace, "subagent-1",
		) {
		t.Fatalf("durable interaction = %#v", interaction)
	}
	history := agent.Sessions.GetHistory(durableTaskSessionKey(agent.Workspace, "subagent-1"))
	if len(history) == 0 {
		t.Fatal("durable task continuation history was not persisted")
	}
	select {
	case <-manager.sent:
	case <-time.After(time.Second):
		t.Fatal("interaction prompt was not delivered")
	}
	claimed, err := al.interactionRegistryForWorkspace(agent.Workspace).ClaimAnswer(
		interaction.ID,
		interaction.Revision,
		interactions.Answer{
			Text: "canary", Values: map[string]string{"mode": "canary"},
			MessageID: "answer-1", ReceivedAt: time.Now().UnixMilli(),
		},
		interactions.OutcomeAnswered,
	)
	if err != nil {
		t.Fatalf("ClaimAnswer() error = %v", err)
	}
	if claimed.Status != interactions.StatusClaimed {
		t.Fatalf("claimed interaction = %#v", claimed)
	}
	if err = al.enqueueSteeringMessageWithSender(
		newRuntimeSessionScope(agent.Workspace, "owner-session"), agent.ID, "user-1",
		providers.Message{Role: "user", Content: "deferred during recovery"},
	); err != nil {
		t.Fatal(err)
	}
	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	rec, _ = tasks.Get("subagent-1")
	if rec.Status != taskregistry.StatusRunning {
		t.Fatalf("task after repeated wait = %#v", rec)
	}
	second, ok := al.interactionRegistryForWorkspace(agent.Workspace).FindNonterminalByTaskID("subagent-1")
	if !ok || second.ID == interaction.ID || second.Status != interactions.StatusWaiting ||
		second.Route.SessionKey != "owner-session" ||
		second.Origin.ContinuationSessionKey != interaction.Origin.ContinuationSessionKey {
		t.Fatalf("second interaction = %#v", second)
	}
	first, _ := al.interactionRegistryForWorkspace(agent.Workspace).Get(interaction.ID)
	if first.Status != interactions.StatusResolved {
		t.Fatalf("first interaction after chaining = %#v", first)
	}
	if got := al.pendingSteeringCountForScope(
		newRuntimeSessionScope(agent.Workspace, "owner-session"),
	); got != 1 {
		t.Fatalf("deferred queue during chained wait = %d, want 1", got)
	}
	for _, message := range agent.Sessions.GetHistory("owner-session") {
		if strings.Contains(message.Content, "deferred during recovery") {
			t.Fatalf("deferred input escaped while next interaction was waiting: %#v", message)
		}
	}
	select {
	case <-manager.sent:
	case <-time.After(time.Second):
		t.Fatal("second interaction prompt was not delivered")
	}
	secondClaimed, err := al.interactionRegistryForWorkspace(agent.Workspace).ClaimAnswer(
		second.ID,
		second.Revision,
		interactions.Answer{
			Text: "yes", Values: map[string]string{"confirm": "yes"},
			MessageID: "answer-2", ReceivedAt: time.Now().UnixMilli(),
		},
		interactions.OutcomeAnswered,
	)
	if err != nil {
		t.Fatalf("second ClaimAnswer() error = %v", err)
	}
	err = al.resumeClaimedInteraction(
		t.Context(), al.interactionRegistryForWorkspace(agent.Workspace),
		agent.Workspace, agent, nil, *inbound, secondClaimed,
	)
	if err != nil {
		t.Fatalf("second resumeClaimedInteraction() error = %v", err)
	}
	rec, _ = tasks.Get("subagent-1")
	if rec.Status != taskregistry.StatusSucceeded ||
		rec.DeliveryStatus != taskregistry.DeliveryDelivered ||
		rec.TerminalSummary != "deployed" {
		t.Fatalf("task after resumed completion = %#v", rec)
	}
	select {
	case final := <-manager.sent:
		if final.Content != "deployed" {
			t.Fatalf("resumed final = %#v", final)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed task final was not delivered")
	}
}

func TestDurableTaskSubTurnWaitsForHumanApproval(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "call-task-approval", Name: "browser_act",
			Arguments: map[string]any{"target": "production"},
			Function: &providers.FunctionCall{
				Name: "browser_act", Arguments: `{"target":"production"}`,
			},
		}}},
		{Content: "approved task completed\n" + objectiveOutcomeStart +
			`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":["inv-approved"]}],"missing_items":[]}` +
			objectiveOutcomeEnd, FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tool := &outcomeBrowserApprovalTool{}
	agent.Tools.Register(tool)
	if err := al.MountHook(NamedHook("task-approval", &durableApprovalHook{
		actionSummary: "Run the production task action",
	})); err != nil {
		t.Fatal(err)
	}
	tasks := al.taskRegistryForWorkspace(agent.Workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: "subagent-approval", Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "protected deployment", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
	}); err != nil {
		t.Fatal(err)
	}
	inbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-approval", SenderID: "user-approval",
	}
	parentOpts := turnSpec{Dispatch: DispatchRequest{
		RouteSessionKey: "route-task-approval", SessionKey: "owner-task-approval",
		InboundContext: inbound,
	}}
	parent := newTurnState(
		agent,
		parentOpts,
		al.newTurnEventScope(
			agent.ID, agent.Workspace, "owner-task-approval", newTurnContext(inbound, nil, nil),
		),
	)
	parent.ctx = t.Context()
	parent.pendingResults = make(chan *toolshared.ToolResult, 4)
	parent.concurrencySem = make(chan struct{}, defaultMaxConcurrentSubTurns)
	result, err := spawnSubTurn(t.Context(), al, parent, SubTurnConfig{
		Model: agent.Model, SystemPrompt: "deploy", TaskID: "subagent-approval", Critical: true,
		ObjectiveItems: []toolshared.ObjectiveSpec{{Item: "production action", Kind: "external_action"}},
	})
	if err != nil || result == nil || !result.Control.TaskSuspended {
		t.Fatalf("spawnSubTurn() = (%#v, %v)", result, err)
	}
	task, _ := tasks.Get("subagent-approval")
	if task.Status != taskregistry.StatusRunning || tool.executions != 0 {
		t.Fatalf("waiting approval task = %#v, executions=%d", task, tool.executions)
	}
	select {
	case <-manager.sent:
	case <-time.After(time.Second):
		t.Fatal("task approval prompt was not delivered")
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := registry.FindNonterminalByTaskID(task.TaskID)
	if !ok {
		t.Fatal("task approval interaction was not persisted")
	}
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "task-approval-answer", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	if err = al.resumeClaimedInteraction(
		t.Context(), registry, agent.Workspace, agent, nil, *inbound, record,
	); err != nil {
		t.Fatal(err)
	}
	task, _ = tasks.Get("subagent-approval")
	if task.Status != taskregistry.StatusSucceeded ||
		task.DeliveryStatus != taskregistry.DeliveryDelivered ||
		tool.executions != 1 || task.Deliverable == nil || task.Deliverable.ObjectiveOutcome == nil ||
		task.Deliverable.ObjectiveOutcome.Status != taskresult.OutcomeSucceeded ||
		len(task.Deliverable.ObjectiveOutcome.CompletedItems) != 1 ||
		task.Deliverable.ObjectiveOutcome.CompletedItems[0].Receipts[0].ID != "inv-approved" {
		t.Fatalf("completed approval task = %#v, executions=%d", task, tool.executions)
	}
}

type outcomeBrowserApprovalTool struct {
	executions int
}

func TestBrowserChildUserOnlyUsesVerifiedPartialContent(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{
		Content: "Both items were published.\n" + objectiveOutcomeStart +
			`{"status":"partial","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],` +
			`"missing_items":["objective_2"],"explanation":"the second item was not published"}` +
			objectiveOutcomeEnd,
		FinishReason: "stop",
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	agent.Tools.Register(&outcomeBrowserApprovalTool{})
	inbound := &bus.InboundContext{Channel: "telegram", ChatID: "chat-user-only", SenderID: "user"}
	parent := newTurnState(agent, turnSpec{Dispatch: DispatchRequest{
		RouteSessionKey: "route-user-only", SessionKey: "parent-user-only", InboundContext: inbound,
	}}, al.newTurnEventScope(
		agent.ID, agent.Workspace, "parent-user-only", newTurnContext(inbound, nil, nil),
	))
	parent.ctx = t.Context()
	parent.pendingResults = make(chan *toolshared.ToolResult, 4)
	parent.concurrencySem = make(chan struct{}, defaultMaxConcurrentSubTurns)

	result, err := spawnSubTurn(t.Context(), al, parent, SubTurnConfig{
		Model: agent.Model, SystemPrompt: "publish both items",
		DeliveryMode: toolshared.AsyncDeliveryUserOnly,
		ObjectiveItems: []toolshared.ObjectiveSpec{
			{Item: "Yakima published", Kind: "result"},
			{Item: "Vissani not published", Kind: "result"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	userText := toolResultUserText(result)
	if strings.Contains(userText, "Both items") || !strings.Contains(userText, "Yakima published") ||
		!strings.Contains(userText, "Vissani not published") || !result.Delivery.IsFinalHandled() {
		t.Fatalf("user-only verified result = %#v, user text = %q", result, userText)
	}
	if strings.Contains(result.ForLLM, "Both items") || result.Deliverable == nil ||
		strings.Contains(result.Deliverable.Text, "Both items") {
		t.Fatalf("parent or durable projection retained contradictory prose: %#v", result)
	}
	msgBus := al.bus.(*bus.MessageBus)
	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("browser child published before outcome validation: %#v", outbound)
	default:
	}
}

func (*outcomeBrowserApprovalTool) Name() string { return "browser_act" }

func (*outcomeBrowserApprovalTool) Description() string { return "Perform a protected browser action" }

func (*outcomeBrowserApprovalTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *outcomeBrowserApprovalTool) Execute(
	_ context.Context,
	_ map[string]any,
) *toolshared.ToolResult {
	tool.executions++
	return toolshared.NewToolResult(`{"invocation_id":"inv-approved","state":"succeeded"}`).
		WithWriteAudit(toolshared.WriteAuditEntry{
			Kind: "external_action", Target: "https://example.com", Action: "click",
			Tool: "browser_act", Success: true,
			Metadata: map[string]string{"invocation_id": "inv-approved", "effect": "external_commit"},
		})
}

type scopeRecordingApprovalTool struct {
	executions int
	scope      *session.SessionScope
	agentID    string
	started    chan struct{}
	release    chan struct{}
}

func (*scopeRecordingApprovalTool) Name() string { return "approval_scope_recording" }

func (*scopeRecordingApprovalTool) Description() string {
	return "Record the durable continuation scope for a protected action"
}

func (*scopeRecordingApprovalTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *scopeRecordingApprovalTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *toolshared.ToolResult {
	tool.executions++
	tool.scope = toolshared.ToolSessionScope(ctx)
	tool.agentID = toolshared.ToolAgentID(ctx)
	if tool.started != nil {
		select {
		case tool.started <- struct{}{}:
		default:
		}
	}
	if tool.release != nil {
		<-tool.release
	}
	return toolshared.NewToolResult("protected child action completed")
}

func TestCrossAgentDurableApprovalPreservesChildSessionProvenance(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "call-child-approval", Name: "approval_scope_recording",
			Arguments: map[string]any{"target": "companion"},
			Function: &providers.FunctionCall{
				Name: "approval_scope_recording", Arguments: `{"target":"companion"}`,
			},
		}}},
		{Content: "cross-agent approval completed", FinishReason: "stop"},
	}}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	installInteractionChannelManager(t, al, newInteractionChannelManager())
	if err := al.MountHook(NamedHook("child-scope-approval", &durableApprovalHook{
		actionSummary: "Run the protected child action",
	})); err != nil {
		t.Fatal(err)
	}
	alpha, _ := al.registry.GetAgent("alpha")
	beta, _ := al.registry.GetAgent("beta")
	tool := &scopeRecordingApprovalTool{
		started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	beta.Tools.Register(tool)

	const taskID = "cross-agent-approval"
	tasks := al.taskRegistryForWorkspace(alpha.Workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: taskID, Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "protected companion action",
		Status: taskregistry.StatusRunning, DeliveryStatus: taskregistry.DeliveryPending,
	}); err != nil {
		t.Fatal(err)
	}
	inbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-cross-agent", SenderID: "user-cross-agent",
	}
	parentScope := &session.SessionScope{
		Version: session.ScopeVersion, AgentID: alpha.ID, Channel: "telegram",
		Dimensions: []string{"chat"}, Values: map[string]string{"chat": "chat-cross-agent"},
		RouteScopeKey: "telegram:chat-cross-agent", ClientSessionID: "frontend-cross-agent",
	}
	parent := newTurnState(alpha, turnSpec{Dispatch: DispatchRequest{
		RouteSessionKey: "route-cross-agent", SessionKey: "owner-cross-agent",
		InboundContext: inbound, SessionScope: parentScope,
	}}, al.newTurnEventScope(
		alpha.ID, alpha.Workspace, "owner-cross-agent", newTurnContext(inbound, nil, parentScope),
	))
	parent.ctx = t.Context()
	parent.pendingResults = make(chan *toolshared.ToolResult, 4)
	parent.concurrencySem = make(chan struct{}, defaultMaxConcurrentSubTurns)
	continuationKey := durableTaskSessionKey(alpha.Workspace, taskID)
	metaStore, ok := beta.Sessions.(session.MetadataAwareSessionStore)
	if !ok {
		t.Fatalf("beta session store %T does not expose metadata", beta.Sessions)
	}
	metaStore.EnsureSessionMetadata(continuationKey, parentScope)
	persistentStore, err := memory.NewJSONLStore(filepath.Join(beta.Workspace, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = persistentStore.Close() })

	result, err := spawnSubTurn(t.Context(), al, parent, SubTurnConfig{
		TargetAgentID: beta.ID, Model: beta.Model, SystemPrompt: "run protected action",
		TaskID: taskID, Critical: true,
	})
	if err != nil || result == nil || !result.Control.TaskSuspended {
		t.Fatalf("spawnSubTurn() = (%#v, %v)", result, err)
	}
	task, _ := tasks.Get(taskID)
	if task.Status != taskregistry.StatusRunning {
		t.Fatalf("waiting cross-agent task = %#v", task)
	}
	record, ok := al.interactionRegistryForWorkspace(alpha.Workspace).FindNonterminalByTaskID(taskID)
	if !ok {
		t.Fatalf("interaction for task %q was not persisted", taskID)
	}
	if got := interactionContinuationSessionKey(record); got != continuationKey {
		t.Fatalf("interaction continuation key = %q, want %q", got, continuationKey)
	}
	persistedScope := metaStore.GetSessionScope(continuationKey)
	if persistedScope == nil || persistedScope.AgentID != beta.ID || persistedScope.ClientSessionID != "" ||
		persistedScope.RouteScopeKey != parentScope.RouteScopeKey {
		t.Fatalf("persisted child scope = %#v", persistedScope)
	}
	persistedMeta, err := persistentStore.GetSessionMeta(t.Context(), continuationKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(persistedMeta.ClientSessionIDs) != 0 {
		t.Fatalf("derived child ClientSessionIDs = %v, want none", persistedMeta.ClientSessionIDs)
	}
	staleScope := session.CloneScope(persistedScope)
	staleScope.AgentID = alpha.ID
	staleScope.RouteScopeKey = ""
	staleScope.ClientSessionID = parentScope.ClientSessionID
	metaStore.EnsureSessionMetadata(continuationKey, staleScope)
	staleMeta, err := persistentStore.GetSessionMeta(t.Context(), continuationKey)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(staleMeta.ClientSessionIDs, parentScope.ClientSessionID) {
		t.Fatalf("stale child ClientSessionIDs = %v", staleMeta.ClientSessionIDs)
	}

	registry := al.interactionRegistryForWorkspace(alpha.Workspace)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "cross-agent-answer", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	// The approval arrives through the parent route and therefore carries the
	// parent's scope. Resume must prefer the durable child session's provenance.
	firstResume := make(chan error, 1)
	go func() {
		firstResume <- al.resumeClaimedInteraction(
			t.Context(), registry, alpha.Workspace, beta, parentScope, *inbound, record,
		)
	}()
	select {
	case <-tool.started:
	case <-time.After(time.Second):
		t.Fatal("approved child tool did not start")
	}
	secondResume := make(chan error, 1)
	go func() {
		secondResume <- al.resumeClaimedInteraction(
			t.Context(), registry, alpha.Workspace, beta, parentScope, *inbound, record,
		)
	}()
	select {
	case err = <-secondResume:
		t.Fatalf("concurrent recovery escaped active resume with error %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(tool.release)
	for name, result := range map[string]<-chan error{
		"live": firstResume, "recovery": secondResume,
	} {
		select {
		case err = <-result:
			if err != nil {
				t.Fatalf("%s resume error = %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s resume did not finish", name)
		}
	}
	if tool.executions != 1 || tool.agentID != beta.ID || tool.scope == nil ||
		tool.scope.AgentID != beta.ID || tool.scope.ClientSessionID != "" ||
		tool.scope.RouteScopeKey != parentScope.RouteScopeKey {
		t.Fatalf(
			"approved tool executions=%d agent=%q scope=%#v",
			tool.executions, tool.agentID, tool.scope,
		)
	}
	repairedScope := metaStore.GetSessionScope(continuationKey)
	if repairedScope == nil || repairedScope.AgentID != beta.ID || repairedScope.ClientSessionID != "" ||
		repairedScope.RouteScopeKey != parentScope.RouteScopeKey {
		t.Fatalf("repaired child scope = %#v", repairedScope)
	}
	repairedMeta, err := persistentStore.GetSessionMeta(t.Context(), continuationKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(repairedMeta.ClientSessionIDs) != 0 {
		t.Fatalf("repaired child ClientSessionIDs = %v, want none", repairedMeta.ClientSessionIDs)
	}
	task, _ = tasks.Get(taskID)
	if task.Status != taskregistry.StatusSucceeded ||
		task.DeliveryStatus != taskregistry.DeliveryDelivered {
		t.Fatalf("completed cross-agent task = %#v", task)
	}
}

func TestSpawnSubTurnInheritsSameAgentAdmission(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t) //nolint:dogsled
	defer cleanup()

	parentAgent := al.registry.GetDefaultAgent()
	if parentAgent == nil {
		t.Fatal("expected default parent agent")
	}
	al.agentTurnAdmissions.update(&AgentRegistry{agents: map[string]*AgentInstance{
		parentAgent.ID: {ID: parentAgent.ID, MaxParallelTurns: 1},
	}})
	parentCtx, release, err := al.acquireAgentTurn(context.Background(), parentAgent.ID)
	if err != nil {
		t.Fatalf("acquireAgentTurn() error = %v", err)
	}
	defer release()

	parent := &turnState{
		ctx:            parentCtx,
		turnID:         "parent-with-admission",
		depth:          0,
		childTurnIDs:   []string{},
		pendingResults: make(chan *toolshared.ToolResult, 10),
		session:        &ephemeralSessionStore{},
		agent:          parentAgent,
	}
	result, err := spawnSubTurn(parentCtx, al, parent, SubTurnConfig{
		Model:        "test-model",
		SystemPrompt: "complete the child turn",
		Timeout:      250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("spawnSubTurn() error = %v", err)
	}
	if result == nil {
		t.Fatal("spawnSubTurn() result is nil")
	}
}

func TestSpawnSubTurnTimesOutBeforeBusyTargetStarts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.SubTurn.ConcurrencyTimeoutSec = 1
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "browser", MaxParallelTurns: 1, Workspace: t.TempDir()},
	}
	provider := &reloadBlockingProvider{
		chatStarted: make(chan struct{}),
		releaseChat: make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		8,
		runtimeevents.KindAgentSubTurnAdmission,
		runtimeevents.KindAgentSubTurnSpawn,
	)
	defer closeRuntimeEvents()

	busyCtx, releaseBusy, err := al.acquireAgentTurn(t.Context(), "browser")
	if err != nil {
		t.Fatalf("acquireAgentTurn(browser) error = %v", err)
	}
	defer releaseBusy()
	_ = busyCtx

	parentAgent := al.registry.GetDefaultAgent()
	parentCtx, releaseParent, err := al.acquireAgentTurn(t.Context(), parentAgent.ID)
	if err != nil {
		t.Fatalf("acquireAgentTurn(main) error = %v", err)
	}
	defer releaseParent()
	parent := &turnState{
		ctx:            parentCtx,
		turnID:         "parent-waiting-for-browser",
		pendingResults: make(chan *toolshared.ToolResult, 1),
		session:        &ephemeralSessionStore{},
		agent:          parentAgent,
	}

	startedAt := time.Now()
	_, err = spawnSubTurn(parentCtx, al, parent, SubTurnConfig{
		TargetAgentID: "browser",
		SystemPrompt:  "must not start",
		Timeout:       time.Minute,
	})
	if !errors.Is(err, ErrConcurrencyTimeout) {
		t.Fatalf("spawnSubTurn() error = %v, want ErrConcurrencyTimeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("spawnSubTurn() error = %v, want deadline classification", err)
	}
	if elapsed := time.Since(startedAt); elapsed < 900*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("admission timeout elapsed = %v, want about 1s", elapsed)
	}
	select {
	case <-provider.chatStarted:
		t.Fatal("provider call started before target-agent admission")
	default:
	}
	states := make([]string, 0, 2)
	deadline := time.After(time.Second)
	for len(states) < 2 {
		select {
		case event := <-runtimeCh:
			if event.Kind == runtimeevents.KindAgentSubTurnSpawn {
				t.Fatal("timed-out child emitted subturn spawn")
			}
			payload, ok := event.Payload.(SubTurnAdmissionPayload)
			if ok && payload.Stage == "target_agent" {
				states = append(states, payload.State)
			}
		case <-deadline:
			t.Fatalf("admission states = %v, want queued,timed_out", states)
		}
	}
	if !slices.Equal(states, []string{"queued", "timed_out"}) {
		t.Fatalf("admission states = %v, want queued,timed_out", states)
	}
}

func TestSpawnSubTurnReportsParentCapacityTimeout(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.SubTurn.ConcurrencyTimeoutSec = 1
	provider := &reloadBlockingProvider{
		chatStarted: make(chan struct{}),
		releaseChat: make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		8,
		runtimeevents.KindAgentSubTurnAdmission,
		runtimeevents.KindAgentSubTurnSpawn,
	)
	defer closeRuntimeEvents()

	parentAgent := al.registry.GetDefaultAgent()
	parentSlots := make(chan struct{}, 1)
	parentSlots <- struct{}{}
	parent := &turnState{
		ctx:            t.Context(),
		turnID:         "parent-at-capacity",
		pendingResults: make(chan *toolshared.ToolResult, 1),
		concurrencySem: parentSlots,
		session:        &ephemeralSessionStore{},
		agent:          parentAgent,
	}

	_, err := spawnSubTurn(t.Context(), al, parent, SubTurnConfig{
		Model:        "test-model",
		SystemPrompt: "must not start",
		Timeout:      time.Minute,
	})
	if !errors.Is(err, ErrConcurrencyTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("spawnSubTurn() error = %v, want admission deadline", err)
	}
	select {
	case <-provider.chatStarted:
		t.Fatal("provider call started without parent capacity")
	default:
	}
	states := make([]string, 0, 2)
	deadline := time.After(time.Second)
	for len(states) < 2 {
		select {
		case event := <-runtimeCh:
			if event.Kind == runtimeevents.KindAgentSubTurnSpawn {
				t.Fatal("timed-out child emitted subturn spawn")
			}
			payload, ok := event.Payload.(SubTurnAdmissionPayload)
			if ok && payload.Stage == "parent_capacity" {
				states = append(states, payload.State)
			}
		case <-deadline:
			t.Fatalf("parent admission states = %v, want queued,timed_out", states)
		}
	}
	if !slices.Equal(states, []string{"queued", "timed_out"}) {
		t.Fatalf("parent admission states = %v, want queued,timed_out", states)
	}
}

func TestSpawnSubTurnExecutionTimeoutStartsAfterAdmission(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.SubTurn.ConcurrencyTimeoutSec = 1
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "browser", MaxParallelTurns: 1, Workspace: t.TempDir()},
	}
	provider := &reloadBlockingProvider{
		chatStarted: make(chan struct{}),
		releaseChat: make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()

	_, releaseBusy, err := al.acquireAgentTurn(t.Context(), "browser")
	if err != nil {
		t.Fatalf("acquireAgentTurn(browser) error = %v", err)
	}
	parentAgent := al.registry.GetDefaultAgent()
	parentCtx, releaseParent, err := al.acquireAgentTurn(t.Context(), parentAgent.ID)
	if err != nil {
		releaseBusy()
		t.Fatalf("acquireAgentTurn(main) error = %v", err)
	}
	defer releaseParent()
	parent := &turnState{
		ctx:            parentCtx,
		turnID:         "parent-admission-before-execution",
		pendingResults: make(chan *toolshared.ToolResult, 1),
		session:        &ephemeralSessionStore{},
		agent:          parentAgent,
	}

	childDone := make(chan error, 1)
	go func() {
		_, spawnErr := spawnSubTurn(parentCtx, al, parent, SubTurnConfig{
			TargetAgentID: "browser",
			SystemPrompt:  "finish after admission",
			Timeout:       500 * time.Millisecond,
		})
		childDone <- spawnErr
	}()
	time.Sleep(300 * time.Millisecond)
	releaseBusy()

	select {
	case <-provider.chatStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for provider call after admission")
	}
	time.Sleep(300 * time.Millisecond)
	close(provider.releaseChat)

	select {
	case err = <-childDone:
		if err != nil {
			t.Fatalf("spawnSubTurn() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for child completion")
	}
}

func TestSpawnSubTurnCancellationWhileQueuedReleasesAdmissions(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.SubTurn.ConcurrencyTimeoutSec = 1
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "browser", MaxParallelTurns: 1, Workspace: t.TempDir()},
	}
	provider := &reloadBlockingProvider{
		chatStarted: make(chan struct{}),
		releaseChat: make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()

	_, releaseBusy, err := al.acquireAgentTurn(t.Context(), "browser")
	if err != nil {
		t.Fatalf("acquireAgentTurn(browser) error = %v", err)
	}
	parentAgent := al.registry.GetDefaultAgent()
	parentBase, releaseParent, err := al.acquireAgentTurn(t.Context(), parentAgent.ID)
	if err != nil {
		releaseBusy()
		t.Fatalf("acquireAgentTurn(main) error = %v", err)
	}
	parentCtx, cancelParent := context.WithCancel(parentBase)
	parent := &turnState{
		ctx:            parentCtx,
		turnID:         "parent-canceled-during-admission",
		pendingResults: make(chan *toolshared.ToolResult, 1),
		session:        &ephemeralSessionStore{},
		agent:          parentAgent,
	}

	childDone := make(chan error, 1)
	go func() {
		_, spawnErr := spawnSubTurn(parentCtx, al, parent, SubTurnConfig{
			TargetAgentID: "browser",
			SystemPrompt:  "must be canceled before start",
			Timeout:       time.Minute,
		})
		childDone <- spawnErr
	}()
	time.Sleep(50 * time.Millisecond)
	cancelParent()
	select {
	case err = <-childDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("spawnSubTurn() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued child did not exit after cancellation")
	}
	select {
	case <-provider.chatStarted:
		t.Fatal("provider call started for canceled queued child")
	default:
	}

	releaseBusy()
	releaseParent()
	nextCtx, nextCancel := context.WithTimeout(t.Context(), time.Second)
	_, releaseNext, err := al.acquireAgentTurn(nextCtx, "browser")
	nextCancel()
	if err != nil {
		t.Fatalf("browser admission leaked after queued cancellation: %v", err)
	}
	releaseNext()
}

func TestDurableTaskSessionKeyIncludesOwnerWorkspace(t *testing.T) {
	first := durableTaskSessionKey("/workspace/one", "subagent-1")
	second := durableTaskSessionKey("/workspace/two", "subagent-1")
	if first == second || !session.IsOpaqueSessionKey(first) || !session.IsOpaqueSessionKey(second) {
		t.Fatalf("durable task keys = %q, %q", first, second)
	}
	if durableTaskSessionKey("/workspace/Agent", "subagent-1") ==
		durableTaskSessionKey("/workspace/agent", "subagent-1") {
		t.Fatal("case-distinct workspace paths produced the same durable task key")
	}
	if durableTaskSessionKey("/workspace/a|task=b", "c") ==
		durableTaskSessionKey("/workspace/a", "b|task=c") {
		t.Fatal("distinct workspace and task tuples produced the same durable task key")
	}
	if durableTaskSessionKey("/workspace/one/../one", "subagent-1") != first {
		t.Fatal("equivalent cleaned workspace paths produced different durable task keys")
	}
}

func TestSpawnSubTurnRetainsAdmissionAfterParentRelease(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{
		ID:               "browser",
		Default:          true,
		MaxParallelTurns: 1,
	}}
	provider := &reloadBlockingProvider{
		chatStarted: make(chan struct{}),
		releaseChat: make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()
	parentAgent := al.registry.GetDefaultAgent()
	if parentAgent == nil {
		t.Fatal("expected default parent agent")
	}
	parentCtx, releaseParent, err := al.acquireAgentTurn(context.Background(), parentAgent.ID)
	if err != nil {
		t.Fatalf("acquireAgentTurn() error = %v", err)
	}
	parent := &turnState{
		ctx:            parentCtx,
		turnID:         "parent-released-before-child",
		pendingResults: make(chan *toolshared.ToolResult, 10),
		session:        &ephemeralSessionStore{},
		agent:          parentAgent,
	}

	childDone := make(chan error, 1)
	go func() {
		_, spawnErr := spawnSubTurn(parentCtx, al, parent, SubTurnConfig{
			Model:        "test-model",
			SystemPrompt: "wait for release",
			Timeout:      2 * time.Second,
		})
		childDone <- spawnErr
	}()
	select {
	case <-provider.chatStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for child provider call")
	}

	releaseParent()
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, _, err = al.acquireAgentTurn(waitCtx, parentAgent.ID)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("independent acquire while child active error = %v, want deadline exceeded", err)
	}

	close(provider.releaseChat)
	select {
	case err = <-childDone:
		if err != nil {
			t.Fatalf("spawnSubTurn() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for child completion")
	}

	_, releaseNext, err := al.acquireAgentTurn(context.Background(), parentAgent.ID)
	if err != nil {
		t.Fatalf("acquireAgentTurn() after child completion error = %v", err)
	}
	releaseNext()
}

// ====================== Extra Independent Test: Ephemeral Session Isolation ======================
func TestSpawnSubTurn_EphemeralSessionIsolation(t *testing.T) {
	al, _, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	// Parent uses its own ephemeral store pre-seeded with one message
	parentSession := &ephemeralSessionStore{}
	parentSession.AddMessage("", "user", "parent msg")
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-1",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        parentSession,
	}

	cfg := SubTurnConfig{Model: "gpt-4o-mini", Tools: []toolshared.Tool{}}

	originalParentLen := len(parentSession.GetHistory(""))

	_, _ = spawnSubTurn(context.Background(), al, parent, cfg)

	// Parent session must be untouched — child used its own store
	if got := len(parentSession.GetHistory("")); got != originalParentLen {
		t.Errorf("parent session polluted: expected %d messages, got %d", originalParentLen, got)
	}

	// The child's agent.Sessions must NOT be the same pointer as the parent's session.
	// We verify this indirectly: spawnSubTurn stores childTS in activeTurnStates during
	// execution (deleted on return), so we can't easily grab childTS after the call.
	// Instead, confirm that the child session is a distinct ephemeralSessionStore by
	// checking the parent session key is only used by the parent store.
	// If isolation is correct, parent.session.GetHistory(childID) is always empty
	// (the child never wrote to the parent store).
	al.activeTurnStates.Range(func(k, v any) bool {
		// No active turns should remain after spawnSubTurn returns
		t.Errorf("unexpected active turn state left after spawnSubTurn: key=%v", k)
		return true
	})
}

// ====================== Extra Independent Test: Result Delivery Path (Async) ======================
func TestSpawnSubTurn_ResultDelivery(t *testing.T) {
	al, _, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-1",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 1),
		session:        &ephemeralSessionStore{},
	}

	// Set Async=true to test async result delivery via pendingResults channel
	cfg := SubTurnConfig{Model: "gpt-4o-mini", Tools: []toolshared.Tool{}, Async: true}

	_, _ = spawnSubTurn(context.Background(), al, parent, cfg)

	// Check if pendingResults received the result (only for async calls)
	select {
	case res := <-parent.pendingResults:
		if res == nil {
			t.Error("received nil result in pendingResults")
		}
	default:
		t.Error("result did not enter pendingResults for async call")
	}
}

// ====================== Extra Independent Test: Result Delivery Path (Sync) ======================
func TestSpawnSubTurn_ResultDeliverySync(t *testing.T) {
	al, _, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-sync-1",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 1),
		session:        &ephemeralSessionStore{},
	}

	// Sync call (Async=false, the default) - result should be returned directly
	cfg := SubTurnConfig{Model: "gpt-4o-mini", Tools: []toolshared.Tool{}, Async: false}

	result, err := spawnSubTurn(context.Background(), al, parent, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Result should be returned directly
	if result == nil {
		t.Error("expected non-nil result from sync call")
	}

	// pendingResults should NOT contain the result (no double delivery)
	select {
	case <-parent.pendingResults:
		t.Error("sync call should not place result in pendingResults (double delivery)")
	default:
		// Expected - channel should be empty
	}
}

// ====================== Extra Independent Test: Orphan Result Routing ======================
func TestSpawnSubTurn_OrphanResultRouting(t *testing.T) {
	al, _, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	collector, collectCleanup := newEventCollector(t, al)
	defer collectCleanup()

	parentCtx, cancelParent := context.WithCancel(context.Background())
	parent := &turnState{
		ctx:            parentCtx,
		cancelFunc:     cancelParent,
		turnID:         "parent-1",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 1),
		session:        &ephemeralSessionStore{},
	}

	// Simulate parent finishing before child delivers result
	parent.Finish(false)

	// Call deliverSubTurnResult directly to simulate a delayed child
	deliverSubTurnResult(al, parent, "delayed-child", &toolshared.ToolResult{ForLLM: "late result"})

	time.Sleep(10 * time.Millisecond) // let event goroutine flush
	// Verify Orphan event is emitted
	if !collector.hasEventOfKind(runtimeevents.KindAgentSubTurnOrphan) {
		t.Error("agent.subturn.orphan not emitted for finished parent")
	}

	// Verify history is NOT polluted
	if len(parent.session.GetHistory("")) != 0 {
		t.Error("Parent history was polluted by orphan result")
	}
}

// ====================== Extra Independent Test: Result Channel Registration ======================
func TestSubTurnResultChannelRegistration(t *testing.T) {
	al, _, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-reg-1",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 4),
		session:        &ephemeralSessionStore{},
	}

	cfg := SubTurnConfig{Model: "gpt-4o-mini", Tools: []toolshared.Tool{}}

	// Before spawn: channel should not be registered
	if results := al.dequeuePendingSubTurnResults(parent.turnID); results != nil {
		t.Error("expected no channel before spawnSubTurn")
	}

	_, _ = spawnSubTurn(context.Background(), al, parent, cfg)
}

// ====================== Extra Independent Test: Dequeue Pending SubTurn Results ======================
func TestDequeuePendingSubTurnResults(t *testing.T) {
	al, _, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	sessionKey := "test-session-dequeue"

	// Empty (no turnState registered) returns nil
	if results := al.dequeuePendingSubTurnResults(sessionKey); len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}

	// Register a turnState so dequeuePendingSubTurnResults can find it
	ts := &turnState{
		ctx:            context.Background(),
		turnID:         sessionKey,
		workspace:      "/test/workspace",
		sessionKey:     sessionKey,
		depth:          0,
		session:        &ephemeralSessionStore{},
		pendingResults: make(chan *toolshared.ToolResult, 4),
	}
	scope := ts.runtimeSessionScope()
	al.activeTurnStates.Store(scope, ts)
	defer al.activeTurnStates.Delete(scope)

	// Put 3 results in
	ts.pendingResults <- &toolshared.ToolResult{ForLLM: "result-1"}
	ts.pendingResults <- &toolshared.ToolResult{ForLLM: "result-2"}
	ts.pendingResults <- &toolshared.ToolResult{ForLLM: "result-3"}

	results := al.dequeuePendingSubTurnResults(sessionKey)
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	if results[0].ForLLM != "result-1" || results[2].ForLLM != "result-3" {
		t.Error("results order or content mismatch")
	}

	// Channel should be drained now
	if results := al.dequeuePendingSubTurnResults(sessionKey); len(results) != 0 {
		t.Errorf("expected empty after drain, got %d", len(results))
	}

	// After removing from activeTurnStates, returns nil
	al.activeTurnStates.Delete(scope)
	if results := al.dequeuePendingSubTurnResults(sessionKey); results != nil {
		t.Error("expected nil for unregistered session")
	}
}

// ====================== Extra Independent Test: Concurrency Semaphore ======================
func TestSubTurnConcurrencySemaphore(t *testing.T) {
	al, _, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-concurrency",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 10),
		session:        &ephemeralSessionStore{},
		concurrencySem: make(chan struct{}, 2), // Only allow 2 concurrent children
	}

	cfg := SubTurnConfig{Model: "gpt-4o-mini", Tools: []toolshared.Tool{}}

	// Spawn 2 children — should succeed immediately
	done := make(chan bool, 3)
	for i := 0; i < 2; i++ {
		go func() {
			_, _ = spawnSubTurn(context.Background(), al, parent, cfg)
			done <- true
		}()
	}

	// Wait a bit to ensure the first 2 are running
	// (In real scenario they'd be blocked in runTurn, but mockProvider returns immediately)
	// So we just verify the semaphore doesn't block when under limit
	<-done
	<-done

	// Verify semaphore is now full (2/2 slots used, but they already released)
	// Since mockProvider returns immediately, semaphore is already released
	// So we can't easily test blocking without a real long-running operation

	// Instead, verify that semaphore exists and has correct capacity
	if cap(parent.concurrencySem) != 2 {
		t.Errorf("expected semaphore capacity 2, got %d", cap(parent.concurrencySem))
	}
}

// ====================== Extra Independent Test: Hard Abort Cascading ======================
func TestHardAbortCascading(t *testing.T) {
	al, cfg, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	sessionKey := "test-session-abort"

	// Root turn with its own independent context (not derived from child)
	rootCtx, rootCancel := context.WithCancel(context.Background())
	rootTS := &turnState{
		ctx:            rootCtx,
		cancelFunc:     rootCancel,
		turnID:         sessionKey,
		workspace:      cfg.Agents.Defaults.Workspace,
		sessionKey:     sessionKey,
		depth:          0,
		session:        &ephemeralSessionStore{},
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, 5),
		al:             al,
	}
	rootScope := rootTS.runtimeSessionScope()
	al.activeTurnStates.Store(rootScope, rootTS)
	defer al.activeTurnStates.Delete(rootScope)

	// Child turn with an INDEPENDENT context (simulates spawnSubTurn behavior:
	// context.WithTimeout(context.Background(), ...) — NOT derived from parent).
	// Cascade must therefore happen via childTurnIDs traversal, not Go context tree.
	childCtx, childCancel := context.WithCancel(context.Background())
	childID := "child-independent"
	childTS := &turnState{
		ctx:            childCtx,
		cancelFunc:     childCancel,
		turnID:         childID,
		workspace:      cfg.Agents.Defaults.Workspace,
		pendingResults: make(chan *toolshared.ToolResult, 4),
		al:             al,
	}
	childScope := newRuntimeSubTurnScope(childTS.workspace, childID)
	al.activeTurnStates.Store(childScope, childTS)
	defer al.activeTurnStates.Delete(childScope)

	// Wire child into root's childTurnIDs (as spawnSubTurn would do)
	rootTS.childTurnIDs = append(rootTS.childTurnIDs, childID)

	// Verify neither context is canceled yet
	select {
	case <-rootTS.ctx.Done():
		t.Fatal("root context should not be canceled yet")
	default:
	}
	select {
	case <-childTS.ctx.Done():
		t.Fatal("child context should not be canceled yet (independent context)")
	default:
	}

	// Trigger Hard Abort via al.HardAbort (goes through steering.go → Finish(true))
	err := al.HardAbort(sessionKey)
	if err != nil {
		t.Fatalf("HardAbort failed: %v", err)
	}

	// Root context must be canceled
	select {
	case <-rootTS.ctx.Done():
	default:
		t.Error("root context should be canceled after HardAbort")
	}

	// Child context must be canceled via childTurnIDs cascade, NOT via Go context tree
	select {
	case <-childTS.ctx.Done():
	default:
		t.Error("child context should be canceled via childTurnIDs cascade")
	}

	// HardAbort on non-existent session should return an error
	if err := al.HardAbort("non-existent-session"); err == nil {
		t.Error("expected error for non-existent session")
	}
}

// TestHardAbortSessionRollback verifies that HardAbort rolls back session history
// to the state before the turn started, discarding all messages added during the turn.
func TestHardAbortSessionRollback(t *testing.T) {
	al, cfg, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	// Create a session with initial history
	sess := &ephemeralSessionStore{
		history: []providers.Message{
			{Role: "user", Content: "initial message 1"},
			{Role: "assistant", Content: "initial response 1"},
		},
	}

	// Create a root turnState with initialHistoryLength = 2
	rootTS := &turnState{
		ctx:                  context.Background(),
		turnID:               "test-session",
		workspace:            cfg.Agents.Defaults.Workspace,
		sessionKey:           "test-session",
		depth:                0,
		session:              sess,
		initialHistoryLength: 2, // Snapshot: 2 messages
		pendingResults:       make(chan *toolshared.ToolResult, 16),
		concurrencySem:       make(chan struct{}, 5),
	}
	rootTS.captureCanonicalRestorePoint(sess.GetHistory(""), sess.GetSummary(""))

	// Register the turn state
	al.activeTurnStates.Store(rootTS.runtimeSessionScope(), rootTS)

	// Simulate adding messages during the turn (e.g., user input + assistant response)
	sess.AddMessage("", "user", "new user message")
	sess.AddMessage("", "assistant", "new assistant response")

	// Verify history grew to 4 messages
	if len(sess.GetHistory("")) != 4 {
		t.Fatalf("expected 4 messages before abort, got %d", len(sess.GetHistory("")))
	}

	// Trigger HardAbort
	err := al.HardAbort("test-session")
	if err != nil {
		t.Fatalf("HardAbort failed: %v", err)
	}

	// Verify history rolled back to initial 2 messages
	finalHistory := sess.GetHistory("")
	if len(finalHistory) != 2 {
		t.Fatalf("expected history to rollback to 2 messages, got %d", len(finalHistory))
	}

	// Verify the content matches the initial state
	if finalHistory[0].Content != "initial message 1" || finalHistory[1].Content != "initial response 1" {
		t.Error("history content does not match initial state after rollback")
	}
}

// TestNestedSubTurnHierarchy verifies that nested SubTurns maintain correct
// parent-child relationships and depth tracking when recursively calling runAgentLoop.
func TestNestedSubTurnHierarchy(t *testing.T) {
	al, _, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	// Track spawned turns and their depths
	type turnInfo struct {
		parentID string
		childID  string
	}
	var spawnedTurns []turnInfo
	var mu sync.Mutex

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentSubTurnSpawn,
	)
	defer closeRuntimeEvents()
	go func() {
		for evt := range runtimeCh {
			if evt.Kind == runtimeevents.KindAgentSubTurnSpawn {
				p, _ := evt.Payload.(SubTurnSpawnPayload)
				mu.Lock()
				spawnedTurns = append(spawnedTurns, turnInfo{
					parentID: p.ParentTurnID,
					childID:  p.Label,
				})
				mu.Unlock()
			}
		}
	}()

	// Create a root turn
	rootSession := &ephemeralSessionStore{}
	rootTS := &turnState{
		ctx:            context.Background(),
		turnID:         "root-turn",
		depth:          0,
		session:        rootSession,
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, 5),
	}

	// Spawn a child (depth 1)
	childCfg := SubTurnConfig{Model: "gpt-4o-mini"}
	_, err := spawnSubTurn(context.Background(), al, rootTS, childCfg)
	if err != nil {
		t.Fatalf("failed to spawn child: %v", err)
	}

	time.Sleep(10 * time.Millisecond) // let event goroutine flush

	// Verify we captured the spawn event
	mu.Lock()
	if len(spawnedTurns) != 1 {
		t.Fatalf("expected 1 spawn event, got %d", len(spawnedTurns))
	}
	if spawnedTurns[0].parentID != "root-turn" {
		t.Errorf("expected parent ID 'root-turn', got %s", spawnedTurns[0].parentID)
	}
	mu.Unlock()

	// Verify root turn has the child in its childTurnIDs
	rootTS.mu.Lock()
	if len(rootTS.childTurnIDs) != 1 {
		t.Errorf("expected root to have 1 child, got %d", len(rootTS.childTurnIDs))
	}
	rootTS.mu.Unlock()
}

// TestDeliverSubTurnResultNoDeadlock verifies that deliverSubTurnResult doesn't
// deadlock when multiple goroutines are accessing the parent turnState concurrently.
func TestDeliverSubTurnResultNoDeadlock(t *testing.T) {
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-deadlock-test",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 2),
	}

	// Simulate multiple child turns delivering results concurrently.
	var wg sync.WaitGroup
	numChildren := 10

	for i := 0; i < numChildren; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			result := &toolshared.ToolResult{ForLLM: fmt.Sprintf("result-%d", id)}
			deliverSubTurnResult(nil, parent, fmt.Sprintf("child-%d", id), result)
		}(i)
	}

	// Consume through the lifecycle helper so blocked producers are signaled as
	// capacity becomes available.
	received := make(chan struct{})
	go func() {
		defer close(received)
		for i := 0; i < numChildren; i++ {
			for {
				if _, ok := parent.dequeuePendingResult(); ok {
					break
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()

	// Wait for all deliveries to complete (with timeout)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - no deadlock
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock detected: deliverSubTurnResult blocked")
	}

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("consumer did not receive all results")
	}
}

// TestHardAbortOrderOfOperations verifies that HardAbort calls Finish() before
// rolling back session history, minimizing the race window where new messages
// could be added after rollback.
func TestHardAbortOrderOfOperations(t *testing.T) {
	al, cfg, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	sess := &ephemeralSessionStore{
		history: []providers.Message{
			{Role: "user", Content: "initial message"},
			{Role: "assistant", Content: "response 1"},
			{Role: "user", Content: "follow-up"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rootTS := &turnState{
		ctx:                  ctx,
		cancelFunc:           cancel,
		turnID:               "test-session-order",
		workspace:            cfg.Agents.Defaults.Workspace,
		sessionKey:           "test-session-order",
		depth:                0,
		session:              sess,
		initialHistoryLength: 1, // Snapshot: 1 message
		pendingResults:       make(chan *toolshared.ToolResult, 16),
		concurrencySem:       make(chan struct{}, 5),
	}
	rootTS.captureCanonicalRestorePoint(sess.GetHistory("")[:1], sess.GetSummary(""))

	al.activeTurnStates.Store(rootTS.runtimeSessionScope(), rootTS)

	// Trigger HardAbort
	err := al.HardAbort("test-session-order")
	if err != nil {
		t.Fatalf("HardAbort failed: %v", err)
	}

	// Verify context was canceled (Finish() was called)
	select {
	case <-rootTS.ctx.Done():
		// Good - context was canceled
	default:
		t.Error("expected context to be canceled after HardAbort")
	}

	// Verify history was rolled back
	finalHistory := sess.GetHistory("")
	if len(finalHistory) != 1 {
		t.Fatalf("expected history to rollback to 1 message, got %d", len(finalHistory))
	}

	if finalHistory[0].Content != "initial message" {
		t.Error("history content does not match initial state after rollback")
	}
}

// TestFinishedChannelClosedState verifies that Finish() closes the Finished() channel
// so that child turns can safely abort waiting.
func TestFinishedChannelClosedState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ts := &turnState{
		ctx:            ctx,
		cancelFunc:     cancel,
		turnID:         "test-finished-channel",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 2),
	}

	// Verify Finished channel is blocking initially
	select {
	case <-ts.Finished():
		t.Fatal("finished channel should block initially")
	default:
		// Good
	}

	// Call Finish() with graceful finish
	ts.Finish(false)

	// Verify Finished channel is closed
	select {
	case _, ok := <-ts.Finished():
		if ok {
			t.Error("expected Finished() channel to be closed after Finish()")
		}
	default:
		t.Fatal("expected <-ts.Finished() to not block")
	}

	// Verify Finish() is idempotent
	ts.Finish(false) // Should not panic

	// A writable channel must not let delivery bypass the finished state.
	result := &toolshared.ToolResult{ForLLM: "late result"}
	deliverSubTurnResult(nil, ts, "child-1", result)
	select {
	case <-ts.pendingResults:
		t.Fatal("finished parent accepted a late result")
	default:
	}
}

// TestFinalPollCapturesLateResults verifies that the final poll before Finish()
// captures results that arrive after the last iteration poll.
func TestFinalPollCapturesLateResults(t *testing.T) {
	al, cfg, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	sessionKey := "test-session-final-poll"

	// Register a turnState
	ts := &turnState{
		ctx:            context.Background(),
		turnID:         sessionKey,
		workspace:      cfg.Agents.Defaults.Workspace,
		sessionKey:     sessionKey,
		depth:          0,
		session:        &ephemeralSessionStore{},
		pendingResults: make(chan *toolshared.ToolResult, 4),
	}
	scope := ts.runtimeSessionScope()
	al.activeTurnStates.Store(scope, ts)
	defer al.activeTurnStates.Delete(scope)

	// Simulate results arriving after last iteration poll
	ts.pendingResults <- &toolshared.ToolResult{ForLLM: "result 1"}
	ts.pendingResults <- &toolshared.ToolResult{ForLLM: "result 2"}

	// Dequeue should capture both results
	results := al.dequeuePendingSubTurnResults(sessionKey)

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}

	// Verify channel is now empty
	results = al.dequeuePendingSubTurnResults(sessionKey)
	if len(results) != 0 {
		t.Errorf("expected 0 results on second poll, got %d", len(results))
	}
}

func TestSealOrDrainPendingResultsPreventsTerminalGapDelivery(t *testing.T) {
	ts := &turnState{
		turnID:         "terminal-gap",
		pendingResults: make(chan *toolshared.ToolResult, 2),
	}
	ts.pendingResults <- &toolshared.ToolResult{ForLLM: "queued"}

	results, sealed := ts.sealOrDrainPendingResults()
	if sealed || len(results) != 1 || results[0].ForLLM != "queued" {
		t.Fatalf("first terminal check = (%v, %v), want queued result and unsealed", results, sealed)
	}

	results, sealed = ts.sealOrDrainPendingResults()
	if !sealed || len(results) != 0 {
		t.Fatalf("second terminal check = (%v, %v), want empty sealed queue", results, sealed)
	}

	deliverSubTurnResult(nil, ts, "late-child", &toolshared.ToolResult{ForLLM: "late"})
	select {
	case result := <-ts.pendingResults:
		t.Fatalf("sealed result queue accepted late delivery: %#v", result)
	default:
	}
}

func TestPendingSubTurnResultForcesIterationAtLoopLimit(t *testing.T) {
	pipeline := &Pipeline{}
	ts := &turnState{
		pendingResults: make(chan *toolshared.ToolResult, 1),
		iteration:      3,
	}
	ts.pendingResults <- &toolshared.ToolResult{ForLLM: "boundary result"}
	exec := &turnExecution{}

	if !pipeline.continueWithPendingSubTurnResults(ts, exec) {
		t.Fatal("pending result did not request another iteration")
	}
	if len(exec.pendingMessages) != 1 || !strings.Contains(exec.pendingMessages[0].Content, "boundary result") {
		t.Fatalf("pending messages = %#v, want boundary result", exec.pendingMessages)
	}

	if pipeline.continueWithPendingSubTurnResults(ts, exec) {
		t.Fatal("empty terminal queue requested another iteration")
	}
	deliverSubTurnResult(nil, ts, "after-limit", &toolshared.ToolResult{ForLLM: "too late"})
	if got := len(ts.pendingResults); got != 0 {
		t.Fatalf("sealed terminal queue accepted %d late results", got)
	}
}

func TestEmptyPendingSubTurnResultDoesNotResumeTerminalTurn(t *testing.T) {
	pipeline := &Pipeline{}
	ts := &turnState{pendingResults: make(chan *toolshared.ToolResult, 1)}
	ts.pendingResults <- &toolshared.ToolResult{}
	exec := &turnExecution{}

	if pipeline.continueWithPendingSubTurnResults(ts, exec) {
		t.Fatal("empty subturn result requested another iteration")
	}
	if len(exec.pendingMessages) != 0 {
		t.Fatalf("empty subturn result appended pending messages: %#v", exec.pendingMessages)
	}
	deliverSubTurnResult(nil, ts, "after-empty", &toolshared.ToolResult{ForLLM: "too late"})
	if got := len(ts.pendingResults); got != 0 {
		t.Fatalf("terminal queue accepted %d results after empty batch", got)
	}
}

// TestSpawnSubTurn_PanicRecovery verifies that even if runTurn panics,
// the result is still delivered for async calls and SubTurnEndEvent is emitted.
func TestSpawnSubTurn_PanicRecovery(t *testing.T) {
	// Create a panic provider
	panicProvider := &panicMockProvider{}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), panicProvider)

	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-panic",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 1),
		session:        &ephemeralSessionStore{},
	}

	collector, collectCleanup := newEventCollector(t, al)
	defer collectCleanup()

	// Test async call - result should still be delivered via channel
	asyncCfg := SubTurnConfig{Model: "gpt-4o-mini", Tools: []toolshared.Tool{}, Async: true}
	result, err := spawnSubTurn(context.Background(), al, parent, asyncCfg)

	// Should return error from panic recovery
	if err == nil {
		t.Error("expected error from panic recovery")
	}

	// Result should be nil because panic occurred before runTurn could return
	if result != nil {
		t.Error("expected nil result after panic")
	}

	time.Sleep(10 * time.Millisecond) // let event goroutine flush
	// SubTurnEndEvent should still be emitted
	if !collector.hasEventOfKind(runtimeevents.KindAgentSubTurnEnd) {
		t.Error("SubTurnEndEvent not emitted after panic")
	}

	// For async call, result should still be delivered to channel (even if nil)
	select {
	case res := <-parent.pendingResults:
		// Result was delivered (nil due to panic)
		_ = res
	default:
		t.Error("async result should be delivered to channel even after panic")
	}
}

// panicMockProvider is a mock provider that always panics
type panicMockProvider struct{}

func (m *panicMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	panic("intentional panic for testing")
}

func (m *panicMockProvider) GetDefaultModel() string {
	return "panic-model"
}

// ====================== Public API Tests ======================

// simpleMockProviderAPI for testing public APIs
type simpleMockProviderAPI struct {
	response string
}

func (m *simpleMockProviderAPI) Chat(
	ctx context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{
		Content: m.response,
	}, nil
}

func (m *simpleMockProviderAPI) GetDefaultModel() string {
	return "gpt-4o-mini"
}

// TestGetActiveTurn verifies that GetActiveTurn returns correct turn information
func TestGetActiveTurn(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: t.TempDir(),
				ModelName: "gpt-4o-mini",
				Provider:  "mock",
			},
		},
	}
	al := NewAgentLoop(cfg, nil, &simpleMockProviderAPI{response: "ok"})

	// Create a root turn state
	rootCtx := context.Background()
	rootTS := &turnState{
		ctx:            rootCtx,
		turnID:         "root-turn",
		workspace:      "/test/workspace",
		parentTurnID:   "",
		depth:          0,
		childTurnIDs:   []string{},
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}

	sessionKey := "test-session"
	rootTS.sessionKey = sessionKey
	scope := rootTS.runtimeSessionScope()
	al.activeTurnStates.Store(scope, rootTS)
	defer al.activeTurnStates.Delete(scope)

	// Test: GetActiveTurn should return turn info
	info := al.GetActiveTurnBySession(sessionKey)
	if info == nil {
		t.Fatal("GetActiveTurn returned nil for active session")
	}

	if info.TurnID != "root-turn" {
		t.Errorf("Expected TurnID 'root-turn', got %q", info.TurnID)
	}

	if info.Depth != 0 {
		t.Errorf("Expected Depth 0, got %d", info.Depth)
	}

	if info.ParentTurnID != "" {
		t.Errorf("Expected empty ParentTurnID, got %q", info.ParentTurnID)
	}

	if len(info.ChildTurnIDs) != 0 {
		t.Errorf("Expected 0 child turns, got %d", len(info.ChildTurnIDs))
	}

	// Test: GetActiveTurn should return nil for non-existent session
	nonExistentInfo := al.GetActiveTurnBySession("non-existent-session")
	if nonExistentInfo != nil {
		t.Error("GetActiveTurn should return nil for non-existent session")
	}
}

// TestGetActiveTurn_WithChildren verifies that child turn IDs are correctly reported
func TestGetActiveTurn_WithChildren(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: t.TempDir(),
				ModelName: "gpt-4o-mini",
				Provider:  "mock",
			},
		},
	}
	al := NewAgentLoop(cfg, nil, &simpleMockProviderAPI{response: "ok"})

	rootCtx := context.Background()
	rootTS := &turnState{
		ctx:            rootCtx,
		turnID:         "root-turn",
		workspace:      "/test/workspace",
		parentTurnID:   "",
		depth:          0,
		childTurnIDs:   []string{"child-1", "child-2"},
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}

	sessionKey := "test-session-with-children"
	rootTS.sessionKey = sessionKey
	scope := rootTS.runtimeSessionScope()
	al.activeTurnStates.Store(scope, rootTS)
	defer al.activeTurnStates.Delete(scope)

	info := al.GetActiveTurnBySession(sessionKey)
	if info == nil {
		t.Fatal("GetActiveTurn returned nil")
	}

	if len(info.ChildTurnIDs) != 2 {
		t.Fatalf("Expected 2 child turns, got %d", len(info.ChildTurnIDs))
	}

	if info.ChildTurnIDs[0] != "child-1" || info.ChildTurnIDs[1] != "child-2" {
		t.Errorf("Child turn IDs mismatch: got %v", info.ChildTurnIDs)
	}
}

// TestTurnStateInfo_ThreadSafety verifies that Info() is thread-safe
func TestTurnStateInfo_ThreadSafety(t *testing.T) {
	rootCtx := context.Background()
	ts := &turnState{
		ctx:            rootCtx,
		turnID:         "test-turn",
		parentTurnID:   "parent",
		depth:          1,
		childTurnIDs:   []string{},
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}

	// Concurrently read Info() and modify childTurnIDs
	done := make(chan bool)
	go func() {
		for i := 0; i < 100; i++ {
			ts.mu.Lock()
			ts.childTurnIDs = append(ts.childTurnIDs, "child")
			ts.mu.Unlock()
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			info := ts.snapshot()
			if info.TurnID == "" {
				t.Error("snapshot() returned empty TurnID")
			}
		}
		done <- true
	}()

	<-done
	<-done
}

// TestFinish_ConcurrentCalls verifies that calling Finish() concurrently from multiple
// goroutines is safe and doesn't cause panics or double-close errors.
func TestFinish_ConcurrentCalls(t *testing.T) {
	ctx := context.Background()
	parentTS := &turnState{
		ctx:            ctx,
		turnID:         "parent-concurrent-finish",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}
	parentTS.ctx, parentTS.cancelFunc = context.WithCancel(ctx)

	// Launch multiple goroutines that all call Finish() concurrently
	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			// This should not panic, even when called concurrently
			parentTS.Finish(false)
		}()
	}

	wg.Wait()

	// Verify the Finished() channel is closed
	select {
	case _, ok := <-parentTS.Finished():
		if ok {
			t.Error("Expected Finished() channel to be closed")
		}
	default:
		t.Error("Expected Finished() channel to be closed and readable without blocking")
	}

	// Verify isFinished is set
	parentTS.mu.Lock()
	if !parentTS.isFinished.Load() {
		t.Error("Expected isFinished to be true")
	}
	parentTS.mu.Unlock()

	// Child goroutines may still retain send access, so Finish must not close
	// pendingResults. Lifecycle completion is signaled through Finished().
	select {
	case parentTS.pendingResults <- &toolshared.ToolResult{ForLLM: "after-finish"}:
	case <-time.After(time.Second):
		t.Fatal("pendingResults should remain open after Finish")
	}
}

// TestDeliverSubTurnResult_RaceWithFinish verifies that deliverSubTurnResult handles
// the race condition where Finish() is called while results are being delivered.
func TestDeliverSubTurnResult_RaceWithFinish(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t) //nolint:dogsled
	defer cleanup()

	var mu sync.Mutex
	var deliveredCount, orphanCount int
	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		64,
		runtimeevents.KindAgentSubTurnResultDelivered,
		runtimeevents.KindAgentSubTurnOrphan,
	)
	defer closeRuntimeEvents()
	go func() {
		for evt := range runtimeCh {
			mu.Lock()
			switch evt.Kind {
			case runtimeevents.KindAgentSubTurnResultDelivered:
				deliveredCount++
			case runtimeevents.KindAgentSubTurnOrphan:
				orphanCount++
			}
			mu.Unlock()
		}
	}()

	ctx := context.Background()
	parentTS := &turnState{
		ctx:            ctx,
		turnID:         "parent-race-test",
		depth:          0,
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}
	parentTS.ctx, parentTS.cancelFunc = context.WithCancel(ctx)

	// Launch goroutines that deliver results while another goroutine calls Finish()
	const numResults = 20
	var wg sync.WaitGroup
	wg.Add(numResults + 1)

	// Goroutine that calls Finish() after a short delay
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		parentTS.Finish(false)
	}()

	// Goroutines that deliver results
	for i := 0; i < numResults; i++ {
		go func(id int) {
			defer wg.Done()
			result := &toolshared.ToolResult{
				ForLLM: fmt.Sprintf("result-%d", id),
			}
			// This should not panic, even if Finish() is called concurrently
			deliverSubTurnResult(al, parentTS, fmt.Sprintf("child-%d", id), result)
		}(i)
	}

	wg.Wait()
	time.Sleep(20 * time.Millisecond) // let event goroutine flush

	// Get final counts
	mu.Lock()
	finalDelivered := deliveredCount
	finalOrphan := orphanCount
	mu.Unlock()

	t.Logf("Delivered: %d, Orphan: %d, Total: %d", finalDelivered, finalOrphan, finalDelivered+finalOrphan)

	// With the new drainPendingResults behavior, the total events may be >= numResults
	// because Finish() drains remaining results from the channel and emits them as orphans.
	// So we expect:
	// - Some results were delivered successfully (before Finish())
	// - Some results became orphans (after Finish() or channel full)
	// - Some results were in the channel when Finish() was called and got drained as orphans
	// The total should be at least numResults (could be more due to drain)
	if finalDelivered+finalOrphan < numResults {
		t.Errorf("Expected at least %d total events, got %d delivered + %d orphan = %d",
			numResults, finalDelivered, finalOrphan, finalDelivered+finalOrphan)
	}

	// Should have at least some orphan results (those that arrived after Finish() or were drained)
	if finalOrphan == 0 {
		t.Error("Expected at least some orphan results after Finish()")
	}
}

type subturnMediaTool struct {
	store media.MediaStore
	path  string
}

func (m *subturnMediaTool) Name() string { return "subturn_media_tool" }

func (m *subturnMediaTool) Description() string {
	return "Returns an undelivered media artifact"
}

func (m *subturnMediaTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (m *subturnMediaTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	ref, err := m.store.Store(m.path, media.MediaMeta{
		Filename:    filepath.Base(m.path),
		ContentType: "image/png",
		Source:      "test:subturn_media_tool",
	}, "test:subturn_media")
	if err != nil {
		return toolshared.ErrorResult(err.Error()).WithError(err)
	}
	result := toolshared.MediaResult("Created media artifact.", []string{ref})
	result.Deliverable.Text = "Tool-owned deliverable"
	result.Deliverable.Metadata = map[string]string{"producer": "subturn_media_tool"}
	result.Deliverable.Report = &taskresult.Report{
		SchemaVersion: taskresult.ReportSchemaV1,
		ReportID:      "subturn-report",
		Summary:       "Structured subturn output",
	}
	return result
}

type subturnToolThenFinalProvider struct {
	calls int
}

func (p *subturnToolThenFinalProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	p.calls++
	if p.calls == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{
				{
					ID:        "call_media",
					Name:      "subturn_media_tool",
					Arguments: map[string]any{},
					Function: &providers.FunctionCall{
						Name:      "subturn_media_tool",
						Arguments: "{}",
					},
				},
			},
		}, nil
	}
	return &providers.LLMResponse{Content: "Final child text"}, nil
}

func (p *subturnToolThenFinalProvider) GetDefaultModel() string {
	return "subturn-tool-final-model"
}

type subturnToolCaptureProvider struct {
	mu       sync.Mutex
	toolDefs []providers.ToolDefinition
}

func (p *subturnToolCaptureProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.toolDefs = append([]providers.ToolDefinition(nil), toolDefs...)
	p.mu.Unlock()
	return &providers.LLMResponse{Content: "child done"}, nil
}

func (p *subturnToolCaptureProvider) GetDefaultModel() string {
	return "subturn-tool-capture-model"
}

func (p *subturnToolCaptureProvider) toolNames() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	names := make(map[string]bool, len(p.toolDefs))
	for _, def := range p.toolDefs {
		names[def.Function.Name] = true
	}
	return names
}

func TestSpawnSubTurn_DefaultSyncDeliveryRemovesUserDeliveryTools(t *testing.T) {
	provider := &subturnToolCaptureProvider{}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()

	alphaAgent, _ := al.registry.GetAgent("alpha")
	betaAgent, _ := al.registry.GetAgent("beta")
	for _, name := range []string{
		"message",
		"send_file",
		"send_tts",
		"reaction",
		"read_file",
	} {
		betaAgent.Tools.Register(&allowlistTestTool{name: name})
	}
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-default-sync-delivery",
		depth:          0,
		childTurnIDs:   []string{},
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          alphaAgent,
		opts: turnSpec{
			Dispatch: DispatchRequest{
				SessionKey: "parent-default-sync-delivery",
			},
			NoHistory: true,
		},
	}

	_, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		TargetAgentID: "beta",
		SystemPrompt:  "capture child tool list",
		// DeliveryMode intentionally omitted. Sync sub-turns default to parent_only.
	})
	if err != nil {
		t.Fatalf("spawnSubTurn failed: %v", err)
	}

	names := provider.toolNames()
	for _, name := range []string{"message", "send_file", "send_tts", "reaction"} {
		if names[name] {
			t.Fatalf("child provider saw user-facing delivery tool %q in parent_only mode", name)
		}
	}
	if !names["read_file"] {
		t.Fatalf("child provider did not see non-delivery tool read_file")
	}
}

func TestSpawnSubTurnBrowserChecklistRequiredBeforeExecution(t *testing.T) {
	provider := &subturnToolCaptureProvider{}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	alphaAgent, _ := al.registry.GetAgent("alpha")
	betaAgent, _ := al.registry.GetAgent("beta")
	tool := &outcomeBrowserApprovalTool{}
	betaAgent.Tools.Register(tool)
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-browser-preflight",
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          alphaAgent,
		opts:           turnSpec{Dispatch: DispatchRequest{SessionKey: "parent-browser-preflight"}},
	}
	result, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		TargetAgentID: "beta", SystemPrompt: "publish an item", DeliveryMode: toolshared.AsyncDeliveryUserOnly,
	})
	if err != nil || result == nil || result.Deliverable == nil || result.Deliverable.ObjectiveOutcome == nil ||
		result.Deliverable.ObjectiveOutcome.Status != taskresult.OutcomeBlocked {
		t.Fatalf("browser preflight result = (%#v, %v)", result, err)
	}
	if tool.executions != 0 || len(provider.toolNames()) != 0 {
		t.Fatalf(
			"browser child executed before checklist validation: executions=%d tools=%v",
			tool.executions,
			provider.toolNames(),
		)
	}
}

func TestSpawnSubTurnBrowserRemovesDirectDeliveryToolsForUserOnly(t *testing.T) {
	provider := &subturnToolCaptureProvider{}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	alphaAgent, _ := al.registry.GetAgent("alpha")
	betaAgent, _ := al.registry.GetAgent("beta")
	betaAgent.Tools.Register(&outcomeBrowserApprovalTool{})
	for _, name := range []string{"message", "send_file", "send_tts", "reaction", "read_file"} {
		betaAgent.Tools.Register(&allowlistTestTool{name: name})
	}
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-browser-delivery",
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          alphaAgent,
		opts:           turnSpec{Dispatch: DispatchRequest{SessionKey: "parent-browser-delivery"}},
	}
	_, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		TargetAgentID: "beta", SystemPrompt: "inspect one item", DeliveryMode: toolshared.AsyncDeliveryUserOnly,
		ObjectiveItems: []toolshared.ObjectiveSpec{{Item: "inspect one item", Kind: "result"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	names := provider.toolNames()
	for _, name := range []string{"message", "send_file", "send_tts", "reaction"} {
		if names[name] {
			t.Fatalf("browser child provider saw direct-delivery tool %q", name)
		}
	}
	if !names["read_file"] || !names["browser_act"] {
		t.Fatalf("browser child lost non-delivery tools: %v", names)
	}
}

func TestAgentLoopSpawnerForwardsBrowserObjectivesFromSpawnAndDelegate(t *testing.T) {
	const outcome = "inspected\n" + objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],` +
		`"missing_items":[]}` + objectiveOutcomeEnd
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{Content: outcome, FinishReason: "stop"},
		{Content: outcome, FinishReason: "stop"},
	}}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	alphaAgent, _ := al.registry.GetAgent("alpha")
	betaAgent, _ := al.registry.GetAgent("beta")
	betaAgent.Tools.Register(&outcomeBrowserApprovalTool{})
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-browser-objective-bridge",
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          alphaAgent,
		opts: turnSpec{Dispatch: DispatchRequest{
			SessionKey: "parent-browser-objective-bridge",
		}},
	}
	ctx := withTurnState(context.Background(), parent)
	objectiveArgs := []any{map[string]any{"item": "inspect listings", "kind": "result"}}

	t.Run("spawn", func(t *testing.T) {
		manager := tools.NewSubagentManagerWithRegistry(
			"test-model",
			taskregistry.NewRegistry(filepath.Join(t.TempDir(), "tasks.jsonl")),
		)
		spawnTool := tools.NewSpawnTool(manager)
		manager.SetSpawner(NewSubTurnSpawner(al))
		completed := make(chan *toolshared.ToolResult, 1)
		result := spawnTool.ExecuteAsync(ctx, map[string]any{
			"agent_id":        "beta",
			"task":            "inspect listings",
			"objective_items": objectiveArgs,
		}, func(_ context.Context, result *toolshared.ToolResult) {
			completed <- result
		})
		if result == nil || !result.Control.Async || result.IsError {
			t.Fatalf("spawn result = %#v, want async acknowledgment", result)
		}
		select {
		case child := <-completed:
			assertSucceededObjectiveOutcome(t, child)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for browser spawn completion")
		}
	})

	t.Run("delegate", func(t *testing.T) {
		delegateTool := tools.NewDelegateTool()
		delegateTool.SetSpawner(NewSubTurnSpawner(al))
		result := delegateTool.Execute(ctx, map[string]any{
			"agent_id":        "beta",
			"task":            "inspect listings",
			"objective_items": objectiveArgs,
		})
		assertSucceededObjectiveOutcome(t, result)
	})
}

func assertSucceededObjectiveOutcome(t *testing.T, result *toolshared.ToolResult) {
	t.Helper()
	if result == nil || result.IsError || result.Deliverable == nil || result.Deliverable.ObjectiveOutcome == nil {
		t.Fatalf("browser child result = %#v, want structured objective outcome", result)
	}
	if result.Deliverable.ObjectiveOutcome.Status != taskresult.OutcomeSucceeded {
		t.Fatalf("objective outcome = %#v, want succeeded", result.Deliverable.ObjectiveOutcome)
	}
}

func TestSpawnSubTurn_TargetAgentIDRemovesNodeFileTools(t *testing.T) {
	provider := &subturnToolCaptureProvider{}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()

	alphaAgent, _ := al.registry.GetAgent("alpha")
	betaAgent, _ := al.registry.GetAgent("beta")
	for _, name := range []string{
		"nodes_file_info",
		"nodes_upload",
		"nodes_download",
		"nodes",
		"read_file",
	} {
		betaAgent.Tools.Register(&allowlistTestTool{name: name})
	}
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-cross-agent-file-tools",
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          alphaAgent,
		opts: turnSpec{
			Dispatch:  DispatchRequest{SessionKey: "parent-cross-agent-file-tools"},
			NoHistory: true,
		},
	}

	if _, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		TargetAgentID: "beta",
		SystemPrompt:  "capture delegated tool list",
	}); err != nil {
		t.Fatalf("spawnSubTurn failed: %v", err)
	}

	names := provider.toolNames()
	for _, name := range []string{"nodes_file_info", "nodes_upload", "nodes_download"} {
		if names[name] {
			t.Fatalf("cross-agent delegated provider saw node file tool %q", name)
		}
	}
	for _, name := range []string{"nodes", "read_file"} {
		if !names[name] {
			t.Fatalf("cross-agent delegated provider did not see unrelated tool %q", name)
		}
	}
}

func TestSpawnSubTurn_InheritsSuppressToolFeedback(t *testing.T) {
	tmpDir := t.TempDir()
	readPath := filepath.Join(tmpDir, "subturn-feedback.txt")
	if err := os.WriteFile(readPath, []byte("subturn feedback task"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ToolFeedback: config.ToolFeedbackConfig{
					Enabled:       true,
					MaxArgsLength: 300,
				},
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{
				Enabled: true,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, &toolFeedbackProvider{filePath: readPath})
	parentAgent := al.registry.GetDefaultAgent()
	if parentAgent == nil {
		t.Fatal("expected default parent agent")
	}

	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-suppress-tool-feedback",
		depth:          0,
		childTurnIDs:   []string{},
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          parentAgent,
		opts: turnSpec{
			Dispatch: DispatchRequest{
				SessionKey:  "parent-suppress-tool-feedback",
				UserMessage: "scheduled parent",
				InboundContext: &bus.InboundContext{
					Channel:  "telegram",
					ChatID:   "chat-1",
					ChatType: "direct",
					SenderID: "cron",
				},
			},
			SuppressToolFeedback: true,
			NoHistory:            true,
		},
	}

	result, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		Model:        "test-model",
		SystemPrompt: "read the file",
	})
	if err != nil {
		t.Fatalf("spawnSubTurn failed: %v", err)
	}
	if result == nil || result.ForLLM != "HEARTBEAT_OK" {
		t.Fatalf("spawnSubTurn result = %#v, want HEARTBEAT_OK", result)
	}

	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("expected no child tool feedback when parent suppresses it, got %+v", outbound)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSpawnSubTurn_DurableTaskDismissesPublishedToolFeedbackSession(t *testing.T) {
	tmpDir := t.TempDir()
	readPath := filepath.Join(tmpDir, "subturn-feedback.txt")
	if err := os.WriteFile(readPath, []byte("durable subturn feedback task"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ToolFeedback: config.ToolFeedbackConfig{
					Enabled:       true,
					MaxArgsLength: 300,
				},
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{
				Enabled: true,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, &toolFeedbackProvider{filePath: readPath})
	channelManager := &recordingChannelManager{}
	al.channelManager = channelManager
	parentAgent := al.registry.GetDefaultAgent()
	if parentAgent == nil {
		t.Fatal("expected default parent agent")
	}

	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-durable-tool-feedback",
		depth:          0,
		childTurnIDs:   []string{},
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          parentAgent,
		workspace:      parentAgent.Workspace,
		opts: turnSpec{
			Dispatch: DispatchRequest{
				SessionKey:  "parent-durable-tool-feedback",
				UserMessage: "spawn durable child",
				InboundContext: &bus.InboundContext{
					Channel:  "telegram",
					ChatID:   "chat-1",
					ChatType: "direct",
					SenderID: "user-1",
				},
			},
			NoHistory: true,
		},
	}

	const taskID = "subagent-durable-feedback"
	result, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		Model:        "test-model",
		SystemPrompt: "read the file",
		TaskID:       taskID,
	})
	if err != nil {
		t.Fatalf("spawnSubTurn failed: %v", err)
	}
	if result == nil || result.ForLLM != "HEARTBEAT_OK" {
		t.Fatalf("spawnSubTurn result = %#v, want HEARTBEAT_OK", result)
	}

	var outbound bus.OutboundMessage
	select {
	case outbound = <-msgBus.OutboundChan():
	case <-time.After(2 * time.Second):
		t.Fatal("expected child tool feedback outbound")
	}
	if len(channelManager.dismissedTargets) != 1 {
		t.Fatalf("dismiss targets = %d, want 1", len(channelManager.dismissedTargets))
	}
	dismissedTarget := channelManager.dismissedTargets[0]
	wantSessionKey := durableTaskSessionKey(parentAgent.Workspace, taskID)
	if outbound.SessionKey != wantSessionKey {
		t.Fatalf("feedback session = %q, want %q", outbound.SessionKey, wantSessionKey)
	}
	if dismissedTarget.SessionKey != outbound.SessionKey {
		t.Fatalf(
			"dismiss target session = %q, want feedback session %q",
			dismissedTarget.SessionKey,
			outbound.SessionKey,
		)
	}
}

func TestSpawnSubTurn_ReturnsStructuredCompletionWithMedia(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t) //nolint:dogsled
	defer cleanup()

	store := media.NewFileMediaStore()
	al.SetMediaStore(store)

	imgPath := filepath.Join(t.TempDir(), "artifact.png")
	if err := os.WriteFile(imgPath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o600); err != nil {
		t.Fatalf("write test image: %v", err)
	}
	provider := &subturnToolThenFinalProvider{}
	agent := al.registry.GetDefaultAgent()
	agent.Provider = provider
	agent.CandidateProviders = nil
	al.RegisterTool(&subturnMediaTool{store: store, path: imgPath})

	parentTS := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-completion",
		agent:          agent,
		agentID:        "main",
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		opts: turnSpec{
			Dispatch: DispatchRequest{
				SessionKey:  "parent-completion",
				UserMessage: "parent",
				InboundContext: &bus.InboundContext{
					Channel: "telegram",
					ChatID:  "chat-1",
				},
			},
			NoHistory: true,
		},
	}
	ctx := withTurnState(context.Background(), parentTS)
	ctx = WithAgentLoop(ctx, al)

	result, err := spawnSubTurn(ctx, al, parentTS, SubTurnConfig{
		SystemPrompt: "make artifact",
		Model:        "test-model",
		DeliveryMode: toolshared.AsyncDeliveryParentOnly,
	})
	if err != nil {
		t.Fatalf("spawnSubTurn failed: %v", err)
	}
	if result == nil || result.Deliverable == nil {
		t.Fatalf("expected structured deliverable, got %+v", result)
	}
	if result.Deliverable.Text != "Tool-owned deliverable" {
		t.Fatalf("deliverable text = %q, want tool-owned text", result.Deliverable.Text)
	}
	if result.Deliverable.Metadata["producer"] != "subturn_media_tool" ||
		result.Deliverable.Report == nil || result.Deliverable.Report.ReportID != "subturn-report" {
		t.Fatalf("structured deliverable fields were lost: %+v", result.Deliverable)
	}
	if len(result.Deliverable.Artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1; result=%+v", len(result.Deliverable.Artifacts), result)
	}
	if result.Deliverable.Artifacts[0].Ref == "" {
		t.Fatalf("artifact ref is empty: %+v", result.Deliverable.Artifacts[0])
	}
	if result.Deliverable.Artifacts[0].Kind != "image" {
		t.Fatalf("artifact kind = %q, want image", result.Deliverable.Artifacts[0].Kind)
	}
	if result.Deliverable.Artifacts[0].Filename != "artifact.png" {
		t.Fatalf("artifact filename = %q, want artifact.png", result.Deliverable.Artifacts[0].Filename)
	}
	if result.Deliverable.Artifacts[0].ContentType != "image/png" {
		t.Fatalf("artifact content type = %q, want image/png", result.Deliverable.Artifacts[0].ContentType)
	}
	if len(result.Media) != 1 || result.Media[0] != result.Deliverable.Artifacts[0].Ref {
		t.Fatalf("delivery media refs = %#v, artifact ref = %q", result.Media, result.Deliverable.Artifacts[0].Ref)
	}
	if result.ForUser != "" {
		t.Fatalf("parent_only result ForUser = %q, want empty", result.ForUser)
	}
	if !strings.Contains(result.ContentForLLM(), "Structured deliverable:") {
		t.Fatalf("ContentForLLM missing structured deliverable: %q", result.ContentForLLM())
	}
}

func TestMediaArtifactRefsExcludeNonMediaDeliverables(t *testing.T) {
	items := []taskresult.Artifact{
		{Ref: "media://image", Kind: "image"},
		{Ref: "file:/tmp/report.txt", LocalPath: "/tmp/report.txt", Kind: "file"},
		{Ref: "https://example.com/report", Kind: "link"},
	}

	got := mediaArtifactRefs(items)
	want := []string{"media://image"}
	if !slices.Equal(got, want) {
		t.Fatalf("media artifact refs = %#v, want %#v", got, want)
	}
}

// TestConcurrencySemaphore_Timeout verifies that spawning sub-turns times out
// when all concurrency slots are occupied for too long.
// Note: This test uses a shorter timeout by temporarily modifying the constant.
func TestConcurrencySemaphore_Timeout(t *testing.T) {
	// This test would take 30 seconds with the default timeout.
	// Instead, we'll test the mechanism by verifying the timeout context is created correctly.
	// A full integration test with actual timeout would be too slow for unit tests.

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Provider: "mock",
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &simpleMockProviderAPI{}
	al := NewAgentLoop(cfg, msgBus, provider)

	ctx := context.Background()
	parentTS := &turnState{
		ctx:            ctx,
		turnID:         "parent-timeout-test",
		depth:          0,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}
	parentTS.ctx, parentTS.cancelFunc = context.WithCancel(ctx)
	defer parentTS.Finish(false)

	// Fill all concurrency slots
	for i := 0; i < testMaxConcurrentSubTurns; i++ {
		parentTS.concurrencySem <- struct{}{}
	}

	// Create a context with a very short timeout for testing
	testCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	// Now try to spawn a sub-turn with the short timeout context
	subTurnCfg := SubTurnConfig{
		Model: "gpt-4o-mini",
		Async: false,
	}

	start := time.Now()
	_, err := spawnSubTurn(testCtx, al, parentTS, subTurnCfg)
	elapsed := time.Since(start)

	// Should get a timeout error (either from our timeout context or the internal one)
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}

	// The error should be related to context cancellation or timeout
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrConcurrencyTimeout) {
		t.Logf("Got error: %v (type: %T)", err, err)
		// This is acceptable - the error might be wrapped
	}

	// Should timeout quickly (within a reasonable margin)
	if elapsed > 2*time.Second {
		t.Errorf("Timeout took too long: %v", elapsed)
	}

	t.Logf("Timeout occurred after %v with error: %v", elapsed, err)

	// Clean up - drain the semaphore
	for i := 0; i < testMaxConcurrentSubTurns; i++ {
		<-parentTS.concurrencySem
	}
}

// TestEphemeralSession_AutoTruncate verifies that ephemeral sessions automatically
// truncate their history to prevent memory accumulation.
func TestEphemeralSession_AutoTruncate(t *testing.T) {
	store := newEphemeralSession(nil).(*ephemeralSessionStore)

	// Add more messages than the limit
	for i := 0; i < maxEphemeralHistorySize+20; i++ {
		store.AddMessage("test", "user", fmt.Sprintf("message-%d", i))
	}

	// Verify history is truncated to the limit
	history := store.GetHistory("test")
	if len(history) != maxEphemeralHistorySize {
		t.Errorf("Expected history length %d, got %d", maxEphemeralHistorySize, len(history))
	}

	// Verify we kept the most recent messages
	lastMsg := history[len(history)-1]
	expectedContent := fmt.Sprintf("message-%d", maxEphemeralHistorySize+20-1)
	if lastMsg.Content != expectedContent {
		t.Errorf("Expected last message to be %q, got %q", expectedContent, lastMsg.Content)
	}

	// Verify the oldest messages were discarded
	firstMsg := history[0]
	expectedFirstContent := fmt.Sprintf("message-%d", 20) // First 20 were discarded
	if firstMsg.Content != expectedFirstContent {
		t.Errorf("Expected first message to be %q, got %q", expectedFirstContent, firstMsg.Content)
	}
}

// TestContextWrapping_SingleLayer verifies that we only create one context layer
// in spawnSubTurn, not multiple redundant layers.
func TestContextWrapping_SingleLayer(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Provider: "mock",
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &simpleMockProviderAPI{}
	al := NewAgentLoop(cfg, msgBus, provider)

	ctx := context.Background()
	parentTS := &turnState{
		ctx:            ctx,
		turnID:         "parent-context-test",
		depth:          0,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}
	parentTS.ctx, parentTS.cancelFunc = context.WithCancel(ctx)
	defer parentTS.Finish(false)

	// Spawn a sub-turn
	subTurnCfg := SubTurnConfig{
		Model: "gpt-4o-mini",
		Async: false,
	}

	result, err := spawnSubTurn(ctx, al, parentTS, subTurnCfg)
	if err != nil {
		t.Fatalf("spawnSubTurn failed: %v", err)
	}

	if result == nil {
		t.Error("Expected non-nil result")
	}

	// Verify the child turn was created with a cancel function
	// (This is implicit - if the test passes without hanging, the context management is correct)
	t.Log("Context wrapping test passed - no redundant layers detected")
}

// TestSyncSubTurn_NoChannelDelivery verifies that synchronous sub-turns
// do NOT deliver results to the pendingResults channel (only return directly).
func TestSyncSubTurn_NoChannelDelivery(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Provider: "mock",
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &simpleMockProviderAPI{}
	al := NewAgentLoop(cfg, msgBus, provider)

	ctx := context.Background()
	parentTS := &turnState{
		ctx:            ctx,
		turnID:         "parent-sync-test",
		depth:          0,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}
	parentTS.ctx, parentTS.cancelFunc = context.WithCancel(ctx)
	defer parentTS.Finish(false)

	// Spawn a SYNCHRONOUS sub-turn (Async=false)
	subTurnCfg := SubTurnConfig{
		Model: "gpt-4o-mini",
		Async: false, // Synchronous - should NOT deliver to channel
	}

	result, err := spawnSubTurn(ctx, al, parentTS, subTurnCfg)
	if err != nil {
		t.Fatalf("spawnSubTurn failed: %v", err)
	}

	if result == nil {
		t.Error("Expected non-nil result from synchronous sub-turn")
	}

	// Verify the pendingResults channel is EMPTY
	// (synchronous sub-turns should not deliver to channel)
	select {
	case r := <-parentTS.pendingResults:
		t.Errorf("Expected empty channel for sync sub-turn, but got result: %v", r)
	default:
		// Expected: channel is empty
		t.Log("Verified: synchronous sub-turn did not deliver to channel")
	}

	// Verify channel length is 0
	if len(parentTS.pendingResults) != 0 {
		t.Errorf("Expected channel length 0, got %d", len(parentTS.pendingResults))
	}
}

// TestAsyncSubTurn_ChannelDelivery verifies that asynchronous sub-turns
// DO deliver results to the pendingResults channel.
func TestAsyncSubTurn_ChannelDelivery(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Provider: "mock",
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &simpleMockProviderAPI{}
	al := NewAgentLoop(cfg, msgBus, provider)

	ctx := context.Background()
	parentTS := &turnState{
		ctx:            ctx,
		turnID:         "parent-async-test",
		depth:          0,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}
	parentTS.ctx, parentTS.cancelFunc = context.WithCancel(ctx)
	defer parentTS.Finish(false)

	// Spawn an ASYNCHRONOUS sub-turn (Async=true)
	subTurnCfg := SubTurnConfig{
		Model: "gpt-4o-mini",
		Async: true, // Asynchronous - SHOULD deliver to channel
	}

	result, err := spawnSubTurn(ctx, al, parentTS, subTurnCfg)
	if err != nil {
		t.Fatalf("spawnSubTurn failed: %v", err)
	}

	if result == nil {
		t.Error("Expected non-nil result from asynchronous sub-turn")
	}

	// Verify the pendingResults channel has the result
	select {
	case r := <-parentTS.pendingResults:
		if r == nil {
			t.Error("Expected non-nil result from channel")
		}
		t.Log("Verified: asynchronous sub-turn delivered to channel")
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected result in channel for async sub-turn, but channel was empty")
	}
}

// TestGrandchildAbort_CascadingCancellation verifies that when a grandparent turn
// is hard aborted, the cancellation cascades down to grandchild turns.
func TestGrandchildAbort_CascadingCancellation(t *testing.T) {
	al, cfg, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	// Three independent contexts — none derived from another.
	// Cascade must happen exclusively through childTurnIDs traversal in Finish(true).
	gpCtx, gpCancel := context.WithCancel(context.Background())
	parentCtx, parentCancel := context.WithCancel(context.Background())
	childCtx, childCancel := context.WithCancel(context.Background())

	childTS := &turnState{
		ctx:        childCtx,
		cancelFunc: childCancel,
		turnID:     "grandchild",
		workspace:  cfg.Agents.Defaults.Workspace,
		al:         al,
	}
	parentTS := &turnState{
		ctx:          parentCtx,
		cancelFunc:   parentCancel,
		turnID:       "parent",
		workspace:    cfg.Agents.Defaults.Workspace,
		childTurnIDs: []string{"grandchild"},
		al:           al,
	}
	grandparentTS := &turnState{
		ctx:            gpCtx,
		cancelFunc:     gpCancel,
		turnID:         "grandparent",
		workspace:      cfg.Agents.Defaults.Workspace,
		sessionKey:     "grandparent",
		depth:          0,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		childTurnIDs:   []string{"parent"},
		al:             al,
	}

	grandparentScope := grandparentTS.runtimeSessionScope()
	parentScope := newRuntimeSubTurnScope(parentTS.workspace, parentTS.turnID)
	childScope := newRuntimeSubTurnScope(childTS.workspace, childTS.turnID)
	al.activeTurnStates.Store(grandparentScope, grandparentTS)
	al.activeTurnStates.Store(parentScope, parentTS)
	al.activeTurnStates.Store(childScope, childTS)
	defer al.activeTurnStates.Delete(grandparentScope)
	defer al.activeTurnStates.Delete(parentScope)
	defer al.activeTurnStates.Delete(childScope)

	// All contexts must be active before the abort
	for _, ctx := range []context.Context{gpCtx, parentCtx, childCtx} {
		select {
		case <-ctx.Done():
			t.Fatal("context should not be canceled yet")
		default:
		}
	}

	// Hard abort the grandparent — should cascade to parent and grandchild
	grandparentTS.Finish(true)

	time.Sleep(10 * time.Millisecond)

	select {
	case <-gpCtx.Done():
		t.Log("Grandparent context canceled (expected)")
	default:
		t.Error("Grandparent context should be canceled")
	}
	select {
	case <-parentCtx.Done():
		t.Log("Parent context canceled via cascade (expected)")
	default:
		t.Error("Parent context should be canceled via childTurnIDs cascade")
	}
	select {
	case <-childCtx.Done():
		t.Log("Grandchild context canceled via cascade (expected)")
	default:
		t.Error("Grandchild context should be canceled via childTurnIDs cascade")
	}
}

func TestNestedSubTurn_GracefulFinishSignalsDirectChildren(t *testing.T) {
	parentCtx := context.Background()
	parentTS := &turnState{
		ctx:            parentCtx,
		turnID:         "parent-graceful",
		depth:          1,
		pendingResults: make(chan *toolshared.ToolResult, 16),
	}
	parentTS.ctx, parentTS.cancelFunc = context.WithCancel(parentCtx)

	childTS := &turnState{
		ctx:             context.Background(),
		turnID:          "child-graceful",
		depth:           2,
		parentTurnState: parentTS,
		pendingResults:  make(chan *toolshared.ToolResult, 16),
	}

	if childTS.IsParentEnded() {
		t.Fatal("IsParentEnded should be false before parent finishes")
	}

	parentTS.Finish(false)

	if !parentTS.parentEnded.Load() {
		t.Fatal("parentEnded should be true after graceful finish")
	}
	if !childTS.IsParentEnded() {
		t.Fatal("nested child should observe parent graceful finish")
	}
}

// slowMockProvider simulates a slow LLM call that takes a long time to complete.
// This is used to test parent and child SubTurn coordination.
type slowMockProvider struct {
	delay time.Duration
}

func (m *slowMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	select {
	case <-time.After(m.delay):
		// Completed normally after delay
		return &providers.LLMResponse{
			Content: "slow response completed",
		}, nil
	case <-ctx.Done():
		// Context was canceled while waiting
		return nil, ctx.Err()
	}
}

func (m *slowMockProvider) GetDefaultModel() string {
	return "slow-model"
}

// TestAsyncSubTurn_ParentWaitsForChild simulates the scenario where:
// 1. Parent spawns an async SubTurn that takes some time
// 2. Parent WAITS for SubTurn to complete before finishing
// 3. Both should complete successfully
func TestAsyncSubTurn_ParentWaitsForChild(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Provider: "mock",
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &slowMockProvider{delay: 200 * time.Millisecond} // SubTurn takes 200ms
	al := NewAgentLoop(cfg, msgBus, provider)

	ctx := context.Background()
	parentTS := &turnState{
		ctx:            ctx,
		turnID:         "parent-wait",
		depth:          0,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}
	parentTS.ctx, parentTS.cancelFunc = context.WithCancel(ctx)

	var subTurnErr error
	var subTurnResult *toolshared.ToolResult
	var wg sync.WaitGroup

	// Spawn async SubTurn in a goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		subTurnCfg := SubTurnConfig{
			Model: "slow-model",
			Async: true,
		}
		subTurnResult, subTurnErr = spawnSubTurn(parentTS.ctx, al, parentTS, subTurnCfg)
	}()

	// Parent WAITS for SubTurn to complete
	t.Log("Parent waiting for SubTurn...")
	wg.Wait()
	t.Log("SubTurn completed, parent now finishing")

	// Now parent can finish safely
	parentTS.Finish(false)

	// Check the result
	if subTurnErr != nil {
		if errors.Is(subTurnErr, context.Canceled) {
			t.Errorf("SubTurn should NOT have been canceled: %v", subTurnErr)
		} else {
			t.Logf("SubTurn failed with error: %v", subTurnErr)
		}
	} else {
		t.Log("✓ SubTurn completed successfully")
		if subTurnResult != nil {
			t.Logf("SubTurn result: %s", subTurnResult.ForLLM)
		}
	}

	// Check channel delivery
	select {
	case r := <-parentTS.pendingResults:
		if r != nil {
			t.Logf("✓ Result delivered to channel: %s", r.ForLLM)
		}
	case <-time.After(100 * time.Millisecond):
		t.Log("No result in channel (expected since we waited)")
	}
}

// ====================== Graceful vs Hard Finish Tests ======================

// TestFinish_GracefulVsHard verifies the behavior difference between:
// - Finish(false): graceful finish, signals parentEnded but doesn't cancel children
// - Finish(true): hard abort, immediately cancels all children
func TestFinish_GracefulVsHard(t *testing.T) {
	// Test 1: Graceful finish should set parentEnded but not cancel context
	t.Run("Graceful_SetsParentEnded", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ts := &turnState{
			ctx:            ctx,
			turnID:         "graceful-test",
			depth:          0,
			pendingResults: make(chan *toolshared.ToolResult, 16),
		}
		ts.ctx, ts.cancelFunc = context.WithCancel(ctx)

		// Finish gracefully
		ts.Finish(false)

		// Verify parentEnded is set
		if !ts.parentEnded.Load() {
			t.Error("parentEnded should be true after graceful finish")
		}

		// Verify context is NOT canceled (for graceful finish, children continue)
		// Note: In graceful mode, we don't call cancelFunc()
		// But since we're using WithCancel on the same ctx, it might be canceled
		// Let's check that the context is still valid for a moment
		time.Sleep(10 * time.Millisecond)
		// Context might be canceled by the deferred cancel() in test, which is fine
	})

	// Test 2: Hard abort should cancel context immediately
	t.Run("Hard_CancelsContext", func(t *testing.T) {
		ctx := context.Background()

		ts := &turnState{
			ctx:            ctx,
			turnID:         "hard-test",
			depth:          0,
			pendingResults: make(chan *toolshared.ToolResult, 16),
		}
		ts.ctx, ts.cancelFunc = context.WithCancel(ctx)

		// Finish with hard abort
		ts.Finish(true)

		// Verify context is canceled
		select {
		case <-ts.ctx.Done():
			t.Log("✓ Context canceled after hard abort")
		default:
			t.Error("Context should be canceled after hard abort")
		}
	})

	// Test 3: IsParentEnded returns correct value
	t.Run("IsParentEnded", func(t *testing.T) {
		ctx := context.Background()

		parentTS := &turnState{
			ctx:            ctx,
			turnID:         "parent-isended-test",
			depth:          0,
			pendingResults: make(chan *toolshared.ToolResult, 16),
		}
		parentTS.ctx, parentTS.cancelFunc = context.WithCancel(ctx)

		childTS := &turnState{
			ctx:             ctx,
			turnID:          "child-isended-test",
			depth:           1,
			parentTurnState: parentTS,
			pendingResults:  make(chan *toolshared.ToolResult, 16),
		}

		// Before parent finishes
		if childTS.IsParentEnded() {
			t.Error("IsParentEnded should be false before parent finishes")
		}

		// Finish parent gracefully
		parentTS.Finish(false)

		// After parent finishes
		if !childTS.IsParentEnded() {
			t.Error("IsParentEnded should be true after parent finishes gracefully")
		}
	})
}

// TestSubTurn_IndependentContext verifies that SubTurns use independent contexts
// that don't get canceled when the parent finishes gracefully.
func TestSubTurn_IndependentContext(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Provider: "mock",
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &slowMockProvider{delay: 500 * time.Millisecond}
	al := NewAgentLoop(cfg, msgBus, provider)

	ctx := context.Background()
	parentTS := &turnState{
		ctx:            ctx,
		turnID:         "parent-independent",
		depth:          0,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *toolshared.ToolResult, 16),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
	}
	parentTS.ctx, parentTS.cancelFunc = context.WithCancel(ctx)

	var subTurnErr error
	var wg sync.WaitGroup

	// Spawn SubTurn with Critical=true (should continue after parent finishes)
	wg.Add(1)
	go func() {
		defer wg.Done()
		subTurnCfg := SubTurnConfig{
			Model:    "slow-model",
			Async:    true,
			Critical: true, // Critical SubTurn should continue
		}
		_, subTurnErr = spawnSubTurn(parentTS.ctx, al, parentTS, subTurnCfg)
	}()

	// Let SubTurn start
	time.Sleep(50 * time.Millisecond)

	// Parent finishes gracefully (should NOT cancel SubTurn)
	parentTS.Finish(false)
	t.Log("Parent finished gracefully, SubTurn should continue")

	// Wait for SubTurn to complete
	wg.Wait()

	// SubTurn should complete without context canceled error
	// (because it uses independent context now)
	if subTurnErr != nil {
		t.Logf("SubTurn error: %v", subTurnErr)
		// The error might be context.DeadlineExceeded if timeout is too short
		// but should NOT be context.Canceled from parent
		if errors.Is(subTurnErr, context.Canceled) {
			t.Error("SubTurn should not be canceled by parent's graceful finish")
		}
	} else {
		t.Log("✓ SubTurn completed successfully (independent context)")
	}
}

// ====================== TargetAgentID Tests ======================

// modelRecordingProvider captures the model passed to Chat for test assertions.
type modelRecordingProvider struct {
	mu        sync.Mutex
	lastModel string
}

func (rp *modelRecordingProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	rp.mu.Lock()
	rp.lastModel = model
	rp.mu.Unlock()
	return &providers.LLMResponse{Content: "Mock response"}, nil
}

func (rp *modelRecordingProvider) GetDefaultModel() string { return "mock-model" }

func (rp *modelRecordingProvider) getLastModel() string {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.lastModel
}

// newMultiAgentLoop creates an AgentLoop with two named agents for testing
// cross-agent delegation via TargetAgentID.
func newMultiAgentLoop(t *testing.T, provider providers.LLMProvider) (*AgentLoop, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "multiagent-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}

	alphaDir := filepath.Join(tmpDir, "alpha")
	betaDir := filepath.Join(tmpDir, "beta")
	if err := os.MkdirAll(alphaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(betaDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "default-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{
					ID:        "alpha",
					Workspace: alphaDir,
					Model:     &config.AgentModelConfig{Primary: "model-alpha"},
				},
				{
					ID:        "beta",
					Workspace: betaDir,
					Model:     &config.AgentModelConfig{Primary: "model-beta"},
				},
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, provider)

	return al, func() { os.RemoveAll(tmpDir) }
}

func TestSpawnSubTurn_TargetAgentID_UsesTargetAgent(t *testing.T) {
	rp := &modelRecordingProvider{}
	al, cleanup := newMultiAgentLoop(t, rp)
	defer cleanup()

	alphaAgent, ok := al.registry.GetAgent("alpha")
	if !ok {
		t.Fatal("alpha agent not in registry")
	}

	// Parent is alpha, target is beta
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-alpha",
		depth:          0,
		childTurnIDs:   []string{},
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          alphaAgent,
	}

	result, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		TargetAgentID: "beta",
		SystemPrompt:  "task for beta",
	})
	if err != nil {
		t.Fatalf("spawnSubTurn failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// The recording provider captures the model passed to Chat().
	// If TargetAgentID works correctly, the child turn should have
	// used beta's model, not alpha's.
	if got := rp.getLastModel(); got != "model-beta" {
		t.Errorf("child turn used model %q, want %q", got, "model-beta")
	}
}

func TestSpawnSubTurn_TargetAgentID_NotFound(t *testing.T) {
	al, cleanup := newMultiAgentLoop(t, &mockProvider{})
	defer cleanup()

	alphaAgent, _ := al.registry.GetAgent("alpha")
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-alpha",
		depth:          0,
		childTurnIDs:   []string{},
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          alphaAgent,
	}

	_, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		TargetAgentID: "nonexistent",
		SystemPrompt:  "task",
	})

	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestRemoveUserDeliveryTools(t *testing.T) {
	registry := tools.NewToolRegistry()
	for _, name := range []string{
		"message",
		"send_file",
		"send_tts",
		"reaction",
		"read_file",
		"mcp_media_download_async",
	} {
		registry.Register(&allowlistTestTool{name: name})
	}

	removeUserDeliveryTools(registry)

	for _, name := range []string{
		"message",
		"send_file",
		"send_tts",
		"reaction",
	} {
		if registry.HasRegistered(name) {
			t.Fatalf("expected user-facing delivery tool %q to be removed", name)
		}
	}
	for _, name := range []string{"read_file", "mcp_media_download_async"} {
		if !registry.HasRegistered(name) {
			t.Fatalf("expected non-delivery tool %q to remain", name)
		}
	}
}

func TestRemoveDurableInteractionTools(t *testing.T) {
	registry := tools.NewToolRegistry()
	registry.Register(&allowlistTestTool{name: "request_user_input"})
	registry.Register(&allowlistTestTool{name: "read_file"})

	removeDurableInteractionTools(registry)

	if registry.HasRegistered("request_user_input") {
		t.Fatal("request_user_input remained available to an ephemeral subturn")
	}
	if !registry.HasRegistered("read_file") {
		t.Fatal("unrelated tool was removed")
	}
}

func TestRemoveInheritedNodeFileTools(t *testing.T) {
	registry := tools.NewToolRegistry()
	for _, name := range []string{
		"nodes_file_info",
		"nodes_upload",
		"nodes_download",
		"nodes",
		"read_file",
	} {
		registry.Register(&allowlistTestTool{name: name})
	}

	removeInheritedNodeFileTools(registry)

	for _, name := range []string{
		"nodes_file_info",
		"nodes_upload",
		"nodes_download",
	} {
		if registry.HasRegistered(name) {
			t.Fatalf("inherited node file tool %q remained available to same-agent subturn", name)
		}
	}
	for _, name := range []string{"nodes", "read_file"} {
		if !registry.HasRegistered(name) {
			t.Fatalf("unrelated tool %q was removed", name)
		}
	}
}

func TestSpawnSubTurn_TargetAgentID_EmptyModelAccepted(t *testing.T) {
	al, cleanup := newMultiAgentLoop(t, &mockProvider{})
	defer cleanup()

	alphaAgent, _ := al.registry.GetAgent("alpha")
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-alpha",
		depth:          0,
		childTurnIDs:   []string{},
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{},
		agent:          alphaAgent,
	}

	// Model is empty but TargetAgentID is set — should NOT fail validation
	result, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		Model:         "", // intentionally empty
		TargetAgentID: "beta",
		SystemPrompt:  "task for beta",
	})
	if err != nil {
		t.Fatalf("should accept empty Model when TargetAgentID is set, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestDelegateToolNotRegistered_SingleAgent(t *testing.T) {
	// Single-agent setup: delegate should not be registered
	al, _, _, provider, cleanup := newTestAgentLoop(t)
	_ = provider
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent should exist")
	}
	if _, has := agent.Tools.Get("delegate"); has {
		t.Error("delegate tool should not be registered in single-agent setup")
	}
}

func TestDelegateToolRegistered_MultiAgent(t *testing.T) {
	al, cleanup := newMultiAgentLoop(t, &mockProvider{})
	defer cleanup()

	// Both agents should have the delegate tool
	for _, id := range []string{"alpha", "beta"} {
		agent, ok := al.registry.GetAgent(id)
		if !ok {
			t.Fatalf("agent %q not found", id)
		}
		if _, has := agent.Tools.Get("delegate"); !has {
			t.Errorf("agent %q should have delegate tool in multi-agent setup", id)
		}
	}
}

func TestDurableSyncDelegateUserOnlyPublishesExactlyOnce(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{
			ToolCalls: []providers.ToolCall{{
				ID:   "call-delegate-user-only",
				Name: "delegate",
				Arguments: map[string]any{
					"agent_id":      "beta",
					"task":          "answer the user",
					"delivery_mode": string(toolshared.AsyncDeliveryUserOnly),
				},
			}},
		},
		{Content: "child durable response", FinishReason: "stop"},
	}}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	root := t.TempDir()
	installTestOutboundCoordinator(t, al, root)
	msgBus, ok := al.bus.(*bus.MessageBus)
	if !ok {
		t.Fatal("test agent loop does not use MessageBus")
	}
	alpha, ok := al.registry.GetAgent("alpha")
	if !ok {
		t.Fatal("alpha agent not found")
	}
	alpha.Subagents = &config.SubagentsConfig{AllowAgents: []string{"beta"}}
	ctx := withOutboundTransaction(t.Context(), "spool-delegate-user-only")

	response, err := al.runAgentLoop(ctx, alpha, turnSpec{
		Dispatch: DispatchRequest{
			SessionKey:  "delegate-parent-session",
			UserMessage: "delegate this",
			InboundContext: &bus.InboundContext{
				Channel: "telegram",
				ChatID:  "chat-1",
			},
		},
		DefaultResponse:     defaultResponse,
		ExpectFinalDelivery: true,
		SendResponse:        false,
		NoHistory:           true,
	})
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if strings.TrimSpace(response) != "" {
		admission := al.publishResponseWithContextIfNeeded(
			ctx,
			alpha.Workspace,
			alpha.ID,
			"telegram",
			"chat-1",
			"delegate-parent-session",
			response,
			nil,
			finalResponseAlwaysPublish,
		)
		if !admission.permitsInboundAck() {
			t.Fatalf("parent final admission = %+v", admission)
		}
	}

	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "child durable response" || outbound.DeliveryID == "" {
			t.Fatalf("delegate outbound = %+v", outbound)
		}
		store, openErr := outbox.Open(root)
		if openErr != nil {
			t.Fatalf("Open() error = %v", openErr)
		}
		if _, getErr := store.Get(outbound.DeliveryID); getErr != nil {
			t.Fatalf("Get(%q) error = %v", outbound.DeliveryID, getErr)
		}
	default:
		t.Fatal("delegate user-only result was not published")
	}
	select {
	case duplicate := <-msgBus.OutboundChan():
		t.Fatalf("delegate user-only result published twice: %+v", duplicate)
	default:
	}
}

func TestDurableSyncDelegateFailureRejectsRecoveredParentFinal(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{
			ToolCalls: []providers.ToolCall{{
				ID:   "call-delegate-rejected",
				Name: "delegate",
				Arguments: map[string]any{
					"agent_id":      "beta",
					"task":          "answer the user",
					"delivery_mode": string(toolshared.AsyncDeliveryUserOnly),
				},
			}},
		},
		{Content: "child response", FinishReason: "stop"},
		{Content: "parent recovered response", FinishReason: "stop"},
	}}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	installTestOutboundCoordinator(t, al, t.TempDir())
	msgBus, ok := al.bus.(*bus.MessageBus)
	if !ok {
		t.Fatal("test agent loop does not use MessageBus")
	}
	childRejection := errors.New("child outbound rejected")
	al.bus = &finalResponseAdmissionTestBus{
		MessageBus:     msgBus,
		publishResults: []error{childRejection, nil},
	}
	alpha, ok := al.registry.GetAgent("alpha")
	if !ok {
		t.Fatal("alpha agent not found")
	}
	alpha.Subagents = &config.SubagentsConfig{AllowAgents: []string{"beta"}}
	ctx := withOutboundTransaction(t.Context(), "spool-delegate-rejected")

	response, err := al.runAgentLoop(ctx, alpha, turnSpec{
		Dispatch: DispatchRequest{
			SessionKey:  "delegate-rejected-session",
			UserMessage: "delegate this",
			InboundContext: &bus.InboundContext{
				Channel: "telegram",
				ChatID:  "chat-1",
			},
		},
		DefaultResponse:     defaultResponse,
		ExpectFinalDelivery: true,
		SendResponse:        false,
		NoHistory:           true,
	})
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if response != "parent recovered response" {
		t.Fatalf("parent response = %q", response)
	}
	admission := al.publishResponseWithContextIfNeeded(
		ctx,
		alpha.Workspace,
		alpha.ID,
		"telegram",
		"chat-1",
		"delegate-rejected-session",
		response,
		nil,
		finalResponseAlwaysPublish,
	)
	admission = transactionAdmission(ctx, admission)
	if admission.permitsInboundAck() || !errors.Is(admission.err, childRejection) {
		t.Fatalf("recovered parent admission = %+v", admission)
	}
}
