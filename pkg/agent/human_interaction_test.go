package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type interactionChannelManager struct {
	*recordingChannelManager
	sent        chan bus.OutboundMessage
	synced      chan bus.OutboundMessage
	sendErrMu   sync.RWMutex
	sendErr     error
	sendStarted chan struct{}
	sendRelease chan struct{}
	outbox      *outbox.Coordinator
}

type blockingInteractionProvider struct {
	started chan struct{}
	release chan struct{}
	err     error

	mu       sync.Mutex
	calls    int
	messages [][]providers.Message
}

type interactionDrainProvider struct {
	secondStarted chan struct{}
	secondRelease chan struct{}
	mu            sync.Mutex
	calls         int
}

type interactionCaptureProvider struct {
	messages []providers.Message
}

func (p *interactionCaptureProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.messages = append([]providers.Message(nil), messages...)
	return &providers.LLMResponse{Content: "continued with corrected navigation", FinishReason: "stop"}, nil
}

func (*interactionCaptureProvider) GetDefaultModel() string { return "interaction-capture-model" }

func (p *interactionDrainProvider) Chat(
	ctx context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 2 {
		close(p.secondStarted)
		select {
		case <-p.secondRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &providers.LLMResponse{Content: "interaction drain complete", FinishReason: "stop"}, nil
}

func (*interactionDrainProvider) GetDefaultModel() string { return "interaction-drain-model" }

func newBlockingInteractionProvider() *blockingInteractionProvider {
	return &blockingInteractionProvider{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (p *blockingInteractionProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.calls++
	p.messages = append(p.messages, append([]providers.Message(nil), messages...))
	p.mu.Unlock()
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.err != nil {
		return nil, p.err
	}
	return &providers.LLMResponse{
		Content: "DUPLICATE_INTERACTION_OK: первый", FinishReason: "stop",
	}, nil
}

func (p *blockingInteractionProvider) GetDefaultModel() string {
	return "blocking-interaction-model"
}

func (p *blockingInteractionProvider) snapshot() (int, [][]providers.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	messages := make([][]providers.Message, len(p.messages))
	for i := range p.messages {
		messages[i] = append([]providers.Message(nil), p.messages[i]...)
	}
	return p.calls, messages
}

type durableApprovalHook struct {
	actionSummary string
	revoked       bool
	calls         int
}

type durableApprovalHardAbortHook struct{ durableApprovalHook }

func (*durableApprovalHardAbortHook) AfterTool(
	_ context.Context,
	response *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision) {
	return response, HookDecision{Action: HookActionHardAbort}
}

func (h *durableApprovalHook) ApproveTool(
	context.Context,
	*ToolApprovalRequest,
) (ApprovalDecision, error) {
	h.calls++
	if h.revoked {
		return ApprovalDecision{Reason: "policy revoked human override"}, nil
	}
	return ApprovalDecision{RequireHuman: true, ActionSummary: h.actionSummary}, nil
}

type postExecutionBarrierApprovalHook struct {
	afterTool chan struct{}
}

func (h *postExecutionBarrierApprovalHook) ApproveTool(
	context.Context,
	*ToolApprovalRequest,
) (ApprovalDecision, error) {
	return ApprovalDecision{
		RequireHuman:  true,
		ActionSummary: "Run the post-execution barrier action",
	}, nil
}

func (*postExecutionBarrierApprovalHook) BeforeTool(
	_ context.Context,
	req *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	return req, HookDecision{Action: HookActionContinue}, nil
}

func (h *postExecutionBarrierApprovalHook) AfterTool(
	ctx context.Context,
	result *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	select {
	case h.afterTool <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return result, HookDecision{Action: HookActionContinue}, nil
}

type immediateApprovalTool struct {
	executions int
	result     *toolshared.ToolResult
}

func (*immediateApprovalTool) Name() string { return "approval_immediate" }

func (*immediateApprovalTool) Description() string { return "Run an immediate protected test action" }

func (*immediateApprovalTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *immediateApprovalTool) Execute(
	context.Context,
	map[string]any,
) *toolshared.ToolResult {
	tool.executions++
	return tool.result
}

type approvalCountingTool struct {
	executions int
}

func (*approvalCountingTool) Name() string { return "approval_counting" }

func (*approvalCountingTool) Description() string { return "Run a protected test action" }

func (*approvalCountingTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (t *approvalCountingTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	t.executions++
	return toolshared.NewToolResult("protected action completed")
}

type hardAbortApprovalCountingTool struct{ executions int }

func (*hardAbortApprovalCountingTool) Name() string { return "approval_hard_abort" }
func (*hardAbortApprovalCountingTool) Description() string {
	return "Run a protected test action that hard-aborts its turn"
}

func (*hardAbortApprovalCountingTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *hardAbortApprovalCountingTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *toolshared.ToolResult {
	tool.executions++
	if ts := turnStateFromContext(ctx); ts != nil {
		_ = ts.requestHardAbort()
	}
	return toolshared.NewToolResult("protected action hard-aborted")
}

type suspendingHardAbortApprovalTool struct{ executions int }

func (*suspendingHardAbortApprovalTool) Name() string { return "approval_suspend_hard_abort" }
func (*suspendingHardAbortApprovalTool) Description() string {
	return "Run a protected test action that transfers descendant continuation ownership"
}

func (*suspendingHardAbortApprovalTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *suspendingHardAbortApprovalTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *toolshared.ToolResult {
	tool.executions++
	if ts := turnStateFromContext(ctx); ts != nil {
		_ = ts.requestHardAbort()
	}
	return &toolshared.ToolResult{
		ForLLM:  "descendant task owns a durable continuation",
		Control: toolshared.ToolControl{TaskSuspended: true},
	}
}

type blockingApprovalTool struct {
	started        chan struct{}
	canceled       chan struct{}
	terminalResult *toolshared.ToolResult
}

func newBlockingApprovalTool() *blockingApprovalTool {
	return &blockingApprovalTool{
		started:  make(chan struct{}, 1),
		canceled: make(chan struct{}, 1),
	}
}

func (*blockingApprovalTool) Name() string { return "approval_blocking" }

func (*blockingApprovalTool) Description() string { return "Run a blocking protected test action" }

func (*blockingApprovalTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *blockingApprovalTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *toolshared.ToolResult {
	select {
	case tool.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case tool.canceled <- struct{}{}:
	default:
	}
	if tool.terminalResult != nil {
		return tool.terminalResult
	}
	return &toolshared.ToolResult{ForLLM: ctx.Err().Error(), IsError: true}
}

type approvalBindingTool struct {
	executions           int
	bindingCalls         []string
	bindingContinuations []bool
	executionIDs         []string
	workspaces           []string
}

type browserHandoffContinuationTool struct {
	ownerExecutionID      string
	released              bool
	operations            []string
	executionIDs          []string
	approvalContinuations []bool
}

func (*browserHandoffContinuationTool) Name() string { return "browser_handoff_continuation" }

func (*browserHandoffContinuationTool) Description() string {
	return "Exercise browser ownership across a human handoff continuation"
}

func (*browserHandoffContinuationTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type": "string", "enum": []string{"handoff", "resume", "observe"},
			},
		},
		"required":             []string{"operation"},
		"additionalProperties": false,
	}
}

func (tool *browserHandoffContinuationTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	operation, _ := args["operation"].(string)
	executionID := toolshared.ToolExecutionID(ctx)
	tool.operations = append(tool.operations, operation)
	tool.executionIDs = append(tool.executionIDs, executionID)
	tool.approvalContinuations = append(
		tool.approvalContinuations,
		toolshared.ToolApprovalContinuation(ctx),
	)
	switch operation {
	case "handoff":
		tool.ownerExecutionID = executionID
		return &toolshared.ToolResult{
			ForLLM: `{"controller":"human"}`,
			Control: toolshared.ToolControl{
				Suspension: &interactions.SuspensionRequest{
					Kind: interactions.KindQuestion, PromptSummary: "Release browser control", Timeout: time.Minute,
					Questions: []interactions.Question{{
						ID: "release_browser", Header: "Browser control",
						Question: "Release browser control?",
					}},
				},
				ResolveSuspension: func(_ context.Context, outcome interactions.Outcome) error {
					tool.released = outcome == interactions.OutcomeAnswered
					return nil
				},
			},
		}
	case "resume", "observe":
		if !tool.released || executionID == "" || executionID != tool.ownerExecutionID {
			return toolshared.ErrorResult("browser owner identity changed across continuation")
		}
		return toolshared.NewToolResult(`{"controller":"agent"}`)
	default:
		return toolshared.ErrorResult("unknown browser continuation operation")
	}
}

func (*approvalBindingTool) Name() string { return "approval_binding" }

func (*approvalBindingTool) Description() string { return "Run a prepared protected action" }

func (*approvalBindingTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"mutable": map[string]any{"type": "string"}},
		"required":   []any{"mutable"},
	}
}

func (t *approvalBindingTool) ApprovalArguments(
	ctx context.Context,
	_ map[string]any,
) (map[string]any, error) {
	t.bindingCalls = append(t.bindingCalls, toolshared.ToolCallID(ctx))
	t.bindingContinuations = append(
		t.bindingContinuations,
		toolshared.ToolApprovalContinuation(ctx),
	)
	t.executionIDs = append(t.executionIDs, toolshared.ToolExecutionID(ctx))
	t.workspaces = append(t.workspaces, toolshared.ToolWorkspace(ctx))
	return map[string]any{"plan_hash": "prepared-plan-hash"}, nil
}

func (t *approvalBindingTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	t.executions++
	return toolshared.NewToolResult("prepared action completed")
}

func TestToolExecutionIdentityDoesNotRepeatAcrossAgentLoops(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "test-model"
	executionIDs := make([]string, 0, 2)
	turnIDs := make([]string, 0, 2)
	for range 2 {
		loop := NewAgentLoop(
			cfg,
			bus.NewMessageBus(),
			&sequenceProvider{},
			WithIsolatedToolBootstrap(),
		)
		agent := loop.registry.GetDefaultAgent()
		scope := loop.newTurnEventScope(agent.ID, agent.Workspace, "session-1", nil)
		state := newTurnState(
			agent,
			turnSpec{Dispatch: DispatchRequest{SessionKey: "session-1"}},
			scope,
		)
		executionIDs = append(executionIDs, state.executionID)
		turnIDs = append(turnIDs, state.turnID)
		loop.Close()
	}
	if turnIDs[0] != turnIDs[1] {
		t.Fatalf("test setup did not reproduce turn counter reset: %#v", turnIDs)
	}
	if executionIDs[0] == "" || executionIDs[1] == "" ||
		executionIDs[0] == executionIDs[1] {
		t.Fatalf("execution identities repeated across loops: %#v", executionIDs)
	}
}

type approvalContextTool struct {
	executions   int
	inbound      bus.InboundContext
	routeChannel string
	routeChatID  string
	bypass       bool
	continued    bool
}

func (*approvalContextTool) Name() string { return "approval_context" }

func (*approvalContextTool) Description() string { return "Capture protected inbound context" }

func (*approvalContextTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (t *approvalContextTool) Execute(ctx context.Context, _ map[string]any) *toolshared.ToolResult {
	t.executions++
	t.inbound = toolshared.ToolInboundContext(ctx)
	t.routeChannel = toolshared.ToolChannel(ctx)
	t.routeChatID = toolshared.ToolChatID(ctx)
	t.bypass = toolshared.ToolApprovalBypass(ctx)
	t.continued = toolshared.ToolApprovalContinuation(ctx)
	return toolshared.NewToolResult("protected context captured")
}

type interactionOwnershipBus struct {
	*bus.MessageBus
	mu       sync.Mutex
	acked    []string
	released []string
}

func (b *interactionOwnershipBus) AckInbound(ctx context.Context, msg bus.InboundMessage) error {
	b.mu.Lock()
	b.acked = append(b.acked, msg.SpoolID)
	b.mu.Unlock()
	return b.MessageBus.AckInbound(ctx, msg)
}

func (b *interactionOwnershipBus) ReleaseInbound(
	ctx context.Context,
	msg bus.InboundMessage,
	cause error,
) error {
	b.mu.Lock()
	b.released = append(b.released, msg.SpoolID)
	b.mu.Unlock()
	return b.MessageBus.ReleaseInbound(ctx, msg, cause)
}

func (b *interactionOwnershipBus) counts() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.acked), len(b.released)
}

func (b *interactionOwnershipBus) ownership() ([]string, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.acked...), append([]string(nil), b.released...)
}

func countMatchingStrings(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func newInteractionChannelManager() *interactionChannelManager {
	return &interactionChannelManager{
		recordingChannelManager: &recordingChannelManager{},
		sent:                    make(chan bus.OutboundMessage, 16),
		synced:                  make(chan bus.OutboundMessage, 16),
	}
}

func (m *interactionChannelManager) SyncInteractionControls(msg bus.OutboundMessage) error {
	m.synced <- msg
	return nil
}

func (m *interactionChannelManager) SendMessage(_ context.Context, msg bus.OutboundMessage) error {
	if msg.DeliveryID != "" && m.outbox != nil {
		if err := m.outbox.BeginAttempt(msg.DeliveryID); err != nil {
			return err
		}
	}
	if m.sendStarted != nil {
		select {
		case m.sendStarted <- struct{}{}:
		default:
		}
	}
	if m.sendRelease != nil {
		<-m.sendRelease
	}
	m.sendErrMu.RLock()
	sendErr := m.sendErr
	m.sendErrMu.RUnlock()
	if sendErr != nil {
		if msg.DeliveryID != "" && m.outbox != nil {
			outcome := outbox.Outcome{Error: sendErr.Error()}
			var persistErr error
			if channels.DeliveryDefinitelyNotSent(sendErr) {
				persistErr = m.outbox.MarkDefinitelyFailed(msg.DeliveryID, outcome)
			} else {
				persistErr = m.outbox.MarkAmbiguous(msg.DeliveryID, outcome)
			}
			return errors.Join(sendErr, persistErr)
		}
		return sendErr
	}
	m.sent <- msg
	if msg.DeliveryID != "" && m.outbox != nil {
		if err := m.outbox.MarkDelivered(msg.DeliveryID, outbox.Outcome{}); err != nil {
			return err
		}
	}
	return nil
}

func (m *interactionChannelManager) setSendError(err error) {
	m.sendErrMu.Lock()
	m.sendErr = err
	m.sendErrMu.Unlock()
}

func (m *interactionChannelManager) SupportsDurableDeliveryReceipts() bool {
	return m != nil && m.outbox != nil
}

func attachInteractionOutbox(
	t *testing.T,
	al *AgentLoop,
	messageBus *bus.MessageBus,
	manager *interactionChannelManager,
) *outbox.Coordinator {
	t.Helper()
	ensureTestTurnRunner(al)
	coordinator, err := outbox.OpenCoordinator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	al.SetOutboundOutbox(coordinator)
	manager.outbox = coordinator
	dispatchCtx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-dispatchCtx.Done():
				return
			case msg := <-messageBus.OutboundChan():
				_ = manager.SendMessage(dispatchCtx, msg)
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		al.SetOutboundOutbox(nil)
		manager.outbox = nil
		if err := coordinator.Close(); err != nil {
			t.Error(err)
		}
	})
	return coordinator
}

func installInteractionChannelManager(
	t *testing.T,
	al *AgentLoop,
	manager *interactionChannelManager,
) *outbox.Coordinator {
	t.Helper()
	messageBus, ok := al.bus.(*bus.MessageBus)
	if !ok {
		t.Fatalf("interaction test bus = %T, want *bus.MessageBus", al.bus)
	}
	al.SetChannelManager(manager)
	return attachInteractionOutbox(t, al, messageBus, manager)
}

func openTestInteractionOutbox(t *testing.T, al *AgentLoop) *outbox.Coordinator {
	t.Helper()
	ensureTestTurnRunner(al)
	coordinator, err := outbox.OpenCoordinator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	al.SetOutboundOutbox(coordinator)
	t.Cleanup(func() {
		al.SetOutboundOutbox(nil)
		if err := coordinator.Close(); err != nil {
			t.Error(err)
		}
	})
	return coordinator
}

func bindTestInteractionPrompt(
	t *testing.T,
	registry *interactions.Registry,
	record interactions.Record,
) interactions.Record {
	t.Helper()
	deliveryID, err := outbox.DeliveryID(interactionPromptDeliveryIdentity(record))
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.BindPromptDelivery(record.ID, record.Revision, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func seedTestInteractionPromptOutcome(
	t *testing.T,
	coordinator *outbox.Coordinator,
	workspace string,
	record interactions.Record,
	status outbox.Status,
	attempts int,
	retryAfter ...time.Time,
) outbox.Intent {
	t.Helper()
	return seedTestInteractionPromptOutcomeWithMessages(
		t, coordinator, workspace, record, status, attempts, nil, retryAfter...,
	)
}

func seedTestInteractionPromptOutcomeWithMessages(
	t *testing.T,
	coordinator *outbox.Coordinator,
	workspace string,
	record interactions.Record,
	status outbox.Status,
	attempts int,
	platformMessageIDs []string,
	retryAfter ...time.Time,
) outbox.Intent {
	t.Helper()
	message := interactionPromptMessage(record)
	message.Content = renderInteractionPrompt(record)
	for attempt := range attempts {
		admission, err := coordinator.AdmitMessage(
			workspace,
			interactionPromptDeliveryIdentity(record),
			message,
		)
		if err != nil || !admission.Dispatch {
			t.Fatalf("AdmitMessage(attempt %d) = (%#v, %v)", attempt+1, admission, err)
		}
		if err = coordinator.PrepareAdmission(admission.Lease); err != nil {
			t.Fatal(err)
		}
		if err = coordinator.CommitAdmission(admission.Lease); err != nil {
			t.Fatal(err)
		}
		if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
			t.Fatal(err)
		}
		outcome := outbox.Outcome{
			PlatformMessageIDs: append([]string(nil), platformMessageIDs...),
			Error:              "test delivery outcome",
		}
		if len(retryAfter) > 0 && attempt == attempts-1 {
			outcome.RetryAfter = retryAfter[0]
		}
		if attempt < attempts-1 || status == outbox.StatusDefinitelyFailed {
			err = coordinator.MarkDefinitelyFailed(admission.Intent.ID, outcome)
		} else if status == outbox.StatusAmbiguous {
			err = coordinator.MarkAmbiguous(admission.Intent.ID, outcome)
		} else {
			err = coordinator.MarkDelivered(admission.Intent.ID, outbox.Outcome{
				PlatformMessageIDs: append([]string(nil), platformMessageIDs...),
			})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	intent, err := coordinator.Get(record.PromptDeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func TestSyncInteractionControlsUsesDeliveredPromptMessageID(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	al.SetChannelManager(manager)
	coordinator := openTestInteractionOutbox(t, al)
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	argumentHash, err := interactions.HashArguments(agent.Workspace, map[string]any{"target": "test"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := registry.Create(interactions.CreateRequest{
		Kind: interactions.KindApproval,
		Route: interactions.Route{
			AgentID: agent.ID, SessionKey: "session-prompt-id", RouteSessionKey: "route-prompt-id",
			Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
		},
		Origin: interactions.Origin{
			TurnID: "turn-prompt-id", ToolCallID: "call-prompt-id", ToolName: "protected_tool",
			ArgumentHash: argumentHash,
			ExecutionContext: &bus.InboundContext{
				Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", MessageID: "origin-1",
			},
		},
		PromptSummary: "Run protected action", ApprovalAction: "Run protected action",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	message := interactionPromptMessage(record)
	message.Content = renderInteractionPrompt(record)
	admission, err := coordinator.AdmitMessage(
		agent.Workspace,
		interactionPromptDeliveryIdentity(record),
		message,
	)
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMessage() = (%#v, %v)", admission, err)
	}
	if err = coordinator.PrepareAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.CommitAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.MarkDelivered(admission.Intent.ID, outbox.Outcome{
		PlatformMessageIDs: []string{"7716"},
	}); err != nil {
		t.Fatal(err)
	}

	al.syncInteractionControls(agent.Workspace, record, bus.OutboundInteractionControlsRemove)
	select {
	case synced := <-manager.synced:
		if synced.ReplyToMessageID != "7716" || synced.Metadata.InteractionShortID != record.ShortID ||
			!synced.Metadata.RemovesInteractionControls() {
			t.Fatalf("synced interaction controls = %#v", synced)
		}
	case <-time.After(time.Second):
		t.Fatal("interaction controls were not synchronized")
	}
}

func testInteractionFinalIdentity(record interactions.Record) outbox.Identity {
	return outbox.Identity{
		SourceID:   interactionDeliveryKey(record.ID, "final"),
		Ordinal:    0,
		Kind:       outbox.KindMessage,
		Channel:    record.Route.Channel,
		ChatID:     record.Route.ChatID,
		SessionKey: record.Route.SessionKey,
	}
}

func bindTestInteractionFinal(
	t *testing.T,
	registry *interactions.Registry,
	record interactions.Record,
) interactions.Record {
	t.Helper()
	deliveryID, err := outbox.DeliveryID(testInteractionFinalIdentity(record))
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.BindFinalDelivery(record.ID, record.Revision, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func seedTestInteractionFinalOutcome(
	t *testing.T,
	coordinator *outbox.Coordinator,
	registry *interactions.Registry,
	workspace string,
	record interactions.Record,
	status outbox.Status,
	attempts int,
	retryAfter ...time.Time,
) (interactions.Record, outbox.Intent) {
	t.Helper()
	record = bindTestInteractionFinal(t, registry, record)
	message := bus.OutboundMessage{
		Channel: record.Route.Channel, ChatID: record.Route.ChatID,
		AgentID: record.Route.AgentID, SessionKey: record.Route.SessionKey,
		Content: "final response",
	}
	for attempt := range attempts {
		admission, err := coordinator.AdmitMessage(
			workspace,
			testInteractionFinalIdentity(record),
			message,
		)
		if err != nil || !admission.Dispatch {
			t.Fatalf("AdmitMessage(attempt %d) = (%#v, %v)", attempt+1, admission, err)
		}
		if err = coordinator.PrepareAdmission(admission.Lease); err != nil {
			t.Fatal(err)
		}
		if err = coordinator.CommitAdmission(admission.Lease); err != nil {
			t.Fatal(err)
		}
		if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
			t.Fatal(err)
		}
		outcome := outbox.Outcome{Error: "test final delivery outcome"}
		if len(retryAfter) > 0 && attempt == attempts-1 {
			outcome.RetryAfter = retryAfter[0]
		}
		if attempt < attempts-1 || status == outbox.StatusDefinitelyFailed {
			err = coordinator.MarkDefinitelyFailed(admission.Intent.ID, outcome)
		} else if status == outbox.StatusAmbiguous {
			err = coordinator.MarkAmbiguous(admission.Intent.ID, outcome)
		} else {
			err = coordinator.MarkDelivered(admission.Intent.ID, outbox.Outcome{})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	intent, err := coordinator.Get(record.FinalDeliveryIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	return record, intent
}

func TestInteractionPromptRecoveryHonorsRetryDeadline(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	coordinator := installInteractionChannelManager(t, al, manager)
	workspace := agent.Workspace
	request := testToolSuspensionRequest(workspace)
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	now := time.Now().UTC()
	retryAt := now.Add(10 * time.Minute)
	seedTestInteractionPromptOutcome(
		t, coordinator, workspace, record, outbox.StatusDefinitelyFailed, 1, retryAt,
	)

	if recovered := al.recoverInteractionPromptAt(t.Context(), workspace, registry, record, now); recovered {
		t.Fatal("recovery retried prompt before its durable retry deadline")
	}
	intent, err := coordinator.Get(record.PromptDeliveryID)
	if err != nil || intent.Attempts != 1 || intent.Status != outbox.StatusDefinitelyFailed {
		t.Fatalf("intent before retry deadline = %+v, %v", intent, err)
	}
	select {
	case outbound := <-manager.sent:
		t.Fatalf("prompt sent before retry deadline: %#v", outbound)
	default:
	}

	if recovered := al.recoverInteractionPromptAt(
		t.Context(), workspace, registry, record, retryAt,
	); !recovered {
		t.Fatal("recovery did not retry prompt at its durable retry deadline")
	}
	updated, _ := registry.Get(record.ID)
	intent, err = coordinator.Get(record.PromptDeliveryID)
	if err != nil || updated.Status != interactions.StatusWaiting ||
		intent.Status != outbox.StatusDelivered || intent.Attempts != 2 {
		t.Fatalf("retried interaction = %+v, intent = %+v, %v", updated, intent, err)
	}
}

func TestRecoveredInteractionPromptAdmissionIsAbandonedAfterExpiry(t *testing.T) {
	al := &AgentLoop{cfg: config.DefaultConfig()}
	coordinator := openTestInteractionOutbox(t, al)
	workspace := t.TempDir()
	request := testToolSuspensionRequest(workspace)
	now := time.Now().UTC()
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	message := interactionPromptMessage(record)
	message.Content = renderInteractionPrompt(record)
	admission, err := coordinator.AdmitMessage(
		workspace,
		interactionPromptDeliveryIdentity(record),
		message,
	)
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMessage() = %+v, %v", admission, err)
	}

	publish, err := al.ReconcileRecoveredInteractionAdmission(admission, now)
	if err != nil || !publish {
		t.Fatalf("active admission reconciliation = %t, %v", publish, err)
	}
	publish, err = al.ReconcileRecoveredInteractionAdmission(admission, now.Add(2*time.Hour))
	if err != nil || publish {
		t.Fatalf("expired admission reconciliation = %t, %v", publish, err)
	}
	intent, err := coordinator.Get(admission.Intent.ID)
	if err != nil || intent.Status != outbox.StatusAbandoned {
		t.Fatalf("expired prompt intent = %+v, %v", intent, err)
	}
}

func TestRecoveredInteractionPromptAdmissionIsAbandonedAfterAgentRemoval(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	orphanWorkspace := filepath.Join(t.TempDir(), "removed-agent-workspace")
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(orphanWorkspace))
	request := testToolSuspensionRequest(orphanWorkspace)
	request.Route.AgentID = "removed-agent"
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	instanceRoot := t.TempDir()
	first, err := outbox.OpenCoordinator(instanceRoot)
	if err != nil {
		t.Fatal(err)
	}
	message := interactionPromptMessage(record)
	message.Content = renderInteractionPrompt(record)
	admission, err := first.AdmitMessage(
		orphanWorkspace,
		interactionPromptDeliveryIdentity(record),
		message,
	)
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMessage() = %+v, %v", admission, err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := outbox.OpenCoordinator(instanceRoot)
	if err != nil {
		t.Fatal(err)
	}
	al.SetOutboundOutbox(second)
	t.Cleanup(func() {
		al.SetOutboundOutbox(nil)
		_ = second.Close()
	})
	admissions, err := second.Recover()
	if err != nil || len(admissions) != 1 {
		t.Fatalf("Recover() = %+v, %v", admissions, err)
	}
	publish, err := al.ReconcileRecoveredInteractionAdmission(admissions[0], time.Now().UTC())
	if err != nil || publish {
		t.Fatalf("orphaned prompt reconciliation = %t, %v", publish, err)
	}
	intent, err := second.Get(admission.Intent.ID)
	if err != nil || intent.Status != outbox.StatusAbandoned {
		t.Fatalf("orphaned prompt intent = %+v, %v", intent, err)
	}
	if current, ok := al.GetRegistry().GetAgent(agent.ID); !ok || current == nil {
		t.Fatal("active agent was disturbed while abandoning orphaned prompt")
	}
}

func TestRecoveredTaskInteractionPromptIsAbandonedAfterOwnerWorkspaceRemoval(t *testing.T) {
	al, continuationAgent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	removedOwnerWorkspace := filepath.Join(t.TempDir(), "removed-parent-workspace")
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(removedOwnerWorkspace))
	request := testToolSuspensionRequest(removedOwnerWorkspace)
	request.Route.AgentID = continuationAgent.ID
	request.Origin.TaskID = "task-owned-by-removed-parent"
	request.Origin.ContinuationSessionKey = "continuation-agent-session"
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	instanceRoot := t.TempDir()
	first, err := outbox.OpenCoordinator(instanceRoot)
	if err != nil {
		t.Fatal(err)
	}
	message := interactionPromptMessage(record)
	message.Content = renderInteractionPrompt(record)
	admission, err := first.AdmitMessage(
		removedOwnerWorkspace,
		interactionPromptDeliveryIdentity(record),
		message,
	)
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMessage() = %+v, %v", admission, err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := outbox.OpenCoordinator(instanceRoot)
	if err != nil {
		t.Fatal(err)
	}
	al.SetOutboundOutbox(second)
	t.Cleanup(func() {
		al.SetOutboundOutbox(nil)
		_ = second.Close()
	})
	admissions, err := second.Recover()
	if err != nil || len(admissions) != 1 {
		t.Fatalf("Recover() = %+v, %v", admissions, err)
	}
	publish, err := al.ReconcileRecoveredInteractionAdmission(admissions[0], time.Now().UTC())
	if err != nil || publish {
		t.Fatalf("orphaned task prompt reconciliation = %t, %v", publish, err)
	}
	intent, err := second.Get(admission.Intent.ID)
	if err != nil || intent.Status != outbox.StatusAbandoned {
		t.Fatalf("orphaned task prompt intent = %+v, %v", intent, err)
	}
	if current, ok := al.GetRegistry().GetAgent(continuationAgent.ID); !ok || current == nil {
		t.Fatal("continuation agent was disturbed while abandoning orphaned task prompt")
	}
}

func TestTerminalInteractionAbandonsUnpublishedPrompt(t *testing.T) {
	al := &AgentLoop{cfg: config.DefaultConfig()}
	coordinator := openTestInteractionOutbox(t, al)
	workspace := t.TempDir()
	request := testToolSuspensionRequest(workspace)
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	message := interactionPromptMessage(record)
	message.Content = renderInteractionPrompt(record)
	admission, err := coordinator.AdmitMessage(
		workspace,
		interactionPromptDeliveryIdentity(record),
		message,
	)
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMessage() = %+v, %v", admission, err)
	}
	if _, err = registry.Cancel(record.ID, record.Revision, "test_cancel"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	intent, err := coordinator.Get(admission.Intent.ID)
	if err != nil || intent.Status != outbox.StatusAbandoned {
		t.Fatalf("canceled prompt intent = %+v, %v", intent, err)
	}
	if err = coordinator.PrepareAdmission(admission.Lease); err == nil {
		t.Fatal("PrepareAdmission() accepted a canceled prompt lease")
	}
}

func TestInteractionPromptPublicationRevalidatesAfterAdmission(t *testing.T) {
	al := &AgentLoop{cfg: config.DefaultConfig()}
	coordinator := openTestInteractionOutbox(t, al)
	workspace := t.TempDir()
	request := testToolSuspensionRequest(workspace)
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	if _, err = registry.Cancel(record.ID, record.Revision, "cancel_before_admission"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	message := interactionPromptMessage(record)
	message.Content = renderInteractionPrompt(record)
	admission, err := coordinator.AdmitMessage(
		workspace,
		interactionPromptDeliveryIdentity(record),
		message,
	)
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMessage() = %+v, %v", admission, err)
	}
	if err = al.validateInteractionPromptPublication(registry, workspace, record, time.Now().UTC()); err == nil {
		t.Fatal("publication validation accepted a prompt canceled before admission")
	}
	intent, err := coordinator.Get(admission.Intent.ID)
	if err != nil || intent.Status != outbox.StatusAbandoned {
		t.Fatalf("revalidated prompt intent = %+v, %v", intent, err)
	}
}

func TestRecoveredInteractionPromptSettlesExactAdmissionReceipt(t *testing.T) {
	al := &AgentLoop{cfg: config.DefaultConfig()}
	coordinator := openTestInteractionOutbox(t, al)
	workspace := t.TempDir()
	request := testToolSuspensionRequest(workspace)
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	seedTestInteractionPromptOutcome(
		t, coordinator, workspace, record, outbox.StatusDefinitelyFailed, 1,
	)
	admissions, err := coordinator.Recover()
	if err != nil || len(admissions) != 1 {
		t.Fatalf("Recover() = %+v, %v", admissions, err)
	}
	settled := make(chan error, 1)
	go func() {
		settled <- al.SettleRecoveredInteractionAdmission(t.Context(), admissions[0])
	}()
	if err = coordinator.PrepareAdmission(admissions[0].Lease); err != nil {
		t.Fatalf("PrepareAdmission() error = %v", err)
	}
	if err = coordinator.CommitAdmission(admissions[0].Lease); err != nil {
		t.Fatalf("CommitAdmission() error = %v", err)
	}
	if err = coordinator.BeginAttempt(admissions[0].Intent.ID); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}
	if err = coordinator.MarkDelivered(admissions[0].Intent.ID, outbox.Outcome{}); err != nil {
		t.Fatalf("MarkDelivered() error = %v", err)
	}
	select {
	case err = <-settled:
		if err != nil {
			t.Fatalf("SettleRecoveredInteractionAdmission() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovered interaction prompt receipt was not settled")
	}
	updated, _ := registry.Get(record.ID)
	if updated.Status != interactions.StatusWaiting {
		t.Fatalf("settled interaction = %+v", updated)
	}
}

func TestRecoveredInteractionFinalAdmissionIsAbandonedWhenInteractionEnds(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	coordinator := openTestInteractionOutbox(t, al)
	workspace := agent.Workspace
	request := testToolSuspensionRequest(workspace)
	request.Route.AgentID = agent.ID
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(
		record.ID,
		record.Revision,
		interactions.Answer{Text: "Canary", ReceivedAt: time.Now().UnixMilli()},
		interactions.OutcomeAnswered,
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionFinal(t, registry, record)
	admission, err := coordinator.AdmitMessage(
		workspace,
		testInteractionFinalIdentity(record),
		bus.OutboundMessage{
			Channel: record.Route.Channel, ChatID: record.Route.ChatID,
			SessionKey: record.Route.SessionKey, Content: "recovered final",
		},
	)
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMessage() = %+v, %v", admission, err)
	}
	publish, err := al.ReconcileRecoveredInteractionAdmission(admission, time.Now().UTC())
	if err != nil || !publish {
		t.Fatalf("active final reconciliation = %t, %v", publish, err)
	}
	if _, err = registry.Fail(record.ID, record.Revision, "test_failure", "interaction ended"); err != nil {
		t.Fatal(err)
	}
	publish, err = al.ReconcileRecoveredInteractionAdmission(admission, time.Now().UTC())
	if err != nil || publish {
		t.Fatalf("terminal final reconciliation = %t, %v", publish, err)
	}
	intent, err := coordinator.Get(admission.Intent.ID)
	if err != nil || intent.Status != outbox.StatusAbandoned {
		t.Fatalf("terminal final intent = %+v, %v", intent, err)
	}
}

func TestRecoveredInteractionFinalSettlesExactAdmissionReceipt(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	coordinator := openTestInteractionOutbox(t, al)
	workspace := agent.Workspace
	request := testToolSuspensionRequest(workspace)
	request.Route.AgentID = agent.ID
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(
		record.ID,
		record.Revision,
		interactions.Answer{Text: "Canary", ReceivedAt: time.Now().UnixMilli()},
		interactions.OutcomeAnswered,
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionFinal(t, registry, record)
	admission, err := coordinator.AdmitMessage(
		workspace,
		testInteractionFinalIdentity(record),
		bus.OutboundMessage{
			Channel: record.Route.Channel, ChatID: record.Route.ChatID,
			SessionKey: record.Route.SessionKey, Content: "recovered final",
		},
	)
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMessage() = %+v, %v", admission, err)
	}
	if err = coordinator.PrepareAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.CommitAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.MarkDelivered(admission.Intent.ID, outbox.Outcome{}); err != nil {
		t.Fatal(err)
	}
	if err = al.SettleRecoveredInteractionAdmission(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.Status != interactions.StatusResolved || len(resolved.FinalDeliveryIDs) != 1 {
		t.Fatalf("settled final interaction = %+v", resolved)
	}
}

func TestCorruptHumanInteractionStoreFailsClosed(t *testing.T) {
	workspace := t.TempDir()
	storePath := interactions.WorkspaceStorePath(workspace)
	if err := os.MkdirAll(filepath.Dir(storePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	al := &AgentLoop{}
	if !al.hasNonterminalInteraction(workspace, "session") {
		t.Fatal("corrupt interaction state did not block normal inbound handling")
	}
}

func testToolSuspensionRequest(workspace string) ToolSuspensionRequest {
	return ToolSuspensionRequest{
		Workspace: workspace,
		Prompt: interactions.SuspensionRequest{
			Kind: interactions.KindQuestion,
			Questions: []interactions.Question{{
				ID: "deploy_mode", Header: "Deploy", Question: "Which mode should be used?",
				Options: []interactions.Option{
					{Label: "Canary", Description: "Deploy one profile first."},
					{Label: "All", Description: "Deploy every profile now."},
				},
			}},
			Timeout: time.Hour,
		},
		Route: interactions.Route{
			AgentID: "main", SessionKey: "session-1", RouteSessionKey: "route-1",
			Channel: "telegram", AccountID: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
		Origin: interactions.Origin{
			TurnID: "turn-1", ToolCallID: "call-question", ToolName: "request_user_input",
		},
	}
}

func markTestInteractionWaiting(
	t *testing.T,
	registry *interactions.Registry,
	record interactions.Record,
) interactions.Record {
	t.Helper()
	record = bindTestInteractionPrompt(t, registry, record)
	var err error
	record, err = registry.MarkWaiting(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func prepareWaitingControlInteraction(
	t *testing.T,
	al *AgentLoop,
	agent *AgentInstance,
	msg bus.InboundMessage,
	taskID string,
) (interactions.Record, *inboundDispatchTarget) {
	t.Helper()
	target, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("failed to resolve interaction control target")
	}
	route := interactions.Route{
		AgentID:         agent.ID,
		SessionKey:      target.SessionKey,
		RouteSessionKey: target.Allocation.RouteScopeKey,
		Channel:         msg.Context.Channel,
		AccountID:       msg.Context.Account,
		ChatID:          msg.Context.ChatID,
		ChatType:        msg.Context.ChatType,
		TopicID:         msg.Context.TopicID,
		SenderID:        msg.Context.SenderID,
	}
	origin := interactions.Origin{
		TurnID:     "turn-control",
		ToolCallID: "call-control-question",
		ToolName:   "request_user_input",
		TaskID:     taskID,
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
		Questions: []interactions.Question{{ID: "confirm", Question: "Proceed?"}},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	return record, target
}

func prepareWaitingControlInteractionWithContinuation(
	t *testing.T,
	al *AgentLoop,
	ownerAgent *AgentInstance,
	continuationAgent *AgentInstance,
	msg bus.InboundMessage,
	continuationKey string,
) (interactions.Record, *inboundDispatchTarget) {
	t.Helper()
	target, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("failed to resolve interaction target")
	}
	origin := interactions.Origin{
		TurnID: "turn-distinct-continuation", ToolCallID: "call-distinct-continuation",
		ToolName: "request_user_input", ContinuationSessionKey: continuationKey,
	}
	continuationAgent.Sessions.AddFullMessage(continuationKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: origin.ToolCallID, Name: origin.ToolName, Arguments: map[string]any{},
		}},
	})
	registry := al.interactionRegistryForWorkspace(ownerAgent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: interactions.KindQuestion,
		Route: interactions.Route{
			AgentID: continuationAgent.ID, SessionKey: target.SessionKey,
			RouteSessionKey: target.Allocation.RouteScopeKey,
			Channel:         msg.Context.Channel, AccountID: msg.Context.Account,
			ChatID: msg.Context.ChatID, ChatType: msg.Context.ChatType,
			SenderID: msg.Context.SenderID,
		},
		Origin: origin, Questions: []interactions.Question{{ID: "confirm", Question: "Proceed?"}},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	return record, target
}

func countInteractionToolResults(history []providers.Message, toolCallID string) int {
	count := 0
	for _, message := range history {
		if message.Role == "tool" && message.ToolCallID == toolCallID {
			count++
		}
	}
	return count
}

func TestHumanInteractionRuntimePersistsAndQueuesPromptBeforeWaiting(t *testing.T) {
	messageBus := bus.NewMessageBus()
	manager := newInteractionChannelManager()
	al := &AgentLoop{cfg: config.DefaultConfig(), bus: messageBus, channelManager: manager}
	attachInteractionOutbox(t, al, messageBus, manager)
	workspace := t.TempDir()

	request := testToolSuspensionRequest(workspace)
	request.Route.ChatType = "supergroup"
	request.Route.TopicID = "1771"
	request.ExecutionContext = &bus.InboundContext{MessageID: "question-origin"}
	disposition, err := (&humanInteractionRuntime{al: al, coordinator: &al.interactions}).SuspendToolCall(
		t.Context(), request,
	)
	if err != nil || !disposition.Durable || disposition.InteractionID == "" {
		t.Fatalf("SuspendToolCall() = (%#v, %v)", disposition, err)
	}
	record, ok := al.interactionRegistryForWorkspace(workspace).Get(disposition.InteractionID)
	if !ok || record.Status != interactions.StatusWaiting || record.PromptDeliveryID == "" {
		t.Fatalf("record = %#v", record)
	}
	select {
	case outbound := <-manager.sent:
		if !strings.Contains(outbound.Content, "Which mode should be used?") ||
			outbound.DeliveryID != record.PromptDeliveryID ||
			!strings.Contains(outbound.Content, "Canary") ||
			!strings.Contains(outbound.Content, "`/answer "+record.ShortID+" …`") ||
			strings.Contains(outbound.Content, "Input needed") ||
			strings.Contains(outbound.Content, "Reply with your answer") ||
			outbound.Metadata.InteractionID != record.ID ||
			outbound.Metadata.InteractionShortID != record.ShortID ||
			outbound.Context.Account != "primary" || outbound.Context.TopicID != "1771" ||
			outbound.ReplyToMessageID != "question-origin" ||
			outbound.Metadata.RequestID != "question-origin" ||
			!strings.Contains(outbound.Content, "`/stop`") ||
			outbound.Metadata.IsApprovalPrompt() {
			t.Fatalf("outbound prompt = %#v", outbound)
		}
		metadata := outbound.Metadata
		if !metadata.IsQuestionPrompt() ||
			!reflect.DeepEqual(metadata.InteractionChoices(), []string{"Canary", "All"}) {
			t.Fatalf("question prompt metadata = %#v", metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for interaction prompt")
	}
}

func TestTerminalInteractionDismissesContinuationToolFeedbackCarrier(t *testing.T) {
	manager := &recordingChannelManager{}
	record := interactions.Record{
		Route: interactions.Route{
			Channel: "telegram", ChatID: "chat-1", SessionKey: "owner-session",
		},
		Origin: interactions.Origin{
			ContinuationSessionKey: "task-continuation-session",
			ExecutionContext: &bus.InboundContext{
				Channel: "telegram", ChatID: "chat-1", TopicID: "topic-1",
			},
		},
	}
	(&toolFeedbackPublisher{channelManager: manager}).dismissTerminalInteraction(record)
	if len(manager.dismissedTargets) != 1 {
		t.Fatalf("dismissed targets = %#v, want one", manager.dismissedTargets)
	}
	target := manager.dismissedTargets[0]
	if target.SessionKey != record.Origin.ContinuationSessionKey ||
		target.SessionKey == record.Route.SessionKey {
		t.Fatalf("terminal feedback target = %#v", target)
	}
}

func TestChainedInteractionKeepsContinuationToolFeedbackCarrier(t *testing.T) {
	manager := &recordingChannelManager{}
	al := &AgentLoop{channelManager: manager}
	ensureTestTurnRunner(al)
	record := interactions.Record{
		ID: "interaction-chain", Status: interactions.StatusResolved,
		Route: interactions.Route{
			AgentID: "main", Channel: "telegram", ChatID: "chat-1", SessionKey: "owner-session",
		},
		Origin: interactions.Origin{
			TurnID: "turn-1", ContinuationSessionKey: "task-continuation-session",
			ExecutionContext: &bus.InboundContext{Channel: "telegram", ChatID: "chat-1"},
		},
	}
	al.observeInteractionEvent(t.TempDir(), interactions.EventObservation{
		Record: record,
		Event: interactions.Event{
			Type: interactions.EventResolved, Code: "continued_with_next_interaction",
		},
	})
	if len(manager.dismissedTargets) != 0 {
		t.Fatalf("chained interaction dismissed carrier: %#v", manager.dismissedTargets)
	}
	record.ID = "interaction-chain-2"
	al.observeInteractionEvent(t.TempDir(), interactions.EventObservation{
		Record: record,
		Event: interactions.Event{
			Type: interactions.EventResolved, Code: "continued_with_next_interaction",
		},
	})
	if len(manager.dismissedTargets) != 0 {
		t.Fatalf("second chained interaction dismissed carrier: %#v", manager.dismissedTargets)
	}

	record.ID = "interaction-chain-3"
	al.observeInteractionEvent(t.TempDir(), interactions.EventObservation{
		Record: record,
		Event:  interactions.Event{Type: interactions.EventResolved, Code: "completed"},
	})
	if len(manager.dismissedTargets) != 1 {
		t.Fatalf("final interaction dismissed targets = %#v, want one", manager.dismissedTargets)
	}
}

func TestNonTelegramApprovalPromptCarriesGenericControlsWithoutReplyThread(t *testing.T) {
	messageBus := bus.NewMessageBus()
	manager := newInteractionChannelManager()
	al := &AgentLoop{cfg: config.DefaultConfig(), bus: messageBus, channelManager: manager}
	attachInteractionOutbox(t, al, messageBus, manager)
	workspace := t.TempDir()
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: interactions.KindApproval,
		Route: interactions.Route{
			AgentID: "main", SessionKey: "session-1", Channel: "slack", ChatID: "chat-1",
			SenderID: "user-1",
		},
		Origin: interactions.Origin{
			TurnID: "turn-1", ToolCallID: "call-1", ToolName: "protected",
			ArgumentHash:     strings.Repeat("a", 64),
			ExecutionContext: &bus.InboundContext{MessageID: "origin-message"},
		},
		ApprovalAction: "Run protected action", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)

	if _, err = (&humanInteractionRuntime{al: al}).publishPrompt(
		t.Context(),
		registry,
		workspace,
		record,
	); err != nil {
		t.Fatal(err)
	}
	prompt := <-manager.sent
	if prompt.ReplyToMessageID != "" ||
		prompt.Metadata.RequestID != "origin-message" ||
		!prompt.Metadata.IsApprovalPrompt() {
		t.Fatalf("non-Telegram approval prompt = %#v", prompt)
	}
}

func TestInteractionAnswerContentUsesTelegramApprovalButtonChoice(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Channels["tg1"] = &config.Channel{Enabled: true, Type: config.ChannelTelegram}
	al := &AgentLoop{cfg: cfg}
	record := interactions.Record{Kind: interactions.KindApproval}
	msg := bus.InboundMessage{
		Content: "[quoted assistant message]: approve?\n\nAllow once",
		Context: bus.InboundContext{
			Channel: "tg1", ReplyToMessageID: "prompt-1",
			Raw: map[string]string{
				bus.InboundMetadataKeyInteractionChoice: bus.InboundInteractionChoiceAllowOnce,
			},
		},
	}

	content := al.interactionAnswerContent(record, msg)
	if content != bus.InboundInteractionChoiceAllowOnce {
		t.Fatalf("interactionAnswerContent() = %q", content)
	}
	answer, err := parseInteractionAnswer(record, content, "answer-1")
	if err != nil || answer.Text != "allow_once" {
		t.Fatalf("parseInteractionAnswer() = (%#v, %v)", answer, err)
	}
}

func TestInteractionAnswerContentUsesCleanTelegramQuestionReply(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Channels["tg1"] = &config.Channel{Enabled: true, Type: config.ChannelTelegram}
	al := &AgentLoop{cfg: cfg}
	msg := bus.InboundMessage{
		Content: "[quoted assistant message]: What value?\n\ngenerate it yourself",
		Context: bus.InboundContext{
			Channel: "tg1", ReplyToMessageID: "prompt-1",
			Raw: map[string]string{
				bus.InboundMetadataKeyInteractionResponse: "generate it yourself",
			},
		},
	}

	if got := al.interactionAnswerContent(
		interactions.Record{Kind: interactions.KindQuestion}, msg,
	); got != "generate it yourself" {
		t.Fatalf("interactionAnswerContent() = %q", got)
	}
}

func TestInteractionAnswerContentIgnoresChoiceOutsideTelegramApprovalReply(t *testing.T) {
	cfg := config.DefaultConfig()
	al := &AgentLoop{cfg: cfg}
	record := interactions.Record{Kind: interactions.KindQuestion}
	msg := bus.InboundMessage{
		Content: "Allow once",
		Context: bus.InboundContext{
			Channel: "telegram", ReplyToMessageID: "prompt-1",
			Raw: map[string]string{
				bus.InboundMetadataKeyInteractionChoice: bus.InboundInteractionChoiceAllowOnce,
			},
		},
	}
	if got := al.interactionAnswerContent(record, msg); got != msg.Content {
		t.Fatalf("question interaction content = %q", got)
	}

	record.Kind = interactions.KindApproval
	msg.Context.Channel = "slack"
	if got := al.interactionAnswerContent(record, msg); got != msg.Content {
		t.Fatalf("non-Telegram interaction content = %q", got)
	}

	msg.Context.Channel = "telegram"
	msg.Context.ReplyToMessageID = ""
	if got := al.interactionAnswerContent(record, msg); got != msg.Content {
		t.Fatalf("non-reply interaction content = %q", got)
	}
}

func TestInteractionAnswerContentRejectsNonTelegramInstanceNamedTelegram(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Channels["telegram"] = &config.Channel{Enabled: true, Type: config.ChannelSlack}
	al := &AgentLoop{cfg: cfg}
	msg := bus.InboundMessage{
		Content: "[quoted assistant message]: approve?\n\nAllow once",
		Context: bus.InboundContext{
			Channel: "telegram", ReplyToMessageID: "prompt-1",
			Raw: map[string]string{
				bus.InboundMetadataKeyInteractionChoice: bus.InboundInteractionChoiceAllowOnce,
			},
		},
	}

	if got := al.interactionAnswerContent(
		interactions.Record{Kind: interactions.KindApproval},
		msg,
	); got != msg.Content {
		t.Fatalf("non-Telegram instance content = %q", got)
	}
}

func TestInteractionAnswerContentConcurrentConfigReload(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.Channels["tg1"] = &config.Channel{Enabled: true, Type: config.ChannelTelegram}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	defer al.Close()

	record := interactions.Record{Kind: interactions.KindApproval}
	msg := bus.InboundMessage{
		Content: "[quoted assistant message]: approve?\n\nAllow once",
		Context: bus.InboundContext{
			Channel: "tg1", ReplyToMessageID: "prompt-1",
			Raw: map[string]string{
				bus.InboundMetadataKeyInteractionChoice: bus.InboundInteractionChoiceAllowOnce,
			},
		},
	}

	reloadDone := make(chan error, 1)
	go func() {
		for i := 0; i < 10; i++ {
			reloaded := config.DefaultConfig()
			reloaded.Agents.Defaults.Workspace = cfg.Agents.Defaults.Workspace
			reloaded.Agents.Defaults.ContextManager = "none"
			reloaded.Agents.List = cfg.Agents.List
			reloaded.Channels["tg1"] = &config.Channel{
				Enabled: true,
				Type:    []string{config.ChannelTelegram, config.ChannelSlack}[i%2],
			}
			if err := al.ReloadProviderAndConfig(t.Context(), &mockProvider{}, reloaded); err != nil {
				reloadDone <- err
				return
			}
		}
		reloadDone <- nil
	}()

	for {
		select {
		case err := <-reloadDone:
			if err != nil {
				t.Fatalf("ReloadProviderAndConfig() error = %v", err)
			}
			return
		default:
			got := al.interactionAnswerContent(record, msg)
			if got != msg.Content && got != bus.InboundInteractionChoiceAllowOnce {
				t.Fatalf("interactionAnswerContent() = %q", got)
			}
		}
	}
}

func TestInteractionEventsDoNotProjectOwningTaskState(t *testing.T) {
	workspace := t.TempDir()
	al := &AgentLoop{cfg: config.DefaultConfig()}
	ensureTestTurnRunner(al)
	tasks := al.taskRegistryForWorkspace(workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: "task-1", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
	}); err != nil {
		t.Fatal(err)
	}
	record := interactions.Record{
		ID: "interaction-1", ShortID: "abc123", Status: interactions.StatusWaiting,
		PromptSummary: "Choose a deployment mode",
		Origin:        interactions.Origin{TaskID: "task-1"},
	}
	al.observeInteractionEvent(workspace, interactions.EventObservation{
		Event: interactions.Event{Type: interactions.EventWaiting}, Record: record,
	})
	task, _ := tasks.Get("task-1")
	if task.Status != taskregistry.StatusRunning || task.ProgressSummary != "" {
		t.Fatalf("interaction event mutated task = %#v", task)
	}

	record.Status = interactions.StatusClaimed
	al.observeInteractionEvent(workspace, interactions.EventObservation{
		Event: interactions.Event{Type: interactions.EventAnswerClaimed}, Record: record,
	})
	task, _ = tasks.Get("task-1")
	if task.Status != taskregistry.StatusRunning || task.ProgressSummary != "" {
		t.Fatalf("answer event mutated task = %#v", task)
	}

	record.Status = interactions.StatusFailed
	record.FailureDetail = "continuation failed"
	al.observeInteractionEvent(workspace, interactions.EventObservation{
		Event: interactions.Event{Type: interactions.EventFailed}, Record: record,
	})
	task, _ = tasks.Get("task-1")
	if task.Status != taskregistry.StatusRunning || task.Error != "" {
		t.Fatalf("failure event mutated task = %#v", task)
	}
}

func TestInteractionResolutionCallbackRunsOnceForTerminalHumanOutcome(t *testing.T) {
	for _, test := range []struct {
		name    string
		event   interactions.EventType
		outcome interactions.Outcome
	}{
		{name: "answered", event: interactions.EventAnswerClaimed, outcome: interactions.OutcomeAnswered},
		{name: "timed out", event: interactions.EventAnswerClaimed, outcome: interactions.OutcomeTimedOut},
		{name: "canceled", event: interactions.EventCancelled, outcome: interactions.OutcomeCanceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			al := &AgentLoop{cfg: config.DefaultConfig()}
			called := make(chan interactions.Outcome, 1)
			al.interactions.resolutions.Store(
				"interaction-browser",
				func(_ context.Context, outcome interactions.Outcome) error {
					called <- outcome
					return nil
				},
			)
			observation := interactions.EventObservation{
				Event:  interactions.Event{Type: test.event},
				Record: interactions.Record{ID: "interaction-browser", Outcome: test.outcome},
			}
			al.observeInteractionEvent(t.TempDir(), observation)
			if got := <-called; got != test.outcome {
				t.Fatalf("resolution outcome = %q, want %q", got, test.outcome)
			}
			al.observeInteractionEvent(t.TempDir(), observation)
			select {
			case duplicate := <-called:
				t.Fatalf("duplicate resolution = %q", duplicate)
			default:
			}
		})
	}
}

func TestTaskInteractionFinalHonorsParentOnlyDelivery(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	coordinator := installInteractionChannelManager(t, al, manager)
	workspace := agent.Workspace
	tasks := al.taskRegistryForWorkspace(workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: "subagent-parent", Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "finish in parent", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
		DeliveryMode:   string(toolshared.AsyncDeliveryParentOnly),
		Channel:        "telegram", ChatID: "chat-1", RequesterSessionKey: "owner-session",
	}); err != nil {
		t.Fatal(err)
	}
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		ID: "interaction-parent", Kind: interactions.KindQuestion,
		Route: interactions.Route{
			AgentID: agent.ID, SessionKey: "owner-session", RouteSessionKey: "route-owner",
			Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
		},
		Origin: interactions.Origin{
			TurnID: "turn-task", ToolCallID: "call-task", ToolName: "request_user_input",
			TaskID: "subagent-parent", ContinuationSessionKey: "task-session",
		},
		Questions: []interactions.Question{{ID: "confirm", Question: "Proceed?"}},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if al.interactionContinuationExpectsUserDelivery(workspace, record) {
		t.Fatal("parent-only interaction must not wait for user delivery settlement")
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "yes", Values: map[string]string{"confirm": "yes"},
		MessageID: "question-answer", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	inbound := bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
	}
	resumedDeliverable := &taskresult.Deliverable{
		Text: "tool-owned child result",
		Artifacts: []taskresult.Artifact{{
			Ref:       "file:/tmp/report.txt",
			LocalPath: "/tmp/report.txt",
			Kind:      "file",
		}},
		Metadata: map[string]string{"producer": "resumed-tool"},
		Report: &taskresult.Report{
			SchemaVersion: taskresult.ReportSchemaV1,
			ReportID:      "resume-report",
		},
	}
	if err := al.deliverTaskInteractionFinal(
		t.Context(), registry, workspace, record, inbound, "raw child final", resumedDeliverable, nil,
	); err != nil {
		t.Fatalf("deliverTaskInteractionFinal() error = %v", err)
	}
	select {
	case acknowledgement := <-manager.sent:
		metadata := acknowledgement.Metadata
		if acknowledgement.Content == "raw child final" ||
			acknowledgement.ReplyToMessageID != "question-answer" ||
			!metadata.RemovesInteractionControls() ||
			metadata.InteractionKind != bus.OutboundInteractionQuestion {
			t.Fatalf("question control acknowledgement = %#v", acknowledgement)
		}
	case <-time.After(time.Second):
		t.Fatal("parent-only question did not remove Telegram controls")
	}
	task, _ := tasks.Get("subagent-parent")
	if task.Status != taskregistry.StatusSucceeded ||
		task.DeliveryStatus != taskregistry.DeliverySessionQueued {
		t.Fatalf("parent-only task = %#v", task)
	}
	if task.Deliverable == nil || task.Deliverable.Text != "tool-owned child result" ||
		len(task.Deliverable.Artifacts) != 1 || task.Deliverable.Artifacts[0].Ref != "file:/tmp/report.txt" ||
		task.Deliverable.Metadata["producer"] != "resumed-tool" ||
		task.Deliverable.Report == nil || task.Deliverable.Report.ReportID != "resume-report" {
		t.Fatalf("parent-only task lost resumed deliverable: %#v", task.Deliverable)
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.Status != interactions.StatusResolved || len(resolved.FinalDeliveryIDs) != 2 {
		t.Fatalf("interaction after parent handoff = %#v", resolved)
	}
	for _, deliveryID := range resolved.FinalDeliveryIDs {
		intent, getErr := coordinator.Get(deliveryID)
		if getErr != nil || intent.Status != outbox.StatusDelivered {
			t.Fatalf("parent delivery %q = (%+v, %v)", deliveryID, intent, getErr)
		}
	}
	events := registry.ListEvents(record.ID)
	boundAt, resolvedAt := -1, -1
	for i, event := range events {
		switch event.Type {
		case interactions.EventFinalDelivery:
			boundAt = i
		case interactions.EventResolved:
			resolvedAt = i
		}
	}
	if boundAt < 0 || resolvedAt <= boundAt {
		t.Fatalf("task delivery was not bound before resolution: %#v", events)
	}
	select {
	case outbound := <-manager.sent:
		metadata := outbound.Metadata
		if strings.TrimSpace(outbound.Content) == "" || outbound.Content == "raw child final" ||
			metadata.OutboundKind != bus.OutboundKindFinal ||
			metadata.MessageKind != bus.OutboundMessageKindFinalReply {
			t.Fatalf("parent completion = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("parent completion was not delivered")
	}
}

func TestTaskInteractionParentFinalRetriesAfterDefiniteTransportFailure(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	manager.setSendError(channels.DefiniteNotSentDeliveryError(errors.New("worker unavailable")))
	coordinator := installInteractionChannelManager(t, al, manager)
	workspace := agent.Workspace
	tasks := al.taskRegistryForWorkspace(workspace)
	const taskID = "subagent-parent-retry"
	ownerSession := session.BuildOpaqueSessionKey("agent:main:test:parent-final-retry-owner")
	continuationSession := session.BuildOpaqueSessionKey("agent:main:test:parent-final-retry-child")
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: taskID, Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "retry in parent", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
		DeliveryMode:   string(toolshared.AsyncDeliveryParentOnly),
		Channel:        "discord", ChatID: "chat-1", RequesterSessionKey: ownerSession,
	}); err != nil {
		t.Fatal(err)
	}
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		ID: "interaction-parent-retry", Kind: interactions.KindQuestion,
		Route: interactions.Route{
			AgentID: agent.ID, SessionKey: ownerSession, RouteSessionKey: "route-owner",
			Channel: "discord", ChatID: "chat-1", SenderID: "user-1",
		},
		Origin: interactions.Origin{
			TurnID: "turn-task-retry", ToolCallID: "call-task-retry", ToolName: "request_user_input",
			TaskID: taskID, ContinuationSessionKey: continuationSession,
		},
		Questions: []interactions.Question{{ID: "confirm", Question: "Proceed?"}},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "yes", Values: map[string]string{"confirm": "yes"}, ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	agent.Sessions.AddFullMessage(continuationSession, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-task-retry"}},
	})
	if err = al.ensureInteractionToolResult(t.Context(), agent, record); err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	agent.Sessions.AddFullMessage(continuationSession, providers.Message{
		Role: "assistant", Content: "raw child final",
		Deliverable: &taskresult.Deliverable{Text: "canonical child result"},
	})
	inbound := bus.InboundContext{Channel: "discord", ChatID: "chat-1", SenderID: "user-1"}
	err = al.deliverTaskInteractionFinal(
		t.Context(), registry, workspace, record, inbound, "raw child final",
		&taskresult.Deliverable{Text: "canonical child result"}, nil,
	)
	if err == nil {
		t.Fatal("definitely-not-sent parent final unexpectedly succeeded")
	}
	task, _ := tasks.Get(taskID)
	if task.Status != taskregistry.StatusSucceeded || task.DeliveryStatus != taskregistry.DeliveryPending ||
		task.LastCompletionID != "interaction:"+record.ID {
		t.Fatalf("task after failed parent transport = %+v", task)
	}
	active, _ := registry.Get(record.ID)
	if active.Status != interactions.StatusResuming || len(active.FinalDeliveryIDs) != 1 {
		t.Fatalf("interaction after failed parent transport = %+v", active)
	}
	intent, err := coordinator.Get(active.FinalDeliveryIDs[0])
	if err != nil || intent.Status != outbox.StatusDefinitelyFailed || intent.Attempts != 1 {
		t.Fatalf("failed parent intent = (%+v, %v)", intent, err)
	}
	// The canonical outbox failure must remain replayable even if the task read
	// model is stale and already claims success.
	if err = tasks.Update(taskID, func(record *taskregistry.Record) {
		record.DeliveryStatus = taskregistry.DeliverySessionQueued
	}); err != nil {
		t.Fatal(err)
	}

	manager.setSendError(nil)
	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	resolved, _ := registry.Get(record.ID)
	task, _ = tasks.Get(taskID)
	intent, err = coordinator.Get(active.FinalDeliveryIDs[0])
	if resolved.Status != interactions.StatusResolved ||
		task.DeliveryStatus != taskregistry.DeliverySessionQueued ||
		err != nil || intent.Status != outbox.StatusDelivered || intent.Attempts != 2 {
		t.Fatalf(
			"recovered parent delivery = interaction %+v, task %+v, intent %+v, err %v",
			resolved,
			task,
			intent,
			err,
		)
	}
	select {
	case outbound := <-manager.sent:
		if strings.TrimSpace(outbound.Content) == "" || outbound.Content == "raw child final" {
			t.Fatalf("retried parent completion = %+v", outbound)
		}
	default:
		t.Fatal("retried parent completion was not delivered")
	}
}

func TestInteractionResponseReplyTargetUsesPersistedCallbackMessage(t *testing.T) {
	record := interactions.Record{
		Kind:  interactions.KindQuestion,
		Route: interactions.Route{Channel: "telegram"},
		Answer: &interactions.Answer{
			MessageID:         "10698106213006357",
			ResponseMessageID: "7716",
		},
	}
	inbound := bus.InboundContext{
		Channel: "telegram", MessageID: "10698106213006357", ReplyToMessageID: "7716",
	}

	if got := interactionResponseReplyTarget(record, inbound); got != "7716" {
		t.Fatalf("callback final reply target = %q, want original Telegram message 7716", got)
	}
	if got := interactionResponseReplyTarget(record, bus.InboundContext{}); got != "7716" {
		t.Fatalf("recovered callback final reply target = %q, want persisted Telegram message 7716", got)
	}
}

func TestProjectedInteractionCallbackPersistsFinalReplyTarget(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "Canary",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:callback-reply-target"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
			MessageID: "10698106213006357", ReplyToMessageID: "7716",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, msg, "")
	msg.Context.Raw = map[string]string{
		bus.InboundMetadataKeyInteractionResponse:          "Canary",
		bus.InboundMetadataKeyInteractionShortID:           record.ShortID,
		bus.InboundMetadataKeyInteractionResponseMessageID: "7716",
	}

	newInboundTurnCoordinator(al).handleInteractionInbound(t.Context(), msg, target)

	select {
	case final := <-manager.sent:
		if final.ReplyToMessageID != "7716" {
			t.Fatalf("callback final reply target = %q, want original Telegram message 7716", final.ReplyToMessageID)
		}
	case <-time.After(time.Second):
		t.Fatal("callback continuation final was not delivered")
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	deadline := time.Now().Add(time.Second)
	resolved, _ := registry.Get(record.ID)
	for resolved.Status != interactions.StatusResolved && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		resolved, _ = registry.Get(record.ID)
	}
	if resolved.Status != interactions.StatusResolved || resolved.Answer == nil ||
		resolved.Answer.MessageID != "10698106213006357" || resolved.Answer.ResponseMessageID != "7716" {
		t.Fatalf("persisted callback answer = %#v", resolved)
	}
}

func TestParentOnlyTaskApprovalDeliversOnlyParentResult(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	coordinator := installInteractionChannelManager(t, al, manager)
	workspace := agent.Workspace
	tasks := al.taskRegistryForWorkspace(workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: "approval-parent", Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "finish approval in parent", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
		DeliveryMode:   string(toolshared.AsyncDeliveryParentOnly),
		Channel:        "telegram", ChatID: "chat-1", RequesterSessionKey: "owner-session",
	}); err != nil {
		t.Fatal(err)
	}
	argumentHash := strings.Repeat("a", 64)
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		ID: "interaction-approval-parent", Kind: interactions.KindApproval,
		Route: interactions.Route{
			AgentID: agent.ID, SessionKey: "owner-session", RouteSessionKey: "route-owner",
			Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
		},
		Origin: interactions.Origin{
			TurnID: "turn-task", ToolCallID: "call-task", ToolName: "protected",
			TaskID: "approval-parent", ContinuationSessionKey: "task-session",
			ArgumentHash: argumentHash,
			ExecutionContext: &bus.InboundContext{
				Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", MessageID: "origin-message",
			},
		},
		PromptSummary:  "Approve protected task action",
		ApprovalAction: "Run protected task action",
		ExpiresAt:      time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "typed-fallback-answer", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.ConsumeApproval(
		record.ID, record.Revision, record.Origin.ToolCallID, record.Origin.ToolName, argumentHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := al.deliverInteractionFinal(
		t.Context(), registry, workspace, record,
		bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", MessageID: "recovery-message",
		},
		"raw child final", nil, nil,
	); err != nil {
		t.Fatal(err)
	}

	select {
	case outbound := <-manager.sent:
		metadata := outbound.Metadata
		if strings.TrimSpace(outbound.Content) == "" ||
			outbound.Content == "raw child final" ||
			outbound.Content == "Response recorded." ||
			metadata.OutboundKind != bus.OutboundKindFinal ||
			metadata.MessageKind != bus.OutboundMessageKindFinalReply {
			t.Fatalf("parent approval completion = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("parent-only approval completion was not delivered")
	}
	select {
	case extra := <-manager.sent:
		t.Fatalf("parent-only approval delivered an extra message: %#v", extra)
	default:
	}
	task, _ := tasks.Get("approval-parent")
	if task.Status != taskregistry.StatusSucceeded ||
		task.DeliveryStatus != taskregistry.DeliverySessionQueued {
		t.Fatalf("parent-only approval task = %#v", task)
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.Status != interactions.StatusResolved || len(resolved.FinalDeliveryIDs) != 1 {
		t.Fatalf("parent-only approval interaction = %#v", resolved)
	}
	for _, deliveryID := range resolved.FinalDeliveryIDs {
		intent, err := coordinator.Get(deliveryID)
		if err != nil || intent.Status != outbox.StatusDelivered || intent.Identity.Ordinal != 0 {
			t.Fatalf("approval delivery %q = (%+v, %v)", deliveryID, intent, err)
		}
	}
	select {
	case outbound := <-al.bus.(*bus.MessageBus).OutboundChan():
		if outbound.Content == "raw child final" {
			t.Fatalf("parent-only approval leaked raw child final: %#v", outbound)
		}
	default:
	}
}

func TestTaskInteractionFinalCarriesResumeScopeToUserDelivery(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	workspace := agent.Workspace
	tasks := al.taskRegistryForWorkspace(workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: "subagent-user", Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "finish for user", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
		DeliveryMode:   string(toolshared.AsyncDeliveryUserOnly),
		Channel:        "telegram", ChatID: "chat-1", RequesterSessionKey: "owner-session",
	}); err != nil {
		t.Fatal(err)
	}
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		ID: "interaction-user", Kind: interactions.KindQuestion,
		Route: interactions.Route{
			AgentID: agent.ID, SessionKey: "owner-session", RouteSessionKey: "route-owner",
			Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
		},
		Origin: interactions.Origin{
			TurnID: "turn-task", ToolCallID: "call-task", ToolName: "request_user_input",
			TaskID: "subagent-user", ContinuationSessionKey: "task-session",
		},
		Questions: []interactions.Question{{ID: "confirm", Question: "Proceed?"}},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "yes", Values: map[string]string{"confirm": "yes"}, ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !al.interactionContinuationExpectsUserDelivery(workspace, record) {
		t.Fatal("user-only interaction must wait for user delivery settlement")
	}
	traceScope := runtimeevents.NewTraceScope(workspace, "resume-turn")
	objectiveOutcome := &taskresult.Outcome{
		Status:         taskresult.OutcomePartial,
		CompletedItems: []taskresult.Item{{Item: "Yakima published", Kind: "external_action"}},
		MissingItems:   []string{"Vissani not published"},
	}
	projection := objectiveOutcomeUserContent("Both items were published.", objectiveOutcome)
	if err := al.deliverTaskInteractionFinal(
		t.Context(), registry, workspace, record,
		bus.InboundContext{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1"},
		"Both items were published.",
		&taskresult.Deliverable{ObjectiveOutcome: objectiveOutcome},
		[]runtimeevents.TraceScope{traceScope},
	); err != nil {
		t.Fatalf("deliverTaskInteractionFinal() error = %v", err)
	}
	select {
	case outbound := <-manager.sent:
		if outbound.Content != projection || strings.Contains(outbound.Content, "Both items") ||
			!outbound.TraceSettlement ||
			len(outbound.TraceScopes) != 1 ||
			outbound.TraceScopes[0] != traceScope {
			t.Fatalf("task user delivery = %#v", outbound)
		}
		metadata := outbound.Metadata
		if metadata.OutboundKind != bus.OutboundKindFinal ||
			metadata.MessageKind != bus.OutboundMessageKindFinalReply {
			t.Fatalf("task user delivery metadata = %#v", metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("user-only task completion was not queued")
	}
	task, _ := tasks.Get("subagent-user")
	if task.TerminalSummary != projection || task.Deliverable == nil || task.Deliverable.Text != projection ||
		strings.Contains(task.TerminalSummary, "Both items") {
		t.Fatalf("task retained optimistic resume projection: %#v", task)
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.Status != interactions.StatusResolved || len(resolved.FinalDeliveryIDs) == 0 {
		t.Fatalf("interaction after user delivery = %#v", resolved)
	}
}

func TestNewAgentLoopRegistersRequestUserInputByDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleConvProvider{})
	defer al.Close()
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil || !agent.Tools.HasRegistered("request_user_input") {
		t.Fatal("request_user_input is not registered by default")
	}
	if _, ok := al.interactions.registries.Load(agent.Workspace); !ok {
		t.Fatal("interaction registry was not initialized for recovery")
	}
}

func TestDisabledRequestUserInputStillInitializesRecoveryRegistry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.RequestUserInput.Enabled = false
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleConvProvider{})
	defer al.Close()
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("missing default agent")
	}
	if agent.Tools.HasRegistered("request_user_input") {
		t.Fatal("disabled request_user_input was registered")
	}
	if _, ok := al.interactions.registries.Load(agent.Workspace); !ok {
		t.Fatal("disabled tool prevented durable interaction recovery")
	}
}

func TestHumanInteractionPromptFailureRemainsAmbiguousAndDoesNotRetry(t *testing.T) {
	messageBus := bus.NewMessageBus()
	manager := newInteractionChannelManager()
	manager.setSendError(errors.New("delivery failed"))
	al := &AgentLoop{cfg: config.DefaultConfig(), bus: messageBus, channelManager: manager}
	coordinator := attachInteractionOutbox(t, al, messageBus, manager)
	workspace := t.TempDir()
	disposition, err := (&humanInteractionRuntime{al: al, coordinator: &al.interactions}).SuspendToolCall(
		t.Context(),
		testToolSuspensionRequest(workspace),
	)
	if err == nil || !disposition.Durable {
		t.Fatalf("SuspendToolCall() = (%#v, %v), want durable delivery error", disposition, err)
	}
	record, _ := al.interactionRegistryForWorkspace(workspace).Get(disposition.InteractionID)
	intent, getErr := coordinator.Get(record.PromptDeliveryID)
	if getErr != nil || record.Status != interactions.StatusCreated ||
		intent.Status != outbox.StatusAmbiguous || intent.Attempts != 1 {
		t.Fatalf("record after failed delivery = %#v", record)
	}

	manager.setSendError(nil)
	if _, duplicateIntent, retryErr := (&humanInteractionRuntime{al: al}).deliverPrompt(
		t.Context(),
		al.interactionRegistryForWorkspace(workspace),
		workspace,
		record,
	); retryErr == nil || duplicateIntent.Status != outbox.StatusAmbiguous {
		t.Fatalf("ambiguous retry = (%#v, %v)", duplicateIntent, retryErr)
	}
	select {
	case duplicate := <-manager.sent:
		t.Fatalf("ambiguous prompt was duplicated: %#v", duplicate)
	default:
	}
}

func TestHumanInteractionDefiniteNotSentPromptRetries(t *testing.T) {
	messageBus := bus.NewMessageBus()
	manager := newInteractionChannelManager()
	manager.setSendError(channels.DefiniteNotSentDeliveryError(errors.New("worker unavailable")))
	al := &AgentLoop{cfg: config.DefaultConfig(), bus: messageBus, channelManager: manager}
	coordinator := attachInteractionOutbox(t, al, messageBus, manager)
	workspace := t.TempDir()
	disposition, err := (&humanInteractionRuntime{al: al, coordinator: &al.interactions}).SuspendToolCall(
		t.Context(),
		testToolSuspensionRequest(workspace),
	)
	if err == nil || !disposition.Durable {
		t.Fatalf("SuspendToolCall() = (%#v, %v), want durable not-sent error", disposition, err)
	}
	registry := al.interactionRegistryForWorkspace(workspace)
	record, _ := registry.Get(disposition.InteractionID)
	failed, getErr := coordinator.Get(record.PromptDeliveryID)
	if getErr != nil || failed.Status != outbox.StatusDefinitelyFailed || failed.Attempts != 1 {
		t.Fatalf("definite failure = (%#v, %v)", failed, getErr)
	}

	manager.setSendError(nil)
	record, delivered, retryErr := (&humanInteractionRuntime{al: al}).deliverPrompt(
		t.Context(), registry, workspace, record,
	)
	if retryErr != nil || delivered.Status != outbox.StatusDelivered || delivered.Attempts != 2 {
		t.Fatalf("retry result = (%#v, %v)", delivered, retryErr)
	}
	if record.Status != interactions.StatusWaiting || record.PromptDeliveryID != delivered.ID {
		t.Fatalf("record after definite retry = %#v", record)
	}
	select {
	case <-manager.sent:
	default:
		t.Fatal("retry did not send the prompt")
	}
}

func TestRecoveryFailsInteractionAfterFinalDeliveryRetryBudget(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	workspace := agent.Workspace
	registry := al.interactionRegistryForWorkspace(workspace)
	coordinator := openTestInteractionOutbox(t, al)
	request := testToolSuspensionRequest(workspace)
	request.Route.AgentID = agent.ID
	request.Origin.TaskID = "task-final-delivery-budget"
	const interactionID = "interaction_final_budget"
	tasks := al.taskRegistryForWorkspace(workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: request.Origin.TaskID, Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
	}); err != nil {
		t.Fatal(err)
	}
	record, err := registry.Create(interactions.CreateRequest{
		ID:   interactionID,
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, PromptSummary: request.Prompt.PromptSummary,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(
		record.ID,
		record.Revision,
		interactions.Answer{Text: "continue", ReceivedAt: time.Now().UnixMilli()},
		interactions.OutcomeAnswered,
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = tasks.Complete(
		request.Origin.TaskID,
		"task result that could not be delivered",
		&taskresult.Deliverable{Text: "task result that could not be delivered"},
		taskregistry.DeliveryPending,
	); err != nil {
		t.Fatal(err)
	}
	record, _ = seedTestInteractionFinalOutcome(
		t,
		coordinator,
		registry,
		workspace,
		record,
		outbox.StatusDefinitelyFailed,
		outbox.MaxDeliveryAttempts,
	)
	stateDir := filepath.Dir(taskregistry.WorkspaceStorePath(workspace))
	if err = os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })
	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 0 {
		t.Fatalf("recovery with unwritable task store = %d, want 0", recovered)
	}
	nonterminal, ok := registry.Get(record.ID)
	if !ok || nonterminal.Status != interactions.StatusResuming {
		t.Fatalf("interaction after failed task settlement = %#v, found=%t", nonterminal, ok)
	}
	unprojectedTask, ok := tasks.Get(request.Origin.TaskID)
	if !ok || unprojectedTask.Status != taskregistry.StatusSucceeded {
		t.Fatalf("task after failed settlement = %#v, found=%t", unprojectedTask, ok)
	}
	if err = os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	al.tasks.registries.Delete(workspace)
	al.interactions.registries.Delete(workspace)
	registry = al.interactionRegistryForWorkspace(workspace)
	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	failed, ok := registry.Get(record.ID)
	if !ok || failed.Status != interactions.StatusFailed ||
		failed.FailureCode != "final_delivery_exhausted" {
		t.Fatalf("interaction after exhausted recovery = %#v, found=%t", failed, ok)
	}
	reloadedInteractions := interactions.NewRegistry(interactions.WorkspaceStorePath(workspace))
	if err = reloadedInteractions.LastLoadError(); err != nil {
		t.Fatalf("reload interactions: %v", err)
	}
	reloadedInteraction, ok := reloadedInteractions.Get(record.ID)
	if !ok || reloadedInteraction.Status != interactions.StatusFailed {
		t.Fatalf("reloaded interaction = %#v, found=%t", reloadedInteraction, ok)
	}
	reloadedTasks := taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(workspace))
	if err = reloadedTasks.LastLoadError(); err != nil {
		t.Fatalf("reload tasks: %v", err)
	}
	reloadedTask, ok := reloadedTasks.Get(request.Origin.TaskID)
	if !ok || reloadedTask.Status != taskregistry.StatusFailed ||
		reloadedTask.DeliveryStatus != taskregistry.DeliveryFailed {
		t.Fatalf("reloaded task = %#v, found=%t", reloadedTask, ok)
	}
}

func TestRecoveryFailsInteractionAfterPromptDeliveryRetryBudget(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	workspace := agent.Workspace
	registry := al.interactionRegistryForWorkspace(workspace)
	request := testToolSuspensionRequest(workspace)
	request.Route.AgentID = agent.ID
	request.Origin.TaskID = "task-prompt-delivery-budget"
	const interactionID = "interaction_prompt_budget"
	tasks := al.taskRegistryForWorkspace(workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: request.Origin.TaskID, Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
	}); err != nil {
		t.Fatal(err)
	}
	agent.Sessions.AddFullMessage(request.Route.SessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: request.Origin.ToolCallID, Name: request.Origin.ToolName,
		}},
	})
	record, err := registry.Create(interactions.CreateRequest{
		ID:   interactionID,
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, PromptSummary: request.Prompt.PromptSummary,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := openTestInteractionOutbox(t, al)
	record = bindTestInteractionPrompt(t, registry, record)
	seedTestInteractionPromptOutcome(
		t, coordinator, workspace, record, outbox.StatusDefinitelyFailed, outbox.MaxDeliveryAttempts,
	)
	stateDir := filepath.Dir(taskregistry.WorkspaceStorePath(workspace))
	if err = os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })
	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 0 {
		t.Fatalf("recovery with unwritable task store = %d, want 0", recovered)
	}
	nonterminal, ok := registry.Get(record.ID)
	if !ok || nonterminal.Status != interactions.StatusCreated {
		t.Fatalf("interaction after failed task settlement = %#v, found=%t", nonterminal, ok)
	}
	unprojectedTask, ok := tasks.Get(request.Origin.TaskID)
	if !ok || unprojectedTask.Status != taskregistry.StatusRunning {
		t.Fatalf("task after failed settlement = %#v, found=%t", unprojectedTask, ok)
	}
	if err = os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	al.tasks.registries.Delete(workspace)
	al.interactions.registries.Delete(workspace)
	_ = al.taskRegistryForWorkspace(workspace)
	registry = al.interactionRegistryForWorkspace(workspace)
	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	failed, ok := registry.Get(record.ID)
	if !ok || failed.Status != interactions.StatusFailed ||
		failed.FailureCode != "prompt_delivery_exhausted" {
		t.Fatalf("interaction after exhausted recovery = %#v, found=%t", failed, ok)
	}
	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 0 {
		t.Fatalf("second RecoverHumanInteractions() = %d, want 0", recovered)
	}
	history := agent.Sessions.GetHistory(request.Route.SessionKey)
	if got := countInteractionToolResults(history, request.Origin.ToolCallID); got != 1 {
		t.Fatalf("terminal prompt tool results = %d, want exactly 1", got)
	}
	reloaded := interactions.NewRegistry(interactions.WorkspaceStorePath(workspace))
	if err = reloaded.LastLoadError(); err != nil {
		t.Fatalf("reload interaction: %v", err)
	}
	reloadedRecord, ok := reloaded.Get(record.ID)
	if !ok || reloadedRecord.Status != interactions.StatusFailed ||
		reloadedRecord.FailureCode != "prompt_delivery_exhausted" {
		t.Fatalf("reloaded prompt interaction = %#v, found=%t", reloadedRecord, ok)
	}
}

func TestRecoveryDoesNotResendPromptAfterAmbiguousCrashWindow(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	coordinator := installInteractionChannelManager(t, al, manager)
	sessionKey := "session-ambiguous-prompt"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "user", Content: "Deploy this"})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-question", Name: "request_user_input", Arguments: map[string]any{},
		}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	seedTestInteractionPromptOutcomeWithMessages(
		t,
		coordinator,
		agent.Workspace,
		record,
		outbox.StatusAmbiguous,
		1,
		[]string{"", "7716"},
	)

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusResolved ||
		record.Outcome != interactions.OutcomeDeliveryUnknown {
		t.Fatalf("record after ambiguous prompt recovery = %#v", record)
	}
	select {
	case synced := <-manager.synced:
		if synced.ReplyToMessageID != "7716" || !synced.Metadata.RemovesInteractionControls() {
			t.Fatalf("ambiguous prompt control cleanup = %#v", synced)
		}
	case <-time.After(time.Second):
		t.Fatal("ambiguous prompt recovery did not clear its confirmed Telegram controls")
	}
	select {
	case outbound := <-manager.sent:
		if strings.Contains(outbound.Content, "Input needed") {
			t.Fatalf("recovery resent ambiguous prompt: %#v", outbound)
		}
	default:
		t.Fatal("recovery did not deliver the delivery-unknown continuation")
	}
	select {
	case duplicate := <-manager.sent:
		t.Fatalf("recovery emitted a duplicate message: %#v", duplicate)
	default:
	}
}

func TestRecoveryDoesNotResendAmbiguousFinal(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	coordinator := installInteractionChannelManager(t, al, manager)
	sessionKey := "session-ambiguous-final"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-question"}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, _ = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", Values: map[string]string{"deploy_mode": "Canary"},
	}, interactions.OutcomeAnswered)
	if ensureErr := al.ensureInteractionToolResult(t.Context(), agent, record); ensureErr != nil {
		t.Fatal(ensureErr)
	}
	record, _ = registry.MarkResuming(record.ID, record.Revision)
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "assistant", Content: "Final response"})
	record, _ = seedTestInteractionFinalOutcome(
		t, coordinator, registry, agent.Workspace, record, outbox.StatusAmbiguous, 1,
	)

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusFailed || record.FailureCode != "final_delivery_ambiguous" {
		t.Fatalf("record after ambiguous final recovery = %#v", record)
	}
	select {
	case duplicate := <-manager.sent:
		t.Fatalf("recovery resent ambiguous final: %#v", duplicate)
	default:
	}
}

