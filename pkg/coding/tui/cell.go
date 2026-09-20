package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

// cellRenderMode keeps the bounded viewport, complete evidence, and
// copy-friendly representations distinct. The semantic cell owns all three;
// callers never reconstruct full evidence from a compact preview.
type cellRenderMode uint8

const (
	cellRenderCompact cellRenderMode = iota
	cellRenderFull
	cellRenderPlain
)

type cellTheme uint8

const (
	cellThemeUnknown cellTheme = iota
	cellThemeDark
	cellThemeLight
)

type cellColorLevel uint8

const (
	cellColorNone cellColorLevel = iota
	cellColorANSI16
	cellColorANSI256
	cellColorTrueColor
)

// cellRenderContext contains deterministic presentation capabilities only. It
// intentionally has no Bubble Tea, viewport, terminal, clock, or runtime
// dependency.
type cellRenderContext struct {
	Width      int
	Theme      cellTheme
	ColorLevel cellColorLevel
}

type cellIdentity struct {
	ID        string
	Kind      frontend.PresentationKind
	Sequence  uint64
	Revision  uint64
	Lifecycle frontend.PresentationLifecycle
}

// cellStyleRole expresses semantic intent without choosing a terminal color.
// A later renderer maps roles to an admitted palette; plain mode removes them.
type cellStyleRole uint8

const (
	cellStyleDefault cellStyleRole = iota
	cellStyleMuted
	cellStyleAccent
	cellStylePath
	cellStyleSuccess
	cellStyleFailure
	cellStyleInsertion
	cellStyleDeletion
	cellStylePlanTitle
	cellStylePlanExplanation
	cellStylePlanCompleted
	cellStylePlanCurrent
	cellStylePlanPending
	cellStyleDiffGutter
	cellStyleDiffGutterInsertion
	cellStyleDiffGutterDeletion
	cellStyleSyntaxKeyword
	cellStyleSyntaxString
	cellStyleSyntaxNumber
	cellStyleSyntaxComment
	cellStyleSyntaxType
	cellStyleMarkdownHeading
	cellStyleMarkdownStrong
	cellStyleMarkdownEmphasis
	cellStyleMarkdownStrongEmphasis
	cellStyleMarkdownStrikethrough
	cellStyleMarkdownCode
	cellStyleMarkdownLink
	cellStyleMarkdownQuote
	cellStyleMarkdownTableHeader
	cellStyleMarkdownTableRule
)

type cellRowStyle uint8

const (
	cellRowDefault cellRowStyle = iota
	cellRowInsertion
	cellRowDeletion
)

type cellSpan struct {
	Text string
	Role cellStyleRole
}

type cellLine struct {
	Spans           []cellSpan
	RowStyle        cellRowStyle
	structuralBlank bool
}

func styledCellLine(value string, role cellStyleRole) cellLine {
	return cellLine{Spans: []cellSpan{{Text: value, Role: role}}}
}

// statusTitleCellLine keeps lifecycle color on the compact status marker and
// renders the human-readable title in the terminal foreground. This mirrors
// Codex's visual hierarchy: a small green/red/cyan signal followed by a bold,
// neutral label.
func statusTitleCellLine(value string, markerRole cellStyleRole) cellLine {
	runes := []rune(value)
	if len(runes) == 0 {
		return cellLine{}
	}
	return cellLine{Spans: []cellSpan{
		{Text: string(runes[0]), Role: markerRole},
		{Text: string(runes[1:]), Role: cellStyleMarkdownStrong},
	}}
}

func (line cellLine) plainText() string {
	var text strings.Builder
	for _, span := range line.Spans {
		text.WriteString(span.Text)
	}
	return text.String()
}

type cellDocument struct {
	Lines             []cellLine
	Truncated         bool
	TruncationVisible bool
}

func (document cellDocument) plainText() string {
	lines := make([]string, 0, len(document.Lines))
	for _, line := range document.Lines {
		lines = append(lines, line.plainText())
	}
	return strings.Join(lines, "\n")
}

// semanticCell is the renderer-neutral boundary for one authoritative
// frontend presentation item.
type semanticCell interface {
	Identity() cellIdentity
	Render(cellRenderContext, cellRenderMode) cellDocument
}

type cellRenderCacheKey struct {
	Context cellRenderContext
	Mode    cellRenderMode
}

// A cell normally needs one compact viewport rendering and one copy-safe
// transcript rendering. Keep a little room for a resize or capability change,
// but never retain one document for every terminal width seen by a long-lived
// session.
const maxCellRenderCacheEntries = 4

func storeCellRenderDocument(
	cache map[cellRenderCacheKey]cellDocument,
	key cellRenderCacheKey,
	document cellDocument,
) map[cellRenderCacheKey]cellDocument {
	if cache == nil || len(cache) >= maxCellRenderCacheEntries {
		cache = make(map[cellRenderCacheKey]cellDocument, maxCellRenderCacheEntries)
	}
	cache[key] = document
	return cache
}

type presentationCell struct {
	item         frontend.PresentationItem
	renderCache  map[cellRenderCacheKey]cellDocument
	markdown     *parsedMarkdownDocument
	renderMisses uint64
}

func newPresentationCell(item frontend.PresentationItem) *presentationCell {
	return &presentationCell{item: cloneCellPresentationItem(item)}
}

func (cell *presentationCell) Identity() cellIdentity {
	return cellIdentity{
		ID:        cell.item.ID,
		Kind:      cell.item.Kind,
		Sequence:  cell.item.Sequence,
		Revision:  cell.item.Revision,
		Lifecycle: cell.item.Lifecycle,
	}
}

func (cell *presentationCell) Render(context cellRenderContext, mode cellRenderMode) cellDocument {
	context.Width = max(1, context.Width)
	key := cellRenderCacheKey{Context: context, Mode: mode}
	if document, ok := cell.renderCache[key]; ok {
		return document
	}
	var document cellDocument
	if cell.item.Kind == frontend.PresentationPlanUpdate {
		document = cell.planDocument(context.Width)
	} else if cell.item.Kind == frontend.PresentationCompaction {
		document = cell.compactionDocument(context.Width, mode)
	} else if cell.item.Kind == frontend.PresentationTurnSeparator {
		document = cell.turnBoundaryDocument(context.Width)
	} else if cell.markdownMessage() {
		document = cell.markdownMessageDocument(context.Width)
	} else if cell.item.Tool != nil && cell.item.Tool.Command != nil {
		document = wrapCellDocument(
			cell.commandDocument(*cell.item.Tool, *cell.item.Tool.Command, mode, context.Width),
			context.Width,
		)
	} else if cell.item.Tool != nil && cell.item.Tool.RepositoryDiff != nil {
		document = cell.repositoryDiffDocument(*cell.item.Tool, *cell.item.Tool.RepositoryDiff, mode, context.Width)
	} else if cell.item.Tool != nil && cell.item.Tool.Exploration != nil {
		document = wrapCellDocument(
			cell.explorationDocument(*cell.item.Tool, *cell.item.Tool.Exploration, mode),
			context.Width,
		)
	} else if cell.item.Tool != nil && cell.item.Tool.MCP != nil {
		document = cell.mcpDocument(*cell.item.Tool, *cell.item.Tool.MCP, mode, context.Width)
	} else {
		document = wrapCellDocument(cell.semanticDocument(mode), context.Width)
	}
	if mode == cellRenderPlain {
		for lineIndex := range document.Lines {
			document.Lines[lineIndex].RowStyle = cellRowDefault
			for spanIndex := range document.Lines[lineIndex].Spans {
				document.Lines[lineIndex].Spans[spanIndex].Role = cellStyleDefault
			}
		}
	}
	cell.renderCache = storeCellRenderDocument(cell.renderCache, key, document)
	cell.renderMisses++
	return document
}

