package document

import "testing"

func TestFormIntakeAcceptedFactsLeaveConnectiveProsePendingUntilAccounted(t *testing.T) {
	store, options := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	text := "I am the borrower, Casey Mint. I was born 1989-02-15. My email is casey.mint@example.invalid."
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "natural-prose", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	facts := reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{0, 1, 2}, "facts")
	for i, value := range []string{"Casey Mint", "1989-02-15", "casey.mint@example.invalid"} {
		facts.Proposals = append(facts.Proposals, intakeProposal(start.Schema, i, value))
	}
	accepted, err := store.ApplyFormIntakePass(t.Context(), facts)
	if err != nil || accepted.Consumed || len(accepted.AcceptedFieldIDs) != 3 ||
		accepted.Progress.PendingInputs != 1 || len(accepted.Findings) != 0 {
		t.Fatalf("facts-only acceptance did not preserve unaccounted prose: %v", err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	empty := reserveIntakeApply(t, store, start, admitted.Receipt, accepted.Job.Revision, []int{8}, "empty")
	empty.Intent = FormIntakeIntentFacts
	stillPending, err := store.ApplyFormIntakePass(t.Context(), empty)
	if err != nil || stillPending.Consumed || stillPending.Progress.PendingInputs != 1 {
		t.Fatal("an empty proposal list discarded unaccounted connective text")
	}
	prose := reserveIntakeApply(t, store, start, admitted.Receipt, stillPending.Job.Revision, []int{8}, "prose")
	prose.Intent = FormIntakeIntentFacts
	for _, fragment := range []string{"I am the borrower", "I was born", "My email is"} {
		prose.NonData = append(prose.NonData, intakeLiteral(fragment))
	}
	settled, err := store.ApplyFormIntakePass(t.Context(), prose)
	if err != nil || !settled.Consumed || settled.Progress.PendingInputs != 0 ||
		settled.Job.Revision != accepted.Job.Revision || settled.Job.LedgerRevision != accepted.Job.LedgerRevision ||
		len(settled.AcceptedFieldIDs)+len(settled.AcknowledgedIDs) != 0 || settled.Job.ApprovalDigest != "" {
		t.Fatal("connective accounting repeated facts, changed revisions or acquired authority")
	}
	replay, err := store.ApplyFormIntakePass(t.Context(), prose)
	if err != nil || !replay.Reused || !replay.Consumed || replay.Job.LedgerRevision != 3 {
		t.Fatal("connective accounting failed exact replay")
	}
}
