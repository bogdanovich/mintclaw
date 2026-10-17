package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	protectedVisualProbeName  = "protected_visual_probe"
	protectedVisualProbeText  = "private-live-desktop-canary-99fdd124"
	protectedVisualProbeImage = "data:image/png;base64,cHJvdGVjdGVkLWRlc2t0b3A="
)

// An arbitrary tool name verifies the capability flag, not a name-based
// computer/document special case. Native capture is qualified separately.
type protectedVisualProbeTool struct{}

func (*protectedVisualProbeTool) Name() string { return protectedVisualProbeName }
func (*protectedVisualProbeTool) Description() string {
	return "return one protected visual observation"
}

func (*protectedVisualProbeTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false}
}

func (*protectedVisualProbeTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	return &toolshared.ToolResult{
		ForLLM: `{"state":"observed"}`, ContextText: protectedVisualProbeText,
		ContextMedia: []string{protectedVisualProbeImage}, ContextMediaRequiresVision: true,
	}
}

type protectedVisualProbeProvider struct{ requests [][]providers.Message }

func (*protectedVisualProbeProvider) GetDefaultModel() string { return "visual-probe-model" }
func (provider *protectedVisualProbeProvider) Chat(
	_ context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any,
) (*providers.LLMResponse, error) {
	copyMessages := append([]providers.Message(nil), messages...)
	for index := range copyMessages {
		copyMessages[index].Media = append([]string(nil), messages[index].Media...)
	}
	provider.requests = append(provider.requests, copyMessages)
	switch len(provider.requests) {
	case 1:
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
			ID: "visual-probe-call", Type: "function", Name: protectedVisualProbeName, Arguments: map[string]any{},
		}}}, nil
	case 2:
		return liveContextNoopCallResponse(), nil
	default:
		return &providers.LLMResponse{Content: "Observation checked."}, nil
	}
}

func TestProtectedVisualContextRequiresVisionAndNeverPersists(t *testing.T) {
	for _, vision := range []bool{true, false} {
		name := "text_only"
		if vision {
			name = "vision_capable"
		}
		t.Run(name, func(t *testing.T) {
			model := &config.ModelConfig{
				ModelName: "visual-probe-model", Provider: "openai", Model: "visual-probe-model", Enabled: true,
			}
			if vision {
				model.Capabilities = &config.ModelCapabilities{Vision: &config.ModelCapabilityOverride{}}
			}
			cfg := &config.Config{
				Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
					Workspace: t.TempDir(), ModelName: model.ModelName, MaxTokens: 4096,
					MaxToolIterations: 5, ContextManager: "none",
				}}, ModelList: []*config.ModelConfig{model},
			}
			provider := &protectedVisualProbeProvider{}
			loop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
			defer loop.Close()
			useTestSideQuestionProvider(loop, provider)
			loop.RegisterTool(&protectedVisualProbeTool{})
			loop.RegisterTool(&liveContextRetryNoopTool{})
			key := session.BuildOpaqueSessionKey("agent:main:test:protected-visual-" + name)
			response, err := loop.processMessage(t.Context(), testInboundMessage(bus.InboundMessage{
				Context:    bus.InboundContext{Channel: "test", ChatID: "protected-visual-" + name},
				SessionKey: key, Content: "Inspect the current observation, then finish.",
			}))
			if vision {
				if err != nil || response != "Observation checked." || len(provider.requests) != 3 {
					t.Fatalf("vision route did not complete: calls=%d error=%v", len(provider.requests), err)
				}
				if !messagesContainExactMedia(provider.requests[1], protectedVisualProbeImage) ||
					!messagesContainText(provider.requests[1], protectedVisualProbeText) {
					t.Fatal("vision provider did not receive the live observation")
				}
				if messagesContainExactMedia(provider.requests[2], protectedVisualProbeImage) ||
					messagesContainText(provider.requests[2], protectedVisualProbeText) {
					t.Fatal("consumed observation survived into the next model iteration")
				}
			} else if len(provider.requests) != 1 {
				t.Fatal("text-only provider was called with or after stripping required visual context")
			}
			history := loop.registry.GetDefaultAgent().Sessions.GetHistory(key)
			if messagesContainExactMedia(history, protectedVisualProbeImage) ||
				messagesContainText(history, protectedVisualProbeText) {
				t.Fatal("protected visual observation entered canonical history")
			}
		})
	}
}
