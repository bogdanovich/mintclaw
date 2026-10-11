package document

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func intakeApplyFixture(t *testing.T, store *FormJobStore, schema FormFieldsFacts) FormIntakeStartRequest {
	t.Helper()
	owner := testFormJobOwner()
	job := createMappedFormJob(t, store, owner, schema)
	ids := make([]string, 0, MaxFormIntakeCandidates)
	for _, field := range schema.Fields {
		if !field.ReadOnly && len(ids) < MaxFormIntakeCandidates {
			ids = append(ids, field.ID)
		}
	}
	return FormIntakeStartRequest{
		JobID:            job.JobID,
		ExpectedRevision: job.Revision,
		Owner:            owner,
		Schema:           schema,
		Context: FormIntakeContext{
			CandidateFieldIDs: ids,
			Question:          "Tell me the known details in your own words",
		},
		IdempotencyKey: "start-grounded",
	}
}

func intakeTextSchema() FormFieldsFacts {
	_, schema := fillNormalizationFixture()
	schema.Fields = nil
	for i := 0; i < 16; i++ {
		schema.Fields = append(schema.Fields, FormField{
			ID: fillFieldID(i + 1), Kind: FormFieldText,
			Widgets: []FormFieldWidget{{ID: fillWidgetID(i + 1), Page: 1, Ordinal: 1}},
		})
	}
	return schema
}

func intakeLiteral(text string) FormIntakeLiteral {
	return FormIntakeLiteral{Text: text, Occurrence: 1}
}

func intakeProposal(schema FormFieldsFacts, index int, text string) FormIntakeProposal {
	return FormIntakeProposal{FieldID: schema.Fields[index].ID, Literals: []FormIntakeLiteral{intakeLiteral(text)}}
}

func reserveIntakeApply(
	t *testing.T, store *FormJobStore, request FormIntakeStartRequest,
	reference string, revision int64, indices []int, id string,
) FormIntakeApplyRequest {
	t.Helper()
	ids := make([]string, len(indices))
	for i, index := range indices {
		ids[i] = request.Schema.Fields[index].ID
	}
	pass, err := store.ReserveFormIntakePass(t.Context(), FormIntakePassRequest{
		Reference: reference, ExpectedRevision: revision, Owner: request.Owner, Schema: request.Schema,
		CandidateIDs: ids, IdempotencyKey: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	return FormIntakeApplyRequest{
		Reference: reference, PassID: pass.ID, ExpectedRevision: pass.Revision,
		Owner: request.Owner, Schema: request.Schema,
	}
}

func TestFormIntakeApplyFiveFactsOutsideFirstWindowRecoveryAndIncrementalMessages(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	text := "I am Casey Mint, my contact is Robin Mint; I live at 14 Mint Lane in Mintford, postcode 20500."
	admitted, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "natural-1", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	firstRequest := reserveIntakeApply(
		t,
		store,
		request,
		admitted.Receipt,
		admitted.Job.Revision,
		[]int{0, 1},
		"pass-a",
	)
	firstRequest.Proposals = []FormIntakeProposal{
		intakeProposal(request.Schema, 0, "Casey Mint"), intakeProposal(request.Schema, 1, "Robin Mint"),
	}
	firstRequest.NonData = []FormIntakeLiteral{intakeLiteral("I am"), intakeLiteral("my contact is")}
	first, err := store.ApplyFormIntakePass(t.Context(), firstRequest)
	if err != nil || first.Consumed || len(first.AcceptedFieldIDs) != 2 || first.Progress.PendingInputs != 1 {
		t.Fatalf("first bounded pass = %#v, %v", first, err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	input, err := store.ReadFormIntakeInput(t.Context(), admitted.Receipt, request.Owner)
	if err != nil || input.Text != text || len(input.Accounted) != 4 || input.Consumed {
		t.Fatalf("restart lost remaining facts/accounting: %v", err)
	}
	secondRequest := reserveIntakeApply(
		t,
		store,
		request,
		admitted.Receipt,
		first.Job.Revision,
		[]int{0, 8, 9, 10},
		"pass-b",
	)
	secondRequest.Proposals = []FormIntakeProposal{
		intakeProposal(request.Schema, 0, "Casey Mint"), intakeProposal(request.Schema, 8, "14 Mint Lane"),
		intakeProposal(request.Schema, 9, "Mintford"), intakeProposal(request.Schema, 10, "20500"),
	}
	secondRequest.NonData = []FormIntakeLiteral{
		intakeLiteral("I live at"),
		intakeLiteral("in"),
		intakeLiteral("postcode"),
	}
	second, err := store.ApplyFormIntakePass(t.Context(), secondRequest)
	if err != nil || !second.Consumed || len(second.AcceptedFieldIDs) != 3 || len(second.AcknowledgedIDs) != 1 ||
		second.Job.LedgerRevision != 5 || second.Progress.PendingInputs != 0 || second.Progress.RetainedBytes != 0 {
		t.Fatalf("later view lost/reapplied facts: %#v, %v", second, err)
	}
	input, err = store.ReadFormIntakeInput(t.Context(), admitted.Receipt, request.Owner)
	if err != nil || !input.Consumed || input.Text != "" || len(input.Accounted) != 0 {
		t.Fatal("consumed input still exposes raw text")
	}
	revision := second.Job.Revision
	for i, value := range []string{"casey.mint@example.invalid", "202-555-0147", "United States"} {
		message, admitErr := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
			request, started.Progress.ModeID, revision, fmt.Sprintf("incremental-%d", i), value,
		))
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		apply := reserveIntakeApply(
			t,
			store,
			request,
			message.Receipt,
			message.Job.Revision,
			[]int{i + 2},
			fmt.Sprintf("next-%d", i),
		)
		apply.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, i+2, value)}
		result, applyErr := store.ApplyFormIntakePass(t.Context(), apply)
		if applyErr != nil || !result.Consumed || result.Progress.InputCount != i+2 ||
			result.Job.JobID != request.JobID {
			t.Fatalf("incremental message = %#v, %v", result, applyErr)
		}
		revision = result.Job.Revision
	}
	for _, original := range []FormIntakeApplyRequest{firstRequest, secondRequest} {
		replay, replayErr := store.ApplyFormIntakePass(t.Context(), original)
		if replayErr != nil || !replay.Reused || replay.Job.Revision != revision ||
			replay.AppliedRevision >= replay.Job.Revision || replay.Job.LedgerRevision != 8 {
			t.Fatalf("old exact outcome replay = %#v, %v", replay, replayErr)
		}
	}
	assertFormStoreContainsNoPlaintext(t, options, text, "Casey Mint", "Robin Mint", "202-555-0147")
	encoded, err := json.Marshal([]any{first, second, firstRequest, input})
	if err != nil || strings.Contains(string(encoded), "Casey Mint") || strings.Contains(string(encoded), "Mintford") {
		t.Fatal("protected interpretation leaked into ordinary JSON")
	}
}

