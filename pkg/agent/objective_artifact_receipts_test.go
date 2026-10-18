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

func artifactObjectiveFixture() (reportedObjectiveOutcome, []toolshared.WriteAuditEntry, []runtimeObjectiveItem) {
	refs := []string{"transfer-artifact://photo_1", "transfer-artifact://photo_2", "transfer-artifact://photo_3"}
	listing, closure := "Found three product photographs.", "Browser closure was confirmed."
	summary := listing + "\n\nObtained originals: " + strings.Join(refs, ", ") + "\n\n" + closure + " END-OF-REPORT"
	reported := reportedObjectiveOutcome{
		Status: "succeeded", Result: summary,
		CompletedItems: []reportedObjectiveItem{
			{ObjectiveID: "objective_1", Output: &taskresult.ObjectiveOutput{Kind: "text", Text: listing}},
			{ObjectiveID: "objective_2", Output: &taskresult.ObjectiveOutput{Kind: "artifact", ArtifactRefs: refs}},
			{ObjectiveID: "objective_3", Output: &taskresult.ObjectiveOutput{Kind: "text", Text: closure}},
		},
	}
	var audits []toolshared.WriteAuditEntry
	for index, ref := range refs {
		audits = append(audits, toolshared.WriteAuditEntry{
			Kind: "external_action", Tool: "browser_act", Action: "download", Success: true,
			Metadata: map[string]string{
				"invocation_id": fmt.Sprintf("inv_photo_%d", index+1), "effect": "unknown", "artifact_ref": ref,
			},
		})
	}
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "Inspect the listing", Kind: "result"},
		{Item: "Obtain original photograph files", Kind: "result"},
		{Item: "Confirm browser closure", Kind: "result"},
	})
	return reported, audits, checklist
}

func TestArtifactResultAccountsForDownloadsAndPreservesCompleteAnswer(t *testing.T) {
	reported, audits, checklist := artifactObjectiveFixture()
	encoded, err := json.Marshal(reported)
	require.NoError(t, err)
	content := objectiveOutcomeStart + string(encoded) + objectiveOutcomeEnd
	clean, outcome := extractObjectiveOutcome(content, audits, true, checklist)
	require.Equal(t, taskresult.OutcomeSucceeded, outcome.Status, "%+v", outcome)
	require.Empty(t, outcome.MissingItems)
	require.Equal(t, reported.Result, clean)
	require.Equal(t, reported.Result, outcome.UserSummary)
	require.Len(t, outcome.CompletedItems[1].Receipts, 3)
	for index, receipt := range outcome.CompletedItems[1].Receipts {
		require.Equal(t, fmt.Sprintf("inv_photo_%d", index+1), receipt.ID)
		require.Equal(t, "external_action", receipt.Kind, "retain original audit semantics")
	}
	instruction, repair := objectiveOutcomeRepairInstruction(content, audits, checklist)
	require.False(t, repair, instruction)
}

func TestArtifactResultDoesNotAbsorbUnrelatedActionReceipts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*toolshared.WriteAuditEntry)
	}{
		{"missing artifact proof", func(a *toolshared.WriteAuditEntry) { delete(a.Metadata, "artifact_ref") }},
		{"different artifact", func(a *toolshared.WriteAuditEntry) { a.Metadata["artifact_ref"] += "_other" }},
		{"external commit", func(a *toolshared.WriteAuditEntry) { a.Metadata["effect"] = "external_commit" }},
		{"click", func(a *toolshared.WriteAuditEntry) { a.Action = "click" }},
		{"upload", func(a *toolshared.WriteAuditEntry) { a.Action = "upload" }},
		{"file chooser", func(a *toolshared.WriteAuditEntry) { a.Action = "file_chooser" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			reported, audits, checklist := artifactObjectiveFixture()
			test.mutate(&audits[0])
			outcome := validateObjectiveOutcome(reported, audits, nil, checklist)
			require.Equal(t, taskresult.OutcomePartial, outcome.Status)
			require.Contains(t, strings.Join(outcome.MissingItems, " "), "receipt was not claimed")
			require.Len(t, outcome.CompletedItems[1].Receipts, 2)
			require.Empty(t, outcome.UserSummary)
		})
	}
}

func TestTextResultCannotConsumeDownloadsByMentioningArtifactRefs(t *testing.T) {
	reported, audits, checklist := artifactObjectiveFixture()
	reported.CompletedItems[1].Output = &taskresult.ObjectiveOutput{Kind: "text", Text: reported.Result}
	outcome := validateObjectiveOutcome(reported, audits, nil, checklist)
	require.Equal(t, taskresult.OutcomePartial, outcome.Status)
	require.Empty(t, outcome.CompletedItems[1].Receipts)
}

