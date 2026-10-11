package document

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func outputScopeFixture(
	t *testing.T, mode FormOutputMode,
) (*FormJobStore, FormJobStoreOptions, FormJobRecord, FormFieldsFacts, FormAuditPolicy) {
	t.Helper()
	store, options := newTestFormJobStore(t)
	schema := *successfulTestFormFields()
	schema.Fields[0].ID = "field_" + strings.Repeat("a", 64)
	schema.Fields[0].Required = true
	for index, letter := range []string{"b", "c", "d"} {
		field := cloneFormField(schema.Fields[0])
		field.ID = "field_" + strings.Repeat(letter, 64)
		field.Name = "scope-field-" + letter
		field.HasValue = index == 1
		field.ReadOnly = index == 2
		field.Widgets[0].ID = "widget_" + strings.Repeat(letter, 64)
		field.Widgets[0].Ordinal = 1
		schema.Fields = append(schema.Fields, field)
	}
	policy := testFormAuditPolicy("scope-audit")
	request := testFormJobCreateRequest(testFormJobOwner())
	var err error
	request.SourceDigest = schema.SourceSHA256
	request.FieldSchemaDigest, err = FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	request.BackendRevision, err = FormFieldsBackendRevision(schema)
	if err != nil {
		t.Fatal(err)
	}
	request.AuditPolicyRevision, err = policy.Revision()
	if err != nil {
		t.Fatal(err)
	}
	request.OutputMode = mode
	job, err := store.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err = store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(),
		FieldID: schema.Fields[0].ID, IdempotencyKey: "scope-confirmed",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Synthetic scope value"},
		State: FormValueConfirmed, Source: FormValueSourceUser,
		Confidence: FormValueConfidenceConfirmed, Validation: FormValueValidationValid,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, options, job, schema, policy
}

func scopeReviewRequest(job FormJobRecord, schema FormFieldsFacts, policy FormAuditPolicy) FormReviewRequest {
	return FormReviewRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(), Schema: schema, Policy: policy,
		Auditor: &testFormAuditor{proposals: map[string]FormAuditProposal{"scope-audit": {Decision: FormAuditPass}}},
	}
}

