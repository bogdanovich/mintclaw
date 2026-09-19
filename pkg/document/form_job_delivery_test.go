package document

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFormDeliveryAdmissionAndSettlement(t *testing.T) {
	t.Run("delivered", func(t *testing.T) {
		store, options, delivering := newTestDeliveringFormJob(t)
		request := testFormDeliveryRequest(delivering)
		admitted, publish, err := store.AdmitFormDeliveryRecovery(t.Context(), request)
		if err != nil || !publish || admitted.State != FormJobDelivering {
			t.Fatalf("admission = %#v, %t, %v", admitted, publish, err)
		}
		request.Outcome = FormDeliveryDelivered
		completed, err := store.SettleFormDelivery(t.Context(), request)
		if err != nil || completed.State != FormJobCompleted || completed.FailureCode != "" ||
			completed.ArtifactRef != delivering.ArtifactRef || completed.ReviewDigest == "" {
			t.Fatalf("completed = %#v, %v", completed, err)
		}
		replayed, err := store.SettleFormDelivery(t.Context(), request)
		if err != nil || replayed.Revision != completed.Revision {
			t.Fatalf("replayed settlement = %#v, %v", replayed, err)
		}
		request.Outcome = FormDeliveryAmbiguous
		if _, err = store.SettleFormDelivery(t.Context(), request); !errors.Is(err, ErrFormJobConflict) {
			t.Fatalf("conflicting settlement error = %v", err)
		}
		request.Outcome = ""
		terminal, publish, err := store.AdmitFormDeliveryRecovery(t.Context(), request)
		if err != nil || publish || terminal.State != FormJobCompleted {
			t.Fatalf("terminal admission = %#v, %t, %v", terminal, publish, err)
		}
		assertStoredJobHasNoCiphertext(t, options, delivering.JobID)
		store.Close()
		reopened, openErr := OpenFormJobStore(options)
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(reopened.Close)
		persisted, getErr := reopened.Get(t.Context(), delivering.JobID, testFormJobOwner())
		if getErr != nil || persisted.State != FormJobCompleted {
			t.Fatalf("persisted completion = %#v, %v", persisted, getErr)
		}
	})

	for _, scenario := range []struct {
		name        string
		outcome     FormDeliveryOutcome
		state       FormJobState
		failureCode string
	}{
		{
			name: "definite failure", outcome: FormDeliveryDefinitelyFailed, state: FormJobFailed,
			failureCode: string(FailureDeliveryFailed),
		},
		{
			name: "ambiguous", outcome: FormDeliveryAmbiguous, state: FormJobUncertain,
			failureCode: string(FailureDeliveryAmbiguous),
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store, options, delivering := newTestDeliveringFormJob(t)
			request := testFormDeliveryRequest(delivering)
			request.Outcome = scenario.outcome
			settled, err := store.SettleFormDelivery(t.Context(), request)
			if err != nil || settled.State != scenario.state || settled.FailureCode != scenario.failureCode ||
				settled.ArtifactRef != delivering.ArtifactRef || settled.ReviewDigest != "" {
				t.Fatalf("settled = %#v, %v", settled, err)
			}
			assertStoredJobHasNoCiphertext(t, options, delivering.JobID)
		})
	}
}

