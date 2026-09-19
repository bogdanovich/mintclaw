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

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFormCommitUsesApprovalAndPDF2WithoutDelivery(t *testing.T) {
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
		committedProjection.Commit.VisualAssertions == 0 || len(committed.Media) != 0 ||
		committed.Delivery.Intent != "" || committed.Delivery.Outbound != nil {
		t.Fatalf("committed = %#v projection=%#v", committed, committedProjection)
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
	replayed := tool.Execute(
		workflowToolContext(t, "commit-replay", "commit-replay-call", nil),
		commitArgs,
	)
	replayedProjection := decodeWorkflowResult(t, replayed.ForLLM)
	if replayed.IsError || replayedProjection.Commit == nil ||
		replayedProjection.Commit.OperationID != committedProjection.Commit.OperationID ||
		replayedProjection.Commit.ArtifactRef != committedProjection.Commit.ArtifactRef ||
		len(replayed.Media) != 0 || replayed.Delivery.Outbound != nil {
		t.Fatalf("replayed commit = %#v projection=%#v", replayed, replayedProjection)
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
