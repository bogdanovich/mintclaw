package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type intakeWorkflowTestInterpreter struct {
	workflowTestAuditor
	interpret   func(document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error)
	intakeCalls int
}

func (model *intakeWorkflowTestInterpreter) InterpretFormIntake(
	_ context.Context, _ string, view document.FormIntakeInterpretationView,
) (document.FormIntakeInterpretation, error) {
	model.intakeCalls++
	return model.interpret(view)
}

func intakeWorkflowFixture(
	t *testing.T, model *intakeWorkflowTestInterpreter, schemaChanges ...func(*document.FormFieldsFacts),
) (*DocumentTool, context.Context, document.FormFieldsFacts, string, document.FormJobStoreOptions) {
	t.Helper()
	store, options := newWorkflowFormStore(t)
	t.Cleanup(store.Close)
	mediaStore := media.NewFileMediaStore()
	t.Cleanup(mediaStore.Stop)
	data := []byte("%PDF-1.7\nintake fixture\n%%EOF\n")
	path := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := mediaStore.Store(path, media.MediaMeta{
		Filename: "source.pdf", Source: "test",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "intake-source")
	if err != nil {
		t.Fatal(err)
	}
	if err = mediaStore.BindOwner(ref, documentToolTestOwner(t)); err != nil {
		t.Fatal(err)
	}
	schema := workflowTestSchema(data)
	schema.Fields = nil
	for index := 0; index < 13; index++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("generic-intake-field-%d", index)))
		schema.Fields = append(schema.Fields, document.FormField{
			ID: fmt.Sprintf("field_%064x", index+1), Name: fmt.Sprintf("Synthetic fact %d", index),
			Kind: document.FormFieldText, Required: true,
			Widgets: []document.FormFieldWidget{{ID: "widget_" + hex.EncodeToString(digest[:]), Page: 1, Ordinal: 1}},
		})
	}
	for _, change := range schemaChanges {
		change(&schema)
	}
	tool := NewDocumentTool(WithDocumentFormJobStore(store), WithDocumentFormAudit(document.FormAuditPolicy{
		PrimaryModel: "deliberative", PrimaryIdentity: "test:deliberative",
	}, model), WithDocumentFormIntake(model))
	tool.SetMediaStore(mediaStore)
	tool.formSchema = workflowSchemaResolver(schema)
	tool.formIntakeEvidence = func(
		context.Context, ownedDocumentMediaStore, string, media.MediaOwner, []int,
	) ([]document.FormIntakeTextEvidence, error) {
		return nil, nil
	}
	ctx := workflowToolContext(t, "intake-execution", "intake-call", []string{ref})
	discovered := tool.Execute(ctx, map[string]any{"action": "form", "form_action": "discover", "source": ref})
	if discovered.IsError {
		t.Fatal(discovered.ForLLM)
	}
	prepared := tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "start", "source": ref,
		"field_schema_digest": decodeWorkflowResult(t, discovered.ForLLM).FieldSchemaDigest,
	})
	if prepared.IsError {
		t.Fatal(prepared.ForLLM)
	}
	jobID := decodeWorkflowResult(t, prepared.ForLLM).Job.JobID
	ids := make([]string, 8)
	for index := range ids {
		ids[index] = schema.Fields[index].ID
	}
	collect := tool.Execute(ctx, map[string]any{
		"action":              "form",
		"form_action":         "collect",
		"job_id":              jobID,
		"candidate_field_ids": ids,
		"question":            "Provide identity, contact and address details in your own words.",
		"role_context":        "The user supplies their own facts.",
		"form_summary":        "This generic test form records several details.",
		"collection_plan":     "Identity, contact and address; only missing or unclear information will be requested.",
	})
	if collect.IsError || collect.Control.Suspension != nil {
		t.Fatal("grouped collect did not start ordinary-message intake")
	}
	projection := decodeWorkflowResult(t, collect.ForLLM)
	if projection.Intake == nil || projection.Intake.Progress.JobID != jobID {
		t.Fatal("missing intake progress")
	}
	followup, err := tool.ToolResultFollowup(collect)
	if err != nil || followup == nil || !followup.ResponseOnly {
		t.Fatal("grouped question did not release a response-only checkpoint")
	}
	return tool, ctx, schema, jobID, options
}

