package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestExtractObjectiveOutcomeDowngradesUnverifiedExternalItem(t *testing.T) {
	content := "Published what could be completed.\n" + objectiveOutcomeStart + `{
  "status":"succeeded",
  "completed_items":[
    {"objective_id":"objective_1","receipt_ids":["inv_yakima"]},
    {"objective_id":"objective_2","receipt_ids":["inv_missing"]}
  ],
  "missing_items":[],
  "result":"Published what could be completed."
}` + objectiveOutcomeEnd
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Target: "https://example.com/yakima", Action: "click",
		Tool: "browser_act", Success: true, Summary: "publish",
		Metadata: map[string]string{"invocation_id": "inv_yakima", "effect": "external_commit"},
	}}

	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "Yakima published", Kind: "external_action"},
		{Item: "Vissani published", Kind: "external_action"},
	})
	clean, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if clean != "Published what could be completed." || outcome == nil ||
		outcome.Status != taskresult.OutcomePartial || len(outcome.CompletedItems) != 1 ||
		outcome.CompletedItems[0].Item != "Yakima published" ||
		len(outcome.CompletedItems[0].Receipts) != 1 ||
		outcome.CompletedItems[0].Receipts[0].ID != "inv_yakima" || len(outcome.MissingItems) != 1 ||
		!strings.Contains(outcome.MissingItems[0], "Vissani") {
		t.Fatalf("clean = %q; outcome = %#v", clean, outcome)
	}
}

func TestBrowserObjectiveOutcomeInstructionDrivesClickEffectFromWorkflow(t *testing.T) {
	instruction := browserObjectiveOutcomeInstruction("inspect and publish", normalizeObjectiveChecklist(
		[]toolshared.ObjectiveSpec{
			{Item: "inspect postings", Kind: "result"},
			{Item: "publish selected posting", Kind: "external_action"},
		},
	))
	for _, required := range []string{
		"declare effect from this checklist and the requested workflow",
		"read, navigation, or local_edit for non-committing UI steps",
		"external_commit only immediately before an important external state change",
		"Do not infer click effect from the element role or HTTP method",
		"call browser_act during this turn with the effect required by the trusted browser contract",
		"external_commit for a known important external state change, or unknown only",
		"Never replace that tool call with a prose approval question",
		"do not close the browser session while the runtime is suspended",
		"keep the external_action item missing and explain the unverified postcondition",
	} {
		if !strings.Contains(instruction, required) {
			t.Fatalf("objective instruction omitted %q: %s", required, instruction)
		}
	}
}

func TestObjectiveOutcomeCarriesBoundedReportedBlocker(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"blocked","completed_items":[],"missing_items":["objective_1"],` +
		`"explanation":"All six source photo files are missing from temporary storage."}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "upload saved photos", Kind: "external_action",
	}})
	_, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if outcome.Status != taskresult.OutcomeBlocked ||
		outcome.Explanation != "All six source photo files are missing from temporary storage." {
		t.Fatalf("blocked outcome = %#v", outcome)
	}
	userContent := objectiveOutcomeUserContent("Photos uploaded.", outcome)
	if strings.Contains(userContent, "Photos uploaded") ||
		!strings.Contains(userContent, "Reported reason: All six source photo files are missing") {
		t.Fatalf("blocked user content = %q", userContent)
	}
}

