package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func TestRepositoryDiffEvidenceKeepsTypedStatesAndSemanticStyles(t *testing.T) {
	diff := repositoryDiffRenderFixture()
	document := cellDocument{Lines: renderRepositoryDiffEvidence(diff, 24)}
	plain := document.plainText()
	normalized := strings.Join(strings.Fields(plain), " ")
	for _, want := range []string{
		"old.go -> new.go",
		"pre-existing",
		"provenance: indeterminate (baseline snapshot incomplete)",
		"[binary, submodule, symlink, omitted: exceeds byte limit, truncated]",
		"provenance: baseline refresh unavailable",
		"@@ -10,2 +10,2 @@ func render()",
		"[… hunk evidence truncated …]",
		"[… diff evidence incomplete or stale …]",
	} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("repository diff omits %q:\n%s", want, plain)
		}
	}
	if strings.Contains(strings.ToLower(plain), "edited") {
		t.Fatalf("passive evidence makes an authorship claim:\n%s", plain)
	}

	var insertionRows, deletionRows, contextRows int
	roles := make(map[cellStyleRole]bool)
	for _, line := range document.Lines {
		if width := ansi.StringWidth(line.plainText()); width > 24 {
			t.Fatalf("row width %d exceeds 24: %q", width, line.plainText())
		}
		switch line.RowStyle {
		case cellRowInsertion:
			insertionRows++
		case cellRowDeletion:
			deletionRows++
		default:
			if strings.Contains(line.plainText(), "unchanged") {
				contextRows++
			}
		}
		for _, span := range line.Spans {
			roles[span.Role] = true
		}
	}
	if insertionRows < 2 || deletionRows < 2 {
		t.Fatalf("wrapped row styles were not preserved: additions=%d deletions=%d", insertionRows, deletionRows)
	}
	if contextRows == 0 {
		t.Fatal("context diff row lost its default row style")
	}
	for _, role := range []cellStyleRole{
		cellStyleSyntaxKeyword,
		cellStyleSyntaxString,
		cellStyleSyntaxNumber,
		cellStyleSyntaxComment,
		cellStyleSyntaxType,
	} {
		if !roles[role] {
			t.Fatalf("syntax role %d was not preserved across diff wrapping", role)
		}
	}
}

func TestRepositoryDiffPaletteGoldens(t *testing.T) {
	testCases := []struct {
		name    string
		context cellRenderContext
	}{
		{
			name: "dark_truecolor",
			context: cellRenderContext{
				Width: 24, Theme: cellThemeDark, ColorLevel: cellColorTrueColor,
			},
		},
		{
			name: "light_truecolor",
			context: cellRenderContext{
				Width: 24, Theme: cellThemeLight, ColorLevel: cellColorTrueColor,
			},
		},
		{
			name: "dark_ansi256",
			context: cellRenderContext{
				Width: 24, Theme: cellThemeDark, ColorLevel: cellColorANSI256,
			},
		},
		{
			name: "light_ansi256",
			context: cellRenderContext{
				Width: 24, Theme: cellThemeLight, ColorLevel: cellColorANSI256,
			},
		},
		{
			name: "dark_ansi16",
			context: cellRenderContext{
				Width: 24, Theme: cellThemeDark, ColorLevel: cellColorANSI16,
			},
		},
		{
			name: "light_ansi16",
			context: cellRenderContext{
				Width: 24, Theme: cellThemeLight, ColorLevel: cellColorANSI16,
			},
		},
		{
			name: "light_no_color",
			context: cellRenderContext{
				Width: 24, Theme: cellThemeLight, ColorLevel: cellColorNone,
			},
		},
	}

	document := repositoryDiffPaletteDocument(24)
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			rendered := renderCellDocument(document, testCase.context, cellRenderFull)
			assertRepositoryDiffPaletteInvariants(t, document, rendered, testCase.context)
			goldenPath := filepath.Join("testdata", "repository_diff_palette", testCase.name+".golden")
			golden, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			visible := strings.ReplaceAll(rendered, "\x1b", "<ESC>")
			if want := strings.TrimRight(string(golden), "\r\n"); visible != want {
				t.Fatalf("palette golden mismatch:\nwant:\n%s\n\ngot:\n%s", want, visible)
			}
		})
	}
}