func TestRecoveryDoesNotFailActiveFinalDelivery(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	manager.sendStarted = make(chan struct{}, 1)
	manager.sendRelease = make(chan struct{})
	coordinator := installInteractionChannelManager(t, al, manager)
	sessionKey := "session-active-final"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-question"}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, _ = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", Values: map[string]string{"deploy_mode": "Canary"},
	}, interactions.OutcomeAnswered)
	if err = al.ensureInteractionToolResult(t.Context(), agent, record); err != nil {
		t.Fatal(err)
	}
	record, _ = registry.MarkResuming(record.ID, record.Revision)
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", Content: "Final response",
	})
	flightKey, recoveryFlight, owner := al.startInteractionResumeFlight(
		agent.Workspace, record.ID,
	)
	if !owner {
		t.Fatal("failed to simulate recovery ownership")
	}
	resumeDone := make(chan error, 1)
	go func() {
		resumeDone <- al.resumeClaimedInteraction(
			t.Context(), registry, agent.Workspace, agent,
			&session.SessionScope{
				Version: session.ScopeVersion, AgentID: agent.ID, Channel: record.Route.Channel,
				RouteScopeKey: record.Route.RouteSessionKey,
			},
			inboundContextForInteraction(record.Route), record,
		)
	}()
	select {
	case <-manager.sendStarted:
		t.Fatal("live continuation bypassed recovery ownership")
	case <-time.After(50 * time.Millisecond):
	}
	al.finishInteractionResumeFlight(flightKey, recoveryFlight, false, nil)
	select {
	case <-manager.sendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("active final delivery did not start")
	}
	active, _ := registry.Get(record.ID)
	if active.Status != interactions.StatusResuming || len(active.FinalDeliveryIDs) != 1 {
		t.Fatalf("active final delivery = %#v", active)
	}
	intent, getErr := coordinator.Get(active.FinalDeliveryIDs[0])
	if getErr != nil || intent.Status != outbox.StatusAttempting {
		t.Fatalf("active outbox delivery = (%#v, %v)", intent, getErr)
	}
	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 0 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 0 while live owner is sending", recovered)
	}
	active, _ = registry.Get(record.ID)
	if active.Status != interactions.StatusResuming || len(active.FinalDeliveryIDs) != 1 {
		t.Fatalf("recovery changed active final delivery = %#v", active)
	}
	close(manager.sendRelease)
	select {
	case err = <-resumeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active final delivery did not finish")
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.Status != interactions.StatusResolved || len(resolved.FinalDeliveryIDs) != 1 {
		t.Fatalf("resolved interaction = %#v", resolved)
	}
}

