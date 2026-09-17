package document

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

type testFormAuditor struct {
	proposals map[string]FormAuditProposal
	errors    map[string]error
	calls     []string
	views     []FormAuditView
	onAudit   func(string, FormAuditView)
}

func (auditor *testFormAuditor) AuditForm(
	_ context.Context,
	model string,
	view FormAuditView,
) (FormAuditProposal, error) {
	auditor.calls = append(auditor.calls, model)
	auditor.views = append(auditor.views, cloneFormAuditView(view))
	if auditor.onAudit != nil {
		auditor.onAudit(model, view)
	}
	if err := auditor.errors[model]; err != nil {
		return FormAuditProposal{}, err
	}
	return auditor.proposals[model], nil
}

func TestFormReviewUsesConfiguredAuditAndSurvivesRestartWithoutPlaintext(t *testing.T) {
	store, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	policy := FormAuditPolicy{
		PrimaryModel: "document-deliberative", EquivalentFallbacks: []string{"document-equivalent"},
	}
	record := createReviewMappedJob(t, store, owner, schema, policy, "MINTCLAW_PDF3_AUDIT_PRIVATE_7231")
	auditor := &testFormAuditor{proposals: map[string]FormAuditProposal{
		"document-deliberative": {Decision: FormAuditPass},
	}}
	result, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: auditor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UsedFallback || !slices.Equal(auditor.calls, []string{"document-deliberative"}) ||
		result.Job.State != FormJobReviewReady || result.Job.ReviewRevision != result.Job.Revision ||
		result.Job.AuditRevision != result.Job.Revision || result.Job.AssignmentDigest == "" ||
		result.Job.AuditDigest == "" || result.Job.ReviewDigest == "" || !result.Review.Ready ||
		result.Review.RequestedAction != "fill_and_deliver_verified_pdf" {
		t.Fatalf("review result = %#v, calls=%v", result, auditor.calls)
	}
	if len(auditor.views) != 1 || len(auditor.views[0].Fields) != 1 ||
		auditor.views[0].Fields[0].Value.Text != "MINTCLAW_PDF3_AUDIT_PRIVATE_7231" {
		t.Fatalf("protected audit view = %#v", auditor.views)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "MINTCLAW_PDF3_AUDIT_PRIVATE_7231") ||
		!strings.Contains(string(encoded), `"summary":"provided"`) {
		t.Fatalf("unsafe review projection = %s", encoded)
	}
	state, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state), "MINTCLAW_PDF3_AUDIT_PRIVATE_7231") {
		t.Fatal("audit sentinel leaked into durable job state")
	}

	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	restarted, err := reopened.CurrentFormReview(t.Context(), record.JobID, owner, schema)
	if err != nil || !restarted.Ready || restarted.ReviewDigest != result.Review.ReviewDigest ||
		restarted.AssignmentDigest != result.Review.AssignmentDigest {
		t.Fatalf("restarted review = %#v, err=%v", restarted, err)
	}
}

func TestFormReviewUsesOnlyDeclaredEquivalentFallback(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	policy := FormAuditPolicy{
		PrimaryModel: "audit-primary", EquivalentFallbacks: []string{"audit-equivalent"},
	}
	record := createReviewMappedJob(t, store, owner, schema, policy, "fallback value")
	auditor := &testFormAuditor{
		proposals: map[string]FormAuditProposal{
			"audit-equivalent": {Decision: FormAuditPass},
			"undeclared":       {Decision: FormAuditPass},
		},
		errors: map[string]error{"audit-primary": errors.New("provider unavailable")},
	}
	result, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: auditor,
	})
	if err != nil || !result.UsedFallback || result.Job.AuditModel != "audit-equivalent" ||
		!slices.Equal(auditor.calls, []string{"audit-primary", "audit-equivalent"}) {
		t.Fatalf("fallback review = %#v, calls=%v, err=%v", result, auditor.calls, err)
	}
}

