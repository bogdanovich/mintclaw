package document

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestFormIntakeDialogueExtensionsPreserveLegacyEmptyProposalDigest(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := FormIntakeApplyRequest{
		Reference: "form_intake.form_job_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.form_input_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		PassID:    "form_pass_cccccccccccccccccccccccccccccccc", ExpectedRevision: 17,
		Schema: intakeTextSchema(),
	}
	legacy, err := formJobJSONKeyedDigest(store.profileKey, struct {
		Reference string
		PassID    string
		Revision  int64
		Schema    FormFieldsFacts
		Proposals []json.RawMessage
		NonData   []json.RawMessage
	}{
		request.Reference, request.PassID, request.ExpectedRevision, request.Schema,
		[]json.RawMessage{},
		[]json.RawMessage{},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.formIntakeProposalDigest(request)
	if err != nil || current != legacy {
		t.Fatal("optional dialog fields changed an existing replay digest")
	}
	request.Intent = FormIntakeIntentClarify
	changed, err := store.formIntakeProposalDigest(request)
	if err != nil || changed == current {
		t.Fatal("advisory intent was not bound into the new exact replay digest")
	}
}

func TestFormIntakeRevisionRetryUsesOneBoundedReservationGroup(t *testing.T) {
	store, options := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "first", "Casey Mint",
	))
	if err != nil {
		t.Fatal(err)
	}
	request := FormIntakePassRequest{
		Reference: admitted.Receipt, ExpectedRevision: admitted.Job.Revision,
		Owner: start.Owner, Schema: start.Schema, CandidateIDs: []string{start.Schema.Fields[0].ID},
		IdempotencyKey: "same-window", RetryStale: true,
	}
	stale, err := store.ReserveFormIntakePass(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, admitted.Job.Revision, "second", "Additional facts",
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := FormIntakeApplyRequest{
		Reference:        admitted.Receipt,
		PassID:           stale.ID,
		ExpectedRevision: stale.Revision,
		Owner:            start.Owner,
		Schema:           start.Schema,
		Proposals:        []FormIntakeProposal{intakeProposal(start.Schema, 0, "Casey Mint")},
		Intent:           FormIntakeIntentFacts,
	}
	if _, err = store.ApplyFormIntakePass(t.Context(), apply); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("stale interpretation was applied")
	}
	request.ExpectedRevision = second.Job.Revision
	fresh, err := store.ReserveFormIntakePass(t.Context(), request)
	if err != nil || fresh.ID == stale.ID || fresh.Input.Passes != 2 || fresh.Reused {
		t.Fatal("revision retry reset budget or reused stale authority")
	}
	again, err := store.ReserveFormIntakePass(t.Context(), request)
	if err != nil || !again.Reused || again.ID != fresh.ID || again.Input.Passes != 2 {
		t.Fatal("same revision retry allocated another reservation")
	}
	changed := request
	changed.CandidateIDs = []string{start.Schema.Fields[1].ID}
	if _, err = store.ReserveFormIntakePass(t.Context(), changed); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("reservation group changed candidate authority")
	}
	apply.PassID, apply.ExpectedRevision = fresh.ID, fresh.Revision
	applied, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || !applied.Consumed || applied.Intent != FormIntakeIntentFacts {
		t.Fatal(err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	outcome, err := store.FormIntakePassOutcome(t.Context(), admitted.Receipt, request.IdempotencyKey, start.Owner)
	if err != nil || outcome == nil || !outcome.Reused || outcome.Intent != FormIntakeIntentFacts ||
		!slices.Equal(outcome.AcceptedFieldIDs, applied.AcceptedFieldIDs) {
		t.Fatal("restart did not replay the exact safe group outcome")
	}
}

func TestFormIntakeRevisionRetriesNeverResetTheDurablePassBudget(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(start,
		mode.Progress.ModeID, mode.Job.Revision, "long-lived-input", "Casey Mint"))
	if err != nil {
		t.Fatal(err)
	}
	revision := admitted.Job.Revision
	for index := 0; index <= MaxFormIntakeExtractionPasses; index++ {
		pass, reserveErr := store.ReserveFormIntakePass(t.Context(), FormIntakePassRequest{
			Reference: admitted.Receipt, ExpectedRevision: revision, Owner: start.Owner, Schema: start.Schema,
			CandidateIDs: []string{start.Schema.Fields[0].ID}, IdempotencyKey: "same-window", RetryStale: true,
		})
		if index == MaxFormIntakeExtractionPasses {
			if !errors.Is(reserveErr, ErrFormJobCapacityExceeded) {
				t.Fatal("stale retries escaped the extraction budget")
			}
			break
		}
		if reserveErr != nil || pass.Input.Passes != index+1 {
			t.Fatal("retry budget drifted from durable reservations")
		}
		update := start
		update.ExpectedRevision = revision
		update.Context.Question = fmt.Sprintf("Clarify role %d", index)
		progress, updateErr := store.UpdateFormIntakeContext(t.Context(), update, mode.Progress.ModeID)
		if updateErr != nil {
			t.Fatal(updateErr)
		}
		revision = progress.Revision
	}
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || input.Passes != MaxFormIntakeExtractionPasses || input.Text != "Casey Mint" || input.Consumed {
		t.Fatal("exhaustion discarded protected input or reset the count")
	}
}

func TestFormIntakeContextUpdatePreservesInputOriginAndExactReplay(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	message := intakeMessage(start, mode.Progress.ModeID, mode.Job.Revision, "same-message", "Casey Mint")
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), message)
	if err != nil {
		t.Fatal(err)
	}
	update := start
	update.ExpectedRevision = admitted.Job.Revision
	update.Context.Question = "Provide the missing contact details together"
	updated, err := store.UpdateFormIntakeContext(t.Context(), update, mode.Progress.ModeID)
	if err != nil || updated.Revision != update.ExpectedRevision+1 {
		t.Fatal(err)
	}
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || input.Context.Question != start.Context.Question {
		t.Fatal("immutable interpretation origin changed")
	}
	message.ExpectedRevision = updated.Revision
	replay, err := store.AdmitFormIntakeMessage(t.Context(), message)
	if err != nil || !replay.Reused || replay.Receipt != admitted.Receipt || replay.Job.Revision != updated.Revision {
		t.Fatal("new question caused an accepted message to be admitted twice")
	}
	refs, err := store.FormIntakeReferences(t.Context(), start.JobID, mode.Progress.ModeID, start.Owner)
	if err != nil || !slices.Equal(refs, []string{admitted.Receipt}) {
		t.Fatal("pending receipt projection changed")
	}
	wrong := start.Owner
	wrong.SenderID = "different-user"
	if _, err = store.FormIntakeReferences(t.Context(), start.JobID, mode.Progress.ModeID, wrong); err == nil {
		t.Fatal("another user read protected references")
	}
	update.ExpectedRevision = updated.Revision
	update.Owner = wrong
	if _, err = store.UpdateFormIntakeContext(t.Context(), update, mode.Progress.ModeID); err == nil {
		t.Fatal("another user rebound protected context")
	}
}
