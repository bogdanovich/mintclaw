package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type workflowTestAuditor struct {
	proposal document.FormAuditProposal
	values   []string
	calls    []string
}

func (auditor *workflowTestAuditor) AuditForm(
	_ context.Context,
	model string,
	view document.FormAuditView,
) (document.FormAuditProposal, error) {
	auditor.calls = append(auditor.calls, model)
	for _, field := range view.Fields {
		auditor.values = append(auditor.values, field.Value.Text)
	}
	return auditor.proposal, nil
}

func TestDocumentFormWorkflowSurvivesRestartAndProducesRedactedReview(t *testing.T) {
	formStore, options := newWorkflowFormStore(t)
	mediaIndex := filepath.Join(t.TempDir(), "media", "index.json")
	mediaStore, err := media.NewFileMediaStoreWithPersistentIndex(
		mediaIndex,
		media.MediaCleanerConfig{MaxAge: time.Nanosecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mediaStore.Stop)
	owner := documentToolTestOwner(t)
	sourceBytes := []byte("%PDF-1.7\nprotected form source\n%%EOF\n")
	sourcePath := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	sourceRef, err := mediaStore.Store(sourcePath, media.MediaMeta{
		Filename: "source.pdf", ContentType: "",
		Source: "test", CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "inbound-form")
	if err != nil {
		t.Fatal(err)
	}
	if err = mediaStore.BindOwner(sourceRef, owner); err != nil {
		t.Fatal(err)
	}
	schema := workflowTestSchema(sourceBytes)
	auditor := &workflowTestAuditor{proposal: document.FormAuditProposal{Decision: document.FormAuditPass}}
	policy := document.FormAuditPolicy{
		PrimaryModel: "document-deliberative", PrimaryIdentity: "resolved:document-deliberative",
	}
	tool := NewDocumentTool(
		WithDocumentFormJobStore(formStore),
		WithDocumentFormAudit(policy, auditor),
	)
	tool.SetMediaStore(mediaStore)
	tool.formSchema = workflowSchemaResolver(schema)
	discovered := tool.Execute(
		workflowToolContext(t, "execution-discover", "call-discover", []string{sourceRef}),
		map[string]any{"action": "form", "form_action": "discover", "source": sourceRef},
	)
	discoveryProjection := decodeWorkflowResult(t, discovered.ForLLM)
	if discovered.IsError || !discovered.Control.PreserveToolVisibility ||
		discoveryProjection.SourceRef != sourceRef ||
		discoveryProjection.FieldSchemaDigest != workflowFieldDiscoveryDigest(t, schema) ||
		discoveryProjection.Mapping == nil || len(discoveryProjection.Mapping.CandidateFields) != 1 {
		t.Fatalf("discover projection = %#v result=%#v", discoveryProjection, discovered)
	}

	missingDiscovery := tool.Execute(
		workflowToolContext(t, "execution-start-no-discovery", "call-start-no-discovery", []string{sourceRef}),
		map[string]any{"action": "form", "form_action": "start", "source": sourceRef},
	)
	if !missingDiscovery.IsError || missingDiscovery.Control.Suspension != nil ||
		!strings.Contains(missingDiscovery.ForLLM, `"code":"field_discovery_required"`) {
		t.Fatalf("start without field discovery = %#v", missingDiscovery)
	}
	emptyWindow := tool.Execute(
		workflowToolContext(t, "execution-start-empty-window", "call-start-empty-window", []string{sourceRef}),
		map[string]any{
			"action": "form", "form_action": "start", "source": sourceRef,
			"field_schema_digest": discoveryProjection.FieldSchemaDigest, "pages": []int{2},
		},
	)
	if !emptyWindow.IsError || emptyWindow.Control.Suspension != nil ||
		!strings.Contains(emptyWindow.ForLLM, `"code":"invalid_input"`) {
		t.Fatalf("empty starting field window = %#v", emptyWindow)
	}
	wrongDigest := strings.Repeat("f", sha256.Size*2)
	if wrongDigest == workflowFieldDiscoveryDigest(t, schema) {
		wrongDigest = strings.Repeat("e", sha256.Size*2)
	}
	staleStart := tool.Execute(
		workflowToolContext(t, "execution-start-stale", "call-start-stale", []string{sourceRef}),
		map[string]any{
			"action": "form", "form_action": "start", "source": sourceRef,
			"field_schema_digest": wrongDigest,
		},
	)
	if !staleStart.IsError || staleStart.Control.Suspension != nil ||
		!strings.Contains(staleStart.ForLLM, `"code":"form_job_stale"`) {
		t.Fatalf("stale field schema start = %#v", staleStart)
	}
	otherSourceSchema := schema
	otherSourceSchema.SourceSHA256 = workflowTestSchema(
		[]byte("%PDF-1.7\nother source, same schema\n%%EOF\n"),
	).SourceSHA256
	schemaDigest, err := document.FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	otherSchemaDigest, err := document.FormFieldSchemaDigest(otherSourceSchema)
	if err != nil {
		t.Fatal(err)
	}
	if schemaDigest != otherSchemaDigest {
		t.Fatal("cross-source regression fixtures do not share the same field schema")
	}
	crossSourceStart := tool.Execute(
		workflowToolContext(t, "execution-start-cross-source", "call-start-cross-source", []string{sourceRef}),
		map[string]any{
			"action": "form", "form_action": "start", "source": sourceRef,
			"field_schema_digest": workflowFieldDiscoveryDigest(t, otherSourceSchema),
		},
	)
	if !crossSourceStart.IsError || crossSourceStart.Control.Suspension != nil ||
		!strings.Contains(crossSourceStart.ForLLM, `"code":"form_job_stale"`) {
		t.Fatalf("cross-source field discovery reuse = %#v", crossSourceStart)
	}
	startCtx := workflowToolContext(t, "execution-start", "call-start", []string{sourceRef})
	started := tool.Execute(startCtx, map[string]any{
		"action": "form", "form_action": "start", "source": sourceRef,
		"field_schema_digest": discoveryProjection.FieldSchemaDigest,
	})
	if started.IsError || started.Control.Suspension != nil || !started.Control.PreserveToolVisibility {
		t.Fatalf("start result = %#v", started)
	}
	startProjection := decodeWorkflowResult(t, started.ForLLM)
	if startProjection.Job == nil || startProjection.Job.State != document.FormJobPrepared ||
		startProjection.Mapping == nil || len(startProjection.Mapping.CandidateFields) != 1 ||
		startProjection.Mapping.CandidateFields[0].FieldID != schema.Fields[0].ID ||
		startProjection.NextField != nil {
		t.Fatalf("start projection = %#v", startProjection)
	}
	missingPlan := tool.Execute(
		workflowToolContext(t, "execution-collect-missing-plan", "call-collect-missing-plan", nil),
		map[string]any{
			"action": "form", "form_action": "collect", "job_id": startProjection.Job.JobID,
			"field_id": schema.Fields[0].ID, "question": "What name should this PDF contain?",
		},
	)
	if !missingPlan.IsError || missingPlan.Control.Suspension != nil ||
		!strings.Contains(missingPlan.ForLLM, `"code":"agent_plan_required"`) {
		t.Fatalf("first collect without an agent plan = %#v", missingPlan)
	}
	formSummary := strings.Repeat("Описание формы. ", 30)
	collectionPlan := strings.Repeat("Соберу недостающие сведения. ", 25)
	questionText := strings.Repeat("Вопрос о текущем имени. ", 30) + "Какова ваша фамилия по документам?"
	collected := tool.Execute(
		workflowToolContext(t, "execution-collect", "call-collect", nil),
		map[string]any{
			"action": "form", "form_action": "collect", "job_id": startProjection.Job.JobID,
			"field_id": schema.Fields[0].ID, "question": questionText,
			"form_summary": formSummary, "collection_plan": collectionPlan, "interaction_language": "RU-ru",
		},
	)
	if collected.IsError || collected.Control.Suspension == nil ||
		collected.Control.Suspension.ProtectedAnswer == nil {
		t.Fatalf("collect result = %#v", collected)
	}
	if got := collected.Control.Suspension.ProtectedAnswer.Actions; !slices.Equal(
		got,
		[]interactions.ProtectedAnswerAction{
			interactions.ProtectedAnswerActionClarify,
		},
	) {
		t.Fatalf("first protected question actions = %#v", got)
	}
	collectedProjection := decodeWorkflowResult(t, collected.ForLLM)
	wantIntroduction := strings.TrimSpace(formSummary) + "\n\n" + strings.TrimSpace(collectionPlan)
	question := collected.Control.Suspension.Questions[0]
	if collectedProjection.NextField == nil || collectedProjection.NextField.FieldID != schema.Fields[0].ID ||
		question.Question != questionText || question.Introduction != wantIntroduction ||
		question.Header != "PDF-форма" || collected.Control.Suspension.PromptLanguage != "ru-ru" ||
		len(questionText) <= interactions.MaxQuestionLength {
		t.Fatalf("collect projection = %#v", collectedProjection)
	}
	time.Sleep(time.Millisecond)
	if removed := mediaStore.CleanExpired(); removed != 1 {
		t.Fatalf("media cleanup removed = %d, want only the unpinned inbound source", removed)
	}

	sink, err := document.NewFormProtectedAnswerSink(formStore)
	if err != nil {
		t.Fatal(err)
	}
	route := workflowInteractionRoute()
	privateValue := "MINTCLAW_PDF3_WORKFLOW_PRIVATE_6f81"
	receipt, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: *collected.Control.Suspension.ProtectedAnswer, Workspace: "workspace", Route: route,
		InteractionID: "interaction-workflow", IdempotencyKey: "message-workflow-1",
		Intent: interactions.ProtectedAnswerValue, Text: privateValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: *collected.Control.Suspension.ProtectedAnswer, Workspace: "workspace", Route: route,
		InteractionID: "interaction-workflow", Receipt: receipt,
	}); err != nil {
		t.Fatal(err)
	}
	sink.Close()
	mediaStore.Stop()

	reopened, err := document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	restartedMediaStore, err := media.NewFileMediaStoreWithPersistentIndex(
		mediaIndex,
		media.MediaCleanerConfig{MaxAge: time.Nanosecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restartedMediaStore.Stop)
	restartedAuditor := &workflowTestAuditor{proposal: document.FormAuditProposal{Decision: document.FormAuditPass}}
	restarted := NewDocumentTool(
		WithDocumentFormJobStore(reopened),
		WithDocumentFormAudit(policy, restartedAuditor),
	)
	restarted.SetMediaStore(restartedMediaStore)
	restarted.formSchema = workflowSchemaResolver(schema)
	time.Sleep(time.Millisecond)
	if removed := restartedMediaStore.CleanExpired(); removed != 0 {
		t.Fatalf("restart cleanup removed active workflow sources = %d", removed)
	}
	continued := restarted.Execute(
		workflowToolContext(t, "execution-continue", "call-continue", nil),
		map[string]any{
			"action": "form", "form_action": "continue",
			"answer_ref": receipt.Reference,
		},
	)
	if continued.IsError || continued.Control.Suspension != nil || !continued.Control.PreserveToolVisibility {
		t.Fatalf("continued result = %#v", continued)
	}
	continuedProjection := decodeWorkflowResult(t, continued.ForLLM)
	if continuedProjection.Job == nil || continuedProjection.Job.State != document.FormJobCollecting ||
		continuedProjection.Mapping == nil || !continuedProjection.Mapping.ReadyForReview ||
		continuedProjection.Review != nil || continuedProjection.NextField != nil ||
		len(restartedAuditor.calls) != 0 {
		t.Fatalf("continued projection = %#v, audit=%#v", continuedProjection, restartedAuditor)
	}
	correction := restarted.Execute(
		workflowToolContext(t, "execution-correct", "call-correct", nil),
		map[string]any{
			"action": "form", "form_action": "correct", "job_id": startProjection.Job.JobID,
			"field_id": schema.Fields[0].ID, "question": "What corrected name should this PDF contain?",
		},
	)
	if correction.IsError || correction.Control.Suspension == nil ||
		correction.Control.Suspension.ProtectedAnswer == nil ||
		!slices.Equal(
			correction.Control.Suspension.ProtectedAnswer.Actions,
			[]interactions.ProtectedAnswerAction{
				interactions.ProtectedAnswerActionClarify,
			},
		) {
		t.Fatalf("correction actions = %#v", correction)
	}
	restartedSink, err := document.NewFormProtectedAnswerSink(reopened)
	if err != nil {
		t.Fatal(err)
	}
	navigationReceipt, err := restartedSink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: *correction.Control.Suspension.ProtectedAnswer, Workspace: "workspace",
		Route: workflowInteractionRoute(), InteractionID: "interaction-workflow-clarify",
		IdempotencyKey: "message-workflow-clarify", Intent: interactions.ProtectedAnswerClarify,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = restartedSink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: *correction.Control.Suspension.ProtectedAnswer, Workspace: "workspace",
		Route: workflowInteractionRoute(), InteractionID: "interaction-workflow-clarify",
		Receipt: navigationReceipt,
	}); err != nil {
		t.Fatal(err)
	}
	navigationArguments, err := restarted.ProtectedAnswerContinuationArguments(navigationReceipt.Reference)
	if err != nil {
		t.Fatal(err)
	}
	clarified := restarted.Execute(
		workflowToolContext(t, "execution-clarify", "call-clarify", nil),
		navigationArguments,
	)
	clarifiedProjection := decodeWorkflowResult(t, clarified.ForLLM)
	if clarified.IsError || clarified.Control.Suspension == nil || clarifiedProjection.FormAction != "clarify" ||
		clarifiedProjection.NextField == nil || clarifiedProjection.NextField.FieldID != schema.Fields[0].ID ||
		!strings.Contains(clarified.Control.Suspension.Questions[0].Question, "Provide Legal name") ||
		!slices.Equal(
			clarified.Control.Suspension.ProtectedAnswer.Actions,
			[]interactions.ProtectedAnswerAction{interactions.ProtectedAnswerActionClarify},
		) {
		t.Fatalf("native clarification result = %#v projection=%#v", clarified, clarifiedProjection)
	}
	replayedNavigation := restarted.Execute(
		workflowToolContext(t, "execution-clarify", "call-clarify-replay", nil),
		navigationArguments,
	)
	if !replayedNavigation.IsError || !strings.Contains(replayedNavigation.ForLLM, "form_job_conflict") {
		t.Fatalf("cross-execution navigation replay = %#v", replayedNavigation)
	}
	reviewed := restarted.Execute(
		workflowToolContext(t, "execution-review", "call-review", nil),
		map[string]any{
			"action": "form", "form_action": "review", "job_id": startProjection.Job.JobID,
		},
	)
	reviewedProjection := decodeWorkflowResult(t, reviewed.ForLLM)
	if reviewed.IsError || !reviewed.Control.PreserveToolVisibility || reviewedProjection.Job == nil ||
		reviewedProjection.Job.State != document.FormJobReviewReady || reviewedProjection.Review == nil ||
		!reviewedProjection.Review.Ready || reviewedProjection.Review.ReviewDigest == "" ||
		reviewedProjection.Review.WritableFieldCount != 1 || len(reviewedProjection.Review.Fields) != 1 ||
		!slicesEqualStrings(restartedAuditor.calls, []string{"document-deliberative"}) ||
		!slicesEqualStrings(restartedAuditor.values, []string{privateValue}) {
		t.Fatalf("reviewed projection = %#v, audit=%#v", reviewedProjection, restartedAuditor)
	}
	if strings.Contains(continued.ForLLM, privateValue) {
		t.Fatal("protected form value leaked into tool result")
	}
	stateBytes, err := os.ReadFile(filepath.Join(options.StateRoot, "document_form_jobs", "form_jobs.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateBytes), privateValue) {
		t.Fatal("protected form value leaked into durable job snapshot")
	}

	status := restarted.Execute(
		workflowToolContext(t, "execution-status", "call-status", nil),
		map[string]any{
			"action": "form", "form_action": "status", "job_id": startProjection.Job.JobID,
		},
	)
	statusProjection := decodeWorkflowResult(t, status.ForLLM)
	if status.IsError || statusProjection.Review == nil || !statusProjection.Review.Ready ||
		statusProjection.Review.WritableFieldCount != 1 || len(statusProjection.Review.Fields) != 1 ||
		statusProjection.Review.ReviewDigest != reviewedProjection.Review.ReviewDigest {
		t.Fatalf("restart/compaction-independent status = %#v", status)
	}
	formOwner, err := documentFormOwner(workflowToolContext(t, "execution-owner", "call-owner", nil))
	if err != nil {
		t.Fatal(err)
	}
	retainedRef, err := reopened.SourceRef(t.Context(), startProjection.Job.JobID, formOwner)
	if err != nil {
		t.Fatal(err)
	}
	canceled := restarted.Execute(
		workflowToolContext(t, "execution-cancel", "call-cancel", nil),
		map[string]any{
			"action": "form", "form_action": "cancel", "job_id": startProjection.Job.JobID,
		},
	)
	if canceled.IsError {
		t.Fatalf("cancel result = %#v", canceled)
	}
	if _, err = restartedMediaStore.Resolve(retainedRef); err == nil {
		t.Fatal("canceled workflow retained its immutable source")
	}
	terminalStatus := restarted.Execute(
		workflowToolContext(t, "execution-terminal-status", "call-terminal-status", nil),
		map[string]any{
			"action": "form", "form_action": "status", "job_id": startProjection.Job.JobID,
		},
	)
	terminalProjection := decodeWorkflowResult(t, terminalStatus.ForLLM)
	if terminalStatus.IsError || terminalProjection.FormAction != "status" ||
		terminalProjection.Job == nil || terminalProjection.Job.State != document.FormJobCanceled ||
		terminalProjection.Review != nil || terminalProjection.Commit != nil {
		t.Fatalf("canceled terminal status = %#v projection=%#v", terminalStatus, terminalProjection)
	}
}

