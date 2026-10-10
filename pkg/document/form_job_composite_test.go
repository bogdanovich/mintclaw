package document

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func compositeFixtureRequest(t *testing.T, store *FormJobStore) FormCompositeRequest {
	t.Helper()
	_, schema := fillNormalizationFixture()
	owner := testFormJobOwner()
	job := createMappedFormJob(t, store, owner, schema)
	values := []FormProtectedValue{
		{Kind: ProtectedValueText, Text: "Synthetic Person"},
		{Kind: ProtectedValueText, Text: "MINTCLAW_PDFI4_COMPOSITE_PRIVATE_f128"},
		{Kind: ProtectedValueText, Text: "yes"},
		{Kind: ProtectedValueText, Text: "United States"},
		{Kind: ProtectedValueChoice, Text: "CA"},
		{Kind: ProtectedValueChoices, Choices: []string{"one", "three"}},
		{Kind: ProtectedValueChoice, Text: "one"},
		{Kind: ProtectedValueText, Text: "02/15/1990"},
	}
	assignments := make([]FormCompositeAssignment, 0, len(values))
	for index, value := range values {
		assignments = append(assignments, FormCompositeAssignment{FieldID: schema.Fields[index].ID, Value: value})
	}
	return FormCompositeRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: owner, Schema: schema,
		Assignments: assignments, IdempotencyKey: "platform-message-composite-1",
	}
}

func TestFormCompositeAtomicReceiptNormalizationRestartAndPrivacy(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	first, err := store.AppendComposite(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused || first.Job.Revision != 9 || first.Job.LedgerRevision != 8 || len(first.FieldIDs) != 8 {
		t.Fatalf("composite result = %#v", first)
	}
	jobID, batchID, err := ParseFormCompositeReceipt(first.Receipt)
	if err != nil || jobID != request.JobID || batchID == "" {
		t.Fatalf("receipt shape = (%q, %q, %v)", jobID, batchID, err)
	}
	for _, field := range first.Job.Fields {
		if field.State != FormValueConfirmed || field.Validation != FormValueValidationValid {
			t.Fatalf("unconfirmed composite field = %#v", field)
		}
	}
	values, err := store.ReadValues(t.Context(), request.JobID, request.Owner, first.FieldIDs)
	if err != nil || values[request.Schema.Fields[2].ID].Value.Boolean == nil ||
		!*values[request.Schema.Fields[2].ID].Value.Boolean || values[request.Schema.Fields[3].ID].Value.Text != "US" {
		t.Fatalf("normalization failed, error = %v", err)
	}
	assertFormStoreContainsNoPlaintext(t, options, request.Assignments[1].Value.Text, "Synthetic Person")
	encoded, err := json.Marshal(first)
	if err != nil || strings.Contains(string(encoded), request.Assignments[1].Value.Text) ||
		strings.Contains(string(encoded), "Synthetic Person") {
		t.Fatal("protected value leaked into composite result")
	}
	request.Assignments = slices.Clone(request.Assignments)
	slices.Reverse(request.Assignments)
	replayed, err := store.AppendComposite(t.Context(), request)
	if err != nil || !replayed.Reused || replayed.Receipt != first.Receipt ||
		replayed.Job.Revision != first.Job.Revision {
		t.Fatalf("reordered replay = %#v, %v", replayed, err)
	}
	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	read, err := reopened.ReadCompositeReceipt(t.Context(), first.Receipt, request.Owner)
	if err != nil || read.Receipt != first.Receipt || !slices.Equal(read.FieldIDs, first.FieldIDs) {
		t.Fatalf("restarted receipt = %#v, %v", read, err)
	}
	wrongOwner := request.Owner
	wrongOwner.SenderID = "other"
	if _, err := reopened.ReadCompositeReceipt(t.Context(), first.Receipt, wrongOwner); !errors.Is(
		err, ErrFormJobUnauthorized,
	) {
		t.Fatalf("cross-owner receipt read: %v", err)
	}
	replayed, err = reopened.AppendComposite(t.Context(), request)
	if err != nil || !replayed.Reused || replayed.Receipt != first.Receipt {
		t.Fatalf("restarted replay = %#v, %v", replayed, err)
	}
}

func TestFormCompositeRejectsInvalidSetWithoutAnyPrefix(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*FormCompositeRequest)
		want   string
	}{
		{"unknown", func(r *FormCompositeRequest) { r.Assignments[7].FieldID = fillFieldID(99) }, "field_unresolved"},
		{"invalid type", func(r *FormCompositeRequest) {
			r.Assignments[7].Value = FormProtectedValue{Kind: ProtectedValueChoice, Text: "not a date"}
		}, "field_invalid"},
		{"invalid choice", func(r *FormCompositeRequest) {
			r.Assignments[3].Value.Text = "not an export"
		}, "field_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newTestFormJobStore(t)
			request := compositeFixtureRequest(t, store)
			test.mutate(&request)
			result, err := store.AppendComposite(t.Context(), request)
			var validation *FormCompositeValidationError
			if !errors.As(err, &validation) || len(validation.Findings) != 1 ||
				validation.Findings[0].Code != test.want ||
				result.Receipt != "" ||
				result.Job.JobID != "" {
				t.Fatalf("rejection = %#v, %v", result, err)
			}
			current, err := store.Get(t.Context(), request.JobID, request.Owner)
			if err != nil || current.Revision != request.ExpectedRevision || len(current.Fields) != 0 {
				t.Fatalf("rejected set left a prefix = %#v, %v", current, err)
			}
		})
	}
}

