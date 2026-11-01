package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func documentFieldsTestObservation() toolshared.DocumentFieldsObservation {
	return toolshared.DocumentFieldsObservation{
		Operation: "fields", SourceSHA256: strings.Repeat("a", 64),
		SchemaFields: 5, ReturnedFields: 5, RequiredFields: 2,
	}
}

func TestDocumentFieldsTraceIsContentFreeAndBounded(t *testing.T) {
	started := time.Now()
	value := ToolExecEndPayload{
		Tool: "document", ToolCallID: "call-fields", ToolCallGeneration: "generation-fields",
		ResultHash: "native-result-hash", DiagnosticResult: "PRIVATE_FIELD_PATH_VALUE",
		DocumentFields: documentFieldsTestObservation(),
	}
	record, _, ok := runtimeEventRecord(
		traceCaptureSettings{contentMode: diagnostictrace.ContentMetadataOnly},
		&activeTraceCapture{startedAt: started, turnID: "turn-fields"},
		runtimeevents.Event{
			ID: "event-fields", Kind: runtimeevents.KindAgentToolExecEnd,
			Time: started.Add(time.Millisecond), Payload: value,
		},
	)
	var payload diagnostictrace.ToolPayload
	if !ok || json.Unmarshal(record.Data, &payload) != nil || payload.DocumentFields == nil ||
		!payload.DocumentFields.Valid() || payload.DocumentFields.SourceSHA256 != value.DocumentFields.SourceSHA256 ||
		record.Correlation.ToolCallID != value.ToolCallID ||
		record.Correlation.ToolCallGeneration != value.ToolCallGeneration ||
		payload.ResultHash != value.ResultHash || bytes.Contains(record.Data, []byte("PRIVATE_FIELD_PATH_VALUE")) {
		t.Fatalf("native field trace = %#v / %s", record, record.Data)
	}
	encoded, err := json.Marshal(value)
	if err != nil || bytes.Contains(encoded, []byte(value.DocumentFields.SourceSHA256)) {
		t.Fatalf("runtime log retained transient read: %s / %v", encoded, err)
	}
	for _, test := range []struct {
		name string
		edit func(*ToolExecEndPayload)
	}{
		{"other tool", func(v *ToolExecEndPayload) { v.Tool = "read_file" }},
		{"error", func(v *ToolExecEndPayload) { v.IsError = true }},
		{"async", func(v *ToolExecEndPayload) { v.Async = true }},
		{"suspended", func(v *ToolExecEndPayload) { v.Suspended = true }},
		{"capabilities", func(v *ToolExecEndPayload) { v.DocumentFields.Operation = "capabilities" }},
		{"inspect", func(v *ToolExecEndPayload) { v.DocumentFields.Operation = "inspect" }},
		{"path as digest", func(v *ToolExecEndPayload) { v.DocumentFields.SourceSHA256 = "/private/source.pdf" }},
		{"upper digest", func(v *ToolExecEndPayload) { v.DocumentFields.SourceSHA256 = strings.Repeat("A", 64) }},
		{"zero fields", func(v *ToolExecEndPayload) { v.DocumentFields.SchemaFields = 0 }},
		{"too many fields", func(v *ToolExecEndPayload) { v.DocumentFields.SchemaFields = 257 }},
		{"negative count", func(v *ToolExecEndPayload) { v.DocumentFields.ValuesPresent = -1 }},
		{"large count", func(v *ToolExecEndPayload) { v.DocumentFields.DefaultsPresent = 6 }},
		{"short fields result", func(v *ToolExecEndPayload) { v.DocumentFields.ReturnedFields = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := value
			test.edit(&invalid)
			if diagnosticDocumentFields(invalid) != nil {
				t.Fatal("invalid/unsuccessful read became native trace evidence")
			}
		})
	}
	value.DocumentFields.Operation = "form.discover"
	value.DocumentFields.ReturnedFields = 1
	got := diagnosticDocumentFields(value)
	if got == nil || got.SchemaFields != 5 || got.ReturnedFields != 1 {
		t.Fatalf("window was conflated with complete model-visible schema: %#v", got)
	}
}

type documentFieldsMutationHook struct {
	respond bool
}

func (h *documentFieldsMutationHook) BeforeTool(
	_ context.Context, request *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	if h.respond {
		request.HookResult = &toolshared.ToolResult{
			ForLLM:         "hook response",
			DocumentFields: documentFieldsTestObservation(),
		}
		return request, HookDecision{Action: HookActionRespond}, nil
	}
	return request, HookDecision{Action: HookActionContinue}, nil
}

func (*documentFieldsMutationHook) AfterTool(
	_ context.Context, response *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	response.Result.DocumentFields = documentFieldsTestObservation()
	response.Result.DocumentFields.SourceSHA256 = strings.Repeat("b", 64)
	response.Result.IsError = false
	return response, HookDecision{Action: HookActionModify}, nil
}

func TestDocumentFieldsTraceUsesOriginatingInvocationNotHooks(t *testing.T) {
	for _, test := range []struct {
		name    string
		failed  bool
		respond bool
		want    bool
	}{
		{name: "native read survives later mutation", want: true},
		{name: "hook cannot promote failed invocation", failed: true},
		{name: "hook-only response cannot claim a read", respond: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool := &fixedToolResultTool{name: "document", result: &toolshared.ToolResult{
				ForLLM: "native safe report", IsError: test.failed, DocumentFields: documentFieldsTestObservation(),
			}}
			registry := tools.NewToolRegistry()
			registry.Register(tool)
			agent := &AgentInstance{
				ID: "main", Tools: registry, Sessions: session.NewMemoryStore(),
				ToolLoopDetection: loopguard.DefaultConfig(),
			}
			ts := &turnState{
				agent: agent, agentID: agent.ID, turnID: "turn-fields", sessionKey: "session-fields",
				opts: freezeTurnInput(turnSpec{NoHistory: true}),
			}
			exec := newTurnExecution(agent, ts.opts, nil, nil, nil)
			llm := newLLMIterationState(1)
			llm.normalizedToolCalls = []providers.ToolCall{
				{ID: "call-fields", Name: "document", Arguments: map[string]any{}},
			}
			hooks := NewHookManager(nil)
			t.Cleanup(hooks.Close)
			if err := hooks.Mount(
				NamedHook("mutate-fields", &documentFieldsMutationHook{respond: test.respond}),
			); err != nil {
				t.Fatal(err)
			}
			emitter := &captureRuntimeEmitter{}
			pipeline := &Pipeline{events: emitter, Interaction: PipelineInteractionServices{Hooks: hooks}}
			if outcome := pipeline.ExecuteTools(
				t.Context(),
				t.Context(),
				ts,
				exec,
				llm,
			); outcome.Control != turnStepContinue {
				t.Fatalf("tool outcome = %#v", outcome)
			}
			if test.respond && tool.executions != 0 || !test.respond && tool.executions != 1 {
				t.Fatalf("native executions = %d", tool.executions)
			}
			for _, event := range emitter.events {
				value, ok := event.payload.(ToolExecEndPayload)
				if event.kind != runtimeevents.KindAgentToolExecEnd || !ok {
					continue
				}
				got := diagnosticDocumentFields(value)
				if (got != nil) != test.want || got != nil && got.SourceSHA256 != strings.Repeat("a", 64) {
					t.Fatalf("hook changed originating native read: %#v", got)
				}
				return
			}
			t.Fatal("missing tool execution end event")
		})
	}
}
