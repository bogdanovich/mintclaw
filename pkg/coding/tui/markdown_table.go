package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/gomarkdown/markdown/ast"
)

const (
	markdownTableColumnGap       = 2
	markdownTableMinimumColumn   = 8
	markdownTableMaximumColumns  = 6
	markdownTableNaturalWidthCap = 40
	// Repeating long headings for every stacked row can amplify a bounded
	// source by orders of magnitude. Beyond this total label-byte cost, render
	// the full headings once and use stable numeric labels for each row.
	markdownTableRepeatedLabelBudget = 16 << 10
)

type markdownTableCell struct {
	lines [][]cellSpan
	align ast.CellAlignFlags
}

func (cell markdownTableCell) plainText() string {
	return markdownSpansPlainText(cell.inlineSpans())
}

func (cell markdownTableCell) inlineSpans() []cellSpan {
	var spans []cellSpan
	for index, line := range cell.lines {
		if index != 0 {
			appendMarkdownSpan(&spans, cellSpan{Text: " "})
		}
		for _, span := range line {
			appendMarkdownSpan(&spans, span)
		}
	}
	return spans
}

func (cell markdownTableCell) naturalWidth() int {
	width := 1
	for _, line := range cell.lines {
		width = max(width, ansi.StringWidth(markdownSpansPlainText(line)))
	}
	return min(width, markdownTableNaturalWidthCap)
}

func (renderer *markdownRenderer) tableLines(table *ast.Table) []cellLine {
	header, rows := markdownTableRows(table, renderer.baseRole)
	columnCount := len(header)
	for _, row := range rows {
		columnCount = max(columnCount, len(row))
	}
	if columnCount == 0 {
		return nil
	}
	header = normalizeMarkdownTableRow(header, columnCount)
	for index := range rows {
		rows[index] = normalizeMarkdownTableRow(rows[index], columnCount)
	}

	natural := make([]int, columnCount)
	for column := range columnCount {
		natural[column] = header[column].naturalWidth()
		for _, row := range rows {
			natural[column] = max(natural[column], row[column].naturalWidth())
		}
	}
	widths, ok := allocateMarkdownTableWidths(natural, renderer.width)
	if !ok {
		return renderer.stackedTableLines(header, rows)
	}

	lines := renderer.gridTableRow(header, widths, true)
	lines = append(lines, markdownTableRule(widths, '━'))
	for index, row := range rows {
		lines = append(lines, renderer.gridTableRow(row, widths, false)...)
		if index+1 < len(rows) {
			lines = append(lines, markdownTableRule(widths, '─'))
		}
	}
	return lines
}

func markdownTableRows(
	table *ast.Table,
	baseRole cellStyleRole,
) ([]markdownTableCell, [][]markdownTableCell) {
	var header []markdownTableCell
	var rows [][]markdownTableCell
	var collectRows func(ast.Node, bool)
	collectRows = func(node ast.Node, inHeader bool) {
		switch typed := node.(type) {
		case *ast.TableHeader:
			for _, child := range typed.GetChildren() {
				collectRows(child, true)
			}
			return
		case *ast.TableBody, *ast.TableFooter:
			for _, child := range node.GetChildren() {
				collectRows(child, false)
			}
			return
		case *ast.TableRow:
			row := markdownTableRow(typed, baseRole, inHeader)
			if inHeader && header == nil {
				header = row
			} else {
				rows = append(rows, row)
			}
			return
		}
		for _, child := range node.GetChildren() {
			collectRows(child, inHeader)
		}
	}
	collectRows(table, false)
	return header, rows
}

func markdownTableRow(row *ast.TableRow, baseRole cellStyleRole, inHeader bool) []markdownTableCell {
	cells := make([]markdownTableCell, 0, len(row.GetChildren()))
	for _, child := range row.GetChildren() {
		cell, ok := child.(*ast.TableCell)
		if !ok {
			continue
		}
		collector := markdownInlineCollector{lines: [][]cellSpan{{}}}
		collector.walkChildren(cell.GetChildren(), markdownInlineStyle{
			base: baseRole, tableHeader: inHeader || cell.IsHeader,
		})
		cells = append(cells, markdownTableCell{lines: collector.lines, align: cell.Align})
	}
	return cells
}