func TestRepositoryDiffWrappingBoundsTinyUnicodeAndTabs(t *testing.T) {
	for _, width := range []int{1, 2, 5, 12} {
		rows := renderRepositoryDiffLine(
			"unicode.go",
			codingworkspace.DiffLine{Kind: "addition", NewLine: 7, Text: "\t界e\u0301🙂value"},
			width,
			1,
		)
		if len(rows) == 0 {
			t.Fatalf("width %d produced no rows", width)
		}
		for _, row := range rows {
			if row.RowStyle != cellRowInsertion {
				t.Fatalf("width %d lost insertion style: %+v", width, row)
			}
			if visible := ansi.StringWidth(row.plainText()); visible > width {
				t.Fatalf("width %d produced %d-cell row %q", width, visible, row.plainText())
			}
		}
		if strings.Contains(rows[0].plainText(), "�") {
			t.Fatalf("width %d replaced a tab instead of wrapping its spaces: %q", width, rows[0].plainText())
		}
		rendered := strings.Split(renderCellDocument(
			cellDocument{Lines: rows},
			cellRenderContext{Width: width, Theme: cellThemeDark, ColorLevel: cellColorTrueColor},
			cellRenderFull,
		), "\n")
		for index, line := range rendered {
			if visible := ansi.StringWidth(line); visible != width {
				t.Fatalf("rich tiny row %d at width %d uses %d cells: %q", index, width, visible, line)
			}
			if !strings.Contains(line, "\x1b[48;2;33;58;43") {
				t.Fatalf("rich tiny row %d at width %d lost its background: %q", index, width, line)
			}
		}

		tabRows := wrapRepositoryDiffSpans([]cellSpan{{Text: "\t", Role: cellStyleDefault}}, width)
		var expandedTab strings.Builder
		for _, row := range tabRows {
			for _, span := range row {
				expandedTab.WriteString(span.Text)
			}
		}
		if expandedTab.String() != "    " {
			t.Fatalf("width %d tab expansion = %q", width, expandedTab.String())
		}
	}
}

func TestRepositoryDiffToolExpandsToRichHistoricalCell(t *testing.T) {
	controller := newController(t)
	controller.TurnStarted("turn-1", "inspect changes")
	controller.ToolStarted("turn-1", "call-1", "repository_diff", "{}")
	controller.ToolRepositoryDiff("turn-1", "call-1", repositoryDiffRenderFixture())
	controller.ToolCompleted("turn-1", "call-1", "repository_diff", "", 0, false, nil)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.theme = cellThemeDark
	model.colorLevel = cellColorTrueColor
	model.resize(48, 18)
	model.refreshViewport()
	if strings.Contains(ansi.Strip(model.document.text()), "-func Old") {
		t.Fatalf("compact diff unexpectedly includes full hunks: %q", ansi.Strip(model.document.text()))
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlO})
	if model.expandedToolID == "" || !model.toolSelectionActive {
		t.Fatalf("repository diff did not expand: id=%q notice=%q", model.expandedToolID, model.workspaceNotice)
	}
	rendered := model.document.text()
	if !strings.Contains(rendered, "\x1b[48;2;33;58;43") ||
		!strings.Contains(rendered, "\x1b[48;2;74;34;29") ||
		!strings.Contains(ansi.Strip(rendered), "-func Old") ||
		!strings.Contains(ansi.Strip(rendered), "+func NewType") {
		t.Fatalf("expanded repository diff is not the rich historical cell: %q", rendered)
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlO})
	if model.expandedToolID != "" {
		t.Fatalf("second Ctrl+O did not collapse repository diff: %q", model.expandedToolID)
	}
}

func TestRepositoryDiffRenameHighlightsEachSideByItsOwnExtension(t *testing.T) {
	file := codingworkspace.DiffFile{
		OriginalPath: "old.go",
		Path:         "new.json",
	}
	deletion := renderRepositoryDiffLine(
		repositoryDiffSyntaxPath(file, codingworkspace.DiffLine{Kind: "deletion"}),
		codingworkspace.DiffLine{Kind: "deletion", OldLine: 1, Text: "func Old()"},
		80,
		1,
	)
	addition := renderRepositoryDiffLine(
		repositoryDiffSyntaxPath(file, codingworkspace.DiffLine{Kind: "addition"}),
		codingworkspace.DiffLine{Kind: "addition", NewLine: 1, Text: `{"ready": true}`},
		80,
		1,
	)
	if !repositoryDiffRowsContainRole(deletion, cellStyleSyntaxKeyword) {
		t.Fatalf("deletion was not highlighted as %s: %+v", file.OriginalPath, deletion)
	}
	if !repositoryDiffRowsContainRole(addition, cellStyleSyntaxString) ||
		!repositoryDiffRowsContainRole(addition, cellStyleSyntaxKeyword) {
		t.Fatalf("addition was not highlighted as %s: %+v", file.Path, addition)
	}
}

func repositoryDiffRowsContainRole(rows []cellLine, role cellStyleRole) bool {
	for _, row := range rows {
		for _, span := range row.Spans {
			if span.Role == role {
				return true
			}
		}
	}
	return false
}