func TestDocumentFormIntakeFiveFactsOutsideFirstWindowAndThreeOrdinaryMessages(t *testing.T) {
	values := []string{
		"robin.mint@example.invalid", "202-555-0168", "Robin Mint", "1975-11-23",
		"14 Mint Lane", "Mintford", "20500", "US", "Casey Mint", "1989-02-15",
		"casey.mint@example.invalid", "202-555-0147", "8 Paper Road",
	}
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, options := intakeWorkflowFixture(t, model)
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}
		for _, field := range view.Pass.Fields {
			index := slices.IndexFunc(
				schema.Fields,
				func(candidate document.FormField) bool { return candidate.ID == field.ID },
			)
			if strings.Contains(view.Pass.Input.Text, values[index]) {
				result.Proposals = append(result.Proposals, document.FormIntakeProposal{
					FieldID:  field.ID,
					Literals: []document.FormIntakeLiteral{{Text: values[index], Occurrence: 1}},
				})
			}
		}
		proseFragments := []string{"The address is", "in", "postal code", "country"}
		if strings.HasPrefix(view.Pass.Input.Text, "I am") {
			proseFragments = []string{"I am", "born", "you can reach me at", "or", "I live at"}
		} else if strings.HasPrefix(view.Pass.Input.Text, "My contact") {
			proseFragments = []string{"My contact is", "was born", "email", "phone"}
		}
		for _, prose := range proseFragments {
			if strings.Contains(view.Pass.Input.Text, prose) {
				result.NonData = append(result.NonData, document.FormIntakeLiteral{Text: prose, Occurrence: 1})
			}
		}
		return result, nil
	}
	messages := []string{
		"I am Casey Mint, born 1989-02-15; you can reach me at casey.mint@example.invalid or 202-555-0147. I live at 8 Paper Road.",
		"My contact is Robin Mint, was born 1975-11-23; email robin.mint@example.invalid, phone 202-555-0168.",
		"The address is 14 Mint Lane in Mintford, postal code 20500, country US.",
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for index, text := range messages {
		progress, err := tool.FindProtectedFormIntake(ctx)
		if err != nil || progress == nil {
			t.Fatal(err)
		}
		guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
		ref, err := tool.AdmitProtectedFormIntake(ctx, guard, fmt.Sprintf("ordinary-%d", index), text)
		if err != nil {
			t.Fatal(err)
		}
		args, err := tool.ProtectedAnswerContinuationArguments(ref)
		if err != nil {
			t.Fatal(err)
		}
		continued := tool.Execute(ctx, args)
		projection := decodeWorkflowResult(t, continued.ForLLM)
		if continued.IsError || projection.Intake == nil || projection.Intake.Failure != nil ||
			projection.Intake.Progress.PendingInputs != 0 || projection.Intake.AcceptedCount != []int{5, 4, 4}[index] {
			t.Fatalf("ordinary message %d was not fully accounted: %s", index, continued.ForLLM)
		}
		for _, private := range values {
			if strings.Contains(continued.ForLLM, private) || strings.Contains(continued.ContextText, private) {
				t.Fatal("accepted personal value escaped safe progress")
			}
		}
		calls := model.intakeCalls
		replayed := tool.Execute(ctx, args)
		if replayed.IsError || model.intakeCalls != calls {
			t.Fatal("receipt replay called the private model again")
		}
		if index == 0 {
			tool.formJobs.Close()
			tool.formJobs, err = document.OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tool.formJobs.Close)
		}
	}
	record, err := tool.formJobs.Get(t.Context(), jobID, owner)
	if err != nil || len(record.Fields) != 13 {
		t.Fatal("successive paragraphs did not share the same ledger")
	}
	ids := make([]string, len(schema.Fields))
	for index, field := range schema.Fields {
		ids[index] = field.ID
	}
	accepted, err := tool.formJobs.ReadValues(t.Context(), jobID, owner, ids)
	if err != nil {
		t.Fatal(err)
	}
	for index, field := range schema.Fields {
		if accepted[field.ID].Value.Text != values[index] {
			t.Fatal("fact was lost or assigned to the wrong field")
		}
	}
	if model.intakeCalls != 4 {
		t.Fatalf("unbounded or repeated private passes: %d", model.intakeCalls)
	}
	model.proposal = document.FormAuditProposal{Decision: document.FormAuditPass}
	reviewed := tool.Execute(ctx, map[string]any{"action": "form", "form_action": "review", "job_id": jobID})
	review := decodeWorkflowResult(t, reviewed.ForLLM)
	if reviewed.IsError || review.Review == nil || !review.Review.Ready {
		t.Fatal("accepted paragraphs did not reach native review")
	}
	reviewFollowup, err := tool.ToolResultFollowup(reviewed)
	if err != nil || reviewFollowup == nil || !reviewFollowup.ResponseOnly {
		t.Fatal("review did not wait for a separate finish request")
	}
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		return document.FormIntakeInterpretation{
			Intent:  document.FormIntakeIntentFinish,
			NonData: []document.FormIntakeLiteral{{Text: view.Pass.Input.Text, Occurrence: 1}},
		}, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finishRef, err := tool.AdmitProtectedFormIntake(ctx,
		bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}, "separate-finish", "Finish this PDF")
	if err != nil {
		t.Fatal(err)
	}
	finishArgs, err := tool.ProtectedAnswerContinuationArguments(finishRef)
	if err != nil {
		t.Fatal(err)
	}
	finished := tool.Execute(ctx, finishArgs)
	finish := decodeWorkflowResult(t, finished.ForLLM)
	if finished.IsError || finish.Review == nil || !finish.Review.Ready || finish.Intake == nil ||
		finish.Intake.Progress.PendingInputs != 0 || finish.Intake.Intent != document.FormIntakeIntentFinish ||
		finish.Review.ReviewDigest != review.Review.ReviewDigest ||
		finish.Intake.RequestRevision != review.Review.ReviewRevision {
		t.Fatal("control-only finish reset the review or lost its admission provenance")
	}
	followup, err := tool.ProtectedAnswerContinuationFollowup(finished)
	if err != nil || followup == nil || followup.ResponseOnly {
		t.Fatal("separate finish failed to request existing final approval")
	}
	if err = followup.ValidateArguments(
		map[string]any{"action": "form", "form_action": "commit", "job_id": jobID},
	); err != nil {
		t.Fatal(err)
	}
	calls := model.intakeCalls
	tool.formJobs.Close()
	tool.formJobs, err = document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tool.formJobs.Close)
	replayed := tool.Execute(ctx, finishArgs)
	replay := decodeWorkflowResult(t, replayed.ForLLM)
	if replayed.IsError || model.intakeCalls != calls || replay.Intake == nil ||
		replay.Intake.Intent != finish.Intake.Intent || replay.Intake.RequestRevision != finish.Intake.RequestRevision {
		t.Fatal("restart lost finish intent or called the private interpreter again")
	}
}

