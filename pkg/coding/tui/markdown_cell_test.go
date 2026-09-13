package tui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestAssistantMarkdownRendersSemanticStructureAtAdmittedWidths(t *testing.T) {
	source := "# Result\n\n" +
		"The **fix** is *ready*, ***combined***, ~~obsolete~~, and uses `go test`.\n\n" +
		"- First item\n  - Nested item\n- Second item\n\n" +
		"3. Ordered item\n4. Another item\n\n" +
		"> Quoted **evidence** remains readable.\n\n" +
		"[Documentation](https://example.com/docs)\n\n---\n\n" +
		"```go\nfunc main() {\n\tprintln(\"界\")\n}\n```"
	cell := markdownPresentationCell(frontend.PresentationFinalAnswer, source, true)

	for _, width := range []int{40, 80, 120} {
		context := cellRenderContext{Width: width, Theme: cellThemeDark, ColorLevel: cellColorNone}
		document := cell.Render(context, cellRenderCompact)
		plain := document.plainText()
		normalized := strings.Join(strings.Fields(plain), " ")
		for _, want := range []string{
			"Result", "The fix is ready, combined, obsolete, and uses go test.", "• First item", "• Nested item",
			"3. Ordered item", "4. Another item", "│ Quoted evidence remains readable.",
			"Documentation (https://example.com/docs)", "func main() {", `println("界")`,
		} {
			if !strings.Contains(normalized, want) {
				t.Fatalf("width %d Markdown omits %q:\n%s", width, want, plain)
			}
		}
		for _, forbidden := range []string{
			"# Result", "**fix**", "*ready*", "***combined***", "~~obsolete~~", "`go test`", "```",
		} {
			if strings.Contains(plain, forbidden) {
				t.Fatalf("width %d Markdown retained delimiter %q:\n%s", width, forbidden, plain)
			}
		}
		assertMarkdownDocumentWidth(t, document, width)
	}

	styled := cell.Render(cellRenderContext{
		Width: 80, Theme: cellThemeDark, ColorLevel: cellColorTrueColor,
	}, cellRenderCompact)
	for _, role := range []cellStyleRole{
		cellStyleMarkdownHeading,
		cellStyleMarkdownStrong,
		cellStyleMarkdownEmphasis,
		cellStyleMarkdownStrongEmphasis,
		cellStyleMarkdownStrikethrough,
		cellStyleMarkdownCode,
		cellStyleMarkdownLink,
		cellStyleMarkdownQuote,
	} {
		if !markdownDocumentHasRole(styled, role) {
			t.Fatalf("styled Markdown omits role %d: %+v", role, styled)
		}
	}
	rendered := renderCellDocument(styled, cellRenderContext{
		Width: 80, Theme: cellThemeDark, ColorLevel: cellColorTrueColor,
	}, cellRenderCompact)
	if !strings.Contains(rendered, "\x1b[") || ansi.Strip(rendered) != styled.plainText() {
		t.Fatalf("styled Markdown ANSI projection = %q", rendered)
	}
}

func TestAssistantMarkdownStylesRespectTerminalColorCapabilities(t *testing.T) {
	cell := markdownPresentationCell(
		frontend.PresentationFinalAnswer,
		"## Result\n\n**strong** *emphasis* `code` [link](https://example.com)",
		true,
	)
	tests := []struct {
		name       string
		theme      cellTheme
		color      cellColorLevel
		wantEscape bool
	}{
		{name: "no color", theme: cellThemeDark, color: cellColorNone},
		{name: "ansi16 dark", theme: cellThemeDark, color: cellColorANSI16, wantEscape: true},
		{name: "ansi16 light", theme: cellThemeLight, color: cellColorANSI16, wantEscape: true},
		{name: "ansi256 dark", theme: cellThemeDark, color: cellColorANSI256, wantEscape: true},
		{name: "ansi256 light", theme: cellThemeLight, color: cellColorANSI256, wantEscape: true},
		{name: "truecolor dark", theme: cellThemeDark, color: cellColorTrueColor, wantEscape: true},
		{name: "truecolor light", theme: cellThemeLight, color: cellColorTrueColor, wantEscape: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			context := cellRenderContext{Width: 40, Theme: test.theme, ColorLevel: test.color}
			document := cell.Render(context, cellRenderCompact)
			rendered := renderCellDocument(document, context, cellRenderCompact)
			if strings.Contains(rendered, "\x1b[") != test.wantEscape || ansi.Strip(rendered) != document.plainText() {
				t.Fatalf("rendered Markdown = %q", rendered)
			}
			assertMarkdownDocumentWidth(t, document, context.Width)
		})
	}

	plainContext := cellRenderContext{Width: 40, Theme: cellThemeDark, ColorLevel: cellColorTrueColor}
	plainDocument := cell.Render(plainContext, cellRenderPlain)
	for _, line := range plainDocument.Lines {
		for _, span := range line.Spans {
			if span.Role != cellStyleDefault {
				t.Fatalf("plain Markdown retained role %d", span.Role)
			}
		}
	}
}