func (cell *presentationCell) markdownMessage() bool {
	return cell.item.Message != nil &&
		(cell.item.Kind == frontend.PresentationAssistantMessage ||
			cell.item.Kind == frontend.PresentationFinalAnswer)
}

func (cell *presentationCell) renderMissCount() uint64 {
	return cell.renderMisses
}

func (cell *presentationCell) semanticDocument(mode cellRenderMode) cellDocument {
	switch cell.item.Kind {
	case frontend.PresentationUserMessage,
		frontend.PresentationReasoning,
		frontend.PresentationToolMessage,
		frontend.PresentationWarning,
		frontend.PresentationError:
		return cell.messageDocument(mode)
	case frontend.PresentationToolCall:
		return cell.toolDocument(mode)
	case frontend.PresentationCompaction:
		return cell.compactionDocument(120, mode)
	case frontend.PresentationTurnSeparator:
		return cell.turnBoundaryDocument(120)
	default:
		return cellDocument{Lines: []cellLine{styledCellLine("• Unsupported presentation item", cellStyleFailure)}}
	}
}

func (cell *presentationCell) messageDocument(_ cellRenderMode) cellDocument {
	message := cell.item.Message
	if message == nil {
		return cellDocument{}
	}
	text := sanitizeTerminalText(message.Text)
	if strings.TrimSpace(text) == "" && !message.Truncated {
		return cellDocument{}
	}
	role := cellStyleDefault
	prefix := "• "
	switch cell.item.Kind {
	case frontend.PresentationUserMessage:
		prefix = "› "
	case frontend.PresentationReasoning:
		prefix = "• Reasoning\n  "
		role = cellStyleMuted
	case frontend.PresentationWarning:
		prefix = "! Warning\n  "
		role = cellStyleFailure
	case frontend.PresentationError:
		prefix = "! Error\n  "
		role = cellStyleFailure
	case frontend.PresentationToolMessage:
		prefix = "• Tool\n  "
	}
	return cellDocument{
		Lines:     logicalCellLines(prefix+text, role),
		Truncated: message.Truncated,
	}
}

func (cell *presentationCell) turnBoundaryDocument(width int) cellDocument {
	boundary := cell.item.Turn
	if boundary == nil {
		return cellDocument{}
	}
	role := cellStyleMuted
	label := ""
	duration := max(time.Duration(0), cell.item.Duration)
	durationLabel := ""
	if duration > time.Minute {
		durationLabel = formatWorkingElapsed(duration)
	}
	switch boundary.Outcome {
	case frontend.TurnOutcomeCompleted:
		if durationLabel != "" {
			label = "• Worked for " + durationLabel
		}
	case frontend.TurnOutcomeFailed:
		label = "! Work failed"
		role = cellStyleFailure
		if durationLabel != "" {
			label += " after " + durationLabel
		}
	case frontend.TurnOutcomeInterrupted:
		label = "! Work interrupted"
		role = cellStyleFailure
		if durationLabel != "" {
			label += " after " + durationLabel
		}
	case frontend.TurnOutcomeSuspended:
		label = "• Work paused"
		if durationLabel != "" {
			label += " after " + durationLabel
		}
	default:
		label = "? Work outcome unavailable"
	}
	separatorWidth := max(1, width)
	lines := []cellLine{styledCellLine(strings.Repeat("─", separatorWidth), cellStyleMuted)}
	if label != "" {
		lines = append(lines, styledCellLine(label, role))
	}
	return cellDocument{Lines: lines}
}

func (cell *presentationCell) compactionDocument(width int, mode cellRenderMode) cellDocument {
	compaction := cell.item.Compaction
	if compaction == nil {
		return cellDocument{}
	}
	title := "• Compacting context"
	role := cellStyleAccent
	switch compaction.Status {
	case frontend.CompactionCompleted:
		title = "• Context compacted"
		role = cellStyleSuccess
	case frontend.CompactionNoProgress:
		title = "• Context already compact"
		role = cellStyleMuted
	case frontend.CompactionFailed:
		title = "! Context compaction failed"
		role = cellStyleFailure
	case frontend.CompactionInterrupted:
		title = "! Context compaction interrupted"
		role = cellStyleFailure
	}
	if compaction.Background {
		title += " in background"
	}
	if compaction.Duration > 0 && compaction.Status != frontend.CompactionRunning &&
		compaction.Status != frontend.CompactionProgress {
		title += " · " + formatToolDuration(compaction.Duration)
	}
	lines := []cellLine{statusTitleCellLine(title, role)}
	if compaction.Status == frontend.CompactionCompleted && compaction.TokenCountsObserved {
		lines = append(lines, styledCellLine(
			fmt.Sprintf(
				"  %s → %s tokens · %s saved",
				formatTokenCount(compaction.TokensBefore),
				formatTokenCount(compaction.TokensAfter),
				formatTokenCount(compaction.TokensSaved),
			),
			cellStyleMuted,
		))
	}
	if mode != cellRenderCompact {
		lines = append(lines, styledCellLine("  trigger: "+compactionTrigger(compaction.Reason), cellStyleMuted))
		if compaction.TokenCountsObserved && compaction.Status != frontend.CompactionCompleted {
			lines = append(lines, styledCellLine(
				fmt.Sprintf(
					"  context: %s → %s tokens · %s saved",
					formatTokenCount(compaction.TokensBefore),
					formatTokenCount(compaction.TokensAfter),
					formatTokenCount(compaction.TokensSaved),
				),
				cellStyleMuted,
			))
		}
		if compaction.SummariesCreated > 0 {
			lines = append(lines, styledCellLine(
				fmt.Sprintf(
					"  summaries: %d total (%d leaf, %d condensed)",
					compaction.SummariesCreated,
					compaction.LeafSummaries,
					compaction.CondensedSummaries,
				),
				cellStyleMuted,
			))
		}
		if compaction.Status == frontend.CompactionFailed || compaction.Status == frontend.CompactionInterrupted {
			guidance := "current turn may stop"
			if compaction.Background {
				guidance = "work can continue"
			}
			lines = append(lines, styledCellLine("  "+guidance, cellStyleMuted))
		}
	}
	return wrapCellDocument(cellDocument{Lines: lines}, width)
}

