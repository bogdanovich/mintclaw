package document

import (
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func exclusionFixture(
	t *testing.T,
) (*FormJobStore, FormJobStoreOptions, FormIntakeStartRequest, string, FormIntakeApplyRequest) {
	t.Helper()
	store, options := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "ambiguous-reference", "Shared reference is MINT.",
	))
	if err != nil {
		t.Fatal(err)
	}
	first := reserveIntakeApply(t, store, start, old.Receipt, old.Job.Revision, []int{0, 1}, "uncertain")
	for _, index := range []int{0, 1} {
		first.Findings = append(
			first.Findings,
			FormFieldMappingBlocker{FieldID: start.Schema.Fields[index].ID, Code: "field_ambiguous"},
		)
	}
	blocked, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	text := "That reference belongs only to the first person; the other reference is not supplied."
	clarification, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, blocked.Job.Revision, "explicit-exclusion", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(t, store, start, old.Receipt, clarification.Job.Revision, []int{0, 1}, "clarified")
	request.Intent = FormIntakeIntentFacts
	request.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "MINT")}
	request.NonData = []FormIntakeLiteral{intakeLiteral("Shared reference is")}
	request.Exclusions = []FormIntakeCandidateExclusion{{
		FieldID: start.Schema.Fields[1].ID, EvidenceReference: clarification.Receipt,
		Evidence: intakeLiteral(text), Verified: true,
	}}
	return store, options, start, clarification.Receipt, request
}

func TestFormIntakeExclusionSettlesAssociationWithoutAssigningUnknownField(t *testing.T) {
	store, _, start, _, request := exclusionFixture(t)
	result, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.ReadFormIntakeInput(t.Context(), request.Reference, start.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(input.Findings, func(finding FormFieldMappingBlocker) bool {
		return finding.FieldID == start.Schema.Fields[1].ID
	}) {
		t.Fatal("explicit exclusion retained the already resolved candidate ambiguity")
	}
	if !input.Consumed || input.Passes != 2 || len(result.Job.Fields) != 1 || result.Job.LedgerRevision != 1 ||
		result.Job.Fields[0].FieldID != start.Schema.Fields[0].ID || result.Job.ApprovalDigest != "" {
		t.Fatal("exclusion changed a missing field, lost retained evidence, reset budget or gained approval")
	}
}

func TestFormIntakeExclusionRejectsUnboundProofAtomically(t *testing.T) {
	for _, name := range []string{
		"unverified", "invented quote", "same input", "foreign job", "stale proof",
		"unknown candidate", "duplicate", "assigned field", "current finding", "wrong owner", "wrong schema",
	} {
		t.Run(name, func(t *testing.T) {
			store, _, start, _, request := exclusionFixture(t)
			before, err := store.Get(t.Context(), start.JobID, start.Owner)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "unverified":
				request.Exclusions[0].Verified = false
			case "invented quote":
				request.Exclusions[0].Evidence = intakeLiteral("Not received")
			case "same input":
				request.Exclusions[0].EvidenceReference = request.Reference
			case "foreign job":
				request.Exclusions[0].EvidenceReference = strings.Replace(
					request.Exclusions[0].EvidenceReference,
					start.JobID,
					"form_job_"+strings.Repeat("a", 32),
					1,
				)
			case "stale proof":
				request.Exclusions[0].ExpectedPasses++
			case "unknown candidate":
				request.Exclusions[0].FieldID = start.Schema.Fields[8].ID
			case "duplicate":
				request.Exclusions = append(request.Exclusions, request.Exclusions[0])
			case "assigned field":
				request.Exclusions[0].FieldID = start.Schema.Fields[0].ID
			case "current finding":
				request.Findings = []FormFieldMappingBlocker{
					{FieldID: request.Exclusions[0].FieldID, Code: "field_ambiguous"},
				}
			case "wrong owner":
				request.Owner.ChatID = "another-chat"
			case "wrong schema":
				request.Schema.Fields = slices.Clone(request.Schema.Fields)
				request.Schema.Fields[0].Name = "Changed source schema"
			}
			if _, err = store.ApplyFormIntakePass(t.Context(), request); err == nil {
				t.Fatal("unbound exclusion accepted")
			}
			after, err := store.Get(t.Context(), start.JobID, start.Owner)
			input, readErr := store.ReadFormIntakeInput(t.Context(), request.Reference, start.Owner)
			if err != nil || readErr != nil || after.Revision != before.Revision || after.LedgerRevision != 0 ||
				len(after.Fields) != 0 || input.Consumed || len(input.Findings) != 2 {
				t.Fatal("rejected exclusion published a partial value or removed uncertainty")
			}
		})
	}
}