func TestAssistantMarkdownPreservesInlineCodeWhitespace(t *testing.T) {
	cell := markdownPresentationCell(
		frontend.PresentationFinalAnswer,
		"Run `git  status` and compare `a\tb`.",
		true,
	)
	for _, mode := range []cellRenderMode{cellRenderCompact, cellRenderPlain} {
		document := cell.Render(cellRenderContext{Width: 80, ColorLevel: cellColorNone}, mode)
		plain := document.plainText()
		if !strings.Contains(plain, "git  status") || !strings.Contains(plain, "a   b") {
			t.Fatalf("mode %d inline-code whitespace = %q", mode, plain)
		}
		if strings.ContainsRune(plain, '\t') {
			t.Fatalf("mode %d retained a terminal-dependent tab: %q", mode, plain)
		}
		assertMarkdownDocumentWidth(t, document, 80)
	}
}

func TestAssistantMarkdownTightListPreservesNestedCodeBlankLine(t *testing.T) {
	cell := markdownPresentationCell(
		frontend.PresentationFinalAnswer,
		"- Evidence:\n  ```text\n  a\n\n  b\n  ```",
		true,
	)
	for _, mode := range []cellRenderMode{cellRenderCompact, cellRenderPlain} {
		document := cell.Render(cellRenderContext{Width: 40, ColorLevel: cellColorNone}, mode)
		lines := strings.Split(document.plainText(), "\n")
		aIndex := -1
		bIndex := -1
		for index, line := range lines {
			switch strings.TrimSpace(line) {
			case "a":
				aIndex = index
			case "b":
				bIndex = index
			}
		}
		if aIndex < 0 || bIndex != aIndex+2 || strings.TrimSpace(lines[aIndex+1]) != "" {
			t.Fatalf("mode %d nested code blank line = %q", mode, document.plainText())
		}
		assertMarkdownDocumentWidth(t, document, 40)
	}
}

func TestAssistantMarkdownTablesAdaptWithoutDroppingData(t *testing.T) {
	source := "| Key | Value | State |\n| --- | --- | ---: |\n" +
		"| alpha | A readable explanation for the first row | 1 |\n" +
		"| beta | Another explanation with `inline code` | 2 |"
	cell := markdownPresentationCell(frontend.PresentationFinalAnswer, source, true)

	wide := cell.Render(cellRenderContext{
		Width: 80, Theme: cellThemeLight, ColorLevel: cellColorNone,
	}, cellRenderCompact).plainText()
	for _, want := range []string{"Key", "Value", "State", "alpha", "first row", "beta", "inline code", "━"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide table omits %q:\n%s", want, wide)
		}
	}
	if strings.Contains(wide, "| ---") || strings.Contains(wide, "| alpha |") {
		t.Fatalf("wide table retained Markdown pipes:\n%s", wide)
	}

	narrowDocument := cell.Render(cellRenderContext{
		Width: 24, Theme: cellThemeLight, ColorLevel: cellColorNone,
	}, cellRenderCompact)
	narrow := narrowDocument.plainText()
	narrowNormalized := strings.Join(strings.Fields(narrow), " ")
	for _, want := range []string{
		"Key: alpha", "Value: A readable", "State: 1", "Key: beta", "inline code", "State: 2",
	} {
		if !strings.Contains(narrowNormalized, want) {
			t.Fatalf("narrow table omits %q:\n%s", want, narrow)
		}
	}
	if strings.Contains(narrow, "━") || strings.Contains(narrow, "|") {
		t.Fatalf("narrow table did not use records:\n%s", narrow)
	}
	if !markdownDocumentHasRole(narrowDocument, cellStyleMarkdownCode) {
		t.Fatalf("narrow table dropped inline-code styling: %+v", narrowDocument)
	}
	assertMarkdownDocumentWidth(t, narrowDocument, 24)
}

