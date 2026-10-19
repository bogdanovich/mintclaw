package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestPDFConversationSuppliesPrivateHandleWithoutReportingValueOrClaimingArtifact(t *testing.T) {
	prepared := newPDFTrialFixture(t)
	defer func() { _ = prepared.release(true) }()
	_, prompt, _ := nativePDFPromptFixture(t, prepared)
	item := prepared.item
	item.Case.Driver.Mode, item.Case.Driver.Persona = "scripted", ""
	item.Case.Driver.Steps = []Step{{
		WaitFor: Expectation{Kind: "question"},
		Action:  Action{Kind: "protected_answer", Fact: "contact-reference"},
	}}
	item.Case.Rubric = nil
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		response := testResponse(request, number)
		response.Payload[protocol.PayloadKeyAgentID] = prepared.agent
		response.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{{
			Workspace: prepared.workspace, TurnID: request.ID,
		}}
		if number == 1 {
			response.Payload[protocol.PayloadKeyControls] = "prompt"
			response.Payload[protocol.PayloadKeyInteraction] = "question"
			response.Payload[protocol.PayloadKeyInteractionID] = prompt.InteractionID
			response.Payload[protocol.PayloadKeyInteractionShortID] = prompt.InteractionShortID
			response.Payload[protocol.PayloadKeyMessageID] = prompt.MessageID
		}
		transport.events <- response
	}
	trial := runDomainConversation(t.Context(), transport, item, prepared.sessionID,
		prepared.agent, prepared.workspace, conversationRoles{}, &pdfConversation{prepared: prepared})
	value := prepared.item.Case.Driver.Facts["contact-reference"]
	data, err := json.Marshal(trial)
	if err != nil || bytes.Contains(data, []byte(value)) || trial.Verdict != Inconclusive ||
		len(transport.sent) != 2 || trial.PDF == nil || trial.PDF.JobID == "" || trial.EvidenceEligible ||
		!strings.HasPrefix(payloadString(transport.sent[1].Payload, protocol.PayloadKeyContent), "/answer ") ||
		!strings.Contains(payloadString(transport.sent[1].Payload, protocol.PayloadKeyContent), value) {
		t.Fatal("protected input was leaked, not bound, or mistaken for task success", trial.Code)
	}
	if !strings.Contains(trial.Events[2].Content, "protected answer supplied") ||
		payloadString(transport.sent[0].Payload, protocol.PayloadKeyContent) != prepared.task ||
		transport.sent[0].SessionID != transport.sent[1].SessionID {
		t.Fatal("private input projection or same-session task binding lost")
	}
}

func TestPDFConversationRejectsPrivateAuxiliaryBeforeDiscardingIt(t *testing.T) {
	prepared := newPDFTrialFixture(t)
	defer func() { _ = prepared.release(true) }()
	item := prepared.item
	item.Case.Driver.Mode, item.Case.Driver.Persona = "scripted", ""
	item.Case.Driver.Steps = []Step{{
		WaitFor: Expectation{Kind: "final"},
		Action:  Action{Kind: "message", Text: "Continue"},
	}}
	item.Case.Rubric = nil
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		response := testResponse(request, number)
		response.Payload[protocol.PayloadKeyKind] = protocol.MessageKindToolCalls
		response.Payload["hidden"] = prepared.item.Case.Driver.Facts["contact-reference"]
		transport.events <- response
	}
	trial := runDomainConversation(t.Context(), transport, item, prepared.sessionID,
		prepared.agent, prepared.workspace, conversationRoles{}, &pdfConversation{prepared: prepared})
	data, _ := json.Marshal(trial)
	if trial.Verdict != Fail || trial.Code != "private_wire_evidence" ||
		bytes.Contains(data, []byte(prepared.item.Case.Driver.Facts["contact-reference"])) {
		t.Fatal("discarded auxiliary frame hid or reported private data", trial.Code)
	}
}

func TestPDFWireInspectionSeparatesLeaksFromUnscannableEvidence(t *testing.T) {
	facts := map[string]string{"contact": "MC_EVAL_1122334455667788"}
	for _, test := range []struct {
		data string
		want error
	}{
		{`{"type":"pong","value":"MC_EVAL_1122334455667788","value":"safe"}`, ErrPrivateEvidence},
		{`{"safe":true} trailing`, ErrPDFTrial},
		{`{"truncated":`, ErrPDFTrial},
		{`{"content":"opaque\\prose"}`, ErrPDFTrial},
		{`{"type":"pong","content":"ordinary text"}`, nil},
	} {
		if err := inspectPDFWire([]byte(test.data), facts, nil); !errors.Is(err, test.want) {
			t.Fatal("wire inspection confused a leak, unknown, and safe evidence", err)
		}
	}
}

func TestPDFWireCredentialInspectionPrecedesDuplicateMapProjection(t *testing.T) {
	filter := strings.NewReplacer("private-test-credential", "[redacted]")
	err := inspectPDFWire([]byte(`{"credential":"private-test-credential","credential":"safe"}`), nil, filter)
	if !errors.Is(err, ErrPrivateEvidence) {
		t.Fatal("raw duplicate JSON key hid a configured credential")
	}
}

