package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func ordinaryQuestionFixture(
	t *testing.T, domain *questionConversation, request protocol.MintClawMessage, privateOutbox bool,
) (*interactions.Registry, interactions.Record, protocol.MintClawMessage) {
	t.Helper()
	response := testResponse(request, 1)
	response.Payload[protocol.PayloadKeyFinal] = false
	response.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{
		{Workspace: domain.workspace, TurnID: request.ID},
	}
	response.Payload[protocol.PayloadKeyControls] = "prompt"
	response.Payload[protocol.PayloadKeyInteraction] = "question"
	route := interactions.Route{
		AgentID: domain.agent, SessionKey: "eval-session-key", RouteSessionKey: "eval-session-key",
		Channel: "mintclaw", ChatID: "mintclaw:" + domain.sessionID, ChatType: "direct", SenderID: "mintclaw-user",
	}
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(domain.workspace))
	record, err := registry.Create(interactions.CreateRequest{
		ID: "interaction_" + digest([]byte(domain.sessionID))[:24], Kind: interactions.KindQuestion, Route: route,
		Origin: interactions.Origin{
			TurnID: request.ID, ToolName: "request_user_input", ToolCallID: "ordinary-call",
			ExecutionContext: &bus.InboundContext{MessageID: request.ID},
		},
		Questions: []interactions.Question{{ID: "preference", Question: "Email or paper?"}},
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	content := "Email or paper?"
	if privateOutbox {
		content = "private-question-canary"
	}
	intent, err := outbox.NewMessageIntent(domain.workspace, outbox.Identity{
		SourceID: "ordinary-prompt", Channel: route.Channel, ChatID: route.ChatID, SessionKey: route.SessionKey,
	}, bus.OutboundMessage{
		AgentID: domain.agent, SessionKey: route.SessionKey, Content: content,
		Metadata: bus.OutboundMetadata{
			InteractionID: record.ID, InteractionShortID: record.ShortID,
			InteractionKind: "question", InteractionControls: bus.OutboundInteractionControlsPrompt,
		},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	box, err := outbox.Open(domain.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = box.Create(intent); err != nil {
		t.Fatal(err)
	}
	if _, err = box.BeginAttempt(intent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = box.MarkDelivered(intent.ID, outbox.Outcome{PlatformMessageIDs: []string{"prompt-1"}}); err != nil {
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
	response.Payload[protocol.PayloadKeyInteractionID] = record.ID
	response.Payload[protocol.PayloadKeyInteractionShortID] = record.ShortID
	return registry, record, response
}

func runOrdinaryQuestion(t *testing.T, mode string) (Trial, *fakeConversationTransport, LoadedCase) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	domain := &questionConversation{
		task: "Ask email or paper and wait.", home: home, workspace: workspace,
		sessionID: "eval-test-session", agent: "main", started: time.Now(),
		filter: strings.NewReplacer("private-question-canary", "redacted"),
	}
	transport := newFakeConversation()
	var registry *interactions.Registry
	var record interactions.Record
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		if number == 1 {
			var response protocol.MintClawMessage
			registry, record, response = ordinaryQuestionFixture(t, domain, request, mode == "private-outbox")
			if mode == "home-only-outbox" {
				directory := outbox.Path(domain.home)
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(
					filepath.Join(outbox.Path(domain.workspace), record.PromptDeliveryID+".json"),
					filepath.Join(directory, record.PromptDeliveryID+".json"),
				); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "foreign-owner" {
				response.Payload[protocol.PayloadKeySessionKey] = "foreign-session"
			}
			transport.events <- response
			return
		}
		if request.Payload[protocol.PayloadKeyInteractionPrompt] != "prompt-1" ||
			request.Payload[protocol.PayloadKeyInteractionShortID] != record.ShortID ||
			request.Payload[protocol.PayloadKeyInteractionChoice] != "cancel" {
			t.Fatal("cancellation was not bound to the actual native prompt")
		}
		if mode != "waiting-only" {
			var err error
			record, err = registry.BeginCancellation(record.ID, record.Revision, "session_control_stop")
			if err != nil {
				t.Fatal(err)
			}
			record, err = registry.CompleteCancellation(record.ID, record.Revision)
			if err != nil {
				t.Fatal(err)
			}
		}
		response := testResponse(request, number)
		delete(response.Payload, protocol.PayloadKeyTraceScopes)
		response.Payload[protocol.PayloadKeyControls] = "remove"
		response.Payload[protocol.PayloadKeyInteraction] = "question"
		if mode == "missing-controls" {
			delete(response.Payload, protocol.PayloadKeyControls)
		}
		switch mode {
		case "missing-reply-id":
			delete(response.Payload, protocol.PayloadKeyMessageID)
		case "oversized-reply-id":
			response.Payload[protocol.PayloadKeyMessageID] = strings.Repeat("r", 129)
		case "whitespace-reply-id":
			response.Payload[protocol.PayloadKeyMessageID] = "reply with spaces"
		}
		if mode == "private-registry" {
			path := interactions.WorkspaceStorePath(workspace)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.ReplaceAll(string(data), "Email or paper?", "private-question-canary"))
			if err = os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if mode == "stale-ack" {
			stale := testResponse(request, number)
			stale.Payload[protocol.PayloadKeyRequestID] = "stale-request"
			transport.events <- stale
		}
		transport.events <- response
	}
	item := testCase()
	item.Task, item.Driver.Steps = domain.task, []Step{
		{WaitFor: Expectation{Kind: "question"}, Action: Action{Kind: "cancel"}},
	}
	item.Checks = []Check{
		{ID: "observed", Kind: "interaction_observed"},
		{ID: "same", Kind: "same_session"},
		{ID: "canceled", Kind: "last_outcome", Expected: "canceled"},
	}
	item.Rubric = []Criterion{{ID: "coherent", Description: "The visible question is coherent.", Required: false}}
	if mode == "unsupported-action" {
		item.Driver.Steps[0].Action.Kind = "clarify"
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	loaded := LoadedCase{Case: item, Digest: digest(data)}
	trial := runDomainConversation(t.Context(), transport, loaded,
		domain.sessionID, domain.agent, workspace, conversationRoles{filter: domain.filter}, domain)
	return trial, transport, loaded
}

func TestOrdinaryQuestionCancellationRequiresNativeTerminalEvidence(t *testing.T) {
	for _, mode := range []string{
		"complete", "stale-ack", "waiting-only", "missing-controls", "foreign-owner",
		"private-registry", "private-outbox", "unsupported-action", "home-only-outbox",
		"missing-reply-id", "oversized-reply-id", "whitespace-reply-id",
	} {
		t.Run(mode, func(t *testing.T) {
			trial, transport, _ := runOrdinaryQuestion(t, mode)
			if mode == "complete" || mode == "stale-ack" {
				if trial.Verdict != Pass || trial.Cancellation == nil ||
					!validQuestionCancellation(trial, lastOutcome(trial.Events), trial.Cancellation.Workspace) ||
					len(transport.sent) != 2 || trial.RemoteState != "unknown" {
					t.Fatal("native cancellation failed or implied whole-runtime cleanup", trial.Verdict, trial.Code)
				}
			} else if trial.Verdict == Pass || trial.Cancellation != nil {
				t.Fatal("unsupported, private or unsettled evidence passed", trial.Verdict, trial.Code)
			}
			if strings.HasPrefix(mode, "private-") && trial.Code != "private_native_evidence" {
				t.Fatal("native privacy failure was softened", trial.Verdict, trial.Code)
			}
			if (mode == "unsupported-action" || mode == "home-only-outbox") && len(transport.sent) != 1 {
				t.Fatal("unsupported ordinary-question control was dispatched")
			}
		})
	}
}

func TestRetainedQuestionCancellationRejectsChangedIdentity(t *testing.T) {
	trial, _, item := runOrdinaryQuestion(t, "complete")
	summary := TrialSummary{
		ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256,
		Verdict: trial.Verdict, Code: trial.Code,
	}
	preset := Preset{ExpectedAgent: "main"}
	if !gradeEligible(trial, item, summary, preset, trial.Cancellation.Workspace) {
		t.Fatal("retained native cancellation could not be regraded")
	}
	for _, mutate := range []func(*QuestionCancellationEvidence){
		func(p *QuestionCancellationEvidence) { p.PromptMessageID = "stale-prompt" },
		func(p *QuestionCancellationEvidence) { p.CancelRequestID = "foreign-request" },
		func(p *QuestionCancellationEvidence) { p.ReplyMessageID = "foreign-reply" },
		func(p *QuestionCancellationEvidence) { p.Workspace = "/production" },
		func(p *QuestionCancellationEvidence) { p.TerminalRevision = p.WaitingRevision },
		func(p *QuestionCancellationEvidence) { p.OriginTurnID = "foreign-turn" },
	} {
		copy := trial
		proof := *trial.Cancellation
		copy.Cancellation = &proof
		mutate(&proof)
		if validQuestionCancellation(copy, lastOutcome(copy.Events), trial.Cancellation.Workspace) {
			t.Fatal("changed retained cancellation evidence admitted")
		}
		if gradeEligible(copy, item, summary, preset, trial.Cancellation.Workspace) {
			t.Fatal("changed cancellation evidence reached regrading")
		}
	}
	trial.Cancellation = nil
	if gradeEligible(trial, item, summary, preset, preset.ExpectedWorkspace) {
		t.Fatal("text-only cancellation was eligible for regrading")
	}
}
