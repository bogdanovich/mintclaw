package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

const (
	statusCardMaximumWidth = 100
	statusCardWideMinimum  = 48
)

type statusField struct {
	group int
	label string
	value string
}

// RenderStatusPlain returns a copy-safe, control-sanitized status report for
// noninteractive diagnostics. home may be empty; when supplied, paths within
// it are abbreviated with ~ without exposing additional host state.
func RenderStatusPlain(snapshot frontend.ThreadSnapshot, home string) string {
	fields := collectStatusFields(snapshot, home)
	lines := []string{"MintClaw coding session"}
	group := -1
	for _, field := range fields {
		if group >= 0 && field.group != group {
			lines = append(lines, "")
		}
		group = field.group
		if field.label == "" {
			lines = append(lines, "  "+field.value)
			continue
		}
		lines = append(lines, field.label+": "+field.value)
	}
	return strings.Join(lines, "\n")
}

func renderStatusCard(snapshot frontend.ThreadSnapshot, terminalWidth int, home string) []string {
	width := min(max(1, terminalWidth), statusCardMaximumWidth)
	if width < 8 {
		return wrapStatusLines(strings.Split(RenderStatusPlain(snapshot, home), "\n"), width)
	}
	innerWidth := width - 2
	content := renderStatusCardContent(collectStatusFields(snapshot, home), innerWidth)
	lines := make([]string, 0, len(content)+2)
	lines = append(lines, "╭"+strings.Repeat("─", innerWidth)+"╮")
	for _, line := range content {
		line = clipLine(line, innerWidth)
		padding := max(0, innerWidth-ansi.StringWidth(line))
		lines = append(lines, "│"+line+strings.Repeat(" ", padding)+"│")
	}
	lines = append(lines, "╰"+strings.Repeat("─", innerWidth)+"╯")
	return lines
}

func renderStatusCardContent(fields []statusField, width int) []string {
	if width <= 0 {
		return nil
	}
	header := clipLine("  MintClaw coding session", width)
	lines := []string{header}
	if width < statusCardWideMinimum {
		return append(lines, renderNarrowStatusFields(fields, width)...)
	}
	return append(lines, renderWideStatusFields(fields, width)...)
}

func renderWideStatusFields(fields []statusField, width int) []string {
	labelWidth := 0
	for _, field := range fields {
		labelWidth = max(labelWidth, ansi.StringWidth(field.label))
	}
	labelWidth = min(labelWidth, 18)
	prefixWidth := 2 + labelWidth + 2
	valueWidth := max(1, width-prefixWidth-2)
	lines := make([]string, 0, len(fields)+4)
	group := -1
	for _, field := range fields {
		if group < 0 || field.group != group {
			lines = append(lines, "")
		}
		group = field.group
		wrapped := wrapStatusValue(field.value, valueWidth)
		label := field.label
		if ansi.StringWidth(label) > labelWidth {
			label = clipLine(label, labelWidth)
		}
		padding := max(0, labelWidth-ansi.StringWidth(label))
		prefix := "  " + strings.Repeat(" ", padding) + label + "  "
		continuation := strings.Repeat(" ", prefixWidth)
		for index, part := range wrapped {
			if index == 0 {
				lines = append(lines, prefix+part)
			} else {
				lines = append(lines, continuation+part)
			}
		}
	}
	return lines
}

func renderNarrowStatusFields(fields []statusField, width int) []string {
	lines := make([]string, 0, len(fields)*2)
	group := -1
	for _, field := range fields {
		if group < 0 || field.group != group {
			lines = append(lines, "")
		}
		group = field.group
		if field.label != "" {
			lines = append(lines, clipLine("  "+field.label, width))
		}
		indent := "    "
		for _, part := range wrapStatusValue(field.value, max(1, width-ansi.StringWidth(indent))) {
			lines = append(lines, indent+part)
		}
	}
	return lines
}

func wrapStatusLines(lines []string, width int) []string {
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		wrapped = append(wrapped, wrapStatusValue(line, width)...)
	}
	return wrapped
}

func wrapStatusValue(value string, width int) []string {
	value = strings.TrimSpace(sanitizeTerminalText(value))
	if value == "" {
		return []string{"unavailable"}
	}
	parts := strings.Split(ansi.Wrap(value, max(1, width), ""), "\n")
	for index := range parts {
		parts[index] = clipLine(parts[index], max(1, width))
	}
	return parts
}

