package agent

import (
	"strings"
	"testing"

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
		outcome.Status != toolshared.ObjectiveOutcomePartial || len(outcome.CompletedItems) != 1 ||
		outcome.CompletedItems[0].Item != "Yakima published" ||
		len(outcome.CompletedItems[0].Receipts) != 1 ||
		outcome.CompletedItems[0].Receipts[0].ID != "inv_yakima" || len(outcome.MissingItems) != 1 ||
		!strings.Contains(outcome.MissingItems[0], "Vissani") {
		t.Fatalf("clean = %q; outcome = %#v", clean, outcome)
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
	if outcome.Status != toolshared.ObjectiveOutcomeBlocked || len(outcome.CompletedItems) != 0 ||
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
	if outcome.Status != toolshared.ObjectiveOutcomeBlocked || len(outcome.CompletedItems) != 0 ||
		len(outcome.MissingItems) != 1 {
		t.Fatalf("misclassified publish claim was accepted: %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeRequiresBrowserChildReport(t *testing.T) {
	clean, outcome := extractObjectiveOutcome("I think it worked.", nil, true)
	if clean != "I think it worked." || outcome == nil ||
		outcome.Status != toolshared.ObjectiveOutcomeBlocked || len(outcome.CompletedItems) != 0 ||
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
	if outcome == nil || outcome.Status != toolshared.ObjectiveOutcomeSucceeded ||
		len(outcome.CompletedItems) != 1 || len(outcome.MissingItems) != 0 {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeNeverUpgradesProducerReportedPartial(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"partial","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],"missing_items":[]}` +
		objectiveOutcomeEnd
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{Item: "account inspected", Kind: "result"}})
	_, outcome := extractObjectiveOutcome(content, nil, true, checklist)
	if outcome == nil || outcome.Status != toolshared.ObjectiveOutcomePartial ||
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
	if outcome.Status != toolshared.ObjectiveOutcomePartial || len(outcome.CompletedItems) != 1 ||
		len(outcome.MissingItems) != 1 || !strings.Contains(outcome.MissingItems[0], "old postings") {
		t.Fatalf("omitted objective was accepted: %#v", outcome)
	}
}

func TestObjectiveOutcomeUserContentReplacesContradictoryPartialProse(t *testing.T) {
	outcome := &toolshared.ObjectiveOutcome{
		Status:         toolshared.ObjectiveOutcomePartial,
		CompletedItems: []toolshared.ObjectiveItem{{Item: "Yakima published"}},
		MissingItems:   []string{"Vissani not verified"},
	}
	got := objectiveOutcomeUserContent("Both items were published.", outcome)
	if strings.Contains(got, "Both items") || !strings.Contains(got, "Yakima published") ||
		!strings.Contains(got, "Vissani not verified") {
		t.Fatalf("user content contradicts verified outcome: %q", got)
	}
}
