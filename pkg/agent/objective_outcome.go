package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	objectiveOutcomeStart = "<task_outcome>"
	objectiveOutcomeEnd   = "</task_outcome>"
	objectiveOutcomeLimit = 64
)

type reportedObjectiveOutcome struct {
	Status         string                  `json:"status"`
	CompletedItems []reportedObjectiveItem `json:"completed_items"`
	MissingItems   []string                `json:"missing_items"`
	Result         string                  `json:"result,omitempty"`
	Explanation    string                  `json:"explanation,omitempty"`
}

func objectiveOutcomeUserContent(content string, outcome *taskresult.Outcome) string {
	if outcome == nil || outcome.Status == taskresult.OutcomeSucceeded {
		return content
	}
	var lines []string
	if outcome.Status == taskresult.OutcomePartial {
		lines = append(lines, "Task completed partially.")
	} else {
		lines = append(lines, "Task could not be completed.")
	}
	for _, item := range outcome.CompletedItems {
		lines = append(lines, fmt.Sprintf("Completed: %s", item.Item))
	}
	for _, item := range outcome.MissingItems {
		lines = append(lines, fmt.Sprintf("Not completed: %s", item))
	}
	if outcome.Explanation != "" {
		lines = append(lines, "Reported reason: "+outcome.Explanation)
	}
	return strings.Join(lines, "\n")
}

type reportedObjectiveItem struct {
	ObjectiveID string   `json:"objective_id"`
	ReceiptIDs  []string `json:"receipt_ids"`
}

type runtimeObjectiveItem struct {
	ID   string
	Item string
	Kind string
}

func normalizeObjectiveChecklist(specs []toolshared.ObjectiveSpec) []runtimeObjectiveItem {
	items := make([]runtimeObjectiveItem, 0, min(len(specs), objectiveOutcomeLimit))
	for _, spec := range specs {
		item := boundedObjectiveText(spec.Item)
		kind := strings.TrimSpace(spec.Kind)
		if item == "" || (kind != "result" && kind != "external_action") || len(items) >= objectiveOutcomeLimit {
			return nil
		}
		items = append(items, runtimeObjectiveItem{
			ID: fmt.Sprintf("objective_%d", len(items)+1), Item: item, Kind: kind,
		})
	}
	return items
}

func interactionObjectiveChecklist(items []runtimeObjectiveItem) []interactions.ObjectiveChecklistItem {
	out := make([]interactions.ObjectiveChecklistItem, 0, len(items))
	for _, item := range items {
		out = append(out, interactions.ObjectiveChecklistItem{ID: item.ID, Item: item.Item, Kind: item.Kind})
	}
	return out
}

func runtimeObjectiveChecklist(items []interactions.ObjectiveChecklistItem) []runtimeObjectiveItem {
	out := make([]runtimeObjectiveItem, 0, len(items))
	for _, item := range items {
		out = append(out, runtimeObjectiveItem{ID: item.ID, Item: item.Item, Kind: item.Kind})
	}
	return out
}

func browserObjectiveOutcomeInstruction(task string, checklist []runtimeObjectiveItem) string {
	task = strings.TrimSpace(task)
	encoded, _ := json.Marshal(interactionObjectiveChecklist(checklist))
	return task + "\n\nRuntime outcome contract (required): finish with exactly one JSON block " +
		objectiveOutcomeStart +
		`{"status":"succeeded|partial|blocked","completed_items":[{"objective_id":"objective_1","receipt_ids":[]}],"missing_items":["objective_2"],"result":"concise terminal result with links or IDs when succeeded","explanation":"specific blocker when partial or blocked"}` +
		objectiveOutcomeEnd +
		". The runtime-owned objective checklist is: " + string(encoded) +
		". Put every checklist ID exactly once in completed_items or missing_items; never add or rename IDs. " +
		"For browser_act click calls, declare effect from this checklist and the requested workflow: use read, " +
		"navigation, or local_edit for non-committing UI steps; use external_commit only immediately before an " +
		"important external state change; use unknown only when the workflow impact is genuinely unclear. " +
		"Do not infer click effect from the element role or HTTP method. " +
		"When an external_action requires human approval, call browser_act with external_commit during this turn; " +
		"the runtime suspends before execution and preserves the continuation. Never replace that tool call with a " +
		"prose approval question, a textual awaiting_approval status, or a completed result, and do not close the " +
		"browser session while the runtime is suspended. " +
		"For result items, omit receipt_ids or use an empty array; read, navigation, and local_edit invocation IDs " +
		"are not external-action receipts. " +
		"For each external_action, copy one or more " +
		"invocation_id values from successful browser_act results into receipt_ids. Do not claim an external action " +
		"without its runtime receipt. For partial or blocked outcomes, include one concise, specific explanation of " +
		"the first blocker; the runtime bounds it and labels it as producer-reported. For succeeded outcomes, include " +
		"one concise user-facing result with any requested public links or IDs in result. The runtime removes this block " +
		"and preserves result as the terminal deliverable."
}

