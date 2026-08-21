package agent

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
)

const (
	maxTerminalTaskPromptRecords = 8
	maxTerminalTaskPromptRunes   = 1600
	maxTerminalTaskIDRunes       = 128
)

func backgroundTaskStatusSafetyRule() string {
	return "A historical spawn or delegate acknowledgement proves only that the task was accepted. " +
		"Never treat it as proof that work is still active. Check task_status when that tool is available; " +
		"when it is unavailable or the task record is absent, treat the state as unknown, not running. " +
		"A terminal task does not satisfy a new request."
}

func (al *AgentLoop) terminalTaskContextForTurn(ts *turnState) []providers.Message {
	if al == nil || ts == nil || ts.opts.NoHistory || ts.agent == nil {
		return nil
	}
	registry := al.taskRegistryForWorkspace(ts.workspace)
	if registry == nil {
		return nil
	}
	records := make([]taskregistry.Record, 0)
	for _, record := range registry.List() {
		if !record.HistoryPolicyKnown || record.HistoryDisabled || record.OwnerKey != ts.agent.ID ||
			record.RequesterSessionKey != ts.sessionKey || !terminalTaskPromptStatus(record.Status) {
			continue
		}
		if record.Runtime != taskregistry.RuntimeSubagent && record.Runtime != taskregistry.RuntimeDelegate {
			continue
		}
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b taskregistry.Record) int {
		if order := cmp.Compare(terminalTaskPromptTimestamp(a), terminalTaskPromptTimestamp(b)); order != 0 {
			return order
		}
		return cmp.Compare(a.TaskID, b.TaskID)
	})
	if len(records) > maxTerminalTaskPromptRecords {
		records = records[len(records)-maxTerminalTaskPromptRecords:]
	}
	messages := make([]providers.Message, 0, len(records))
	for _, record := range records {
		summary := terminalTaskPromptSummary(record)
		if cfg := al.GetConfig(); cfg != nil {
			summary = cfg.FilterSensitiveData(summary)
		}
		content := fmt.Sprintf(
			"[Durable background task status]\ntask_id: %s\nstate: %s\n"+
				"This task is no longer running. A terminal task does not satisfy a new request.\nResult:\n%s",
			boundedTerminalTaskPromptText(record.TaskID, maxTerminalTaskIDRunes),
			boundedTerminalTaskPromptText(terminalTaskPromptState(record), 64),
			summary,
		)
		messages = append(messages, providers.Message{
			Role: "assistant", Content: boundedTerminalTaskPromptText(content, maxTerminalTaskPromptRunes),
		})
	}
	return messages
}

func terminalTaskPromptStatus(status taskregistry.Status) bool {
	switch status {
	case taskregistry.StatusSucceeded, taskregistry.StatusFailed, taskregistry.StatusTimedOut,
		taskregistry.StatusCancelled, taskregistry.StatusLost:
		return true
	default:
		return false
	}
}

func terminalTaskPromptTimestamp(record taskregistry.Record) int64 {
	if record.EndedAt != 0 {
		return record.EndedAt
	}
	if record.LastEventAt != 0 {
		return record.LastEventAt
	}
	return record.CreatedAt
}

func terminalTaskPromptState(record taskregistry.Record) string {
	if record.Deliverable != nil && record.Deliverable.ObjectiveOutcome != nil &&
		strings.TrimSpace(string(record.Deliverable.ObjectiveOutcome.Status)) != "" {
		return string(record.Deliverable.ObjectiveOutcome.Status)
	}
	return string(record.Status)
}

func terminalTaskPromptSummary(record taskregistry.Record) string {
	for _, candidate := range []string{
		record.TerminalSummary,
		deliverableTaskPromptText(record.Deliverable),
		record.Error,
	} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return "No result summary was recorded."
}

func deliverableTaskPromptText(deliverable *taskresult.Deliverable) string {
	if deliverable == nil {
		return ""
	}
	if strings.TrimSpace(deliverable.Text) != "" {
		return deliverable.Text
	}
	if deliverable.Report != nil {
		return deliverable.Report.Summary
	}
	return ""
}

func boundedTerminalTaskPromptText(value string, limit int) string {
	value = strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	chars := []rune(value)
	if limit <= 0 || len(chars) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return string(chars[:limit-1]) + "…"
}