func collectStatusFields(snapshot frontend.ThreadSnapshot, home string) []statusField {
	fields := make([]statusField, 0, 24)
	add := func(group int, label, value string) {
		fields = append(fields, statusField{
			group: group,
			label: boundedSingleLine(label, 64),
			value: boundedSingleLine(value, 1024),
		})
	}

	if snapshot.Runtime != nil && strings.TrimSpace(snapshot.Runtime.Version) != "" {
		add(0, "Version", snapshot.Runtime.Version)
	}
	if title := strings.TrimSpace(snapshot.Metadata.Title); title != "" {
		add(0, "Thread", title)
	}
	add(0, "Session", snapshot.ThreadID)
	add(0, "State", statusSessionState(snapshot))
	add(0, "Activity", activityStatus(snapshot))

	add(1, "Model", fallbackStatusValue(snapshot.Metadata.Model))
	if provider := strings.TrimSpace(snapshot.Metadata.Provider); provider != "" {
		add(1, "Provider", provider)
	}
	add(1, "Reasoning", statusReasoning(snapshot.Runtime))

	add(2, "Project", statusPathDisplay(snapshot.Metadata.ProjectRoot, home))
	add(2, "Directory", statusPathDisplay(snapshot.Metadata.CWD, home))
	workspace := statusWorkspaceSnapshot(snapshot)
	if workspace != nil {
		add(2, "Branch", branchStatus(workspace))
		add(2, "Repository", repositoryStatus(
			workspace.Git.Available,
			workspace.Git.StatusAvailable,
			workspace.Git.Dirty,
		))
	}
	appendStatusInstructionFields(&fields, snapshot, home)

	add(3, "Context", strings.TrimPrefix(contextStatus(snapshot.ContextUsage), "context "))
	appendStatusCompactionFields(&fields, snapshot.LastCompaction)
	add(3, "Permissions", statusPermission(snapshot.Runtime))
	add(3, "Autonomy", statusAutonomy(snapshot.Runtime))
	if account := statusAccount(snapshot.Runtime); account != "" {
		add(3, "Account", account)
	}

	appendStatusPlanFields(&fields, snapshot.CurrentPlan())
	return fields
}

func statusSessionState(snapshot frontend.ThreadSnapshot) string {
	opening := "new"
	if snapshot.Runtime != nil && snapshot.Runtime.Resumed {
		opening = "resumed"
	}
	return opening + " · " + threadLifecycleStatus(snapshot.Metadata.Archived)
}

func statusReasoning(runtimeStatus *frontend.RuntimeStatus) string {
	if runtimeStatus == nil || strings.TrimSpace(runtimeStatus.ReasoningEffort) == "" {
		return "unavailable"
	}
	value := runtimeStatus.ReasoningEffort
	if !runtimeStatus.ReasoningConfigured {
		value += " (default)"
	}
	return value
}

func statusPermission(runtimeStatus *frontend.RuntimeStatus) string {
	if runtimeStatus == nil {
		return "unavailable"
	}
	switch runtimeStatus.Permission {
	case frontend.PermissionFullAccess:
		return "full access"
	case frontend.PermissionReadOnly:
		return "read only"
	default:
		return "unavailable"
	}
}

func statusAutonomy(runtimeStatus *frontend.RuntimeStatus) string {
	if runtimeStatus != nil && runtimeStatus.Autonomy == frontend.AutonomyYolo {
		return "autonomous (--yolo default; no approval prompts)"
	}
	return "unavailable"
}

func statusAccount(runtimeStatus *frontend.RuntimeStatus) string {
	if runtimeStatus == nil || runtimeStatus.Account == nil {
		return ""
	}
	account := runtimeStatus.Account
	parts := make([]string, 0, 3)
	if provider := strings.TrimSpace(account.Provider); provider != "" {
		parts = append(parts, provider)
	}
	if method := strings.TrimSpace(account.AuthMethod); method != "" {
		parts = append(parts, strings.ReplaceAll(method, "_", " "))
	}
	if state := strings.TrimSpace(string(account.State)); state != "" {
		parts = append(parts, strings.ReplaceAll(state, "_", " "))
	}
	return strings.Join(parts, " · ")
}

func statusWorkspaceSnapshot(snapshot frontend.ThreadSnapshot) *codingworkspace.Snapshot {
	if snapshot.RepositoryStatus != nil {
		workspace := snapshot.RepositoryStatus.Snapshot
		return &workspace
	}
	return snapshot.Workspace
}

