package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type taskScopeInboundProvider struct {
	simpleConvProvider
	sawOriginalFile atomic.Bool
}

func (provider *taskScopeInboundProvider) Chat(
	ctx context.Context, messages []providers.Message, definitions []providers.ToolDefinition,
	model string, options map[string]any,
) (*providers.LLMResponse, error) {
	for index := len(messages) - 1; index >= 0; index-- {
		text := messages[index].Content
		if strings.Contains(text, "/private/synthetic-second.pdf") {
			provider.sawOriginalFile.Store(true)
		}
		if !strings.Contains(text, "<runtime_protected_answer_continuation>") {
			continue
		}
		start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
		var args map[string]any
		if start < 0 || end <= start || json.Unmarshal([]byte(text[start:end+1]), &args) != nil {
			return nil, errors.New("missing protected continuation arguments")
		}
		if len(definitions) == 1 && definitions[0].Function.Name == "document" {
			return &providers.LLMResponse{FinishReason: "tool_calls", ToolCalls: []providers.ToolCall{{
				ID: "task-scope-continuation", Name: "document", Arguments: args,
			}}}, nil
		}
		break
	}
	return provider.simpleConvProvider.Chat(ctx, messages, definitions, model, options)
}

type taskScopeInboundInterpreter struct{ documentFormAuditor }

func (*taskScopeInboundInterpreter) InterpretFormTaskRoute(
	_ context.Context, _ string, view document.FormTaskRouteSnapshot,
) (document.FormTaskRouteInterpretation, error) {
	text := view.Text
	if view.Answer != nil {
		text = view.Answer.Text
	}
	return document.FormTaskRouteInterpretation{
		Action:   document.FormTaskSwitch,
		Evidence: document.FormIntakeLiteral{Text: strings.TrimSpace(text), Occurrence: 1},
	}, nil
}

type taskScopeInboundFixture struct {
	loop   *AgentLoop
	agent  *AgentInstance
	bus    *interactionOwnershipBus
	store  *document.FormJobStore
	owner  document.FormJobOwner
	route  document.FormTaskRouteSnapshot
	record interactions.Record
	msg    bus.InboundMessage
	target *inboundDispatchTarget
	model  *taskScopeInboundProvider
}