func TestFormIntakeApplyTypedControlsAndRetainInvalidFacts(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	_, schema := fillNormalizationFixture()
	request := intakeApplyFixture(t, store, schema)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	text := "yes, United States, Canada, one and three, 02/15/1990; not-a-date"
	message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "typed", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(
		t,
		store,
		request,
		message.Receipt,
		message.Job.Revision,
		[]int{2, 3, 4, 5, 7},
		"typed-pass",
	)
	apply.Proposals = []FormIntakeProposal{
		intakeProposal(
			schema,
			2,
			"yes",
		),
		intakeProposal(schema, 3, "United States"),
		intakeProposal(schema, 4, "Canada"),
		{FieldID: schema.Fields[5].ID, Literals: []FormIntakeLiteral{intakeLiteral("one"), intakeLiteral("three")}},
		intakeProposal(schema, 7, "not-a-date"),
	}
	apply.NonData = []FormIntakeLiteral{intakeLiteral("and")}
	result, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || len(result.AcceptedFieldIDs) != 4 || len(result.Findings) != 1 || result.Consumed ||
		result.Findings[0].FieldID != schema.Fields[7].ID || result.Findings[0].Code != "field_invalid" {
		t.Fatalf("typed selection = %#v, %v", result, err)
	}
	values, err := store.ReadValues(t.Context(), request.JobID, request.Owner, result.AcceptedFieldIDs)
	if err != nil || values[schema.Fields[2].ID].Value.Boolean == nil || !*values[schema.Fields[2].ID].Value.Boolean ||
		values[schema.Fields[3].ID].Value.Text != "US" || values[schema.Fields[4].ID].Value.Text != "CA" ||
		!slices.Equal(values[schema.Fields[5].ID].Value.Choices, []string{"one", "three"}) {
		t.Fatalf("native normalization failed: %v", err)
	}
	input, err := store.ReadFormIntakeInput(t.Context(), message.Receipt, request.Owner)
	if err != nil || input.Text != text || len(input.Accounted) != 6 {
		t.Fatal("invalid/out-of-view facts were discarded")
	}
}