func (cell *presentationCell) planDocument(width int) cellDocument {
	plan := cell.item.Plan
	if plan == nil {
		return cellDocument{}
	}
	lines := wrappedPlanText("Updated Plan", "• ", "  ", "", cellStylePlanTitle, width)
	bodyStarted := false
	appendBody := func(text, firstIndent, nextIndent, essentialPrefix string, role cellStyleRole) {
		outer := "    "
		if !bodyStarted {
			outer = "  └ "
		}
		lines = append(
			lines,
			wrappedPlanText(text, outer+firstIndent, "    "+nextIndent, essentialPrefix, role, width)...,
		)
		bodyStarted = true
	}
	if explanation := strings.TrimSpace(sanitizeTerminalText(plan.Explanation)); explanation != "" {
		appendBody(explanation, "", "", "", cellStylePlanExplanation)
	}
	for _, step := range plan.Steps {
		glyph, role := planStepCellStyle(step.Status)
		appendBody(sanitizeTerminalText(step.Step), glyph+" ", "  ", glyph, role)
	}
	if plan.Truncated {
		appendBody("[…truncated]", "", "", "", cellStyleMuted)
	}
	return cellDocument{Lines: lines, Truncated: plan.Truncated}
}

func planStepCellStyle(status frontend.PlanStepStatus) (string, cellStyleRole) {
	switch status {
	case frontend.PlanStepCompleted:
		return "✔", cellStylePlanCompleted
	case frontend.PlanStepInProgress:
		return "→", cellStylePlanCurrent
	default:
		return "□", cellStylePlanPending
	}
}

func wrappedPlanText(
	value, initialPrefix, continuationPrefix string,
	essentialPrefix string,
	role cellStyleRole,
	width int,
) []cellLine {
	width = max(1, width)
	value = expandCellTabs(sanitizeTerminalText(value), 4)
	logical := strings.Split(value, "\n")
	lines := make([]cellLine, 0, len(logical))
	first := true
	for _, text := range logical {
		continuation := fitPlanPrefix(continuationPrefix, "", width)
		prefix := continuation
		if first {
			prefix = initialPrefix
		}
		prefix = fitPlanPrefix(prefix, essentialPrefix, width)
		if essentialPrefix != "" && ansi.StringWidth(prefix) >= width {
			lines = append(lines, styledCellLine(prefix, role))
			first = false
			prefix = continuation
		}
		bodyWidth := max(1, width-max(ansi.StringWidth(prefix), ansi.StringWidth(continuation)))
		text = replaceOverwideCellGraphemes(text, bodyWidth)
		parts := strings.Split(ansi.Wrap(text, bodyWidth, ""), "\n")
		if len(parts) == 0 {
			parts = []string{""}
		}
		for index, part := range parts {
			linePrefix := prefix
			if !first || index > 0 {
				linePrefix = continuation
			}
			lines = append(lines, styledCellLine(linePrefix+part, role))
			first = false
		}
	}
	return lines
}

func fitPlanPrefix(prefix, essential string, width int) string {
	width = max(1, width)
	maximum := max(0, width-1)
	if ansi.StringWidth(prefix) <= maximum {
		return prefix
	}
	if essential == "" {
		return ""
	}
	if width == 1 {
		return ansi.Truncate(essential, width, "")
	}
	return ansi.Truncate(essential+" ", maximum, "")
}

func (cell *presentationCell) toolDocument(mode cellRenderMode) cellDocument {
	tool := cell.item.Tool
	if tool == nil {
		return cellDocument{}
	}
	if tool.Command != nil {
		return cell.commandDocument(*tool, *tool.Command, mode, 120)
	}
	if tool.RepositoryDiff != nil {
		return cell.repositoryDiffDocument(*tool, *tool.RepositoryDiff, mode, 120)
	}
	if tool.Exploration != nil {
		return cell.explorationDocument(*tool, *tool.Exploration, mode)
	}
	if tool.MCP != nil {
		return cell.mcpDocument(*tool, *tool.MCP, mode, 120)
	}
	name := strings.TrimSpace(sanitizeTerminalText(tool.Name))
	if name == "" {
		name = "tool"
	}
	title := "• Tool " + name + " [" + toolStatusLabel(tool.Status) + "]"
	if len(tool.WriteAudit) != 0 {
		title = "• Edited " + strconv.Itoa(len(tool.WriteAudit)) + " " + pluralize("file", len(tool.WriteAudit)) +
			" [" + toolStatusLabel(tool.Status) + "]"
	}
	if cell.item.Duration > 0 {
		title += " · " + cell.item.Duration.String()
	}
	lines := []cellLine{statusTitleCellLine(title, lifecycleCellRole(cell.item.Lifecycle))}
	for _, audit := range tool.WriteAudit {
		action := strings.TrimSpace(sanitizeTerminalText(audit.Action))
		path := strings.TrimSpace(sanitizeTerminalText(audit.Target))
		lines = append(lines, logicalCellLines("  "+action+" "+path, writeAuditCellRole(action, audit.Success))...)
	}
	if mode == cellRenderCompact {
		return cellDocument{Lines: lines, Truncated: tool.OutputTruncated || toolCommandTruncated(tool.Command)}
	}
	if output := sanitizeTerminalText(tool.Output); strings.TrimSpace(output) != "" {
		lines = append(
			lines,
			logicalCellLines("  output:\n"+indentCellEvidence(output), cellStyleDefault)...)
	}
	return cellDocument{Lines: lines, Truncated: tool.OutputTruncated || toolCommandTruncated(tool.Command)}
}

func (cell *presentationCell) mcpDocument(
	tool frontend.ToolState,
	observation frontend.MCPState,
	mode cellRenderMode,
	width int,
) cellDocument {
	title, role := mcpCellTitle(observation, tool.Duration)
	lines := []cellLine{statusTitleCellLine(title, role)}
	if purpose := strings.TrimSpace(sanitizeTerminalText(observation.Purpose)); purpose != "" {
		lines = append(lines, logicalCellLines("  purpose: "+purpose, cellStyleMuted)...)
	}
	if shape := strings.TrimSpace(sanitizeTerminalText(tool.Arguments)); shape != "" {
		lines = append(lines, logicalCellLines("  input: "+shape, cellStyleMuted)...)
	}
	header := wrapCellDocument(cellDocument{Lines: lines}, width)

	evidence := observation.Result
	evidenceRole := cellStyleMuted
	if observation.Error != "" {
		evidence = "Error: " + observation.Error
		evidenceRole = cellStyleFailure
	}
	evidence = strings.TrimSpace(sanitizeTerminalText(evidence))
	if evidence == "" {
		if observation.Truncated {
			header.Lines = append(
				header.Lines,
				styledCellLine("  [… MCP evidence bounded …]", cellStyleMuted),
			)
			header.Truncated = true
			header.TruncationVisible = true
		}
		return header
	}

	detailLines := make([]cellLine, 0, strings.Count(evidence, "\n")+1)
	for index, line := range strings.Split(evidence, "\n") {
		prefix := "    "
		if index == 0 {
			prefix = "  └ "
		}
		detailLines = append(detailLines, styledCellLine(prefix+line, evidenceRole))
	}
	details := wrapCellDocument(cellDocument{Lines: detailLines}, width)
	compactOmitted := false
	if mode == cellRenderCompact {
		const maximumMCPPreviewLines = 5
		if len(details.Lines) > maximumMCPPreviewLines {
			details.Lines = append(
				slices.Clone(details.Lines[:maximumMCPPreviewLines-1]),
				styledCellLine(mcpOmissionMarker(width), cellStyleMuted),
			)
			compactOmitted = true
		}
	}
	header.Lines = append(header.Lines, details.Lines...)
	if observation.Truncated && !compactOmitted {
		header.Lines = append(header.Lines, styledCellLine("    [… MCP evidence bounded …]", cellStyleMuted))
	}
	header.Truncated = observation.Truncated || compactOmitted
	header.TruncationVisible = header.Truncated
	return header
}

