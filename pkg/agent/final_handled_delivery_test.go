package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type finalHandledSettlementDelivery struct {
	store       session.SessionStore
	sessionKey  string
	delivered   bool
	pending     bool
	ambiguous   bool
	calls       int
	pendingSeen bool
	cancel      context.CancelFunc
	hardAbort   func()
}

func (d *finalHandledSettlementDelivery) applySyncToolResultDelivery(
	_ context.Context,
	_ *turnState,
	result *toolshared.ToolResult,
	_ string,
) ([]providers.Attachment, *toolshared.ToolResult) {
	d.calls++
	history := d.store.GetHistory(d.sessionKey)
	for _, pending := range history {
		if pending.Role == "tool" &&
			pending.Content == finalHandledDeliveryPendingContent &&
			pending.ToolResultStatus == providers.ToolResultStatusUnresolved {
			d.pendingSeen = true
			break
		}
	}
	if d.hardAbort != nil {
		d.hardAbort()
	}
	if d.pending {
		if d.cancel != nil {
			d.cancel()
		}
		err := fmt.Errorf("%w: context canceled", errFinalHandledDeliveryPending)
		result.ResponseHandled = false
		return nil, wrapToolDeliveryError(result, "delivery confirmation pending", err)
	}
	if d.delivered {
		result.ForLLM = "Message confirmed delivered to the user."
		return nil, result
	}
	if d.ambiguous {
		err := fmt.Errorf("%w: acceptance unknown", errFinalHandledDeliveryAmbiguous)
		return nil, wrapToolDeliveryError(result, "delivery outcome is ambiguous", err)
	}
	err := &channels.MediaConstraintError{
		Channel: "telegram",
		Ref:     "media://oversized-video",
		Size:    132186801,
		MaxSize: 50000000,
	}
	return nil, wrapToolDeliveryError(result, "failed to deliver attachment: "+err.Error(), err)
}

type recordingCanonicalIngest struct {
	messages []providers.Message
}

func (*recordingCanonicalIngest) Assemble(
	context.Context,
	*AssembleRequest,
) (*AssembleResponse, error) {
	return &AssembleResponse{}, nil
}

func (*recordingCanonicalIngest) Compact(context.Context, *CompactRequest) error {
	return nil
}

func (r *recordingCanonicalIngest) Ingest(_ context.Context, req *IngestRequest) error {
	if req != nil {
		r.messages = append(r.messages, req.Message)
	}
	return nil
}

type mutateFailingSessionStore struct {
	session.SessionStore
	err    error
	failAt int
	calls  int
}

func (s *mutateFailingSessionStore) MutateTurnHistory(
	ctx context.Context,
	sessionKey string,
	mutate func([]providers.Message) ([]providers.Message, bool, error),
) (bool, error) {
	s.calls++
	if s.failAt == 0 || s.calls == s.failAt {
		return false, s.err
	}
	return s.SessionStore.MutateTurnHistory(ctx, sessionKey, mutate)
}

type finalHandledReceiptManager struct {
	recordingChannelManager
	preflightErr error
}

func (m *finalHandledReceiptManager) PreflightMedia(
	context.Context,
	bus.OutboundMediaMessage,
) error {
	return m.preflightErr
}

func (*finalHandledReceiptManager) SupportsDurableDeliveryReceipts() bool {
	return true
}

type finalHandledRequestProvider struct {
	toolCalls []providers.ToolCall
	requests  [][]providers.Message
}

func (p *finalHandledRequestProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.requests = append(p.requests, append([]providers.Message(nil), messages...))
	if len(p.requests) == 1 {
		return &providers.LLMResponse{ToolCalls: append([]providers.ToolCall(nil), p.toolCalls...)}, nil
	}
	return &providers.LLMResponse{Content: "settlement observed", FinishReason: "stop"}, nil
}

func (*finalHandledRequestProvider) GetDefaultModel() string {
	return "final-handled-request-model"
}

