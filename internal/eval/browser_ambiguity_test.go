package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func completeBrowserAmbiguityControl(t *testing.T) *browserAmbiguityControl {
	t.Helper()
	c := newBrowserAmbiguityControl(t)
	c.trial.Events = []Event{
		{ID: uuid.NewString(), Kind: "input", Turn: 1, RequestID: c.requestID, Content: c.domain.initialTask()},
		c.prompt,
	}
	c.answer(t)
	prepared, record, prompt := c.prepare(t)
	c.approveAndExecute(t, prepared, record, prompt)
	if err := c.broker.CloseOwner(context.Background(), c.request.Owner); err != nil || !c.nativeWorker.closed {
		t.Fatal("native owner did not close", err)
	}
	if err := os.MkdirAll(filepath.Join(c.fixture.home, ownedGatewayTempDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	c.domain.fixture.seal()
	final := Event{
		ID: uuid.NewString(), Kind: "final", Turn: c.turn, RequestID: c.requestID,
		MessageID: uuid.NewString(), AgentID: c.trial.AgentID, SessionKey: c.trial.SessionKey,
		Content:     "Removed only the South account.",
		TraceScopes: []events.TraceScope{events.NewTraceScope(c.fixture.workspace, "ambiguity-final")},
	}
	c.trial.Events = append(c.trial.Events, final)
	intent, err := newBrowserFinalIntent(c.fixture.workspace, c.trial, final)
	if err != nil {
		t.Fatal(err)
	}
	writeAdmissionJSON(t, filepath.Join(c.fixture.workspace, "state/outbox", intent.ID+".json"), intent)
	c.trial.ExecutionVerdict, c.trial.ExecutionCode = Pass, "script_completed"
	c.trial.TransportClosed, c.trial.Repetition = true, 1
	c.trial.Checks = gradeConversation(c.item.Case.Checks, c.trial.Events, final)
	c.trial.Verdict, c.trial.Code = Inconclusive, "required_check_unknown"
	return c
}

func finalizedAmbiguityControl(t *testing.T, c *browserAmbiguityControl) (*privateReport, Trial) {
	t.Helper()
	trial := c.trial
	writer, err := newPrivateReport(c.fixture.output, c.fixture.home, c.fixture.workspace, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.root.Close() })
	name := "trial-" + trial.ID + ".json"
	if err = writer.write("pending-"+name, trial); err != nil {
		t.Fatal(err)
	}
	pending := browserRun{}
	pending.retain(c.item.Case, trial, c.domain.fixture)
	entry := pending.trials[trial.ID]
	entry.conversation = c.domain
	pending.trials[trial.ID] = entry
	result := RunReport{Trials: []TrialSummary{{
		ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: trial.Repetition,
		Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
	}}}
	if err = pending.finish(t.Context(), c.domain.target, writer, &result, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	return writer, comparisonTrial(t, RunReport{ReportDir: writer.dir, Trials: result.Trials}, 0)
}

func ambiguityHost(c *browserAmbiguityControl) *BrowserFixtureHostEvidence {
	return &BrowserFixtureHostEvidence{
		Scope: "owned_http_fixture_host_v1", Code: "closed", Trials: 1,
		OriginSHA256: digest([]byte(c.domain.fixture.origin)),
	}
}

func ambiguityJudgePass(trial Trial) string {
	return `{"version":1,"criteria":[{"id":"clarify","verdict":"pass","reason":"Clarified before removing South only.","evidence_ids":["` +
		trial.Events[1].ID + `","` + lastOutcome(
		trial.Events,
	).ID + `"]}]}`
}

func TestBrowserAmbiguityPostStopNativeFixtureAndJudge(t *testing.T) {
	c := completeBrowserAmbiguityControl(t)
	_, trial := finalizedAmbiguityControl(t, c)
	if !validBrowserAmbiguityEvidence(trial, c.item.Case, c.fixture.workspace) {
		proof := trial.Browser.Ambiguity
		value := proof.Clarification
		t.Logf("clarification IDs=%v hashes=%v revision=%v time=%v", []bool{
			validID(value.InteractionID), validID(value.ShortID), validID(value.PromptDeliveryID),
			validID(value.PromptRequestID), validID(value.RequestID), validID(value.SessionID),
			validID(value.TabID), validID(value.ExecutionID),
		}, []bool{
			validDigest(value.PromptSHA256), validDigest(value.RegistrySHA256),
			validDigest(value.PageStateSHA256), validDigest(value.FixturePageSHA256),
		},
			value.WaitingRevision > 0 && value.ObservedRevision > value.WaitingRevision,
			value.RequestedAt >= trial.StartedAt.UnixMilli() && value.ReceivedAt >= value.RequestedAt &&
				value.ResolvedAt >= value.ReceivedAt)
		t.Fatalf("complete native join unavailable: conversation=%v native=%v clarification=%v events=%v approval=%v",
			validBrowserConversation(trial, c.item.Case, c.fixture.workspace),
			validBrowserTaskProof(trial, proof.Native, browserAmbiguityAssessmentScope),
			validBrowserClarification(proof.Clarification, trial), browserClarificationEventsMatch(trial, proof),
			browserApprovalEventsMatch(trial, proof.Approval))
	}
	provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
		Content: ambiguityJudgePass(trial), FinishReason: "stop",
	}}
	judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
	assessFinalizedBrowser(t.Context(), judge, c.item.Case, &trial, c.fixture.workspace, cleanPDFLifecycle(),
		&PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}, ambiguityHost(c))
	if trial.Verdict != Pass || !trial.EvidenceEligible || provider.calls != 1 ||
		trial.Browser.Ambiguity.Native.PermissionPromptDeliveries != 1 ||
		trial.Browser.Ambiguity.Native.ApprovalPromptDeliveries != 1 {
		t.Fatal("complete model-free ambiguity control was not assessed", trial.Code)
	}
}