func normalizeMarkdownTableRow(row []markdownTableCell, columns int) []markdownTableCell {
	if len(row) > columns {
		return row[:columns]
	}
	for len(row) < columns {
		row = append(row, markdownTableCell{lines: [][]cellSpan{{}}})
	}
	return row
}

func allocateMarkdownTableWidths(natural []int, width int) ([]int, bool) {
	columns := len(natural)
	if columns == 0 || columns > markdownTableMaximumColumns {
		return nil, false
	}
	contentWidth := width - markdownTableColumnGap*(columns-1)
	if contentWidth < columns*markdownTableMinimumColumn {
		return nil, false
	}
	widths := make([]int, columns)
	remaining := contentWidth
	for index, naturalWidth := range natural {
		widths[index] = min(naturalWidth, markdownTableMinimumColumn)
		remaining -= widths[index]
	}
	for remaining > 0 {
		progress := false
		for index := range widths {
			if widths[index] >= natural[index] || remaining == 0 {
				continue
			}
			widths[index]++
			remaining--
			progress = true
		}
		if !progress {
			break
		}
	}
	return widths, true
}

func (renderer *markdownRenderer) gridTableRow(
	row []markdownTableCell,
	widths []int,
	header bool,
) []cellLine {
	wrapped := make([][]cellLine, len(widths))
	rowHeight := 1
	for column, width := range widths {
		wrapped[column] = wrapMarkdownTableCell(row[column], width)
		rowHeight = max(rowHeight, len(wrapped[column]))
	}
	lines := make([]cellLine, 0, rowHeight)
	for lineIndex := range rowHeight {
		line := cellLine{}
		for column, width := range widths {
			var content cellLine
			if lineIndex < len(wrapped[column]) {
				content = wrapped[column][lineIndex]
			}
			if header {
				for index := range content.Spans {
					if content.Spans[index].Role == renderer.baseRole {
						content.Spans[index].Role = cellStyleMarkdownTableHeader
					}
				}
			}
			contentWidth := ansi.StringWidth(content.plainText())
			leftPadding, rightPadding := markdownTablePadding(row[column].align, width-contentWidth)
			if leftPadding > 0 {
				appendMarkdownSpan(&line.Spans, cellSpan{Text: strings.Repeat(" ", leftPadding)})
			}
			for _, span := range content.Spans {
				appendMarkdownSpan(&line.Spans, span)
			}
			if column+1 < len(widths) {
				appendMarkdownSpan(&line.Spans, cellSpan{
					Text: strings.Repeat(" ", rightPadding+markdownTableColumnGap),
				})
			}
		}
		lines = append(lines, line)
	}
	return lines
}

func wrapMarkdownTableCell(cell markdownTableCell, width int) []cellLine {
	lines := make([]cellLine, 0, len(cell.lines))
	for _, logical := range cell.lines {
		lines = append(lines, wrapMarkdownSpans(logical, width)...)
	}
	if len(lines) == 0 {
		return []cellLine{{}}
	}
	return lines
}

func markdownTablePadding(align ast.CellAlignFlags, remaining int) (int, int) {
	remaining = max(0, remaining)
	switch align {
	case ast.TableAlignmentRight:
		return remaining, 0
	case ast.TableAlignmentCenter:
		return remaining / 2, remaining - remaining/2
	default:
		return 0, remaining
	}
}

func markdownTableRule(widths []int, character rune) cellLine {
	line := cellLine{}
	for index, width := range widths {
		if index != 0 {
			appendMarkdownSpan(&line.Spans, cellSpan{
				Text: strings.Repeat(" ", markdownTableColumnGap),
				Role: cellStyleMarkdownTableRule,
			})
		}
		appendMarkdownSpan(&line.Spans, cellSpan{
			Text: strings.Repeat(string(character), width),
			Role: cellStyleMarkdownTableRule,
		})
	}
	return line
}

