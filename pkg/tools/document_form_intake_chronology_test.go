package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestDocumentFormIntakeRoleClarificationUsesNativeChronologyAndReportsActualFields(t *testing.T) {
	for _, language := range []string{"en", "ru"} {
		for _, size := range []int{13, 37} {
			t.Run(fmt.Sprintf("%s/%d", language, size), func(t *testing.T) {
				model := &intakeWorkflowTestInterpreter{}
				tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model, func(schema *document.FormFieldsFacts) {
					for index := len(schema.Fields); index < size; index++ {
						field := schema.Fields[0]
						field.ID, field.Name = fmt.Sprintf(
							"field_%064x",
							index+1,
						), fmt.Sprintf(
							"Other detail %d",
							index,
						)
						field.Widgets = []document.FormFieldWidget{{
							ID: fmt.Sprintf("widget_%064x", index+1), Page: index/8 + 1, Ordinal: 1,
						}}
						schema.Fields = append(schema.Fields, field)
					}
				})
				original := "Borrower Casey Mint. Contact Robin Mint. Telephone 202-555-0147; owner unspecified."
				clarification := "The previous telephone belongs only to the borrower. Contact telephone not supplied. City Mintford."
				if language == "ru" {
					original = "Я заёмщик Casey Mint. Контакт Robin Mint. Телефон 202-555-0147; пока не указываю, чей он."
					clarification = "Предыдущий телефон принадлежит только заёмщику. Телефон контакта пока не указан. Город Mintford."
				}
				model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
					result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}
					clarified := false
					for _, related := range view.RelatedInputs {
						clarified = clarified || related.Text == clarification &&
							view.Pass.Input.AdmissionOrder > 0 && related.AdmissionOrder > view.Pass.Input.AdmissionOrder
					}
					facts := map[int]string{}
					if view.Pass.Input.Text == original {
						facts[2], facts[3] = "Casey Mint", "Robin Mint"
						if clarified {
							facts[0] = "202-555-0147"
						} else {
							for _, index := range []int{0, 1} {
								if slices.Contains(view.Pass.CandidateIDs, schema.Fields[index].ID) {
									result.Findings = append(result.Findings, document.FormFieldMappingBlocker{
										FieldID: schema.Fields[index].ID, Code: "field_ambiguous",
									})
								}
							}
						}
					} else {
						facts[4] = "Mintford"
					}
					for index, value := range facts {
						if slices.Contains(view.Pass.CandidateIDs, schema.Fields[index].ID) {
							result.Proposals = append(result.Proposals, document.FormIntakeProposal{
								FieldID:  schema.Fields[index].ID,
								Literals: []document.FormIntakeLiteral{{Text: value, Occurrence: 1}},
							})
						}
					}
					return result, nil
				}
				progress, err := tool.FindProtectedFormIntake(ctx)
				if err != nil || progress == nil {
					t.Fatal(err)
				}
				guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
				var continued *toolshared.ToolResult
				for index, message := range []string{original, clarification} {
					ref, admitErr := tool.AdmitProtectedFormIntake(ctx, guard, fmt.Sprint("input-", index), message)
					if admitErr != nil {
						t.Fatal(admitErr)
					}
					callCtx := toolshared.WithToolExecutionIdentity(ctx, "workspace", fmt.Sprint("turn-", index))
					continued = tool.Execute(callCtx, map[string]any{
						"action": "form", "form_action": "continue", "answer_ref": ref,
					})
					if continued.IsError {
						t.Fatal("safe continuation failed")
					}
				}
				owner, err := documentFormOwner(ctx)
				if err != nil {
					t.Fatal(err)
				}
				values, err := tool.formJobs.ReadValues(ctx, jobID, owner, []string{
					schema.Fields[0].ID, schema.Fields[2].ID, schema.Fields[3].ID, schema.Fields[4].ID,
				})
				if err != nil || len(values) != 4 || values[schema.Fields[0].ID].Value.Text != "202-555-0147" ||
					values[schema.Fields[2].ID].Value.Text != "Casey Mint" ||
					values[schema.Fields[3].ID].Value.Text != "Robin Mint" ||
					values[schema.Fields[4].ID].Value.Text != "Mintford" {
					t.Fatal(
						"clarification did not preserve the two people and apply the retained value only to its owner",
					)
				}
				if _, err = tool.formJobs.ReadValues(
					ctx,
					jobID,
					owner,
					[]string{schema.Fields[1].ID},
				); !errors.Is(
					err,
					document.ErrFormJobNotFound,
				) {
					t.Fatal("the other person's unspecified value was invented")
				}
				var wire struct {
					Intake struct {
						AcceptedFields []safeDocumentFormField `json:"accepted_fields"`
					} `json:"intake"`
				}
				if json.Unmarshal([]byte(continued.ForLLM), &wire) != nil || len(wire.Intake.AcceptedFields) != 2 {
					t.Fatal("ordinary progress omits which native fields were actually accepted")
				}
				for _, field := range wire.Intake.AcceptedFields {
					if field.FieldID != schema.Fields[0].ID && field.FieldID != schema.Fields[4].ID {
						t.Fatal("progress presented another field as newly accepted")
					}
				}
				for _, value := range []string{"Casey Mint", "Robin Mint", "202-555-0147", "Mintford"} {
					if strings.Contains(continued.ForLLM, value) {
						t.Fatal("protected value escaped ordinary progress")
					}
				}
			})
		}
	}
}

