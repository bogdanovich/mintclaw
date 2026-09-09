package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestSemanticCellStoreReconcilesStableActiveAndCommittedCells(t *testing.T) {
	active := semanticMessageItem("assistant-1", 1, 1, frontend.PresentationActive, "work")
	store, err := newSemanticCellStore([]frontend.PresentationItem{active})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.ordered) != 1 || len(store.active) != 1 || len(store.committed) != 0 {
		t.Fatalf("initial semantic store = %+v", store)
	}
	first := store.ordered[0]

	active.Revision = 2
	active.Message.Text = "working"
	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{active})
	if err != nil {
		t.Fatal(err)
	}
	if store.ordered[0] == first || first.item.Message.Text != "work" ||
		store.ordered[0].item.Message.Text != "working" {
		t.Fatalf("active cell replacement mutated prior state: first=%+v next=%+v", first.item, store.ordered[0].item)
	}

	active.Revision = 3
	active.Lifecycle = frontend.PresentationCompleted
	active.Message.Complete = true
	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{active})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.active) != 0 || len(store.committed) != 1 || store.committed[0] != store.ordered[0] {
		t.Fatalf("completed semantic store = %+v", store)
	}
	committed := store.ordered[0]

	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{active})
	if err != nil {
		t.Fatal(err)
	}
	if store.ordered[0] != committed {
		t.Fatal("unchanged ID/revision did not reuse the immutable semantic cell")
	}

	next := semanticToolItem("tool-1", 2, 1, frontend.PresentationActive, frontend.ToolRunning)
	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{active, next})
	if err != nil {
		t.Fatal(err)
	}
	identities := []cellIdentity{store.ordered[0].Identity(), store.ordered[1].Identity()}
	if len(store.committed) != 1 || len(store.active) != 1 || identities[0].Sequence != 1 ||
		identities[1].Sequence != 2 {
		t.Fatalf("ordered semantic store = %+v identities=%+v", store, identities)
	}
	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{next})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.ordered) != 1 || store.ordered[0].Identity().ID != "tool-1" {
		t.Fatalf("bounded snapshot did not prune absent cells: %+v", store)
	}
}

