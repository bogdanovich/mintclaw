package document

import (
	"errors"
	"strings"
	"testing"
)

func TestFormIntakeWholeGuidanceResolutionSettlesEarlierUncertainty(t *testing.T) {
	store, options, start, mode, review := reviewedIntakeFixture(t)
	text := "Finish the reviewed PDF without changing any values"
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.ModeID, review.Job.Revision, "uncertain-finish", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	first := reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{0}, "uncertain")
	first.Intent = FormIntakeIntentClarify
	first.Findings = []FormFieldMappingBlocker{
		{FieldID: start.Schema.Fields[0].ID, Code: "field_confirmation_required"},
	}
	uncertain, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil || uncertain.Consumed || uncertain.Progress.PendingInputs != 1 {
		t.Fatalf("initial uncertainty was not retained: %v", err)
	}
	resolved := reserveIntakeApply(t, store, start, admitted.Receipt, uncertain.Job.Revision, []int{0}, "resolved")
	resolved.Intent, resolved.NonData = FormIntakeIntentFinish, []FormIntakeLiteral{intakeLiteral(text)}
	settled, err := store.ApplyFormIntakePass(t.Context(), resolved)
	if err != nil || !settled.Consumed || settled.Progress.PendingInputs != 0 ||
		settled.Job.LedgerRevision != review.Job.LedgerRevision || settled.Job.Revision != review.Job.Revision ||
		settled.Job.ReviewDigest != review.Job.ReviewDigest || settled.Job.ApprovalDigest != "" {
		t.Fatalf("resolved guidance still blocks the unchanged review: consumed=%v pending=%d err=%v",
			settled.Consumed, settled.Progress.PendingInputs, err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || !input.Consumed || input.Text != "" || len(input.Findings) != 0 ||
		input.DialogIntent != FormIntakeIntentFinish || input.AdmittedRevision != review.Job.ReviewRevision {
		t.Fatal("restart lost settlement, resurrected a finding, or rebound finish authority")
	}
	replayed, err := store.ApplyFormIntakePass(t.Context(), resolved)
	if err != nil || !replayed.Reused || !replayed.Consumed ||
		replayed.Job.LedgerRevision != review.Job.LedgerRevision {
		t.Fatal("guidance resolution did not replay exactly")
	}
	resolved.Intent = FormIntakeIntentCancel
	if _, err = store.ApplyFormIntakePass(t.Context(), resolved); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("changed control replay acquired authority")
	}
	commit := FormCommitRequest{
		JobID: start.JobID, ExpectedRevision: settled.Job.Revision, Owner: start.Owner, Schema: start.Schema,
		AuditPolicyRevision: review.Job.AuditPolicyRevision, OutputPolicyRevision: FormCommitOutputPolicyRevision,
	}
	if _, err = store.BeginFormCommit(t.Context(), commit, FormCommitBinding{}); !errors.Is(err, ErrFormApprovalStale) {
		t.Fatal("resolved guidance bypassed final approval")
	}
}