func (f taskScopeInboundFixture) wait(t *testing.T, completions ...int) {
	t.Helper()
	want := 1
	if len(completions) != 0 {
		want = completions[0]
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		acked, released := f.bus.counts()
		_, resuming := f.loop.loadInteractionResumeFlight(f.agent.Workspace, f.record.ID)
		if acked+released >= want && !resuming &&
			f.loop.turns.activeTurnState(f.target.runtimeSessionScope()) == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("task-scope ingress worker did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func newTaskScopeInboundFixture(t *testing.T, waiting ...bool) taskScopeInboundFixture {
	t.Helper()
	model := &taskScopeInboundProvider{}
	fixture := newAgentLoopTestFixture(t, model)
	loop, agent := fixture.Loop, fixture.Agent
	installInteractionChannelManager(t, loop, newInteractionChannelManager())
	tracking := &interactionOwnershipBus{MessageBus: fixture.Bus}
	setTestMessageBus(loop, tracking)
	msg := finalResponseAdmissionInboundMessage("scope-free-answer")
	msg.Content = "Start a new task with the second file; preserve the draft."
	target, ok := loop.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("missing test route")
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
	t.Cleanup(store.Close)
	interactionRoute := interactions.Route{
		AgentID: agent.ID, SessionKey: target.SessionKey, RouteSessionKey: target.Allocation.RouteScopeKey,
		Channel: msg.Context.Channel, ChatID: msg.Context.ChatID, SenderID: msg.Context.SenderID,
		AccountID: msg.Context.Account, ChatType: msg.Context.ChatType, TopicID: msg.Context.TopicID,
		SpaceID: msg.Context.SpaceID, SpaceType: msg.Context.SpaceType,
	}
	owner, err := document.FormJobOwnerFromProtectedRoute(agent.Workspace, interactionRoute)
	if err != nil {
		t.Fatal(err)
	}
	backend := document.Capabilities().Operations["fields"].Primary
	if backend == nil {
		t.Skip("field-schema backend unavailable")
	}
	schema := document.FormFieldsFacts{
		SourceSHA256: strings.Repeat("a", 64), Backend: *backend,
		Limits: document.FormFieldLimits{
			MaxFields: document.DefaultMaxFormFields, MaxWidgets: document.DefaultMaxFieldWidgets,
			MaxOptions: document.DefaultMaxFieldOptions, MaxTextBytes: document.DefaultMaxFieldTextBytes,
			MaxReportBytes: document.DefaultMaxFormReportBytes,
		},
		Fields: []document.FormField{{
			ID: "field_" + strings.Repeat("b", 64), Name: "Name", Kind: document.FormFieldText,
			Widgets: []document.FormFieldWidget{{ID: "widget_" + strings.Repeat("c", 64), Page: 1, Ordinal: 1}},
		}},
	}
	digest, err := document.FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	backendRevision, err := document.FormFieldsBackendRevision(schema)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Create(t.Context(), document.FormJobCreateRequest{
		Owner: owner, StartIdempotencyKey: "scope-fixture", SourceRef: "media://original-form",
		SourceDigest: schema.SourceSHA256, FieldSchemaDigest: digest, BackendRevision: backendRevision,
		AuditPolicyRevision: "scope-test-policy",
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
			Question:          "Provide known facts",
			CandidateFieldIDs: []string{schema.Fields[0].ID},
		},
		IdempotencyKey: "scope-mode",
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.BeginFormTaskRoute(t.Context(), document.FormTaskRouteRequest{
		JobID: job.JobID, ModeID: mode.Progress.ModeID, ExpectedRevision: job.Revision, Owner: owner,
		MessageIdentity: "second-pdf", Text: `"/private/synthetic-second.pdf"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.QuestionFormTaskRoute(t.Context(), route.Reference, route.Revision, owner); err != nil {
		t.Fatal(err)
	}
	binding, err := store.NewFormTaskRouteQuestion(t.Context(), route.Reference, owner)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := document.NewFormProtectedAnswerSink(store)
	if err != nil {
		t.Fatal(err)
	}
	if err = loop.interactions.registerProtectedAnswerSink(sink); err != nil {
		t.Fatal(err)
	}
	interpreter := &taskScopeInboundInterpreter{}
	tool := tools.NewDocumentTool(
		tools.WithDocumentFormJobStore(store),
		tools.WithDocumentFormIntake(interpreter),
		tools.WithDocumentFormAudit(
			document.FormAuditPolicy{PrimaryModel: "scope-test", PrimaryIdentity: "test:scope"},
			interpreter,
		),
	)
	mediaStore := media.NewFileMediaStore()
	t.Cleanup(mediaStore.Stop)
	tool.SetMediaStore(mediaStore)
	agent.Tools.Register(tool)
	origin := interactions.Origin{TurnID: "scope-turn", ToolCallID: "scope-call", ToolName: "document"}
	agent.Sessions.AddFullMessage(
		target.SessionKey,
		providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: origin.ToolCallID, Name: origin.ToolName, Arguments: map[string]any{},
		}}},
	)
	registry := loop.interactionRegistryForWorkspace(agent.Workspace)
	record, err := registry.Create(interactions.CreateRequest{
		Kind: interactions.KindQuestion, Route: interactionRoute, Origin: origin, ProtectedAnswer: &binding,
		Questions: []interactions.Question{{ID: "arbitrary_ui_id", Question: "New task or previous form?"}},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) == 0 || waiting[0] {
		record = markTestInteractionWaiting(t, registry, record)
	}
	return taskScopeInboundFixture{loop, agent, tracking, store, owner, route, record, msg, target, model}
}

func TestDocumentTaskScopeBareAnswerWaitsForPromptAndPersistsItsIdentity(t *testing.T) {
	f := newTaskScopeInboundFixture(t, false)
	coordinator := newInboundTurnCoordinator(f.loop)
	coordinator.handleInbound(t.Context(), f.msg)
	coordinator.wait()
	f.wait(t)
	f.bus.mu.Lock()
	persisted := append([]bus.InboundMessage(nil), f.bus.persisted...)
	released := append([]string(nil), f.bus.released...)
	f.bus.mu.Unlock()
	if len(persisted) != 1 || persisted[0].Context.Interaction.ShortID != f.record.ShortID ||
		!containsExactly(released, f.msg.SpoolID) {
		t.Fatal("early free reply was not bound and retained for the same undelivered prompt")
	}
	work, err := f.store.FormTaskRouteWork(t.Context(), f.route.JobID, f.owner)
	if err != nil || len(work) != 1 || work[0] != f.route.Reference {
		t.Fatal("undelivered prompt created a second root or accepted the answer early")
	}
	registry := f.loop.interactionRegistryForWorkspace(f.agent.Workspace)
	markTestInteractionWaiting(t, registry, f.record)
	coordinator.handleInbound(t.Context(), persisted[0])
	coordinator.wait()
	f.wait(t, 2)
	view, err := f.store.ReadFormTaskRoute(t.Context(), f.route.Reference, f.owner)
	if err != nil || view.State != document.FormTaskGranted {
		t.Fatal("delivery retry did not finish the exact original question")
	}
}

func TestDocumentTaskScopeAnswerPersistenceFailureDoesNotAdmitOrAccept(t *testing.T) {
	f := newTaskScopeInboundFixture(t)
	failure := errors.New("synthetic persistence failure")
	tracking := &finalResponseAdmissionTestBus{MessageBus: f.bus.MessageBus, persistErr: failure}
	setTestMessageBus(f.loop, tracking)
	coordinator := newInboundTurnCoordinator(f.loop)
	coordinator.handleInbound(t.Context(), f.msg)
	coordinator.wait()
	tracking.mu.Lock()
	released, cause := append([]string(nil), tracking.released...), tracking.releaseCause
	tracking.mu.Unlock()
	work, err := f.store.FormTaskRouteWork(t.Context(), f.route.JobID, f.owner)
	if !errors.Is(cause, failure) || !containsExactly(released, f.msg.SpoolID) ||
		err != nil || len(work) != 1 || work[0] != f.route.Reference {
		t.Fatal("persistence failure accepted or reinterpreted an unbound answer")
	}
}

func TestDocumentTaskScopeBareAnswerDoesNotOverrideOtherIngressAuthority(t *testing.T) {
	for _, scenario := range []string{"admitted-root", "attachment", "command", "wrong-sender", "projected-answer"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTaskScopeInboundFixture(t)
			switch scenario {
			case "admitted-root":
				f.msg.Context.DocumentIntake.Reference = f.route.Reference
			case "attachment":
				f.msg.Media = []string{"media://new-attachment"}
			case "command":
				f.msg.Content = "/cancel_form"
			case "wrong-sender":
				f.msg.Context.SenderID = "different-user"
			case "projected-answer":
				f.msg.Context.Interaction = bus.InboundInteractionProjection{
					ShortID:  "other-id",
					Response: "other-answer",
				}
			}
			if newInboundTurnCoordinator(f.loop).routeDocumentTaskScopeAnswer(t.Context(), f.msg, f.target) {
				t.Fatal("bare reply routing overrode an existing binding, attachment, control or owner")
			}
			f.bus.mu.Lock()
			persisted := len(f.bus.persisted)
			f.bus.mu.Unlock()
			if persisted != 0 {
				t.Fatal("non-bare reply received a task-question binding")
			}
		})
	}
}

func TestDocumentTaskScopeBareAnswerCannotRefreshStaleNativeConsent(t *testing.T) {
	f := newTaskScopeInboundFixture(t)
	progress, err := f.store.FormIntakeForJob(t.Context(), f.route.JobID, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []document.FormIntakeState{document.FormIntakePaused, document.FormIntakeActive} {
		updated, updateErr := f.store.SetFormIntakeState(
			t.Context(), f.route.JobID, progress.ModeID, progress.Revision, f.owner, state,
		)
		if updateErr != nil {
			t.Fatal(updateErr)
		}
		progress = &updated
	}
	coordinator := newInboundTurnCoordinator(f.loop)
	coordinator.handleInbound(t.Context(), f.msg)
	coordinator.wait()
	f.wait(t)
	view, err := f.store.ReadFormTaskRoute(t.Context(), f.route.Reference, f.owner)
	if err != nil || view.State != document.FormTaskQuestion {
		t.Fatal("free text refreshed stale native consent")
	}
	work, err := f.store.FormTaskRouteWork(t.Context(), f.route.JobID, f.owner)
	if err != nil || len(work) != 1 || work[0] != f.route.Reference {
		t.Fatal("stale question released text or created another root")
	}
}

func TestDocumentTaskScopeBareAnswerKeepsOriginalFileAndDoesNotBecomeANewRoot(t *testing.T) {
	f := newTaskScopeInboundFixture(t)
	coordinator := newInboundTurnCoordinator(f.loop)
	coordinator.handleInbound(t.Context(), f.msg)
	coordinator.wait()
	f.wait(t)
	view, err := f.store.ReadFormTaskRoute(t.Context(), f.route.Reference, f.owner)
	if err != nil || view.State != document.FormTaskGranted {
		t.Fatalf("bare reply did not complete the original task: state=%s err=%v", view.State, err)
	}
	progress, err := f.store.FormIntakeForJob(t.Context(), view.JobID, f.owner)
	if err != nil || progress.State != document.FormIntakeSuspended || progress.PendingTasks != 0 {
		t.Fatalf("answer became a separate root or lost the old draft: %#v %v", progress, err)
	}
	f.bus.mu.Lock()
	persisted := append([]bus.InboundMessage(nil), f.bus.persisted...)
	f.bus.mu.Unlock()
	if len(persisted) == 0 || persisted[0].Context.Interaction.ShortID != f.record.ShortID ||
		persisted[0].Context.Interaction.Response != f.msg.Content || !persisted[0].Context.DocumentIntake.IsZero() {
		t.Fatal("bare answer was not durably bound before execution")
	}
	history := f.agent.Sessions.GetHistory(f.target.SessionKey)
	if !f.model.sawOriginalFile.Load() {
		t.Fatal("the authorized handoff lost the original second-file selector")
	}
	// An exact persisted delivery replay must not become a new root or release
	// the second file again, even though the original form is now suspended.
	coordinator.handleInbound(t.Context(), persisted[0])
	coordinator.wait()
	f.wait(t)
	after := f.agent.Sessions.GetHistory(f.target.SessionKey)
	if len(after) != len(history) {
		t.Fatal("persisted reply replay started another turn")
	}
	grant, err := f.store.GrantFormTaskHandoff(t.Context(), view.Reference, f.owner)
	if err != nil || !grant.Reused || grant.Text != "" || len(grant.Media) != 0 {
		t.Fatal("replay released the authorized request again")
	}
}
