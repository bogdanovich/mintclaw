package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	objectiveOutcomeStart          = "<task_outcome>"
	objectiveOutcomeEnd            = "</task_outcome>"
	objectiveOutcomeLimit          = 64
	objectiveOutcomeResultRequired = "objective outcome result was required"
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
		if rendered := renderObjectiveOutput(item.Item, item.Output); rendered != "" {
			lines = append(lines, rendered)
		}
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
	ObjectiveID string                      `json:"objective_id"`
	ReceiptIDs  []string                    `json:"receipt_ids"`
	Output      *taskresult.ObjectiveOutput `json:"output,omitempty"`
}

type runtimeObjectiveItem struct {
	ID         string
	Item       string
	Kind       string
	Acceptance *taskresult.ObjectiveAcceptance
}

func cloneRuntimeObjectiveChecklist(items []runtimeObjectiveItem) []runtimeObjectiveItem {
	cloned := make([]runtimeObjectiveItem, len(items))
	for index, item := range items {
		cloned[index] = item
		cloned[index].Acceptance = taskresult.CloneObjectiveAcceptance(item.Acceptance)
	}
	return cloned
}

func normalizeObjectiveChecklist(specs []toolshared.ObjectiveSpec) []runtimeObjectiveItem {
	items := make([]runtimeObjectiveItem, 0, min(len(specs), objectiveOutcomeLimit))
	for _, spec := range specs {
		item := boundedObjectiveText(spec.Item)
		kind := strings.TrimSpace(spec.Kind)
		acceptance, valid := normalizeObjectiveAcceptance(spec.Acceptance, kind)
		if item == "" || (kind != "result" && kind != "external_action") || !valid ||
			len(items) >= objectiveOutcomeLimit {
			return nil
		}
		items = append(items, runtimeObjectiveItem{
			ID: fmt.Sprintf("objective_%d", len(items)+1), Item: item, Kind: kind, Acceptance: acceptance,
		})
	}
	return items
}

func interactionObjectiveChecklist(items []runtimeObjectiveItem) []interactions.ObjectiveChecklistItem {
	out := make([]interactions.ObjectiveChecklistItem, 0, len(items))
	for _, item := range items {
		out = append(out, interactions.ObjectiveChecklistItem{
			ID: item.ID, Item: item.Item, Kind: item.Kind,
			Acceptance: taskresult.CloneObjectiveAcceptance(item.Acceptance),
		})
	}
	return out
}

func runtimeObjectiveChecklist(items []interactions.ObjectiveChecklistItem) []runtimeObjectiveItem {
	out := make([]runtimeObjectiveItem, 0, len(items))
	for _, item := range items {
		out = append(out, runtimeObjectiveItem{
			ID: item.ID, Item: item.Item, Kind: item.Kind,
			Acceptance: taskresult.CloneObjectiveAcceptance(item.Acceptance),
		})
	}
	return out
}

func normalizeObjectiveAcceptance(
	input *taskresult.ObjectiveAcceptance,
	objectiveKind string,
) (*taskresult.ObjectiveAcceptance, bool) {
	if input == nil {
		return nil, true
	}
	if objectiveKind != "result" {
		return nil, false
	}
	outputKind := strings.TrimSpace(input.OutputKind)
	if outputKind != "text" && outputKind != "records" && outputKind != "artifact" {
		return nil, false
	}
	if input.MinItems < 0 || input.MinItems > 1024 ||
		(input.MinItems > 0 && outputKind != "records") ||
		(len(input.RequiredFields) > 0 && outputKind != "records") || len(input.RequiredFields) > 32 {
		return nil, false
	}
	out := &taskresult.ObjectiveAcceptance{OutputKind: outputKind, MinItems: input.MinItems}
	seen := make(map[string]struct{}, len(input.RequiredFields))
	for _, value := range input.RequiredFields {
		field := strings.TrimSpace(value)
		if field == "" || len([]rune(field)) > 64 {
			return nil, false
		}
		if _, duplicate := seen[field]; duplicate {
			continue
		}
		seen[field] = struct{}{}
		out.RequiredFields = append(out.RequiredFields, field)
	}
	return out, true
}

func browserObjectiveOutcomeInstruction(task string, checklist []runtimeObjectiveItem) string {
	return objectiveOutcomeInstruction(task, checklist, true)
}

