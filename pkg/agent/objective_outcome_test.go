package agent

import (
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
  "missing_items":[]
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
		"call browser_act with external_commit during this turn",
		"Never replace that tool call with a prose approval question",
		"do not close the browser session while the runtime is suspended",
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

func TestObjectiveOutcomeDropsExplanationOnVerifiedSuccess(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],` +
		`"missing_items":[],"explanation":"stale blocker"}` + objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{Item: "inspected", Kind: "result"}})
	_, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if outcome.Status != taskresult.OutcomeSucceeded || outcome.Explanation != "" {
		t.Fatalf("successful outcome retained explanation: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeRejectsNonBrowserExternalReceipt(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":["inv-fake"]}],"missing_items":[]}` +
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
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],"missing_items":[]}` +
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
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],"missing_items":[]}` +
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
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":["inv-navigation"]}],"missing_items":[]}` +
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
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],"missing_items":[]}` +
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

func TestExtractObjectiveOutcomeNeverUpgradesProducerReportedPartial(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"partial","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],"missing_items":[]}` +
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
		`{"status":"succeeded","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],"missing_items":[]}` +
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
		`{"objective_id":"objective_2","receipt_ids":["inv-one"]}],"missing_items":[]}` +
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