func TestFormIntakeExclusionCannotSettleNativeBlockers(t *testing.T) {
	for _, code := range []string{"field_invalid", "field_conflicting", "field_unsupported", "field_unresolved"} {
		store, _, start, proof, request := exclusionFixture(t)
		_, oldID, _ := ParseFormIntakeReference(request.Reference)
		_, proofID, _ := ParseFormIntakeReference(proof)
		input, err := store.ReadFormIntakeInput(t.Context(), proof, start.Owner)
		if err != nil {
			t.Fatal(err)
		}
		mode := &formIntakeStoredMode{Inputs: []formIntakeStoredInput{
			{
				ID:             oldID,
				AdmissionOrder: 1,
				State:          formIntakeInputCommitted,
				Intent:         input.Intent,
				Reservations: []formIntakeStoredPass{
					{
						Outcome: &formIntakeStoredOutcome{
							Findings: []FormFieldMappingBlocker{{FieldID: request.Exclusions[0].FieldID, Code: code}},
						},
					},
				},
			},
			{ID: proofID, AdmissionOrder: 2, State: formIntakeInputCommitted, Intent: input.Intent, Text: input.Text},
		}}
		pass := formIntakeStoredPass{ReceiptCount: 2, CandidateIDs: []string{request.Exclusions[0].FieldID}}
		if _, err = validateFormIntakeCandidateExclusions(mode, 0, pass, request); err == nil {
			t.Fatalf("exclusion hid native blocker %s", code)
		}
	}
}

func TestFormIntakeExclusionSurvivesRestartReplayAndAllowsFutureValue(t *testing.T) {
	store, options, start, proof, request := exclusionFixture(t)
	settled, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	// Consume the later clarification normally; no persistent raw quote is required.
	finish := reserveIntakeApply(t, store, start, proof, settled.Job.Revision, []int{0, 1}, "account-clarification")
	finish.Intent = FormIntakeIntentFacts
	finish.NonData = []FormIntakeLiteral{request.Exclusions[0].Evidence}
	result, err := store.ApplyFormIntakePass(t.Context(), finish)
	if err != nil || result.Progress.PendingInputs != 0 {
		t.Fatalf("clarification accounting failed: %v", err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	replay, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || !replay.Reused || replay.Job.LedgerRevision != 1 {
		t.Fatal("restart/replay lost exclusion or reapplied value")
	}
	changed := request
	changed.Exclusions = slices.Clone(request.Exclusions)
	changed.Exclusions[0].Evidence = intakeLiteral("different evidence")
	if _, err = store.ApplyFormIntakePass(t.Context(), changed); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("changed exclusion replay accepted")
	}
	newInput, err := store.AdmitFormIntakeMessage(
		t.Context(),
		intakeMessage(start, settled.Progress.ModeID, result.Job.Revision, "later-independent-value", "PAPER"),
	)
	if err != nil {
		t.Fatal(err)
	}
	later := reserveIntakeApply(t, store, start, newInput.Receipt, newInput.Job.Revision, []int{1}, "new-value")
	later.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 1, "PAPER")}
	accepted, err := store.ApplyFormIntakePass(t.Context(), later)
	if err != nil || accepted.Job.LedgerRevision != 2 || len(accepted.AcceptedFieldIDs) != 1 {
		t.Fatal("old exclusion blocked a future supplied field value")
	}
}

