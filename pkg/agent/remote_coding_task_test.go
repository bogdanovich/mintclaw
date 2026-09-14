package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type remoteCodingInvocationCall struct {
	authority tools.CodingInvocationAuthority
	target    string
	command   string
	input     any
	ephemeral any
}

type fakeRemoteCodingInvoker struct {
	mu       sync.Mutex
	calls    []remoteCodingInvocationCall
	threadID string
	workerID string
}

func newFakeRemoteCodingInvoker() *fakeRemoteCodingInvoker {
	return &fakeRemoteCodingInvoker{threadID: uuid.NewString(), workerID: uuid.NewString()}
}

func (invoker *fakeRemoteCodingInvoker) Invoke(
	_ context.Context,
	authority tools.CodingInvocationAuthority,
	target string,
	command string,
	input any,
	ephemeral any,
) (json.RawMessage, error) {
	invoker.mu.Lock()
	invoker.calls = append(invoker.calls, remoteCodingInvocationCall{
		authority: authority, target: target, command: command, input: input, ephemeral: ephemeral,
	})
	invoker.mu.Unlock()

	result := nodes.CodingTaskResult{
		ProjectAlias: "mintclaw", ProjectRevision: "project-v1",
		Mode: codingtask.TaskModeInvestigate, ThreadID: invoker.threadID,
		ThreadOpenMode: codingtask.ThreadOpenNew, WorkerGenerationID: invoker.workerID,
		State: codingtask.StateRunning, Revision: 1, Activity: codingtask.ActivityRunning,
		AcceptedAt: 1, UpdatedAt: 1,
	}
	switch command {
	case nodes.CodingCommandTaskStart:
		request, ok := input.(nodes.CodingTaskStartInput)
		if !ok {
			return nil, tools.ErrCodingInvocationUncertain
		}
		result.TaskID = request.TaskID
		result.TaskGenerationID = request.TaskGenerationID
		result.ProjectAlias = request.ProjectAlias
		result.ProjectRevision = request.ProjectRevision
		result.Mode = request.Mode
	case nodes.CodingCommandTaskStatus:
		request, ok := input.(nodes.CodingTaskIdentityInput)
		if !ok {
			return nil, tools.ErrCodingInvocationUncertain
		}
		result.TaskID = request.TaskID
		result.TaskGenerationID = request.TaskGenerationID
	case nodes.CodingCommandTaskSteer:
		request, ok := input.(nodes.CodingTaskSteerInput)
		if !ok {
			return nil, tools.ErrCodingInvocationUncertain
		}
		result.TaskID = request.TaskID
		result.TaskGenerationID = request.TaskGenerationID
		result.Revision = 2
	case nodes.CodingCommandTaskCancel:
		request, ok := input.(nodes.CodingTaskCancelInput)
		if !ok {
			return nil, tools.ErrCodingInvocationUncertain
		}
		result.TaskID = request.TaskID
		result.TaskGenerationID = request.TaskGenerationID
		result.State = codingtask.StateCanceled
		result.Activity = codingtask.ActivityIdle
		result.Revision = 3
		result.TerminalReport = &codingtask.TerminalReport{
			Summary:      "Coding task was canceled by the requester.",
			CleanupState: "not_applicable",
		}
	default:
		return nil, tools.ErrCodingNodeUnavailable
	}
	return json.Marshal(result)
}

func (invoker *fakeRemoteCodingInvoker) snapshot() []remoteCodingInvocationCall {
	invoker.mu.Lock()
	defer invoker.mu.Unlock()
	return append([]remoteCodingInvocationCall(nil), invoker.calls...)
}