func TestDocumentFormIntakeFailureReportsAcceptedPrefixWithoutExposingIt(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model)
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		if !slices.Contains(view.Pass.CandidateIDs, schema.Fields[0].ID) {
			return document.FormIntakeInterpretation{}, errors.New("PRIVATE_PROVIDER_FAILURE")
		}
		return document.FormIntakeInterpretation{
			Intent: document.FormIntakeIntentFacts,
			Proposals: []document.FormIntakeProposal{{
				FieldID:  schema.Fields[0].ID,
				Literals: []document.FormIntakeLiteral{{Text: "Casey Mint", Occurrence: 1}},
			}},
		}, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := tool.AdmitProtectedFormIntake(
		ctx,
		bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID},
		"mixed-message",
		"Casey Mint and additional unmapped facts",
	)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(ctx, map[string]any{"action": "form", "form_action": "continue", "answer_ref": ref})
	projection := decodeWorkflowResult(t, result.ForLLM)
	if result.IsError || projection.Intake == nil || projection.Intake.AcceptedCount != 1 ||
		projection.Intake.Failure == nil || projection.Intake.Progress.PendingInputs != 1 ||
		strings.Contains(result.ForLLM, "Casey Mint") || strings.Contains(result.ForLLM, "PRIVATE_PROVIDER_FAILURE") {
		t.Fatal("partial interpretation was falsely all-failed, all-consumed or exposed")
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, err := tool.formJobs.Get(t.Context(), jobID, owner)
	if err != nil || len(record.Fields) != 1 {
		t.Fatal("accepted prefix was rolled back")
	}
	followup, err := tool.ProtectedAnswerContinuationFollowup(result)
	if err != nil || followup == nil || !followup.ResponseOnly {
		t.Fatal("failure did not produce a truthful progress checkpoint")
	}
}

var _ toolshared.ProtectedAnswerContinuationFollowupProvider = (*DocumentTool)(nil)

