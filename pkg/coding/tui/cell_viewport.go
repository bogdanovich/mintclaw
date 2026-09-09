package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

// semanticViewport keeps Bubble Tea's admitted scrolling/key behavior while
// retaining the transcript as independently cached cell blocks. The embedded
// viewport receives only blank geometry when the document height changes; it
// never receives or reparses the complete transcript on a streaming delta.
type semanticViewport struct {
	viewport.Model
	document         semanticViewportDocument
	placeholderLines int
	geometryResets   uint64
}

func newSemanticViewport(width, height int) semanticViewport {
	return semanticViewport{
		Model:            viewport.New(width, height),
		placeholderLines: -1,
	}
}

func (view *semanticViewport) setDocument(document semanticViewportDocument) {
	view.document = document
	if document.lineCount == view.placeholderLines {
		return
	}
	placeholder := ""
	if document.lineCount > 1 {
		placeholder = strings.Repeat("\n", document.lineCount-1)
	}
	view.SetContent(placeholder)
	view.placeholderLines = document.lineCount
	view.geometryResets++
}

func (view *semanticViewport) Update(message tea.Msg) (semanticViewport, tea.Cmd) {
	model, command := view.Model.Update(message)
	view.Model = model
	return *view, command
}

func (view *semanticViewport) View() string {
	width, height := view.Width, view.Height
	if styledWidth := view.Style.GetWidth(); styledWidth != 0 {
		width = min(width, styledWidth)
	}
	if styledHeight := view.Style.GetHeight(); styledHeight != 0 {
		height = min(height, styledHeight)
	}
	contentWidth := max(0, width-view.Style.GetHorizontalFrameSize())
	contentHeight := max(0, height-view.Style.GetVerticalFrameSize())
	lines := view.document.visibleLines(view.YOffset, view.YOffset+contentHeight)
	contents := lipgloss.NewStyle().
		Width(contentWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		MaxWidth(contentWidth).
		Render(strings.Join(lines, "\n"))
	return view.Style.UnsetWidth().UnsetHeight().Render(contents)
}

type semanticCellRenderSpec struct {
	cell semanticCell
	mode cellRenderMode
}

type semanticCellBlock struct {
	cell    semanticCell
	context cellRenderContext
	mode    cellRenderMode
	lines   []string
	start   int
	end     int
}

type semanticViewportDocument struct {
	blocks         []semanticCellBlock
	layout         cellLayout
	lineCount      int
	renderedBlocks int
	reusedBlocks   int
}

func reconcileSemanticViewportDocument(
	previous semanticViewportDocument,
	specs []semanticCellRenderSpec,
	context cellRenderContext,
) semanticViewportDocument {
	previousByID := make(map[string]semanticCellBlock, len(previous.blocks))
	for _, block := range previous.blocks {
		previousByID[block.cell.Identity().ID] = block
	}
	next := semanticViewportDocument{
		blocks: make([]semanticCellBlock, 0, len(specs)),
		layout: cellLayout{Blocks: make([]cellLayoutBlock, 0, len(specs))},
	}
	seen := make(map[string]struct{}, len(specs))
	visibleBlocks := 0
	for _, spec := range specs {
		if spec.cell == nil {
			continue
		}
		identity := spec.cell.Identity()
		if identity.ID == "" {
			continue
		}
		if _, duplicate := seen[identity.ID]; duplicate {
			continue
		}
		seen[identity.ID] = struct{}{}

		block := semanticCellBlock{cell: spec.cell, context: context, mode: spec.mode}
		if current, ok := previousByID[identity.ID]; ok &&
			sameSemanticCellInstance(current.cell, spec.cell) && current.context == context &&
			current.mode == spec.mode {
			block.lines = current.lines
			next.reusedBlocks++
		} else {
			block.lines = renderSemanticCellLines(spec, context)
			next.renderedBlocks++
		}
		if len(block.lines) > 0 && visibleBlocks > 0 {
			next.lineCount++
		}
		block.start = next.lineCount
		next.lineCount += len(block.lines)
		block.end = next.lineCount
		if len(block.lines) > 0 {
			visibleBlocks++
		}
		next.blocks = append(next.blocks, block)
		next.layout.Blocks = append(next.layout.Blocks, cellLayoutBlock{
			ID: identity.ID, Start: block.start, End: block.end,
		})
	}
	return next
}

func sameSemanticCellInstance(left, right semanticCell) bool {
	switch left := left.(type) {
	case *presentationCell:
		right, ok := right.(*presentationCell)
		return ok && left == right
	case *staticSemanticCell:
		right, ok := right.(*staticSemanticCell)
		return ok && left == right
	case *activityGroupCell:
		right, ok := right.(*activityGroupCell)
		return ok && left.Identity() == right.Identity()
	default:
		return false
	}
}

func renderSemanticCellLines(spec semanticCellRenderSpec, context cellRenderContext) []string {
	document := spec.cell.Render(context, spec.mode)
	text := renderCellDocument(document, context, spec.mode)
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func renderCellDocument(document cellDocument, context cellRenderContext, mode cellRenderMode) string {
	lines := make([]string, 0, len(document.Lines))
	for _, line := range document.Lines {
		var rendered strings.Builder
		for _, span := range line.Spans {
			prefix := cellSpanANSI(span.Role, line.RowStyle, context, mode)
			if prefix == "" {
				rendered.WriteString(span.Text)
				continue
			}
			rendered.WriteString(prefix)
			rendered.WriteString(span.Text)
			rendered.WriteString("\x1b[0m")
		}
		if cellRowHasBackground(line.RowStyle, context, mode) {
			padding := max(0, context.Width-ansi.StringWidth(line.plainText()))
			if padding > 0 {
				rendered.WriteString(cellSpanANSI(cellStyleDefault, line.RowStyle, context, mode))
				rendered.WriteString(strings.Repeat(" ", padding))
				rendered.WriteString("\x1b[0m")
			}
		}
		lines = append(lines, rendered.String())
	}
	return strings.Join(lines, "\n")
}

func cellSpanANSI(
	role cellStyleRole,
	rowStyle cellRowStyle,
	context cellRenderContext,
	mode cellRenderMode,
) string {
	if mode == cellRenderPlain || context.ColorLevel == cellColorNone {
		return ""
	}
	codes := cellRowBackgroundCodes(rowStyle, context)
	switch role {
	case cellStylePlanTitle:
		codes = append(codes, "1")
	case cellStylePlanExplanation:
		codes = append(codes, "2", "3")
	case cellStylePlanCompleted:
		codes = append(codes, "2", "9")
	case cellStylePlanCurrent:
		switch context.ColorLevel {
		case cellColorANSI16:
			codes = append(codes, "1", "36")
		case cellColorANSI256:
			codes = append(codes, "1", "38", "5", "75")
		case cellColorTrueColor:
			if context.Theme == cellThemeLight {
				codes = append(codes, "1", "38", "2", "0", "112", "160")
				break
			}
			codes = append(codes, "1", "38", "2", "92", "200", "255")
		}
	case cellStylePlanPending:
		codes = append(codes, "2")
	case cellStyleDiffGutter:
		codes = append(codes, "2")
	case cellStyleDiffGutterInsertion, cellStyleDiffGutterDeletion:
		codes = append(codes, cellDiffGutterCodes(role, context)...)
	case cellStyleInsertion:
		codes = append(codes, "32")
	case cellStyleDeletion:
		codes = append(codes, "31")
	case cellStyleSyntaxKeyword:
		codes = append(codes, cellSyntaxForegroundCodes(role, context)...)
	case cellStyleSyntaxString:
		codes = append(codes, cellSyntaxForegroundCodes(role, context)...)
	case cellStyleSyntaxNumber:
		codes = append(codes, cellSyntaxForegroundCodes(role, context)...)
	case cellStyleSyntaxComment:
		codes = append(codes, cellSyntaxForegroundCodes(role, context)...)
	case cellStyleSyntaxType:
		codes = append(codes, cellSyntaxForegroundCodes(role, context)...)
	case cellStyleDefault:
		if context.Theme == cellThemeDark {
			switch rowStyle {
			case cellRowInsertion:
				codes = append(codes, "32")
			case cellRowDeletion:
				codes = append(codes, "31")
			}
		} else if context.ColorLevel == cellColorANSI16 {
			switch rowStyle {
			case cellRowInsertion:
				codes = append(codes, "32")
			case cellRowDeletion:
				codes = append(codes, "31")
			}
		}
	}
	if rowStyle == cellRowDeletion && isCellSyntaxRole(role) &&
		(role != cellStyleSyntaxComment || context.ColorLevel != cellColorANSI16) {
		codes = append(codes, "2")
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

func cellRowHasBackground(rowStyle cellRowStyle, context cellRenderContext, mode cellRenderMode) bool {
	return mode != cellRenderPlain && context.ColorLevel >= cellColorANSI256 &&
		(rowStyle == cellRowInsertion || rowStyle == cellRowDeletion)
}

func cellRowBackgroundCodes(rowStyle cellRowStyle, context cellRenderContext) []string {
	if context.ColorLevel < cellColorANSI256 {
		return nil
	}
	switch context.ColorLevel {
	case cellColorANSI256:
		switch rowStyle {
		case cellRowInsertion:
			if context.Theme == cellThemeLight {
				return []string{"48", "5", "194"}
			}
			return []string{"48", "5", "22"}
		case cellRowDeletion:
			if context.Theme == cellThemeLight {
				return []string{"48", "5", "224"}
			}
			return []string{"48", "5", "52"}
		}
	case cellColorTrueColor:
		switch rowStyle {
		case cellRowInsertion:
			if context.Theme == cellThemeLight {
				return []string{"48", "2", "218", "251", "225"}
			}
			return []string{"48", "2", "33", "58", "43"}
		case cellRowDeletion:
			if context.Theme == cellThemeLight {
				return []string{"48", "2", "255", "235", "233"}
			}
			return []string{"48", "2", "74", "34", "29"}
		}
	}
	return nil
}

func cellDiffGutterCodes(role cellStyleRole, context cellRenderContext) []string {
	if context.Theme == cellThemeLight && context.ColorLevel == cellColorANSI16 {
		return []string{"30"}
	}
	if context.Theme != cellThemeLight || context.ColorLevel < cellColorANSI256 {
		return []string{"2"}
	}
	if context.ColorLevel == cellColorANSI256 {
		background := "157"
		if role == cellStyleDiffGutterDeletion {
			background = "217"
		}
		return []string{"38", "5", "236", "48", "5", background}
	}
	red, green, blue := "172", "238", "187"
	if role == cellStyleDiffGutterDeletion {
		red, green, blue = "255", "206", "203"
	}
	return []string{"38", "2", "31", "35", "40", "48", "2", red, green, blue}
}

func cellSyntaxForegroundCodes(role cellStyleRole, context cellRenderContext) []string {
	if context.ColorLevel == cellColorANSI16 {
		switch role {
		case cellStyleSyntaxKeyword:
			return []string{"35"}
		case cellStyleSyntaxString:
			return []string{"36"}
		case cellStyleSyntaxNumber:
			return []string{"34"}
		case cellStyleSyntaxComment:
			return []string{"2"}
		case cellStyleSyntaxType:
			return []string{"33"}
		}
	}
	if context.ColorLevel == cellColorANSI256 {
		if context.Theme == cellThemeLight {
			switch role {
			case cellStyleSyntaxKeyword:
				return []string{"38", "5", "160"}
			case cellStyleSyntaxString:
				return []string{"38", "5", "24"}
			case cellStyleSyntaxNumber:
				return []string{"38", "5", "25"}
			case cellStyleSyntaxComment:
				return []string{"38", "5", "244"}
			case cellStyleSyntaxType:
				return []string{"38", "5", "130"}
			}
		}
		switch role {
		case cellStyleSyntaxKeyword:
			return []string{"38", "5", "203"}
		case cellStyleSyntaxString:
			return []string{"38", "5", "117"}
		case cellStyleSyntaxNumber:
			return []string{"38", "5", "75"}
		case cellStyleSyntaxComment:
			return []string{"38", "5", "245"}
		case cellStyleSyntaxType:
			return []string{"38", "5", "222"}
		}
	}
	if context.Theme == cellThemeLight {
		switch role {
		case cellStyleSyntaxKeyword:
			return []string{"38", "2", "207", "34", "46"}
		case cellStyleSyntaxString:
			return []string{"38", "2", "10", "48", "105"}
		case cellStyleSyntaxNumber:
			return []string{"38", "2", "5", "80", "174"}
		case cellStyleSyntaxComment:
			return []string{"38", "2", "110", "119", "129"}
		case cellStyleSyntaxType:
			return []string{"38", "2", "149", "56", "0"}
		}
	}
	switch role {
	case cellStyleSyntaxKeyword:
		return []string{"38", "2", "255", "123", "114"}
	case cellStyleSyntaxString:
		return []string{"38", "2", "165", "214", "255"}
	case cellStyleSyntaxNumber:
		return []string{"38", "2", "121", "192", "255"}
	case cellStyleSyntaxComment:
		return []string{"38", "2", "139", "148", "158"}
	case cellStyleSyntaxType:
		return []string{"38", "2", "255", "166", "87"}
	}
	return nil
}

func isCellSyntaxRole(role cellStyleRole) bool {
	switch role {
	case cellStyleSyntaxKeyword, cellStyleSyntaxString, cellStyleSyntaxNumber,
		cellStyleSyntaxComment, cellStyleSyntaxType:
		return true
	default:
		return false
	}
}

func (document semanticViewportDocument) visibleLines(start, end int) []string {
	start = max(0, min(start, document.lineCount))
	end = max(start, min(end, document.lineCount))
	if start == end {
		return nil
	}
	lines := make([]string, end-start)
	for _, block := range document.blocks {
		if block.end <= start || block.start >= end || len(block.lines) == 0 {
			continue
		}
		from := max(start, block.start)
		to := min(end, block.end)
		copy(lines[from-start:to-start], block.lines[from-block.start:to-block.start])
	}
	return lines
}

func (document semanticViewportDocument) text() string {
	return strings.Join(document.visibleLines(0, document.lineCount), "\n")
}

type staticSemanticCell struct {
	identity     cellIdentity
	role         cellStyleRole
	label        string
	text         string
	truncated    bool
	document     cellDocument
	renderCache  map[cellRenderCacheKey]cellDocument
	renderMisses uint64
}

func newStaticSemanticCell(
	id string,
	revision uint64,
	role cellStyleRole,
	label, text string,
	truncated bool,
) *staticSemanticCell {
	lines := logicalCellLines(label, role)
	if text != "" {
		lines = append(lines, logicalCellLines(indentTranscript(sanitizeTerminalText(text), "  "), cellStyleMuted)...)
	}
	return &staticSemanticCell{
		identity: cellIdentity{
			ID:        id,
			Revision:  revision,
			Kind:      frontend.PresentationToolMessage,
			Lifecycle: frontend.PresentationCompleted,
		},
		role: role, label: label, text: text, truncated: truncated,
		document: cellDocument{Lines: lines, Truncated: truncated},
	}
}

func (cell *staticSemanticCell) Identity() cellIdentity {
	return cell.identity
}

func (cell *staticSemanticCell) Render(context cellRenderContext, mode cellRenderMode) cellDocument {
	context.Width = max(1, context.Width)
	key := cellRenderCacheKey{Context: context, Mode: mode}
	if document, ok := cell.renderCache[key]; ok {
		return document
	}
	document := wrapCellDocument(cell.document, context.Width)
	if mode == cellRenderPlain {
		for lineIndex := range document.Lines {
			for spanIndex := range document.Lines[lineIndex].Spans {
				document.Lines[lineIndex].Spans[spanIndex].Role = cellStyleDefault
			}
		}
	}
	cell.renderCache = storeCellRenderDocument(cell.renderCache, key, document)
	cell.renderMisses++
	return document
}

func (cell *staticSemanticCell) matches(
	role cellStyleRole,
	label, text string,
	truncated bool,
) bool {
	return cell.role == role && cell.label == label && cell.text == text && cell.truncated == truncated
}