func TestRemoteCodingTaskStartIsDurableAndOwnerScoped(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &mockProvider{})
	configureRemoteCodingTestGrant(fixture.Config)
	invoker := newFakeRemoteCodingInvoker()
	if err := fixture.Loop.ConfigureRemoteCodingTaskRuntime(
		func(*config.Config) (RemoteCodingInvoker, error) { return invoker, nil },
	); err != nil {
		t.Fatal(err)
	}
	runtimeCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	fixture.Loop.remoteCoding.start(runtimeCtx)
	tool, err := fixture.Loop.NewRemoteCodingTaskTool(fixture.Config, fixture.Agent.ID)
	if err != nil || tool == nil {
		t.Fatalf("NewRemoteCodingTaskTool() = %#v, %v", tool, err)
	}

	owner := remoteCodingTestContext(
		fixture.Agent.Workspace,
		"history-one",
		"telegram-route",
		"owner-42",
		"start-call",
	)
	started := tool.Execute(owner, map[string]any{
		"action": "start", "project": "mintclaw", "mode": "investigate",
		"objective":     "Inspect the failing test without changing files.",
		"done_criteria": "Return the root cause and supporting evidence.",
	})
	if started == nil || started.IsError {
		t.Fatalf("start result = %#v", started)
	}
	var projection map[string]any
	if err := json.Unmarshal([]byte(started.ContentForLLM()), &projection); err != nil {
		t.Fatal(err)
	}
	taskID, _ := projection["task_id"].(string)
	if taskID == "" || projection["status"] != string(taskregistry.StatusQueued) {
		t.Fatalf("immediate start result = %#v", projection)
	}
	tasks := fixture.Loop.taskRegistryForWorkspace(fixture.Agent.Workspace)
	waitRemoteCodingTest(t, func() bool {
		record, found := tasks.Get(taskID)
		return found && record.Coding != nil && record.Coding.ThreadID == invoker.threadID
	})
	record, found := tasks.Get(taskID)
	if !found || record.Status != taskregistry.StatusRunning || record.Coding == nil ||
		record.Coding.RequestDigest == "" || record.Coding.RouteSessionKey != "telegram-route" {
		t.Fatalf("durable coding task = %#v, %v", record, found)
	}
	repeated := tool.Execute(owner, map[string]any{
		"action": "start", "project": "mintclaw", "mode": "investigate",
		"objective":     "Inspect the failing test without changing files.",
		"done_criteria": "Return the root cause and supporting evidence.",
	})
	if repeated == nil || repeated.IsError || !strings.Contains(repeated.ContentForLLM(), taskID) {
		t.Fatalf("idempotent repeated start = %#v", repeated)
	}
	conflict := tool.Execute(owner, map[string]any{
		"action": "start", "project": "mintclaw", "mode": "investigate",
		"objective": "A changed objective under the same provider call.",
	})
	if conflict == nil || !conflict.IsError || !strings.Contains(conflict.ContentForLLM(), "conflicts") {
		t.Fatalf("changed repeated start = %#v", conflict)
	}

	newHistorySameRoute := remoteCodingTestContext(
		fixture.Agent.Workspace,
		"history-two",
		"telegram-route",
		"owner-42",
		"status-call",
	)
	status := tool.Execute(newHistorySameRoute, map[string]any{"action": "status", "task_id": taskID})
	if status == nil || status.IsError {
		t.Fatalf("same-route status = %#v", status)
	}
	steered := tool.Execute(newHistorySameRoute, map[string]any{
		"action": "steer", "task_id": taskID, "text": "Also inspect the recent configuration change.",
	})
	if steered == nil || steered.IsError {
		t.Fatalf("same-route steer = %#v", steered)
	}

	wrongSender := remoteCodingTestContext(
		fixture.Agent.Workspace,
		"history-two",
		"telegram-route",
		"other-user",
		"wrong-owner-call",
	)
	denied := tool.Execute(wrongSender, map[string]any{"action": "status", "task_id": taskID})
	if denied == nil || !denied.IsError || !strings.Contains(denied.ContentForLLM(), "different route or sender") {
		t.Fatalf("wrong-owner status = %#v", denied)
	}

	calls := invoker.snapshot()
	var startCalls, steerCalls int
	for _, call := range calls {
		if call.authority.Workspace != fixture.Agent.Workspace || call.target != "companion" {
			t.Fatalf("invocation authority = %#v", call)
		}
		switch call.command {
		case nodes.CodingCommandTaskStart:
			startCalls++
			ephemeral, ok := call.ephemeral.(nodes.CodingTaskStartEphemeralInput)
			if !ok || !strings.Contains(ephemeral.Objective, "failing test") {
				t.Fatalf("start ephemeral content = %#v", call.ephemeral)
			}
		case nodes.CodingCommandTaskSteer:
			steerCalls++
			ephemeral, ok := call.ephemeral.(nodes.CodingTaskSteerEphemeralInput)
			if !ok || !strings.Contains(ephemeral.Text, "configuration change") {
				t.Fatalf("steer ephemeral content = %#v", call.ephemeral)
			}
		}
	}
	if startCalls != 1 || steerCalls != 1 {
		t.Fatalf("coding calls = start %d, steer %d; all=%#v", startCalls, steerCalls, calls)
	}
}

