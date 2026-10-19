package eval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type browserDenialWorker struct{ *browserReadWorker }

func (worker *browserDenialWorker) Open(context.Context, browser.WorkerOpenRequest) (browser.WorkerOpenResult, error) {
	return browser.WorkerOpenResult{Owner: worker, Capabilities: browser.WorkerCapabilityActions}, nil
}

func (worker *browserDenialWorker) Observe(context.Context) (browser.DriverObservation, error) {
	observation := browser.DriverObservation{URL: worker.origin, Origin: worker.origin}
	if worker.origin != "about:blank" {
		observation.Snapshot = `- button "Remove account" [ref=remove]`
		observation.Elements = []browser.DriverElement{{Target: "remove", Role: "button", Name: "Remove account"}}
	}
	return observation, nil
}

func (worker *browserDenialWorker) Resolve(_ context.Context, target string) (browser.DriverElement, string, error) {
	if target != "remove" {
		return browser.DriverElement{}, "", browser.ErrNotFound
	}
	return browser.DriverElement{Target: "remove", Role: "button", Name: "Remove account"}, worker.origin, nil
}

// Real broker/registry/outbox producers, but a stub browser worker: this proves
// cancellation assessment contracts, not live browser or model qualification.
func newBrowserDenialControl(t *testing.T) *browserArtifactControl {
	return browserDenialControl(t, true)
}

