package agent

import (
	"errors"
	"testing"

	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

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