func TestFormIntakeApplyRejectsInventedOrMisboundEvidenceWithoutPrefix(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*FormIntakeApplyRequest)
	}{
		{"invented", func(r *FormIntakeApplyRequest) { r.Proposals[1].Literals[0].Text = "Invented" }},
		{"inside token", func(r *FormIntakeApplyRequest) { r.Proposals[1].Literals[0].Text = "Mintf" }},
		{"wrong occurrence", func(r *FormIntakeApplyRequest) { r.Proposals[1].Literals[0].Occurrence = 2 }},
		{"outside candidate view", func(r *FormIntakeApplyRequest) { r.Proposals[1].FieldID = r.Schema.Fields[9].ID }},
		{"duplicate field", func(r *FormIntakeApplyRequest) { r.Proposals[1].FieldID = r.Proposals[0].FieldID }},
		{"prose hides fact", func(r *FormIntakeApplyRequest) { r.NonData = []FormIntakeLiteral{intakeLiteral("Casey Mint")} }},
		{"wrong owner", func(r *FormIntakeApplyRequest) { r.Owner.SenderID = "other" }},
		{"wrong route", func(r *FormIntakeApplyRequest) { r.Owner.TopicID = "other" }},
		{"wrong schema", func(r *FormIntakeApplyRequest) { r.Schema.SourceSHA256 = strings.Repeat("b", 64) }},
		{"wrong revision", func(r *FormIntakeApplyRequest) { r.ExpectedRevision++ }},
		{"wrong pass", func(r *FormIntakeApplyRequest) { r.PassID = "form_pass_" + strings.Repeat("f", 32) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, options := newTestFormJobStore(t)
			request := intakeApplyFixture(t, store, intakeTextSchema())
			started, err := store.StartFormIntake(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				request, started.Progress.ModeID, started.Job.Revision, "bound", "Casey Mint; Mintford",
			))
			if err != nil {
				t.Fatal(err)
			}
			apply := reserveIntakeApply(
				t,
				store,
				request,
				message.Receipt,
				message.Job.Revision,
				[]int{0, 1},
				"bound-pass",
			)
			apply.Proposals = []FormIntakeProposal{
				intakeProposal(request.Schema, 0, "Casey Mint"),
				intakeProposal(request.Schema, 1, "Mintford"),
			}
			before, err := os.ReadFile(formJobStatePath(options))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&apply)
			result, applyErr := store.ApplyFormIntakePass(t.Context(), apply)
			if applyErr == nil || result.Job.JobID != "" || len(result.AcceptedFieldIDs) != 0 {
				t.Fatalf("invalid evidence advertised acceptance: %#v, %v", result, applyErr)
			}
			after, err := os.ReadFile(formJobStatePath(options))
			if err != nil || !slices.Equal(before, after) {
				t.Fatal("failed proposal published a prefix")
			}
		})
	}
}

func TestFormIntakeApplyAmbiguousPersonSpanIsNotCopied(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "two-people", "Casey Mint; Robin Mint; 20500",
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(
		t,
		store,
		request,
		message.Receipt,
		message.Job.Revision,
		[]int{0, 1, 2},
		"two-people-pass",
	)
	apply.Proposals = []FormIntakeProposal{
		intakeProposal(request.Schema, 0, "Casey Mint"), intakeProposal(request.Schema, 1, "Casey Mint"),
		intakeProposal(request.Schema, 2, "20500"),
	}
	result, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || len(result.AcceptedFieldIDs) != 1 || len(result.Findings) != 2 || result.Consumed ||
		result.Findings[0].Code != "field_ambiguous" || result.Findings[1].Code != "field_ambiguous" {
		t.Fatalf("person evidence was silently copied: %#v, %v", result, err)
	}
	second := reserveIntakeApply(
		t,
		store,
		request,
		message.Receipt,
		result.Job.Revision,
		[]int{0, 1},
		"resolved-people",
	)
	second.Proposals = []FormIntakeProposal{
		intakeProposal(request.Schema, 0, "Casey Mint"),
		intakeProposal(request.Schema, 1, "Robin Mint"),
	}
	resolved, err := store.ApplyFormIntakePass(t.Context(), second)
	if err != nil || len(resolved.AcceptedFieldIDs) != 2 || !resolved.Consumed {
		t.Fatalf("distinct person evidence did not resolve: %#v, %v", resolved, err)
	}
}

