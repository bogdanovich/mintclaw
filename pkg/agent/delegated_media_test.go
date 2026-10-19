package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type retainedMediaSource struct {
	claims []bus.OutboundRecovery
}

func (*retainedMediaSource) Available() bool { return true }
func (source *retainedMediaSource) ClaimArtifactDelivery(_ context.Context, recovery bus.OutboundRecovery) error {
	source.claims = append(source.claims, recovery)
	return nil
}

func retainedMediaTurn(t *testing.T, channel string) (*turnState, *config.Config) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	return &turnState{
		agent: &AgentInstance{ID: "browser"}, agentID: "browser",
		workspace: filepath.Join(cfg.WorkspacePath(), "agents", "browser"),
		channel:   channel, chatID: "chat", sessionKey: "child-session", turnID: "child-turn",
		opts: freezeTurnInput(turnSpec{
			SuppressToolUserDelivery: true,
			Dispatch: DispatchRequest{
				SessionKey: "child-session", RouteSessionKey: "original-route",
				InboundContext: &bus.InboundContext{Channel: channel, ChatID: "chat", ActorID: "actor"},
			},
		}),
	}, cfg
}

func retainedMediaResult(t *testing.T, ts *turnState, cfg *config.Config, suffix string) *toolshared.ToolResult {
	t.Helper()
	owner, err := nodeFileMediaOwnerForWorkspace(ts, cfg.WorkspacePath())
	if err != nil {
		t.Fatal(err)
	}
	recovery := &bus.OutboundRecovery{
		Kind:        bus.OutboundRecoveryBrowserScreenshot,
		ArtifactRef: "transfer-artifact://" + suffix,
		MediaRef:    "media://" + suffix,
		WorkspaceID: owner.WorkspaceID,
		AgentID:     owner.AgentID,
		ActorID:     owner.ActorID,
		RouteID:     owner.RouteID,
		SessionID:   "closed-browser-session",
		ToolCallID:  "capture-" + suffix,
	}
	result := toolshared.NewToolResult(`{"artifact":{"ref":"` + recovery.ArtifactRef + `"}}`).
		WithOutboundDelivery(toolshared.OutboundDelivery{
			Media: []bus.MediaPart{
				{Type: "image", Ref: recovery.MediaRef, Filename: "screenshot.png", ContentType: "image/png"},
			},
			Recovery: recovery,
		}).WithOutboundCommit(func(context.Context) error {
		t.Error("suppressed child claimed delivery")
		return nil
	}).WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
	result.Media = []string{recovery.MediaRef}
	return result
}

