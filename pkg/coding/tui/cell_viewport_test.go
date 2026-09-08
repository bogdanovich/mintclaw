package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestPresentationCellCachesByRevisionWidthThemeColorAndMode(t *testing.T) {
	cell := newPresentationCell(semanticMessageItem(
		"assistant-1",
		1,
		7,
		frontend.PresentationCompleted,
		"cached response",
	))
	dark := cellRenderContext{Width: 80, Theme: cellThemeDark, ColorLevel: cellColorTrueColor}
	cell.Render(dark, cellRenderCompact)
	cell.Render(dark, cellRenderCompact)
	if misses := cell.renderMissCount(); misses != 1 {
		t.Fatalf("same render key misses = %d, want 1", misses)
	}
	cell.Render(cellRenderContext{Width: 40, Theme: cellThemeDark, ColorLevel: cellColorTrueColor}, cellRenderCompact)
	cell.Render(cellRenderContext{Width: 40, Theme: cellThemeLight, ColorLevel: cellColorTrueColor}, cellRenderCompact)
	cell.Render(cellRenderContext{Width: 40, Theme: cellThemeLight, ColorLevel: cellColorANSI256}, cellRenderCompact)
	cell.Render(cellRenderContext{Width: 40, Theme: cellThemeLight, ColorLevel: cellColorANSI256}, cellRenderFull)
	if misses := cell.renderMissCount(); misses != 5 {
		t.Fatalf("distinct render key misses = %d, want 5", misses)
	}

	revised := semanticMessageItem("assistant-1", 1, 8, frontend.PresentationCompleted, "revised response")
	store, err := newSemanticCellStore([]frontend.PresentationItem{cell.item})
	if err != nil {
		t.Fatal(err)
	}
	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{revised})
	if err != nil {
		t.Fatal(err)
	}
	if store.ordered[0] == cell || store.ordered[0].renderMissCount() != 0 {
		t.Fatalf("revision did not create an empty immutable cache: %+v", store.ordered[0])
	}
}

func TestSemanticViewportStylesNativePlanWithoutLosingPlainFallback(t *testing.T) {
	cell := newPresentationCell(frontend.PresentationItem{
		ID: "plan", TurnID: "turn", Sequence: 1, Revision: 1,
		Kind: frontend.PresentationPlanUpdate, Lifecycle: frontend.PresentationCompleted,
		Plan: &frontend.PlanState{
			Explanation: "Implement in order.",
			Steps: []frontend.PlanStepState{
				{Step: "Inspect", Status: frontend.PlanStepCompleted},
				{Step: "Implement", Status: frontend.PlanStepInProgress},
				{Step: "Verify", Status: frontend.PlanStepPending},
			},
		},
	})
	context := cellRenderContext{Width: 80, Theme: cellThemeDark, ColorLevel: cellColorTrueColor}
	styled := renderSemanticCellLines(semanticCellRenderSpec{cell: cell, mode: cellRenderCompact}, context)
	joined := strings.Join(styled, "\n")
	for _, sequence := range []string{"\x1b[1m", "\x1b[2;3m", "\x1b[2;9m", "\x1b[1;38;2;92;200;255m", "\x1b[2m"} {
		if !strings.Contains(joined, sequence) {
			t.Fatalf("styled plan omits %q: %q", sequence, joined)
		}
	}
	plain := renderSemanticCellLines(
		semanticCellRenderSpec{cell: cell, mode: cellRenderCompact},
		cellRenderContext{Width: 80, Theme: cellThemeDark, ColorLevel: cellColorNone},
	)
	plainText := strings.Join(plain, "\n")
	if strings.Contains(plainText, "\x1b") || ansi.Strip(joined) != plainText ||
		!strings.Contains(plainText, "  └ Implement in order.") ||
		!strings.Contains(plainText, "    ✔ Inspect") || !strings.Contains(plainText, "    □ Implement") {
		t.Fatalf("plan fallback = %q; styled = %q", plainText, joined)
	}
}