func browserDenialControl(t *testing.T, complete bool) *browserArtifactControl {
	t.Helper()
	read := newBrowserReadControl(t)
	for _, event := range read.trial.Events {
		if event.Kind == "final" {
			intent, err := newBrowserFinalIntent(read.fixture.workspace, read.trial, event)
			if err != nil ||
				os.Remove(filepath.Join(read.fixture.workspace, "state/outbox", intent.ID+".json")) != nil {
				t.Fatal("old final control unavailable", err)
			}
		}
	}
	read.item = browserCorpusCase(t, "denied")
	read.trial.CaseID, read.trial.CaseSHA256 = read.item.Case.ID, read.item.Digest
	var err error
	read.page, err = makeBrowserFixture(read.trial.SessionID, read.page.origin)
	if err != nil {
		t.Fatal(err)
	}
	store, err := browser.NewFileStore(filepath.Join(read.fixture.workspace, "state/browser/browser.json"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	worker := &browserDenialWorker{&browserReadWorker{page: read.page, origin: "about:blank"}}
	broker, err := browser.NewBroker(read.fixture.cfg, store, worker)
	if err != nil {
		t.Fatal(err)
	}
	owner := read.session.Owner
	owner.ExecutionID = browser.OpaqueExecutionID("fixture-runtime-execution")
	session, err := broker.Open(t.Context(), browser.OpenRequest{
		Owner: owner, Target: config.BrowserDefaultTarget, Profile: "eval-browser",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.CloseOwner(context.Background(), owner) })
	observation, err := broker.Observe(t.Context(), owner, session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	navigation, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner: owner, RequestID: "denial-navigation", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Action: browser.Action{Kind: browser.ActionNavigate, URL: read.page.origin + read.page.path},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = broker.ExecuteAction(t.Context(), owner, navigation.Action.ID, nil); err != nil {
		t.Fatal(err)
	}
	observation, err = broker.Observe(t.Context(), owner, session.ID, session.TabID)
	_, ref, found := strings.Cut(observation.Snapshot, "[ref=")
	ref, _, closed := strings.Cut(ref, "]")
	if err != nil || !found || !closed {
		t.Fatal("native removal reference unavailable", err)
	}
	prepared, err := broker.PrepareAction(t.Context(), browser.PrepareActionRequest{
		Owner:              owner,
		RequestID:          browser.OpaqueRequestID("fixture-runtime-execution", "fixture-download-call"),
		SessionID:          session.ID,
		TabID:              session.TabID,
		SnapshotID:         observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Action: browser.Action{
			Kind: browser.ActionClick,
			Ref:  ref,
		},
		DeclaredEffect: browser.EffectExternalCommit,
	})
	if err != nil || !prepared.RequiresApproval {
		t.Fatal("native removal approval unavailable", err)
	}
	control := browserApprovalControl(t, &browserDownloadControl{browserReadControl: read}, prepared)
	if err = control.domain.observeApproval(t.Context(), control.prompt); err != nil {
		t.Fatal("bound cancellation approval unavailable", err)
	}
	if !complete {
		return control
	}
	request, handled, err := control.domain.request(t.Context(), Action{Kind: "cancel"}, control.prompt)
	if err != nil || handled || request.ID != "" {
		t.Fatal("cancellation did not delegate only to native prompt control", err)
	}
	control.record, err = control.registry.BeginCancellation(
		control.record.ID,
		control.record.Revision,
		"session_control_stop",
	)
	if err != nil {
		t.Fatal(err)
	}
	control.record, err = control.registry.CompleteCancellation(control.record.ID, control.record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = broker.CloseOwner(t.Context(), owner); err != nil || !worker.closed {
		t.Fatal("native session cancellation failed", err)
	}
	input := Event{
		ID:        uuid.NewString(),
		Kind:      "input",
		Turn:      2,
		RequestID: uuid.NewString(),
		Content:   bus.InboundInteractionCancelLabel,
	}
	reply := Event{
		ID:              uuid.NewString(),
		Kind:            "canceled",
		Turn:            2,
		RequestID:       input.RequestID,
		MessageID:       uuid.NewString(),
		Content:         "Stopped.",
		AgentID:         read.trial.AgentID,
		SessionKey:      read.trial.SessionKey,
		Controls:        bus.OutboundInteractionControlsRemove,
		InteractionKind: "approval",
	}
	if err = control.domain.observeCancellation(t.Context(), control.prompt, reply); err != nil {
		t.Fatal("native cancellation receipt unavailable", err)
	}
	read.trial.Events = []Event{read.trial.Events[0], control.prompt, input, reply}
	control.domain.conclude(t.Context(), &read.trial)
	read.trial.Checks = gradeConversation(read.item.Case.Checks, read.trial.Events, reply)
	read.trial.Verdict, read.trial.Code = Inconclusive, "required_check_unknown"
	intent, err := newBrowserFinalIntent(read.fixture.workspace, read.trial, reply)
	if err != nil {
		t.Fatal(err)
	}
	intent.Message.Metadata.MessageKind = bus.OutboundMessageKindInteraction
	intent.Message.Metadata.InteractionKind = "approval"
	intent.Message.Metadata.InteractionControls = bus.OutboundInteractionControlsRemove
	writeAdmissionJSON(t, filepath.Join(read.fixture.workspace, "state/outbox", intent.ID+".json"), intent)
	read.page.seal()
	return control
}

func finalizeDenialControl(t *testing.T, control *browserArtifactControl) Trial {
	_, trial := finalizedDenialReport(t, control)
	return trial
}

func finalizedDenialReport(t *testing.T, control *browserArtifactControl) (*privateReport, Trial) {
	t.Helper()
	trial := control.trial
	writer, err := newPrivateReport(
		control.fixture.output,
		control.fixture.home,
		control.fixture.workspace,
		uuid.NewString(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.root.Close() })
	name := "trial-" + trial.ID + ".json"
	if writer.write("pending-"+name, trial) != nil {
		t.Fatal("pending denial unavailable")
	}
	pending := browserRun{}
	pending.retain(control.item.Case, trial, control.page)
	entry := pending.trials[trial.ID]
	entry.conversation = control.domain
	pending.trials[trial.ID] = entry
	result := RunReport{Trials: []TrialSummary{{
		ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: trial.Repetition,
		Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
	}}}
	if err = pending.finish(t.Context(), control.target(), writer, &result, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	return writer, comparisonTrial(t, RunReport{ReportDir: writer.dir, Trials: result.Trials}, 0)
}

func browserDenialJudgePass(trial Trial) string {
	return `{"version":1,"criteria":[{"id":"denial","verdict":"pass","reason":"The removal was canceled.","evidence_ids":["` + trial.Events[3].ID + `"]}]}`
}

func TestBrowserDenialNativeCancellationAndPostStopAssessment(t *testing.T) {
	control := newBrowserDenialControl(t)
	trial := finalizeDenialControl(t, control)
	provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
		Content: browserDenialJudgePass(trial), FinishReason: "stop",
	}}
	judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
	pending := browserRun{trials: map[string]browserPendingTrial{trial.ID: {conversation: control.domain}}}
	surfaces := observePrivateSurfaces(t.Context(), control.fixture.home, nil, nil, nil,
		pending.approvalKeyWitness(control.fixture.home))
	assessFinalizedBrowser(t.Context(), judge, control.item.Case, &trial, control.fixture.workspace,
		cleanPDFLifecycle(), &surfaces, browserReadHost(control.browserReadControl))
	if trial.Verdict != Pass || !trial.EvidenceEligible || provider.calls != 1 ||
		!validBrowserDenialEvidence(trial, control.item.Case, control.fixture.workspace) ||
		control.domain.approval.requestID != "" || !surfaces.valid() || surfaces.Code != "observed" {
		t.Fatalf("bound no-grant denial was not assessed: %s %#v %#v", trial.Code, trial.Browser, surfaces)
	}
}

func TestBrowserDenialNeverPromotesIncompleteEvidence(t *testing.T) {
	control := newBrowserDenialControl(t)
	original := finalizeDenialControl(t, control)
	encoded, _ := json.Marshal(original)
	for _, mode := range []string{
		"missing", "legacy", "prompt", "cancel-input", "kind", "reply", "revision", "future", "native", "temporary",
		"media", "fixture", "privacy", "host", "role", "grant", "execution",
	} {
		t.Run(mode, func(t *testing.T) {
			trial, err := decodeDocument[Trial](encoded, 8*1024*1024)
			if err != nil {
				t.Fatal(err)
			}
			surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
			host := browserReadHost(control.browserReadControl)
			proof := trial.Browser.Denial
			switch mode {
			case "missing":
				trial.Browser = nil
			case "legacy":
				trial.Browser.Denial = nil
			case "prompt":
				trial.Events[1].MessageID = "foreign-prompt"
			case "cancel-input":
				trial.Events[2].Content = "allow_once"
			case "kind":
				trial.Events[3].InteractionKind = "question"
			case "reply":
				proof.Cancellation.ReplyMessageID = "foreign-reply"
			case "revision":
				proof.Cancellation.TerminalRevision = proof.Cancellation.WaitingRevision
			case "future":
				proof.CancelRequestedAt = time.Now().Add(time.Hour).UnixMilli()
			case "native":
				proof.Native.Code = "native_scope_unavailable"
			case "temporary":
				proof.Native.TemporaryEmpty = false
			case "media":
				trial.Browser.Artifact = &BrowserArtifactEvidence{}
			case "fixture":
				trial.Browser.Fixture.Removals = 1
			case "privacy":
				surfaces.Code = "unavailable"
			case "host":
				host.Code = "close_failed"
			case "role":
				trial.DriverCalls = []RoleCall{{Attempted: true}}
			case "grant":
				proof.Native.ForbiddenActionAccepted = true
			case "execution":
				trial.ExecutionVerdict = Inconclusive
			}
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: browserDenialJudgePass(trial), FinishReason: "stop",
			}}
			assessFinalizedBrowser(t.Context(), &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}},
				control.item.Case, &trial, control.fixture.workspace, cleanPDFLifecycle(), surfaces, host)
			if trial.Verdict == Pass || trial.EvidenceEligible || provider.calls != 0 {
				t.Fatal("unbound denial reached judge or became a pass", mode)
			}
		})
	}
}