func TestRecoveryRetriesDefinitelyNotSentFinal(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	coordinator := installInteractionChannelManager(t, al, manager)
	sessionKey := "session-not-sent-final"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-question"}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, _ = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", Values: map[string]string{"deploy_mode": "Canary"},
	}, interactions.OutcomeAnswered)
	if ensureErr := al.ensureInteractionToolResult(t.Context(), agent, record); ensureErr != nil {
		t.Fatal(ensureErr)
	}
	record, _ = registry.MarkResuming(record.ID, record.Revision)
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "assistant", Content: "Final response"})
	record, _ = seedTestInteractionFinalOutcome(
		t, coordinator, registry, agent.Workspace, record, outbox.StatusDefinitelyFailed, 1,
	)

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusResolved || len(record.FinalDeliveryIDs) != 1 {
		t.Fatalf("record after not-sent final recovery = %#v", record)
	}
	select {
	case outbound := <-manager.sent:
		if outbound.Content != "final response" {
			t.Fatalf("retried final = %#v", outbound)
		}
	default:
		t.Fatal("definitely not-sent final was not retried")
	}
}

func TestRecoveryRetriesReleasedPendingFinalWithoutRestart(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	coordinator := installInteractionChannelManager(t, al, manager)
	sessionKey := "session-released-pending-final"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-question"}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", Values: map[string]string{"deploy_mode": "Canary"},
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	if err = al.ensureInteractionToolResult(t.Context(), agent, record); err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "assistant", Content: "regenerated final"})
	record = bindTestInteractionFinal(t, registry, record)
	admission, err := coordinator.AdmitMessage(
		agent.Workspace,
		testInteractionFinalIdentity(record),
		bus.OutboundMessage{
			Channel: record.Route.Channel, ChatID: record.Route.ChatID,
			SessionKey: record.Route.SessionKey, Content: "canonical released final",
		},
	)
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMessage() = %+v, %v", admission, err)
	}
	if err = coordinator.ReleaseAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	inspection, err := coordinator.Inspect(admission.Intent.ID)
	if err != nil || inspection.Active || inspection.Intent.Status != outbox.StatusPending {
		t.Fatalf("released final = %+v, %v", inspection, err)
	}

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.Status != interactions.StatusResolved || len(resolved.FinalDeliveryIDs) != 1 {
		t.Fatalf("resolved interaction = %+v", resolved)
	}
	intent, err := coordinator.Get(admission.Intent.ID)
	if err != nil || intent.Status != outbox.StatusDelivered || intent.Attempts != 1 {
		t.Fatalf("retried final = %+v, %v", intent, err)
	}
	select {
	case outbound := <-manager.sent:
		if outbound.Content != "canonical released final" {
			t.Fatalf("retried outbound = %+v", outbound)
		}
	default:
		t.Fatal("released pending final was not retried")
	}
}

func TestRecoveryRetriesPreparedTaskFinalBeforeExternalSend(t *testing.T) {
	for _, test := range []struct {
		name              string
		completeTaskFirst bool
	}{
		{name: "after interaction fence"},
		{name: "after task completion", completeTaskFirst: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
			defer cleanup()
			manager := newInteractionChannelManager()
			coordinator := installInteractionChannelManager(t, al, manager)
			workspace := agent.Workspace
			taskID := "task-prepared-final-" + strings.ReplaceAll(test.name, " ", "-")
			interactionID := "interaction_prepared_" + strings.ReplaceAll(test.name, " ", "_")
			continuationSession := session.BuildOpaqueSessionKey(
				"agent:main:test:task-prepared-session-" + strings.ReplaceAll(test.name, " ", "-"),
			)
			ownerSession := session.BuildOpaqueSessionKey(
				"agent:main:test:owner-prepared-session-" + strings.ReplaceAll(test.name, " ", "-"),
			)

			tasks := al.taskRegistryForWorkspace(workspace)
			if err := tasks.Upsert(taskregistry.Record{
				TaskID: taskID, Runtime: taskregistry.RuntimeSubagent,
				TaskKind: "spawn", Task: "recover prepared task final",
				Status: taskregistry.StatusRunning, DeliveryStatus: taskregistry.DeliveryPending,
				DeliveryMode: string(toolshared.AsyncDeliveryUserOnly),
				Channel:      "telegram", ChatID: "chat-1",
				RequesterSessionKey: ownerSession,
			}); err != nil {
				t.Fatal(err)
			}

			request := testToolSuspensionRequest(workspace)
			request.Route.AgentID = agent.ID
			request.Route.SessionKey = ownerSession
			request.Route.RouteSessionKey = "route-" + ownerSession
			request.Origin.TaskID = taskID
			request.Origin.ContinuationSessionKey = continuationSession
			registry := al.interactionRegistryForWorkspace(workspace)
			record, err := registry.Create(interactions.CreateRequest{
				ID: interactionID, Kind: request.Prompt.Kind, Route: request.Route,
				Origin: request.Origin, Questions: request.Prompt.Questions,
				PromptSummary: request.Prompt.PromptSummary,
				ExpiresAt:     time.Now().Add(time.Hour),
			})
			if err != nil {
				t.Fatal(err)
			}
			record = markTestInteractionWaiting(t, registry, record)
			record, err = registry.ClaimAnswer(
				record.ID,
				record.Revision,
				interactions.Answer{Text: "continue", ReceivedAt: time.Now().UnixMilli()},
				interactions.OutcomeAnswered,
			)
			if err != nil {
				t.Fatal(err)
			}
			record, err = registry.MarkResuming(record.ID, record.Revision)
			if err != nil {
				t.Fatal(err)
			}
			agent.Sessions.AddFullMessage(continuationSession, providers.Message{
				Role: "assistant", ToolCalls: []providers.ToolCall{{
					ID: record.Origin.ToolCallID, Name: record.Origin.ToolName,
				}},
			})
			agent.Sessions.AddFullMessage(continuationSession, providers.Message{
				Role: "tool", ToolCallID: record.Origin.ToolCallID,
				Content: `{"interaction_id":"` + record.ID + `","outcome":"answered"}`,
			})
			const finalContent = "recovered prepared task completion"
			agent.Sessions.AddFullMessage(continuationSession, providers.Message{
				Role: "assistant", Content: finalContent,
			})
			_ = bindTestInteractionFinal(t, registry, record)
			if test.completeTaskFirst {
				if err = tasks.Complete(
					taskID,
					finalContent,
					&taskresult.Deliverable{Text: finalContent},
					taskregistry.DeliveryPending,
				); err != nil {
					t.Fatal(err)
				}
			}

			al.tasks.registries.Delete(normalizeRuntimeWorkspace(workspace))
			al.interactions.registries.Delete(workspace)
			if reloaded := al.interactionRegistryForWorkspace(workspace); reloaded.LastLoadError() != nil {
				t.Fatalf("reload prepared interaction registry: %v", reloaded.LastLoadError())
			}
			if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
				t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
			}
			reloadedRegistry := al.interactionRegistryForWorkspace(workspace)
			resolved, ok := reloadedRegistry.Get(interactionID)
			if !ok || resolved.Status != interactions.StatusResolved || len(resolved.FinalDeliveryIDs) != 1 {
				t.Fatalf("recovered interaction = %#v, found=%t", resolved, ok)
			}
			intent, getErr := coordinator.Get(resolved.FinalDeliveryIDs[0])
			if getErr != nil || intent.Status != outbox.StatusDelivered {
				t.Fatalf("recovered final outbox delivery = (%#v, %v)", intent, getErr)
			}
			reloadedTasks := al.taskRegistryForWorkspace(workspace)
			task, ok := reloadedTasks.Get(taskID)
			if !ok || task.Status != taskregistry.StatusSucceeded ||
				task.DeliveryStatus != taskregistry.DeliveryDelivered {
				t.Fatalf("recovered task = %#v, found=%t", task, ok)
			}
			select {
			case outbound := <-manager.sent:
				if outbound.Content != finalContent {
					t.Fatalf("recovered task outbound = %#v", outbound)
				}
			case <-time.After(time.Second):
				t.Fatal("prepared task final was not delivered after recovery")
			}
		})
	}
}

func TestRecoveryCommitsAcknowledgedPromptWithoutDuplicateSend(t *testing.T) {
	manager := newInteractionChannelManager()
	al := &AgentLoop{cfg: config.DefaultConfig(), channelManager: manager}
	coordinator := openTestInteractionOutbox(t, al)
	workspace := t.TempDir()
	request := testToolSuspensionRequest(workspace)
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	seedTestInteractionPromptOutcome(t, coordinator, workspace, record, outbox.StatusDelivered, 1)
	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusWaiting || record.PromptDeliveryID == "" {
		t.Fatalf("recovered record = %#v", record)
	}
	select {
	case duplicate := <-manager.sent:
		t.Fatalf("recovery duplicated acknowledged prompt: %#v", duplicate)
	default:
	}
}

func TestParseInteractionAnswerSupportsWhitespaceDelimitedCommands(t *testing.T) {
	singleRecord := interactions.Record{
		ShortID: "ABC12345",
		Questions: []interactions.Question{
			{ID: "confirmation", Question: "Proceed?"},
		},
	}
	multipleRecord := interactions.Record{
		ShortID: "13CCBF94",
		Questions: []interactions.Question{
			{ID: "test_region", Question: "Where?"},
			{ID: "test_mode", Question: "How?"},
		},
	}
	tests := []struct {
		name       string
		record     interactions.Record
		content    string
		wantText   string
		wantValues map[string]string
		wantError  bool
	}{
		{
			name: "single question command", record: singleRecord,
			content: "/answer abc12345 yes", wantText: "yes",
			wantValues: map[string]string{"confirmation": "yes"},
		},
		{
			name: "multiple answers first pair on command line", record: multipleRecord,
			content:  "/answer 13ccbf94 test_region: eu\ntest_mode: balanced",
			wantText: "test_region: eu\ntest_mode: balanced",
			wantValues: map[string]string{
				"test_region": "eu",
				"test_mode":   "balanced",
			},
		},
		{
			name: "production newline after id regression", record: multipleRecord,
			content:  "/answer 13ccbf94\ntest_region: eu\ntest_mode: balanced",
			wantText: "test_region: eu\ntest_mode: balanced",
			wantValues: map[string]string{
				"test_region": "eu",
				"test_mode":   "balanced",
			},
		},
		{
			name: "tab separator", record: singleRecord,
			content: "/answer abc12345\tyes", wantText: "yes",
			wantValues: map[string]string{"confirmation": "yes"},
		},
		{
			name: "crlf multiline body", record: multipleRecord,
			content:  "/answer\r\n13ccbf94\r\ntest_region: eu\r\ntest_mode: balanced",
			wantText: "test_region: eu\r\ntest_mode: balanced",
			wantValues: map[string]string{
				"test_region": "eu",
				"test_mode":   "balanced",
			},
		},
		{
			name: "unicode and surrounding whitespace", record: multipleRecord,
			content:  "\u2003/answer\u200313ccbf94\u2003 test_region: eu \n test_mode: balanced \u2003",
			wantText: "test_region: eu \n test_mode: balanced",
			wantValues: map[string]string{
				"test_region": "eu",
				"test_mode":   "balanced",
			},
		},
		{
			name: "telegram bot mention", record: multipleRecord,
			content:  "/answer@MintClawBot 13ccbf94\ntest_region: eu\ntest_mode: balanced",
			wantText: "test_region: eu\ntest_mode: balanced",
			wantValues: map[string]string{
				"test_region": "eu",
				"test_mode":   "balanced",
			},
		},
		{
			name: "plain message answer", record: singleRecord,
			content: "yes", wantText: "yes",
			wantValues: map[string]string{"confirmation": "yes"},
		},
		{
			name: "wrong short id", record: singleRecord,
			content: "/answer wrong-id yes", wantError: true,
		},
		{
			name: "missing short id", record: singleRecord,
			content: "/answer", wantError: true,
		},
		{
			name: "missing answer body", record: singleRecord,
			content: "/answer abc12345 \t\r\n", wantError: true,
		},
		{
			name: "unknown question id", record: multipleRecord,
			content: "/answer 13ccbf94\ntest_region: eu\nunknown: balanced", wantError: true,
		},
		{
			name: "duplicate question id", record: multipleRecord,
			content: "/answer 13ccbf94\ntest_region: eu\ntest_region: us", wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			answer, parseErr := parseInteractionAnswer(test.record, test.content, "message-1")
			if test.wantError {
				if parseErr == nil {
					t.Fatalf("parseInteractionAnswer(%q) accepted malformed answer: %#v", test.content, answer)
				}
				return
			}
			if parseErr != nil {
				t.Fatalf("parseInteractionAnswer(%q) error = %v", test.content, parseErr)
			}
			if answer.Text != test.wantText || answer.MessageID != "message-1" {
				t.Fatalf("answer = %#v, want text %q and message id message-1", answer, test.wantText)
			}
			for questionID, want := range test.wantValues {
				if answer.Values[questionID] != want {
					t.Errorf("answer.Values[%q] = %q, want %q", questionID, answer.Values[questionID], want)
				}
			}
		})
	}
	if _, incompleteErr := parseInteractionAnswer(
		multipleRecord,
		"test_region: eu",
		"message-incomplete",
	); incompleteErr == nil {
		t.Fatal("parseInteractionAnswer() accepted incomplete multi-question answer")
	}
	_, _, matched, err := parseInteractionAnswerEnvelope("/answerfoo 13ccbf94 yes")
	if matched || err != nil {
		t.Fatalf("/answerfoo envelope = (matched:%v, err:%v), want non-command", matched, err)
	}

	prompt := renderInteractionPrompt(multipleRecord)
	if !strings.Contains(prompt, "`test_region`") || !strings.Contains(prompt, "`test_mode`") {
		t.Fatalf("multi-question prompt omitted canonical IDs: %q", prompt)
	}
	if _, roundTripErr := parseInteractionAnswer(
		multipleRecord,
		"test_region: eu\ntest_mode: balanced",
		"message-plain",
	); roundTripErr != nil {
		t.Fatalf("rendered question IDs did not round-trip through parser: %v", roundTripErr)
	}
	templateStart := strings.Index(prompt, "`/answer")
	if templateStart < 0 {
		t.Fatalf("rendered prompt omitted answer template: %q", prompt)
	}
	renderedSubmission := strings.ReplaceAll(prompt[templateStart:], "`", "")
	renderedSubmission = strings.Replace(renderedSubmission, "test_region: …", "test_region: eu", 1)
	renderedSubmission = strings.Replace(renderedSubmission, "test_mode: …", "test_mode: balanced", 1)
	answer, err := parseInteractionAnswer(multipleRecord, renderedSubmission, "message-rendered")
	if err != nil || answer.Values["test_region"] != "eu" ||
		answer.Values["test_mode"] != "balanced" {
		t.Fatalf("rendered answer template did not round-trip: (%#v, %v)", answer, err)
	}
}

