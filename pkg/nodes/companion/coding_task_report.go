package companion

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
	"github.com/bogdanovich/mintclaw/pkg/coding/worktree"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

var absolutePathTokenPattern = regexp.MustCompile(
	`(^|[^A-Za-z0-9._~+@%/\\-])(?:\\\\(?:\?\\)?[^\s"'` + "`" + `\])}>,;]+|` +
		`/[A-Za-z0-9._~+@%:,=\\/-]+|[A-Za-z]:[\\/][^\s"'` + "`" + `\])}>,;]+)`,
)

const maxRetainedTerminalReportItems = 256

func (active *activeCodingTask) projectReportItem(item worker.Item) {
	if active == nil || item.ID == "" {
		return
	}
	active.reportMu.Lock()
	defer active.reportMu.Unlock()
	retained, exists := active.reportItems[item.ID]
	if exists && (retained.Revision > item.Revision ||
		(retained.Revision == item.Revision && retained.Sequence >= item.Sequence)) {
		return
	}
	if !exists && len(active.reportItems) >= maxRetainedTerminalReportItems {
		oldestID := ""
		oldestSequence := ^uint64(0)
		for candidateID, candidate := range active.reportItems {
			if candidate.Sequence < oldestSequence ||
				(candidate.Sequence == oldestSequence && (oldestID == "" || candidateID < oldestID)) {
				oldestID = candidateID
				oldestSequence = candidate.Sequence
			}
		}
		delete(active.reportItems, oldestID)
	}
	active.reportItems[item.ID] = item
}

// captureTerminalReportEvents closes the small race where the worker process
// exits after retaining its final item but before the watcher consumes the
// corresponding wake signal. The retained event page is already bounded by
// the worker protocol; a history gap means the latest prior snapshot remains
// the only safe evidence.
func (active *activeCodingTask) captureTerminalReportEvents() {
	if active == nil || active.process == nil {
		return
	}
	page := active.process.EventsAfter(0)
	if page.HistoryGap {
		return
	}
	for _, retained := range page.Events {
		payload, err := worker.DecodeEventPayload(retained.Record.Event, retained.Record.Payload)
		if err != nil || !codingEventMatches(active, payload) {
			continue
		}
		if item, ok := payload.(*worker.ItemUpdatedPayload); ok {
			active.projectReportItem(item.Item)
		}
	}
}

func (active *activeCodingTask) terminalReport(result codingTaskProcessResult) *codingtask.TerminalReport {
	report := &codingtask.TerminalReport{}
	active.reportMu.Lock()
	items := make([]worker.Item, 0, len(active.reportItems))
	for _, item := range active.reportItems {
		items = append(items, item)
	}
	active.reportMu.Unlock()
	slices.SortFunc(items, func(left, right worker.Item) int {
		if left.Sequence < right.Sequence {
			return -1
		}
		if left.Sequence > right.Sequence {
			return 1
		}
		return 0
	})
	for _, item := range items {
		if item.Message != nil && item.Message.Kind == worker.MessageAssistant &&
			item.Message.Phase == worker.AssistantPhaseFinal && item.Message.Complete {
			report.Summary, report.SummaryTruncated = safeCodingTerminalSummary(item.Message.Text)
		}
		if item.Tool == nil || item.Tool.Command == nil {
			continue
		}
		status := terminalValidationStatus(item.Tool.Command.Status)
		if status == "" {
			continue
		}
		if len(report.Validations) >= codingtask.MaxTerminalValidations {
			report.ValidationsTruncated = true
			continue
		}
		report.Validations = append(report.Validations, codingtask.ValidationOutcome{
			Kind: "command", Status: status,
		})
	}
	if report.Summary == "" {
		report.Summary = codingTaskOutcomeSummary(result.outcome)
	}
	if result.handoff != nil {
		report.ChangedPaths, report.PathsTruncated = codingTerminalChangedPaths(result.handoff.Changes)
		report.Commit = result.handoff.Head
		report.CleanupState = codingHandoffCleanupState(result.handoff.Class)
		if result.handoff.Class == worktree.HandoffConflicted ||
			result.handoff.Class == worktree.HandoffMissing ||
			result.handoff.Class == worktree.HandoffMismatch ||
			result.handoff.Class == worktree.HandoffUncertain {
			report.Unresolved = "repository handoff requires operator inspection"
		}
	} else {
		report.CleanupState = "not_applicable"
	}
	if result.outcome == codingTaskOutcomeFailed || result.outcome == codingTaskOutcomeUncertain {
		report.Unresolved = "coding task did not produce a verified complete outcome"
	}
	boundCodingTerminalReport(report)
	if report.Validate() != nil {
		return &codingtask.TerminalReport{
			Summary:      codingTaskOutcomeSummary(result.outcome),
			CleanupState: "unknown",
			Unresolved:   "terminal report was reduced because bounded evidence was invalid",
		}
	}
	return report
}

