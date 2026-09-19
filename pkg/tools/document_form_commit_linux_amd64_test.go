//go:build linux && amd64

package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFormCommitUsesApprovalPDF2AndOneDelivery(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		outboxStatus outbox.Status
		formState    document.FormJobState
		failureCode  string
		interrupt    bool
	}{
		{
			name: "delivered after settlement interruption", outboxStatus: outbox.StatusDelivered,
			formState: document.FormJobCompleted, interrupt: true,
		},
		{
			name: "definitely failed", outboxStatus: outbox.StatusDefinitelyFailed,
			formState: document.FormJobFailed, failureCode: string(document.FailureDeliveryFailed),
		},
		{
			name: "ambiguous", outboxStatus: outbox.StatusAmbiguous,
			formState: document.FormJobUncertain, failureCode: string(document.FailureDeliveryAmbiguous),
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			testDocumentFormCommitDelivery(
				t,
				scenario.outboxStatus,
				scenario.formState,
				scenario.failureCode,
				scenario.interrupt,
			)
		})
	}
}

func testDocumentFormCommitDelivery(
	t *testing.T,
	terminalStatus outbox.Status,
	wantFormState document.FormJobState,
	wantFailureCode string,
	interruptSettlement bool,
) {
	t.Helper()
	capability := document.Capabilities().Operations["fill"]
	if capability.State != document.CapabilitySupported {
		if os.Getenv("MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E") == "1" {
			t.Fatalf("document form backend is required: %s", capability.Reason)
		}
		t.Skipf("document form backend is unavailable: %s", capability.Reason)
	}
	formStore, options := newWorkflowFormStore(t)
	t.Cleanup(formStore.Close)
	mediaStore := media.NewFileMediaStore()
	t.Cleanup(mediaStore.Stop)
	owner := documentToolTestOwner(t)
	sourcePath := filepath.Join("..", "document", "testdata", "acroform-fields.pdf")
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceDigest := sha256.Sum256(sourceBytes)
	sourceRef, err := mediaStore.Store(sourcePath, media.MediaMeta{
		Filename: "synthetic-form.pdf", ContentType: "application/pdf", Source: "test",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "pdf3-commit-input")
	if err != nil {
		t.Fatal(err)
	}
	if err = mediaStore.BindOwner(sourceRef, owner); err != nil {
		t.Fatal(err)
	}
	policy := document.FormAuditPolicy{
		PrimaryModel: "document-deliberative", PrimaryIdentity: "resolved:document-deliberative",
	}
	tool := NewDocumentTool(
		WithDocumentFormJobStore(formStore),
		WithDocumentFormAudit(policy, &workflowTestAuditor{
			proposal: document.FormAuditProposal{Decision: document.FormAuditPass},
		}),
		WithDocumentStateRoot(options.StateRoot),
	)
	tool.SetMediaStore(mediaStore)
	result := tool.Execute(
		workflowToolContext(t, "commit-start", "commit-start-call", []string{sourceRef}),
		map[string]any{"action": "form", "form_action": "start", "source": sourceRef},
	)
	projection := decodeWorkflowResult(t, result.ForLLM)
	if result.IsError || projection.Job == nil {
		t.Fatalf("start = %#v", result)
	}
	jobID := projection.Job.JobID
	sink, err := document.NewFormProtectedAnswerSink(formStore)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	for question := 0; result.Control.Suspension != nil; question++ {
		if question > 8 || result.Control.Suspension.ProtectedAnswer == nil {
			t.Fatalf("unexpected form question sequence = %#v", result)
		}
		projection = decodeWorkflowResult(t, result.ForLLM)
		answer := "Protected PDF3 notes"
		if projection.NextField != nil && projection.NextField.Kind == document.FormFieldDate {
			answer = "09/14/2026"
		}
		receipt, acceptErr := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
			Binding: *result.Control.Suspension.ProtectedAnswer, Workspace: "workspace",
			Route: workflowInteractionRoute(), InteractionID: "commit-question-" + string(rune('a'+question)),
			IdempotencyKey: "commit-answer-" + string(rune('a'+question)),
			Intent:         interactions.ProtectedAnswerValue, Text: answer,
		})
		if acceptErr != nil {
			t.Fatal(acceptErr)
		}
		if err = sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
			Binding: *result.Control.Suspension.ProtectedAnswer, Workspace: "workspace",
			Route: workflowInteractionRoute(), InteractionID: "commit-question-" + string(rune('a'+question)),
			Receipt: receipt,
		}); err != nil {
			t.Fatal(err)
		}
		result = tool.Execute(
			workflowToolContext(t, "commit-continue", "commit-continue-"+string(rune('a'+question)), nil),
			map[string]any{"action": "form", "form_action": "continue", "event_id": receipt.Reference},
		)
		if result.IsError {
			t.Fatalf("continue = %#v", result)
		}
	}
	projection = decodeWorkflowResult(t, result.ForLLM)
	if projection.Review == nil || !projection.Review.Ready {
		t.Fatalf("review = %#v", projection)
	}
	commitArgs := map[string]any{"action": "form", "form_action": "commit", "job_id": jobID}
	commitCtx := workflowToolContext(t, "commit-approval", "commit-approval-call", nil)
	approval := tool.Execute(commitCtx, commitArgs)
	approvalProjection := decodeWorkflowResult(t, approval.ForLLM)
	if approval.IsError || approval.Control.Suspension == nil ||
		approval.Control.Suspension.Kind != interactions.KindApproval ||
		approvalProjection.Job == nil || approvalProjection.Job.State != document.FormJobAwaitingApproval {
		t.Fatalf("approval = %#v", approval)
	}
	bound, err := tool.ApprovalArguments(commitCtx, commitArgs)
	if err != nil || strings.Contains(string(mustJSON(t, bound)), "Protected PDF3 notes") {
		t.Fatalf("approval arguments = %#v, %v", bound, err)
	}
	tampered := make(map[string]any, len(bound))
	for key, value := range bound {
		tampered[key] = value
	}
	tampered["review_digest"] = strings.Repeat("a", 64)
	tamperedResult := tool.Execute(
		toolshared.WithToolApprovalArguments(
			toolshared.WithToolApprovalContinuation(commitCtx, true),
			tampered,
		),
		commitArgs,
	)
	if !tamperedResult.IsError || tamperedResult.Control.Suspension != nil ||
		!strings.Contains(tamperedResult.ForLLM, `"code":"approval_stale"`) {
		t.Fatalf("tampered approval = %#v", tamperedResult)
	}
	approvedCtx := toolshared.WithToolApprovalArguments(
		toolshared.WithToolApprovalContinuation(commitCtx, true),
		bound,
	)
	committed := tool.Execute(approvedCtx, commitArgs)
	committedProjection := decodeWorkflowResult(t, committed.ForLLM)
	if committed.IsError || committedProjection.Job == nil ||
		committedProjection.Job.State != document.FormJobDelivering || committedProjection.Commit == nil ||
		committedProjection.Commit.ArtifactRef == "" || !committedProjection.Commit.SourceUnchanged ||
		committedProjection.Commit.StructuralAssertions == 0 ||
		committedProjection.Commit.VisualAssertions == 0 || len(committed.Media) != 1 ||
		committed.Media[0] != committedProjection.Commit.ArtifactRef ||
		committed.Delivery.Intent != toolshared.DeliveryImmediateContinue || committed.Delivery.Outbound == nil ||
		committed.Delivery.Commit == nil || committed.Delivery.Settle == nil ||
		committed.Delivery.Outbound.Recovery == nil ||
		committed.Delivery.Outbound.Recovery.DomainJobID != jobID ||
		committed.Delivery.Outbound.Recovery.DomainOwnerDigest == "" {
		t.Fatalf("committed = %#v projection=%#v", committed, committedProjection)
	}
	instanceRoot := filepath.Dir(options.StateRoot)
	coordinator, err := outbox.OpenCoordinator(instanceRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	identity := outbox.Identity{
		SourceID: "pdf3-form-" + jobID, Channel: "telegram", ChatID: "pdf3-chat", SessionKey: "pdf3-session",
	}
	outbound := committed.Delivery.Outbound
	admission, err := coordinator.AdmitMedia("workspace", identity, bus.OutboundMediaMessage{
		Context: bus.InboundContext{Channel: identity.Channel, ChatID: identity.ChatID},
		AgentID: "main", SessionKey: identity.SessionKey,
		Parts: append([]bus.MediaPart(nil), outbound.Media...), Recovery: outbound.Recovery,
	})
	if err != nil || !admission.Dispatch {
		t.Fatalf("outbox admission = %#v, %v", admission, err)
	}
	outboxDeliveryID := admission.Intent.ID
	if err = committed.Delivery.Commit(
		toolshared.WithToolOutboundDeliveryID(commitCtx, outboxDeliveryID),
	); err != nil {
		t.Fatal(err)
	}
	pending := tool.Execute(
		workflowToolContext(t, "commit-pending-replay", "commit-pending-replay-call", nil),
		commitArgs,
	)
	pendingProjection := decodeWorkflowResult(t, pending.ForLLM)
	if pending.IsError || len(pending.Media) != 0 || pending.Delivery.Outbound != nil ||
		pendingProjection.Job == nil || pendingProjection.Job.State != document.FormJobDelivering {
		t.Fatalf("pending commit replay = %#v", pending)
	}
	formStore.Close()
	reopened, err := document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	recoveryTool := NewDocumentTool(
		WithDocumentFormJobStore(reopened),
		WithDocumentStateRoot(options.StateRoot),
	)
	recoveryTool.SetMediaStore(mediaStore)
	recoveredIntent := admission.Intent
	publish, err := recoveryTool.ReconcileRecoveredDeliveryAdmission(t.Context(), recoveredIntent)
	if err != nil || !publish {
		t.Fatalf("recovered admission = %t, %v", publish, err)
	}
	if err = coordinator.PrepareAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.CommitAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.BeginAttempt(outboxDeliveryID); err != nil {
		t.Fatal(err)
	}
	switch terminalStatus {
	case outbox.StatusDelivered:
		err = coordinator.MarkDelivered(outboxDeliveryID, outbox.Outcome{})
	case outbox.StatusDefinitelyFailed:
		err = coordinator.MarkDefinitelyFailed(outboxDeliveryID, outbox.Outcome{Error: "test rejection"})
	case outbox.StatusAmbiguous:
		err = coordinator.MarkAmbiguous(outboxDeliveryID, outbox.Outcome{Error: "test ambiguity"})
	default:
		t.Fatalf("unsupported test terminal status %q", terminalStatus)
	}
	if err != nil {
		t.Fatal(err)
	}
	recoveredIntent, err = coordinator.Get(outboxDeliveryID)
	if err != nil || !recoveredIntent.RecoverySettlementPending() {
		t.Fatalf("terminal outbox intent = %#v, %v", recoveredIntent, err)
	}
	formOwner, err := documentFormOwner(commitCtx)
	if err != nil {
		t.Fatal(err)
	}
	if interruptSettlement {
		reopened.Close()
		if err = recoveryTool.SettleRecoveredDelivery(t.Context(), recoveredIntent); err == nil {
			t.Fatal("settlement unexpectedly crossed a closed PDF3 form store")
		}
		writeOwner, operationID, ok := recoveredDocumentDeliveryAuthority(recoveredIntent)
		if !ok {
			t.Fatal("terminal outbox intent lost document authority")
		}
		journal, journalErr := recoveryTool.documentWriteJournal()
		if journalErr != nil {
			t.Fatal(journalErr)
		}
		writeRecord, found, lookupErr := journal.Lookup(t.Context(), operationID, writeOwner)
		if lookupErr != nil || !found || writeRecord.State != document.WriteDelivered {
			t.Fatalf("PDF2 settlement before interruption = %#v, %t, %v", writeRecord, found, lookupErr)
		}
		if err = coordinator.Close(); err != nil {
			t.Fatal(err)
		}
		coordinator, err = outbox.OpenCoordinator(instanceRoot)
		if err != nil {
			t.Fatal(err)
		}
		recoveredAdmissions, recoverErr := coordinator.Recover()
		if recoverErr != nil || len(recoveredAdmissions) != 1 || !recoveredAdmissions[0].Settle ||
			recoveredAdmissions[0].Dispatch || recoveredAdmissions[0].Intent.ID != outboxDeliveryID {
			t.Fatalf("settlement-only restart admission = %#v, %v", recoveredAdmissions, recoverErr)
		}
		reopened, err = document.OpenFormJobStore(options)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		beforeRecovery, getErr := reopened.Get(t.Context(), jobID, formOwner)
		if getErr != nil || beforeRecovery.State != document.FormJobDelivering {
			t.Fatalf("PDF3 state before recovered settlement = %#v, %v", beforeRecovery, getErr)
		}
		recoveryTool = NewDocumentTool(
			WithDocumentFormJobStore(reopened),
			WithDocumentStateRoot(options.StateRoot),
		)
		recoveryTool.SetMediaStore(mediaStore)
		recoveredIntent = recoveredAdmissions[0].Intent
	}
	if err = recoveryTool.SettleRecoveredDelivery(t.Context(), recoveredIntent); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.MarkRecoverySettled(outboxDeliveryID); err != nil {
		t.Fatal(err)
	}
	if recoveredAdmissions, recoverErr := coordinator.Recover(); recoverErr != nil || len(recoveredAdmissions) != 0 {
		t.Fatalf("outbox replay after recovered settlement = %#v, %v", recoveredAdmissions, recoverErr)
	}
	settled, err := reopened.Get(t.Context(), jobID, formOwner)
	if err != nil || settled.State != wantFormState || settled.FailureCode != wantFailureCode {
		t.Fatalf("settled form job = %#v, %v", settled, err)
	}
	sourceAfter, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if digest := sha256.Sum256(sourceAfter); hex.EncodeToString(digest[:]) != hex.EncodeToString(sourceDigest[:]) {
		t.Fatal("source bytes changed during PDF3 commit")
	}
	verified, verifyReport := document.VerifyMedia(
		t.Context(),
		mediaStore,
		committedProjection.Commit.ArtifactRef,
		owner,
		document.FormWriteOptions{
			Acquire:   document.AcquireOptions{ScratchRoot: filepath.Join(t.TempDir(), "verify")},
			StateRoot: options.StateRoot, OperationID: committedProjection.Commit.OperationID,
		},
	)
	if verified != nil {
		defer func() { _ = verified.Close() }()
	}
	if verified == nil || verifyReport.State != document.StateSucceeded {
		t.Fatalf("independent verify = %#v", verifyReport)
	}
	if err = mediaStore.ReleaseAll(documentFormSourceScope(jobID)); err != nil {
		t.Fatal(err)
	}
	recoveryTool.formPolicy = document.FormAuditPolicy{}
	recoveryTool.formAudit = nil
	replayed := recoveryTool.Execute(
		workflowToolContext(t, "commit-replay", "commit-replay-call", nil),
		commitArgs,
	)
	replayedProjection := decodeWorkflowResult(t, replayed.ForLLM)
	if wantFormState == document.FormJobCompleted {
		if replayed.IsError || replayedProjection.Commit == nil || replayedProjection.Job == nil ||
			replayedProjection.Job.State != document.FormJobCompleted ||
			replayedProjection.Commit.OperationID != committedProjection.Commit.OperationID ||
			replayedProjection.Commit.ArtifactRef != committedProjection.Commit.ArtifactRef ||
			len(replayed.Media) != 0 || replayed.Delivery.Outbound != nil {
			t.Fatalf("replayed commit = %#v projection=%#v", replayed, replayedProjection)
		}
		status := recoveryTool.Execute(
			workflowToolContext(t, "completed-status", "completed-status-call", nil),
			map[string]any{
				"action": "form", "form_action": "status", "job_id": jobID,
			},
		)
		statusProjection := decodeWorkflowResult(t, status.ForLLM)
		if status.IsError || statusProjection.FormAction != "status" || statusProjection.Job == nil ||
			statusProjection.Job.State != document.FormJobCompleted || statusProjection.Commit == nil ||
			statusProjection.Commit.OperationID != committedProjection.Commit.OperationID ||
			statusProjection.Commit.ArtifactRef != committedProjection.Commit.ArtifactRef ||
			len(status.Media) != 0 || status.Delivery.Outbound != nil {
			t.Fatalf("completed terminal status = %#v projection=%#v", status, statusProjection)
		}
	} else if !replayed.IsError || !strings.Contains(replayed.ForLLM, `"code":"`+wantFailureCode+`"`) ||
		len(replayed.Media) != 0 || replayed.Delivery.Outbound != nil {
		t.Fatalf("terminal commit replay = %#v projection=%#v", replayed, replayedProjection)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