func TestMalformedMultilineAnswerCanRetryAndResumeExactlyOnce(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{
		Content: "Interaction resumed.",
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)

	sessionKey := "session-multiline-answer-retry"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "user", Content: "Configure the test"})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-multiline-question", Name: "request_user_input", Arguments: map[string]any{},
		}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	request.Origin.ToolCallID = "call-multiline-question"
	request.Prompt.Questions = []interactions.Question{
		{ID: "test_region", Question: "Which region?"},
		{ID: "test_mode", Question: "Which mode?"},
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	waitingRevision := record.Revision
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: sessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}

	malformed := bus.InboundMessage{
		Content: fmt.Sprintf(
			"/answer %s\ntest_region: eu\nunknown_question: balanced",
			record.ShortID,
		),
		SpoolID: "spool-malformed-answer",
		Context: inboundContextForInteraction(request.Route),
	}
	malformed.Context.MessageID = "malformed-answer"
	ownership, _, err := al.processInteractionInbound(t.Context(), malformed, target)
	if err != nil || ownership != interactionInboundCallerOwned {
		t.Fatalf("malformed processInteractionInbound() = (%v, %v)", ownership, err)
	}
	afterMalformed, _ := registry.Get(record.ID)
	if afterMalformed.Status != interactions.StatusWaiting ||
		afterMalformed.Revision != waitingRevision ||
		afterMalformed.Answer != nil {
		t.Fatalf("malformed answer mutated waiting interaction: %#v", afterMalformed)
	}
	if acked, released := tracker.counts(); acked != 0 || released != 0 {
		t.Fatalf("malformed answer ownership = acked:%d released:%d, want 0/0", acked, released)
	}
	select {
	case outbound := <-manager.sent:
		if !strings.Contains(outbound.Content, "unknown question id") {
			t.Fatalf("malformed answer response = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("malformed answer response was not delivered")
	}

	valid := bus.InboundMessage{
		Content: fmt.Sprintf(
			"/answer %s\ntest_region: eu\ntest_mode: balanced",
			record.ShortID,
		),
		SpoolID: "spool-valid-answer",
		Context: inboundContextForInteraction(request.Route),
	}
	valid.Context.MessageID = "valid-answer"
	ownership, _, err = al.processInteractionInbound(t.Context(), valid, target)
	if err != nil || ownership != interactionInboundClaimed {
		t.Fatalf("valid processInteractionInbound() = (%v, %v)", ownership, err)
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.Status != interactions.StatusResolved || resolved.Answer == nil ||
		resolved.Answer.Values["test_region"] != "eu" ||
		resolved.Answer.Values["test_mode"] != "balanced" {
		t.Fatalf("resolved interaction = %#v", resolved)
	}
	if acked, released := tracker.counts(); acked != 1 || released != 0 {
		t.Fatalf("valid retry ownership = acked:%d released:%d, want 1/0", acked, released)
	}
	provider.mu.Lock()
	providerCalls := provider.callCount
	provider.mu.Unlock()
	if providerCalls != 1 {
		t.Fatalf("resumption provider calls = %d, want 1", providerCalls)
	}
	toolResults := 0
	for _, message := range agent.Sessions.GetHistory(sessionKey) {
		if message.Role == "tool" && message.ToolCallID == "call-multiline-question" {
			toolResults++
		}
	}
	if toolResults != 1 {
		t.Fatalf("matching tool results = %d, want 1", toolResults)
	}
	select {
	case outbound := <-manager.sent:
		if outbound.Content != "Interaction resumed." {
			t.Fatalf("resumed final response = %#v", outbound)
		}
	default:
		t.Fatal("resumed final response was not delivered")
	}
}

func TestRenderInteractionPromptUsesAgentAuthoredLanguage(t *testing.T) {
	tests := []struct {
		name   string
		record interactions.Record
		want   string
	}{
		{
			name: "Russian single question with options",
			record: interactions.Record{
				ShortID: "16131195",
				Questions: []interactions.Question{{
					ID: "environment", Question: "Какую среду выбрать?",
					Options: []interactions.Option{
						{Label: "development", Description: "Среда разработки."},
						{Label: "staging", Description: "Предпродовая среда."},
						{Label: "production", Description: "Боевая среда."},
					},
				}},
			},
			want: "Какую среду выбрать?\n\n" +
				"• development — Среда разработки.\n" +
				"• staging — Предпродовая среда.\n" +
				"• production — Боевая среда.\n\n" +
				"`/answer 16131195 …`\n`/stop`",
		},
		{
			name: "Japanese single question with header",
			record: interactions.Record{
				ShortID: "8f03c2aa",
				Questions: []interactions.Question{{
					ID: "region", Header: "地域", Question: "デプロイ先を選んでください。",
				}},
			},
			want: "地域\n\nデプロイ先を選んでください。\n\n`/answer 8f03c2aa …`\n`/stop`",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := renderInteractionPrompt(test.record); got != test.want {
				t.Fatalf("renderInteractionPrompt() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRenderMultipleQuestionPromptUsesNeutralAnswerTemplate(t *testing.T) {
	record := interactions.Record{
		ShortID: "16131195",
		Questions: []interactions.Question{
			{ID: "region", Header: "Регион", Question: "Какой регион использовать?"},
			{ID: "mode", Header: "Режим", Question: "Какой режим развёртывания выбрать?"},
		},
	}
	want := "1. `region` Регион\nКакой регион использовать?\n\n" +
		"2. `mode` Режим\nКакой режим развёртывания выбрать?\n\n" +
		"`/answer 16131195`\n`region: …`\n`mode: …`"
	got := renderInteractionPrompt(record)
	if got != want {
		t.Fatalf("renderInteractionPrompt() = %q, want %q", got, want)
	}
	for _, removed := range []string{"Input needed", "Reply with", "question_id", "<answer>"} {
		if strings.Contains(got, removed) {
			t.Fatalf("prompt retained runtime prose %q: %q", removed, got)
		}
	}
}

func TestParseSingleInteractionAnswerSupportsDirectAndCommandReplies(t *testing.T) {
	record := interactions.Record{
		ShortID: "16131195",
		Questions: []interactions.Question{{
			ID: "environment", Question: "Какую среду выбрать?",
		}},
	}
	for _, reply := range []string{"production", "/answer 16131195 production"} {
		answer, err := parseInteractionAnswer(record, reply, "message-single")
		if err != nil || answer.Text != "production" || answer.MessageID != "message-single" {
			t.Fatalf("parseInteractionAnswer(%q) = (%#v, %v)", reply, answer, err)
		}
	}
}

func TestApprovalPromptAndAnswerUseFixedPolicyChoices(t *testing.T) {
	record := interactions.Record{
		Kind: interactions.KindApproval, ShortID: "APR123",
		Origin:         interactions.Origin{ToolName: "deploy"},
		PromptSummary:  "Run a protected deployment command?",
		ApprovalAction: "Run a protected deployment command?",
	}
	prompt := renderInteractionPrompt(record)
	want := "`deploy`\nAllow this action?\n\nExact action: Run a protected deployment command?\n\n" +
		"`/answer APR123 allow_once`\n`/answer APR123 deny`"
	if prompt != want {
		t.Fatalf("approval prompt = %q, want %q", prompt, want)
	}
	for _, removed := range []string{"Approval needed", "Requested action", "Reply"} {
		if strings.Contains(prompt, removed) {
			t.Fatalf("approval prompt retained runtime prose %q: %q", removed, prompt)
		}
	}
	answer, err := parseInteractionAnswer(record, "/answer apr123 allow once", "message-approval")
	if err != nil || answer.Text != "allow_once" || answer.MessageID != "message-approval" {
		t.Fatalf("allow answer = (%#v, %v)", answer, err)
	}
	answer, err = parseInteractionAnswer(record, "deny", "message-deny")
	if err != nil || answer.Text != "deny" {
		t.Fatalf("deny answer = (%#v, %v)", answer, err)
	}
	if _, err = parseInteractionAnswer(record, "always", "message-invalid"); err == nil {
		t.Fatal("approval parser accepted a persistent grant")
	}
}

func TestApprovalPromptIncludesOnlyUnambiguousExternalObjective(t *testing.T) {
	record := interactions.Record{
		Kind: interactions.KindApproval, ShortID: "APR123",
		Origin: interactions.Origin{
			ToolName: "browser_act",
			ObjectiveChecklist: []interactions.ObjectiveChecklistItem{
				{ID: "objective_1", Item: "Find the expired listing", Kind: "result"},
				{
					ID: "objective_2", Item: "Republish Lenovo ThinkVision LT1421 at $25",
					Kind: "external_action",
				},
			},
		},
		ApprovalAction: "Click button \"publish\" on https://post.craigslist.org; effect: `external_commit`",
	}
	prompt := renderInteractionPrompt(record)
	for _, required := range []string{
		"`browser_act`",
		"Requested outcome: Republish Lenovo ThinkVision LT1421 at $25",
		"Exact action: Click button \"publish\" on https://post.craigslist.org",
		"effect: `external_commit`",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("approval prompt omitted %q: %q", required, prompt)
		}
	}

	record.Origin.ObjectiveChecklist = append(record.Origin.ObjectiveChecklist, interactions.ObjectiveChecklistItem{
		ID: "objective_3", Item: "Publish a second listing", Kind: "external_action",
	})
	if ambiguous := renderInteractionPrompt(record); strings.Contains(ambiguous, "Requested outcome:") {
		t.Fatalf("ambiguous approval prompt selected an objective: %q", ambiguous)
	}
}

func TestApprovalAnswerOutcomeIsChannelIndependent(t *testing.T) {
	for _, channel := range []string{"telegram", "slack"} {
		for _, test := range []struct {
			answer string
			want   interactions.Outcome
		}{
			{answer: "allow_once", want: interactions.OutcomeAllowed},
			{answer: "deny", want: interactions.OutcomeDenied},
		} {
			record := interactions.Record{
				Kind:  interactions.KindApproval,
				Route: interactions.Route{Channel: channel},
			}
			got := interactionAnswerOutcome(record, interactions.Answer{Text: test.answer})
			if got != test.want {
				t.Fatalf(
					"interactionAnswerOutcome(channel=%q, answer=%q) = %q, want %q",
					channel,
					test.answer,
					got,
					test.want,
				)
			}
		}
	}
	question := interactions.Record{Kind: interactions.KindQuestion}
	if got := interactionAnswerOutcome(
		question,
		interactions.Answer{Text: "allow_once"},
	); got != interactions.OutcomeAnswered {
		t.Fatalf("question outcome = %q, want answered", got)
	}
}

func TestDurableHumanApprovalAllowsOrDeniesOriginalToolCall(t *testing.T) {
	for _, test := range []struct {
		name           string
		answer         string
		outcome        interactions.Outcome
		wantExecutions int
		wantConsumed   bool
		revokePolicy   bool
		mutateArgs     bool
		noContext      bool
	}{
		{
			name: "allow once", answer: "allow_once", outcome: interactions.OutcomeAllowed,
			wantExecutions: 1, wantConsumed: true,
		},
		{
			name: "allow once without context history", answer: "allow_once", outcome: interactions.OutcomeAllowed,
			wantExecutions: 1, wantConsumed: true, noContext: true,
		},
		{name: "deny", answer: "deny", outcome: interactions.OutcomeDenied},
		{name: "policy revoked", answer: "allow_once", outcome: interactions.OutcomeAllowed, revokePolicy: true},
		{name: "arguments changed", answer: "allow_once", outcome: interactions.OutcomeAllowed, mutateArgs: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &sequenceProvider{responses: []*providers.LLMResponse{
				{ToolCalls: []providers.ToolCall{{
					ID: "call-protected", Name: "approval_counting",
					Arguments: map[string]any{"token": "secret-value"},
				}}},
				{Content: "approval flow finished", FinishReason: "stop"},
			}}
			al, agent, cleanup := newTurnCoordTestLoop(t, provider)
			defer cleanup()
			if test.noContext {
				al.contextManager = &noneContextManager{}
			}
			manager := newInteractionChannelManager()
			installInteractionChannelManager(t, al, manager)
			tool := &approvalCountingTool{}
			agent.Tools.Register(tool)
			hook := &durableApprovalHook{
				actionSummary: "Run the protected test action",
			}
			if err := al.MountHook(NamedHook("durable-approval", hook)); err != nil {
				t.Fatal(err)
			}
			inbound := &bus.InboundContext{
				Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", MessageID: "origin-message",
			}
			turnStatus := TurnEndStatusCompleted
			response, err := al.runAgentLoop(t.Context(), agent, turnSpec{
				TurnStatus:            &turnStatus,
				InteractionSessionKey: "owner-session",
				Dispatch: DispatchRequest{
					RouteSessionKey: "route-approval", SessionKey: "session-approval",
					UserMessage: "run protected action", InboundContext: inbound,
				},
				DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
			})
			if err != nil || response != "" || turnStatus != TurnEndStatusSuspended || tool.executions != 0 {
				t.Fatalf(
					"initial approval turn = (%q, %q, executions=%d, err=%v)",
					response,
					turnStatus,
					tool.executions,
					err,
				)
			}
			registry := al.interactionRegistryForWorkspace(agent.Workspace)
			record, ok := activeInteractionForSession(registry, "owner-session")
			if !ok || record.Kind != interactions.KindApproval ||
				record.Status != interactions.StatusWaiting || record.Origin.ArgumentHash == "" {
				t.Fatalf("approval interaction = %#v", record)
			}
			select {
			case prompt := <-manager.sent:
				if !strings.Contains(prompt.Content, "approval_counting") ||
					!strings.Contains(prompt.Content, "Run the protected test action") ||
					!strings.Contains(prompt.Content, "`/answer "+record.ShortID+" allow_once`") ||
					strings.Contains(prompt.Content, "Approval needed") ||
					strings.Contains(prompt.Content, "secret-value") ||
					prompt.ReplyToMessageID != "origin-message" ||
					prompt.Metadata.RequestID != "origin-message" ||
					len(prompt.TraceScopes) != 1 ||
					prompt.TraceScopes[0] != runtimeevents.NewTraceScope(agent.Workspace, record.Origin.TurnID) ||
					!prompt.Metadata.IsApprovalPrompt() {
					t.Fatalf("approval prompt = %#v", prompt)
				}
			case <-time.After(time.Second):
				t.Fatal("approval prompt was not delivered")
			}
			if len(manager.pausedTargets) != 1 ||
				manager.pausedTargets[0].SessionKey != "session-approval" ||
				len(manager.dismissedSessions) != 0 {
				t.Fatalf(
					"suspension feedback lifecycle = paused:%#v dismissed:%#v",
					manager.pausedTargets,
					manager.dismissedSessions,
				)
			}
			if test.revokePolicy {
				hook.revoked = true
			}
			if test.mutateArgs {
				history := agent.Sessions.GetHistory("session-approval")
				for messageIndex := range history {
					for callIndex := range history[messageIndex].ToolCalls {
						call := &history[messageIndex].ToolCalls[callIndex]
						if call.ID == "call-protected" {
							call.Arguments = map[string]any{"token": "changed-after-approval"}
						}
					}
				}
				agent.Sessions.SetHistory("session-approval", history)
			}
			record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
				Text: test.answer, MessageID: "approval-answer", ReceivedAt: time.Now().UnixMilli(),
			}, test.outcome)
			if err != nil {
				t.Fatal(err)
			}
			if err = al.resumeClaimedInteraction(
				t.Context(), registry, agent.Workspace, agent, nil, *inbound, record,
			); err != nil {
				t.Fatalf("resumeClaimedInteraction() error = %v", err)
			}
			resolved, _ := registry.Get(record.ID)
			if resolved.Status != interactions.StatusResolved ||
				(resolved.ApprovalConsumedAt != 0) != test.wantConsumed ||
				tool.executions != test.wantExecutions {
				t.Fatalf("resolved approval = %#v, executions=%d", resolved, tool.executions)
			}
			if test.wantExecutions > 0 {
				provider.mu.Lock()
				requests := append([][]providers.Message(nil), provider.requests...)
				provider.mu.Unlock()
				if len(requests) != 2 {
					t.Fatalf("provider requests = %d, want initial and continuation", len(requests))
				}
				callCount, resultCount := 0, 0
				for _, message := range requests[1] {
					if messageContainsToolCall(message, "call-protected") {
						callCount++
					}
					if message.Role == "tool" && message.ToolCallID == "call-protected" {
						resultCount++
						if message.ToolResultStatus == providers.ToolResultStatusUnresolved {
							t.Fatalf("continuation request retained unresolved result: %#v", requests[1])
						}
					}
				}
				if callCount != 1 || resultCount != 1 {
					t.Fatalf("continuation tool pair = call:%d result:%d", callCount, resultCount)
				}
			}
			select {
			case final := <-manager.sent:
				metadata := final.Metadata
				if final.Content != "approval flow finished" || !metadata.IsFinalReply() || !metadata.IsFinal() ||
					!metadata.RemovesInteractionControls() ||
					final.ReplyToMessageID != "approval-answer" {
					t.Fatalf("approval final = %#v", final)
				}
			case <-time.After(time.Second):
				t.Fatal("approval continuation final was not delivered")
			}
			wantDismissed := []string{
				"telegram:chat-1:owner-session",
				"telegram:chat-1:session-approval",
			}
			if strings.Join(manager.dismissedSessions, "\n") != strings.Join(wantDismissed, "\n") {
				t.Fatalf(
					"feedback cleanup = %#v, want %#v",
					manager.dismissedSessions,
					wantDismissed,
				)
			}
		})
	}
}

func TestStopCancellationAbortsBlockingApprovedTool(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "call-blocking-protected", Name: "approval_blocking", Arguments: map[string]any{},
		}}},
		{Content: "SHOULD_NOT_BE_DELIVERED", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tool := newBlockingApprovalTool()
	tool.terminalResult = toolshared.ErrorResult(
		`{"state":"unknown","code":"DISPATCH_UNCERTAIN","invocation_id":"inv_recover"}`,
	)
	tool.terminalResult.Media = []string{"media://recoverable-partial-artifact"}
	agent.Tools.Register(tool)
	if err := al.MountHook(NamedHook("blocking-approval", &durableApprovalHook{
		actionSummary: "Run the blocking protected action",
	})); err != nil {
		t.Fatal(err)
	}
	inbound := &bus.InboundContext{
		Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
		SenderID: "user-1", MessageID: "approval-origin",
	}
	turnStatus := TurnEndStatusCompleted
	response, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus:            &turnStatus,
		InteractionSessionKey: "owner-approval-stop",
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-approval-stop", SessionKey: "continuation-approval-stop",
			UserMessage: "run blocking protected action", InboundContext: inbound,
		},
		DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
	})
	if err != nil || response != "" || turnStatus != TurnEndStatusSuspended {
		t.Fatalf("initial approval turn = (%q, %q, %v)", response, turnStatus, err)
	}
	select {
	case <-manager.sent:
	case <-time.After(time.Second):
		t.Fatal("approval prompt was not delivered")
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(registry, "owner-approval-stop")
	if !ok || record.Kind != interactions.KindApproval {
		t.Fatalf("approval interaction = %#v", record)
	}
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: record.Route.SessionKey,
		RouteClaimKey: runtimeRouteClaimKey(record.Route.RouteSessionKey, ""),
		Allocation:    session.Allocation{RouteScopeKey: record.Route.RouteSessionKey},
	}
	answer := bus.InboundMessage{
		Content: "/answer " + record.ShortID + " allow_once", SpoolID: "approval-answer-spool",
		Context: inboundContextForInteraction(record.Route),
	}
	answer.Context.MessageID = "approval-answer"
	if !newInboundTurnCoordinator(al).routeExplicitInteractionAnswer(
		t.Context(), answer, target,
	) {
		t.Fatal("approval answer did not enter the continuation worker")
	}
	select {
	case <-tool.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the blocking approved tool")
	}

	stop := bus.InboundMessage{
		Content: "/stop", Context: inboundContextForInteraction(record.Route),
	}
	stop.Context.MessageID = "approval-stop"
	cancellation, err := al.cancelInteractionForControlMessage(t.Context(), stop, target)
	if err != nil {
		t.Fatal(err)
	}
	if !cancellation.Matched || !cancellation.Canceled || cancellation.Failed ||
		!cancellation.CommandHandled {
		t.Fatalf("approval stop cancellation = %#v", cancellation)
	}
	select {
	case <-tool.canceled:
	case <-time.After(time.Second):
		t.Fatal("blocking approved tool did not observe cancellation")
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled || record.ApprovalConsumedAt == 0 {
		t.Fatalf("canceled approval interaction = %#v", record)
	}
	history := agent.Sessions.GetHistory("continuation-approval-stop")
	if countInteractionToolResults(history, record.Origin.ToolCallID) != 1 {
		t.Fatal("approval stop did not pair the protected tool call exactly once")
	}
	_, resultIndex := interactionToolPairIndexes(history, record.Origin.ToolCallID)
	if resultIndex < 0 ||
		!strings.Contains(history[resultIndex].Content, `"code":"DISPATCH_UNCERTAIN"`) ||
		!strings.Contains(history[resultIndex].Content, `"invocation_id":"inv_recover"`) ||
		strings.Contains(history[resultIndex].Content, `"outcome":"canceled"`) ||
		history[resultIndex].ToolResultStatus != providers.ToolResultStatusError ||
		len(history[resultIndex].Media) != 1 ||
		history[resultIndex].Media[0] != "media://recoverable-partial-artifact" {
		t.Fatalf("approved tool terminal result = %#v", history)
	}
	select {
	case final := <-manager.sent:
		t.Fatalf("aborted approved tool published a final response: %#v", final)
	case <-time.After(100 * time.Millisecond):
	}
	claim, _, claimed := al.turns.claimRuntimeRouteSession(target, "post-approval-stop-reuse")
	if !claimed {
		t.Fatal("canceled approved tool did not release the route for reuse")
	}
	claim.releaseIfOwned()
}

func TestStopCancellationAfterApprovedToolExecutionPersistsTerminalResult(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "call-immediate-protected", Name: "approval_immediate", Arguments: map[string]any{},
		}}},
		{Content: "SHOULD_NOT_BE_DELIVERED", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tool := &immediateApprovalTool{result: toolshared.ErrorResult(
		`{"state":"unknown","code":"DISPATCH_UNCERTAIN","invocation_id":"inv_post_execute"}`,
	)}
	agent.Tools.Register(tool)
	hook := &postExecutionBarrierApprovalHook{afterTool: make(chan struct{}, 1)}
	if err := al.MountHook(NamedHook("post-execution-barrier", hook)); err != nil {
		t.Fatal(err)
	}
	inbound := &bus.InboundContext{
		Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
		SenderID: "user-1", MessageID: "approval-origin",
	}
	turnStatus := TurnEndStatusCompleted
	response, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus:            &turnStatus,
		InteractionSessionKey: "owner-approval-post-execute-stop",
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-approval-post-execute-stop",
			SessionKey:      "continuation-approval-post-execute-stop",
			UserMessage:     "run immediate protected action",
			InboundContext:  inbound,
		},
		DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
	})
	if err != nil || response != "" || turnStatus != TurnEndStatusSuspended {
		t.Fatalf("initial approval turn = (%q, %q, %v)", response, turnStatus, err)
	}
	select {
	case <-manager.sent:
	case <-time.After(time.Second):
		t.Fatal("approval prompt was not delivered")
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(registry, "owner-approval-post-execute-stop")
	if !ok || record.Kind != interactions.KindApproval {
		t.Fatalf("approval interaction = %#v", record)
	}
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: record.Route.SessionKey,
		RouteClaimKey: runtimeRouteClaimKey(record.Route.RouteSessionKey, ""),
		Allocation:    session.Allocation{RouteScopeKey: record.Route.RouteSessionKey},
	}
	answer := bus.InboundMessage{
		Content: "/answer " + record.ShortID + " allow_once", SpoolID: "approval-answer-spool",
		Context: inboundContextForInteraction(record.Route),
	}
	answer.Context.MessageID = "approval-answer"
	if !newInboundTurnCoordinator(al).routeExplicitInteractionAnswer(t.Context(), answer, target) {
		t.Fatal("approval answer did not enter the continuation worker")
	}
	select {
	case <-hook.afterTool:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the post-execution barrier")
	}

	stop := bus.InboundMessage{
		Content: "/stop", Context: inboundContextForInteraction(record.Route),
	}
	stop.Context.MessageID = "approval-stop"
	cancellation, err := al.cancelInteractionForControlMessage(t.Context(), stop, target)
	if err != nil {
		t.Fatal(err)
	}
	if !cancellation.Matched || !cancellation.Canceled || cancellation.Failed ||
		!cancellation.CommandHandled {
		t.Fatalf("approval stop cancellation = %#v", cancellation)
	}
	if tool.executions != 1 {
		t.Fatalf("approved tool executions = %d, want 1", tool.executions)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled || record.ApprovalConsumedAt == 0 {
		t.Fatalf("canceled approval interaction = %#v", record)
	}
	history := agent.Sessions.GetHistory("continuation-approval-post-execute-stop")
	if countInteractionToolResults(history, record.Origin.ToolCallID) != 1 {
		t.Fatalf("post-execution stop did not pair the protected tool call exactly once: %#v", history)
	}
	_, resultIndex := interactionToolPairIndexes(history, record.Origin.ToolCallID)
	if resultIndex < 0 ||
		!strings.Contains(history[resultIndex].Content, `"code":"DISPATCH_UNCERTAIN"`) ||
		!strings.Contains(history[resultIndex].Content, `"invocation_id":"inv_post_execute"`) ||
		strings.Contains(history[resultIndex].Content, `"outcome":"canceled"`) ||
		history[resultIndex].ToolResultStatus != providers.ToolResultStatusError {
		t.Fatalf("approved tool terminal result = %#v", history)
	}
	select {
	case final := <-manager.sent:
		t.Fatalf("post-execution stop published a final response: %#v", final)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestInteractionCancellationDoesNotReplaceConsumedApprovalResult(t *testing.T) {
	sessionKey := session.BuildOpaqueSessionKey("agent:main:test:consumed-approval-cancellation")
	agent := &AgentInstance{Sessions: session.NewMemoryStore()}
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role:      "assistant",
		ToolCalls: []providers.ToolCall{{ID: "call-consumed", Name: "protected_mutation"}},
	})
	record := interactions.Record{
		ID: "interaction-consumed", Kind: interactions.KindApproval, ApprovalConsumedAt: time.Now().UnixMilli(),
		Origin: interactions.Origin{
			ToolCallID: "call-consumed", ContinuationSessionKey: sessionKey,
		},
	}
	err := (&AgentLoop{}).ensureInteractionCancellationToolResult(
		t.Context(),
		agent,
		record,
		"session_control_stop",
	)
	if err == nil || !strings.Contains(err.Error(), "terminal result is unavailable") {
		t.Fatalf("consumed approval cancellation error = %v", err)
	}
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) != 1 || history[0].Role != "assistant" {
		t.Fatalf("consumed approval gained synthetic cancellation result: %#v", history)
	}
}

func TestDurableHumanApprovalBindsTrustedPreparedArguments(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID:        "call-prepared",
			Name:      "approval_binding",
			Arguments: map[string]any{"mutable": "model-value"},
		}}},
		{Content: "approval flow finished", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tool := &approvalBindingTool{}
	agent.Tools.Register(tool)
	if err := al.MountHook(NamedHook("prepared-approval", &durableApprovalHook{
		actionSummary: "Run the prepared protected action",
	})); err != nil {
		t.Fatal(err)
	}
	inbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
	}
	turnStatus := TurnEndStatusCompleted
	response, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus: &turnStatus,
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-prepared", SessionKey: "session-prepared",
			UserMessage: "run prepared action", InboundContext: inbound,
		},
		DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
	})
	if err != nil || response != "" || turnStatus != TurnEndStatusSuspended {
		t.Fatalf("initial approval turn = (%q, %q, %v)", response, turnStatus, err)
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(registry, "session-prepared")
	if !ok {
		t.Fatal("approval interaction not found")
	}
	wantHash, err := interactions.HashArguments(agent.Workspace, map[string]any{
		"plan_hash": "prepared-plan-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Origin.ArgumentHash != wantHash {
		t.Fatalf("argument hash = %q, want trusted binding %q", record.Origin.ArgumentHash, wantHash)
	}
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "approval-answer", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	if err = al.resumeClaimedInteraction(
		t.Context(), registry, agent.Workspace, agent, nil, *inbound, record,
	); err != nil {
		t.Fatal(err)
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.ApprovalConsumedAt == 0 || tool.executions != 1 {
		t.Fatalf("resolved approval = %#v, executions=%d", resolved, tool.executions)
	}
	if len(tool.bindingCalls) != 2 ||
		tool.bindingCalls[0] != "call-prepared" ||
		tool.bindingCalls[1] != "call-prepared" {
		t.Fatalf("approval binding calls = %#v", tool.bindingCalls)
	}
	if len(tool.bindingContinuations) != 2 ||
		tool.bindingContinuations[0] ||
		!tool.bindingContinuations[1] {
		t.Fatalf("approval continuation markers = %#v", tool.bindingContinuations)
	}
	if len(tool.executionIDs) != 2 ||
		tool.executionIDs[0] != record.Origin.ExecutionID ||
		tool.executionIDs[1] != record.Origin.ExecutionID {
		t.Fatalf(
			"approval execution identities = %#v, origin = %q",
			tool.executionIDs,
			record.Origin.ExecutionID,
		)
	}
	if len(tool.workspaces) != 2 ||
		tool.workspaces[0] != agent.Workspace ||
		tool.workspaces[1] != agent.Workspace {
		t.Fatalf("approval workspaces = %#v", tool.workspaces)
	}
}

func TestQuestionContinuationPreservesBrowserOwnerWithoutApproval(t *testing.T) {
	toolCall := func(id, operation string) providers.ToolCall {
		return providers.ToolCall{
			ID: id, Name: "browser_handoff_continuation",
			Arguments: map[string]any{"operation": operation},
		}
	}
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{toolCall("call-handoff", "handoff")}},
		{ToolCalls: []providers.ToolCall{toolCall("call-resume", "resume")}},
		{ToolCalls: []providers.ToolCall{toolCall("call-observe", "observe")}},
		{Content: "browser handoff continuation finished", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tool := &browserHandoffContinuationTool{}
	agent.Tools.Register(tool)
	inbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-browser-owner", SenderID: "user-browser-owner",
	}
	turnStatus := TurnEndStatusCompleted
	response, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus: &turnStatus,
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-browser-owner", SessionKey: "session-browser-owner",
			UserMessage: "hand off browser control", InboundContext: inbound,
		},
		DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
	})
	if err != nil || response != "" || turnStatus != TurnEndStatusSuspended {
		t.Fatalf("initial handoff turn = (%q, %q, %v)", response, turnStatus, err)
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(registry, "session-browser-owner")
	if !ok || record.Kind != interactions.KindQuestion || record.Origin.ExecutionID == "" {
		t.Fatalf("browser handoff interaction = %#v, found=%t", record, ok)
	}
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "release_browser: release", Values: map[string]string{"release_browser": "release"},
		MessageID: "browser-release", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	if err = al.resumeClaimedInteraction(
		t.Context(), registry, agent.Workspace, agent, nil, *inbound, record,
	); err != nil {
		t.Fatal(err)
	}
	if !tool.released || !reflect.DeepEqual(tool.operations, []string{"handoff", "resume", "observe"}) {
		t.Fatalf("browser continuation operations = %#v, released=%t", tool.operations, tool.released)
	}
	if len(tool.executionIDs) != 3 {
		t.Fatalf("browser execution identities = %#v", tool.executionIDs)
	}
	for index, executionID := range tool.executionIDs {
		if executionID != record.Origin.ExecutionID || tool.approvalContinuations[index] {
			t.Fatalf(
				"browser continuation identity[%d] = %q, approval=%t, origin=%q",
				index,
				executionID,
				tool.approvalContinuations[index],
				record.Origin.ExecutionID,
			)
		}
	}
}

func TestDurableHumanApprovalDoesNotPrepareAfterPolicyRevocation(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "call-revoked", Name: "approval_binding",
			Arguments: map[string]any{"mutable": "model-value"},
		}}},
		{Content: "approval denied", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	installInteractionChannelManager(t, al, newInteractionChannelManager())
	tool := &approvalBindingTool{}
	agent.Tools.Register(tool)
	hook := &durableApprovalHook{actionSummary: "Run the prepared protected action"}
	if err := al.MountHook(NamedHook("revoked-prepared-approval", hook)); err != nil {
		t.Fatal(err)
	}
	inbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
	}
	turnStatus := TurnEndStatusCompleted
	if _, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus: &turnStatus,
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-revoked", SessionKey: "session-revoked",
			UserMessage: "run prepared action", InboundContext: inbound,
		},
		DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
	}); err != nil || turnStatus != TurnEndStatusSuspended {
		t.Fatalf("initial approval turn = (%q, %v)", turnStatus, err)
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(registry, "session-revoked")
	if !ok || len(tool.bindingCalls) != 1 {
		t.Fatalf("initial approval = (%#v, binding calls=%#v)", record, tool.bindingCalls)
	}
	hook.revoked = true
	record, err := registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "approval-answer", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	if err = al.resumeClaimedInteraction(
		t.Context(), registry, agent.Workspace, agent, nil, *inbound, record,
	); err != nil {
		t.Fatal(err)
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.ApprovalConsumedAt != 0 || tool.executions != 0 ||
		len(tool.bindingCalls) != 1 {
		t.Fatalf(
			"revoked approval = (%#v, executions=%d, binding calls=%#v)",
			resolved,
			tool.executions,
			tool.bindingCalls,
		)
	}
}

func TestHumanApprovalNeverRendersGenericArguments(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "call-opaque", Name: "approval_counting",
			Arguments: map[string]any{"source": "-----BEGIN PRIVATE KEY-----\nsecret"},
		}}},
		{Content: "approval flow finished", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tool := &approvalCountingTool{}
	agent.Tools.Register(tool)
	if err := al.MountHook(NamedHook("opaque-approval", &durableApprovalHook{
		actionSummary: "Rotate production signing material",
	})); err != nil {
		t.Fatal(err)
	}
	inbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
	}
	turnStatus := TurnEndStatusCompleted
	response, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus: &turnStatus,
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-opaque", SessionKey: "session-opaque",
			UserMessage: "run opaque action", InboundContext: inbound,
		},
		DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
	})
	if err != nil || response != "" ||
		turnStatus != TurnEndStatusSuspended || tool.executions != 0 {
		t.Fatalf(
			"opaque approval turn = (%q, %q, executions=%d, err=%v)",
			response,
			turnStatus,
			tool.executions,
			err,
		)
	}
	record, ok := activeInteractionForSession(
		al.interactionRegistryForWorkspace(agent.Workspace), "session-opaque",
	)
	if !ok || strings.Contains(record.ApprovalAction, "PRIVATE KEY") ||
		record.ApprovalAction != "Rotate production signing material" {
		t.Fatalf("approval interaction = %#v", record)
	}
	select {
	case prompt := <-manager.sent:
		if strings.Contains(prompt.Content, "PRIVATE KEY") ||
			!strings.Contains(prompt.Content, record.ApprovalAction) {
			t.Fatalf("approval prompt = %#v", prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("approval prompt was not delivered")
	}
}

func TestApprovalRecoveryNeverReexecutesConsumedOrTimedOutCall(t *testing.T) {
	for _, test := range []struct {
		name        string
		consume     bool
		wantOutcome interactions.Outcome
	}{
		{name: "consumed before crash", consume: true, wantOutcome: interactions.OutcomeDeliveryUnknown},
		{name: "timed out", wantOutcome: interactions.OutcomeTimedOut},
	} {
		t.Run(test.name, func(t *testing.T) {
			al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
			defer cleanup()
			manager := newInteractionChannelManager()
			installInteractionChannelManager(t, al, manager)
			tool := &approvalCountingTool{}
			agent.Tools.Register(tool)
			sessionKey := "session-approval-recovery"
			args := map[string]any{"token": "recovery-secret"}
			agent.Sessions.AddFullMessage(sessionKey, providers.Message{
				Role: "assistant", ToolCalls: []providers.ToolCall{{
					ID: "call-approval-recovery", Name: tool.Name(), Arguments: args,
				}},
			})
			argumentHash, err := interactions.HashArguments(agent.Workspace, args)
			if err != nil {
				t.Fatal(err)
			}
			expiresAt := time.Now().Add(time.Minute)
			registry := al.interactionRegistryForWorkspace(agent.Workspace)
			record, err := registry.Create(interactions.CreateRequest{
				Kind: interactions.KindApproval,
				Route: interactions.Route{
					AgentID: agent.ID, SessionKey: sessionKey, RouteSessionKey: "route-recovery",
					Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
				},
				Origin: interactions.Origin{
					TurnID: "turn-recovery", ToolCallID: "call-approval-recovery",
					ToolName: tool.Name(), ArgumentHash: argumentHash,
					ExecutionContext: &bus.InboundContext{
						Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
					},
				},
				PromptSummary:  "Run recovery action",
				ApprovalAction: "Run recovery action",
				ExpiresAt:      expiresAt,
			})
			if err != nil {
				t.Fatal(err)
			}
			record = markTestInteractionWaiting(t, registry, record)
			seedTestInteractionPromptOutcomeWithMessages(
				t,
				al.outboundCoordinator(),
				agent.Workspace,
				record,
				outbox.StatusDelivered,
				1,
				[]string{"7716"},
			)
			if test.consume {
				record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
					Text: "allow_once", MessageID: "answer-recovery", ReceivedAt: time.Now().UnixMilli(),
				}, interactions.OutcomeAllowed)
				if err != nil {
					t.Fatal(err)
				}
				record, err = registry.MarkResuming(record.ID, record.Revision)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = registry.ConsumeApproval(
					record.ID, record.Revision, record.Origin.ToolCallID,
					record.Origin.ToolName, record.Origin.ArgumentHash,
				); err != nil {
					t.Fatal(err)
				}
			} else {
				claimed, claimErr := registry.ClaimOverdue(expiresAt.Add(time.Second))
				if claimErr != nil || len(claimed) != 1 {
					t.Fatalf("ClaimOverdue() = (%#v, %v)", claimed, claimErr)
				}
			}
			if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
				t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
			}
			resolved, _ := registry.Get(record.ID)
			if resolved.Status != interactions.StatusResolved || resolved.Outcome != test.wantOutcome ||
				tool.executions != 0 {
				t.Fatalf("recovered approval = %#v, executions=%d", resolved, tool.executions)
			}
			_, resultIndex := interactionToolPairIndexes(
				agent.Sessions.GetHistory(sessionKey), record.Origin.ToolCallID,
			)
			if resultIndex < 0 {
				t.Fatal("recovery did not pair the protected tool call")
			}
			result := agent.Sessions.GetHistory(sessionKey)[resultIndex]
			if !strings.Contains(result.Content, string(test.wantOutcome)) {
				t.Fatalf("recovery tool result = %q", result.Content)
			}
			if test.wantOutcome == interactions.OutcomeTimedOut &&
				!strings.Contains(result.Content, "The protected tool was not executed") {
				t.Fatalf("timed-out approval result = %q", result.Content)
			}
			if test.consume {
				for len(manager.sent) > 0 {
					<-manager.sent
				}
				target := &inboundDispatchTarget{
					Agent: agent, SessionKey: sessionKey,
					Allocation: session.Allocation{RouteScopeKey: record.Route.RouteSessionKey},
				}
				repeated := bus.InboundMessage{
					Content: "Allow once", SpoolID: "spool-repeated-unknown-approval",
					Context: inboundContextForInteraction(record.Route),
				}
				repeated.Context.MessageID = "repeated-unknown-approval"
				repeated.Context.Raw = map[string]string{
					bus.InboundMetadataKeyInteractionChoice:            bus.InboundInteractionChoiceAllowOnce,
					bus.InboundMetadataKeyInteractionResponse:          "Allow once",
					bus.InboundMetadataKeyInteractionShortID:           record.ShortID,
					bus.InboundMetadataKeyInteractionResponseMessageID: "7716",
				}
				if !newInboundTurnCoordinator(al).routeProjectedInteractionAnswer(
					t.Context(), repeated, target,
				) {
					t.Fatal("repeated unknown approval escaped interaction routing")
				}
				select {
				case notice := <-manager.sent:
					if !strings.Contains(notice.Content, "unknown delivery outcome") ||
						!strings.Contains(notice.Content, "was not retried") {
						t.Fatalf("repeated unknown approval notice = %#v", notice)
					}
				case <-time.After(time.Second):
					t.Fatal("repeated unknown approval did not publish its durable outcome")
				}
			}
		})
	}
}

