package agent

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestDocumentConversationCurrentRootKeepsAuthoredTextAndNativeExecutionFence(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &sequenceProvider{}, documentConversationHistoryConfig)
	loop, agent := fixture.Loop, fixture.Agent
	agent.Tools.Register(&documentIntakeContextProbe{})
	const text = "Я заёмщик Casey Mint; город Mintford. И сколько будет семь в квадрате?"
	opts := makeTestTurnSpec("ordinary-form-root")
	opts.Dispatch = testDispatchRequest("ordinary-form-root", "cli", "test-chat", text)
	opts.Dispatch.InboundContext.MessageID = "ordinary-input-one"
	opts.DocumentIntakeReference = intakeIngressTestReference()
	opts.DocumentIntakeSpoolID = "ordinary-input-one"
	pipeline := newTestPipeline(loop)
	for range 2 {
		ts := newTurnState(agent, opts, turnEventScope{turnID: "ordinary-root-attempt"})
		exec, err := pipeline.SetupTurn(t.Context(), ts)
		if err != nil {
			t.Fatal(err)
		}
		llm := newLLMIterationState(1)
		if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); err != nil {
			t.Fatal(err)
		}
		if len(llm.providerToolDefs) != 1 || llm.providerToolDefs[0].Function.Name != "document" {
			t.Fatal("readable ordinary input bypassed its exact native continuation fence")
		}
		seen := 0
		for _, message := range llm.callMessages {
			if message.Role == "user" && strings.Contains(message.Content, text) {
				seen++
			}
		}
		if seen != 1 {
			t.Fatal("current ordinary input was omitted or duplicated during replay")
		}
	}
	history := agent.Sessions.GetHistory("ordinary-form-root")
	if len(history) != 1 || history[0].Content != text || documentConversationID(history[0]) == "" {
		t.Fatal("ordinary form input did not become exactly one identity-bound canonical message")
	}
}

func TestDocumentConversationTaskReplyIsRetainedWhileOriginalRequestStaysPending(t *testing.T) {
	f := newTaskScopeInboundFixtureConfigured(t, documentConversationHistoryConfig)
	f.interpreter.action = document.FormTaskIndependent
	const text = "My temporary label is FIR_MINT_37. Please only acknowledge it."
	f.msg.Content, f.model.sideText = text, text
	active, found := activeInteractionForSession(
		f.loop.interactionRegistryForWorkspace(f.agent.Workspace), f.target.SessionKey,
	)
	if !found || !document.IsFormTaskRouteQuestionBinding(active.ProtectedAnswer) ||
		!interactionRouteAuthorizes(active.Route, f.target, f.msg.Context) {
		t.Fatal("fixture lost the authorized native task question")
	}
	// The pending request must actually be visible in ordinary history. Its
	// absence from model context is not evidence of an execution fence.
	history := f.agent.Sessions.GetHistory(f.target.SessionKey)
	original := providers.Message{
		Role: "user", Content: f.route.Text, RootTurnStart: true,
		TurnEnvelope: &providers.TurnEnvelope{Version: 1, DocumentConversationID: "second-pdf"},
	}
	if err := f.agent.Sessions.ReplaceTurnHistory(
		t.Context(), f.target.SessionKey, append([]providers.Message{original}, history...),
	); err != nil {
		t.Fatal(err)
	}
	coordinator := newInboundTurnCoordinator(f.loop)
	coordinator.handleInbound(t.Context(), f.msg)
	coordinator.wait()
	f.wait(t)
	view, err := f.store.ReadFormTaskRoute(t.Context(), f.route.Reference, f.owner)
	if err != nil || view.State != document.FormTaskQuestion || view.Text != f.route.Text ||
		view.GrantRevision != f.route.GrantRevision ||
		!f.model.sawOriginalFile.Load() || !f.model.sawSideText.Load() ||
		f.model.invalidPairing.Load() || f.model.duplicateSideInput.Load() {
		t.Fatalf("history/native boundary: state=%s err=%v original_visible=%v reply_visible=%v",
			view.State, err, f.model.sawOriginalFile.Load(), f.model.sawSideText.Load())
	}
	seen := 0
	for _, message := range f.agent.Sessions.GetHistory(f.target.SessionKey) {
		if message.Role == "user" && message.Content == text {
			seen++
			if documentConversationID(message) == "" {
				t.Fatal("ordinary task-question reply lost durable identity/provenance")
			}
		}
	}
	if seen != 1 {
		t.Fatal("ordinary reply was not retained exactly once alongside the independent draft")
	}
}

