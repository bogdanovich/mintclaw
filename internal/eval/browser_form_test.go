package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type browserFormControl struct {
	*browserInputControl
	item      LoadedCase
	trial     Trial
	requestID string
	nextCall  string
	turn      int
}

// Native broker, interaction state and delivered messages, but a stub worker
// and trace. This is an offline contract test, not live qualification.
func newBrowserFormControl(t *testing.T, scenario string) *browserFormControl {
	t.Helper()
	c := browserInputControlWithLocalEdits(t, true)
	item := browserCorpusCase(t, scenario)
	var err error
	item.Case.Driver.Facts, err = materializePrivateFacts(item.Case.Driver.Facts)
	if err != nil {
		t.Fatal(err)
	}
	c.domain.item = item.Case
	c.domain.questions.facts = item.Case.Driver.Facts
	c.worker.form = true
	if err = os.Mkdir(filepath.Join(c.domain.questions.home, ownedGatewayTempDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	trial := newUnattemptedTrial(item, c.domain.questions.agent)
	trial.SessionID, trial.SessionKey = c.domain.questions.sessionID, c.record.Route.SessionKey
	trial.StartedAt = c.domain.questions.started
	trial.ExecutionVerdict, trial.ExecutionCode = Pass, "driver_finished"
	trial.TransportClosed, trial.Repetition = true, 1
	trial.Events = []Event{
		{
			ID:        uuid.NewString(),
			Kind:      "input",
			Turn:      1,
			RequestID: c.prompt.RequestID,
			Content:   "Fill the fixture form.",
		},
	}
	control := &browserFormControl{
		browserInputControl: c,
		item:                item,
		trial:               trial,
		requestID:           c.prompt.RequestID,
		turn:                1,
	}
	handle := "user-name"
	if scenario == "followup" {
		handle = "initial-name"
	}
	control.fill(t, c.record, c.prompt, handle)
	return control
}

func (c *browserFormControl) recordQuestion(
	t *testing.T,
	tool, description string,
	binding *interactions.ProtectedAnswerBinding,
	callID string,
	approval browser.Preparation,
) (interactions.Record, Event) {
	t.Helper()
	route := c.record.Route
	scope := events.NewTraceScope(c.domain.questions.workspace, "form-turn-"+uuid.NewString())
	request := interactions.CreateRequest{
		ID:    "interaction_" + uuid.NewString(),
		Kind:  interactions.KindQuestion,
		Route: route,
		Origin: interactions.Origin{
			ToolName: tool, ToolCallID: callID, ExecutionID: "input-execution", TurnID: scope.TurnID,
			ExecutionContext: &bus.InboundContext{
				MessageID: c.requestID, ClientSessionID: c.domain.questions.sessionID,
				Channel: route.Channel, ChatID: route.ChatID, ChatType: route.ChatType, SenderID: route.SenderID,
			},
		},
		Questions: []interactions.Question{
			{ID: "review", Question: description},
		},
		ProtectedAnswer: binding,
		ExpiresAt:       time.Now().Add(time.Minute),
	}
	if binding != nil {
		native, err := c.broker.InspectProtectedFillBinding(t.Context(), c.request.Owner, binding.Token)
		if err != nil {
			t.Fatal(err)
		}
		request.Questions[0].ID = "browser_value"
		request.ExpiresAt = time.Unix(0, native.ExpiresAt)
	}
	if tool == "browser_act" {
		request.Kind = interactions.KindApproval
		request.Questions = nil
		request.ApprovalAction = description
		var err error
		request.Origin.ArgumentHash, err = interactions.HashArguments(
			c.domain.questions.workspace,
			tools.BrowserActionApprovalArguments(approval),
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	record, err := c.registry.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	kind := string(request.Kind)
	intent, err := outbox.NewMessageIntent(c.domain.questions.workspace, outbox.Identity{
		SourceID: record.ID, Channel: route.Channel, ChatID: route.ChatID, SessionKey: route.SessionKey,
	}, bus.OutboundMessage{
		AgentID:     route.AgentID,
		SessionKey:  route.SessionKey,
		Content:     description,
		TraceScopes: []events.TraceScope{scope},
		Context:     bus.InboundContext{MessageID: c.requestID},
		Metadata: bus.OutboundMetadata{
			InteractionID: record.ID, InteractionShortID: record.ShortID,
			InteractionKind: kind, InteractionControls: bus.OutboundInteractionControlsPrompt,
		},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	box, err := outbox.Open(c.domain.questions.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = box.Create(intent); err != nil {
		t.Fatal(err)
	}
	if _, err = box.BeginAttempt(intent.ID); err != nil {
		t.Fatal(err)
	}
	messageID := uuid.NewString()
	if _, err = box.MarkDelivered(intent.ID, outbox.Outcome{PlatformMessageIDs: []string{messageID}}); err != nil {
		t.Fatal(err)
	}
	record, err = c.registry.BindPromptDelivery(record.ID, record.Revision, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, err = c.registry.MarkWaiting(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	prompt := Event{
		ID:                 uuid.NewString(),
		Kind:               kind,
		Turn:               c.turn,
		RequestID:          c.requestID,
		MessageID:          messageID,
		Content:            description,
		AgentID:            route.AgentID,
		SessionKey:         route.SessionKey,
		InteractionID:      record.ID,
		InteractionShortID: record.ShortID,
		InteractionKind:    kind,
		Controls:           "prompt",
		TraceScopes:        []events.TraceScope{scope},
	}
	return record, prompt
}

func (c *browserFormControl) nextFill(t *testing.T, field, handle string) {
	t.Helper()
	request := c.actionRequest(t, browser.ActionFill, field, "")
	token, err := c.broker.CreateProtectedFillBinding(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	native, err := c.broker.InspectProtectedFillBinding(t.Context(), request.Owner, token)
	if err != nil {
		t.Fatal(err)
	}
	call := c.nextCall
	record, prompt := c.recordQuestion(
		t,
		"browser_input",
		"What value should I enter?",
		&interactions.ProtectedAnswerBinding{
			Namespace: tools.BrowserProtectedAnswerNamespace,
			Token:     token,
		},
		call,
		browser.Preparation{},
	)
	data, err := json.Marshal(c.trace)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &c.trace); err != nil {
		t.Fatal(err)
	}
	c.trace.TraceID = "trace-" + uuid.NewString()
	c.trace.Metadata.RootTurnID = record.Origin.TurnID
	for index := range c.trace.Records {
		r := &c.trace.Records[index]
		r.Scope.TurnID = record.Origin.TurnID
		if r.Kind != diagnostictrace.RecordToolCall && r.Kind != diagnostictrace.RecordToolResult {
			continue
		}
		r.Correlation.ToolCallID = call
		var payload diagnostictrace.ToolPayload
		if err = json.Unmarshal(r.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if r.Kind == diagnostictrace.RecordToolResult {
			payload.BrowserInputTarget = &diagnostictrace.BrowserInputTargetPayload{
				InteractionID: record.ID,
				Target:        native.Observation,
			}
		}
		r.Data, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	c.writeTrace(t)
	c.fill(t, record, prompt, handle)
}

func (c *browserFormControl) fill(t *testing.T, record interactions.Record, prompt Event, handle string) {
	t.Helper()
	request, _, err := c.domain.request(t.Context(), Action{Kind: "protected_answer", Fact: handle}, prompt)
	if err != nil {
		t.Fatal("protected form request", handle, err)
	}
	prompt.ID, prompt.Turn, prompt.InteractionKind, prompt.Controls = uuid.NewString(), c.turn, "question", "prompt"
	prompt.Content = record.Questions[0].Question
	c.trial.Events = append(c.trial.Events, prompt)
	c.appendInput(request, "[protected answer supplied using an admitted fact handle]")
	prepared, err := c.broker.PrepareProtectedFill(
		t.Context(),
		c.request.Owner,
		record.ProtectedAnswer.Token,
		c.item.Case.Driver.Facts[handle],
	)
	if err != nil || prepared.RequiresApproval {
		t.Fatal("native form fill", err)
	}
	current, err := c.registry.ClaimProtectedAnswer(record.ID, record.Revision, interactions.Answer{
		MessageID: request.ID, ReceivedAt: time.Now().UnixMilli(), Protected: &interactions.ProtectedAnswerReceipt{
			Reference: "browser_fill." + prepared.Action.ID, State: "prepared",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err = c.registry.MarkResuming(current.ID, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.broker.ExecuteAction(t.Context(), c.request.Owner, prepared.Action.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = c.registry.Resolve(current.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
}

func (c *browserFormControl) appendInput(request protocol.MintClawMessage, text string) {
	c.turn++
	c.requestID = request.ID
	c.trial.Events = append(
		c.trial.Events,
		Event{ID: uuid.NewString(), Kind: "input", Turn: c.turn, RequestID: request.ID, Content: text},
	)
}

func (c *browserFormControl) actionRequest(
	t *testing.T,
	kind browser.ActionKind,
	target, value string,
) browser.PrepareActionRequest {
	t.Helper()
	observed, err := c.broker.Observe(t.Context(), c.request.Owner, c.request.SessionID, c.request.TabID)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(observed.Snapshot, "\n") {
		label := map[string]string{"name": "Name", "note": "Note", "color": "Color", "submit": "Submit form"}[target]
		if !strings.Contains(line, "\""+label+"\"") {
			continue
		}
		_, ref, found := strings.Cut(line, "[ref=")
		ref, _, closed := strings.Cut(ref, "]")
		if !found || !closed {
			t.Fatal("missing form reference")
		}
		c.nextCall = "call-" + uuid.NewString()
		return browser.PrepareActionRequest{
			Owner: c.request.Owner, RequestID: browser.OpaqueRequestID("input-execution", c.nextCall),
			SessionID: c.request.SessionID, TabID: c.request.TabID, SnapshotID: observed.SnapshotID,
			SnapshotGeneration: observed.SnapshotGeneration, Action: browser.Action{Kind: kind, Ref: ref, Value: value},
		}
	}
	t.Fatal("form target missing", target)
	return browser.PrepareActionRequest{}
}

func (c *browserFormControl) prepareReview(t *testing.T) (interactions.Record, Event) {
	t.Helper()
	c.nextFill(t, "note", "user-note")
	color := "blue"
	if c.item.Case.Browser.Scenario == "followup" {
		c.nextFill(t, "name", "corrected-name")
		color = "red"
	}
	prepared, err := c.broker.PrepareAction(t.Context(), c.actionRequest(t, browser.ActionSelect, "color", color))
	if err != nil || prepared.RequiresApproval {
		t.Fatal("color selection", err)
	}
	if _, err = c.broker.ExecuteAction(t.Context(), c.request.Owner, prepared.Action.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = c.broker.Observe(t.Context(), c.request.Owner, c.request.SessionID, c.request.TabID); err != nil {
		t.Fatal(err)
	}
	return c.recordQuestion(
		t,
		"request_user_input",
		"Review complete. Submit the requested form?",
		nil,
		"review-call",
		browser.Preparation{},
	)
}

func (c *browserFormControl) confirm(t *testing.T, record interactions.Record, prompt Event) {
	t.Helper()
	if err := c.domain.questions.observe(t.Context(), prompt); err != nil {
		t.Fatal("review question", err)
	}
	inputs, state, err := c.domain.readFormInputState()
	if err != nil {
		t.Fatal("review inputs", err)
	}
	if !c.domain.formStateReady(inputs, state, "") {
		t.Fatal("review native scope")
	}
	request, _, err := c.domain.request(t.Context(), Action{Kind: "confirm"}, prompt)
	if err != nil {
		t.Fatal("form confirmation", err)
	}
	c.trial.Events = append(c.trial.Events, prompt)
	c.appendInput(request, browserFormConfirmationText)
	current, err := c.registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		MessageID: request.ID, Text: browserFormConfirmationText,
		Values: map[string]string{record.Questions[0].ID: browserFormConfirmationText},
	}, interactions.OutcomeAnswered)
	if err != nil {
		t.Fatal(err)
	}
	current, err = c.registry.MarkResuming(current.ID, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.registry.Resolve(current.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
}

func (c *browserFormControl) submit(t *testing.T) {
	t.Helper()
	prepared, err := c.broker.PrepareAction(t.Context(), c.actionRequest(t, browser.ActionClick, "submit", ""))
	if err != nil || !prepared.RequiresApproval {
		t.Fatal("submit preparation", err)
	}
	record, prompt := c.recordQuestion(t, "browser_act", "Submit the reviewed form once", nil, c.nextCall, prepared)
	confirmation, confirmErr := c.domain.formConfirmation()
	if confirmErr != nil {
		t.Fatal("consumed confirmation", confirmErr)
	}
	inputs, state, inputErr := c.domain.readFormInputState()
	if inputErr != nil {
		t.Fatal("approval input read", inputErr)
	}
	if !c.domain.formStateReady(inputs, state, prepared.Action.ID) {
		t.Fatal("approval native scope")
	}
	if !c.domain.formApprovalMatches(record, prepared.Action, state) {
		session := state.Sessions[prepared.Action.SessionID]
		t.Fatal(
			"approval confirmation join",
			session.PageStateHash == confirmation.PageStateSHA256,
			browserFormInputSet(
				inputs,
			) == confirmation.InputSetSHA256,
			session.ControllerGeneration,
			confirmation.ControllerGeneration,
		)
	}
	if err = c.domain.observeApproval(t.Context(), prompt); err != nil {
		t.Fatal("form approval observation", err)
	}
	request, _, err := c.domain.request(t.Context(), Action{Kind: "allow_once"}, prompt)
	if err != nil {
		t.Fatal("form approval", err)
	}
	c.trial.Events = append(c.trial.Events, prompt)
	c.appendInput(request, "Allow the reviewed form once.")
	current, err := c.registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: request.ID, ResponseMessageID: prompt.MessageID,
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	current, err = c.registry.MarkResuming(current.ID, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	current, err = c.registry.ConsumeApproval(
		current.ID,
		current.Revision,
		current.Origin.ToolCallID,
		current.Origin.ToolName,
		current.Origin.ArgumentHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.broker.ExecuteAction(
		t.Context(),
		c.request.Owner,
		prepared.Action.ID,
		&prepared.Approval,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = c.registry.Resolve(current.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.domain.request(t.Context(), Action{Kind: "allow_once"}, prompt); err == nil {
		t.Fatal("submission approval replayed")
	}
}

func (c *browserFormControl) finalize(t *testing.T) (Trial, browserNativeSnapshot) {
	t.Helper()
	if err := c.broker.CloseOwner(context.Background(), c.request.Owner); err != nil {
		t.Fatal(err)
	}
	c.domain.fixture.seal()
	final := Event{
		ID: uuid.NewString(), Kind: "final", Turn: c.turn, RequestID: c.requestID, MessageID: uuid.NewString(),
		Content: "Submitted the requested fixture form once.", AgentID: c.trial.AgentID, SessionKey: c.trial.SessionKey,
		TraceScopes: []events.TraceScope{events.NewTraceScope(c.domain.questions.workspace, "form-final-turn")},
	}
	c.trial.Events = append(c.trial.Events, final)
	intent, err := newBrowserFinalIntent(c.domain.questions.workspace, c.trial, final)
	if err != nil {
		t.Fatal(err)
	}
	writeAdmissionJSON(t, filepath.Join(c.domain.questions.workspace, "state/outbox", intent.ID+".json"), intent)
	observed, err := observeBrowserNative(t.Context(), c.domain.questions.workspace, c.item.Case.Driver.Facts, nil)
	if err != nil {
		t.Fatal(err)
	}
	oracle, err := inspectBrowserFixture(c.item.Case, c.domain.fixture, c.trial.Events, nil)
	if err != nil {
		t.Fatal(err)
	}
	trial := c.trial
	trial.Browser = &BrowserTrialEvidence{
		Version: Version, Scope: "owned_fixture_execution_v1", Fixture: &oracle,
		NativeAuthority: "unavailable", NativeCleanup: "unavailable",
	}
	pending := browserPendingTrial{item: c.item.Case, fixture: c.domain.fixture, conversation: c.domain}
	if err = finalizeBrowserForm(
		&trial,
		pending,
		c.domain.target,
		observed,
		browserTemporaryRootsEmpty(c.domain.questions.home),
	); err != nil {
		t.Fatal("form finalization", err)
	}
	encoded, err := json.Marshal(trial)
	if err != nil || inspectNativeJSON(encoded, c.item.Case.Driver.Facts, nil) != nil {
		t.Fatal("private value reached trial")
	}
	return trial, observed
}

func TestBrowserFormNativeSubmitAndCorrection(t *testing.T) {
	for _, scenario := range []string{"submit", "followup"} {
		t.Run(scenario, func(t *testing.T) {
			c := newBrowserFormControl(t, scenario)
			record, prompt := c.prepareReview(t)
			c.confirm(t, record, prompt)
			c.submit(t)
			trial, _ := c.finalize(t)
			if !validBrowserFormEvidence(trial, c.item.Case, c.domain.questions.workspace) {
				t.Fatalf("native form rejected: %#v", trial.Browser.Form)
			}
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				FinishReason: "stop",
				Content: `{"version":1,"criteria":[{"id":"coherent","verdict":"pass","reason":"The requested form was submitted once.","evidence_ids":["` + lastOutcome(
					trial.Events,
				).ID + `"]}]}`,
			}}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			host := &BrowserFixtureHostEvidence{
				Scope: "owned_http_fixture_host_v1", Code: "closed", Trials: 1,
				OriginSHA256: digest([]byte(c.domain.fixture.origin)),
			}
			pending := browserRun{trials: map[string]browserPendingTrial{trial.ID: {conversation: c.domain}}}
			surfaces := observePrivateSurfaces(t.Context(), c.domain.questions.home, c.item.Case.Driver.Facts, nil, nil,
				pending.approvalKeyWitness(c.domain.questions.home))
			writer, err := newPrivateReport(
				filepath.Join(c.domain.questions.home, "reports"),
				c.domain.questions.home,
				c.domain.questions.workspace,
				uuid.NewString(),
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = writer.root.Close() })
			trial.Checks = gradeConversation(c.item.Case.Checks, trial.Events, lastOutcome(trial.Events))
			trial.Verdict, trial.Code = Inconclusive, "required_check_unknown"
			name := "trial-" + trial.ID + ".json"
			if err = writer.write("pending-"+name, trial); err != nil {
				t.Fatal(err)
			}
			pending.retain(c.item.Case, trial, c.domain.fixture)
			entry := pending.trials[trial.ID]
			entry.conversation = c.domain
			pending.trials[trial.ID] = entry
			report := RunReport{
				TargetLifecycle: cleanPDFLifecycle(), PrivateSurfaces: &surfaces, BrowserFixtures: host,
				Trials: []TrialSummary{{
					ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: 1,
					Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
				}},
			}
			if err = pending.finish(
				t.Context(),
				c.domain.target,
				writer,
				&report,
				c.item.Case.Driver.Facts,
				nil,
				func(item Case, trial *Trial) {
					assessFinalizedBrowser(
						t.Context(),
						judge,
						item,
						trial,
						c.domain.questions.workspace,
						report.TargetLifecycle,
						&surfaces,
						host,
					)
				},
			); err != nil {
				t.Fatal("pending form finalization", err)
			}
			trial = comparisonTrial(t, RunReport{ReportDir: writer.dir, Trials: report.Trials}, 0)
			if trial.Verdict != Pass || !trial.EvidenceEligible || provider.calls != 1 {
				t.Fatal("form did not reach isolated judge", trial.Code)
			}
		})
	}
}