func appendStatusInstructionFields(fields *[]statusField, snapshot frontend.ThreadSnapshot, home string) {
	runtimeStatus := snapshot.Runtime
	if runtimeStatus == nil {
		*fields = append(*fields, statusField{group: 2, label: "Instructions", value: "unavailable"})
		return
	}
	if len(runtimeStatus.InstructionSources) == 0 {
		*fields = append(*fields, statusField{group: 2, label: "Instructions", value: "none found"})
	} else {
		for index, source := range runtimeStatus.InstructionSources {
			label := ""
			if index == 0 {
				label = "Instructions"
			}
			*fields = append(*fields, statusField{
				group: 2,
				label: label,
				value: statusInstructionSource(source, snapshot.Metadata.ProjectRoot, home),
			})
		}
	}
	if runtimeStatus.InstructionWarningCount > 0 {
		*fields = append(*fields, statusField{
			group: 2,
			label: "",
			value: fmt.Sprintf("%d loading warning(s)", runtimeStatus.InstructionWarningCount),
		})
	}
	if runtimeStatus.InstructionSourcesTruncated {
		*fields = append(*fields, statusField{
			group: 2,
			label: "",
			value: "additional instruction sources omitted",
		})
	}
}

func statusInstructionSource(source frontend.InstructionSource, projectRoot, home string) string {
	path := strings.TrimSpace(source.Path)
	if !source.Global {
		if relative, ok := statusRelativePath(projectRoot, path); ok {
			path = relative
		}
	}
	path = statusPathDisplay(path, home)
	if path == "unavailable" && strings.TrimSpace(source.Label) != "" {
		path = source.Label
	}
	suffixes := make([]string, 0, 2)
	if source.Global {
		suffixes = append(suffixes, "global")
	}
	if source.Truncated {
		suffixes = append(suffixes, "truncated")
	}
	if len(suffixes) > 0 {
		path += " (" + strings.Join(suffixes, ", ") + ")"
	}
	return path
}

func statusPathDisplay(path, home string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "unavailable"
	}
	if relative, ok := statusRelativePath(home, path); ok {
		if relative == "." {
			return "~"
		}
		return filepath.Join("~", relative)
	}
	return path
}

func statusRelativePath(root, path string) (string, bool) {
	root = strings.TrimSpace(root)
	path = strings.TrimSpace(path)
	if root == "" || path == "" {
		return "", false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func appendStatusCompactionFields(fields *[]statusField, compaction *frontend.CompactionState) {
	if compaction == nil {
		return
	}
	value := string(compaction.Status) + " (" + compactionMode(compaction) + ")"
	if compaction.TokenCountsObserved {
		value += fmt.Sprintf(
			" · %s → %s tokens · %s saved",
			formatTokenCount(compaction.TokensBefore),
			formatTokenCount(compaction.TokensAfter),
			formatTokenCount(compaction.TokensSaved),
		)
	}
	*fields = append(*fields, statusField{group: 3, label: "Compaction", value: value})
}

func appendStatusPlanFields(fields *[]statusField, plan *frontend.PlanState) {
	if plan == nil {
		return
	}
	completed := 0
	for _, step := range plan.Steps {
		if step.Status == frontend.PlanStepCompleted {
			completed++
		}
	}
	*fields = append(*fields, statusField{
		group: 4,
		label: "Plan",
		value: fmt.Sprintf("%d/%d completed", completed, len(plan.Steps)),
	})
	if explanation := strings.TrimSpace(plan.Explanation); explanation != "" {
		*fields = append(*fields, statusField{group: 4, label: "", value: explanation})
	}
	for _, step := range plan.Steps {
		glyph, _ := planStepCellStyle(step.Status)
		*fields = append(*fields, statusField{group: 4, label: "", value: glyph + " " + step.Step})
	}
	if plan.Truncated {
		*fields = append(*fields, statusField{group: 4, label: "", value: "[plan observation truncated]"})
	}
}

func statusHomeDirectory(environment []string) string {
	if home := strings.TrimSpace(environmentValue(environment, "HOME")); home != "" {
		return filepath.Clean(home)
	}
	if home := strings.TrimSpace(environmentValue(environment, "USERPROFILE")); home != "" {
		return filepath.Clean(home)
	}
	drive := strings.TrimSpace(environmentValue(environment, "HOMEDRIVE"))
	path := strings.TrimSpace(environmentValue(environment, "HOMEPATH"))
	if drive != "" && path != "" {
		return filepath.Clean(drive + path)
	}
	return ""
}