func TestDocumentFormNavigationRestoresTargetControlsAfterRestart(t *testing.T) {
	for _, intent := range []interactions.ProtectedAnswerIntent{
		interactions.ProtectedAnswerClarify, interactions.ProtectedAnswerBack,
	} {
		t.Run(string(intent), func(t *testing.T) {
			store, options := newWorkflowFormStore(t)
			mediaStore := media.NewFileMediaStore()
			t.Cleanup(mediaStore.Stop)
			data := []byte("%PDF-1.7\nnavigation fixture\n%%EOF\n")
			path := filepath.Join(t.TempDir(), "source.pdf")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			ref, err := mediaStore.Store(path, media.MediaMeta{}, "navigation-fixture")
			if err != nil {
				t.Fatal(err)
			}
			if err = mediaStore.BindOwner(ref, documentToolTestOwner(t)); err != nil {
				t.Fatal(err)
			}
			schema := workflowTestSchema(data)
			schema.Fields[0].Required = false
			next := schema.Fields[0]
			next.ID = "field_" + strings.Repeat("f", 64)
			next.Name = "Notes"
			next.Widgets = []document.FormFieldWidget{{
				ID: "widget_" + strings.Repeat("b", 64), Page: 1, Ordinal: 1,
			}}
			schema.Fields = append(schema.Fields, next)
			digest, err := document.FormFieldSchemaDigest(schema)
			if err != nil {
				t.Fatal(err)
			}
			backend, err := document.FormFieldsBackendRevision(schema)
			if err != nil {
				t.Fatal(err)
			}
			ctx := workflowToolContext(t, "navigation-create", "create", nil)
			owner, err := documentFormOwner(ctx)
			if err != nil {
				t.Fatal(err)
			}
			job, err := store.Create(ctx, document.FormJobCreateRequest{
				Owner: owner, StartIdempotencyKey: "navigation", SourceRef: ref,
				SourceDigest: schema.SourceSHA256, FieldSchemaDigest: digest, BackendRevision: backend,
				AuditPolicyRevision: "document-audit-v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			tool := NewDocumentTool(WithDocumentFormJobStore(store))
			tool.SetMediaStore(mediaStore)
			tool.formSchema = workflowSchemaResolver(schema)
			question := tool.Execute(ctx, map[string]any{
				"action": "form", "form_action": "collect", "job_id": job.JobID,
				"field_id": schema.Fields[0].ID, "question": "Какое имя указать?",
				"form_summary": "Тестовая форма.", "collection_plan": "Уточню данные и покажу сводку.",
				"interaction_language": "ru", "blank_actions": []any{"skip", "not_applicable"},
			})
			if question.IsError || question.Control.Suspension == nil {
				t.Fatalf("collect = %#v", question)
			}
			sink, err := document.NewFormProtectedAnswerSink(store)
			if err != nil {
				t.Fatal(err)
			}
			accept := func(action interactions.ProtectedAnswerIntent, identity string) interactions.ProtectedAnswerReceipt {
				t.Helper()
				binding := *question.Control.Suspension.ProtectedAnswer
				receipt, acceptErr := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
					Binding: binding, Workspace: "workspace", Route: workflowInteractionRoute(),
					InteractionID: identity, IdempotencyKey: identity, Intent: action, Text: "NAV_PRIVATE_VALUE",
				})
				if acceptErr != nil {
					t.Fatal(acceptErr)
				}
				if commitErr := sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
					Binding: binding, Workspace: "workspace", Route: workflowInteractionRoute(),
					InteractionID: identity, Receipt: receipt,
				}); commitErr != nil {
					t.Fatal(commitErr)
				}
				return receipt
			}
			if intent == interactions.ProtectedAnswerBack {
				value := accept(interactions.ProtectedAnswerValue, "value")
				continued := tool.Execute(ctx, map[string]any{
					"action": "form", "form_action": "continue", "answer_ref": value.Reference,
				})
				if continued.IsError {
					t.Fatalf("continue = %#v", continued)
				}
				question = tool.Execute(ctx, map[string]any{
					"action": "form", "form_action": "collect", "job_id": job.JobID,
					"field_id": next.ID, "question": "Any notes?", "interaction_language": "en",
				})
				if question.IsError || question.Control.Suspension == nil {
					t.Fatalf("second collect = %#v", question)
				}
			}
			before, err := store.Get(ctx, job.JobID, owner)
			if err != nil {
				t.Fatal(err)
			}
			receipt := accept(intent, "navigation")
			store.Close()
			reopened, err := document.OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.Close)
			restarted := NewDocumentTool(WithDocumentFormJobStore(reopened))
			restarted.SetMediaStore(mediaStore)
			restarted.formSchema = workflowSchemaResolver(schema)
			args, err := restarted.ProtectedAnswerContinuationArguments(receipt.Reference)
			if err != nil {
				t.Fatal(err)
			}
			result := restarted.Execute(workflowToolContext(t, "navigation-resume", "resume", nil), args)
			projection := decodeWorkflowResult(t, result.ForLLM)
			if result.IsError || result.Control.Suspension == nil || projection.NextField == nil ||
				projection.NextField.FieldID != schema.Fields[0].ID {
				t.Fatalf("navigation = %#v; projection=%#v", result, projection)
			}
			suspension := result.Control.Suspension
			if suspension.PromptLanguage != "ru" || suspension.Questions[0].Header != "PDF-форма" ||
				!slices.Equal(suspension.ProtectedAnswer.Actions, []interactions.ProtectedAnswerAction{
					interactions.ProtectedAnswerActionClarify, interactions.ProtectedAnswerActionSkip,
					interactions.ProtectedAnswerActionNotApplicable,
				}) {
				t.Fatalf("target controls lost = %#v", suspension)
			}
			after, err := reopened.Get(ctx, job.JobID, owner)
			if err != nil || after.Revision != before.Revision || !slices.Equal(after.Fields, before.Fields) {
				t.Fatalf("navigation changed values: before=%#v after=%#v err=%v", before, after, err)
			}
		})
	}
}

