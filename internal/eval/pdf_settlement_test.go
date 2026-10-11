package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func settledPDFControl(t *testing.T) (*pdfConversation, pdfSettlement) {
	t.Helper()
	domain, store, schema, final := reviewedPDFControl(t)
	if _, err := domain.confirmReview(final); err != nil {
		t.Fatal(err)
	}
	prompt := approvalPDFControl(t, domain, store, schema, nil)
	request, err := domain.approveReviewedWrite(prompt)
	if err != nil {
		t.Fatal(err)
	}
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(domain.prepared.workspace))
	record := *domain.approvalRecord
	record, err = registry.ClaimAnswer(record.ID, record.Revision, interactions.Answer{
		Text: "allow_once", MessageID: request.ID, ResponseMessageID: prompt.MessageID,
		ReceivedAt: time.Now().UnixMilli(),
	}, interactions.OutcomeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.MarkResuming(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	record, err = registry.ConsumeApproval(record.ID, record.Revision,
		record.Origin.ToolCallID, record.Origin.ToolName, record.Origin.ArgumentHash)
	if err != nil {
		t.Fatal(err)
	}
	job, err := domain.prepared.observeExistingJob(domain.jobID, domain.owner)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := job.CommitBinding()
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.BeginFormCommit(t.Context(), document.FormCommitRequest{
		JobID: job.JobID, Owner: domain.owner, ExpectedRevision: job.Revision, Schema: schema,
		AuditPolicyRevision: job.AuditPolicyRevision, OutputPolicyRevision: job.OutputPolicyRevision,
	}, binding)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := pdfWriteOwner(domain.prepared.workspace, record)
	if err != nil {
		t.Fatal(err)
	}
	value := domain.prepared.item.Case.Driver.Facts["contact-reference"]
	fill, err := document.NormalizeFillMap(document.DocumentRef{SHA256: schema.SourceSHA256}, schema, document.FillMap{
		SchemaVersion: document.FillMapSchemaVersion, Assignments: []document.FormFillAssignment{{
			FieldID: schema.Fields[0].ID, Value: document.FormValue{Type: document.FormValueText, Text: &value},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := document.NewWriteJournal(filepath.Join(domain.prepared.home, "state", "document-writes", "journal"))
	if err != nil {
		t.Fatal(err)
	}
	operation, _, err := journal.Accept(t.Context(), binding.OperationID, owner, fill)
	if err != nil {
		t.Fatal(err)
	}
	artifact := document.WriteArtifactEvidence{SHA256: digest([]byte("metadata-control-artifact")), Size: 25}
	ref := "media://11111111-1111-4111-8111-111111111111"
	for _, transition := range []document.WriteTransition{
		{State: document.WriteWriting},
		{State: document.WriteWritten, Artifact: &artifact},
		{State: document.WriteVerifying},
		{State: document.WriteVerified, Verification: &document.WriteVerificationEvidence{
			StructuralAssertions: 1, VisualAssertions: 1, CheckedFields: 1, CheckedWidgets: 1, RenderedPages: 1,
		}},
		{State: document.WriteRegistered, ArtifactRef: ref},
	} {
		transition.ExpectedRevision = operation.Revision
		operation, _, err = journal.Transition(t.Context(), operation.OperationID, owner, transition)
		if err != nil {
			t.Fatal(err)
		}
	}
	job, err = store.RecordFormCommitArtifact(t.Context(), document.FormCommitArtifactRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: domain.owner,
		OperationID: operation.OperationID, ArtifactRef: ref, ArtifactDigest: artifact.SHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := outbox.NewMediaIntent(domain.prepared.workspace, outbox.Identity{
		SourceID: "control-native-transaction", Channel: domain.owner.Channel,
		ChatID: domain.owner.ChatID, SessionKey: domain.owner.RouteSessionKey,
	}, bus.OutboundMediaMessage{
		AgentID: domain.owner.AgentID, Parts: []bus.MediaPart{{Ref: ref, Type: "file", ContentType: "application/pdf"}},
		Recovery: &bus.OutboundRecovery{
			Kind: bus.OutboundRecoveryDocumentFill, MediaRef: ref, WorkspaceID: owner.WorkspaceID,
			AgentID: owner.AgentID, ActorID: owner.ActorID, RouteID: owner.RouteID, SessionID: owner.SessionID,
			AuthorityKind: owner.Kind, OperationID: operation.OperationID, DomainDeliveryID: operation.DeliveryID,
			DomainJobID: job.JobID, DomainOwnerDigest: job.OwnerDigest,
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
	operation, _, err = journal.Transition(t.Context(), operation.OperationID, owner, document.WriteTransition{
		ExpectedRevision: operation.Revision, State: document.WriteDeliveryPending, OutboxDeliveryID: intent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = box.BeginAttempt(intent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = box.MarkDelivered(
		intent.ID,
		outbox.Outcome{PlatformMessageIDs: []string{"native-pdf-message"}},
	); err != nil {
		t.Fatal(err)
	}
	operation, _, err = journal.Transition(t.Context(), operation.OperationID, owner, document.WriteTransition{
		ExpectedRevision: operation.Revision, State: document.WriteDelivered,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SettleFormDelivery(t.Context(), document.FormDeliveryRequest{
		JobID: job.JobID, OwnerDigest: job.OwnerDigest, OperationID: operation.OperationID,
		ArtifactRef: ref, Outcome: document.FormDeliveryDelivered,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = box.MarkRecoverySettled(intent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Resolve(record.ID, record.Revision); err != nil {
		t.Fatal(err)
	}
	settlement, err := domain.observeSettlement()
	if err != nil {
		t.Fatal("native metadata control was not admitted", err)
	}
	return domain, settlement
}

func TestPDFSettlementBindsConsumedGrantOperationAndDelivery(t *testing.T) {
	domain, settlement := settledPDFControl(t)
	if settlement.job.State != document.FormJobCompleted || settlement.approval.ApprovalConsumedAt == 0 ||
		settlement.operation.State != document.WriteDelivered || settlement.delivery.ID != settlement.operation.OutboxDeliveryID {
		t.Fatal("settlement replaced native receipts with intent")
	}
	trial := Trial{}
	domain.conclude(t.Context(), &trial)
	if trial.EvidenceEligible || trial.Verdict == Pass || trial.PDF.Settlement == nil ||
		trial.PDF.Settlement.OutboxDeliveryID != settlement.delivery.ID {
		t.Fatal("native metadata alone became artifact/privacy qualification")
	}
}

func TestPDFSettlementRefusesUnconsumedOrForeignGrantAndReceipt(t *testing.T) {
	for _, mode := range []string{
		"reply", "confirmation", "unconsumed", "scope", "delivery", "journal_private", "outbox_private",
		"expiry", "artifact_digest", "multiple_messages", "recovery_pending",
	} {
		t.Run(mode, func(t *testing.T) {
			domain, settlement := settledPDFControl(t)
			switch mode {
			case "reply":
				domain.approvalRequestID = "foreign-reply"
			case "confirmation":
				domain.confirmationRequestID = "foreign-confirmation"
			case "unconsumed":
				settlement.approval.ApprovalConsumedAt = 0
				if domain.settledApprovalMatches(settlement.approval, settlement.job.TerminalAt) {
					t.Fatal("unconsumed approval became settlement")
				}
				return
			case "scope":
				domain.review.OutputScope = document.FormOutputEditableDraft
			case "expiry":
				domain.approvalRecord.ExpiresAt++
			case "artifact_digest":
				settlement.operation.Artifact.SHA256 = digest([]byte("foreign artifact"))
				data, err := json.Marshal(settlement.operation)
				if err != nil || os.WriteFile(filepath.Join(domain.prepared.home, "state", "document-writes", "journal",
					domain.operationID+".json"), data, 0o600) != nil {
					t.Fatal("cannot inject changed artifact metadata")
				}
			case "delivery", "multiple_messages", "recovery_pending":
				switch mode {
				case "delivery":
					settlement.delivery.Media.Recovery.DomainJobID = "form_job_foreign"
				case "multiple_messages":
					settlement.delivery.PlatformMessageIDs = append(
						settlement.delivery.PlatformMessageIDs,
						"second-message",
					)
				case "recovery_pending":
					settlement.delivery.RecoverySettled = false
				}
				data, _ := json.Marshal(settlement.delivery)
				if err := os.WriteFile(
					filepath.Join(domain.prepared.home, "state", "outbox", settlement.delivery.ID+".json"),
					data,
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			case "journal_private", "outbox_private":
				path := filepath.Join(
					domain.prepared.home,
					"state",
					"document-writes",
					"journal",
					domain.operationID+".json",
				)
				if mode == "outbox_private" {
					path = filepath.Join(domain.prepared.home, "state", "outbox", settlement.delivery.ID+".json")
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var raw map[string]any
				if json.Unmarshal(data, &raw) != nil {
					t.Fatal("control native JSON not readable")
				}
				raw["private"] = domain.prepared.item.Case.Driver.Facts["contact-reference"]
				data, err = json.Marshal(raw)
				if err != nil || os.WriteFile(path, data, 0o600) != nil {
					t.Fatal("cannot inject raw leak control")
				}
			}
			_, err := domain.observeSettlement()
			want := ErrPDFTrial
			if mode == "journal_private" || mode == "outbox_private" {
				want = ErrPrivateEvidence
			}
			if !errors.Is(err, want) {
				t.Fatal("settlement admitted foreign/incomplete evidence or softened a privacy failure", err)
			}
		})
	}
}

func TestPDFSettlementRejectsStaleOrNonAllowedAnswer(t *testing.T) {
	for _, mode := range []string{"expired", "future", "old_answer", "denied", "superseded", "wrong_prompt"} {
		t.Run(mode, func(t *testing.T) {
			domain, settlement := settledPDFControl(t)
			record := settlement.approval
			answer := *record.Answer
			record.Answer = &answer
			switch mode {
			case "expired":
				record.ApprovalConsumedAt = record.ExpiresAt
			case "future":
				record.ResolvedAt = time.Now().Add(time.Hour).UnixMilli()
			case "old_answer":
				record.Answer.ReceivedAt = domain.confirmedAt.Add(-time.Second).UnixMilli()
			case "denied":
				record.Outcome = interactions.OutcomeDenied
			case "superseded":
				record.Answer.Superseded = true
			case "wrong_prompt":
				record.Answer.ResponseMessageID = "foreign-prompt"
			}
			if domain.settledApprovalMatches(record, settlement.job.TerminalAt) {
				t.Fatal("non-current allowed answer became a consumed approval receipt")
			}
		})
	}
}

func TestPDFSettlementBindsCapturedExecutionContext(t *testing.T) {
	domain, settlement := settledPDFControl(t)
	if !domain.settledApprovalMatches(settlement.approval, settlement.job.TerminalAt) {
		t.Fatal("unchanged native context was rejected")
	}
	for _, mode := range []string{"actor", "sender", "account", "raw", "missing", "missing_capture"} {
		t.Run(mode, func(t *testing.T) {
			record := settlement.approval
			context := *record.Origin.ExecutionContext
			record.Origin.ExecutionContext = &context
			switch mode {
			case "actor":
				context.ActorID = "foreign-actor"
			case "sender":
				context.SenderID = "foreign-sender"
			case "account":
				context.Account = "foreign-account"
			case "raw":
				context.Raw = map[string]string{"foreign": "context"}
			case "missing":
				record.Origin.ExecutionContext = nil
			case "missing_capture":
				captured := domain.approvalRecord.Origin.ExecutionContext
				domain.approvalRecord.Origin.ExecutionContext = nil
				t.Cleanup(func() { domain.approvalRecord.Origin.ExecutionContext = captured })
			}
			if domain.settledApprovalMatches(record, settlement.job.TerminalAt) {
				t.Fatal("changed or missing execution context admitted settlement authority")
			}
		})
	}
	if !domain.settledApprovalMatches(settlement.approval, settlement.job.TerminalAt) {
		t.Fatal("negative controls changed the captured approval")
	}
}