func TestFormIntakeExclusionDoesNotResetOriginalPassBudget(t *testing.T) {
	store, _, start, _, request := exclusionFixture(t)
	for count := 2; count < MaxFormIntakeExtractionPasses; count++ {
		last := reserveIntakeApply(
			t,
			store,
			start,
			request.Reference,
			request.ExpectedRevision,
			[]int{0, 1},
			fmt.Sprint("spent-", count),
		)
		request.PassID = last.PassID
	}
	if _, err := store.ApplyFormIntakePass(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	input, err := store.ReadFormIntakeInput(t.Context(), request.Reference, start.Owner)
	if err != nil || input.Passes != MaxFormIntakeExtractionPasses || !input.Consumed {
		t.Fatal("exclusion reset or extended earlier extraction budget")
	}
}

func TestFormIntakeExclusionMetadataIsFailClosedForOlderReaders(t *testing.T) {
	key := make([]byte, 32)
	for _, excluded := range []bool{false, true} {
		payload := formIntakeStoredOutcome{Digest: strings.Repeat("a", 64), AppliedRevision: 2}
		if excluded {
			payload.ExcludedCandidateIDs = []string{fillFieldID(1)}
			payload.ExclusionThroughPasses = 1
		}
		envelope, err := sealFormJobEnvelope(
			key,
			formJobEnvelope{Kind: "test", JobID: "test", Revision: 2},
			payload,
			rand.Reader,
		)
		if err != nil {
			t.Fatal(err)
		}
		var legacy struct {
			Digest          string `json:"digest"`
			AppliedRevision int64  `json:"applied_revision"`
			Consumed        bool   `json:"consumed"`
		}
		err = openFormJobEnvelope(key, envelope, &legacy)
		if excluded && !errors.Is(err, ErrFormJobRecordCorrupt) || !excluded && err != nil {
			t.Fatal("older reader silently accepted exclusion metadata or lost legacy compatibility")
		}
		var current formIntakeStoredOutcome
		if err = openFormJobEnvelope(
			key,
			envelope,
			&current,
		); err != nil ||
			!slices.Equal(current.ExcludedCandidateIDs, payload.ExcludedCandidateIDs) ||
			current.ExclusionThroughPasses != payload.ExclusionThroughPasses {
			t.Fatal("new reader lost exclusion or legacy outcome")
		}
	}
}

func TestFormIntakeExclusionCutoffMustCoverItsOwnPassAndOnlyExistingReservations(t *testing.T) {
	for _, cutoff := range []int{-1, 0, 1, 2, 3, MaxFormIntakeExtractionPasses + 1} {
		input := formIntakeStoredInput{
			State: formIntakeInputConsumed,
			Reservations: []formIntakeStoredPass{{}, {Outcome: &formIntakeStoredOutcome{
				ExcludedCandidateIDs: []string{fillFieldID(1)}, ExclusionThroughPasses: cutoff,
			}}},
		}
		err := validateFormIntakeAccounting(FormJobRecord{}, input)
		if cutoff == 2 && err != nil || cutoff != 2 && !errors.Is(err, ErrFormJobRecordCorrupt) {
			t.Fatalf("invalid exclusion cutoff validation for %d: %v", cutoff, err)
		}
		input.Reservations[1].Outcome.ExcludedCandidateIDs = nil
		if err = validateFormIntakeAccounting(FormJobRecord{}, input); cutoff != 0 && err == nil {
			t.Fatal("outcome without exclusion acquired a reservation fence")
		}
	}
}

func TestFormIntakeExclusionDoesNotDiscardOtherUnaccountedFacts(t *testing.T) {
	field := fillFieldID(1)
	input := formIntakeStoredInput{
		State: formIntakeInputCommitted, Text: "Reference MINT. Other data UNKNOWN.",
		Accounted: []formIntakeSpan{{Start: 0, End: len("Reference MINT")}},
		Reservations: []formIntakeStoredPass{
			{
				Outcome: &formIntakeStoredOutcome{
					Findings: []FormFieldMappingBlocker{{FieldID: field, Code: "field_ambiguous"}},
				},
			},
			{Outcome: &formIntakeStoredOutcome{ExcludedCandidateIDs: []string{field}}},
		},
	}
	if len(formIntakeCurrentFindings(input)) != 0 || formIntakeTextAccounted(input.Text, input.Accounted) {
		t.Fatal("association settlement discarded unrelated unaccounted text")
	}
	input.Reservations = append(
		input.Reservations,
		formIntakeStoredPass{
			Outcome: &formIntakeStoredOutcome{
				Findings: []FormFieldMappingBlocker{{FieldID: field, Code: "field_conflicting"}},
			},
		},
	)
	if got := formIntakeCurrentFindings(input); len(got) != 1 || got[0].Code != "field_conflicting" {
		t.Fatal("old exclusion hid a later genuine conflict")
	}
}

func TestFormIntakeExclusionRejectsEarlierUnappliedInterpretationWithoutRevisionChange(t *testing.T) {
	store, _, start, _, request := exclusionFixture(t)
	stale := reserveIntakeApply(
		t,
		store,
		start,
		request.Reference,
		request.ExpectedRevision,
		[]int{1},
		"before-exclusion",
	)
	stale.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 1, "MINT")}
	next := reserveIntakeApply(
		t,
		store,
		start,
		request.Reference,
		request.ExpectedRevision,
		[]int{0, 1},
		"exclude-only",
	)
	request.PassID = next.PassID
	request.Proposals = nil
	request.Intent = FormIntakeIntentClarify
	settled, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || settled.Job.Revision != request.ExpectedRevision || settled.Job.LedgerRevision != 0 ||
		settled.Consumed {
		t.Fatalf("exclusion changed values/revision or discarded another unresolved field: %v", err)
	}
	input, err := store.ReadFormIntakeInput(t.Context(), request.Reference, start.Owner)
	if err != nil || len(input.Findings) != 1 || input.Findings[0].FieldID != start.Schema.Fields[0].ID {
		t.Fatal("a targeted exclusion was treated as whole-utterance guidance")
	}
	if _, err = store.ApplyFormIntakePass(t.Context(), stale); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("earlier interpretation assigned the excluded candidate after bookkeeping-only settlement")
	}
	record, err := store.Get(t.Context(), start.JobID, start.Owner)
	if err != nil || len(record.Fields) != 0 || record.LedgerRevision != 0 {
		t.Fatal("stale interpretation mutated values")
	}
}