func TestFormOutputScopeDraftRequiresApprovalAndPreservesAssignmentsAfterRestart(t *testing.T) {
	store, options, job, schema, policy := outputScopeFixture(t, FormOutputEditableAcroForm)
	if _, err := store.ReviewFormJob(
		t.Context(),
		scopeReviewRequest(job, schema, policy),
	); !errors.Is(
		err,
		ErrFormReviewStale,
	) {
		t.Fatal("complete output accepted an unanswered required field")
	}
	scope := FormOutputScopeRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(), Schema: schema,
		Scope:            FormOutputEditableDraft,
		DeferredFieldIDs: []string{schema.Fields[2].ID, schema.Fields[1].ID, schema.Fields[0].ID},
	}
	draft, err := store.SetFormOutputScope(t.Context(), scope)
	if err != nil || draft.Revision != job.Revision+1 || draft.OutputMode != FormOutputEditableAcroForm ||
		draft.OutputScope != FormOutputEditableDraft || !schema.Fields[1].Required {
		t.Fatalf("draft scope failed or weakened the source requirement: %v", err)
	}
	// The returned and caller slices must not alias persisted authority.
	draft.DeferredFieldIDs[0] = "not-a-field"
	scope.DeferredFieldIDs[0] = "also-not-a-field"
	draft, err = store.Get(t.Context(), job.JobID, testFormJobOwner())
	if err != nil || slices.Contains(draft.DeferredFieldIDs, "not-a-field") {
		t.Fatal("public scope aliased a caller slice")
	}
	summary, err := store.FormMappingSummary(t.Context(), job.JobID, testFormJobOwner(), schema)
	if err != nil || !summary.ReadyForReview || len(summary.ConfirmedFieldIDs) != 2 ||
		!slices.Equal(summary.DeferredFieldIDs, []string{schema.Fields[1].ID}) {
		t.Fatalf("draft omitted an accepted or preserved value: %v", err)
	}
	auditRequest := scopeReviewRequest(draft, schema, policy)
	auditor := auditRequest.Auditor.(*testFormAuditor)
	reviewed, err := store.ReviewFormJob(t.Context(), auditRequest)
	if err != nil || !reviewed.Review.Ready ||
		reviewed.Review.RequestedAction != "fill_and_deliver_editable_draft" ||
		len(auditor.views) != 1 || !auditor.views[0].Fields[1].Deferred ||
		!auditor.views[0].Fields[1].Schema.Required || auditor.views[0].Fields[1].Value.Kind != "" ||
		!auditor.views[0].Fields[2].Existing {
		t.Fatalf("draft audit lost omission or existing-value evidence: %v", err)
	}
	commit := FormCommitRequest{
		JobID: job.JobID, ExpectedRevision: reviewed.Job.Revision, Owner: testFormJobOwner(), Schema: schema,
		AuditPolicyRevision: job.AuditPolicyRevision, OutputPolicyRevision: FormCommitOutputPolicyRevision,
	}
	if _, _, err = store.PrepareFormCommitApproval(t.Context(), commit); !errors.Is(err, ErrFormApprovalStale) {
		t.Fatal("a complete approval policy authorized a draft")
	}
	commit.OutputPolicyRevision = FormCommitDraftOutputPolicyRevision
	awaiting, binding, err := store.PrepareFormCommitApproval(t.Context(), commit)
	if err != nil || binding.OutputScope != FormOutputEditableDraft ||
		binding.OutputMode != FormOutputEditableAcroForm {
		t.Fatal("draft approval did not bind the output contract")
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if review, reviewErr := store.CurrentFormReview(
		t.Context(),
		job.JobID,
		testFormJobOwner(),
		schema,
	); reviewErr != nil || !review.Ready ||
		review.ReviewDigest != reviewed.Review.ReviewDigest {
		t.Fatal("restart changed the reviewed draft")
	}
	commit.ExpectedRevision = awaiting.Revision
	wrongPolicy := commit
	wrongPolicy.OutputPolicyRevision = FormCommitOutputPolicyRevision
	if _, err = store.BeginFormCommit(t.Context(), wrongPolicy, binding); !errors.Is(err, ErrFormApprovalStale) {
		t.Fatal("restart allowed a mismatched output policy")
	}
	committing, err := store.BeginFormCommit(t.Context(), commit, binding)
	if err != nil {
		t.Fatal(err)
	}
	commit.ExpectedRevision = committing.Revision
	_, fill, err := store.MaterializeFormCommit(t.Context(), commit)
	if err != nil || len(fill.Assignments) != 1 || fill.Assignments[0].FieldID != schema.Fields[0].ID {
		t.Fatalf("draft cleared a deferred field or dropped the accepted field: %v", err)
	}
	clearFillMap(&fill)
	artifact := FormCommitArtifactRequest{
		JobID: job.JobID, ExpectedRevision: committing.Revision, Owner: testFormJobOwner(),
		OperationID: committing.OperationID, ArtifactRef: "media://11111111-1111-4111-8111-111111111111",
		ArtifactDigest: strings.Repeat("f", 64), OutputMode: FormOutputFlattenedPrint,
	}
	if _, err = store.RecordFormCommitArtifact(t.Context(), artifact); !errors.Is(err, ErrFormApprovalStale) {
		t.Fatal("a flattened artifact fulfilled editable-draft authority")
	}
	artifact.OutputMode = FormOutputEditableAcroForm
	delivering, err := store.RecordFormCommitArtifact(t.Context(), artifact)
	if err != nil || delivering.State != FormJobDelivering {
		t.Fatal("verified output could not reach the delivery gate")
	}
}