func TestTerminalInteractionNoticeUsesDurableOutcome(t *testing.T) {
	tests := []struct {
		name   string
		record interactions.Record
		want   string
	}{
		{
			name: "approval expired before execution",
			record: interactions.Record{
				Kind: interactions.KindApproval, Outcome: interactions.OutcomeTimedOut,
			},
			want: "expired before execution",
		},
		{
			name: "approval consumed before unknown result",
			record: interactions.Record{
				Kind: interactions.KindApproval, Outcome: interactions.OutcomeDeliveryUnknown,
				ApprovalConsumedAt: 1,
			},
			want: "unknown delivery outcome",
		},
		{
			name: "denied",
			record: interactions.Record{
				Kind: interactions.KindApproval, Outcome: interactions.OutcomeDenied,
			},
			want: "already denied",
		},
		{
			name: "canceled status",
			record: interactions.Record{
				Kind: interactions.KindQuestion, Status: interactions.StatusCancelled,
			},
			want: "already canceled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := terminalInteractionNotice(test.record); !strings.Contains(got, test.want) {
				t.Fatalf("terminalInteractionNotice() = %q, want substring %q", got, test.want)
			}
		})
	}
}

func TestExpiredProjectedApprovalPublishesDurableStatus(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)

	request := testToolSuspensionRequest(agent.Workspace)
	request.Prompt.Kind = interactions.KindApproval
	request.Origin.ToolName = "browser_act"
	request.Origin.ExecutionContext = &bus.InboundContext{
		Channel: request.Route.Channel, ChatID: request.Route.ChatID, SenderID: request.Route.SenderID,
	}
	argumentHash, err := interactions.HashArguments(agent.Workspace, map[string]any{"action": "delete"})
	if err != nil {
		t.Fatal(err)
	}
	request.Origin.ArgumentHash = argumentHash
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		ApprovalAction: "Delete an external resource", ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	seedTestInteractionPromptOutcomeWithMessages(
		t,
		al.outboundCoordinator(),
		agent.Workspace,
		record,
		outbox.StatusDelivered,
		1,
		[]string{"7716"},
	)
	claimed, err := registry.ClaimOverdue(time.Now().Add(2 * time.Minute))
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimOverdue() = (%#v, %v)", claimed, err)
	}
	record = claimed[0]
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: request.Route.SessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	answer := bus.InboundMessage{
		Content: "Allow once", SpoolID: "spool-expired-projected-approval",
		Context: inboundContextForInteraction(request.Route),
	}
	answer.Context.MessageID = "expired-projected-answer"
	answer.Context.Raw = map[string]string{
		bus.InboundMetadataKeyInteractionChoice:            bus.InboundInteractionChoiceAllowOnce,
		bus.InboundMetadataKeyInteractionResponse:          "Allow once",
		bus.InboundMetadataKeyInteractionShortID:           record.ShortID,
		bus.InboundMetadataKeyInteractionResponseMessageID: "7716",
	}
	if !newInboundTurnCoordinator(al).routeProjectedInteractionAnswer(t.Context(), answer, target) {
		t.Fatal("expired projected approval escaped interaction protocol routing")
	}
	select {
	case synced := <-manager.synced:
		if !synced.Metadata.RemovesInteractionControls() || synced.ReplyToMessageID != "7716" {
			t.Fatalf("expired approval control sync = %#v", synced)
		}
	case <-time.After(time.Second):
		t.Fatal("expired projected approval did not retry terminal control cleanup")
	}
	select {
	case notice := <-manager.sent:
		if !strings.Contains(notice.Content, "expired before execution") ||
			!strings.Contains(notice.Content, "was not executed") {
			t.Fatalf("expired approval notice = %#v", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("expired projected approval did not publish durable status")
	}
	stored, _ := registry.Get(record.ID)
	if stored.Outcome != interactions.OutcomeTimedOut || stored.ApprovalConsumedAt != 0 {
		t.Fatalf("expired projected approval was mutated: %#v", stored)
	}
}

func TestApprovalRecoveryUsesPersistedOriginalExecutionContext(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "call-context", Name: "approval_context",
			Arguments: map[string]any{"target": "production"},
		}}},
		{Content: "context approval finished", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	installInteractionChannelManager(t, al, newInteractionChannelManager())
	tool := &approvalContextTool{}
	agent.Tools.Register(tool)
	cleanupTool := &turnCleanupTestTool{
		countingTestTool: &countingTestTool{name: "approval-context-cleanup"},
	}
	agent.Tools.Register(cleanupTool)
	if err := al.MountHook(NamedHook("context-approval", &durableApprovalHook{
		actionSummary: "Run the context-sensitive action",
	})); err != nil {
		t.Fatal(err)
	}
	original := &bus.InboundContext{
		Channel: "telegram", Account: "bot-1", ChatID: "chat-1", ChatType: "group",
		TopicID: "topic-1", SpaceID: "space-1", SpaceType: "workspace",
		SenderID: "user-1", ActorID: "actor-1", MessageID: "origin-message",
		OriginID: "origin-1", OriginType: "forward", SourceRef: "source-1",
		ReplyToMessageID: "origin-reply", ReplyToSenderID: "reply-user",
		ReplyHandles: map[string]string{"telegram": "reply-handle"},
		Raw:          map[string]string{"thread_ts": "original-thread", "transport": "original"},
	}
	turnStatus := TurnEndStatusCompleted
	if response, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus: &turnStatus,
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-context", SessionKey: "session-context",
			UserMessage: "run context action", InboundContext: original,
		},
		DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
	}); err != nil || response != "" || turnStatus != TurnEndStatusSuspended {
		t.Fatalf("initial approval turn = (%q, %q, %v)", response, turnStatus, err)
	}

	// Mutate every map supplied by the caller, then force a registry reload to
	// model process restart before the approval answer arrives.
	original.ReplyHandles["telegram"] = "mutated"
	original.Raw["thread_ts"] = "mutated"
	al.interactions.registries.Delete(agent.Workspace)
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(registry, "session-context")
	if !ok || record.Origin.ExecutionContext == nil {
		t.Fatalf("reloaded approval interaction = %#v", record)
	}
	originExecutionID := record.Origin.ExecutionID
	record, err := registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "answer-message", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	answerContext := bus.InboundContext{
		Channel: "discord", Account: "bot-2", ChatID: "approval-chat", ChatType: "direct",
		TopicID: "topic-1", SpaceID: "space-1", SpaceType: "workspace",
		SenderID: "user-1", MessageID: "answer-message", ReplyToMessageID: "answer-reply",
		ReplyHandles: map[string]string{"discord": "answer-handle"},
		Raw:          map[string]string{"thread_ts": "answer-thread"},
	}
	if err = al.resumeClaimedInteraction(
		t.Context(), registry, agent.Workspace, agent, nil, answerContext, record,
	); err != nil {
		t.Fatalf("resumeClaimedInteraction() error = %v", err)
	}
	if tool.executions != 1 {
		t.Fatalf("protected tool executions = %d, want 1", tool.executions)
	}
	if tool.routeChannel != original.Channel || tool.routeChatID != original.ChatID {
		t.Fatalf(
			"protected tool registry route = %q/%q, want %q/%q",
			tool.routeChannel,
			tool.routeChatID,
			original.Channel,
			original.ChatID,
		)
	}
	if tool.inbound.MessageID != "origin-message" ||
		tool.inbound.ReplyToMessageID != "origin-reply" ||
		tool.inbound.ReplyHandles["telegram"] != "reply-handle" ||
		tool.inbound.Raw["thread_ts"] != "original-thread" ||
		tool.inbound.ActorID != "actor-1" || tool.inbound.SourceRef != "source-1" {
		t.Fatalf("protected tool inbound context = %#v", tool.inbound)
	}
	if cleanupTool.cleanupCalls != 1 || cleanupTool.executionID != originExecutionID ||
		cleanupTool.inbound.ActorID != "actor-1" ||
		cleanupTool.inbound.MessageID != "origin-message" {
		t.Fatalf(
			"continuation cleanup = calls %d, execution %q, inbound %#v",
			cleanupTool.cleanupCalls,
			cleanupTool.executionID,
			cleanupTool.inbound,
		)
	}
}

func TestApprovedToolHardAbortCleansOriginalExecution(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{
		ToolCalls: []providers.ToolCall{{
			ID: "call-hard-abort", Name: "approval_counting", Arguments: map[string]any{},
		}},
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	installInteractionChannelManager(t, al, newInteractionChannelManager())
	tool := &approvalCountingTool{}
	agent.Tools.Register(tool)
	cleanupTool := &turnCleanupTestTool{
		countingTestTool: &countingTestTool{name: "approved-hard-abort-cleanup"},
	}
	agent.Tools.Register(cleanupTool)
	if err := al.MountHook(NamedHook("approved-hard-abort", &durableApprovalHardAbortHook{
		durableApprovalHook: durableApprovalHook{actionSummary: "Run the aborting action"},
	})); err != nil {
		t.Fatal(err)
	}
	original := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", ActorID: "actor-1",
		MessageID: "hard-abort-origin",
	}
	turnStatus := TurnEndStatusCompleted
	if response, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus: &turnStatus,
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-hard-abort", SessionKey: "session-hard-abort",
			UserMessage: "run aborting action", InboundContext: original,
		},
		DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
	}); err != nil || response != "" || turnStatus != TurnEndStatusSuspended {
		t.Fatalf("initial approval turn = (%q, %q, %v)", response, turnStatus, err)
	}
	if cleanupTool.cleanupCalls != 0 {
		t.Fatalf("suspended cleanup calls = %d, want 0", cleanupTool.cleanupCalls)
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(registry, "session-hard-abort")
	if !ok {
		t.Fatal("approval interaction is missing")
	}
	originExecutionID := record.Origin.ExecutionID
	record, err := registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "hard-abort-answer", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	answer := bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", ActorID: "actor-2",
		MessageID: "hard-abort-answer",
	}
	if err = al.resumeClaimedInteraction(
		t.Context(), registry, agent.Workspace, agent, nil, answer, record,
	); err != nil {
		t.Fatalf("resumeClaimedInteraction() error = %v", err)
	}
	if tool.executions != 1 || cleanupTool.cleanupCalls != 1 ||
		cleanupTool.executionID != originExecutionID || cleanupTool.inbound.ActorID != "actor-1" {
		t.Fatalf(
			"approved hard-abort = executions %d, cleanup calls %d, execution %q, inbound %#v",
			tool.executions,
			cleanupTool.cleanupCalls,
			cleanupTool.executionID,
			cleanupTool.inbound,
		)
	}
}

func TestApprovedToolDescendantSuspensionDominatesHardAbort(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{
		ToolCalls: []providers.ToolCall{{
			ID: "call-suspend-hard-abort", Name: "approval_suspend_hard_abort", Arguments: map[string]any{},
		}},
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	installInteractionChannelManager(t, al, newInteractionChannelManager())
	tool := &suspendingHardAbortApprovalTool{}
	agent.Tools.Register(tool)
	cleanupTool := &turnCleanupTestTool{
		countingTestTool: &countingTestTool{name: "approved-suspension-cleanup"},
	}
	agent.Tools.Register(cleanupTool)
	if err := al.MountHook(NamedHook("approved-descendant-suspension", &durableApprovalHook{
		actionSummary: "Run the suspending action",
	})); err != nil {
		t.Fatal(err)
	}
	original := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", MessageID: "suspension-origin",
	}
	turnStatus := TurnEndStatusCompleted
	if response, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus: &turnStatus,
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-suspend-hard-abort", SessionKey: "session-suspend-hard-abort",
			UserMessage: "run suspending action", InboundContext: original,
		},
		DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
	}); err != nil || response != "" || turnStatus != TurnEndStatusSuspended {
		t.Fatalf("initial approval turn = (%q, %q, %v)", response, turnStatus, err)
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(registry, "session-suspend-hard-abort")
	if !ok {
		t.Fatal("approval interaction is missing")
	}
	record, err := registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "suspension-answer", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	answer := bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", MessageID: "suspension-answer",
	}
	if err = al.resumeClaimedInteraction(
		t.Context(), registry, agent.Workspace, agent, nil, answer, record,
	); err != nil {
		t.Fatalf("resumeClaimedInteraction() error = %v", err)
	}
	if tool.executions != 1 || cleanupTool.cleanupCalls != 0 {
		t.Fatalf(
			"approved descendant suspension = executions %d, cleanup calls %d, want 1/0",
			tool.executions,
			cleanupTool.cleanupCalls,
		)
	}
	history := agent.Sessions.GetHistory("session-suspend-hard-abort")
	_, resultIndex := interactionToolPairIndexes(history, record.Origin.ToolCallID)
	if resultIndex < 0 || !strings.Contains(history[resultIndex].Content, "durable continuation") {
		t.Fatalf("suspended approval history = %#v", history)
	}
}

func TestApprovedToolHardAbortCleansWhenJournalFails(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{
		ToolCalls: []providers.ToolCall{{
			ID: "call-hard-abort-journal", Name: "approval_hard_abort", Arguments: map[string]any{},
		}},
	}}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	installInteractionChannelManager(t, al, newInteractionChannelManager())
	tool := &hardAbortApprovalCountingTool{}
	agent.Tools.Register(tool)
	cleanupTool := &turnCleanupTestTool{
		countingTestTool: &countingTestTool{name: "approved-hard-abort-journal-cleanup"},
	}
	agent.Tools.Register(cleanupTool)
	if err := al.MountHook(NamedHook("approved-hard-abort-journal", &durableApprovalHook{
		actionSummary: "Run the aborting journal action",
	})); err != nil {
		t.Fatal(err)
	}
	original := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", ActorID: "actor-journal",
		MessageID: "hard-abort-journal-origin",
	}
	turnStatus := TurnEndStatusCompleted
	if _, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus: &turnStatus,
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-hard-abort-journal", SessionKey: "session-hard-abort-journal",
			UserMessage: "run aborting journal action", InboundContext: original,
		},
		DefaultResponse: defaultResponse, EnableSummary: true, SendResponse: false,
	}); err != nil || turnStatus != TurnEndStatusSuspended {
		t.Fatalf("initial approval turn = (%q, %v)", turnStatus, err)
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(registry, "session-hard-abort-journal")
	if !ok {
		t.Fatal("approval interaction is missing")
	}
	originExecutionID := record.Origin.ExecutionID
	record, err := registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "hard-abort-journal-answer", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	journalErr := errors.New("persist approved hard-abort result")
	agent.Sessions = &toolResultFailingJournal{SessionStore: agent.Sessions, err: journalErr}
	answer := bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", ActorID: "actor-2",
		MessageID: "hard-abort-journal-answer",
	}
	err = al.resumeClaimedInteraction(
		t.Context(), registry, agent.Workspace, agent, nil, answer, record,
	)
	if !errors.Is(err, journalErr) {
		t.Fatalf("resumeClaimedInteraction() error = %v, want %v", err, journalErr)
	}
	if tool.executions != 1 || cleanupTool.cleanupCalls != 1 ||
		cleanupTool.executionID != originExecutionID ||
		cleanupTool.inbound.ActorID != "actor-journal" {
		t.Fatalf(
			"journal hard-abort = executions %d, cleanup calls %d, execution %q, inbound %#v",
			tool.executions,
			cleanupTool.cleanupCalls,
			cleanupTool.executionID,
			cleanupTool.inbound,
		)
	}
}

type journalReceiptApprovalTool struct {
	executions int
	endpoint   string
	nonce      string
}

func (*journalReceiptApprovalTool) Name() string { return "browser_act" }

func (*journalReceiptApprovalTool) Description() string { return "Commit an approved external action" }

func (*journalReceiptApprovalTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *journalReceiptApprovalTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *toolshared.ToolResult {
	tool.executions++
	if tool.endpoint != "" {
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			tool.endpoint,
			strings.NewReader("nonce="+tool.nonce),
		)
		if err != nil {
			return toolshared.ErrorResult(err.Error())
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return toolshared.ErrorResult(err.Error())
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK || response.Header.Get("X-Smoke-Receipt") != tool.nonce {
			return toolshared.ErrorResult("external commit postcondition was not verified")
		}
	}
	return toolshared.NewToolResult(`{"invocation_id":"inv-journal","state":"succeeded"}`).
		WithWriteAudit(toolshared.WriteAuditEntry{
			Kind: "external_action", Target: "https://example.com", Action: "click",
			Tool: "browser_act", Success: true,
			Metadata: map[string]string{"invocation_id": "inv-journal", "effect": "external_commit"},
		})
}

func TestApprovedExternalReceiptSurvivesToolResultJournalFailure(t *testing.T) {
	const nonce = "mintclaw-approval-smoke"
	var submissions atomic.Int64
	fixture := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.FormValue("nonce") != nonce {
			http.Error(writer, "invalid smoke submission", http.StatusBadRequest)
			return
		}
		submissions.Add(1)
		writer.Header().Set("X-Smoke-Receipt", nonce)
		writer.WriteHeader(http.StatusOK)
	}))
	defer fixture.Close()

	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{
			ID: "call-journal-receipt", Name: "browser_act", Arguments: map[string]any{},
		}}},
		{Content: "Recovered committed action", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	installInteractionChannelManager(t, al, newInteractionChannelManager())
	tool := &journalReceiptApprovalTool{endpoint: fixture.URL, nonce: nonce}
	agent.Tools.Register(tool)
	if err := al.MountHook(NamedHook("approved-journal-receipt", &durableApprovalHook{
		actionSummary: "Commit the external action",
	})); err != nil {
		t.Fatal(err)
	}
	inbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-journal-receipt", SenderID: "user",
	}
	turnStatus := TurnEndStatusCompleted
	if _, err := al.runAgentLoop(t.Context(), agent, turnSpec{
		TurnStatus: &turnStatus,
		Dispatch: DispatchRequest{
			RouteSessionKey: "route-journal-receipt", SessionKey: "session-journal-receipt",
			UserMessage: "commit action", InboundContext: inbound,
		},
		DefaultResponse: defaultResponse, SendResponse: false,
	}); err != nil || turnStatus != TurnEndStatusSuspended {
		t.Fatalf("initial approval turn = (%q, %v)", turnStatus, err)
	}
	if submissions.Load() != 0 {
		t.Fatalf("external action ran before approval: submissions=%d", submissions.Load())
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, ok := activeInteractionForSession(registry, "session-journal-receipt")
	if !ok {
		t.Fatal("approval interaction is missing")
	}
	record, err := registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: "answer-journal-receipt", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	baseStore := agent.Sessions
	journalErr := errors.New("persist committed browser result")
	agent.Sessions = &toolResultFailingJournal{SessionStore: baseStore, err: journalErr}
	err = al.resumeClaimedInteraction(t.Context(), registry, agent.Workspace, agent, nil, *inbound, record)
	if !errors.Is(err, journalErr) {
		t.Fatalf("first resume error = %v, want %v", err, journalErr)
	}
	current, _ := registry.Get(record.ID)
	if tool.executions != 1 || submissions.Load() != 1 || len(current.OutcomeReceipts) != 1 ||
		current.OutcomeReceipts[0].ID != "inv-journal" {
		t.Fatalf(
			"post-journal interaction = %#v, executions=%d, submissions=%d",
			current,
			tool.executions,
			submissions.Load(),
		)
	}

	agent.Sessions = baseStore
	if err = al.resumeClaimedInteraction(
		t.Context(), registry, agent.Workspace, agent, nil, *inbound, current,
	); err != nil {
		t.Fatal(err)
	}
	history := agent.Sessions.GetHistory(interactionContinuationSessionKey(current))
	_, resultIndex := interactionToolPairIndexes(history, current.Origin.ToolCallID)
	if resultIndex < 0 || !strings.Contains(history[resultIndex].Content, "inv-journal") ||
		tool.executions != 1 || submissions.Load() != 1 {
		t.Fatalf(
			"recovered history = %#v, executions=%d, submissions=%d",
			history,
			tool.executions,
			submissions.Load(),
		)
	}
}

func TestExpiredAllowOnceNeverExecutesProtectedTool(t *testing.T) {
	for _, test := range []struct {
		name              string
		expireBeforeClaim bool
		reloadAfterClaim  bool
	}{
		{name: "expired when answer is claimed", expireBeforeClaim: true},
		{name: "expired before consumption after restart", reloadAfterClaim: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &sequenceProvider{responses: []*providers.LLMResponse{{
				Content: "approval expired", FinishReason: "stop",
			}}}
			al, agent, cleanup := newTurnCoordTestLoop(t, provider)
			defer cleanup()
			installInteractionChannelManager(t, al, newInteractionChannelManager())
			tool := &approvalCountingTool{}
			agent.Tools.Register(tool)
			if err := al.MountHook(NamedHook("expiry-approval", &durableApprovalHook{
				actionSummary: "Run the protected action",
			})); err != nil {
				t.Fatal(err)
			}
			sessionKey := "session-expired-approval"
			args := map[string]any{"target": "production"}
			agent.Sessions.AddFullMessage(sessionKey, providers.Message{
				Role: "assistant", ToolCalls: []providers.ToolCall{{
					ID: "call-expired", Name: tool.Name(), Arguments: args,
				}},
			})
			argumentHash, err := interactions.HashArguments(agent.Workspace, args)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1_800_000_000, 0)
			registryPath := interactions.WorkspaceStorePath(agent.Workspace)
			registry := interactions.NewRegistryWithOptions(
				registryPath,
				interactions.Options{Now: func() time.Time { return now }},
			)
			al.interactions.registries.Store(agent.Workspace, registry)
			inbound := &bus.InboundContext{
				Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
				MessageID: "origin-message",
			}
			record, err := registry.Create(interactions.CreateRequest{
				Kind: interactions.KindApproval,
				Route: interactions.Route{
					AgentID: agent.ID, SessionKey: sessionKey, RouteSessionKey: "route-expired",
					Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
				},
				Origin: interactions.Origin{
					TurnID: "turn-expired", ToolCallID: "call-expired", ToolName: tool.Name(),
					ArgumentHash: argumentHash, ExecutionContext: inbound,
				},
				PromptSummary:  "Run the protected action",
				ApprovalAction: "Run the protected action",
				ExpiresAt:      now.Add(time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			record = markTestInteractionWaiting(t, registry, record)
			if test.expireBeforeClaim {
				now = time.UnixMilli(record.ExpiresAt)
			} else {
				now = time.UnixMilli(record.ExpiresAt - 1)
			}
			record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
				Text: "allow_once", MessageID: "allow-once", ReceivedAt: now.UnixMilli(),
			}, interactions.OutcomeAllowed)
			if err != nil {
				t.Fatal(err)
			}
			if test.reloadAfterClaim {
				now = time.UnixMilli(record.ExpiresAt)
				registry = interactions.NewRegistryWithOptions(
					registryPath,
					interactions.Options{Now: func() time.Time { return now }},
				)
				if registry.LastLoadError() != nil {
					t.Fatal(registry.LastLoadError())
				}
				al.interactions.registries.Store(agent.Workspace, registry)
				record, _ = registry.Get(record.ID)
			}
			if err = al.resumeClaimedInteraction(
				t.Context(), registry, agent.Workspace, agent, nil, *inbound, record,
			); err != nil {
				t.Fatalf("resumeClaimedInteraction() error = %v", err)
			}
			resolved, _ := registry.Get(record.ID)
			if tool.executions != 0 || resolved.ApprovalConsumedAt != 0 ||
				resolved.Status != interactions.StatusResolved ||
				resolved.Outcome != interactions.OutcomeTimedOut {
				t.Fatalf("expired approval = %#v, executions=%d", resolved, tool.executions)
			}
			history := agent.Sessions.GetHistory(sessionKey)
			_, resultIndex := interactionToolPairIndexes(history, record.Origin.ToolCallID)
			if resultIndex < 0 ||
				!strings.Contains(strings.ToLower(history[resultIndex].Content), "protected tool was not executed") {
				t.Fatalf("expired approval tool result = %#v", history)
			}
		})
	}
}

func TestInteractionRouteAuthorizationRequiresTrustedEnvelope(t *testing.T) {
	route := interactions.Route{
		SessionKey: "session-1", RouteSessionKey: "route-1", Channel: "telegram",
		AccountID: "primary", ChatID: "chat-1", TopicID: "topic-1", SenderID: "user-1",
	}
	target := &inboundDispatchTarget{
		SessionKey: "session-1",
		Allocation: session.Allocation{RouteScopeKey: "route-1"},
	}
	inbound := bus.InboundContext{
		Channel: "telegram", Account: "primary", ChatID: "chat-1", TopicID: "topic-1",
		SenderID: "user-1",
	}
	if !interactionRouteAuthorizes(route, target, inbound) {
		t.Fatal("matching trusted envelope was rejected")
	}
	inbound.SenderID = "user-2"
	if interactionRouteAuthorizes(route, target, inbound) {
		t.Fatal("different sender was authorized")
	}
	inbound.SenderID = "user-1"
	inbound.TopicID = "topic-2"
	if interactionRouteAuthorizes(route, target, inbound) {
		t.Fatal("different topic was authorized")
	}
}

func TestInteractionIngressOnlyClaimsAuthorizedAnswers(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	workspace := agent.Workspace
	request := testToolSuspensionRequest(workspace)
	agent.Sessions.AddFullMessage(request.Route.SessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: request.Origin.ToolCallID, Name: request.Origin.ToolName, Arguments: map[string]any{},
		}},
	})
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	waitingRevision := record.Revision
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: request.Route.SessionKey,
		RouteClaimKey: runtimeRouteClaimKey(request.Route.RouteSessionKey, ""),
		Allocation:    session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	msg := bus.InboundMessage{Content: "Canary", Context: inboundContextForInteraction(request.Route)}
	if !al.shouldHandleInteractionInbound(msg, target) {
		t.Fatal("authorized plain answer was not claimed")
	}
	msg.Content = "unrelated message"
	msg.Context.SenderID = "someone-else"
	if al.shouldHandleInteractionInbound(msg, target) {
		t.Fatal("unrelated sender message was consumed as an interaction answer")
	}
	msg.Content = "/reset"
	msg.Context.SenderID = request.Route.SenderID
	if al.shouldHandleInteractionInbound(msg, target) {
		t.Fatal("control command was consumed as an interaction answer")
	}
	msg.Content = "/answerfoo"
	if al.shouldHandleInteractionInbound(msg, target) {
		t.Fatal("command-prefix collision was consumed as an interaction answer")
	}
	for _, malformedCommand := range []string{
		fmt.Sprintf("/answer@bot@junk %s yes", record.ShortID),
		fmt.Sprintf("/answer@bot/path %s yes", record.ShortID),
		fmt.Sprintf("/answer@ %s yes", record.ShortID),
	} {
		msg.Content = malformedCommand
		if al.shouldHandleInteractionInbound(msg, target) {
			t.Errorf("malformed answer command %q was consumed", malformedCommand)
		}
		current, _ := registry.Get(record.ID)
		if current.Status != interactions.StatusWaiting ||
			current.Revision != waitingRevision ||
			current.Answer != nil {
			t.Errorf("malformed answer command %q mutated interaction: %#v", malformedCommand, current)
		}
	}
	msg.Content = "/reset"
	result, err := al.cancelInteractionForControlMessage(t.Context(), msg, target)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || !result.Canceled || result.Failed || result.CommandHandled {
		t.Fatalf("reset cancellation result = %#v", result)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled {
		t.Fatalf("reset did not cancel pending interaction: %#v", record)
	}
}

func TestInteractionIngressRetainsClaimedAnswerReplayOwnership(t *testing.T) {
	workspace := t.TempDir()
	al := &AgentLoop{cfg: config.DefaultConfig()}
	request := testToolSuspensionRequest(workspace)
	registry := al.interactionRegistryForWorkspace(workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", Values: map[string]string{"deploy_mode": "Canary"}, MessageID: "answer-1",
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	target := &inboundDispatchTarget{
		Agent: &AgentInstance{Workspace: workspace}, SessionKey: request.Route.SessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	msg := bus.InboundMessage{Content: "Canary", Context: inboundContextForInteraction(request.Route)}
	msg.Context.MessageID = "answer-1"
	if !al.shouldHandleInteractionInbound(msg, target) {
		t.Fatal("claimed answer replay escaped interaction dispatch")
	}
	if !interactionInboundReplaysAnswer(record, msg.Context) {
		t.Fatal("persisted answer replay was not recognized")
	}
	msg.Context.MessageID = "answer-2"
	if !al.shouldHandleInteractionInbound(msg, target) {
		t.Fatal("new authorized message escaped the owned interaction session")
	}
	if interactionInboundReplaysAnswer(record, msg.Context) {
		t.Fatal("different message was recognized as the persisted answer")
	}
	msg.Context.SenderID = "user-2"
	if al.shouldHandleInteractionInbound(msg, target) {
		t.Fatal("unrelated sender was consumed by the claimed interaction")
	}
}

func TestClaimedAnswerIsNotReleasedAfterResumeFailure(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	al.channelManager = manager
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)
	sessionKey := "session-claimed-spool-ownership"
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: sessionKey,
		RouteClaimKey: runtimeRouteClaimKey(request.Route.RouteSessionKey, ""),
		Allocation:    session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	msg := bus.InboundMessage{
		Content: "Canary", SpoolID: "spool-claimed-answer",
		Context: inboundContextForInteraction(request.Route),
	}
	msg.Context.MessageID = "answer-claimed"
	scope := newRuntimeSessionScope(agent.Workspace, sessionKey)
	claim, claimed := al.turns.claimRuntimeSession(scope, "test-claimed-spool")
	if !claimed {
		t.Fatal("failed to claim test session")
	}

	// No originating tool call exists, so continuation fails after ClaimAnswer.
	newInboundTurnCoordinator(al).runInteractionWorker(t.Context(), msg, target, claim)
	acked, released := tracker.counts()
	if acked != 1 || released != 0 {
		t.Fatalf("spool ownership = acked:%d released:%d, want 1/0", acked, released)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusClaimed {
		t.Fatalf("record status = %q, want claimed recovery ownership", record.Status)
	}
	select {
	case synced := <-manager.synced:
		if !synced.Metadata.RemovesInteractionControls() {
			t.Fatalf("claimed answer control sync = %#v", synced)
		}
	case <-time.After(time.Second):
		t.Fatal("claimed answer did not clear projected controls")
	}
}

func TestAdditionalMessageDuringResumeIsDeferred(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)
	sessionKey := "session-resume-additional-input"
	continuationSessionKey := "task-resume-additional-input"
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	request.Origin.ContinuationSessionKey = continuationSessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	if _, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", Values: map[string]string{"deploy_mode": "Canary"}, MessageID: "answer-1",
	}, interactions.OutcomeAnswered); err != nil {
		t.Fatal(err)
	}
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: sessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	msg := bus.InboundMessage{
		Content: "Use staging instead", SpoolID: "spool-correction",
		Context: inboundContextForInteraction(request.Route),
	}
	msg.Context.MessageID = "answer-2"
	ownerScope := newRuntimeSessionScope(agent.Workspace, sessionKey)
	claim, claimed := al.turns.claimRuntimeSession(ownerScope, "test-active-resume")
	if !claimed {
		t.Fatal("failed to claim test session")
	}
	defer claim.releaseIfOwned()
	flightKey, flight, flightOwner := al.startInteractionResumeFlight(agent.Workspace, record.ID)
	if !flightOwner {
		t.Fatal("failed to own test interaction resume flight")
	}
	if err := configureInteractionSteeringHandoff(
		flight, agent.Workspace, record, agent,
	); err != nil {
		t.Fatal(err)
	}
	defer al.finishInteractionResumeFlight(flightKey, flight, true, nil)

	newInboundTurnCoordinator(al).handleInteractionInbound(t.Context(), msg, target)
	acked, released := tracker.counts()
	if acked != 0 || released != 0 {
		t.Fatalf("deferred spool ownership = acked:%d released:%d, want 0/0", acked, released)
	}
	continuationScope := newRuntimeSessionScope(agent.Workspace, continuationSessionKey)
	if got := al.pendingSteeringCountForScope(continuationScope); got != 1 {
		t.Fatalf("deferred queue depth = %d, want 1", got)
	}
	if got := al.pendingSteeringCountForScope(ownerScope); got != 0 {
		t.Fatalf("owner queue depth = %d, want 0", got)
	}
	queued := al.dequeueSteeringMessagesForTurn(continuationScope, request.Route.SenderID)
	if len(queued) != 1 || queued[0].InboundSpoolID != "spool-correction" {
		t.Fatalf("deferred message = %#v", queued)
	}
}

func TestPlainGuidanceSupersedesPendingApprovalAndResumesOriginatingContinuation(t *testing.T) {
	provider := &interactionCaptureProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)

	const (
		ownerSession        = "owner-approval-steering"
		continuationSession = "task-approval-steering"
		guidance            = "Открой All postings и найди микроволновку там"
	)
	ensureSessionMetadata(agent.Sessions, continuationSession, &session.SessionScope{
		Version: session.ScopeVersion, AgentID: agent.ID, Channel: "telegram", RouteScopeKey: "route-owner",
	})
	agent.Sessions.AddFullMessage(continuationSession, providers.Message{
		Role: "user", Content: "Find the expired microwave listing",
	})
	agent.Sessions.AddFullMessage(continuationSession, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-pending-click", Name: "browser_act", Arguments: map[string]any{},
		}},
	})
	inbound := bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", ChatType: "direct",
		SenderID: "user-1", MessageID: "guidance-1",
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: interactions.KindApproval,
		Route: interactions.Route{
			AgentID: agent.ID, SessionKey: ownerSession, RouteSessionKey: "route-owner",
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
		Origin: interactions.Origin{
			TurnID: "turn-pending-click", ToolCallID: "call-pending-click", ToolName: "browser_act",
			ContinuationSessionKey: continuationSession, ArgumentHash: strings.Repeat("a", 64),
			ExecutionContext: &inbound,
		},
		PromptSummary:  "Approve the pending browser click",
		ApprovalAction: "Click search",
		ExpiresAt:      time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: ownerSession,
		RouteClaimKey: runtimeRouteClaimKey("route-owner", ""),
		Allocation:    session.Allocation{RouteScopeKey: "route-owner"},
	}
	msg := bus.InboundMessage{
		Content: guidance, SpoolID: "spool-guidance", Context: inbound,
	}
	if !al.shouldHandleInteractionInbound(msg, target) {
		t.Fatal("plain guidance escaped interaction-aware routing")
	}
	ownership, _, err := al.processInteractionInbound(t.Context(), msg, target)
	if err != nil || ownership != interactionInboundClaimed {
		t.Fatalf("processInteractionInbound() = ownership:%v err:%v", ownership, err)
	}

	current, _ := registry.Get(record.ID)
	if current.Status != interactions.StatusResolved || current.Outcome != interactions.OutcomeDenied ||
		current.Answer == nil || !current.Answer.Superseded || current.Answer.Text != guidance {
		t.Fatalf("superseded interaction = %#v", current)
	}
	var sawGuidance bool
	for _, message := range provider.messages {
		if message.Role == "user" && strings.Contains(message.Content, guidance) {
			sawGuidance = true
		}
	}
	if !sawGuidance {
		t.Fatalf("resumed request missing guidance: %#v", provider.messages)
	}
	_, resultIndex := interactionToolPairIndexes(
		agent.Sessions.GetHistory(continuationSession),
		"call-pending-click",
	)
	if resultIndex < 0 || !strings.Contains(
		agent.Sessions.GetHistory(continuationSession)[resultIndex].Content,
		"superseded by new user guidance",
	) {
		t.Fatalf("superseded tool result missing from continuation history")
	}
	acked, released := tracker.counts()
	if acked != 1 || released != 0 {
		t.Fatalf("guidance spool ownership = acked:%d released:%d, want 1/0", acked, released)
	}
}