func TestDocumentFormWorkflowAllowsInitialCorrectionWithAgentPlan(t *testing.T) {
	formStore, _ := newWorkflowFormStore(t)
	mediaStore, err := media.NewFileMediaStoreWithPersistentIndex(
		filepath.Join(t.TempDir(), "media", "index.json"),
		media.MediaCleanerConfig{MaxAge: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mediaStore.Stop)
	owner := documentToolTestOwner(t)
	sourceBytes := []byte("%PDF-1.7\nform with an existing value\n%%EOF\n")
	sourcePath := filepath.Join(t.TempDir(), "source.pdf")
	if err = os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	sourceRef, err := mediaStore.Store(sourcePath, media.MediaMeta{
		Filename: "source.pdf", Source: "test", CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "inbound-form")
	if err != nil {
		t.Fatal(err)
	}
	if err = mediaStore.BindOwner(sourceRef, owner); err != nil {
		t.Fatal(err)
	}
	schema := workflowTestSchema(sourceBytes)
	schema.Fields[0].HasValue = true
	confirmedFieldID := schema.Fields[0].ID
	missingField := schema.Fields[0]
	missingFieldDigest := sha256.Sum256([]byte("workflow-missing-field"))
	missingWidgetDigest := sha256.Sum256([]byte("workflow-missing-widget"))
	missingField.ID = "field_" + hex.EncodeToString(missingFieldDigest[:])
	missingField.Name = "Optional note"
	missingField.Required = false
	missingField.HasValue = false
	missingField.Widgets = []document.FormFieldWidget{{
		ID: "widget_" + hex.EncodeToString(missingWidgetDigest[:]), Page: 1, Ordinal: 1,
	}}
	schema.Fields = append(schema.Fields, missingField)
	if schema.Fields[1].ID < schema.Fields[0].ID {
		schema.Fields[0], schema.Fields[1] = schema.Fields[1], schema.Fields[0]
	}
	tool := NewDocumentTool(
		WithDocumentFormJobStore(formStore),
		WithDocumentFormAudit(
			document.FormAuditPolicy{PrimaryModel: "document-deliberative", PrimaryIdentity: "test:model"},
			&workflowTestAuditor{proposal: document.FormAuditProposal{Decision: document.FormAuditPass}},
		),
	)
	tool.SetMediaStore(mediaStore)
	tool.formSchema = workflowSchemaResolver(schema)

	discovered := tool.Execute(
		workflowToolContext(t, "execution-correction-discover", "call-correction-discover", []string{sourceRef}),
		map[string]any{"action": "form", "form_action": "discover", "source": sourceRef},
	)
	discovery := decodeWorkflowResult(t, discovered.ForLLM)
	if discovered.IsError || discovery.Mapping == nil || discovery.Mapping.ConfirmedFieldCount != 1 ||
		discovery.Mapping.UnresolvedFieldCount != 1 {
		t.Fatalf("existing-value discovery = result:%#v projection:%#v", discovered, discovery)
	}
	started := tool.Execute(
		workflowToolContext(t, "execution-correction-start", "call-correction-start", []string{sourceRef}),
		map[string]any{
			"action": "form", "form_action": "start", "source": sourceRef,
			"field_schema_digest": discovery.FieldSchemaDigest,
		},
	)
	start := decodeWorkflowResult(t, started.ForLLM)
	if started.IsError || start.Job == nil || start.Mapping == nil || len(start.Mapping.CandidateFields) != 2 {
		t.Fatalf("existing-value start = result:%#v projection:%#v", started, start)
	}
	missingPlan := tool.Execute(
		workflowToolContext(t, "execution-correction-missing-plan", "call-correction-missing-plan", nil),
		map[string]any{
			"action": "form", "form_action": "correct", "job_id": start.Job.JobID,
			"field_id": confirmedFieldID, "question": "What should replace the current value?",
		},
	)
	if !missingPlan.IsError || missingPlan.Control.Suspension != nil ||
		!strings.Contains(missingPlan.ForLLM, `"code":"agent_plan_required"`) {
		t.Fatalf("initial correction without plan = %#v", missingPlan)
	}
	corrected := tool.Execute(
		workflowToolContext(t, "execution-correction", "call-correction", nil),
		map[string]any{
			"action": "form", "form_action": "correct", "job_id": start.Job.JobID,
			"field_id": confirmedFieldID, "question": "What should replace the current value?",
			"form_summary":    "This form already contains a value that the user asked to replace.",
			"collection_plan": "I will collect the replacement and show a review before writing.",
		},
	)
	if corrected.IsError || corrected.Control.Suspension == nil ||
		corrected.Control.Suspension.ProtectedAnswer == nil {
		t.Fatalf("initial correction = %#v", corrected)
	}
	wantIntroduction := "This form already contains a value that the user asked to replace.\n\n" +
		"I will collect the replacement and show a review before writing."
	question := corrected.Control.Suspension.Questions[0]
	if question.Question != "What should replace the current value?" || question.Introduction != wantIntroduction {
		t.Fatalf("initial correction question = %#v", question)
	}
	sink, err := document.NewFormProtectedAnswerSink(formStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sink.Close)
	route := workflowInteractionRoute()
	receipt, err := sink.Accept(t.Context(), interactions.ProtectedAnswerSinkRequest{
		Binding: *corrected.Control.Suspension.ProtectedAnswer, Workspace: "workspace", Route: route,
		InteractionID: "interaction-initial-correction", IdempotencyKey: "message-initial-correction",
		Intent: interactions.ProtectedAnswerValue, Text: "private replacement",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = sink.Commit(t.Context(), interactions.ProtectedAnswerCommitRequest{
		Binding: *corrected.Control.Suspension.ProtectedAnswer, Workspace: "workspace", Route: route,
		InteractionID: "interaction-initial-correction", Receipt: receipt,
	}); err != nil {
		t.Fatal(err)
	}
	continued := tool.Execute(
		workflowToolContext(t, "execution-correction-continue", "call-correction-continue", nil),
		map[string]any{
			"action": "form", "form_action": "continue", "answer_ref": receipt.Reference,
		},
	)
	continuedProjection := decodeWorkflowResult(t, continued.ForLLM)
	if continued.IsError || continuedProjection.Mapping == nil ||
		len(continuedProjection.Mapping.CandidateFields) != 1 ||
		continuedProjection.Mapping.CandidateFields[0].FieldID != missingField.ID ||
		strings.Contains(continued.ForLLM, "private replacement") {
		t.Fatalf("continued correction projection = result:%#v projection:%#v", continued, continuedProjection)
	}
	followup, err := tool.ProtectedAnswerContinuationFollowup(continued)
	if err != nil || followup == nil {
		t.Fatalf("continued correction follow-up = %#v, error = %v", followup, err)
	}
	if err = followup.ValidateArguments(map[string]any{
		"action": "form", "form_action": "correct", "job_id": start.Job.JobID,
		"field_id": confirmedFieldID, "question": "Repeat the same correction?",
	}); err == nil {
		t.Fatal("continued correction allowed the just-completed field to be repeated")
	}
	if err = followup.ValidateArguments(map[string]any{
		"action": "form", "form_action": "collect", "job_id": start.Job.JobID,
		"field_id": missingField.ID, "question": "What optional note should I use?",
	}); err != nil {
		t.Fatalf("continued correction rejected the remaining unresolved field: %v", err)
	}
}

func TestPrepareDocumentFormSourceTreatsTransportContentTypeAsAdvisory(t *testing.T) {
	for _, contentType := range []string{"", "application/pdf", "application/octet-stream", "image/jpeg"} {
		contentType := contentType
		t.Run(contentType, func(t *testing.T) {
			store, err := media.NewFileMediaStoreWithPersistentIndex(
				filepath.Join(t.TempDir(), "media", "index.json"),
				media.MediaCleanerConfig{},
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Stop)
			owner := documentToolTestOwner(t)
			sourceBytes := []byte("%PDF-1.7\nmetadata-independent form source\n%%EOF\n")
			sourcePath := filepath.Join(t.TempDir(), "source.pdf")
			if err = os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			ref, storeErr := store.Store(sourcePath, media.MediaMeta{
				Filename: "source.pdf", ContentType: contentType, Source: "test",
				CleanupPolicy: media.CleanupPolicyForgetOnly,
			}, "inbound-form")
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			if err = store.BindOwner(ref, owner); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(sourceBytes)
			prepared, prepareErr := prepareDocumentFormSource(
				store,
				owner,
				ref,
				"metadata-independent-"+contentType,
				hex.EncodeToString(digest[:]),
			)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			t.Cleanup(func() { _ = os.Remove(prepared.path) })
			retained, readErr := os.ReadFile(prepared.path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(retained) != string(sourceBytes) {
				t.Fatal("retained source bytes differ from inspected source")
			}
		})
	}
}

func TestPrepareDocumentFormSourceFailsClosedOnAuthorityAndDigestMismatch(t *testing.T) {
	store, err := media.NewFileMediaStoreWithPersistentIndex(
		filepath.Join(t.TempDir(), "media", "index.json"),
		media.MediaCleanerConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Stop)
	owner := documentToolTestOwner(t)
	sourceBytes := []byte("%PDF-1.7\nprotected form source\n%%EOF\n")
	sourcePath := filepath.Join(t.TempDir(), "source.pdf")
	if err = os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(sourcePath, media.MediaMeta{
		Filename: "source.pdf", ContentType: "", Source: "test",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "inbound-form")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}

	wrongOwner, err := media.NewMediaOwner(
		"workspace", "agent", "different-actor", "route", "session", "telegram", "chat", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	actualDigest := sha256.Sum256(sourceBytes)
	_, err = prepareDocumentFormSource(
		store,
		wrongOwner,
		ref,
		"wrong-owner",
		hex.EncodeToString(actualDigest[:]),
	)
	assertDocumentFormRetentionPhase(t, err, "source_open")

	_, err = prepareDocumentFormSource(
		store,
		owner,
		ref,
		"wrong-digest",
		strings.Repeat("0", sha256.Size*2),
	)
	assertDocumentFormRetentionPhase(t, err, "inspected_identity")

	storedPath, err := store.Resolve(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(storedPath, []byte("%PDF-1.7\ntampered\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = prepareDocumentFormSource(
		store,
		owner,
		ref,
		"changed-bytes",
		hex.EncodeToString(actualDigest[:]),
	)
	assertDocumentFormRetentionPhase(t, err, "source_open")
}

func TestDocumentFormWorkflowKeepsStableFailureForInspectedDigestMismatch(t *testing.T) {
	formStore, _ := newWorkflowFormStore(t)
	mediaStore, err := media.NewFileMediaStoreWithPersistentIndex(
		filepath.Join(t.TempDir(), "media", "index.json"),
		media.MediaCleanerConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mediaStore.Stop)
	owner := documentToolTestOwner(t)
	sourceBytes := []byte("%PDF-1.7\nprotected form source\n%%EOF\n")
	sourcePath := filepath.Join(t.TempDir(), "source.pdf")
	if err = os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := mediaStore.Store(sourcePath, media.MediaMeta{
		Filename: "source.pdf", ContentType: "", Source: "test",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "inbound-form")
	if err != nil {
		t.Fatal(err)
	}
	if err = mediaStore.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	schema := workflowTestSchema(sourceBytes)
	schema.SourceSHA256 = strings.Repeat("0", sha256.Size*2)
	auditor := &workflowTestAuditor{proposal: document.FormAuditProposal{Decision: document.FormAuditPass}}
	tool := NewDocumentTool(
		WithDocumentFormJobStore(formStore),
		WithDocumentFormAudit(document.FormAuditPolicy{
			PrimaryModel: "document-deliberative", PrimaryIdentity: "resolved:document-deliberative",
		}, auditor),
	)
	tool.SetMediaStore(mediaStore)
	tool.formSchema = workflowSchemaResolver(schema)

	result := tool.Execute(
		workflowToolContext(t, "execution-digest-mismatch", "call-digest-mismatch", []string{ref}),
		map[string]any{
			"action": "form", "form_action": "start", "source": ref,
			"field_schema_digest": workflowFieldDiscoveryDigest(t, schema),
		},
	)
	projection := decodeWorkflowResult(t, result.ForLLM)
	if !result.IsError || result.Control.Suspension != nil || projection.Failure == nil {
		t.Fatalf("start result = %#v; projection = %#v", result, projection)
	}
	if projection.Failure.Code != "protected_store_unavailable" ||
		projection.Failure.Message != "the immutable form source could not be retained" {
		t.Fatalf("failure = %#v", projection.Failure)
	}
}

func assertDocumentFormRetentionPhase(t *testing.T, err error, expected string) {
	t.Helper()
	var failure *documentFormSourceRetentionError
	if !errors.As(err, &failure) {
		t.Fatalf("retention error = %v, want typed safe failure", err)
	}
	if failure.phase != expected {
		t.Fatalf("retention phase = %q, want %q", failure.phase, expected)
	}
}

func TestDocumentFormWorkflowFailsClosedWithoutAuditRole(t *testing.T) {
	formStore, _ := newWorkflowFormStore(t)
	mediaStore := media.NewFileMediaStore()
	tool := NewDocumentTool(WithDocumentFormJobStore(formStore))
	tool.SetMediaStore(mediaStore)
	result := tool.Execute(
		workflowToolContext(t, "execution", "call", []string{"media://missing"}),
		map[string]any{
			"action": "form", "form_action": "start", "source": "media://missing",
			"field_schema_digest": strings.Repeat("a", sha256.Size*2),
		},
	)
	if !result.IsError || !strings.Contains(result.ForLLM, `"code":"audit_unavailable"`) ||
		strings.Contains(result.ForLLM, "media://missing") {
		t.Fatalf("missing audit result = %#v", result)
	}
}

func TestDocumentFormStatusRecoversTerminalTransitionDuringSchemaLoad(t *testing.T) {
	formStore, _ := newWorkflowFormStore(t)
	ctx := workflowToolContext(t, "execution-terminal-race", "call-terminal-race", nil)
	owner, err := documentFormOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	created, err := formStore.Create(t.Context(), document.FormJobCreateRequest{
		Owner: owner, StartIdempotencyKey: "terminal-race-start", SourceRef: "media://terminal-race-source",
		SourceDigest: strings.Repeat("a", 64), FieldSchemaDigest: strings.Repeat("b", 64),
		BackendRevision: "pdfcpu-v0.15.0-mintclaw-write-v1", AuditPolicyRevision: "document-audit-v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	tool := NewDocumentTool(WithDocumentFormJobStore(formStore))
	tool.SetMediaStore(media.NewFileMediaStore())
	tool.formSchema = func(
		context.Context,
		ownedDocumentMediaStore,
		string,
		media.MediaOwner,
	) (document.FormFieldsFacts, error) {
		if _, cancelErr := formStore.Cancel(
			t.Context(),
			created.JobID,
			created.Revision,
			owner,
		); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		return document.FormFieldsFacts{}, errors.New("retained source disappeared during terminal transition")
	}

	status := tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "status", "job_id": created.JobID,
	})
	projection := decodeWorkflowResult(t, status.ForLLM)
	if status.IsError || projection.FormAction != "status" || projection.Job == nil ||
		projection.Job.State != document.FormJobCanceled || projection.Review != nil || projection.Commit != nil {
		t.Fatalf("terminal transition status = %#v projection=%#v", status, projection)
	}
}

func TestDocumentFormWorkflowArgumentsAreCompactAndStrict(t *testing.T) {
	clarifyReference := "form_navigation.clarify.form_job_a." + strings.Repeat("a", sha256.Size*2) +
		".1." + strings.Repeat("b", 32) + "." + strings.Repeat("c", sha256.Size*2)
	backReference := "form_navigation.back.form_job_a." + strings.Repeat("a", sha256.Size*2) +
		".1." + strings.Repeat("b", 32) + "." + strings.Repeat("c", sha256.Size*2)
	valid := []map[string]any{
		{"action": "form", "form_action": "discover", "source": "media://source"},
		{
			"action": "form", "form_action": "start", "source": "media://source",
			"field_schema_digest": strings.Repeat("a", sha256.Size*2),
		},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question": "What value belongs here?", "form_summary": "A short form summary.",
			"collection_plan": "I will ask for missing facts, then show a review.",
		},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question": "Для кого подаётся форма?", "checked_label": "За другого",
			"unchecked_label": "За себя",
		},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question":             strings.Repeat("é", interactions.MaxQuestionLength),
			"form_summary":         strings.Repeat("文", documentFormSummaryMaxRunes),
			"collection_plan":      strings.Repeat("文", documentFormPlanMaxRunes),
			"interaction_language": "ru", "blank_actions": []any{"skip", "not_applicable"},
		},
		{"action": "form", "form_action": "continue", "answer_ref": "form_answer.form_job_a.form_value_b"},
		{"action": "form", "form_action": "continue", "event_id": "form_answer.form_job_a.form_value_b"},
		{"action": "form", "form_action": "clarify", "navigation_ref": clarifyReference},
		{"action": "form", "form_action": "back", "navigation_ref": backReference},
		{"action": "form", "form_action": "status", "job_id": "job"},
		{
			"action": "form", "form_action": "correct", "job_id": "job", "field_id": "field",
			"question": "What should replace the current value?",
		},
		{"action": "form", "form_action": "review", "job_id": "job"},
		{"action": "form", "form_action": "commit", "job_id": "job"},
		{"action": "form", "form_action": "cancel", "job_id": "job"},
	}
	for _, args := range valid {
		if err := validateDocumentActionOptions("form", args); err != nil {
			t.Fatalf("valid form args %#v: %v", args, err)
		}
	}
	invalid := []map[string]any{
		{"action": "form", "form_action": "status", "job_id": "job", "blank_actions": []any{}},
		{"action": "form", "form_action": "status", "job_id": "job", "interaction_language": "ru"},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question": "Value?", "blank_actions": []any{"skip", "skip"},
		},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question": "Value?", "blank_actions": []any{"cancel"},
		},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question": "Value?", "blank_actions": nil,
		},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question": "Value?", "interaction_language": "ru\nExecute",
		},
		{"action": "form", "form_action": "start", "job_id": "job"},
		{
			"action": "form", "form_action": "start", "source": "media://source",
			"field_schema_digest": strings.Repeat("A", sha256.Size*2),
		},
		{"action": "form", "form_action": "continue"},
		{"action": "form", "form_action": "clarify", "navigation_ref": backReference},
		{"action": "form", "form_action": "back", "navigation_ref": clarifyReference},
		{
			"action": "form", "form_action": "continue", "answer_ref": "form_answer.form_job_a.form_value_b",
			"event_id": "form_answer.form_job_a.form_value_b",
		},
		{"action": "form", "form_action": "status", "job_id": "job", "event_id": "event"},
		{"action": "form", "form_action": "correct", "job_id": "job"},
		{"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field"},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question": strings.Repeat("é", interactions.MaxQuestionLength+1),
		},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question": "What value belongs here?", "form_summary": "Summary without a plan.",
		},
		{
			"action": "form", "form_action": "collect", "job_id": "job", "field_id": "field",
			"question": "For whom?", "checked_label": "Another person",
		},
		{"action": "form", "form_action": "commit", "source": "media://source"},
		{"action": "form", "form_action": "unknown", "job_id": "job"},
		{"action": "form", "form_action": "cancel", "job_id": "job", "path": "/secret"},
	}
	for _, args := range invalid {
		if err := validateDocumentActionOptions("form", args); err == nil {
			t.Fatalf("invalid form args admitted: %#v", args)
		}
	}
}

