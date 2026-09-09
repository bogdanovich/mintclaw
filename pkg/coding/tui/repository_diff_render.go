package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func renderRepositoryDiffEvidence(diff codingworkspace.DiffResult, width int) []cellLine {
	width = max(1, width)
	lines := make([]cellLine, 0, len(diff.Files)*3)
	appendWrapped := func(text string, role cellStyleRole) {
		wrapped := wrapCellDocument(
			cellDocument{Lines: logicalCellLines(text, role)},
			width,
		)
		lines = append(lines, wrapped.Lines...)
	}
	if diff.UnavailableReason != "" {
		appendWrapped("  unavailable: "+sanitizeTerminalText(diff.UnavailableReason), cellStyleFailure)
	}
	if diff.Warning != "" {
		appendWrapped("  warning: "+sanitizeTerminalText(diff.Warning), cellStyleFailure)
	}
	if diff.Provenance != nil {
		switch {
		case diff.Provenance.Indeterminate:
			label := "  provenance: indeterminate"
			if reason := boundedSingleLine(diff.Provenance.Reason, 4096); reason != "" {
				label += " (" + reason + ")"
			}
			appendWrapped(label, cellStyleMuted)
		case diff.Provenance.Reason != "":
			appendWrapped("  provenance: "+sanitizeTerminalText(diff.Provenance.Reason), cellStyleMuted)
		}
	}
	if len(diff.Files) == 0 && diff.UnavailableReason == "" {
		appendWrapped("  No changed files observed.", cellStyleMuted)
	}

	for fileIndex, file := range diff.Files {
		if fileIndex > 0 {
			lines = append(lines, styledCellLine("", cellStyleDefault))
		}
		appendWrapped(repositoryDiffFileSummary(file), cellStyleMuted)
		if file.ProvenanceReason != "" {
			appendWrapped("    provenance: "+sanitizeTerminalText(file.ProvenanceReason), cellStyleMuted)
		}
		lineNumberWidth := repositoryDiffLineNumberWidth(file)
		for _, hunk := range file.Hunks {
			appendWrapped(repositoryDiffHunkHeader(hunk), cellStyleAccent)
			for _, line := range hunk.Lines {
				lines = append(
					lines,
					renderRepositoryDiffLine(repositoryDiffSyntaxPath(file, line), line, width, lineNumberWidth)...,
				)
			}
			if hunk.Truncated {
				appendWrapped("    [… hunk evidence truncated …]", cellStyleMuted)
			}
		}
	}
	if diff.Truncated || diff.Stale {
		appendWrapped("  [… diff evidence incomplete or stale …]", cellStyleMuted)
	}
	return lines
}

func repositoryDiffSyntaxPath(file codingworkspace.DiffFile, line codingworkspace.DiffLine) string {
	if line.Kind == "deletion" && file.OriginalPath != "" {
		return file.OriginalPath
	}
	return file.Path
}

func repositoryDiffHunkHeader(hunk codingworkspace.DiffHunk) string {
	header := fmt.Sprintf(
		"  @@ -%s +%s @@",
		repositoryDiffRange(hunk.OldStart, hunk.OldLines),
		repositoryDiffRange(hunk.NewStart, hunk.NewLines),
	)
	if detail := boundedSingleLine(hunk.Header, 4096); detail != "" {
		header += " " + detail
	}
	return header
}

