package tui

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/parser"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const markdownParserExtensions = parser.NoIntraEmphasis |
	parser.Tables |
	parser.FencedCode |
	parser.Autolink |
	parser.Strikethrough |
	parser.SpaceHeadings |
	parser.BackslashLineBreak |
	parser.OrderedListStart

type markdownInlineStyle struct {
	base          cellStyleRole
	heading       bool
	strong        bool
	emphasis      bool
	strikethrough bool
	code          bool
	link          bool
	tableHeader   bool
}

func (style markdownInlineStyle) role() cellStyleRole {
	switch {
	case style.code:
		return cellStyleMarkdownCode
	case style.link:
		return cellStyleMarkdownLink
	case style.strong && style.emphasis:
		return cellStyleMarkdownStrongEmphasis
	case style.strong:
		return cellStyleMarkdownStrong
	case style.emphasis:
		return cellStyleMarkdownEmphasis
	case style.strikethrough:
		return cellStyleMarkdownStrikethrough
	case style.tableHeader:
		return cellStyleMarkdownTableHeader
	case style.heading:
		return cellStyleMarkdownHeading
	default:
		return style.base
	}
}

type markdownRenderer struct {
	width    int
	baseRole cellStyleRole
	lines    []cellLine
}

// parsedMarkdownDocument is a derived, per-revision cache. The authoritative
// source remains on the presentation item, while width, theme, and output-mode
// changes can project the same immutable syntax tree without reparsing it.
type parsedMarkdownDocument struct {
	source string
	root   ast.Node
}

func (cell *presentationCell) markdownMessageDocument(width int) cellDocument {
	message := cell.item.Message
	if message == nil {
		return cellDocument{}
	}
	source := sanitizeTerminalText(message.Text)
	if strings.TrimSpace(source) == "" && !message.Truncated {
		return cellDocument{}
	}

	prefixWidth := 0
	if cell.item.Kind == frontend.PresentationAssistantMessage && width > 2 {
		prefixWidth = 2
	}
	renderer := markdownRenderer{
		width:    max(1, width-prefixWidth),
		baseRole: lifecycleCellRole(cell.item.Lifecycle),
	}
	document := renderer.render(cell.parsedMarkdown(source), source)
	if message.Truncated {
		document.Lines = appendMarkdownSection(
			document.Lines,
			[]cellLine{styledCellLine("[…truncated]", cellStyleMuted)},
		)
		document.Truncated = true
		document.TruncationVisible = true
	}
	if prefixWidth != 0 {
		document.Lines = prefixMarkdownLines(document.Lines, width, "• ", "  ")
	}
	return document
}

func (cell *presentationCell) parsedMarkdown(source string) ast.Node {
	if cell.markdown != nil && cell.markdown.source == source {
		return cell.markdown.root
	}
	cell.markdown = &parsedMarkdownDocument{
		source: source,
		root:   parseMarkdownDocument(source),
	}
	return cell.markdown.root
}

func parseMarkdownDocument(source string) (document ast.Node) {
	// Markdown comes from a model and is therefore untrusted. Some malformed
	// table shapes can panic inside gomarkdown; degrade to the sanitized source
	// rather than letting presentation input terminate the coding process.
	defer func() {
		if recover() != nil {
			document = nil
		}
	}()
	markdownParser := parser.NewWithExtensions(markdownParserExtensions)
	return markdown.Parse([]byte(source), markdownParser)
}

func (renderer *markdownRenderer) render(document ast.Node, source string) cellDocument {
	if document == nil || len(document.GetChildren()) == 0 {
		return cellDocument{Lines: renderer.paragraphLines([]cellSpan{{Text: source, Role: renderer.baseRole}})}
	}
	renderer.renderChildren(document.GetChildren())
	renderer.lines = trimMarkdownBlankLines(renderer.lines)
	if len(renderer.lines) == 0 {
		renderer.lines = renderer.paragraphLines([]cellSpan{{Text: source, Role: renderer.baseRole}})
	}
	return cellDocument{Lines: renderer.lines}
}