func TestExplicitApprovalDecisionDoesNotBecomeSteering(t *testing.T) {
	record := interactions.Record{Kind: interactions.KindApproval, ShortID: "apr123"}
	for _, content := range []string{"allow_once", "allow", "deny", "/answer apr123 allow_once"} {
		msg := bus.InboundMessage{Content: content}
		if interactionApprovalSupersededByInbound(record, msg) {
			t.Fatalf("approval decision %q was classified as steering", content)
		}
	}
	if !interactionApprovalSupersededByInbound(
		record,
		bus.InboundMessage{Content: "Open All postings instead"},
	) {
		t.Fatal("plain correction was not classified as superseding guidance")
	}
}

func TestConcurrentExplicitInteractionAnswersNeverBecomeSteering(t *testing.T) {
	provider := newBlockingInteractionProvider()
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)

	sessionKey := "session-concurrent-explicit-answers"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "user", Content: "Choose a value"})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-concurrent-answer", Name: "request_user_input", Arguments: map[string]any{},
		}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	request.Route.TopicID = "topic-1"
	request.Origin.ToolCallID = "call-concurrent-answer"
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	target := &inboundDispatchTarget{
		Agent:         agent,
		SessionKey:    sessionKey,
		RouteClaimKey: runtimeRouteClaimKey(request.Route.RouteSessionKey, ""),
		Allocation:    session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	first := bus.InboundMessage{
		Content: "/answer " + record.ShortID + " первый", SpoolID: "spool-answer-first",
		Context: inboundContextForInteraction(request.Route),
	}
	first.Context.MessageID = "answer-first"
	coordinator := newInboundTurnCoordinator(al)
	if !coordinator.routeExplicitInteractionAnswer(t.Context(), first, target) {
		t.Fatal("first explicit answer was not classified as interaction protocol")
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the accepted interaction continuation")
	}

	contenders := []bus.InboundMessage{
		first,
		{
			Content: "/answer " + record.ShortID + " первый", SpoolID: "spool-answer-same",
			Context: inboundContextForInteraction(request.Route),
		},
		{
			Content: "/answer " + record.ShortID + " второй", SpoolID: "spool-answer-second",
			Context: inboundContextForInteraction(request.Route),
		},
		{
			Content: "/answer deadbeef второй", SpoolID: "spool-answer-wrong-id",
			Context: inboundContextForInteraction(request.Route),
		},
		{
			Content: "/answer " + record.ShortID + " второй", SpoolID: "spool-answer-wrong-sender",
			Context: inboundContextForInteraction(request.Route),
		},
		{
			Content: "/answer " + record.ShortID + " второй", SpoolID: "spool-answer-wrong-chat",
			Context: inboundContextForInteraction(request.Route),
		},
		{
			Content: "/answer " + record.ShortID + " второй", SpoolID: "spool-answer-wrong-topic",
			Context: inboundContextForInteraction(request.Route),
		},
		{
			Content: "Allow once", SpoolID: "spool-projected-answer-second",
			Context: inboundContextForInteraction(request.Route),
		},
	}
	contenders[0].SpoolID = "spool-answer-replay"
	contenders[1].Context.MessageID = "answer-same"
	contenders[2].Context.MessageID = "answer-second"
	contenders[3].Context.MessageID = "answer-wrong-id"
	contenders[4].Context.MessageID = "answer-wrong-sender"
	contenders[4].Context.SenderID = "user-2"
	contenders[5].Context.MessageID = "answer-wrong-chat"
	contenders[5].Context.ChatID = "chat-2"
	contenders[6].Context.MessageID = "answer-wrong-topic"
	contenders[6].Context.TopicID = "topic-2"
	contenders[7].Context.MessageID = "projected-answer-second"
	contenders[7].Context.Raw = map[string]string{
		bus.InboundMetadataKeyInteractionChoice:   bus.InboundInteractionChoiceAllowOnce,
		bus.InboundMetadataKeyInteractionResponse: "Allow once",
		bus.InboundMetadataKeyInteractionShortID:  record.ShortID,
	}
	for _, contender := range contenders {
		if _, projected := projectedInteractionAnswer(contender); projected {
			if !coordinator.routeProjectedInteractionAnswer(t.Context(), contender, target) {
				t.Fatalf("projected contender escaped protocol routing: %q", contender.Content)
			}
		} else if !coordinator.routeExplicitInteractionAnswer(t.Context(), contender, target) {
			t.Fatalf("explicit contender escaped protocol routing: %q", contender.Content)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		acked, released := tracker.counts()
		if acked == 1+len(contenders) {
			if released != 0 {
				t.Fatalf("interaction ingress released %d spool entries", released)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("spool acknowledgements = %d, want %d", acked, 1+len(contenders))
		}
		time.Sleep(time.Millisecond)
	}
	scope := newRuntimeSessionScope(agent.Workspace, sessionKey)
	if got := al.pendingSteeringCountForScope(scope); got != 0 {
		t.Fatalf("explicit answer contenders entered steering queue: %d", got)
	}
	close(provider.release)

	deadline = time.Now().Add(2 * time.Second)
	for {
		record, _ = registry.Get(record.ID)
		if record.Status == interactions.StatusResolved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("interaction did not resolve: %#v", record)
		}
		time.Sleep(time.Millisecond)
	}
	if record.Answer == nil || record.Answer.Text != "первый" ||
		record.Answer.MessageID != "answer-first" {
		t.Fatalf("durable winner = %#v", record.Answer)
	}
	if record.ResumeTries != 1 || len(record.FinalDeliveryIDs) != 1 {
		t.Fatalf("continuation/delivery counts = %#v", record)
	}
	eventCounts := map[interactions.EventType]int{}
	for _, event := range registry.ListEvents(record.ID) {
		eventCounts[event.Type]++
	}
	if eventCounts[interactions.EventAnswerClaimed] != 1 ||
		eventCounts[interactions.EventResumeStarted] != 1 {
		t.Fatalf("interaction event counts = %#v", eventCounts)
	}
	calls, modelCalls := provider.snapshot()
	if calls != 1 || len(modelCalls) != 1 {
		t.Fatalf("provider calls = %d, messages = %d", calls, len(modelCalls))
	}
	for _, message := range modelCalls[0] {
		if strings.Contains(message.Content, "/answer") || strings.Contains(message.Content, "второй") {
			t.Fatalf("losing answer entered model context: %#v", modelCalls[0])
		}
	}
	for _, message := range agent.Sessions.GetHistory(sessionKey) {
		if strings.Contains(message.Content, "/answer") || strings.Contains(message.Content, "второй") {
			t.Fatalf("losing answer entered conversation history: %#v", message)
		}
	}
	finals := 0
	for len(manager.sent) > 0 {
		outbound := <-manager.sent
		if outbound.Content == "DUPLICATE_INTERACTION_OK: первый" {
			finals++
		}
	}
	if finals != 1 {
		t.Fatalf("final responses = %d, want 1", finals)
	}

	terminalReplay := first
	terminalReplay.SpoolID = "spool-answer-terminal-replay"
	if !coordinator.routeExplicitInteractionAnswer(t.Context(), terminalReplay, target) {
		t.Fatal("terminal replay escaped interaction protocol routing")
	}
	acked, released := tracker.counts()
	if acked != 2+len(contenders) || released != 0 {
		t.Fatalf("terminal replay ownership = acked:%d released:%d", acked, released)
	}
	if callsAfterReplay, _ := provider.snapshot(); callsAfterReplay != 1 {
		t.Fatalf("terminal replay started another continuation: %d calls", callsAfterReplay)
	}
}

func TestExplicitAnswerContentionReleasesBeforeDurableAnswerAdmission(t *testing.T) {
	for _, test := range []struct {
		name        string
		markWaiting bool
		projected   bool
		unresolved  bool
		consumed    bool
		status      interactions.Status
	}{
		{name: "created_after_delivery", projected: true, status: interactions.StatusCreated},
		{name: "waiting", markWaiting: true, status: interactions.StatusWaiting},
		{
			name: "unresolved_waiting_callback", markWaiting: true, projected: true,
			unresolved: true, consumed: true, status: interactions.StatusWaiting,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
			defer cleanup()
			installInteractionChannelManager(t, al, newInteractionChannelManager())
			tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
			setTestMessageBus(al, tracker)
			sessionKey := "session-answer-admission-contention-" + test.name
			request := testToolSuspensionRequest(agent.Workspace)
			request.Route.SessionKey = sessionKey
			registry := al.interactionRegistryForWorkspace(agent.Workspace)
			record, err := registry.Create(interactions.CreateRequest{
				Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
				Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
			})
			if err != nil {
				t.Fatal(err)
			}
			record = bindTestInteractionPrompt(t, registry, record)
			if test.projected {
				seedTestInteractionPromptOutcomeWithMessages(
					t,
					al.outboundCoordinator(),
					agent.Workspace,
					record,
					outbox.StatusDelivered,
					1,
					[]string{"7716"},
				)
			}
			if test.markWaiting {
				record, _ = registry.MarkWaiting(record.ID, record.Revision)
			}
			target := &inboundDispatchTarget{
				Agent: agent, SessionKey: sessionKey,
				Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
			}
			scope := newRuntimeSessionScope(agent.Workspace, sessionKey)
			blocker, claimed := al.turns.claimRuntimeSession(scope, "answer-admission-blocker")
			if !claimed {
				t.Fatal("failed to claim the interaction session blocker")
			}
			defer blocker.releaseIfOwned()

			contender := bus.InboundMessage{
				Content: "/answer " + record.ShortID + " valid",
				SpoolID: "spool-admission-contender-" + test.name,
				Context: inboundContextForInteraction(request.Route),
			}
			contender.Context.MessageID = "admission-contender-" + test.name
			coordinator := newInboundTurnCoordinator(al)
			if test.projected {
				contender.Content = "Allow once"
				contender.Context.Raw = map[string]string{
					bus.InboundMetadataKeyInteractionChoice:            bus.InboundInteractionChoiceAllowOnce,
					bus.InboundMetadataKeyInteractionResponse:          "Allow once",
					bus.InboundMetadataKeyInteractionShortID:           record.ShortID,
					bus.InboundMetadataKeyInteractionResponseMessageID: "7716",
				}
				if test.unresolved {
					delete(contender.Context.Raw, bus.InboundMetadataKeyInteractionChoice)
					delete(contender.Context.Raw, bus.InboundMetadataKeyInteractionResponse)
					contender.Context.Raw[bus.InboundMetadataKeyInteractionResponseError] = "unresolved callback option"
				}
				if !coordinator.routeProjectedInteractionAnswer(t.Context(), contender, target) {
					t.Fatal("projected pre-admission contender escaped protocol routing")
				}
			} else if !coordinator.routeExplicitInteractionAnswer(t.Context(), contender, target) {
				t.Fatal("explicit pre-admission contender escaped protocol routing")
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				acked, released := tracker.counts()
				if test.consumed && acked == 1 && released == 0 {
					break
				}
				if !test.consumed && released == 1 && acked == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf(
						"timed out waiting for answer ownership: acked=%d released=%d",
						acked,
						released,
					)
				}
				time.Sleep(time.Millisecond)
			}
			record, _ = registry.Get(record.ID)
			if record.Status != test.status || record.Answer != nil {
				t.Fatalf("runtime contention chose a durable answer: %#v", record)
			}
			if got := al.pendingSteeringCountForScope(scope); got != 0 {
				t.Fatalf("released answer entered steering queue: %d", got)
			}
			for _, event := range registry.ListEvents(record.ID) {
				if event.Type == interactions.EventAnswerClaimed ||
					event.Type == interactions.EventResumeStarted {
					t.Fatalf("contention emitted durable transition: %#v", event)
				}
			}
		})
	}
}