func repositoryDiffRange(start, count int) string {
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

func repositoryDiffLineNumberWidth(file codingworkspace.DiffFile) int {
	maximum := 0
	for _, hunk := range file.Hunks {
		for _, line := range hunk.Lines {
			maximum = max(maximum, line.OldLine, line.NewLine)
		}
	}
	return max(1, len(fmt.Sprintf("%d", maximum)))
}

func renderRepositoryDiffLine(
	path string,
	line codingworkspace.DiffLine,
	width int,
	lineNumberWidth int,
) []cellLine {
	rowStyle, sign, gutterRole := repositoryDiffLineStyle(line.Kind)
	lineNumber := line.NewLine
	if line.Kind == "deletion" || lineNumber == 0 {
		lineNumber = line.OldLine
	}
	lineNumberText := ""
	if lineNumber > 0 {
		lineNumberText = fmt.Sprintf("%d", lineNumber)
	}
	text := sanitizeTerminalText(line.Text)
	text = strings.ReplaceAll(text, "\n", `\n`)
	content := highlightRepositoryDiffContent(path, text)

	gutter := "  " + fmt.Sprintf("%*s", lineNumberWidth, lineNumberText) + " "
	prefixWidth := ansi.StringWidth(gutter) + 1
	if prefixWidth >= width {
		compact := append([]cellSpan{{Text: sign, Role: repositoryDiffSignRole(line.Kind)}}, content...)
		chunks := wrapRepositoryDiffSpans(compact, width)
		return repositoryDiffRows(chunks, nil, nil, rowStyle)
	}

	chunks := wrapRepositoryDiffSpans(content, width-prefixWidth)
	firstPrefix := []cellSpan{
		{Text: gutter, Role: gutterRole},
		{Text: sign, Role: repositoryDiffSignRole(line.Kind)},
	}
	continuationPrefix := []cellSpan{{
		Text: strings.Repeat(" ", prefixWidth),
		Role: gutterRole,
	}}
	return repositoryDiffRows(chunks, firstPrefix, continuationPrefix, rowStyle)
}

func repositoryDiffRows(
	chunks [][]cellSpan,
	firstPrefix []cellSpan,
	continuationPrefix []cellSpan,
	rowStyle cellRowStyle,
) []cellLine {
	rows := make([]cellLine, 0, len(chunks))
	for index, chunk := range chunks {
		prefix := continuationPrefix
		if index == 0 {
			prefix = firstPrefix
		}
		spans := make([]cellSpan, 0, len(prefix)+len(chunk))
		spans = append(spans, prefix...)
		spans = append(spans, chunk...)
		rows = append(rows, cellLine{Spans: spans, RowStyle: rowStyle})
	}
	return rows
}

func repositoryDiffLineStyle(kind string) (cellRowStyle, string, cellStyleRole) {
	switch kind {
	case "addition":
		return cellRowInsertion, "+", cellStyleDiffGutterInsertion
	case "deletion":
		return cellRowDeletion, "-", cellStyleDiffGutterDeletion
	default:
		return cellRowDefault, " ", cellStyleDiffGutter
	}
}

func repositoryDiffSignRole(kind string) cellStyleRole {
	switch kind {
	case "addition":
		return cellStyleInsertion
	case "deletion":
		return cellStyleDeletion
	default:
		return cellStyleDefault
	}
}

func wrapRepositoryDiffSpans(spans []cellSpan, width int) [][]cellSpan {
	width = max(1, width)
	rows := make([][]cellSpan, 0, 1)
	current := make([]cellSpan, 0, len(spans))
	column := 0
	var pendingText strings.Builder
	var pendingRole cellStyleRole
	hasPending := false
	flushPending := func() {
		if !hasPending {
			return
		}
		current = append(current, cellSpan{Text: pendingText.String(), Role: pendingRole})
		pendingText = strings.Builder{}
		hasPending = false
	}
	flush := func() {
		flushPending()
		rows = append(rows, current)
		current = make([]cellSpan, 0, len(spans))
		column = 0
	}
	appendText := func(text string, role cellStyleRole, displayWidth int) {
		if hasPending && pendingRole != role {
			flushPending()
		}
		pendingRole = role
		hasPending = true
		pendingText.WriteString(text)
		column += displayWidth
	}

	for _, span := range spans {
		value := span.Text
		for value != "" {
			cluster, clusterWidth := ansi.FirstGraphemeCluster(value, ansi.GraphemeWidth)
			if cluster == "" {
				break
			}
			value = value[len(cluster):]
			if cluster == "\t" {
				for range 4 {
					if column > 0 && column+1 > width {
						flush()
					}
					appendText(" ", span.Role, 1)
					if column >= width {
						flush()
					}
				}
				continue
			}
			if clusterWidth > width {
				cluster = "�"
				clusterWidth = 1
			}
			if column > 0 && column+clusterWidth > width {
				flush()
			}
			appendText(cluster, span.Role, max(0, clusterWidth))
			if column >= width {
				flush()
			}
		}
	}
	flushPending()
	if len(current) != 0 || len(rows) == 0 {
		rows = append(rows, current)
	}
	return rows
}
