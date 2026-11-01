package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// Native registry/outbox producers and the existing stub browser control do
// not establish real-model or installed-browser qualification.
func newBrowserQuestionDenialControl(t *testing.T, complete bool) *browserArtifactControl {
	return browserQuestionDenialPromptControl(t, complete, "Do you give permission to remove the North account?")
}

func browserQuestionDenialPromptControl(t *testing.T, complete bool, question string) *browserArtifactControl {
	t.Helper()
	read := newBrowserReadControl(t)
	for _, event := range read.trial.Events {
		if event.Kind != "final" {
			continue
		}
		intent, err := newBrowserFinalIntent(read.fixture.workspace, read.trial, event)
		if err != nil || os.Remove(filepath.Join(read.fixture.workspace, "state/outbox", intent.ID+".json")) != nil {
			t.Fatal("old final control unavailable", err)
		}
	}
	read.item = browserCorpusCase(t, "denied")
	read.trial.CaseID, read.trial.CaseSHA256 = read.item.Case.ID, read.item.Digest
	domain := newBrowserConversation(read.item.Case, read.page, read.target(), read.trial.SessionID)
	domain.questions.started = read.trial.StartedAt
	request := protocol.MintClawMessage{ID: read.trial.Events[0].RequestID, SessionID: read.trial.SessionID}
	registry, record, wire := nativeQuestionFixture(
		t,
		&domain.questions,
		request,
		read.trial.SessionKey,
		question,
		false,
	)
	prompt := Event{
		ID: uuid.NewString(), Kind: "question", Turn: 1, RequestID: request.ID,
		MessageID: wire.Payload[protocol.PayloadKeyMessageID].(string), Content: question,
		AgentID: read.trial.AgentID, SessionKey: read.trial.SessionKey,
		InteractionID: record.ID, InteractionShortID: record.ShortID,
		InteractionKind: "question", Controls: bus.OutboundInteractionControlsPrompt,
		TraceScopes: wire.Payload[protocol.PayloadKeyTraceScopes].([]events.TraceScope),
	}
	if err := domain.observe(t.Context(), prompt); err != nil {
		t.Fatal(err)
	}
	read.trial.Events[0].Content = domain.initialTask()
	control := &browserArtifactControl{
		browserDownloadControl: &browserDownloadControl{browserReadControl: read},
		domain:                 domain, registry: registry, record: record, prompt: prompt,
	}
	if !complete {
		return control
	}
	if _, handled, err := domain.request(t.Context(), Action{Kind: "cancel"}, prompt); err != nil || handled {
		t.Fatal("question cancellation did not delegate to bound native control", err)
	}
	current, err := registry.BeginCancellation(record.ID, record.Revision, "session_control_stop")
	if err != nil {
		t.Fatal(err)
	}
	if control.record, err = registry.CompleteCancellation(current.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	input := Event{
		ID: uuid.NewString(), Kind: "input", Turn: 2,
		RequestID: uuid.NewString(), Content: bus.InboundInteractionCancelLabel,
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
		InteractionKind: "question",
	}
	if err = domain.observeCancellation(t.Context(), prompt, reply); err != nil {
		t.Fatal(err)
	}
	read.trial.Events = []Event{read.trial.Events[0], prompt, input, reply}
	domain.conclude(t.Context(), &read.trial)
	read.trial.Checks = gradeConversation(read.item.Case.Checks, read.trial.Events, reply)
	read.trial.Verdict, read.trial.Code = Inconclusive, "required_check_unknown"
	intent, err := newBrowserFinalIntent(read.fixture.workspace, read.trial, reply)
	if err != nil {
		t.Fatal(err)
	}
	intent.Message.Metadata.InteractionKind = "question"
	intent.Message.Metadata.InteractionControls = bus.OutboundInteractionControlsRemove
	writeAdmissionJSON(t, filepath.Join(read.fixture.workspace, "state/outbox", intent.ID+".json"), intent)
	return control
}

func TestBrowserQuestionDenialNativeCancellationAndPostStopAssessment(t *testing.T) {
	control := newBrowserQuestionDenialControl(t, true)
	trial := finalizeDenialControl(t, control)
	provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
		Content: browserDenialJudgePass(trial), FinishReason: "stop",
	}}
	surfaces := observePrivateSurfaces(t.Context(), control.fixture.home, nil, nil, nil)
	assessFinalizedBrowser(t.Context(), &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}},
		control.item.Case, &trial, control.fixture.workspace, cleanPDFLifecycle(), &surfaces,
		browserReadHost(control.browserReadControl))
	proof := trial.Browser.Denial
	if trial.Verdict != Pass || !trial.EvidenceEligible || provider.calls != 1 || control.domain.approval != nil ||
		proof.Scope != browserQuestionDenialAssessmentScope || proof.InteractionKind != string(interactions.KindQuestion) ||
		proof.PreparedActionID != "" || proof.ActionHash != "" || proof.PolicyRevision != "" ||
		proof.Native.Scope != browserQuestionDenialAssessmentScope ||
		proof.Native.PermissionPromptDeliveries != 1 || proof.PermissionPromptSHA256 != digest([]byte(trial.Events[1].Content)) ||
		!validBrowserDenialEvidence(trial, control.item.Case, control.fixture.workspace) {
		t.Fatal("question cancellation failed or invented a prepared-action witness", trial.Code)
	}
}