func boundCodingTerminalReport(report *codingtask.TerminalReport) {
	if report == nil {
		return
	}
	for report.Validate() != nil && len(report.ChangedPaths) > 0 {
		report.PathsTruncated = true
		report.ChangedPaths = report.ChangedPaths[:len(report.ChangedPaths)/2]
	}
	for report.Validate() != nil && len(report.Validations) > 0 {
		report.ValidationsTruncated = true
		report.Validations = report.Validations[:len(report.Validations)/2]
	}
	for report.Validate() != nil && report.Summary != "" {
		report.SummaryTruncated = true
		limit := len(report.Summary) / 2
		for limit > 0 && !utf8.ValidString(report.Summary[:limit]) {
			limit--
		}
		report.Summary = strings.TrimSpace(report.Summary[:limit])
	}
}

func safeCodingTerminalSummary(value string) (string, bool) {
	const marker = "[ABSOLUTE PATH REDACTED]"
	redacted := diagnostictrace.Redactor{}.RedactText(value, codingtask.MaxTerminalSummaryBytes)
	redacted = absolutePathTokenPattern.ReplaceAllString(redacted, "$1"+marker)
	trimmed := strings.TrimSpace(redacted)
	truncated := len(redacted) < len(value)
	if len(trimmed) > codingtask.MaxTerminalSummaryBytes {
		trimmed = diagnostictrace.Redactor{}.RedactText(trimmed, codingtask.MaxTerminalSummaryBytes)
		truncated = true
	}
	return trimmed, truncated
}

func terminalValidationStatus(status worker.CommandStatus) string {
	switch status {
	case worker.CommandSucceeded:
		return "succeeded"
	case worker.CommandFailed:
		return "failed"
	case worker.CommandCanceled:
		return "canceled"
	case worker.CommandTimedOut:
		return "timed_out"
	default:
		return ""
	}
}

func codingTaskOutcomeSummary(outcome codingTaskOutcome) string {
	switch outcome {
	case codingTaskOutcomeCompleted:
		return "Coding task completed."
	case codingTaskOutcomeCanceled:
		return "Coding task was canceled."
	case codingTaskOutcomeIdle:
		return "Coding worker became idle."
	case codingTaskOutcomeFailed:
		return "Coding task failed."
	default:
		return "Coding task outcome is uncertain."
	}
}

func codingTerminalChangedPaths(changes worktree.HandoffChangeset) ([]string, bool) {
	paths := make([]string, 0, codingtask.MaxTerminalPaths)
	seen := make(map[string]struct{})
	truncated := changes.Truncated
	for _, group := range [][]worktree.PathChange{
		changes.Staged,
		changes.Unstaged,
		changes.Untracked,
		changes.Unmerged,
	} {
		for _, change := range group {
			for _, path := range []string{change.Path, change.OriginalPath} {
				if path == "" {
					continue
				}
				if _, exists := seen[path]; exists {
					continue
				}
				if len(paths) >= codingtask.MaxTerminalPaths {
					truncated = true
					continue
				}
				seen[path] = struct{}{}
				paths = append(paths, path)
			}
		}
	}
	slices.Sort(paths)
	return paths, truncated
}

func codingHandoffCleanupState(class worktree.HandoffClass) string {
	switch class {
	case worktree.HandoffReady, worktree.HandoffChanges, worktree.HandoffConflicted:
		return "retained"
	default:
		return "uncertain"
	}
}