func objectiveOutcomeInstruction(task string, checklist []runtimeObjectiveItem, browser bool) string {
	task = strings.TrimSpace(task)
	encoded, _ := json.Marshal(interactionObjectiveChecklist(checklist))
	instruction := task + "\n\nRuntime outcome contract (required): finish with exactly one JSON block " +
		objectiveOutcomeStart +
		`{"status":"succeeded|partial|blocked","completed_items":[{"objective_id":"objective_1","receipt_ids":[],"output":{"kind":"text|records|artifact","text":"standalone result","records":[{"field":"value"}],"artifact_refs":["stable-ref"]}}],"missing_items":["objective_2"],"result":"concise terminal summary when succeeded","explanation":"specific blocker when partial or blocked"}` +
		objectiveOutcomeEnd +
		". The runtime-owned objective checklist is: " + string(encoded) +
		". Put every checklist ID exactly once in completed_items or missing_items; never add or rename IDs. " +
		"Every completed result item must include output containing the actual standalone result, never a claim that " +
		"the result appears elsewhere in tool output or prior context. Use kind=text with non-empty text for prose, " +
		"kind=records with the complete records array for requested lists or tables, or kind=artifact with stable " +
		"artifact_refs. Satisfy each declared acceptance output_kind, required_fields, and min_items exactly. Set " +
		"truncated=true if any requested output is missing due to size; truncated output is not accepted as complete. " +
		"For result items, omit receipt_ids or use an empty array. "
	if browser {
		instruction += "For browser_act click calls, declare effect from this checklist and the requested workflow: use read, " +
			"navigation, or local_edit for non-committing UI steps; use external_commit only immediately before an " +
			"important external state change; use unknown only when the workflow impact is genuinely unclear. " +
			"Do not infer click effect from the element role or HTTP method. " +
			"When an external_action requires human approval, call browser_act during this turn with the effect required " +
			"by the trusted browser contract: external_commit for a known important external state change, or unknown only " +
			"when its impact is genuinely unclear; " +
			"the runtime suspends before execution and preserves the continuation. Never replace that tool call with a " +
			"prose approval question, a textual awaiting_approval status, or a completed result, and do not close the " +
			"browser session while the runtime is suspended. Read, navigation, and local_edit invocation IDs " +
			"are not external-action receipts. For each external_action, copy one or more " +
			"invocation_id values from successful browser_act results into receipt_ids. Do not claim an external action " +
			"without its runtime receipt. If the commit ran but later verification cannot confirm the requested external " +
			"effect, keep the external_action item missing and explain the unverified postcondition instead of claiming it " +
			"completed. "
	}
	instruction += "For partial or blocked outcomes, include one concise, specific explanation of the first blocker; " +
		"the runtime bounds it and labels it as producer-reported. For succeeded outcomes, include one concise " +
		"user-facing summary with any requested public links or IDs in result. The runtime removes this block and " +
		"preserves the validated objective outputs as the terminal deliverable."
	return instruction
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
		clean = terminalObjectiveResult(reported.Result, outcome)
	}
	return clean, outcome
}

func objectiveOutcomeRepairInstruction(
	content string,
	audits []toolshared.WriteAuditEntry,
	checklist []runtimeObjectiveItem,
) (string, bool) {
	start := strings.LastIndex(content, objectiveOutcomeStart)
	end := strings.LastIndex(content, objectiveOutcomeEnd)
	reason := "the required objective outcome block was not reported"
	if start >= 0 && end >= start {
		raw := strings.TrimSpace(content[start+len(objectiveOutcomeStart) : end])
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		var reported reportedObjectiveOutcome
		if decoder.Decode(&reported) == nil && decoder.Decode(&struct{}{}) == io.EOF {
			if strings.TrimSpace(reported.Status) != string(taskresult.OutcomeSucceeded) {
				return "", false
			}
			outcome := validateObjectiveOutcome(reported, audits, checklist)
			if outcome.Status == taskresult.OutcomeSucceeded {
				return "", false
			}
			if len(outcome.MissingItems) > 0 {
				reason = strings.Join(outcome.MissingItems, "; ")
			} else {
				reason = "the reported succeeded outcome did not satisfy the runtime contract"
			}
		} else {
			reason = "the objective outcome block was invalid"
		}
	}
	return "Finalization repair required: " + reason + ". Return a corrected standalone final result and exactly " +
		"one complete " + objectiveOutcomeStart + " JSON block using the same runtime-owned objective IDs. " +
		"Include the actual output for every completed result objective. Do not refer to data as appearing above, " +
		"in tool output, or elsewhere in context. This is a model-only repair pass: do not call any tool, repeat any " +
		"external action, or claim a new side effect. If the existing context cannot supply a complete output, report " +
		"partial or blocked and identify the missing objective instead of claiming succeeded.", true
}