func TestFormIntakeGuidanceResolutionCannotHideFactsOrUnrelatedUncertainty(t *testing.T) {
	for _, test := range []struct {
		name       string
		text       string
		intent     FormIntakeDialogIntent
		nonData    string
		accepted   bool
		newFinding bool
	}{
		{
			name: "unaccounted fact", text: "Casey Mint. Finish this PDF", intent: FormIntakeIntentFinish,
			nonData: "Finish this PDF",
		},
		{name: "facts intent", text: "Finish this PDF", intent: FormIntakeIntentFacts, nonData: "Finish this PDF"},
		{name: "unrelated field", text: "Finish this PDF", intent: FormIntakeIntentFinish, nonData: "Finish this PDF"},
		{
			name: "accepted fact", text: "Casey Mint. Finish this PDF", intent: FormIntakeIntentFinish,
			nonData: "Finish this PDF", accepted: true,
		},
		{
			name: "current uncertainty", text: "Finish this PDF", intent: FormIntakeIntentFinish,
			nonData: "Finish this PDF", newFinding: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newTestFormJobStore(t)
			start := intakeApplyFixture(t, store, intakeTextSchema())
			mode, err := store.StartFormIntake(t.Context(), start)
			if err != nil {
				t.Fatal(err)
			}
			admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				start, mode.Progress.ModeID, mode.Job.Revision, test.name, test.text,
			))
			if err != nil {
				t.Fatal(err)
			}
			first := reserveIntakeApply(
				t,
				store,
				start,
				admitted.Receipt,
				admitted.Job.Revision,
				[]int{0, 1},
				"uncertain",
			)
			first.Intent = FormIntakeIntentClarify
			first.Findings = []FormFieldMappingBlocker{{FieldID: start.Schema.Fields[1].ID, Code: "field_ambiguous"}}
			if test.accepted {
				first.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "Casey Mint")}
			}
			uncertain, err := store.ApplyFormIntakePass(t.Context(), first)
			if err != nil {
				t.Fatal(err)
			}
			second := reserveIntakeApply(
				t,
				store,
				start,
				admitted.Receipt,
				uncertain.Job.Revision,
				[]int{0},
				"guidance",
			)
			second.Intent, second.NonData = test.intent, []FormIntakeLiteral{intakeLiteral(test.nonData)}
			if test.newFinding {
				second.Findings = []FormFieldMappingBlocker{
					{FieldID: start.Schema.Fields[0].ID, Code: "field_ambiguous"},
				}
			}
			result, err := store.ApplyFormIntakePass(t.Context(), second)
			if err != nil || result.Consumed || result.Progress.PendingInputs != 1 ||
				result.Job.LedgerRevision != uncertain.Job.LedgerRevision {
				t.Fatalf("guidance hid unresolved facts or another field: consumed=%v err=%v", result.Consumed, err)
			}
			input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
			if err != nil || input.Text != test.text || len(input.Findings) == 0 {
				t.Fatal("unresolved protected text or findings were discarded")
			}
		})
	}
}

func TestFormIntakeGuidanceResolutionPreservesNativeFindingsAndTheLatestIntent(t *testing.T) {
	for _, code := range []string{"field_invalid", "field_conflicting", "field_unsupported", "field_unresolved"} {
		t.Run(code, func(t *testing.T) {
			input := formIntakeStoredInput{
				State: formIntakeInputCommitted, Text: "Finish this PDF",
				Accounted: []formIntakeSpan{{Start: 0, End: len("Finish this PDF")}},
				Reservations: []formIntakeStoredPass{
					{CandidateIDs: []string{"field_one"}, Outcome: &formIntakeStoredOutcome{
						Intent:   FormIntakeIntentFacts,
						Findings: []FormFieldMappingBlocker{{FieldID: "field_one", Code: code}},
					}},
					{
						CandidateIDs: []string{"field_one"},
						Outcome:      &formIntakeStoredOutcome{Intent: FormIntakeIntentFinish},
					},
				},
			}
			findings := formIntakeCurrentFindings(input)
			if len(findings) != 1 || findings[0].Code != code ||
				len(formIntakeResolvedGuidanceIDs(input, input.Reservations[1])) != 0 {
				t.Fatal("guidance classification erased native validation")
			}
		})
	}
	input := formIntakeStoredInput{
		State: formIntakeInputCommitted, Text: "Finish this PDF",
		Accounted: []formIntakeSpan{{Start: 0, End: len("Finish this PDF")}},
		Reservations: []formIntakeStoredPass{
			{CandidateIDs: []string{"field_one"}, Outcome: &formIntakeStoredOutcome{
				Intent: FormIntakeIntentClarify, Findings: []FormFieldMappingBlocker{
					{FieldID: "field_one", Code: "field_confirmation_required"},
				},
			}},
			{CandidateIDs: []string{"field_one"}, Outcome: &formIntakeStoredOutcome{Intent: FormIntakeIntentFinish}},
			{CandidateIDs: []string{"field_one"}, Outcome: &formIntakeStoredOutcome{Intent: FormIntakeIntentFacts}},
		},
	}
	if len(formIntakeCurrentFindings(input)) != 1 ||
		len(formIntakeResolvedGuidanceIDs(input, input.Reservations[2])) != 0 {
		t.Fatal("an earlier control pass retroactively replaced the latest facts intent")
	}
	input.Reservations[2].Outcome.Intent = FormIntakeIntentFinish
	input.Accounted[0].ScopedInstruction = true
	if len(formIntakeCurrentFindings(input)) != 1 ||
		len(formIntakeResolvedGuidanceIDs(input, input.Reservations[2])) != 0 {
		t.Fatal("a scope instruction was treated as wholly non-data guidance")
	}
}