func TestPDFConversationNeverSendsOrReportsPrivatePlaintextAndGenerationMarker(t *testing.T) {
	prepared := newPDFTrialFixture(t)
	defer func() { _ = prepared.release(true) }()
	_, prompt, _ := nativePDFPromptFixture(t, prepared)
	for _, text := range []string{prepared.item.Case.Driver.Facts["contact-reference"], generatePrivateFact} {
		transport := newFakeConversation()
		trial := Trial{SessionID: prepared.sessionID, Events: []Event{}}
		flow := conversation{
			transport: transport, trial: &trial, facts: prepared.item.Case.Driver.Facts,
			domain: &pdfConversation{prepared: prepared},
		}
		verdict, code := flow.turn(t.Context(), prepared.item.Case.Limits,
			Action{Kind: "message", Text: text}, prompt, 2)
		if verdict != Fail || code != "private_input" || len(transport.sent) != 0 || len(trial.Events) != 0 {
			t.Fatal("unprotected private value or generation marker reached wire/evidence", code)
		}
	}
}

func TestRunPDFActuallySendsOwnedFixtureWithoutClaimingDomainQualification(t *testing.T) {
	fixture := newAdmissionFixture(t)
	enableAdmissionPDF(t, fixture)
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	items, err := Select(corpus, Selection{IDs: []string{"pdf.loan.fill"}})
	if err != nil {
		t.Fatal(err)
	}
	items[0].Case.Driver.Mode, items[0].Case.Driver.Persona = "scripted", ""
	items[0].Case.Driver.Steps = []Step{{
		WaitFor: Expectation{Kind: "final"},
		Action:  Action{Kind: "message", Text: "Please continue"},
	}}
	items[0].Case.Rubric = nil
	result, err := Run(t.Context(), fixture.preset, items, RunOptions{
		Home: fixture.home, OutputParent: fixture.output,
	})
	if !errors.Is(err, ErrNotPassed) || !result.Complete || result.Verdict != Inconclusive ||
		fixture.tasks.Load() != 2 || len(result.Trials) != 1 || result.Trials[0].Code == "domain_evidence_unavailable" {
		t.Fatal("admitted PDF was skipped or fabricated text became task success", err, result.Verdict)
	}
	first := <-fixture.requests
	second := <-fixture.requests
	content := payloadString(first.Payload, protocol.PayloadKeyContent)
	if !strings.Contains(content, fixture.workspace+"/.eval-trials/"+first.SessionID+"/source.pdf") ||
		strings.Contains(content, "{{fixture}}") || first.ID == second.ID || first.SessionID != second.SessionID {
		t.Fatal("task did not use a fresh owned fixture and sequential session")
	}
	assertPrivateReport(t, result)
}

func enableAdmissionPDF(t *testing.T, fixture *admissionFixture) {
	t.Helper()
	fixture.preset.Capabilities = append(fixture.preset.Capabilities, "pdf")
	fixture.cfg.Tools.Document.Enabled, fixture.cfg.Tools.Document.AuditModel = true, "eval-model"
	fixture.cfg.Agents.List[0].ToolPolicy.Allow = append(
		fixture.cfg.Agents.List[0].ToolPolicy.Allow, "document", tools.BM25SearchToolName,
	)
	fixture.writeConfig(t)
	profile, err := readPreparedProfile(fixture.preset, fixture.home)
	if err != nil {
		t.Fatal("PDF file profile not admitted", err)
	}
	if err = validateConversationProfile(profile.config, fixture.preset, fixture.workspace); err != nil {
		t.Fatal("PDF capability policy not admitted", err)
	}
	fingerprint, err := preparedRuntimeFingerprint(profile.config)
	if err != nil {
		t.Fatal(err)
	}
	fixture.probe.ConfigSHA256 = fingerprint
}

func TestPreparedPDFAdmissionRequiresExactDocumentAuditAndToolPolicy(t *testing.T) {
	fixture := newAdmissionFixture(t)
	enableAdmissionPDF(t, fixture)
	if _, err := admitPreparedTarget(t.Context(), fixture.preset, fixture.home); err != nil {
		t.Fatal("narrow isolated PDF policy not admitted", err)
	}
	for _, mutate := range []func(*admissionFixture){
		func(f *admissionFixture) { f.cfg.Tools.Document.AuditModel = "unknown-model" },
		func(f *admissionFixture) { f.cfg.Tools.Document.Enabled = false },
		func(f *admissionFixture) { f.cfg.Agents.List[0].ToolPolicy.Allow = []string{"request_user_input"} },
		func(f *admissionFixture) { f.cfg.Tools.Exec.Enabled = true },
		func(f *admissionFixture) { f.cfg.Tools.Document.AuditEquivalentFallbacks = []string{"eval-model"} },
	} {
		other := newAdmissionFixture(t)
		enableAdmissionPDF(t, other)
		mutate(other)
		if validateConversationProfile(other.cfg, other.preset, other.workspace) == nil {
			t.Fatal("PDF capability widened tool or audit admission")
		}
	}
}
