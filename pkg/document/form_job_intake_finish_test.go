package document

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func reviewedIntakeFixture(
	t *testing.T,
) (*FormJobStore, FormJobStoreOptions, FormIntakeStartRequest, FormIntakeProgress, FormReviewResult) {
	t.Helper()
	store, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	policy := testFormAuditPolicy("audit-primary")
	job := createReviewMappedJob(t, store, owner, schema, policy, "Synthetic initial value")
	start := FormIntakeStartRequest{
		JobID:            job.JobID,
		ExpectedRevision: job.Revision,
		Owner:            owner,
		Schema:           schema,
		Context: FormIntakeContext{
			CandidateFieldIDs: []string{schema.Fields[0].ID},
			Question:          "Any corrections?",
		},
		IdempotencyKey: "intake-before-review",
	}
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	review, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: owner, Schema: schema, Policy: policy,
		Auditor: &testFormAuditor{proposals: map[string]FormAuditProposal{"audit-primary": {Decision: FormAuditPass}}},
	})
	if err != nil || !review.Review.Ready {
		t.Fatalf("review fixture failed: %v", err)
	}
	return store, options, start, mode.Progress, review
}

func TestFormIntakeFinishAccountingPreservesReviewAndRequiresFinalApproval(t *testing.T) {
	store, options, start, mode, review := reviewedIntakeFixture(t)
	message := intakeMessage(start, mode.ModeID, review.Job.Revision, "separate-finish", "Finish this PDF")
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), message)
	if err != nil || admitted.Job.Revision != review.Job.Revision ||
		admitted.Job.ReviewDigest != review.Job.ReviewDigest {
		t.Fatal("a finish message changed the reviewed assignments")
	}
	commit := FormCommitRequest{
		JobID: start.JobID, ExpectedRevision: review.Job.Revision, Owner: start.Owner, Schema: start.Schema,
		AuditPolicyRevision: review.Job.AuditPolicyRevision, OutputPolicyRevision: FormCommitOutputPolicyRevision,
	}
	if _, _, err = store.PrepareFormCommitApproval(t.Context(), commit); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("uninterpreted finish input acquired approval")
	}
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || input.AdmittedRevision != review.Job.ReviewRevision {
		t.Fatal("finish receipt lost its immutable admission revision")
	}
	apply := reserveIntakeApply(t, store, start, admitted.Receipt, input.Revision, []int{0}, "finish-pass")
	apply.NonData, apply.Intent = []FormIntakeLiteral{intakeLiteral(message.Text)}, FormIntakeIntentFinish
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	write := store.writeFile
	store.writeFile = func(string, []byte, os.FileMode) error { return errors.New("injected finish write failure") }
	if failed, err := store.ApplyFormIntakePass(t.Context(), apply); err == nil || failed.Job.JobID != "" {
		t.Fatal("failed finish accounting returned acceptance")
	}
	store.writeFile = write
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || string(after) != string(before) {
		t.Fatal("failed finish publication mutated the store")
	}
	applied, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || !applied.Consumed || applied.AppliedRevision != review.Job.Revision ||
		applied.Job.ReviewDigest != review.Job.ReviewDigest || applied.Job.ApprovalDigest != "" ||
		applied.Job.State != FormJobReviewReady || applied.Intent != FormIntakeIntentFinish {
		t.Fatalf("finish accounting invalidated review or authorized commit: %v", err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	replay, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || !replay.Reused || replay.AppliedRevision != applied.AppliedRevision ||
		replay.Intent != applied.Intent {
		t.Fatal("restart changed the exact finish outcome")
	}
	input, err = store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || !input.Consumed || input.Text != "" || input.DialogIntent != FormIntakeIntentFinish ||
		input.AdmittedRevision != review.Job.ReviewRevision {
		t.Fatal("restart lost finish provenance or retained consumed text")
	}
	public, err := json.Marshal(input)
	if err != nil || strings.Contains(string(public), "admitted") || strings.Contains(string(public), "dialog") {
		t.Fatal("trusted input metadata escaped private projection")
	}
	current, err := store.CurrentFormReview(t.Context(), start.JobID, start.Owner, start.Schema)
	if err != nil || !current.Ready || current.ReviewDigest != review.Review.ReviewDigest {
		t.Fatal("settled finish requires a needless re-review")
	}
	job, approval, err := store.PrepareFormCommitApproval(t.Context(), commit)
	if err != nil || job.State != FormJobAwaitingApproval || approval.ReviewDigest != current.ReviewDigest {
		t.Fatal("settled finish could not request existing final approval")
	}
	commit.ExpectedRevision = job.Revision
	if _, err = store.BeginFormCommit(t.Context(), commit, FormCommitBinding{}); !errors.Is(err, ErrFormApprovalStale) {
		t.Fatal("advisory finish intent replaced final approval authority")
	}
}

func TestFormIntakeCorrectionStillInvalidatesReviewedAuthority(t *testing.T) {
	store, _, start, mode, review := reviewedIntakeFixture(t)
	commit := FormCommitRequest{
		JobID: start.JobID, ExpectedRevision: review.Job.Revision, Owner: start.Owner, Schema: start.Schema,
		AuditPolicyRevision: review.Job.AuditPolicyRevision, OutputPolicyRevision: FormCommitOutputPolicyRevision,
	}
	approvedJob, oldApproval, err := store.PrepareFormCommitApproval(t.Context(), commit)
	if err != nil {
		t.Fatal(err)
	}
	message := intakeMessage(
		start,
		mode.ModeID,
		approvedJob.Revision,
		"correction",
		"Correct it to Synthetic corrected value",
	)
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), message)
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{0}, "correction-pass")
	proposal := intakeProposal(start.Schema, 0, "Synthetic corrected value")
	proposal.SupersedesEventID = approvedJob.Fields[0].EventID
	proposal.CorrectionEvidence = []FormIntakeLiteral{intakeLiteral("Correct it to")}
	apply.Proposals, apply.Intent = []FormIntakeProposal{proposal}, FormIntakeIntentFacts
	corrected, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || len(corrected.AcceptedFieldIDs) != 1 || corrected.Job.ReviewDigest != "" ||
		corrected.Job.ApprovalDigest != "" || corrected.Job.Revision != approvedJob.Revision+1 {
		t.Fatalf("accepted correction retained stale authority: %v", err)
	}
	commit.ExpectedRevision = corrected.Job.Revision
	if _, err = store.BeginFormCommit(t.Context(), commit, oldApproval); !errors.Is(err, ErrFormApprovalStale) {
		t.Fatal("pre-correction approval remained usable")
	}
}

