package document

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func dispositionIntakeFixture(
	t *testing.T, required bool, schemaChanges ...func(*FormFieldsFacts),
) (*FormJobStore, FormJobStoreOptions, FormIntakeStartRequest, FormIntakeStartResult) {
	t.Helper()
	store, options := newTestFormJobStore(t)
	schema := *successfulTestFormFields()
	template := schema.Fields[0]
	schema.Fields = nil
	for index := 1; index <= 13; index++ {
		field := cloneFormField(template)
		field.ID, field.Name = fillFieldID(index), fmt.Sprintf("Representative detail %d", index)
		field.Widgets[0].ID = fmt.Sprintf("widget_%064x", index)
		field.Required = required && index == 1
		schema.Fields = append(schema.Fields, field)
	}
	for _, change := range schemaChanges {
		change(&schema)
	}
	create := testFormJobCreateRequest(testFormJobOwner())
	var err error
	create.SourceDigest = schema.SourceSHA256
	create.FieldSchemaDigest, err = FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	create.BackendRevision, err = FormFieldsBackendRevision(schema)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Create(t.Context(), create)
	if err != nil {
		t.Fatal(err)
	}
	start := FormIntakeStartRequest{
		JobID:            job.JobID,
		ExpectedRevision: job.Revision,
		Owner:            testFormJobOwner(),
		Schema:           schema,
		Context: FormIntakeContext{
			CandidateFieldIDs: []string{schema.Fields[0].ID},
			Question:          "Explain applicable sections",
		},
		IdempotencyKey: "disposition-start",
	}
	mode, err := store.StartFormIntake(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	return store, options, start, mode
}

func dispositionWindowRequest(
	t *testing.T, store *FormJobStore, start FormIntakeStartRequest, reference, text string,
	positions []int, identity string, disposition FormIntakeDisposition,
) FormIntakeApplyRequest {
	t.Helper()
	job, err := store.Get(t.Context(), start.JobID, start.Owner)
	if err != nil {
		t.Fatal(err)
	}
	request := reserveIntakeApply(t, store, start, reference, job.Revision, positions, identity)
	for _, position := range positions {
		request.Proposals = append(request.Proposals, FormIntakeProposal{
			FieldID: start.Schema.Fields[position].ID, Disposition: disposition,
			Literals: []FormIntakeLiteral{intakeLiteral(text)},
		})
	}
	return request
}

func TestFormIntakeDispositionSectionSurvivesFirstEightFieldsAndRestart(t *testing.T) {
	store, options, start, mode := dispositionIntakeFixture(t, false)
	text := "The representative section does not apply DISPOSITION_PRIVATE_7ad1"
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "disposition-section", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	first := dispositionWindowRequest(t, store, start, admission.Receipt, text,
		[]int{0, 1, 2, 3, 4, 5, 6, 7}, "disposition-first", FormIntakeNotApplicable)
	applied, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil || len(applied.AcceptedFieldIDs) != 8 || len(applied.Findings) != 0 || applied.Consumed {
		t.Fatalf("section instruction stopped at the first window: %v", err)
	}
	input, err := store.ReadFormIntakeInput(t.Context(), admission.Receipt, start.Owner)
	if err != nil || input.Consumed || len(input.Accounted) != 1 || !input.Accounted[0].ScopedInstruction ||
		input.Accounted[0].FieldID != "" || input.Text != text {
		t.Fatal("scoped instruction became a field value or was prematurely discarded")
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	second := dispositionWindowRequest(t, store, start, admission.Receipt, text,
		[]int{8, 9, 10, 11, 12}, "disposition-second", FormIntakeNotApplicable)
	finished, err := store.ApplyFormIntakePass(t.Context(), second)
	if err != nil || len(finished.AcceptedFieldIDs) != 5 || !finished.Consumed || len(finished.Job.Fields) != 13 {
		t.Fatalf("section did not reach fields outside the first window: %v", err)
	}
	for _, field := range finished.Job.Fields {
		if field.State != FormValueNotApplicable || field.ValueKind != ProtectedValueBlank ||
			field.BlankReason != FormBlankNotApplicable {
			t.Fatal("explicit not-applicable instruction was recorded as a supplied string")
		}
	}
	replay, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil || !replay.Reused || replay.AppliedRevision != applied.AppliedRevision ||
		len(replay.Job.Fields) != 13 {
		t.Fatal("scoped disposition replay changed the ledger")
	}
	first.Proposals[0].Disposition = FormIntakeIntentionallyBlank
	if _, err = store.ApplyFormIntakePass(t.Context(), first); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("changed disposition replay matched the old private proposal digest")
	}
	input, err = store.ReadFormIntakeInput(t.Context(), admission.Receipt, start.Owner)
	if err != nil || !input.Consumed || input.Text != "" {
		t.Fatal("fully handled instruction retained a replayable raw utterance")
	}
	durable, err := os.ReadFile(formJobStatePath(options))
	if err != nil || strings.Contains(string(durable), text) {
		t.Fatal("scoped instruction escaped encrypted storage")
	}
	public, err := json.Marshal(finished)
	if err != nil || strings.Contains(string(public), text) {
		t.Fatal("scoped instruction escaped the value-free result")
	}
}

func TestFormIntakeDispositionCannotReuseInstructionAsPositiveData(t *testing.T) {
	store, _, start, mode := dispositionIntakeFixture(t, false)
	text := "This section is not applicable"
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "disposition-mixed", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := dispositionWindowRequest(t, store, start, admission.Receipt, text, []int{0, 1},
		"disposition-mixed-pass", FormIntakeNotApplicable)
	request.Proposals[1].Disposition = ""
	result, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || len(result.AcceptedFieldIDs) != 0 || len(result.Findings) != 2 || result.Consumed ||
		result.Job.Revision != mode.Job.Revision {
		t.Fatal("shared instruction evidence became another field's positive value")
	}
}

func TestFormIntakeDispositionRetainsSourceRequiredBlocker(t *testing.T) {
	store, _, start, mode := dispositionIntakeFixture(t, true)
	text := "This section does not apply"
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "disposition-required", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := dispositionWindowRequest(t, store, start, admission.Receipt, text, []int{0, 1},
		"disposition-required-pass", FormIntakeNotApplicable)
	result, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || len(result.AcceptedFieldIDs) != 1 || len(result.Findings) != 1 ||
		result.Findings[0].FieldID != start.Schema.Fields[0].ID || result.Consumed || !start.Schema.Fields[0].Required {
		t.Fatal("applicability instruction bypassed a source-required field")
	}
}

func TestFormIntakeDispositionRequiresExplicitCorrectionAndExactSupersession(t *testing.T) {
	store, _, start, mode := dispositionIntakeFixture(t, false)
	job, event, err := store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: start.JobID, ExpectedRevision: mode.Job.Revision, Owner: start.Owner,
		FieldID: start.Schema.Fields[0].ID, IdempotencyKey: "disposition-old-value",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Synthetic accepted value"},
		State: FormValueConfirmed, Source: FormValueSourceUser,
		Confidence: FormValueConfidenceExact, Validation: FormValueValidationValid,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := "Clear the earlier value and leave this field blank"
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, job.Revision, "disposition-correction", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := dispositionWindowRequest(t, store, start, admission.Receipt, text, []int{0},
		"disposition-no-correction", FormIntakeIntentionallyBlank)
	request.Proposals[0].SupersedesEventID = event.EventID
	refused, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || len(refused.AcceptedFieldIDs) != 0 || len(refused.Findings) != 1 ||
		refused.Job.Revision != job.Revision {
		t.Fatal("a disposition without explicit correction replaced accepted data")
	}
	request = dispositionWindowRequest(t, store, start, admission.Receipt, text, []int{0},
		"disposition-explicit-correction", FormIntakeIntentionallyBlank)
	request.Proposals[0].SupersedesEventID = event.EventID
	request.Proposals[0].CorrectionEvidence = []FormIntakeLiteral{intakeLiteral(text)}
	corrected, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || len(corrected.AcceptedFieldIDs) != 1 || len(corrected.Findings) != 0 ||
		currentField(t, corrected.Job, start.Schema.Fields[0].ID).State != FormValueBlanked ||
		currentField(t, corrected.Job, start.Schema.Fields[0].ID).SupersedesEventID != event.EventID {
		t.Fatalf("explicit scoped correction failed: %v", err)
	}
}

func TestFormIntakeDispositionCannotHideInstructionAsNonDataAndPublishesAtomically(t *testing.T) {
	store, options, start, mode := dispositionIntakeFixture(t, false)
	text := "This section does not apply"
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "disposition-atomic", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := dispositionWindowRequest(t, store, start, admission.Receipt, text, []int{0, 1},
		"disposition-atomic-pass", FormIntakeNotApplicable)
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	request.NonData = []FormIntakeLiteral{intakeLiteral(text)}
	if _, err = store.ApplyFormIntakePass(t.Context(), request); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatal("explicit scoped evidence was hidden as non-data")
	}
	request.NonData = nil
	write := store.writeFile
	store.writeFile = func(string, []byte, os.FileMode) error { return errors.New("injected disposition write failure") }
	result, err := store.ApplyFormIntakePass(t.Context(), request)
	store.writeFile = write
	if err == nil || result.Job.JobID != "" {
		t.Fatal("failed disposition publication returned accepted fields")
	}
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || string(before) != string(after) {
		t.Fatal("failed disposition publication mutated the ledger or instruction accounting")
	}
	accepted, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || len(accepted.AcceptedFieldIDs) != 2 || accepted.Consumed {
		t.Fatal("unchanged disposition retry could not resume the same input")
	}
}

