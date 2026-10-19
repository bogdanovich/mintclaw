package eval

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type browserInputWorker struct {
	*browserReadWorker
	values map[string]string
	form   bool
	title  string
}

func (worker *browserInputWorker) AuthorizeFill(ctx context.Context, expected, target string) error {
	current, err := worker.NavigationIdentity(ctx)
	if err != nil || current != expected || (target != "name" && target != "note") {
		return browser.ErrStale
	}
	return nil
}

func (worker *browserInputWorker) Execute(ctx context.Context, action browser.DriverAction) error {
	if worker.form && action.Kind == browser.DriverSelect && action.Target == "color" {
		worker.values["color"] = action.Value
		return nil
	}
	if worker.form && action.Kind == browser.DriverClick && action.Target == "submit" {
		data := url.Values{"nonce": {worker.page.nonce}}
		for _, field := range []string{"name", "color", "note"} {
			data.Set(field, worker.values[field])
		}
		request := httptest.NewRequest(
			http.MethodPost,
			worker.page.origin+worker.page.path+"submit",
			strings.NewReader(data.Encode()),
		)
		request.Header.Set("Origin", worker.page.origin)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		worker.page.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			return browser.ErrWorkerUnavailable
		}
		return nil
	}
	if action.Kind == browser.DriverFill && (action.Target == "name" || action.Target == "note") {
		if worker.values == nil {
			worker.values = map[string]string{}
		}
		worker.values[action.Target] = action.Value
		return nil
	}
	return worker.browserReadWorker.Execute(ctx, action)
}

func (worker *browserInputWorker) ExecuteAfterNavigationCheck(
	ctx context.Context, expected string, action browser.DriverAction,
) error {
	current, _ := worker.NavigationIdentity(ctx)
	if current != expected {
		return browser.ErrStale
	}
	return worker.Execute(ctx, action)
}

func (worker *browserInputWorker) Open(context.Context, browser.WorkerOpenRequest) (browser.WorkerOpenResult, error) {
	return browser.WorkerOpenResult{Owner: worker, Capabilities: browser.WorkerCapabilityActions}, nil
}

func (worker *browserInputWorker) Observe(context.Context) (browser.DriverObservation, error) {
	if worker.origin == "about:blank" {
		return browser.DriverObservation{URL: worker.origin, Origin: worker.origin}, nil
	}
	observation := browser.DriverObservation{
		URL: worker.page.origin + worker.page.path, Origin: worker.origin, Title: worker.title,
		Snapshot: "- textbox \"Name\" [ref=name]\n- textbox \"Note\" [ref=note]",
		Elements: []browser.DriverElement{
			{Target: "name", Role: "textbox", Name: "Name"},
			{Target: "note", Role: "textbox", Name: "Note"},
		},
	}
	if worker.form {
		observation.Snapshot += "\n- combobox \"Color\" [ref=color]\n- button \"Submit form\" [ref=submit]"
		observation.Elements = append(observation.Elements,
			browser.DriverElement{Target: "color", Role: "combobox", Name: "Color"},
			browser.DriverElement{Target: "submit", Role: "button", Name: "Submit form"})
	}
	return observation, nil
}

func (worker *browserInputWorker) Resolve(_ context.Context, target string) (browser.DriverElement, string, error) {
	var label string
	switch target {
	case "color":
		if worker.form {
			return browser.DriverElement{Target: target, Role: "combobox", Name: "Color"}, worker.origin, nil
		}
		return browser.DriverElement{}, "", browser.ErrNotFound
	case "submit":
		if worker.form {
			return browser.DriverElement{Target: target, Role: "button", Name: "Submit form"}, worker.origin, nil
		}
		return browser.DriverElement{}, "", browser.ErrNotFound
	case "name":
		label = "Name"
	case "note":
		label = "Note"
	default:
		return browser.DriverElement{}, "", browser.ErrNotFound
	}
	return browser.DriverElement{Target: target, Role: "textbox", Name: label}, worker.origin, nil
}

type browserInputControl struct {
	domain   *browserConversation
	registry *interactions.Registry
	record   interactions.Record
	prompt   Event
	trace    diagnostictrace.Trace
	store    string
	broker   *browser.Broker
	request  browser.PrepareActionRequest
	worker   *browserInputWorker
}

