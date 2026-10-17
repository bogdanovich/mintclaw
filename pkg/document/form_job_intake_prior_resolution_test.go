package document

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func priorResolutionFixture(t *testing.T, extra ...string) (
	*FormJobStore, FormJobStoreOptions, FormIntakeStartRequest, string, FormIntakeApplyRequest,
) {
	t.Helper()
	store, options := newTestFormJobStore(t)
	schema := intakeTextSchema()
	schema.Fields[0].MaxLength = 4
	start := intakeApplyFixture(t, store, schema)
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "invalid-first", "My code is OVERLONG."+strings.Join(extra, ""),
	))
	if err != nil {
		t.Fatal(err)
	}
	first := reserveIntakeApply(t, store, start, old.Receipt, old.Job.Revision, []int{0}, "invalid")
	first.Proposals = []FormIntakeProposal{intakeProposal(schema, 0, "OVERLONG")}
	first.NonData = []FormIntakeLiteral{intakeLiteral("My code is")}
	invalid, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil || invalid.Consumed || len(invalid.Findings) != 1 || invalid.Findings[0].Code != "field_invalid" {
		t.Fatal("fixture did not retain a native invalid literal")
	}
	current, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, invalid.Job.Revision, "valid-correction", "Correction: my code is MINT.",
	))
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(t, store, start, current.Receipt, current.Job.Revision, []int{0, 1}, "correct")
	request.Intent = FormIntakeIntentFacts
	request.Proposals = []FormIntakeProposal{intakeProposal(schema, 0, "MINT")}
	request.NonData = []FormIntakeLiteral{intakeLiteral("Correction: my code is")}
	request.Proposals[0].Resolves = []FormIntakePriorLiteral{{
		Reference: old.Receipt, ExpectedPasses: 1, Literal: intakeLiteral("OVERLONG"),
	}}
	return store, options, start, old.Receipt, request
}

func TestFormIntakePriorResolutionHandlesSeveralEarlierInputsAtomically(t *testing.T) {
	store, _, start, first, second := priorResolutionFixture(t)
	second.Proposals[0].Literals = []FormIntakeLiteral{intakeLiteral("Correction: my code is MINT")}
	second.Proposals[0].Resolves, second.NonData = nil, nil
	invalid, err := store.ApplyFormIntakePass(t.Context(), second)
	if err != nil || len(invalid.Findings) != 1 || invalid.Progress.PendingInputs != 2 {
		t.Fatal("second rejected input was not retained")
	}
	input, err := store.ReadFormIntakeInput(t.Context(), second.Reference, start.Owner)
	if err != nil {
		t.Fatal(err)
	}
	third, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, input.ModeID, invalid.Job.Revision, "final-correction", "MINT",
	))
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(t, store, start, third.Receipt, third.Job.Revision, []int{0}, "final")
	request.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "MINT")}
	request.Proposals[0].Resolves = []FormIntakePriorLiteral{
		{Reference: first, ExpectedPasses: 1, Literal: intakeLiteral("OVERLONG")},
		{Reference: second.Reference, ExpectedPasses: 2, Literal: intakeLiteral("Correction: my code is MINT")},
	}
	if _, err = store.ApplyFormIntakePass(t.Context(), request); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("stale second reference did not fail the complete apply")
	}
	record, err := store.Get(t.Context(), start.JobID, start.Owner)
	input, readErr := store.ReadFormIntakeInput(t.Context(), first, start.Owner)
	if err != nil || readErr != nil || record.LedgerRevision != 0 || input.Consumed || input.Passes != 1 {
		t.Fatal("failure in a later reference published an earlier resolution or value")
	}
	request.Proposals[0].Resolves[1].ExpectedPasses = 1
	result, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || result.Job.LedgerRevision != 1 || result.Progress.PendingInputs != 0 {
		t.Fatalf("several earlier inputs did not settle together: %v", err)
	}
	for _, reference := range []string{first, second.Reference} {
		input, err = store.ReadFormIntakeInput(t.Context(), reference, start.Owner)
		if err != nil || !input.Consumed || input.Passes != 2 || input.Text != "" {
			t.Fatal("settlement did not charge and clean each earlier input exactly once")
		}
	}
}

