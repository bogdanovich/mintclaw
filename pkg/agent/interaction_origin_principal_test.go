package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type originPrincipalProbe struct {
	principal runtimecap.Principal
	resolved  bool
	cleaned   bool
}

func (*originPrincipalProbe) Name() string               { return "origin_principal_probe" }
func (*originPrincipalProbe) Description() string        { return "inspect trusted lifecycle context" }
func (*originPrincipalProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*originPrincipalProbe) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	return toolshared.SilentResult("unused")
}

func (probe *originPrincipalProbe) capture(ctx context.Context) error {
	runtime, found := toolshared.RuntimeCapabilities(ctx)
	principal, bound := runtime.Principal()
	if !found || !bound {
		return errors.New("lifecycle context has no trusted runtime principal")
	}
	probe.principal = principal
	return nil
}

func (probe *originPrincipalProbe) ResolveLiveResourceHandoff(
	ctx context.Context, _ toolshared.LiveResourceHandoff, _ toolshared.LiveResourceHandoffDisposition,
) error {
	if err := probe.capture(ctx); err != nil {
		return err
	}
	probe.resolved = true
	return nil
}

func (probe *originPrincipalProbe) CleanupTurn(ctx context.Context) error {
	if err := probe.capture(ctx); err != nil {
		return err
	}
	probe.cleaned = true
	return nil
}

func TestDurableInteractionLifecycleRestoresOriginalRuntimePrincipal(t *testing.T) {
	loop, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer loop.Close()
	probe := &originPrincipalProbe{}
	loop.RegisterTool(probe)
	agent := loop.GetRegistry().GetDefaultAgent()
	record := interactions.Record{
		Route: interactions.Route{SessionKey: "display-session", RouteSessionKey: "original-routed-session"},
		Origin: interactions.Origin{ExecutionID: "original-execution", ExecutionContext: &bus.InboundContext{
			Channel: "Telegram", ActorID: "telegram:linked-user", SenderID: "transport-sender",
		}},
		Outcome: interactions.OutcomeAnswered,
		OutcomeReceipts: []taskresult.Receipt{{
			Kind: taskresult.ObjectiveKindLiveHandoff, Tool: probe.Name(),
			Metadata: map[string]string{"resource_kind": "fixture_resource", "resource_id": "original-resource"},
		}},
	}
	// An answer/cleanup caller may have no bound turn, or belong to a different
	// turn. Neither is authority to substitute the original resource owner.
	for _, ambient := range []bool{false, true} {
		ctx := t.Context()
		if ambient {
			ctx = toolshared.WithRuntimeCapabilities(ctx, runtimecap.NewContext(runtimecap.Inputs{
				Kind: runtimecap.KindGateway,
			}).BindPrincipal(runtimecap.Principal{
				Runtime: runtimecap.KindGateway, ActorID: "telegram:other", AgentID: "other",
				SessionID: "other-session", ExecutionID: "other-execution",
			}))
		}
		probe.resolved, probe.cleaned = false, false
		if _, err := loop.resolveDurableLiveHandoffs(ctx, agent, record); err != nil {
			t.Fatal(err)
		}
		want := runtimecap.Principal{
			Runtime: runtimecap.KindGateway, ActorID: "telegram:linked-user",
			AgentID: agent.ID, SessionID: "original-routed-session", ExecutionID: "original-execution",
		}
		if !probe.resolved || probe.principal != want {
			t.Fatal("handoff did not preserve the full original owner")
		}
		loop.cleanupInteractionOriginTools(ctx, agent, record)
		if !probe.cleaned || probe.principal != want {
			t.Fatal("terminal cleanup did not preserve the full original owner")
		}
	}
	updated := make(chan struct{})
	go func() {
		defer close(updated)
		for range 20 {
			loop.SetBrowserCapabilityClient(&agentCapabilityBrowser{available: true})
		}
	}()
	for range 20 {
		if _, err := loop.resolveDurableLiveHandoffs(t.Context(), agent, record); err != nil {
			t.Error(err)
		}
		loop.cleanupInteractionOriginTools(t.Context(), agent, record)
	}
	<-updated
}

func TestInteractionOriginTypedCapabilitiesPreserveRuntimeAndRejectIncompleteOwners(t *testing.T) {
	agent := &AgentInstance{ID: "Main Agent", Workspace: t.TempDir()}
	for _, kind := range []runtimecap.Kind{runtimecap.KindGateway, runtimecap.KindCoding} {
		t.Run(string(kind), func(t *testing.T) {
			browser := &agentCapabilityBrowser{available: true}
			capabilities := runtimecap.NewContext(runtimecap.Inputs{Kind: kind, Browser: browser})
			record := interactions.Record{
				Route: interactions.Route{SessionKey: "original-session"},
				Origin: interactions.Origin{
					ExecutionID: "original-execution", RuntimeActorID: "coding-owner",
					ExecutionContext: &bus.InboundContext{Channel: "Telegram", SenderID: "linked-user"},
				},
			}
			for _, missing := range []string{"", "actor", "session"} {
				origin := record
				inbound := *record.Origin.ExecutionContext
				origin.Origin.ExecutionContext = &inbound
				if missing == "actor" {
					origin.Origin.RuntimeActorID = ""
					inbound.SenderID = ""
				}
				if missing == "session" {
					origin.Route.SessionKey = ""
				}
				ambient := runtimecap.Principal{
					Runtime: kind, ActorID: "ambient", AgentID: "ambient",
					SessionID: "ambient", ExecutionID: "ambient",
				}
				ctx, cancel, err := interactionOriginToolContext(
					toolshared.WithRuntimeCapabilities(t.Context(), capabilities.BindPrincipal(ambient)),
					agent, origin, capabilities,
				)
				if err != nil {
					t.Fatal(err)
				}
				bound, found := toolshared.RuntimeCapabilities(ctx)
				principal, complete := bound.Principal()
				client, available := bound.BrowserClient()
				cancel()
				if !found || bound.Kind() != kind || !available || client != browser {
					t.Fatal("origin reconstruction discarded runtime-generation service capabilities")
				}
				if missing != "" {
					if complete {
						t.Fatal("incomplete origin inherited ambient authority", missing)
					}
					continue
				}
				actor := "coding-owner"
				if kind == runtimecap.KindGateway {
					actor = "telegram:linked-user"
				}
				want := runtimecap.Principal{
					Runtime: kind, ActorID: actor, AgentID: "main-agent",
					SessionID: "original-session", ExecutionID: "original-execution",
				}
				if !complete || principal != want {
					t.Fatal("typed lifecycle owner differs from its origin")
				}
			}
		})
	}
}
