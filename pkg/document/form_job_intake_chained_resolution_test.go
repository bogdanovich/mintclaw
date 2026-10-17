package document

import (
	"errors"
	"testing"
)

func TestFormIntakeSettledRejectedEvidenceCannotAuthorizeAnotherSettlement(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	admit := func(id, text string, revision int64) FormIntakeAdmission {
		t.Helper()
		input, admitErr := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
			start, mode.Progress.ModeID, revision, id, text,
		))
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		return input
	}
	seed := admit("canonical", "CANO", mode.Job.Revision)
	initial := reserveIntakeApply(t, store, start, seed.Receipt, seed.Job.Revision, []int{0}, "canonical")
	initial.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "CANO")}
	canonical, err := store.ApplyFormIntakePass(t.Context(), initial)
	if err != nil {
		t.Fatal(err)
	}
	a := admit("conflict-a", "AAAA", canonical.Job.Revision)
	aPass := reserveIntakeApply(t, store, start, a.Receipt, a.Job.Revision, []int{0}, "conflict-a")
	aPass.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "AAAA")}
	aConflict, err := store.ApplyFormIntakePass(t.Context(), aPass)
	if err != nil || len(aConflict.Findings) != 1 {
		t.Fatal("first conflict was not retained")
	}
	b := admit("conflict-b", "BBBB Other facts remain.", aConflict.Job.Revision)
	bPass := reserveIntakeApply(t, store, start, b.Receipt, b.Job.Revision, []int{0}, "conflict-b")
	bPass.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "BBBB")}
	bConflict, err := store.ApplyFormIntakePass(t.Context(), bPass)
	if err != nil || len(bConflict.Findings) != 1 {
		t.Fatal("second conflict was not retained")
	}
	answer := admit("confirm-canonical", "CANO", bConflict.Job.Revision)
	confirmation := reserveIntakeApply(t, store, start, answer.Receipt, answer.Job.Revision, []int{0}, "confirm")
	confirmation.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "CANO")}
	confirmation.Proposals[0].Resolves = []FormIntakePriorLiteral{{
		Reference: b.Receipt, ExpectedPasses: 1, Literal: intakeLiteral("BBBB"),
	}}
	confirmed, err := store.ApplyFormIntakePass(t.Context(), confirmation)
	if err != nil || len(confirmed.AcknowledgedIDs) != 1 || confirmed.Job.LedgerRevision != 1 {
		t.Fatalf("canonical-value acknowledgement failed to settle its exact prior input: %v", err)
	}
	chained := reserveIntakeApply(t, store, start, b.Receipt, confirmed.Job.Revision, []int{0}, "chained")
	chained.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "BBBB")}
	chained.Proposals[0].Resolves = []FormIntakePriorLiteral{{
		Reference: a.Receipt, ExpectedPasses: 1, Literal: intakeLiteral("AAAA"),
	}}
	if _, err = store.ApplyFormIntakePass(t.Context(), chained); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("accounted rejected evidence authorized another settlement without matching the canonical value")
	}
	old, err := store.ReadFormIntakeInput(t.Context(), a.Receipt, start.Owner)
	if err != nil || old.Consumed || len(old.Findings) != 1 || old.Findings[0].Code != "field_conflicting" {
		t.Fatal("failed chained settlement erased the earlier protected conflict")
	}
	values, err := store.ReadValues(t.Context(), start.JobID, start.Owner, []string{start.Schema.Fields[0].ID})
	if err != nil || values[start.Schema.Fields[0].ID].Value.Text != "CANO" {
		t.Fatal("rejected chain altered the canonical value")
	}
}