func TestFormDeliveryRecoveryRejectsWrongAuthorityAndExpiredJob(t *testing.T) {
	store, options, delivering := newTestDeliveringFormJob(t)
	request := testFormDeliveryRequest(delivering)
	wrong := request
	wrong.OwnerDigest = strings.Repeat("f", 64)
	if _, _, err := store.AdmitFormDeliveryRecovery(t.Context(), wrong); !errors.Is(err, ErrFormJobUnauthorized) {
		t.Fatalf("wrong owner error = %v", err)
	}
	wrong = request
	wrong.OperationID = NewWriteOperationID()
	if _, _, err := store.AdmitFormDeliveryRecovery(t.Context(), wrong); !errors.Is(err, ErrFormJobUnauthorized) {
		t.Fatalf("wrong operation error = %v", err)
	}
	wrong = request
	wrong.JobID += " "
	if _, _, err := store.AdmitFormDeliveryRecovery(t.Context(), wrong); err == nil {
		t.Fatal("non-canonical delivery identity was accepted")
	}
	store.now = func() time.Time { return time.UnixMilli(delivering.ExpiresAt + 1) }
	expired, publish, err := store.AdmitFormDeliveryRecovery(t.Context(), request)
	if err != nil || publish || expired.State != FormJobUncertain ||
		expired.FailureCode != formJobExpiredDuringCommitFailure || expired.ReviewDigest == "" ||
		expired.OperationID != delivering.OperationID || expired.ArtifactRef != delivering.ArtifactRef {
		t.Fatalf("expired admission = %#v, %t, %v", expired, publish, err)
	}
	assertStoredJobHasNoCiphertext(t, options, delivering.JobID)
	request.Outcome = FormDeliveryDelivered
	settled, err := store.SettleFormDelivery(t.Context(), request)
	if err != nil || settled.State != FormJobCompleted || settled.FailureCode != "" ||
		settled.ReviewDigest != delivering.ReviewDigest || settled.ArtifactRef != delivering.ArtifactRef {
		t.Fatalf("settlement after delivery expiry = %#v, %v", settled, err)
	}
	assertStoredJobHasNoCiphertext(t, options, delivering.JobID)
}

func TestFormDeliveryFailureReceiptSupersedesDeliveryExpiry(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		outcome     FormDeliveryOutcome
		state       FormJobState
		failureCode string
	}{
		{
			name: "definitely failed", outcome: FormDeliveryDefinitelyFailed,
			state: FormJobFailed, failureCode: string(FailureDeliveryFailed),
		},
		{
			name: "ambiguous", outcome: FormDeliveryAmbiguous,
			state: FormJobUncertain, failureCode: string(FailureDeliveryAmbiguous),
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store, options, delivering := newTestDeliveringFormJob(t)
			request := testFormDeliveryRequest(delivering)
			store.now = func() time.Time { return time.UnixMilli(delivering.ExpiresAt + 1) }
			if _, _, err := store.AdmitFormDeliveryRecovery(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			request.Outcome = scenario.outcome
			settled, err := store.SettleFormDelivery(t.Context(), request)
			if err != nil || settled.State != scenario.state || settled.FailureCode != scenario.failureCode ||
				settled.OperationID != delivering.OperationID || settled.ArtifactRef != delivering.ArtifactRef {
				t.Fatalf("settlement after delivery expiry = %#v, %v", settled, err)
			}
			assertStoredJobHasNoCiphertext(t, options, delivering.JobID)
		})
	}
}

func newTestDeliveringFormJob(t *testing.T) (*FormJobStore, FormJobStoreOptions, FormJobRecord) {
	t.Helper()
	store, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	policy := testFormAuditPolicy("delivery-audit")
	record := createReviewMappedJob(t, store, owner, schema, policy, "delivery-private-value")
	reviewed, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		Schema: schema, Policy: policy, Auditor: &testFormAuditor{proposals: map[string]FormAuditProposal{
			"delivery-audit": {Decision: FormAuditPass},
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
	request.ExpectedRevision = awaiting.Revision
	committing, err := store.BeginFormCommit(t.Context(), request, binding)
	if err != nil {
		t.Fatal(err)
	}
	delivering, err := store.RecordFormCommitArtifact(t.Context(), FormCommitArtifactRequest{
		JobID: committing.JobID, ExpectedRevision: committing.Revision, Owner: owner,
		OperationID: committing.OperationID, ArtifactRef: "media://11111111-1111-4111-8111-111111111111",
		ArtifactDigest: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, options, delivering
}

func testFormDeliveryRequest(record FormJobRecord) FormDeliveryRequest {
	return FormDeliveryRequest{
		JobID: record.JobID, OwnerDigest: record.OwnerDigest,
		OperationID: record.OperationID, ArtifactRef: record.ArtifactRef,
	}
}
