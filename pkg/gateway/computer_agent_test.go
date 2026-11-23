package gateway

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
)

const (
	computerAgentFixtureActor = "cli:cron"
	computerAgentFixtureAgent = "fixture"
)

type computerAgentFixtureAction struct {
	arguments   map[string]any
	observation string
}

type computerAgentFixtureStep struct {
	tool        string
	arguments   map[string]any
	observation string
}

type computerAgentFixtureView struct {
	SessionID      string                 `json:"computer_session_id"`
	ObservationID  string                 `json:"observation_id"`
	Control        computer.ControlState  `json:"control"`
	Targets        []computer.NamedTarget `json:"targets"`
	InputAvailable bool                   `json:"input_available"`
}

func runComputerAgentFixture(
	t *testing.T, cfg *config.Config, runtime *nodeAdmissionRuntime, initial string,
	actions []computerAgentFixtureAction,
) {
	t.Helper()
	tools := []string{"computer_act", "computer_observe", "computer_session", "computer_targets"}
	const model = "computer-fixture-model"
	const final = "Fixture computer session released."
	steps := []computerAgentFixtureStep{
		{tool: "computer_targets", arguments: map[string]any{}},
		{tool: "computer_session", arguments: map[string]any{"operation": "open", "target": "desktop"}},
		{tool: "computer_observe", arguments: map[string]any{}, observation: initial},
	}
	for _, action := range actions {
		steps = append(steps, computerAgentFixtureStep{
			tool: "computer_act", arguments: action.arguments, observation: action.observation,
		})
	}
	steps = append(steps, computerAgentFixtureStep{
		tool: "computer_session", arguments: map[string]any{"operation": "release"},
	})
	script := make([]llmscenario.ProviderStep, 0, len(steps)+1)
	var windowSession, observation string
	var sensitive []string
	for index, step := range steps {
		toolCall := llmscenario.ToolCall(
			fmt.Sprintf("computer-fixture-%d", index),
			step.tool,
			maps.Clone(step.arguments),
		)
		response := llmscenario.ToolCallResponse("", toolCall)
		script = append(script, llmscenario.ProviderStep{
			Name: step.tool, Response: response, Assert: func(call llmscenario.ProviderCall) error {
				var advertised []string
				for _, definition := range call.Tools {
					advertised = append(advertised, definition.Function.Name)
				}
				slices.Sort(advertised)
				if !slices.Equal(advertised, tools) {
					return fmt.Errorf("unexpected tools in computer evaluation turn")
				}
				if index > 0 {
					previous := steps[index-1]
					view, err := computerAgentFixtureResult(call, index-1, previous.observation, sensitive)
					if err != nil {
						return err
					}
					switch previous.tool {
					case "computer_targets":
						if len(view.Targets) != 1 || view.Targets[0].Alias != "desktop" ||
							!view.Targets[0].InputAvailable || !view.InputAvailable {
							return fmt.Errorf("admitted input target missing from discovery")
						}
					case "computer_session":
						if view.SessionID == "" || view.Control != computer.ControlAgent {
							return fmt.Errorf("open did not return the agent-owned session")
						}
						windowSession = view.SessionID
					default:
						if view.SessionID != windowSession || view.ObservationID == "" ||
							view.ObservationID == observation {
							return fmt.Errorf("result failed to advance same-window observation")
						}
						observation = view.ObservationID
					}
				}
				arguments := response.ToolCalls[0].Arguments
				if index > 1 {
					arguments["computer_session_id"] = windowSession
				}
				if step.tool == "computer_act" {
					arguments["observation_id"] = observation
				}
				return nil
			},
		})
		if step.observation != "" {
			sensitive = append(sensitive, step.observation)
		}
		if text, ok := step.arguments["text"].(string); ok {
			sensitive = append(sensitive, text)
		}
	}
	script = append(script, llmscenario.ProviderStep{
		Name: "finish", Response: llmscenario.TextResponse(final),
		Assert: func(call llmscenario.ProviderCall) error {
			view, err := computerAgentFixtureResult(call, len(steps)-1, "", sensitive)
			if err != nil || view.SessionID != windowSession || view.Control != computer.ControlReleased {
				return fmt.Errorf("agent turn did not retain terminal release proof")
			}
			return nil
		},
	})
	provider := llmscenario.NewScriptedProvider(model, script...)
	cfg.Agents.List = []config.AgentConfig{{ID: computerAgentFixtureAgent, Default: true}}
	cfg.Agents.Defaults.ModelName = model
	cfg.Agents.Defaults.MaxToolIterations = len(script) + 1
	cfg.Agents.Defaults.ResponseFooter.Enabled = false
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.TurnProfile = config.TurnProfileConfig{
		Enabled: true,
		Skills:  config.TurnProfileBlock{Mode: config.TurnProfileModeOff},
		Tools:   config.TurnProfileBlock{Mode: config.TurnProfileModeCustom, Allow: tools},
	}
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	loop := agent.NewAgentLoop(
		cfg, messageBus, provider, agent.WithIsolatedToolBootstrap(), agent.WithIsolatedSkillBootstrap(),
	)
	defer loop.Close()
	running := &services{NodeAdmission: runtime}
	if err := setupComputerTools(cfg, loop, running); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeComputerRuntime(t.Context(), running); err != nil {
			t.Error(err)
		}
	}()
	key := session.BuildOpaqueSessionKey("computer-fixture-turn")
	got, err := loop.ProcessDirectWithOptions(t.Context(),
		"Use the selected fixture window, verify the result, and release the session.",
		key, "cli", "computer-fixture", agent.DirectTurnOptions{SuppressBackgroundCompaction: true})
	if err != nil || got != final {
		t.Fatalf("computer fixture did not finish through AgentLoop: result length %d, error %v",
			len(got), err)
	}
	if err := provider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}
	store := loop.GetRegistry().GetDefaultAgent().Sessions
	history := store.GetHistory(key)
	if len(history) == 0 {
		t.Fatal("fixture turn journal is missing")
	}
	for _, message := range history {
		for _, private := range sensitive {
			if strings.Contains(message.Content, private) {
				t.Fatal("live computer data entered the durable conversation")
			}
		}
		for _, call := range message.ToolCalls {
			if text, supplied := call.Arguments["text"]; call.Name == "computer_act" && supplied && text != "*" {
				t.Fatal("protected input entered durable tool arguments")
			}
		}
	}
}