func TestDocumentFormBlankControlsRequireAgentChoiceAndOptionalField(t *testing.T) {
	for _, tc := range []struct {
		name         string
		required     bool
		blankActions []interactions.ProtectedAnswerAction
		wantError    bool
	}{
		{name: "optional PDF flag alone", required: false},
		{name: "explicit skip", blankActions: []interactions.ProtectedAnswerAction{interactions.ProtectedAnswerActionSkip}},
		{name: "explicit not applicable", blankActions: []interactions.ProtectedAnswerAction{interactions.ProtectedAnswerActionNotApplicable}},
		{name: "required cannot skip", required: true, blankActions: []interactions.ProtectedAnswerAction{interactions.ProtectedAnswerActionSkip}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newWorkflowFormStore(t)
			ctx := workflowToolContext(t, "blank-control-execution", "blank-control-call", nil)
			owner, err := documentFormOwner(ctx)
			if err != nil {
				t.Fatal(err)
			}
			schema := workflowTestSchema([]byte("source"))
			schema.Fields[0].Required = tc.required
			digest, err := document.FormFieldSchemaDigest(schema)
			if err != nil {
				t.Fatal(err)
			}
			backend, err := document.FormFieldsBackendRevision(schema)
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Create(ctx, document.FormJobCreateRequest{
				Owner: owner, StartIdempotencyKey: "blank-control-start", SourceRef: "media://source",
				SourceDigest: schema.SourceSHA256, FieldSchemaDigest: digest, BackendRevision: backend,
				AuditPolicyRevision: "document-audit-v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			tool := NewDocumentTool(WithDocumentFormJobStore(store))
			result := tool.formQuestionResult(
				ctx,
				owner,
				schema,
				record,
				schema.Fields[0].ID,
				"collect",
				documentFormQuestionPresentation{
					question:     "Какое значение указать?",
					blankActions: tc.blankActions,
					language:     "ru",
				},
			)
			if result.IsError != tc.wantError {
				t.Fatalf("question result = %#v", result)
			}
			if tc.wantError {
				if result.Control.Suspension != nil {
					t.Fatal("required field opened a blank-capable question")
				}
				return
			}
			want := append(
				[]interactions.ProtectedAnswerAction{interactions.ProtectedAnswerActionClarify},
				tc.blankActions...)
			if result.Control.Suspension == nil ||
				!slices.Equal(result.Control.Suspension.ProtectedAnswer.Actions, want) {
				t.Fatalf("question actions = %#v", result)
			}
		})
	}
}

func TestDocumentFormMappingProjectionBoundsLargeForms(t *testing.T) {
	schema := document.FormFieldsFacts{Fields: make([]document.FormField, 0, 250)}
	summary := document.FormJobMappingSummary{
		Revision: 7, WritableFieldCount: 250,
		Unresolved: make([]document.FormFieldMappingBlocker, 0, 250),
	}
	for index := 249; index >= 0; index-- {
		fieldID := fmt.Sprintf("field_%03d", index)
		schema.Fields = append(schema.Fields, document.FormField{
			ID: fieldID, Name: fmt.Sprintf("Form field %03d", index), Kind: document.FormFieldText,
			Widgets: []document.FormFieldWidget{{
				ID: fmt.Sprintf("widget_%03d", index), Page: index/25 + 1, Ordinal: 1,
			}},
		})
		summary.Unresolved = append(summary.Unresolved, document.FormFieldMappingBlocker{
			FieldID: fieldID, Code: "field_unresolved",
		})
	}

	projection := documentFormMappingProjection(summary, schema)
	if projection.UnresolvedFieldCount != 250 || projection.WritableFieldCount != 250 ||
		len(projection.CandidateFields) != documentFormCandidateLimit {
		t.Fatalf("large-form projection = %#v", projection)
	}
	for index, candidate := range projection.CandidateFields {
		wantID := fmt.Sprintf("field_%03d", 24-index)
		if candidate.FieldID != wantID || candidate.Page != 1 {
			t.Fatalf("candidate %d = %#v, want %s on page 1", index, candidate, wantID)
		}
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 8*1024 || strings.Contains(string(encoded), "field_016") ||
		strings.Contains(string(encoded), "field_249") {
		t.Fatalf("large-form projection is not bounded (%d bytes): %s", len(encoded), encoded)
	}
}

func TestDocumentFormMappingProjectionRepresentsDistinctFieldKinds(t *testing.T) {
	schema := document.FormFieldsFacts{Fields: make([]document.FormField, 0, 20)}
	summary := document.FormJobMappingSummary{
		WritableFieldCount: 20,
		Unresolved:         make([]document.FormFieldMappingBlocker, 0, 20),
	}
	for index := range 20 {
		kind := document.FormFieldText
		switch index {
		case 12:
			kind = document.FormFieldDate
		case 17:
			kind = document.FormFieldCheckbox
		}
		fieldID := fmt.Sprintf("field_%02d", index)
		schema.Fields = append(schema.Fields, document.FormField{
			ID: fieldID, Name: fieldID, Kind: kind,
			Widgets: []document.FormFieldWidget{{ID: "widget_" + fieldID, Page: index/5 + 1, Ordinal: 1}},
		})
		summary.Unresolved = append(summary.Unresolved, document.FormFieldMappingBlocker{
			FieldID: fieldID, Code: "field_unresolved",
		})
	}

	projection := documentFormMappingProjection(summary, schema)
	if len(projection.CandidateFields) != documentFormCandidateLimit {
		t.Fatalf("candidate count = %d", len(projection.CandidateFields))
	}
	wantKinds := map[document.FormFieldKind]bool{
		document.FormFieldText: false, document.FormFieldDate: false, document.FormFieldCheckbox: false,
	}
	lastPage := 0
	for _, candidate := range projection.CandidateFields {
		if candidate.Page < lastPage {
			t.Fatalf("candidate fields are not page ordered: %#v", projection.CandidateFields)
		}
		lastPage = candidate.Page
		if _, ok := wantKinds[candidate.Kind]; ok {
			wantKinds[candidate.Kind] = true
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Fatalf("candidate window omitted %s: %#v", kind, projection.CandidateFields)
		}
	}
}

func TestDocumentFormMappingProjectionUsesConfirmedFieldsToCompleteCandidateWindow(t *testing.T) {
	schema := document.FormFieldsFacts{Fields: make([]document.FormField, 0, 8)}
	summary := document.FormJobMappingSummary{
		WritableFieldCount: 8,
		Unresolved: []document.FormFieldMappingBlocker{
			{FieldID: "field_1", Code: "field_unresolved"},
			{FieldID: "field_6", Code: "field_unresolved"},
		},
		ConfirmedFieldIDs: []string{"field_0", "field_2", "field_3", "field_4", "field_5", "field_7"},
	}
	kinds := []document.FormFieldKind{
		document.FormFieldText,
		document.FormFieldText,
		document.FormFieldCheckbox,
		document.FormFieldRadio,
		document.FormFieldCombo,
		document.FormFieldList,
		document.FormFieldDate,
		document.FormFieldText,
	}
	for index, kind := range kinds {
		fieldID := fmt.Sprintf("field_%d", index)
		schema.Fields = append(schema.Fields, document.FormField{
			ID: fieldID, Name: fieldID, Kind: kind,
			Widgets: []document.FormFieldWidget{{ID: "widget_" + fieldID, Page: index/4 + 1, Ordinal: 1}},
		})
	}

	projection := documentFormMappingProjection(summary, schema)
	if len(projection.CandidateFields) != 8 {
		t.Fatalf("candidate count = %d, want 8", len(projection.CandidateFields))
	}
	if projection.CandidateFields[0].FieldID != "field_1" || projection.CandidateFields[1].FieldID != "field_6" {
		t.Fatalf("unresolved fields are not first: %#v", projection.CandidateFields)
	}
	seenKinds := make(map[document.FormFieldKind]bool)
	for _, candidate := range projection.CandidateFields {
		seenKinds[candidate.Kind] = true
		wantBlocker := ""
		if candidate.FieldID == "field_1" || candidate.FieldID == "field_6" {
			wantBlocker = "field_unresolved"
		}
		if candidate.Blocker != wantBlocker {
			t.Fatalf("candidate %s blocker = %q, want %q", candidate.FieldID, candidate.Blocker, wantBlocker)
		}
	}
	for _, kind := range []document.FormFieldKind{
		document.FormFieldText,
		document.FormFieldDate,
		document.FormFieldCheckbox,
		document.FormFieldRadio,
		document.FormFieldCombo,
		document.FormFieldList,
	} {
		if !seenKinds[kind] {
			t.Fatalf("candidate window omitted %s: %#v", kind, projection.CandidateFields)
		}
	}
	afterResolvedAnswer := documentFormMappingProjectionExcludingConfirmed(summary, schema, "field_0")
	if len(afterResolvedAnswer.CandidateFields) != 7 {
		t.Fatalf("post-answer candidate count = %d, want 7", len(afterResolvedAnswer.CandidateFields))
	}
	for _, candidate := range afterResolvedAnswer.CandidateFields {
		if candidate.FieldID == "field_0" {
			t.Fatalf("post-answer projection repeated completed field: %#v", afterResolvedAnswer.CandidateFields)
		}
	}
	afterInvalidAnswer := documentFormMappingProjectionExcludingConfirmed(summary, schema, "field_1")
	if len(afterInvalidAnswer.CandidateFields) != 8 || afterInvalidAnswer.CandidateFields[0].FieldID != "field_1" {
		t.Fatalf("unresolved answered field was incorrectly excluded: %#v", afterInvalidAnswer.CandidateFields)
	}
}

func TestDocumentFormReviewProjectionBoundsLargeForms(t *testing.T) {
	review := document.FormReview{
		SchemaVersion:       document.FormReviewSchemaVersion,
		JobID:               "form_job_large",
		State:               document.FormJobReviewReady,
		Revision:            9,
		ReviewRevision:      9,
		FieldSchemaDigest:   strings.Repeat("a", sha256.Size*2),
		AuditPolicyRevision: "document-audit-v1",
		ReviewDigest:        strings.Repeat("b", sha256.Size*2),
		RequestedAction:     "fill_and_deliver_verified_pdf",
		Fields:              make([]document.FormReviewField, 0, 250),
		Blockers: []document.FormJobReviewBlocker{
			{FieldID: "field_249", Code: "confirmation_required"},
		},
	}
	for index := range 250 {
		fieldID := fmt.Sprintf("field_%03d", index)
		kind := document.FormFieldText
		if index == 248 {
			kind = document.FormFieldDate
		}
		field := document.FormReviewField{
			FieldID: fieldID, Label: fieldID, Kind: kind, State: document.FormValueSupplied,
			Source: document.FormValueSourceUser, Validation: document.FormValueValidationValid,
			Summary: "provided",
		}
		switch index {
		case 245:
			field.State = ""
			field.Source = document.FormValueSourceDocument
			field.Validation = ""
			field.Summary = "existing"
		case 246:
			field.State = document.FormValueBlanked
			field.Summary = "blank"
		case 247:
			field.State = ""
			field.Source = ""
			field.Validation = ""
			field.Summary = "unresolved"
		case 248:
			field.State = document.FormValueInvalid
			field.Validation = document.FormValueValidationInvalid
		}
		review.Fields = append(review.Fields, field)
	}

	projection := documentFormReviewProjection(review)
	if projection.WritableFieldCount != 250 || projection.ProvidedFieldCount != 247 ||
		projection.BlankFieldCount != 1 || projection.ExistingFieldCount != 1 ||
		projection.UnresolvedFieldCount != 1 || projection.BlockerCount != 1 ||
		len(projection.Fields) != documentFormCandidateLimit || !projection.FieldsTruncated {
		t.Fatalf("large-form review projection = %#v", projection)
	}
	wantFirstIDs := []string{"field_249", "field_247", "field_248"}
	for index, wantID := range wantFirstIDs {
		if projection.Fields[index].FieldID != wantID {
			t.Fatalf("review field %d = %#v, want %s", index, projection.Fields[index], wantID)
		}
	}
	if len(projection.Blockers) != 1 || projection.Blockers[0].FieldID != projection.Fields[0].FieldID ||
		projection.Blockers[0].Code != "confirmation_required" {
		t.Fatalf("blocked review field = %#v blockers=%#v", projection.Fields[0], projection.Blockers)
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 12*1024 || strings.Contains(string(encoded), "field_100") ||
		strings.Contains(string(encoded), "field_245") {
		t.Fatalf("large-form review projection is not bounded (%d bytes): %s", len(encoded), encoded)
	}
}

func TestDocumentFormQuestionOptionsBindLocalizedLabelsToBooleanValues(t *testing.T) {
	options, err := documentFormQuestionOptions(
		document.FormField{Kind: document.FormFieldCheckbox},
		"За другого",
		"За себя",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []interactions.Option{
		{Label: "За другого", Value: "true"},
		{Label: "За себя", Value: "false"},
	}
	if !slices.Equal(options, want) {
		t.Fatalf("checkbox options = %#v, want %#v", options, want)
	}
	if _, err = documentFormQuestionOptions(
		document.FormField{Kind: document.FormFieldText},
		"Да",
		"Нет",
	); err == nil {
		t.Fatal("localized checkbox labels were accepted for a text field")
	}
}

func workflowFieldDiscoveryDigest(t *testing.T, schema document.FormFieldsFacts) string {
	t.Helper()
	digest, err := documentFormDiscoveryDigest(schema.SourceSHA256, schema)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func newWorkflowFormStore(t *testing.T) (*document.FormJobStore, document.FormJobStoreOptions) {
	t.Helper()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	keyRoot := filepath.Join(root, "keys")
	for _, directory := range []string{stateRoot, keyRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	options := document.FormJobStoreOptions{
		StateRoot: stateRoot, KeyRoot: keyRoot,
		Retention:         document.DefaultFormJobRetention,
		TerminalRetention: document.DefaultFormJobTerminalRetention,
		MaxJobs:           document.DefaultFormJobMaxJobs, MaxEventsPerJob: document.DefaultFormJobMaxEvents,
		MaxBytes: document.DefaultFormJobMaxBytes,
	}
	store, err := document.OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	return store, options
}

func workflowTestSchema(source []byte) document.FormFieldsFacts {
	sourceDigest := sha256.Sum256(source)
	fieldDigest := sha256.Sum256([]byte("workflow-field"))
	widgetDigest := sha256.Sum256([]byte("workflow-widget"))
	return document.FormFieldsFacts{
		SourceSHA256: hex.EncodeToString(sourceDigest[:]),
		Backend: document.BackendIdentity{
			Name: document.PDFCPUBackendName, Version: document.PDFCPUBackendVersion,
			Package: document.PDFCPUBackendPackage, PackageRevision: document.PDFCPUBackendVersion,
			Role: "production", IsolationMode: document.WorkerIsolationMode,
		},
		Limits: document.FormFieldLimits{
			MaxFields: document.DefaultMaxFormFields, MaxWidgets: document.DefaultMaxFieldWidgets,
			MaxOptions: document.DefaultMaxFieldOptions, MaxTextBytes: document.DefaultMaxFieldTextBytes,
			MaxReportBytes: document.DefaultMaxFormReportBytes,
		},
		Fields: []document.FormField{{
			ID: "field_" + hex.EncodeToString(fieldDigest[:]), Name: "Legal name", Kind: document.FormFieldText,
			Required: true, Widgets: []document.FormFieldWidget{{
				ID: "widget_" + hex.EncodeToString(widgetDigest[:]), Page: 1, Ordinal: 1,
			}},
		}},
	}
}

func workflowSchemaResolver(schema document.FormFieldsFacts) documentFormSchemaResolver {
	return func(
		_ context.Context,
		store ownedDocumentMediaStore,
		ref string,
		owner media.MediaOwner,
	) (document.FormFieldsFacts, error) {
		source, err := store.OpenOwned(ref, owner)
		if err != nil {
			return document.FormFieldsFacts{}, err
		}
		_ = source.Close()
		return schema, nil
	}
}

func workflowToolContext(t *testing.T, executionID, callID string, refs []string) context.Context {
	t.Helper()
	ctx := toolshared.WithToolExecutionIdentity(t.Context(), "workspace", executionID)
	ctx = toolshared.WithToolSessionContext(ctx, "agent", "session", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "route")
	ctx = toolshared.WithToolCallID(ctx, callID)
	ctx = toolshared.WithToolContext(ctx, "telegram", "chat")
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "telegram", Account: "primary", ChatID: "chat", ChatType: "direct",
		SenderID: "actor", ActorID: "actor", MessageID: "message-" + executionID,
	})
	return toolshared.WithToolDocumentContext(ctx, refs, true)
}

func workflowInteractionRoute() interactions.Route {
	return interactions.Route{
		AgentID: "agent", SessionKey: "session", RouteSessionKey: "route",
		Channel: "telegram", AccountID: "primary", ChatID: "chat", ChatType: "direct",
		SenderID: "actor",
	}
}

func decodeWorkflowResult(t *testing.T, content string) safeDocumentFormResult {
	t.Helper()
	var projection safeDocumentFormResult
	if err := json.Unmarshal([]byte(content), &projection); err != nil {
		t.Fatalf("decode workflow result: %v\n%s", err, content)
	}
	return projection
}

func slicesEqualStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