func (renderer *markdownRenderer) stackedTableLines(
	header []markdownTableCell,
	rows [][]markdownTableCell,
) []cellLine {
	if len(rows) == 0 {
		var spans []cellSpan
		for index, cell := range header {
			if index != 0 {
				appendMarkdownSpan(&spans, cellSpan{Text: " · ", Role: cellStyleMarkdownTableRule})
			}
			appendMarkdownSpan(&spans, cellSpan{Text: cell.plainText(), Role: cellStyleMarkdownTableHeader})
		}
		return wrapMarkdownSpans(spans, renderer.width)
	}
	if stackedTableRepeatedLabelBytes(header, len(rows)) > markdownTableRepeatedLabelBudget {
		return renderer.indexedStackedTableLines(header, rows)
	}
	lines := make([]cellLine, 0, len(rows)*len(header))
	for rowIndex, row := range rows {
		if rowIndex != 0 {
			lines = append(lines, styledCellLine(
				strings.Repeat("─", renderer.width),
				cellStyleMarkdownTableRule,
			))
		}
		for column, cell := range row {
			label := markdownTableLabel(header, column)
			spans := []cellSpan{{Text: label + ": ", Role: cellStyleMarkdownTableHeader}}
			value := cell.inlineSpans()
			if strings.TrimSpace(markdownSpansPlainText(value)) == "" {
				value = []cellSpan{{Text: "—", Role: renderer.baseRole}}
			}
			for _, span := range value {
				appendMarkdownSpan(&spans, span)
			}
			lines = append(lines, wrapMarkdownSpans(spans, renderer.width)...)
		}
	}
	return lines
}

func stackedTableRepeatedLabelBytes(header []markdownTableCell, rows int) int {
	if rows <= 0 {
		return 0
	}
	total := 0
	for column := range header {
		labelBytes := len(markdownTableLabel(header, column)) + len(": ")
		if labelBytes > markdownTableRepeatedLabelBudget ||
			total > markdownTableRepeatedLabelBudget-labelBytes {
			return markdownTableRepeatedLabelBudget + 1
		}
		total += labelBytes
	}
	if total == 0 || rows <= markdownTableRepeatedLabelBudget/total {
		return total * rows
	}
	return markdownTableRepeatedLabelBudget + 1
}

func markdownTableLabel(header []markdownTableCell, column int) string {
	if column >= 0 && column < len(header) {
		if label := strings.TrimSpace(header[column].plainText()); label != "" {
			return label
		}
	}
	return fmt.Sprintf("Column %d", column+1)
}

func (renderer *markdownRenderer) indexedStackedTableLines(
	header []markdownTableCell,
	rows [][]markdownTableCell,
) []cellLine {
	lines := []cellLine{styledCellLine("Columns", cellStyleMarkdownTableHeader)}
	for column, cell := range header {
		spans := []cellSpan{{Text: strconv.Itoa(column+1) + ": ", Role: cellStyleMarkdownTableHeader}}
		value := cell.inlineSpans()
		if strings.TrimSpace(markdownSpansPlainText(value)) == "" {
			value = []cellSpan{{Text: markdownTableLabel(header, column), Role: cellStyleMarkdownTableHeader}}
		}
		for _, span := range value {
			appendMarkdownSpan(&spans, span)
		}
		lines = append(lines, wrapMarkdownSpans(spans, renderer.width)...)
	}
	lines = append(lines, cellLine{})
	for rowIndex, row := range rows {
		if rowIndex != 0 {
			lines = append(lines, styledCellLine(
				strings.Repeat("─", renderer.width),
				cellStyleMarkdownTableRule,
			))
		}
		lines = append(lines, styledCellLine(
			"Row "+strconv.Itoa(rowIndex+1),
			cellStyleMarkdownTableHeader,
		))
		for column, cell := range row {
			spans := []cellSpan{{Text: strconv.Itoa(column+1) + ": ", Role: cellStyleMarkdownTableHeader}}
			value := cell.inlineSpans()
			if strings.TrimSpace(markdownSpansPlainText(value)) == "" {
				value = []cellSpan{{Text: "—", Role: renderer.baseRole}}
			}
			for _, span := range value {
				appendMarkdownSpan(&spans, span)
			}
			lines = append(lines, wrapMarkdownSpans(spans, renderer.width)...)
		}
	}
	return lines
}

func markdownSpansPlainText(spans []cellSpan) string {
	var text strings.Builder
	for _, span := range spans {
		text.WriteString(span.Text)
	}
	return text.String()
}