func TestPipelineFinalHandledDeliveryCanonicalizesSettlement(t *testing.T) {
	for _, hook := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			for _, delivered := range []bool{false, true} {
				name := fmt.Sprintf("hook_%t/legacy_%t/delivered_%t", hook, legacy, delivered)
				t.Run(name, func(t *testing.T) {
					const sessionKey = "final-handled-settlement"
					store := session.NewSessionManager("")
					result := &toolshared.ToolResult{
						ForLLM: "Message prepared for delivery to telegram:chat-1",
						Silent: true,
					}
					if legacy {
						result.Media = []string{"media://legacy-final-handled"}
						result.WithResponseHandled()
					} else {
						result.WithOutboundDelivery(toolshared.OutboundDelivery{
							Channel: "telegram",
							ChatID:  "chat-1",
							Text:    "hello",
						}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
					}
					result.WriteAudit = []toolshared.WriteAuditEntry{{
						Target:  "outbound:telegram:chat-1",
						Action:  "send",
						Success: true,
					}}
					tool := &fixedToolResultTool{name: "final_message", result: result}
					registry := tools.NewToolRegistry()
					registry.Register(tool)
					agent := &AgentInstance{ID: "main", Tools: registry, Sessions: store}
					ts := &turnState{
						agent:      agent,
						agentID:    agent.ID,
						turnID:     "turn-final-handled",
						sessionKey: sessionKey,
						session:    store,
						opts: processOptions{
							SendResponse: true,
							Dispatch:     DispatchRequest{SessionKey: sessionKey},
						},
					}
					toolCall := providers.ToolCall{
						ID:        "call-final-message",
						Name:      tool.Name(),
						Arguments: map[string]any{},
					}
					intent := providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{toolCall}}
					if err := store.AppendTurnMessage(t.Context(), sessionKey, intent); err != nil {
						t.Fatal(err)
					}
					exec := newTurnExecution(agent, ts.opts, nil, "", []providers.Message{intent})
					llm := newLLMIterationState(1)
					llm.normalizedToolCalls = []providers.ToolCall{toolCall}
					llm.assistantToolCallsPersisted = true
					settlement := &finalHandledSettlementDelivery{
						store:      store,
						sessionKey: sessionKey,
						delivered:  delivered,
					}
					ingest := &recordingCanonicalIngest{}
					pipeline := &Pipeline{
						Context: PipelineContextServices{Runtime: ingest},
						Interaction: PipelineInteractionServices{
							SyncToolDelivery: settlement,
						},
					}
					if hook {
						pipeline.Interaction.Hooks = &toolResultRespondHook{result: result}
					}

					outcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
					if outcome.JournalErr != nil {
						t.Fatalf("ExecuteTools() journal error = %v", outcome.JournalErr)
					}
					if settlement.calls != 1 || !settlement.pendingSeen {
						t.Fatalf(
							"settlement calls = %d, pending seen = %t",
							settlement.calls,
							settlement.pendingSeen,
						)
					}
					if hook && tool.executions != 0 {
						t.Fatalf("hook-response tool executions = %d, want 0", tool.executions)
					}

					historyResult := matchingToolResult(t, store.GetHistory(sessionKey), toolCall.ID)
					liveResult := matchingToolResult(t, exec.messages, toolCall.ID)
					if historyResult.Content != liveResult.Content ||
						historyResult.ToolResultStatus != liveResult.ToolResultStatus {
						t.Fatalf("canonical/live result mismatch: %#v / %#v", historyResult, liveResult)
					}
					if strings.Contains(historyResult.Content, finalHandledDeliveryPendingContent) ||
						strings.Contains(strings.ToLower(historyResult.Content), "prepared for delivery") {
						t.Fatalf("canonical result retained pre-settlement content: %q", historyResult.Content)
					}
					if delivered {
						if historyResult.ToolResultStatus != providers.ToolResultStatusSuccess ||
							!strings.Contains(historyResult.Content, "confirmed delivered") {
							t.Fatalf("delivered canonical result = %#v", historyResult)
						}
					} else {
						if historyResult.ToolResultStatus != providers.ToolResultStatusError ||
							!strings.Contains(historyResult.Content, "132186801 bytes") ||
							!strings.Contains(historyResult.Content, "reduce or transcode") ||
							strings.Contains(historyResult.Content, toolshared.HandledToolLLMNote) {
							t.Fatalf("failed canonical result = %#v", historyResult)
						}
					}
					if len(exec.writeAudit) != 1 || exec.writeAudit[0].Action != "send" {
						t.Fatalf("write audit = %#v", exec.writeAudit)
					}
					ingestedResult := matchingToolResult(t, ingest.messages, toolCall.ID)
					if ingestedResult.Content != historyResult.Content ||
						ingestedResult.ToolResultStatus != historyResult.ToolResultStatus {
						t.Fatalf("ingested/canonical tool result mismatch: %#v / %#v", ingestedResult, historyResult)
					}
				})
			}
		}
	}
}

