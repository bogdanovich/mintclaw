package tools

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFormIntakeNewMessageDoesNotRevisitCompletedUnrelatedWindows(t *testing.T) {
	for _, size := range []int{13, 193} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			model := &intakeWorkflowTestInterpreter{}
			tool, ctx, schema, jobID, options := intakeWorkflowFixture(t, model,
				func(schema *document.FormFieldsFacts) {
					for index := len(schema.Fields); index < size; index++ {
						schema.Fields = append(schema.Fields, document.FormField{
							ID: fmt.Sprintf("field_%064x", index+1), Name: fmt.Sprintf("Generic detail %d", index),
							Kind: document.FormFieldText, Required: true,
							Widgets: []document.FormFieldWidget{{
								ID: fmt.Sprintf("widget_%064x", index+1), Page: index/8 + 1, Ordinal: 1,
							}},
						})
					}
				})
			const original, answer = "Some as yet unresolved text", "Casey Mint"
			target := schema.Fields[len(schema.Fields)-1].ID
			oldCalls := 0
			model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
				result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}
				if view.Pass.Input.Text == original {
					oldCalls++
				} else if slices.Contains(view.Pass.CandidateIDs, target) {
					result.Proposals = []document.FormIntakeProposal{{
						FieldID: target, Literals: []document.FormIntakeLiteral{{Text: answer, Occurrence: 1}},
					}}
				}
				return result, nil
			}
			progress, err := tool.FindProtectedFormIntake(ctx)
			if err != nil || progress == nil {
				t.Fatal("intake unavailable")
			}
			guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
			old, err := tool.AdmitProtectedFormIntake(ctx, guard, "unresolved-original", original)
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"action": "form", "form_action": "continue", "answer_ref": old}
			initial := decodeWorkflowResult(t, tool.Execute(ctx, args).ForLLM)
			owner, err := documentFormOwner(ctx)
			if err != nil || initial.Intake == nil || initial.Intake.Progress.PendingInputs != 1 {
				t.Fatal("fixture did not retain input")
			}
			before, err := tool.formJobs.ReadFormIntakeInput(ctx, old, owner)
			if err != nil {
				t.Fatal(err)
			}
			beforeCalls := oldCalls
			tool.formJobs.Close()
			tool.formJobs, err = document.OpenFormJobStore(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tool.formJobs.Close)
			ref, err := tool.AdmitProtectedFormIntake(ctx, guard, "new-fact", answer)
			if err != nil {
				t.Fatal(err)
			}
			newCtx := toolshared.WithToolExecutionIdentity(ctx, "workspace", "new-message")
			newArgs := map[string]any{"action": "form", "form_action": "continue", "answer_ref": ref}
			result := tool.Execute(newCtx, newArgs)
			got := decodeWorkflowResult(t, result.ForLLM)
			after, readErr := tool.formJobs.ReadFormIntakeInput(ctx, old, owner)
			// Revision is a current job projection, not an old-input mutation.
			before.Revision = after.Revision
			if readErr != nil || !reflect.DeepEqual(before, after) || oldCalls != beforeCalls {
				t.Fatal("new message spent reservations on unchanged completed windows")
			}
			if result.IsError || got.Intake == nil || got.Intake.AcceptedCount != 1 ||
				got.Intake.Progress.PendingInputs != 1 || got.Intake.Failure != nil {
				t.Fatal("new fact was not accepted alongside preserved old input")
			}
			if strings.Contains(result.ForLLM, original) || strings.Contains(result.ForLLM, answer) {
				t.Fatal("private input escaped progress")
			}
			calls := model.intakeCalls
			if tool.Execute(newCtx, newArgs).IsError || model.intakeCalls != calls {
				t.Fatal("same-message replay reinterpreted retained input")
			}
			// Only an explicit retry of the old receipt revisits its empty outcomes.
			retryCtx := toolshared.WithToolExecutionIdentity(ctx, "workspace", "explicit-old-retry")
			tool.Execute(retryCtx, args)
			if oldCalls <= beforeCalls {
				t.Fatal("explicit retry lost authority to process retained accounting")
			}
		})
	}
}