func TestFormIntakeApplyRepeatCorrectionAndExactWholeProposalReplay(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	applyMessage := func(identity, text string, revision int64) FormIntakeApplyRequest {
		message, admitErr := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
			request, started.Progress.ModeID, revision, identity, text,
		))
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		return reserveIntakeApply(t, store, request, message.Receipt, message.Job.Revision, []int{0}, identity+"-pass")
	}
	first := applyMessage("original", "Casey Mint", started.Job.Revision)
	first.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "Casey Mint")}
	accepted, err := store.ApplyFormIntakePass(t.Context(), first)
	if err != nil || !accepted.Consumed {
		t.Fatalf("first = %#v, %v", accepted, err)
	}
	repeat := applyMessage("repeat", "Casey Mint", accepted.Job.Revision)
	repeat.Proposals = first.Proposals
	acknowledged, err := store.ApplyFormIntakePass(t.Context(), repeat)
	if err != nil || !acknowledged.Consumed || len(acknowledged.AcceptedFieldIDs) != 0 ||
		len(acknowledged.AcknowledgedIDs) != 1 || acknowledged.Job.LedgerRevision != 1 {
		t.Fatalf("accepted surname was re-appended: %#v, %v", acknowledged, err)
	}
	correction := applyMessage("correction", "Actually change my name to Casey Paper.", acknowledged.Job.Revision)
	correction.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "Casey Paper")}
	blocked, err := store.ApplyFormIntakePass(t.Context(), correction)
	if err != nil || len(blocked.Findings) != 1 || blocked.Findings[0].Code != "field_conflicting" ||
		blocked.Consumed || blocked.Job.LedgerRevision != 1 {
		t.Fatalf("implicit correction was applied: %#v, %v", blocked, err)
	}
	correctedRequest := reserveIntakeApply(
		t,
		store,
		request,
		correction.Reference,
		blocked.Job.Revision,
		[]int{0},
		"explicit-correction",
	)
	correctedRequest.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "Casey Paper")}
	correctedRequest.Proposals[0].SupersedesEventID = blocked.Job.Fields[0].EventID
	correctedRequest.Proposals[0].CorrectionEvidence = []FormIntakeLiteral{intakeLiteral("Actually change my name to")}
	corrected, err := store.ApplyFormIntakePass(t.Context(), correctedRequest)
	if err != nil || !corrected.Consumed || corrected.Job.LedgerRevision != 2 ||
		corrected.Job.Fields[0].SupersedesEventID != accepted.Job.Fields[0].EventID {
		t.Fatalf("explicit exact-event correction = %#v, %v", corrected, err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	for _, original := range []FormIntakeApplyRequest{first, repeat, correction, correctedRequest} {
		replayed, replayErr := store.ApplyFormIntakePass(t.Context(), original)
		if replayErr != nil || !replayed.Reused || replayed.Job.Revision != corrected.Job.Revision ||
			replayed.Job.LedgerRevision != 2 {
			t.Fatalf("exact outcome replay changed corrected facts: %#v, %v", replayed, replayErr)
		}
	}
	for _, change := range []func(*FormIntakeApplyRequest){
		func(r *FormIntakeApplyRequest) { r.Proposals[0].Literals[0].Text = "Invented" },
		func(r *FormIntakeApplyRequest) { r.Proposals[0].Literals[0].Occurrence = 2 },
		func(r *FormIntakeApplyRequest) { r.Proposals[0].SupersedesEventID = "" },
		func(r *FormIntakeApplyRequest) { r.Proposals[0].CorrectionEvidence = nil },
		func(r *FormIntakeApplyRequest) { r.Proposals[0].ReplaceExisting = true },
		func(r *FormIntakeApplyRequest) { r.ExpectedRevision = corrected.Job.Revision },
	} {
		changed := correctedRequest
		changed.Proposals = slices.Clone(correctedRequest.Proposals)
		changed.Proposals[0].Literals = slices.Clone(correctedRequest.Proposals[0].Literals)
		change(&changed)
		if result, replayErr := store.ApplyFormIntakePass(
			t.Context(),
			changed,
		); !errors.Is(replayErr, ErrFormJobAnswerConflict) ||
			result.Job.JobID != "" {
			t.Fatalf("changed whole proposal was reused: %#v, %v", result, replayErr)
		}
	}
	values, err := store.ReadValues(t.Context(), request.JobID, request.Owner, []string{request.Schema.Fields[0].ID})
	if err != nil || values[request.Schema.Fields[0].ID].Value.Text != "Casey Paper" {
		t.Fatal("replay restored old name")
	}
}

func TestFormIntakeApplySourceExistingRequiresExplicitReplacement(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	schema := intakeTextSchema()
	schema.Fields[0].HasValue = true
	request := intakeApplyFixture(t, store, schema)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "replace", "Replace the existing name with Casey Mint.",
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(
		t,
		store,
		request,
		message.Receipt,
		message.Job.Revision,
		[]int{0},
		"replacement-blocked",
	)
	apply.Proposals = []FormIntakeProposal{intakeProposal(schema, 0, "Casey Mint")}
	apply.Proposals[0].CorrectionEvidence = []FormIntakeLiteral{intakeLiteral("Replace the existing name with")}
	blocked, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || len(blocked.Findings) != 1 || blocked.Findings[0].Code != "field_conflicting" ||
		blocked.Job.LedgerRevision != 0 {
		t.Fatalf("existing source field silently replaced: %#v, %v", blocked, err)
	}
	replace := reserveIntakeApply(
		t,
		store,
		request,
		message.Receipt,
		blocked.Job.Revision,
		[]int{0},
		"replacement-explicit",
	)
	replace.Proposals = slices.Clone(apply.Proposals)
	replace.Proposals[0].ReplaceExisting = true
	result, err := store.ApplyFormIntakePass(t.Context(), replace)
	if err != nil || !result.Consumed || len(result.AcceptedFieldIDs) != 1 {
		t.Fatalf("replacement = %#v, %v", result, err)
	}
}

func TestFormIntakeApplyWriteOrSealFailureDoesNotPublishEventsOrAccounting(t *testing.T) {
	for _, failure := range []string{"write", "second event seal", "accounting seal"} {
		t.Run(failure, func(t *testing.T) {
			store, options := newTestFormJobStore(t)
			request := intakeApplyFixture(t, store, intakeTextSchema())
			started, err := store.StartFormIntake(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
				request, started.Progress.ModeID, started.Job.Revision, "atomic", "Casey Mint; Robin Paper",
			))
			if err != nil {
				t.Fatal(err)
			}
			apply := reserveIntakeApply(
				t,
				store,
				request,
				message.Receipt,
				message.Job.Revision,
				[]int{0, 1},
				"atomic-pass",
			)
			apply.Proposals = []FormIntakeProposal{
				intakeProposal(request.Schema, 0, "Casey Mint"),
				intakeProposal(request.Schema, 1, "Robin Paper"),
			}
			before, err := os.ReadFile(formJobStatePath(options))
			if err != nil {
				t.Fatal(err)
			}
			originalWrite, originalRandom := store.writeFile, store.random
			switch failure {
			case "write":
				store.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic write failure") }
			case "second event seal":
				store.random = io.LimitReader(rand.Reader, 24)
			case "accounting seal":
				store.random = io.LimitReader(rand.Reader, 48)
			}
			result, applyErr := store.ApplyFormIntakePass(t.Context(), apply)
			store.writeFile, store.random = originalWrite, originalRandom
			if applyErr == nil || result.Job.JobID != "" || len(result.AcceptedFieldIDs) != 0 {
				t.Fatalf("failed write advertised success: %#v, %v", result, applyErr)
			}
			after, err := os.ReadFile(formJobStatePath(options))
			if err != nil || !slices.Equal(before, after) {
				t.Fatal("failed apply published events/accounting")
			}
			input, err := store.ReadFormIntakeInput(t.Context(), message.Receipt, request.Owner)
			if err != nil || input.Text == "" || input.Consumed || len(input.Accounted) != 0 {
				t.Fatal("failed apply lost raw input")
			}
			retried, err := store.ApplyFormIntakePass(t.Context(), apply)
			if err != nil || retried.Reused || len(retried.AcceptedFieldIDs) != 2 || !retried.Consumed {
				t.Fatalf("retry = %#v, %v", retried, err)
			}
		})
	}
}

