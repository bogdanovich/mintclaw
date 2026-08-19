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
    {"item":"Yakima published","kind":"external_action","receipt_ids":["inv_yakima"]},
    {"item":"Vissani published","kind":"external_action","receipt_ids":["inv_missing"]}
  ],
  "missing_items":[]
}` + objectiveOutcomeEnd
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Target: "https://example.com/yakima", Action: "click",
		Tool: "browser_act", Success: true, Summary: "publish",
		Metadata: map[string]string{"invocation_id": "inv_yakima"},
	}}

	clean, outcome := extractObjectiveOutcome(content, audits, true)
	if clean != "Published what could be completed." || outcome == nil ||
		outcome.Status != toolshared.ObjectiveOutcomePartial || len(outcome.CompletedItems) != 1 ||
		outcome.CompletedItems[0].Item != "Yakima published" ||
		len(outcome.CompletedItems[0].Receipts) != 1 ||
		outcome.CompletedItems[0].Receipts[0].ID != "inv_yakima" || len(outcome.MissingItems) != 1 ||
		!strings.Contains(outcome.MissingItems[0], "Vissani") {
		t.Fatalf("clean = %q; outcome = %#v", clean, outcome)
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
		`{"status":"succeeded","completed_items":[{"item":"account inspected","kind":"result","receipt_ids":[]}],"missing_items":[]}` +
		objectiveOutcomeEnd
	_, outcome := extractObjectiveOutcome(content, nil, true)
	if outcome == nil || outcome.Status != toolshared.ObjectiveOutcomeSucceeded ||
		len(outcome.CompletedItems) != 1 || len(outcome.MissingItems) != 0 {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestExtractObjectiveOutcomeNeverUpgradesProducerReportedPartial(t *testing.T) {
	content := objectiveOutcomeStart +
		`{"status":"partial","completed_items":[{"item":"account inspected","kind":"result","receipt_ids":[]}],"missing_items":[]}` +
		objectiveOutcomeEnd
	_, outcome := extractObjectiveOutcome(content, nil, true)
	if outcome == nil || outcome.Status != toolshared.ObjectiveOutcomePartial ||
		len(outcome.CompletedItems) != 1 || len(outcome.MissingItems) != 1 {
		t.Fatalf("outcome = %#v", outcome)
	}
}