func computerAgentFixtureResult(
	call llmscenario.ProviderCall, index int, expected string, sensitive []string,
) (computerAgentFixtureView, error) {
	id := fmt.Sprintf("computer-fixture-%d", index)
	var result computerAgentFixtureView
	found := false
	for _, message := range call.Messages {
		if message.Role != "tool" {
			continue
		}
		if message.ToolCallID == id {
			line, contextText, _ := strings.Cut(message.Content, "\n")
			if found || message.ToolResultStatus == "error" || contextText != expected ||
				json.Unmarshal([]byte(line), &result) != nil {
				return computerAgentFixtureView{}, fmt.Errorf("invalid computer result for %s", id)
			}
			found = true
			continue
		}
		for _, private := range sensitive {
			if strings.Contains(message.Content, private) {
				return computerAgentFixtureView{}, fmt.Errorf("an older live observation survived its model call")
			}
		}
	}
	if !found {
		return computerAgentFixtureView{}, fmt.Errorf("missing computer result for %s", id)
	}
	return result, nil
}

func TestComputerAgentUsesFreshProtectedInputTarget(t *testing.T) {
	client, handler, cfg := computerClientFixtureWithInput(t, 0, true, true)
	target := cfg.Tools.Computer.Targets["desktop"]
	target.Actors = []string{computerAgentFixtureActor}
	target.Agents = []string{computerAgentFixtureAgent}
	cfg.Tools.Computer.Targets["desktop"] = target
	text := "protected native fixture — café 世界"
	runComputerAgentFixture(t, cfg, client.source.runtime, "private-fixture-canary", []computerAgentFixtureAction{{
		arguments: map[string]any{
			"action": map[string]any{"kind": "insert_text", "text": map[string]any{"element_ref": "fixture_caret"}},
			"text":   text,
		},
		observation: text,
	}})
	want := []string{
		nodes.ComputerCommandTargets, nodes.ComputerCommandOpen, nodes.ComputerCommandObserve,
		nodes.ComputerCommandAct, nodes.ComputerCommandRelease,
	}
	if !slices.Equal(handler.commands, want) || handler.actionText != text ||
		handler.session.Control != computer.ControlReleased {
		t.Fatal("AgentLoop failed to perform exactly one protected action and release")
	}
}