func TestAssistantMarkdownKeepsSourceAuthoritativeAcrossReflowAndResume(t *testing.T) {
	source := "## Summary\n\nA **source-backed** answer with a long sentence that must reflow at narrow widths."
	cell := markdownPresentationCell(frontend.PresentationFinalAnswer, source, true)

	wide := cell.Render(cellRenderContext{Width: 80, ColorLevel: cellColorNone}, cellRenderCompact)
	parsed := cell.markdown
	narrow := cell.Render(cellRenderContext{Width: 20, ColorLevel: cellColorNone}, cellRenderCompact)
	if cell.item.Message.Text != source || wide.plainText() == narrow.plainText() ||
		len(narrow.Lines) <= len(wide.Lines) {
		t.Fatalf(
			"source/reflow mismatch: source=%q wide=%q narrow=%q",
			cell.item.Message.Text,
			wide.plainText(),
			narrow.plainText(),
		)
	}
	if parsed == nil || cell.markdown != parsed {
		t.Fatal("Markdown reflow reparsed the unchanged authoritative source")
	}

	resumedStore, err := newHydratedSemanticCellStore([]frontend.TranscriptEntry{*cell.item.Message})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumedStore.ordered) != 1 || resumedStore.ordered[0].item.Message.Text != source {
		t.Fatalf("hydrated Markdown source = %+v", semanticStoreItems(resumedStore))
	}
	resumedDocument := resumedStore.ordered[0].Render(
		cellRenderContext{Width: 20, ColorLevel: cellColorNone},
		cellRenderCompact,
	)
	if resumedDocument.plainText() != narrow.plainText() {
		t.Fatalf("resumed Markdown = %q, want %q", resumedDocument.plainText(), narrow.plainText())
	}
	for _, width := range []int{30, 40, 50, 60, 70, 80} {
		cell.Render(cellRenderContext{Width: width, ColorLevel: cellColorNone}, cellRenderCompact)
	}
	if len(cell.renderCache) > maxCellRenderCacheEntries {
		t.Fatalf("Markdown render cache entries = %d", len(cell.renderCache))
	}
}

func TestAssistantMarkdownStreamingReplacesOnlyActiveCell(t *testing.T) {
	committed := markdownPresentationItem(
		"committed",
		1,
		1,
		frontend.PresentationCompleted,
		"**Committed** context.",
		true,
	)
	active := markdownPresentationItem(
		"active",
		2,
		1,
		frontend.PresentationActive,
		"## Result\n\n| Key |",
		false,
	)
	store, err := newSemanticCellStore([]frontend.PresentationItem{committed, active})
	if err != nil {
		t.Fatal(err)
	}
	context := cellRenderContext{Width: 40, ColorLevel: cellColorNone}
	store.ordered[0].Render(context, cellRenderCompact)
	partial := store.ordered[1].Render(context, cellRenderCompact).plainText()
	if !strings.Contains(partial, "Result") || !strings.Contains(partial, "Key") {
		t.Fatalf("partial Markdown disappeared while streaming: %q", partial)
	}
	committedCell := store.ordered[0]
	activeCell := store.ordered[1]

	active.Revision = 2
	active.Message.Text = "## Result\n\n| Key | Value |\n| --- | --- |\n| tests | pass |"
	active.Message.Complete = true
	active.Lifecycle = frontend.PresentationCompleted
	updated, err := reconcileSemanticCellStore(store, []frontend.PresentationItem{committed, active})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ordered[0] != committedCell || updated.ordered[1] == activeCell {
		t.Fatalf("stream reconciliation replaced wrong cells")
	}
	text, _ := renderSemanticCells(updated.cells(), context, cellRenderCompact)
	if strings.Count(text, "Committed context.") != 1 || strings.Count(text, "tests") != 1 ||
		!strings.Contains(text, "pass") {
		t.Fatalf("stream finalization duplicated or omitted Markdown:\n%s", text)
	}
}