func TestFormCompositeIdempotencyBindsWholeOriginalSet(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	first, err := store.AppendComposite(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*FormCompositeRequest){
		func(r *FormCompositeRequest) { r.Assignments = r.Assignments[:1] },
		func(r *FormCompositeRequest) { r.Assignments[0].Value.Text = "Changed" },
		func(r *FormCompositeRequest) { r.ExpectedRevision = first.Job.Revision },
	} {
		changed := request
		changed.Assignments = slices.Clone(request.Assignments)
		change(&changed)
		if result, err := store.AppendComposite(t.Context(), changed); !errors.Is(err, ErrFormJobAnswerConflict) ||
			result.Receipt != "" {
			t.Fatalf("changed set replay = %#v, %v", result, err)
		}
	}
	read, err := store.ReadCompositeReceipt(t.Context(), first.Receipt, request.Owner)
	if err != nil || read.Job.Revision != first.Job.Revision {
		t.Fatalf("conflicting replays modified ledger: %#v, %v", read, err)
	}
}

func TestFormCompositeAuthorityIdentityBoundsAndPendingAnswer(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	for _, mutate := range []func(*FormCompositeRequest){
		func(r *FormCompositeRequest) { r.Owner.SenderID = "other" },
		func(r *FormCompositeRequest) { r.Owner.TopicID = "other" },
		func(r *FormCompositeRequest) { r.Owner.RouteSessionKey = "other" },
		func(r *FormCompositeRequest) { r.ExpectedRevision++ },
		func(r *FormCompositeRequest) { r.Schema.SourceSHA256 = strings.Repeat("b", 64) },
		func(r *FormCompositeRequest) { r.Schema.Fields[0].Required = false },
		func(r *FormCompositeRequest) { r.Schema.Backend.Version = "other" },
		func(r *FormCompositeRequest) { r.Assignments = nil },
		func(r *FormCompositeRequest) { r.Assignments = append(r.Assignments, r.Assignments[0]) },
		func(r *FormCompositeRequest) {
			r.Assignments[1].Value.Text = strings.Repeat("x", MaxFormCompositeValueBytes+1)
		},
	} {
		changed := request
		changed.Assignments = slices.Clone(request.Assignments)
		changed.Schema.Fields = slices.Clone(request.Schema.Fields)
		mutate(&changed)
		if _, err := store.AppendComposite(t.Context(), changed); err == nil {
			t.Fatal("invalid identity/bound request accepted")
		}
	}
	binding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
		JobID: request.JobID, ExpectedRevision: request.ExpectedRevision, Owner: request.Owner,
		FieldID: request.Assignments[0].FieldID,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := store.openProtectedAnswerBinding(t.Context(), binding.Token, request.Owner)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.stageProtectedValue(t.Context(), FormJobAppendValueRequest{
		JobID: payload.JobID, ExpectedRevision: payload.ExpectedRevision, Owner: request.Owner,
		FieldID: payload.FieldID, IdempotencyKey: "pending-single", Value: request.Assignments[0].Value,
		State: FormValueSupplied, Source: FormValueSourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendComposite(t.Context(), request); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("pending single answer did not fence composite: %v", err)
	}
}

func TestFormCompositeConcurrentDuplicateHasOneSnapshotWrite(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	write := store.writeFile
	var writes int
	store.writeFile = func(path string, data []byte, mode os.FileMode) error {
		writes++
		return write(path, data, mode)
	}
	results := make([]FormCompositeResult, 12)
	errs := make([]error, len(results))
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index], errs[index] = store.AppendComposite(t.Context(), request)
		}()
	}
	group.Wait()
	if writes != 1 {
		t.Fatalf("duplicate writes = %d", writes)
	}
	for index, result := range results {
		if errs[index] != nil || result.Receipt != results[0].Receipt || result.Job.LedgerRevision != 8 {
			t.Fatalf("concurrent duplicate %d = %#v, %v", index, result, errs[index])
		}
	}
}

