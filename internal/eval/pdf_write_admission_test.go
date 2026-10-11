package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

type pdfPassingAuditor struct{}

func (pdfPassingAuditor) AuditForm(
	context.Context,
	string,
	document.FormAuditView,
) (document.FormAuditProposal, error) {
	return document.FormAuditProposal{Decision: document.FormAuditPass}, nil
}

func reviewedPDFControl(t *testing.T) (*pdfConversation, *document.FormJobStore, document.FormFieldsFacts, Event) {
	t.Helper()
	trial := newPDFTrialFixture(t)
	t.Cleanup(func() { _ = trial.release(true) })
	question, _, store := nativePDFPromptFixture(t, trial)
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(trial.workspace))
	if _, err := registry.Cancel(question.ID, question.Revision, "fixture_question_finished"); err != nil {
		t.Fatal(err)
	}
	owner, err := document.FormJobOwnerFromProtectedRoute(trial.workspace, question.Route)
	if err != nil {
		t.Fatal(err)
	}
	schema := document.FormFieldsFacts{
		SourceSHA256: trial.item.Case.Fixtures[0].SHA256,
		Backend: document.BackendIdentity{
			Name: document.PDFCPUBackendName, Version: document.PDFCPUBackendVersion,
			Package: document.PDFCPUBackendPackage, PackageRevision: document.PDFCPUBackendVersion,
			Role: "production", IsolationMode: document.WorkerIsolationMode,
		},
		Limits: document.FormFieldLimits{
			MaxFields: document.DefaultMaxFormFields, MaxWidgets: document.DefaultMaxFieldWidgets,
			MaxOptions: document.DefaultMaxFieldOptions, MaxTextBytes: document.DefaultMaxFieldTextBytes,
			MaxReportBytes: document.DefaultMaxFormReportBytes,
		},
		Fields: []document.FormField{{
			ID: "field_" + strings.Repeat("1", 64), Name: "contact_reference", Kind: document.FormFieldText,
			Widgets: []document.FormFieldWidget{{ID: "widget_" + strings.Repeat("2", 64), Page: 1, Ordinal: 1}},
		}},
	}
	policy := document.FormAuditPolicy{PrimaryModel: "audit-control", PrimaryIdentity: "audit-control-v1"}
	policyDigest, err := policy.Revision()
	if err != nil {
		t.Fatal(err)
	}
	schemaDigest, err := document.FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := document.FormFieldsBackendRevision(schema)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Create(t.Context(), document.FormJobCreateRequest{
		Owner: owner, StartIdempotencyKey: "review-control", SourceRef: "media://eval-source",
		SourceDigest: schema.SourceSHA256, FieldSchemaDigest: schemaDigest,
		BackendRevision: backend, AuditPolicyRevision: policyDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err = store.AppendValue(t.Context(), document.FormJobAppendValueRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: owner,
		FieldID: schema.Fields[0].ID, IdempotencyKey: "review-value",
		Value: document.FormProtectedValue{
			Kind: document.ProtectedValueText,
			Text: trial.item.Case.Driver.Facts["contact-reference"],
		},
		State: document.FormValueSupplied, Source: document.FormValueSourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := store.MapFormField(t.Context(), document.FormFieldMappingRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: owner, Schema: schema,
		FieldID: schema.Fields[0].ID, SourceEventID: job.Fields[0].EventID, IdempotencyKey: "review-mapping",
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := store.ReviewFormJob(t.Context(), document.FormReviewRequest{
		JobID: job.JobID, ExpectedRevision: mapped.Job.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: pdfPassingAuditor{},
	})
	if err != nil || reviewed.Job.State != document.FormJobReviewReady {
		t.Fatal("native review control not ready", err)
	}
	domain := &pdfConversation{prepared: trial, jobID: job.JobID, owner: owner}
	final := Event{Kind: "final", AgentID: trial.agent, SessionKey: owner.RouteSessionKey}
	return domain, store, schema, final
}

func approvalPDFControl(t *testing.T, domain *pdfConversation, store *document.FormJobStore,
	schema document.FormFieldsFacts, arguments map[string]any,
) Event {
	t.Helper()
	job, err := domain.prepared.observeExistingJob(domain.jobID, domain.owner)
	if err != nil {
		t.Fatal(err)
	}
	_, binding, err := store.PrepareFormCommitApproval(t.Context(), document.FormCommitRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: domain.owner, Schema: schema,
		AuditPolicyRevision: job.AuditPolicyRevision, OutputPolicyRevision: job.FormOutputPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if arguments == nil {
		arguments = binding.ApprovalArguments()
	}
	hash, err := interactions.HashArguments(domain.prepared.workspace, arguments)
	if err != nil {
		t.Fatal(err)
	}
	route := interactions.Route{
		AgentID: domain.prepared.agent, SessionKey: domain.owner.RouteSessionKey,
		RouteSessionKey: domain.owner.RouteSessionKey, Channel: "mintclaw",
		ChatID: "mintclaw:" + domain.prepared.sessionID, ChatType: "direct", SenderID: "mintclaw-user",
	}
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(domain.prepared.workspace))
	record, err := registry.Create(interactions.CreateRequest{
		ID: "interaction_eval_write111", Kind: interactions.KindApproval, Route: route,
		Origin: interactions.Origin{
			ToolName: "document", ToolCallID: "commit-call", TurnID: "commit-turn",
			ArgumentHash: hash, ExecutionContext: &bus.InboundContext{
				Channel: route.Channel, ChatID: route.ChatID, ChatType: route.ChatType, SenderID: route.SenderID,
				MessageID: domain.confirmationRequestID, ClientSessionID: domain.prepared.sessionID,
			},
		}, ApprovalAction: "Create and deliver the reviewed PDF", ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := outbox.NewMessageIntent(domain.prepared.workspace, outbox.Identity{
		SourceID: "write-prompt", Channel: route.Channel, ChatID: route.ChatID, SessionKey: route.SessionKey,
	}, bus.OutboundMessage{
		AgentID: route.AgentID, SessionKey: route.SessionKey, Content: "Create and deliver the reviewed PDF?",
		Metadata: bus.OutboundMetadata{
			InteractionID: record.ID, InteractionShortID: record.ShortID,
			InteractionKind: "approval", InteractionControls: bus.OutboundInteractionControlsPrompt,
		},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	box, err := outbox.Open(domain.prepared.home)
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
		outbox.Outcome{PlatformMessageIDs: []string{"write-wire-prompt"}},
	); err != nil {
		t.Fatal(err)
	}
	record, err = registry.BindPromptDelivery(record.ID, record.Revision, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.MarkWaiting(record.ID, record.Revision); err != nil {
		t.Fatal(err)
	}
	return Event{
		Kind: "approval", AgentID: route.AgentID, SessionKey: route.SessionKey,
		InteractionID: record.ID, InteractionShortID: record.ShortID, MessageID: "write-wire-prompt",
	}
}

func TestPDFWriteAdmissionRequiresNativeReviewSeparateConfirmationAndExactArguments(t *testing.T) {
	domain, store, schema, final := reviewedPDFControl(t)
	confirmation, err := domain.confirmReview(final)
	if err != nil || confirmation.ID == "" || confirmation.SessionID != domain.prepared.sessionID {
		t.Fatal("native reviewed fixture refused confirmation", err)
	}
	prompt := approvalPDFControl(t, domain, store, schema, nil)
	path := filepath.Join(domain.prepared.workspace, "state", "interaction_hmac.key")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	request, err := domain.approveReviewedWrite(prompt)
	if err != nil || payloadString(request.Payload, protocol.PayloadKeyInteractionChoice) != "allow_once" ||
		payloadString(request.Payload, protocol.PayloadKeyInteractionShortID) != prompt.InteractionShortID ||
		payloadString(request.Payload, protocol.PayloadKeyInteractionPrompt) != prompt.MessageID ||
		request.SessionID != domain.prepared.sessionID || domain.operationID == "" {
		t.Fatal("reviewed write not bound to the current native prompt", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("approval observation changed the native key")
	}
	if _, err = domain.approveReviewedWrite(prompt); !errors.Is(err, ErrPDFTrial) {
		t.Fatal("same approval was constructed twice")
	}
	job, err := domain.prepared.observeExistingJob(domain.jobID, domain.owner)
	if err != nil || job.State != document.FormJobAwaitingApproval {
		t.Fatal("request construction itself consumed approval or changed the job", err)
	}
}

func TestPDFWriteAdmissionRefusesUnconfirmedOrWrongNativeArguments(t *testing.T) {
	for _, name := range []string{
		"no confirmation", "wrong arguments", "changed review", "foreign prompt", "foreign confirmation", "no key",
		"native correction",
	} {
		t.Run(name, func(t *testing.T) {
			domain, store, schema, final := reviewedPDFControl(t)
			if name != "no confirmation" {
				if _, err := domain.confirmReview(final); err != nil {
					t.Fatal(err)
				}
			}
			var arguments map[string]any
			if name == "wrong arguments" {
				arguments = map[string]any{"action": "fill", "source": "another-source"}
			}
			prompt := approvalPDFControl(t, domain, store, schema, arguments)
			switch name {
			case "changed review":
				domain.review.ReviewDigest = strings.Repeat("9", 64)
			case "foreign prompt":
				prompt.SessionKey = "foreign-session"
			case "foreign confirmation":
				domain.confirmationRequestID = "another-user-input"
			case "native correction":
				correctPDFControl(t, domain, store, schema)
			case "no key":
				if err := os.Remove(
					filepath.Join(domain.prepared.workspace, "state", "interaction_hmac.key"),
				); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := domain.approveReviewedWrite(prompt); !errors.Is(err, ErrPDFTrial) {
				t.Fatal("unconfirmed or mismatched write admitted", err)
			}
		})
	}
}

func correctPDFControl(t *testing.T, domain *pdfConversation,
	store *document.FormJobStore, schema document.FormFieldsFacts,
) {
	t.Helper()
	job, err := domain.prepared.observeExistingJob(domain.jobID, domain.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.AppendValue(t.Context(), document.FormJobAppendValueRequest{
		JobID:             job.JobID,
		ExpectedRevision:  job.Revision,
		Owner:             domain.owner,
		FieldID:           schema.Fields[0].ID,
		IdempotencyKey:    "correct-after-review",
		SupersedesEventID: job.Fields[0].EventID,
		Value: document.FormProtectedValue{
			Kind: document.ProtectedValueText,
			Text: "corrected synthetic value",
		},
		State:  document.FormValueSupplied,
		Source: document.FormValueSourceUser,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPDFConfirmationRequiresCurrentReviewRouteAndOutputScope(t *testing.T) {
	for _, name := range []string{"foreign agent", "foreign session", "question", "forbidden", "draft", "not reviewed"} {
		t.Run(name, func(t *testing.T) {
			domain, store, schema, final := reviewedPDFControl(t)
			switch name {
			case "foreign agent":
				final.AgentID = "another-agent"
			case "foreign session":
				final.SessionKey = "another-session"
			case "question":
				final.Kind = "question"
			case "forbidden":
				domain.prepared.item.Case.PDF.Output = "forbidden"
			case "draft":
				domain.prepared.item.Case.PDF.Draft = true
			case "not reviewed":
				correctPDFControl(t, domain, store, schema)
			}
			if _, err := domain.confirmReview(final); !errors.Is(err, ErrPDFTrial) || domain.review != nil {
				t.Fatal("confirmation captured an unreviewed, foreign or out-of-scope job", err)
			}
		})
	}
}

func TestPDFWriteIntentIsInvalidatedByAnOrdinaryFollowup(t *testing.T) {
	domain, store, schema, final := reviewedPDFControl(t)
	if _, err := domain.confirmReview(final); err != nil {
		t.Fatal(err)
	}
	prompt := approvalPDFControl(t, domain, store, schema, nil)
	_, handled, err := domain.request(t.Context(), Action{Kind: "message", Text: "Please change the details"}, final)
	if err != nil || handled {
		t.Fatal("ordinary followup did not use the common conversation path", err)
	}
	if _, err := domain.approveReviewedWrite(prompt); !errors.Is(err, ErrPDFTrial) {
		t.Fatal("an old confirmation survived a followup")
	}
}

func TestPDFWriteConversationKeepsOneSessionAndDoesNotInventSettlement(t *testing.T) {
	for _, mode := range []string{"scripted", "llm_user"} {
		t.Run(mode, func(t *testing.T) { runPDFWriteConversationControl(t, mode) })
	}
}

func runPDFWriteConversationControl(t *testing.T, mode string) {
	t.Helper()
	domain, store, schema, _ := reviewedPDFControl(t)
	item := domain.prepared.item
	item.Case.Driver.Mode, item.Case.Driver.Persona = mode, ""
	item.Case.Driver.Steps = []Step{
		{WaitFor: Expectation{Kind: "final"}, Action: Action{Kind: "confirm"}},
		{WaitFor: Expectation{Kind: "approval"}, Action: Action{Kind: "allow_once"}},
	}
	var roles conversationRoles
	var user, judge *roleTestProvider
	if mode == "llm_user" {
		item.Case.Driver.Steps = nil
		item.Case.Driver.Persona = "Confirm the accepted review, then permit that PDF once."
		roles, user, judge = adaptiveRoles(`{"kind":"confirm"}`, adaptiveJudgePass)
	}
	item.Case.Rubric = nil
	transport := newFakeConversation()
	var approval Event
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		response := testResponse(request, number)
		response.Payload[protocol.PayloadKeyAgentID] = domain.prepared.agent
		response.Payload[protocol.PayloadKeySessionKey] = domain.owner.RouteSessionKey
		response.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{{
			Workspace: domain.prepared.workspace, TurnID: request.ID,
		}}
		if number == 2 {
			approval = approvalPDFControl(t, domain, store, schema, nil)
			if user != nil {
				user.response.Content = `{"kind":"allow_once"}`
			}
			response.Payload[protocol.PayloadKeyControls] = "prompt"
			response.Payload[protocol.PayloadKeyInteraction] = "approval"
			response.Payload[protocol.PayloadKeyInteractionID] = approval.InteractionID
			response.Payload[protocol.PayloadKeyInteractionShortID] = approval.InteractionShortID
			response.Payload[protocol.PayloadKeyMessageID] = approval.MessageID
		}
		transport.events <- response
	}
	trial := runDomainConversation(t.Context(), transport, item, domain.prepared.sessionID,
		domain.prepared.agent, domain.prepared.workspace, roles, domain)
	data, err := json.Marshal(trial)
	if err != nil || len(transport.sent) != 3 || trial.Verdict != Inconclusive || trial.EvidenceEligible ||
		trial.PDF == nil || trial.PDF.State != string(document.FormJobAwaitingApproval) ||
		trial.PDF.WriteIntent == nil || trial.PDF.WriteIntent.ApprovalInteractionID != approval.InteractionID ||
		bytes.Contains(data, []byte(domain.prepared.item.Case.Driver.Facts["contact-reference"])) {
		t.Fatal("write intent was leaked, not sent or mistaken for native settlement", trial.Code)
	}
	for _, request := range transport.sent {
		if request.SessionID != domain.prepared.sessionID {
			t.Fatal("confirmation or approval started another session")
		}
	}
	if trial.PDF.WriteIntent.ConfirmationRequestID != transport.sent[1].ID ||
		payloadString(transport.sent[2].Payload, protocol.PayloadKeyInteractionChoice) != "allow_once" {
		t.Fatal("confirmation/approval wire identity not preserved")
	}
	if user != nil {
		if user.calls != 2 || judge.calls != 0 || len(trial.DriverCalls) != 2 {
			t.Fatal("adaptive write intents bypassed user roles or reached an ineligible judge")
		}
		for _, call := range trial.DriverCalls {
			if call.PromptVersion != PDFUserPromptVersion {
				t.Fatal("PDF intent prompt was not independently versioned")
			}
		}
	}
}

func TestPDFWriteActionsRequireAWriteCaseAndMatchingPrompt(t *testing.T) {
	trial := newPDFTrialFixture(t)
	t.Cleanup(func() { _ = trial.release(true) })
	for _, kind := range []string{"confirm", "allow_once"} {
		prompt := "final"
		if kind == "allow_once" {
			prompt = "approval"
		}
		for _, mode := range []string{"valid", "general", "forbidden", "wrong_prompt", "with_text", "with_fact"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				item := trial.item.Case
				pdf := *item.PDF
				item.PDF = &pdf
				item.Driver.Mode, item.Driver.Persona = "scripted", ""
				item.Driver.Steps = []Step{{WaitFor: Expectation{Kind: prompt}, Action: Action{Kind: kind}}}
				switch mode {
				case "general":
					item = testCase()
					item.Driver.Steps[0] = Step{WaitFor: Expectation{Kind: prompt}, Action: Action{Kind: kind}}
				case "forbidden":
					item.PDF.Output = "forbidden"
				case "wrong_prompt":
					item.Driver.Steps[0].WaitFor.Kind = "question"
				case "with_text":
					item.Driver.Steps[0].Action.Text = "approve other work"
				case "with_fact":
					item.Driver.Steps[0].Action.Fact = "contact-reference"
				}
				err := item.Validate()
				if mode == "valid" && err != nil || mode != "valid" && !errors.Is(err, ErrInvalidCase) {
					t.Fatal("write action admitted without its case/prompt contract", err)
				}
			})
		}
	}
}
