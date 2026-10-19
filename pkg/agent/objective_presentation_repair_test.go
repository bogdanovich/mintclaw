package agent

import (
	"context"
	"encoding/json"
	"errors"
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

			_, canonicalRepair := objectiveOutcomeRepairInstruction(content, audits, checklist)
			require.False(t, canonicalRepair, "presentation cannot replace a verified outcome")
			repair := newVerifiedPresentationRepair(terminalContent{content: content}, audits, nil, checklist)
			require.NotNil(t, repair, "successful execution must not skip a rejected final answer")
			instruction := repair.instruction()
			require.Contains(t, instruction, "Return only one complete natural-language answer")
			require.Contains(t, instruction, "requested language and response format")
			require.Contains(t, instruction, "model-only repair pass")
			require.Contains(t, instruction, "do not call any tool")
			require.NotContains(t, instruction, "inv_photo_", "all acquisition receipts were already accounted for")
		})
	}
}

func TestObjectivePresentationRepairLeavesExactJSONAndValidAnswersAlone(t *testing.T) {
	reported, audits, checklist := artifactObjectiveFixture()
	content := encodePresentationReport(t, reported)
	instruction, repair := objectiveOutcomeRepairInstruction(content, audits, checklist)
	require.False(t, repair, instruction)
	require.Nil(t, newVerifiedPresentationRepair(terminalContent{content: content}, audits, nil, checklist))

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
	content = encodePresentationReport(t, reported)
	instruction, repair = objectiveOutcomeRepairInstruction(content, nil, checklist)
	require.False(t, repair, instruction)
	require.Nil(t, newVerifiedPresentationRepair(terminalContent{content: content}, nil, nil, checklist))
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
	mixed, _, _ := artifactObjectiveFixture()
	mixed.CompletedItems[1].Output.Text = "Original files obtained."
	for _, test := range []struct {
		name            string
		corrected       bool
		toolCall        bool
		raw             *string
		change          bool
		failure         bool
		canonicalRepair bool
		initial         string
	}{
		{name: "complete answer", corrected: true},
		{name: "bounded fallback"},
		{name: "repair cannot repeat acquisition", toolCall: true},
		{name: "missing outcome", raw: new("The browser work is done.")},
		{name: "malformed outcome", raw: new("<task_outcome>{invalid}</task_outcome>")},
		{name: "empty replacement", raw: new("")},
		{name: "serialized tool projection", raw: new(`[tool_use: browser_act, args: {"photo":1}]`)},
		{name: "changed verified output", change: true},
		{name: "transient provider failure", failure: true},
		{
			name: "missing report then presentation", canonicalRepair: true,
			initial: "Three originals obtained. Browser closed.", corrected: true,
		},
		{
			name: "malformed report then presentation", canonicalRepair: true,
			initial: "<task_outcome>{invalid}</task_outcome>", corrected: true,
		},
		{name: "canonical repair then bounded fallback", canonicalRepair: true, initial: "Browser work finished."},
		{
			name: "canonical repair then provider failure", canonicalRepair: true,
			initial: "Browser work finished.", failure: true,
		},
		{
			name: "invalid output then presentation", canonicalRepair: true,
			initial: encodePresentationReport(t, mixed), corrected: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reported, _, _ := artifactObjectiveFixture()
			complete := reported.Result
			reported.Result = "Three originals were obtained, and the session is closed. END-OF-REPORT"
			incomplete := encodePresentationReport(t, reported)
			repairResponse := &providers.LLMResponse{Content: incomplete, FinishReason: "stop"}
			if test.corrected {
				repairResponse.Content = complete
			}
			if test.raw != nil {
				repairResponse.Content = *test.raw
			}
			if test.change {
				changed, _, _ := artifactObjectiveFixture()
				changed.CompletedItems[0].Output.Text = "Invented listing details."
				changed.Result = strings.ReplaceAll(
					changed.Result,
					"Found three product photographs.",
					"Invented listing details.",
				)
				repairResponse.Content = encodePresentationReport(t, changed)
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
			if test.canonicalRepair {
				provider.responses[2] = &providers.LLMResponse{Content: test.initial, FinishReason: "stop"}
				provider.responses = append(provider.responses[:3],
					&providers.LLMResponse{Content: incomplete, FinishReason: "stop"}, repairResponse)
			}
			callsBeforePresentation := 3
			if test.canonicalRepair {
				callsBeforePresentation++
			}
			if test.failure {
				provider.responses = provider.responses[:callsBeforePresentation]
				provider.failure = errors.New("connection reset by peer")
			}
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
			if test.failure {
				require.GreaterOrEqual(
					t,
					len(counts),
					callsBeforePresentation+1,
					"provider retry stays within the existing retry policy",
				)
			} else {
				require.Len(t, counts, callsBeforePresentation+1, "each correction phase is bounded independently")
			}
			for _, count := range counts[3:] {
				require.Zero(t, count, "repair and transport retries expose no tools")
			}
			presentationMessages := provider.messagesForCall(callsBeforePresentation)
			require.NotEmpty(t, presentationMessages)
			instruction := presentationMessages[len(presentationMessages)-1].Content
			require.Contains(t, instruction, "Return only one complete natural-language answer")
			require.Contains(t, instruction, "Do not return a task_outcome block")
			if test.canonicalRepair {
				canonicalMessages := provider.messagesForCall(3)
				require.Contains(t, canonicalMessages[len(canonicalMessages)-1].Content, "Finalization repair required")
			}
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

func TestVerifiedPresentationRepairKeepsCanonicalProofAndProtection(t *testing.T) {
	for _, change := range []string{"none", "artifact", "receipt", "missing item", "reordered items"} {
		t.Run(change, func(t *testing.T) {
			reported, audits, checklist := artifactObjectiveFixture()
			candidate := reported.Result
			reported.Result = "The originals are ready."
			original := terminalContent{content: encodePresentationReport(t, reported), protected: true}
			repair := newVerifiedPresentationRepair(original, audits, nil, checklist)
			require.NotNil(t, repair)
			before := taskresult.CloneOutcome(repair.outcome)
			proposed, _, _ := artifactObjectiveFixture()
			switch change {
			case "artifact":
				proposed.CompletedItems[1].Output.ArtifactRefs[0] += "_invented"
			case "receipt":
				proposed.CompletedItems[1].ReceiptIDs = []string{"inv_photo_1"}
			case "missing item":
				proposed.CompletedItems = proposed.CompletedItems[:2]
			case "reordered items":
				proposed.CompletedItems[0], proposed.CompletedItems[2] = proposed.CompletedItems[2], proposed.CompletedItems[0]
			}
			if change != "none" {
				candidate = encodePresentationReport(t, proposed)
			}
			got := repair.accept(terminalContent{content: candidate})
			if change == "none" {
				_, accepted := extractObjectiveOutcome(got.content, audits, true, checklist)
				require.Equal(t, candidate, accepted.UserSummary)
				accepted.UserSummary = ""
				require.Equal(t, before, accepted, "presentation can replace only the natural answer")
				require.True(t, got.protected, "presentation cannot downgrade protected evidence")
			} else {
				require.Equal(t, original, got)
			}
			require.Equal(t, before, repair.outcome, "the retained proof is immutable")
		})
	}
}

func TestPresentationRepairDoesNotFreezeLaterUserSteering(t *testing.T) {
	report := reportedObjectiveOutcome{
		Status: "succeeded", Result: "Ready.",
		CompletedItems: []reportedObjectiveItem{{
			ObjectiveID: "objective_1", Output: &taskresult.ObjectiveOutput{Kind: "text", Text: "Original result."},
		}},
	}
	initial := encodePresentationReport(t, report)
	report.Result = "Original result."
	corrected := report.Result
	report.CompletedItems[0].Output.Text, report.Result = "New user-requested result.", "New user-requested result."
	updated := encodePresentationReport(t, report)
	provider := &objectiveRepairCaptureProvider{responses: []*providers.LLMResponse{
		{Content: initial}, {Content: corrected}, {Content: updated},
	}}
	al, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	provider.onCall = func(call int) {
		if call != 2 {
			return
		}
		al.turns.activeTurnStates.Range(func(key, value any) bool {
			state, ok := value.(*turnState)
			scope, scopeOK := key.(runtimeSessionScope)
			if !ok || !scopeOK || state.agent == nil || state.agent.ID != "beta" {
				return true
			}
			if err := al.steering.pushScopeWithSender(scope, providers.Message{
				Role: "user", Content: "Replace the earlier request with the new user-requested result.",
			}, "test-user"); err != nil {
				t.Errorf("push presentation steering: %v", err)
			}
			return false
		})
	}
	alpha, _ := al.registry.GetAgent("alpha")
	parent := &turnState{
		ctx: t.Context(), turnID: "parent-presentation-steering",
		pendingResults: make(chan *toolshared.ToolResult, 4),
		concurrencySem: make(chan struct{}, testMaxConcurrentSubTurns),
		session:        &ephemeralSessionStore{}, agent: alpha,
		opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{SessionKey: "parent-presentation-steering"}}),
	}
	result, err := spawnSubTurn(t.Context(), al, parent, SubTurnConfig{
		TargetAgentID:  "beta",
		DeliveryMode:   toolshared.AsyncDeliveryUserOnly,
		TaskPrompt:     "Return the original result.",
		ObjectiveItems: []toolshared.ObjectiveSpec{{Item: "Return the requested result", Kind: "result"}},
	})
	require.NoError(t, err)
	require.Equal(t, "New user-requested result.", result.ForUser)
	counts := provider.toolDefinitionCounts()
	require.Len(t, counts, 3)
	require.Zero(t, counts[1])
	require.Positive(t, counts[2], "new user guidance restores ordinary execution")
}
