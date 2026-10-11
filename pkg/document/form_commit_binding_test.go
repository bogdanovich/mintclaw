package document

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestFormCommitApprovalArgumentsPreserveNativeShape(t *testing.T) {
	binding := FormCommitBinding{
		JobID: "job", OwnerDigest: "owner", ApprovalRevision: 3, ReviewRevision: 2,
		SourceDigest: "source", FieldSchemaDigest: "fields", BackendRevision: "backend",
		AuditPolicyRevision: "audit", AssignmentDigest: "assignment", ReviewDigest: "review",
		OutputPolicyRevision: "policy", OperationID: "operation", ExpiresAt: 10252,
	}
	arguments := binding.ApprovalArguments()
	data, err := json.Marshal(arguments)
	want := `{"approval_revision":3,"assignment_digest":"assignment","audit_policy_revision":"audit",` +
		`"backend_revision":"backend","expires_at":10252,"field_schema_digest":"fields","job_id":"job",` +
		`"operation_id":"operation","output_policy_revision":"policy","owner_digest":"owner",` +
		`"review_digest":"review","review_revision":2,"schema_version":"mintclaw.document_form_approval.v1",` +
		`"source_digest":"source"}`
	if err != nil || string(data) != want {
		t.Fatal("public binding changed the native persisted tool approval shape")
	}
	arguments["job_id"] = "changed"
	if binding.ApprovalArguments()["job_id"] != "job" {
		t.Fatal("approval projection aliases a previous result")
	}
	binding.OutputScope, binding.OutputMode = FormOutputEditableDraft, FormOutputEditableAcroForm
	draft := binding.ApprovalArguments()
	if len(draft) != 16 || draft["output_scope"] != binding.OutputScope || draft["output_mode"] != binding.OutputMode {
		t.Fatal("draft projection lost native output identity")
	}
}

func TestFormJobCommitBindingPreservesAndChecksFrozenDigest(t *testing.T) {
	record := FormJobRecord{
		JobID: "job", OwnerDigest: "owner", ApprovalRevision: 3, ReviewRevision: 2,
		SourceDigest: "source", FieldSchemaDigest: "fields", BackendRevision: "backend",
		AuditPolicyRevision: "audit", AssignmentDigest: "assignment", ReviewDigest: "review",
		OutputPolicyRevision: "policy", OperationID: "operation", ExpiresAt: 10252,
		OutputScope: FormOutputEditableDraft, OutputMode: FormOutputEditableAcroForm,
	}
	want := FormCommitBinding{
		JobID: record.JobID, OwnerDigest: record.OwnerDigest,
		ApprovalRevision: record.ApprovalRevision, ReviewRevision: record.ReviewRevision,
		SourceDigest: record.SourceDigest, FieldSchemaDigest: record.FieldSchemaDigest,
		BackendRevision: record.BackendRevision, AuditPolicyRevision: record.AuditPolicyRevision,
		AssignmentDigest: record.AssignmentDigest, ReviewDigest: record.ReviewDigest,
		OutputPolicyRevision: record.OutputPolicyRevision, OperationID: record.OperationID,
		ExpiresAt: record.ExpiresAt, OutputScope: record.OutputScope, OutputMode: record.OutputMode,
	}
	var err error
	record.ApprovalDigest, err = formJobJSONDigest(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := record.CommitBinding()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("public binding differs from the native frozen record")
	}
	record.ReviewRevision++
	got, err = record.CommitBinding()
	if !errors.Is(err, ErrFormApprovalStale) || !reflect.DeepEqual(got, FormCommitBinding{}) {
		t.Fatal("changed review metadata retained a valid binding")
	}
}
