package agent

import (
	"context"
	"errors"
	"testing"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type terminalRenderCallbackProvider struct {
	onChat   func()
	response string
}

type resourceDispositionCloseTestTool struct {
	liveSessions int
	executions   int
}

func (*resourceDispositionCloseTestTool) Name() string { return "browser_session" }
func (*resourceDispositionCloseTestTool) Description() string {
	return "browser session disposition test tool"
}

func (*resourceDispositionCloseTestTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation":          map[string]any{"type": "string"},
			"browser_session_id": map[string]any{"type": "string"},
		},
		"required": []string{"operation", "browser_session_id"}, "additionalProperties": false,
	}
}

func (tool *resourceDispositionCloseTestTool) Execute(
	_ context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	tool.executions++
	if args["operation"] != "close" {
		return toolshared.ErrorResult("unexpected operation")
	}
	if tool.liveSessions > 0 {
		tool.liveSessions--
	}
	return toolshared.NewToolResult(`{"close_state":"closed","state":"closed"}`).WithDeliverable(
		&taskresult.Deliverable{LifecycleReceipts: []taskresult.Receipt{{
			ID: "browser_close_receipt", Kind: taskresult.ReceiptKindResourceCleanup,
			Target: "browser:gateway/managed", Action: "close", Tool: "browser_session",
			Summary:  "Browser session close reached terminal state.",
			Metadata: map[string]string{"state": "closed", "target": "gateway", "profile": "managed"},
		}}},
	)
}

func (tool *resourceDispositionCloseTestTool) TurnFinalizationRequirement(
	context.Context,
) (tools.TurnFinalizationRequirement, bool, error) {
	return tools.TurnFinalizationRequirement{
		RecoveryKind: taskresult.ObjectiveKindResourceDisposition,
		Instruction:  "Close the live browser session before finalizing.",
	}, tool.liveSessions > 0, nil
}

func (*resourceDispositionCloseTestTool) ObjectiveRecoveryParameters(kind string) (map[string]any, bool) {
	if kind != taskresult.ObjectiveKindResourceDisposition {
		return nil, false
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation":          map[string]any{"type": "string", "enum": []string{"close"}},
			"browser_session_id": map[string]any{"type": "string"},
		},
		"required": []string{"operation", "browser_session_id"}, "additionalProperties": false,
	}, true
}

func (provider *terminalRenderCallbackProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	if provider.onChat != nil {
		provider.onChat()
	}
	return &providers.LLMResponse{Content: provider.response, FinishReason: "stop"}, nil
}

func (*terminalRenderCallbackProvider) GetDefaultModel() string { return "terminal-render-model" }

func TestRequiredTerminalRenderDrainsSubturnResultsAfterRendering(t *testing.T) {
	provider := &terminalRenderCallbackProvider{response: "rendered response"}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	al.GetConfig().Agents.Defaults.FinalTurnRenderMode = "llm"
	pipeline := newTestPipeline(al)
	ts := newTurnState(agent, makeTestTurnSpec("terminal-render-subturn"), turnEventScope{})
	ts.pendingResults = make(chan *toolshared.ToolResult, 1)
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	exec.sawSteering = true
	accepted := false
	provider.onChat = func() {
		accepted = ts.enqueuePendingResult(
			(&toolshared.ToolResult{ForLLM: "late child result"}).WithDeliverable(&taskresult.Deliverable{
				Text: "late child failure",
				ObjectiveOutcome: &taskresult.Outcome{
					Status: taskresult.OutcomeBlocked, MissingItems: []string{"late child objective"},
				},
			}),
		)
	}

	outcome := pipeline.completeTerminal(
		t.Context(),
		ts,
		exec,
		newLLMIterationState(1),
		TurnEndStatusCompleted,
		terminalRequest{content: terminalContent{content: "fallback"}, renderMode: terminalRenderRequired},
	)
	if !accepted || !outcome.resume || outcome.err != nil {
		t.Fatalf("terminal outcome = %#v, child accepted = %v", outcome, accepted)
	}
	pending := exec.pendingInputs.Snapshot()
	if !messageContentPresent(pending, "late child result") {
		t.Fatalf("pending messages omitted late child result: %#v", pending)
	}
	if exec.deliverable == nil || exec.deliverable.ObjectiveOutcome == nil ||
		exec.deliverable.ObjectiveOutcome.Status != taskresult.OutcomeBlocked {
		t.Fatalf("late child deliverable was not admitted: %#v", exec.deliverable)
	}
}