func TestFormCompositeWriteFailureReturnsNoReceiptOrMutation(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	write := store.writeFile
	store.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic write failure") }
	result, err := store.AppendComposite(t.Context(), request)
	if !errors.Is(err, ErrFormJobStoreUnavailable) || result.Receipt != "" || result.Job.JobID != "" {
		t.Fatalf("failed write advertised acceptance: %#v, %v", result, err)
	}
	store.writeFile = write
	current, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || current.Revision != 1 || len(current.Fields) != 0 {
		t.Fatalf("failed write changed persisted state: %#v, %v", current, err)
	}
	if _, err := store.AppendComposite(t.Context(), request); err != nil {
		t.Fatalf("retry after write failure: %v", err)
	}
}

func TestFormCompositeFailureDoesNotAliasCorrectionWhilePruningAnotherJob(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	first, err := store.AppendComposite(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	expiringRequest := testFormJobCreateRequest(request.Owner)
	expiringRequest.StartIdempotencyKey = "expire-unrelated"
	expiringRequest.Retention = time.Minute
	expiring, err := store.Create(t.Context(), expiringRequest)
	if err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "correct-two"
	request.ExpectedRevision = first.Job.Revision
	request.Assignments = request.Assignments[:2]
	for index := range request.Assignments {
		request.Assignments[index].SupersedesEventID = first.Job.Fields[index].EventID
		request.Assignments[index].Value.Text = "Corrected"
	}
	store.now = func() time.Time { return time.UnixMilli(expiring.ExpiresAt).Add(time.Second) }
	store.random = io.LimitReader(rand.Reader, 24) // Exactly one envelope nonce, then construction fails.
	if result, err := store.AppendComposite(t.Context(), request); err == nil || result.Receipt != "" {
		t.Fatalf("failed second seal = %#v, %v", result, err)
	}
	current, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || current.Revision != first.Job.Revision || !slices.Equal(current.Fields, first.Job.Fields) {
		t.Fatalf("prune saved an aliased partial correction: %#v, %v", current, err)
	}
	other, err := store.Get(t.Context(), expiring.JobID, request.Owner)
	if err != nil || other.State != FormJobExpired {
		t.Fatalf("unrelated expiry was not saved: %#v, %v", other, err)
	}
}

func TestFormCompositeCorrectionInvalidatesAuthorityAndDoesNotReplayOldValue(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	first, err := store.AppendComposite(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	policy := testFormAuditPolicy("composite-audit")
	policyRevision, err := policy.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.update(t.Context(), func(snapshot *formJobStoreDocument, _ time.Time) (bool, error) {
		record := snapshot.Records[request.JobID]
		record.Public.AuditPolicyRevision = policyRevision
		snapshot.Records[request.JobID] = record
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	review, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: request.JobID, ExpectedRevision: first.Job.Revision, Owner: request.Owner, Schema: request.Schema,
		Policy: policy, Auditor: &testFormAuditor{proposals: map[string]FormAuditProposal{
			"composite-audit": {Decision: FormAuditPass},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, binding, err := store.PrepareFormCommitApproval(t.Context(), FormCommitRequest{
		JobID: request.JobID, ExpectedRevision: review.Job.Revision, Owner: request.Owner, Schema: request.Schema,
		AuditPolicyRevision: policyRevision, OutputPolicyRevision: FormCommitOutputPolicyRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	correction := request
	correction.ExpectedRevision = approved.Revision
	correction.IdempotencyKey = "correction"
	correction.Assignments = []FormCompositeAssignment{{
		FieldID: first.Job.Fields[0].FieldID, SupersedesEventID: first.Job.Fields[0].EventID,
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Corrected"},
	}}
	corrected, err := store.AppendComposite(t.Context(), correction)
	if err != nil || corrected.Job.ReviewDigest != "" || corrected.Job.ApprovalDigest != "" ||
		corrected.Job.LedgerRevision != 9 {
		t.Fatalf("correction = %#v, %v", corrected, err)
	}
	if _, err := store.BeginFormCommit(t.Context(), FormCommitRequest{
		JobID: request.JobID, ExpectedRevision: approved.Revision, Owner: request.Owner, Schema: request.Schema,
		AuditPolicyRevision: policyRevision, OutputPolicyRevision: FormCommitOutputPolicyRevision,
	}, binding); !errors.Is(err, ErrFormApprovalStale) {
		t.Fatalf("old commit approval remained usable: %v", err)
	}
	replayed, err := store.AppendComposite(t.Context(), request)
	if err != nil || !replayed.Reused || replayed.Job.Revision != corrected.Job.Revision {
		t.Fatalf("old batch replay = %#v, %v", replayed, err)
	}
	values, err := store.ReadValues(t.Context(), request.JobID, request.Owner, correctionFieldIDs(correction))
	if err != nil || values[correction.Assignments[0].FieldID].Value.Text != "Corrected" {
		t.Fatal("old receipt overwrote the correction")
	}
	if _, err := store.Cancel(t.Context(), request.JobID, corrected.Job.Revision, request.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadCompositeReceipt(t.Context(), first.Receipt, request.Owner); err == nil {
		t.Fatal("canceled job retained composite authority")
	}
}

func correctionFieldIDs(request FormCompositeRequest) []string {
	ids := make([]string, 0, len(request.Assignments))
	for _, assignment := range request.Assignments {
		ids = append(ids, assignment.FieldID)
	}
	return ids
}

func TestFormCompositeAssignmentAndEventCaps(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	request.Assignments = make([]FormCompositeAssignment, MaxFormCompositeAssignments)
	request.Schema.Fields = make([]FormField, MaxFormCompositeAssignments)
	for index := range request.Assignments {
		id := fillFieldID(index + 1)
		request.Schema.Fields[index] = FormField{
			ID: id, Kind: FormFieldText,
			Widgets: []FormFieldWidget{{ID: fillWidgetID(index + 1), Page: 1, Ordinal: 1}},
		}
		request.Assignments[index] = FormCompositeAssignment{
			FieldID: id, Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Synthetic"},
		}
	}
	// Create a job for the expanded exact schema, independently of the earlier
	// fixture job; a changed schema must never be rebound onto an existing job.
	owner := request.Owner
	owner.RouteSessionKey = "expanded-composite"
	job := createMappedFormJob(t, store, owner, request.Schema)
	request.JobID, request.ExpectedRevision, request.Owner = job.JobID, job.Revision, owner
	excess := request
	excess.Assignments = append(slices.Clone(request.Assignments), FormCompositeAssignment{
		FieldID: fillFieldID(100), Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Extra"},
	})
	if _, err := store.AppendComposite(t.Context(), excess); err == nil {
		t.Fatal("assignment cap exceeded")
	}
	store.maxEventsPerJob = MaxFormCompositeAssignments - 1
	if _, err := store.AppendComposite(t.Context(), request); !errors.Is(err, ErrFormJobCapacityExceeded) {
		t.Fatalf("event cap error = %v", err)
	}
	store.maxEventsPerJob = DefaultFormJobMaxEvents
	result, err := store.AppendComposite(t.Context(), request)
	if err != nil || len(result.FieldIDs) != MaxFormCompositeAssignments {
		t.Fatalf("exact assignment cap rejected: %#v, %v", result, err)
	}
}

func TestFormCompositeRequiresExplicitExistingValueReplacement(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	request.Schema.Fields[0].HasValue = true
	owner := request.Owner
	owner.RouteSessionKey = "preserved-composite"
	job := createMappedFormJob(t, store, owner, request.Schema)
	request.JobID, request.ExpectedRevision, request.Owner = job.JobID, job.Revision, owner
	if _, err := store.AppendComposite(t.Context(), request); err == nil {
		t.Fatal("preserved value overwritten without correction intent")
	}
	request.Assignments[0].ReplaceExisting = true
	if _, err := store.AppendComposite(t.Context(), request); err != nil {
		t.Fatalf("explicit preserved-value replacement: %v", err)
	}
}

func TestFormCompositeRejectsIncompleteAuthenticatedGroup(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	first, err := store.AppendComposite(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	// Re-seal a semantically malformed payload and authenticate the snapshot:
	// this exercises group validation, not merely the AEAD/integrity checks.
	if err := store.update(t.Context(), func(snapshot *formJobStoreDocument, _ time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		payload, err := openFormJobValuePayload(key, record.Events[0])
		if err != nil {
			return false, err
		}
		payload.Composite.Count++
		envelope, err := sealFormJobEnvelope(key, record.Events[0], payload, rand.Reader)
		if err != nil {
			return false, err
		}
		record.Events[0] = envelope
		// Rechain later envelopes so membership is the only broken invariant.
		for index := 1; index < len(record.Events); index++ {
			next, err := openFormJobValuePayload(key, record.Events[index])
			if err != nil {
				return false, err
			}
			next.PreviousDigest, err = formJobJSONDigest(record.Events[index-1])
			if err != nil {
				return false, err
			}
			record.Events[index], err = sealFormJobEnvelope(key, record.Events[index], next, rand.Reader)
			if err != nil {
				return false, err
			}
		}
		record.Public.LedgerDigest, err = formJobJSONDigest(record.Events[len(record.Events)-1])
		snapshot.Records[request.JobID] = record
		return true, err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadCompositeReceipt(t.Context(), first.Receipt, request.Owner); !errors.Is(
		err, ErrFormJobRecordCorrupt,
	) {
		t.Fatalf("incomplete authenticated group accepted: %v", err)
	}
}

func TestFormCompositeAggregateValueBudgetIsIndependentOfPerFieldLimit(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := compositeFixtureRequest(t, store)
	request.Schema.Fields = request.Schema.Fields[:3]
	request.Assignments = request.Assignments[:3]
	for index := range request.Schema.Fields {
		request.Schema.Fields[index].Kind = FormFieldText
		request.Schema.Fields[index].Multiline = true
		request.Schema.Fields[index].MaxLength = 0
		request.Assignments[index].Value = FormProtectedValue{
			Kind: ProtectedValueText, Text: strings.Repeat("x", DefaultMaxFormValueBytes),
		}
	}
	request.Assignments[2].Value.Text = "x"
	owner := request.Owner
	owner.RouteSessionKey = "aggregate-composite"
	job := createMappedFormJob(t, store, owner, request.Schema)
	request.JobID, request.ExpectedRevision, request.Owner = job.JobID, job.Revision, owner
	if _, err := store.AppendComposite(t.Context(), request); !errors.Is(err, ErrFormJobCapacityExceeded) {
		t.Fatalf("aggregate budget exceeded without per-field overflow: %v", err)
	}
	request.Assignments = request.Assignments[:2]
	if _, err := store.AppendComposite(t.Context(), request); err != nil {
		t.Fatalf("exact aggregate budget rejected: %v", err)
	}
}