func TestPipelineFinalHandledPendingReceiptLeavesBarrierUnresolved(t *testing.T) {
	const sessionKey = "final-handled-pending-receipt"
	store := session.NewSessionManager("")
	result := toolshared.MediaResult(
		"File prepared for delivery",
		[]string{"media://legacy-final-handled"},
	).WithResponseHandled()
	tool := &fixedToolResultTool{name: "send_file", result: result}
	sibling := &fixedToolResultTool{
		name:   "destructive_sibling",
		result: toolshared.SilentResult("sibling executed"),
	}
	registry := tools.NewToolRegistry()
	registry.Register(tool)
	registry.Register(sibling)
	agent := &AgentInstance{ID: "main", Tools: registry, Sessions: store}
	ts := &turnState{
		agent: agent, agentID: agent.ID, turnID: "turn-pending-receipt",
		sessionKey: sessionKey, session: store,
		opts: processOptions{Dispatch: DispatchRequest{SessionKey: sessionKey}},
	}
	toolCall := providers.ToolCall{ID: "call-send-file", Name: tool.Name(), Arguments: map[string]any{}}
	siblingCall := providers.ToolCall{
		ID: "call-destructive-sibling", Name: sibling.Name(), Arguments: map[string]any{},
	}
	userMessage := providers.Message{Role: "user", Content: "send the file, then mutate external state"}
	intent := providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{toolCall, siblingCall}}
	if err := store.AppendTurnMessage(t.Context(), sessionKey, userMessage); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurnMessage(t.Context(), sessionKey, intent); err != nil {
		t.Fatal(err)
	}
	exec := newTurnExecution(agent, ts.opts, nil, "", []providers.Message{userMessage, intent})
	llm := newLLMIterationState(1)
	llm.normalizedToolCalls = []providers.ToolCall{toolCall, siblingCall}
	llm.assistantToolCallsPersisted = true
	turnCtx, cancel := context.WithCancel(t.Context())
	settlement := &finalHandledSettlementDelivery{
		store: store, sessionKey: sessionKey, pending: true, cancel: cancel,
	}
	steering := providers.Message{
		Role: "user", Content: "use the corrected caption", InboundSpoolID: "steer-pending-1",
	}
	pipeline := &Pipeline{
		Context: PipelineContextServices{
			Steering: &oneShotLoopGuardSteering{messages: []providers.Message{steering}},
		},
		Interaction: PipelineInteractionServices{SyncToolDelivery: settlement},
	}

	outcome := pipeline.ExecuteTools(turnCtx, turnCtx, ts, exec, llm)
	if !errors.Is(outcome.JournalErr, errFinalHandledDeliveryPending) {
		t.Fatalf("journal error = %v, want pending delivery", outcome.JournalErr)
	}
	if settlement.calls != 1 || !settlement.pendingSeen {
		t.Fatalf("settlement calls = %d, pending seen = %t", settlement.calls, settlement.pendingSeen)
	}
	if sibling.executions != 0 {
		t.Fatalf("sibling executions = %d, want 0", sibling.executions)
	}
	if len(exec.pendingMessages) != 0 {
		t.Fatalf("pending steering was not transferred: %#v", exec.pendingMessages)
	}
	accepted := ts.acceptedSteeringSnapshot()
	if len(accepted) != 1 || accepted[0].InboundSpoolID != steering.InboundSpoolID {
		t.Fatalf("accepted steering = %#v, want transferred message", accepted)
	}
	for source, messages := range map[string][]providers.Message{
		"canonical history": store.GetHistory(sessionKey),
		"execution context": exec.messages,
		"live turn state":   ts.liveTurnMessages,
	} {
		message := matchingToolResult(t, messages, toolCall.ID)
		if message.Content != finalHandledDeliveryPendingContent ||
			message.ToolResultStatus != providers.ToolResultStatusUnresolved {
			t.Fatalf("%s barrier = %#v", source, message)
		}
		skipped := matchingToolResult(t, messages, siblingCall.ID)
		if !strings.Contains(skipped.Content, "final-handled outbound boundary") {
			t.Fatalf("%s skipped sibling = %#v", source, skipped)
		}
	}
	sanitized := sanitizeHistoryForProvider(store.GetHistory(sessionKey))
	barrier := matchingToolResult(t, sanitized, toolCall.ID)
	if barrier.ToolResultStatus != providers.ToolResultStatusUnresolved ||
		barrier.Content != finalHandledDeliveryPendingContent {
		t.Fatalf("sanitized barrier = %#v", barrier)
	}
}