func TestFormIntakePriorResolutionRequiresAnExplicitValidCorrectionOfAnExistingValue(t *testing.T) {
	store, _, start, mode, review := reviewedIntakeFixture(t)
	old, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.ModeID, review.Job.Revision, "conflicting-value", "My name is Morgan Mint.",
	))
	if err != nil {
		t.Fatal(err)
	}
	conflict := reserveIntakeApply(t, store, start, old.Receipt, old.Job.Revision, []int{0}, "conflict")
	conflict.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "Morgan Mint")}
	conflict.NonData = []FormIntakeLiteral{intakeLiteral("My name is")}
	blocked, err := store.ApplyFormIntakePass(t.Context(), conflict)
	if err != nil || len(blocked.Findings) != 1 || blocked.Findings[0].Code != "field_conflicting" {
		t.Fatal("original conflicting value was not retained")
	}
	current, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.ModeID, blocked.Job.Revision, "explicit-correction", "Correct my name to Morgan Mint.",
	))
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(t, store, start, current.Receipt, current.Job.Revision, []int{0}, "replace")
	proposal := intakeProposal(start.Schema, 0, "Morgan Mint")
	proposal.Resolves = []FormIntakePriorLiteral{
		{Reference: old.Receipt, ExpectedPasses: 1, Literal: intakeLiteral("Morgan Mint")},
	}
	request.Proposals = []FormIntakeProposal{proposal}
	if _, err = store.ApplyFormIntakePass(t.Context(), request); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("a resolution replaced a canonical field without correction evidence")
	}
	request.Proposals[0].CorrectionEvidence = []FormIntakeLiteral{intakeLiteral("Correct my name to")}
	request.Proposals[0].SupersedesEventID = review.Job.Fields[0].EventID
	result, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || result.Progress.PendingInputs != 0 || result.Job.ReviewDigest != "" ||
		result.Job.ApprovalDigest != "" || result.Job.LedgerRevision != review.Job.LedgerRevision+1 {
		t.Fatalf("explicit correction did not settle conflict and invalidate old authority: %v", err)
	}
}

func TestFormIntakePriorResolutionPreservesOtherFactsAndLaterFindings(t *testing.T) {
	store, _, start, old, request := priorResolutionFixture(t, " Contact is Robin Mint.")
	result, err := store.ApplyFormIntakePass(t.Context(), request)
	input, readErr := store.ReadFormIntakeInput(t.Context(), old, start.Owner)
	if err != nil || readErr != nil || result.Progress.PendingInputs != 1 || input.Consumed ||
		!strings.Contains(input.Text, "Robin Mint") || len(input.Findings) != 0 || input.Passes != 2 {
		t.Fatal("resolution dropped other facts or did not settle only its field")
	}
	newFinding := reserveIntakeApply(t, store, start, old, result.Job.Revision, []int{0}, "later-invalid")
	newFinding.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "OVERLONG")}
	later, err := store.ApplyFormIntakePass(t.Context(), newFinding)
	input, readErr = store.ReadFormIntakeInput(t.Context(), old, start.Owner)
	if err != nil || readErr != nil || later.Consumed || len(input.Findings) != 1 ||
		input.Findings[0].Code != "field_invalid" || later.Job.LedgerRevision != 1 {
		t.Fatal("earlier settlement suppressed a later native finding or replaced a valid value")
	}
}

func TestFormIntakePriorResolutionChargesTheExistingReservationBudget(t *testing.T) {
	for _, count := range []int{MaxFormIntakeExtractionPasses - 1, MaxFormIntakeExtractionPasses} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			store, _, start, old, request := priorResolutionFixture(t)
			for index := 1; index < count; index++ {
				reserveIntakeApply(
					t,
					store,
					start,
					old,
					request.ExpectedRevision,
					[]int{0},
					fmt.Sprint("spent-", index),
				)
			}
			request.Proposals[0].Resolves[0].ExpectedPasses = count
			result, err := store.ApplyFormIntakePass(t.Context(), request)
			if count == MaxFormIntakeExtractionPasses {
				if !errors.Is(err, ErrFormJobCapacityExceeded) {
					t.Fatal("exhausted original input received a new budget")
				}
				record, getErr := store.Get(t.Context(), start.JobID, start.Owner)
				if getErr != nil || record.LedgerRevision != 0 {
					t.Fatal("capacity failure committed replacement events")
				}
				return
			}
			input, readErr := store.ReadFormIntakeInput(t.Context(), old, start.Owner)
			if err != nil || readErr != nil || !input.Consumed || input.Passes != MaxFormIntakeExtractionPasses ||
				result.Progress.PendingInputs != 0 {
				t.Fatal("native resolution did not consume exactly the final reservation")
			}
		})
	}
}