func TestObjectiveOutcomeRequiresResultForSuccess(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],` +
		`"missing_items":[],"explanation":"stale blocker"}` + objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{Item: "inspected", Kind: "result"}})
	clean, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if clean != "" || outcome.Status != taskresult.OutcomePartial ||
		outcome.Explanation != objectiveOutcomeResultRequired || len(outcome.CompletedItems) != 1 ||
		len(outcome.MissingItems) != 1 || outcome.MissingItems[0] != objectiveOutcomeResultRequired {
		t.Fatalf("clean = %q; outcome = %#v", clean, outcome)
	}
}

func TestObjectiveOutcomePreservesSuccessfulTerminalResult(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],` +
		`"missing_items":[],"result":"Inspection complete: https://example.com/item/42; ID: 42"}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{Item: "inspect item", Kind: "result"}})
	clean, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if outcome.Status != taskresult.OutcomeSucceeded || outcome.Explanation != "" ||
		clean != "Inspection complete: https://example.com/item/42; ID: 42" {
		t.Fatalf("successful terminal result = %q, outcome = %#v", clean, outcome)
	}
}

func TestObjectiveOutcomeUsesCurrentResultWithVerifiedReceipt(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1",` +
		`"receipt_ids":["inv-publish"]}],"missing_items":[],` +
		`"result":"Published once: https://example.com/item/42; ID: 42","explanation":"stale detail"}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "publish item", Kind: "external_action",
	}})
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Target: "https://example.com", Action: "click",
		Tool: "browser_act", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-publish", "effect": "external_commit"},
	}}
	clean, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if clean != "Published once: https://example.com/item/42; ID: 42" ||
		outcome.Status != taskresult.OutcomeSucceeded || outcome.Explanation != "" ||
		len(outcome.CompletedItems) != 1 || len(outcome.CompletedItems[0].Receipts) != 1 ||
		len(outcome.MissingItems) != 0 {
		t.Fatalf("clean = %q; outcome = %#v", clean, outcome)
	}
}

func TestObjectiveOutcomeAcceptsApprovedUnknownEffectReceipt(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1",` +
		`"receipt_ids":["inv-upload"]}],"missing_items":[],"result":"Uploaded once."}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "upload file", Kind: "external_action",
	}})
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Target: "https://example.com", Action: "upload",
		Tool: "browser_act", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-upload", "effect": "unknown"},
	}}
	clean, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if clean != "Uploaded once." || outcome.Status != taskresult.OutcomeSucceeded ||
		len(outcome.CompletedItems) != 1 || len(outcome.CompletedItems[0].Receipts) != 1 ||
		outcome.CompletedItems[0].Receipts[0].ID != "inv-upload" || len(outcome.MissingItems) != 0 {
		t.Fatalf("clean = %q; outcome = %#v", clean, outcome)
	}
}

func TestObjectiveOutcomeRejectsNonExternalReceiptEffects(t *testing.T) {
	for _, effect := range []string{"read", "navigation", "local_edit"} {
		t.Run(effect, func(t *testing.T) {
			content := objectiveOutcomeStart +
				`{"status":"succeeded","completed_items":[{"objective_id":"objective_1",` +
				`"receipt_ids":["inv-action"]}],"missing_items":[],"result":"Completed."}` +
				objectiveOutcomeEnd
			checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
				Item: "perform external action", Kind: "external_action",
			}})
			audits := []toolshared.WriteAuditEntry{{
				Kind: "external_action", Tool: "browser_act", Success: true,
				Metadata: map[string]string{"invocation_id": "inv-action", "effect": effect},
			}}
			_, outcome := extractObjectiveOutcome(content, audits, true, checklist)
			if outcome.Status != taskresult.OutcomeBlocked || len(outcome.CompletedItems) != 0 ||
				len(outcome.MissingItems) != 1 ||
				!strings.Contains(outcome.MissingItems[0], "missing verified runtime receipt") {
				t.Fatalf("effect %q produced outcome %#v", effect, outcome)
			}
		})
	}
}

func TestTerminalTurnDeliverableMakesValidatedResultCanonical(t *testing.T) {
	report := &taskresult.Report{ReportID: "publish-report"}
	base := &taskresult.Deliverable{
		Text:      "stale tool-owned result",
		Artifacts: []taskresult.Artifact{{Ref: "file:/tmp/photo.jpg"}},
		Metadata:  map[string]string{"producer": "browser"},
		Report:    report,
	}
	outcome := &taskresult.Outcome{Status: taskresult.OutcomeSucceeded}
	deliverable := terminalTurnDeliverable(
		base,
		"Published once: https://example.com/item/42; ID: 42",
		outcome,
	)
	if deliverable == nil || deliverable.Text != "Published once: https://example.com/item/42; ID: 42" ||
		len(deliverable.Artifacts) != 1 || deliverable.Artifacts[0].Ref != "file:/tmp/photo.jpg" ||
		deliverable.Metadata["producer"] != "browser" || deliverable.Report == nil ||
		deliverable.Report.ReportID != report.ReportID || deliverable.ObjectiveOutcome == nil ||
		deliverable.ObjectiveOutcome.Status != taskresult.OutcomeSucceeded {
		t.Fatalf("terminal deliverable = %#v", deliverable)
	}
	if base.Text != "stale tool-owned result" || base.ObjectiveOutcome != nil {
		t.Fatalf("base deliverable was mutated: %#v", base)
	}
}

func TestObjectiveOutcomeDoesNotUseExplanationAsReceiptResult(t *testing.T) {
	content := "The approved action finished.\n" + objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1",` +
		`"receipt_ids":["inv-publish"]}],"missing_items":[],` +
		`"explanation":"Published once: https://example.com/item/42; ID: 42"}` + objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "publish item", Kind: "external_action",
	}})
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Target: "https://example.com", Action: "click",
		Tool: "browser_act", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-publish", "effect": "external_commit"},
	}}
	clean, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if clean != "The approved action finished." || outcome.Status != taskresult.OutcomePartial ||
		outcome.Explanation != objectiveOutcomeResultRequired || len(outcome.CompletedItems) != 1 ||
		len(outcome.CompletedItems[0].Receipts) != 1 || len(outcome.MissingItems) != 1 ||
		outcome.MissingItems[0] != objectiveOutcomeResultRequired {
		t.Fatalf("clean = %q; outcome = %#v", clean, outcome)
	}
	if projection := objectiveOutcomeUserContent(clean, outcome); strings.Contains(projection, "Published once") {
		t.Fatalf("legacy explanation projected as terminal result: %q", projection)
	}
}

