package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestDocumentIntakeCoordinatorControlsTheSameNativeModeAcrossRestart(t *testing.T) {
	provider := &countingAdmissionProvider{}
	loop, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	trackingBus := &finalResponseAdmissionTestBus{MessageBus: loop.bus.(*bus.MessageBus)}
	setTestMessageBus(loop, trackingBus)
	msg := finalResponseAdmissionInboundMessage("intake-pause")
	target, ok := loop.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("test message was not routed")
	}
	root := t.TempDir()
	options := document.FormJobStoreOptions{
		StateRoot: filepath.Join(root, "state"),
		KeyRoot:   filepath.Join(root, "keys"),
	}
	for _, path := range []string{options.StateRoot, options.KeyRoot} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	owner := document.FormJobOwner{
		AgentID: agent.ID, WorkspaceID: agent.Workspace,
		RouteSessionKey: firstNonEmptyString(target.Allocation.RouteScopeKey, target.SessionKey),
		Channel:         msg.Context.Channel, AccountID: msg.Context.Account, ChatID: msg.Context.ChatID,
		ChatType: msg.Context.ChatType, SenderID: msg.Context.SenderID, TopicID: msg.Context.TopicID,
		SpaceID: msg.Context.SpaceID, SpaceType: msg.Context.SpaceType,
	}
	fieldsBackend := document.Capabilities().Operations["fields"].Primary
	if fieldsBackend == nil {
		t.Fatal("the fixture has no admitted field-schema backend")
	}
	schema := document.FormFieldsFacts{
		SourceSHA256: strings.Repeat("a", 64),
		Backend:      *fieldsBackend,
		Limits: document.FormFieldLimits{
			MaxFields: document.DefaultMaxFormFields, MaxWidgets: document.DefaultMaxFieldWidgets,
			MaxOptions: document.DefaultMaxFieldOptions, MaxTextBytes: document.DefaultMaxFieldTextBytes,
			MaxReportBytes: document.DefaultMaxFormReportBytes,
		},
		Fields: []document.FormField{{
			ID: "field_" + strings.Repeat("b", 64), Name: "Synthetic identity", Kind: document.FormFieldText,
			Widgets: []document.FormFieldWidget{{ID: "widget_" + strings.Repeat("c", 64), Page: 1, Ordinal: 1}},
		}},
	}
	digest, err := document.FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := document.FormFieldsBackendRevision(schema)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Create(t.Context(), document.FormJobCreateRequest{
		Owner:               owner,
		StartIdempotencyKey: "coordinator-control-fixture",
		SourceRef:           "media://synthetic-control-fixture",
		SourceDigest:        schema.SourceSHA256,
		FieldSchemaDigest:   digest,
		BackendRevision:     backend,
		AuditPolicyRevision: "test-audit-policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	mode, err := store.StartFormIntake(t.Context(), document.FormIntakeStartRequest{
		JobID:            job.JobID,
		ExpectedRevision: job.Revision,
		Owner:            owner,
		Schema:           schema,
		Context: document.FormIntakeContext{
			CandidateFieldIDs: []string{schema.Fields[0].ID},
			Question:          "Provide known facts",
		},
		IdempotencyKey: "same-native-mode",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.Tools.Register(tools.NewDocumentTool(
		tools.WithDocumentFormJobStore(store), tools.WithDocumentFormIntake(&documentFormAuditor{}),
	))
	msg.Content = "/pause_form"
	coordinator := newInboundTurnCoordinator(loop)
	coordinator.handleInbound(t.Context(), msg)
	coordinator.wait()
	store.Close()
	store, err = document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	progress, err := store.FindFormIntake(t.Context(), owner)
	if err != nil || progress == nil || progress.State != document.FormIntakePaused ||
		progress.ModeID != mode.Progress.ModeID {
		t.Fatal("coordinator pause did not preserve the native mode across restart")
	}
	agent.Tools.Register(tools.NewDocumentTool(
		tools.WithDocumentFormJobStore(store), tools.WithDocumentFormIntake(&documentFormAuditor{}),
	))
	msg.SpoolID, msg.Content = "intake-resume", "/resume_form"
	coordinator.handleInbound(t.Context(), msg)
	coordinator.wait()
	progress, err = store.FindFormIntake(t.Context(), owner)
	if err != nil || progress == nil || progress.State != document.FormIntakeActive ||
		progress.ModeID != mode.Progress.ModeID {
		t.Fatal("coordinator resume replaced or failed to resume the native mode")
	}
	if provider.calls != 0 || len(agent.Sessions.GetHistory(target.SessionKey)) != 0 {
		t.Fatal("native controls entered ordinary history or provider execution")
	}
	acked, released, _ := trackingBus.ownership()
	if len(acked) != 2 || len(released) != 0 {
		t.Fatal("control messages did not settle transport")
	}
	// A retry must bypass input admission even when the native pending queue is
	// full. Hold the route so we can inspect its existing steering boundary
	// without invoking a model or replacing the fixture's immutable source.
	var first string
	for index := 0; index < document.MaxFormIntakePendingInputs; index++ {
		admitted, admitErr := store.AdmitFormIntakeMessage(t.Context(), document.FormIntakeMessageRequest{
			JobID: job.JobID, ModeID: progress.ModeID, ExpectedRevision: progress.Revision,
			Owner: owner, Schema: schema, MessageIdentity: fmt.Sprintf("pending-retry-%d", index),
			Text: "PRIVATE_RETRY_SENTINEL",
		})
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		if index == 0 {
			first = admitted.Receipt
		}
	}
	claim, _, claimed := coordinator.claimSession(target)
	if !claimed {
		t.Fatal("test route could not be held")
	}
	defer claim.releaseIfOwned()
	msg.SpoolID, msg.Content = "intake-retry", "/retry_form"
	coordinator.handleInbound(t.Context(), msg)
	coordinator.wait()
	progress, err = store.FindFormIntake(t.Context(), owner)
	if err != nil || progress == nil || progress.PendingInputs != document.MaxFormIntakePendingInputs ||
		progress.InputCount != document.MaxFormIntakePendingInputs || progress.ModeID != mode.Progress.ModeID {
		t.Fatal("retry admitted a new message or changed the same native mode")
	}
	if loop.pendingSteeringCountForScope(target.runtimeSessionScope()) != 1 || provider.calls != 0 {
		t.Fatal("typed retry did not enter the existing protected steering boundary")
	}
	queued := loop.dequeueSteeringMessagesForScope(target.runtimeSessionScope())
	if len(queued) != 1 || queued[0].DocumentIntakeReference != first ||
		queued[0].InboundSpoolID != msg.SpoolID || queued[0].Content == "/retry_form" ||
		strings.Contains(queued[0].Content, "PRIVATE_") {
		t.Fatal("typed retry lost its bound receipt or exposed command/private input")
	}
}

func intakeIngressTestReference() string {
	return "form_intake.form_job_" + strings.Repeat("a", 32) + ".form_input_" + strings.Repeat("b", 32)
}

func TestDocumentIntakeTurnAndSteeringRearmTheExistingProtectedFence(t *testing.T) {
	loop, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	agent.Tools.RegisterHidden(tools.NewDocumentTool())
	reference := intakeIngressTestReference()
	opts := makeTestTurnSpec("intake-transport")
	opts.DocumentIntakeReference = reference
	ts := newTurnState(
		agent,
		opts,
		turnEventScope{turnID: "intake-transport-turn", context: newTurnContext(nil, nil, nil)},
	)
	pipeline := newTestPipeline(loop)
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	llm := newLLMIterationState(1)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); err != nil {
		t.Fatal(err)
	}
	if len(llm.providerToolDefs) != 1 || llm.providerToolDefs[0].Function.Name != "document" {
		t.Fatal("protected intake did not restore its deferred document tool")
	}
	if !exec.protectedAnswerContinuation.pending() ||
		exec.protectedAnswerContinuation.arguments["answer_ref"] != reference {
		t.Fatal("trusted receipt did not install the mechanical document continuation")
	}
	exec.protectedAnswerContinuation.complete()
	agent.Tools.TickTTL()
	steered := strings.Replace(reference, strings.Repeat("b", 32), strings.Repeat("c", 32), 1)
	message := providers.Message{
		Role:                    "user",
		Content:                 "Protected form input is ready.",
		DocumentIntakeReference: steered,
	}
	encoded, err := json.Marshal(message)
	if err != nil || strings.Contains(string(encoded), steered) {
		t.Fatal("private receipt metadata entered durable/provider JSON")
	}
	exec.pendingInputs.AppendSteering(message)
	if _, err = pipeline.injectPendingTurnInputs(t.Context(), ts, exec, nil, 0); err != nil {
		t.Fatal(err)
	}
	if !exec.protectedAnswerContinuation.pending() ||
		exec.protectedAnswerContinuation.arguments["answer_ref"] != steered {
		t.Fatal("busy-turn receipt was treated as unrestricted steering")
	}
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, newLLMIterationState(2)); err != nil {
		t.Fatal(err)
	}
	state := exec.protectedAnswerContinuation
	if state.accept(&providers.LLMResponse{Content: "Please repeat your name"}) {
		t.Fatal("receipt processing could be replaced by a prose re-prompt")
	}
	if !state.accept(&providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		ID: "continue-intake", Name: "document",
		Arguments: map[string]any{"action": "form", "form_action": "continue", "answer_ref": steered},
	}}}) {
		t.Fatal("exact document continuation was refused")
	}
	initial := makeTestTurnSpec("intake-transport")
	initial.InitialSteeringMessages = []providers.Message{message}
	recovered := newProtectedAnswerContinuationState(freezeTurnInput(initial), agent.Tools)
	if !recovered.pending() || recovered.arguments["answer_ref"] != steered {
		t.Fatal("returned steering lost protected ownership")
	}
}

