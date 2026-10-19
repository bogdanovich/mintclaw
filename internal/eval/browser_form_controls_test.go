package eval

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func cloneBrowserFormControlValue[T any](t *testing.T, value T) T {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBrowserFormConfirmationCannotInventOrChangeNativeAnswer(t *testing.T) {
	c := newBrowserFormControl(t, "submit")
	record, prompt := c.prepareReview(t)
	c.confirm(t, record, prompt)
	resolved, err := c.domain.questions.record(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = projectBrowserFormConfirmation(c.domain.review, resolved, time.Now()); err != nil {
		t.Fatal("positive confirmation", err)
	}
	for _, mode := range []string{
		"waiting", "id", "short", "origin", "route", "question", "expiry", "created", "revision",
		"missing-answer", "message", "response", "text", "values", "media", "choice", "superseded", "protected", "late", "future", "unresolved",
	} {
		t.Run(mode, func(t *testing.T) {
			current := cloneBrowserFormControlValue(t, resolved)
			switch mode {
			case "waiting":
				current.Status = interactions.StatusWaiting
			case "id":
				current.ID = "foreign"
			case "short":
				current.ShortID = "foreign"
			case "origin":
				current.Origin.ExecutionID = "foreign"
			case "route":
				current.Route.SenderID = "foreign"
			case "question":
				current.Questions = nil
			case "expiry":
				current.ExpiresAt++
			case "created":
				current.CreatedAt++
			case "revision":
				current.Revision = c.domain.review.record.Revision
			case "missing-answer":
				current.Answer = nil
			case "message":
				current.Answer.MessageID = "foreign"
			case "response":
				current.Answer.ResponseMessageID = "foreign"
			case "text":
				current.Answer.Text = "Yes, remove the account."
			case "values":
				current.Answer.Values = map[string]string{"foreign": "yes"}
			case "media":
				current.Answer.Media = []string{"foreign"}
			case "choice":
				current.Answer.Choice = "allow_once"
			case "superseded":
				current.Answer.Superseded = true
			case "protected":
				current.Answer.Protected = &interactions.ProtectedAnswerReceipt{Reference: "foreign", State: "prepared"}
			case "late":
				current.Answer.ReceivedAt = current.ExpiresAt
			case "future":
				current.UpdatedAt = time.Now().Add(time.Hour).UnixMilli()
			case "unresolved":
				current.ResolvedAt = 0
			}
			if _, err := projectBrowserFormConfirmation(c.domain.review, current, time.Now()); err == nil {
				t.Fatal("changed confirmation admitted", mode)
			}
		})
	}
	resuming := cloneBrowserFormControlValue(t, resolved)
	resuming.Status, resuming.ResolvedAt = interactions.StatusResuming, 0
	if _, err = projectBrowserFormConfirmation(c.domain.review, resuming, time.Now()); err != nil {
		t.Fatal("legitimate pending continuation refused", err)
	}
	legacy := cloneBrowserFormControlValue(t, resolved)
	legacy.Answer.Values = nil
	if _, err = projectBrowserFormConfirmation(c.domain.review, legacy, time.Now()); err != nil {
		t.Fatal("legacy text-only native confirmation refused", err)
	}
	for _, values := range []map[string]string{
		{record.Questions[0].ID: "different answer"},
		{record.Questions[0].ID: browserFormConfirmationText, "foreign": browserFormConfirmationText},
	} {
		changed := cloneBrowserFormControlValue(t, resolved)
		changed.Answer.Values = values
		if _, err = projectBrowserFormConfirmation(c.domain.review, changed, time.Now()); err == nil {
			t.Fatal("mismatched native answer values admitted")
		}
	}
}

func TestBrowserFormReviewRefusesOtherEffectsAndLostContext(t *testing.T) {
	c := newBrowserFormControl(t, "followup")
	c.prepareReview(t)
	inputs, state, err := c.domain.readFormInputState()
	if err != nil || !c.domain.formStateReady(inputs, state, "") {
		t.Fatal("positive review", err)
	}
	sessionID := inputs.Fills[0].Target.SessionID
	for _, mode := range []string{
		"session", "tab", "execution", "controller", "frame", "origin", "expired", "unknown",
		"foreign-action", "wrong-origin", "wrong-color", "unused-action", "missing-correction", "missing-note",
	} {
		t.Run(mode, func(t *testing.T) {
			native := cloneBrowserFormControlValue(t, state)
			proof := cloneBrowserFormControlValue(t, inputs)
			session := native.Sessions[sessionID]
			switch mode {
			case "session":
				delete(native.Sessions, sessionID)
			case "tab":
				session.TabID = "foreign"
			case "execution":
				session.Owner.ExecutionID = "foreign"
			case "controller":
				session.ControllerGeneration++
			case "frame":
				session.FrameID = "foreign"
			case "origin":
				session.SnapshotOrigin = "https://foreign.invalid"
			case "expired":
				session.ExpiresAt = time.Now().Add(-time.Second).UnixNano()
			case "unknown":
				for id, inv := range native.Invocations {
					inv.State = browser.InvocationUnknown
					native.Invocations[id] = inv
					break
				}
			case "foreign-action", "wrong-origin", "wrong-color":
				for id, action := range native.PreparedActions {
					if action.Action.Kind != browser.ActionSelect {
						continue
					}
					if mode == "foreign-action" {
						action.Owner.ExecutionID = "foreign"
					}
					if mode == "wrong-origin" {
						action.CurrentOrigin = "https://foreign.invalid"
					}
					if mode == "wrong-color" {
						action.ElementName = "Foreign color"
					}
					native.PreparedActions[id] = action
				}
			case "unused-action":
				for _, action := range native.PreparedActions {
					action.ID = "prepared_unused"
					native.PreparedActions[action.ID] = action
					break
				}
			case "missing-correction":
				proof.Fills = proof.Fills[:2]
			case "missing-note":
				proof.Fills = append(proof.Fills[:1], proof.Fills[2:]...)
			}
			if mode != "session" {
				native.Sessions[sessionID] = session
			}
			if c.domain.formStateReady(proof, native, "") {
				t.Fatal("foreign or incomplete review admitted", mode)
			}
		})
	}
}

func TestBrowserFormHardEvidenceDominatesPositiveJudge(t *testing.T) {
	c := newBrowserFormControl(t, "followup")
	record, prompt := c.prepareReview(t)
	c.confirm(t, record, prompt)
	c.submit(t)
	complete, observed := c.finalize(t)
	if !validBrowserFormEvidence(complete, c.item.Case, c.domain.questions.workspace) {
		t.Fatal("positive form unavailable")
	}
	for _, mode := range []string{
		"controller", "tab", "execution", "frame", "unknown", "missing-final", "foreign-submit", "wrong-origin", "duplicate-invocation", "temporary",
		"prompt-content", "prompt-request", "prompt-scope", "missing-prompt",
	} {
		t.Run("native/"+mode, func(t *testing.T) {
			native := observed
			native.state = cloneBrowserFormControlValue(t, observed.state)
			native.intents = cloneBrowserFormControlValue(t, observed.intents)
			form := complete.Browser.Form
			action := native.state.PreparedActions[form.SubmissionActionID]
			session := native.state.Sessions[action.SessionID]
			empty := true
			switch mode {
			case "controller":
				session.ControllerGeneration++
			case "tab":
				session.TabID = "foreign"
			case "execution":
				session.Owner.ExecutionID = "foreign"
			case "frame":
				session.FrameID = "foreign"
			case "unknown":
				inv := native.state.Invocations[form.SubmissionInvocationID]
				inv.State = browser.InvocationUnknown
				native.state.Invocations[inv.ID] = inv
			case "missing-final":
				native.intents = nil
			case "foreign-submit":
				action.ElementName = "Remove account"
			case "wrong-origin":
				action.CurrentOrigin = "https://foreign.invalid"
			case "duplicate-invocation":
				inv := native.state.Invocations[form.SubmissionInvocationID]
				inv.ID = "invocation_duplicate"
				native.state.Invocations[inv.ID] = inv
			case "temporary":
				empty = false
			case "prompt-content", "prompt-request", "prompt-scope", "missing-prompt":
				for index, intent := range native.intents {
					if intent.ID != form.Confirmation.PromptDeliveryID {
						continue
					}
					switch mode {
					case "prompt-content":
						intent.Message.Content = "An unrelated native question."
					case "prompt-request":
						intent.Message.Context.MessageID = "foreign"
					case "prompt-scope":
						intent.Message.TraceScopes = nil
					case "missing-prompt":
						intent.ID = "delivery_foreign"
					}
					native.intents[index] = intent
				}
			}
			native.state.Sessions[session.ID] = session
			native.state.PreparedActions[action.ID] = action
			proof := inspectBrowserTaskWithForm(
				complete,
				c.domain.target,
				c.domain.fixture,
				native,
				empty,
				nil,
				nil,
				form,
			)
			// A duplicate submit invocation is rejected by the form-specific join
			// before this shared task observer; its count also forbids judging.
			if mode == "duplicate-invocation" {
				trial := cloneBrowserFormControlValue(t, complete)
				pending := browserPendingTrial{item: c.item.Case, fixture: c.domain.fixture, conversation: c.domain}
				if err := finalizeBrowserForm(&trial, pending, c.domain.target, native, empty); err == nil &&
					trial.Browser.Form.Code == "form_observed" {
					t.Fatal("duplicate native submit admitted")
				}
			} else if proof.Code == "native_post_stop_observed" {
				t.Fatal("native form gap admitted", mode)
			}
		})
	}
	host := &BrowserFixtureHostEvidence{
		Scope: "owned_http_fixture_host_v1", Code: "closed", Trials: 1,
		OriginSHA256: digest([]byte(c.domain.fixture.origin)),
	}
	surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
	for _, mode := range []string{
		"confirmation", "approval", "context", "correction", "fixture", "duplicate-submit", "native",
		"cleanup", "input", "fact", "source", "status", "trace", "answer-event", "confirmation-event", "approval-event", "privacy",
	} {
		t.Run(mode, func(t *testing.T) {
			trial := cloneBrowserFormControlValue(t, complete)
			form := trial.Browser.Form
			scan := *surfaces
			switch mode {
			case "confirmation":
				form.Confirmation = nil
			case "approval":
				form.Approval = nil
			case "context":
				form.ContextRetained = false
			case "correction":
				form.Corrections = 0
			case "fixture":
				trial.Browser.Fixture.Effects = Fail
			case "duplicate-submit":
				form.Submissions = 2
			case "native":
				form.Native.StateSHA256 = strings.Repeat("f", 64)
			case "cleanup":
				form.Native.LogicalSettled = false
			case "input":
				trial.Browser.FormInputs.Fills = trial.Browser.FormInputs.Fills[:2]
			case "fact":
				trial.Browser.FormInputs.Fills[0].Fact = "user-note"
			case "source":
				trial.Browser.FormInputs.Fills[0].Source.Commit = "unknown"
			case "status":
				trial.Browser.FormInputs.Fills[0].InteractionStatus = interactions.StatusResuming
			case "trace":
				trial.Browser.FormInputs.Fills[0].TraceScope.TurnID = "foreign"
			case "answer-event":
				trial.Events[2].RequestID = "foreign"
			case "confirmation-event":
				for i, event := range trial.Events {
					if event.RequestID == form.Confirmation.RequestID && event.Kind == "input" {
						trial.Events[i].Content = "Unrelated answer."
					}
				}
			case "approval-event":
				form.Approval.ApprovalRequestID = "foreign"
			case "privacy":
				scan.Code = "leak_observed"
				scan.LeakObserved = true
			}
			provider := &roleTestProvider{
				mediated: true,
				response: &providers.LLMResponse{
					FinishReason: "stop",
					Content: `{"version":1,"criteria":[{"id":"coherent","verdict":"pass","reason":"Looks fine.","evidence_ids":["` + lastOutcome(
						trial.Events,
					).ID + `"]}]}`,
				},
			}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			assessFinalizedBrowser(
				t.Context(),
				judge,
				c.item.Case,
				&trial,
				c.domain.questions.workspace,
				cleanPDFLifecycle(),
				&scan,
				host,
			)
			if trial.EvidenceEligible || trial.Verdict == Pass || provider.calls != 0 {
				t.Fatal("positive judge bypassed missing hard evidence", mode)
			}
		})
	}
}

func TestBrowserFormApprovalRefusesMissingConfirmationAndChangedPage(t *testing.T) {
	c := newBrowserFormControl(t, "submit")
	record, prompt := c.prepareReview(t)
	inputs, state, err := c.domain.readFormInputState()
	if err != nil {
		t.Fatal(err)
	}
	if c.domain.formApprovalMatches(record, browser.PreparedAction{}, state) {
		t.Fatal("unconfirmed form became a grant")
	}
	wrongPrompt := prompt
	wrongPrompt.Content = "A different question with the same native identity."
	if request, _, err := c.domain.request(
		t.Context(),
		Action{Kind: "confirm"},
		wrongPrompt,
	); err == nil ||
		request.ID != "" {
		t.Fatal("wire/native confirmation text mismatch admitted")
	}
	c.confirm(t, record, prompt)
	if _, _, err = c.domain.request(t.Context(), Action{Kind: "confirm"}, prompt); err == nil {
		t.Fatal("confirmation replayed")
	}
	c.worker.title = "Changed fixture page"
	prepared, err := c.broker.PrepareAction(t.Context(), c.actionRequest(t, browser.ActionClick, "submit", ""))
	if err != nil || !prepared.RequiresApproval {
		t.Fatal("changed page preparation", err)
	}
	_, approvalPrompt := c.recordQuestion(t, "browser_act", "Submit the form once", nil, c.nextCall, prepared)
	if observation, err := c.domain.readApproval(approvalPrompt); err == nil || observation != nil {
		t.Fatal("stale reviewed page approved")
	}
	if len(c.domain.fixture.snapshot().submissions) != 0 || len(inputs.Fills) != 2 {
		t.Fatal("admission control mutated the fixture")
	}
}

func TestBrowserFormActionParserKeepsApprovalScoped(t *testing.T) {
	for _, entry := range []struct {
		body, pending string
		valid         bool
	}{
		{`{"kind":"confirm"}`, "question", true},
		{`{"kind":"confirm"}`, "final", false},
		{`{"kind":"confirm","text":"yes"}`, "question", false},
		{`{"kind":"allow_once"}`, "approval", true},
		{`{"kind":"allow_once"}`, "question", false},
		{`{"kind":"allow_once","prepared_action_id":"foreign"}`, "approval", false},
		{`{"kind":"protected_answer","fact":"name"}`, "question", true},
		{`{"kind":"protected_answer","fact":"name"}`, "final", false},
	} {
		_, err := parseBrowserFormUserAction([]byte(entry.body), []string{"name"}, entry.pending)
		if (err == nil) != entry.valid {
			t.Fatal("unexpected form user intent", entry.body, err)
		}
		if entry.valid && strings.Contains(entry.body, "allow_once") {
			if _, err = ParseUserAction([]byte(entry.body), nil, entry.pending); err == nil {
				t.Fatal("browser scope leaked generic approval")
			}
		}
	}
}
