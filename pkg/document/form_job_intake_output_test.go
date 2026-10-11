package document

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func outputIntakeRequest(
	t *testing.T, store *FormJobStore, start FormIntakeStartRequest, reference, instruction string,
	indices []int, identity string,
) FormIntakeApplyRequest {
	t.Helper()
	request := dispositionWindowRequest(t, store, start, reference, instruction, indices, identity, FormIntakeDeferred)
	request.Intent = FormIntakeIntentDraft
	request.Output = &FormIntakeOutputInstruction{Mode: FormIntakeOutputDraft, Literal: intakeLiteral(instruction)}
	return request
}

func TestFormIntakeOutputScopeCrossesWindowsAndRestartWithoutBlankEvents(t *testing.T) {
	store, options, start, mode := outputIntakeFixture(t, true, FormOutputEditableAcroForm)
	const instruction = "Leave the remaining fields for an editable draft PRIVATE_DRAFT_973a"
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "output-scope", instruction,
	))
	if err != nil {
		t.Fatal(err)
	}
	first := outputIntakeRequest(t, store, start, admission.Receipt, instruction,
		[]int{0, 1, 2, 3, 4, 5, 6, 7}, "output-first")
	applied, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil || applied.Consumed || len(applied.AcceptedFieldIDs) != 0 || len(applied.ScopedFieldIDs) != 8 ||
		len(applied.Job.Fields) != 0 || applied.Job.Revision != mode.Job.Revision+1 ||
		applied.Job.OutputScope != FormOutputEditableDraft || len(applied.Job.DeferredFieldIDs) != 8 {
		t.Fatalf("draft scope fabricated values or stopped at the first window: %v", err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	second := outputIntakeRequest(t, store, start, admission.Receipt, instruction,
		[]int{8, 9, 10, 11, 12}, "output-second")
	finished, err := store.ApplyFormIntakePass(t.Context(), second)
	if err != nil || !finished.Consumed || len(finished.Job.DeferredFieldIDs) != 13 ||
		len(
			finished.Job.Fields,
		) != 0 || finished.Job.Revision != applied.Job.Revision+1 || !start.Schema.Fields[0].Required {
		t.Fatalf("restarted draft did not preserve exact scope and required flags: %v", err)
	}
	if replay, replayErr := store.ApplyFormIntakePass(t.Context(), first); replayErr != nil || !replay.Reused ||
		replay.AppliedRevision != applied.AppliedRevision || len(replay.Job.DeferredFieldIDs) != 13 {
		t.Fatal("scope replay changed the current job or extraction outcome")
	}
	changed := first
	changed.Output = &FormIntakeOutputInstruction{Mode: FormIntakeOutputComplete, Literal: intakeLiteral(instruction)}
	if _, err = store.ApplyFormIntakePass(t.Context(), changed); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("changed output instruction matched the old exact proposal digest")
	}
	mapping, err := store.FormMappingSummary(t.Context(), start.JobID, start.Owner, start.Schema)
	if err != nil || !mapping.ReadyForReview || len(mapping.DeferredFieldIDs) != 13 {
		t.Fatal("draft scope was not distinct from missing and fabricated blank data")
	}
	const complete = "Return to complete filling"
	admission, err = store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, finished.Job.Revision, "output-complete", complete,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(
		t,
		store,
		start,
		admission.Receipt,
		finished.Job.Revision,
		[]int{0},
		"output-complete-pass",
	)
	request.Output = &FormIntakeOutputInstruction{Mode: FormIntakeOutputComplete, Literal: intakeLiteral(complete)}
	cleared, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || cleared.Job.OutputScope != "" || len(cleared.Job.DeferredFieldIDs) != 0 ||
		cleared.Job.Revision != finished.Job.Revision+1 {
		t.Fatalf("explicit complete instruction failed to restore complete authority: %v", err)
	}
	mapping, err = store.FormMappingSummary(t.Context(), start.JobID, start.Owner, start.Schema)
	if err != nil || mapping.ReadyForReview || len(mapping.Unresolved) != 13 || !start.Schema.Fields[0].Required {
		t.Fatal("draft scope weakened complete validation or source-required flags")
	}
	public, err := json.Marshal(finished)
	if err != nil || strings.Contains(string(public), instruction) {
		t.Fatal("private output instruction escaped public JSON")
	}
	assertFormStoreContainsNoPlaintext(t, options, instruction)
}