func TestRetainedAnswerReplayPrecedesNewActiveInteractionWrongID(t *testing.T) {
	provider := &sequenceProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)
	sessionKey := "session-retained-replay"
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	first, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	first = markTestInteractionWaiting(t, registry, first)
	first, err = registry.ClaimAnswer(first.ID, first.Revision, interactions.Answer{
		Text: "первый", Values: map[string]string{"deploy_mode": "первый"},
		MessageID: "retained-answer",
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	first, err = registry.MarkResuming(first.ID, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Resolve(first.ID, first.Revision); err != nil {
		t.Fatal(err)
	}
	request.Origin.ToolCallID = "call-next-question"
	second, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	second = markTestInteractionWaiting(t, registry, second)
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: sessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	replay := bus.InboundMessage{
		Content: "/answer " + first.ShortID + " первый", SpoolID: "spool-retained-replay",
		Context: inboundContextForInteraction(request.Route),
	}
	replay.Context.MessageID = "retained-answer"
	if !newInboundTurnCoordinator(al).routeExplicitInteractionAnswer(t.Context(), replay, target) {
		t.Fatal("retained replay escaped interaction protocol routing")
	}
	staleButton := bus.InboundMessage{
		Content: bus.InboundInteractionCancelLabel, SpoolID: "spool-retained-stale-button",
		Context: inboundContextForInteraction(request.Route),
	}
	staleButton.Context.MessageID = "later-button-message"
	staleButton.Context.Raw = map[string]string{
		bus.InboundMetadataKeyInteractionChoice:  bus.InboundInteractionChoiceCancel,
		bus.InboundMetadataKeyInteractionShortID: first.ShortID,
	}
	newInboundTurnCoordinator(al).handleInbound(t.Context(), staleButton)
	identitylessCancel := staleButton
	identitylessCancel.SpoolID = "spool-retained-identityless-cancel"
	identitylessCancel.Context.MessageID = "identityless-cancel"
	delete(identitylessCancel.Context.Raw, bus.InboundMetadataKeyInteractionShortID)
	newInboundTurnCoordinator(al).handleInbound(t.Context(), identitylessCancel)
	acked, released := tracker.counts()
	if acked != 3 || released != 0 {
		t.Fatalf("retained replay ownership = acked:%d released:%d", acked, released)
	}
	second, _ = registry.Get(second.ID)
	if second.Status != interactions.StatusWaiting || second.Answer != nil {
		t.Fatalf("retained replay mutated the new interaction: %#v", second)
	}
	if got := al.pendingSteeringCountForScope(
		newRuntimeSessionScope(agent.Workspace, sessionKey),
	); got != 0 {
		t.Fatalf("retained replay entered steering queue: %d", got)
	}
	provider.mu.Lock()
	calls := provider.callCount
	provider.mu.Unlock()
	if calls != 0 {
		t.Fatalf("retained replay started %d continuation(s)", calls)
	}
	select {
	case outbound := <-tracker.OutboundChan():
		t.Fatalf("retained replay produced a notice: %#v", outbound)
	default:
	}
}

func TestProjectedAnswerMatchesDurablePromptAcrossRetainedShortIDCollision(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = "session-prompt-identity-collision"

	create := func(id, toolCallID, platformMessageID string) interactions.Record {
		record, err := registry.Create(interactions.CreateRequest{
			ID: id, Kind: request.Prompt.Kind, Route: request.Route,
			Origin: interactions.Origin{
				TurnID: request.Origin.TurnID, ToolCallID: toolCallID,
				ToolName: request.Origin.ToolName,
			},
			Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		record = markTestInteractionWaiting(t, registry, record)
		seedTestInteractionPromptOutcomeWithMessages(
			t,
			al.outboundCoordinator(),
			agent.Workspace,
			record,
			outbox.StatusDelivered,
			1,
			[]string{platformMessageID},
		)
		return record
	}

	first := create("interaction_deadbeef11111111", "call-prompt-old", "100")
	first, err := registry.ClaimAnswer(first.ID, first.Revision, interactions.Answer{
		Text: "old", Values: map[string]string{"deploy_mode": "old"}, MessageID: "old-answer",
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	first, err = registry.MarkResuming(first.ID, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Resolve(first.ID, first.Revision); err != nil {
		t.Fatal(err)
	}
	second := create("interaction_deadbeef22222222", "call-prompt-new", "200")
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: request.Route.SessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	callback := func(messageID string) bus.InboundMessage {
		msg := bus.InboundMessage{Context: inboundContextForInteraction(request.Route)}
		msg.Context.MessageID = "callback-" + messageID
		msg.Context.Raw = map[string]string{
			bus.InboundMetadataKeyInteractionChoice:            bus.InboundInteractionChoiceAllowOnce,
			bus.InboundMetadataKeyInteractionResponse:          "Allow once",
			bus.InboundMetadataKeyInteractionShortID:           second.ShortID,
			bus.InboundMetadataKeyInteractionResponseMessageID: messageID,
		}
		return msg
	}

	classification := al.classifyProjectedInteractionAnswer(callback("200"), target, second.ShortID)
	if classification.Disposition != explicitInteractionAnswerActive || classification.Record.ID != second.ID {
		t.Fatalf("new prompt classification = %#v", classification)
	}
	classification = al.classifyProjectedInteractionAnswer(callback("100"), target, second.ShortID)
	if classification.Disposition != explicitInteractionAnswerDuplicate || classification.Record.ID != first.ID {
		t.Fatalf("retained old prompt classification = %#v", classification)
	}
	if err = registry.Prune(time.Now().Add(8 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	classification = al.classifyProjectedInteractionAnswer(callback("100"), target, second.ShortID)
	if classification.Disposition == explicitInteractionAnswerActive {
		t.Fatalf("pruned old prompt authorized the new interaction: %#v", classification)
	}
	classification = al.classifyProjectedInteractionAnswer(callback("200"), target, second.ShortID)
	if classification.Disposition != explicitInteractionAnswerActive || classification.Record.ID != second.ID {
		t.Fatalf("new prompt after old record pruning = %#v", classification)
	}
}

func TestProjectedAnswerRetriesUntilPromptReceiptIsDurable(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	coordinator := installInteractionChannelManager(t, al, newInteractionChannelManager())
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = "session-prompt-receipt-race"
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionPrompt(t, registry, record)
	message := interactionPromptMessage(record)
	message.Content = renderInteractionPrompt(record)
	admission, err := coordinator.AdmitMessage(
		agent.Workspace,
		interactionPromptDeliveryIdentity(record),
		message,
	)
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMessage() = (%#v, %v)", admission, err)
	}
	if err = coordinator.PrepareAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.CommitAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
		t.Fatal(err)
	}
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: request.Route.SessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	callback := bus.InboundMessage{Context: inboundContextForInteraction(request.Route)}
	callback.Context.MessageID = "callback-fast"
	callback.Content = "Interaction option 1"
	callback.Context.Raw = map[string]string{
		bus.InboundMetadataKeyInteractionOptionIndex:       "0",
		bus.InboundMetadataKeyInteractionResponseError:     "unresolved callback option",
		bus.InboundMetadataKeyInteractionShortID:           record.ShortID,
		bus.InboundMetadataKeyInteractionResponseMessageID: "7716",
	}

	classification := al.classifyProjectedInteractionAnswer(callback, target, record.ShortID)
	if classification.Disposition != explicitInteractionAnswerRetry || classification.Record.ID != record.ID {
		t.Fatalf("attempting prompt classification = %#v", classification)
	}
	if err = coordinator.MarkDelivered(admission.Intent.ID, outbox.Outcome{
		PlatformMessageIDs: []string{"7716"},
	}); err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkWaiting(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	classification = al.classifyProjectedInteractionAnswer(callback, target, record.ShortID)
	if classification.Disposition != explicitInteractionAnswerActive || classification.Record.ID != record.ID {
		t.Fatalf("delivered prompt replay classification = %#v", classification)
	}
	callback = resolveProjectedInteractionOption(classification.Record, callback)
	if callback.Context.Raw[bus.InboundMetadataKeyInteractionResponseError] != "" ||
		callback.Context.Raw[bus.InboundMetadataKeyInteractionResponse] != "Canary" ||
		callback.Content != "Canary" {
		t.Fatalf("replayed option callback = %#v", callback)
	}
}

func TestProjectedAnswerUsesOrdinaryTelegramReplyPromptIdentity(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = "session-ordinary-reply-identity"
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	seedTestInteractionPromptOutcomeWithMessages(
		t,
		al.outboundCoordinator(),
		agent.Workspace,
		record,
		outbox.StatusDelivered,
		1,
		[]string{"7716"},
	)
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: request.Route.SessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	reply := bus.InboundMessage{Context: inboundContextForInteraction(request.Route)}
	reply.Context.MessageID = "reply-1"
	reply.Context.ReplyToMessageID = "7716"
	reply.Context.Raw = map[string]string{
		bus.InboundMetadataKeyInteractionResponse: "generate it yourself",
		bus.InboundMetadataKeyInteractionShortID:  record.ShortID,
	}

	classification := al.classifyProjectedInteractionAnswer(reply, target, record.ShortID)
	if classification.Disposition != explicitInteractionAnswerActive || classification.Record.ID != record.ID {
		t.Fatalf("ordinary Telegram reply classification = %#v", classification)
	}

	wrongPrompt := reply
	wrongPrompt.SpoolID = "spool-ordinary-reply-wrong-prompt"
	wrongPrompt.Context.MessageID = "reply-old-prompt"
	wrongPrompt.Context.ReplyToMessageID = "7715"
	classification = al.classifyProjectedInteractionAnswer(wrongPrompt, target, record.ShortID)
	if classification.Disposition != explicitInteractionAnswerWrongID {
		t.Fatalf("old Telegram prompt classification = %#v", classification)
	}
	if !newInboundTurnCoordinator(al).routeProjectedInteractionAnswer(t.Context(), wrongPrompt, target) {
		t.Fatal("old Telegram prompt reply escaped interaction protocol routing")
	}
	select {
	case synced := <-manager.synced:
		t.Fatalf("wrong prompt identity triggered control sync: %#v", synced)
	default:
	}

	unauthorized := reply
	unauthorized.SpoolID = "spool-ordinary-reply-unauthorized"
	unauthorized.Context.MessageID = "reply-unauthorized"
	unauthorized.Context.SenderID = "other-sender"
	classification = al.classifyProjectedInteractionAnswer(unauthorized, target, record.ShortID)
	if classification.Disposition != explicitInteractionAnswerUnauthorized {
		t.Fatalf("unauthorized Telegram reply classification = %#v", classification)
	}
	if !newInboundTurnCoordinator(al).routeProjectedInteractionAnswer(t.Context(), unauthorized, target) {
		t.Fatal("unauthorized Telegram reply escaped interaction protocol routing")
	}
	select {
	case synced := <-manager.synced:
		t.Fatalf("unauthorized prompt reply triggered control sync: %#v", synced)
	default:
	}
}

func TestProjectedMultiQuestionReplyRequiresDurablePromptIdentity(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	installInteractionChannelManager(t, al, newInteractionChannelManager())
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = "session-multi-reply-identity"
	questions := append([]interactions.Question(nil), request.Prompt.Questions...)
	questions = append(questions, interactions.Question{ID: "test_mode", Question: "Which mode?"})
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	seedTestInteractionPromptOutcomeWithMessages(
		t,
		al.outboundCoordinator(),
		agent.Workspace,
		record,
		outbox.StatusDelivered,
		1,
		[]string{"8800"},
	)
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: request.Route.SessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	reply := bus.InboundMessage{Content: "test_region: eu\ntest_mode: safe"}
	reply.Context = inboundContextForInteraction(request.Route)
	reply.Context.MessageID = "multi-reply"
	reply.Context.ReplyToMessageID = "8800"
	reply.Context.Raw = map[string]string{
		bus.InboundMetadataKeyInteractionResponse: reply.Content,
		bus.InboundMetadataKeyInteractionShortID:  record.ShortID,
	}

	classification := al.classifyProjectedInteractionAnswer(reply, target, record.ShortID)
	if classification.Disposition != explicitInteractionAnswerActive || classification.Record.ID != record.ID {
		t.Fatalf("confirmed multi-question prompt classification = %#v", classification)
	}
	reply.Context.ReplyToMessageID = "8799"
	classification = al.classifyProjectedInteractionAnswer(reply, target, record.ShortID)
	if classification.Disposition != explicitInteractionAnswerWrongID {
		t.Fatalf("other multi-question prompt classification = %#v", classification)
	}
}

func TestStaleCancelCallbackCannotCancelNewerShortIDCollision(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	coordinator := installInteractionChannelManager(t, al, manager)
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = "session-stale-cancel-collision"

	create := func(id, toolCallID, platformMessageID string) interactions.Record {
		record, err := registry.Create(interactions.CreateRequest{
			ID: id, Kind: request.Prompt.Kind, Route: request.Route,
			Origin: interactions.Origin{
				TurnID: request.Origin.TurnID, ToolCallID: toolCallID,
				ToolName: request.Origin.ToolName,
			},
			Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		record = markTestInteractionWaiting(t, registry, record)
		seedTestInteractionPromptOutcomeWithMessages(
			t,
			coordinator,
			agent.Workspace,
			record,
			outbox.StatusDelivered,
			1,
			[]string{platformMessageID},
		)
		return record
	}

	old := create("interaction_deadbeef11111111", "call-stale-cancel", "100")
	if _, err := registry.Cancel(old.ID, old.Revision, "test_cancel"); err != nil {
		t.Fatal(err)
	}
	current := create("interaction_deadbeef22222222", "call-current-cancel", "200")
	callback := bus.InboundMessage{
		Content: bus.InboundInteractionCancelLabel, SpoolID: "spool-stale-cancel",
		Context: inboundContextForInteraction(request.Route),
	}
	callback.Context.MessageID = "callback-stale-cancel"
	callback.Context.ReplyToMessageID = "100"
	callback.Context.Raw = map[string]string{
		bus.InboundMetadataKeyInteractionChoice:            bus.InboundInteractionChoiceCancel,
		bus.InboundMetadataKeyInteractionShortID:           old.ShortID,
		bus.InboundMetadataKeyInteractionResponseMessageID: "100",
	}

	newInboundTurnCoordinator(al).handleInbound(t.Context(), callback)
	deadline := time.Now().Add(2 * time.Second)
	for {
		acked, _ := tracker.counts()
		if acked == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stale Cancel callback was not settled")
		}
		time.Sleep(time.Millisecond)
	}
	current, _ = registry.Get(current.ID)
	if current.Status != interactions.StatusWaiting || current.Answer != nil {
		t.Fatalf("stale Cancel mutated newer interaction: %#v", current)
	}
}

func TestReloadedClaimedInteractionRejectsLosingProjectedAnswer(t *testing.T) {
	provider := &sequenceProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)
	sessionKey := "session-reloaded-claimed-answer"
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "первый", Values: map[string]string{"deploy_mode": "первый"},
		MessageID: "answer-first",
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := *al.GetConfig()
	if err = al.ReloadProviderAndConfig(t.Context(), provider, &reloaded); err != nil {
		t.Fatal(err)
	}
	reloadedAgent, ok := al.GetRegistry().GetAgent(agent.ID)
	if !ok {
		t.Fatal("reloaded agent is unavailable")
	}
	target := &inboundDispatchTarget{
		Agent: reloadedAgent, SessionKey: sessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	loser := bus.InboundMessage{
		Content: "Allow once", SpoolID: "spool-reloaded-loser",
		Context: inboundContextForInteraction(request.Route),
	}
	loser.Context.MessageID = "answer-second"
	loser.Context.Raw = map[string]string{
		bus.InboundMetadataKeyInteractionChoice:   bus.InboundInteractionChoiceAllowOnce,
		bus.InboundMetadataKeyInteractionResponse: "Allow once",
		bus.InboundMetadataKeyInteractionShortID:  record.ShortID,
	}
	if !newInboundTurnCoordinator(al).routeProjectedInteractionAnswer(t.Context(), loser, target) {
		t.Fatal("reloaded losing answer escaped interaction protocol routing")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		acked, released := tracker.counts()
		if acked == 1 {
			if released != 0 {
				t.Fatalf("reloaded loser released %d spool entries", released)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for reloaded loser acknowledgement")
		}
		time.Sleep(time.Millisecond)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusClaimed || record.Answer == nil ||
		record.Answer.MessageID != "answer-first" {
		t.Fatalf("reloaded interaction was mutated: %#v", record)
	}
	provider.mu.Lock()
	calls := provider.callCount
	provider.mu.Unlock()
	if calls != 0 {
		t.Fatalf("reloaded losing answer started %d continuation(s)", calls)
	}
	scope := newRuntimeSessionScope(reloadedAgent.Workspace, sessionKey)
	if got := al.pendingSteeringCountForScope(scope); got != 0 {
		t.Fatalf("reloaded losing answer entered steering queue: %d", got)
	}
	for _, message := range reloadedAgent.Sessions.GetHistory(sessionKey) {
		if strings.Contains(message.Content, "второй") {
			t.Fatalf("reloaded losing answer entered history: %#v", message)
		}
	}
}

func TestTaskInteractionConcurrentExplicitAnswersStartOneContinuation(t *testing.T) {
	provider := newBlockingInteractionProvider()
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)

	sessionKey := session.BuildOpaqueSessionKey("agent:main:test:task-concurrent-answer-owner")
	continuationSessionKey := session.BuildOpaqueSessionKey("agent:main:test:task-concurrent-answer-child")
	agent.Sessions.AddFullMessage(continuationSessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-task-concurrent-answer", Name: "request_user_input", Arguments: map[string]any{},
		}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	request.Origin.ToolCallID = "call-task-concurrent-answer"
	request.Origin.TaskID = "task-concurrent-answer"
	request.Origin.ContinuationSessionKey = continuationSessionKey
	tasks := al.taskRegistryForWorkspace(agent.Workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: "task-concurrent-answer", Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "complete after input", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
		DeliveryMode:   string(toolshared.AsyncDeliveryUserOnly),
		Channel:        request.Route.Channel, ChatID: request.Route.ChatID,
		RequesterSessionKey: sessionKey,
	}); err != nil {
		t.Fatal(err)
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		ID:   "interaction-task-concurrent-answer",
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	target := &inboundDispatchTarget{
		Agent:         agent,
		SessionKey:    sessionKey,
		RouteClaimKey: runtimeRouteClaimKey(request.Route.RouteSessionKey, ""),
		Allocation:    session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	first := bus.InboundMessage{
		Content: "/answer " + record.ShortID + " первый", SpoolID: "spool-task-first",
		Context: inboundContextForInteraction(request.Route),
	}
	first.Context.MessageID = "task-answer-first"
	coordinator := newInboundTurnCoordinator(al)
	coordinator.routeExplicitInteractionAnswer(t.Context(), first, target)
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task continuation")
	}
	second := bus.InboundMessage{
		Content: "/answer " + record.ShortID + " второй", SpoolID: "spool-task-second",
		Context: inboundContextForInteraction(request.Route),
	}
	second.Context.MessageID = "task-answer-second"
	coordinator.routeExplicitInteractionAnswer(t.Context(), second, target)
	deadline := time.Now().Add(2 * time.Second)
	for {
		acked, _ := tracker.counts()
		if acked == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for task answer acknowledgements")
		}
		time.Sleep(time.Millisecond)
	}
	if got := al.pendingSteeringCountForScope(
		newRuntimeSessionScope(agent.Workspace, sessionKey),
	); got != 0 {
		t.Fatalf("task losing answer entered steering queue: %d", got)
	}
	close(provider.release)
	deadline = time.Now().Add(2 * time.Second)
	for {
		record, _ = registry.Get(record.ID)
		task, _ := tasks.Get("task-concurrent-answer")
		if record.Status == interactions.StatusResolved && task.Status == taskregistry.StatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task interaction did not complete once: record=%#v task=%#v", record, task)
		}
		time.Sleep(time.Millisecond)
	}
	if record.Answer == nil || record.Answer.Text != "первый" ||
		record.ResumeTries != 1 || len(record.FinalDeliveryIDs) != 1 {
		t.Fatalf("task interaction winner/counts = %#v", record)
	}
	if calls, _ := provider.snapshot(); calls != 1 {
		t.Fatalf("task provider calls = %d, want 1", calls)
	}
	for _, message := range agent.Sessions.GetHistory(continuationSessionKey) {
		if strings.Contains(message.Content, "второй") || strings.Contains(message.Content, "/answer") {
			t.Fatalf("task losing answer entered continuation history: %#v", message)
		}
	}
}

func TestReloadWhileWaitingResumesAgainstPersistedSession(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	sessionKey := "session-reload-waiting"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "user", Content: "Deploy this"})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-reload-question", Name: "request_user_input", Arguments: map[string]any{},
		}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	request.Origin.ToolCallID = "call-reload-question"
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)

	reloaded := *al.GetConfig()
	if err = al.ReloadProviderAndConfig(t.Context(), &simpleConvProvider{}, &reloaded); err != nil {
		t.Fatal(err)
	}
	reloadedAgent, ok := al.GetRegistry().GetAgent(agent.ID)
	if !ok || reloadedAgent == nil {
		t.Fatal("reloaded agent is unavailable")
	}
	target := &inboundDispatchTarget{
		Agent: reloadedAgent, SessionKey: sessionKey,
		Allocation: session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	msg := bus.InboundMessage{
		Content: "Canary", SpoolID: "spool-reload-answer",
		Context: inboundContextForInteraction(request.Route),
	}
	msg.Context.MessageID = "answer-after-reload"
	ownership, _, err := al.processInteractionInbound(t.Context(), msg, target)
	if err != nil || ownership != interactionInboundClaimed {
		t.Fatalf("processInteractionInbound() = (%v, %v)", ownership, err)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusResolved || len(record.FinalDeliveryIDs) == 0 {
		t.Fatalf("record after reload answer = %#v", record)
	}
	select {
	case outbound := <-manager.sent:
		if strings.TrimSpace(outbound.Content) == "" {
			t.Fatalf("reload continuation outbound = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reload continuation")
	}
}

func TestStopCancellationPairsSuspendedToolCall(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	sessionKey := "session-stop-interaction"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "user", Content: "Deploy this"})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-question", Name: "request_user_input", Arguments: map[string]any{},
		}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	request.Origin.ExecutionID = "execution-stop-interaction"
	request.Origin.ExecutionContext = &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", ActorID: "actor-stop",
	}
	cleanupTool := &turnCleanupTestTool{
		countingTestTool: &countingTestTool{name: "stop-interaction-cleanup"},
	}
	agent.Tools.Register(cleanupTool)
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	target := &inboundDispatchTarget{
		Agent:         agent,
		SessionKey:    sessionKey,
		RouteClaimKey: runtimeRouteClaimKey(request.Route.RouteSessionKey, ""),
		Allocation:    session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	msg := bus.InboundMessage{Content: "/stop", Context: inboundContextForInteraction(request.Route)}
	cancellation, err := al.cancelInteractionForControlMessage(t.Context(), msg, target)
	if err != nil {
		t.Fatal(err)
	}
	if !cancellation.Matched || !cancellation.Canceled ||
		cancellation.Failed || !cancellation.CommandHandled {
		t.Fatalf("stop cancellation result = %#v", cancellation)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled {
		t.Fatalf("record status = %q, want canceled", record.Status)
	}
	_, resultIndex := interactionToolPairIndexes(agent.Sessions.GetHistory(sessionKey), "call-question")
	if resultIndex < 0 {
		t.Fatal("stop left the suspended tool call unpaired")
	}
	result := agent.Sessions.GetHistory(sessionKey)[resultIndex]
	if !strings.Contains(result.Content, `"outcome":"canceled"`) {
		t.Fatalf("cancellation tool result = %q", result.Content)
	}
	if cleanupTool.cleanupCalls != 1 ||
		cleanupTool.executionID != request.Origin.ExecutionID ||
		cleanupTool.inbound.ActorID != "actor-stop" {
		t.Fatalf(
			"stop cleanup = calls %d, execution %q, inbound %#v",
			cleanupTool.cleanupCalls,
			cleanupTool.executionID,
			cleanupTool.inbound,
		)
	}
}

func TestStopDoesNotCancelBoundFinalizationAfterRestart(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	stop := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:final-started"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, stop, "")
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	var err error
	record, err = registry.ClaimAnswer(
		record.ID,
		record.Revision,
		interactions.Answer{Text: "continue", ReceivedAt: time.Now().UnixMilli()},
		interactions.OutcomeAnswered,
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	record = bindTestInteractionFinal(t, registry, record)
	al.interactions.registries.Delete(agent.Workspace)

	result, cancelErr := al.cancelInteractionForControlMessage(t.Context(), stop, target)
	if cancelErr == nil || !strings.Contains(cancelErr.Error(), "finalization already started") {
		t.Fatalf("stop cancellation error = %v", cancelErr)
	}
	if !result.Matched || result.Canceled || !result.Failed || result.CommandHandled {
		t.Fatalf("stop cancellation result = %#v", result)
	}
	reloaded, found := al.interactionRegistryForWorkspace(agent.Workspace).Get(record.ID)
	if !found || reloaded.Status != interactions.StatusResuming || len(reloaded.FinalDeliveryIDs) != 1 {
		t.Fatalf("interaction after rejected stop = %#v, found=%t", reloaded, found)
	}
}

func TestStopCancellationFencesClaimedInteractionBeforeRouteWait(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	stop := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:claimed-resuming-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, stop, "")
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "continue", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	activeClaim, _, claimed := al.turns.claimRuntimeRouteSession(target, "pending-interaction-resume")
	if !claimed {
		t.Fatal("failed to claim the interaction route at the claimed boundary")
	}

	type cancellationResult struct {
		result interactionControlCancellationResult
		err    error
	}
	done := make(chan cancellationResult, 1)
	go func() {
		result, cancelErr := al.cancelInteractionForControlMessage(t.Context(), stop, target)
		done <- cancellationResult{result: result, err: cancelErr}
	}()
	continuationScope := newRuntimeSessionScope(agent.Workspace, target.SessionKey)
	deadline := time.Now().Add(time.Second)
	for {
		current, _ := registry.Get(record.ID)
		if current.Status == interactions.StatusCanceling {
			record = current
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancellation did not durably fence the claimed interaction")
		}
		time.Sleep(time.Millisecond)
	}
	fencedRevision := record.Revision
	activeClaim.releaseIfOwned()

	select {
	case cancellation := <-done:
		if cancellation.err != nil {
			t.Fatal(cancellation.err)
		}
		if !cancellation.result.Matched || !cancellation.result.Canceled ||
			cancellation.result.Failed || !cancellation.result.CommandHandled {
			t.Fatalf("boundary cancellation result = %#v", cancellation.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for boundary cancellation")
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled || record.Revision <= fencedRevision {
		t.Fatalf("boundary interaction = %#v", record)
	}
	if _, armed := al.turns.pendingStops.Load(continuationScope); armed {
		t.Fatal("boundary cancellation left a pending stop armed")
	}
}

func TestStopCancellationRecoveryClearsTimedOutPendingStop(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	stop := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:cancel-recovery-pending-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, stop, "")
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	activeClaim, _, claimed := al.turns.claimRuntimeRouteSession(target, "pending-unregistered-continuation")
	if !claimed {
		t.Fatal("failed to hold the interaction route")
	}

	cancelCtx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	result, cancelErr := al.cancelInteractionForControlMessage(cancelCtx, stop, target)
	if cancelErr == nil || !strings.Contains(cancelErr.Error(), "busy while canceling") {
		t.Fatalf("timed-out stop error = %v", cancelErr)
	}
	if !result.Matched || result.Canceled || !result.Failed || result.CommandHandled {
		t.Fatalf("timed-out stop result = %#v", result)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCanceling {
		t.Fatalf("interaction after timed-out stop = %#v", record)
	}
	continuationScope := newRuntimeSessionScope(
		agent.Workspace,
		interactionContinuationSessionKey(record),
	)
	if _, armed := al.turns.pendingStops.Load(continuationScope); !armed {
		t.Fatal("timed-out stop did not arm the continuation marker")
	}
	activeClaim.releaseIfOwned()

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled {
		t.Fatalf("interaction after cancellation recovery = %#v", record)
	}
	if al.turns.takePendingStop(continuationScope) {
		t.Fatal("next continuation turn consumed a stale recovered stop")
	}
}

func TestStopCancellationReloadsWaitingInteractionAfterAnswerAdmission(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	stop := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:waiting-admission-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, stop, "")
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	loaded := make(chan struct{})
	continueCancel := make(chan struct{})
	var hookOnce sync.Once
	ctx := context.WithValue(
		t.Context(),
		interactionLifecycleBoundaryHookKey{},
		interactionLifecycleBoundaryHook(func(boundary string) {
			if boundary != interactionBoundaryCancelAfterLoad {
				return
			}
			hookOnce.Do(func() { close(loaded) })
			<-continueCancel
		}),
	)

	type cancellationResult struct {
		result interactionControlCancellationResult
		err    error
	}
	done := make(chan cancellationResult, 1)
	go func() {
		result, cancelErr := al.cancelInteractionForControlMessage(ctx, stop, target)
		done <- cancellationResult{result: result, err: cancelErr}
	}()
	select {
	case <-loaded:
	case <-time.After(time.Second):
		t.Fatal("stop did not pause after loading the waiting interaction")
	}
	activeClaim, _, claimed := al.turns.claimRuntimeRouteSession(target, "pending-interaction-answer")
	if !claimed {
		t.Fatal("failed to claim the route for answer admission")
	}
	record, err := registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "continue", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	resumingRevision := record.Revision
	close(continueCancel)

	deadline := time.Now().Add(time.Second)
	for {
		current, _ := registry.Get(record.ID)
		if current.Status == interactions.StatusCanceling {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop did not fence the interaction after answer admission")
		}
		time.Sleep(time.Millisecond)
	}
	activeClaim.releaseIfOwned()
	select {
	case cancellation := <-done:
		if cancellation.err != nil {
			t.Fatal(cancellation.err)
		}
		if !cancellation.result.Matched || !cancellation.result.Canceled ||
			cancellation.result.Failed || !cancellation.result.CommandHandled {
			t.Fatalf("admission cancellation result = %#v", cancellation.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for admission-boundary cancellation")
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled || record.Revision <= resumingRevision {
		t.Fatalf("admission-boundary interaction = %#v", record)
	}
}

func TestStopCancellationAbortsActiveInteractionContinuation(t *testing.T) {
	provider := newBlockingInteractionProvider()
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	al.channelManager = manager
	stop := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:resuming-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, stop, "")
	answer := stop
	answer.Content = "/answer " + record.ShortID + " continue"
	answer.Context.MessageID = "answer-before-stop"
	if !newInboundTurnCoordinator(al).routeExplicitInteractionAnswer(
		t.Context(), answer, target,
	) {
		t.Fatal("interaction answer did not enter the continuation worker")
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the real interaction continuation")
	}

	result, err := al.cancelInteractionForControlMessage(t.Context(), stop, target)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || !result.Canceled || result.Failed || !result.CommandHandled {
		t.Fatalf("resuming stop cancellation result = %#v", result)
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled {
		t.Fatalf("record status = %q, want canceled", record.Status)
	}
	if countInteractionToolResults(
		agent.Sessions.GetHistory(target.SessionKey), record.Origin.ToolCallID,
	) != 1 {
		t.Fatal("stop did not pair the continuation tool call exactly once")
	}
	select {
	case final := <-manager.sent:
		t.Fatalf("aborted interaction published a final response: %#v", final)
	case <-time.After(100 * time.Millisecond):
	}
	claim, _, claimed := al.turns.claimRuntimeRouteSession(target, "post-stop-reuse")
	if !claimed {
		t.Fatal("canceled interaction did not release the route for reuse")
	}
	claim.releaseIfOwned()
}

func TestStopCancellationWinsModelFinalizationBoundary(t *testing.T) {
	provider := newBlockingInteractionProvider()
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	trackingBus := &finalResponseAdmissionTestBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, trackingBus)
	manager := newInteractionChannelManager()
	al.channelManager = manager
	stop := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:model-final-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, stop, "")
	boundaryReached := make(chan struct{})
	releaseFinalization := make(chan struct{})
	var hookOnce sync.Once
	answerCtx := context.WithValue(
		t.Context(),
		interactionLifecycleBoundaryHookKey{},
		interactionLifecycleBoundaryHook(func(boundary string) {
			if boundary != interactionBoundaryModelFinal {
				return
			}
			hookOnce.Do(func() { close(boundaryReached) })
			<-releaseFinalization
		}),
	)
	answer := stop
	answer.Content = "/answer " + record.ShortID + " continue"
	answer.Context.MessageID = "answer-before-model-final-stop"
	if !newInboundTurnCoordinator(al).routeExplicitInteractionAnswer(answerCtx, answer, target) {
		t.Fatal("interaction answer did not enter the continuation worker")
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the interaction provider")
	}
	const steeringSpoolID = "spool-model-final-cancellation"
	if err := al.enqueueSteeringMessageWithSender(
		target.runtimeSessionScope(),
		agent.ID,
		stop.Context.SenderID,
		providers.Message{
			Role: "user", Content: "continue after the browser check",
			InboundSpoolID: steeringSpoolID,
		},
	); err != nil {
		t.Fatalf("enqueue held steering: %v", err)
	}
	close(provider.release)
	select {
	case <-boundaryReached:
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not pause after unregistering the model turn")
	}

	type cancellationResult struct {
		result interactionControlCancellationResult
		err    error
	}
	canceled := make(chan cancellationResult, 1)
	go func() {
		result, cancelErr := al.cancelInteractionForControlMessage(t.Context(), stop, target)
		canceled <- cancellationResult{result: result, err: cancelErr}
	}()
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	deadline := time.Now().Add(time.Second)
	for {
		current, _ := registry.Get(record.ID)
		if current.Status == interactions.StatusCanceling {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop did not fence model finalization")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFinalization)
	select {
	case cancellation := <-canceled:
		if cancellation.err != nil {
			t.Fatal(cancellation.err)
		}
		if !cancellation.result.Matched || !cancellation.result.Canceled ||
			cancellation.result.Failed || !cancellation.result.CommandHandled {
			t.Fatalf("model-final cancellation result = %#v", cancellation.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for model-final cancellation")
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled {
		t.Fatalf("model-final interaction = %#v", record)
	}
	if countInteractionToolResults(
		agent.Sessions.GetHistory(target.SessionKey), record.Origin.ToolCallID,
	) != 1 {
		t.Fatal("model-final cancellation did not preserve exactly one tool result")
	}
	select {
	case final := <-manager.sent:
		t.Fatalf("model-final cancellation published a response: %#v", final)
	case <-time.After(100 * time.Millisecond):
	}
	acked, released, releaseCause := trackingBus.ownership()
	if slices.Contains(acked, steeringSpoolID) || !slices.Contains(released, steeringSpoolID) {
		t.Fatalf("held steering ownership = acked:%v released:%v", acked, released)
	}
	if !errors.Is(releaseCause, errInteractionFinalizationCanceled) {
		t.Fatalf("held steering release cause = %v", releaseCause)
	}
}

func TestLateResumingSteeringHandsOffToOwnerExactlyOnce(t *testing.T) {
	provider := newBlockingInteractionProvider()
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	installInteractionChannelManager(t, al, newInteractionChannelManager())
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "start interaction",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:late-resume-owner"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
	})
	target, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("failed to resolve interaction target")
	}
	continuationKey := session.BuildOpaqueSessionKey("agent:main:test:late-resume-child")
	origin := interactions.Origin{
		TurnID: "turn-late-resume", ToolCallID: "call-late-resume", ToolName: "request_user_input",
		ContinuationSessionKey: continuationKey,
	}
	agent.Sessions.AddFullMessage(continuationKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: origin.ToolCallID, Name: origin.ToolName, Arguments: map[string]any{},
		}},
	})
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: interactions.KindQuestion,
		Route: interactions.Route{
			AgentID: agent.ID, SessionKey: target.SessionKey,
			RouteSessionKey: target.Allocation.RouteScopeKey,
			Channel:         msg.Context.Channel, AccountID: msg.Context.Account,
			ChatID: msg.Context.ChatID, ChatType: msg.Context.ChatType,
			SenderID: msg.Context.SenderID,
		},
		Origin: origin, Questions: []interactions.Question{{ID: "confirm", Question: "Proceed?"}},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)

	boundaryReached := make(chan struct{})
	releaseFinalization := make(chan struct{})
	var hookOnce sync.Once
	answerCtx := context.WithValue(
		t.Context(),
		interactionLifecycleBoundaryHookKey{},
		interactionLifecycleBoundaryHook(func(boundary string) {
			if boundary != interactionBoundaryModelFinal {
				return
			}
			hookOnce.Do(func() { close(boundaryReached) })
			<-releaseFinalization
		}),
	)
	answer := msg
	answer.Content = "/answer " + record.ShortID + " continue"
	answer.Context.MessageID = "answer-before-late-steering"
	if !newInboundTurnCoordinator(al).routeExplicitInteractionAnswer(answerCtx, answer, target) {
		t.Fatal("interaction answer did not enter the continuation worker")
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the interaction provider")
	}
	close(provider.release)
	select {
	case <-boundaryReached:
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not reach the final-delivery boundary")
	}

	const correctionSpoolID = "spool-late-resuming-correction"
	correction := msg
	correction.Content = "open all postings and find the expired microwave"
	correction.Context.MessageID = "late-resuming-correction"
	correction.SpoolID = correctionSpoolID
	newInboundTurnCoordinator(al).handleInteractionInbound(t.Context(), correction, target)
	childScope := newRuntimeSessionScope(agent.Workspace, continuationKey)
	if depth := al.steering.lenScope(childScope); depth != 0 {
		t.Fatalf("continuation steering depth = %d, want 0 after handoff", depth)
	}
	if depth := al.steering.lenScope(target.runtimeSessionScope()); depth != 1 {
		t.Fatalf("owner steering depth = %d, want 1 after handoff", depth)
	}
	close(releaseFinalization)

	deadline := time.Now().Add(2 * time.Second)
	for {
		calls, messages := provider.snapshot()
		acked, released := tracker.ownership()
		if calls >= 2 && countMatchingStrings(acked, correctionSpoolID) == 1 {
			if countMatchingStrings(released, correctionSpoolID) != 0 {
				t.Fatalf("late correction released unexpectedly: %v", released)
			}
			seen := 0
			for _, callMessages := range messages {
				for _, callMessage := range callMessages {
					if strings.Contains(callMessage.Content, correction.Content) {
						seen++
					}
				}
			}
			if seen != 1 {
				t.Fatalf("late correction appeared in provider history %d times, want 1", seen)
			}
			if al.steering.lenScope(target.runtimeSessionScope()) != 0 {
				t.Fatal("owner steering queue was not drained")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"late correction was not drained exactly once: calls=%d acked=%v released=%v",
				calls,
				acked,
				released,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestResumeErrorTeardownSealsStaleInboundFlight(t *testing.T) {
	provider := newBlockingInteractionProvider()
	provider.err = errors.New("resume provider failed")
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "start interaction",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:error-handoff-owner"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
	})
	continuationKey := session.BuildOpaqueSessionKey("agent:main:test:error-handoff-child")
	record, target := prepareWaitingControlInteractionWithContinuation(
		t, al, agent, agent, msg, continuationKey,
	)
	answer := msg
	answer.Content = "/answer " + record.ShortID + " continue"
	answer.Context.MessageID = "answer-before-resume-error"
	if !newInboundTurnCoordinator(al).routeExplicitInteractionAnswer(t.Context(), answer, target) {
		t.Fatal("interaction answer did not enter the continuation worker")
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the interaction provider")
	}

	flightRead := make(chan struct{})
	releaseFlightRead := make(chan struct{})
	var hookOnce sync.Once
	correctionCtx := context.WithValue(
		t.Context(),
		interactionLifecycleBoundaryHookKey{},
		interactionLifecycleBoundaryHook(func(boundary string) {
			if boundary != interactionBoundaryResumeFlightRead {
				return
			}
			hookOnce.Do(func() { close(flightRead) })
			<-releaseFlightRead
		}),
	)
	const correctionSpoolID = "spool-stale-resume-flight"
	correction := msg
	correction.Content = "use the old postings tab"
	correction.Context.MessageID = "correction-during-resume-error"
	correction.SpoolID = correctionSpoolID
	correctionDone := make(chan struct{})
	go func() {
		defer close(correctionDone)
		newInboundTurnCoordinator(al).handleInteractionInbound(correctionCtx, correction, target)
	}()
	select {
	case <-flightRead:
	case <-time.After(2 * time.Second):
		t.Fatal("correction did not load the active resume flight")
	}

	close(provider.release)
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, _ := registry.Get(record.ID)
		_, flightActive := al.loadInteractionResumeFlight(agent.Workspace, record.ID)
		if current.ResumeError != "" && !flightActive {
			record = current
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resume error did not tear down its flight")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFlightRead)
	select {
	case <-correctionDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stale-flight correction did not finish routing")
	}

	childScope := newRuntimeSessionScope(agent.Workspace, continuationKey)
	ownerScope := target.runtimeSessionScope()
	if depth := al.steering.lenScope(childScope); depth != 0 {
		t.Fatalf("retired continuation steering depth = %d, want 0", depth)
	}
	queued := al.dequeueSteeringMessagesForScope(ownerScope)
	if len(queued) != 1 || queued[0].InboundSpoolID != correctionSpoolID {
		t.Fatalf("owner handoff queue = %#v", queued)
	}
	if _, err := registry.Fail(record.ID, record.Revision, "resume_failed", record.ResumeError); err != nil {
		t.Fatal(err)
	}
	if err := al.settleSteeringMessages(
		finalResponseAdmission{status: finalResponseAdmissionAccepted}, queued,
	); err != nil {
		t.Fatal(err)
	}
	acked, released := tracker.ownership()
	if countMatchingStrings(acked, correctionSpoolID) != 1 ||
		countMatchingStrings(released, correctionSpoolID) != 0 {
		t.Fatalf("stale-flight ownership = acked:%v released:%v", acked, released)
	}
}

func TestCrossAgentResumeErrorHandoffUsesContinuationWorkspace(t *testing.T) {
	provider := newBlockingInteractionProvider()
	provider.err = errors.New("cross-agent resume provider failed")
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	ownerAgent, _ := al.registry.GetAgent("alpha")
	continuationAgent, _ := al.registry.GetAgent("beta")
	tracker := &interactionOwnershipBus{MessageBus: al.bus.(*bus.MessageBus)}
	setTestMessageBus(al, tracker)
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "start cross-agent interaction",
		SessionKey: session.BuildOpaqueSessionKey("agent:alpha:test:cross-agent-handoff-owner"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
	})
	continuationKey := session.BuildOpaqueSessionKey("agent:beta:test:cross-agent-handoff-child")
	record, target := prepareWaitingControlInteractionWithContinuation(
		t, al, ownerAgent, continuationAgent, msg, continuationKey,
	)
	if target.Agent.ID != ownerAgent.ID {
		t.Fatalf("interaction owner agent = %q, want %q", target.Agent.ID, ownerAgent.ID)
	}
	answer := msg
	answer.Content = "/answer " + record.ShortID + " continue"
	answer.Context.MessageID = "answer-before-cross-agent-resume-error"
	if !newInboundTurnCoordinator(al).routeExplicitInteractionAnswer(t.Context(), answer, target) {
		t.Fatal("cross-agent answer did not enter the continuation worker")
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the cross-agent continuation provider")
	}

	flightRead := make(chan struct{})
	releaseFlightRead := make(chan struct{})
	var hookOnce sync.Once
	correctionCtx := context.WithValue(
		t.Context(),
		interactionLifecycleBoundaryHookKey{},
		interactionLifecycleBoundaryHook(func(boundary string) {
			if boundary != interactionBoundaryResumeFlightRead {
				return
			}
			hookOnce.Do(func() { close(flightRead) })
			<-releaseFlightRead
		}),
	)
	const correctionSpoolID = "spool-cross-agent-stale-flight"
	correction := msg
	correction.Content = "open the old postings collection"
	correction.Context.MessageID = "cross-agent-correction-during-resume-error"
	correction.SpoolID = correctionSpoolID
	correctionDone := make(chan struct{})
	go func() {
		defer close(correctionDone)
		newInboundTurnCoordinator(al).handleInteractionInbound(correctionCtx, correction, target)
	}()
	select {
	case <-flightRead:
	case <-time.After(2 * time.Second):
		t.Fatal("cross-agent correction did not load the active resume flight")
	}

	close(provider.release)
	registry := al.interactionRegistryForWorkspace(ownerAgent.Workspace)
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, _ := registry.Get(record.ID)
		_, flightActive := al.loadInteractionResumeFlight(ownerAgent.Workspace, record.ID)
		if current.ResumeError != "" && !flightActive {
			record = current
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cross-agent resume error did not tear down its flight")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFlightRead)
	select {
	case <-correctionDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cross-agent stale-flight correction did not finish routing")
	}

	childScope := newRuntimeSessionScope(continuationAgent.Workspace, continuationKey)
	ownerScope := target.runtimeSessionScope()
	if depth := al.steering.lenScope(childScope); depth != 0 {
		t.Fatalf("cross-agent child steering depth = %d, want 0", depth)
	}
	queued := al.dequeueSteeringMessagesForScope(ownerScope)
	if len(queued) != 1 || queued[0].InboundSpoolID != correctionSpoolID {
		t.Fatalf("cross-agent owner handoff queue = %#v", queued)
	}
	if _, err := registry.Fail(record.ID, record.Revision, "resume_failed", record.ResumeError); err != nil {
		t.Fatal(err)
	}
	if err := al.settleSteeringMessages(
		finalResponseAdmission{status: finalResponseAdmissionAccepted}, queued,
	); err != nil {
		t.Fatal(err)
	}
	acked, released := tracker.ownership()
	if countMatchingStrings(acked, correctionSpoolID) != 1 ||
		countMatchingStrings(released, correctionSpoolID) != 0 {
		t.Fatalf("cross-agent stale-flight ownership = acked:%v released:%v", acked, released)
	}
}

func TestStopCancellationWinsTaskFinalPreparationBoundaries(t *testing.T) {
	for _, test := range []struct {
		name     string
		boundary string
	}{
		{name: "ready", boundary: interactionBoundaryFinalReady},
		{name: "task completed", boundary: interactionBoundaryTaskCompleted},
	} {
		t.Run(test.name, func(t *testing.T) {
			al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
			defer cleanup()
			manager := newInteractionChannelManager()
			installInteractionChannelManager(t, al, manager)
			stop := testInboundMessage(bus.InboundMessage{
				Content: "/stop",
				SessionKey: session.BuildOpaqueSessionKey(
					"agent:main:test:task-final-" + strings.ReplaceAll(test.name, " ", "-"),
				),
				Context: bus.InboundContext{
					Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
					SenderID: "user-1",
				},
			})
			taskID := "task-final-" + strings.ReplaceAll(test.name, " ", "-")
			tasks := al.taskRegistryForWorkspace(agent.Workspace)
			if err := tasks.Upsert(taskregistry.Record{
				TaskID: taskID, Runtime: taskregistry.RuntimeSubagent,
				TaskKind: "spawn", Task: "finish after interaction",
				Status: taskregistry.StatusRunning, DeliveryStatus: taskregistry.DeliveryPending,
				DeliveryMode: string(toolshared.AsyncDeliveryUserOnly),
				Channel:      "telegram", ChatID: "chat-1",
				RequesterSessionKey: stop.SessionKey,
			}); err != nil {
				t.Fatal(err)
			}
			record, target := prepareWaitingControlInteraction(t, al, agent, stop, taskID)
			registry := al.interactionRegistryForWorkspace(agent.Workspace)
			var err error
			record, err = registry.ClaimAnswer(
				record.ID,
				record.Revision,
				interactions.Answer{Text: "continue", ReceivedAt: time.Now().UnixMilli()},
				interactions.OutcomeAnswered,
			)
			if err != nil {
				t.Fatal(err)
			}
			record, err = registry.MarkResuming(record.ID, record.Revision)
			if err != nil {
				t.Fatal(err)
			}

			boundaryReached := make(chan struct{})
			releaseDelivery := make(chan struct{})
			var hookOnce sync.Once
			deliveryCtx := context.WithValue(
				t.Context(),
				interactionLifecycleBoundaryHookKey{},
				interactionLifecycleBoundaryHook(func(boundary string) {
					if boundary != test.boundary {
						return
					}
					hookOnce.Do(func() { close(boundaryReached) })
					<-releaseDelivery
				}),
			)
			deliveryDone := make(chan error, 1)
			go func() {
				deliveryDone <- al.deliverTaskInteractionFinal(
					deliveryCtx,
					registry,
					agent.Workspace,
					record,
					inboundContextForInteraction(record.Route),
					"undelivered task final",
					nil,
					nil,
				)
			}()
			select {
			case <-boundaryReached:
			case <-time.After(2 * time.Second):
				t.Fatalf("task delivery did not reach %q", test.boundary)
			}

			result, cancelErr := al.cancelInteractionForControlMessage(t.Context(), stop, target)
			if cancelErr != nil {
				t.Fatal(cancelErr)
			}
			if !result.Matched || !result.Canceled || result.Failed || !result.CommandHandled {
				t.Fatalf("task final cancellation result = %#v", result)
			}
			close(releaseDelivery)
			select {
			case deliveryErr := <-deliveryDone:
				if !errors.Is(deliveryErr, interactions.ErrConflict) {
					t.Fatalf("delivery after cancellation error = %v, want conflict", deliveryErr)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for task final delivery to unwind")
			}

			canceled, _ := registry.Get(record.ID)
			if canceled.Status != interactions.StatusCancelled {
				t.Fatalf("canceled interaction = %#v", canceled)
			}
			task, _ := tasks.Get(taskID)
			if task.Status != taskregistry.StatusCancelled ||
				task.DeliveryStatus != taskregistry.DeliveryNotApplicable ||
				task.Deliverable != nil ||
				task.LastCompletionID != "" || task.TerminalSummary != "" {
				t.Fatalf("canceled task settlement = %#v", task)
			}
			select {
			case outbound := <-manager.sent:
				t.Fatalf("canceled task final was published: %#v", outbound)
			default:
			}
		})
	}
}

func TestStopCancellationWinsPrecomputedFinalizationBoundary(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	al.channelManager = manager
	stop := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:precomputed-final-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, stop, "")
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	var err error
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "continue", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	agent.Sessions.AddFullMessage(target.SessionKey, providers.Message{
		Role: "tool", ToolCallID: record.Origin.ToolCallID,
		Content: `{"interaction_id":"` + record.ID + `","outcome":"answered"}`,
	})
	agent.Sessions.AddFullMessage(target.SessionKey, providers.Message{
		Role: "assistant", Content: "precomputed final response",
	})
	activeClaim, _, claimed := al.turns.claimRuntimeRouteSession(target, "pending-precomputed-final")
	if !claimed {
		t.Fatal("failed to claim the precomputed-final route")
	}
	boundaryReached := make(chan struct{})
	releaseFinalization := make(chan struct{})
	var hookOnce sync.Once
	resumeCtx := context.WithValue(
		t.Context(),
		interactionLifecycleBoundaryHookKey{},
		interactionLifecycleBoundaryHook(func(boundary string) {
			if boundary != interactionBoundaryPrecomputedFinal {
				return
			}
			hookOnce.Do(func() { close(boundaryReached) })
			<-releaseFinalization
		}),
	)
	resumeDone := make(chan error, 1)
	go func() {
		resumeDone <- al.resumeClaimedInteraction(
			resumeCtx,
			registry,
			agent.Workspace,
			agent,
			&session.SessionScope{
				Version: session.ScopeVersion, AgentID: agent.ID, Channel: record.Route.Channel,
				RouteScopeKey: record.Route.RouteSessionKey,
			},
			inboundContextForInteraction(record.Route),
			record,
		)
	}()
	select {
	case <-boundaryReached:
	case <-time.After(2 * time.Second):
		t.Fatal("precomputed continuation did not reach finalization boundary")
	}

	type cancellationResult struct {
		result interactionControlCancellationResult
		err    error
	}
	canceled := make(chan cancellationResult, 1)
	go func() {
		result, cancelErr := al.cancelInteractionForControlMessage(t.Context(), stop, target)
		canceled <- cancellationResult{result: result, err: cancelErr}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		current, _ := registry.Get(record.ID)
		if current.Status == interactions.StatusCanceling {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop did not fence precomputed finalization")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFinalization)
	select {
	case resumeErr := <-resumeDone:
		if resumeErr != nil {
			t.Fatal(resumeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for precomputed finalizer to unwind")
	}
	activeClaim.releaseIfOwned()
	select {
	case cancellation := <-canceled:
		if cancellation.err != nil {
			t.Fatal(cancellation.err)
		}
		if !cancellation.result.Matched || !cancellation.result.Canceled ||
			cancellation.result.Failed || !cancellation.result.CommandHandled {
			t.Fatalf("precomputed-final cancellation result = %#v", cancellation.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for precomputed-final cancellation")
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled {
		t.Fatalf("precomputed-final interaction = %#v", record)
	}
	if countInteractionToolResults(
		agent.Sessions.GetHistory(target.SessionKey), record.Origin.ToolCallID,
	) != 1 {
		t.Fatal("precomputed-final cancellation did not preserve exactly one tool result")
	}
	select {
	case final := <-manager.sent:
		t.Fatalf("precomputed-final cancellation published a response: %#v", final)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestQuestionCancelButtonUsesStopCancellation(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	coordinator := installInteractionChannelManager(t, al, manager)
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "Cancel turn",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:question-cancel"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
			Raw: map[string]string{
				bus.InboundMetadataKeyInteractionChoice: bus.InboundInteractionChoiceCancel,
			},
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, msg, "")
	msg.Context.Raw[bus.InboundMetadataKeyInteractionShortID] = record.ShortID
	msg.Context.ReplyToMessageID = "7716"
	seedTestInteractionPromptOutcomeWithMessages(
		t, coordinator, agent.Workspace, record, outbox.StatusDelivered, 1, []string{"7716"},
	)

	result, err := al.cancelInteractionForControlMessage(t.Context(), msg, target)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || !result.Canceled || result.Failed ||
		!result.CommandHandled || result.Kind != interactions.KindQuestion {
		t.Fatalf("cancel button result = %#v", result)
	}
	select {
	case synced := <-manager.synced:
		if !synced.Metadata.RemovesInteractionControls() {
			t.Fatalf("canceled question control sync = %#v", synced)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled question did not clear projected controls")
	}
}

func TestQuestionResponseTakesPriorityOverCommandShapedOption(t *testing.T) {
	for _, option := range []string{"/stop", "/new", "/reset", "/clear"} {
		t.Run(option, func(t *testing.T) {
			al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
			defer cleanup()
			msg := testInboundMessage(bus.InboundMessage{
				Content:    option,
				SessionKey: session.BuildOpaqueSessionKey("agent:main:test:command-option"),
				Context: bus.InboundContext{
					Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
					Raw: map[string]string{bus.InboundMetadataKeyInteractionResponse: option},
				},
			})
			_, target := prepareWaitingControlInteraction(t, al, agent, msg, "")

			result, err := al.cancelInteractionForControlMessage(t.Context(), msg, target)
			if err != nil {
				t.Fatal(err)
			}
			if result.Matched || result.Canceled || result.CommandHandled {
				t.Fatalf("command-shaped option was treated as session control: %#v", result)
			}
		})
	}
}

func TestWaitingForegroundInteractionStopUsesSuccessfulStopContract(t *testing.T) {
	provider := &sequenceProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	coordinator := openTestInteractionOutbox(t, al)
	msg := testInboundMessage(bus.InboundMessage{
		Content:    bus.InboundInteractionCancelLabel,
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:interaction-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
			TopicID: "topic-1", SenderID: "user-1", MessageID: "stop-1",
			Raw: map[string]string{
				bus.InboundMetadataKeyInteractionChoice: bus.InboundInteractionChoiceCancel,
			},
		},
	})
	record, _ := prepareWaitingControlInteraction(t, al, agent, msg, "")
	msg.Context.Raw[bus.InboundMetadataKeyInteractionShortID] = record.ShortID
	msg.Context.ReplyToMessageID = "7716"
	seedTestInteractionPromptOutcomeWithMessages(
		t, coordinator, agent.Workspace, record, outbox.StatusDelivered, 1, []string{"7716"},
	)

	newInboundTurnCoordinator(al).handleInbound(t.Context(), msg)

	messageBus := al.bus.(*bus.MessageBus)
	select {
	case outbound := <-messageBus.OutboundChan():
		want := "Task stopped. Current task was canceled."
		if outbound.Content != want {
			t.Fatalf("stop reply = %q, want %q", outbound.Content, want)
		}
		metadata := outbound.Metadata
		if !metadata.RemovesInteractionControls() ||
			metadata.InteractionKind != bus.OutboundInteractionQuestion {
			t.Fatalf("stop reply metadata = %#v", metadata)
		}
		if outbound.Context.ReplyToMessageID != "stop-1" {
			t.Fatalf("cancel-button reply target = %q, want stop-1", outbound.Context.ReplyToMessageID)
		}
		if strings.Contains(outbound.Content, "No active task to stop.") {
			t.Fatalf("stop reply used inactive-task contract: %q", outbound.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for interaction /stop reply")
	}

	record, _ = al.interactionRegistryForWorkspace(agent.Workspace).Get(record.ID)
	if record.Status != interactions.StatusCancelled ||
		record.FailureCode != "session_control_stop" {
		t.Fatalf("stopped interaction = %#v", record)
	}
	history := agent.Sessions.GetHistory(record.Route.SessionKey)
	if got := countInteractionToolResults(history, record.Origin.ToolCallID); got != 1 {
		t.Fatalf("cancellation tool results = %d, want 1", got)
	}
	provider.mu.Lock()
	calls := provider.callCount
	provider.mu.Unlock()
	if calls != 0 {
		t.Fatalf("model calls after interaction stop = %d, want 0", calls)
	}
}

func TestWaitingTaskInteractionStopTerminalizesTaskWithoutDelivery(t *testing.T) {
	provider := &sequenceProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:task-interaction-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", Account: "primary", ChatID: "chat-task", ChatType: "direct",
			TopicID: "topic-task", SenderID: "user-task", MessageID: "stop-task",
		},
	})
	tasks := al.taskRegistryForWorkspace(agent.Workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: "task-waiting-stop", Runtime: taskregistry.RuntimeSubagent,
		Status: taskregistry.StatusRunning, DeliveryStatus: taskregistry.DeliveryPending,
	}); err != nil {
		t.Fatal(err)
	}
	record, _ := prepareWaitingControlInteraction(t, al, agent, msg, "task-waiting-stop")
	task, _ := tasks.Get("task-waiting-stop")
	if task.Status != taskregistry.StatusRunning || record.Status != interactions.StatusWaiting {
		t.Fatalf("task before stop = %#v", task)
	}

	newInboundTurnCoordinator(al).handleInbound(t.Context(), msg)
	select {
	case outbound := <-al.bus.(*bus.MessageBus).OutboundChan():
		if outbound.Content != "Task stopped. Current task was canceled." {
			t.Fatalf("task stop reply = %q", outbound.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task interaction /stop reply")
	}

	task, _ = tasks.Get("task-waiting-stop")
	if task.Status != taskregistry.StatusCancelled ||
		task.DeliveryStatus != taskregistry.DeliveryNotApplicable ||
		task.EndedAt == 0 || tasks.Stats().ProtectedTaskCount != 0 {
		t.Fatalf("task after stop = %#v, stats=%#v", task, tasks.Stats())
	}
	record, _ = al.interactionRegistryForWorkspace(agent.Workspace).Get(record.ID)
	if record.Status != interactions.StatusCancelled {
		t.Fatalf("task interaction after stop = %#v", record)
	}
	provider.mu.Lock()
	calls := provider.callCount
	provider.mu.Unlock()
	if calls != 0 {
		t.Fatalf("task continuation model calls after stop = %d, want 0", calls)
	}
	select {
	case outbound := <-al.bus.(*bus.MessageBus).OutboundChan():
		t.Fatalf("unexpected task completion after cancellation: %#v", outbound)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTaskBoundInteractionCancellationReturnsTaskID(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:task-id-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-task", ChatType: "direct",
			SenderID: "user-task",
		},
	})
	tasks := al.taskRegistryForWorkspace(agent.Workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: "task-associated", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
	}); err != nil {
		t.Fatal(err)
	}
	_, target := prepareWaitingControlInteraction(t, al, agent, msg, "task-associated")

	result, err := al.cancelInteractionForControlMessage(t.Context(), msg, target)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || !result.Canceled || result.Failed ||
		!result.CommandHandled || result.TaskID != "task-associated" {
		t.Fatalf("task-bound cancellation result = %#v", result)
	}
}

func TestInteractionControlCancellationReportsFailure(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:failed-interaction-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct", SenderID: "user-1",
		},
	})
	record, target := prepareWaitingControlInteraction(t, al, agent, msg, "")
	agent.Sessions.SetHistory(record.Route.SessionKey, nil)

	result, err := al.cancelInteractionForControlMessage(t.Context(), msg, target)
	if err == nil {
		t.Fatal("cancellation succeeded without the originating tool call")
	}
	if !result.Matched || result.Canceled || !result.Failed || result.CommandHandled {
		t.Fatalf("failed cancellation result = %#v", result)
	}
	record, _ = al.interactionRegistryForWorkspace(agent.Workspace).Get(record.ID)
	if record.Status != interactions.StatusCanceling ||
		record.FailureCode != "session_control_stop" {
		t.Fatalf("interaction after interrupted cancellation = %#v", record)
	}
}

func TestRepeatedStopDoesNotDuplicateInteractionCancellation(t *testing.T) {
	provider := &sequenceProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	msg := testInboundMessage(bus.InboundMessage{
		Content:    "/stop",
		SessionKey: session.BuildOpaqueSessionKey("agent:main:test:repeated-interaction-stop"),
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", ChatType: "direct",
			SenderID: "user-1", MessageID: "stop-first",
		},
	})
	record, _ := prepareWaitingControlInteraction(t, al, agent, msg, "")
	coordinator := newInboundTurnCoordinator(al)
	coordinator.handleInbound(t.Context(), msg)
	select {
	case <-al.bus.(*bus.MessageBus).OutboundChan():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first stop reply")
	}

	msg.Context.MessageID = "stop-second"
	coordinator.handleInbound(t.Context(), msg)
	select {
	case outbound := <-al.bus.(*bus.MessageBus).OutboundChan():
		if outbound.Content != "No active task to stop." {
			t.Fatalf("repeated stop reply = %q", outbound.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for repeated stop reply")
	}

	history := agent.Sessions.GetHistory(record.Route.SessionKey)
	if got := countInteractionToolResults(history, record.Origin.ToolCallID); got != 1 {
		t.Fatalf("repeated stop cancellation tool results = %d, want 1", got)
	}
	provider.mu.Lock()
	calls := provider.callCount
	provider.mu.Unlock()
	if calls != 0 {
		t.Fatalf("model calls after repeated stop = %d, want 0", calls)
	}
}

func TestInteractionControlCancellationRequiresAuthorizedRoute(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*bus.InboundMessage)
	}{
		{name: "channel", mutate: func(msg *bus.InboundMessage) { msg.Context.Channel = "discord" }},
		{name: "account", mutate: func(msg *bus.InboundMessage) { msg.Context.Account = "secondary" }},
		{name: "sender", mutate: func(msg *bus.InboundMessage) { msg.Context.SenderID = "other-user" }},
		{name: "chat", mutate: func(msg *bus.InboundMessage) { msg.Context.ChatID = "other-chat" }},
		{name: "topic", mutate: func(msg *bus.InboundMessage) { msg.Context.TopicID = "other-topic" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
			defer cleanup()
			msg := testInboundMessage(bus.InboundMessage{
				Content:    "/stop",
				SessionKey: session.BuildOpaqueSessionKey("agent:main:test:unauthorized-" + tt.name),
				Context: bus.InboundContext{
					Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
					TopicID: "topic-1", SenderID: "user-1",
				},
			})
			record, target := prepareWaitingControlInteraction(t, al, agent, msg, "")
			tt.mutate(&msg)

			result, err := al.cancelInteractionForControlMessage(t.Context(), msg, target)
			if err != nil {
				t.Fatal(err)
			}
			if result.Matched || result.Canceled || result.Failed || result.CommandHandled {
				t.Fatalf("unauthorized cancellation result = %#v", result)
			}
			record, _ = al.interactionRegistryForWorkspace(agent.Workspace).Get(record.ID)
			if record.Status != interactions.StatusWaiting {
				t.Fatalf("unauthorized route canceled interaction: %#v", record)
			}
		})
	}
}

func TestSessionControlCommandsCancelInteractionAndContinueNormally(t *testing.T) {
	tests := []struct {
		command   string
		replyText string
	}{
		{command: "/new", replyText: "Started a fresh session and cleared the current goal."},
		{command: "/reset", replyText: "Started a fresh session."},
		{command: "/clear", replyText: "Chat history cleared!"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			provider := &sequenceProvider{}
			al, agent, cleanup := newTurnCoordTestLoop(t, provider)
			defer cleanup()
			msg := testInboundMessage(bus.InboundMessage{
				Content:    tt.command,
				SessionKey: session.BuildOpaqueSessionKey("agent:main:test:control-" + tt.command[1:]),
				Context: bus.InboundContext{
					Channel: "telegram", Account: "primary", ChatID: "chat-1", ChatType: "direct",
					TopicID: "topic-1", SenderID: "user-1",
				},
			})
			record, _ := prepareWaitingControlInteraction(t, al, agent, msg, "")

			newInboundTurnCoordinator(al).handleInbound(t.Context(), msg)
			select {
			case outbound := <-al.bus.(*bus.MessageBus).OutboundChan():
				if !strings.Contains(outbound.Content, tt.replyText) {
					t.Fatalf("%s reply = %q", tt.command, outbound.Content)
				}
				if strings.Contains(outbound.Content, "Task stopped.") {
					t.Fatalf("%s emitted an extra stop acknowledgement: %q", tt.command, outbound.Content)
				}
			case <-time.After(time.Second):
				t.Fatalf("timed out waiting for %s reply", tt.command)
			}

			record, _ = al.interactionRegistryForWorkspace(agent.Workspace).Get(record.ID)
			wantCode := "session_control_" + tt.command[1:]
			if record.Status != interactions.StatusCancelled || record.FailureCode != wantCode {
				t.Fatalf("%s interaction = %#v", tt.command, record)
			}
			provider.mu.Lock()
			calls := provider.callCount
			provider.mu.Unlock()
			if calls != 0 {
				t.Fatalf("%s model calls = %d, want 0", tt.command, calls)
			}
		})
	}
}

func TestRecoveryCompletesDurableStopCancellation(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	cleanupTool := &turnCleanupTestTool{
		countingTestTool: &countingTestTool{name: "recovered-stop-cleanup"},
	}
	agent.Tools.Register(cleanupTool)
	sessionKey := "session-stop-cancel-recovery"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-question", Name: "request_user_input", Arguments: map[string]any{},
		}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	request.Origin.ExecutionID = "execution-stop-recovery"
	request.Origin.ExecutionContext = &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", ActorID: "actor-recovery",
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.BeginCancellation(record.ID, record.Revision, "session_control_stop")
	if err != nil || record.Status != interactions.StatusCanceling {
		t.Fatalf("begin cancellation = (%#v, %v)", record, err)
	}

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusCancelled {
		t.Fatalf("record after cancellation recovery = %#v", record)
	}
	_, resultIndex := interactionToolPairIndexes(agent.Sessions.GetHistory(sessionKey), "call-question")
	if resultIndex < 0 {
		t.Fatal("cancellation recovery left the tool call unpaired")
	}
	result := agent.Sessions.GetHistory(sessionKey)[resultIndex]
	if !strings.Contains(result.Content, `"outcome":"canceled"`) {
		t.Fatalf("recovered cancellation result = %q", result.Content)
	}
	if cleanupTool.cleanupCalls != 1 ||
		cleanupTool.executionID != request.Origin.ExecutionID ||
		cleanupTool.inbound.ActorID != "actor-recovery" {
		t.Fatalf(
			"recovered cleanup = calls %d, execution %q, inbound %#v",
			cleanupTool.cleanupCalls,
			cleanupTool.executionID,
			cleanupTool.inbound,
		)
	}
}

func TestRecoveryRestoresWaitingQuestionControlsWithoutRepublishingPrompt(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	al.channelManager = manager
	request := testToolSuspensionRequest(agent.Workspace)
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	markTestInteractionWaiting(t, registry, record)

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 0 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 0 durable transitions", recovered)
	}
	select {
	case synced := <-manager.synced:
		metadata := synced.Metadata
		if synced.Channel != "telegram" || synced.Context.SenderID != "user-1" ||
			!metadata.IsQuestionPrompt() || !reflect.DeepEqual(
			metadata.InteractionChoices(),
			[]string{"Canary", "All"},
		) {
			t.Fatalf("synced controls = %#v", synced)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting question controls were not restored")
	}
	select {
	case duplicate := <-manager.sent:
		t.Fatalf("recovery republished prompt: %#v", duplicate)
	default:
	}
}

func TestRecoveryRestoresWaitingApprovalControlsWithoutRepublishingPrompt(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	al.channelManager = manager
	request := testToolSuspensionRequest(agent.Workspace)
	request.Prompt.Kind = interactions.KindApproval
	request.Prompt.Questions = nil
	request.Origin.ArgumentHash = strings.Repeat("a", 64)
	request.Origin.ExecutionContext = &bus.InboundContext{
		Channel: "telegram", ChatID: "chat-1", SenderID: "user-1",
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		ApprovalAction: "Run the protected action", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	markTestInteractionWaiting(t, registry, record)

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 0 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 0 durable transitions", recovered)
	}
	select {
	case synced := <-manager.synced:
		metadata := synced.Metadata
		if synced.Channel != "telegram" || synced.Context.SenderID != "user-1" ||
			!metadata.IsApprovalPrompt() {
			t.Fatalf("synced approval controls = %#v", synced)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting approval controls were not restored")
	}
	select {
	case duplicate := <-manager.sent:
		t.Fatalf("recovery republished prompt: %#v", duplicate)
	default:
	}
}

func TestDeferredInteractionIngressQueuesWithoutChangingHistory(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	sessionKey := "session-deferred-interaction"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "assistant", Content: "existing"})
	target := &inboundDispatchTarget{Agent: agent, SessionKey: sessionKey}
	msg := bus.InboundMessage{
		Content: "unrelated turn", SpoolID: "spool-2",
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", SenderID: "user-2", MessageID: "message-2",
		},
	}
	newInboundTurnCoordinator(al).deferInteractionInbound(t.Context(), msg, target)
	scope := newRuntimeSessionScope(agent.Workspace, sessionKey)
	if got := al.pendingSteeringCountForScope(scope); got != 1 {
		t.Fatalf("deferred queue depth = %d, want 1", got)
	}
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) != 1 || history[0].Content != "existing" {
		t.Fatalf("deferred ingress changed history: %#v", history)
	}
	queued := al.dequeueSteeringMessagesForTurn(scope, "user-2")
	if len(queued) != 1 || queued[0].InboundSpoolID != "spool-2" ||
		!strings.Contains(queued[0].Content, "unrelated turn") {
		t.Fatalf("deferred message = %#v", queued)
	}
}

func TestResumeClaimedInteractionAppendsOneToolResultAndResolves(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	workspace := agent.Workspace
	sessionKey := "session-resume"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "user", Content: "Deploy this"})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-question", Name: "request_user_input", Arguments: map[string]any{},
		}},
	})
	registry := al.interactionRegistryForWorkspace(workspace)
	request := testToolSuspensionRequest(workspace)
	request.Route.SessionKey = sessionKey
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", Values: map[string]string{"deploy_mode": "Canary"}, MessageID: "answer-1",
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	inbound := inboundContextForInteraction(record.Route)
	scope := &session.SessionScope{
		Version: session.ScopeVersion, AgentID: agent.ID, Channel: record.Route.Channel,
		RouteScopeKey: record.Route.RouteSessionKey,
	}
	if err := al.resumeClaimedInteraction(
		t.Context(), registry, agent.Workspace, agent, scope, inbound, record,
	); err != nil {
		t.Fatalf("resumeClaimedInteraction() error = %v", err)
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.Status != interactions.StatusResolved || len(resolved.FinalDeliveryIDs) == 0 {
		t.Fatalf("record status = %q, want resolved", resolved.Status)
	}
	history := agent.Sessions.GetHistory(sessionKey)
	toolResults := 0
	for _, message := range history {
		if message.Role == "tool" && message.ToolCallID == "call-question" {
			toolResults++
			if !strings.Contains(message.Content, `"deploy_mode":"Canary"`) {
				t.Fatalf("tool result = %q", message.Content)
			}
		}
	}
	if toolResults != 1 {
		t.Fatalf("matching tool results = %d, want 1", toolResults)
	}
	select {
	case outbound := <-manager.sent:
		if strings.TrimSpace(outbound.Content) == "" {
			t.Fatalf("final outbound = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resumed final response")
	}
}

func TestInteractionWorkerReleasesSessionBeforeDrainingDeferredIngress(t *testing.T) {
	provider := &interactionDrainProvider{
		secondStarted: make(chan struct{}), secondRelease: make(chan struct{}),
	}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	al.channelManager = newInteractionChannelManager()

	sessionKey := "session-interaction-drain"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "user", Content: "Deploy this"})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-drain-question", Name: "request_user_input", Arguments: map[string]any{},
		}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	request.Origin.ToolCallID = "call-drain-question"
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	markTestInteractionWaiting(t, registry, record)

	scope := newRuntimeSessionScope(agent.Workspace, sessionKey)
	const followUp = "Check the deployment after approval."
	if err = al.enqueueSteeringMessageWithSender(scope, agent.ID, "user-1", providers.Message{
		Role: "user", Content: followUp,
	}); err != nil {
		t.Fatal(err)
	}
	target := &inboundDispatchTarget{
		Agent: agent, SessionKey: sessionKey,
		RouteClaimKey: runtimeRouteClaimKey(request.Route.RouteSessionKey, ""),
		Allocation:    session.Allocation{RouteScopeKey: request.Route.RouteSessionKey},
	}
	claim, _, claimed := al.turns.claimRuntimeRouteSession(target, "pending-interaction-drain")
	if !claimed {
		t.Fatal("failed to claim interaction session")
	}
	answer := bus.InboundMessage{
		Content: "Canary", Context: inboundContextForInteraction(request.Route),
	}
	answer.Context.MessageID = "answer-drain"

	done := make(chan struct{})
	go func() {
		defer close(done)
		newInboundTurnCoordinator(al).runInteractionWorker(t.Context(), answer, target, claim)
	}()
	select {
	case <-provider.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for deferred continuation")
	}
	if contender, _, contenderClaimed := al.turns.claimRuntimeRouteSession(
		target,
		"newer-inbound-during-drain",
	); contenderClaimed {
		contender.releaseIfOwned()
		t.Fatal("newer inbound claimed the route during deferred drain")
	}
	close(provider.secondRelease)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for interaction drain")
	}

	if active := al.GetActiveTurnByScope(agent.Workspace, sessionKey); active != nil {
		t.Fatalf("interaction drain left active turn: %#v", active)
	}
	if got := al.pendingSteeringCountForScope(scope); got != 0 {
		t.Fatalf("deferred queue depth = %d, want 0", got)
	}
	followUps := 0
	for _, message := range agent.Sessions.GetHistory(sessionKey) {
		if message.Role == "user" && strings.Contains(message.Content, followUp) {
			followUps++
		}
	}
	if followUps != 1 {
		t.Fatalf("deferred follow-up history entries = %d, want 1", followUps)
	}
}