func TestRemoteCodingPromptUsesDurableFieldBounds(t *testing.T) {
	if !validRemoteCodingPrompt(
		strings.Repeat("a", taskregistry.MaxCodingObjectiveBytes),
		true,
		taskregistry.MaxCodingObjectiveBytes,
	) {
		t.Fatal("objective at the durable task limit was rejected")
	}
	if validRemoteCodingPrompt(
		strings.Repeat("a", taskregistry.MaxCodingObjectiveBytes+1),
		true,
		taskregistry.MaxCodingObjectiveBytes,
	) {
		t.Fatal("objective above the durable task limit was accepted")
	}
	if validRemoteCodingPrompt("", true, nodes.MaxCodingTaskTextBytes) {
		t.Fatal("empty required steering text was accepted")
	}
}

func TestRemoteCodingToolRedactsDurablePromptArguments(t *testing.T) {
	tool := &remoteCodingTool{}
	registry := tools.NewToolRegistry()
	registry.Register(tool)
	arguments := map[string]any{
		"action": "start", "project": "mintclaw", "mode": "investigate",
		"objective": "private objective", "done_criteria": "private completion criteria",
	}
	projected, protected, err := registry.DurableArguments("coding_task", arguments)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private") || projected["project"] != "mintclaw" ||
		projected["mode"] != "investigate" || !protected ||
		registry.ProtectedDurableResult("coding_task", arguments) {
		t.Fatalf("durable coding arguments = %s", encoded)
	}
	if tool.ProtectedDurableArguments(map[string]any{"action": "status", "task_id": "coding-one"}) {
		t.Fatal("status-only coding arguments were marked protected")
	}
}