func TestFormIntakeDispositionPreservesSourceValueUntilExplicitReplacement(t *testing.T) {
	store, _, start, mode := dispositionIntakeFixture(t, false, func(schema *FormFieldsFacts) {
		schema.Fields[0].HasValue = true
	})
	t.Cleanup(store.Close)
	text := "Replace the existing source value by leaving this field blank"
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "disposition-source", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	for index, change := range []func(*FormIntakeProposal){
		func(proposal *FormIntakeProposal) { proposal.ReplaceExisting = true },
		func(proposal *FormIntakeProposal) {
			proposal.CorrectionEvidence = []FormIntakeLiteral{intakeLiteral(text)}
		},
	} {
		request := dispositionWindowRequest(t, store, start, admission.Receipt, text, []int{0},
			fmt.Sprintf("disposition-source-refusal-%d", index), FormIntakeIntentionallyBlank)
		change(&request.Proposals[0])
		refused, applyErr := store.ApplyFormIntakePass(t.Context(), request)
		if applyErr != nil || len(refused.AcceptedFieldIDs) != 0 || len(refused.Findings) != 1 ||
			refused.Job.Revision != mode.Job.Revision || len(refused.Job.Fields) != 0 {
			t.Fatal("source data was cleared without both explicit correction and replacement authority")
		}
	}
	request := dispositionWindowRequest(t, store, start, admission.Receipt, text, []int{0},
		"disposition-source-replace", FormIntakeIntentionallyBlank)
	request.Proposals[0].ReplaceExisting = true
	request.Proposals[0].CorrectionEvidence = []FormIntakeLiteral{intakeLiteral(text)}
	accepted, err := store.ApplyFormIntakePass(t.Context(), request)
	if err != nil || len(accepted.AcceptedFieldIDs) != 1 || len(accepted.Findings) != 0 ||
		currentField(t, accepted.Job, start.Schema.Fields[0].ID).State != FormValueBlanked {
		t.Fatalf("explicit source-value blanking failed: %v", err)
	}
}