func TestDocumentIntakeGuardNeverFallsBackWhenItsToolIsUnavailable(t *testing.T) {
	provider := &countingAdmissionProvider{}
	loop, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	trackingBus := &finalResponseAdmissionTestBus{MessageBus: loop.bus.(*bus.MessageBus)}
	setTestMessageBus(loop, trackingBus)
	msg := finalResponseAdmissionInboundMessage("intake-guarded")
	const private = "MINTCLAW_PRIVATE_GUARDED_REPLAY_c0f8"
	msg.Content = private
	msg.Context.DocumentIntake = bus.InboundDocumentIntakeGuard{
		JobID: "form_job_" + strings.Repeat("a", 32), ModeID: "form_intake_" + strings.Repeat("b", 32),
	}
	target, ok := loop.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("test message was not routed")
	}
	coordinator := newInboundTurnCoordinator(loop)
	coordinator.handleInbound(t.Context(), msg)
	coordinator.wait()
	if provider.calls != 0 || len(agent.Sessions.GetHistory(target.SessionKey)) != 0 {
		t.Fatal("guarded replay entered ordinary history or model")
	}
	select {
	case outbound := <-trackingBus.OutboundChan():
		if strings.Contains(outbound.Content, private) || !strings.Contains(outbound.Content, "Protected form") {
			t.Fatal("guard refusal echoed data or provided no notice")
		}
	default:
		t.Fatal("guarded replay was silently dropped")
	}
	acked, released, _ := trackingBus.ownership()
	if len(acked) != 1 || acked[0] != msg.SpoolID || len(released) != 0 {
		t.Fatal("safe refusal did not settle transport")
	}
}
