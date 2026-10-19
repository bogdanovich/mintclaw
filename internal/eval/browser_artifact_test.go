package eval

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type browserArtifactControl struct {
	*browserDownloadControl
	domain          *browserConversation
	registry        *interactions.Registry
	record          interactions.Record
	prompt          Event
	approvalRequest protocol.MintClawMessage
}

func browserApprovalControl(
	t *testing.T, control *browserDownloadControl, prepared browser.Preparation,
) *browserArtifactControl {
	t.Helper()
	domain := newBrowserConversation(control.item.Case, control.page, control.target(), control.trial.SessionID)
	domain.questions.started = control.trial.StartedAt
	requestID := control.trial.Events[0].RequestID
	scope := events.TraceScope{Workspace: control.fixture.workspace, TurnID: "native-turn-" + requestID}
	hash, err := interactions.HashArguments(
		control.fixture.workspace,
		tools.BrowserActionApprovalArguments(prepared),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(control.fixture.workspace))
	description := "Download the fixture report"
	if control.item.Case.Browser.Scenario == "denied" {
		description = "Remove the fixture account"
	}
	route := interactions.Route{
		AgentID:         control.trial.AgentID,
		SessionKey:      control.trial.SessionKey,
		RouteSessionKey: control.trial.SessionKey,
		Channel:         "mintclaw",
		ChatID:          "mintclaw:" + control.trial.SessionID,
		ChatType:        "direct",
		SenderID:        "mintclaw-user",
	}
	record, err := registry.Create(interactions.CreateRequest{
		ID: "interaction_" + uuid.NewString(), Kind: interactions.KindApproval, Route: route,
		Origin: interactions.Origin{
			ToolName: "browser_act", ToolCallID: "fixture-download-call", ExecutionID: "fixture-runtime-execution",
			TurnID: scope.TurnID, ArgumentHash: hash, ExecutionContext: &bus.InboundContext{
				Channel: route.Channel, ChatID: route.ChatID, ChatType: route.ChatType, SenderID: route.SenderID,
				ActorID: "mintclaw-user", MessageID: requestID, ClientSessionID: control.trial.SessionID,
			},
		}, ApprovalAction: description, ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := outbox.NewMessageIntent(control.fixture.workspace, outbox.Identity{
		SourceID: "download-prompt", Channel: route.Channel, ChatID: route.ChatID, SessionKey: route.SessionKey,
	}, bus.OutboundMessage{
		AgentID: route.AgentID, SessionKey: route.SessionKey, Content: description + "?",
		TraceScopes: []events.TraceScope{scope},
		Metadata: bus.OutboundMetadata{
			InteractionID: record.ID, InteractionShortID: record.ShortID, InteractionKind: "approval",
			InteractionControls: bus.OutboundInteractionControlsPrompt,
		},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	box, err := outbox.Open(control.fixture.workspace)
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
		outbox.Outcome{PlatformMessageIDs: []string{"native-download-prompt"}},
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
	prompt := Event{
		ID:                 uuid.NewString(),
		Turn:               1,
		Kind:               "approval",
		RequestID:          requestID,
		MessageID:          "native-download-prompt",
		AgentID:            route.AgentID,
		SessionKey:         route.SessionKey,
		InteractionID:      record.ID,
		InteractionShortID: record.ShortID,
		InteractionKind:    "approval",
		Controls:           "prompt",
		TraceScopes:        []events.TraceScope{scope},
	}
	return &browserArtifactControl{
		browserDownloadControl: control,
		domain:                 domain,
		registry:               registry,
		record:                 record,
		prompt:                 prompt,
	}
}

func newBrowserArtifactControl(t *testing.T) *browserArtifactControl {
	t.Helper()
	var artifact *browserArtifactControl
	control := browserDownloadControlWithHook(t, func(control *browserDownloadControl, prepared browser.Preparation) {
		artifact = browserApprovalControl(t, control, prepared)
		if err := artifact.domain.observeApproval(t.Context(), artifact.prompt); err != nil {
			t.Fatal("approval observation", err)
		}
		request, err := artifact.domain.approveDownload(artifact.prompt)
		if err != nil {
			t.Fatal("native bound approval", err)
		}
		artifact.approvalRequest = request
		record, err := artifact.registry.ClaimAnswer(artifact.record.ID, artifact.record.Revision, interactions.Answer{
			Text: "allow_once", MessageID: request.ID, ResponseMessageID: artifact.prompt.MessageID,
		}, interactions.OutcomeAllowed)
		if err != nil {
			t.Fatal(err)
		}
		record, err = artifact.registry.MarkResuming(record.ID, record.Revision)
		if err != nil {
			t.Fatal(err)
		}
		record, err = artifact.registry.ConsumeApproval(record.ID, record.Revision, record.Origin.ToolCallID,
			record.Origin.ToolName, record.Origin.ArgumentHash)
		if err != nil {
			t.Fatal(err)
		}
		artifact.record = record
		page := httptest.NewRecorder()
		control.page.ServeHTTP(page, httptest.NewRequest(http.MethodGet, control.page.origin+control.page.path, nil))
		if page.Code != http.StatusOK {
			t.Fatal("fixture page unavailable")
		}
	})
	artifact.browserDownloadControl = control
	var err error
	artifact.record, err = artifact.registry.Resolve(artifact.record.ID, artifact.record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	old := control.trial.Events
	intent, err := newBrowserFinalIntent(control.fixture.workspace, control.trial, old[1])
	if err != nil || os.Remove(filepath.Join(control.fixture.workspace, "state/outbox", intent.ID+".json")) != nil {
		t.Fatal("old final control unavailable")
	}
	firstFinal := old[1]
	firstFinal.RequestID, firstFinal.Turn = artifact.approvalRequest.ID, 2
	lastInput, lastFinal := old[2], old[3]
	lastInput.Turn, lastFinal.Turn = 3, 3
	control.trial.Events = []Event{
		old[0],
		artifact.prompt,
		{
			ID:        uuid.NewString(),
			Kind:      "input",
			Turn:      2,
			RequestID: artifact.approvalRequest.ID,
			Content:   "Allow reviewed download once.",
		},
		firstFinal,
		lastInput,
		lastFinal,
	}
	control.writeFinals(t)
	return artifact
}

func (control *browserArtifactControl) collect(t *testing.T) (*privateReport, Trial) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mintclaw/media/fixture" || r.Header.Get("Authorization") != "Bearer test-channel-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write(control.page.file)
	}))
	t.Cleanup(server.Close)
	parsed, _ := url.Parse(server.URL)
	_, port, _ := net.SplitHostPort(parsed.Host)
	control.fixture.cfg.Gateway.Host, control.fixture.cfg.Gateway.Port = "127.0.0.1", mustArtifactPort(t, port)
	message := protocol.MintClawMessage{
		Type: protocol.TypeMessageCreate, SessionID: control.trial.SessionID,
		Payload: map[string]any{
			protocol.PayloadKeyMessageID:   control.intent.PlatformMessageIDs[0],
			protocol.PayloadKeyAgentID:     control.trial.AgentID,
			protocol.PayloadKeySessionKey:  control.trial.SessionKey,
			protocol.PayloadKeyDeliveryID:  control.intent.ID,
			protocol.PayloadKeyTraceScopes: control.intent.Media.TraceScopes,
			"attachments": []any{
				map[string]any{"type": "file", "content_type": "text/csv", "url": "/mintclaw/media/fixture"},
			},
		},
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded protocol.MintClawMessage
	if json.Unmarshal(encoded, &decoded) != nil || control.domain.wire.observe(decoded) != nil {
		t.Fatal("wire control unavailable")
	}
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
	trial := control.trial
	trial.Checks = gradeConversation(control.item.Case.Checks, trial.Events, lastOutcome(trial.Events))
	trial.Verdict, trial.Code = Inconclusive, "required_check_unknown"
	control.domain.collectArtifact(t.Context(), &trial, writer, true)
	if trial.Browser == nil || trial.Browser.Artifact == nil || trial.Browser.Artifact.Code != "artifact_collected" {
		t.Fatal("actual bounded CSV was not collected", trial.Code)
	}
	return writer, trial
}

func mustArtifactPort(t *testing.T, value string) int {
	t.Helper()
	port, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func (control *browserArtifactControl) finalized(t *testing.T) (*privateReport, Trial) {
	t.Helper()
	writer, trial := control.collect(t)
	name := "trial-" + trial.ID + ".json"
	if writer.write("pending-"+name, trial) != nil {
		t.Fatal("pending artifact unavailable")
	}
	pending := browserRun{}
	pending.retain(control.item.Case, trial, control.page)
	entry := pending.trials[trial.ID]
	entry.conversation = control.domain
	pending.trials[trial.ID] = entry
	result := RunReport{
		TargetLifecycle: cleanPDFLifecycle(),
		PrivateSurfaces: &PrivateSurfaceObservation{Scope: "owned_profile_files_v1", Code: "observed"},
		BrowserFixtures: browserReadHost(control.browserReadControl),
		Trials: []TrialSummary{{
			ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256, Repetition: trial.Repetition,
			Verdict: trial.Verdict, Code: trial.Code, DurationMS: trial.DurationMS, File: name,
		}},
	}
	if err := pending.finish(context.Background(), control.target(), writer, &result, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	return writer, comparisonTrial(t, RunReport{ReportDir: writer.dir, Trials: result.Trials}, 0)
}

func browserArtifactJudgePass(trial Trial) string {
	return `{"version":1,"criteria":[{"id":"truthful","verdict":"pass","reason":"The requested file was delivered.","evidence_ids":["` + trial.Events[3].ID + `"]}]}`
}

func TestBrowserArtifactNativeApprovalBytesWireAndPostStopJudge(t *testing.T) {
	control := newBrowserArtifactControl(t)
	_, trial := control.finalized(t)
	provider := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
		Content: browserArtifactJudgePass(trial), FinishReason: "stop",
	}}
	judge := &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}
	pending := browserRun{trials: map[string]browserPendingTrial{trial.ID: {conversation: control.domain}}}
	surfaces := observePrivateSurfaces(t.Context(), control.fixture.home, nil, nil, nil,
		pending.approvalKeyWitness(control.fixture.home))
	assessFinalizedBrowser(t.Context(), judge, control.item.Case, &trial, control.fixture.workspace,
		cleanPDFLifecycle(), &surfaces,
		browserReadHost(control.browserReadControl))
	if trial.Verdict != Pass || !trial.EvidenceEligible || provider.calls != 1 ||
		!validBrowserArtifactEvidence(trial, control.item.Case, control.fixture.workspace) {
		t.Fatalf("complete artifact path was not graded: %s %#v", trial.Code, trial.Browser)
	}
}