func TestFormIntakeOutputScopeAndFactsPublishTogetherOrNotAtAll(t *testing.T) {
	store, options, start, mode := outputIntakeFixture(t, true, FormOutputEditableAcroForm)
	t.Cleanup(store.Close)
	const instruction = "Leave remaining fields for an editable draft"
	text := "Casey Mint. " + instruction
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "mixed-output", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := outputIntakeRequest(t, store, start, admission.Receipt, instruction, []int{0, 1}, "mixed-output-pass")
	request.Proposals[0] = intakeProposal(start.Schema, 0, "Casey Mint")
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	write := store.writeFile
	store.writeFile = func(string, []byte, os.FileMode) error { return errors.New("injected output/fact publication failure") }
	result, err := store.ApplyFormIntakePass(t.Context(), request)
	store.writeFile = write
	if err == nil || result.Job.JobID != "" {
		t.Fatal("failed atomic scope publication returned accepted assignments")
	}
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || string(before) != string(after) {
		t.Fatal("failed publication changed either facts, scope or raw accounting")
	}
	applied, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || len(applied.AcceptedFieldIDs) != 1 || len(applied.ScopedFieldIDs) != 1 ||
		applied.Job.Revision != mode.Job.Revision+2 || applied.Job.OutputScope != FormOutputEditableDraft ||
		len(applied.Job.Fields) != 1 || len(applied.Job.DeferredFieldIDs) != 1 || applied.Consumed {
		t.Fatalf("unchanged retry failed to publish both parts of the same intake: %v", err)
	}
	assertFormStoreContainsNoPlaintext(t, options, "Casey Mint")
}

func TestFormIntakeOutputScopeRefusesUnavailableCapabilityBeforeAcceptingFacts(t *testing.T) {
	for _, capability := range []FormOutputMode{"", FormOutputFlattenedPrint} {
		t.Run(string(capability), func(t *testing.T) {
			store, options, start, mode := outputIntakeFixture(t, true, capability)
			t.Cleanup(store.Close)
			const instruction = "Leave remaining fields for an editable draft"
			text := "Casey Mint. " + instruction
			admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				start, mode.Progress.ModeID, mode.Job.Revision, "unavailable-output", text,
			))
			if err != nil {
				t.Fatal(err)
			}
			request := outputIntakeRequest(
				t,
				store,
				start,
				admission.Receipt,
				instruction,
				[]int{0, 1},
				"unavailable-output-pass",
			)
			request.Proposals[0] = intakeProposal(start.Schema, 0, "Casey Mint")
			before, err := os.ReadFile(formJobStatePath(options))
			if err != nil {
				t.Fatal(err)
			}
			if result, applyErr := store.ApplyFormIntakePass(
				t.Context(),
				request,
			); !errors.Is(
				applyErr,
				ErrFormEditableOutputUnavailable,
			) ||
				result.Job.JobID != "" {
				t.Fatal("unproved draft capability accepted facts or authorized an editable result")
			}
			after, err := os.ReadFile(formJobStatePath(options))
			if err != nil || string(before) != string(after) {
				t.Fatal("unavailable capability changed protected intake state")
			}
			input, err := store.ReadFormIntakeInput(t.Context(), admission.Receipt, start.Owner)
			if err != nil || input.Consumed || input.Text != text {
				t.Fatal("unavailable draft request lost its protected utterance")
			}
		})
	}
}

func TestFormIntakeOutputScopeRejectsContradictoryOrUnprovedInstruction(t *testing.T) {
	for _, mutation := range []string{"complete_defer", "missing_quote", "unknown_mode", "hide_instruction", "scope_correction"} {
		t.Run(mutation, func(t *testing.T) {
			store, _, start, mode := outputIntakeFixture(t, false, FormOutputEditableAcroForm)
			t.Cleanup(store.Close)
			const instruction = "Leave fields for an editable draft"
			admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				start, mode.Progress.ModeID, mode.Job.Revision, "invalid-output", instruction,
			))
			if err != nil {
				t.Fatal(err)
			}
			request := outputIntakeRequest(
				t,
				store,
				start,
				admission.Receipt,
				instruction,
				[]int{0},
				"invalid-output-pass",
			)
			switch mutation {
			case "complete_defer":
				request.Output.Mode = FormIntakeOutputComplete
			case "missing_quote":
				request.Output.Literal.Text = "I did not supply this instruction"
			case "unknown_mode":
				request.Output.Mode = "automatic_finish"
			case "hide_instruction":
				request.NonData = []FormIntakeLiteral{intakeLiteral(instruction)}
			case "scope_correction":
				request.Proposals[0].ReplaceExisting = true
			}
			if result, applyErr := store.ApplyFormIntakePass(
				t.Context(),
				request,
			); applyErr == nil ||
				result.Job.JobID != "" {
				t.Fatal("invalid output instruction acquired scope or publication authority")
			}
			job, err := store.Get(t.Context(), start.JobID, start.Owner)
			if err != nil || job.Revision != mode.Job.Revision || job.OutputScope != "" {
				t.Fatal("refused output instruction mutated the job")
			}
		})
	}
}