func (renderer *markdownRenderer) renderChildren(nodes []ast.Node) {
	for _, node := range nodes {
		renderer.lines = appendMarkdownSection(renderer.lines, renderer.renderBlock(node))
	}
}

func (renderer *markdownRenderer) renderBlock(node ast.Node) []cellLine {
	switch typed := node.(type) {
	case *ast.Paragraph:
		return renderer.inlineBlockLines(typed, markdownInlineStyle{base: renderer.baseRole})
	case *ast.Heading:
		return renderer.inlineBlockLines(typed, markdownInlineStyle{base: renderer.baseRole, heading: true})
	case *ast.BlockQuote:
		return renderer.blockQuoteLines(typed)
	case *ast.List:
		return renderer.listLines(typed)
	case *ast.CodeBlock:
		return renderer.codeBlockLines(typed)
	case *ast.HorizontalRule:
		return []cellLine{styledCellLine(strings.Repeat("─", renderer.width), cellStyleMarkdownTableRule)}
	case *ast.Table:
		return renderer.tableLines(typed)
	case *ast.HTMLBlock:
		return renderer.rawBlockLines(string(typed.Literal))
	case *ast.MathBlock:
		return renderer.rawBlockLines(string(typed.Literal))
	default:
		children := node.GetChildren()
		if len(children) == 0 {
			if leaf := node.AsLeaf(); leaf != nil && len(leaf.Literal) != 0 {
				return renderer.paragraphLines([]cellSpan{{Text: string(leaf.Literal), Role: renderer.baseRole}})
			}
			return nil
		}
		nested := markdownRenderer{width: renderer.width, baseRole: renderer.baseRole}
		nested.renderChildren(children)
		return trimMarkdownBlankLines(nested.lines)
	}
}

func (renderer *markdownRenderer) inlineBlockLines(node ast.Node, style markdownInlineStyle) []cellLine {
	collector := markdownInlineCollector{lines: [][]cellSpan{{}}}
	collector.walkChildren(node.GetChildren(), style)
	lines := make([]cellLine, 0, len(collector.lines))
	for _, spans := range collector.lines {
		lines = append(lines, renderer.paragraphLines(spans)...)
	}
	return lines
}

func (renderer *markdownRenderer) paragraphLines(spans []cellSpan) []cellLine {
	return wrapMarkdownSpans(spans, renderer.width)
}

func (renderer *markdownRenderer) blockQuoteLines(quote *ast.BlockQuote) []cellLine {
	if renderer.width <= 2 {
		nested := markdownRenderer{width: renderer.width, baseRole: renderer.baseRole}
		nested.renderChildren(quote.GetChildren())
		return trimMarkdownBlankLines(nested.lines)
	}
	contentWidth := max(1, renderer.width-2)
	nested := markdownRenderer{width: contentWidth, baseRole: renderer.baseRole}
	nested.renderChildren(quote.GetChildren())
	lines := trimMarkdownBlankLines(nested.lines)
	for index := range lines {
		prefix := "│ "
		if strings.TrimSpace(lines[index].plainText()) == "" {
			prefix = "│"
		}
		lines[index].Spans = append(
			[]cellSpan{{Text: prefix, Role: cellStyleMarkdownQuote}},
			lines[index].Spans...,
		)
	}
	return lines
}