func TestDocumentFormIntakeCollectDoesNotRequestRepeatingDeliveredIntroduction(t *testing.T) {
	projection := safeDocumentFormResult{
		SchemaVersion: documentFormWorkflowSchemaVersion, Operation: "form",
		FormAction: "collect", Job: &safeDocumentFormJob{JobID: "form_job_test"},
		Intake: &safeDocumentFormIntake{},
	}
	followup, err := NewDocumentTool().ToolResultFollowup(documentFormToolResult(projection))
	if err != nil || followup == nil || !followup.ResponseOnly ||
		!strings.Contains(followup.Instruction, "already delivered") ||
		!strings.Contains(followup.Instruction, "accepted_fields") {
		t.Fatal("collect rendering can duplicate its introduction or infer accepted fields from an aggregate count")
	}
}

func TestDocumentFormIntakePartialAcceptanceCannotIdentifyAnUnresolvedFieldAsAccepted(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model)
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}
		if view.Pass.Input.Text == "Still ambiguous" && slices.Contains(view.Pass.CandidateIDs, schema.Fields[0].ID) {
			result.Findings = []document.FormFieldMappingBlocker{
				{FieldID: schema.Fields[0].ID, Code: "field_ambiguous"},
			}
		} else if view.Pass.Input.Text == "New city Mintford" &&
			slices.Contains(view.Pass.CandidateIDs, schema.Fields[4].ID) {
			result.Proposals = []document.FormIntakeProposal{{
				FieldID: schema.Fields[4].ID, Literals: []document.FormIntakeLiteral{{Text: "Mintford", Occurrence: 1}},
			}}
		}
		return result, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal(err)
	}
	guard := bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}
	var result *toolshared.ToolResult
	for index, message := range []string{"Still ambiguous", "New city Mintford"} {
		ref, admitErr := tool.AdmitProtectedFormIntake(ctx, guard, fmt.Sprint(index), message)
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		result = tool.Execute(toolshared.WithToolExecutionIdentity(ctx, "workspace", fmt.Sprint(index)), map[string]any{
			"action": "form", "form_action": "continue", "answer_ref": ref,
		})
	}
	got := decodeWorkflowResult(t, result.ForLLM)
	if result.IsError || got.Intake.AcceptedCount != 1 || len(got.Intake.AcceptedFields) != 1 ||
		got.Intake.AcceptedFields[0].FieldID != schema.Fields[4].ID || len(got.Intake.Findings) != 1 ||
		got.Intake.Findings[0].FieldID != schema.Fields[0].ID || strings.Contains(result.ForLLM, "Mintford") {
		t.Fatal("aggregate acceptance was attributed to an unresolved field or exposed its private value")
	}
	followup, err := tool.ProtectedAnswerContinuationFollowup(result)
	if err != nil || followup == nil || !followup.ResponseOnly ||
		!strings.Contains(followup.Instruction, "Never describe an unresolved field as accepted") {
		t.Fatal("ordinary rendering can mistake another accepted field for resolution")
	}
}

func TestDocumentFormIntakeAcceptedFieldsStayBoundedAndDoNotRepeatOnReplay(t *testing.T) {
	model := &intakeWorkflowTestInterpreter{}
	tool, ctx, schema, jobID, _ := intakeWorkflowFixture(t, model)
	values := make([]string, 9)
	for index := range values {
		values[index] = fmt.Sprintf("PrivateValue%d", index)
	}
	model.interpret = func(view document.FormIntakeInterpretationView) (document.FormIntakeInterpretation, error) {
		result := document.FormIntakeInterpretation{Intent: document.FormIntakeIntentFacts}
		for index, value := range values {
			if slices.Contains(view.Pass.CandidateIDs, schema.Fields[index].ID) {
				result.Proposals = append(result.Proposals, document.FormIntakeProposal{
					FieldID:  schema.Fields[index].ID,
					Literals: []document.FormIntakeLiteral{{Text: value, Occurrence: 1}},
				})
			}
		}
		return result, nil
	}
	progress, err := tool.FindProtectedFormIntake(ctx)
	if err != nil || progress == nil {
		t.Fatal(err)
	}
	ref, err := tool.AdmitProtectedFormIntake(ctx,
		bus.InboundDocumentIntakeGuard{JobID: jobID, ModeID: progress.ModeID}, "nine-facts", strings.Join(values, " "))
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "form", "form_action": "continue", "answer_ref": ref}
	result := tool.Execute(ctx, args)
	got := decodeWorkflowResult(t, result.ForLLM)
	if result.IsError || got.Intake.AcceptedCount != 9 || len(got.Intake.AcceptedFields) != 8 ||
		!got.Intake.AcceptedFieldsTruncated {
		t.Fatal("new acceptance escaped the bounded public field view")
	}
	for _, value := range values {
		if strings.Contains(result.ForLLM, value) {
			t.Fatal("accepted-field labels leaked a private value")
		}
	}
	replay := decodeWorkflowResult(t, tool.Execute(ctx, args).ForLLM)
	if replay.Intake.AcceptedCount != 0 || len(replay.Intake.AcceptedFields) != 0 {
		t.Fatal("replay claimed historical decisions as new acceptance")
	}
}