func TestFormOutputScopeClearInvalidatesReviewApprovalAndRestoresFullRequirements(t *testing.T) {
	store, _, job, schema, policy := outputScopeFixture(t, FormOutputEditableAcroForm)
	scope := FormOutputScopeRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(), Schema: schema,
		Scope: FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[1].ID},
	}
	draft, err := store.SetFormOutputScope(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.SetFormOutputScope(t.Context(), scope)
	if err != nil || replay.Revision != draft.Revision {
		t.Fatal("exact scope replay advanced authority")
	}
	reviewed, err := store.ReviewFormJob(t.Context(), scopeReviewRequest(draft, schema, policy))
	if err != nil {
		t.Fatal(err)
	}
	commit := FormCommitRequest{
		JobID: job.JobID, ExpectedRevision: reviewed.Job.Revision, Owner: testFormJobOwner(), Schema: schema,
		AuditPolicyRevision: job.AuditPolicyRevision, OutputPolicyRevision: FormCommitDraftOutputPolicyRevision,
	}
	awaiting, binding, err := store.PrepareFormCommitApproval(t.Context(), commit)
	if err != nil {
		t.Fatal(err)
	}
	scope.Scope, scope.DeferredFieldIDs = "", nil
	if _, err = store.SetFormOutputScope(t.Context(), scope); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("stale scope change revoked current approval")
	}
	scope.ExpectedRevision = awaiting.Revision
	complete, err := store.SetFormOutputScope(t.Context(), scope)
	if err != nil || complete.State != FormJobCollecting || complete.ReviewDigest != "" ||
		complete.ApprovalDigest != "" || complete.OperationID != "" || complete.Revision != awaiting.Revision+1 {
		t.Fatal("changing scope retained prior write authority")
	}
	commit.ExpectedRevision = complete.Revision
	if _, err = store.BeginFormCommit(t.Context(), commit, binding); !errors.Is(err, ErrFormApprovalStale) {
		t.Fatal("scope change reused the old approval")
	}
	summary, err := store.FormMappingSummary(t.Context(), job.JobID, testFormJobOwner(), schema)
	if err != nil || summary.ReadyForReview || len(summary.Unresolved) != 1 || !schema.Fields[1].Required {
		t.Fatal("complete output did not restore its required-field blocker")
	}
}

func TestFormOutputScopeRejectsUnprovedCapabilityWithoutChangingJob(t *testing.T) {
	for _, mode := range []FormOutputMode{"", FormOutputFlattenedPrint} {
		t.Run(string(mode), func(t *testing.T) {
			store, _, job, schema, _ := outputScopeFixture(t, mode)
			if _, err := store.SetFormOutputScope(t.Context(), FormOutputScopeRequest{
				JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(), Schema: schema,
				Scope: FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[1].ID},
			}); err == nil {
				t.Fatal("unknown or flattened output capability authorized an editable draft")
			}
			after, err := store.Get(t.Context(), job.JobID, testFormJobOwner())
			if err != nil || after.Revision != job.Revision || after.OutputScope != "" || after.OperationID != "" {
				t.Fatal("refused draft changed the job or started a write")
			}
		})
	}
}

func TestFormOutputScopeCannotHideInvalidProvidedValues(t *testing.T) {
	store, _, job, schema, policy := outputScopeFixture(t, FormOutputEditableAcroForm)
	job, _, err := store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(),
		FieldID: schema.Fields[1].ID, IdempotencyKey: "scope-invalid",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Synthetic invalid value"},
		State: FormValueInvalid, Source: FormValueSourceUser,
		Confidence: FormValueConfidenceExact, Validation: FormValueValidationInvalid,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.SetFormOutputScope(t.Context(), FormOutputScopeRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(), Schema: schema,
		Scope: FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := store.FormMappingSummary(t.Context(), job.JobID, testFormJobOwner(), schema)
	if err != nil || summary.ReadyForReview || len(summary.DeferredFieldIDs) != 0 || len(summary.Unresolved) != 1 {
		t.Fatal("a deferred ID hid an invalid supplied value")
	}
	if _, err = store.ReviewFormJob(
		t.Context(),
		scopeReviewRequest(job, schema, policy),
	); !errors.Is(
		err,
		ErrFormReviewStale,
	) {
		t.Fatal("a draft reviewed an invalid supplied value")
	}
}

func TestFormOutputScopeRejectsWrongAuthoritySchemaAndUnboundedIdentities(t *testing.T) {
	store, _, job, schema, _ := outputScopeFixture(t, FormOutputEditableAcroForm)
	valid := FormOutputScopeRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(), Schema: schema,
		Scope: FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[1].ID},
	}
	for _, test := range []struct {
		name   string
		mutate func(*FormOutputScopeRequest)
	}{
		{name: "owner", mutate: func(request *FormOutputScopeRequest) { request.Owner.SenderID = "another-owner" }},
		{name: "source", mutate: func(request *FormOutputScopeRequest) {
			request.Schema.SourceSHA256 = strings.Repeat("f", 64)
		}},
		{name: "schema", mutate: func(request *FormOutputScopeRequest) {
			request.Schema.Fields = append([]FormField(nil), request.Schema.Fields...)
			request.Schema.Fields[1].Required = false
		}},
		{name: "readonly", mutate: func(request *FormOutputScopeRequest) {
			request.DeferredFieldIDs = []string{schema.Fields[3].ID}
		}},
		{name: "unknown", mutate: func(request *FormOutputScopeRequest) {
			request.DeferredFieldIDs = []string{"field_" + strings.Repeat("f", 64)}
		}},
		{name: "duplicate", mutate: func(request *FormOutputScopeRequest) {
			request.DeferredFieldIDs = []string{schema.Fields[1].ID, schema.Fields[1].ID}
		}},
		{name: "capacity", mutate: func(request *FormOutputScopeRequest) {
			request.DeferredFieldIDs = make([]string, DefaultMaxFormFields+1)
		}},
		{name: "complete omissions", mutate: func(request *FormOutputScopeRequest) { request.Scope = "" }},
		{name: "unknown scope", mutate: func(request *FormOutputScopeRequest) { request.Scope = "complete-ish" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if _, err := store.SetFormOutputScope(t.Context(), request); err == nil {
				t.Fatal("invalid scope request changed authority")
			}
			after, err := store.Get(t.Context(), job.JobID, testFormJobOwner())
			if err != nil || after.Revision != job.Revision || after.OutputScope != "" {
				t.Fatal("refused scope request changed the job")
			}
		})
	}
}