// Native broker, registry and delivered outbox, with a stub browser/trace. This
// tests admission contracts, not live model/browser or mutation qualification.
func newBrowserInputControl(t *testing.T) *browserInputControl {
	return browserInputControlWithLocalEdits(t, false)
}

func browserInputControlWithLocalEdits(t *testing.T, allow bool) *browserInputControl {
	t.Helper()
	f := newAdmissionFixture(t)
	enableAdmissionBrowser(t, f)
	if allow {
		cfg := f.cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
		profile := cfg.Profiles["eval-browser"]
		profile.Policy.Rules = append([]browserpolicy.Rule{{
			ID: "edit", Decision: browserpolicy.DecisionAllow,
			Match: browserpolicy.RuleMatch{Origins: profile.AllowedOrigins, Effects: []string{"local_edit"}},
		}}, profile.Policy.Rules...)
		cfg.Profiles["eval-browser"] = profile
		f.cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = cfg
	}
	f.cfg.Diagnostics.TraceCapture = evalModelMetadataCapture()
	target := admittedTarget{config: f.cfg, preset: f.preset, probe: f.probe}
	target.probe.GitCommit, target.probe.BuildVersion = nativeModelTestCommit, "native-test"
	sessionID := uuid.NewString()
	_, profile := browserEvalProfile(f.cfg)
	page, err := makeBrowserFixture(sessionID, profile.AllowedOrigins[0])
	if err != nil {
		t.Fatal(err)
	}
	domain := newBrowserConversation(testBrowserCase(), page, target, sessionID)
	store, err := browser.NewFileStore(filepath.Join(f.workspace, "state/browser/browser.json"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	worker := &browserInputWorker{browserReadWorker: &browserReadWorker{page: page, origin: "about:blank"}}
	broker, err := browser.NewBroker(f.cfg, store, worker)
	if err != nil {
		t.Fatal(err)
	}
	owner := browser.Owner{
		ActorID: browser.OpaqueActorID(
			"mintclaw:mintclaw-user",
		),
		AgentID:     browser.OpaqueAgentID(domain.questions.agent),
		SessionKey:  browser.OpaqueSessionKey("input-route"),
		ExecutionID: browser.OpaqueExecutionID("input-execution"),
	}
	t.Cleanup(func() { _ = broker.CloseOwner(context.Background(), owner) })
	session, err := broker.Open(t.Context(), browser.OpenRequest{
		Owner: owner, Target: config.BrowserDefaultTarget, Profile: "eval-browser",
	})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	navigation, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "input-navigation", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionNavigate, URL: page.origin + page.path},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = broker.ExecuteAction(t.Context(), owner, navigation.Action.ID, nil); err != nil {
		t.Fatal(err)
	}
	observed, err = broker.Observe(t.Context(), owner, session.ID, session.TabID)
	_, ref, found := strings.Cut(observed.Snapshot, "[ref=")
	ref, _, closed := strings.Cut(ref, "]")
	if err != nil || !found || !closed {
		t.Fatal("native input reference unavailable", err)
	}
	request := browser.PrepareActionRequest{
		Owner: owner, RequestID: browser.OpaqueRequestID("input-execution", "input-call"),
		SessionID: session.ID, TabID: session.TabID, SnapshotID: observed.SnapshotID,
		SnapshotGeneration: observed.SnapshotGeneration, Action: browser.Action{Kind: browser.ActionFill, Ref: ref},
	}
	token, err := broker.CreateProtectedFillBinding(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	native, err := broker.InspectProtectedFillBinding(t.Context(), owner, token)
	if err != nil {
		t.Fatal(err)
	}
	scope := events.NewTraceScope(f.workspace, "input-turn")
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(f.workspace))
	record, err := registry.Create(interactions.CreateRequest{
		ID:   "interaction_" + uuid.NewString(),
		Kind: interactions.KindQuestion,
		Route: interactions.Route{
			AgentID: domain.questions.agent, SessionKey: "input-route", RouteSessionKey: "input-route",
			Channel: "mintclaw", ChatID: "mintclaw:" + sessionID, ChatType: "direct", SenderID: "mintclaw-user",
		},
		Origin: interactions.Origin{
			ToolName: "browser_input", ToolCallID: "input-call", ExecutionID: "input-execution", TurnID: scope.TurnID,
			ExecutionContext: &bus.InboundContext{
				MessageID: "input-request", ClientSessionID: sessionID,
				Channel: "mintclaw", ChatID: "mintclaw:" + sessionID, ChatType: "direct", SenderID: "mintclaw-user",
			},
		},
		Questions: []interactions.Question{{ID: "browser_value", Question: "What name should I enter?"}},
		ProtectedAnswer: &interactions.ProtectedAnswerBinding{
			Namespace: tools.BrowserProtectedAnswerNamespace,
			Token:     token,
		},
		ExpiresAt: time.Unix(0, native.ExpiresAt),
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := outbox.NewMessageIntent(f.workspace, outbox.Identity{
		SourceID: "input-prompt", Channel: "mintclaw", ChatID: record.Route.ChatID, SessionKey: record.Route.SessionKey,
	}, bus.OutboundMessage{
		AgentID: record.Route.AgentID, SessionKey: record.Route.SessionKey, Content: record.Questions[0].Question,
		TraceScopes: []events.TraceScope{scope}, Metadata: bus.OutboundMetadata{
			InteractionID: record.ID, InteractionShortID: record.ShortID, InteractionKind: "question",
			InteractionControls: bus.OutboundInteractionControlsPrompt,
		},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	box, err := outbox.Open(f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = box.Create(intent); err != nil {
		t.Fatal(err)
	}
	if _, err = box.BeginAttempt(intent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = box.MarkDelivered(
		intent.ID,
		outbox.Outcome{PlatformMessageIDs: []string{"input-message"}},
	); err != nil {
		t.Fatal(err)
	}
	record, err = registry.BindPromptDelivery(record.ID, record.Revision, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkWaiting(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	control := &browserInputControl{
		domain: domain, registry: registry, record: record, broker: broker, request: request, worker: worker,
		store: filepath.Join(f.workspace, "state/diagnostics/traces"),
		prompt: Event{
			Kind: "question", MessageID: "input-message", RequestID: "input-request", AgentID: record.Route.AgentID,
			SessionKey: record.Route.SessionKey, InteractionID: record.ID, InteractionShortID: record.ShortID,
			TraceScopes: []events.TraceScope{scope},
		},
	}
	if err = os.MkdirAll(control.store, 0o700); err != nil {
		t.Fatal(err)
	}
	binding := nativeModelBinding{AgentID: record.Route.AgentID, SessionKey: record.Route.SessionKey, TraceScope: scope}
	trace := nativeModelTestTrace(t, binding)
	trace.TraceID = "trace-input-question"
	trace.Outcome.Status = "suspended"
	end := trace.Records[len(trace.Records)-1]
	end.Data, _ = json.Marshal(diagnostictrace.TurnPayload{Status: "suspended"})
	trace.Records = trace.Records[:len(trace.Records)-1]
	for _, kind := range []diagnostictrace.RecordKind{diagnostictrace.RecordToolCall, diagnostictrace.RecordToolResult} {
		payload := diagnostictrace.ToolPayload{Tool: "browser_input", Executed: true, Status: "started"}
		if kind == diagnostictrace.RecordToolCall {
			payload.ArgsHash = digest([]byte("value-free-ask"))
		} else {
			payload.Status = "completed"
			payload.BrowserInputTarget = &diagnostictrace.BrowserInputTargetPayload{
				InteractionID: record.ID, Target: native.Observation,
			}
		}
		data, _ := json.Marshal(payload)
		trace.Records = append(trace.Records, diagnostictrace.Record{
			Kind:   kind,
			Origin: diagnostictrace.Origin{Kind: "runtime_event", ID: string(kind)},
			Scope:  trace.Records[0].Scope,
			Data:   data,
			Correlation: diagnostictrace.Correlation{
				ToolCallID:         record.Origin.ToolCallID,
				ToolCallGeneration: "input-generation",
			},
		})
	}
	trace.Records = append(trace.Records, end)
	control.trace = trace
	control.writeTrace(t)
	return control
}

func (control *browserInputControl) writeTrace(t *testing.T) {
	t.Helper()
	for index := range control.trace.Records {
		control.trace.Records[index].Sequence = uint64(index + 1)
		control.trace.Records[index].OffsetNanos = int64(index)
	}
	final, err := diagnostictrace.Finalize(control.trace)
	if err != nil {
		t.Fatal(err)
	}
	control.trace = final
	writeNativeModelTestTrace(t, control.store, final)
}

func TestBrowserInputExpandsOnlyTheNativeFieldFact(t *testing.T) {
	control := newBrowserInputControl(t)
	domain := control.domain
	if err := domain.observe(t.Context(), control.prompt); err != nil || domain.input == nil {
		t.Fatal("delivered native field was not observed", err)
	}
	if matched, err := domain.questions.matchesPrompt(control.record, control.prompt); matched || err != nil {
		t.Fatal("protected field became an ordinary question", err)
	}
	for _, handle := range []string{"note", "missing"} {
		request, _, err := domain.request(t.Context(), Action{Kind: "protected_answer", Fact: handle}, control.prompt)
		if !errors.Is(err, errBrowserEvidence) || request.ID != "" {
			t.Fatal("wrong-field or invented fact was expanded", handle, err)
		}
	}
	request, handled, err := domain.request(t.Context(), Action{Kind: "protected_answer", Fact: "name"}, control.prompt)
	if err != nil || !handled || request.SessionID != domain.questions.sessionID ||
		request.Payload[protocol.PayloadKeyContent] != "/answer "+control.record.ShortID+" "+domain.item.Driver.Facts["name"] {
		t.Fatal("exact native input was refused", err)
	}
	if _, _, err = domain.request(
		t.Context(),
		Action{Kind: "protected_answer", Fact: "name"},
		control.prompt,
	); err == nil {
		t.Fatal("uncertain input send became replayable")
	}
	trial := Trial{ExecutionVerdict: Pass}
	domain.conclude(t.Context(), &trial)
	if trial.EvidenceEligible || trial.Browser == nil || trial.Browser.FormInputs == nil ||
		trial.Browser.FormInputs.Code == "native_inputs_observed" || trial.Browser.NativeAuthority == "observed" {
		t.Fatal("input admission invented mutation, cleanup or judge eligibility")
	}
	if state := domain.fixture.snapshot(); len(state.submissions) != 0 || len(state.removals) != 0 {
		t.Fatal("input admission mutated the fixture")
	}
}

func TestBrowserInputRejectsForeignOrStaleNativeAuthority(t *testing.T) {
	for _, mode := range []string{
		"message", "request", "agent", "session", "short-id", "scope", "namespace", "token", "execution", "call",
		"actor", "client-session", "question", "stale-snapshot", "closed-session", "source", "missing-trace", "outbox-scope",
	} {
		t.Run(mode, func(t *testing.T) {
			c := newBrowserInputControl(t)
			prompt, record := c.prompt, c.record
			switch mode {
			case "message":
				prompt.MessageID = "foreign-message"
			case "request":
				prompt.RequestID = "foreign-request"
			case "agent":
				prompt.AgentID = "foreign-agent"
			case "session":
				prompt.SessionKey = "foreign-session"
			case "short-id":
				prompt.InteractionShortID = "foreign"
			case "scope":
				prompt.TraceScopes = nil
			case "namespace":
				record.ProtectedAnswer.Namespace = "document.form.v1"
			case "token":
				record.ProtectedAnswer.Token += "forged"
			case "execution":
				record.Origin.ExecutionID = "foreign-execution"
			case "call":
				record.Origin.ToolCallID = "foreign-call"
			case "actor":
				record.Origin.RuntimeActorID = "foreign-actor"
			case "client-session":
				record.Origin.ExecutionContext.ClientSessionID = "foreign-client"
			case "question":
				record.Questions[0].ID = "different-field"
			case "stale-snapshot":
				_, err := c.broker.Observe(t.Context(), c.request.Owner, c.request.SessionID, c.request.TabID)
				if err != nil {
					t.Fatal(err)
				}
			case "closed-session":
				if err := c.broker.CloseOwner(t.Context(), c.request.Owner); err != nil {
					t.Fatal(err)
				}
			case "source":
				c.trace.Source.Commit = strings.Repeat("f", 40)
				c.writeTrace(t)
			case "missing-trace":
				if err := os.Remove(filepath.Join(c.store, c.trace.TraceID+".json")); err != nil {
					t.Fatal(err)
				}
			case "outbox-scope":
				name := filepath.Join(c.domain.questions.workspace, "state/outbox", record.PromptDeliveryID+".json")
				data, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				intent, err := outbox.InspectIntent(data, record.PromptDeliveryID)
				if err != nil {
					t.Fatal(err)
				}
				intent.Message.TraceScopes = []events.TraceScope{
					events.NewTraceScope(c.domain.questions.workspace, "other-turn"),
				}
				data, err = json.Marshal(intent)
				if err != nil || os.WriteFile(name, data, 0o600) != nil {
					t.Fatal("outbox trace control unavailable", err)
				}
			}
			if input, err := c.domain.readInput(t.Context(), record, prompt); err == nil || input != nil {
				t.Fatal("foreign/stale protected input admitted", mode)
			}
		})
	}
}

func TestBrowserInputRequiresUniqueCompleteNativeToolPair(t *testing.T) {
	for _, mode := range []string{
		"complete", "no-proof", "call-proof", "foreign-tool", "interaction", "generation", "foreign-turn",
		"error", "unexecuted", "preview", "duplicate", "unpaired", "completed-turn", "truncated", "fixture",
		"snapshot", "controller", "expired", "context", "future-issued", "profile",
	} {
		t.Run(mode, func(t *testing.T) {
			c := newBrowserInputControl(t)
			i := len(c.trace.Records) - 2
			var payload diagnostictrace.ToolPayload
			if err := json.Unmarshal(c.trace.Records[i].Data, &payload); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "no-proof":
				payload.BrowserInputTarget = nil
			case "call-proof":
				c.trace.Records[i-1].Data, _ = json.Marshal(payload)
			case "foreign-tool":
				payload.Tool = "request_user_input"
			case "interaction":
				payload.BrowserInputTarget.InteractionID = "foreign-interaction"
			case "generation":
				c.trace.Records[i].Correlation.ToolCallGeneration = "other-generation"
			case "foreign-turn":
				c.trace.Records[i].Scope.TurnID = "other-turn"
			case "error":
				payload.IsError = true
			case "unexecuted":
				payload.Executed = false
			case "preview":
				payload.ArgumentsPreview = "model text is not authority"
			case "completed-turn":
				c.trace.Outcome.Status = "completed"
				c.trace.Records[i+1].Data, _ = json.Marshal(diagnostictrace.TurnPayload{Status: "completed"})
			case "truncated":
				c.trace.Truncation.DroppedRecords = 1
			case "fixture":
				payload.BrowserInputTarget.Target.URLSHA256 = digest([]byte(c.domain.fixture.origin + "/other/"))
			case "snapshot":
				payload.BrowserInputTarget.Target.SnapshotGeneration++
			case "controller":
				payload.BrowserInputTarget.Target.ControllerGeneration++
			case "expired":
				payload.BrowserInputTarget.Target.ExpiresAt = time.Now().Add(-time.Minute).UnixNano()
			case "context":
				payload.BrowserInputTarget.Target.ContextCatalogID = "foreign-catalog"
				payload.BrowserInputTarget.Target.ContextGeneration = 1
			case "future-issued":
				payload.BrowserInputTarget.Target.IssuedAt = time.Now().Add(time.Minute).UnixNano()
			case "profile":
				target := c.domain.target.config.Tools.Browser.Targets[config.BrowserDefaultTarget]
				profile := target.Profiles["eval-browser"]
				profile.Revision = "changed-profile"
				target.Profiles["eval-browser"] = profile
				c.domain.target.config.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
			}
			c.trace.Records[i].Data, _ = json.Marshal(payload)
			switch mode {
			case "duplicate":
				end := c.trace.Records[i+1]
				duplicate := c.trace.Records[i]
				duplicate.Origin.ID += "-duplicate"
				c.trace.Records = append(c.trace.Records[:i+1], duplicate, end)
			case "unpaired":
				c.trace.Records = append(c.trace.Records[:i-1], c.trace.Records[i:]...)
			}
			c.writeTrace(t)
			input, err := c.domain.readInput(t.Context(), c.record, c.prompt)
			if (mode == "complete") != (err == nil && input != nil) {
				t.Fatal("native input proof mismatch", mode, err)
			}
		})
	}
}

func TestBrowserInputScansRawEvidenceBeforeProjection(t *testing.T) {
	for _, surface := range []string{"registry", "outbox", "trace-after-invalid", "browser"} {
		t.Run(surface, func(t *testing.T) {
			c := newBrowserInputControl(t)
			private := c.domain.item.Driver.Facts["name"]
			name := ""
			switch surface {
			case "registry":
				name = interactions.WorkspaceStorePath(c.domain.questions.workspace)
			case "outbox":
				name = filepath.Join(c.domain.questions.workspace, "state/outbox", c.record.PromptDeliveryID+".json")
			case "trace-after-invalid":
				if err := os.WriteFile(
					filepath.Join(c.store, "aaa-invalid.json"),
					[]byte(`{"invalid":true}`),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
				name = filepath.Join(c.store, c.trace.TraceID+".json")
			case "browser":
				name = filepath.Join(c.domain.questions.workspace, "state/browser/browser.json")
			}
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			data = append([]byte(`{"unknown_private":"`+private+`",`), data[1:]...)
			if err = os.WriteFile(name, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err = c.domain.observe(t.Context(), c.prompt); !errors.Is(err, ErrPrivateEvidence) {
				t.Fatal("original private bytes disappeared through decoding or earlier uncertainty", surface, err)
			}
			if len(c.domain.inputAttempts) != 0 {
				t.Fatal("private evidence failure emitted an answer")
			}
		})
	}
}

func TestBrowserInputCannotExpandGeneratedOrWrongFieldValues(t *testing.T) {
	for _, mode := range []string{"placeholder", "wrong-label", "wrong-role", "ordinary-message", "final"} {
		t.Run(mode, func(t *testing.T) {
			c := newBrowserInputControl(t)
			action := Action{Kind: "protected_answer", Fact: "name"}
			switch mode {
			case "placeholder":
				c.domain.item.Driver.Facts["name"] = generatePrivateFact
			case "wrong-label", "wrong-role":
				i := len(c.trace.Records) - 2
				var payload diagnostictrace.ToolPayload
				_ = json.Unmarshal(c.trace.Records[i].Data, &payload)
				if mode == "wrong-label" {
					payload.BrowserInputTarget.Target.ElementNameSHA256 = digest([]byte("Note"))
				} else {
					payload.BrowserInputTarget.Target.ElementRole = "combobox"
				}
				c.trace.Records[i].Data, _ = json.Marshal(payload)
				c.writeTrace(t)
			case "ordinary-message":
				action = Action{Kind: "message", Text: "put my private name here"}
			case "final":
				c.prompt.Kind = "final"
			}
			request, _, err := c.domain.request(t.Context(), action, c.prompt)
			if err == nil || request.ID != "" {
				t.Fatal("private input escaped the exact protected field boundary", mode)
			}
		})
	}
}

func TestBrowserInputCorrectionUsesExplicitFieldHandles(t *testing.T) {
	for _, handle := range []string{"initial", "name"} {
		t.Run(handle, func(t *testing.T) {
			c := newBrowserInputControl(t)
			c.domain.item.Driver.Facts["initial"] = "initial-private-canary"
			c.domain.item.Browser.Fields[0].InputFacts = []string{"initial", "name"}
			request, _, err := c.domain.request(t.Context(), Action{Kind: "protected_answer", Fact: handle}, c.prompt)
			if err != nil || request.Payload[protocol.PayloadKeyContent] !=
				"/answer "+c.record.ShortID+" "+c.domain.item.Driver.Facts[handle] {
				t.Fatal("explicit initial/corrected handle was not admitted to the same semantic field", err)
			}
		})
	}
}

func TestBrowserInputRechecksNativeWaitingStateBeforeAnswer(t *testing.T) {
	c := newBrowserInputControl(t)
	if err := c.domain.observe(t.Context(), c.prompt); err != nil {
		t.Fatal(err)
	}
	if _, err := c.registry.BeginCancellation(c.record.ID, c.record.Revision, "session_control_stop"); err != nil {
		t.Fatal(err)
	}
	request, _, err := c.domain.request(t.Context(), Action{Kind: "protected_answer", Fact: "name"}, c.prompt)
	if err == nil || request.ID != "" || len(c.domain.inputAttempts) != 0 {
		t.Fatal("cached native target was used after its question stopped waiting", err)
	}
}