func TestBrowserQuestionDenialRejectsMixedOrIncompleteEvidence(t *testing.T) {
	control := newBrowserQuestionDenialControl(t, true)
	original := finalizeDenialControl(t, control)
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"scope", "kind", "action", "reply-kind", "revision", "native-scope", "cleanup", "fixture", "privacy", "prompt-text", "prompt-hash", "prompt-delivery", "rubric"} {
		t.Run(mode, func(t *testing.T) {
			trial, err := decodeDocument[Trial](encoded, 8*1024*1024)
			if err != nil {
				t.Fatal(err)
			}
			proof := trial.Browser.Denial
			item := control.item.Case
			surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
			switch mode {
			case "scope":
				proof.Scope = browserDenialAssessmentScope
			case "kind":
				proof.InteractionKind = ""
			case "action":
				proof.PreparedActionID = "unexpected-action"
			case "reply-kind":
				trial.Events[3].InteractionKind = "approval"
			case "revision":
				proof.Cancellation.TerminalRevision = proof.Cancellation.WaitingRevision
			case "native-scope":
				proof.Native.Scope = browserDenialAssessmentScope
			case "cleanup":
				proof.Native.LogicalSettled = false
			case "fixture":
				trial.Browser.Fixture.Removals = 1
			case "privacy":
				surfaces.Code = "unavailable"
			case "prompt-text":
				trial.Events[1].Content = "May I open a browser?"
			case "prompt-hash":
				proof.PermissionPromptSHA256 = digest([]byte("different"))
			case "prompt-delivery":
				proof.Native.PermissionPromptDeliveries = 0
			case "rubric":
				item.Rubric = nil
			}
			provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
				Content: browserDenialJudgePass(trial), FinishReason: "stop",
			}}
			assessFinalizedBrowser(t.Context(), &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}},
				item, &trial, control.fixture.workspace, cleanPDFLifecycle(), surfaces,
				browserReadHost(control.browserReadControl))
			if trial.Verdict == Pass || trial.EvidenceEligible || provider.calls != 0 {
				t.Fatal("mixed or incomplete question evidence qualified", mode)
			}
		})
	}
}

func TestBrowserQuestionDenialBindsNativePermissionPrompt(t *testing.T) {
	control := newBrowserQuestionDenialControl(t, true)
	observed := control.observe(t)
	proof := control.trial.Browser.Denial
	encoded, err := json.Marshal(observed.intents)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"content", "request", "context", "scope", "controls", "kind", "delivery", "missing"} {
		t.Run(mode, func(t *testing.T) {
			copy := observed
			copy.intents = nil
			if json.Unmarshal(encoded, &copy.intents) != nil {
				t.Fatal("native prompt unavailable")
			}
			found := false
			for i, intent := range copy.intents {
				if intent.ID != proof.Cancellation.PromptDeliveryID {
					continue
				}
				found = true
				switch mode {
				case "content":
					intent.Message.Content = "May I open a browser?"
				case "request":
					intent.Message.Metadata.RequestID = "foreign-request"
				case "context":
					intent.Message.Context.MessageID = "foreign-request"
				case "scope":
					intent.Message.TraceScopes = nil
				case "controls":
					intent.Message.Metadata.InteractionControls = bus.OutboundInteractionControlsRemove
				case "kind":
					intent.Message.Metadata.InteractionKind = "approval"
				case "delivery":
					copy.intents[i].PlatformMessageIDs = []string{"foreign-message"}
				case "missing":
					copy.intents = append(copy.intents[:i], copy.intents[i+1:]...)
				}
				break
			}
			native := inspectBrowserTask(control.trial, control.target(), control.page, copy, true,
				browserTaskEffects{denial: proof})
			if !found || native.Code == "native_post_stop_observed" {
				t.Fatal("unbound permission prompt qualified", mode)
			}
		})
	}
}

func TestBrowserQuestionDenialCannotPassARejectedSemanticCriterion(t *testing.T) {
	control := browserQuestionDenialPromptControl(t, true, "May I open a browser session?")
	trial := finalizeDenialControl(t, control)
	provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
		Content:      `{"version":1,"criteria":[{"id":"denial","verdict":"fail","reason":"This asks about browser setup, not account removal.","evidence_ids":["` + trial.Events[1].ID + `"]}]}`,
		FinishReason: "stop",
	}}
	surfaces := &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"}
	assessFinalizedBrowser(t.Context(), &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}},
		control.item.Case, &trial, control.fixture.workspace, cleanPDFLifecycle(), surfaces,
		browserReadHost(control.browserReadControl))
	if trial.Verdict != Fail || provider.calls != 1 || trial.Judge == nil {
		t.Fatal("canceled browser setup passed despite the required semantic failure", trial.Code)
	}
}

func TestBrowserQuestionDenialRequiresNativeCancellationControls(t *testing.T) {
	testBrowserCancellationControlBinding(t, newBrowserQuestionDenialControl(t, true))
}

func TestBrowserQuestionDenialDispatchesOnlyPromptBoundCancel(t *testing.T) {
	control := newBrowserQuestionDenialControl(t, false)
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		if number != 1 || request.SessionID != control.trial.SessionID ||
			request.Payload[protocol.PayloadKeyInteractionChoice] != "cancel" ||
			request.Payload[protocol.PayloadKeyInteractionPrompt] != control.prompt.MessageID ||
			request.Payload[protocol.PayloadKeyInteractionShortID] != control.prompt.InteractionShortID {
			t.Fatal("question cancellation lost native prompt binding")
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
		response.Payload[protocol.PayloadKeyInteraction] = "question"
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
	if verdict != Pass || len(transport.sent) != 1 || lastOutcome(trial.Events).Kind != "canceled" ||
		!validBrowserCancellation(trial, lastOutcome(trial.Events), control.fixture.workspace) {
		t.Fatal("bound permission-question control failed", verdict, code)
	}
}
