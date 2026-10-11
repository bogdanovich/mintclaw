package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// This fixture exercises the real registry and turn pipeline without touching
// an OS desktop. Application lifetime and automation ownership are independent.
type dispositionSmokeTool struct {
	name            string
	action          string
	live            bool
	applicationOpen bool
	executions      int
	cleanupCalls    int
}

func (tool *dispositionSmokeTool) Name() string   { return tool.name }
func (*dispositionSmokeTool) Description() string { return "fixture resource lifecycle" }
func (tool *dispositionSmokeTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"operation": map[string]any{"type": "string", "enum": []string{tool.action}},
		},
		"required": []string{"operation"},
	}
}

func (tool *dispositionSmokeTool) Execute(_ context.Context, args map[string]any) *toolshared.ToolResult {
	if args["operation"] != tool.action {
		return toolshared.ErrorResult("unexpected disposition")
	}
	tool.executions++
	tool.live = false
	if tool.action == "close" {
		tool.applicationOpen = false
	}
	return toolshared.SilentResult("fixture disposition confirmed").WithDeliverable(&taskresult.Deliverable{
		LifecycleReceipts: []taskresult.Receipt{{
			ID: "receipt_" + tool.name, Kind: taskresult.ReceiptKindResourceCleanup,
			Tool: tool.name, Target: "fixture:" + tool.name, Action: tool.action,
			Summary: "Fixture reached its requested disposition.",
		}},
	})
}

func (tool *dispositionSmokeTool) VerifyResourceDisposition(
	_ context.Context, args map[string]any, _ *toolshared.ToolResult,
) bool {
	return !tool.live && args["operation"] == tool.action
}

func (tool *dispositionSmokeTool) CleanupTurn(context.Context) error {
	tool.cleanupCalls++
	tool.live = false
	if tool.action == "close" {
		tool.applicationOpen = false
	}
	return nil
}

func (tool *dispositionSmokeTool) TurnFinalizationRequirement(
	context.Context,
) (tools.TurnFinalizationRequirement, bool, error) {
	return tools.TurnFinalizationRequirement{
		RecoveryKind: taskresult.ObjectiveKindResourceDisposition,
		Instruction:  fmt.Sprintf("Use %s to %s its fixture resource.", tool.name, tool.action),
	}, tool.live, nil
}

func (tool *dispositionSmokeTool) ObjectiveRecoveryParameters(kind string) (map[string]any, bool) {
	return tool.Parameters(), kind == taskresult.ObjectiveKindResourceDisposition
}

func TestComputerFoundationLifecycleSmoke(t *testing.T) {
	for _, test := range []struct {
		name         string
		mixed        bool
		omitRelease  bool
		wantCalls    int
		wantReleases int
	}{
		{name: "attached release leaves application open", wantCalls: 3, wantReleases: 1},
		{name: "mixed browser close and desktop release", mixed: true, wantCalls: 3, wantReleases: 1},
		{name: "incomplete mixed disposition fails closed", mixed: true, omitRelease: true, wantCalls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			desktop := &dispositionSmokeTool{
				name: "computer_session", action: "release", live: true, applicationOpen: true,
			}
			browser := &dispositionSmokeTool{
				name: "browser_session", action: "close", live: true, applicationOpen: true,
			}
			var calls []providers.ToolCall
			if test.mixed {
				calls = append(calls, providers.ToolCall{
					ID: "close_browser", Name: browser.Name(), Arguments: map[string]any{"operation": browser.action},
				})
			}
			if !test.omitRelease {
				calls = append(calls, providers.ToolCall{
					ID: "release_desktop", Name: desktop.Name(), Arguments: map[string]any{"operation": desktop.action},
				})
			}
			provider := &sequenceProvider{responses: []*providers.LLMResponse{
				{Content: "Task complete.", FinishReason: "stop"},
				{ToolCalls: calls, FinishReason: "tool_calls"},
				{Content: "Automation released; user application lifetime unchanged.", FinishReason: "stop"},
			}}
			al, agent, cleanup := newTurnCoordTestLoop(t, provider)
			defer cleanup()
			agent.MaxIterations = 1
			agent.Tools.Register(desktop)
			if test.mixed {
				agent.Tools.Register(browser)
			}
			ts := newTurnState(agent, makeTestTurnSpec("computer-foundation-smoke"), turnEventScope{
				turnID: "computer-foundation-smoke-turn", context: newTurnContext(nil, nil, nil),
			})
			result, err := runTestTurn(al, t.Context(), ts, newTestPipeline(al))
			if err != nil || result.status != TurnEndStatusCompleted || provider.callCount != test.wantCalls ||
				desktop.executions != test.wantReleases || desktop.live || !desktop.applicationOpen || desktop.cleanupCalls != 1 {
				t.Fatalf("smoke result = %#v, %v; model calls=%d desktop=%#v", result, err, provider.callCount, desktop)
			}
			if test.mixed &&
				(browser.live || browser.applicationOpen || browser.executions != 1 || browser.cleanupCalls != 1) {
				t.Fatalf("browser fixture disposition = %#v", browser)
			}
			if test.omitRelease {
				if !strings.Contains(result.finalContent, "could not be verified for every live resource") {
					t.Fatalf("incomplete disposition reported success: %q", result.finalContent)
				}
			} else if result.finalContent != "Automation released; user application lifetime unchanged." ||
				len(provider.toolRequests[2]) != 0 {
				t.Fatalf("final synthesis = %q, tools=%#v", result.finalContent, provider.toolRequests)
			}
		})
	}
}

func TestResourceDispositionIgnoresPostExecutionProducerFlag(t *testing.T) {
	exec := &turnExecution{objectiveRepairToolKind: taskresult.ObjectiveKindResourceDisposition}
	result := toolshared.SilentResult("not runtime proof")
	result.Control.ResourceDispositionVerified = true
	if successfulResourceDisposition(exec, result, false) {
		t.Fatal("a hook or producer flag replaced the original invocation proof")
	}
	if !successfulResourceDisposition(exec, result, true) {
		t.Fatal("original verified invocation was not accepted")
	}
	result.IsError = true
	if successfulResourceDisposition(exec, result, true) {
		t.Fatal("failed result was accepted")
	}
}