func TestNativePlanReflowsWithinTinyUnicodeWidths(t *testing.T) {
	cell := newPresentationCell(frontend.PresentationItem{
		ID: "plan", TurnID: "turn", Sequence: 1, Revision: 1,
		Kind: frontend.PresentationPlanUpdate, Lifecycle: frontend.PresentationCompleted,
		Plan: &frontend.PlanState{
			Explanation: "界 architecture",
			Steps:       []frontend.PlanStepState{{Step: "Verify 👩🏽‍💻 safely", Status: frontend.PlanStepInProgress}},
			Truncated:   true,
		},
	})
	for width := 1; width <= 12; width++ {
		document := cell.Render(
			cellRenderContext{Width: width, Theme: cellThemeLight, ColorLevel: cellColorNone},
			cellRenderCompact,
		)
		compact := strings.Map(func(value rune) rune {
			if unicode.IsSpace(value) {
				return -1
			}
			return value
		}, document.plainText())
		if !document.Truncated || !strings.Contains(compact, "[…truncated]") {
			t.Fatalf("width %d omitted truncation marker: %q", width, document.plainText())
		}
		for _, line := range document.Lines {
			if visible := ansi.StringWidth(line.plainText()); visible > width {
				t.Fatalf("width %d produced line width %d: %q", width, visible, line.plainText())
			}
		}
	}
}

func TestNativePlanPreservesStatusGlyphBeforeDecorativeIndentAtTinyWidths(t *testing.T) {
	cell := newPresentationCell(frontend.PresentationItem{
		ID: "plan", TurnID: "turn", Sequence: 1, Revision: 1,
		Kind: frontend.PresentationPlanUpdate, Lifecycle: frontend.PresentationCompleted,
		Plan: &frontend.PlanState{Steps: []frontend.PlanStepState{
			{Step: "Done", Status: frontend.PlanStepCompleted},
			{Step: "Later", Status: frontend.PlanStepPending},
		}},
	})
	for width := 1; width <= 6; width++ {
		document := cell.Render(
			cellRenderContext{Width: width, Theme: cellThemeDark, ColorLevel: cellColorNone},
			cellRenderCompact,
		)
		text := document.plainText()
		if !strings.Contains(text, "✔") || !strings.Contains(text, "□") {
			t.Fatalf("width %d dropped plan status glyph: %q", width, text)
		}
		for _, line := range document.Lines {
			if visible := ansi.StringWidth(line.plainText()); visible > width {
				t.Fatalf("width %d produced line width %d: %q", width, visible, line.plainText())
			}
		}
	}
}

func TestSemanticViewportRendersOnlyChangedActiveBlock(t *testing.T) {
	items := make([]frontend.PresentationItem, 0, 65)
	for index := range 64 {
		items = append(items, semanticMessageItem(
			fmt.Sprintf("committed-%d", index),
			uint64(index+1),
			1,
			frontend.PresentationCompleted,
			fmt.Sprintf("committed response %d", index),
		))
	}
	active := semanticMessageItem("active", 65, 1, frontend.PresentationActive, "working")
	items = append(items, active)
	store, err := newSemanticCellStore(items)
	if err != nil {
		t.Fatal(err)
	}
	context := cellRenderContext{Width: 80, Theme: cellThemeDark, ColorLevel: cellColorNone}
	document := reconcileSemanticViewportDocument(
		semanticViewportDocument{},
		semanticSpecs(store.cells()),
		context,
	)
	if document.renderedBlocks != len(items) || document.reusedBlocks != 0 {
		t.Fatalf("initial document work = rendered:%d reused:%d", document.renderedBlocks, document.reusedBlocks)
	}
	committed := append([]*presentationCell(nil), store.committed...)

	for revision := uint64(2); revision <= 100; revision++ {
		active.Revision = revision
		active.Message.Text = fmt.Sprintf("working revision %d", revision)
		store, err = reconcileSemanticCellStore(store, append(items[:len(items)-1], active))
		if err != nil {
			t.Fatal(err)
		}
		document = reconcileSemanticViewportDocument(document, semanticSpecs(store.cells()), context)
		if document.renderedBlocks != 1 || document.reusedBlocks != len(items)-1 {
			t.Fatalf(
				"revision %d document work = rendered:%d reused:%d",
				revision,
				document.renderedBlocks,
				document.reusedBlocks,
			)
		}
		for index, cell := range committed {
			if store.committed[index] != cell || cell.renderMissCount() != 1 {
				t.Fatalf("revision %d rerendered committed cell %d", revision, index)
			}
		}
	}
}