func mcpOmissionMarker(width int) string {
	width = max(1, width)
	if width <= 4 {
		return ansi.Truncate("…", width, "")
	}
	return "    " + ansi.Truncate("… result omitted; Ctrl+T opens full transcript …", width-4, "")
}

func mcpCellTitle(observation frontend.MCPState, duration time.Duration) (string, cellStyleRole) {
	identity := strings.TrimSpace(sanitizeTerminalText(observation.Server)) + "." +
		strings.TrimSpace(sanitizeTerminalText(observation.Tool))
	identity = strings.Trim(identity, ".")
	if identity == "" {
		identity = "MCP tool"
	}
	title := "? MCP outcome unknown " + identity
	role := cellStyleMuted
	switch observation.Outcome {
	case frontend.MCPOutcomeRunning:
		title = "• Calling " + identity
		role = cellStyleAccent
	case frontend.MCPOutcomeSucceeded:
		title = "• Called " + identity
		role = cellStyleSuccess
	case frontend.MCPOutcomeFailed:
		title = "! MCP call failed " + identity
		role = cellStyleFailure
	case frontend.MCPOutcomeCanceled:
		title = "! MCP call canceled " + identity
		role = cellStyleFailure
	case frontend.MCPOutcomeTimedOut:
		title = "! MCP call timed out " + identity
		role = cellStyleFailure
	case frontend.MCPOutcomeUncertain:
		title = "! MCP outcome uncertain " + identity
		role = cellStyleFailure
	}
	switch observation.LoopHaltCode {
	case "identical_call_emergency_halt":
		title += " · turn halted: no progress"
		role = cellStyleFailure
	case "same_tool_failure_halt":
		title += " · turn halted: repeated failures"
		role = cellStyleFailure
	}
	if observation.LoopHaltCount > 0 && observation.LoopHaltThreshold > 0 {
		title += fmt.Sprintf(" (%d/%d)", observation.LoopHaltCount, observation.LoopHaltThreshold)
	}
	return title + commandDurationSuffix(duration), role
}

func (cell *presentationCell) repositoryDiffDocument(
	tool frontend.ToolState,
	diff codingworkspace.DiffResult,
	mode cellRenderMode,
	width int,
) cellDocument {
	role := lifecycleCellRole(cell.item.Lifecycle)
	title := fmt.Sprintf(
		"• Repository diff observed (%s) · %d %s · +%d -%d",
		repositoryDiffTargetLabel(diff.Target),
		len(diff.Files),
		pluralize("file", len(diff.Files)),
		diff.Additions,
		diff.Deletions,
	)
	if diff.UnavailableReason != "" {
		title = "! Repository diff unavailable"
		role = cellStyleFailure
	}
	if tool.Status == frontend.ToolFailed {
		title = "! Repository diff failed"
		role = cellStyleFailure
	}
	title += commandDurationSuffix(tool.Duration)
	lines := []cellLine{statusTitleCellLine(title, role)}
	if mode == cellRenderCompact {
		const compactFileLimit = 6
		for index, file := range diff.Files {
			if index >= compactFileLimit {
				lines = append(lines, styledCellLine("  [… more files in full transcript …]", cellStyleMuted))
				break
			}
			lines = append(lines, logicalCellLines(repositoryDiffFileSummary(file), cellStyleMuted)...)
		}
		if diff.Truncated || diff.Stale {
			lines = append(lines, styledCellLine("  [… diff evidence incomplete or stale …]", cellStyleMuted))
		}
		return wrapCellDocument(cellDocument{
			Lines:             lines,
			Truncated:         diff.Truncated || diff.Stale,
			TruncationVisible: diff.Truncated || diff.Stale || len(diff.Files) > compactFileLimit,
		}, width)
	}

	titleDocument := wrapCellDocument(cellDocument{Lines: lines}, width)
	lines = append(titleDocument.Lines, renderRepositoryDiffEvidence(diff, width)...)
	return cellDocument{
		Lines:             lines,
		Truncated:         diff.Truncated || diff.Stale,
		TruncationVisible: diff.Truncated || diff.Stale,
	}
}

func repositoryDiffTargetLabel(target codingworkspace.DiffTarget) string {
	label := boundedSingleLine(string(target.Kind), 64)
	if label == "" {
		label = "unknown"
	}
	if target.Ref != "" {
		label += " " + boundedSingleLine(target.Ref, 4096)
	}
	return label
}

func repositoryDiffFileSummary(file codingworkspace.DiffFile) string {
	status := boundedSingleLine(file.Status, 64)
	if status == "" {
		status = "?"
	}
	path := boundedSingleLine(file.Path, 4096)
	if file.OriginalPath != "" {
		path = boundedSingleLine(file.OriginalPath, 4096) + " -> " + path
	}
	provenance := repositoryDiffProvenanceLabel(file.Provenance)
	if provenance != "" {
		provenance = " · " + provenance
	}
	states := make([]string, 0, 4)
	if file.Binary {
		states = append(states, "binary")
	}
	if file.Submodule {
		states = append(states, "submodule")
	}
	if file.Symlink {
		states = append(states, "symlink")
	}
	if file.Omitted != "" {
		states = append(states, "omitted: "+boundedSingleLine(file.Omitted, 1024))
	}
	if file.Truncated {
		states = append(states, "truncated")
	}
	stateSuffix := ""
	if len(states) != 0 {
		stateSuffix = " · [" + strings.Join(states, ", ") + "]"
	}
	return fmt.Sprintf(
		"  %s %s · +%d -%d%s%s",
		status,
		path,
		file.Additions,
		file.Deletions,
		provenance,
		stateSuffix,
	)
}