func TestArtifactResultsDoNotStealExplicitClaimsOrConsumeTwice(t *testing.T) {
	for _, externalFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("external first=%v", externalFirst), func(t *testing.T) {
			reported, audits, checklist := artifactObjectiveFixture()
			checklist = append(checklist, runtimeObjectiveItem{
				ID: "objective_4", Item: "Transfer originals", Kind: "external_action",
			})
			claim := reportedObjectiveItem{ObjectiveID: "objective_4", ReceiptIDs: []string{
				"inv_photo_1", "inv_photo_2", "inv_photo_3",
			}}
			if externalFirst {
				reported.CompletedItems = append([]reportedObjectiveItem{claim}, reported.CompletedItems...)
			} else {
				reported.CompletedItems = append(reported.CompletedItems, claim)
			}
			outcome := validateObjectiveOutcome(reported, audits, nil, checklist)
			require.Equal(t, taskresult.OutcomeSucceeded, outcome.Status)
			for _, item := range outcome.CompletedItems {
				if item.Kind == "external_action" {
					require.Len(t, item.Receipts, 3)
				} else {
					require.Empty(t, item.Receipts)
				}
			}
		})
	}
	reported, audits, checklist := artifactObjectiveFixture()
	checklist = append(
		checklist,
		runtimeObjectiveItem{ID: "objective_4", Item: "Repeat file references", Kind: "result"},
	)
	item := reported.CompletedItems[1]
	item.ObjectiveID = "objective_4"
	reported.CompletedItems = append(reported.CompletedItems, item)
	outcome := validateObjectiveOutcome(reported, audits, nil, checklist)
	require.Equal(t, taskresult.OutcomeSucceeded, outcome.Status)
	require.Len(t, outcome.CompletedItems[1].Receipts, 3)
	require.Empty(t, outcome.CompletedItems[3].Receipts)
}

type artifactReceiptBrowserTool struct{ calls atomic.Int32 }

func (*artifactReceiptBrowserTool) Name() string        { return "browser_act" }
func (*artifactReceiptBrowserTool) Description() string { return "Acquire a fixture photograph" }
func (*artifactReceiptBrowserTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"photo": map[string]any{"type": "number"}}}
}

func (tool *artifactReceiptBrowserTool) Execute(_ context.Context, _ map[string]any) *toolshared.ToolResult {
	count := tool.calls.Add(1)
	ref := fmt.Sprintf("transfer-artifact://photo_%d", count)
	return toolshared.NewToolResult(fmt.Sprintf(`{"artifact":{"ref":%q}}`, ref)).
		WithWriteAudit(toolshared.WriteAuditEntry{
			Kind: "external_action", Tool: "browser_act", Action: "download", Success: true,
			Metadata: map[string]string{
				"invocation_id": fmt.Sprintf("inv_photo_%d", count), "effect": "unknown", "artifact_ref": ref,
			},
		})
}

