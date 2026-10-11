package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func TestPDFOrdinaryQuestionCancellationUsesNativeWitness(t *testing.T) {
	for _, mode := range []string{
		"complete", "waiting-only", "missing-controls", "foreign-reply", "private-registry",
		"slash-command", "unsupported-action",
	} {
		t.Run(mode, func(t *testing.T) {
			prepared := newPDFTrialFixture(t)
			defer func() { _ = prepared.release(true) }()
			prepared.item.Case.Driver.Mode, prepared.item.Case.Driver.Persona = "scripted", ""
			prepared.item.Case.Driver.Steps = []Step{{
				WaitFor: Expectation{Kind: "question"}, Action: Action{Kind: "cancel"},
			}}
			prepared.item.Case.Rubric = nil
			if mode == "slash-command" {
				prepared.item.Case.Driver.Steps[0].Action = Action{Kind: "message", Text: "/stop"}
			}
			if mode == "unsupported-action" {
				prepared.item.Case.Driver.Steps[0].Action = Action{Kind: "clarify"}
			}
			domain := &pdfConversation{prepared: prepared}
			question := &questionConversation{
				home: prepared.home, workspace: prepared.workspace,
				sessionID: prepared.sessionID, agent: prepared.agent, started: prepared.started,
			}
			transport := newFakeConversation()
			var registry *interactions.Registry
			var record interactions.Record
			transport.onSend = func(request protocol.MintClawMessage, number int) {
				if number == 1 {
					var response protocol.MintClawMessage
					registry, record, response = ordinaryQuestionFixture(t, question, request, false)
					response.Payload[protocol.PayloadKeyAgentID] = prepared.agent
					transport.events <- response
					return
				}
				if number != 2 || request.Payload[protocol.PayloadKeyInteractionChoice] != "cancel" ||
					request.Payload[protocol.PayloadKeyInteractionPrompt] != "prompt-1" ||
					request.Payload[protocol.PayloadKeyInteractionShortID] != record.ShortID {
					t.Fatal("PDF cancellation was replayed, unbound, or granted a write")
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
				if mode == "private-registry" {
					path := interactions.WorkspaceStorePath(prepared.workspace)
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					value := prepared.item.Case.Driver.Facts["contact-reference"]
					data = []byte(strings.ReplaceAll(string(data), "Email or paper?", value))
					if err = os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				response := testResponse(request, number)
				response.Payload[protocol.PayloadKeyAgentID] = prepared.agent
				delete(response.Payload, protocol.PayloadKeyTraceScopes)
				response.Payload[protocol.PayloadKeyControls] = "remove"
				response.Payload[protocol.PayloadKeyInteraction] = "question"
				if mode == "missing-controls" {
					delete(response.Payload, protocol.PayloadKeyControls)
				}
				if mode == "foreign-reply" {
					response.Payload[protocol.PayloadKeySessionKey] = "foreign-session"
				}
				transport.events <- response
			}
			trial := runDomainConversation(t.Context(), transport, prepared.item, prepared.sessionID,
				prepared.agent, prepared.workspace, conversationRoles{}, domain)
			wantSent := 2
			if mode == "slash-command" || mode == "unsupported-action" {
				wantSent = 1
			}
			if len(transport.sent) != wantSent || trial.Verdict == Pass || trial.EvidenceEligible ||
				trial.RemoteState != "unknown" {
				t.Fatal(
					"control witness replayed input or implied PDF/runtime qualification",
					trial.Verdict,
					trial.Code,
				)
			}
			if mode == "complete" {
				if trial.ExecutionVerdict != Pass || trial.Cancellation == nil ||
					!validQuestionCancellation(trial, lastOutcome(trial.Events), prepared.workspace) ||
					lastOutcome(trial.Events).Kind != "canceled" || trial.PDF == nil || trial.PDF.State != "unknown" {
					t.Fatal("ordinary PDF question lost its native terminal cancellation witness", trial.Code)
				}
			} else if trial.Cancellation != nil || trial.ExecutionVerdict == Pass {
				t.Fatal("unsettled, foreign, or incomplete cancellation was admitted", trial.Code)
			}
			data, err := json.Marshal(trial)
			if err != nil || bytes.Contains(data, []byte(prepared.item.Case.Driver.Facts["contact-reference"])) {
				t.Fatal("private fact leaked through retained ordinary-question evidence")
			}
			if mode == "private-registry" && (trial.Verdict != Fail || trial.Code != "private_native_evidence") {
				t.Fatal("ordinary PDF cancellation softened a raw private-surface failure", trial.Code)
			}
		})
	}
}

func TestPDFOrdinaryObserverNeverAdmitsProtectedJobCancellation(t *testing.T) {
	prepared := newPDFTrialFixture(t)
	defer func() { _ = prepared.release(true) }()
	_, prompt, _ := nativePDFPromptFixture(t, prepared)
	domain := &pdfConversation{prepared: prepared}
	if err := domain.observe(t.Context(), prompt); err != nil {
		t.Fatal("protected native prompt was not observed", err)
	}
	if domain.jobID == "" || domain.ordinary != nil {
		t.Fatal("protected PDF question was confused with an ordinary question")
	}
	if err := domain.observeCancellation(t.Context(), prompt, Event{}); !errors.Is(err, ErrPDFTrial) {
		t.Fatal("ordinary witness admitted an unproved protected PDF cancellation", err)
	}
}