func TestFormIntakeApplyCorrectionFailureWhilePruningDoesNotAliasSnapshot(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	composite := FormCompositeRequest{
		JobID: request.JobID, ExpectedRevision: request.ExpectedRevision,
		Owner: request.Owner, Schema: request.Schema, IdempotencyKey: "before-intake",
		Assignments: []FormCompositeAssignment{
			{
				FieldID: request.Schema.Fields[0].ID,
				Value:   FormProtectedValue{Kind: ProtectedValueText, Text: "Casey Mint"},
			},
			{
				FieldID: request.Schema.Fields[1].ID,
				Value:   FormProtectedValue{Kind: ProtectedValueText, Text: "Robin Mint"},
			},
		},
	}
	accepted, err := store.AppendComposite(t.Context(), composite)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedRevision = accepted.Job.Revision
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "correct-two", "Change to Casey Paper and Robin Paper",
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(
		t,
		store,
		request,
		message.Receipt,
		message.Job.Revision,
		[]int{0, 1},
		"correct-two-pass",
	)
	apply.Proposals = []FormIntakeProposal{
		intakeProposal(request.Schema, 0, "Casey Paper"),
		intakeProposal(request.Schema, 1, "Robin Paper"),
	}
	for i := range apply.Proposals {
		apply.Proposals[i].SupersedesEventID = accepted.Job.Fields[i].EventID
		apply.Proposals[i].CorrectionEvidence = []FormIntakeLiteral{intakeLiteral("Change to")}
	}
	apply.NonData = []FormIntakeLiteral{intakeLiteral("and")}
	expiringRequest := testFormJobCreateRequest(request.Owner)
	expiringRequest.StartIdempotencyKey, expiringRequest.Retention = "expire-unrelated-intake", time.Minute
	expiring, err := store.Create(t.Context(), expiringRequest)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(expiring.ExpiresAt).Add(time.Second) }
	store.random = io.LimitReader(rand.Reader, 24)
	result, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err == nil || result.Job.JobID != "" {
		t.Fatalf("failed second correction advertised success: %#v, %v", result, err)
	}
	current, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || current.Revision != message.Job.Revision || !slices.Equal(current.Fields, accepted.Job.Fields) {
		t.Fatalf("expiry published aliased correction: %#v, %v", current, err)
	}
	input, err := store.ReadFormIntakeInput(t.Context(), message.Receipt, request.Owner)
	if err != nil || len(input.Accounted) != 0 || input.Consumed {
		t.Fatal("expiry saved partial accounting")
	}
	other, err := store.Get(t.Context(), expiring.JobID, request.Owner)
	if err != nil || other.State != FormJobExpired {
		t.Fatalf("unrelated expiry was not saved: %#v, %v", other, err)
	}
}