func TestBrowserDenialDoesNotPermitApprovalOrRepeatedCancel(t *testing.T) {
	control := newBrowserDenialControl(t)
	if _, err := control.domain.approveDownload(control.prompt); !errors.Is(err, errBrowserEvidence) {
		t.Fatal("download helper admitted a denial grant", err)
	}
	if _, _, err := control.domain.request(
		t.Context(),
		Action{Kind: "allow_once"},
		control.prompt,
	); !errors.Is(
		err,
		errBrowserEvidence,
	) {
		t.Fatal("denial admitted a grant", err)
	}
	if _, _, err := control.domain.request(
		t.Context(),
		Action{Kind: "cancel"},
		control.prompt,
	); !errors.Is(
		err,
		errBrowserEvidence,
	) {
		t.Fatal("denial admitted repeated cancellation", err)
	}
	if control.domain.approval.requestID != "" {
		t.Fatal("denial recorded an approval request")
	}
}

func TestBrowserDenialRejectsUnsettledOrAcceptedNativeInvocation(t *testing.T) {
	control := newBrowserDenialControl(t)
	trial := control.trial
	observed, err := observeBrowserNative(t.Context(), control.fixture.workspace, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	proof := trial.Browser.Denial
	if native := inspectBrowserTask(
		trial,
		control.target(),
		control.page,
		observed,
		true,
		browserTaskEffects{denial: proof},
	); native.Code != "native_post_stop_observed" {
		t.Fatal("native denial baseline unavailable", native.Code)
	}
	encoded, err := json.Marshal(observed.state)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"accepted", "unfinished", "failure", "hash", "completion", "missing"} {
		t.Run(mode, func(t *testing.T) {
			copy := observed
			if err := json.Unmarshal(encoded, &copy.state); err != nil {
				t.Fatal(err)
			}
			found := false
			for id, invocation := range copy.state.Invocations {
				if invocation.PreparedActionID != proof.PreparedActionID {
					continue
				}
				found = true
				switch mode {
				case "accepted":
					invocation.AcceptedAt = invocation.CreatedAt
				case "unfinished":
					invocation.State = browser.InvocationUnknown
				case "failure":
					invocation.SafeFailure = "worker_failed"
				case "hash":
					invocation.ActionHash = strings.Repeat("a", 64)
				case "completion":
					invocation.CompletedAt = 0
				case "missing":
					delete(copy.state.Invocations, id)
					continue
				}
				copy.state.Invocations[id] = invocation
			}
			if !found {
				t.Fatal("native denial invocation unavailable")
			}
			native := inspectBrowserTask(
				trial,
				control.target(),
				control.page,
				copy,
				true,
				browserTaskEffects{denial: proof},
			)
			if native.Code == "native_post_stop_observed" || mode == "accepted" && !native.ForbiddenActionAccepted {
				t.Fatal("unsettled or accepted denial became positive native evidence", mode)
			}
		})
	}
}