func TestTerminalTurnDeliverableNormalizesEmptyToolDeliverable(t *testing.T) {
	if deliverable := terminalTurnDeliverable(&taskresult.Deliverable{}, "", nil); deliverable != nil {
		t.Fatalf("empty terminal deliverable = %#v, want nil", deliverable)
	}
}

func TestObjectiveOutcomeRequiresExplanationForIncompleteStatus(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      string
		explanation string
	}{
		{name: "blocked missing", status: "blocked"},
		{name: "blocked whitespace", status: "blocked", explanation: "   "},
		{name: "partial missing", status: "partial"},
		{name: "partial whitespace", status: "partial", explanation: `\n\t`},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := objectiveOutcomeStart + `{"status":"` + test.status +
				`","completed_items":[],"missing_items":["objective_1"],"explanation":"` +
				test.explanation + `"}` + objectiveOutcomeEnd
			checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
				Item: "complete the requested result", Kind: "result",
			}})
			_, outcome := extractObjectiveOutcome(content, nil, true, checklist)
			if outcome.Status != taskresult.OutcomeBlocked ||
				outcome.Explanation != "objective outcome explanation was required" {
				t.Fatalf("outcome = %#v, want invalid-report blocker", outcome)
			}
		})
	}
}

func TestObjectiveOutcomeBoundsIncompleteExplanation(t *testing.T) {
	const explanationLimit = 240
	longExplanation := strings.Repeat("bounded explanation ", explanationLimit)
	content := objectiveOutcomeStart +
		`{"status":"blocked","completed_items":[],"missing_items":["objective_1"],"explanation":"` +
		longExplanation + `"}` + objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{Item: "complete result", Kind: "result"}})
	_, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if outcome.Status != taskresult.OutcomeBlocked || outcome.Explanation == "" ||
		len([]rune(outcome.Explanation)) > explanationLimit {
		t.Fatalf("bounded outcome explanation = %q (%d bytes)", outcome.Explanation, len(outcome.Explanation))
	}
}

