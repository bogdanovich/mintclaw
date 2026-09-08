package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
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
	cellStyleSuccess
	cellStyleFailure
	cellStyleInsertion
	cellStyleDeletion
	cellStylePlanTitle
	cellStylePlanExplanation
	cellStylePlanCompleted
	cellStylePlanCurrent
	cellStylePlanPending
)

type cellSpan struct {
	Text string
	Role cellStyleRole
}

type cellLine struct {
	Spans []cellSpan
}

func styledCellLine(value string, role cellStyleRole) cellLine {
	return cellLine{Spans: []cellSpan{{Text: value, Role: role}}}
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

type presentationCell struct {
	item         frontend.PresentationItem
	renderCache  map[cellRenderCacheKey]cellDocument
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
	} else if cell.item.Tool != nil && cell.item.Tool.Command != nil {
		document = wrapCellDocument(
			cell.commandDocument(*cell.item.Tool, *cell.item.Tool.Command, mode, context.Width),
			context.Width,
		)
	} else {
		document = wrapCellDocument(cell.semanticDocument(mode), context.Width)
	}
	if mode == cellRenderPlain {
		for lineIndex := range document.Lines {
			for spanIndex := range document.Lines[lineIndex].Spans {
				document.Lines[lineIndex].Spans[spanIndex].Role = cellStyleDefault
			}
		}
	}
	if cell.renderCache == nil {
		cell.renderCache = make(map[cellRenderCacheKey]cellDocument)
	}
	cell.renderCache[key] = document
	cell.renderMisses++
	return document
}

func (cell *presentationCell) renderMissCount() uint64 {
	return cell.renderMisses
}

func (cell *presentationCell) semanticDocument(mode cellRenderMode) cellDocument {
	switch cell.item.Kind {
	case frontend.PresentationUserMessage,
		frontend.PresentationAssistantMessage,
		frontend.PresentationReasoning,
		frontend.PresentationToolMessage,
		frontend.PresentationWarning,
		frontend.PresentationError:
		return cell.messageDocument(mode)
	case frontend.PresentationToolCall:
		return cell.toolDocument(mode)
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
	role := lifecycleCellRole(cell.item.Lifecycle)
	prefix := "• "
	switch cell.item.Kind {
	case frontend.PresentationUserMessage:
		prefix = "› "
		role = cellStyleAccent
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
	lines := []cellLine{styledCellLine(title, lifecycleCellRole(cell.item.Lifecycle))}
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

func (cell *presentationCell) commandDocument(
	tool frontend.ToolState,
	command frontend.CommandState,
	mode cellRenderMode,
	width int,
) cellDocument {
	title, role := commandCellTitle(tool, command)
	lines := []cellLine{styledCellLine(title, role)}
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
		lines = append(lines, compactCommandEvidenceLines(evidence, width)...)
		if evidence != "" || command.Truncated {
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
		lines = append(lines, logicalCellLines("  $ "+sanitizeTerminalText(command.Command), cellStyleAccent)...)
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

func commandCellTitle(tool frontend.ToolState, command frontend.CommandState) (string, cellStyleRole) {
	display := boundedSingleLine(command.Command, 512)
	if display == "" {
		display = boundedSingleLine(tool.Name, 256)
	}
	if display == "" {
		display = "command"
	}
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
		return fmt.Sprintf(
				"• %s %s [%s]",
				verb,
				display,
				commandStatusLabel(status),
			), lifecycleCellRole(
				cellLifecycleForCommand(command),
			)
	}
	switch status {
	case frontend.CommandRunning:
		if command.Background {
			return "• Running in background " + display, cellStyleAccent
		}
		return "• Running " + display, cellStyleAccent
	case frontend.CommandSucceeded:
		if command.Source == frontend.CommandSourceUserShell {
			return "• You ran " + display + commandDurationSuffix(tool.Duration), cellStyleSuccess
		}
		return "• Ran " + display + commandDurationSuffix(tool.Duration), cellStyleSuccess
	case frontend.CommandFailed, frontend.CommandTimedOut:
		return "! Command failed " + display + commandDurationSuffix(tool.Duration), cellStyleFailure
	case frontend.CommandCanceled:
		return "! Command interrupted " + display + commandDurationSuffix(tool.Duration), cellStyleFailure
	default:
		return "? Command outcome unknown " + display, cellStyleMuted
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

func compactCommandEvidenceLines(evidence string, width int) []cellLine {
	if evidence == "" {
		return nil
	}
	logical := strings.Split(evidence, "\n")
	lines := make([]cellLine, 0, len(logical))
	for index, line := range logical {
		prefix := "    "
		if index == 0 {
			prefix = "  └ "
		}
		lines = append(lines, styledCellLine(prefix+line, cellStyleMuted))
	}
	wrapped := wrapCellDocument(cellDocument{Lines: lines}, max(1, width)).Lines
	const maximum = 5
	if len(wrapped) <= maximum {
		return wrapped
	}
	omitted := len(wrapped) - 4
	return []cellLine{
		wrapped[0], wrapped[1],
		styledCellLine("    "+fmt.Sprintf("… %d lines omitted …", omitted), cellStyleMuted),
		wrapped[len(wrapped)-2], wrapped[len(wrapped)-1],
	}
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
		role := cellStyleDefault
		if len(line.Spans) != 0 {
			role = line.Spans[0].Role
		}
		value := expandCellTabs(line.plainText(), 4)
		indent := value[:len(value)-len(strings.TrimLeft(value, " "))]
		body := strings.TrimPrefix(value, indent)
		if body == "" {
			wrapped = append(wrapped, styledCellLine(ansi.Truncate(indent, width, ""), role))
			continue
		}
		if ansi.StringWidth(indent) >= width {
			indent = strings.Repeat(" ", max(0, width-1))
		}
		bodyWidth := max(1, width-ansi.StringWidth(indent))
		body = replaceOverwideCellGraphemes(body, bodyWidth)
		parts := strings.Split(ansi.Wrap(body, bodyWidth, ""), "\n")
		for _, part := range parts {
			wrapped = append(wrapped, styledCellLine(indent+part, role))
		}
	}
	document.Lines = wrapped
	return document
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