func TestRemoteCodingQuestionUsesDurableInteractionAndTypedAnswer(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &mockProvider{})
	configureRemoteCodingTestGrant(fixture.Config)
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, fixture.Loop, manager)
	invoker := newFakeRemoteCodingInvoker()
	if err := fixture.Loop.ConfigureRemoteCodingTaskRuntime(
		func(*config.Config) (RemoteCodingInvoker, error) { return invoker, nil },
	); err != nil {
		t.Fatal(err)
	}
	record := createRemoteCodingTestRecord(t, fixture, taskregistry.StatusRunning)
	tasks := fixture.Loop.taskRegistryForWorkspace(fixture.Agent.Workspace)
	waiting := nodes.CodingTaskResult{
		TaskID: record.TaskID, TaskGenerationID: record.GenerationID,
		ProjectAlias: record.Coding.Project, ProjectRevision: record.Coding.Revision,
		Mode: record.Coding.Mode, ThreadID: invoker.threadID,
		ThreadOpenMode: codingtask.ThreadOpenNew, WorkerGenerationID: invoker.workerID,
		State: codingtask.StateWaitingInput, Revision: 2, Activity: codingtask.ActivityWaitingInput,
		AcceptedAt: 1, UpdatedAt: 2,
		Question: &nodes.CodingQuestionResult{
			QuestionID: "scope-question", Revision: 3, Prompt: "Which scope should I inspect?",
			Options: []nodes.CodingQuestionOption{
				{ID: "focused", Label: "Focused", Description: "Only the failing package."},
				{ID: "broad", Label: "Broad", Description: "Inspect related packages too."},
			},
		},
	}
	if err := fixture.Loop.remoteCoding.projectResult(
		fixture.Agent.Workspace,
		tasks,
		record,
		waiting,
	); err != nil {
		t.Fatal(err)
	}
	var prompt bus.OutboundMessage
	select {
	case prompt = <-manager.sent:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for coding question")
	}
	if prompt.Metadata.InteractionKind != bus.OutboundInteractionQuestion ||
		prompt.Context.SenderID != "owner-42" || prompt.Context.TopicID != "topic-1" {
		t.Fatalf("coding question outbound = %#v", prompt)
	}
	updated, _ := tasks.Get(record.TaskID)
	if updated.Coding.Question == nil || updated.Coding.Question.InteractionID == "" {
		t.Fatalf("question projection = %#v", updated.Coding)
	}
	registry := fixture.Loop.interactionRegistryForWorkspace(fixture.Agent.Workspace)
	interaction, found := registry.Get(updated.Coding.Question.InteractionID)
	if !found || interaction.Status != interactions.StatusWaiting || interaction.Origin.TaskID != record.TaskID {
		t.Fatalf("durable interaction = %#v, %v", interaction, found)
	}
	interaction, err := registry.ClaimAnswer(
		interaction.ID,
		interaction.Revision,
		interactions.Answer{Text: "Focused", ReceivedAt: time.Now().UnixMilli()},
		interactions.OutcomeAnswered,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.Loop.remoteCoding.resumeQuestionInteraction(
		t.Context(),
		fixture.Agent.Workspace,
		registry,
		interaction,
	); err != nil {
		t.Fatal(err)
	}
	resolved, _ := registry.Get(interaction.ID)
	if resolved.Status != interactions.StatusResolved {
		t.Fatalf("resolved coding question = %#v", resolved)
	}
	calls := invoker.snapshot()
	var answer *nodes.CodingQuestionAnswer
	for _, call := range calls {
		if call.command != nodes.CodingCommandTaskSteer {
			continue
		}
		request, ok := call.input.(nodes.CodingTaskSteerInput)
		if ok {
			answer = request.QuestionAnswer
		}
	}
	if answer == nil || answer.QuestionID != "scope-question" || answer.QuestionRevision != 3 {
		t.Fatalf("typed coding answer = %#v; calls=%#v", answer, calls)
	}
}