func TestDocumentFormIntakeAcceptedClarificationAndExhaustedOlderInputHaveSeparateStatus(t *testing.T) {
	for _, code := range []string{"field_ambiguous", "field_confirmation_required", "field_conflicting"} {
		t.Run(code, func(t *testing.T) {
			model := &intakeWorkflowTestInterpreter{}
			tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model)
			const original, answer = "PRIVATE_UNACCOUNTED_ORIGINAL", "PRIVATE_ACCEPTED_VALUE"
			target := schema.Fields[0].ID
			model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
				result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}
				if slices.Contains(view.Pass.CandidateIDs, target) {
					if view.Pass.Input.Text == original {
						result.Findings = []document.FormFieldMappingBlocker{{FieldID: target, Code: code}}
					} else {
						result.Proposals = []document.FormIntakeProposal{{
							FieldID: target, Literals: []document.FormIntakeLiteral{{Text: answer, Occurrence: 1}},
						}}
					}
				}
				return result, nil
			}
			progress, err := tool.FindProtectedFormIntake(ctx)
			if err != nil || progress == nil {
				t.Fatal("intake unavailable")
			}
			guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
			old, err := tool.AdmitProtectedFormIntake(ctx, guard, "old-finding", original)
			if err != nil {
				t.Fatal(err)
			}
			tool.Execute(ctx, map[string]any{"action": "form", "form_action": "continue", "answer_ref": old})
			owner, err := documentFormOwner(ctx)
			if err != nil {
				t.Fatal(err)
			}
			input, err := tool.formJobs.ReadFormIntakeInput(ctx, old, owner)
			if err != nil || len(input.Findings) != 1 {
				t.Fatal("fixture did not retain a finding")
			}
			for index := input.Passes; index < document.MaxFormIntakeExtractionPasses; index++ {
				_, err = tool.formJobs.ReserveFormIntakePass(ctx, document.FormIntakePassRequest{
					Reference: old, ExpectedRevision: input.Revision, Owner: owner, Schema: schema,
					CandidateIDs: []string{target}, IdempotencyKey: fmt.Sprintf("exhausted-%d", index),
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			ref, err := tool.AdmitProtectedFormIntake(ctx, guard, "new-clarification", answer)
			if err != nil {
				t.Fatal(err)
			}
			newCtx := toolshared.WithToolExecutionIdentity(ctx, "workspace", "clarification-at-capacity")
			result := tool.Execute(newCtx, map[string]any{
				"action": "form", "form_action": "continue", "answer_ref": ref,
			})
			got := decodeWorkflowResult(t, result.ForLLM)
			if result.IsError || got.Intake == nil || got.Intake.Failure == nil ||
				got.Intake.Failure.Reason != "capacity_exceeded" || got.Intake.AcceptedCount != 1 ||
				got.Intake.Progress.PendingInputs != 1 || got.Intake.ReceivedInputState != "accounted" ||
				got.Intake.RetainedFindingCount != 1 {
				t.Fatal("accepted clarification was conflated with retained older failure")
			}
			wantFindings := 0
			if code == "field_conflicting" {
				wantFindings = 1
			}
			if len(got.Intake.Findings) != wantFindings || len(got.Intake.FindingFields) != wantFindings {
				t.Fatal("question projection repeated settled advice or hid a genuine conflict")
			}
			retained, err := tool.formJobs.ReadFormIntakeInput(ctx, old, owner)
			if err != nil || retained.Consumed || retained.Text != original ||
				retained.Passes != document.MaxFormIntakeExtractionPasses || len(retained.Findings) != 1 {
				t.Fatal("public projection changed protected input, native findings or its exhausted budget")
			}
			values, err := tool.formJobs.ReadValues(ctx, jobID, owner, []string{target})
			if err != nil || values[target].Value.Text != answer {
				t.Fatal("accepted clarification was not in the canonical ledger")
			}
			followup, err := tool.ProtectedAnswerContinuationFollowup(result)
			if err != nil || followup == nil || !followup.ResponseOnly ||
				!strings.Contains(followup.Instruction, "does not undo newly accepted decisions") ||
				!strings.Contains(followup.Instruction, "do not offer a blind retry") {
				t.Fatal("failure guidance lost partial success or suggested retrying an exhausted input")
			}
			if strings.Contains(result.ForLLM, "PRIVATE_") || strings.Contains(followup.Instruction, "PRIVATE_") {
				t.Fatal("status or guidance leaked protected input")
			}
			review := tool.Execute(newCtx, map[string]any{"action": "form", "form_action": "review", "job_id": jobID})
			reviewProjection := decodeWorkflowResult(t, review.ForLLM)
			if reviewProjection.Review != nil && reviewProjection.Review.Ready {
				t.Fatal("filtered presentation advice granted review despite unaccounted input")
			}
		})
	}
}

func TestDocumentFormIntakeQuestionFilteringKeepsNativeBlockersAndUnconfirmedValues(t *testing.T) {
	summary := document.FormJobMappingSummary{ConfirmedFieldIDs: []string{"confirmed", "preserved", "optional_blank"}}
	for _, code := range []string{
		"field_ambiguous", "field_confirmation_required", "field_invalid", "field_conflicting", "field_unsupported",
	} {
		for _, id := range []string{"confirmed", "preserved", "optional_blank", "missing", "invalid", "low_confidence"} {
			finding := document.FormFieldMappingBlocker{FieldID: id, Code: code}
			want := code != "field_ambiguous" && code != "field_confirmation_required" ||
				!slices.Contains(summary.ConfirmedFieldIDs, id)
			if documentFormIntakeActionableFinding(summary, finding) != want {
				t.Fatalf("incorrect question eligibility for code=%s field=%s", code, id)
			}
		}
	}
}
