package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type routedCaptureFixture struct {
	*protectedFixedToolResultTool
	workspace string
	ref       string
	claim     func(context.Context) error
}

func (tool *routedCaptureFixture) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if args["delivery"] == "interaction" && !toolshared.ToolRecoverableOutbound(ctx) {
		return toolshared.ErrorResult("browser screenshots require a durable outbound delivery transaction")
	}
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
	if args["delivery"] == "interaction" {
		result.Delivery.Intent = toolshared.DeliveryInteractionMedia
	}
	if tool.claim != nil {
		result.WithOutboundCommit(tool.claim)
	}
	return result
}

func TestDelegateRetainedScreenshotReachesChannelWithCompleteReport(t *testing.T) {
	for _, channel := range []string{"telegram", "discord"} {
		for _, mode := range []toolshared.AsyncDeliveryMode{
			toolshared.AsyncDeliveryUserOnly, toolshared.AsyncDeliveryParentOnly, toolshared.AsyncDeliveryUserAndParent,
		} {
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
				wantText := 0
				if mode == toolshared.AsyncDeliveryUserAndParent {
					wantText = 1 // The declared mode also requests the parent's continuation.
				}
				if len(adapter.sentMedia) != 1 || len(adapter.sentMessages) != wantText || len(source.claims) != 1 {
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

// Observe transport order, not merely publication intent or the final report.
type challengeOrderChannel struct {
	fakeMediaChannel
	questionBeforeImage bool
}

func (channel *challengeOrderChannel) DeliverText(
	ctx context.Context,
	messages []bus.OutboundMessage,
) channels.DeliveryResult[bus.OutboundMessage] {
	channel.mu.Lock()
	for _, message := range messages {
		if strings.Contains(message.Content, "Enter the fixture challenge") && len(channel.sentMedia) != 1 {
			channel.questionBeforeImage = true
		}
	}
	channel.mu.Unlock()
	return channel.fakeMediaChannel.DeliverText(ctx, messages)
}

func TestDelegatedInteractionImageReachesOwnerBeforeNativeQuestion(t *testing.T) {
	testDelegatedInteractionImageBeforeQuestion(t, false)
}

func TestDelegatedInteractionImageAfterExternalWaitReachesOwnerBeforeNativeQuestion(t *testing.T) {
	testDelegatedInteractionImageBeforeQuestion(t, true)
}

func testDelegatedInteractionImageBeforeQuestion(t *testing.T, delayed bool) {
	t.Helper()
	wait := &externalWaitTestTool{}
	wait.state.Store(toolshared.ExternalWaitPending)
	provider := &sequenceProvider{responses: []*providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{{ID: "delegate-challenge", Name: "delegate", Arguments: map[string]any{
			"agent_id": "browser", "task": "Show the challenge image, then ask for its answer.",
		}}}},
		{ToolCalls: []providers.ToolCall{{ID: "capture-challenge", Name: "browser_capture", Arguments: map[string]any{
			"delivery": "interaction",
		}}}},
		{ToolCalls: []providers.ToolCall{{ID: "ask-challenge", Name: "request_user_input", Arguments: map[string]any{
			"questions": []any{
				map[string]any{"id": "challenge", "header": "Challenge", "question": "Enter the fixture challenge"},
			},
		}}}},
	}}
	if delayed {
		provider.responses = append([]*providers.LLMResponse{
			provider.responses[0],
			{ToolCalls: []providers.ToolCall{{ID: "wait-phone", Name: wait.Name(), Arguments: map[string]any{}}}},
		}, provider.responses[1:]...)
	}
	fixture := newAgentLoopTestFixture(t, provider, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{
			{
				ID:        "main",
				Default:   true,
				Workspace: filepath.Join(cfg.WorkspacePath(), "main"),
				Subagents: &config.SubagentsConfig{AllowAgents: []string{"browser"}},
			},
			{ID: "browser", Workspace: filepath.Join(cfg.WorkspacePath(), "browser")},
		}
	})
	loop, store := fixture.Loop, media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "challenge.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nfixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{Filename: "challenge.png", ContentType: "image/png"}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	loop.SetMediaStore(store)
	if err = loop.RegisterRuntimeTool(wait.Name(), func(*config.Config) (toolshared.Tool, error) {
		return wait, nil
	}); err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int32
	if err = loop.RegisterRuntimeTool("browser_capture", func(*config.Config) (toolshared.Tool, error) {
		return &routedCaptureFixture{
			protectedFixedToolResultTool: &protectedFixedToolResultTool{
				fixedToolResultTool: &fixedToolResultTool{name: "browser_capture"},
			},
			workspace: fixture.Config.WorkspacePath(),
			ref:       ref,
			claim:     func(context.Context) error { claims.Add(1); return nil },
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = loop.RegisterRuntimeTool("request_user_input", func(*config.Config) (toolshared.Tool, error) {
		return tools.NewRequestUserInputTool(tools.RequestUserInputToolOptions{})
	}); err != nil {
		t.Fatal(err)
	}
	installTestOutboundCoordinator(t, loop, fixture.Config.WorkspacePath())
	adapter := &challengeOrderChannel{}
	loop.SetChannelManager(newStartedTestChannelManagerWithConfig(t, fixture.Config, fixture.Bus, store,
		"telegram", adapter, channels.WithOutboundOutbox(loop.outboundCoordinator())))
	_, status, err := runAgentLoopWithStatusForTest(
		withOutboundTransaction(t.Context(), "delegated-challenge"),
		loop,
		fixture.Agent,
		turnSpec{
			Dispatch: DispatchRequest{
				SessionKey:      "parent-challenge-session",
				RouteSessionKey: "original-route",
				UserMessage:     "Help me with the challenge.",
				InboundContext: &bus.InboundContext{
					Channel:  "telegram",
					ChatID:   "chat",
					ActorID:  "actor",
					SenderID: "user-1",
					ChatType: "direct",
				},
			},
			SendResponse: true,
		},
	)
	if err != nil || status != TurnEndStatusSuspended {
		for _, request := range provider.requests {
			for _, message := range request {
				if message.ToolCallID == "ask-challenge" {
					t.Logf("native question result: %s", message.Content)
				}
			}
		}
		t.Fatalf("native question did not suspend: %s, %v", status, err)
	}
	record, ok := activeInteractionForSession(
		loop.interactionRegistryForWorkspace(fixture.Agent.Workspace),
		"parent-challenge-session",
	)
	if !ok || record.Status != interactions.StatusWaiting || record.Origin.TaskID == "" {
		t.Fatal("delegated question did not reach the owner")
	}
	if delayed {
		if record.Kind != interactions.KindExternalWait {
			t.Fatal("delegated task did not wait for phone approval")
		}
		wait.state.Store(toolshared.ExternalWaitReady)
		// The background phone-ready event has no inbound publication context.
		loop.recoverHumanInteractionsAt(t.Context(), time.Now())
		loop.recoverHumanInteractionsAt(t.Context(), time.Now())
		question, found := activeInteractionForSession(
			loop.interactionRegistryForWorkspace(fixture.Agent.Workspace),
			"parent-challenge-session",
		)
		if !found || question.Kind != interactions.KindQuestion || question.Origin.TaskID != record.Origin.TaskID {
			t.Fatal("resumed delegated question lost its owner")
		}
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.questionBeforeImage || len(adapter.sentMedia) != 1 || claims.Load() != 1 {
		t.Fatalf(
			"image/question order or durable claim failed: early=%v images=%d claims=%d",
			adapter.questionBeforeImage,
			len(adapter.sentMedia),
			claims.Load(),
		)
	}
}