func TestFormIntakeFinishBeforeNewAuditKeepsItsOldOrigin(t *testing.T) {
	store, _, start, mode, old := reviewedIntakeFixture(t)
	update := start
	update.ExpectedRevision = old.Job.Revision
	update.Context.RoleContext = "The operator confirmed the section scope."
	changed, err := store.UpdateFormIntakeContext(t.Context(), update, mode.ModeID)
	if err != nil {
		t.Fatal(err)
	}
	message := intakeMessage(start, mode.ModeID, changed.Revision, "before-audit", "Finish this PDF")
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), message)
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{0}, "before-audit-pass")
	apply.NonData, apply.Intent = []FormIntakeLiteral{intakeLiteral(message.Text)}, FormIntakeIntentFinish
	if _, err := store.ApplyFormIntakePass(t.Context(), apply); err != nil {
		t.Fatal(err)
	}
	newReview, err := store.ReviewFormJob(t.Context(), FormReviewRequest{
		JobID: start.JobID, ExpectedRevision: changed.Revision, Owner: start.Owner, Schema: start.Schema,
		Policy:  testFormAuditPolicy("audit-primary"),
		Auditor: &testFormAuditor{proposals: map[string]FormAuditProposal{"audit-primary": {Decision: FormAuditPass}}},
	})
	if err != nil || !newReview.Review.Ready {
		t.Fatal(err)
	}
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || input.AdmittedRevision != changed.Revision ||
		input.AdmittedRevision >= newReview.Job.ReviewRevision || input.DialogIntent != FormIntakeIntentFinish {
		t.Fatal("an older finish request acquired a newly published review's origin")
	}
}

func TestFormIntakeOutcomeReadsLegacyAccountingRevisionButRejectsInventedShape(t *testing.T) {
	id := intakeTextSchema().Fields[0].ID
	for _, accepted := range [][]string{nil, {id}} {
		pass := formIntakeStoredPass{
			Revision: 10, CandidateIDs: []string{id},
			Outcome: &formIntakeStoredOutcome{Digest: strings.Repeat("a", 64), AcceptedIDs: accepted},
		}
		for _, extra := range []int64{0, 1, 2} {
			pass.Outcome.AppliedRevision = pass.Revision + int64(len(accepted)) + extra
			err := validateFormIntakeOutcome(FormJobRecord{Revision: 20}, pass)
			if extra <= 1 && err != nil || extra == 2 && !errors.Is(err, ErrFormJobRecordCorrupt) {
				t.Fatalf("accounting revision shape accepted=%d extra=%d: %v", len(accepted), extra, err)
			}
		}
	}
}