func repositoryDiffProvenanceLabel(provenance codingworkspace.ProvenanceKind) string {
	switch provenance {
	case codingworkspace.ProvenancePreExisting:
		return "pre-existing"
	case codingworkspace.ProvenanceFirstObservedDuringThread:
		return "first observed during thread"
	case codingworkspace.ProvenanceResolvedSinceBaseline:
		return "resolved since baseline"
	case codingworkspace.ProvenanceIndeterminate:
		return "provenance indeterminate"
	default:
		return ""
	}
}

func (cell *presentationCell) explorationDocument(
	tool frontend.ToolState,
	exploration frontend.ExplorationState,
	mode cellRenderMode,
) cellDocument {
	title := "• Explored"
	role := cellStyleSuccess
	switch tool.Status {
	case frontend.ToolRunning:
		title = "• Exploring"
		role = cellStyleAccent
	case frontend.ToolFailed:
		title = "! Exploration failed"
		role = cellStyleFailure
	case frontend.ToolInterrupted:
		title = "! Exploration interrupted"
		role = cellStyleFailure
	case frontend.ToolSuspended:
		title = "• Exploration suspended"
		role = cellStyleMuted
	case frontend.ToolUnknown:
		title = "? Exploration outcome unknown"
		role = cellStyleMuted
	}
	if tool.Status == frontend.ToolSucceeded {
		// Codex keeps completed exploration headers neutral. The action verbs
		// below already provide the blue scanning anchors.
		role = cellStyleMuted
	}
	lines := []cellLine{statusTitleCellLine(title+commandDurationSuffix(tool.Duration), role)}
	lines = append(lines, explorationDetailCellLine("  └ ", exploration, 1))
	if exploration.Truncated {
		lines = append(lines, styledCellLine("    [… exploration label bounded …]", cellStyleMuted))
	}
	if mode != cellRenderCompact {
		if output := sanitizeTerminalText(tool.Output); strings.TrimSpace(output) != "" {
			lines = append(lines, logicalCellLines("  output:\n"+indentCellEvidence(output), cellStyleDefault)...)
		}
		lines = append(lines, styledCellLine("  "+toolStatusLabel(tool.Status), lifecycleCellRole(cell.item.Lifecycle)))
	}
	return cellDocument{
		Lines: lines, Truncated: tool.OutputTruncated || exploration.Truncated,
		TruncationVisible: exploration.Truncated,
	}
}

func explorationDetail(exploration frontend.ExplorationState) string {
	return explorationDetailCellLine("", exploration, 1).plainText()
}

func explorationDetailCellLine(prefix string, exploration frontend.ExplorationState, count int) cellLine {
	path := boundedSingleLine(exploration.Path, 1024)
	if path == "" {
		path = "."
	}
	workspace := boundedSingleLine(exploration.Workspace, 1024)
	spans := make([]cellSpan, 0, 8)
	appendCellSpan(&spans, prefix, cellStyleMuted)
	appendAction := func(action string) {
		spans = append(spans, cellSpan{Text: action, Role: cellStyleAccent})
	}
	switch exploration.Operation {
	case frontend.ExplorationRead:
		appendAction("Read")
	case frontend.ExplorationList:
		appendAction("List")
	case frontend.ExplorationSearch:
		appendAction("Search")
		pattern := boundedSingleLine(exploration.Pattern, 1024)
		if pattern == "" {
			pattern = "pattern"
		}
		spans = append(
			spans,
			cellSpan{Text: " " + strconv.Quote(pattern), Role: cellStyleSyntaxString},
			cellSpan{Text: " in ", Role: cellStyleMuted},
		)
	default:
		appendAction("Inspect")
	}
	if exploration.Operation != frontend.ExplorationSearch {
		spans = append(spans, cellSpan{Text: " ", Role: cellStyleMuted})
	}
	spans = append(spans, cellSpan{Text: path, Role: cellStyleDefault})
	if workspace != "" {
		spans = append(
			spans,
			cellSpan{Text: " on ", Role: cellStyleMuted},
			cellSpan{Text: workspace, Role: cellStyleDefault},
		)
	}
	if count > 1 {
		spans = append(spans, cellSpan{Text: " ×" + strconv.Itoa(count), Role: cellStyleMuted})
	}
	return cellLine{Spans: spans}
}

func (cell *presentationCell) commandDocument(
	tool frontend.ToolState,
	command frontend.CommandState,
	mode cellRenderMode,
	width int,
) cellDocument {
	titleLines, commandHidden := commandCellTitleLines(tool, command, width)
	lines := append([]cellLine(nil), titleLines...)
	if command.Orphan {
		lines = append(lines, styledCellLine("  outcome arrived without a matching start event", cellStyleFailure))
	}
	if command.CWD != "" {
		lines = append(lines, logicalCellLines("  cwd: "+sanitizeTerminalText(command.CWD), cellStyleMuted)...)
	}
	if command.Background {
		lines = append(lines, styledCellLine("  execution: background", cellStyleMuted))
	}
	if command.SessionID != "" {
		lines = append(
			lines,
			logicalCellLines("  process: "+sanitizeTerminalText(command.SessionID), cellStyleMuted)...,
		)
	}

	evidence := commandTranscriptText(command)
	if mode == cellRenderCompact {
		preview := commandPreviewText(command)
		if preview == "" && command.Status != frontend.CommandRunning {
			preview = "(no output)"
		}
		previewLines, outputHidden := compactCommandEvidenceLines(preview, width)
		lines = append(lines, previewLines...)
		if commandHidden || outputHidden || command.Truncated {
			hint := "  ctrl+t to view full transcript"
			if command.Truncated {
				hint = "  output truncated · ctrl+t to view full transcript"
			}
			lines = append(lines, styledCellLine(hint, cellStyleMuted))
		}
		return cellDocument{
			Lines: lines, Truncated: command.Truncated, TruncationVisible: command.Truncated,
		}
	}
	if command.Command != "" {
		lines = append(lines, shellCommandCellLine("  $ ", command.Command))
	}
	if command.Action != "" && command.Action != "run" {
		lines = append(lines, styledCellLine("  action: "+sanitizeTerminalText(command.Action), cellStyleMuted))
	}
	if evidence != "" {
		lines = append(lines, logicalCellLines(indentCellEvidence(evidence), cellStyleDefault)...)
	}
	if command.Truncated {
		lines = append(lines, styledCellLine("  [… transcript bounded …]", cellStyleMuted))
	}
	lines = append(lines, commandOutcomeCellLines(command, cell.item.Duration)...)
	return cellDocument{
		Lines: lines, Truncated: command.Truncated, TruncationVisible: command.Truncated,
	}
}

func commandCellTitleLine(tool frontend.ToolState, command frontend.CommandState) cellLine {
	lines, _ := commandCellTitleLines(tool, command, 4096)
	if len(lines) == 0 {
		return cellLine{}
	}
	return lines[0]
}