func TestFormIntakeOutputScopeIncludesFieldsAndDoesNotHideLaterValues(t *testing.T) {
	store, _, start, mode := outputIntakeFixture(t, true, FormOutputEditableAcroForm)
	t.Cleanup(store.Close)
	var deferred []string
	for _, field := range start.Schema.Fields {
		deferred = append(deferred, field.ID)
	}
	draft, err := store.SetFormOutputScope(t.Context(), FormOutputScopeRequest{
		JobID: start.JobID, ExpectedRevision: mode.Job.Revision, Owner: start.Owner, Schema: start.Schema,
		Scope: FormOutputEditableDraft, DeferredFieldIDs: deferred,
	})
	if err != nil {
		t.Fatal(err)
	}
	const instruction = "Bring this field back into the filling scope"
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, draft.Revision, "include-field", instruction,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := dispositionWindowRequest(t, store, start, admission.Receipt, instruction, []int{0},
		"include-field-pass", FormIntakeIncluded)
	included, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || len(included.ScopedFieldIDs) != 1 || len(included.Job.Fields) != 0 ||
		len(included.Job.DeferredFieldIDs) != 12 || included.Job.Revision != draft.Revision+1 {
		t.Fatalf("including an unanswered field fabricated or cleared data: %v", err)
	}
	mapping, err := store.FormMappingSummary(t.Context(), start.JobID, start.Owner, start.Schema)
	if err != nil || mapping.ReadyForReview || len(mapping.Unresolved) != 1 || !start.Schema.Fields[0].Required {
		t.Fatal("included field did not regain its unchanged missing/required blocker")
	}
	// A later positive fact may fill a still-deferred field. Its real ledger
	// state must participate normally rather than disappearing under the scope.
	const fact = "Casey Mint"
	admission, err = store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, included.Job.Revision, "later-deferred-fact", fact,
	))
	if err != nil {
		t.Fatal(err)
	}
	request = reserveIntakeApply(
		t,
		store,
		start,
		admission.Receipt,
		included.Job.Revision,
		[]int{1},
		"later-deferred-fact-pass",
	)
	request.Proposals = []FormIntakeProposal{intakeProposal(start.Schema, 1, fact)}
	provided, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || len(provided.AcceptedFieldIDs) != 1 || len(provided.Job.Fields) != 1 {
		t.Fatalf("deferred field could not accept its later supplied value: %v", err)
	}
	mapping, err = store.FormMappingSummary(t.Context(), start.JobID, start.Owner, start.Schema)
	if err != nil || len(mapping.ConfirmedFieldIDs) != 1 || len(mapping.DeferredFieldIDs) != 11 ||
		len(mapping.Unresolved) != 1 {
		t.Fatal("later provided data was concealed or the unrelated included field was resolved")
	}
}

func TestFormIntakeOutputScopeInvalidatesExistingReviewAndApproval(t *testing.T) {
	store, _, job, schema, policy := outputScopeFixture(t, FormOutputEditableAcroForm)
	t.Cleanup(store.Close)
	owner := testFormJobOwner()
	start := FormIntakeStartRequest{
		JobID:            job.JobID,
		ExpectedRevision: job.Revision,
		Owner:            owner,
		Schema:           schema,
		Context: FormIntakeContext{
			CandidateFieldIDs: []string{schema.Fields[1].ID},
			Question:          "Provide applicable details",
		},
		IdempotencyKey: "reviewed-output-mode",
	}
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := store.SetFormOutputScope(t.Context(), FormOutputScopeRequest{
		JobID: job.JobID, ExpectedRevision: mode.Job.Revision, Owner: owner, Schema: schema,
		Scope: FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := store.ReviewFormJob(t.Context(), scopeReviewRequest(draft, schema, policy))
	if err != nil {
		t.Fatal(err)
	}
	commit := FormCommitRequest{
		JobID: job.JobID, ExpectedRevision: reviewed.Job.Revision, Owner: owner, Schema: schema,
		AuditPolicyRevision: job.AuditPolicyRevision, OutputPolicyRevision: FormCommitDraftOutputPolicyRevision,
	}
	awaiting, binding, err := store.PrepareFormCommitApproval(t.Context(), commit)
	if err != nil {
		t.Fatal(err)
	}
	const instruction = "Return to complete filling"
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, awaiting.Revision, "clear-reviewed-output", instruction,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(
		t,
		store,
		start,
		admission.Receipt,
		awaiting.Revision,
		[]int{1},
		"clear-reviewed-output-pass",
	)
	request.Output = &FormIntakeOutputInstruction{Mode: FormIntakeOutputComplete, Literal: intakeLiteral(instruction)}
	applied, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || applied.Job.State != FormJobCollecting || applied.Job.OutputScope != "" ||
		applied.Job.Revision != awaiting.Revision+1 || applied.Job.ReviewDigest != "" ||
		applied.Job.ApprovalDigest != "" || applied.Job.OperationID != "" {
		t.Fatalf("natural scope change retained old output authority: %v", err)
	}
	commit.ExpectedRevision = applied.Job.Revision
	if _, err = store.BeginFormCommit(t.Context(), commit, binding); err == nil {
		t.Fatal("natural scope change reused the old draft approval")
	}
}