func TestProtectedSuppressedMediaSurvivesCompletionWithoutProviderHistoryLeak(t *testing.T) {
	ts, cfg := retainedMediaTurn(t, "telegram")
	result := retainedMediaResult(t, ts, cfg, "verification")
	store := session.NewMemoryStore()
	registry := tools.NewToolRegistry()
	tool := &protectedFixedToolResultTool{
		fixedToolResultTool: &fixedToolResultTool{name: "browser_capture", result: result},
	}
	registry.Register(tool)
	ts.agent.Tools, ts.agent.Sessions = registry, store
	call := providers.ToolCall{ID: "capture", Name: tool.Name(), Arguments: map[string]any{}}
	intent := providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{call}}
	if err := store.AppendTurnMessage(t.Context(), ts.sessionKey, intent); err != nil {
		t.Fatal(err)
	}
	exec := newTurnExecution(ts.agent, ts.opts, nil, nil, []providers.Message{intent})
	llm := newLLMIterationState(1)
	llm.normalizedToolCalls, llm.assistantToolCallsPersisted = []providers.ToolCall{call}, true
	delivery := &recordingToolResultDelivery{}
	pipeline := &Pipeline{Cfg: cfg, bus: delivery, Interaction: PipelineInteractionServices{SyncToolDelivery: delivery}}
	outcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
	if outcome.JournalErr != nil || delivery.syncCalls != 0 || delivery.outboundCalls != 0 {
		t.Fatalf("suppressed execution delivered or failed: %#v, %#v", outcome, delivery)
	}
	if exec.deliverable == nil || len(exec.deliverable.Artifacts) != 1 ||
		exec.deliverable.Artifacts[0].Recovery == nil || exec.deliverable.Artifacts[0].LocalPath != "" {
		t.Fatalf("trusted delivery capability lost: %#v", exec.deliverable)
	}
	history := store.GetHistory(ts.sessionKey)
	if len(history) != 2 || len(history[1].Media) != 0 || history[1].Deliverable != nil ||
		strings.Contains(history[1].Content, "transfer-artifact://") {
		t.Fatalf("protected capability leaked into provider history: %#v", history)
	}
	completed := terminalTurnDeliverable(exec.deliverable, "Verified result. Session closed.", nil)
	encoded, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	var recovered taskresult.Deliverable
	if err = json.Unmarshal(encoded, &recovered); err != nil {
		t.Fatal(err)
	}
	if len(mediaArtifactRefs(recovered.Artifacts)) != 1 || recovered.Artifacts[0].Recovery == nil {
		t.Fatalf("durable result lost the attachment: %#v", recovered)
	}
	merged := mergeDeliverables(&recovered, &taskresult.Deliverable{Artifacts: []taskresult.Artifact{{
		Ref: "media://verification", LocalPath: "/private/runtime/screenshot.png",
	}}})
	if merged.Artifacts[0].LocalPath != "" || merged.Artifacts[0].Recovery == recovered.Artifacts[0].Recovery {
		t.Fatalf("artifact merge exposed a path or aliased authority: %#v", merged)
	}
}

func TestRetainedMediaRejectsUnboundOrUntrustedCapability(t *testing.T) {
	for _, mutate := range []func(*turnState, *toolshared.ToolResult){
		func(_ *turnState, r *toolshared.ToolResult) { r.Delivery.Outbound.Recovery.ActorID = "another-actor" },
		func(_ *turnState, r *toolshared.ToolResult) { r.Delivery.Outbound.Recovery.RouteID = "another-route" },
		func(_ *turnState, r *toolshared.ToolResult) {
			r.Delivery.Outbound.Recovery.WorkspaceID = "another-workspace"
		},
		func(_ *turnState, r *toolshared.ToolResult) {
			r.Delivery.Outbound.Recovery.MediaRef = "media://not-attached"
		},
		func(_ *turnState, r *toolshared.ToolResult) { r.Delivery.Outbound.Recovery = nil },
		func(_ *turnState, r *toolshared.ToolResult) { r.IsError = true },
		func(_ *turnState, r *toolshared.ToolResult) { r.Control.TaskSuspended = true },
		func(ts *turnState, _ *toolshared.ToolResult) { ts.opts.SuppressToolUserDelivery = false },
	} {
		ts, cfg := retainedMediaTurn(t, "telegram")
		result := retainedMediaResult(t, ts, cfg, "private")
		mutate(ts, result)
		if got := retainSuppressedMedia(ts, cfg, result); got != nil {
			t.Fatalf("unbound media was retained: %#v", got)
		}
	}
}