func commandCellTitleLines(
	tool frontend.ToolState,
	command frontend.CommandState,
	width int,
) ([]cellLine, bool) {
	parts := commandCellTitlePartsFor(tool, command)
	prefixSpans := make([]cellSpan, 0, 2)
	prefixRunes := []rune(parts.prefix)
	if len(prefixRunes) != 0 {
		prefixSpans = append(prefixSpans, cellSpan{Text: string(prefixRunes[0]), Role: parts.role})
		appendCellSpan(&prefixSpans, string(prefixRunes[1:]), cellStyleMarkdownStrong)
	}
	headerPrefix := cellLine{Spans: prefixSpans}
	continuationPrefix := styledCellLine("  │ ", cellStyleMuted)
	logical := strings.Split(parts.display, "\n")
	lines := make([]cellLine, 0, len(logical)+1)
	for index, commandLine := range logical {
		body := highlightShellCommand(commandLine)
		if index == len(logical)-1 {
			appendCellSpan(&body, parts.suffix, cellStyleMuted)
		}
		initialPrefix := continuationPrefix
		if index == 0 {
			initialPrefix = headerPrefix
		}
		lines = append(lines, wrapCellSpansWithPrefixes(
			body,
			initialPrefix,
			continuationPrefix,
			width,
		)...)
	}
	if len(lines) <= 3 {
		return lines, false
	}
	omitted := len(lines) - 3
	lines = append(lines[:3], styledCellLine(
		"  │ … +"+strconv.Itoa(omitted)+" "+pluralize("line", omitted),
		cellStyleMuted,
	))
	return lines, true
}

func commandCellDisplay(tool frontend.ToolState, command frontend.CommandState) string {
	display := boundedCommandDisplay(command.Command, 4096)
	if display == "" {
		display = boundedSingleLine(tool.Name, 256)
	}
	if display == "" {
		display = "command"
	}
	return display
}

func boundedCommandDisplay(value string, maximumBytes int) string {
	value = strings.TrimSpace(sanitizeTerminalText(value))
	if maximumBytes <= 0 || len(value) <= maximumBytes {
		return value
	}
	value = value[:maximumBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "…"
}

type commandCellTitleParts struct {
	prefix  string
	display string
	suffix  string
	role    cellStyleRole
}

func commandCellTitlePartsFor(tool frontend.ToolState, command frontend.CommandState) commandCellTitleParts {
	display := commandCellDisplay(tool, command)
	status := command.Status
	if status == "" {
		status = frontend.CommandUnknown
	}
	action := strings.TrimSpace(command.Action)
	if action == "" {
		action = "run"
	}
	if action != "run" {
		verb := map[string]string{
			"poll": "Checked", "read": "Read output from", "write": "Interacted with",
			"send-keys": "Interacted with", "kill": "Stopped",
		}[action]
		if verb == "" {
			verb = "Observed"
		}
		return commandCellTitleParts{
			prefix:  "• " + verb + " ",
			display: display,
			suffix:  " [" + commandStatusLabel(status) + "]",
			role:    lifecycleCellRole(cellLifecycleForCommand(command)),
		}
	}
	switch status {
	case frontend.CommandRunning:
		if command.Background {
			return commandCellTitleParts{
				prefix: "• Running in background ", display: display, role: cellStyleAccent,
			}
		}
		return commandCellTitleParts{prefix: "• Running ", display: display, role: cellStyleAccent}
	case frontend.CommandSucceeded:
		if command.Source == frontend.CommandSourceUserShell {
			return commandCellTitleParts{
				prefix: "• You ran ", display: display, suffix: commandDurationSuffix(tool.Duration),
				role: cellStyleSuccess,
			}
		}
		return commandCellTitleParts{
			prefix: "• Ran ", display: display, suffix: commandDurationSuffix(tool.Duration), role: cellStyleSuccess,
		}
	case frontend.CommandFailed, frontend.CommandTimedOut:
		return commandCellTitleParts{
			prefix: "! Command failed ", display: display, suffix: commandDurationSuffix(tool.Duration),
			role: cellStyleFailure,
		}
	case frontend.CommandCanceled:
		return commandCellTitleParts{
			prefix: "! Command interrupted ", display: display, suffix: commandDurationSuffix(tool.Duration),
			role: cellStyleFailure,
		}
	default:
		return commandCellTitleParts{
			prefix: "? Command outcome unknown ", display: display, role: cellStyleMuted,
		}
	}
}

func commandDurationSuffix(duration time.Duration) string {
	if duration <= 0 {
		return ""
	}
	return " · " + formatToolDuration(duration)
}

func commandStatusLabel(status frontend.CommandStatus) string {
	if status == "" {
		return string(frontend.CommandUnknown)
	}
	return sanitizeTerminalText(string(status))
}

func cellLifecycleForCommand(command frontend.CommandState) frontend.PresentationLifecycle {
	switch command.Status {
	case frontend.CommandSucceeded:
		return frontend.PresentationCompleted
	case frontend.CommandFailed, frontend.CommandTimedOut:
		return frontend.PresentationFailed
	case frontend.CommandCanceled:
		return frontend.PresentationInterrupted
	case frontend.CommandRunning:
		return frontend.PresentationActive
	default:
		return frontend.PresentationUnknown
	}
}

func commandTranscriptText(command frontend.CommandState) string {
	if len(command.Transcript) == 0 {
		parts := make([]string, 0, 3)
		if command.Stdout != "" {
			parts = append(parts, "stdout> "+command.Stdout)
		}
		if command.Stderr != "" {
			parts = append(parts, "stderr> "+command.Stderr)
		}
		if command.Stdout == "" && command.Stderr == "" && command.Output != "" {
			parts = append(parts, "output> "+command.Output)
		}
		return sanitizeTerminalText(strings.Join(parts, "\n"))
	}
	var transcript strings.Builder
	stream := ""
	endsWithNewline := true
	for _, entry := range command.Transcript {
		value := sanitizeTerminalText(entry.Text)
		if value == "" {
			continue
		}
		label := commandStreamLabel(entry.Stream)
		if label != stream {
			if transcript.Len() != 0 && !endsWithNewline {
				transcript.WriteByte('\n')
			}
			transcript.WriteString(label)
			transcript.WriteString("> ")
			stream = label
		}
		transcript.WriteString(value)
		endsWithNewline = strings.HasSuffix(value, "\n")
	}
	return transcript.String()
}

// commandPreviewText mirrors Codex's compact command cells: stdout and stderr
// are presented as one ordered output block instead of exposing transport
// labels such as "stdout>" in the main conversation.
func commandPreviewText(command frontend.CommandState) string {
	if len(command.Transcript) == 0 {
		var output strings.Builder
		appendCommandPreviewText(&output, command.Stdout)
		appendCommandPreviewText(&output, command.Stderr)
		if output.Len() == 0 {
			appendCommandPreviewText(&output, command.Output)
		}
		return strings.TrimRight(sanitizeTerminalText(output.String()), "\n")
	}
	var output strings.Builder
	for _, entry := range command.Transcript {
		output.WriteString(entry.Text)
	}
	return strings.TrimRight(sanitizeTerminalText(output.String()), "\n")
}

func appendCommandPreviewText(output *strings.Builder, value string) {
	if value == "" {
		return
	}
	if output.Len() != 0 {
		current := output.String()
		if !strings.HasSuffix(current, "\n") && !strings.HasPrefix(value, "\n") {
			output.WriteByte('\n')
		}
	}
	output.WriteString(value)
}

func commandStreamLabel(stream string) string {
	switch strings.ToLower(strings.TrimSpace(stream)) {
	case "stdout":
		return "stdout"
	case "stderr":
		return "stderr"
	case "input", "stdin":
		return "stdin"
	case "error":
		return "error"
	case "system":
		return "system"
	default:
		return "terminal"
	}
}

func compactCommandEvidenceLines(evidence string, width int) ([]cellLine, bool) {
	if evidence == "" {
		return nil, false
	}
	logical := strings.Split(evidence, "\n")
	lines := make([]cellLine, 0, len(logical)+1)
	continuationPrefix := styledCellLine("    ", cellStyleMuted)
	for index, line := range logical {
		initialPrefix := continuationPrefix
		if index == 0 {
			initialPrefix = styledCellLine("  └ ", cellStyleMuted)
		}
		lines = append(lines, wrapCellSpansWithPrefixes(
			[]cellSpan{{Text: line, Role: cellStyleMuted}},
			initialPrefix,
			continuationPrefix,
			width,
		)...)
	}
	const maximum = 5
	if len(lines) <= maximum {
		return lines, false
	}
	omitted := len(lines) - 4
	return []cellLine{
		lines[0], lines[1],
		styledCellLine("    "+fmt.Sprintf("… %d lines omitted …", omitted), cellStyleMuted),
		lines[len(lines)-2], lines[len(lines)-1],
	}, true
}

func commandOutcomeCellLines(command frontend.CommandState, duration time.Duration) []cellLine {
	parts := []string{commandStatusLabel(command.Status)}
	if command.ExitCode != nil {
		parts = append(parts, "exit "+strconv.Itoa(*command.ExitCode))
	}
	if duration > 0 {
		parts = append(parts, formatToolDuration(duration))
	}
	return []cellLine{styledCellLine("  "+strings.Join(parts, " · "), lifecycleCellRoleForCommand(command))}
}

func lifecycleCellRoleForCommand(command frontend.CommandState) cellStyleRole {
	if command.Canceled || command.TimedOut || command.Status == frontend.CommandFailed ||
		command.Status == frontend.CommandCanceled || command.Status == frontend.CommandTimedOut {
		return cellStyleFailure
	}
	return cellStyleSuccess
}

func lifecycleCellRole(lifecycle frontend.PresentationLifecycle) cellStyleRole {
	switch lifecycle {
	case frontend.PresentationCompleted:
		return cellStyleSuccess
	case frontend.PresentationFailed, frontend.PresentationInterrupted:
		return cellStyleFailure
	case frontend.PresentationActive:
		return cellStyleAccent
	default:
		return cellStyleMuted
	}
}

func writeAuditCellRole(action string, success bool) cellStyleRole {
	if !success {
		return cellStyleFailure
	}
	switch strings.ToLower(action) {
	case "create", "add", "added", "insert":
		return cellStyleInsertion
	case "delete", "deleted", "remove", "removed":
		return cellStyleDeletion
	default:
		return cellStyleAccent
	}
}

func toolStatusLabel(status frontend.ToolStatus) string {
	if status == "" {
		return string(frontend.ToolUnknown)
	}
	return sanitizeTerminalText(string(status))
}

func toolCommandTruncated(command *frontend.CommandState) bool {
	return command != nil && command.Truncated
}

func pluralize(word string, count int) string {
	if count == 1 {
		return word
	}
	return word + "s"
}

func indentCellEvidence(value string) string {
	return "    " + strings.ReplaceAll(value, "\n", "\n    ")
}

func logicalCellLines(value string, role cellStyleRole) []cellLine {
	parts := strings.Split(sanitizeTerminalText(value), "\n")
	lines := make([]cellLine, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, styledCellLine(part, role))
	}
	return lines
}