func TestPipelineFinalHandledAmbiguousReceiptSettlesAndStopsTurn(t *testing.T) {
	const sessionKey = "final-handled-ambiguous-receipt"
	store := session.NewSessionManager("")
	result := (&toolshared.ToolResult{
		ForLLM: "Message prepared for delivery",
		Silent: true,
	}).WithOutboundDelivery(toolshared.OutboundDelivery{
		Channel: "telegram",
		ChatID:  "chat-1",
		Text:    "hello",
	}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	tool := &fixedToolResultTool{name: "send_message", result: result}
	registry := tools.NewToolRegistry()
	registry.Register(tool)
	agent := &AgentInstance{ID: "main", Tools: registry, Sessions: store}
	ts := &turnState{
		agent: agent, agentID: agent.ID, turnID: "turn-ambiguous-receipt",
		sessionKey: sessionKey, session: store,
		opts: processOptions{Dispatch: DispatchRequest{SessionKey: sessionKey}},
	}
	toolCall := providers.ToolCall{
		ID: "call-ambiguous-message", Name: tool.Name(), Arguments: map[string]any{},
	}
	intent := providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{toolCall}}
	if err := store.AppendTurnMessage(t.Context(), sessionKey, intent); err != nil {
		t.Fatal(err)
	}
	exec := newTurnExecution(agent, ts.opts, nil, "", []providers.Message{intent})
	llm := newLLMIterationState(1)
	llm.normalizedToolCalls = []providers.ToolCall{toolCall}
	llm.assistantToolCallsPersisted = true
	settlement := &finalHandledSettlementDelivery{
		store: store, sessionKey: sessionKey, ambiguous: true,
	}
	pipeline := &Pipeline{Interaction: PipelineInteractionServices{SyncToolDelivery: settlement}}

	outcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
	if !errors.Is(outcome.TurnErr, errFinalHandledDeliveryAmbiguous) {
		t.Fatalf("turn error = %v, want ambiguous delivery", outcome.TurnErr)
	}
	if outcome.JournalErr != nil || outcome.Control != ToolControlBreak {
		t.Fatalf("outcome = %#v", outcome)
	}
	canonical := matchingToolResult(t, store.GetHistory(sessionKey), toolCall.ID)
	if canonical.ToolResultStatus != providers.ToolResultStatusError ||
		!strings.Contains(canonical.Content, "ambiguous") ||
		strings.Contains(canonical.Content, finalHandledDeliveryPendingContent) {
		t.Fatalf("canonical ambiguous result = %#v", canonical)
	}
}

func TestReceiptlessLegacyFinalHandledTextUsesSynchronousConfirmation(t *testing.T) {
	manager := &recordingChannelManager{}
	al := &AgentLoop{channelManager: manager}
	agent := &AgentInstance{ID: "main"}
	ts := &turnState{
		agent:      agent,
		channel:    "telegram",
		chatID:     "chat-1",
		sessionKey: "receiptless-legacy-text",
		opts: processOptions{Dispatch: DispatchRequest{
			InboundContext: &bus.InboundContext{Channel: "telegram", ChatID: "chat-1"},
		}},
	}
	result := toolshared.UserResult("hello").WithResponseHandled()

	_, outcome, err := al.deliverToolResultToUser(
		withOutboundTransaction(t.Context(), "receiptless-legacy-text"),
		ts,
		result,
		"legacy_send",
	)
	if err != nil {
		t.Fatalf("deliverToolResultToUser() error = %v", err)
	}
	if outcome != toolResultDeliveryDirect || manager.definiteTextSends != 1 ||
		len(manager.sentMessages) != 1 {
		t.Fatalf("outcome = %v, manager = %#v", outcome, manager)
	}
	if result.Outbound == nil || !result.ResponseHandled ||
		!strings.Contains(result.ForLLM, "confirmed delivered") {
		t.Fatalf("settled result = %#v", result)
	}
}

func TestImmediateContinueKeepsNormalSynchronousRetryPolicy(t *testing.T) {
	for _, media := range []bool{false, true} {
		t.Run(fmt.Sprintf("media_%t", media), func(t *testing.T) {
			manager := &recordingChannelManager{}
			al := &AgentLoop{channelManager: manager}
			ts := &turnState{
				agent:      &AgentInstance{ID: "main"},
				channel:    "telegram",
				chatID:     "chat-1",
				sessionKey: "immediate-continue",
			}
			outbound := toolshared.OutboundDelivery{
				Channel: "telegram", ChatID: "chat-1", Text: "progress",
			}
			if media {
				outbound.Text = ""
				outbound.Media = []bus.MediaPart{{Type: "image", Ref: "media://progress"}}
			}
			result := (&toolshared.ToolResult{Silent: true}).
				WithOutboundDelivery(outbound).
				WithDeliveryIntent(toolshared.DeliveryImmediateContinue)

			_, outcome, err := al.deliverToolResultToUser(t.Context(), ts, result, "progress")
			if err != nil || outcome != toolResultDeliveryDirect {
				t.Fatalf("outcome = %v, error = %v", outcome, err)
			}
			if manager.definiteTextSends != 0 || manager.definiteMediaSends != 0 {
				t.Fatalf("immediate delivery used final-only retry policy: %#v", manager)
			}
			if media && len(manager.sentMedia) != 1 {
				t.Fatalf("sent media = %#v", manager.sentMedia)
			}
			if !media && len(manager.sentMessages) != 1 {
				t.Fatalf("sent messages = %#v", manager.sentMessages)
			}
		})
	}
}

