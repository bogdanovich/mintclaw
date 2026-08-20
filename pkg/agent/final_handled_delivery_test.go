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
	calls       int
	pendingSeen bool
}

func (d *finalHandledSettlementDelivery) applySyncToolResultDelivery(
	_ context.Context,
	_ *turnState,
	result *toolshared.ToolResult,
	_ string,
) ([]providers.Attachment, *toolshared.ToolResult) {
	d.calls++
	history := d.store.GetHistory(d.sessionKey)
	if len(history) > 0 {
		pending := history[len(history)-1]
		d.pendingSeen = pending.Role == "tool" &&
			pending.Content == finalHandledDeliveryPendingContent &&
			pending.ToolResultStatus == providers.ToolResultStatusUnresolved
	}
	if d.delivered {
		result.ForLLM = "Message confirmed delivered to the user."
		return nil, result
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
	err error
}

func (s *mutateFailingSessionStore) MutateTurnHistory(
	context.Context,
	string,
	func([]providers.Message) ([]providers.Message, bool, error),
) (bool, error) {
	return false, s.err
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
		for _, delivered := range []bool{false, true} {
			name := fmt.Sprintf("hook_%t/delivered_%t", hook, delivered)
			t.Run(name, func(t *testing.T) {
				const sessionKey = "final-handled-settlement"
				store := session.NewSessionManager("")
				result := (&toolshared.ToolResult{
					ForLLM: "Message prepared for delivery to telegram:chat-1",
					Silent: true,
				}).WithOutboundDelivery(toolshared.OutboundDelivery{
					Channel: "telegram",
					ChatID:  "chat-1",
					Text:    "hello",
				}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
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

func TestPipelineFinalHandledDeliveryFinalizationFailureStopsBeforeModel(t *testing.T) {
	const sessionKey = "final-handled-finalization-failure"
	baseStore := session.NewSessionManager("")
	mutationErr := errors.New("replace settled tool result")
	store := &mutateFailingSessionStore{SessionStore: baseStore, err: mutationErr}
	result := (&toolshared.ToolResult{
		ForLLM: "Message prepared for delivery to telegram:chat-1",
		Silent: true,
	}).WithOutboundDelivery(toolshared.OutboundDelivery{
		Channel: "telegram",
		ChatID:  "chat-1",
		Text:    "hello",
	}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	tool := &fixedToolResultTool{name: "final_message", result: result}
	registry := tools.NewToolRegistry()
	registry.Register(tool)
	agent := &AgentInstance{ID: "main", Tools: registry, Sessions: store}
	ts := &turnState{
		agent: agent, agentID: agent.ID, turnID: "turn-finalization-failure",
		sessionKey: sessionKey, session: store,
		opts: processOptions{Dispatch: DispatchRequest{SessionKey: sessionKey}},
	}
	toolCall := providers.ToolCall{ID: "call-final-message", Name: tool.Name(), Arguments: map[string]any{}}
	intent := providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{toolCall}}
	if err := baseStore.AppendTurnMessage(t.Context(), sessionKey, intent); err != nil {
		t.Fatal(err)
	}
	exec := newTurnExecution(agent, ts.opts, nil, "", []providers.Message{intent})
	llm := newLLMIterationState(1)
	llm.normalizedToolCalls = []providers.ToolCall{toolCall}
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
	historyResult := matchingToolResult(t, baseStore.GetHistory(sessionKey), toolCall.ID)
	if historyResult.Content != finalHandledDeliveryPendingContent ||
		historyResult.ToolResultStatus != providers.ToolResultStatusUnresolved {
		t.Fatalf("failed-finalization barrier = %#v", historyResult)
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

func TestRunAgentLoopFinalHandledConfirmedSettlementReachesNextProviderAndHistory(t *testing.T) {
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
	result := (&toolshared.ToolResult{
		ForLLM: "Message prepared for delivery to telegram:chat-1",
		Silent: true,
	}).WithOutboundDelivery(toolshared.OutboundDelivery{
		Channel: "telegram",
		ChatID:  "chat-1",
		Text:    "hello",
	}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
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