func conversationRequestHasCompletePairing(messages []providers.Message) bool {
	pending := map[string]bool{}
	for _, message := range messages {
		if len(pending) != 0 && message.Role != "tool" {
			return false
		}
		if message.Role == "tool" {
			if !pending[message.ToolCallID] {
				return false
			}
			delete(pending, message.ToolCallID)
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" || pending[call.ID] {
				return false
			}
			pending[call.ID] = true
		}
	}
	return len(pending) == 0
}

func TestDocumentConversationTaskReplySurvivesUnavailableNativeSinkWithoutGrant(t *testing.T) {
	f := newTaskScopeInboundFixtureConfigured(t, documentConversationHistoryConfig)
	const text = "Remember my arbitrary label OAK_MINT_926; only acknowledge."
	f.msg.Content = text
	f.loop.interactions.protectedSinks.Delete(document.FormProtectedAnswerNamespace)
	coordinator := newInboundTurnCoordinator(f.loop)
	coordinator.handleInbound(t.Context(), f.msg)
	coordinator.wait()
	f.wait(t)
	seen := 0
	for _, message := range f.agent.Sessions.GetHistory(f.target.SessionKey) {
		if message.Role == "user" && message.Content == text && documentConversationID(message) != "" {
			seen++
		}
	}
	view, err := f.store.ReadFormTaskRoute(t.Context(), f.route.Reference, f.owner)
	if seen != 1 || err != nil || view.State != document.FormTaskQuestion || view.Text != f.route.Text ||
		view.Answer != nil || view.GrantRevision != f.route.GrantRevision {
		t.Fatal("native sink failure erased ordinary text or granted/applied an unconfirmed answer")
	}
}

func TestDocumentConversationUnsupportedAttachmentStillKeepsOrdinaryHistory(t *testing.T) {
	f := newTaskScopeInboundFixtureConfigured(t, documentConversationHistoryConfig)
	// Narrow the existing interpreter interface: intake is configured but this
	// composition has no native attachment/task router. No new backend is added.
	f.agent.Tools.Register(tools.NewDocumentTool(
		tools.WithDocumentFormJobStore(f.store),
		tools.WithDocumentFormIntake(struct{ document.FormIntakeInterpreter }{f.interpreter}),
	))
	const text = "This is a different document. Keep my current draft."
	f.msg.Content = text
	f.msg.Media = []string{"media://new-uninterpreted-attachment"}
	coordinator := newInboundTurnCoordinator(f.loop)
	if !coordinator.handleDocumentIntakeInbound(t.Context(), f.msg, f.target) {
		t.Fatal("configured intake fell through without a native disposition")
	}
	coordinator.wait()
	seen := 0
	for _, message := range f.agent.Sessions.GetHistory(f.target.SessionKey) {
		if message.Role == "user" && message.Content == text && documentConversationID(message) != "" {
			seen++
			if len(message.Media) != 1 {
				t.Fatal("ordinary attachment reference was discarded")
			}
		}
	}
	view, err := f.store.ReadFormTaskRoute(t.Context(), f.route.Reference, f.owner)
	if seen != 1 || err != nil || view.State != document.FormTaskQuestion ||
		view.GrantRevision != f.route.GrantRevision {
		t.Fatal("unsupported attachment erased conversation or granted the existing pending request")
	}
}

func documentConversationHistoryConfig(cfg *config.Config) {
	cfg.Agents.Defaults.ContextManager = "seahorse"
}

func TestDocumentConversationMediaOnlyRootKeepsIdentityAndReferencesWithoutRelease(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &sequenceProvider{}, documentConversationHistoryConfig)
	agent, loop := fixture.Agent, fixture.Loop
	agent.Tools.Register(&documentIntakeContextProbe{})
	opts := makeTestTurnSpec("media-only-form-root")
	opts.Dispatch = testDispatchRequest("media-only-form-root", "cli", "test-chat", "")
	opts.Dispatch.Media = []string{"media://owned-new-attachment"}
	opts.Dispatch.InboundContext.MessageID = "media-only-form-input"
	opts.DocumentIntakeReference = intakeIngressTestReference()
	opts.DocumentIntakeSpoolID = "media-only-form-spool"
	pipeline := newTestPipeline(loop)
	for range 2 {
		ts := newTurnState(agent, opts, turnEventScope{turnID: "media-only-form-turn"})
		exec, err := pipeline.SetupTurn(t.Context(), ts)
		if err != nil {
			t.Fatal(err)
		}
		llm := newLLMIterationState(1)
		if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); err != nil {
			t.Fatal(err)
		}
		for _, message := range llm.callMessages {
			if len(message.Media) != 0 {
				t.Fatal("media-only input was released before native selection")
			}
		}
	}
	history := agent.Sessions.GetHistory("media-only-form-root")
	if len(history) != 1 || len(history[0].Media) != 1 || history[0].Content != "" ||
		documentConversationID(history[0]) == "" {
		t.Fatal("media-only replay lost canonical attachment provenance or created another input")
	}
}

