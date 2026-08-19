package agent

import (
	"encoding/json"
	"io"
	"strings"

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

type reportedObjectiveItem struct {
	Item       string   `json:"item"`
	Kind       string   `json:"kind"`
	ReceiptIDs []string `json:"receipt_ids"`
}

func browserObjectiveOutcomeInstruction(task string) string {
	task = strings.TrimSpace(task)
	return task + "\n\nRuntime outcome contract (required): finish with exactly one JSON block " +
		objectiveOutcomeStart +
		`{"status":"succeeded|partial|blocked","completed_items":[{"item":"...","kind":"result|external_action","receipt_ids":["..."]}],"missing_items":["..."]}` +
		objectiveOutcomeEnd +
		". Put every requested item in completed_items or missing_items. For each external_action, copy one or more " +
		"invocation_id values from successful browser_act results into receipt_ids. Do not claim an external action " +
		"without its runtime receipt. The runtime removes this block before showing your prose."
}

func extractObjectiveOutcome(
	content string,
	audits []toolshared.WriteAuditEntry,
	required bool,
) (string, *toolshared.ObjectiveOutcome) {
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
	return clean, validateObjectiveOutcome(reported, audits)
}

func validateObjectiveOutcome(
	reported reportedObjectiveOutcome,
	audits []toolshared.WriteAuditEntry,
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
		if !audit.Success {
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
	for _, item := range reported.MissingItems {
		appendMissing(item)
	}
	for _, reportedItem := range reported.CompletedItems {
		if len(outcome.CompletedItems) >= objectiveOutcomeLimit {
			appendMissing("additional completed items were omitted by the runtime limit")
			break
		}
		item := toolshared.ObjectiveItem{
			Item: boundedObjectiveText(reportedItem.Item), Kind: strings.TrimSpace(reportedItem.Kind),
		}
		if item.Item == "" || (item.Kind != "result" && item.Kind != "external_action") {
			appendMissing("an objective item had an invalid description or kind")
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
