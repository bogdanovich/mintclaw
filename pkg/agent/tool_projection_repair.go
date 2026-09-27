package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const maxSerializedToolProjectionRepairs = 1

func serializedToolProjectionName(content string) (string, bool) {
	const prefix = "[tool_use:"
	const argumentsSeparator = ", args:"
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}
	remainder := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
	separator := strings.Index(remainder, argumentsSeparator)
	if separator <= 0 {
		return "", false
	}
	name := strings.TrimSpace(remainder[:separator])
	if name == "" || strings.ContainsAny(name, " \t\r\n[]{}") {
		return "", false
	}
	return name, true
}

func (p *Pipeline) repairSerializedToolProjection(
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	llm *LLMIterationState,
	content string,
) (LLMCallOutcome, bool, error) {
	toolName, projected := serializedToolProjectionName(content)
	if !projected || ts == nil || ts.agent == nil || ts.agent.Tools == nil {
		return LLMCallOutcome{}, false, nil
	}
	if _, registered := ts.agent.Tools.GetRegistered(toolName); !registered {
		return LLMCallOutcome{}, false, nil
	}

	discardConfiguredStreamingLLM(turnCtx, llm)
	if exec.serializedToolProjectionRepairs >= maxSerializedToolProjectionRepairs {
		return LLMCallOutcome{}, true, errors.New(
			"model repeatedly returned a registered tool call as plain text instead of structured output",
		)
	}
	exec.serializedToolProjectionRepairs++
	exec.messages = append(exec.messages, providers.Message{
		Role: "user",
		Content: fmt.Sprintf(
			"<runtime_correction>The previous response serialized the registered tool %q as plain text. "+
				"That text did not execute. Re-evaluate the current user request. If the action is still needed, "+
				"call the tool through the structured tool interface; otherwise answer normally without claiming "+
				"the action happened. Do not repeat the serialized tool syntax.</runtime_correction>",
			toolName,
		),
	})
	logger.WarnCF("agent", "Repairing serialized tool projection in model text", map[string]any{
		"agent_id": ts.agent.ID,
		"tool":     toolName,
		"attempt":  exec.serializedToolProjectionRepairs,
	})
	return LLMCallOutcome{Control: turnStepContinue}, true, nil
}