func TestAssistantMarkdownBoundsDeepAndOversizedStructures(t *testing.T) {
	deep := markdownPresentationCell(
		frontend.PresentationFinalAnswer,
		strings.Repeat("> ", 512)+"deep payload",
		true,
	)
	deepDocument := deep.Render(
		cellRenderContext{Width: 40, ColorLevel: cellColorNone},
		cellRenderCompact,
	)
	if !strings.Contains(deepDocument.plainText(), "deep payload") {
		t.Fatalf("deep Markdown omitted its payload: %q", deepDocument.plainText())
	}
	assertMarkdownDocumentWidth(t, deepDocument, 40)

	const columns = 20
	headings := make([]string, 0, columns)
	separators := make([]string, 0, columns)
	values := make([]string, 0, columns)
	for column := range columns {
		headings = append(headings, "H"+strconv.Itoa(column))
		separators = append(separators, "---")
		values = append(values, "V"+strconv.Itoa(column))
	}
	table := "| " + strings.Join(headings, " | ") + " |\n" +
		"| " + strings.Join(separators, " | ") + " |\n" +
		"| " + strings.Join(values, " | ") + " |"
	tableDocument := markdownPresentationCell(
		frontend.PresentationFinalAnswer,
		table,
		true,
	).Render(cellRenderContext{Width: 40, ColorLevel: cellColorNone}, cellRenderCompact)
	plain := tableDocument.plainText()
	if !strings.Contains(plain, "H0: V0") || !strings.Contains(plain, "H19: V19") {
		t.Fatalf("oversized table did not use a complete bounded record projection:\n%s", plain)
	}
	assertMarkdownDocumentWidth(t, tableDocument, 40)
}

func TestAssistantMarkdownBoundsStackedTableHeaderAmplification(t *testing.T) {
	const rows = 200
	header := strings.Repeat("long-header-", 400)
	var source strings.Builder
	source.WriteString("| ")
	source.WriteString(header)
	source.WriteString(" |\n| --- |\n")
	for row := range rows {
		fmt.Fprintf(&source, "| value-%03d |\n", row)
	}
	markdownSource := source.String()
	document := markdownPresentationCell(
		frontend.PresentationFinalAnswer,
		markdownSource,
		true,
	).Render(cellRenderContext{Width: 7, ColorLevel: cellColorNone}, cellRenderCompact)
	plain := document.plainText()
	flattened := strings.ReplaceAll(plain, "\n", "")
	if !strings.Contains(plain, "Columns") || !strings.Contains(plain, "Row 1") ||
		!strings.Contains(flattened, "value-000") || !strings.Contains(flattened, "value-199") {
		t.Fatalf("bounded stacked table omitted semantic data")
	}
	if count := strings.Count(flattened, "long-header-"); count != 400 {
		t.Fatalf("long header repetitions = %d, want 400 from one complete legend", count)
	}
	if len(plain) > len(markdownSource)*4 || len(document.Lines) > len(markdownSource)*2 {
		t.Fatalf(
			"stacked table amplification source=%d rendered=%d lines=%d",
			len(markdownSource),
			len(plain),
			len(document.Lines),
		)
	}
	assertMarkdownDocumentWidth(t, document, 7)
}

func TestAssistantMarkdownSanitizesControlsAndUnsafeDestinations(t *testing.T) {
	source := "# Safe\x1b[31m\n\n" +
		"[bad](javascript:alert(1)) and [good](https://example.com).\n\n" +
		"<script>\x1b]52;c;Zm9yZ2Vk\aalert('x')</script>\u202e"
	cell := markdownPresentationCell(frontend.PresentationFinalAnswer, source, true)
	document := cell.Render(cellRenderContext{Width: 80, ColorLevel: cellColorNone}, cellRenderCompact)
	plain := document.plainText()
	for _, forbidden := range []string{"\x1b", "\a", "\u202e", "javascript:alert", "52;c;"} {
		if strings.Contains(plain, forbidden) {
			t.Fatalf("sanitized Markdown retained %q: %q", forbidden, plain)
		}
	}
	for _, want := range []string{"Safe", "bad", "good (https://example.com)", "<script>", "alert('x')"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("sanitized Markdown omits inert content %q: %q", want, plain)
		}
	}
}

func TestAssistantMarkdownRecoversFromMalformedTableParserPanic(t *testing.T) {
	const source = "-|-\n0| "
	document := markdownPresentationCell(
		frontend.PresentationFinalAnswer,
		source,
		true,
	).Render(cellRenderContext{Width: 40, ColorLevel: cellColorNone}, cellRenderCompact)
	if plain := document.plainText(); !strings.Contains(plain, "-|- 0|") {
		t.Fatalf("malformed table fallback = %q", plain)
	}
	assertMarkdownDocumentWidth(t, document, 40)
}

