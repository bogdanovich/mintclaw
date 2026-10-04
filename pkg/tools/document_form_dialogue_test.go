package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFormDialogueBrowsesBeforeAndAfterProtectedAnswer(t *testing.T) {
	for _, count := range []int{2, 250} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			tool, job, schema := newDocumentDialogueJob(t, count, document.FormFieldText)
			ctx := workflowToolContext(t, "dialog-browse", "dialog-browse-call", nil)
			initial := tool.formProgressResult(ctx, mustDocumentFormOwner(t, ctx), schema, job, "start", "")
			followup, err := tool.ToolResultFollowup(initial)
			if err != nil || followup == nil {
				t.Fatalf("initial follow-up = %#v, %v", followup, err)
			}
			args := map[string]any{
				"action": "form", "form_action": "status", "job_id": job.JobID,
				"pages": []int{schema.Fields[count-1].Widgets[0].Page}, "field_offset": 0,
			}
			if err = followup.ValidateArguments(args); err != nil {
				t.Fatal(err)
			}
			browse := tool.Execute(ctx, args)
			view := decodeWorkflowResult(t, browse.ForLLM)
			if browse.IsError || view.Job.JobID != job.JobID || !view.Job.NeedsInitialPlan ||
				!view.Mapping.Window.Selected || len(view.Mapping.CandidateFields) > documentFormCandidateLimit {
				t.Fatalf("prepared view = %#v, %#v", view, browse)
			}
			followup, err = tool.ToolResultFollowup(browse)
			if err != nil || followup == nil ||
				!strings.Contains(followup.Instruction, "form_summary and collection_plan") {
				t.Fatalf("browsing lost initial plan fence: %#v, %v", followup, err)
			}
			field := view.Mapping.CandidateFields[0]
			question := map[string]any{
				"action": "form", "form_action": "collect", "job_id": job.JobID, "field_id": field.FieldID,
				"question": "Which test fact belongs in this section?",
			}
			if err = followup.ValidateArguments(question); err == nil {
				t.Fatal("browsing bypassed the initial plan")
			}
			question["form_summary"] = "A multi-section test form, with existing facts preserved."
			question["collection_plan"] = "I will ask personal facts through protected questions, then show a review."
			if err = followup.ValidateArguments(question); err != nil {
				t.Fatal(err)
			}
			projection := view
			projection.FormAction = "continue"
			projection.Job.NeedsInitialPlan = false
			followup, err = tool.ProtectedAnswerContinuationFollowup(documentFormToolResult(projection))
			if err != nil || followup == nil || followup.ValidateArguments(args) != nil {
				t.Fatalf("receipt cannot browse: %#v, %v", followup, err)
			}
			if err = followup.ValidateArguments(map[string]any{
				"action": "form", "form_action": "clarify_intent", "job_id": job.JobID,
			}); err == nil {
				t.Fatal("protected receipt escaped into ordinary intent collection")
			}
		})
	}
}

func TestDocumentFormDialogueEmptyWindowCannotEscapeFence(t *testing.T) {
	tool, job, _ := newDocumentDialogueJob(t, 2, document.FormFieldText)
	result := tool.Execute(workflowToolContext(t, "empty", "empty-call", nil), map[string]any{
		"action": "form", "form_action": "status", "job_id": job.JobID, "pages": []int{99},
	})
	view := decodeWorkflowResult(t, result.ForLLM)
	if result.IsError || len(view.Mapping.CandidateFields) != 0 {
		t.Fatalf("empty window = %#v", result)
	}
	followup, err := tool.ToolResultFollowup(result)
	if err != nil || followup == nil || followup.ResponseOnly {
		t.Fatalf("empty window released the fence: %#v, %v", followup, err)
	}
	for _, args := range []map[string]any{
		{"action": "form", "form_action": "status", "job_id": "another", "pages": []int{1}},
		{"action": "form", "form_action": "status", "job_id": job.JobID},
		{"action": "form", "form_action": "commit", "job_id": job.JobID},
		{"action": "form", "form_action": "collect", "job_id": job.JobID, "field_id": "invented", "question": "Value?"},
	} {
		if followup.ValidateArguments(args) == nil {
			t.Fatalf("empty-window escape accepted: %#v", args)
		}
	}
	if followup.ValidateArguments(map[string]any{
		"action": "form", "form_action": "status", "job_id": job.JobID, "field_offset": 0,
	}) != nil {
		t.Fatal("empty window cannot browse back")
	}
}

