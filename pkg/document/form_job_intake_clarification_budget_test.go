package document

import (
	"errors"
	"fmt"
	"testing"
)

func TestFormIntakeClarificationDoesNotNeedAnOldExtractionReservation(t *testing.T) {
	store, _, start, old, request := priorResolutionFixture(t)
	for index := 1; index < MaxFormIntakeExtractionPasses; index++ {
		reserveIntakeApply(t, store, start, old, request.ExpectedRevision, []int{0}, fmt.Sprint("old-budget-", index))
	}
	request.Proposals[0].Resolves[0].ExpectedPasses = MaxFormIntakeExtractionPasses
	result, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || result.Progress.PendingInputs != 0 || result.Job.LedgerRevision != 1 {
		t.Fatalf("new independently reserved clarification was blocked by old extraction exhaustion: %v", err)
	}
	prior, err := store.ReadFormIntakeInput(t.Context(), old, start.Owner)
	if err != nil || !prior.Consumed || prior.Passes != MaxFormIntakeExtractionPasses {
		t.Fatal("clarification reset or extended the original extraction budget")
	}
	current, err := store.ReadFormIntakeInput(t.Context(), request.Reference, start.Owner)
	if err != nil || !current.Consumed || current.Passes != 1 {
		t.Fatal("clarification did not retain its own extraction reservation")
	}
}

func TestFormIntakeClarificationPreservesUnrelatedFactsAndTheOldHardStop(t *testing.T) {
	store, _, start, old, request := priorResolutionFixture(t, " Contact is Robin Mint.")
	for index := 1; index < MaxFormIntakeExtractionPasses; index++ {
		reserveIntakeApply(t, store, start, old, request.ExpectedRevision, []int{0}, fmt.Sprint("old-cap-", index))
	}
	request.Proposals[0].Resolves[0].ExpectedPasses = MaxFormIntakeExtractionPasses
	result, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || result.Job.LedgerRevision != 1 || result.Progress.PendingInputs != 1 {
		t.Fatalf("new clarification failed or discarded other pending facts: %v", err)
	}
	prior, err := store.ReadFormIntakeInput(t.Context(), old, start.Owner)
	if err != nil || prior.Consumed || len(prior.Text) == 0 || len(prior.Findings) != 0 ||
		prior.Passes != MaxFormIntakeExtractionPasses {
		t.Fatal("original facts, settled finding, or old extraction count changed")
	}
	_, err = store.ReserveFormIntakePass(t.Context(), FormIntakePassRequest{
		Reference: old, ExpectedRevision: result.Job.Revision, Owner: start.Owner, Schema: start.Schema,
		CandidateIDs: []string{start.Schema.Fields[0].ID}, IdempotencyKey: "cannot-exceed-old-cap",
	})
	if !errors.Is(err, ErrFormJobCapacityExceeded) {
		t.Fatal("new clarification reopened direct extraction on an exhausted old input")
	}
}

func TestFormIntakePriorSettlementFencesAnUnappliedOldReservationWithoutANewFieldEvent(t *testing.T) {
	store, _, start, old, seed := priorResolutionFixture(t, " Contact is Robin Mint.")
	seed.Proposals[0].Resolves = nil
	accepted, err := store.ApplyFormIntakePass(t.Context(), seed)
	if err != nil || accepted.Job.LedgerRevision != 1 {
		t.Fatal("could not establish the existing canonical replacement")
	}
	mode, err := store.FindFormIntake(t.Context(), start.Owner)
	if err != nil || mode == nil {
		t.Fatal(err)
	}
	current, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.ModeID, accepted.Job.Revision, "acknowledge-replacement", "MINT",
	))
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(t, store, start, current.Receipt, current.Job.Revision, []int{0}, "settlement")
	request.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "MINT")}
	stale := reserveIntakeApply(t, store, start, old, current.Job.Revision, []int{0}, "reserved-before-settlement")
	stale.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "OVERLONG")}
	request.Proposals[0].Resolves = []FormIntakePriorLiteral{{
		Reference: old, ExpectedPasses: 2, Literal: intakeLiteral("OVERLONG"),
	}}
	settled, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || len(settled.AcknowledgedIDs) != 1 || settled.Job.Revision != current.Job.Revision {
		t.Fatalf("acknowledgement did not settle without a duplicate event: %v", err)
	}
	if _, err = store.ApplyFormIntakePass(t.Context(), stale); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("a pre-settlement reservation applied against its changed source-accounting snapshot")
	}
	_, err = store.ReserveFormIntakePass(t.Context(), FormIntakePassRequest{
		Reference: old, ExpectedRevision: settled.Job.Revision, Owner: start.Owner, Schema: start.Schema,
		CandidateIDs: []string{start.Schema.Fields[0].ID}, IdempotencyKey: "reserved-before-settlement",
	})
	if !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("reservation reuse exposed stale authority for another private interpretation")
	}
}