func (renderer *markdownRenderer) listLines(list *ast.List) []cellLine {
	items := make([]*ast.ListItem, 0, len(list.GetChildren()))
	for _, child := range list.GetChildren() {
		if item, ok := child.(*ast.ListItem); ok {
			items = append(items, item)
		}
	}
	ordered := list.ListFlags&ast.ListTypeOrdered != 0
	start := list.Start
	if start <= 0 {
		start = 1
	}
	var lines []cellLine
	for index, item := range items {
		marker := "• "
		if ordered {
			delimiter := list.Delimiter
			if delimiter == 0 {
				delimiter = '.'
			}
			marker = strconv.Itoa(start+index) + string(delimiter) + " "
		}
		markerWidth := ansi.StringWidth(marker)
		continuation := strings.Repeat(" ", markerWidth)
		contentWidth := max(1, renderer.width-markerWidth)
		if renderer.width <= markerWidth {
			marker = ""
			continuation = ""
			contentWidth = renderer.width
		}
		nested := markdownRenderer{width: contentWidth, baseRole: renderer.baseRole}
		nested.renderChildren(item.GetChildren())
		itemLines := trimMarkdownBlankLines(nested.lines)
		if list.Tight {
			itemLines = removeMarkdownBlankLines(itemLines)
		}
		if len(itemLines) == 0 {
			itemLines = []cellLine{{}}
		}
		itemLines = prefixMarkdownLines(itemLines, renderer.width, marker, continuation)
		if len(lines) != 0 && !list.Tight {
			lines = appendMarkdownBlank(lines)
		}
		lines = append(lines, itemLines...)
	}
	return lines
}

func (renderer *markdownRenderer) codeBlockLines(block *ast.CodeBlock) []cellLine {
	prefix := "  "
	contentWidth := max(1, renderer.width-2)
	if renderer.width <= 2 {
		prefix = ""
		contentWidth = renderer.width
	}
	value := sanitizeTerminalText(string(block.Literal))
	value = strings.TrimSuffix(value, "\n")
	logical := strings.Split(value, "\n")
	if len(logical) == 0 {
		logical = []string{""}
	}
	lines := make([]cellLine, 0, len(logical))
	for _, line := range logical {
		wrapped := hardWrapMarkdownText(expandCellTabs(line, 4), contentWidth, cellStyleMarkdownCode)
		if len(wrapped) == 0 {
			wrapped = []cellLine{{}}
		}
		for _, part := range wrapped {
			if prefix != "" {
				part.Spans = append([]cellSpan{{Text: prefix, Role: cellStyleMuted}}, part.Spans...)
			}
			lines = append(lines, part)
		}
	}
	return lines
}

func (renderer *markdownRenderer) rawBlockLines(value string) []cellLine {
	value = sanitizeTerminalText(value)
	logical := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	lines := make([]cellLine, 0, len(logical))
	for _, line := range logical {
		lines = append(lines, hardWrapMarkdownText(line, renderer.width, cellStyleMuted)...)
	}
	return lines
}

type markdownInlineCollector struct {
	lines [][]cellSpan
}

func (collector *markdownInlineCollector) walkChildren(nodes []ast.Node, style markdownInlineStyle) {
	for _, node := range nodes {
		collector.walk(node, style)
	}
}

func (collector *markdownInlineCollector) walk(node ast.Node, style markdownInlineStyle) {
	switch typed := node.(type) {
	case *ast.Text:
		collector.appendText(string(typed.Literal), style.role())
	case *ast.Code:
		style.code = true
		collector.appendText(string(typed.Literal), style.role())
	case *ast.Emph:
		style.emphasis = true
		collector.walkChildren(typed.GetChildren(), style)
	case *ast.Strong:
		style.strong = true
		collector.walkChildren(typed.GetChildren(), style)
	case *ast.Del:
		style.strikethrough = true
		collector.walkChildren(typed.GetChildren(), style)
	case *ast.Link:
		collector.appendLink(typed, style)
	case *ast.Image:
		collector.appendImage(typed, style)
	case *ast.Softbreak:
		collector.appendText(" ", style.role())
	case *ast.Hardbreak:
		collector.breakLine()
	case *ast.NonBlockingSpace:
		collector.appendText(" ", style.role())
	case *ast.HTMLSpan:
		collector.appendText(string(typed.Literal), style.role())
	case *ast.Math:
		collector.appendText(string(typed.Literal), cellStyleMarkdownCode)
	default:
		if leaf := node.AsLeaf(); leaf != nil && len(leaf.Literal) != 0 {
			collector.appendText(string(leaf.Literal), style.role())
			return
		}
		collector.walkChildren(node.GetChildren(), style)
	}
}