func TestFormIntakeExclusionFencesReservationsMadeAfterItsReservationBeforeApply(t *testing.T) {
	store, options, start, _, request := exclusionFixture(t)
	stale := reserveIntakeApply(t, store, start, request.Reference, request.ExpectedRevision,
		[]int{1}, "reserved-after-exclusion-pass-before-its-apply")
	stale.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 1, "MINT")}
	request.Proposals, request.Intent = nil, FormIntakeIntentClarify
	settled, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || settled.Job.Revision != request.ExpectedRevision || settled.Consumed {
		t.Fatal("fixture did not leave a bookkeeping-only exclusion and another pending field", err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err = store.ApplyFormIntakePass(t.Context(), stale); !errors.Is(err, ErrFormJobConflict) {
		t.Fatal("reservation pending at exclusion application assigned the excluded candidate")
	}
	replay, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || !replay.Reused || replay.Job.LedgerRevision != 0 {
		t.Fatal("settlement cutoff broke exact replay")
	}
	fresh := reserveIntakeApply(t, store, start, request.Reference, request.ExpectedRevision,
		[]int{0}, "genuinely-after-settlement")
	fresh.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "MINT")}
	accepted, err := store.ApplyFormIntakePass(t.Context(), fresh)
	if err != nil || accepted.Job.LedgerRevision != 1 || len(accepted.AcceptedFieldIDs) != 1 ||
		accepted.AcceptedFieldIDs[0] != start.Schema.Fields[0].ID {
		t.Fatal("settlement blocked a genuinely later interpretation of the still-pending field", err)
	}
}

