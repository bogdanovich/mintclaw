package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type routedCaptureFixture struct {
	*protectedFixedToolResultTool
	workspace string
	ref       string
}

func (tool *routedCaptureFixture) Execute(ctx context.Context, _ map[string]any) *toolshared.ToolResult {
	owner, err := media.NewMediaOwner(tool.workspace, toolshared.ToolAgentID(ctx), toolshared.ToolActorID(ctx),
		toolshared.ToolRouteSessionKey(ctx), toolshared.ToolSessionKey(ctx),
		toolshared.ToolChannel(ctx), toolshared.ToolChatID(ctx), toolshared.ToolTopicID(ctx))
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	result := toolshared.NewToolResult(`{"artifact":{"ref":"transfer-artifact://verification"}}`).
		WithOutboundDelivery(toolshared.OutboundDelivery{
			Media: []bus.MediaPart{
				{Type: "image", Ref: tool.ref, Filename: "verification.png", ContentType: "image/png"},
			},
			Recovery: &bus.OutboundRecovery{
				Kind:        bus.OutboundRecoveryBrowserScreenshot,
				ArtifactRef: "transfer-artifact://verification",
				MediaRef:    tool.ref,
				WorkspaceID: owner.WorkspaceID,
				AgentID:     owner.AgentID,
				ActorID:     owner.ActorID,
				RouteID:     owner.RouteID,
				SessionID:   "already-closed-session",
				ToolCallID:  "capture",
			},
		}).WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
	result.Media = []string{tool.ref}
	return result
}

func TestDelegateRetainedScreenshotReachesChannelWithCompleteReport(t *testing.T) {
	for _, channel := range []string{"telegram", "discord"} {
		for _, mode := range []toolshared.AsyncDeliveryMode{toolshared.AsyncDeliveryUserOnly, toolshared.AsyncDeliveryParentOnly} {
			t.Run(channel+"/"+string(mode), func(t *testing.T) {
				const report = "Fresh verification found zero product photos. The upload did not save. The browser is closed."
				outcome, err := json.Marshal(reportedObjectiveOutcome{
					Status: "partial", Result: report,
					MissingItems: []string{"objective_2"}, Explanation: "The upload did not save.",
					CompletedItems: []reportedObjectiveItem{{
						ObjectiveID: "objective_1", Output: &taskresult.ObjectiveOutput{Kind: "text", Text: report},
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
				provider := &sequenceProvider{responses: []*providers.LLMResponse{
					{
						ToolCalls: []providers.ToolCall{
							{ID: "delegate-browser", Name: "delegate", Arguments: map[string]any{
								"agent_id":      "browser",
								"task":          "Save the original photos, verify the result and return a screenshot with the complete report.",
								"delivery_mode": string(mode),
								"objective_items": []any{
									map[string]any{
										"item": "Return the verified result and closure state",
										"kind": "result",
									},
									map[string]any{
										"item": "Upload and save the original photographs",
										"kind": "external_action",
									},
								},
							}},
						},
					},
					{
						ToolCalls: []providers.ToolCall{
							{ID: "capture", Name: "browser_capture", Arguments: map[string]any{}},
						},
					},
					{Content: objectiveOutcomeStart + string(outcome) + objectiveOutcomeEnd, FinishReason: "stop"},
					{Content: report, FinishReason: "stop"},
				}}
				fixture := newAgentLoopTestFixture(t, provider, func(cfg *config.Config) {
					cfg.Agents.List = []config.AgentConfig{
						{
							ID: "main", Default: true, Workspace: filepath.Join(cfg.WorkspacePath(), "main"),
							Subagents: &config.SubagentsConfig{AllowAgents: []string{"browser"}},
						},
						{ID: "browser", Workspace: filepath.Join(cfg.WorkspacePath(), "browser")},
					}
				})
				loop, store := fixture.Loop, media.NewFileMediaStore()
				path := filepath.Join(t.TempDir(), "verification.png")
				if err = os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nfixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				ref, err := store.Store(
					path,
					media.MediaMeta{Filename: "verification.png", ContentType: "image/png"},
					"fixture",
				)
				if err != nil {
					t.Fatal(err)
				}
				loop.SetMediaStore(store)
				source := &retainedMediaSource{}
				loop.SetBrowserCapabilityClient(source)
				if err = loop.RegisterRuntimeTool("browser_capture", func(*config.Config) (toolshared.Tool, error) {
					return &routedCaptureFixture{
						protectedFixedToolResultTool: &protectedFixedToolResultTool{
							fixedToolResultTool: &fixedToolResultTool{
								name: "browser_capture",
							},
						},
						workspace: fixture.Config.WorkspacePath(),
						ref:       ref,
					}, nil
				}); err != nil {
					t.Fatal(err)
				}
				installTestOutboundCoordinator(t, loop, fixture.Config.WorkspacePath())
				adapter := &fakeMediaChannel{}
				loop.SetChannelManager(newStartedTestChannelManagerWithConfig(t, fixture.Config, fixture.Bus, store,
					channel, adapter, channels.WithOutboundOutbox(loop.outboundCoordinator())))
				ctx := withOutboundTransaction(t.Context(), "delegated-verification")
				_, err = loop.runAgentLoop(ctx, fixture.Agent, turnSpec{
					Dispatch: DispatchRequest{
						SessionKey:      "parent-session",
						RouteSessionKey: "original-route",
						UserMessage:     "Verify and send the screenshot.",
						InboundContext:  &bus.InboundContext{Channel: channel, ChatID: "chat", ActorID: "actor"},
					},
					SendResponse: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				adapter.mu.Lock()
				defer adapter.mu.Unlock()
				if len(adapter.sentMedia) != 1 || len(adapter.sentMessages) != 0 || len(source.claims) != 1 {
					t.Fatalf("expected one receipt-backed final media: media=%#v text=%#v claims=%#v",
						adapter.sentMedia, adapter.sentMessages, source.claims)
				}
				if parts := adapter.sentMedia[0].Parts; len(parts) != 1 || parts[0].Caption != report ||
					parts[0].Ref != ref {
					t.Fatalf("missing screenshot or incomplete final report: %#v", parts)
				}
			})
		}
	}
}
