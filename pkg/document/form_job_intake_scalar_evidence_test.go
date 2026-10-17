package document

import (
	"testing"
)

func TestFormIntakeScalarEvidenceRequiresUnanimousNativeValues(t *testing.T) {
	for _, test := range []struct {
		name, first, second, finding string
		kind                         FormFieldKind
	}{
		{name: "synonymous booleans", kind: FormFieldCheckbox, first: "yes", second: "true"},
		{name: "false synonyms", kind: FormFieldCheckbox, first: "No", second: "unchecked"},
		{name: "duplicate text", kind: FormFieldText, first: "MINT", second: "MINT"},
		{name: "opposite booleans", kind: FormFieldCheckbox, first: "yes", second: "no", finding: "field_conflicting"},
		{name: "different text", kind: FormFieldText, first: "MINT", second: "CODE", finding: "field_conflicting"},
		{name: "invalid second boolean", kind: FormFieldCheckbox, first: "yes", second: "perhaps", finding: "field_invalid"},
		{name: "invalid first boolean", kind: FormFieldCheckbox, first: "perhaps", second: "yes", finding: "field_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newTestFormJobStore(t)
			schema := intakeTextSchema()
			schema.Fields[0].Kind = test.kind
			start := intakeApplyFixture(t, store, schema)
			mode, err := store.StartFormIntake(t.Context(), start)
			if err != nil {
				t.Fatal(err)
			}
			text := test.first + ", " + test.second
			admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				start, mode.Progress.ModeID, mode.Job.Revision, "scalar-evidence", text,
			))
			if err != nil {
				t.Fatal(err)
			}
			request := reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{0}, "scalar")
			secondOccurrence := 1
			if test.first == test.second {
				secondOccurrence = 2
			}
			request.Proposals = []FormIntakeProposal{{FieldID: schema.Fields[0].ID, Literals: []FormIntakeLiteral{
				intakeLiteral(test.first), {Text: test.second, Occurrence: secondOccurrence},
			}}}
			result, err := store.ApplyFormIntakePass(t.Context(), request)
			if err != nil {
				t.Fatalf("native scalar interpretation failed instead of returning a finding: %v", err)
			}
			if test.finding != "" {
				if result.Consumed || result.Job.LedgerRevision != 0 || len(result.Findings) != 1 ||
					result.Findings[0].Code != test.finding {
					t.Fatal("inconsistent/invalid evidence committed a preferred fragment")
				}
				return
			}
			if !result.Consumed || result.Progress.PendingInputs != 0 || result.Job.LedgerRevision != 1 ||
				len(result.AcceptedFieldIDs) != 1 {
				t.Fatal("unanimous evidence did not become exactly one accepted field")
			}
			replayed, err := store.ApplyFormIntakePass(t.Context(), request)
			if err != nil || !replayed.Reused || replayed.Job.LedgerRevision != 1 {
				t.Fatal("unanimous evidence replay appended another event")
			}
		})
	}
}

func TestFormIntakeScalarEvidenceRejectedSetCanBeCorrectedWithoutStrandingValidFragments(t *testing.T) {
	for _, second := range []string{"perhaps", "no"} {
		t.Run(second, func(t *testing.T) {
			store, _ := newTestFormJobStore(t)
			schema := intakeTextSchema()
			schema.Fields[0].Kind = FormFieldCheckbox
			start := intakeApplyFixture(t, store, schema)
			mode, err := store.StartFormIntake(t.Context(), start)
			if err != nil {
				t.Fatal(err)
			}
			old, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				start, mode.Progress.ModeID, mode.Job.Revision, "rejected-set", "yes, "+second,
			))
			if err != nil {
				t.Fatal(err)
			}
			request := reserveIntakeApply(t, store, start, old.Receipt, old.Job.Revision, []int{0}, "old")
			request.Proposals = []FormIntakeProposal{{
				FieldID:  schema.Fields[0].ID,
				Literals: []FormIntakeLiteral{intakeLiteral("yes"), intakeLiteral(second)},
			}}
			blocked, err := store.ApplyFormIntakePass(t.Context(), request)
			if err != nil || len(blocked.Findings) != 1 || blocked.Job.LedgerRevision != 0 {
				t.Fatal("rejected evidence set was not retained as one unresolved field")
			}
			current, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				start, mode.Progress.ModeID, blocked.Job.Revision, "corrected-set", "No",
			))
			if err != nil {
				t.Fatal(err)
			}
			correction := reserveIntakeApply(t, store, start, current.Receipt, current.Job.Revision, []int{0}, "new")
			correction.Proposals = []FormIntakeProposal{intakeProposal(schema, 0, "No")}
			correction.Proposals[0].Resolves = []FormIntakePriorLiteral{
				{Reference: old.Receipt, ExpectedPasses: 1, Literal: intakeLiteral("yes")},
				{Reference: old.Receipt, ExpectedPasses: 1, Literal: intakeLiteral(second)},
			}
			result, err := store.ApplyFormIntakePass(t.Context(), correction)
			if err != nil || result.Progress.PendingInputs != 0 || result.Job.LedgerRevision != 1 {
				t.Fatalf("valid member of rejected set remained stranded: %v", err)
			}
		})
	}
}