func (collector *markdownInlineCollector) appendLink(link *ast.Link, style markdownInlineStyle) {
	linkStyle := style
	linkStyle.link = true
	child := markdownInlineCollector{lines: [][]cellSpan{{}}}
	child.walkChildren(link.GetChildren(), linkStyle)
	collector.appendCollected(child.lines)
	destination := sanitizeTerminalText(string(link.Destination))
	label := markdownLinesPlainText(child.lines)
	if safeMarkdownLinkDestination(destination) && destination != "" && destination != label {
		collector.appendText(" ("+destination+")", cellStyleMuted)
	}
}

func (collector *markdownInlineCollector) appendImage(image *ast.Image, style markdownInlineStyle) {
	child := markdownInlineCollector{lines: [][]cellSpan{{}}}
	child.walkChildren(image.GetChildren(), style)
	label := strings.TrimSpace(markdownLinesPlainText(child.lines))
	if label == "" {
		label = "image"
	}
	collector.appendText("Image: "+label, cellStyleMuted)
	destination := sanitizeTerminalText(string(image.Destination))
	if safeMarkdownLinkDestination(destination) && destination != "" {
		collector.appendText(" ("+destination+")", cellStyleMuted)
	}
}

func (collector *markdownInlineCollector) appendCollected(lines [][]cellSpan) {
	for index, line := range lines {
		if index != 0 {
			collector.breakLine()
		}
		for _, span := range line {
			collector.appendText(span.Text, span.Role)
		}
	}
}

func (collector *markdownInlineCollector) appendText(value string, role cellStyleRole) {
	if len(collector.lines) == 0 {
		collector.lines = [][]cellSpan{{}}
	}
	parts := strings.Split(value, "\n")
	for index, part := range parts {
		if index != 0 {
			collector.breakLine()
		}
		appendMarkdownSpan(&collector.lines[len(collector.lines)-1], cellSpan{Text: part, Role: role})
	}
}

func (collector *markdownInlineCollector) breakLine() {
	collector.lines = append(collector.lines, nil)
}

func safeMarkdownLinkDestination(destination string) bool {
	if destination == "" || strings.ContainsAny(destination, "\n\r\t") {
		return false
	}
	parsed, err := url.Parse(destination)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "", "http", "https", "mailto":
		return true
	default:
		return false
	}
}

func markdownLinesPlainText(lines [][]cellSpan) string {
	plain := make([]string, 0, len(lines))
	for _, line := range lines {
		var text strings.Builder
		for _, span := range line {
			text.WriteString(span.Text)
		}
		plain = append(plain, text.String())
	}
	return strings.Join(plain, " ")
}

func appendMarkdownSection(lines, section []cellLine) []cellLine {
	section = trimMarkdownBlankLines(section)
	if len(section) == 0 {
		return lines
	}
	lines = trimMarkdownTrailingBlankLines(lines)
	if len(lines) != 0 {
		lines = append(lines, cellLine{})
	}
	return append(lines, section...)
}

func appendMarkdownBlank(lines []cellLine) []cellLine {
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1].plainText()) == "" {
		return lines
	}
	return append(lines, cellLine{})
}

func trimMarkdownBlankLines(lines []cellLine) []cellLine {
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start].plainText()) == "" {
		start++
	}
	return trimMarkdownTrailingBlankLines(lines[start:])
}

func trimMarkdownTrailingBlankLines(lines []cellLine) []cellLine {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1].plainText()) == "" {
		end--
	}
	return lines[:end]
}

func removeMarkdownBlankLines(lines []cellLine) []cellLine {
	compacted := make([]cellLine, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line.plainText()) != "" {
			compacted = append(compacted, line)
		}
	}
	return compacted
}