func TestTerminalRenderSteeringCancelsConfiguredStream(t *testing.T) {
	provider := &terminalRenderCallbackProvider{response: "abandoned rendered response"}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	al.GetConfig().Agents.Defaults.FinalTurnRenderMode = "llm"
	pipeline := newTestPipeline(al)
	ts := newTurnState(agent, makeTestTurnSpec("terminal-render-stream"), turnEventScope{})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	exec.sawSteering = true
	streamer := &recordingStreamer{}
	llm := newLLMIterationState(1)
	llm.streamingPublisher = &streamingChunkPublisher{streamer: streamer}
	provider.onChat = func() {
		if pushErr := al.steering.pushScopeWithSender(
			ts.runtimeSessionScope(),
			providers.Message{Role: "user", Content: "new direction"},
			"",
		); pushErr != nil {
			t.Errorf("push steering: %v", pushErr)
		}
	}

	outcome := pipeline.completeTerminal(
		t.Context(),
		ts,
		exec,
		llm,
		TurnEndStatusCompleted,
		terminalRequest{content: terminalContent{content: "fallback"}, renderMode: terminalRenderRequired},
	)
	if !outcome.resume || outcome.err != nil {
		t.Fatalf("terminal outcome = %#v", outcome)
	}
	if streamer.canceled != 1 || llm.streamingPublisher != nil {
		t.Fatalf("stream cancellation = %d, publisher = %#v", streamer.canceled, llm.streamingPublisher)
	}
	pending := exec.pendingInputs.Snapshot()
	if !messageContentPresent(pending, "new direction") {
		t.Fatalf("pending messages omitted steering: %#v", pending)
	}
}

func TestExactTerminalPreservesRegularTurnBehavior(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	pipeline := newTestPipeline(al)
	ts := newTurnState(agent, makeTestTurnSpec("regular-exact-terminal"), turnEventScope{})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	if err := al.steering.pushScopeWithSender(
		ts.runtimeSessionScope(),
		providers.Message{Role: "user", Content: "next regular turn"},
		"",
	); err != nil {
		t.Fatalf("push steering: %v", err)
	}

	outcome := pipeline.completeTerminal(
		t.Context(),
		ts,
		exec,
		newLLMIterationState(1),
		TurnEndStatusCompleted,
		terminalRequest{content: terminalContent{content: "safety stop"}, renderMode: terminalRenderExact},
	)
	if outcome.resume || outcome.err != nil || outcome.result.finalContent != "safety stop" {
		t.Fatalf("regular exact terminal outcome = %#v", outcome)
	}
	if depth := al.steering.lenScope(ts.runtimeSessionScope()); depth != 1 {
		t.Fatalf("regular steering queue depth = %d, want 1", depth)
	}
}

func TestExactTerminalContinuesForAcceptedCodingSteering(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	pipeline := newTestPipeline(al)
	spec := makeTestTurnSpec("coding-exact-terminal")
	spec.mode = turnModeCoding
	ts := newTurnState(agent, spec, turnEventScope{})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	if err := al.steering.pushScopeWithSender(
		ts.runtimeSessionScope(),
		providers.Message{Role: "user", Content: "recover with this evidence"},
		"",
	); err != nil {
		t.Fatalf("push steering: %v", err)
	}

	outcome := pipeline.completeTerminal(
		t.Context(),
		ts,
		exec,
		newLLMIterationState(1),
		TurnEndStatusCompleted,
		terminalRequest{content: terminalContent{content: "safety stop"}, renderMode: terminalRenderExact},
	)
	if !outcome.resume || outcome.err != nil {
		t.Fatalf("coding exact terminal outcome = %#v", outcome)
	}
	if pending := exec.pendingInputs.Snapshot(); !messageContentPresent(pending, "recover with this evidence") {
		t.Fatalf("coding exact terminal pending input = %#v", pending)
	}
}