func extractObjectiveOutcome(
	content string,
	audits []toolshared.WriteAuditEntry,
	required bool,
	checklists ...[]runtimeObjectiveItem,
) (string, *taskresult.Outcome) {
	var checklist []runtimeObjectiveItem
	if len(checklists) > 0 {
		checklist = checklists[0]
	}
	if required && len(checklist) == 0 {
		return strings.TrimSpace(content), blockedObjectiveOutcome("runtime objective checklist was not provided")
	}
	start := strings.LastIndex(content, objectiveOutcomeStart)
	end := strings.LastIndex(content, objectiveOutcomeEnd)
	if start < 0 || end < start {
		if !required {
			return content, nil
		}
		return strings.TrimSpace(content), blockedObjectiveOutcome("objective outcome was not reported")
	}
	clean := strings.TrimSpace(content[:start] + content[end+len(objectiveOutcomeEnd):])
	raw := strings.TrimSpace(content[start+len(objectiveOutcomeStart) : end])
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var reported reportedObjectiveOutcome
	if decoder.Decode(&reported) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return clean, blockedObjectiveOutcome("objective outcome report was invalid")
	}
	outcome := validateObjectiveOutcome(reported, audits, checklist)
	if outcome.Status == taskresult.OutcomeSucceeded {
		if terminalResult := boundedTerminalResult(reported.Result); terminalResult != "" {
			clean = terminalResult
		} else if objectiveOutcomeHasReceipt(outcome) {
			// Compatibility for continuations suspended before the result field
			// was introduced. A verified external-action receipt makes the
			// successful terminal state authoritative; retain the child's
			// bounded detail instead of generic surrounding prose.
			if legacyResult := boundedTerminalResult(reported.Explanation); legacyResult != "" {
				clean = legacyResult
			}
		}
	}
	return clean, outcome
}

func objectiveOutcomeHasReceipt(outcome *taskresult.Outcome) bool {
	if outcome == nil {
		return false
	}
	for _, item := range outcome.CompletedItems {
		if len(item.Receipts) > 0 {
			return true
		}
	}
	return false
}

