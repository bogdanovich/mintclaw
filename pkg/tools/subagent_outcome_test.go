package tools

import (
	"testing"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestTaskRegistryPayloadPreservesVerifiedObjectiveOutcome(t *testing.T) {
	result := toolshared.NewToolResult("partial result").WithCompletion(&toolshared.CompletionResult{
		Text: "Yakima published; Vissani missing",
		ObjectiveOutcome: &toolshared.ObjectiveOutcome{
			Status: toolshared.ObjectiveOutcomePartial,
			CompletedItems: []toolshared.ObjectiveItem{{
				Item: "Yakima published", Kind: "external_action",
				Receipts: []toolshared.ObjectiveReceipt{{
					ID: "inv_yakima", Kind: "external_action", Tool: "browser_act",
					Metadata: map[string]string{"invocation_id": "inv_yakima"},
				}},
			}},
			MissingItems: []string{"Vissani missing"},
		},
	})

	completion := completionPayloadForTaskRegistry(result)
	deliverable := deliverablePayloadForTaskRegistry(result)
	if completion == nil || completion.ObjectiveOutcome == nil ||
		completion.ObjectiveOutcome.Status != "partial" || deliverable == nil ||
		deliverable.ObjectiveOutcome == nil || len(deliverable.ObjectiveOutcome.CompletedItems) != 1 ||
		deliverable.ObjectiveOutcome.CompletedItems[0].Receipts[0].ID != "inv_yakima" ||
		len(deliverable.ObjectiveOutcome.MissingItems) != 1 {
		t.Fatalf("completion = %#v; deliverable = %#v", completion, deliverable)
	}
}
