package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	cell     semanticCell
	mode     cellRenderMode
	selected bool
}

type semanticCellBlock struct {
	cell     semanticCell
	context  cellRenderContext
	mode     cellRenderMode
	selected bool
	lines    []string
	start    int
	end      int
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

		block := semanticCellBlock{
			cell: spec.cell, context: context, mode: spec.mode, selected: spec.selected,
		}
		if current, ok := previousByID[identity.ID]; ok &&
			sameSemanticCellInstance(current.cell, spec.cell) && current.context == context &&
			current.mode == spec.mode && current.selected == spec.selected {
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
	lines := strings.Split(text, "\n")
	if !spec.selected || len(lines) == 0 {
		return lines
	}
	lines = append([]string(nil), lines...)
	if strings.HasPrefix(lines[0], "• ") {
		lines[0] = "▶ " + strings.TrimPrefix(lines[0], "• ")
	} else {
		lines[0] = "▶ " + lines[0]
	}
	return lines
}

func renderCellDocument(document cellDocument, context cellRenderContext, mode cellRenderMode) string {
	lines := make([]string, 0, len(document.Lines))
	for _, line := range document.Lines {
		var rendered strings.Builder
		for _, span := range line.Spans {
			prefix := cellRoleANSI(span.Role, context, mode)
			if prefix == "" {
				rendered.WriteString(span.Text)
				continue
			}
			rendered.WriteString(prefix)
			rendered.WriteString(span.Text)
			rendered.WriteString("\x1b[0m")
		}
		lines = append(lines, rendered.String())
	}
	return strings.Join(lines, "\n")
}

func cellRoleANSI(role cellStyleRole, context cellRenderContext, mode cellRenderMode) string {
	if mode == cellRenderPlain || context.ColorLevel == cellColorNone {
		return ""
	}
	switch role {
	case cellStylePlanTitle:
		return "\x1b[1m"
	case cellStylePlanExplanation:
		return "\x1b[2;3m"
	case cellStylePlanCompleted:
		return "\x1b[2;9m"
	case cellStylePlanCurrent:
		switch context.ColorLevel {
		case cellColorANSI16:
			return "\x1b[1;36m"
		case cellColorANSI256:
			return "\x1b[1;38;5;75m"
		case cellColorTrueColor:
			if context.Theme == cellThemeLight {
				return "\x1b[1;38;2;0;112;160m"
			}
			return "\x1b[1;38;2;92;200;255m"
		}
	case cellStylePlanPending:
		return "\x1b[2m"
	}
	return ""
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
	if cell.renderCache == nil {
		cell.renderCache = make(map[cellRenderCacheKey]cellDocument)
	}
	cell.renderCache[key] = document
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