func TestDocumentFormIntakeWindowsCoverHashSortedFieldsWithoutExpandingEvidence(t *testing.T) {
	schema := document.FormFieldsFacts{}
	var initial []string
	for index := 0; index < 64; index++ {
		id := fmt.Sprintf("field_%064x", index+1)
		schema.Fields = append(schema.Fields, document.FormField{
			ID:      id,
			Widgets: []document.FormFieldWidget{{Page: index%8 + 1}},
		})
		if index < 8 {
			initial = append(initial, id)
		}
	}
	seen := make(map[string]bool)
	for _, window := range formIntakeCandidateWindows(schema, initial) {
		if len(window) == 0 || len(window) > document.MaxFormIntakeCandidates {
			t.Fatal("field view exceeded its bound")
		}
		pages := make(map[int]bool)
		for _, id := range window {
			if seen[id] {
				t.Fatal("a field was unnecessarily scanned in two windows")
			}
			seen[id] = true
			index := slices.IndexFunc(schema.Fields, func(field document.FormField) bool { return field.ID == id })
			pages[schema.Fields[index].Widgets[0].Page] = true
		}
		if len(pages) > documentFormEvidencePageLimit {
			t.Fatal("hash order expanded a private request into many pages")
		}
	}
	if len(seen) != len(schema.Fields) {
		t.Fatal("structural page batching discarded a later field")
	}
}

func TestDocumentFormIntakeIsNotEnabledByAuditWiringAlone(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool := NewDocumentTool(WithDocumentFormAudit(document.FormAuditPolicy{}, model))
	properties := tool.Parameters()["properties"].(map[string]any)
	if tool.formInterpreter != nil || tool.ProtectedFormIntakeConfigured() ||
		properties["candidate_field_ids"] != nil || properties["role_context"] != nil {
		t.Fatal("incomplete output integration enabled grouped production intake")
	}
}

func TestDocumentFormIntakeLateWindowFindingHasAValueFreeFieldLabel(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model)
	last := schema.Fields[len(schema.Fields)-1]
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentClarify}
		if slices.Contains(view.Pass.CandidateIDs, last.ID) {
			result.Findings = []document.FormFieldMappingBlocker{{FieldID: last.ID, Code: "field_ambiguous"}}
		}
		return result, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const private = "MINTCLAW_PRIVATE_UNCERTAIN_PERSON_15ef"
	ref, err := tool.AdmitProtectedFormIntake(ctx, bus.InboundDocumentIntakeGuard{
		JobID: jobID, ModeID: progress.ModeID,
	}, "late-window-uncertainty", private)
	if err != nil {
		t.Fatal(err)
	}
	continued := tool.Execute(ctx, map[string]any{"action": "form", "form_action": "continue", "answer_ref": ref})
	projection := decodeWorkflowResult(t, continued.ForLLM)
	if continued.IsError || projection.Intake == nil || len(projection.Intake.FindingFields) != 1 ||
		projection.Intake.FindingFields[0].FieldID != last.ID || projection.Intake.FindingFields[0].Label != last.Name ||
		projection.Intake.Progress.PendingInputs != 1 || strings.Contains(continued.ForLLM, private) {
		t.Fatal("late-window uncertainty lost its meaningful label or exposed protected input")
	}
}

func TestDocumentFormIntakePauseResumeAndCancelPreserveOrEraseAsRequested(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, _, jobID, _ := intakeWorkflowFixture(t, model)
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
	ref, err := tool.AdmitProtectedFormIntake(ctx, guard, "pending-before-pause", "Private pending facts")
	if err != nil {
		t.Fatal(err)
	}
	if err = tool.ControlProtectedFormIntake(ctx, guard, "pause"); err != nil {
		t.Fatal(err)
	}
	paused, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || paused.State != document.FormIntakePaused || paused.PendingInputs != 1 {
		t.Fatal("pause discarded pending input")
	}
	if _, err = tool.AdmitProtectedFormIntake(ctx, guard, "while-paused", "Other private facts"); err == nil {
		t.Fatal("paused mode admitted new data")
	}
	if err = tool.ControlProtectedFormIntake(ctx, guard, "resume"); err != nil {
		t.Fatal(err)
	}
	resumed, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || resumed.ModeID != guard.ModeID || resumed.State != document.FormIntakeActive ||
		resumed.PendingInputs != 1 {
		t.Fatal("resume replaced the document or lost pending data")
	}
	if err = tool.ControlProtectedFormIntake(ctx, guard, "cancel"); err != nil {
		t.Fatal(err)
	}
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tool.formJobs.ReadFormIntakeInput(ctx, ref, owner); err == nil {
		t.Fatal("cancel did not erase protected input authority")
	}
	current, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || current != nil {
		t.Fatal("canceled mode still owns ordinary ingress")
	}
}