func TestSpawnSubTurnReturnsArtifactResultWithoutRepairOrReplay(t *testing.T) {
	reported, _, _ := artifactObjectiveFixture()
	encoded, err := json.Marshal(reported)
	require.NoError(t, err)
	var calls []providers.ToolCall
	for index := range 3 {
		calls = append(calls, providers.ToolCall{
			ID: fmt.Sprintf(
				"call_photo_%d",
				index+1,
			),
			Name:      "browser_act",
			Arguments: map[string]any{"photo": index + 1},
		})
	}
	provider := &objectiveRepairCaptureProvider{responses: []*providers.LLMResponse{
		{ToolCalls: calls},
		{Content: objectiveOutcomeStart + string(encoded) + objectiveOutcomeEnd, FinishReason: "stop"},
	}}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	alpha, _ := al.registry.GetAgent("alpha")
	beta, _ := al.registry.GetAgent("beta")
	tool := &artifactReceiptBrowserTool{}
	beta.Tools.Register(tool)
	parent := &turnState{
		ctx: context.Background(), turnID: "parent-artifact-result",
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{}, agent: alpha,
		opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{SessionKey: "parent-artifact-result"}}),
	}
	result, err := spawnSubTurn(t.Context(), al, parent, SubTurnConfig{
		TargetAgentID: "beta", TaskPrompt: "Obtain the original photographs; do not change any listing",
		ObjectiveItems: []toolshared.ObjectiveSpec{
			{Item: "Inspect the listing", Kind: "result"},
			{Item: "Obtain original photograph files", Kind: "result"},
			{Item: "Confirm browser closure", Kind: "result"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Deliverable)
	require.Equal(t, taskresult.OutcomeSucceeded, result.Deliverable.ObjectiveOutcome.Status)
	require.Equal(t, reported.Result, result.Deliverable.Text)
	require.Equal(t, int32(3), tool.calls.Load(), "each acquisition runs once, with no report-repair replay")
	require.Len(t, provider.toolDefinitionCounts(), 2, "no model-only repair needed")
}

func TestArtifactOutputInstructionsGiveExclusiveShapes(t *testing.T) {
	reported, audits, checklist := artifactObjectiveFixture()
	reported.CompletedItems[1].Output.Text = "The originals were obtained."
	encoded, err := json.Marshal(reported)
	require.NoError(t, err)
	content := objectiveOutcomeStart + string(encoded) + objectiveOutcomeEnd
	instruction, repair := objectiveOutcomeRepairInstruction(content, audits, checklist)
	require.True(t, repair)
	require.Contains(t, instruction, "artifact output contained fields for a different output kind")
	for _, prompt := range []string{browserObjectiveOutcomeInstruction("Obtain originals", checklist), instruction} {
		for _, example := range []string{
			`{"kind":"text","text":"standalone result"}`,
			`{"kind":"records","records":[{"field":"value"}]}`,
			`{"kind":"artifact","artifact_refs":["stable-ref"]}`,
		} {
			require.Contains(t, prompt, example, "show valid, disjoint output shapes")
		}
		require.Contains(t, prompt, "Never mix text, records, and artifact_refs")
		require.Contains(t, prompt, "matching artifact results automatically")
		require.Contains(t, prompt, "keep result receipt_ids empty")
	}
	require.Contains(t, instruction, "model-only repair pass")
	require.Contains(t, instruction, "do not call any tool")
}

func TestSpawnSubTurnRepairsMixedArtifactOutputWithoutReplay(t *testing.T) {
	reported, _, _ := artifactObjectiveFixture()
	reported.CompletedItems = []reportedObjectiveItem{reported.CompletedItems[1]}
	reported.CompletedItems[0].ObjectiveID = "objective_1"
	valid, err := json.Marshal(reported)
	require.NoError(t, err)
	reported.CompletedItems[0].Output.Text = "The originals were obtained."
	invalid, err := json.Marshal(reported)
	require.NoError(t, err)
	var calls []providers.ToolCall
	for index := range 3 {
		calls = append(calls, providers.ToolCall{
			ID: fmt.Sprintf("call_mixed_photo_%d", index+1), Name: "browser_act",
			Arguments: map[string]any{"photo": index + 1},
		})
	}
	provider := &objectiveRepairCaptureProvider{responses: []*providers.LLMResponse{
		{ToolCalls: calls},
		{Content: objectiveOutcomeStart + string(invalid) + objectiveOutcomeEnd, FinishReason: "stop"},
		{Content: objectiveOutcomeStart + string(valid) + objectiveOutcomeEnd, FinishReason: "stop"},
	}}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	alpha, _ := al.registry.GetAgent("alpha")
	beta, _ := al.registry.GetAgent("beta")
	tool := &artifactReceiptBrowserTool{}
	beta.Tools.Register(tool)
	parent := &turnState{
		ctx: context.Background(), turnID: "parent-mixed-artifact-result",
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{}, agent: alpha,
		opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{SessionKey: "parent-mixed-artifact-result"}}),
	}
	result, err := spawnSubTurn(t.Context(), al, parent, SubTurnConfig{
		TargetAgentID: "beta", TaskPrompt: "Obtain originals and report closure",
		ObjectiveItems: []toolshared.ObjectiveSpec{
			{
				Item: "Obtain original photograph files and report the outcome", Kind: "result",
				Acceptance: &taskresult.ObjectiveAcceptance{OutputKind: "artifact"},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Deliverable)
	require.Equal(t, taskresult.OutcomeSucceeded, result.Deliverable.ObjectiveOutcome.Status)
	require.Equal(t, reported.Result, result.Deliverable.Text)
	require.Equal(t, int32(3), tool.calls.Load(), "repair must not repeat acquisition")
	counts := provider.toolDefinitionCounts()
	require.Len(t, counts, 3)
	require.Zero(t, counts[2], "repair has no tools")
	messages := provider.messagesForCall(2)
	require.NotEmpty(t, messages)
	repairPrompt := messages[len(messages)-1].Content
	require.Contains(t, repairPrompt, `{"kind":"artifact","artifact_refs":["stable-ref"]}`)
	require.Contains(t, repairPrompt, "matching artifact results automatically")
}