func TestPipelineFinalHandledHardAbortKeepsToolBatchComplete(t *testing.T) {
	const sessionKey = "final-handled-hard-abort"
	store := session.NewSessionManager("")
	result := (&toolshared.ToolResult{
		ForLLM: "Message prepared for delivery to telegram:chat-1",
		Silent: true,
	}).WithOutboundDelivery(toolshared.OutboundDelivery{
		Channel: "telegram",
		ChatID:  "chat-1",
		Text:    "hello",
	}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	tool := &fixedToolResultTool{name: "final_message", result: result}
	sibling := &fixedToolResultTool{
		name:   "destructive_sibling",
		result: toolshared.SilentResult("sibling executed"),
	}
	registry := tools.NewToolRegistry()
	registry.Register(tool)
	registry.Register(sibling)
	agent := &AgentInstance{ID: "main", Tools: registry, Sessions: store}
	ts := &turnState{
		agent: agent, agentID: agent.ID, turnID: "turn-hard-abort",
		sessionKey: sessionKey, session: store,
		opts: processOptions{Dispatch: DispatchRequest{SessionKey: sessionKey}},
	}
	toolCall := providers.ToolCall{ID: "call-final-message", Name: tool.Name(), Arguments: map[string]any{}}
	siblingCall := providers.ToolCall{
		ID: "call-destructive-sibling", Name: sibling.Name(), Arguments: map[string]any{},
	}
	userMessage := providers.Message{Role: "user", Content: "send the message, then mutate external state"}
	intent := providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{toolCall, siblingCall}}
	if err := store.AppendTurnMessage(t.Context(), sessionKey, userMessage); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurnMessage(t.Context(), sessionKey, intent); err != nil {
		t.Fatal(err)
	}
	exec := newTurnExecution(agent, ts.opts, nil, "", []providers.Message{userMessage, intent})
	llm := newLLMIterationState(1)
	llm.normalizedToolCalls = []providers.ToolCall{toolCall, siblingCall}
	llm.assistantToolCallsPersisted = true
	settlement := &finalHandledSettlementDelivery{
		store: store, sessionKey: sessionKey, delivered: true,
		hardAbort: func() { _ = ts.requestHardAbort() },
	}
	pipeline := &Pipeline{Interaction: PipelineInteractionServices{SyncToolDelivery: settlement}}

	outcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
	if outcome.AbortCause != TurnAbortHard {
		t.Fatalf("abort cause = %v, want hard abort", outcome.AbortCause)
	}
	if sibling.executions != 0 {
		t.Fatalf("sibling executions = %d, want 0", sibling.executions)
	}
	for source, messages := range map[string][]providers.Message{
		"canonical history": store.GetHistory(sessionKey),
		"execution context": exec.messages,
		"live turn state":   ts.liveTurnMessages,
	} {
		settled := matchingToolResult(t, messages, toolCall.ID)
		if settled.ToolResultStatus != providers.ToolResultStatusSuccess ||
			!strings.Contains(settled.Content, "confirmed delivered") {
			t.Fatalf("%s settlement = %#v", source, settled)
		}
		skipped := matchingToolResult(t, messages, siblingCall.ID)
		if !strings.Contains(skipped.Content, "final-handled outbound boundary") {
			t.Fatalf("%s skipped sibling = %#v", source, skipped)
		}
	}
	sanitized := sanitizeHistoryForProvider(store.GetHistory(sessionKey))
	_ = matchingToolResult(t, sanitized, toolCall.ID)
	_ = matchingToolResult(t, sanitized, siblingCall.ID)
}