func TestBrowserAmbiguityNeverPromotesPartialOrChangedEvidence(t *testing.T) {
	c := completeBrowserAmbiguityControl(t)
	_, original := finalizedAmbiguityControl(t, c)
	for _, mode := range []string{
		"clarification", "approval", "native", "question", "answer", "approval-event", "fixture", "effect",
		"page", "policy", "consumption", "registry", "revision", "time", "cleanup", "logical", "extra-scope",
	} {
		t.Run(mode, func(t *testing.T) {
			trial := cloneBrowserFormControlValue(t, original)
			proof := trial.Browser.Ambiguity
			switch mode {
			case "clarification":
				proof.Clarification = nil
			case "approval":
				proof.Approval = nil
			case "native":
				proof.Native = nil
			case "question":
				trial.Events[1].Content = "Unrelated question"
			case "answer":
				trial.Events[2].Content = "North"
			case "approval-event":
				trial.Events[3].InteractionShortID = "foreign"
			case "fixture":
				trial.Browser.Fixture.Removals = 0
			case "effect":
				trial.Browser.Fixture.Effects = Fail
			case "page":
				proof.Clarification.FixturePageSHA256 = strings.Repeat("f", 64)
			case "policy":
				proof.Approval.PolicyRevision = strings.Repeat("f", 64)
			case "consumption":
				proof.Approval.ConsumedAt = 0
			case "registry":
				proof.Clarification.RegistrySHA256 = ""
			case "revision":
				proof.Clarification.ObservedRevision = proof.Clarification.WaitingRevision
			case "time":
				proof.Clarification.ResolvedAt = 0
			case "cleanup":
				proof.Native.TemporaryEmpty = false
			case "logical":
				proof.Native.LogicalSettled = false
			case "extra-scope":
				trial.Browser.Read = proof.Native
			}
			if validBrowserAmbiguityEvidence(trial, c.item.Case, c.fixture.workspace) {
				t.Fatal("partial or changed retained evidence qualified", mode)
			}
		})
	}
}