func TestExtractObjectiveOutcomeRejectsNonBrowserExternalReceipt(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1",` +
		`"receipt_ids":["inv-fake"]}],"missing_items":[],"result":"Published."}` +
		objectiveOutcomeEnd
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Tool: "custom_tool", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-fake", "effect": "external_commit"},
	}}
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{Item: "published", Kind: "external_action"}})
	_, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if outcome.Status != taskresult.OutcomeBlocked || len(outcome.CompletedItems) != 0 ||
		len(outcome.MissingItems) != 1 {
		t.Fatalf("non-browser receipt verified external action: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeUsesRuntimeClassification(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],` +
		`"missing_items":[],"result":"Account updated."}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "like and follow the account", Kind: "external_action",
	}})
	_, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if outcome.Status != taskresult.OutcomeBlocked || len(outcome.CompletedItems) != 0 ||
		len(outcome.MissingItems) != 1 {
		t.Fatalf("misclassified publish claim was accepted: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeRequiresBrowserChildReport(t *testing.T) {
	clean, outcome := extractObjectiveOutcome("I think it worked.", nil, true)
	if clean != "I think it worked." || outcome == nil ||
		outcome.Status != taskresult.OutcomeBlocked || len(outcome.CompletedItems) != 0 ||
		len(outcome.MissingItems) != 1 {
		t.Fatalf("clean = %q; outcome = %#v", clean, outcome)
	}
}

func TestExtractObjectiveOutcomeAcceptsReadResultWithoutWriteReceipt(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],` +
		`"missing_items":[],"result":"Account inspection complete."}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{Item: "account inspected", Kind: "result"}})
	_, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if outcome == nil || outcome.Status != taskresult.OutcomeSucceeded ||
		len(outcome.CompletedItems) != 1 || len(outcome.MissingItems) != 0 {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeIgnoresNavigationInvocationIDForReadResult(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1",` +
		`"receipt_ids":["inv-navigation"]}],"missing_items":[],"result":"Account inspection complete."}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{Item: "account inspected", Kind: "result"}})
	_, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if outcome == nil || outcome.Status != taskresult.OutcomeSucceeded ||
		len(outcome.CompletedItems) != 1 || len(outcome.MissingItems) != 0 {
		t.Fatalf("navigation invocation downgraded read result: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeRejectsUnclaimedExternalActionForReadResult(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],` +
		`"missing_items":[],"result":"Account inspection complete."}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{Item: "account inspected", Kind: "result"}})
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Tool: "browser_act", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-write", "effect": "external_commit"},
	}}
	_, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if outcome == nil || outcome.Status != taskresult.OutcomePartial ||
		len(outcome.CompletedItems) != 1 || len(outcome.MissingItems) != 1 ||
		!strings.Contains(outcome.MissingItems[0], "external browser action") {
		t.Fatalf("unclaimed external action did not downgrade read result: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeAccountsForCommitWithUnverifiedPostcondition(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"blocked","completed_items":[],` +
		`"missing_items":["objective_1","objective_2"],` +
		`"explanation":"The commit ran, but the external system did not expose a confirmation."}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "report the submission result", Kind: "result"},
		{Item: "submit the form exactly once", Kind: "external_action"},
	})
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Tool: "browser_act", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-submit", "effect": "external_commit"},
	}}

	_, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if outcome == nil || outcome.Status != taskresult.OutcomeBlocked || len(outcome.CompletedItems) != 0 ||
		len(outcome.MissingItems) != 2 || outcome.MissingItems[0] != "report the submission result" ||
		outcome.MissingItems[1] != "submit the form exactly once" {
		t.Fatalf("unverified postcondition outcome = %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeDoesNotHideCommitBehindSucceededMissingItem(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[],"missing_items":["objective_1"],` +
		`"result":"Submission complete."}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "submit the form", Kind: "external_action",
	}})
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Tool: "browser_act", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-submit", "effect": "external_commit"},
	}}

	_, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if outcome == nil || outcome.Status != taskresult.OutcomeBlocked || len(outcome.CompletedItems) != 0 ||
		len(outcome.MissingItems) != 2 ||
		!strings.Contains(strings.Join(outcome.MissingItems, "\n"), "receipt was not claimed") {
		t.Fatalf("succeeded status hid an unclaimed commit: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeKeepsAmbiguousUnclaimedCommitDiagnostic(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"blocked","completed_items":[],"missing_items":["objective_1"],` +
		`"explanation":"The external result could not be verified."}` + objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "submit the form", Kind: "external_action",
	}})
	audits := []toolshared.WriteAuditEntry{
		{
			Kind: "external_action", Tool: "browser_act", Success: true,
			Metadata: map[string]string{"invocation_id": "inv-one", "effect": "external_commit"},
		},
		{
			Kind: "external_action", Tool: "browser_act", Success: true,
			Metadata: map[string]string{"invocation_id": "inv-two", "effect": "external_commit"},
		},
	}

	_, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if outcome == nil || len(outcome.MissingItems) != 2 ||
		!strings.Contains(outcome.MissingItems[1], "receipt was not claimed") {
		t.Fatalf("ambiguous unclaimed commits were hidden: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeDoesNotAccountReceiptWithOmittedExternalObjective(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"blocked","completed_items":[],"missing_items":["objective_1"],` +
		`"explanation":"The external result could not be verified."}` + objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "submit the first form", Kind: "external_action"},
		{Item: "submit the second form", Kind: "external_action"},
	})
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Tool: "browser_act", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-ambiguous", "effect": "external_commit"},
	}}

	_, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if outcome == nil || len(outcome.MissingItems) != 3 ||
		!strings.Contains(strings.Join(outcome.MissingItems, "\n"), "objective ID was omitted") ||
		!strings.Contains(strings.Join(outcome.MissingItems, "\n"), "receipt was not claimed") {
		t.Fatalf("ambiguous omitted external objective was hidden: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeDoesNotConsumeReceiptFromRejectedCompletedItem(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[` +
		`{"objective_id":"objective_1","receipt_ids":["inv-valid","inv-unknown"]}],` +
		`"missing_items":[],"result":"Submission complete."}` + objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "submit the form", Kind: "external_action",
	}})
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Tool: "browser_act", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-valid", "effect": "external_commit"},
	}}

	_, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if outcome == nil || len(outcome.CompletedItems) != 0 || len(outcome.MissingItems) != 2 ||
		!strings.Contains(strings.Join(outcome.MissingItems, "\n"), "missing verified runtime receipt") ||
		!strings.Contains(strings.Join(outcome.MissingItems, "\n"), "receipt was not claimed") {
		t.Fatalf("rejected item consumed its valid receipt: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomePrioritizesOrphanReceiptAtMissingLimit(t *testing.T) {
	specs := make([]toolshared.ObjectiveSpec, objectiveOutcomeLimit)
	missingIDs := make([]string, objectiveOutcomeLimit)
	for index := range objectiveOutcomeLimit {
		specs[index] = toolshared.ObjectiveSpec{
			Item: "external action " + strconv.Itoa(index+1), Kind: "external_action",
		}
		missingIDs[index] = "objective_" + strconv.Itoa(index+1)
	}
	reported, err := json.Marshal(reportedObjectiveOutcome{
		Status:       string(taskresult.OutcomeBlocked),
		MissingItems: missingIDs,
		Explanation:  "The external results could not be verified.",
	})
	if err != nil {
		t.Fatal(err)
	}
	checklist := normalizeObjectiveChecklist(specs)
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Tool: "browser_act", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-orphan", "effect": "external_commit"},
	}}

	_, outcome := extractObjectiveOutcome(
		objectiveOutcomeStart+string(reported)+objectiveOutcomeEnd,
		audits,
		true,
		checklist,
	)
	if outcome == nil || len(outcome.MissingItems) != objectiveOutcomeLimit ||
		!strings.Contains(strings.Join(outcome.MissingItems, "\n"), "receipt was not claimed") {
		t.Fatalf("orphan receipt diagnostic was dropped at the missing-item limit: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeNeverUpgradesProducerReportedPartial(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"partial","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],` +
		`"missing_items":[],"explanation":"producer reported incomplete execution"}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{Item: "account inspected", Kind: "result"}})
	_, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if outcome == nil || outcome.Status != taskresult.OutcomePartial ||
		len(outcome.CompletedItems) != 1 || len(outcome.MissingItems) != 1 {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeRejectsOmittedRequestedItem(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],` +
		`"missing_items":[],"result":"Active postings inspected."}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "inspect active postings", Kind: "result"},
		{Item: "inspect old postings", Kind: "result"},
	})
	_, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if outcome.Status != taskresult.OutcomePartial || len(outcome.CompletedItems) != 1 ||
		len(outcome.MissingItems) != 1 || !strings.Contains(outcome.MissingItems[0], "old postings") {
		t.Fatalf("omitted objective was accepted: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeDoesNotReuseReceiptAcrossActions(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[` +
		`{"objective_id":"objective_1","receipt_ids":["inv-one"]},` +
		`{"objective_id":"objective_2","receipt_ids":["inv-one"]}],"missing_items":[],` +
		`"result":"Publishing complete."}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "publish Yakima", Kind: "external_action"},
		{Item: "publish Vissani", Kind: "external_action"},
	})
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Tool: "browser_act", Success: true,
		Metadata: map[string]string{"invocation_id": "inv-one", "effect": "external_commit"},
	}}
	_, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	if outcome.Status != taskresult.OutcomePartial || len(outcome.CompletedItems) != 1 ||
		len(outcome.MissingItems) != 1 || !strings.Contains(outcome.MissingItems[0], "Vissani") {
		t.Fatalf("one receipt certified multiple actions: %#v", outcome)
	}
}

func TestObjectiveOutcomeUserContentReplacesContradictoryPartialProse(t *testing.T) {
	outcome := &taskresult.Outcome{
		Status:         taskresult.OutcomePartial,
		CompletedItems: []taskresult.Item{{Item: "Yakima published"}},
		MissingItems:   []string{"Vissani not verified"},
	}
	got := objectiveOutcomeUserContent("Both items were published.", outcome)
	if strings.Contains(got, "Both items") || !strings.Contains(got, "Yakima published") ||
		!strings.Contains(got, "Vissani not verified") {
		t.Fatalf("user content contradicts verified outcome: %q", got)
	}
}