func TestPipelineFinalHandledBatchReservationFailureIsAtomic(t *testing.T) {
	const sessionKey = "final-handled-reservation-failure"
	baseStore := session.NewSessionManager("")
	mutationErr := errors.New("reserve terminal delivery batch")
	store := &mutateFailingSessionStore{SessionStore: baseStore, err: mutationErr, failAt: 1}
	result := (&toolshared.ToolResult{
		ForLLM: "Message prepared for delivery to telegram:chat-1",
		Silent: true,
	}).WithOutboundDelivery(toolshared.OutboundDelivery{
		Channel: "telegram", ChatID: "chat-1", Text: "hello",
	}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	tool := &fixedToolResultTool{name: "final_message", result: result}
	prior := &fixedToolResultTool{
		name:   "prior_non_idempotent",
		result: toolshared.SilentResult("prior side effect committed"),
	}
	sibling := &fixedToolResultTool{name: "destructive_sibling", result: toolshared.SilentResult("executed")}
	registry := tools.NewToolRegistry()
	registry.Register(prior)
	registry.Register(tool)
	registry.Register(sibling)
	agent := &AgentInstance{ID: "main", Tools: registry, Sessions: store}
	ts := &turnState{
		agent: agent, agentID: agent.ID, turnID: "turn-reservation-failure",
		sessionKey: sessionKey, session: store,
		opts: processOptions{Dispatch: DispatchRequest{SessionKey: sessionKey}},
	}
	priorCall := providers.ToolCall{ID: "call-prior", Name: prior.Name(), Arguments: map[string]any{}}
	toolCall := providers.ToolCall{ID: "call-final-message", Name: tool.Name(), Arguments: map[string]any{}}
	siblingCall := providers.ToolCall{ID: "call-sibling", Name: sibling.Name(), Arguments: map[string]any{}}
	intent := providers.Message{
		Role: "assistant", ToolCalls: []providers.ToolCall{priorCall, toolCall, siblingCall},
	}
	userMessage := providers.Message{Role: "user", Content: "run the three-call batch"}
	if err := baseStore.AppendTurnMessage(t.Context(), sessionKey, userMessage); err != nil {
		t.Fatal(err)
	}
	if err := baseStore.AppendTurnMessage(t.Context(), sessionKey, intent); err != nil {
		t.Fatal(err)
	}
	exec := newTurnExecution(agent, ts.opts, nil, "", []providers.Message{userMessage, intent})
	llm := newLLMIterationState(1)
	llm.normalizedToolCalls = []providers.ToolCall{priorCall, toolCall, siblingCall}
	llm.assistantToolCallsPersisted = true
	settlement := &finalHandledSettlementDelivery{store: baseStore, sessionKey: sessionKey, delivered: true}
	pipeline := &Pipeline{Interaction: PipelineInteractionServices{SyncToolDelivery: settlement}}

	outcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
	if !errors.Is(outcome.JournalErr, mutationErr) {
		t.Fatalf("journal error = %v, want %v", outcome.JournalErr, mutationErr)
	}
	if settlement.calls != 1 {
		t.Fatalf("delivery calls = %d, want only the prior tool", settlement.calls)
	}
	if prior.executions != 1 {
		t.Fatalf("prior executions = %d, want 1", prior.executions)
	}
	if sibling.executions != 0 {
		t.Fatalf("sibling executions = %d, want 0", sibling.executions)
	}
	canonical := baseStore.GetHistory(sessionKey)
	for source, messages := range map[string][]providers.Message{
		"canonical history": canonical,
		"live turn state":   ts.liveTurnMessages,
	} {
		priorResult := matchingToolResult(t, messages, priorCall.ID)
		if !strings.Contains(priorResult.Content, "prior side effect committed") {
			t.Fatalf("%s prior result = %#v", source, priorResult)
		}
		for _, missingID := range []string{toolCall.ID, siblingCall.ID} {
			for _, message := range messages {
				if message.Role == "tool" && message.ToolCallID == missingID {
					t.Fatalf("%s contains partially reserved result: %#v", source, message)
				}
			}
		}
	}
	for _, message := range exec.messages {
		if message.Role == "tool" {
			t.Fatalf("stopped execution context contains partial result: %#v", message)
		}
	}
	sanitized := sanitizeHistoryForProvider(canonical)
	_ = matchingToolResult(t, sanitized, priorCall.ID)
	for _, missingID := range []string{toolCall.ID, siblingCall.ID} {
		repaired := matchingToolResult(t, sanitized, missingID)
		if repaired.ToolResultStatus != providers.ToolResultStatusUnresolved ||
			!strings.Contains(repaired.Content, "do not assume success") {
			t.Fatalf("repaired result for %s = %#v", missingID, repaired)
		}
	}
}

func TestPipelineFinalHandledDeliveryFinalizationFailureStopsBeforeModel(t *testing.T) {
	const sessionKey = "final-handled-finalization-failure"
	baseStore := session.NewSessionManager("")
	mutationErr := errors.New("replace settled tool result")
	store := &mutateFailingSessionStore{SessionStore: baseStore, err: mutationErr, failAt: 2}
	result := (&toolshared.ToolResult{
		ForLLM: "Message prepared for delivery to telegram:chat-1",
		Silent: true,
	}).WithOutboundDelivery(toolshared.OutboundDelivery{
		Channel: "telegram",
		ChatID:  "chat-1",
		Text:    "hello",
	}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	tool := &fixedToolResultTool{name: "final_message", result: result}
	sibling := &fixedToolResultTool{
		name:   "destructive_sibling",
		result: toolshared.SilentResult("sibling executed"),
	}
	registry := tools.NewToolRegistry()
	registry.Register(tool)
	registry.Register(sibling)
	agent := &AgentInstance{ID: "main", Tools: registry, Sessions: store}
	ts := &turnState{
		agent: agent, agentID: agent.ID, turnID: "turn-finalization-failure",
		sessionKey: sessionKey, session: store,
		opts: processOptions{Dispatch: DispatchRequest{SessionKey: sessionKey}},
	}
	toolCall := providers.ToolCall{ID: "call-final-message", Name: tool.Name(), Arguments: map[string]any{}}
	siblingCall := providers.ToolCall{
		ID: "call-destructive-sibling", Name: sibling.Name(), Arguments: map[string]any{},
	}
	userMessage := providers.Message{Role: "user", Content: "send the message, then mutate external state"}
	intent := providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{toolCall, siblingCall}}
	if err := baseStore.AppendTurnMessage(t.Context(), sessionKey, userMessage); err != nil {
		t.Fatal(err)
	}
	if err := baseStore.AppendTurnMessage(t.Context(), sessionKey, intent); err != nil {
		t.Fatal(err)
	}
	exec := newTurnExecution(agent, ts.opts, nil, "", []providers.Message{userMessage, intent})
	llm := newLLMIterationState(1)
	llm.normalizedToolCalls = []providers.ToolCall{toolCall, siblingCall}
	llm.assistantToolCallsPersisted = true
	settlement := &finalHandledSettlementDelivery{
		store: baseStore, sessionKey: sessionKey, delivered: true,
	}
	ingest := &recordingCanonicalIngest{}
	pipeline := &Pipeline{
		Context: PipelineContextServices{Runtime: ingest},
		Interaction: PipelineInteractionServices{
			SyncToolDelivery: settlement,
		},
	}

	outcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
	if !errors.Is(outcome.JournalErr, mutationErr) {
		t.Fatalf("journal error = %v, want %v", outcome.JournalErr, mutationErr)
	}
	if settlement.calls != 1 || !settlement.pendingSeen {
		t.Fatalf("settlement calls = %d, pending seen = %t", settlement.calls, settlement.pendingSeen)
	}
	if sibling.executions != 0 {
		t.Fatalf("sibling executions = %d, want 0", sibling.executions)
	}
	for source, messages := range map[string][]providers.Message{
		"canonical history": baseStore.GetHistory(sessionKey),
		"execution context": exec.messages,
		"live turn state":   ts.liveTurnMessages,
	} {
		barrier := matchingToolResult(t, messages, toolCall.ID)
		if barrier.Content != finalHandledDeliveryPendingContent ||
			barrier.ToolResultStatus != providers.ToolResultStatusUnresolved {
			t.Fatalf("%s failed-finalization barrier = %#v", source, barrier)
		}
		skipped := matchingToolResult(t, messages, siblingCall.ID)
		if !strings.Contains(skipped.Content, "final-handled outbound boundary") {
			t.Fatalf("%s skipped sibling = %#v", source, skipped)
		}
	}
	sanitized := sanitizeHistoryForProvider(baseStore.GetHistory(sessionKey))
	barrier := matchingToolResult(t, sanitized, toolCall.ID)
	if barrier.ToolResultStatus != providers.ToolResultStatusUnresolved ||
		barrier.Content != finalHandledDeliveryPendingContent {
		t.Fatalf("sanitized barrier = %#v", barrier)
	}
	if len(ingest.messages) != 0 {
		t.Fatalf("unfinalized pending result was ingested: %#v", ingest.messages)
	}
}

func matchingToolResult(
	t *testing.T,
	messages []providers.Message,
	toolCallID string,
) providers.Message {
	t.Helper()
	var matches []providers.Message
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == toolCallID {
			matches = append(matches, message)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("tool results for %q = %#v, want exactly one", toolCallID, matches)
	}
	return matches[0]
}

func TestRunAgentLoopFinalHandledPreflightFailureReachesNextProviderAndHistory(t *testing.T) {
	const (
		sessionKey = "final-handled-preflight-failure"
		toolCallID = "call-oversized-video"
	)
	provider := &finalHandledRequestProvider{toolCalls: []providers.ToolCall{{
		ID: toolCallID, Name: "send_oversized_video", Arguments: map[string]any{},
	}}}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "test-model"
	cfg.Agents.Defaults.MaxTokens = 4096
	cfg.Agents.Defaults.MaxToolIterations = 3
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.ToolFeedback.Enabled = false
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(func() { al.Close() })
	al.channelManager = &finalHandledReceiptManager{preflightErr: &channels.MediaConstraintError{
		Channel: "telegram",
		Ref:     "media://oversized-video",
		Size:    132186801,
		MaxSize: 50000000,
	}}
	agent := al.registry.GetDefaultAgent()
	result := (&toolshared.ToolResult{
		ForLLM: "Message with 1 media attachment(s) prepared for delivery to telegram:chat-1",
		Silent: true,
	}).WithOutboundDelivery(toolshared.OutboundDelivery{
		Channel: "telegram",
		ChatID:  "chat-1",
		Media: []bus.MediaPart{{
			Type: "video",
			Ref:  "media://oversized-video",
		}},
	}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	agent.Tools.Register(&fixedToolResultTool{name: "send_oversized_video", result: result})

	response, err := al.runAgentLoop(
		withOutboundTransaction(t.Context(), "trace-turn-096307c49f75e730ec5540b5"),
		agent,
		processOptions{
			Dispatch: DispatchRequest{
				SessionKey:  sessionKey,
				UserMessage: "send the video",
				InboundContext: &bus.InboundContext{
					Channel: "telegram",
					ChatID:  "chat-1",
				},
			},
			DefaultResponse: defaultResponse,
			SendResponse:    true,
		},
	)
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if response != "settlement observed" || len(provider.requests) != 2 {
		t.Fatalf("response = %q, provider requests = %d", response, len(provider.requests))
	}

	nextResult := matchingToolResult(t, provider.requests[1], toolCallID)
	historyResult := matchingToolResult(t, agent.Sessions.GetHistory(sessionKey), toolCallID)
	for source, message := range map[string]providers.Message{
		"next provider request": nextResult,
		"canonical history":     historyResult,
	} {
		if message.ToolResultStatus != providers.ToolResultStatusError ||
			!strings.Contains(message.Content, "132186801 bytes") ||
			!strings.Contains(message.Content, "reduce or transcode") ||
			strings.Contains(strings.ToLower(message.Content), "prepared") ||
			strings.Contains(message.Content, toolshared.HandledToolLLMNote) {
			t.Fatalf("%s tool result = %#v", source, message)
		}
	}
}

func TestRunAgentLoopLegacyFinalHandledConfirmedSettlementReachesNextProviderAndHistory(t *testing.T) {
	const (
		sessionKey = "final-handled-confirmed-settlement"
		toolCallID = "call-confirmed-message"
	)
	provider := &finalHandledRequestProvider{toolCalls: []providers.ToolCall{
		{ID: toolCallID, Name: "send_confirmed_message", Arguments: map[string]any{}},
		{ID: "call-followup-context", Name: "followup_context", Arguments: map[string]any{}},
	}}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "test-model"
	cfg.Agents.Defaults.MaxTokens = 4096
	cfg.Agents.Defaults.MaxToolIterations = 3
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.ToolFeedback.Enabled = false
	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, provider)
	t.Cleanup(func() { al.Close() })
	al.channelManager = &finalHandledReceiptManager{}
	coordinator, err := outbox.OpenCoordinator(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCoordinator() error = %v", err)
	}
	al.SetOutboundOutbox(coordinator)
	agent := al.registry.GetDefaultAgent()
	result := toolshared.UserResult("hello").WithResponseHandled()
	result.ForLLM = "Message prepared for delivery to telegram:chat-1"
	agent.Tools.Register(&fixedToolResultTool{name: "send_confirmed_message", result: result})
	agent.Tools.Register(&fixedToolResultTool{
		name: "followup_context",
		result: &toolshared.ToolResult{
			ForLLM: "continue to the model",
			Silent: true,
		},
	})

	type runResult struct {
		response string
		err      error
	}
	done := make(chan runResult, 1)
	go func() {
		response, runErr := al.runAgentLoop(
			withOutboundTransaction(t.Context(), "confirmed-settlement"),
			agent,
			processOptions{
				Dispatch: DispatchRequest{
					SessionKey:  sessionKey,
					UserMessage: "send the message and continue",
					InboundContext: &bus.InboundContext{
						Channel: "telegram",
						ChatID:  "chat-1",
					},
				},
				DefaultResponse: defaultResponse,
				SendResponse:    true,
			},
		)
		done <- runResult{response: response, err: runErr}
	}()

	var outbound bus.OutboundMessage
	select {
	case outbound = <-msgBus.OutboundChan():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for durable final-handled outbound")
	}
	if outbound.DeliveryID == "" {
		t.Fatalf("outbound delivery ID is empty: %#v", outbound)
	}
	if err := coordinator.BeginAttempt(outbound.DeliveryID); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}
	if err := coordinator.MarkDelivered(
		outbound.DeliveryID,
		outbox.Outcome{PlatformMessageIDs: []string{"telegram-1"}},
	); err != nil {
		t.Fatalf("MarkDelivered() error = %v", err)
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("runAgentLoop() error = %v", got.err)
		}
		if got.response != "settlement observed" {
			t.Fatalf("runAgentLoop() response = %q", got.response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for confirmed-settlement turn")
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(provider.requests))
	}
	nextResult := matchingToolResult(t, provider.requests[1], toolCallID)
	historyResult := matchingToolResult(t, agent.Sessions.GetHistory(sessionKey), toolCallID)
	for source, message := range map[string]providers.Message{
		"next provider request": nextResult,
		"canonical history":     historyResult,
	} {
		if message.ToolResultStatus != providers.ToolResultStatusSuccess ||
			!strings.Contains(message.Content, "confirmed delivered") ||
			strings.Contains(strings.ToLower(message.Content), "prepared") ||
			strings.Contains(message.Content, finalHandledDeliveryPendingContent) {
			t.Fatalf("%s tool result = %#v", source, message)
		}
	}
}