func TestRemoteCodingQuestionIsRetiredWhenNodeStateAdvances(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &mockProvider{})
	configureRemoteCodingTestGrant(fixture.Config)
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, fixture.Loop, manager)
	if err := fixture.Loop.ConfigureRemoteCodingTaskRuntime(
		func(*config.Config) (RemoteCodingInvoker, error) { return newFakeRemoteCodingInvoker(), nil },
	); err != nil {
		t.Fatal(err)
	}
	record := createRemoteCodingTestRecord(t, fixture, taskregistry.StatusRunning)
	tasks := fixture.Loop.taskRegistryForWorkspace(fixture.Agent.Workspace)
	waiting := nodes.CodingTaskResult{
		TaskID: record.TaskID, TaskGenerationID: record.GenerationID,
		ProjectAlias: record.Coding.Project, ProjectRevision: record.Coding.Revision,
		Mode: record.Coding.Mode, ThreadID: record.Coding.ThreadID,
		ThreadOpenMode: codingtask.ThreadOpenNew, WorkerGenerationID: record.Coding.WorkerGenerationID,
		State: codingtask.StateWaitingInput, Revision: 2, Activity: codingtask.ActivityWaitingInput,
		AcceptedAt: 1, UpdatedAt: 2,
		Question: &nodes.CodingQuestionResult{
			QuestionID: "stale-question", Revision: 1, Prompt: "Continue?",
		},
	}
	if err := fixture.Loop.remoteCoding.projectResult(
		fixture.Agent.Workspace,
		tasks,
		record,
		waiting,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-manager.sent:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for coding question")
	}
	projected, _ := tasks.Get(record.TaskID)
	interactionID := projected.Coding.Question.InteractionID
	running := waiting
	running.State = codingtask.StateRunning
	running.Activity = codingtask.ActivityRunning
	running.Revision = 3
	running.UpdatedAt = 3
	running.Question = nil
	if err := fixture.Loop.remoteCoding.projectResult(
		fixture.Agent.Workspace,
		tasks,
		projected,
		running,
	); err != nil {
		t.Fatal(err)
	}
	interaction, found := fixture.Loop.interactionRegistryForWorkspace(
		fixture.Agent.Workspace,
	).Get(interactionID)
	if !found || interaction.Status != interactions.StatusCancelled {
		t.Fatalf("retired coding question = %#v, %v", interaction, found)
	}
	select {
	case controls := <-manager.synced:
		if controls.Metadata.InteractionControls != bus.OutboundInteractionControlsRemove {
			t.Fatalf("retired question controls = %#v", controls)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for stale question control removal")
	}
}