func terminalResultText(value string) string {
	return strings.TrimSpace(value)
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
	missingResult := status == string(taskresult.OutcomeSucceeded) && terminalResultText(reported.Result) == ""
	receipts := make(map[string]taskresult.Receipt)
	for _, audit := range audits {
		if !audit.Success || audit.Kind != "external_action" || audit.Tool != "browser_act" ||
			!isBrowserExternalActionReceiptEffect(audit.Metadata["effect"]) {
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
	missingExternalObjectives := 0
	partitionValid := true
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
	appendPriorityMissing := func(item string) {
		item = boundedObjectiveText(item)
		if item == "" {
			return
		}
		if _, exists := missingSeen[item]; exists {
			return
		}
		if len(outcome.MissingItems) < objectiveOutcomeLimit {
			missingSeen[item] = struct{}{}
			outcome.MissingItems = append(outcome.MissingItems, item)
			return
		}
		replaced := outcome.MissingItems[len(outcome.MissingItems)-1]
		delete(missingSeen, replaced)
		missingSeen[item] = struct{}{}
		outcome.MissingItems[len(outcome.MissingItems)-1] = item
	}
	for _, id := range reported.MissingItems {
		id = strings.TrimSpace(id)
		item, found := expected[id]
		if !found {
			partitionValid = false
			appendMissing("objective outcome contained an unknown checklist ID")
			continue
		}
		if _, duplicate := partitioned[id]; duplicate {
			partitionValid = false
			appendMissing(item.Item + " (objective ID was reported more than once)")
			continue
		}
		partitioned[id] = struct{}{}
		if item.Kind == "external_action" {
			missingExternalObjectives++
		}
		appendMissing(item.Item)
	}
	for _, reportedItem := range reported.CompletedItems {
		if len(outcome.CompletedItems) >= objectiveOutcomeLimit {
			partitionValid = false
			appendMissing("additional completed items were omitted by the runtime limit")
			break
		}
		id := strings.TrimSpace(reportedItem.ObjectiveID)
		spec, found := expected[id]
		if !found {
			partitionValid = false
			appendMissing("objective outcome contained an unknown checklist ID")
			continue
		}
		if _, duplicate := partitioned[id]; duplicate {
			partitionValid = false
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
				partitionValid = false
				appendMissing(item.Item + " (read-only result included a verified external-action receipt)")
				continue
			}
			output, reason := normalizeObjectiveOutput(reportedItem.Output, spec.Acceptance)
			if reason != "" {
				partitionValid = false
				appendMissing(item.Item + " (" + reason + ")")
				continue
			}
			item.Output = output
			outcome.CompletedItems = append(outcome.CompletedItems, item)
			continue
		}
		valid := true
		seenReceipts := make(map[string]struct{})
		stagedReceiptIDs := make([]string, 0, len(reportedItem.ReceiptIDs))
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
			stagedReceiptIDs = append(stagedReceiptIDs, receiptID)
			item.Receipts = append(item.Receipts, receipt)
		}
		if item.Kind == "external_action" && len(item.Receipts) == 0 {
			valid = false
		}
		if !valid {
			partitionValid = false
			appendMissing(item.Item + " (missing verified runtime receipt)")
			continue
		}
		for _, receiptID := range stagedReceiptIDs {
			consumedReceipts[receiptID] = struct{}{}
		}
		outcome.CompletedItems = append(outcome.CompletedItems, item)
	}
	for _, item := range checklist {
		if _, found := partitioned[item.ID]; !found {
			partitionValid = false
			appendMissing(item.Item + " (objective ID was omitted from the outcome)")
		}
	}
	if missingResult {
		appendMissing(objectiveOutcomeResultRequired)
	}
	unclaimedReceipts := 0
	for receiptID := range receipts {
		if _, consumed := consumedReceipts[receiptID]; !consumed {
			unclaimedReceipts++
		}
	}
	reportedStatus := strings.TrimSpace(reported.Status)
	// A single unclaimed commit and a single producer-reported missing external
	// objective have an unambiguous relationship: the commit ran, but its
	// requested postcondition was not verified. An explicitly incomplete outcome
	// carries the required explanation, so the missing objective already
	// represents that incomplete result. Preserve the diagnostic for succeeded,
	// ambiguous, or unexpected commits so the runtime never silently accounts for
	// extra external actions.
	unverifiedPostcondition := reportedStatus == string(taskresult.OutcomePartial) ||
		reportedStatus == string(taskresult.OutcomeBlocked)
	if unclaimedReceipts > 0 &&
		(!unverifiedPostcondition || !partitionValid || unclaimedReceipts != 1 || missingExternalObjectives != 1) {
		appendPriorityMissing(
			"an external browser action completed, but its receipt was not claimed by a completed " +
				"external_action objective",
		)
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
	switch reportedStatus {
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
	if missingResult {
		outcome.Explanation = objectiveOutcomeResultRequired
	} else if outcome.Status == taskresult.OutcomeSucceeded {
		outcome.Explanation = ""
	}
	return outcome
}

func normalizeObjectiveOutput(
	input *taskresult.ObjectiveOutput,
	acceptance *taskresult.ObjectiveAcceptance,
) (*taskresult.ObjectiveOutput, string) {
	if input == nil {
		return nil, "standalone objective output was required"
	}
	output := taskresult.CloneObjectiveOutput(input)
	output.Kind = strings.TrimSpace(output.Kind)
	output.Text = strings.TrimSpace(output.Text)
	if output.Truncated {
		return nil, "standalone objective output was truncated"
	}
	if acceptance != nil && output.Kind != acceptance.OutputKind {
		return nil, "output kind did not match the declared acceptance contract"
	}
	switch output.Kind {
	case "text":
		if output.Text == "" {
			return nil, "standalone text output was required"
		}
		if len(output.Records) > 0 || len(output.ArtifactRefs) > 0 {
			return nil, "text output contained fields for a different output kind"
		}
	case "records":
		if len(output.Records) == 0 && acceptance == nil {
			return nil, "at least one standalone record was required"
		}
		if len(output.Records) > 1024 {
			return nil, "record output exceeded the runtime item limit"
		}
		if len(output.ArtifactRefs) > 0 {
			return nil, "record output contained artifact references"
		}
		if acceptance != nil && len(output.Records) < acceptance.MinItems {
			return nil, "record output did not meet the declared minimum item count"
		}
		for _, record := range output.Records {
			if len(record) == 0 || len(record) > 64 {
				return nil, "each record must contain between 1 and 64 fields"
			}
			normalizedRecord := make(map[string]string, len(record))
			for key, value := range record {
				trimmedKey := strings.TrimSpace(key)
				trimmedValue := strings.TrimSpace(value)
				if trimmedKey == "" || trimmedValue == "" || len([]rune(trimmedKey)) > 64 ||
					len([]rune(trimmedValue)) > 4096 {
					return nil, "record fields require bounded non-empty names and values"
				}
				if _, duplicate := normalizedRecord[trimmedKey]; duplicate {
					return nil, "record output contained duplicate normalized field names"
				}
				normalizedRecord[trimmedKey] = trimmedValue
			}
			if acceptance != nil {
				for _, field := range acceptance.RequiredFields {
					if strings.TrimSpace(normalizedRecord[field]) == "" {
						return nil, "record output omitted a declared required field"
					}
				}
			}
			for key := range record {
				delete(record, key)
			}
			for key, value := range normalizedRecord {
				record[key] = value
			}
		}
	case "artifact":
		if len(output.ArtifactRefs) == 0 || len(output.ArtifactRefs) > 64 {
			return nil, "at least one bounded artifact reference was required"
		}
		if len(output.Records) > 0 {
			return nil, "artifact output contained records"
		}
		for index, ref := range output.ArtifactRefs {
			ref = strings.TrimSpace(ref)
			if ref == "" || len([]rune(ref)) > 2048 {
				return nil, "artifact references must be bounded and non-empty"
			}
			output.ArtifactRefs[index] = ref
		}
	default:
		return nil, "output kind must be text, records, or artifact"
	}
	return output, ""
}

func terminalObjectiveResult(summary string, outcome *taskresult.Outcome) string {
	parts := make([]string, 0, len(outcome.CompletedItems)+1)
	if summary = strings.TrimSpace(summary); summary != "" {
		parts = append(parts, summary)
	}
	for _, item := range outcome.CompletedItems {
		if item.Kind != "result" || item.Output == nil {
			continue
		}
		rendered := renderObjectiveOutput(item.Item, item.Output)
		if rendered == "" || strings.Contains(summary, rendered) {
			continue
		}
		parts = append(parts, rendered)
	}
	return strings.Join(parts, "\n\n")
}

func renderObjectiveOutput(label string, output *taskresult.ObjectiveOutput) string {
	if output == nil {
		return ""
	}
	switch output.Kind {
	case "text":
		return output.Text
	case "records":
		lines := []string{strings.TrimSpace(label) + ":"}
		for _, record := range output.Records {
			keys := make([]string, 0, len(record))
			for key := range record {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			fields := make([]string, 0, len(keys))
			for _, key := range keys {
				fields = append(fields, key+": "+record[key])
			}
			lines = append(lines, "- "+strings.Join(fields, "; "))
		}
		return strings.Join(lines, "\n")
	case "artifact":
		return strings.TrimSpace(label) + ":\n- " + strings.Join(output.ArtifactRefs, "\n- ")
	default:
		return ""
	}
}

func isBrowserExternalActionReceiptEffect(effect string) bool {
	switch strings.TrimSpace(effect) {
	case "external_commit", "unknown":
		return true
	default:
		return false
	}
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