func TestDocumentFormDialogueEvidenceIsBoundedAndJobFenced(t *testing.T) {
	projection := safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: "evidence",
		Job: &safeDocumentFormJob{JobID: "form_job_a", State: document.FormJobPrepared, NeedsInitialPlan: true},
		Mapping: &safeDocumentFormMapping{
			Window: safeDocumentFormWindow{Selected: true, Pages: []int{3}},
			CandidateFields: []safeDocumentFormField{
				{FieldID: "field_a", Kind: document.FormFieldText, Blocker: "field_unresolved"},
			},
		},
	}
	followup, err := NewDocumentTool().ToolResultFollowup(documentFormToolResult(projection))
	if err != nil || followup == nil || followup.ResponseOnly {
		t.Fatalf("evidence released the fence: %#v, %v", followup, err)
	}
	for _, args := range []map[string]any{
		{"action": "form", "form_action": "evidence", "job_id": "form_job_a", "pages": []int{1, 2}},
		{"action": "form", "form_action": "evidence", "job_id": "form_job_a", "pages": []int{3}, "evidence_mode": "render"},
	} {
		if followup.ValidateArguments(args) != nil {
			t.Fatalf("bounded evidence rejected: %#v", args)
		}
	}
	for _, args := range []map[string]any{
		{"action": "form", "form_action": "evidence", "job_id": "form_job_a"},
		{"action": "form", "form_action": "evidence", "job_id": "another", "pages": []int{1}},
		{"action": "form", "form_action": "evidence", "job_id": "form_job_a", "pages": []int{1, 2, 3}},
		{"action": "form", "form_action": "evidence", "job_id": "form_job_a", "pages": []int{1, 2}, "evidence_mode": "render"},
		{"action": "form", "form_action": "evidence", "job_id": "form_job_a", "pages": []int{1}, "evidence_mode": "invalid"},
		{"action": "form", "form_action": "status", "job_id": "form_job_a", "pages": []int{1}, "evidence_mode": "text"},
		{"action": "form", "form_action": "evidence", "job_id": "form_job_a", "pages": []int{1}, "max_characters": 40000},
		{"action": "form", "form_action": "evidence", "job_id": "form_job_a", "pages": []int{1}, "retain": true},
	} {
		if followup.ValidateArguments(args) == nil {
			t.Fatalf("unbounded or different-job evidence accepted: %#v", args)
		}
	}
	tool, job, _ := newDocumentDialogueJob(t, 1, document.FormFieldText)
	ctx := workflowToolContext(t, "vision-unavailable", "vision-unavailable-call", nil)
	ctx = toolshared.WithToolDocumentContext(ctx, nil, false)
	unavailable := tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "evidence", "job_id": job.JobID, "pages": []int{1}, "evidence_mode": "render",
	})
	if !unavailable.IsError || !strings.Contains(unavailable.ForLLM, "vision_unavailable") ||
		len(unavailable.ContextMedia) != 0 {
		t.Fatalf("job evidence bypassed the vision guard: %#v", unavailable)
	}
}