func wrapCellDocument(document cellDocument, width int) cellDocument {
	width = max(1, width)
	logicalLines := document.Lines
	if document.Truncated && !document.TruncationVisible {
		logicalLines = append(slices.Clone(logicalLines), styledCellLine("[…truncated]", cellStyleMuted))
	}
	wrapped := make([]cellLine, 0, len(logicalLines))
	for _, line := range logicalLines {
		line = expandCellLineTabs(line, 4)
		role := firstCellLineRole(line)
		value := line.plainText()
		indent := value[:len(value)-len(strings.TrimLeft(value, " "))]
		body := trimCellSpanPrefix(line.Spans, len(indent))
		if len(body) == 0 {
			wrapped = append(wrapped, styledCellLine(ansi.Truncate(indent, width, ""), role))
			continue
		}
		if ansi.StringWidth(indent) >= width {
			indent = strings.Repeat(" ", max(0, width-1))
		}
		bodyWidth := max(1, width-ansi.StringWidth(indent))
		body = replaceOverwideCellSpanGraphemes(body, bodyWidth)
		parts := decodeWrappedCellSpans(ansi.Wrap(encodeCellSpans(body), bodyWidth, ""), line.RowStyle)
		for _, part := range parts {
			if indent != "" {
				part.Spans = append([]cellSpan{{Text: indent, Role: role}}, part.Spans...)
			}
			part.structuralBlank = line.structuralBlank
			wrapped = append(wrapped, part)
		}
	}
	document.Lines = wrapped
	return document
}

func wrapCellSpansWithPrefixes(
	body []cellSpan,
	initialPrefix cellLine,
	continuationPrefix cellLine,
	width int,
) []cellLine {
	width = max(1, width)
	body = expandCellLineTabs(cellLine{Spans: body}, 4).Spans
	prefixWidth := ansi.StringWidth(initialPrefix.plainText())
	if prefixWidth >= width {
		return []cellLine{styledCellLine(
			ansi.Truncate(initialPrefix.plainText(), width, ""),
			firstCellLineRole(initialPrefix),
		)}
	}
	bodyWidth := max(1, width-prefixWidth)
	body = replaceOverwideCellSpanGraphemes(body, bodyWidth)
	parts := decodeWrappedCellSpans(ansi.Wrap(encodeCellSpans(body), bodyWidth, ""), cellRowDefault)
	if len(parts) == 0 {
		parts = []cellLine{{}}
	}
	for index := range parts {
		prefix := continuationPrefix
		if index == 0 {
			prefix = initialPrefix
		}
		parts[index].Spans = append(slices.Clone(prefix.Spans), parts[index].Spans...)
	}
	return parts
}

func firstCellLineRole(line cellLine) cellStyleRole {
	if len(line.Spans) == 0 {
		return cellStyleDefault
	}
	return line.Spans[0].Role
}