func TestRemoteCodingTerminalDeliveryIsDeduplicated(t *testing.T) {
	al, messageBus, _, workspace := newDeliveryCoordinatorTestRuntime(t, "unused")
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	al.cfg.Execution.RemoteCodingProjects = remoteCodingTestProjects()
	if err := al.ConfigureRemoteCodingTaskRuntime(
		func(*config.Config) (RemoteCodingInvoker, error) { return newFakeRemoteCodingInvoker(), nil },
	); err != nil {
		t.Fatal(err)
	}
	agent := al.GetRegistry().GetDefaultAgent()
	fixture := &agentLoopTestFixture{Loop: al, Agent: agent, Config: al.cfg, Bus: messageBus}
	record := createRemoteCodingTestRecord(t, fixture, taskregistry.StatusRunning)
	result := nodes.CodingTaskResult{
		TaskID: record.TaskID, TaskGenerationID: record.GenerationID,
		ProjectAlias: record.Coding.Project, ProjectRevision: record.Coding.Revision,
		Mode: record.Coding.Mode, ThreadID: record.Coding.ThreadID,
		ThreadOpenMode: codingtask.ThreadOpenNew, WorkerGenerationID: record.Coding.WorkerGenerationID,
		State: codingtask.StateCompleted, Revision: 2, Activity: codingtask.ActivityIdle,
		Branch: "mintclaw/coding-task", HandoffID: strings.Repeat("b", 64),
		AcceptedAt: 1, UpdatedAt: 2,
		TerminalReport: &codingtask.TerminalReport{
			Summary:      "The root cause was identified and fixed.",
			ChangedPaths: []string{"pkg/example.go"},
			Validations:  []codingtask.ValidationOutcome{{Kind: "command", Status: "succeeded"}},
			Commit:       "0123456789abcdef", CleanupState: "retained",
		},
	}
	tasks := al.taskRegistryForWorkspace(workspace)
	if err := al.remoteCoding.projectResult(workspace, tasks, record, result); err != nil {
		t.Fatal(err)
	}
	var message bus.OutboundMessage
	select {
	case message = <-manager.sent:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for terminal coding delivery")
	}
	if !strings.Contains(message.Content, "root cause was identified") {
		t.Fatalf("terminal coding content = %q", message.Content)
	}
	if message.Context.SenderID != "owner-42" || message.Context.TopicID != "topic-1" ||
		message.Metadata.MessageKind != bus.OutboundMessageKindFinalReply {
		t.Fatalf("terminal coding delivery = %#v", message)
	}
	settled, _ := tasks.Get(record.TaskID)
	if settled.Status != taskregistry.StatusSucceeded ||
		settled.DeliveryStatus != taskregistry.DeliveryDelivered ||
		settled.LastCompletionID != "coding-task:"+record.GenerationID {
		t.Fatalf("settled coding task = %#v", settled)
	}
	if err := al.remoteCoding.deliverTerminal(t.Context(), workspace, tasks, settled); err != nil {
		t.Fatal(err)
	}
	select {
	case duplicate := <-manager.sent:
		t.Fatalf("duplicate coding completion: %#v", duplicate)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRemoteCodingTaskCancellationTargetsExactWorkerAndDelivers(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &mockProvider{})
	configureRemoteCodingTestGrant(fixture.Config)
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, fixture.Loop, manager)
	invoker := newFakeRemoteCodingInvoker()
	if err := fixture.Loop.ConfigureRemoteCodingTaskRuntime(
		func(*config.Config) (RemoteCodingInvoker, error) { return invoker, nil },
	); err != nil {
		t.Fatal(err)
	}
	record := createRemoteCodingTestRecord(t, fixture, taskregistry.StatusRunning)
	tool, err := fixture.Loop.NewRemoteCodingTaskTool(fixture.Config, fixture.Agent.ID)
	if err != nil || tool == nil {
		t.Fatalf("NewRemoteCodingTaskTool() = %#v, %v", tool, err)
	}
	owner := remoteCodingTestContext(
		fixture.Agent.Workspace,
		"history-two",
		"telegram-route",
		"owner-42",
		"cancel-call",
	)
	result := tool.Execute(owner, map[string]any{"action": "cancel", "task_id": record.TaskID})
	if result == nil || result.IsError {
		t.Fatalf("cancel result = %#v", result)
	}
	var delivered bus.OutboundMessage
	select {
	case delivered = <-manager.sent:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for coding cancellation delivery")
	}
	if !strings.Contains(delivered.Content, "canceled by the requester") {
		t.Fatalf("cancellation delivery = %q", delivered.Content)
	}
	settled, _ := fixture.Loop.taskRegistryForWorkspace(fixture.Agent.Workspace).Get(record.TaskID)
	if settled.Status != taskregistry.StatusCancelled ||
		settled.DeliveryStatus != taskregistry.DeliveryDelivered {
		t.Fatalf("canceled coding task = %#v", settled)
	}
	var cancelCalls int
	for _, call := range invoker.snapshot() {
		if call.command != nodes.CodingCommandTaskCancel {
			continue
		}
		cancelCalls++
		input, ok := call.input.(nodes.CodingTaskCancelInput)
		if !ok || input.TaskID != record.TaskID ||
			input.TaskGenerationID != record.GenerationID ||
			input.WorkerGenerationID != record.Coding.WorkerGenerationID {
			t.Fatalf("coding cancellation authority = %#v", call.input)
		}
	}
	if cancelCalls != 1 {
		t.Fatalf("coding cancellation calls = %d", cancelCalls)
	}
}