func TestDocumentFormDialogueReadyWindowAllowsCorrectionOrReviewNotCommit(t *testing.T) {
	projection := safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form", FormAction: "status",
		Job: &safeDocumentFormJob{JobID: "form_job_a"},
		Mapping: &safeDocumentFormMapping{
			ReadyForReview: true,
			Window:         safeDocumentFormWindow{Selected: true},
			CandidateFields: []safeDocumentFormField{
				{FieldID: "field_a", Kind: document.FormFieldText, Status: "preserved"},
			},
		},
	}
	followup, err := NewDocumentTool().ToolResultFollowup(documentFormToolResult(projection))
	if err != nil || followup == nil {
		t.Fatalf("ready browse unavailable: %#v, %v", followup, err)
	}
	for _, args := range []map[string]any{
		{"action": "form", "form_action": "review", "job_id": "form_job_a"},
		{"action": "form", "form_action": "correct", "job_id": "form_job_a", "field_id": "field_a", "question": "What replaces this fact?"},
	} {
		if followup.ValidateArguments(args) != nil {
			t.Fatalf("ready correction/review rejected: %#v", args)
		}
	}
	if followup.ValidateArguments(
		map[string]any{"action": "form", "form_action": "commit", "job_id": "form_job_a"},
	) == nil {
		t.Fatal("ready browse bypassed review and approval")
	}
}

func TestDocumentFormDialogueIntentCheckpointKeepsJobWithoutAttachment(t *testing.T) {
	tool, job, schema := newDocumentDialogueJob(t, 2, document.FormFieldText)
	ctx := workflowToolContext(t, "intent", "intent-call", nil)
	result := tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "clarify_intent", "job_id": job.JobID,
	})
	view := decodeWorkflowResult(t, result.ForLLM)
	if result.IsError || result.Control.Suspension != nil || view.Job.JobID != job.JobID ||
		view.Job.Revision != job.Revision || !view.Job.NeedsInitialPlan {
		t.Fatalf("intent checkpoint = %#v, %#v", result, view)
	}
	followup, err := tool.ToolResultFollowup(result)
	if err != nil || followup == nil || !followup.ResponseOnly || followup.ValidateArguments != nil ||
		!strings.Contains(followup.Instruction, "Do not ask for a personal value") {
		t.Fatalf("intent response boundary = %#v, %v", followup, err)
	}
	resumed := tool.Execute(workflowToolContext(t, "next-turn", "next-call", nil), map[string]any{
		"action": "form", "form_action": "collect", "job_id": job.JobID, "field_id": schema.Fields[1].ID,
		"question":        "What test fact belongs to the selected party?",
		"form_summary":    "A form with two possible parties.",
		"collection_plan": "I will collect the selected party's facts through protected questions and show a review.",
	})
	if resumed.IsError || resumed.Control.Suspension == nil {
		t.Fatalf("same job cannot resume without current attachment: %#v", resumed)
	}
	wrongOwner := workflowToolContext(t, "another-owner", "another-call", nil)
	wrongOwner = toolshared.WithToolInboundMetadata(wrongOwner, bus.InboundContext{
		Channel: "telegram", Account: "primary", ChatID: "chat", ChatType: "direct",
		SenderID: "another-sender", ActorID: "another-sender",
	})
	denied := tool.Execute(wrongOwner, map[string]any{
		"action": "form", "form_action": "clarify_intent", "job_id": job.JobID,
	})
	if !denied.IsError {
		t.Fatal("another owner reached the intent checkpoint")
	}
	private := "PDFI3_PRIVATE_ANSWER_7b42"
	if _, _, err = tool.formJobs.AppendValue(ctx, document.FormJobAppendValueRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: mustDocumentFormOwner(t, ctx),
		FieldID: schema.Fields[1].ID, IdempotencyKey: "intent-first-answer",
		Value: document.FormProtectedValue{Kind: document.ProtectedValueText, Text: private},
		State: document.FormValueConfirmed, Source: document.FormValueSourceUser,
	}); err != nil {
		t.Fatal(err)
	}
	blocked := tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "clarify_intent", "job_id": job.JobID,
	})
	if !blocked.IsError || !strings.Contains(blocked.ForLLM, "form_job_conflict") ||
		strings.Contains(blocked.ForLLM, private) {
		t.Fatalf("recorded answer escaped into ordinary planning: %#v", blocked)
	}
}