func TestFormIntakePriorResolutionSettlesInvalidLiteralAndReplaysAfterRestart(t *testing.T) {
	store, options, start, old, request := priorResolutionFixture(t)
	settled, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || !settled.Consumed || settled.Progress.PendingInputs != 0 ||
		settled.Job.LedgerRevision != 1 || len(settled.AcceptedFieldIDs) != 1 || settled.Job.ApprovalDigest != "" {
		t.Fatalf("correction did not settle the exact original literal: pending=%d err=%v",
			settled.Progress.PendingInputs, err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	input, err := store.ReadFormIntakeInput(t.Context(), old, start.Owner)
	if err != nil || !input.Consumed || input.Text != "" || input.Passes != 2 || len(input.Findings) != 0 {
		t.Fatal("restart lost the charged native settlement or restored raw input")
	}
	replay, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || !replay.Reused || replay.Job.LedgerRevision != 1 || replay.Progress.PendingInputs != 0 {
		t.Fatal("exact replay reapplied a value or lost settlement")
	}
	request.Proposals[0].Resolves[0].Literal = intakeLiteral("My code is")
	if _, err = store.ApplyFormIntakePass(t.Context(), request); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("changed resolution replay was accepted")
	}
}

func TestFormIntakePriorResolutionRejectsInvalidReferencesAtomically(t *testing.T) {
	for _, name := range []string{
		"unknown fragment", "prose overlap", "current input", "stale passes", "duplicate", "wrong field",
		"invalid replacement", "foreign job", "wrong owner", "wrong source", "wrong schema", "too many",
	} {
		t.Run(name, func(t *testing.T) {
			store, _, start, old, request := priorResolutionFixture(t)
			prior := &request.Proposals[0].Resolves[0]
			switch name {
			case "unknown fragment":
				prior.Literal = intakeLiteral("NOT_RECEIVED")
			case "prose overlap":
				prior.Literal = intakeLiteral("My code is")
			case "current input":
				prior.Reference = request.Reference
			case "stale passes":
				prior.ExpectedPasses = 2
			case "duplicate":
				request.Proposals[0].Resolves = append(request.Proposals[0].Resolves, *prior)
			case "wrong field":
				request.Proposals[0].FieldID = start.Schema.Fields[1].ID
			case "invalid replacement":
				request.Proposals[0].Literals = []FormIntakeLiteral{intakeLiteral("Correction: my code is MINT")}
				request.NonData = nil
			case "foreign job":
				prior.Reference = "form_intake.form_job_0123456789abcdef0123456789abcdef.form_input_0123456789abcdef0123456789abcdef"
			case "wrong owner":
				request.Owner.SenderID = "another-user"
			case "wrong source":
				request.Schema.SourceSHA256 = strings.Repeat("f", 64)
			case "wrong schema":
				request.Schema.Fields = slices.Clone(request.Schema.Fields)
				request.Schema.Fields[0].Name = "Different field meaning"
			case "too many":
				request.Proposals[0].Resolves = make([]FormIntakePriorLiteral, MaxFormIntakeCandidates+1)
				for index := range request.Proposals[0].Resolves {
					request.Proposals[0].Resolves[index] = *prior
				}
			}
			if _, err := store.ApplyFormIntakePass(t.Context(), request); err == nil {
				t.Fatal("invalid resolution was accepted")
			}
			record, err := store.Get(t.Context(), start.JobID, start.Owner)
			if err != nil || record.LedgerRevision != 0 || len(record.Fields) != 0 {
				t.Fatal("failed resolution published a prefix of accepted events")
			}
			input, err := store.ReadFormIntakeInput(t.Context(), old, start.Owner)
			if err != nil || input.Consumed || input.Passes != 1 || len(input.Findings) != 1 {
				t.Fatal("failed resolution altered original protected evidence")
			}
		})
	}
}