func TestFormOutputScopeNewlySuppliedDeferredFieldParticipatesNormally(t *testing.T) {
	store, _, job, schema, _ := outputScopeFixture(t, FormOutputEditableAcroForm)
	job, err := store.SetFormOutputScope(t.Context(), FormOutputScopeRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(), Schema: schema,
		Scope: FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err = store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(),
		FieldID: schema.Fields[1].ID, IdempotencyKey: "scope-later",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Synthetic later value"},
		State: FormValueConfirmed, Source: FormValueSourceUser,
		Confidence: FormValueConfidenceConfirmed, Validation: FormValueValidationValid,
	})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := store.FormMappingSummary(t.Context(), job.JobID, testFormJobOwner(), schema)
	if err != nil || !summary.ReadyForReview || len(summary.DeferredFieldIDs) != 0 ||
		len(summary.ConfirmedFieldIDs) != 3 {
		t.Fatal("a later accepted fact remained omitted under draft scope")
	}
}

func TestFormOutputScopeAuditorCanBlockADeferredRequiredField(t *testing.T) {
	store, _, job, schema, policy := outputScopeFixture(t, FormOutputEditableAcroForm)
	job, err := store.SetFormOutputScope(t.Context(), FormOutputScopeRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(), Schema: schema,
		Scope: FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := scopeReviewRequest(job, schema, policy)
	request.Auditor = &testFormAuditor{proposals: map[string]FormAuditProposal{"scope-audit": {
		Decision: FormAuditBlock,
		Findings: []FormAuditFinding{{FieldID: schema.Fields[1].ID, Code: "field_confirmation_required"}},
	}}}
	review, err := store.ReviewFormJob(t.Context(), request)
	if err != nil || review.Review.Ready || len(review.Review.Blockers) != 1 {
		t.Fatal("draft scope overrode the independent audit")
	}
}

func TestFormOutputScopeCannotCrossAStagedQuestionClaim(t *testing.T) {
	store, _, job, schema, _ := outputScopeFixture(t, FormOutputEditableAcroForm)
	owner := testFormJobOwner()
	start, err := store.StartFormIntake(t.Context(), FormIntakeStartRequest{
		JobID:            job.JobID,
		ExpectedRevision: job.Revision,
		Owner:            owner,
		Schema:           schema,
		Context: FormIntakeContext{
			CandidateFieldIDs: []string{schema.Fields[1].ID},
			Question:          "Provide known details",
		},
		IdempotencyKey: "scope-staged-question",
	})
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewFormProtectedAnswerSink(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: start.Binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
		InteractionID: "scope-question", IdempotencyKey: "scope-answer", MessageIdentity: "scope-platform-message",
		Intent: interactions.ProtectedAnswerValue, Text: "Synthetic staged detail",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetFormOutputScope(t.Context(), FormOutputScopeRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: owner, Schema: schema,
		Scope: FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[1].ID},
	}); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("output scope advanced a staged protected question")
	}
}

