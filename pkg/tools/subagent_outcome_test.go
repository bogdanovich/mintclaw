package tools

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestTerminalTaskStatusForResultUsesObjectiveOutcome(t *testing.T) {
	result := &toolshared.ToolResult{Deliverable: &taskresult.Deliverable{
		ObjectiveOutcome: &taskresult.Outcome{Status: taskresult.OutcomeBlocked},
	}}
	if got := terminalTaskStatusForResult(result); got != taskregistry.StatusFailed {
		t.Fatalf("status = %q, want %q", got, taskregistry.StatusFailed)
	}
}

func TestTaskDeliverablePreservesVerifiedObjectiveOutcome(t *testing.T) {
	result := toolshared.NewToolResult("partial result").WithDeliverable(&taskresult.Deliverable{
		Text: "Yakima published; Vissani missing",
		ObjectiveOutcome: &taskresult.Outcome{
			Status: taskresult.OutcomePartial,
			CompletedItems: []taskresult.Item{{
				Item: "Yakima published", Kind: "external_action",
				Receipts: []taskresult.Receipt{{
					ID: "inv_yakima", Kind: "external_action", Tool: "browser_act",
					Metadata: map[string]string{"invocation_id": "inv_yakima"},
				}},
			}},
			MissingItems: []string{"Vissani missing"},
		},
	})

	deliverable := taskDeliverable(result)
	if deliverable == nil || deliverable.ObjectiveOutcome == nil ||
		deliverable.ObjectiveOutcome.Status != taskresult.OutcomePartial ||
		len(deliverable.ObjectiveOutcome.CompletedItems) != 1 ||
		deliverable.ObjectiveOutcome.CompletedItems[0].Receipts[0].ID != "inv_yakima" ||
		len(deliverable.ObjectiveOutcome.MissingItems) != 1 {
		t.Fatalf("deliverable = %#v", deliverable)
	}
}
