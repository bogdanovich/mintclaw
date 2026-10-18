package tools

import (
	"slices"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFormIntakeCollectAcrossBrowsedWindowsUsesCurrentNativeState(t *testing.T) {
	tool, ctx, schema, jobID, _ := intakeWorkflowFixture(
		t, &intakeWorkflowTestInterpreter{}, func(schema *document.FormFieldsFacts) {
			schema.Fields[12].Widgets[0].Page = 2
		},
	)
	owner := mustDocumentFormOwner(t, ctx)
	job, err := tool.formJobs.Get(t.Context(), jobID, owner)
	if err != nil {
		t.Fatal(err)
	}
	// Match the live incident: collection is already active and a prior fact
	// was accepted. A zero-value prepared job still needs its initial plan.
	if _, _, err = tool.formJobs.AppendValue(t.Context(), document.FormJobAppendValueRequest{
		JobID: jobID, ExpectedRevision: job.Revision, Owner: owner, FieldID: schema.Fields[1].ID,
		IdempotencyKey: "prior-accepted-fact",
		Value:          document.FormProtectedValue{Kind: document.ProtectedValueText, Text: "PRIVATE_EXISTING"},
		State:          document.FormValueConfirmed, Source: document.FormValueSourceUser,
		Confidence: document.FormValueConfidenceExact, Validation: document.FormValueValidationValid,
	}); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{0, 8} {
		browsed := tool.Execute(ctx, map[string]any{
			"action": "form", "form_action": "status", "job_id": jobID, "pages": []int{1, 2}, "field_offset": offset,
		})
		if browsed.IsError {
			t.Fatal(browsed.ForLLM)
		}
		if offset == 0 {
			continue
		}
		followup, err := tool.ToolResultFollowup(browsed)
		if err != nil || followup == nil || followup.ResponseOnly {
			t.Fatal("browsing lost the intake boundary")
		}
		ids := []string{schema.Fields[0].ID, schema.Fields[12].ID}
		args := map[string]any{
			"action": "form", "form_action": "collect", "job_id": jobID, "candidate_field_ids": ids,
			"question": "Provide the two related details in one paragraph.", "role_context": "One coherent section.",
		}
		if err = followup.ValidateArguments(args); err != nil {
			t.Fatalf("valid mixed-window proposal rejected: %v", err)
		}
		selected := tool.Execute(toolshared.WithToolExecutionIdentity(ctx, "workspace", "mixed-window-collect"), args)
		if selected.IsError {
			t.Fatal(selected.ForLLM)
		}
		progress, err := tool.FindProtectedFormIntake(ctx)
		if err != nil || progress == nil || !slices.Equal(progress.Context.CandidateFieldIDs, ids) ||
			progress.PendingInputs != 0 {
			t.Fatal("coherent selection did not retain one bounded native context")
		}
		record, err := tool.formJobs.Get(t.Context(), jobID, mustDocumentFormOwner(t, ctx))
		if err != nil || record.LedgerRevision != 1 || len(record.Fields) != 1 {
			t.Fatal("planning wrote a value")
		}
	}
}

func TestDocumentFormIntakeCollectionRejectsNonCurrentUnresolvedSelections(t *testing.T) {
	for _, kind := range []string{
		"unknown", "read only", "preserved", "confirmed", "deferred", "stale record", "wrong owner", "wrong schema",
	} {
		t.Run(kind, func(t *testing.T) {
			tool, ctx, schema, jobID, _ := intakeWorkflowFixture(
				t,
				&intakeWorkflowTestInterpreter{},
				func(schema *document.FormFieldsFacts) {
					if kind == "read only" {
						schema.Fields[12].ReadOnly = true
					}
					if kind == "preserved" {
						schema.Fields[12].HasValue = true
					}
				},
			)
			owner := mustDocumentFormOwner(t, ctx)
			original, err := tool.formJobs.Get(t.Context(), jobID, owner)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "deferred" {
				// The ordinary intake fixture deliberately has no proven editable
				// backend. Seed a separate native editable unit fixture rather than
				// disabling that production capability check or rewriting its job.
				if _, err = tool.formJobs.Delete(t.Context(), jobID, original.Revision, owner); err != nil {
					t.Fatal(err)
				}
				original, err = tool.formJobs.Create(t.Context(), document.FormJobCreateRequest{
					Owner: owner, StartIdempotencyKey: "editable-collection-fixture",
					SourceRef:    "media://00000000-0000-0000-0000-000000000001",
					SourceDigest: original.SourceDigest, FieldSchemaDigest: original.FieldSchemaDigest,
					BackendRevision: original.BackendRevision, AuditPolicyRevision: original.AuditPolicyRevision,
					OutputMode: document.FormOutputEditableAcroForm,
				})
				if err != nil {
					t.Fatal(err)
				}
				jobID = original.JobID
				ids := make([]string, 8)
				for index := range ids {
					ids[index] = schema.Fields[index].ID
				}
				if _, err = tool.formJobs.StartFormIntake(t.Context(), document.FormIntakeStartRequest{
					JobID:            jobID,
					ExpectedRevision: original.Revision,
					Owner:            owner,
					Schema:           schema,
					IdempotencyKey:   "editable-intake-fixture",
					Context: document.FormIntakeContext{
						CandidateFieldIDs: ids,
						Question:          "Provide these details.",
					},
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := original
			if kind == "deferred" {
				before, err = tool.formJobs.SetFormOutputScope(t.Context(), document.FormOutputScopeRequest{
					JobID: jobID, ExpectedRevision: original.Revision, Owner: owner, Schema: schema,
					Scope: document.FormOutputEditableDraft, DeferredFieldIDs: []string{schema.Fields[12].ID},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "confirmed" || kind == "stale record" {
				before, _, err = tool.formJobs.AppendValue(t.Context(), document.FormJobAppendValueRequest{
					JobID:            jobID,
					ExpectedRevision: original.Revision,
					Owner:            owner,
					FieldID:          schema.Fields[12].ID,
					IdempotencyKey:   "existing-confirmed-field",
					Value: document.FormProtectedValue{
						Kind: document.ProtectedValueText,
						Text: "PRIVATE_TEST_FACT",
					},
					State:      document.FormValueConfirmed,
					Source:     document.FormValueSourceUser,
					Confidence: document.FormValueConfidenceExact,
					Validation: document.FormValueValidationValid,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			selected := schema.Fields[12].ID
			record := before
			if kind == "unknown" {
				selected = "field_unknown"
			}
			if kind == "stale record" {
				record = original
				selected = schema.Fields[11].ID
			}
			requestOwner, requestSchema := owner, schema
			if kind == "wrong owner" {
				requestOwner.AgentID = "another-agent"
			}
			if kind == "wrong schema" {
				requestSchema.Fields = slices.Clone(schema.Fields)
				requestSchema.Fields[12].Name += " changed"
			}
			args := map[string]any{"candidate_field_ids": []string{selected}, "question": "Provide this detail."}
			result := tool.collectFormIntake(ctx, requestOwner, requestSchema, record, args, "", "")
			if !result.IsError {
				t.Fatal("non-current unresolved selection was admitted")
			}
			after, err := tool.formJobs.Get(t.Context(), jobID, owner)
			if err != nil || after.Revision != before.Revision || after.LedgerRevision != before.LedgerRevision {
				t.Fatal("rejected selection changed the job or ledger")
			}
			progress, err := tool.FindProtectedFormIntake(ctx)
			if err != nil || progress == nil || !slices.Equal(progress.Context.CandidateFieldIDs, []string{
				schema.Fields[0].ID, schema.Fields[1].ID, schema.Fields[2].ID, schema.Fields[3].ID,
				schema.Fields[4].ID, schema.Fields[5].ID, schema.Fields[6].ID, schema.Fields[7].ID,
			}) {
				t.Fatal("rejected selection changed the current context")
			}
		})
	}
}