func TestFormIntakeAdmissionSnapshotInvalidatesInterpretationWithoutResettingReviewRevision(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(start,
		mode.Progress.ModeID, mode.Job.Revision, "first-before-extraction", "Casey Mint"))
	if err != nil {
		t.Fatal(err)
	}
	reserve := FormIntakePassRequest{
		Reference: first.Receipt, ExpectedRevision: first.Job.Revision, Owner: start.Owner, Schema: start.Schema,
		CandidateIDs: []string{start.Schema.Fields[0].ID}, IdempotencyKey: "one-window", RetryStale: true,
	}
	old, err := store.ReserveFormIntakePass(t.Context(), reserve)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(start,
		mode.Progress.ModeID, first.Job.Revision, "new-related-input", "These facts belong to the recipient."))
	if err != nil || second.Job.Revision != first.Job.Revision {
		t.Fatal("new related input changed the assignment revision")
	}
	apply := FormIntakeApplyRequest{
		Reference:        first.Receipt,
		PassID:           old.ID,
		ExpectedRevision: old.Revision,
		Owner:            start.Owner,
		Schema:           start.Schema,
		Proposals:        []FormIntakeProposal{intakeProposal(start.Schema, 0, "Casey Mint")},
	}
	if _, err := store.ApplyFormIntakePass(t.Context(), apply); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("interpretation ignored newly admitted related input")
	}
	fresh, err := store.ReserveFormIntakePass(t.Context(), reserve)
	if err != nil || fresh.ID == old.ID || fresh.Revision != old.Revision || fresh.Input.Passes != 2 {
		t.Fatal("same-revision admission retry reused stale authority or reset budget")
	}
	apply.PassID = fresh.ID
	applied, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || len(applied.AcceptedFieldIDs) != 1 || applied.Progress.PendingInputs != 1 {
		t.Fatalf("fresh admission snapshot could not publish once: %v", err)
	}
}

func TestFormIntakeReservationUsesFreshQuestionWithoutRebindingOrigin(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(start,
		mode.Progress.ModeID, mode.Job.Revision, "before-question-change", "Casey Mint"))
	if err != nil {
		t.Fatal(err)
	}
	update := start
	update.ExpectedRevision = admitted.Job.Revision
	update.Context.Question = "Which person does this name identify?"
	update.Context.RoleContext = "Distinct sponsor and recipient roles."
	changed, err := store.UpdateFormIntakeContext(t.Context(), update, mode.Progress.ModeID)
	if err != nil {
		t.Fatal(err)
	}
	pass, err := store.ReserveFormIntakePass(t.Context(), FormIntakePassRequest{
		Reference: admitted.Receipt, ExpectedRevision: changed.Revision, Owner: start.Owner, Schema: start.Schema,
		CandidateIDs: []string{start.Schema.Fields[0].ID}, IdempotencyKey: "fresh-question",
	})
	if err != nil || !equalFormIntakeOrigin(formIntakeBindingPayload{Context: pass.Input.Context},
		formIntakeBindingPayload{Context: update.Context}) {
		t.Fatal("private pass used the stale question instead of its current native snapshot")
	}
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || input.Context.Question != start.Context.Question || input.AdmittedRevision != mode.Job.Revision {
		t.Fatal("fresh interpretation rebound the immutable receipt origin")
	}
}

func TestFormIntakeUnfinishedLegacyReservationCannotAcquireCurrentSnapshot(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(start,
		mode.Progress.ModeID, mode.Job.Revision, "legacy-reservation", "Casey Mint"))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{0}, "legacy-window")
	apply.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "Casey Mint")}
	if err := store.update(t.Context(), func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(snapshot, start.JobID, start.Owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		source, err := readFormIntakeSource(record, key)
		if err != nil {
			return false, err
		}
		source.Intake.Inputs[0].Reservations[0].ReceiptCount = 0
		if err := store.writeFormIntakeSource(&record, key, source, now); err != nil {
			return false, err
		}
		snapshot.Records[start.JobID] = record
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyFormIntakePass(t.Context(), apply); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("unbound unfinished legacy reservation authorized a new interpretation")
	}
	if _, err := store.ReserveFormIntakePass(t.Context(), FormIntakePassRequest{
		Reference: admitted.Receipt, ExpectedRevision: admitted.Job.Revision, Owner: start.Owner, Schema: start.Schema,
		CandidateIDs: []string{start.Schema.Fields[0].ID}, IdempotencyKey: "legacy-window",
	}); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("legacy reservation identity rebound itself to the current admission snapshot")
	}
}