func TestFormIntakeApplyStalePassKeepsAllMessagesAndFacts(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "stale-first", "Casey Mint",
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(t, store, request, first.Receipt, first.Job.Revision, []int{0}, "stale-pass")
	apply.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "Casey Mint")}
	second, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, first.Job.Revision, "stale-second", "Robin Mint",
	))
	if err != nil {
		t.Fatal(err)
	}
	update := request
	update.ExpectedRevision = second.Job.Revision
	update.Context.Question = "Identify the person for these facts."
	updated, err := store.UpdateFormIntakeContext(t.Context(), update, started.Progress.ModeID)
	if err != nil {
		t.Fatal(err)
	}
	if result, applyErr := store.ApplyFormIntakePass(
		t.Context(),
		apply,
	); !errors.Is(applyErr, ErrFormJobConflict) ||
		result.Job.JobID != "" {
		t.Fatalf("stale pass applied: %#v, %v", result, applyErr)
	}
	progress, err := store.FindFormIntake(t.Context(), request.Owner)
	if err != nil || progress.PendingInputs != 2 || progress.Revision != updated.Revision {
		t.Fatal("stale pass discarded a message")
	}
	job, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || job.LedgerRevision != 0 {
		t.Fatal("stale pass published facts")
	}
}

func TestFormIntakeApplyConcurrentExactRetriesPublishOnce(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "concurrent", "Casey Mint",
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(t, store, request, message.Receipt, message.Job.Revision, []int{0}, "concurrent-pass")
	apply.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "Casey Mint")}
	var wait sync.WaitGroup
	results := make(chan FormIntakeApplyResult, 6)
	errors := make(chan error, 6)
	for range 6 {
		wait.Go(func() {
			result, applyErr := store.ApplyFormIntakePass(t.Context(), apply)
			results <- result
			errors <- applyErr
		})
	}
	wait.Wait()
	close(results)
	close(errors)
	for applyErr := range errors {
		if applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	publications := 0
	for result := range results {
		if !result.Reused {
			publications++
		}
		if !result.Consumed || result.Job.LedgerRevision != 1 || result.AppliedRevision != message.Job.Revision+1 {
			t.Fatalf("inconsistent replay: %#v", result)
		}
	}
	if publications != 1 {
		t.Fatalf("publication count = %d", publications)
	}
}