func TestDocumentFormDialogueNotesAreBoundedAndCannotAssignValues(t *testing.T) {
	tool := NewDocumentTool()
	notes := map[string]any{
		"action":          "form",
		"form_action":     "discover",
		"source":          "media://00000000-0000-0000-0000-000000000123",
		"form_summary":    "This form records parties, their eligibility, and a declaration.",
		"collection_plan": "I will clarify applicability, collect the selected party's protected facts, then show a review.",
	}
	if err := validateDocumentActionOptions("form", notes); err != nil {
		t.Fatal(err)
	}
	durable, err := tool.DurableArguments(notes)
	if err != nil || durable["form_summary"] != notes["form_summary"] ||
		durable["collection_plan"] != notes["collection_plan"] {
		t.Fatalf("value-free notes cannot survive the evidence call: %#v, %v", durable, err)
	}
	notes["form_summary"] = strings.Repeat("x", documentFormSummaryMaxRunes+1)
	if validateDocumentActionOptions("form", notes) == nil {
		t.Fatal("unbounded source-sized summary accepted")
	}
	notes["form_summary"] = "A short form."
	notes["collection_plan"] = strings.Repeat("x", documentFormPlanMaxRunes+1)
	if validateDocumentActionOptions("form", notes) == nil {
		t.Fatal("unbounded plan accepted")
	}
	notes["collection_plan"] = "A short protected collection plan."
	notes["assignments"] = map[string]any{"field": "value"}
	if validateDocumentActionOptions("form", notes) == nil {
		t.Fatal("planning notes gained assignment authority")
	}
}

