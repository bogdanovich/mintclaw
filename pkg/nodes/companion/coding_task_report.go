package companion

import (
	"net/url"
	"path/filepath"
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

var externalEffectURLPattern = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)

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
	if active.profile == codingtask.TaskModeProjectYolo {
		var uncertain bool
		report.ExternalEffects, report.EffectsTruncated, uncertain = active.externalEffectReceipts(items, result)
		if uncertain {
			report.Unresolved = "one or more external effects require operator verification"
		}
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
	for report.Validate() != nil && len(report.ExternalEffects) > 0 {
		report.EffectsTruncated = true
		report.ExternalEffects = report.ExternalEffects[:len(report.ExternalEffects)/2]
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

func (active *activeCodingTask) externalEffectReceipts(
	items []worker.Item,
	result codingTaskProcessResult,
) ([]codingtask.ExternalEffectReceipt, bool, bool) {
	receipts := make([]codingtask.ExternalEffectReceipt, 0, codingtask.MaxTerminalEffects)
	seen := make(map[string]struct{})
	truncated := false
	uncertain := false
	appendReceipt := func(receipt codingtask.ExternalEffectReceipt) {
		if receipt.Validate() != nil {
			return
		}
		key := string(receipt.Kind) + "\x00" + string(receipt.Outcome) + "\x00" + receipt.Reference
		if _, duplicate := seen[key]; duplicate {
			return
		}
		if len(receipts) >= codingtask.MaxTerminalEffects {
			truncated = true
			return
		}
		seen[key] = struct{}{}
		receipts = append(receipts, receipt)
		if receipt.Outcome == codingtask.ExternalEffectUncertain {
			uncertain = true
		}
	}
	if result.handoff != nil && result.handoff.Head != "" && result.handoff.Head != active.baseGitHead {
		appendReceipt(codingtask.ExternalEffectReceipt{
			Kind: codingtask.ExternalEffectCommit, Outcome: codingtask.ExternalEffectVerified,
			Reference: result.handoff.Head,
		})
	}
	for _, item := range items {
		if item.Tool == nil || item.Tool.Command == nil {
			continue
		}
		command := item.Tool.Command
		for _, kind := range externalEffectKinds(command.Command) {
			outcome := externalEffectOutcome(command.Status)
			reference := externalEffectReference(kind, command, active.branch, result)
			appendReceipt(codingtask.ExternalEffectReceipt{
				Kind: kind, Outcome: outcome, Reference: reference,
			})
		}
	}
	return receipts, truncated, uncertain
}

func externalEffectKinds(command string) []codingtask.ExternalEffectKind {
	var kinds []codingtask.ExternalEffectKind
	for _, segment := range splitExternalEffectCommands(command) {
		tokens := externalEffectCommandTokens(segment)
		if len(tokens) == 0 {
			continue
		}
		first := tokens[0]
		switch {
		case first == "git" && len(tokens) > 1 && tokens[1] == "push":
			kinds = append(kinds, codingtask.ExternalEffectPush)
		case first == "gh" && len(tokens) > 2 && tokens[1] == "pr" &&
			containsExternalEffectAction(tokens[2], "create", "edit", "merge", "close", "reopen", "comment", "review"):
			kinds = append(kinds, codingtask.ExternalEffectPullRequest)
		case first == "gh" && len(tokens) > 2 && tokens[1] == "repo" && tokens[2] == "create":
			kinds = append(kinds, codingtask.ExternalEffectRepository)
		case first == "gh" && len(tokens) > 2 && tokens[1] == "release" &&
			containsExternalEffectAction(tokens[2], "create", "edit", "delete", "upload"):
			kinds = append(kinds, codingtask.ExternalEffectRelease)
		case isReleaseCommand(tokens):
			kinds = append(kinds, codingtask.ExternalEffectRelease)
		case isDeploymentCommand(tokens):
			kinds = append(kinds, codingtask.ExternalEffectDeployment)
		}
	}
	return kinds
}

func externalEffectOutcome(status worker.CommandStatus) codingtask.ExternalEffectOutcome {
	switch status {
	case worker.CommandSucceeded:
		return codingtask.ExternalEffectVerified
	case worker.CommandFailed:
		return codingtask.ExternalEffectFailed
	default:
		return codingtask.ExternalEffectUncertain
	}
}

func externalEffectReference(
	kind codingtask.ExternalEffectKind,
	command *worker.Command,
	branch string,
	result codingTaskProcessResult,
) string {
	if kind != codingtask.ExternalEffectCommit && kind != codingtask.ExternalEffectPush {
		if reference := firstSafeExternalEffectURL(command); reference != "" {
			return reference
		}
	}
	head := ""
	if result.handoff != nil {
		head = result.handoff.Head
		if len(head) > 12 {
			head = head[:12]
		}
	}
	if (kind == codingtask.ExternalEffectCommit || kind == codingtask.ExternalEffectPush) && branch != "" {
		if head != "" {
			return branch + "@" + head
		}
		return branch
	}
	return string(kind)
}

func firstSafeExternalEffectURL(command *worker.Command) string {
	if command == nil {
		return ""
	}
	values := []string{command.Stdout, command.Stderr, command.Output}
	for _, entry := range command.Transcript {
		values = append(values, entry.Text)
	}
	for _, value := range values {
		for _, candidate := range externalEffectURLPattern.FindAllString(value, -1) {
			candidate = strings.TrimRight(candidate, ".,;:!?)]}>")
			parsed, err := url.Parse(candidate)
			if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") &&
				parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
				len(candidate) <= codingtask.MaxEffectReferenceBytes &&
				(diagnostictrace.Redactor{}).RedactText(candidate, len(candidate)) == candidate {
				return candidate
			}
		}
	}
	return ""
}

func splitExternalEffectCommands(command string) []string {
	var result []string
	var current strings.Builder
	singleQuoted := false
	doubleQuoted := false
	escaped := false
	flush := func() {
		if value := strings.TrimSpace(current.String()); value != "" {
			result = append(result, value)
		}
		current.Reset()
	}
	for index := 0; index < len(command); index++ {
		character := command[index]
		if escaped {
			current.WriteByte(character)
			escaped = false
			continue
		}
		if character == '\\' {
			current.WriteByte(character)
			escaped = true
			continue
		}
		switch character {
		case '\'':
			if !doubleQuoted {
				singleQuoted = !singleQuoted
			}
			current.WriteByte(character)
		case '"':
			if !singleQuoted {
				doubleQuoted = !doubleQuoted
			}
			current.WriteByte(character)
		case ';', '|', '&':
			if singleQuoted || doubleQuoted {
				current.WriteByte(character)
				continue
			}
			flush()
			if index+1 < len(command) && command[index+1] == character {
				index++
			}
		default:
			current.WriteByte(character)
		}
	}
	flush()
	return result
}

func externalEffectCommandTokens(command string) []string {
	fields := strings.Fields(command)
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		token := strings.ToLower(filepath.Base(strings.Trim(field, `"'`)))
		if token == "command" || token == "env" || strings.Contains(token, "=") && !strings.Contains(token, "/") {
			continue
		}
		tokens = append(tokens, token)
	}
	return tokens
}

func containsExternalEffectAction(value string, allowed ...string) bool {
	return slices.Contains(allowed, value)
}

func isReleaseCommand(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	return tokens[0] == "goreleaser" && len(tokens) > 1 && tokens[1] == "release" ||
		(tokens[0] == "make" || tokens[0] == "just") && len(tokens) > 1 && tokens[1] == "release" ||
		tokens[0] == "npm" && len(tokens) > 2 && tokens[1] == "run" && tokens[2] == "release"
}

func isDeploymentCommand(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	first := tokens[0]
	if first == "deploy" || strings.HasPrefix(first, "deploy-") || strings.HasSuffix(first, "-deploy") {
		return true
	}
	return (first == "make" || first == "just") && len(tokens) > 1 && tokens[1] == "deploy" ||
		first == "npm" && len(tokens) > 2 && tokens[1] == "run" && tokens[2] == "deploy" ||
		first == "kubectl" && len(tokens) > 1 && tokens[1] == "apply" ||
		first == "helm" && len(tokens) > 1 && (tokens[1] == "upgrade" || tokens[1] == "install") ||
		first == "terraform" && len(tokens) > 1 && tokens[1] == "apply" ||
		(first == "vercel" || first == "netlify") && (len(tokens) == 1 || tokens[1] == "deploy") ||
		first == "fly" && len(tokens) > 1 && tokens[1] == "deploy" ||
		first == "railway" && len(tokens) > 1 && tokens[1] == "up" ||
		first == "serverless" && len(tokens) > 1 && tokens[1] == "deploy"
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