func TestFormReviewBlockerRequiresCorrectionBeforeReaudit(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	policy := FormAuditPolicy{PrimaryModel: "audit-primary"}
	record := createReviewMappedJob(t, store, owner, schema, policy, "ambiguous audit value")
	auditor := &testFormAuditor{proposals: map[string]FormAuditProposal{
		"audit-primary": {
			Decision: FormAuditBlock,
			Findings: []FormAuditFinding{{
				FieldID: schema.Fields[0].ID, Code: "field_confirmation_required",
			}},
		},
	}}
	blocked, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: auditor,
	})
	if err != nil || blocked.Job.State != FormJobCollecting || blocked.Review.Ready ||
		len(blocked.Job.AuditBlockers) != 1 || blocked.Job.ReviewRevision != 0 {
		t.Fatalf("blocked review = %#v, err=%v", blocked, err)
	}
	summary, err := store.FormMappingSummary(t.Context(), record.JobID, owner, schema)
	if err != nil || summary.ReadyForReview ||
		!mappingSummaryHasBlocker(summary, schema.Fields[0].ID, "field_confirmation_required") {
		t.Fatalf("audit-blocked summary = %#v, err=%v", summary, err)
	}
	if _, err = store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: blocked.Job.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: auditor,
	}); !errors.Is(err, ErrFormReviewStale) {
		t.Fatalf("reaudit without correction error = %v", err)
	}

	previous := currentField(t, blocked.Job, schema.Fields[0].ID)
	correctedRaw := appendMappedSource(
		t,
		store,
		blocked.Job,
		owner,
		schema.Fields[0].ID,
		"audit-correction",
		FormProtectedValue{Kind: ProtectedValueText, Text: "confirmed correction"},
		FormValueSupplied,
		FormBlankNone,
		previous.EventID,
	)
	if correctedRaw.AuditRevision != 0 || len(correctedRaw.AuditBlockers) != 0 ||
		correctedRaw.AssignmentDigest != "" {
		t.Fatalf("correction retained stale audit = %#v", correctedRaw)
	}
	source := currentField(t, correctedRaw, schema.Fields[0].ID)
	mapped, err := store.MapFormField(t.Context(), FormFieldMappingRequest{
		JobID: correctedRaw.JobID, ExpectedRevision: correctedRaw.Revision, Owner: owner,
		Schema: schema, FieldID: source.FieldID, SourceEventID: source.EventID,
		IdempotencyKey: "audit-correction-map",
	})
	if err != nil {
		t.Fatal(err)
	}
	auditor.proposals["audit-primary"] = FormAuditProposal{Decision: FormAuditPass}
	ready, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: mapped.Job.JobID, ExpectedRevision: mapped.Job.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: auditor,
	})
	if err != nil || !ready.Review.Ready {
		t.Fatalf("corrected review = %#v, err=%v", ready, err)
	}
}

func TestFormReviewFailsClosedOnPolicyChangeRaceAndInvalidProposal(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	policy := FormAuditPolicy{PrimaryModel: "audit-primary", EquivalentFallbacks: []string{"audit-fallback"}}
	record := createReviewMappedJob(t, store, owner, schema, policy, "race value")
	changedPolicy := FormAuditPolicy{PrimaryModel: "different-audit"}
	if _, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: changedPolicy, Auditor: &testFormAuditor{},
	}); !errors.Is(err, ErrFormAuditPolicyChanged) {
		t.Fatalf("changed policy error = %v", err)
	}

	invalid := &testFormAuditor{proposals: map[string]FormAuditProposal{
		"audit-primary":  {Decision: FormAuditPass, Findings: []FormAuditFinding{{FieldID: "bad", Code: "bad"}}},
		"audit-fallback": {Decision: "invented"},
	}}
	if _, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: invalid,
	}); !errors.Is(err, ErrFormAuditUnavailable) {
		t.Fatalf("invalid proposal error = %v", err)
	}
	if !slices.Equal(invalid.calls, []string{"audit-primary", "audit-fallback"}) {
		t.Fatalf("invalid proposal calls = %v", invalid.calls)
	}

	racing := &testFormAuditor{proposals: map[string]FormAuditProposal{
		"audit-primary": {Decision: FormAuditPass},
	}}
	racing.onAudit = func(_ string, _ FormAuditView) {
		current, getErr := store.Get(t.Context(), record.JobID, owner)
		if getErr != nil {
			t.Fatal(getErr)
		}
		field := currentField(t, current, schema.Fields[0].ID)
		if _, _, appendErr := store.AppendValue(t.Context(), FormJobAppendValueRequest{
			JobID: current.JobID, ExpectedRevision: current.Revision, Owner: owner,
			FieldID: field.FieldID, IdempotencyKey: "audit-race-correction",
			Value: FormProtectedValue{Kind: ProtectedValueText, Text: "raced correction"},
			State: FormValueSupplied, Source: FormValueSourceUser, SupersedesEventID: field.EventID,
		}); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	if _, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: racing,
	}); !errors.Is(err, ErrFormReviewStale) {
		t.Fatalf("audit race error = %v", err)
	}
	current, err := store.Get(t.Context(), record.JobID, owner)
	if err != nil || current.State == FormJobReviewReady || current.ReviewRevision != 0 {
		t.Fatalf("raced record = %#v, err=%v", current, err)
	}
}

func createReviewMappedJob(
	t *testing.T,
	store *FormJobStore,
	owner FormJobOwner,
	schema FormFieldsFacts,
	policy FormAuditPolicy,
	value string,
) FormJobRecord {
	t.Helper()
	policyRevision, err := policy.Revision()
	if err != nil {
		t.Fatal(err)
	}
	schemaDigest, err := FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	backendRevision, err := FormFieldsBackendRevision(schema)
	if err != nil {
		t.Fatal(err)
	}
	request := testFormJobCreateRequest(owner)
	request.SourceDigest = schema.SourceSHA256
	request.FieldSchemaDigest = schemaDigest
	request.BackendRevision = backendRevision
	request.AuditPolicyRevision = policyRevision
	record, err := store.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	record = appendMappedSource(t, store, record, owner, schema.Fields[0].ID, "review-source-"+value,
		FormProtectedValue{Kind: ProtectedValueText, Text: value}, FormValueSupplied, FormBlankNone, "")
	source := currentField(t, record, schema.Fields[0].ID)
	mapped, err := store.MapFormField(t.Context(), FormFieldMappingRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, FieldID: source.FieldID, SourceEventID: source.EventID,
		IdempotencyKey: "review-map-" + value,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mapped.Job
}