func TestSemanticCellStoreRejectsAmbiguousSnapshotsAtomically(t *testing.T) {
	item := semanticMessageItem("assistant-1", 1, 2, frontend.PresentationActive, "working")
	store, err := newSemanticCellStore([]frontend.PresentationItem{item})
	if err != nil {
		t.Fatal(err)
	}
	original := store.ordered[0]

	tests := []struct {
		name  string
		items []frontend.PresentationItem
	}{
		{
			name: "duplicate ID",
			items: []frontend.PresentationItem{
				item,
				semanticToolItem("assistant-1", 2, 1, frontend.PresentationActive, frontend.ToolRunning),
			},
		},
		{
			name: "unordered sequence",
			items: []frontend.PresentationItem{
				semanticToolItem("tool-2", 2, 1, frontend.PresentationCompleted, frontend.ToolSucceeded),
				semanticToolItem("tool-1", 1, 1, frontend.PresentationCompleted, frontend.ToolSucceeded),
			},
		},
		{
			name: "ambiguous payload",
			items: []frontend.PresentationItem{{
				ID: "ambiguous", Sequence: 1, Revision: 1, Kind: frontend.PresentationToolCall,
				Lifecycle: frontend.PresentationCompleted,
				Message:   &frontend.TranscriptEntry{Text: "claim"},
				Tool:      &frontend.ToolState{Name: "exec", Status: frontend.ToolSucceeded},
			}},
		},
		{
			name: "final kind without final phase",
			items: []frontend.PresentationItem{{
				ID: "final", TurnID: "turn-1", Sequence: 1, Revision: 1,
				Kind: frontend.PresentationFinalAnswer, Lifecycle: frontend.PresentationCompleted,
				Message: &frontend.TranscriptEntry{
					ID: "final", TurnID: "turn-1", Kind: frontend.EntryAssistant,
					Phase: frontend.AssistantPhaseCommentary, Text: "still working", Complete: true,
				},
			}},
		},
		{
			name: "compaction lifecycle mismatch",
			items: []frontend.PresentationItem{{
				ID: "compaction", TurnID: "turn-1", Sequence: 1, Revision: 1,
				Kind: frontend.PresentationCompaction, Lifecycle: frontend.PresentationCompleted,
				Compaction: &frontend.CompactionState{
					AttemptID: "attempt-1", Status: frontend.CompactionRunning,
				},
			}},
		},
		{
			name: "turn lifecycle mismatch",
			items: []frontend.PresentationItem{{
				ID: "boundary", TurnID: "turn-1", Sequence: 1, Revision: 1,
				Kind: frontend.PresentationTurnSeparator, Lifecycle: frontend.PresentationCompleted,
				Turn: &frontend.TurnBoundaryState{Outcome: frontend.TurnOutcomeInterrupted},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, reconcileErr := reconcileSemanticCellStore(store, tt.items)
			if reconcileErr == nil {
				t.Fatalf("reconcile succeeded with store %+v", next)
			}
			if store.ordered[0] != original || store.ordered[0].item.Message.Text != "working" {
				t.Fatalf("failed reconcile mutated original store: %+v", store)
			}
		})
	}
}

func TestSemanticCellStoreRebuildsEqualRevisionWithDifferentAuthoritativeContent(t *testing.T) {
	item := semanticMessageItem("assistant-1", 1, 3, frontend.PresentationActive, "canceled value")
	store, err := newSemanticCellStore([]frontend.PresentationItem{item})
	if err != nil {
		t.Fatal(err)
	}
	previous := store.ordered[0]
	item.Message.Text = "fallback value"
	store, err = reconcileSemanticCellStore(store, []frontend.PresentationItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if store.ordered[0] == previous || store.ordered[0].item.Message.Text != "fallback value" ||
		previous.item.Message.Text != "canceled value" {
		t.Fatalf("authoritative replacement failed: previous=%+v next=%+v", previous.item, store.ordered[0].item)
	}
}

func TestSemanticCellOwnsTypedPayloadAndSanitizesEveryRenderMode(t *testing.T) {
	item := semanticToolItem("tool-1", 1, 1, frontend.PresentationFailed, frontend.ToolFailed)
	item.Tool.Name = "exec\x1b[31m"
	item.Tool.Arguments = "SECRET-ARGUMENT\x07"
	item.Tool.Command = &frontend.CommandState{
		Status: frontend.CommandFailed, Stderr: "failure\x1b[0m", Truncated: true,
	}
	cell := newPresentationCell(item)
	item.Tool.Command.Stderr = "mutated"

	for _, mode := range []cellRenderMode{cellRenderCompact, cellRenderFull, cellRenderPlain} {
		document := cell.Render(cellRenderContext{
			Width: 16, Theme: cellThemeDark, ColorLevel: cellColorTrueColor,
		}, mode)
		text := document.plainText()
		if strings.Contains(text, "\x1b") || strings.Contains(text, "\x07") || strings.Contains(text, "mutated") ||
			strings.Contains(text, "SECRET") {
			t.Fatalf("mode %d leaked control or aliased content: %q", mode, text)
		}
		for _, line := range document.Lines {
			if width := visibleCellWidth(line.plainText()); width > 16 {
				t.Fatalf("mode %d line width %d > 16: %q", mode, width, line.plainText())
			}
		}
		if !strings.Contains(text, "failure") {
			t.Fatalf("mode %d omitted full command evidence: %q", mode, text)
		}
		if mode == cellRenderCompact && !strings.Contains(text, "ctrl+t") {
			t.Fatalf("compact command omitted transcript route: %q", text)
		}
		if mode == cellRenderPlain {
			for _, line := range document.Lines {
				for _, span := range line.Spans {
					if span.Role != cellStyleDefault {
						t.Fatalf("plain mode retained role %d in %+v", span.Role, document)
					}
				}
			}
		}
	}
}

func TestTurnBoundaryAndFinalAnswerHaveDistinctRendering(t *testing.T) {
	boundary := newPresentationCell(frontend.PresentationItem{
		ID: "boundary", TurnID: "turn-1", Sequence: 1, Revision: 1,
		Kind: frontend.PresentationTurnSeparator, Lifecycle: frontend.PresentationCompleted,
		Duration: 4*time.Minute + 18*time.Second,
		Turn:     &frontend.TurnBoundaryState{Outcome: frontend.TurnOutcomeCompleted},
	})
	final := newPresentationCell(frontend.PresentationItem{
		ID: "final", TurnID: "turn-1", Sequence: 2, Revision: 1,
		Kind: frontend.PresentationFinalAnswer, Lifecycle: frontend.PresentationCompleted,
		Message: &frontend.TranscriptEntry{
			ID: "final", TurnID: "turn-1", Kind: frontend.EntryAssistant,
			Phase: frontend.AssistantPhaseFinal, Text: "The fix is complete.", Complete: true,
		},
	})
	context := cellRenderContext{Width: 80, Theme: cellThemeDark, ColorLevel: cellColorNone}
	boundaryText := boundary.Render(context, cellRenderCompact).plainText()
	finalText := final.Render(context, cellRenderCompact).plainText()
	if !strings.Contains(boundaryText, "Worked for 4m 18s") ||
		!strings.HasPrefix(boundaryText, strings.Repeat("─", 72)) {
		t.Fatalf("boundary render = %q", boundaryText)
	}
	if finalText != "The fix is complete." || strings.HasPrefix(finalText, "•") {
		t.Fatalf("final render = %q", finalText)
	}

	boundary.item.Duration = time.Minute
	short := newPresentationCell(boundary.item).Render(context, cellRenderCompact).plainText()
	if strings.Contains(short, "Worked for") || strings.Contains(short, "Work completed") {
		t.Fatalf("short boundary exposed noisy duration: %q", short)
	}
	boundary.item.Duration = 10 * time.Second
	boundary.item.Turn.Outcome = frontend.TurnOutcomeInterrupted
	interrupted := newPresentationCell(boundary.item).Render(context, cellRenderCompact).plainText()
	if !strings.Contains(interrupted, "Work interrupted") || strings.Contains(interrupted, "after") {
		t.Fatalf("interrupted boundary render = %q", interrupted)
	}
}

func TestCompactionCellRendersLifecycleWithoutGenericToolCard(t *testing.T) {
	item := frontend.PresentationItem{
		ID: "compaction", TurnID: "turn-1", Sequence: 1, Revision: 2,
		Kind: frontend.PresentationCompaction, Lifecycle: frontend.PresentationCompleted,
		Duration: 2100 * time.Millisecond,
		Compaction: &frontend.CompactionState{
			AttemptID: "attempt-1", Status: frontend.CompactionCompleted,
			Reason: "llm_retry", Duration: 2100 * time.Millisecond,
			TokensBefore: 8000, TokensAfter: 3000, TokensSaved: 5000,
			TokenCountsObserved: true, SummariesCreated: 3, LeafSummaries: 2, CondensedSummaries: 1,
		},
	}
	cell := newPresentationCell(item)
	context := cellRenderContext{Width: 80, Theme: cellThemeLight, ColorLevel: cellColorNone}
	compact := cell.Render(context, cellRenderCompact).plainText()
	full := cell.Render(context, cellRenderFull).plainText()
	if !strings.Contains(compact, "Context compacted · 2.1s") ||
		!strings.Contains(compact, "8.0k → 3.0k tokens · 5.0k saved") ||
		strings.Contains(compact, "Tool compact") {
		t.Fatalf("compact compaction = %q", compact)
	}
	if !strings.Contains(full, "trigger: context overflow retry") ||
		!strings.Contains(full, "summaries: 3 total (2 leaf, 1 condensed)") {
		t.Fatalf("full compaction = %q", full)
	}

	item.Revision++
	item.Lifecycle = frontend.PresentationFailed
	item.Compaction.Status = frontend.CompactionFailed
	failed := newPresentationCell(item).Render(context, cellRenderCompact).plainText()
	if !strings.Contains(failed, "Context compaction failed") || strings.Contains(failed, "Context compacted") {
		t.Fatalf("failed compaction = %q", failed)
	}
}

func TestHydratedTranscriptReconstructsStableWorkBoundary(t *testing.T) {
	started := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	completed := started.Add(4*time.Minute + 18*time.Second)
	entries := []frontend.TranscriptEntry{
		{ID: "user", Kind: frontend.EntryUser, Text: "fix it", RootTurnStart: true, OccurredAt: started},
		{
			ID: "tool", Kind: frontend.EntryTool, ConcreteWork: true, EvidenceOnly: true,
			OccurredAt: started.Add(time.Minute),
		},
		{
			ID: "final", Kind: frontend.EntryAssistant, Phase: frontend.AssistantPhaseFinal,
			Text: "fixed", OccurredAt: completed,
		},
	}
	first, err := newHydratedSemanticCellStore(entries)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newHydratedSemanticCellStore(entries)
	if err != nil {
		t.Fatal(err)
	}
	firstItems := semanticStoreItems(first)
	if !reflect.DeepEqual(firstItems, semanticStoreItems(second)) || len(firstItems) != 3 ||
		firstItems[1].Kind != frontend.PresentationTurnSeparator ||
		firstItems[1].ID != "hydrated-turn-boundary:final" ||
		firstItems[1].Duration != 4*time.Minute+18*time.Second ||
		firstItems[2].Kind != frontend.PresentationFinalAnswer {
		t.Fatalf("hydrated work presentation = %+v", firstItems)
	}
	partial, err := newHydratedSemanticCellStore(entries[1:])
	if err != nil {
		t.Fatal(err)
	}
	partialItems := semanticStoreItems(partial)
	if len(partialItems) != 2 || partialItems[0].ID != firstItems[1].ID ||
		partialItems[0].Duration == firstItems[1].Duration {
		t.Fatalf("partial hydration boundary = %+v", partialItems)
	}
	window := transcriptWindow{historical: entries}
	if visible := window.entries(nil); len(visible) != 2 || visible[0].ID != "user" || visible[1].ID != "final" {
		t.Fatalf("evidence-only marker leaked through transcript entries = %+v", visible)
	}

	chat, err := newHydratedSemanticCellStore([]frontend.TranscriptEntry{
		{ID: "chat-user", Kind: frontend.EntryUser, Text: "hello", RootTurnStart: true, OccurredAt: started},
		{
			ID: "chat-final", Kind: frontend.EntryAssistant, Phase: frontend.AssistantPhaseFinal,
			Text: "hello", OccurredAt: completed,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if items := semanticStoreItems(chat); len(items) != 2 || items[1].Kind != frontend.PresentationFinalAnswer {
		t.Fatalf("hydrated chat-only presentation = %+v", items)
	}
}

func TestModelRendersAuthoritativeSemanticCells(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	before := renderedModelTranscript(model, 80)
	if len(model.cells.ordered) != 1 || model.cells.ordered[0].Identity().Kind != frontend.PresentationUserMessage {
		t.Fatalf("initial model semantic cells = %+v", model.cells)
	}

	projector.PlanUpdated("turn-1", "call-1", frontend.PlanState{Steps: []frontend.PlanStepState{{
		Step: "Inspect", Status: frontend.PlanStepInProgress,
	}}})
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := model.installSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(model.cells.ordered) != 2 || model.cells.ordered[1].Identity().Kind != frontend.PresentationPlanUpdate {
		t.Fatalf("updated model semantic cells = %+v", model.cells)
	}
	if after := renderedModelTranscript(model, 80); after == before ||
		!strings.Contains(after, "• Updated Plan") || !strings.Contains(after, "→ Inspect") {
		t.Fatalf("semantic plan cell was not visible:\nbefore: %q\n after: %q", before, after)
	}
}

func TestModelHidesOnlySuccessfulToolCardRepresentedByNativePlan(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	plan := frontend.PlanState{Steps: []frontend.PlanStepState{{
		Step: "Inspect", Status: frontend.PlanStepInProgress,
	}}}
	projector.ToolStarted("turn-1", "call-1", "update_plan", "")
	projector.ToolPlanObserved("turn-1", "call-1")
	projector.PlanUpdated("turn-1", "call-1", plan)
	projector.ToolCompleted("turn-1", "call-1", "update_plan", "", 0, false, nil)

	projector.ToolStarted("turn-2", "call-2", "update_plan", "")
	projector.ToolPlanObserved("turn-2", "call-2")
	projector.PlanUpdated("turn-2", "call-2", plan)
	projector.ToolCompleted("turn-2", "call-2", "update_plan", "", 0, false, nil)

	projector.ToolStarted("turn-3", "call-3", "update_plan", "")
	projector.ToolCompleted("turn-3", "call-3", "update_plan", "invalid plan", 0, true, nil)
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := model.installSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	rendered := renderedModelTranscript(model, 80)
	if strings.Count(rendered, "Updated Plan") != 1 ||
		strings.Contains(rendered, "Tool update_plan [succeeded]") ||
		!strings.Contains(rendered, "Tool update_plan [failed]") {
		t.Fatalf("native plan/tool visibility = %q", rendered)
	}
}

func TestModelSemanticCellsAcceptProjectorStreamRollback(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model = startModelSubscription(t, model)
	streamer, ok := frontend.NewStreamDelegate(projector, "thread-1").GetStreamer(
		t.Context(),
		"coding",
		"thread-1",
		"thread-1",
		"",
		runtimeevents.NewTraceScope("/repo", "turn-1"),
	)
	if !ok {
		t.Fatal("matching stream was rejected")
	}
	if err := streamer.Update(t.Context(), "first provisional value"); err != nil {
		t.Fatal(err)
	}
	projector.AssistantAccumulated("turn-1", "committed value", false)
	if err := streamer.Update(t.Context(), "second provisional value"); err != nil {
		t.Fatal(err)
	}
	provisional, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := model.installSnapshot(provisional); err != nil {
		t.Fatal(err)
	}
	provisionalRevision := model.cells.ordered[0].Identity().Revision

	streamer.Cancel(t.Context())
	rolledBack, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	updated, command := model.Update(SnapshotMsg{Snapshot: rolledBack})
	model, ok = updated.(*Model)
	if !ok {
		t.Fatalf("updated model = %T", updated)
	}
	if model.err != nil || command == nil {
		t.Fatalf("rollback update error=%v next-subscription=%v", model.err, command)
	}
	if len(model.cells.ordered) != 1 || model.cells.ordered[0].Identity().Revision >= provisionalRevision ||
		model.cells.ordered[0].item.Message.Text != "committed value" {
		t.Fatalf("rollback semantic cells = %+v, provisional revision=%d", model.cells, provisionalRevision)
	}
}

func TestModelSemanticCellsAcceptCoalescedStreamRemovalAndFallback(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model = startModelSubscription(t, model)
	newStream := func() bus.Streamer {
		streamer, accepted := frontend.NewStreamDelegate(projector, "thread-1").GetStreamer(
			t.Context(),
			"coding",
			"thread-1",
			"thread-1",
			"",
			runtimeevents.NewTraceScope("/repo", "turn-1"),
		)
		if !accepted {
			t.Fatal("matching stream was rejected")
		}
		return streamer
	}
	canceled := newStream()
	for _, value := range []string{"canceled one", "canceled two", "canceled value"} {
		if err := canceled.Update(t.Context(), value); err != nil {
			t.Fatal(err)
		}
	}
	canceledSnapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := model.installSnapshot(canceledSnapshot); err != nil {
		t.Fatal(err)
	}
	previous := model.cells.ordered[0]
	canceled.Cancel(t.Context())

	fallback := newStream()
	for _, value := range []string{"fallback one", "fallback two", "fallback value"} {
		if err := fallback.Update(t.Context(), value); err != nil {
			t.Fatal(err)
		}
	}
	fallbackSnapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	updated, command := model.Update(SnapshotMsg{Snapshot: fallbackSnapshot})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("updated model = %T", updated)
	}
	if model.err != nil || command == nil {
		t.Fatalf("coalesced fallback error=%v next-subscription=%v", model.err, command)
	}
	if len(model.cells.ordered) != 1 || model.cells.ordered[0] == previous ||
		model.cells.ordered[0].Identity().Sequence == previous.Identity().Sequence ||
		model.cells.ordered[0].Identity().Revision != previous.Identity().Revision ||
		model.cells.ordered[0].item.Message.Text != "fallback value" {
		t.Fatalf("coalesced fallback cells=%+v previous=%+v", model.cells, previous.item)
	}
	fallback.Cancel(t.Context())
}

func TestCellRenderContextValidation(t *testing.T) {
	valid := []cellRenderContext{
		{Width: 40, Theme: cellThemeDark, ColorLevel: cellColorTrueColor},
		{Width: 80, Theme: cellThemeLight, ColorLevel: cellColorANSI256},
		{Width: 120, Theme: cellThemeDark, ColorLevel: cellColorANSI16},
		{Width: 40, Theme: cellThemeLight, ColorLevel: cellColorNone},
	}
	for _, context := range valid {
		if err := validateCellRenderContext(context); err != nil {
			t.Fatalf("valid context %+v: %v", context, err)
		}
	}
	for _, context := range []cellRenderContext{
		{Width: 0, Theme: cellThemeDark, ColorLevel: cellColorNone},
		{Width: 40, Theme: cellThemeUnknown, ColorLevel: cellColorNone},
		{Width: 40, Theme: cellThemeDark, ColorLevel: cellColorLevel(99)},
	} {
		if err := validateCellRenderContext(context); err == nil {
			t.Fatalf("invalid context accepted: %+v", context)
		}
	}
}

func TestSemanticCellSanitizerPreservesOrdinaryPunctuation(t *testing.T) {
	const value = "fields: command, yield-time-ms"
	if sanitized := sanitizeTerminalText(value); sanitized != value {
		t.Fatalf("sanitized punctuation = %q (%x), want %q (%x)", sanitized, []byte(sanitized), value, []byte(value))
	}
	document := wrapCellDocument(cellDocument{Lines: logicalCellLines("  "+value, cellStyleMuted)}, 120)
	if wrapped := document.plainText(); wrapped != "  "+value {
		t.Fatalf(
			"wrapped punctuation = %q (%x), want %q (%x)",
			wrapped,
			[]byte(wrapped),
			"  "+value,
			[]byte("  "+value),
		)
	}
}

func TestSemanticCellWrappingBoundsIndentAndTruncationAtNarrowWidths(t *testing.T) {
	document := cellDocument{
		Lines:     logicalCellLines("\t界👩🏽‍💻 evidence", cellStyleDefault),
		Truncated: true,
	}
	for width := 1; width <= 12; width++ {
		wrapped := wrapCellDocument(document, width)
		if !wrapped.Truncated {
			t.Fatalf("width %d lost truncation state", width)
		}
		for _, line := range wrapped.Lines {
			if strings.Contains(line.plainText(), "\t") {
				t.Fatalf("width %d retained a terminal-dependent tab: %q", width, line.plainText())
			}
			if visible := visibleCellWidth(line.plainText()); visible > width {
				t.Fatalf("width %d produced line width %d: %q", width, visible, line.plainText())
			}
		}
	}
}

func TestSemanticCellFullEvidencePreservesSignificantWhitespace(t *testing.T) {
	command := semanticToolItem("command", 1, 1, frontend.PresentationCompleted, frontend.ToolSucceeded)
	command.Tool.Command = &frontend.CommandState{
		Status: frontend.CommandSucceeded,
		Stdout: "  indented  \n\n",
	}
	generic := semanticToolItem("generic", 2, 1, frontend.PresentationCompleted, frontend.ToolSucceeded)
	generic.Tool.Name = "inspect"
	generic.Tool.Output = "  generic  \n\n"

	for _, test := range []struct {
		name string
		cell *presentationCell
		want string
	}{
		{
			name: "typed command",
			cell: newPresentationCell(command),
			want: "• Ran exec\n    stdout>   indented  \n    \n    \n  succeeded",
		},
		{
			name: "generic tool",
			cell: newPresentationCell(generic),
			want: "• Tool inspect [succeeded]\n  output:\n      generic  \n    \n    ",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, mode := range []cellRenderMode{cellRenderFull, cellRenderPlain} {
				got := test.cell.Render(cellRenderContext{Width: 80}, mode).plainText()
				if got != test.want {
					t.Fatalf("mode %d evidence = %q, want %q", mode, got, test.want)
				}
			}
		})
	}
}

func TestSemanticCellLayoutDoesNotInventLinesForEmptyCells(t *testing.T) {
	empty := newPresentationCell(semanticMessageItem(
		"empty",
		1,
		1,
		frontend.PresentationCompleted,
		"",
	))
	visible := newPresentationCell(semanticMessageItem(
		"visible",
		2,
		1,
		frontend.PresentationCompleted,
		"done",
	))
	rendered, layout := renderSemanticCells(
		[]semanticCell{empty, visible},
		cellRenderContext{Width: 80},
		cellRenderCompact,
	)
	if rendered != "• done" {
		t.Fatalf("rendered semantic cells = %q", rendered)
	}
	want := cellLayout{Blocks: []cellLayoutBlock{
		{ID: "empty", Start: 0, End: 0},
		{ID: "visible", Start: 0, End: 1},
	}}
	if !reflect.DeepEqual(layout, want) {
		t.Fatalf("layout = %+v, want %+v", layout, want)
	}
}

func TestSemanticCellRolesPreservePlanAndVerifiedWriteMeaning(t *testing.T) {
	plan := newPresentationCell(frontend.PresentationItem{
		ID: "plan-1", TurnID: "turn-1", Sequence: 1, Revision: 1,
		Kind: frontend.PresentationPlanUpdate, Lifecycle: frontend.PresentationCompleted,
		Plan: &frontend.PlanState{Steps: []frontend.PlanStepState{
			{Step: "done", Status: frontend.PlanStepCompleted},
			{Step: "current", Status: frontend.PlanStepInProgress},
			{Step: "later", Status: frontend.PlanStepPending},
		}},
	}).Render(cellRenderContext{Width: 80}, cellRenderCompact)
	roles := []cellStyleRole{
		plan.Lines[1].Spans[0].Role,
		plan.Lines[2].Spans[0].Role,
		plan.Lines[3].Spans[0].Role,
	}
	if !reflect.DeepEqual(roles, []cellStyleRole{
		cellStylePlanCompleted,
		cellStylePlanCurrent,
		cellStylePlanPending,
	}) {
		t.Fatalf("plan roles = %v", roles)
	}

	writes := newPresentationCell(frontend.PresentationItem{
		ID:        "writes-1",
		TurnID:    "turn-1",
		Sequence:  2,
		Revision:  1,
		Kind:      frontend.PresentationToolCall,
		Lifecycle: frontend.PresentationCompleted,
		Tool: &frontend.ToolState{
			Name:   "apply_patch",
			Status: frontend.ToolSucceeded,
			WriteAudit: []frontend.WriteAudit{
				{Target: "created.go", Action: "create", Success: true},
				{Target: "deleted.go", Action: "delete", Success: true},
				{Target: "failed.go", Action: "update", Success: false},
			},
		},
	}).Render(cellRenderContext{Width: 80}, cellRenderCompact)
	roles = []cellStyleRole{
		writes.Lines[1].Spans[0].Role,
		writes.Lines[2].Spans[0].Role,
		writes.Lines[3].Spans[0].Role,
	}
	if !reflect.DeepEqual(roles, []cellStyleRole{cellStyleInsertion, cellStyleDeletion, cellStyleFailure}) {
		t.Fatalf("write roles = %v", roles)
	}
}

func semanticMessageItem(
	id string,
	sequence uint64,
	revision uint64,
	lifecycle frontend.PresentationLifecycle,
	text string,
) frontend.PresentationItem {
	return frontend.PresentationItem{
		ID: id, TurnID: "turn-1", Sequence: sequence, Revision: revision,
		Kind: frontend.PresentationAssistantMessage, Lifecycle: lifecycle,
		Message: &frontend.TranscriptEntry{
			ID: id, TurnID: "turn-1", Kind: frontend.EntryAssistant, Text: text,
			Complete: presentationCellCommitted(lifecycle),
		},
	}
}

func semanticToolItem(
	id string,
	sequence uint64,
	revision uint64,
	lifecycle frontend.PresentationLifecycle,
	status frontend.ToolStatus,
) frontend.PresentationItem {
	return frontend.PresentationItem{
		ID: id, TurnID: "turn-1", Sequence: sequence, Revision: revision,
		Kind: frontend.PresentationToolCall, Lifecycle: lifecycle,
		Tool: &frontend.ToolState{
			TurnID: "turn-1", CallID: id, Name: "exec", Status: status,
		},
	}
}

func visibleCellWidth(value string) int {
	// All renderer output is ANSI-free by contract; rune-aware ANSI width also
	// handles CJK, emoji, and combining characters used by later fixtures.
	return ansi.StringWidth(value)
}
