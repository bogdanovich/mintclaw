package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browseraction"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func browserInputTestObservation(
	t *testing.T,
) (browseraction.InputTargetObservation, interactions.ProtectedAnswerBinding) {
	t.Helper()
	fixture := newBrowserInputNativeFixture(t, browserpolicy.DecisionAllow)
	target, err := fixture.source.InspectProtectedFillBinding(fixture.ctx, fixture.source.latestSession.Owner,
		fixture.request.Binding.Token)
	if err != nil {
		t.Fatal(err)
	}
	return target.Observation, fixture.request.Binding
}

func TestBrowserInputTargetTraceIsBoundedNativeMetadata(t *testing.T) {
	observation, _ := browserInputTestObservation(t)
	started := time.Now()
	value := ToolExecEndPayload{
		Tool: "browser_input", ToolCallID: "call-input", ToolCallGeneration: "generation-input",
		Suspended: true, InteractionID: "interaction-input", BrowserInputTarget: observation,
		DiagnosticResult: "PRIVATE_INPUT_CONTENT_4591",
	}
	for _, mode := range []diagnostictrace.ContentMode{diagnostictrace.ContentMetadataOnly, diagnostictrace.ContentRedacted} {
		record, _, ok := runtimeEventRecord(traceCaptureSettings{contentMode: mode},
			&activeTraceCapture{startedAt: started, turnID: "turn-input"}, runtimeevents.Event{
				ID: "event-input", Kind: runtimeevents.KindAgentToolExecEnd,
				Time: started.Add(time.Millisecond), Payload: value,
			})
		var payload diagnostictrace.ToolPayload
		if !ok || json.Unmarshal(record.Data, &payload) != nil || payload.BrowserInputTarget == nil ||
			payload.BrowserInputTarget.Target != observation || !payload.BrowserInputTarget.Valid() ||
			record.Correlation.ToolCallID != value.ToolCallID ||
			record.Correlation.ToolCallGeneration != value.ToolCallGeneration ||
			bytes.Contains(record.Data, []byte(value.DiagnosticResult)) || payload.ResultPreview != "" {
			t.Fatal("native input target was missing, unbound or contained private content")
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil || bytes.Contains(encoded, []byte(observation.BindingSHA256)) {
		t.Fatal("ordinary runtime log retained transient target metadata")
	}
	for _, test := range []struct {
		name string
		edit func(*ToolExecEndPayload)
	}{
		{"other tool", func(v *ToolExecEndPayload) { v.Tool = "document" }},
		{"failed", func(v *ToolExecEndPayload) { v.IsError = true }},
		{"async", func(v *ToolExecEndPayload) { v.Async = true }},
		{"not suspended", func(v *ToolExecEndPayload) { v.Suspended = false }},
		{"not durable", func(v *ToolExecEndPayload) { v.InteractionID = "" }},
		{"invalid interaction", func(v *ToolExecEndPayload) { v.InteractionID = "private/path" }},
		{"raw label", func(v *ToolExecEndPayload) { v.BrowserInputTarget.ElementNameSHA256 = "private label" }},
		{"invalid binding", func(v *ToolExecEndPayload) { v.BrowserInputTarget.BindingSHA256 = "invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := value
			test.edit(&invalid)
			if diagnosticBrowserInputTarget(invalid) != nil {
				t.Fatal("invalid or unowned target was projected as evidence")
			}
		})
	}
}

type browserInputTargetMutationHook struct {
	respond  bool
	result   *toolshared.ToolResult
	retarget bool
	drop     bool
}

func (hook *browserInputTargetMutationHook) BeforeTool(
	_ context.Context, request *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	if hook.respond {
		request.HookResult = hook.result
		return request, HookDecision{Action: HookActionRespond}, nil
	}
	return request, HookDecision{Action: HookActionContinue}, nil
}

func (hook *browserInputTargetMutationHook) AfterTool(
	_ context.Context, response *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	response.Result.BrowserInputTarget.ElementNameSHA256 = strings.Repeat("b", 64)
	response.Result.IsError = false
	if hook.retarget {
		response.Result.Control.Suspension.ProtectedAnswer.Token += "tampered"
	}
	if hook.drop {
		response.Result.Control.Suspension = nil
	}
	return response, HookDecision{Action: HookActionModify}, nil
}

func TestBrowserInputTargetUsesNativeInvocationAndDurableQuestionNotHooks(t *testing.T) {
	for _, test := range []struct {
		name     string
		failed   bool
		respond  bool
		retarget bool
		drop     bool
		unowned  bool
		want     bool
	}{
		{name: "native survives result mutation", want: true},
		{name: "failed native cannot be promoted", failed: true},
		{name: "hook-only response", respond: true},
		{name: "changed binding", retarget: true},
		{name: "dropped suspension", drop: true},
		{name: "suspension not durably owned", unowned: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation, binding := browserInputTestObservation(t)
			result := &toolshared.ToolResult{
				ForLLM: "native safe report", IsError: test.failed, BrowserInputTarget: observation,
				Control: toolshared.ToolControl{Suspension: &interactions.SuspensionRequest{
					Kind: interactions.KindQuestion, Timeout: time.Minute,
					Questions:       []interactions.Question{{ID: "browser_value", Question: "What value?"}},
					ProtectedAnswer: &binding,
				}},
			}
			tool := &fixedToolResultTool{name: "browser_input", result: result}
			registry := tools.NewToolRegistry()
			registry.Register(tool)
			agent := &AgentInstance{ID: "main", Tools: registry, Sessions: session.NewMemoryStore()}
			ts := &turnState{
				agent: agent, agentID: agent.ID, turnID: "turn-input", sessionKey: "session-input",
				opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{SessionKey: "session-input"}}),
			}
			exec := newTurnExecution(agent, ts.opts, nil, nil, nil)
			llm := newLLMIterationState(1)
			llm.normalizedToolCalls = []providers.ToolCall{
				{ID: "call-input", Name: "browser_input", Arguments: map[string]any{}},
			}
			llm.assistantToolCallsPersisted = true
			hooks := NewHookManager(nil)
			t.Cleanup(hooks.Close)
			if err := hooks.Mount(NamedHook("mutate-input", &browserInputTargetMutationHook{
				respond: test.respond, result: result, retarget: test.retarget, drop: test.drop,
			})); err != nil {
				t.Fatal(err)
			}
			emitter := &captureRuntimeEmitter{}
			manager := &fakeToolSuspensionManager{
				disposition: ToolSuspensionDisposition{InteractionID: "interaction-input", Durable: !test.unowned},
			}
			pipeline := &Pipeline{
				events:      emitter,
				Interaction: PipelineInteractionServices{Hooks: hooks, Suspension: manager},
			}
			_ = pipeline.ExecuteTools(t.Context(), t.Context(), ts, exec, llm)
			if test.respond && tool.executions != 0 || !test.respond && tool.executions != 1 {
				t.Fatal("unexpected native execution count")
			}
			seen := false
			for _, event := range emitter.events {
				value, ok := event.payload.(ToolExecEndPayload)
				if event.kind != runtimeevents.KindAgentToolExecEnd || !ok {
					continue
				}
				seen = true
				payload := diagnosticBrowserInputTarget(value)
				if (payload != nil) != test.want || payload != nil && payload.Target != observation {
					t.Fatal("hook or unowned suspension changed native input target evidence")
				}
			}
			if !seen {
				t.Fatal("missing execution result event")
			}
		})
	}
}