func TestFormIntakeApplyAccountingCapKeepsUnaccountedInput(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	words := make([]string, MaxFormIntakeAccountedSpans+1)
	for i := range words {
		words[i] = fmt.Sprintf("word%d", i)
	}
	text := strings.Join(words, "; ")
	message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "accounting-cap", text,
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(t, store, request, message.Receipt, message.Job.Revision, []int{0}, "accounting-cap-a")
	for _, word := range words[:MaxFormIntakeAccountedSpans] {
		apply.NonData = append(apply.NonData, intakeLiteral(word))
	}
	first, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || first.Consumed {
		t.Fatalf("first accounting = %#v, %v", first, err)
	}
	second := reserveIntakeApply(t, store, request, message.Receipt, first.Job.Revision, []int{0}, "accounting-cap-b")
	second.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, words[MaxFormIntakeAccountedSpans])}
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	if result, applyErr := store.ApplyFormIntakePass(
		t.Context(),
		second,
	); !errors.Is(applyErr, ErrFormJobCapacityExceeded) ||
		result.Job.JobID != "" {
		t.Fatalf("accounting cap = %#v, %v", result, applyErr)
	}
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || !slices.Equal(before, after) {
		t.Fatal("accounting exhaustion published partial state")
	}
	input, err := store.ReadFormIntakeInput(t.Context(), message.Receipt, request.Owner)
	if err != nil || input.Text != text || input.Consumed || len(input.Accounted) != MaxFormIntakeAccountedSpans {
		t.Fatal("accounting exhaustion discarded a fact")
	}
}

func TestFormIntakeApplyReplayCannotNormalizeInvalidUTF8IntoAnAcceptedLiteral(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "unicode-replay", "\uFFFD",
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(
		t,
		store,
		request,
		message.Receipt,
		message.Job.Revision,
		[]int{0},
		"unicode-replay-pass",
	)
	apply.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "\uFFFD")}
	accepted, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || !accepted.Consumed {
		t.Fatalf("valid unicode = %#v, %v", accepted, err)
	}
	// encoding/json maps invalid UTF-8 to U+FFFD; reject before computing the
	// replay digest, not only during first-time literal resolution.
	apply.Proposals[0].Literals[0].Text = "\xff"
	if result, replayErr := store.ApplyFormIntakePass(
		t.Context(),
		apply,
	); !errors.Is(replayErr, ErrFormJobAnswerConflict) ||
		result.Reused {
		t.Fatalf("invalid bytes replayed as valid replacement rune: %#v, %v", result, replayErr)
	}
}

func TestFormIntakeApplyTypedControlsConsumeWithoutFieldAssignment(t *testing.T) {
	for _, intent := range []interactions.ProtectedAnswerIntent{interactions.ProtectedAnswerClarify, interactions.ProtectedAnswerBack} {
		t.Run(string(intent), func(t *testing.T) {
			store, _ := newTestFormJobStore(t)
			request := intakeApplyFixture(t, store, intakeTextSchema())
			started, err := store.StartFormIntake(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			sink, err := NewFormProtectedAnswerSink(store)
			if err != nil {
				t.Fatal(err)
			}
			answer := interactions.ProtectedAnswerSinkRequest{
				Binding:         started.Binding,
				Workspace:       request.Owner.WorkspaceID,
				Route:           testProtectedAnswerRoute(request.Owner),
				InteractionID:   "control-question",
				IdempotencyKey:  "control-question-key",
				MessageIdentity: "control-message",
				Intent:          intent,
			}
			receipt, err := sink.Accept(t.Context(), answer)
			if err != nil {
				t.Fatal(err)
			}
			if err := sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
				Binding:       answer.Binding,
				Workspace:     answer.Workspace,
				Route:         answer.Route,
				InteractionID: answer.InteractionID,
				Receipt:       receipt,
			}); err != nil {
				t.Fatal(err)
			}
			input, err := store.ReadFormIntakeInput(t.Context(), receipt.Reference, request.Owner)
			if err != nil {
				t.Fatal(err)
			}
			apply := reserveIntakeApply(t, store, request, receipt.Reference, input.Revision, []int{0}, "control-pass")
			wrong := apply
			wrong.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "invented")}
			if _, applyErr := store.ApplyFormIntakePass(
				t.Context(),
				wrong,
			); !errors.Is(
				applyErr,
				ErrFormJobAnswerConflict,
			) {
				t.Fatalf("control became a fact: %v", applyErr)
			}
			result, err := store.ApplyFormIntakePass(t.Context(), apply)
			if err != nil || !result.Consumed || result.Job.LedgerRevision != 0 || len(result.AcceptedFieldIDs) != 0 ||
				result.Progress.PendingInputs != 0 {
				t.Fatalf("control = %#v, %v", result, err)
			}
		})
	}
}