func TestFormOutputScopeCannotCrossPendingValuePublicationAndRecovery(t *testing.T) {
	for _, recovery := range []string{"commit", "discard"} {
		t.Run(recovery, func(t *testing.T) {
			store, options, job, schema, _ := outputScopeFixture(t, FormOutputEditableAcroForm)
			owner := testFormJobOwner()
			binding, err := store.NewProtectedAnswerBinding(t.Context(), FormProtectedAnswerBindingRequest{
				JobID: job.JobID, ExpectedRevision: job.Revision, Owner: owner, FieldID: schema.Fields[1].ID,
			})
			if err != nil {
				t.Fatal(err)
			}
			sink, err := NewFormProtectedAnswerSink(store)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
				Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
				InteractionID: "scope-pending-value", IdempotencyKey: "scope-pending-answer",
				Intent: interactions.ProtectedAnswerValue, Text: "Synthetic pending source detail",
			})
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(formJobStatePath(options))
			if err != nil {
				t.Fatal(err)
			}
			request := FormOutputScopeRequest{
				JobID: job.JobID, ExpectedRevision: job.Revision, Owner: owner, Schema: schema,
				Scope: FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[1].ID},
			}
			if result, scopeErr := store.SetFormOutputScope(
				t.Context(),
				request,
			); !errors.Is(
				scopeErr,
				ErrFormJobConflict,
			) ||
				result.JobID != "" {
				t.Fatal("scope mutation advanced a pending protected value binding")
			}
			// Exact no-op scope reads are harmless and must not publish a snapshot.
			request.Scope, request.DeferredFieldIDs = "", nil
			if replayed, scopeErr := store.SetFormOutputScope(t.Context(), request); scopeErr != nil ||
				replayed.Revision != job.Revision {
				t.Fatal("non-mutating scope replay changed pending publication")
			}
			after, err := os.ReadFile(formJobStatePath(options))
			if err != nil || string(before) != string(after) {
				t.Fatal("refused scope mutation changed the durable pending envelope")
			}
			store.Close()
			store, err = OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
			sink, err = NewFormProtectedAnswerSink(store)
			if err != nil {
				t.Fatal(err)
			}
			if recovery == "commit" {
				err = sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
					Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
					InteractionID: "scope-pending-value", Receipt: receipt,
				})
			} else {
				err = sink.Discard(t.Context(), interactions.ProtectedAnswerDiscardRequest{
					Binding: binding, Workspace: owner.WorkspaceID, Route: testProtectedAnswerRoute(owner),
					InteractionID: "scope-pending-value", Receipt: &receipt, Force: true,
				})
			}
			if err != nil {
				t.Fatalf("pending value could not %s after restart: %v", recovery, err)
			}
			current, err := store.Get(t.Context(), job.JobID, owner)
			if err != nil || current.OutputScope != "" {
				t.Fatal("pending value recovery left a corrupt job or changed output authority")
			}
			if recovery == "commit" && (current.Revision != job.Revision+1 || len(current.Fields) != 2) ||
				recovery == "discard" && (current.Revision != job.Revision || len(current.Fields) != 1) {
				t.Fatal("recovery changed an unrelated assignment or revision")
			}
			assertFormStoreContainsNoPlaintext(t, options, "Synthetic pending source detail")
		})
	}
}

func TestFormOutputScopeFailedPublicationIsAtomic(t *testing.T) {
	store, options, job, schema, _ := outputScopeFixture(t, FormOutputEditableAcroForm)
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	write := store.writeFile
	store.writeFile = func(string, []byte, os.FileMode) error { return errors.New("injected scope write failure") }
	result, err := store.SetFormOutputScope(t.Context(), FormOutputScopeRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: testFormJobOwner(), Schema: schema,
		Scope: FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[1].ID},
	})
	store.writeFile = write
	if err == nil || result.JobID != "" {
		t.Fatal("failed scope write returned accepted authority")
	}
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || string(before) != string(after) {
		t.Fatal("failed scope publication mutated durable authority")
	}
}

func TestFormOutputScopeCreateReplayCannotUpgradeAnUnprovedLegacyCapability(t *testing.T) {
	for _, mode := range []FormOutputMode{"", FormOutputEditableAcroForm} {
		t.Run(string(mode), func(t *testing.T) {
			store, _ := newTestFormJobStore(t)
			request := testFormJobCreateRequest(testFormJobOwner())
			request.OutputMode = mode
			job, err := store.Create(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			request.OutputMode = FormOutputFlattenedPrint
			replayed, err := store.Create(t.Context(), request)
			if mode == "" {
				if err != nil || replayed.JobID != job.JobID || replayed.OutputMode != "" ||
					replayed.Revision != job.Revision {
					t.Fatal("start replay upgraded the legacy job's immutable output capability")
				}
			} else if !errors.Is(err, ErrFormJobConflict) {
				t.Fatal("start replay changed the proved output capability")
			}
		})
	}
}