func TestSemanticViewportCompletionMovesExactlyOneActiveCellToCommitted(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	projector.AssistantAccumulated("turn-1", "working", false)
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	if len(model.cells.active) != 1 || len(model.cells.committed) != 1 {
		t.Fatalf("initial cells = active:%d committed:%d", len(model.cells.active), len(model.cells.committed))
	}
	activeID := model.cells.active[0].Identity().ID
	projector.AssistantAccumulated("turn-1", "done", true)
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := model.installSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(model.cells.active) != 0 || len(model.cells.committed) != 2 {
		t.Fatalf("completed cells = active:%d committed:%d", len(model.cells.active), len(model.cells.committed))
	}
	committedCount := 0
	for _, cell := range model.cells.committed {
		if cell.Identity().ID == activeID {
			committedCount++
		}
	}
	if committedCount != 1 || model.document.renderedBlocks != 1 || model.document.reusedBlocks != 1 {
		t.Fatalf(
			"completion = matching:%d rendered:%d reused:%d",
			committedCount,
			model.document.renderedBlocks,
			model.document.reusedBlocks,
		)
	}
}

func TestSemanticCellStoreFlushesMutableIndexAtShutdownWithoutRewritingLifecycle(t *testing.T) {
	items := []frontend.PresentationItem{
		semanticMessageItem("committed", 1, 1, frontend.PresentationCompleted, "done"),
		semanticMessageItem("active", 2, 3, frontend.PresentationActive, "working"),
		semanticToolItem("suspended", 3, 2, frontend.PresentationSuspended, frontend.ToolSuspended),
	}
	store, err := newSemanticCellStore(items)
	if err != nil {
		t.Fatal(err)
	}
	if flushed := store.flushActiveForShutdown(); flushed != 2 {
		t.Fatalf("flushed cells = %d, want 2", flushed)
	}
	if len(store.active) != 0 || len(store.committed) != len(store.ordered) {
		t.Fatalf("shutdown store = active:%d committed:%d", len(store.active), len(store.committed))
	}
	if lifecycle := store.byID["active"].Identity().Lifecycle; lifecycle != frontend.PresentationActive {
		t.Fatalf("shutdown rewrote authoritative lifecycle to %q", lifecycle)
	}
	if flushed := store.flushActiveForShutdown(); flushed != 0 {
		t.Fatalf("second shutdown flush = %d, want 0", flushed)
	}
}