func TestFormIntakeGuidanceResolutionCannotHideConflictBehindLaterAdvice(t *testing.T) {
	store, options, start, mode, review := reviewedIntakeFixture(t)
	text := "Actually change my name to Casey Paper."
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.ModeID, review.Job.Revision, "unresolved-correction", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	conflict := reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{0}, "conflict")
	conflict.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 0, "Casey Paper")}
	blocked, err := store.ApplyFormIntakePass(t.Context(), conflict)
	if err != nil || blocked.Consumed || len(blocked.Findings) != 1 ||
		blocked.Findings[0].Code != "field_conflicting" {
		t.Fatalf("correction without evidence was not blocked: %v", err)
	}
	advice := reserveIntakeApply(t, store, start, admitted.Receipt, blocked.Job.Revision, []int{0}, "advice")
	advice.Intent = FormIntakeIntentClarify
	advice.Findings = []FormFieldMappingBlocker{
		{FieldID: start.Schema.Fields[0].ID, Code: "field_confirmation_required"},
	}
	advised, err := store.ApplyFormIntakePass(t.Context(), advice)
	if err != nil || advised.Consumed {
		t.Fatal("intervening advice consumed the correction")
	}
	finish := reserveIntakeApply(t, store, start, admitted.Receipt, advised.Job.Revision, []int{0}, "finish")
	finish.Intent, finish.NonData = FormIntakeIntentFinish, []FormIntakeLiteral{intakeLiteral(text)}
	result, err := store.ApplyFormIntakePass(t.Context(), finish)
	if err != nil || result.Consumed || result.Progress.PendingInputs != 1 ||
		result.Job.LedgerRevision != review.Job.LedgerRevision || result.Job.Revision != review.Job.Revision ||
		result.Job.Fields[0].EventID != review.Job.Fields[0].EventID {
		t.Fatalf("finish discarded an unresolved correction: consumed=%v pending=%d err=%v",
			result.Consumed, result.Progress.PendingInputs, err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || input.Text != text || input.Consumed || len(input.Findings) != 1 ||
		input.Findings[0].Code != "field_conflicting" {
		t.Fatal("restart lost the original protected correction or native blocker")
	}
	replay, err := store.ApplyFormIntakePass(t.Context(), finish)
	if err != nil || !replay.Reused || replay.Consumed || replay.Progress.PendingInputs != 1 {
		t.Fatal("replay discarded the unresolved correction")
	}
}

func TestFormIntakeGuidanceResolutionPreservesNativeHistoryUntilPositiveResolution(t *testing.T) {
	for _, native := range []string{"field_invalid", "field_conflicting", "field_unsupported", "field_unresolved"} {
		for _, advisory := range []string{"field_ambiguous", "field_confirmation_required"} {
			t.Run(native+"/"+advisory, func(t *testing.T) {
				input := formIntakeStoredInput{
					State: formIntakeInputCommitted, Text: "Finish this PDF",
					Accounted: []formIntakeSpan{{Start: 0, End: len("Finish this PDF")}},
					Reservations: []formIntakeStoredPass{
						{Outcome: &formIntakeStoredOutcome{
							Findings: []FormFieldMappingBlocker{{FieldID: "field_one", Code: native}},
						}},
						{Outcome: &formIntakeStoredOutcome{
							Findings: []FormFieldMappingBlocker{{FieldID: "field_one", Code: advisory}},
						}},
						{CandidateIDs: []string{"field_one"}, Outcome: &formIntakeStoredOutcome{
							Intent: FormIntakeIntentFinish,
						}},
					},
				}
				findings := formIntakeCurrentFindings(input)
				if len(findings) != 1 || findings[0].Code != native ||
					len(formIntakeResolvedGuidanceIDs(input, input.Reservations[2])) != 0 {
					t.Fatal("advisory projection hid unresolved native history")
				}
				for _, acknowledged := range []bool{false, true} {
					outcome := &formIntakeStoredOutcome{AcceptedIDs: []string{"field_one"}}
					if acknowledged {
						outcome.AcceptedIDs, outcome.AcknowledgedIDs = nil, []string{"field_one"}
					}
					resolved := input
					resolved.Reservations = append(append([]formIntakeStoredPass{}, input.Reservations...),
						formIntakeStoredPass{Outcome: outcome})
					if len(formIntakeCurrentFindings(resolved)) != 0 {
						t.Fatal("positive field resolution failed to clear native history")
					}
				}
			})
		}
	}
}

func TestFormIntakeGuidanceResolutionSurvivesAnotherWindowAndRestart(t *testing.T) {
	store, options := newTestFormJobStore(t)
	start := intakeApplyFixture(t, store, intakeTextSchema())
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	text := "Review this PDF without changing its values"
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "two-window-guidance", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	first := reserveIntakeApply(t, store, start, admitted.Receipt, admitted.Job.Revision, []int{0, 1}, "uncertain-both")
	first.Intent = FormIntakeIntentClarify
	for _, index := range []int{0, 1} {
		first.Findings = append(first.Findings, FormFieldMappingBlocker{
			FieldID: start.Schema.Fields[index].ID, Code: "field_confirmation_required",
		})
	}
	uncertain, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	resolve := reserveIntakeApply(t, store, start, admitted.Receipt, uncertain.Job.Revision, []int{0}, "resolve-first")
	resolve.Intent, resolve.NonData = FormIntakeIntentReview, []FormIntakeLiteral{intakeLiteral(text)}
	one, err := store.ApplyFormIntakePass(t.Context(), resolve)
	if err != nil || one.Consumed || one.Progress.PendingInputs != 1 {
		t.Fatal("one window erased another window's uncertainty")
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, start.Owner)
	if err != nil || len(input.Findings) != 1 || input.Findings[0].FieldID != start.Schema.Fields[1].ID {
		t.Fatal("restart resurrected resolved uncertainty or hid the other window")
	}
	resolve = reserveIntakeApply(t, store, start, admitted.Receipt, input.Revision, []int{1}, "resolve-second")
	resolve.Intent, resolve.NonData = FormIntakeIntentReview, []FormIntakeLiteral{intakeLiteral(text)}
	both, err := store.ApplyFormIntakePass(t.Context(), resolve)
	if err != nil || !both.Consumed || both.Progress.PendingInputs != 0 || both.Job.LedgerRevision != 0 {
		t.Fatal("a later window resurrected an already resolved finding")
	}
}

func TestFormIntakeGuidanceResolutionOutcomeRemainsBoundedAndAdvisory(t *testing.T) {
	pass := formIntakeStoredPass{
		Revision: 1, CandidateIDs: []string{"field_a", "field_b"},
		Outcome: &formIntakeStoredOutcome{
			Digest: strings.Repeat("a", 64), AppliedRevision: 1, Intent: FormIntakeIntentFinish,
			ResolvedGuidanceIDs: []string{"field_a"},
		},
	}
	record := FormJobRecord{Revision: 1}
	if err := validateFormIntakeOutcome(record, pass); err != nil {
		t.Fatal("valid native guidance settlement is unreadable")
	}
	for _, mutate := range []func(*formIntakeStoredOutcome){
		func(outcome *formIntakeStoredOutcome) { outcome.ResolvedGuidanceIDs = []string{"unknown"} },
		func(outcome *formIntakeStoredOutcome) { outcome.ResolvedGuidanceIDs = []string{"field_a", "field_a"} },
		func(outcome *formIntakeStoredOutcome) { outcome.ResolvedGuidanceIDs = []string{"field_b", "field_a"} },
		func(outcome *formIntakeStoredOutcome) { outcome.Intent = FormIntakeIntentFacts },
		func(outcome *formIntakeStoredOutcome) { outcome.ScopedInstruction = true },
		func(outcome *formIntakeStoredOutcome) { outcome.ScopedIDs = []string{"field_b"} },
		func(outcome *formIntakeStoredOutcome) { outcome.AcknowledgedIDs = []string{"field_b"} },
		func(outcome *formIntakeStoredOutcome) {
			outcome.Findings = []FormFieldMappingBlocker{{FieldID: "field_b", Code: "field_ambiguous"}}
		},
	} {
		changed := pass
		outcome := *pass.Outcome
		changed.Outcome = &outcome
		mutate(changed.Outcome)
		if err := validateFormIntakeOutcome(record, changed); !errors.Is(err, ErrFormJobRecordCorrupt) {
			t.Fatal("malformed guidance settlement acquired native authority")
		}
	}
}
