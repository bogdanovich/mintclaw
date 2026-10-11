package agent

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestGatewayDocumentIntakeCompositionKeepsNormalChatAndUnavailableStateSeparate(t *testing.T) {
	if !documentToolAvailable() {
		t.Skip("document runtime is unavailable on this platform")
	}
	for _, model := range []string{"", "unconfigured-audit-alias", "test-model"} {
		t.Run("audit="+model, func(t *testing.T) {
			t.Setenv(config.EnvHome, t.TempDir())
			provider := &countingAdmissionProvider{}
			fixture := newAgentLoopTestFixture(t, provider, func(cfg *config.Config) {
				cfg.Tools.Document.Enabled = true
				cfg.Tools.Document.AuditModel = model
				cfg.ModelList = []*config.ModelConfig{{
					ModelName: "test-model", Provider: "openai", Model: "test-model", Enabled: true,
				}}
			})
			registered, exists := fixture.Agent.Tools.GetRegistered("document")
			tool, typed := registered.(*tools.DocumentTool)
			if !exists || !typed || !tool.ProtectedFormIntakeConfigured() {
				t.Fatal("production composition did not retain protected intake ingress")
			}
			properties := tool.Parameters()["properties"].(map[string]any)
			if properties["candidate_field_ids"] == nil {
				t.Fatal("production document surface omitted ordinary-message intake")
			}
			tracking := &finalResponseAdmissionTestBus{MessageBus: fixture.Bus}
			setTestMessageBus(fixture.Loop, tracking)
			msg := finalResponseAdmissionInboundMessage("healthy-normal-chat")
			target, routed := fixture.Loop.resolveSteeringTarget(msg)
			if !routed {
				t.Fatal("normal chat route was unavailable")
			}
			coordinator := newInboundTurnCoordinator(fixture.Loop)
			if coordinator.handleDocumentIntakeInbound(t.Context(), msg, target) {
				t.Fatal("healthy absent intake intercepted normal chat")
			}
			sink, exists := fixture.Loop.interactions.protectedAnswerSink(document.FormProtectedAnswerNamespace)
			protected, typed := sink.(*document.FormProtectedAnswerSink)
			if !exists || !typed {
				t.Fatal("production composition did not reuse its protected form store")
			}
			protected.FormJobStore().Close()
			msg.SpoolID = "unavailable-protected-state"
			const private = "MINTCLAW_PRIVATE_COMPOSITION_42ad"
			msg.Content = private
			if !coordinator.handleDocumentIntakeInbound(t.Context(), msg, target) {
				t.Fatal("unreadable protected state fell through into normal chat")
			}
			if provider.calls != 0 || len(fixture.Agent.Sessions.GetHistory(target.SessionKey)) != 0 {
				t.Fatal("ingress lookup exposed data to ordinary model/history")
			}
			select {
			case notice := <-tracking.OutboundChan():
				if strings.Contains(notice.Content, private) ||
					!strings.Contains(notice.Content, "state could not be read") {
					t.Fatal("fail-closed composition omitted its notice or echoed private input")
				}
			default:
				t.Fatal("unavailable state silently dropped the operator's message")
			}
		})
	}
}

func TestGatewayDisabledDocumentDoesNotInstallIntakeOrSharedState(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	fixture := newAgentLoopTestFixture(t, &countingAdmissionProvider{})
	if fixture.Agent.Tools.HasRegistered("document") {
		t.Fatal("disabled document tool was installed")
	}
	if _, exists := fixture.Loop.interactions.protectedAnswerSink(document.FormProtectedAnswerNamespace); exists {
		t.Fatal("disabled document tool opened protected form state")
	}
	// Coding/read-only runtimes use isolated composition; this gateway switch
	// must not grant them form or grouped-intake parameters.
	for _, option := range []tools.DocumentToolOption{
		tools.WithDocumentReadOnlySurface(), tools.WithDocumentLocalWriteSurface(),
	} {
		tool := tools.NewDocumentTool(option)
		properties := tool.Parameters()["properties"].(map[string]any)
		if tool.ProtectedFormIntakeConfigured() || properties["candidate_field_ids"] != nil {
			t.Fatal("non-gateway document surface gained protected intake")
		}
	}
}