func TestSemanticViewportResizePreservesCellAnchorAndComposer(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 12 {
		turnID := fmt.Sprintf("turn-%d", index)
		projector.TurnStarted(turnID, strings.Repeat(fmt.Sprintf("question-%d ", index), 5))
		projector.AssistantAccumulated(turnID, strings.Repeat("answer ", 8), true)
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(42, 12)
	model.composer.SetValue("unsubmitted resize draft")
	anchorBlock := model.layout.Blocks[len(model.layout.Blocks)/2]
	model.viewport.SetYOffset(anchorBlock.Start)
	anchor := model.layout.anchorAt(model.viewport.YOffset)

	model.resize(24, 9)
	if model.ComposerValue() != "unsubmitted resize draft" {
		t.Fatalf("resize changed composer to %q", model.ComposerValue())
	}
	if got := model.layout.anchorAt(model.viewport.YOffset); got.id != anchor.id || got.offset != anchor.offset {
		t.Fatalf("resize moved anchor from %+v to %+v", anchor, got)
	}
	if model.document.reusedBlocks != 0 {
		t.Fatalf("width change incorrectly reused %d old render blocks", model.document.reusedBlocks)
	}
}

func TestSemanticViewportResumeHydrationPreservesOrderAndComposer(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("live-turn", "current prompt")
	projector.AssistantAccumulated("live-turn", "current answer", true)
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.composer.SetValue("unsubmitted resume draft")
	live := model.Snapshot().Entries
	page := []frontend.TranscriptEntry{
		{
			ID:       "history-user",
			TurnID:   "history-turn",
			Kind:     frontend.EntryUser,
			Text:     "historical prompt",
			Complete: true,
		},
		{
			ID: "history-answer", TurnID: "history-turn", Kind: frontend.EntryAssistant,
			Text: "historical answer", Complete: true,
		},
	}
	page = append(page, live...)
	model = updateModel(t, model, TranscriptPageMsg{Page: frontend.TranscriptPage{
		Entries: page, Start: 0, End: len(page), Total: len(page),
	}, Mode: transcriptPageInitial})

	if model.ComposerValue() != "unsubmitted resume draft" {
		t.Fatalf("resume hydration changed composer to %q", model.ComposerValue())
	}
	transcript := model.document.text()
	ordered := []string{"historical prompt", "historical answer", "current prompt", "current answer"}
	previous := -1
	for _, value := range ordered {
		index := strings.Index(transcript, value)
		if index <= previous {
			t.Fatalf("resume order for %q after offset %d: %q", value, previous, transcript)
		}
		previous = index
	}
	for _, value := range []string{"current prompt", "current answer"} {
		if count := strings.Count(transcript, value); count != 1 {
			t.Fatalf("resume rendered %q %d times: %q", value, count, transcript)
		}
	}
}

func TestSemanticViewportSeparatorAnchorSurvivesStreamingAndResize(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 12 {
		turnID := fmt.Sprintf("turn-%d", index)
		projector.TurnStarted(turnID, strings.Repeat("question ", 4))
		projector.AssistantAccumulated(turnID, strings.Repeat("answer ", 4), true)
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(40, 10)
	target := model.layout.Blocks[len(model.layout.Blocks)/2]
	separator := target.Start - 1
	model.viewport.SetYOffset(separator)
	anchor := model.captureViewportPosition().anchor
	if !anchor.before || anchor.id != target.ID {
		t.Fatalf("separator anchor = %+v, target = %+v", anchor, target)
	}

	projector.TurnStarted("streaming", "later question")
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := model.installSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if model.viewport.YOffset != separator {
		t.Fatalf("streaming moved separator from %d to %d", separator, model.viewport.YOffset)
	}

	model.resize(24, 9)
	want, ok := model.layout.lineFor(anchor)
	if !ok || model.viewport.YOffset != want {
		t.Fatalf("resize separator offset = %d, want %d (found=%v)", model.viewport.YOffset, want, ok)
	}
	if got := model.layout.anchorAt(model.viewport.YOffset); !got.before || got.id != anchor.id {
		t.Fatalf("resize changed separator anchor from %+v to %+v", anchor, got)
	}
}

func TestSemanticViewportLongActiveOutputBoundsVisibleRendering(t *testing.T) {
	cell := newPresentationCell(semanticMessageItem(
		"active",
		1,
		1,
		frontend.PresentationActive,
		strings.Repeat("bounded output ", 4_000),
	))
	context := cellRenderContext{Width: 32, Theme: cellThemeDark, ColorLevel: cellColorNone}
	document := reconcileSemanticViewportDocument(
		semanticViewportDocument{},
		[]semanticCellRenderSpec{{cell: cell}},
		context,
	)
	view := newSemanticViewport(32, 8)
	view.setDocument(document)
	visible := view.View()
	if lines := strings.Count(visible, "\n") + 1; lines > view.Height {
		t.Fatalf("visible long output lines = %d, height = %d", lines, view.Height)
	}
	if len(visible) >= len(cell.item.Message.Text) {
		t.Fatalf("visible output was not bounded: visible=%d source=%d", len(visible), len(cell.item.Message.Text))
	}
}

func BenchmarkSemanticViewportActiveTailUpdate(b *testing.B) {
	items := make([]frontend.PresentationItem, 0, 129)
	for index := range 128 {
		items = append(items, semanticMessageItem(
			fmt.Sprintf("committed-%d", index),
			uint64(index+1),
			1,
			frontend.PresentationCompleted,
			strings.Repeat("committed evidence ", 8),
		))
	}
	active := semanticMessageItem("active", 129, 1, frontend.PresentationActive, "working")
	items = append(items, active)
	store, err := newSemanticCellStore(items)
	if err != nil {
		b.Fatal(err)
	}
	context := cellRenderContext{Width: 80, Theme: cellThemeDark, ColorLevel: cellColorNone}
	document := reconcileSemanticViewportDocument(
		semanticViewportDocument{},
		semanticSpecs(store.cells()),
		context,
	)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		active.Revision++
		active.Message.Text = fmt.Sprintf("working %d", iteration)
		store, err = reconcileSemanticCellStore(store, append(items[:len(items)-1], active))
		if err != nil {
			b.Fatal(err)
		}
		document = reconcileSemanticViewportDocument(document, semanticSpecs(store.cells()), context)
	}
}

func semanticSpecs(cells []semanticCell) []semanticCellRenderSpec {
	specs := make([]semanticCellRenderSpec, len(cells))
	for index, cell := range cells {
		specs[index] = semanticCellRenderSpec{cell: cell, mode: cellRenderCompact}
	}
	return specs
}