func TestExactTerminalDoesNotTreatCodingSubTurnResultAsSteering(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	pipeline := newTestPipeline(al)
	spec := makeTestTurnSpec("coding-exact-terminal-subturn")
	spec.mode = turnModeCoding
	ts := newTurnState(agent, spec, turnEventScope{})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	exec.pendingInputs.AppendSubTurn(subTurnResultPromptMessage("child-only result"))

	outcome := pipeline.completeTerminal(
		t.Context(),
		ts,
		exec,
		newLLMIterationState(1),
		TurnEndStatusCompleted,
		terminalRequest{content: terminalContent{content: "safety stop"}, renderMode: terminalRenderExact},
	)
	if outcome.resume || outcome.err != nil || outcome.result.finalContent != "safety stop" {
		t.Fatalf("coding sub-turn-only exact terminal outcome = %#v", outcome)
	}
	if !messageContentPresent(exec.pendingInputs.Snapshot(), "child-only result") {
		t.Fatalf("exact terminal consumed pending sub-turn result: %#v", exec.pendingInputs.Snapshot())
	}
}

func TestTerminalTurnPathsProduceExactlyOneOutcomeAndFinalization(t *testing.T) {
	repairedOutcome := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[],` +
		`"output":{"kind":"text","text":"complete standalone result"}}],` +
		`"missing_items":[],"result":"complete standalone result"}` + objectiveOutcomeEnd
	tests := []struct {
		name                 string
		provider             func() providers.LLMProvider
		configure            func(*AgentLoop, *AgentInstance, *turnSpec)
		wantFinalContent     string
		wantPersistedContent string
		wantIterations       int
	}{
		{
			name: "direct model response",
			provider: func() providers.LLMProvider {
				return &sequenceProvider{responses: []*providers.LLMResponse{{
					Content: "direct terminal response", FinishReason: "stop",
				}}}
			},
			wantFinalContent:     "direct terminal response",
			wantPersistedContent: "direct terminal response",
			wantIterations:       1,
		},
		{
			name: "post-tool final render",
			provider: func() providers.LLMProvider {
				return &sequenceProvider{responses: []*providers.LLMResponse{
					{
						Content: "tool intent",
						ToolCalls: []providers.ToolCall{{
							ID: "call-final-render", Name: "contract_tool", Arguments: map[string]any{},
						}},
						FinishReason: "tool_calls",
					},
					{Content: "rendered terminal response", FinishReason: "stop"},
				}}
			},
			configure: func(al *AgentLoop, agent *AgentInstance, opts *turnSpec) {
				al.GetConfig().Agents.Defaults.FinalTurnRenderMode = "llm"
				agent.Tools.Register(&fixedToolResultTool{
					name: "contract_tool", result: toolshared.SilentResult("tool completed"),
				})
				opts.InitialSteeringMessages = []providers.Message{{Role: "user", Content: "clarification"}}
			},
			wantFinalContent:     "rendered terminal response",
			wantPersistedContent: "rendered terminal response",
			wantIterations:       1,
		},
		{
			name: "verified child failure overrides parent success claim",
			provider: func() providers.LLMProvider {
				return &sequenceProvider{responses: []*providers.LLMResponse{
					{
						Content: "delegate browser task",
						ToolCalls: []providers.ToolCall{{
							ID: "call-blocked-child", Name: "contract_tool", Arguments: map[string]any{},
						}},
						FinishReason: "tool_calls",
					},
					{Content: "Amazon remains open for you.", FinishReason: "stop"},
				}}
			},
			configure: func(al *AgentLoop, agent *AgentInstance, opts *turnSpec) {
				al.GetConfig().Agents.Defaults.FinalTurnRenderMode = "llm"
				agent.Tools.Register(&fixedToolResultTool{
					name: "contract_tool",
					result: (&toolshared.ToolResult{ForLLM: "browser handoff failed"}).WithDeliverable(
						&taskresult.Deliverable{
							Text: "Browser handoff failed; the session was not left open.",
							ObjectiveOutcome: &taskresult.Outcome{
								Status:       taskresult.OutcomeBlocked,
								MissingItems: []string{"renew browser handoff"},
								Explanation:  "the live resource could not be handed back",
							},
						},
					),
				})
				opts.InitialSteeringMessages = []providers.Message{{Role: "user", Content: "clarification"}}
			},
			wantFinalContent:     "Browser handoff failed; the session was not left open.",
			wantPersistedContent: "Browser handoff failed; the session was not left open.",
			wantIterations:       1,
		},
		{
			name: "post-tool render failure resumes model loop",
			provider: func() providers.LLMProvider {
				return &sequenceProvider{
					responses: []*providers.LLMResponse{
						{
							Content: "tool intent",
							ToolCalls: []providers.ToolCall{{
								ID: "call-render-retry", Name: "contract_tool", Arguments: map[string]any{},
							}},
							FinishReason: "tool_calls",
						},
						nil,
						{Content: "model-loop terminal response", FinishReason: "stop"},
					},
					errors: []error{nil, errors.New("render unavailable"), nil, errors.New("render unavailable")},
				}
			},
			configure: func(al *AgentLoop, agent *AgentInstance, opts *turnSpec) {
				al.GetConfig().Agents.Defaults.FinalTurnRenderMode = "llm"
				agent.Tools.Register(&fixedToolResultTool{
					name: "contract_tool", result: toolshared.SilentResult("tool completed"),
				})
				opts.InitialSteeringMessages = []providers.Message{{Role: "user", Content: "clarification"}}
			},
			wantFinalContent:     "model-loop terminal response",
			wantPersistedContent: "model-loop terminal response",
			wantIterations:       2,
		},
		{
			name: "tool-loop safety halt",
			provider: func() providers.LLMProvider {
				return &toolLimitOnlyProvider{}
			},
			configure: func(_ *AgentLoop, agent *AgentInstance, _ *turnSpec) {
				agent.MaxIterations = 10
				agent.ToolLoopDetection = loopguard.DefaultConfig()
				agent.ToolLoopDetection.IdenticalCallHalt = 3
				agent.Tools.Register(&toolLimitTestTool{})
			},
			wantFinalContent: "Stopped the turn after 3 consecutive identical successful calls to " +
				"tool_limit_test_tool because the operation was not making progress.",
			wantPersistedContent: "Stopped the turn after 3 consecutive identical successful calls to " +
				"tool_limit_test_tool because the operation was not making progress.",
			wantIterations: 3,
		},
		{
			name: "already-handled tool response",
			provider: func() providers.LLMProvider {
				return &toolCallRespProvider{toolName: "contract_tool", response: "must not be called"}
			},
			configure: func(_ *AgentLoop, agent *AgentInstance, _ *turnSpec) {
				agent.Tools.Register(&fixedToolResultTool{
					name: "contract_tool",
					result: toolshared.SilentResult("already delivered").
						WithDeliveryIntent(toolshared.DeliveryFinalHandled),
				})
			},
			wantFinalContent:     "",
			wantPersistedContent: handledToolResponseSummary,
			wantIterations:       1,
		},
		{
			name: "iteration exhaustion",
			provider: func() providers.LLMProvider {
				return &toolLimitOnlyProvider{}
			},
			configure: func(_ *AgentLoop, agent *AgentInstance, _ *turnSpec) {
				agent.MaxIterations = 2
				agent.ToolLoopDetection = loopguard.DefaultConfig()
				agent.ToolLoopDetection.IdenticalCallHalt = 10
				agent.Tools.Register(&toolLimitTestTool{})
			},
			wantFinalContent:     toolLimitResponse,
			wantPersistedContent: toolLimitResponse,
			wantIterations:       2,
		},
		{
			name: "iteration exhaustion with bounded objective repair",
			provider: func() providers.LLMProvider {
				return &sequenceProvider{responses: []*providers.LLMResponse{
					{
						ToolCalls: []providers.ToolCall{{
							ID: "call-before-repair", Name: "contract_tool", Arguments: map[string]any{},
						}},
						FinishReason: "tool_calls",
					},
					{Content: repairedOutcome, FinishReason: "stop"},
				}}
			},
			configure: func(_ *AgentLoop, agent *AgentInstance, opts *turnSpec) {
				agent.MaxIterations = 1
				agent.Tools.Register(&fixedToolResultTool{
					name: "contract_tool", result: toolshared.SilentResult("tool completed"),
				})
				opts.ObjectiveChecklist = normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
					Item: "return a complete standalone result", Kind: "result",
				}})
			},
			wantFinalContent:     repairedOutcome,
			wantPersistedContent: repairedOutcome,
			wantIterations:       2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			al, agent, cleanup := newTurnCoordTestLoop(t, test.provider())
			defer cleanup()
			opts := makeTestTurnSpec("terminal-contract-" + test.name)
			if test.configure != nil {
				test.configure(al, agent, &opts)
			}
			emitter := &captureRuntimeEmitter{}
			pipeline := newTestPipeline(al)
			pipeline.events = emitter
			ts := newTurnState(agent, opts, turnEventScope{
				turnID: "turn-terminal-contract", context: newTurnContext(nil, nil, nil),
			})

			result, err := runTestTurn(al, t.Context(), ts, pipeline)
			if err != nil {
				t.Fatalf("runTestTurn() error = %v", err)
			}
			if result.status != TurnEndStatusCompleted || result.finalContent != test.wantFinalContent {
				t.Fatalf("turn result = %#v, want completed with content %q", result, test.wantFinalContent)
			}
			if phase := ts.snapshot().Phase; phase != TurnPhaseCompleted {
				t.Fatalf("terminal phase = %q, want %q", phase, TurnPhaseCompleted)
			}

			assertOneTerminalOutcome(t, emitter.events, result, test.wantIterations)
			assertOneTerminalHistoryMessage(
				t,
				agent.Sessions.GetHistory(ts.sessionKey),
				test.wantPersistedContent,
			)
		})
	}
}

func TestResourceDispositionCloseReservesModelOnlyFinalSynthesisAtIterationLimit(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{Content: "The browser remains open.", FinishReason: "stop"},
		{
			ToolCalls: []providers.ToolCall{
				{
					ID: "close-live-browser-one", Name: "browser_session",
					Arguments: map[string]any{
						"operation": "close", "browser_session_id": "browser_session_one",
					},
				},
				{
					ID: "close-live-browser-two", Name: "browser_session",
					Arguments: map[string]any{
						"operation": "close", "browser_session_id": "browser_session_two",
					},
				},
			},
			FinishReason: "tool_calls",
		},
		{Content: "The browser session was closed after the task completed.", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	agent.MaxIterations = 1
	tool := &resourceDispositionCloseTestTool{liveSessions: 2}
	agent.Tools.Register(tool)
	opts := makeTestTurnSpec("resource-disposition-final-synthesis")
	ts := newTurnState(agent, opts, turnEventScope{
		turnID: "resource-disposition-turn", context: newTurnContext(nil, nil, nil),
	})
	result, err := runTestTurn(al, t.Context(), ts, newTestPipeline(al))
	if err != nil || result.status != TurnEndStatusCompleted ||
		result.finalContent != "The browser session was closed after the task completed." ||
		provider.callCount != 3 || tool.liveSessions != 0 || tool.executions != 2 {
		t.Fatalf(
			"turn result = %#v, %v; calls=%d live=%d executions=%d",
			result, err, provider.callCount, tool.liveSessions, tool.executions,
		)
	}
	if len(provider.toolRequests) != 3 || len(provider.toolRequests[2]) != 0 {
		t.Fatalf("final synthesis tools = %#v", provider.toolRequests)
	}
	if result.finalContent == toolLimitResponse {
		t.Fatal("verified close fell through to the iteration-limit response")
	}
}

func TestResourceDispositionBatchKeepsSiblingRestrictionsAndRequiresAllSessionsTerminal(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{Content: "The browsers remain open.", FinishReason: "stop"},
		{
			ToolCalls: []providers.ToolCall{
				{
					ID: "close-first-browser", Name: "browser_session",
					Arguments: map[string]any{
						"operation": "close", "browser_session_id": "browser_session_one",
					},
				},
				{
					ID: "unrestricted-sibling", Name: "browser_session",
					Arguments: map[string]any{
						"operation": "status", "browser_session_id": "browser_session_two",
					},
				},
			},
			FinishReason: "tool_calls",
		},
		{Content: "must not synthesize", FinishReason: "stop"},
	}}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	agent.MaxIterations = 1
	tool := &resourceDispositionCloseTestTool{liveSessions: 2}
	agent.Tools.Register(tool)
	ts := newTurnState(agent, makeTestTurnSpec("resource-disposition-multi-session"), turnEventScope{
		turnID: "resource-disposition-multi-turn", context: newTurnContext(nil, nil, nil),
	})
	result, err := runTestTurn(al, t.Context(), ts, newTestPipeline(al))
	if err != nil || result.status != TurnEndStatusCompleted || provider.callCount != 2 ||
		tool.executions != 1 || tool.liveSessions != 1 ||
		result.finalContent != "Browser session disposition could not be verified for every live session. "+
			"Runtime cleanup will close any remaining session." {
		t.Fatalf(
			"turn result = %#v, %v; calls=%d executions=%d live=%d",
			result, err, provider.callCount, tool.executions, tool.liveSessions,
		)
	}
}

func assertOneTerminalOutcome(
	t *testing.T,
	events []capturedRuntimeEvent,
	result turnResult,
	wantIterations int,
) {
	t.Helper()
	var outcomes []TurnEndPayload
	for _, event := range events {
		if event.kind != runtimeevents.KindAgentTurnEnd {
			continue
		}
		payload, ok := event.payload.(TurnEndPayload)
		if !ok {
			t.Fatalf("turn-end payload type = %T", event.payload)
		}
		outcomes = append(outcomes, payload)
	}
	if len(outcomes) != 1 {
		t.Fatalf("terminal outcome count = %d, want 1", len(outcomes))
	}
	if outcomes[0].Status != result.status || outcomes[0].FinalContent != result.finalContent {
		t.Fatalf(
			"terminal outcome = %#v, want status %q and content %q",
			outcomes[0],
			result.status,
			result.finalContent,
		)
	}
	if outcomes[0].Iterations != wantIterations {
		t.Fatalf("terminal outcome iterations = %d, want %d", outcomes[0].Iterations, wantIterations)
	}
}

func assertOneTerminalHistoryMessage(t *testing.T, history []providers.Message, wantContent string) {
	t.Helper()
	terminalMessages := 0
	matchingMessages := 0
	for _, message := range history {
		if message.Role == "assistant" && len(message.ToolCalls) == 0 {
			terminalMessages++
			if message.Content == wantContent {
				matchingMessages++
			}
		}
	}
	if terminalMessages != 1 || matchingMessages != 1 {
		t.Fatalf(
			"terminal assistant history messages = %d with %d matching expected content, want exactly 1",
			terminalMessages,
			matchingMessages,
		)
	}
	last := history[len(history)-1]
	if last.Role != "assistant" || len(last.ToolCalls) != 0 || last.Content != wantContent {
		t.Fatalf("final history message = %#v, want terminal assistant content %q", last, wantContent)
	}
}