func repositoryDiffRenderFixture() codingworkspace.DiffResult {
	return codingworkspace.DiffResult{
		SchemaVersion: codingworkspace.RepositoryDiffSchemaV1,
		Target:        codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetCurrent},
		Files: []codingworkspace.DiffFile{
			{
				Path: "new.go", OriginalPath: "old.go", Status: "R ", Additions: 2, Deletions: 1,
				Binary: true, Submodule: true, Symlink: true, Omitted: "exceeds byte limit", Truncated: true,
				Provenance:       codingworkspace.ProvenancePreExisting,
				ProvenanceReason: "baseline refresh unavailable",
				Hunks: []codingworkspace.DiffHunk{{
					OldStart: 10, OldLines: 2, NewStart: 10, NewLines: 2, Header: "func render()", Truncated: true,
					Lines: []codingworkspace.DiffLine{
						{Kind: "context", OldLine: 10, NewLine: 10, Text: "unchanged"},
						{Kind: "deletion", OldLine: 11, Text: `func Old() string { return "gone" } // 41`},
						{
							Kind:    "addition",
							NewLine: 11,
							Text:    `func NewType() string { count := 42; return "added" } // in`,
						},
					},
				}},
			},
		},
		Additions: 2,
		Deletions: 1,
		Truncated: true,
		Stale:     true,
		Provenance: &codingworkspace.ProvenanceResult{
			Indeterminate: true,
			Reason:        "baseline snapshot incomplete",
		},
	}
}

func repositoryDiffPaletteDocument(width int) cellDocument {
	lines := make([]cellLine, 0, 6)
	for _, line := range []codingworkspace.DiffLine{
		{Kind: "context", OldLine: 9, NewLine: 9, Text: "unchanged context"},
		{Kind: "deletion", OldLine: 10, Text: `return "old value" // out`},
		{Kind: "addition", NewLine: 10, Text: `return "new value" // in`},
	} {
		lines = append(lines, renderRepositoryDiffLine("sample.go", line, width, 2)...)
	}
	return cellDocument{Lines: lines}
}

func assertRepositoryDiffPaletteInvariants(
	t *testing.T,
	document cellDocument,
	rendered string,
	context cellRenderContext,
) {
	t.Helper()
	renderedLines := strings.Split(rendered, "\n")
	if len(renderedLines) != len(document.Lines) {
		t.Fatalf("rendered lines = %d, semantic rows = %d", len(renderedLines), len(document.Lines))
	}
	for index, line := range document.Lines {
		renderedLine := renderedLines[index]
		if ansi.StringWidth(renderedLine) > context.Width {
			t.Fatalf("line %d exceeds width %d: %q", index, context.Width, renderedLine)
		}
		if line.RowStyle == cellRowDefault {
			if strings.Contains(renderedLine, "48;") {
				t.Fatalf("context row %d has a background: %q", index, renderedLine)
			}
			continue
		}
		plain := ansi.Strip(renderedLine)
		if !strings.Contains(plain, "+") && !strings.Contains(plain, "-") && index > 0 &&
			document.Lines[index-1].RowStyle != line.RowStyle {
			t.Fatalf("first changed row lacks an explicit sign: %q", plain)
		}
		if context.ColorLevel >= cellColorANSI256 {
			if width := ansi.StringWidth(renderedLine); width != context.Width {
				t.Fatalf("rich diff row %d width = %d, want %d: %q", index, width, context.Width, renderedLine)
			}
			background := strings.Join(cellRowBackgroundCodes(line.RowStyle, context), ";")
			if background == "" || !strings.Contains(renderedLine, "\x1b["+background) {
				t.Fatalf("rich diff row %d omits %s background: %q", index, background, renderedLine)
			}
		} else if strings.Contains(renderedLine, "48;") {
			t.Fatalf("degraded diff row %d unexpectedly uses a background: %q", index, renderedLine)
		}
	}
	if context.ColorLevel == cellColorNone && strings.Contains(rendered, "\x1b") {
		t.Fatalf("no-color rendering contains SGR: %q", rendered)
	}
	if context.Theme == cellThemeLight && context.ColorLevel == cellColorTrueColor {
		for _, want := range []string{"48;2;172;238;187", "48;2;255;206;203"} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("light truecolor rendering omits distinct gutter %s: %q", want, rendered)
			}
		}
	}
	if context.Theme == cellThemeLight && context.ColorLevel == cellColorANSI256 {
		for _, want := range []string{"48;5;157", "48;5;217"} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("light ANSI256 rendering omits distinct gutter %s: %q", want, rendered)
			}
		}
	}
	if !strings.Contains(ansi.Strip(rendered), "-return") || !strings.Contains(ansi.Strip(rendered), "+return") {
		t.Fatalf("rendering is not distinguishable without color: %q", ansi.Strip(rendered))
	}
	if strings.Contains(rendered, fmt.Sprintf("%c]8;", 0x1b)) {
		t.Fatalf("diff rendering unexpectedly emitted hyperlinks: %q", rendered)
	}
}
