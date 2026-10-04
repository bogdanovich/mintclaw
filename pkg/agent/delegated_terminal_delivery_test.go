package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDelegateUserOnlyIncompleteOutcomeHasOneChannelDelivery(t *testing.T) {
	for _, route := range []struct {
		channel string
		durable bool
	}{
		{channel: "telegram"},
		{channel: "discord"},
		{channel: "telegram", durable: true},
		{channel: "discord", durable: true},
	} {
		for _, status := range []taskresult.OutcomeStatus{
			taskresult.OutcomeSucceeded, taskresult.OutcomePartial, taskresult.OutcomeBlocked,
		} {
			name := route.channel + "/" + string(status)
			if route.durable {
				name += "/durable"
			}
			t.Run(name, func(t *testing.T) {
				completed := []reportedObjectiveItem{}
				missing := []string{"objective_1", "objective_2"}
				if status != taskresult.OutcomeBlocked {
					completed = append(completed, reportedObjectiveItem{
						ObjectiveID: "objective_1",
						Output:      &taskresult.ObjectiveOutput{Kind: "text", Text: "Verified first finding."},
					})
					missing = []string{"objective_2"}
				}
				if status == taskresult.OutcomeSucceeded {
					completed = append(completed, reportedObjectiveItem{
						ObjectiveID: "objective_2",
						Output:      &taskresult.ObjectiveOutput{Kind: "text", Text: "Verified second finding."},
					})
					missing = nil
				}
				encoded, err := json.Marshal(reportedObjectiveOutcome{
					Status: string(status), CompletedItems: completed, MissingItems: missing,
					Result: "Inspection finished.", Explanation: "The remaining account requires sign-in.",
				})
				if err != nil {
					t.Fatal(err)
				}
				provider := &sequenceProvider{responses: []*providers.LLMResponse{
					{ToolCalls: []providers.ToolCall{{
						ID: "delegate-two-findings", Name: "delegate",
						Arguments: map[string]any{
							"agent_id": "beta", "task": "Inspect both accounts and report the findings.",
							"delivery_mode": string(toolshared.AsyncDeliveryUserOnly),
							"objective_items": []any{
								map[string]any{"item": "first required finding", "kind": "result"},
								map[string]any{"item": "second required finding", "kind": "result"},
							},
						},
					}}},
					{Content: objectiveOutcomeStart + string(encoded) + objectiveOutcomeEnd, FinishReason: "stop"},
				}}
				fixture := newAgentLoopTestFixture(t, provider, func(cfg *config.Config) {
					cfg.Agents.List = []config.AgentConfig{
						{
							ID: "alpha", Default: true, Workspace: filepath.Join(cfg.WorkspacePath(), "alpha"),
							Subagents: &config.SubagentsConfig{AllowAgents: []string{"beta"}},
						},
						{ID: "beta", Workspace: filepath.Join(cfg.WorkspacePath(), "beta")},
					}
				})
				loop, alpha := fixture.Loop, fixture.Agent
				ctx := t.Context()
				var options []channels.ManagerOption
				if route.durable {
					installTestOutboundCoordinator(t, loop, t.TempDir())
					options = append(options, channels.WithOutboundOutbox(loop.outboundCoordinator()))
					ctx = withOutboundTransaction(ctx, "two-findings")
				}
				adapter := &fakeMediaChannel{}
				loop.SetChannelManager(newStartedTestChannelManagerWithConfig(
					t, fixture.Config, fixture.Bus, media.NewFileMediaStore(), route.channel, adapter, options...,
				))
				response, err := loop.runAgentLoop(ctx, alpha, turnSpec{
					Dispatch: DispatchRequest{
						SessionKey: "two-findings", UserMessage: "Inspect both accounts.",
						InboundContext: &bus.InboundContext{Channel: route.channel, ChatID: "chat-1"},
					},
					SendResponse: true, DefaultResponse: defaultResponse,
				})
				if err != nil {
					t.Fatal(err)
				}
				if response != "" {
					t.Errorf("already-delivered result returned another response: %q", response)
				}
				messages := adapter.messagesSnapshot()
				if len(messages) != 1 {
					t.Fatalf("adapter delivered %d messages, want exactly one: %#v", len(messages), messages)
				}
				if status != taskresult.OutcomeBlocked &&
					!strings.Contains(messages[0].Content, "Verified first finding.") {
					t.Fatalf("delivered result lost the verified finding: %q", messages[0].Content)
				}
				if provider.callCount != 2 {
					t.Fatalf("provider calls = %d, want one parent and one child", provider.callCount)
				}
				history := alpha.Sessions.GetHistory("two-findings")
				if len(history) == 0 {
					t.Fatal("canonical history is empty")
				}
				last := history[len(history)-1]
				if last.Deliverable == nil || last.Deliverable.ObjectiveOutcome == nil ||
					last.Deliverable.ObjectiveOutcome.Status != status {
					t.Fatalf("canonical history lost the verified outcome: %#v", last)
				}
			})
		}
	}
}