func TestDocumentFormDialogueCheckboxFailsSafelyAndRetainsPresentation(t *testing.T) {
	tool, job, schema := newDocumentDialogueJob(t, 1, document.FormFieldCheckbox)
	ctx := workflowToolContext(t, "checkbox", "checkbox-call", nil)
	owner := mustDocumentFormOwner(t, ctx)
	projection := safeDocumentFormResult{
		Job: &safeDocumentFormJob{JobID: job.JobID, NeedsInitialPlan: true},
		Mapping: &safeDocumentFormMapping{CandidateFields: []safeDocumentFormField{{
			FieldID: schema.Fields[0].ID, Kind: document.FormFieldCheckbox, Blocker: "field_unresolved",
		}}},
	}
	followup, err := documentFormToolOnlyFollowup(projection, true)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"action": "form", "form_action": "collect", "job_id": job.JobID, "field_id": schema.Fields[0].ID,
		"question": "Who is this form for?", "form_summary": "A two-party test form.",
		"collection_plan": "I will collect protected answers for the applicable party, then show a review.",
	}
	if followup.ValidateArguments(args) == nil {
		t.Fatal("checkbox follow-up silently admitted generic Yes/No")
	}
	rejected := tool.Execute(ctx, args)
	if !rejected.IsError || rejected.Control.Suspension != nil ||
		!strings.Contains(rejected.ForLLM, "checkbox_labels_required") {
		t.Fatalf("checkbox omitted-label request did not fail safely: %#v", rejected)
	}
	presentation := documentFormQuestionPresentation{
		question: "Who is this form for?", checkedLabel: "Another person", uncheckedLabel: "Myself", language: "ru",
	}
	result := tool.formQuestionResult(ctx, owner, schema, job, schema.Fields[0].ID, "collect", presentation)
	if result.IsError || result.Control.Suspension == nil {
		t.Fatalf("checkbox question = %#v", result)
	}
	saved, err := tool.formJobs.QuestionControls(ctx, document.FormProtectedAnswerBindingRequest{
		JobID: job.JobID, ExpectedRevision: job.Revision, Owner: owner, FieldID: schema.Fields[0].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := result.Control.Suspension.Questions[0]
	for _, action := range []string{"clarify", "back"} {
		reasked := tool.formQuestionResult(ctx, owner, schema, job, schema.Fields[0].ID, action,
			documentFormQuestionPresentation{
				question: saved.Question, language: saved.Language,
				checkedLabel: saved.CheckedLabel, uncheckedLabel: saved.UncheckedLabel,
			})
		if reasked.IsError || reasked.Control.Suspension == nil {
			t.Fatalf("checkbox %s re-ask = %#v", action, reasked)
		}
		next := reasked.Control.Suspension.Questions[0]
		if first.Question != next.Question || !slices.Equal(first.Options, next.Options) ||
			first.Header != next.Header {
			t.Fatalf("%s changed question meaning: %#v -> %#v", action, first, next)
		}
		if next.Introduction != interactions.PromptText(saved.Language, interactions.PromptFormAnswerHint) {
			t.Fatalf("%s omitted the localized answer hint: %#v", action, next)
		}
	}
}

func newDocumentDialogueJob(
	t *testing.T,
	count int,
	lastKind document.FormFieldKind,
) (*DocumentTool, document.FormJobRecord, document.FormFieldsFacts) {
	return newDocumentDialogueJobWithSource(t, count, lastKind,
		[]byte("%PDF-1.7\ngeneric semantic dialog fixture\n%%EOF\n"))
}

func newDocumentDialogueJobWithSource(
	t *testing.T,
	count int,
	lastKind document.FormFieldKind,
	source []byte,
) (*DocumentTool, document.FormJobRecord, document.FormFieldsFacts) {
	t.Helper()
	jobs, _ := newWorkflowFormStore(t)
	store := media.NewFileMediaStore()
	t.Cleanup(store.Stop)
	path := filepath.Join(t.TempDir(), "form.pdf")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{ContentType: "application/pdf"}, "dialog")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindOwner(ref, documentToolTestOwner(t)); err != nil {
		t.Fatal(err)
	}
	schema := workflowTestSchema(source)
	schema.Fields = nil
	for index := range count {
		field := sha256.Sum256([]byte(fmt.Sprintf("dialog-field-%d", index)))
		widget := sha256.Sum256([]byte(fmt.Sprintf("dialog-widget-%d", index)))
		schema.Fields = append(schema.Fields, document.FormField{
			ID: fmt.Sprintf("field_%x", field), Name: fmt.Sprintf("Party fact %d", index), Kind: document.FormFieldText,
			Widgets: []document.FormFieldWidget{{ID: fmt.Sprintf("widget_%x", widget), Page: index/25 + 1, Ordinal: 1}},
		})
	}
	schema.Fields[count-1].Kind = lastKind
	slices.SortFunc(
		schema.Fields,
		func(left, right document.FormField) int { return strings.Compare(left.ID, right.ID) },
	)
	tool := NewDocumentTool(WithDocumentFormJobStore(jobs), WithDocumentFormAudit(document.FormAuditPolicy{
		PrimaryModel: "document-deliberative", PrimaryIdentity: "resolved:document-deliberative",
	}, &workflowTestAuditor{}))
	tool.SetMediaStore(store)
	tool.formSchema = workflowSchemaResolver(schema)
	result := tool.Execute(
		workflowToolContext(t, "dialog-start", "dialog-start-call", []string{ref}),
		map[string]any{
			"action": "form", "form_action": "start", "source": ref,
			"field_schema_digest": workflowFieldDiscoveryDigest(t, schema),
		},
	)
	if result.IsError {
		t.Fatalf("prepare dialog job: %#v", result)
	}
	view := decodeWorkflowResult(t, result.ForLLM)
	ctx := workflowToolContext(t, "dialog-read", "dialog-read-call", nil)
	job, err := jobs.Get(ctx, view.Job.JobID, mustDocumentFormOwner(t, ctx))
	if err != nil {
		t.Fatal(err)
	}
	return tool, job, schema
}

func mustDocumentFormOwner(t *testing.T, ctx context.Context) document.FormJobOwner {
	t.Helper()
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}