func TestFormIntakeDispositionDefaultProposalDigestMatchesLegacy(t *testing.T) {
	store, _, start, mode := dispositionIntakeFixture(t, false)
	t.Cleanup(store.Close)
	const text = "Legacy positive fact"
	admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		start, mode.Progress.ModeID, mode.Job.Revision, "legacy-digest", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	request := dispositionWindowRequest(t, store, start, admission.Receipt, text, []int{0}, "legacy-digest-pass", "")
	type legacyLiteral struct {
		Text       string
		Occurrence int
	}
	type legacyProposal struct {
		FieldID           string
		Literals          []legacyLiteral
		SupersedesEventID string
		ReplaceExisting   bool
		Correction        []legacyLiteral
	}
	legacy, err := formJobJSONKeyedDigest(store.profileKey, struct {
		Reference string
		PassID    string
		Revision  int64
		Schema    FormFieldsFacts
		Proposals []legacyProposal
		NonData   []legacyLiteral
		Findings  []FormFieldMappingBlocker `json:"Findings,omitempty"`
		Intent    FormIntakeDialogIntent    `json:"Intent,omitempty"`
	}{
		request.Reference, request.PassID, request.ExpectedRevision, request.Schema,
		[]legacyProposal{{start.Schema.Fields[0].ID, []legacyLiteral{{text, 1}}, "", false, []legacyLiteral{}}},
		[]legacyLiteral{},
		nil, "",
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.formIntakeProposalDigest(request)
	if err != nil || current != legacy {
		t.Fatal("optional dispositions broke exact replay for legacy positive-fact proposals")
	}
}

func TestFormIntakeDispositionCannotCrossOwnerSchemaOrRevision(t *testing.T) {
	for _, mutation := range []string{"owner", "schema", "revision", "unknown_disposition", "multiple_literals"} {
		t.Run(mutation, func(t *testing.T) {
			store, _, start, mode := dispositionIntakeFixture(t, false)
			t.Cleanup(store.Close)
			const text = "Leave this field blank"
			admission, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				start, mode.Progress.ModeID, mode.Job.Revision, "disposition-boundary", text,
			))
			if err != nil {
				t.Fatal(err)
			}
			request := dispositionWindowRequest(t, store, start, admission.Receipt, text, []int{0},
				"disposition-boundary-pass", FormIntakeIntentionallyBlank)
			switch mutation {
			case "owner":
				request.Owner.SenderID = "unrelated-sender"
			case "schema":
				request.Schema.Fields = append([]FormField(nil), request.Schema.Fields...)
				request.Schema.Fields[0].Name = "Changed schema"
			case "revision":
				request.ExpectedRevision++
			case "unknown_disposition":
				request.Proposals[0].Disposition = "inferred_blank"
			case "multiple_literals":
				request.Proposals[0].Literals = append(request.Proposals[0].Literals, intakeLiteral(text))
			}
			if result, applyErr := store.ApplyFormIntakePass(
				t.Context(),
				request,
			); applyErr == nil ||
				result.Job.JobID != "" {
				t.Fatal("invalid scoped disposition crossed a native authority boundary")
			}
			job, err := store.Get(t.Context(), start.JobID, start.Owner)
			if err != nil || job.Revision != mode.Job.Revision || len(job.Fields) != 0 {
				t.Fatal("refused scoped disposition changed durable fields")
			}
		})
	}
}
