package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const (
	toolExecutionStartedState = "started"
	maxToolExecutionMarkers   = 64
	maxToolExecutionNameBytes = 128
)

func (r *toolLoopRunner) journalToolExecutionStart(
	ctx context.Context,
	call providers.ToolCall,
	toolName string,
) error {
	if r == nil || r.ts == nil || r.ts.agent == nil || r.ts.agent.Sessions == nil || r.ts.opts.NoHistory {
		return fmt.Errorf("durable coding session is unavailable")
	}
	marker := newToolExecutionMarker(call.ID, toolName, time.Now())
	_, err := r.ts.agent.Sessions.MutateTurnHistory(
		ctx,
		r.ts.sessionKey,
		func(current []providers.Message) ([]providers.Message, bool, error) {
			history := cloneProviderMessages(current)
			assistantIndex := -1
			for index := len(history) - 1; index >= 0; index-- {
				if history[index].Role != "assistant" {
					continue
				}
				for _, candidate := range history[index].ToolCalls {
					if candidate.ID == call.ID {
						assistantIndex = index
						break
					}
				}
				if assistantIndex >= 0 {
					break
				}
			}
			if assistantIndex < 0 {
				return nil, false, fmt.Errorf("canonical assistant intent for tool call is missing")
			}
			for _, existing := range history[assistantIndex].ToolExecutions {
				if existing.CallIDHash == marker.CallIDHash {
					return history, false, nil
				}
			}
			if len(history[assistantIndex].ToolExecutions) >= maxToolExecutionMarkers {
				return nil, false, fmt.Errorf(
					"assistant tool batch exceeds %d durable start markers",
					maxToolExecutionMarkers,
				)
			}
			history[assistantIndex].ToolExecutions = append(history[assistantIndex].ToolExecutions, marker)
			return history, true, nil
		},
	)
	if err != nil {
		return fmt.Errorf("write canonical tool start marker: %w", err)
	}
	return nil
}

func newToolExecutionMarker(
	callID string,
	toolName string,
	startedAt time.Time,
) providers.ToolExecution {
	return providers.ToolExecution{
		CallIDHash: toolLifecycleCallHash(callID),
		Tool:       boundedLifecycleText(strings.TrimSpace(toolName), maxToolExecutionNameBytes),
		State:      toolExecutionStartedState,
		StartedAt:  startedAt.UTC(),
	}
}

func toolLifecycleCallHash(callID string) string {
	digest := sha256.Sum256([]byte(callID))
	return hex.EncodeToString(digest[:])
}

func boundedLifecycleText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (al *AgentLoop) repairCodingToolLifecycles(ctx context.Context) error {
	if al == nil || !al.usesCodingProfile() {
		return nil
	}
	registry := al.GetRegistry()
	if registry == nil {
		return nil
	}
	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	for _, agentID := range agentIDs {
		instance, ok := registry.GetAgent(agentID)
		if !ok || instance == nil || instance.Sessions == nil {
			continue
		}
		sessionKeys := currentRuntimeSessionKeys(instance, instance.Sessions)
		sort.Strings(sessionKeys)
		for _, sessionKey := range sessionKeys {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := instance.Sessions.MutateTurnHistory(
				ctx,
				sessionKey,
				func(history []providers.Message) ([]providers.Message, bool, error) {
					repaired, changed := repairDanglingToolLifecycles(history, time.Now())
					return repaired, changed, nil
				},
			)
			if err != nil {
				return fmt.Errorf("repair coding session %q: %w", sessionKey, err)
			}
		}
	}
	return nil
}

func repairDanglingToolLifecycles(
	history []providers.Message,
	recoveredAt time.Time,
) ([]providers.Message, bool) {
	repaired := make([]providers.Message, 0, len(history))
	changed := false
	for index := 0; index < len(history); index++ {
		message := history[index]
		repaired = append(repaired, message)
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		terminalCalls := make(map[string]struct{})
		for index+1 < len(history) && history[index+1].Role == "tool" {
			index++
			repaired = append(repaired, history[index])
			if strings.TrimSpace(history[index].ToolCallID) != "" {
				terminalCalls[history[index].ToolCallID] = struct{}{}
			}
		}
		markers := make(map[string]providers.ToolExecution, len(message.ToolExecutions))
		for _, marker := range message.ToolExecutions {
			markers[marker.CallIDHash] = marker
		}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" {
				continue
			}
			if _, resolved := terminalCalls[call.ID]; resolved {
				continue
			}
			marker, started := markers[toolLifecycleCallHash(call.ID)]
			toolName := toolLifecycleCallName(call)
			status := providers.ToolResultStatusInterrupted
			content := fmt.Sprintf(
				"[mintclaw tool recovery: interrupted_before_start] Tool %q has no durable start marker or terminal result. It was not replayed.",
				toolName,
			)
			if started && marker.State == toolExecutionStartedState {
				status = providers.ToolResultStatusUnknown
				content = fmt.Sprintf(
					"[mintclaw tool recovery: outcome_unknown] Tool %q crossed its durable start boundary, but no terminal result was persisted. It was not replayed; inspect current state before deciding next steps.",
					toolName,
				)
			}
			createdAt := recoveredAt.UTC()
			repaired = append(repaired, providers.Message{
				Role:             "tool",
				Content:          content,
				ToolCallID:       call.ID,
				ToolResultStatus: status,
				CreatedAt:        &createdAt,
			})
			changed = true
		}
	}
	return repaired, changed
}

func toolLifecycleCallName(call providers.ToolCall) string {
	return boundedLifecycleText(strings.TrimSpace(call.Name), maxToolExecutionNameBytes)
}