func TestRemoteCodingTaskSurvivesGatewayRegistryRestore(t *testing.T) {
	workspace := t.TempDir()
	first := newAgentLoopTestFixtureWithWorkspace(t, workspace, &mockProvider{})
	configureRemoteCodingTestGrant(first.Config)
	coding := createRemoteCodingTestRecord(t, first, taskregistry.StatusRunning)
	tasks := first.Loop.taskRegistryForWorkspace(workspace)
	if err := tasks.Create(taskregistry.Record{
		TaskID: "ordinary-active-task", Runtime: taskregistry.RuntimeTool,
		Task: "ordinary process-local task", Status: taskregistry.StatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	first.Close()

	second := newAgentLoopTestFixtureWithWorkspace(t, workspace, &mockProvider{})
	configureRemoteCodingTestGrant(second.Config)
	restored := second.Loop.taskRegistryForWorkspace(workspace)
	codingAfter, found := restored.Get(coding.TaskID)
	if !found || codingAfter.Status != taskregistry.StatusRunning || codingAfter.Coding == nil {
		t.Fatalf("restored coding task = %#v, %v", codingAfter, found)
	}
	ordinary, found := restored.Get("ordinary-active-task")
	if !found || ordinary.Status != taskregistry.StatusLost {
		t.Fatalf("restored ordinary task = %#v, %v", ordinary, found)
	}
}

func configureRemoteCodingTestGrant(cfg *config.Config) {
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"companion": {Type: "node", Node: "developer-mac"},
	}
	cfg.Execution.RemoteCodingProjects = remoteCodingTestProjects()
}

func remoteCodingTestProjects() map[string]config.RemoteCodingProject {
	return map[string]config.RemoteCodingProject{
		"mintclaw": {
			Target: "companion", Project: "mintclaw", Revision: "project-v1",
			Modes: []codingtask.TaskMode{codingtask.TaskModeInvestigate},
			Requesters: []config.RemoteCodingRequester{{
				Agent: "main", Channel: "telegram", Sender: "owner-42",
			}},
		},
	}
}

func remoteCodingTestContext(
	workspace string,
	sessionKey string,
	routeSessionKey string,
	sender string,
	toolCallID string,
) context.Context {
	inbound := bus.InboundContext{
		Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
		TopicID: "topic-1", SenderID: sender, ActorID: sender, MessageID: "message-1",
	}
	ctx := toolshared.WithToolInboundMetadata(context.Background(), inbound)
	ctx = toolshared.WithToolContext(ctx, inbound.Channel, inbound.ChatID)
	ctx = toolshared.WithToolTopicID(ctx, inbound.TopicID)
	ctx = toolshared.WithToolSessionContext(ctx, "main", sessionKey, nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, routeSessionKey)
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, "turn-one")
	return toolshared.WithToolCallID(ctx, toolCallID)
}

func createRemoteCodingTestRecord(
	t *testing.T,
	fixture *agentLoopTestFixture,
	status taskregistry.Status,
) taskregistry.Record {
	t.Helper()
	tasks := fixture.Loop.taskRegistryForWorkspace(fixture.Agent.Workspace)
	record := taskregistry.Record{
		TaskID: "coding-" + uuid.NewString(), Runtime: taskregistry.RuntimeCoding,
		TaskKind: "coding_task", RequesterSessionKey: "telegram-route",
		OwnerKey: remoteCodingOwnerKey("main", "telegram-route", "owner-42"),
		Channel:  "telegram", ChatID: "chat-1", TopicID: "topic-1", AgentID: "main",
		Label: "mintclaw", Task: "Investigate the regression.", Status: status,
		DeliveryStatus: taskregistry.DeliveryPending, NotifyPolicy: taskregistry.NotifyDoneOnly,
		DeliveryMode: string(toolshared.AsyncDeliveryUserOnly),
		Coding: &taskregistry.CodingProjection{
			SchemaVersion: taskregistry.CodingProjectionSchemaV1,
			Alias:         "mintclaw", Target: "companion", Project: "mintclaw",
			Revision: "project-v1", Mode: codingtask.TaskModeInvestigate,
			RequestDigest:   strings.Repeat("a", 64),
			RouteSessionKey: "telegram-route", SessionKey: "history-one",
			ActorID: "owner-42", SenderID: "owner-42", AccountID: "primary",
			ChatType: "direct", OriginMessageID: "message-1",
			ThreadID: uuid.NewString(), WorkerGenerationID: uuid.NewString(),
			NodeState: string(codingtask.StateRunning), NodeRevision: 1,
		},
	}
	if err := tasks.Create(record); err != nil {
		t.Fatal(err)
	}
	stored, found := tasks.Get(record.TaskID)
	if !found {
		t.Fatal("created coding task was not found")
	}
	return stored
}

func waitRemoteCodingTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for remote coding state")
}
