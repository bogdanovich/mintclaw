package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// Domain projections are fixtures; the tool-only fences and one-shot context
// lifecycle are production implementations. Native job behavior is tested in tools.
type semanticDialogueTestTool struct {
	*tools.DocumentTool
	results map[string]*toolshared.ToolResult
}

func (semanticDialogueTestTool) Name() string { return "semantic_document_test" }

func (tool semanticDialogueTestTool) Execute(_ context.Context, args map[string]any) *toolshared.ToolResult {
	action, _ := args["action"].(string)
	if action == "form" {
		action, _ = args["form_action"].(string)
	}
	return tool.results[action]
}

func TestDocumentSemanticNotesSurviveBoundedBrowsingWithoutSourceText(t *testing.T) {
	for _, count := range []int{2, 250} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			loop, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
			defer cleanup()
			source := "media://00000000-0000-0000-0000-000000000123"
			digest := strings.Repeat("a", 64)
			jobID := "form_job_semantic_test"
			firstID, laterID := "field_"+strings.Repeat("b", 64), "field_"+strings.Repeat("c", 64)
			mapping := func(fieldID string, selected bool) map[string]any {
				return map[string]any{
					"ready_for_review": false, "missing_field_count": count,
					"field_window": map[string]any{
						"limit": 8, "total": count, "truncated": count > 8, "selected": selected,
					},
					"candidate_fields": []any{map[string]any{
						"field_id": fieldID, "kind": "text", "blocker": "field_unresolved",
					}},
				}
			}
			projection := func(action string, fieldID string, selected bool) *toolshared.ToolResult {
				data := map[string]any{
					"schema_version": "mintclaw.document_form_workflow.v1", "operation": "form", "form_action": action,
					"mapping": mapping(fieldID, selected),
				}
				if action == "discover" {
					data["source_ref"], data["field_schema_digest"] = source, digest
				} else {
					data["job"] = map[string]any{"job_id": jobID, "state": "prepared", "needs_initial_plan": true}
				}
				encoded, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				return &toolshared.ToolResult{ForLLM: string(encoded)}
			}
			private := "PDFI3_SOURCE_PRIVATE_63d9"
			agent.Tools.Register(semanticDialogueTestTool{
				DocumentTool: tools.NewDocumentTool(),
				results: map[string]*toolshared.ToolResult{
					"extract": {
						ForLLM:      `{"operation":"extract","text_truncated":true}`,
						ContextText: "[page 1] Parties and eligibility. [page 2] Declaration. Existing value: " + private,
					},
					"discover": projection("discover", firstID, false),
					"start":    projection("start", firstID, false),
					"status":   projection("status", laterID, true),
					"evidence": {
						ForLLM:      projection("evidence", laterID, true).ForLLM,
						ContextText: "[page 3] Selected party's section. Existing value: " + private,
					},
					"collect": projection("collect", laterID, true),
				},
			})
			pipeline := newTestPipeline(loop)
			ts := newTurnState(agent, makeTestTurnSpec("semantic-dialog"), turnEventScope{
				turnID: "semantic-dialog-turn", context: newTurnContext(nil, nil, nil),
			})
			exec, err := pipeline.SetupTurn(t.Context(), ts)
			if err != nil {
				t.Fatal(err)
			}
			summary := "This form records parties, eligibility, and a declaration."
			plan := "I will clarify applicable sections and ask personal facts through protected questions before review."
			calls := []map[string]any{
				{"action": "extract", "source": source, "pages": []any{1, 2}, "max_characters": 4000},
				{
					"action":          "form",
					"form_action":     "discover",
					"source":          source,
					"form_summary":    summary,
					"collection_plan": plan,
				},
				{"action": "form", "form_action": "start", "source": source, "field_schema_digest": digest},
				{"action": "form", "form_action": "status", "job_id": jobID, "pages": []any{3}},
				{"action": "form", "form_action": "evidence", "job_id": jobID, "pages": []any{3}},
				{
					"action":          "form",
					"form_action":     "collect",
					"job_id":          jobID,
					"field_id":        laterID,
					"question":        "What test fact belongs to the selected party?",
					"form_summary":    summary,
					"collection_plan": plan,
				},
			}
			for index, args := range calls {
				llm := newLLMIterationState(index + 1)
				if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, llm); err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(llm.callMessages)
				if err != nil {
					t.Fatal(err)
				}
				if (index == 1 || index == 5) && !strings.Contains(string(encoded), private) {
					t.Fatal("reading evidence was not available for its one model call")
				}
				if index >= 2 && (!strings.Contains(string(encoded), summary) ||
					(index != 5 && strings.Contains(string(encoded), private))) {
					t.Fatal("source text survived or value-free interpretation notes were lost")
				}
				if index >= 2 && !exec.protectedAnswerContinuation.pending() {
					t.Fatal("planning transition released the protected question fence")
				}
				llm.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
					ID: fmt.Sprintf("semantic-call-%d", index), Name: "semantic_document_test", Arguments: args,
				}}}
				model, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm)
				if err != nil || model.Control != turnStepExecuteTools {
					t.Fatalf("model transition %d = %#v, %v", index, model, err)
				}
				outcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
				if outcome.TurnErr != nil || outcome.Control != turnStepContinue {
					t.Fatalf("tool transition %d = %#v", index, outcome)
				}
			}
			if exec.protectedAnswerContinuation.pending() {
				t.Fatal("selected protected-question transition did not complete")
			}
			encoded, err := json.Marshal(exec.messages)
			if err != nil || strings.Contains(string(encoded), private) {
				t.Fatal("later-page evidence survived its single model call")
			}
		})
	}
}
