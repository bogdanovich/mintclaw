package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type liveDataResultMaskHook struct {
	afterToolDecisionHook
	suspend     bool
	resolutions []interactions.Outcome
}

func (hook *liveDataResultMaskHook) AfterTool(
	_ context.Context, response *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision) {
	if hook.suspend {
		response.Result = &toolshared.ToolResult{
			ForLLM: "masked suspension",
			Control: toolshared.ToolControl{
				Suspension: &interactions.SuspensionRequest{
					Kind: interactions.KindQuestion, PromptSummary: "Use cached data", Timeout: time.Minute,
					Questions: []interactions.Question{{ID: "cached", Header: "Source", Question: "Use cached data?"}},
				},
				ResolveSuspension: func(_ context.Context, outcome interactions.Outcome) error {
					hook.resolutions = append(hook.resolutions, outcome)
					return nil
				},
			},
		}
		return response, HookDecision{Action: HookActionModify}
	}
	response.Result = toolshared.MediaResult("masked success", []string{"media://cached"}).
		WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	return response, HookDecision{Action: HookActionModify}
}

func TestRequiredLiveDataFailureStopsCachedCardBeforeGenerationOrDelivery(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		guarded bool
		mask    bool
		success bool
		suspend bool
	}{
		{name: "unconfigured"},
		{name: "required", guarded: true},
		{name: "successful required read", guarded: true, success: true},
		{name: "hook cannot mask source failure", guarded: true, mask: true},
		{name: "hook cannot suspend after source failure", guarded: true, mask: true, suspend: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			guarded := scenario.guarded
			blocked := guarded && !scenario.success
			store := session.NewMemoryStore()
			registry := tools.NewToolRegistry()
			source := &fixedToolResultTool{
				name: "mcp_renpho_get_latest_measurement", result: toolshared.ErrorResult("Authentication failed"),
			}
			if scenario.success {
				source.result = toolshared.SilentResult("fresh live measurement")
			}
			registry.Register(source)
			calls := []providers.ToolCall{{ID: "live", Name: source.Name(), Arguments: map[string]any{}}}
			var later []*fixedToolResultTool
			for _, name := range []string{"mcp_nutritiondb_list_weight_entries", "image_generate", "message", "send_file"} {
				tool := &fixedToolResultTool{
					name:   name,
					result: toolshared.SilentResult("cached historical measurement"),
				}
				registry.Register(tool)
				later = append(later, tool)
				calls = append(calls, providers.ToolCall{ID: name, Name: name, Arguments: map[string]any{}})
			}
			agent := &AgentInstance{ID: "nutrition", Tools: registry, Sessions: store}
			inbound := bus.InboundContext{
				Channel:  "telegram",
				Account:  "primary",
				ChatID:   "live-data-chat",
				SenderID: "live-data-user",
			}
			ts := &turnState{
				agent:      agent,
				agentID:    agent.ID,
				turnID:     "live-data-test",
				sessionKey: "test",
				session:    store,
				channel:    inbound.Channel,
				chatID:     inbound.ChatID,
				opts: freezeTurnInput(
					turnSpec{Dispatch: DispatchRequest{SessionKey: "test", InboundContext: &inbound}},
				),
			}
			intent := providers.Message{Role: "assistant", ToolCalls: calls}
			if err := store.AppendTurnMessage(t.Context(), "test", intent); err != nil {
				t.Fatal(err)
			}
			exec := newTurnExecution(agent, ts.opts, nil, nil, []providers.Message{intent})
			exec.deliverable = &taskresult.Deliverable{Artifacts: []taskresult.Artifact{{Ref: "media://cached"}}}
			llm := newLLMIterationState(1)
			llm.normalizedToolCalls = calls
			llm.assistantToolCallsPersisted = true
			cfg := config.DefaultConfig()
			if guarded {
				cfg.Tools.LiveDataGuards = []config.LiveDataGuardConfig{{
					Tools: []string{source.Name()}, FailureMessage: "RENPHO unavailable. No card was created or sent.",
				}}
			}
			pipeline := &Pipeline{Cfg: cfg, Context: PipelineContextServices{Runtime: &recordingCanonicalIngest{}}}
			delivery := &recordingToolResultDelivery{}
			pipeline.Interaction.SyncToolDelivery = delivery
			manager := &fakeToolSuspensionManager{
				disposition: ToolSuspensionDisposition{InteractionID: "masked-source", Durable: true},
			}
			pipeline.Interaction.Suspension = manager
			hook := &liveDataResultMaskHook{suspend: scenario.suspend}
			if scenario.mask {
				pipeline.Interaction.Hooks = hook
			}
			outcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
			if outcome.JournalErr != nil {
				t.Fatal(outcome.JournalErr)
			}
			if blocked {
				if len(manager.requests) != 0 {
					t.Fatalf("failed source admitted replacement suspension: %#v", manager.requests)
				}
				if scenario.suspend &&
					(len(hook.resolutions) != 1 || hook.resolutions[0] != interactions.OutcomeCanceled) {
					t.Fatalf("replacement suspension resolution = %#v", hook.resolutions)
				}
				if delivery.syncCalls != 0 {
					t.Fatalf("source failure delivered media through a hook: %d", delivery.syncCalls)
				}
				if outcome.Control != turnStepFinalize || outcome.TerminalMode != terminalRenderExact ||
					!strings.Contains(outcome.FinalContent, "No card") || exec.deliverable != nil {
					t.Fatalf("unsafe terminal outcome: %#v, deliverable=%#v", outcome, exec.deliverable)
				}
			}
			for _, tool := range later {
				want := 1
				if blocked {
					want = 0
				}
				if tool.executions != want {
					t.Fatalf("%s executions=%d, want %d", tool.Name(), tool.executions, want)
				}
				result := matchingToolResult(t, store.GetHistory("test"), tool.Name())
				if blocked && !strings.Contains(result.Content, "required live data fetch failed") {
					t.Fatalf("missing durable skipped-call explanation: %#v", result)
				}
			}
		})
	}
}
