package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// Verify the real response-only boundary retains the earlier message and the
// field-specific facts needed by its renderer. Live delivery is qualified separately.
func TestDocumentIntakeResponseRendererRetainsDeliveredIntroductionAndActualFieldContext(t *testing.T) {
	loop, agent, cleanup := newTurnCoordTestLoop(t, &sequenceProvider{})
	defer cleanup()
	agent.Tools.Register(semanticDialogueTestTool{
		DocumentTool: tools.NewDocumentTool(),
		results: map[string]*toolshared.ToolResult{"collect": {
			ForLLM: `{"schema_version":"mintclaw.document_form_workflow.v1","operation":"form",` +
				`"form_action":"collect","job":{"job_id":"form_job_test"},"intake":{` +
				`"form_summary":"A generic form with two people.","question":"Which facts are missing?",` +
				`"accepted_count":1,"accepted_fields":[{"field_id":"city","label":"Applicant city","kind":"text"}]}}`,
		}},
	})
	pipeline := newTestPipeline(loop)
	ts := newTurnState(agent, makeTestTurnSpec("intake-render-context"), turnEventScope{
		turnID: "intake-render-context", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(t.Context(), ts)
	if err != nil {
		t.Fatal(err)
	}
	const introduction = "I already explained this generic form and its two people."
	exec.messages = append(exec.messages,
		providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "delivered-introduction", Name: "message", Arguments: map[string]any{
				"content": introduction, "delivery_intent": "immediate_continue",
			},
		}}},
		providers.Message{Role: "tool", ToolCallID: "delivered-introduction", Content: "Message delivered."},
	)
	llm := newLLMIterationState(1)
	llm.response = &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		ID: "collect", Name: "semantic_document_test",
		Arguments: map[string]any{"action": "form", "form_action": "collect", "job_id": "form_job_test"},
	}}}
	model, err := pipeline.normalizeAndDispatchLLMResponse(t.Context(), ts, exec, llm)
	if err != nil || model.Control != turnStepExecuteTools {
		t.Fatal("collection did not reach its trusted tool")
	}
	outcome := pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
	if outcome.TurnErr != nil || outcome.Control != turnStepContinue {
		t.Fatal("trusted collection did not reach its renderer")
	}
	next := newLLMIterationState(2)
	if _, err = pipeline.prepareLLMRequest(t.Context(), ts, exec, next); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(next.callMessages)
	if err != nil || len(next.providerToolDefs) != 0 || !strings.Contains(string(encoded), introduction) ||
		!strings.Contains(
			string(encoded),
			"already delivered",
		) || !strings.Contains(string(encoded), "accepted_fields") ||
		!strings.Contains(string(encoded), "Applicant city") {
		t.Fatal("response-only rendering lost prior delivery, actual acceptance or the no-repeat contract")
	}
}