func TestRetainedMediaFinalPublicationClaimsAllArtifactsAndPreservesReport(t *testing.T) {
	for _, channel := range []string{"telegram", "discord"} {
		t.Run(channel, func(t *testing.T) {
			ts, cfg := retainedMediaTurn(t, channel)
			var deliverable *taskresult.Deliverable
			for _, suffix := range []string{"first", "second"} {
				capture := retainedMediaResult(t, ts, cfg, suffix)
				if suffix == "second" {
					capture.Delivery.Outbound.Recovery.Kind = bus.OutboundRecoveryBrowserDownload
				}
				deliverable = mergeDeliverables(
					deliverable,
					retainSuppressedMedia(ts, cfg, capture),
				)
			}
			// The parent is a different agent, with the original authenticated route.
			ts.agent, ts.agentID, ts.workspace = &AgentInstance{ID: "main"}, "main", cfg.WorkspacePath()
			source := &retainedMediaSource{}
			ts.runtimeCapabilities = runtimecap.NewContext(
				runtimecap.Inputs{Kind: runtimecap.KindGateway, Browser: source},
			)
			loop := &AgentLoop{cfg: cfg, bus: bus.NewMessageBus(), channelManager: &finalHandledReceiptManager{}}
			installTestOutboundCoordinator(t, loop, cfg.WorkspacePath())
			const report = "The upload did not save. Fresh verification found zero photos. The session is closed."
			result := (&toolshared.ToolResult{ForUser: report}).WithDeliverable(deliverable).
				WithDeliveryIntent(toolshared.DeliveryFinalHandled)
			ctx := withOutboundTransaction(t.Context(), "retained-final")
			if err := loop.ensureFinalHandledOutbound(ts, result); err != nil {
				t.Fatal(err)
			}
			if len(source.claims) != 0 || result.Delivery.Outbound.Text != report {
				t.Fatal("attachment was claimed before durable admission or report was replaced")
			}
			message := bus.OutboundMediaMessage{
				Channel: channel, ChatID: ts.chatID, Context: bus.NewOutboundContext(channel, ts.chatID, ""),
				Parts: result.Delivery.Outbound.Media, Recovery: result.Delivery.Outbound.Recovery,
			}
			publication, err := loop.publishTransactionMediaReceiptAtBoundary(
				ctx,
				cfg.WorkspacePath(),
				message,
				result.Delivery.Commit,
			)
			if err != nil || !publication.published || len(source.claims) != 2 {
				t.Fatalf("publication = %#v, %v; claims = %#v", publication, err, source.claims)
			}
			select {
			case delivered := <-loop.bus.(*bus.MessageBus).OutboundMediaChan():
				if len(delivered.Parts) != 2 || delivered.Parts[0].Caption != report ||
					delivered.Recovery == nil || len(delivered.Recovery.Additional) != 1 {
					t.Fatalf("incomplete final media: %#v", delivered)
				}
			default:
				t.Fatal("no durable media publication")
			}
		})
	}
}

func TestUnavailableRetainedMediaFallsBackToCompletePrimaryText(t *testing.T) {
	ts, cfg := retainedMediaTurn(t, "telegram")
	deliverable := retainSuppressedMedia(ts, cfg, retainedMediaResult(t, ts, cfg, "unavailable"))
	loop := &AgentLoop{cfg: cfg, bus: bus.NewMessageBus(), channelManager: &finalHandledReceiptManager{}}
	const report = "Three photos were saved. Verification succeeded. Session closed."
	loop.deliverFinalTurnResult(t.Context(), runtimeevents.NewTraceScope(ts.workspace, "final"), ts.agent,
		freezeTurnInput(turnSpec{SendResponse: true, Dispatch: ts.opts.Dispatch}),
		turnResult{finalContent: report, deliverable: deliverable})
	manager := loop.channelManager.(*finalHandledReceiptManager)
	if len(manager.sentMedia) != 0 || len(manager.sentMessages) != 1 || manager.sentMessages[0].Content != report {
		t.Fatalf("attachment failure erased or duplicated primary report: %#v", manager)
	}
	_, _, err := loop.deliverToolResultToUser(
		t.Context(),
		ts,
		(&toolshared.ToolResult{ForUser: report}).WithDeliverable(deliverable).
			WithDeliveryIntent(toolshared.DeliveryFinalHandled),
		"final_turn",
	)
	if err == nil || !channels.DeliveryDefinitelyNotSent(err) || errors.Is(err, errFinalHandledDeliveryAmbiguous) {
		t.Fatalf("unpublished capability failure was not classified safely: %v", err)
	}
}