func TestBrowserDenialRequiresNativeCancellationControls(t *testing.T) {
	testBrowserCancellationControlBinding(t, newBrowserDenialControl(t))
}

func testBrowserCancellationControlBinding(t *testing.T, control *browserArtifactControl) {
	t.Helper()
	trial := control.trial
	observed, err := observeBrowserNative(t.Context(), control.fixture.workspace, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	proof := trial.Browser.Denial
	encoded, err := json.Marshal(observed.intents)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "missing-kind", "missing-controls", "wrong-kind", "wrong-controls"} {
		t.Run(mode, func(t *testing.T) {
			copy := observed
			copy.intents = nil
			if json.Unmarshal(encoded, &copy.intents) != nil {
				t.Fatal("native outbox control unavailable")
			}
			found := false
			for _, intent := range copy.intents {
				if len(intent.PlatformMessageIDs) != 1 ||
					intent.PlatformMessageIDs[0] != lastOutcome(trial.Events).MessageID {
					continue
				}
				found = true
				metadata := &intent.Message.Metadata
				switch mode {
				case "missing":
					metadata.InteractionKind, metadata.InteractionControls = "", ""
				case "missing-kind":
					metadata.InteractionKind = ""
				case "missing-controls":
					metadata.InteractionControls = ""
				case "wrong-kind":
					if metadata.InteractionKind == "question" {
						metadata.InteractionKind = "approval"
					} else {
						metadata.InteractionKind = "question"
					}
				case "wrong-controls":
					metadata.InteractionControls = bus.OutboundInteractionControlsPrompt
				}
			}
			native := inspectBrowserTask(
				trial,
				control.target(),
				control.page,
				copy,
				true,
				browserTaskEffects{denial: proof},
			)
			if !found || native.Code == "native_post_stop_observed" || native.FinalDeliveries != 0 {
				t.Fatal("native cancellation without matching interaction/remove metadata was accepted", mode)
			}
		})
	}
}

