package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

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
	Lines     []cellLine
	Truncated bool
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

type presentationCell struct {
	item frontend.PresentationItem
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
	document := cell.semanticDocument(mode)
	document = wrapCellDocument(document, max(1, context.Width))
	if mode == cellRenderPlain {
		for lineIndex := range document.Lines {
			for spanIndex := range document.Lines[lineIndex].Spans {
				document.Lines[lineIndex].Spans[spanIndex].Role = cellStyleDefault
			}
		}
	}
	return document
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
	case frontend.PresentationPlanUpdate:
		return cell.planDocument()
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

func (cell *presentationCell) planDocument() cellDocument {
	plan := cell.item.Plan
	if plan == nil {
		return cellDocument{}
	}
	lines := []cellLine{styledCellLine("• Updated Plan", lifecycleCellRole(cell.item.Lifecycle))}
	if explanation := strings.TrimSpace(sanitizeTerminalText(plan.Explanation)); explanation != "" {
		lines = append(lines, logicalCellLines("  "+explanation, cellStyleMuted)...)
	}
	for _, step := range plan.Steps {
		glyph, role := planStepCellStyle(step.Status)
		lines = append(lines, logicalCellLines("  "+glyph+" "+sanitizeTerminalText(step.Step), role)...)
	}
	return cellDocument{Lines: lines, Truncated: plan.Truncated}
}

func planStepCellStyle(status frontend.PlanStepStatus) (string, cellStyleRole) {
	switch status {
	case frontend.PlanStepCompleted:
		return "✓", cellStyleSuccess
	case frontend.PlanStepInProgress:
		return "→", cellStyleAccent
	default:
		return "□", cellStyleMuted
	}
}

func (cell *presentationCell) toolDocument(mode cellRenderMode) cellDocument {
	tool := cell.item.Tool
	if tool == nil {
		return cellDocument{}
	}
	name := strings.TrimSpace(sanitizeTerminalText(tool.Name))
	if name == "" {
		name = "tool"
	}
	title := "• Tool " + name + " [" + toolStatusLabel(tool.Status) + "]"
	if tool.Command != nil {
		if tool.Command.Background {
			title = "• Background " + name + " [" + toolStatusLabel(tool.Status) + "]"
		} else {
			title = "• Ran " + name + " [" + toolStatusLabel(tool.Status) + "]"
		}
	}
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
	if tool.Command != nil {
		lines = append(lines, commandEvidenceCellLines(*tool.Command)...)
	} else if output := sanitizeTerminalText(tool.Output); strings.TrimSpace(output) != "" {
		lines = append(
			lines,
			logicalCellLines("  output:\n"+indentCellEvidence(output), cellStyleDefault)...)
	}
	return cellDocument{Lines: lines, Truncated: tool.OutputTruncated || toolCommandTruncated(tool.Command)}
}

func commandEvidenceCellLines(command frontend.CommandState) []cellLine {
	lines := make([]cellLine, 0, 6)
	if command.Background {
		lines = append(lines, styledCellLine("  execution: background", cellStyleMuted))
	}
	if command.SessionID != "" {
		lines = append(
			lines,
			logicalCellLines("  session: "+sanitizeTerminalText(command.SessionID), cellStyleMuted)...)
	}
	if command.ExitCode != nil {
		lines = append(
			lines,
			styledCellLine("  exit: "+strconv.Itoa(*command.ExitCode), lifecycleCellRoleForCommand(command)),
		)
	}
	for _, evidence := range []struct {
		label string
		value string
	}{
		{label: "stdout", value: command.Stdout},
		{label: "stderr", value: command.Stderr},
		{label: "output", value: command.Output},
	} {
		value := sanitizeTerminalText(evidence.value)
		if strings.TrimSpace(value) == "" {
			continue
		}
		lines = append(
			lines,
			logicalCellLines("  "+evidence.label+":\n"+indentCellEvidence(value), cellStyleDefault)...)
	}
	return lines
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
	if document.Truncated {
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
