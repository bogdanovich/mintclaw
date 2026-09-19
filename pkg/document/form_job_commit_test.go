package document

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestFormCommitApprovalMaterializationAndRestart(t *testing.T) {
	store, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	policy := testFormAuditPolicy("commit-audit")
	privateValue := "MINTCLAW_PDF3_COMMIT_PRIVATE_9a41"
	record := createReviewMappedJob(t, store, owner, schema, policy, privateValue)
	reviewed, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: &testFormAuditor{proposals: map[string]FormAuditProposal{
			"commit-audit": {Decision: FormAuditPass},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	policyRevision, err := policy.Revision()
	if err != nil {
		t.Fatal(err)
	}
	request := FormCommitRequest{
		JobID: reviewed.Job.JobID, ExpectedRevision: reviewed.Job.Revision, Owner: owner, Schema: schema,
		AuditPolicyRevision: policyRevision, OutputPolicyRevision: FormCommitOutputPolicyRevision,
	}
	awaiting, binding, err := store.PrepareFormCommitApproval(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if awaiting.State != FormJobAwaitingApproval || awaiting.ApprovalRevision != awaiting.Revision ||
		binding.ApprovalRevision != awaiting.Revision || binding.ReviewRevision != reviewed.Job.Revision ||
		binding.OperationID == "" || binding.OperationID != awaiting.OperationID || awaiting.ApprovalDigest == "" {
		t.Fatalf("awaiting approval = %#v, binding=%#v", awaiting, binding)
	}
	replayed, replayBinding, err := store.PrepareFormCommitApproval(t.Context(), FormCommitRequest{
		JobID: request.JobID, ExpectedRevision: awaiting.Revision, Owner: owner, Schema: schema,
		AuditPolicyRevision: policyRevision, OutputPolicyRevision: FormCommitOutputPolicyRevision,
	})
	if err != nil || replayed.Revision != awaiting.Revision || replayBinding != binding {
		t.Fatalf("replayed approval = %#v, %#v, %v", replayed, replayBinding, err)
	}
	currentReview, err := store.CurrentFormReview(t.Context(), record.JobID, owner, schema)
	if err != nil || !currentReview.Ready || currentReview.ReviewDigest != reviewed.Review.ReviewDigest {
		t.Fatalf("review after approval preparation = %#v, %v", currentReview, err)
	}

	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	request.ExpectedRevision = awaiting.Revision
	restarted, restartedBinding, err := reopened.CurrentFormCommitBinding(t.Context(), request)
	if err != nil || restartedBinding != binding || restarted.State != FormJobAwaitingApproval {
		t.Fatalf("restarted approval = %#v, %#v, %v", restarted, restartedBinding, err)
	}
	wrong := binding
	wrong.ReviewDigest = strings.Repeat("a", 64)
	if _, err = reopened.BeginFormCommit(t.Context(), request, wrong); !errors.Is(err, ErrFormApprovalStale) {
		t.Fatalf("wrong binding error = %v", err)
	}
	committing, err := reopened.BeginFormCommit(t.Context(), request, binding)
	if err != nil || committing.State != FormJobCommitting || committing.Revision != awaiting.Revision+1 {
		t.Fatalf("committing = %#v, %v", committing, err)
	}
	request.ExpectedRevision = committing.Revision
	materialized, fill, err := reopened.MaterializeFormCommit(t.Context(), request)
	if err != nil || materialized.OperationID != binding.OperationID || len(fill.Assignments) != 1 ||
		fill.Assignments[0].Value.Text == nil || *fill.Assignments[0].Value.Text != privateValue {
		t.Fatalf("materialized = %#v, fill=%#v, err=%v", materialized, fill, err)
	}
	clearFillMap(&fill)
	artifactDigest := strings.Repeat("b", 64)
	delivering, err := reopened.RecordFormCommitArtifact(t.Context(), FormCommitArtifactRequest{
		JobID: committing.JobID, ExpectedRevision: committing.Revision, Owner: owner,
		OperationID: committing.OperationID,
		ArtifactRef: "media://11111111-1111-4111-8111-111111111111", ArtifactDigest: artifactDigest,
	})
	if err != nil || delivering.State != FormJobDelivering || delivering.ArtifactDigest != artifactDigest {
		t.Fatalf("delivering = %#v, %v", delivering, err)
	}
	if _, err = reopened.Cancel(
		t.Context(), delivering.JobID, delivering.Revision, owner,
	); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("post-commit cancel error = %v", err)
	}
	state, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state), privateValue) {
		t.Fatal("commit materialization leaked protected value into durable job state")
	}
	var persisted formJobStoreDocument
	if err = json.Unmarshal(state, &persisted); err != nil {
		t.Fatal(err)
	}
	stored := persisted.Records[delivering.JobID]
	stored.Public.ArtifactRef = "media://malformed"
	stored.IntegrityDigest, err = reopened.storedRecordIntegrity(stored)
	if err != nil {
		t.Fatal(err)
	}
	persisted.Records[delivering.JobID] = stored
	tampered, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	if err = os.WriteFile(formJobStatePath(options), append(tampered, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenFormJobStore(options); !errors.Is(err, ErrFormJobRecordCorrupt) {
		t.Fatalf("malformed persisted artifact reference error = %v", err)
	}
}

func TestFormCommitCorrectionInvalidatesPreparedApproval(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	policy := testFormAuditPolicy("correction-audit")
	record := createReviewMappedJob(t, store, owner, schema, policy, "first-value")
	reviewed, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: &testFormAuditor{proposals: map[string]FormAuditProposal{
			"correction-audit": {Decision: FormAuditPass},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	policyRevision, err := policy.Revision()
	if err != nil {
		t.Fatal(err)
	}
	request := FormCommitRequest{
		JobID: reviewed.Job.JobID, ExpectedRevision: reviewed.Job.Revision, Owner: owner, Schema: schema,
		AuditPolicyRevision: policyRevision, OutputPolicyRevision: FormCommitOutputPolicyRevision,
	}
	awaiting, binding, err := store.PrepareFormCommitApproval(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	current := currentField(t, awaiting, schema.Fields[0].ID)
	corrected, _, err := store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: awaiting.JobID, ExpectedRevision: awaiting.Revision, Owner: owner,
		FieldID: current.FieldID, IdempotencyKey: "commit-correction",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "corrected-value"},
		State: FormValueConfirmed, Source: FormValueSourceUser,
		Confidence: FormValueConfidenceConfirmed, Validation: FormValueValidationValid,
		SupersedesEventID: current.EventID,
	})
	if err != nil || corrected.State != FormJobCollecting || corrected.ApprovalRevision != 0 ||
		corrected.OperationID != "" || corrected.ReviewDigest != "" {
		t.Fatalf("corrected job = %#v, %v", corrected, err)
	}
	request.ExpectedRevision = corrected.Revision
	if _, err = store.BeginFormCommit(t.Context(), request, binding); !errors.Is(err, ErrFormApprovalStale) {
		t.Fatalf("stale approval error = %v", err)
	}
}

func TestFormCommitRejectsNoOpBeforeApproval(t *testing.T) {
	schema := *successfulTestFormFields()
	field := schema.Fields[0]
	if !formCommitHasMaterializableAssignment(FormJobRecord{Fields: []FormJobFieldState{{
		FieldID: field.ID, ValueKind: ProtectedValueText,
	}}}, schema) {
		t.Fatal("supplied field should produce a materializable assignment")
	}
	if formCommitHasMaterializableAssignment(FormJobRecord{}, schema) {
		t.Fatal("an already-populated source without corrections should not request commit approval")
	}
	if formCommitHasMaterializableAssignment(FormJobRecord{Fields: []FormJobFieldState{{
		FieldID: field.ID, ValueKind: ProtectedValueBlank,
	}}}, schema) {
		t.Fatal("an originally empty optional field left blank should not request commit approval")
	}
	schema.Fields[0].HasValue = true
	if !formCommitHasMaterializableAssignment(FormJobRecord{Fields: []FormJobFieldState{{
		FieldID: field.ID, ValueKind: ProtectedValueBlank,
	}}}, schema) {
		t.Fatal("clearing an existing optional field should produce a materializable assignment")
	}
}