func TestFormIntakeApplyEventCapDoesNotAcceptPrefix(t *testing.T) {
	store, options := newTestFormJobStoreWithLimits(t, 10, 1)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "event-cap", "Casey Mint; Robin Paper",
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(t, store, request, message.Receipt, message.Job.Revision, []int{0, 1}, "event-cap-pass")
	apply.Proposals = []FormIntakeProposal{
		intakeProposal(request.Schema, 0, "Casey Mint"),
		intakeProposal(request.Schema, 1, "Robin Paper"),
	}
	before, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	if result, applyErr := store.ApplyFormIntakePass(
		t.Context(),
		apply,
	); !errors.Is(applyErr, ErrFormJobCapacityExceeded) ||
		result.Job.JobID != "" {
		t.Fatalf("event cap = %#v, %v", result, applyErr)
	}
	after, err := os.ReadFile(formJobStatePath(options))
	if err != nil || !slices.Equal(before, after) {
		t.Fatal("event cap published a prefix")
	}
}

func TestFormIntakeApplyCancelRaceNeverResurrectsErasedInput(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "cancel-race", intakePrivateSentinel,
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(t, store, request, message.Receipt, message.Job.Revision, []int{0}, "cancel-race-pass")
	apply.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, intakePrivateSentinel)}
	var wait sync.WaitGroup
	var result FormIntakeApplyResult
	var applyErr, cancelErr error
	wait.Go(func() { result, applyErr = store.ApplyFormIntakePass(t.Context(), apply) })
	wait.Go(func() { _, cancelErr = store.Cancel(t.Context(), request.JobID, message.Job.Revision, request.Owner) })
	wait.Wait()
	if (applyErr == nil) == (cancelErr == nil) {
		t.Fatalf("competing revisions both succeeded or failed: apply=%v cancel=%v", applyErr, cancelErr)
	}
	if cancelErr != nil {
		if !errors.Is(cancelErr, ErrFormJobConflict) || result.Job.JobID == "" {
			t.Fatalf("unexpected cancel race: %v", cancelErr)
		}
		if _, err := store.Cancel(t.Context(), request.JobID, result.Job.Revision, request.Owner); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := store.ApplyFormIntakePass(t.Context(), apply); err == nil || result.Job.JobID != "" {
		t.Fatalf("cancel resurrected input: %#v, %v", result, err)
	}
	if _, err := store.ReadFormIntakeInput(t.Context(), message.Receipt, request.Owner); err == nil {
		t.Fatal("canceled raw input remained readable")
	}
	assertFormStoreContainsNoPlaintext(t, options, intakePrivateSentinel)
	job, err := store.Get(t.Context(), request.JobID, request.Owner)
	if err != nil || job.State != FormJobCanceled {
		t.Fatalf("cancel state = %#v, %v", job, err)
	}
}

func TestFormIntakeApplyDoesNotAcknowledgeAnUnresolvedLowConfidenceValue(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	request := intakeApplyFixture(t, store, intakeTextSchema())
	job, _, err := store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: request.JobID, ExpectedRevision: request.ExpectedRevision, Owner: request.Owner,
		FieldID: request.Schema.Fields[0].ID, IdempotencyKey: "low-confidence-before-intake",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Casey Mint"},
		State: FormValueConfirmed, Source: FormValueSourceUser, Confidence: FormValueConfidenceLow,
		Validation: FormValueValidationValid,
	})
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedRevision = job.Revision
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request, started.Progress.ModeID, started.Job.Revision, "repeat-unresolved", "Casey Mint",
	))
	if err != nil {
		t.Fatal(err)
	}
	apply := reserveIntakeApply(
		t,
		store,
		request,
		message.Receipt,
		message.Job.Revision,
		[]int{0},
		"repeat-unresolved-pass",
	)
	apply.Proposals = []FormIntakeProposal{intakeProposal(request.Schema, 0, "Casey Mint")}
	result, err := store.ApplyFormIntakePass(t.Context(), apply)
	if err != nil || result.Consumed || len(result.AcknowledgedIDs) != 0 || len(result.AcceptedFieldIDs) != 0 ||
		len(result.Findings) != 1 || result.Findings[0].Code != "field_conflicting" {
		t.Fatalf("unresolved low-confidence value was acknowledged: %#v, %v", result, err)
	}
}