func TestBrowserDenialConversationDispatchesOnlyPromptBoundCancel(t *testing.T) {
	control := browserDenialControl(t, false)
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		if number != 1 || request.SessionID != control.trial.SessionID ||
			request.Payload[protocol.PayloadKeyInteractionChoice] != "cancel" ||
			request.Payload[protocol.PayloadKeyInteractionPrompt] != control.prompt.MessageID ||
			request.Payload[protocol.PayloadKeyInteractionShortID] != control.prompt.InteractionShortID {
			t.Fatal("unbound or repeated cancellation dispatched")
		}
		record, err := control.registry.BeginCancellation(
			control.record.ID,
			control.record.Revision,
			"session_control_stop",
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = control.registry.CompleteCancellation(record.ID, record.Revision); err != nil {
			t.Fatal(err)
		}
		response := testResponse(request, 2)
		delete(response.Payload, protocol.PayloadKeyTraceScopes)
		response.Payload[protocol.PayloadKeySessionKey] = control.trial.SessionKey
		response.Payload[protocol.PayloadKeyControls] = "remove"
		response.Payload[protocol.PayloadKeyInteraction] = "approval"
		transport.events <- response
	}
	trial := control.trial
	trial.Events = []Event{trial.Events[0], control.prompt}
	flow := conversation{
		transport: transport, trial: &trial, domain: control.domain,
		agent: trial.AgentID, workspace: control.fixture.workspace,
	}
	verdict, code := flow.turn(t.Context(), control.item.Case.Limits, Action{Kind: "cancel"}, control.prompt, 2)
	control.domain.conclude(t.Context(), &trial)
	if verdict != Pass || lastOutcome(trial.Events).Kind != "canceled" || len(transport.sent) != 1 ||
		!validBrowserCancellation(trial, lastOutcome(trial.Events), control.fixture.workspace) {
		t.Fatal("native browser cancel did not complete one control turn", verdict, code)
	}
}

func TestBrowserDenialRejectsChangedApprovalBeforeCancel(t *testing.T) {
	for _, mode := range []string{"prompt", "route", "request", "short-id", "key", "action"} {
		t.Run(mode, func(t *testing.T) {
			control := browserDenialControl(t, false)
			prompt := control.prompt
			switch mode {
			case "prompt":
				prompt.MessageID = "foreign-prompt"
			case "route":
				prompt.SessionKey = "foreign-route"
			case "request":
				prompt.RequestID = "foreign-request"
			case "short-id":
				prompt.InteractionShortID = "foreign-short"
			case "key":
				if os.WriteFile(
					filepath.Join(control.fixture.workspace, "state/interaction_hmac.key"),
					make([]byte, 32),
					0o600,
				) != nil {
					t.Fatal("key control unavailable")
				}
			case "action":
				control.domain.approval.action.ActionHash = strings.Repeat("a", 64)
			}
			if _, _, err := control.domain.request(
				t.Context(),
				Action{Kind: "cancel"},
				prompt,
			); !errors.Is(err, errBrowserEvidence) ||
				!control.domain.questions.cancelAt.IsZero() ||
				control.domain.approval.requestID != "" {
				t.Fatal("changed native evidence admitted cancellation or a grant", mode, err)
			}
		})
	}
}