func TestFormIntakeExclusionSettlesLaterReservedCompletedAdviceThroughCutoff(t *testing.T) {
	for _, code := range []string{"field_ambiguous", "field_confirmation_required"} {
		t.Run(code, func(t *testing.T) {
			store, options, start, _, exclusion := exclusionFixture(t)
			advice := reserveIntakeApply(t, store, start, exclusion.Reference, exclusion.ExpectedRevision,
				[]int{1}, "later-reserved-completed-advice")
			advice.Intent = FormIntakeIntentClarify
			advice.Findings = []FormFieldMappingBlocker{{FieldID: start.Schema.Fields[1].ID, Code: code}}
			if _, err := store.ApplyFormIntakePass(t.Context(), advice); err != nil {
				t.Fatal(err)
			}
			exclusion.Proposals, exclusion.Intent = nil, FormIntakeIntentClarify
			settled, err := store.ApplyFormIntakePass(t.Context(), exclusion)
			if err != nil || settled.Job.Revision != exclusion.ExpectedRevision || settled.Consumed {
				t.Fatal("fixture did not retain only the other pending candidate", err)
			}
			store.Close()
			store, err = OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
			input, err := store.ReadFormIntakeInput(t.Context(), exclusion.Reference, start.Owner)
			if err != nil || len(input.Findings) != 1 || input.Findings[0].FieldID != start.Schema.Fields[0].ID {
				t.Fatal("a completed pre-settlement advisory outcome restored the excluded ambiguity")
			}
			replay, err := store.ApplyFormIntakePass(t.Context(), advice)
			if err != nil || !replay.Reused || !slices.Equal(replay.Findings, advice.Findings) ||
				replay.Job.LedgerRevision != 0 {
				t.Fatal("settlement rewrote a completed outcome or replayed an assignment")
			}
			fresh := reserveIntakeApply(t, store, start, exclusion.Reference, exclusion.ExpectedRevision,
				[]int{1}, "genuinely-later-advice")
			fresh.Intent = FormIntakeIntentClarify
			fresh.Findings = advice.Findings
			if _, err = store.ApplyFormIntakePass(t.Context(), fresh); err != nil {
				t.Fatal(err)
			}
			input, err = store.ReadFormIntakeInput(t.Context(), exclusion.Reference, start.Owner)
			if err != nil || len(input.Findings) != 2 || !slices.Contains(input.Findings, advice.Findings[0]) {
				t.Fatal("old cutoff hid genuinely later advice")
			}
		})
	}
}

func TestFormIntakeExclusionAggregationDoesNotHideNativeBlockersOrMutateCallerCutoffs(t *testing.T) {
	field := fillFieldID(1)
	for _, code := range []string{"field_invalid", "field_conflicting", "field_unsupported", "field_unresolved"} {
		input := formIntakeStoredInput{State: formIntakeInputCommitted, Reservations: []formIntakeStoredPass{
			{Outcome: &formIntakeStoredOutcome{Findings: []FormFieldMappingBlocker{{FieldID: field, Code: code}}}},
			{Outcome: &formIntakeStoredOutcome{ExcludedCandidateIDs: []string{field}, ExclusionThroughPasses: 3}},
			{Outcome: &formIntakeStoredOutcome{
				Findings: []FormFieldMappingBlocker{{FieldID: field, Code: "field_ambiguous"}},
			}},
		}}
		guidance := map[string]int{fillFieldID(0): 1}
		findings := formIntakeFindingsThrough(input, nil, guidance)
		if len(findings) != 1 || findings[0].FieldID != field || findings[0].Code != code ||
			len(guidance) != 1 || guidance[field] != 0 {
			t.Fatal("exclusion aggregation hid a native blocker or mutated caller-owned guidance")
		}
	}
}

func TestFormIntakeExclusionConsumesAccountedInputDespiteCompletedStaleAdvice(t *testing.T) {
	store, _, start, _, exclusion := exclusionFixture(t)
	advice := reserveIntakeApply(t, store, start, exclusion.Reference, exclusion.ExpectedRevision,
		[]int{1}, "completed-before-positive-and-negative-settlement")
	advice.Findings = []FormFieldMappingBlocker{{FieldID: start.Schema.Fields[1].ID, Code: "field_ambiguous"}}
	if _, err := store.ApplyFormIntakePass(t.Context(), advice); err != nil {
		t.Fatal(err)
	}
	settled, err := store.ApplyFormIntakePass(t.Context(), exclusion)
	if err != nil || !settled.Consumed || settled.Job.LedgerRevision != 1 || len(settled.AcceptedFieldIDs) != 1 ||
		settled.AcceptedFieldIDs[0] != start.Schema.Fields[0].ID {
		t.Fatal("completed old advice kept an otherwise accounted input pending", err)
	}
	if replay, replayErr := store.ApplyFormIntakePass(t.Context(), advice); replayErr != nil || !replay.Reused {
		t.Fatal("completed historical advice could not replay without effects", replayErr)
	}
	input, err := store.ReadFormIntakeInput(t.Context(), exclusion.Reference, start.Owner)
	if err != nil || !input.Consumed || input.Text != "" || len(input.Findings) != 0 {
		t.Fatal("exact historical replay resurrected consumed input or obsolete findings")
	}
}
