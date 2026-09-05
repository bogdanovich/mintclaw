package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestRepairJournaledToolPair(t *testing.T) {
	call := providers.ToolCall{ID: "call-approved", Name: "protected"}
	origin := providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{call}}
	durableResult := providers.Message{
		Role:             "tool",
		ToolCallID:       call.ID,
		Content:          "protected result",
		ToolResultStatus: providers.ToolResultStatusSuccess,
	}
	liveResult := durableResult
	liveResult.Content = "full live result"
	history := []providers.Message{origin, durableResult}

	for _, test := range []struct {
		name      string
		history   []providers.Message
		messages  []providers.Message
		wantPairs int
	}{
		{
			name: "replace unresolved result while preserving live content", history: history, wantPairs: 1,
			messages: []providers.Message{
				{Role: "system", Content: "system"},
				origin,
				{
					Role:             "tool",
					ToolCallID:       call.ID,
					Content:          "unresolved",
					ToolResultStatus: providers.ToolResultStatusUnresolved,
				},
				liveResult,
			},
		},
		{
			name: "restore pair without context history", history: history, wantPairs: 1,
			messages: []providers.Message{
				{Role: "system", Content: "system"},
				liveResult,
			},
		},
		{
			name: "scope reused IDs to latest tool block", wantPairs: 2,
			history: []providers.Message{
				origin,
				{Role: "tool", ToolCallID: call.ID, Content: "older result"},
				{Role: "user", Content: "next turn"},
				origin,
				durableResult,
			},
			messages: []providers.Message{
				origin,
				{Role: "tool", ToolCallID: call.ID, Content: "older result"},
				{Role: "user", Content: "next turn"},
				origin,
				{
					Role:             "tool",
					ToolCallID:       call.ID,
					Content:          "unresolved",
					ToolResultStatus: providers.ToolResultStatusUnresolved,
				},
				liveResult,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			exec := &turnExecution{messages: append([]providers.Message(nil), test.messages...)}
			if err := repairJournaledToolPair(exec, test.history, call.ID); err != nil {
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
					if resultCount == test.wantPairs && (message.Content != liveResult.Content ||
						message.ToolResultStatus != providers.ToolResultStatusSuccess) {
						t.Fatalf("tool result = %#v", message)
					}
				}
			}
			if callCount != test.wantPairs || resultCount != test.wantPairs {
				t.Fatalf("provider tool pair counts = call:%d result:%d", callCount, resultCount)
			}
		})
	}
}

func TestApprovedToolExecutionIdentityIsScopedAndRestored(t *testing.T) {
	resumeInbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "approval-chat", MessageID: "approval-response",
		ReplyToMessageID: "approval-reply",
	}
	origin := &bus.InboundContext{
		Channel: "discord", ChatID: "origin-chat", MessageID: "origin-message",
		ReplyToMessageID: "origin-reply",
	}
	ts := &turnState{
		agent: &AgentInstance{ID: "main"}, channel: resumeInbound.Channel, chatID: resumeInbound.ChatID,
		workspace: "workspace", sessionKey: "session",
		opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{InboundContext: resumeInbound}}),
	}
	registeredInbound := ts.opts.Dispatch.InboundContext
	if registeredInbound == nil || registeredInbound == resumeInbound {
		t.Fatalf("registered turn identity was not detached: %#v", registeredInbound)
	}

	executionCtx := withApprovedToolExecutionIdentity(context.Background(), origin)
	assertToolIdentity(t, toolExecutionContextForTurn(executionCtx, ts), origin)
	assertToolIdentity(t, toolExecutionContextForTurn(context.Background(), ts), registeredInbound)
	if ts.channel != resumeInbound.Channel || ts.chatID != resumeInbound.ChatID ||
		ts.opts.Dispatch.InboundContext != registeredInbound {
		t.Fatalf("registered turn identity mutated: %#v", ts)
	}
}

func assertToolIdentity(t *testing.T, ctx context.Context, want *bus.InboundContext) {
	t.Helper()
	if toolshared.ToolChannel(ctx) != want.Channel ||
		toolshared.ToolChatID(ctx) != want.ChatID ||
		toolshared.ToolMessageID(ctx) != want.MessageID ||
		toolshared.ToolReplyToMessageID(ctx) != want.ReplyToMessageID {
		t.Fatalf(
			"tool identity = %q/%q/%q/%q, want %q/%q/%q/%q",
			toolshared.ToolChannel(ctx),
			toolshared.ToolChatID(ctx),
			toolshared.ToolMessageID(ctx),
			toolshared.ToolReplyToMessageID(ctx),
			want.Channel,
			want.ChatID,
			want.MessageID,
			want.ReplyToMessageID,
		)
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
	opts := turnSpec{Dispatch: DispatchRequest{InboundContext: resumeInbound}}

	executor.configure(&opts)

	if opts.ApprovalGrant != approval {
		t.Fatalf("approval grant = %#v, want configured grant", opts.ApprovalGrant)
	}
	if opts.Dispatch.InboundContext != resumeInbound {
		t.Fatalf("resume inbound context changed: %#v", opts.Dispatch.InboundContext)
	}
}

func TestToolTerminalRequestPreservesExactSafetyHalt(t *testing.T) {
	llm := newLLMIterationState(1)
	llm.toolResponseDisposition = toolResponseHandled

	got := toolTerminalRequest(ToolLoopOutcome{
		Control:      turnStepFinalize,
		FinalContent: "  runtime-owned halt reason  ",
		TerminalMode: terminalRenderExact,
	}, llm, terminalContent{})
	if got.renderMode != terminalRenderExact || strings.TrimSpace(got.content.content) != "runtime-owned halt reason" ||
		!got.content.persistIfToolHandled {
		t.Fatalf("tool terminal request = %#v, want exact runtime halt", got)
	}
}
