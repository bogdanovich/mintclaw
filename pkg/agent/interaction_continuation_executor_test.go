package agent

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestRepairJournaledToolPair(t *testing.T) {
	call := providers.ToolCall{ID: "call-approved", Name: "protected"}
	origin := providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{call}}
	result := providers.Message{
		Role:             "tool",
		ToolCallID:       call.ID,
		Content:          "completed",
		ToolResultStatus: providers.ToolResultStatusSuccess,
	}
	history := []providers.Message{origin, result}

	for _, test := range []struct {
		name     string
		messages []providers.Message
	}{
		{
			name: "replace unresolved result",
			messages: []providers.Message{
				{Role: "system", Content: "system"},
				origin,
				{
					Role:             "tool",
					ToolCallID:       call.ID,
					Content:          "unresolved",
					ToolResultStatus: providers.ToolResultStatusUnresolved,
				},
				result,
			},
		},
		{
			name: "restore pair without context history",
			messages: []providers.Message{
				{Role: "system", Content: "system"},
				result,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			exec := &turnExecution{messages: append([]providers.Message(nil), test.messages...)}
			if err := repairJournaledToolPair(exec, history, call.ID); err != nil {
				t.Fatal(err)
			}

			callCount := 0
			resultCount := 0
			for _, message := range exec.messages {
				if messageContainsToolCall(message, call.ID) {
					callCount++
				}
				if message.Role == "tool" && message.ToolCallID == call.ID {
					resultCount++
					if message.Content != result.Content ||
						message.ToolResultStatus != providers.ToolResultStatusSuccess {
						t.Fatalf("tool result = %#v", message)
					}
				}
			}
			if callCount != 1 || resultCount != 1 {
				t.Fatalf("provider tool pair counts = call:%d result:%d", callCount, resultCount)
			}
		})
	}
}

func TestInteractionContinuationConfigureKeepsResumeInboundContext(t *testing.T) {
	resumeInbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "approval-chat", SenderID: "approver", MessageID: "approval-response",
	}
	approval := &ToolApprovalGrant{InteractionID: "interaction-1"}
	executor := &interactionContinuationExecutor{
		approvedTool: &providers.ToolCall{ID: "call-approved"},
		approval:     approval,
	}
	opts := processOptions{Dispatch: DispatchRequest{InboundContext: resumeInbound}}

	executor.configure(&opts)

	if opts.ApprovalGrant != approval {
		t.Fatalf("approval grant = %#v, want configured grant", opts.ApprovalGrant)
	}
	if opts.Dispatch.InboundContext != resumeInbound {
		t.Fatalf("resume inbound context changed: %#v", opts.Dispatch.InboundContext)
	}
}