func prefixMarkdownLines(lines []cellLine, width int, firstPrefix, continuationPrefix string) []cellLine {
	width = max(1, width)
	first := true
	for index := range lines {
		if strings.TrimSpace(lines[index].plainText()) == "" {
			lines[index].Spans = nil
			continue
		}
		prefix := continuationPrefix
		if first {
			prefix = firstPrefix
			first = false
		}
		prefix = ansi.Truncate(prefix, width, "")
		lines[index].Spans = append(
			[]cellSpan{{Text: prefix, Role: cellStyleMuted}},
			lines[index].Spans...,
		)
	}
	return lines
}

type markdownStyledUnit struct {
	text  string
	role  cellStyleRole
	width int
}

func wrapMarkdownSpans(spans []cellSpan, width int) []cellLine {
	width = max(1, width)
	words := markdownWords(spans)
	if len(words) == 0 {
		return []cellLine{{}}
	}
	lines := make([]cellLine, 0, 1)
	line := cellLine{}
	lineWidth := 0
	for _, word := range words {
		wordWidth := markdownUnitsWidth(word)
		if lineWidth > 0 && lineWidth+1+wordWidth <= width {
			appendMarkdownSpan(&line.Spans, cellSpan{Text: " ", Role: word[0].role})
			lineWidth++
		} else if lineWidth > 0 {
			lines = append(lines, line)
			line = cellLine{}
			lineWidth = 0
		}
		for _, unit := range word {
			if unit.width > width {
				unit.text = "�"
				unit.width = 1
			}
			if lineWidth > 0 && lineWidth+unit.width > width {
				lines = append(lines, line)
				line = cellLine{}
				lineWidth = 0
			}
			appendMarkdownSpan(&line.Spans, cellSpan{Text: unit.text, Role: unit.role})
			lineWidth += unit.width
		}
	}
	if len(line.Spans) != 0 {
		lines = append(lines, line)
	}
	return lines
}

func markdownWords(spans []cellSpan) [][]markdownStyledUnit {
	words := make([][]markdownStyledUnit, 0, len(spans))
	var word []markdownStyledUnit
	flush := func() {
		if len(word) == 0 {
			return
		}
		words = append(words, word)
		word = nil
	}
	for _, span := range spans {
		value := span.Text
		for value != "" {
			cluster, width := ansi.FirstGraphemeCluster(value, ansi.GraphemeWidth)
			if cluster == "" {
				break
			}
			value = value[len(cluster):]
			runeValue, _ := utf8.DecodeRuneInString(cluster)
			if unicode.IsSpace(runeValue) {
				flush()
				continue
			}
			word = append(word, markdownStyledUnit{text: cluster, role: span.Role, width: max(0, width)})
		}
	}
	flush()
	return words
}

func hardWrapMarkdownText(value string, width int, role cellStyleRole) []cellLine {
	width = max(1, width)
	if value == "" {
		return []cellLine{{}}
	}
	lines := make([]cellLine, 0, 1)
	line := cellLine{}
	lineWidth := 0
	for value != "" {
		cluster, clusterWidth := ansi.FirstGraphemeCluster(value, ansi.GraphemeWidth)
		if cluster == "" {
			break
		}
		value = value[len(cluster):]
		if clusterWidth > width {
			cluster = "�"
			clusterWidth = 1
		}
		if lineWidth > 0 && lineWidth+clusterWidth > width {
			lines = append(lines, line)
			line = cellLine{}
			lineWidth = 0
		}
		appendMarkdownSpan(&line.Spans, cellSpan{Text: cluster, Role: role})
		lineWidth += max(0, clusterWidth)
	}
	if len(line.Spans) != 0 {
		lines = append(lines, line)
	}
	return lines
}

func appendMarkdownSpan(spans *[]cellSpan, span cellSpan) {
	if span.Text == "" {
		return
	}
	if len(*spans) != 0 && (*spans)[len(*spans)-1].Role == span.Role {
		(*spans)[len(*spans)-1].Text += span.Text
		return
	}
	*spans = append(*spans, span)
}

func markdownUnitsWidth(units []markdownStyledUnit) int {
	width := 0
	for _, unit := range units {
		width += unit.width
	}
	return width
}