func TestBrowserAmbiguityNativeAssessmentReadsEveryRecordAndPrompt(t *testing.T) {
	c := completeBrowserAmbiguityControl(t)
	_, original := finalizedAmbiguityControl(t, c)
	native, err := observeBrowserNative(t.Context(), c.fixture.workspace, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{
		"region", "hash", "before-consumption", "unknown", "duplicate", "unexpected-effect", "frame",
		"question-body", "approval-body", "question-request", "approval-request", "missing-question", "missing-approval",
	} {
		t.Run(mode, func(t *testing.T) {
			encoded, _ := json.Marshal(native.state)
			copy := native
			if json.Unmarshal(encoded, &copy.state) != nil {
				t.Fatal("native clone unavailable")
			}
			copy.intents = cloneBrowserFormControlValue(t, native.intents)
			proof := cloneBrowserFormControlValue(t, original.Browser.Ambiguity)
			action := copy.state.PreparedActions[proof.RemovalActionID]
			invocation := copy.state.Invocations[proof.RemovalInvocationID]
			switch mode {
			case "region":
				action.ElementRegionName = "North account"
			case "hash":
				invocation.ActionHash = strings.Repeat("f", 64)
			case "before-consumption":
				invocation.AcceptedAt = (proof.Approval.ConsumedAt - 1) * 1_000_000
			case "unknown":
				invocation.State = browser.InvocationUnknown
			case "duplicate":
				copy.state.Invocations["duplicate"] = invocation
			case "unexpected-effect":
				other := action
				other.ID = "unexpected-action"
				other.ElementRegionName = "North account"
				copy.state.PreparedActions[other.ID] = other
				otherInvocation := invocation
				otherInvocation.PreparedActionID = other.ID
				copy.state.Invocations["unexpected-invocation"] = otherInvocation
			case "frame":
				action.FrameID = "foreign-frame"
			default:
				for index, intent := range copy.intents {
					if intent.Message == nil {
						continue
					}
					kind := intent.Message.Metadata.InteractionKind
					if kind == "" || !strings.HasPrefix(mode, kind+"-") && !strings.HasSuffix(mode, "-"+kind) {
						continue
					}
					if strings.HasPrefix(mode, "missing-") {
						intent.ID = "missing"
					} else if strings.HasSuffix(mode, "-body") {
						intent.Message.Content = "Different prompt"
					} else {
						intent.Message.Context.MessageID = "foreign-request"
					}
					copy.intents[index] = intent
				}
			}
			copy.state.PreparedActions[action.ID], copy.state.Invocations[invocation.ID] = action, invocation
			observed := inspectBrowserTask(original, c.domain.target, c.domain.fixture, copy, true,
				browserTaskEffects{ambiguity: proof})
			if observed.Code == "native_post_stop_observed" {
				t.Fatal("changed native source record was ignored", mode)
			}
		})
	}
}

func TestBrowserAmbiguityPreservesHardFailureAndRequiresOwnedPrivacy(t *testing.T) {
	c := completeBrowserAmbiguityControl(t)
	_, original := finalizedAmbiguityControl(t, c)
	for _, mode := range []string{"hard", "lifecycle", "privacy", "host"} {
		t.Run(mode, func(t *testing.T) {
			trial := cloneBrowserFormControlValue(t, original)
			lifecycle := cleanPDFLifecycle()
			surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
			host := ambiguityHost(c)
			switch mode {
			case "hard":
				trial.Checks[0].Verdict, trial.Checks[0].Code = Fail, "retained_contradiction"
				trial.Verdict, trial.Code = Fail, "hard_check_failed"
			case "lifecycle":
				lifecycle = nil
			case "privacy":
				surfaces.Code = "unavailable"
			case "host":
				host.Code = "target_exit_unverified"
			}
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: ambiguityJudgePass(trial), FinishReason: "stop",
			}}
			judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
			assessFinalizedBrowser(t.Context(), judge, c.item.Case, &trial, c.fixture.workspace,
				lifecycle, surfaces, host)
			if provider.calls != 0 || trial.EvidenceEligible || trial.Verdict == Pass ||
				mode == "hard" && trial.Verdict != Fail {
				t.Fatal("hard failure or missing ownership/privacy was promoted", mode)
			}
		})
	}
}

func TestBrowserAmbiguityFinalizationRequiresIndependentFixtureAndSettledRegistry(t *testing.T) {
	for _, mode := range []string{"wrong-account", "temporary", "extra-question", "missing-registry"} {
		t.Run(mode, func(t *testing.T) {
			c := completeBrowserAmbiguityControl(t)
			switch mode {
			case "wrong-account":
				c.domain.fixture.mu.Lock()
				c.domain.fixture.state.removals = []string{"north"}
				c.domain.fixture.mu.Unlock()
			case "temporary":
				if os.WriteFile(
					filepath.Join(c.fixture.home, ownedGatewayTempDirectory, "remaining"),
					nil,
					0o600,
				) != nil {
					t.Fatal("temporary witness unavailable")
				}
			case "extra-question":
				c.recordQuestion(
					t,
					"request_user_input",
					"Unsettled extra question",
					nil,
					"extra-call",
					browser.Preparation{},
				)
			case "missing-registry":
				if os.Remove(filepath.Join(c.fixture.workspace, "state/interaction_registry.json")) != nil {
					t.Fatal("registry witness unavailable")
				}
			}
			_, trial := finalizedAmbiguityControl(t, c)
			if validBrowserAmbiguityEvidence(trial, c.item.Case, c.fixture.workspace) || trial.EvidenceEligible ||
				mode == "wrong-account" && trial.Verdict != Fail {
				t.Fatal("native/prose evidence overrode independent effects or missing settlement", mode)
			}
		})
	}
}

func TestBrowserAmbiguityCaseAdmitsOnlyReviewedScript(t *testing.T) {
	item := browserCorpusCase(t, "ambiguous").Case
	for _, mode := range []string{"text", "order", "steps", "rubric", "fact", "adaptive"} {
		t.Run(mode, func(t *testing.T) {
			copy := cloneBrowserFormControlValue(t, item)
			switch mode {
			case "text":
				copy.Driver.Steps[0].Action.Text = "Remove North instead."
			case "order":
				copy.Driver.Steps[0], copy.Driver.Steps[1] = copy.Driver.Steps[1], copy.Driver.Steps[0]
			case "steps":
				copy.Driver.Steps = append(copy.Driver.Steps, copy.Driver.Steps[1])
			case "rubric":
				copy.Rubric[0].Required = false
			case "fact":
				copy.Driver.Facts = map[string]string{"unneeded": "value"}
			case "adaptive":
				copy.Driver.Mode = "llm_user"
			}
			if copy.Validate() == nil {
				t.Fatal("unsupported approval-intent script was admitted", mode)
			}
		})
	}
}