func boundedTerminalResult(value string) string {
	value = strings.TrimSpace(value)
	const limit = 2048
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func validateObjectiveOutcome(
	reported reportedObjectiveOutcome,
	audits []toolshared.WriteAuditEntry,
	checklist []runtimeObjectiveItem,
) *taskresult.Outcome {
	status := strings.TrimSpace(reported.Status)
	switch status {
	case string(taskresult.OutcomeSucceeded),
		string(taskresult.OutcomePartial),
		string(taskresult.OutcomeBlocked):
	default:
		return blockedObjectiveOutcome("objective outcome status was invalid")
	}
	if (status == string(taskresult.OutcomePartial) || status == string(taskresult.OutcomeBlocked)) &&
		strings.TrimSpace(reported.Explanation) == "" {
		return blockedObjectiveOutcome("objective outcome explanation was required")
	}
	receipts := make(map[string]taskresult.Receipt)
	for _, audit := range audits {
		if !audit.Success || audit.Kind != "external_action" || audit.Tool != "browser_act" ||
			strings.TrimSpace(audit.Metadata["effect"]) != "external_commit" {
			continue
		}
		id := strings.TrimSpace(audit.Metadata["invocation_id"])
		if id == "" {
			continue
		}
		receipts[id] = taskresult.Receipt{
			ID: id, Kind: audit.Kind, Target: audit.Target, Action: audit.Action,
			Tool: audit.Tool, Summary: audit.Summary, Metadata: copyObjectiveMetadata(audit.Metadata),
		}
	}
	outcome := &taskresult.Outcome{Explanation: boundedObjectiveText(reported.Explanation)}
	expected := make(map[string]runtimeObjectiveItem, len(checklist))
	for _, item := range checklist {
		expected[item.ID] = item
	}
	partitioned := make(map[string]struct{}, len(checklist))
	consumedReceipts := make(map[string]struct{})
	missingSeen := make(map[string]struct{})
	appendMissing := func(item string) {
		item = boundedObjectiveText(item)
		if item == "" || len(outcome.MissingItems) >= objectiveOutcomeLimit {
			return
		}
		if _, exists := missingSeen[item]; exists {
			return
		}
		missingSeen[item] = struct{}{}
		outcome.MissingItems = append(outcome.MissingItems, item)
	}
	for _, id := range reported.MissingItems {
		id = strings.TrimSpace(id)
		item, found := expected[id]
		if !found {
			appendMissing("objective outcome contained an unknown checklist ID")
			continue
		}
		if _, duplicate := partitioned[id]; duplicate {
			appendMissing(item.Item + " (objective ID was reported more than once)")
			continue
		}
		partitioned[id] = struct{}{}
		appendMissing(item.Item)
	}
	for _, reportedItem := range reported.CompletedItems {
		if len(outcome.CompletedItems) >= objectiveOutcomeLimit {
			appendMissing("additional completed items were omitted by the runtime limit")
			break
		}
		id := strings.TrimSpace(reportedItem.ObjectiveID)
		spec, found := expected[id]
		if !found {
			appendMissing("objective outcome contained an unknown checklist ID")
			continue
		}
		if _, duplicate := partitioned[id]; duplicate {
			appendMissing(spec.Item + " (objective ID was reported more than once)")
			continue
		}
		partitioned[id] = struct{}{}
		item := taskresult.Item{Item: spec.Item, Kind: spec.Kind}
		if item.Kind == "result" {
			unexpectedExternalAction := false
			for _, receiptID := range reportedItem.ReceiptIDs {
				_, unexpectedExternalAction = receipts[strings.TrimSpace(receiptID)]
				if unexpectedExternalAction {
					break
				}
			}
			if unexpectedExternalAction {
				appendMissing(item.Item + " (read-only result included a verified external-action receipt)")
				continue
			}
			outcome.CompletedItems = append(outcome.CompletedItems, item)
			continue
		}
		valid := true
		seenReceipts := make(map[string]struct{})
		for _, receiptID := range reportedItem.ReceiptIDs {
			receiptID = strings.TrimSpace(receiptID)
			if _, duplicate := seenReceipts[receiptID]; duplicate {
				continue
			}
			seenReceipts[receiptID] = struct{}{}
			if _, consumed := consumedReceipts[receiptID]; consumed {
				valid = false
				continue
			}
			receipt, found := receipts[receiptID]
			if !found {
				valid = false
				continue
			}
			consumedReceipts[receiptID] = struct{}{}
			item.Receipts = append(item.Receipts, receipt)
		}
		if item.Kind == "external_action" && len(item.Receipts) == 0 {
			valid = false
		}
		if !valid {
			appendMissing(item.Item + " (missing verified runtime receipt)")
			continue
		}
		outcome.CompletedItems = append(outcome.CompletedItems, item)
	}
	for receiptID := range receipts {
		if _, consumed := consumedReceipts[receiptID]; !consumed {
			appendMissing("an external browser action was not associated with an external_action objective")
			break
		}
	}
	for _, item := range checklist {
		if _, found := partitioned[item.ID]; !found {
			appendMissing(item.Item + " (objective ID was omitted from the outcome)")
		}
	}
	switch {
	case len(outcome.MissingItems) == 0 && len(outcome.CompletedItems) > 0:
		outcome.Status = taskresult.OutcomeSucceeded
	case len(outcome.CompletedItems) > 0:
		outcome.Status = taskresult.OutcomePartial
	default:
		outcome.Status = taskresult.OutcomeBlocked
		if len(outcome.MissingItems) == 0 {
			appendMissing("no objective items were completed")
		}
	}
	switch strings.TrimSpace(reported.Status) {
	case string(taskresult.OutcomeBlocked):
		outcome.Status = taskresult.OutcomeBlocked
		if len(outcome.MissingItems) == 0 {
			appendMissing("producer reported the objective as blocked")
		}
	case string(taskresult.OutcomePartial):
		if outcome.Status == taskresult.OutcomeSucceeded {
			outcome.Status = taskresult.OutcomePartial
			appendMissing("producer reported the objective as partial")
		}
	}
	if outcome.Status == taskresult.OutcomeSucceeded {
		outcome.Explanation = ""
	}
	return outcome
}

func blockedObjectiveOutcome(reason string) *taskresult.Outcome {
	return &taskresult.Outcome{
		Status: taskresult.OutcomeBlocked, MissingItems: []string{reason}, Explanation: reason,
	}
}

func boundedObjectiveText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 240 {
		return string(runes[:240])
	}
	return value
}

func copyObjectiveMetadata(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneObjectiveOutcome(input *taskresult.Outcome) *taskresult.Outcome {
	return taskresult.CloneOutcome(input)
}
