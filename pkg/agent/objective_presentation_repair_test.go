package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func encodePresentationReport(t *testing.T, report reportedObjectiveOutcome) string {
	t.Helper()
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	return objectiveOutcomeStart + string(encoded) + objectiveOutcomeEnd
}

func TestObjectiveRepairIncludesRejectedSuccessfulPresentation(t *testing.T) {
	for _, candidate := range []string{
		"Three original files obtained. Browser closed. END-OF-REPORT",
		"Found three product photographs. END-OF-REPORT",
		`{"files":3,"closed":true}`,
	} {
		t.Run(candidate, func(t *testing.T) {
			reported, audits, checklist := artifactObjectiveFixture()
			reported.Result = candidate
			content := encodePresentationReport(t, reported)
			outcome := validateObjectiveOutcome(reported, audits, nil, checklist)
			require.Equal(t, taskresult.OutcomeSucceeded, outcome.Status)
			require.Empty(t, outcome.UserSummary)
			require.Len(t, outcome.CompletedItems[1].Receipts, 3)

			instruction, repair := objectiveOutcomeRepairInstruction(content, audits, checklist)
			require.True(t, repair, "successful execution must not skip a rejected final answer")
			require.Contains(t, instruction, "user-facing answer did not preserve the verified objective outputs")
			require.Contains(t, instruction, "Preserve completed_items, receipt_ids, and output values unchanged")
			require.Contains(t, instruction, "requested language and response format")
			require.Contains(t, instruction, "model-only repair pass")
			require.Contains(t, instruction, "do not call any tool")
			require.NotContains(t, instruction, "inv_photo_", "all acquisition receipts were already accounted for")
		})
	}
}

func TestObjectivePresentationRepairLeavesExactJSONAndValidAnswersAlone(t *testing.T) {
	reported, audits, checklist := artifactObjectiveFixture()
	instruction, repair := objectiveOutcomeRepairInstruction(encodePresentationReport(t, reported), audits, checklist)
	require.False(t, repair, instruction)

	checklist = normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "Return exact JSON", Kind: "result",
		Acceptance: &taskresult.ObjectiveAcceptance{OutputKind: "text", ExactJSON: true},
	}})
	reported = reportedObjectiveOutcome{
		Status: "succeeded", Result: `{"ok":true}`,
		CompletedItems: []reportedObjectiveItem{{
			ObjectiveID: "objective_1", Output: &taskresult.ObjectiveOutput{Kind: "text", Text: `{"ok":true}`},
		}},
	}
	instruction, repair = objectiveOutcomeRepairInstruction(encodePresentationReport(t, reported), nil, checklist)
	require.False(t, repair, instruction)
}

type presentationCloseTool struct{ calls atomic.Int32 }

func (*presentationCloseTool) Name() string        { return "browser_session" }
func (*presentationCloseTool) Description() string { return "Close the fixture browser session" }
func (*presentationCloseTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string"}}}
}

func (tool *presentationCloseTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	tool.calls.Add(1)
	return toolshared.NewToolResult(`{"state":"closed"}`)
}

func TestSpawnSubTurnRepairsPresentationAfterAcquisitionAndClosureWithoutReplay(t *testing.T) {
	for _, test := range []struct {
		name      string
		corrected bool
		toolCall  bool
	}{
		{name: "complete answer", corrected: true},
		{name: "bounded fallback"},
		{name: "repair cannot repeat acquisition", toolCall: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reported, _, _ := artifactObjectiveFixture()
			complete := encodePresentationReport(t, reported)
			reported.Result = "Three originals were obtained, and the session is closed. END-OF-REPORT"
			incomplete := encodePresentationReport(t, reported)
			repairResponse := &providers.LLMResponse{Content: incomplete, FinishReason: "stop"}
			if test.corrected {
				repairResponse.Content = complete
			}
			if test.toolCall {
				repairResponse.ToolCalls = []providers.ToolCall{{ID: "forbidden-repeat", Name: "browser_act"}}
			}
			var calls []providers.ToolCall
			for index := range 3 {
				calls = append(calls, providers.ToolCall{
					ID: fmt.Sprintf("original-photo-%d", index), Name: "browser_act",
					Arguments: map[string]any{"photo": index + 1},
				})
			}
			provider := &objectiveRepairCaptureProvider{responses: []*providers.LLMResponse{
				{ToolCalls: calls},
				{ToolCalls: []providers.ToolCall{{
					ID: "close-fixture", Name: "browser_session", Arguments: map[string]any{"action": "close"},
				}}},
				{Content: incomplete, FinishReason: "stop"},
				repairResponse,
			}}
			al, cleanup := newMultiAgentLoop(t, provider)
			defer cleanup()
			alpha, _ := al.registry.GetAgent("alpha")
			beta, _ := al.registry.GetAgent("beta")
			acquisition, closure := &artifactReceiptBrowserTool{}, &presentationCloseTool{}
			beta.Tools.Register(acquisition)
			beta.Tools.Register(closure)
			parent := &turnState{
				ctx: t.Context(), turnID: "parent-presentation-repair",
				pendingResults: make(chan *toolshared.ToolResult, 4),
				concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
				session:        &ephemeralSessionStore{}, agent: alpha,
				opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{SessionKey: "parent-presentation-repair"}}),
			}
			result, err := spawnSubTurn(t.Context(), al, parent, SubTurnConfig{
				TargetAgentID: "beta",
				DeliveryMode:  toolshared.AsyncDeliveryUserOnly,
				TaskPrompt:    "Obtain originals, close, and reply in three paragraphs ending END-OF-REPORT",
				ObjectiveItems: []toolshared.ObjectiveSpec{
					{Item: "Inspect the listing", Kind: "result"},
					{Item: "Obtain original photograph files", Kind: "result"},
					{Item: "Confirm browser closure", Kind: "result"},
				},
			})
			require.NoError(t, err)
			require.NotNil(t, result.Deliverable)
			outcome := result.Deliverable.ObjectiveOutcome
			require.Equal(t, taskresult.OutcomeSucceeded, outcome.Status, "execution proof remains successful")
			require.Len(t, outcome.CompletedItems[1].Receipts, 3)
			require.Equal(t, int32(3), acquisition.calls.Load())
			require.Equal(t, int32(1), closure.calls.Load())
			counts := provider.toolDefinitionCounts()
			require.Len(t, counts, 4, "one bounded presentation repair, not a retry loop")
			require.Zero(t, counts[3], "repair exposes no tools")
			if test.corrected {
				require.Equal(t, result.ForUser, result.Deliverable.Text)
				require.Equal(t, 3, len(strings.Split(result.ForUser, "\n\n")))
				require.True(t, strings.HasSuffix(result.ForUser, "END-OF-REPORT"))
				require.NotEmpty(t, outcome.UserSummary)
			} else {
				require.Empty(t, outcome.UserSummary)
				require.NotContains(t, result.ForUser, reported.Result, "rejected prose must not become a fallback")
			}
			for _, ref := range outcome.CompletedItems[1].Output.ArtifactRefs {
				require.Contains(t, result.ForUser, ref)
			}
			require.Contains(t, result.ForUser, "Browser closure was confirmed.")
		})
	}
}
