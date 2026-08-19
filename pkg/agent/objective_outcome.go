package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
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
}

func objectiveOutcomeUserContent(content string, outcome *toolshared.ObjectiveOutcome) string {
	if outcome == nil || outcome.Status == toolshared.ObjectiveOutcomeSucceeded {
		return content
	}
	var lines []string
	if outcome.Status == toolshared.ObjectiveOutcomePartial {
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
		`{"status":"succeeded|partial|blocked","completed_items":[{"objective_id":"objective_1","receipt_ids":["..."]}],"missing_items":["objective_2"]}` +
		objectiveOutcomeEnd +
		". The runtime-owned objective checklist is: " + string(encoded) +
		". Put every checklist ID exactly once in completed_items or missing_items; never add or rename IDs. " +
		"For each external_action, copy one or more " +
		"invocation_id values from successful browser_act results into receipt_ids. Do not claim an external action " +
		"without its runtime receipt. The runtime removes this block before showing your prose."
}

func extractObjectiveOutcome(
	content string,
	audits []toolshared.WriteAuditEntry,
	required bool,
	checklists ...[]runtimeObjectiveItem,
) (string, *toolshared.ObjectiveOutcome) {
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
	return clean, validateObjectiveOutcome(reported, audits, checklist)
}

func validateObjectiveOutcome(
	reported reportedObjectiveOutcome,
	audits []toolshared.WriteAuditEntry,
	checklist []runtimeObjectiveItem,
) *toolshared.ObjectiveOutcome {
	switch strings.TrimSpace(reported.Status) {
	case string(toolshared.ObjectiveOutcomeSucceeded),
		string(toolshared.ObjectiveOutcomePartial),
		string(toolshared.ObjectiveOutcomeBlocked):
	default:
		return blockedObjectiveOutcome("objective outcome status was invalid")
	}
	receipts := make(map[string]toolshared.ObjectiveReceipt)
	for _, audit := range audits {
		if !audit.Success || audit.Kind != "external_action" || audit.Tool != "browser_act" ||
			strings.TrimSpace(audit.Metadata["effect"]) != "external_commit" {
			continue
		}
		id := strings.TrimSpace(audit.Metadata["invocation_id"])
		if id == "" {
			continue
		}
		receipts[id] = toolshared.ObjectiveReceipt{
			ID: id, Kind: audit.Kind, Target: audit.Target, Action: audit.Action,
			Tool: audit.Tool, Summary: audit.Summary, Metadata: copyObjectiveMetadata(audit.Metadata),
		}
	}
	outcome := &toolshared.ObjectiveOutcome{}
	expected := make(map[string]runtimeObjectiveItem, len(checklist))
	for _, item := range checklist {
		expected[item.ID] = item
	}
	partitioned := make(map[string]struct{}, len(checklist))
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
		item := toolshared.ObjectiveItem{Item: spec.Item, Kind: spec.Kind}
		if item.Kind == "result" && len(reportedItem.ReceiptIDs) > 0 {
			appendMissing(item.Item + " (read-only result unexpectedly included an external-action receipt)")
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
			receipt, found := receipts[receiptID]
			if !found {
				valid = false
				continue
			}
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
	for _, item := range checklist {
		if _, found := partitioned[item.ID]; !found {
			appendMissing(item.Item + " (objective ID was omitted from the outcome)")
		}
	}
	switch {
	case len(outcome.MissingItems) == 0 && len(outcome.CompletedItems) > 0:
		outcome.Status = toolshared.ObjectiveOutcomeSucceeded
	case len(outcome.CompletedItems) > 0:
		outcome.Status = toolshared.ObjectiveOutcomePartial
	default:
		outcome.Status = toolshared.ObjectiveOutcomeBlocked
		if len(outcome.MissingItems) == 0 {
			appendMissing("no objective items were completed")
		}
	}
	switch strings.TrimSpace(reported.Status) {
	case string(toolshared.ObjectiveOutcomeBlocked):
		outcome.Status = toolshared.ObjectiveOutcomeBlocked
		if len(outcome.MissingItems) == 0 {
			appendMissing("producer reported the objective as blocked")
		}
	case string(toolshared.ObjectiveOutcomePartial):
		if outcome.Status == toolshared.ObjectiveOutcomeSucceeded {
			outcome.Status = toolshared.ObjectiveOutcomePartial
			appendMissing("producer reported the objective as partial")
		}
	}
	return outcome
}

func blockedObjectiveOutcome(reason string) *toolshared.ObjectiveOutcome {
	return &toolshared.ObjectiveOutcome{
		Status: toolshared.ObjectiveOutcomeBlocked, MissingItems: []string{reason},
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

func cloneObjectiveOutcome(input *toolshared.ObjectiveOutcome) *toolshared.ObjectiveOutcome {
	if input == nil {
		return nil
	}
	out := &toolshared.ObjectiveOutcome{
		Status: input.Status, MissingItems: append([]string(nil), input.MissingItems...),
	}
	for _, item := range input.CompletedItems {
		cloned := toolshared.ObjectiveItem{Item: item.Item, Kind: item.Kind}
		for _, receipt := range item.Receipts {
			receipt.Metadata = copyObjectiveMetadata(receipt.Metadata)
			cloned.Receipts = append(cloned.Receipts, receipt)
		}
		out.CompletedItems = append(out.CompletedItems, cloned)
	}
	return out
}