func TestDocumentConversationSummaryOnlyContextKeepsDiagnosticBoundary(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &sequenceProvider{}, documentConversationHistoryConfig)
	agent, loop := fixture.Agent, fixture.Loop
	const sessionKey = "summary-form-conversation"
	const fact = "BORROWER_HISTORY_SENTINEL_824"
	message := providers.Message{
		Role: "user", Content: fact,
		TurnEnvelope: &providers.TurnEnvelope{Version: 1, DocumentConversationID: "summary-source"},
	}
	if err := persistFullSessionMessage(t.Context(), agent.Sessions, sessionKey, &message); err != nil {
		t.Fatal(err)
	}
	opts := makeTestTurnSpec(sessionKey)
	opts.Dispatch.UserMessage = "What did I say earlier?"
	ts := newTurnState(agent, opts, turnEventScope{turnID: "summary-followup"})
	pipeline := newTestPipeline(loop)
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	// Emulate ordinary context selection after compaction: the model sees a
	// derived summary, not the original marked canonical user message.
	exec.messages = []providers.Message{
		{Role: "system", Content: "Conversation summary: " + fact},
		{Role: "user", Content: opts.Dispatch.UserMessage},
	}
	llm := newLLMIterationState(1)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.callMessages[0].Content, fact) || !llm.protectedDiagnosticContext ||
		strings.Contains(ts.snapshot().UserMessage, opts.Dispatch.UserMessage) {
		t.Fatal("summary selection lost model context or source-derived diagnostic protection")
	}
	for _, message := range llm.diagnosticMessagesForAttempt() {
		if strings.Contains(message.Content, fact) {
			t.Fatal("summary-only form context escaped diagnostics")
		}
	}
}

func TestDocumentConversationQueuedInputPreservesHistoryWithoutEarlyMediaOrDuplicateProjection(t *testing.T) {
	fixture := newAgentLoopTestFixture(t, &sequenceProvider{}, documentConversationHistoryConfig)
	agent, loop := fixture.Agent, fixture.Loop
	agent.Tools.Register(&documentIntakeContextProbe{})
	opts := makeTestTurnSpec("queued-form-conversation")
	ts := newTurnState(agent, opts, turnEventScope{turnID: "queued-form-turn"})
	pipeline := newTestPipeline(loop)
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	const text = "Заёмщик Casey Mint; город Mintford. What is seven squared?"
	queued := providers.Message{
		Role: "user", Content: text, Media: []string{"media://new-attachment"},
		DocumentIntakeReference: intakeIngressTestReference(), InboundSpoolID: "queued-form-input",
		TurnEnvelope: &providers.TurnEnvelope{
			Version: 1, DocumentConversationID: "queued-form-input",
			Parts: []providers.TurnEnvelopePart{{ID: "context.runtime", Content: "frozen steering context"}},
		},
	}
	exec.pendingInputs.AppendSteering(queued)
	exec.pendingInputs.AppendSteering(queued)
	if _, err = pipeline.injectPendingTurnInputs(t.Context(), ts, exec, nil, 0); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, message := range agent.Sessions.GetHistory(ts.sessionKey) {
		if message.Role == "user" && message.Content == text {
			seen++
			if len(message.Media) != 1 || documentConversationID(message) != "queued-form-input" {
				t.Fatal("canonical steering lost authored media references or identity")
			}
		}
	}
	if seen != 1 {
		t.Fatal("duplicate transport appended ordinary steering more than once")
	}
	llm := newLLMIterationState(1)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); err != nil {
		t.Fatal(err)
	}
	seen = 0
	for _, message := range llm.callMessages {
		if message.Role == "user" && strings.Contains(message.Content, text) {
			seen++
			if len(message.Media) != 0 || strings.Count(message.Content, "frozen steering context") != 1 {
				t.Fatal("steering exposed media before grant or rendered frozen context twice")
			}
		}
	}
	if seen != 1 || len(llm.providerToolDefs) != 1 || llm.providerToolDefs[0].Function.Name != "document" {
		t.Fatal("queued ordinary text was duplicated or bypassed the native fence")
	}
}