func TestRecoverHumanInteractionsResumesDurableClaimAfterRestartWindow(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	sessionKey := "session-recover-interaction"
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{Role: "user", Content: "Deploy this"})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-question", Name: "request_user_input", Arguments: map[string]any{},
		}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", Values: map[string]string{"deploy_mode": "Canary"}, MessageID: "answer-recover",
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != interactions.StatusClaimed {
		t.Fatalf("status before recovery = %q", record.Status)
	}
	scope := newRuntimeSessionScope(agent.Workspace, sessionKey)
	if err := al.enqueueSteeringMessageWithSender(scope, agent.ID, "user-2", providers.Message{
		Role: "user", Content: "Check the deployment after recovery.",
	}); err != nil {
		t.Fatal(err)
	}

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusResolved || len(record.FinalDeliveryIDs) == 0 {
		t.Fatalf("status after recovery = %q", record.Status)
	}
	if got := al.pendingSteeringCountForScope(scope); got != 0 {
		t.Fatalf("deferred queue depth after recovery = %d, want 0", got)
	}
	foundDeferred := false
	for _, message := range agent.Sessions.GetHistory(sessionKey) {
		if message.Role == "user" && strings.Contains(message.Content, "Check the deployment") {
			foundDeferred = true
			break
		}
	}
	if !foundDeferred {
		t.Fatal("recovery did not continue the deferred inbound message")
	}
	select {
	case outbound := <-manager.sent:
		if strings.TrimSpace(outbound.Content) == "" {
			t.Fatalf("recovery outbound = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovered continuation")
	}
}

func TestRecoverResumingInteractionReplaysPersistedFinalWithoutModelCall(t *testing.T) {
	provider := &toolCallRespProvider{toolName: "must_not_run", response: "must not run"}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	sessionKey := session.BuildOpaqueSessionKey("agent:main:test:recover-final")
	const (
		taskID        = "recover-final-task"
		interactionID = "recover-final-interaction"
	)
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-question"}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	request.Origin.TaskID = taskID
	request.Origin.ContinuationSessionKey = sessionKey
	tasks := al.taskRegistryForWorkspace(agent.Workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: taskID, Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "recover final", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
		DeliveryMode:   string(toolshared.AsyncDeliveryUserOnly),
		Channel:        "telegram", ChatID: "chat-1", RequesterSessionKey: sessionKey,
	}); err != nil {
		t.Fatal(err)
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		ID:   interactionID,
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, _ = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", Values: map[string]string{"deploy_mode": "Canary"},
	}, interactions.OutcomeAnswered)
	if ensureErr := al.ensureInteractionToolResult(t.Context(), agent, record); ensureErr != nil {
		t.Fatal(ensureErr)
	}
	_, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	recoveredDeliverable := &taskresult.Deliverable{
		Text: "tool-owned recovered result",
		Artifacts: []taskresult.Artifact{{
			Ref: "file:/tmp/recovered.txt", LocalPath: "/tmp/recovered.txt", Kind: "file",
		}},
		Metadata: map[string]string{"producer": "recovered-tool"},
		Report: &taskresult.Report{
			SchemaVersion: taskresult.ReportSchemaV1,
			ReportID:      "recovered-report",
		},
	}
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", Content: "Recovered final", Deliverable: recoveredDeliverable,
	})

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	if provider.callCount != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.callCount)
	}
	record, _ = registry.Get(record.ID)
	if record.Status != interactions.StatusResolved || len(record.FinalDeliveryIDs) == 0 {
		t.Fatalf("status = %q, want resolved", record.Status)
	}
	task, _ := tasks.Get(taskID)
	if task.Status != taskregistry.StatusSucceeded || task.Deliverable == nil ||
		task.Deliverable.Text != "tool-owned recovered result" || len(task.Deliverable.Artifacts) != 1 ||
		task.Deliverable.Artifacts[0].Ref != "file:/tmp/recovered.txt" ||
		task.Deliverable.Metadata["producer"] != "recovered-tool" ||
		task.Deliverable.Report == nil || task.Deliverable.Report.ReportID != "recovered-report" {
		t.Fatalf("recovered task lost canonical deliverable: %#v", task)
	}
	select {
	case outbound := <-manager.sent:
		if outbound.Content != "Recovered final" {
			t.Fatalf("outbound = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replayed final")
	}
}

func TestRecoverResumingInteractionHydratesJournaledDeliverableBeforeFinal(t *testing.T) {
	provider := &interactionCaptureProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	sessionKey := session.BuildOpaqueSessionKey("agent:main:test:recover-open-tool-round")
	const (
		taskID        = "recover-open-tool-round-task"
		interactionID = "recover-open-tool-round-interaction"
	)
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "user", Content: "previous request", RootTurnStart: true,
	})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-prior-final-handled"}},
	})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "tool", ToolCallID: "call-prior-final-handled", Content: "prior final-handled result",
		ToolResultStatus: providers.ToolResultStatusSuccess,
		Deliverable: &taskresult.Deliverable{
			Artifacts: []taskresult.Artifact{{Ref: "file:/tmp/prior-turn.txt", Kind: "file", Delivered: true}},
			Metadata:  map[string]string{"turn": "prior"},
		},
	})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "user", Content: "current request", RootTurnStart: true,
	})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-before-chained-interaction"}},
	})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "tool", ToolCallID: "call-before-chained-interaction", Content: "earlier structured result",
		ToolResultStatus: providers.ToolResultStatusSuccess,
		Deliverable: &taskresult.Deliverable{
			Artifacts: []taskresult.Artifact{{Ref: "file:/tmp/before-interaction.txt", Kind: "file"}},
			Metadata:  map[string]string{"phase": "before-interaction"},
		},
	})
	agent.Sessions.AddFullMessage(sessionKey, steeringPromptMessage(providers.Message{
		Role: "user", Content: "keep the current request",
	}))
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-question"}},
	})
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	request.Origin.TaskID = taskID
	request.Origin.ContinuationSessionKey = sessionKey
	tasks := al.taskRegistryForWorkspace(agent.Workspace)
	if err := tasks.Upsert(taskregistry.Record{
		TaskID: taskID, Runtime: taskregistry.RuntimeSubagent,
		TaskKind: "spawn", Task: "recover open tool round", Status: taskregistry.StatusRunning,
		DeliveryStatus: taskregistry.DeliveryPending,
		DeliveryMode:   string(toolshared.AsyncDeliveryUserOnly),
		Channel:        "telegram", ChatID: "chat-1", RequesterSessionKey: sessionKey,
	}); err != nil {
		t.Fatal(err)
	}
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		ID:   interactionID,
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, _ = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", Values: map[string]string{"deploy_mode": "Canary"},
	}, interactions.OutcomeAnswered)
	if ensureErr := al.ensureInteractionToolResult(t.Context(), agent, record); ensureErr != nil {
		t.Fatal(ensureErr)
	}
	_, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-produced-result"}},
	})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "tool", ToolCallID: "call-produced-result", Content: "structured result",
		ToolResultStatus: providers.ToolResultStatusSuccess,
		Deliverable: &taskresult.Deliverable{
			Text:      "tool-owned recovered result",
			Artifacts: []taskresult.Artifact{{Ref: "file:/tmp/recovered.txt", Kind: "file"}},
			Metadata:  map[string]string{"producer": "recovered-tool"},
			Report: &taskresult.Report{
				SchemaVersion: taskresult.ReportSchemaV1,
				ReportID:      "recovered-report",
			},
		},
	})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-produced-second-result"}},
	})
	agent.Sessions.AddFullMessage(sessionKey, providers.Message{
		Role: "tool", ToolCallID: "call-produced-second-result", Content: "second structured result",
		ToolResultStatus: providers.ToolResultStatusSuccess,
		Deliverable: &taskresult.Deliverable{
			Artifacts: []taskresult.Artifact{{Ref: "file:/tmp/second.txt", Kind: "file"}},
			Metadata:  map[string]string{"round": "second"},
		},
	})
	if closeErr := agent.Sessions.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	reloadedSessions, err := initRuntimeSessionStore(filepath.Join(agent.Workspace, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	agent.Sessions = reloadedSessions
	var foundCurrentRoot, foundReloadedSteering bool
	for _, message := range agent.Sessions.GetHistory(sessionKey) {
		switch message.Content {
		case "current request":
			foundCurrentRoot = message.RootTurnStart
		case "keep the current request":
			foundReloadedSteering = true
			if message.RootTurnStart || message.PromptSource != "" {
				t.Fatalf("reloaded steering acquired root identity: %#v", message)
			}
		}
	}
	if !foundCurrentRoot || !foundReloadedSteering {
		t.Fatalf(
			"reloaded turn identity incomplete: root=%v steering=%v",
			foundCurrentRoot,
			foundReloadedSteering,
		)
	}

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	for _, message := range provider.messages {
		if message.Deliverable != nil {
			t.Fatalf("provider received canonical deliverable: %#v", message)
		}
	}
	task, _ := tasks.Get(taskID)
	if task.Status != taskregistry.StatusSucceeded || task.Deliverable == nil ||
		task.Deliverable.Text != "tool-owned recovered result" || len(task.Deliverable.Artifacts) != 3 ||
		task.Deliverable.Artifacts[0].Ref != "file:/tmp/before-interaction.txt" ||
		task.Deliverable.Artifacts[1].Ref != "file:/tmp/recovered.txt" ||
		task.Deliverable.Artifacts[2].Ref != "file:/tmp/second.txt" ||
		task.Deliverable.Metadata["phase"] != "before-interaction" ||
		task.Deliverable.Metadata["producer"] != "recovered-tool" ||
		task.Deliverable.Metadata["round"] != "second" ||
		task.Deliverable.Metadata["turn"] != "" ||
		task.Deliverable.Report == nil || task.Deliverable.Report.ReportID != "recovered-report" {
		t.Fatalf("open-round recovery lost canonical deliverable: task=%#v deliverable=%+v", task, task.Deliverable)
	}
	select {
	case outbound := <-manager.sent:
		if outbound.Content != "continued with corrected navigation" {
			t.Fatalf("outbound = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovered completion")
	}
}

func TestInteractionFinalAfterToolResultRequiresMatchingOrder(t *testing.T) {
	history := []providers.Message{
		{Role: "assistant", Content: "old"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-1"}}},
		{Role: "tool", ToolCallID: "call-1", Content: "answer"},
		{Role: "assistant", Content: "continued"},
	}
	if content, deliverable, ok := interactionFinalAfterToolResult(
		history,
		"call-1",
	); !ok || content != "continued" ||
		deliverable != nil {
		t.Fatalf("interactionFinalAfterToolResult() = (%q, %#v, %v)", content, deliverable, ok)
	}
	if _, _, ok := interactionFinalAfterToolResult(history, "other"); ok {
		t.Fatal("unmatched tool result produced a final response")
	}
}

func TestInteractionFinalAfterToolResultDetachesDeliverable(t *testing.T) {
	history := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-1"}}},
		{Role: "tool", ToolCallID: "call-1", Content: "answer"},
		{
			Role: "assistant", Content: "continued",
			Deliverable: &taskresult.Deliverable{
				Text:     "tool-owned result",
				Metadata: map[string]string{"producer": "tool"},
			},
		},
	}

	_, deliverable, ok := interactionFinalAfterToolResult(history, "call-1")
	if !ok || deliverable == nil {
		t.Fatalf("interaction final = (%#v, %t), want deliverable", deliverable, ok)
	}
	history[2].Deliverable.Metadata["producer"] = "mutated"
	if deliverable.Text != "tool-owned result" || deliverable.Metadata["producer"] != "tool" {
		t.Fatalf("recovered deliverable was not detached: %#v", deliverable)
	}
}

func TestInteractionFinalAfterToolResultDoesNotDuplicateHandledAttachment(t *testing.T) {
	history := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-media"}}},
		{Role: "tool", ToolCallID: "call-media", Content: "delivered"},
		{
			Role:    "assistant",
			Content: handledToolResponseSummary,
			Attachments: []providers.Attachment{{
				Ref: "media://delivered",
			}},
		},
	}
	if content, _, ok := interactionFinalAfterToolResult(history, "call-media"); !ok || content != "" {
		t.Fatalf("interactionFinalAfterToolResult() = (%q, %v), want empty handled final", content, ok)
	}
}

func TestHandledAttachmentQuestionFinalRemovesTelegramControls(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	manager := newInteractionChannelManager()
	installInteractionChannelManager(t, al, manager)
	request := testToolSuspensionRequest(agent.Workspace)
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "Canary", MessageID: "answer-message", ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	history := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: record.Origin.ToolCallID}}},
		{Role: "tool", ToolCallID: record.Origin.ToolCallID, Content: "delivered"},
		{
			Role: "assistant", Content: handledToolResponseSummary,
			Attachments: []providers.Attachment{{Ref: "media://delivered"}},
		},
	}
	content, _, ok := interactionFinalAfterToolResult(history, record.Origin.ToolCallID)
	if !ok || content != "" {
		t.Fatalf("handled attachment final = (%q, %t)", content, ok)
	}
	if err = al.deliverInteractionFinal(
		t.Context(), registry, agent.Workspace, record,
		bus.InboundContext{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1"},
		content, nil, nil,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case acknowledgement := <-manager.sent:
		metadata := acknowledgement.Metadata
		if acknowledgement.Content != "Response recorded." ||
			acknowledgement.ReplyToMessageID != "answer-message" ||
			!metadata.RemovesInteractionControls() {
			t.Fatalf("handled attachment acknowledgement = %#v", acknowledgement)
		}
	case <-time.After(time.Second):
		t.Fatal("handled attachment final did not remove Telegram controls")
	}
	resolved, _ := registry.Get(record.ID)
	if resolved.Status != interactions.StatusResolved || len(resolved.FinalDeliveryIDs) == 0 {
		t.Fatalf("handled attachment interaction = %#v", resolved)
	}
}

func TestInteractionPairingIgnoresReusedToolCallIDFromOlderRound(t *testing.T) {
	history := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-reused"}}},
		{Role: "tool", ToolCallID: "call-reused", Content: "old result"},
		{Role: "assistant", Content: "old final"},
		{Role: "user", Content: "new request"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call-reused"}}},
	}
	origin, result := interactionToolPairIndexes(history, "call-reused")
	if origin != 4 || result != -1 {
		t.Fatalf("interactionToolPairIndexes() = (%d, %d), want (4, -1)", origin, result)
	}
	if _, _, ok := interactionFinalAfterToolResult(history, "call-reused"); ok {
		t.Fatal("older reused result was treated as current continuation")
	}
}

func TestRecoverHumanInteractionsTerminalizesCatalogedOrphanWorkspace(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	catalogRoot := t.TempDir()
	orphanWorkspace := filepath.Join(catalogRoot, "removed-agent-workspace")
	catalog := interactions.NewWorkspaceCatalog(catalogRoot)
	if err := catalog.Register(orphanWorkspace); err != nil {
		t.Fatal(err)
	}
	al.interactions.catalog = catalog

	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(orphanWorkspace))
	request := testToolSuspensionRequest(orphanWorkspace)
	request.Route.AgentID = "removed-agent"
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record = markTestInteractionWaiting(t, registry, record)

	if recovered := al.RecoverHumanInteractions(t.Context()); recovered != 1 {
		t.Fatalf("RecoverHumanInteractions() = %d, want 1", recovered)
	}
	loaded := al.interactionRegistryForWorkspace(orphanWorkspace)
	terminal, ok := loaded.Get(record.ID)
	if !ok || terminal.Status != interactions.StatusFailed ||
		terminal.FailureCode != "agent_unavailable" {
		t.Fatalf("orphan record = %#v", terminal)
	}
	if current, ok := al.GetRegistry().GetAgent(agent.ID); !ok || current == nil {
		t.Fatal("active agent was disturbed while recovering orphan workspace")
	}
}

func TestDrainQueuedSteeringStopsWhileInteractionIsNonterminal(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	sessionKey := "session-suspended-steering"
	request := testToolSuspensionRequest(agent.Workspace)
	request.Route.SessionKey = sessionKey
	registry := al.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: request.Prompt.Kind, Route: request.Route, Origin: request.Origin,
		Questions: request.Prompt.Questions, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	markTestInteractionWaiting(t, registry, record)
	scope := newRuntimeSessionScope(agent.Workspace, sessionKey)
	if err = al.enqueueSteeringMessageWithSender(scope, agent.ID, "user-2", providers.Message{
		Role: "user", Content: "message that arrived during suspension",
	}); err != nil {
		t.Fatal(err)
	}

	continued, err := al.drainQueuedSteeringContinuations(t.Context(), &continuationTarget{
		AgentID:    agent.ID,
		SessionKey: sessionKey,
		Channel:    "telegram",
		ChatID:     "chat-1",
		Workspace:  agent.Workspace,
	})
	if err != nil || continued != "" {
		t.Fatalf("drain = (%q, %v), want empty success", continued, err)
	}
	if got := al.pendingSteeringCountForScope(scope); got != 1 {
		t.Fatalf("deferred queue depth = %d, want 1", got)
	}
}

func TestDrainDeferredInteractionIngressReleasesSteeringAfterAggregateRejection(t *testing.T) {
	rejection := errors.New("deferred aggregate rejected")
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	trackingBus := &finalResponseAdmissionTestBus{
		MessageBus: al.bus.(*bus.MessageBus),
		publishErr: rejection,
	}
	setTestMessageBus(al, trackingBus)
	sessionKey := "session-deferred-admission"
	if err := al.enqueueSteeringMessageWithSender(
		newRuntimeSessionScope(agent.Workspace, sessionKey),
		agent.ID,
		"user-2",
		providers.Message{
			Role:           "user",
			Content:        "deferred interaction follow-up",
			InboundSpoolID: "spool-deferred",
		},
	); err != nil {
		t.Fatal(err)
	}

	err := al.drainDeferredInteractionIngress(t.Context(), agent.Workspace, interactions.Route{
		AgentID: agent.ID, SessionKey: sessionKey, Channel: "telegram", ChatID: "chat-1",
	}, bus.InboundContext{Channel: "telegram", ChatID: "chat-1"})

	if !errors.Is(err, rejection) {
		t.Fatalf("drainDeferredInteractionIngress() error = %v, want %v", err, rejection)
	}
	acked, released, cause := trackingBus.ownership()
	if len(acked) != 0 || !containsExactly(released, "spool-deferred") {
		t.Fatalf("deferred ownership = acked:%v released:%v", acked, released)
	}
	if !errors.Is(cause, rejection) {
		t.Fatalf("release cause = %v, want %v", cause, rejection)
	}
}

func TestRecoveryKeepsCatalogEntryWhenRegistryLoadFails(t *testing.T) {
	provider := &simpleConvProvider{}
	al, _, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	catalogRoot := t.TempDir()
	workspace := filepath.Join(catalogRoot, "corrupt-workspace")
	storePath := interactions.WorkspaceStorePath(workspace)
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := interactions.NewWorkspaceCatalog(catalogRoot)
	if err := catalog.Register(workspace); err != nil {
		t.Fatal(err)
	}
	al.interactions.catalog = catalog

	al.RecoverHumanInteractions(t.Context())
	workspaces, err := catalog.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 1 || workspaces[0] != workspace {
		t.Fatalf("catalog workspaces = %#v, want corrupt store retained", workspaces)
	}
}