func TestOnlyAssistantMessagesUseMarkdownProjection(t *testing.T) {
	user := markdownPresentationCell(frontend.PresentationUserMessage, "**literal user syntax**", true)
	plain := user.Render(
		cellRenderContext{Width: 80, ColorLevel: cellColorNone},
		cellRenderCompact,
	).plainText()
	if !strings.Contains(plain, "**literal user syntax**") {
		t.Fatalf("user message was interpreted as Markdown: %q", plain)
	}

	commentary := markdownPresentationCell(frontend.PresentationAssistantMessage, "**Rendered** commentary.", true)
	commentaryPlain := commentary.Render(
		cellRenderContext{Width: 80, ColorLevel: cellColorNone},
		cellRenderCompact,
	).plainText()
	if commentaryPlain != "• Rendered commentary." {
		t.Fatalf("assistant commentary Markdown = %q", commentaryPlain)
	}
}

func FuzzAssistantMarkdownIsBoundedAndControlFree(f *testing.F) {
	for _, seed := range []string{
		"# Heading\n\n**bold** and `code`",
		"| A | B |\n| --- | --- |\n| 界 | 👩🏽‍💻 |",
		strings.Repeat("> - **nested**\n", 80),
		"```\n\x1b]8;;https://example.com\aunsafe\x1b]8;;\a\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		cell := markdownPresentationCell(frontend.PresentationFinalAnswer, source, true)
		for _, width := range []int{1, 8, 40, 120} {
			document := cell.Render(
				cellRenderContext{Width: width, Theme: cellThemeDark, ColorLevel: cellColorNone},
				cellRenderCompact,
			)
			assertMarkdownDocumentWidth(t, document, width)
			plain := document.plainText()
			if strings.ContainsAny(plain, "\x1b\a\r") {
				t.Fatalf("width %d Markdown leaked terminal control: %q", width, plain)
			}
		}
	})
}

func markdownPresentationCell(
	kind frontend.PresentationKind,
	text string,
	complete bool,
) *presentationCell {
	lifecycle := frontend.PresentationActive
	if complete {
		lifecycle = frontend.PresentationCompleted
	}
	entryKind := frontend.EntryAssistant
	phase := frontend.AssistantPhaseCommentary
	switch kind {
	case frontend.PresentationUserMessage:
		entryKind = frontend.EntryUser
		phase = ""
	case frontend.PresentationFinalAnswer:
		phase = frontend.AssistantPhaseFinal
	}
	return newPresentationCell(frontend.PresentationItem{
		ID: "message", TurnID: "turn-1", Sequence: 1, Revision: 1,
		Kind: kind, Lifecycle: lifecycle,
		Message: &frontend.TranscriptEntry{
			ID: "message", TurnID: "turn-1", Kind: entryKind, Phase: phase,
			Text: text, Complete: complete,
		},
	})
}

func markdownPresentationItem(
	id string,
	sequence, revision uint64,
	lifecycle frontend.PresentationLifecycle,
	text string,
	complete bool,
) frontend.PresentationItem {
	kind := frontend.PresentationAssistantMessage
	phase := frontend.AssistantPhaseCommentary
	if complete {
		kind = frontend.PresentationFinalAnswer
		phase = frontend.AssistantPhaseFinal
	}
	return frontend.PresentationItem{
		ID: id, TurnID: "turn-1", Sequence: sequence, Revision: revision,
		Kind: kind, Lifecycle: lifecycle,
		Message: &frontend.TranscriptEntry{
			ID: id, TurnID: "turn-1", Kind: frontend.EntryAssistant, Phase: phase,
			Text: text, Complete: complete,
		},
	}
}

func markdownDocumentHasRole(document cellDocument, role cellStyleRole) bool {
	for _, line := range document.Lines {
		for _, span := range line.Spans {
			if span.Role == role {
				return true
			}
		}
	}
	return false
}

func assertMarkdownDocumentWidth(t *testing.T, document cellDocument, width int) {
	t.Helper()
	for _, line := range document.Lines {
		if got := ansi.StringWidth(line.plainText()); got > width {
			t.Fatalf("Markdown line width %d > %d: %q", got, width, line.plainText())
		}
	}
}