func expandCellLineTabs(line cellLine, tabWidth int) cellLine {
	tabWidth = max(1, tabWidth)
	expanded := cellLine{RowStyle: line.RowStyle, structuralBlank: line.structuralBlank}
	column := 0
	for _, span := range line.Spans {
		value := span.Text
		for value != "" {
			cluster, width := ansi.FirstGraphemeCluster(value, ansi.GraphemeWidth)
			if cluster == "" {
				break
			}
			value = value[len(cluster):]
			if cluster == "\t" {
				spaces := tabWidth - column%tabWidth
				appendCellSpan(&expanded.Spans, strings.Repeat(" ", spaces), span.Role)
				column += spaces
				continue
			}
			appendCellSpan(&expanded.Spans, cluster, span.Role)
			column += max(0, width)
		}
	}
	return expanded
}

func trimCellSpanPrefix(spans []cellSpan, count int) []cellSpan {
	trimmed := make([]cellSpan, 0, len(spans))
	for _, span := range spans {
		if count >= len(span.Text) {
			count -= len(span.Text)
			continue
		}
		appendCellSpan(&trimmed, span.Text[count:], span.Role)
		count = 0
	}
	return trimmed
}

func encodeCellSpans(spans []cellSpan) string {
	var encoded strings.Builder
	for _, span := range spans {
		fmt.Fprintf(&encoded, "\x1b[38;5;%dm", 16+int(span.Role))
		encoded.WriteString(span.Text)
	}
	encoded.WriteString("\x1b[0m")
	return encoded.String()
}

func decodeWrappedCellSpans(value string, rowStyle cellRowStyle) []cellLine {
	lines := []cellLine{{RowStyle: rowStyle}}
	role := cellStyleDefault
	for offset := 0; offset < len(value); {
		if value[offset] == '\n' {
			lines = append(lines, cellLine{RowStyle: rowStyle})
			offset++
			continue
		}
		if strings.HasPrefix(value[offset:], "\x1b[0m") {
			role = cellStyleDefault
			offset += len("\x1b[0m")
			continue
		}
		if strings.HasPrefix(value[offset:], "\x1b[38;5;") {
			end := strings.IndexByte(value[offset:], 'm')
			if end >= 0 {
				code, err := strconv.Atoi(value[offset+len("\x1b[38;5;") : offset+end])
				if err == nil && code >= 16 && code-16 <= int(cellStyleMarkdownTableRule) {
					role = cellStyleRole(code - 16)
					offset += end + 1
					continue
				}
			}
		}
		end := offset + 1
		for end < len(value) && value[end] != '\n' && value[end] != '\x1b' {
			end++
		}
		appendCellSpan(&lines[len(lines)-1].Spans, value[offset:end], role)
		offset = end
	}
	return lines
}

func replaceOverwideCellSpanGraphemes(spans []cellSpan, widthLimit int) []cellSpan {
	widthLimit = max(1, widthLimit)
	bounded := make([]cellSpan, 0, len(spans))
	for _, span := range spans {
		value := span.Text
		for value != "" {
			cluster, width := ansi.FirstGraphemeCluster(value, ansi.GraphemeWidth)
			if cluster == "" {
				break
			}
			value = value[len(cluster):]
			if width > widthLimit {
				cluster = "�"
			}
			appendCellSpan(&bounded, cluster, span.Role)
		}
	}
	return bounded
}

func expandCellTabs(value string, tabWidth int) string {
	tabWidth = max(1, tabWidth)
	var expanded strings.Builder
	column := 0
	for value != "" {
		cluster, width := ansi.FirstGraphemeCluster(value, ansi.GraphemeWidth)
		if cluster == "" {
			break
		}
		value = value[len(cluster):]
		if cluster == "\t" {
			spaces := tabWidth - column%tabWidth
			expanded.WriteString(strings.Repeat(" ", spaces))
			column += spaces
			continue
		}
		expanded.WriteString(cluster)
		column += max(0, width)
	}
	return expanded.String()
}

func replaceOverwideCellGraphemes(value string, widthLimit int) string {
	widthLimit = max(1, widthLimit)
	var bounded strings.Builder
	for value != "" {
		cluster, width := ansi.FirstGraphemeCluster(value, ansi.GraphemeWidth)
		if cluster == "" {
			break
		}
		value = value[len(cluster):]
		if width > widthLimit {
			bounded.WriteRune('�')
			continue
		}
		bounded.WriteString(cluster)
	}
	return bounded.String()
}

type cellLayoutBlock struct {
	ID    string
	Start int
	End   int
}

type cellLayout struct {
	Blocks []cellLayoutBlock
}

func (layout cellLayout) anchorAt(line int) transcriptAnchor {
	for _, block := range layout.Blocks {
		if line >= block.Start && line < block.End {
			return transcriptAnchor{id: block.ID, offset: line - block.Start, valid: true}
		}
		if line < block.Start {
			return transcriptAnchor{id: block.ID, before: true, valid: true}
		}
	}
	if len(layout.Blocks) > 0 {
		block := layout.Blocks[len(layout.Blocks)-1]
		return transcriptAnchor{
			id: block.ID, offset: max(0, block.End-block.Start-1), valid: true,
		}
	}
	return transcriptAnchor{}
}

func (layout cellLayout) lineFor(anchor transcriptAnchor) (int, bool) {
	if !anchor.valid {
		return 0, false
	}
	for _, block := range layout.Blocks {
		if block.ID == anchor.id {
			if anchor.before {
				return max(0, block.Start-1), true
			}
			return block.Start + min(anchor.offset, max(0, block.End-block.Start-1)), true
		}
	}
	return 0, false
}

func renderSemanticCells(cells []semanticCell, context cellRenderContext, mode cellRenderMode) (string, cellLayout) {
	context.Width = max(1, context.Width)
	var content strings.Builder
	layout := cellLayout{Blocks: make([]cellLayoutBlock, 0, len(cells))}
	lineCount := 0
	for _, cell := range cells {
		if cell == nil {
			continue
		}
		document := cell.Render(context, mode)
		value := document.plainText()
		if value == "" {
			layout.Blocks = append(
				layout.Blocks,
				cellLayoutBlock{ID: cell.Identity().ID, Start: lineCount, End: lineCount},
			)
			continue
		}
		if content.Len() != 0 {
			content.WriteString("\n\n")
			lineCount++
		}
		start := lineCount
		content.WriteString(value)
		lineCount += strings.Count(value, "\n") + 1
		layout.Blocks = append(layout.Blocks, cellLayoutBlock{ID: cell.Identity().ID, Start: start, End: lineCount})
	}
	return content.String(), layout
}

func validateCellRenderContext(context cellRenderContext) error {
	if context.Width <= 0 {
		return fmt.Errorf("cell render width must be positive")
	}
	if context.Theme != cellThemeDark && context.Theme != cellThemeLight {
		return fmt.Errorf("unsupported cell theme %d", context.Theme)
	}
	switch context.ColorLevel {
	case cellColorNone, cellColorANSI16, cellColorANSI256, cellColorTrueColor:
		return nil
	default:
		return fmt.Errorf("unsupported cell color level %d", context.ColorLevel)
	}
}