func TestAlreadyHandledTerminalRetainsResultWithoutTextOrMediaResend(t *testing.T) {
	for _, status := range []taskresult.OutcomeStatus{
		taskresult.OutcomeSucceeded, taskresult.OutcomePartial, taskresult.OutcomeBlocked,
	} {
		for _, withMedia := range []bool{false, true} {
			name := string(status) + "/text"
			if withMedia {
				name = string(status) + "/media"
			}
			t.Run(name, func(t *testing.T) {
				fixture := newAgentLoopTestFixture(t, &simpleConvProvider{})
				store := media.NewFileMediaStore()
				fixture.Loop.SetMediaStore(store)
				adapter := &fakeMediaChannel{}
				fixture.Loop.SetChannelManager(newStartedTestChannelManager(
					t, fixture.Bus, store, "telegram", adapter,
				))
				const retainedText = "Verified result already delivered."
				deliverable := &taskresult.Deliverable{
					Text: retainedText,
					ObjectiveOutcome: &taskresult.Outcome{
						Status: status, Explanation: "Some requested work remains blocked.",
					},
				}
				if withMedia {
					path := filepath.Join(t.TempDir(), "report.txt")
					if err := os.WriteFile(path, []byte("verified finding"), 0o600); err != nil {
						t.Fatal(err)
					}
					ref, err := store.Store(path, media.MediaMeta{ContentType: "text/plain"}, "test")
					if err != nil {
						t.Fatal(err)
					}
					deliverable.Artifacts = []taskresult.Artifact{{Ref: ref, Kind: "file", ContentType: "text/plain"}}
				}
				spec := turnSpec{
					Dispatch: DispatchRequest{SessionKey: "handled-terminal", InboundContext: &bus.InboundContext{
						Channel: "telegram", ChatID: "chat-1",
					}},
					SendResponse: true,
				}
				state := fixture.turnState(spec)
				exec := &turnExecution{deliverable: deliverable}
				llm := newLLMIterationState(1)
				llm.toolResponseDisposition = toolResponseHandled
				terminal := newTestPipeline(fixture.Loop).completeTerminal(
					t.Context(), state, exec, llm, TurnEndStatusCompleted,
					terminalRequest{content: terminalContent{content: retainedText}},
				)
				if terminal.err != nil || terminal.resume {
					t.Fatalf("terminal completion = %#v", terminal)
				}
				result := terminal.result
				fixture.Loop.deliverFinalTurnResult(
					t.Context(), runtimeevents.NewTraceScope(fixture.Agent.Workspace, "handled-terminal"),
					fixture.Agent, freezeTurnInput(spec), result,
				)
				if got := adapter.messagesSnapshot(); len(got) != 0 || len(adapter.sentMedia) != 0 {
					t.Fatalf("already-handled final was sent again: text=%#v media=%#v", got, adapter.sentMedia)
				}
				if result.responseContent() != "" {
					t.Fatalf("already-handled final escaped through responseContent: %q", result.responseContent())
				}
				if result.deliverable == nil || result.deliverable.Text != retainedText ||
					result.deliverable.ObjectiveOutcome.Status != status || result.finalContent != retainedText {
					t.Fatalf("delivery suppression erased the retained result: %#v", result)
				}
			})
		}
	}
}
