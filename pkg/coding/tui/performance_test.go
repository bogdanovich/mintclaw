package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const syntheticPresentationSessionMinutes = 4 * 60

func TestFirstPaintIncludesFirstCompleteView(t *testing.T) {
	started := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	clock := &diagnosticTestClock{now: started}
	model, err := newModel(t.Context(), newController(t), modelOptions{diagnosticNow: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics := model.Diagnostics(); diagnostics.FirstPaint != 0 {
		t.Fatalf("first paint before View = %s, want zero", diagnostics.FirstPaint)
	}

	clock.Advance(137 * time.Millisecond)
	_ = model.View()
	if diagnostics := model.Diagnostics(); diagnostics.FirstPaint != 137*time.Millisecond {
		t.Fatalf("first paint after View = %s, want 137ms", diagnostics.FirstPaint)
	}

	clock.Advance(time.Second)
	_ = model.View()
	if diagnostics := model.Diagnostics(); diagnostics.FirstPaint != 137*time.Millisecond {
		t.Fatalf("first paint after subsequent View = %s, want the original 137ms", diagnostics.FirstPaint)
	}
}

type diagnosticTestClock struct {
	now time.Time
}

func (clock *diagnosticTestClock) Now() time.Time {
	return clock.now
}

func (clock *diagnosticTestClock) Advance(duration time.Duration) {
	clock.now = clock.now.Add(duration)
}

func TestFourHourPresentationSessionRemainsStructurallyBounded(t *testing.T) {
	projector, err := frontend.NewProjector("long-session", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	controller := &fakeController{Projector: projector}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}

	history := benchmarkTranscriptEntries(300, 256)
	updated, _ := model.Update(TranscriptPageMsg{Page: frontend.TranscriptPage{
		Entries: history,
		Start:   0,
		End:     len(history),
		Total:   len(history),
	}, Mode: transcriptPageInitial})
	model = updated.(*Model)
	if entries := len(model.transcript.historical); entries != maxHydratedTranscriptEntries {
		t.Fatalf("initial hydrated entries = %d, want %d", entries, maxHydratedTranscriptEntries)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, updates, err := projector.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	widths := []int{40, 80, 120}
	for minute := range syntheticPresentationSessionMinutes {
		turnID := fmt.Sprintf("minute-%03d", minute)
		projector.TurnStarted(turnID, fmt.Sprintf("inspect logical minute %d", minute))
		for revision := range 10 {
			projector.AssistantAccumulated(
				turnID,
				fmt.Sprintf("stream revision %d %s", revision, strings.Repeat("bounded output ", 24)),
				false,
			)
		}
		callID := "command-" + turnID
		projector.ToolStarted(turnID, callID, "exec_command", `{"cmd":"go test ./..."}`)
		output := "tests passed"
		if minute%60 == 0 {
			output = strings.Repeat("bounded command output ", 4_000)
		}
		projector.ToolCompleted(turnID, callID, "exec_command", output, 0, false, nil)
		if minute%15 == 0 {
			projector.PlanUpdated(turnID, "plan-"+turnID, frontend.PlanState{
				Explanation: "Keep the synthetic session moving.",
				Steps: []frontend.PlanStepState{
					{Step: "Observe", Status: frontend.PlanStepCompleted},
					{Step: "Continue", Status: frontend.PlanStepInProgress},
				},
			})
		}
		projector.AssistantAccumulated(turnID, fmt.Sprintf("logical minute %d complete", minute), true)
		projector.TurnCompleted(turnID, "completed")

		snapshot := <-updates
		if err := model.installSnapshot(snapshot); err != nil {
			t.Fatalf("install logical minute %d: %v", minute, err)
		}
		model.resize(widths[minute%len(widths)], 24)
		if minute%60 == 0 {
			model.openTranscriptOverlay()
			model.transcriptOverlay.query = "complete"
			model.transcriptOverlay.recomputeMatches(model.transcriptOverlay.lines)
			_ = model.View()
			model.closeTranscriptOverlay()
		}
		assertPresentationCacheBounds(t, model)
	}

	snapshot := model.Snapshot()
	if messages := len(snapshot.Messages()); messages > 256 {
		t.Fatalf("live messages = %d, want at most 256", messages)
	}
	if tools := len(snapshot.ToolStates()); tools > 128 {
		t.Fatalf("live tools = %d, want at most 128", tools)
	}
	observations := 0
	for _, item := range snapshot.Items {
		if item.Plan != nil || item.Compaction != nil || item.Turn != nil {
			observations++
		}
	}
	if observations > 64 {
		t.Fatalf("live observations = %d, want at most 64", observations)
	}
	if !snapshot.HasOlderEntries {
		t.Fatal("long session did not report bounded older presentation history")
	}
	if blocks := len(model.document.blocks); blocks > len(snapshot.Items)+maxHydratedTranscriptEntries+3 {
		t.Fatalf("render blocks = %d, snapshot items = %d", blocks, len(snapshot.Items))
	}
	if len(model.transcriptOverlay.lines) != 0 || len(model.transcriptOverlay.matches) != 0 {
		t.Fatal("closed transcript overlay retained its derived line or match cache")
	}
	diagnostics := model.Diagnostics()
	if diagnostics.CoalescedUpdates == 0 || diagnostics.PeakHydratedEntries != maxHydratedTranscriptEntries {
		t.Fatalf("long-session diagnostics = %+v", diagnostics)
	}
	if diagnostics.TruncationObservations == 0 || diagnostics.PeakCells > 710 {
		t.Fatalf("long-session bounds diagnostics = %+v", diagnostics)
	}
}

func TestPresentationDiagnosticsCountCoalescingTruncationAndHydrationFailure(t *testing.T) {
	projector, err := frontend.NewProjector("diagnostics", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "question")
	projector.AssistantAccumulated("turn-1", "a", false)
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}

	projector.AssistantAccumulated("turn-1", "ab", false)
	projector.AssistantAccumulated("turn-1", "abc", false)
	projector.AssistantAccumulated("turn-1", "abcd", false)
	projector.ToolStarted("turn-1", "tool-1", "exec_command", "shape-only")
	projector.ToolCompleted(
		"turn-1",
		"tool-1",
		"exec_command",
		strings.Repeat("bounded ", 20_000),
		0,
		false,
		nil,
	)
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := model.installSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	updated, _ := model.Update(TranscriptPageMsg{Err: frontend.ErrTranscriptHistoryChanged})
	model = updated.(*Model)

	diagnostics := model.Diagnostics()
	if diagnostics.CoalescedUpdates != 3 {
		t.Fatalf("coalesced updates = %d, want 3", diagnostics.CoalescedUpdates)
	}
	if diagnostics.CurrentTruncatedSurfaces != 1 || diagnostics.TruncationObservations == 0 {
		t.Fatalf("truncation diagnostics = %+v", diagnostics)
	}
	if diagnostics.HydrationResults != 1 || diagnostics.HydrationFailures != 1 {
		t.Fatalf("hydration diagnostics = %+v", diagnostics)
	}
	if diagnostics.RenderPasses < 2 || diagnostics.PresentationLatencySamples < 2 {
		t.Fatalf("presentation diagnostics = %+v", diagnostics)
	}
}

func TestCellRenderCachesAndClosedOverlayRemainBounded(t *testing.T) {
	cell := newPresentationCell(semanticMessageItem(
		"bounded-cache",
		1,
		1,
		frontend.PresentationCompleted,
		strings.Repeat("render me ", 32),
	))
	for width := 20; width < 220; width++ {
		cell.Render(
			cellRenderContext{Width: width, Theme: cellThemeDark, ColorLevel: cellColorANSI256},
			cellRenderCompact,
		)
		if entries := len(cell.renderCache); entries > maxCellRenderCacheEntries {
			t.Fatalf("render cache entries at width %d = %d", width, entries)
		}
	}

	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	model.openTranscriptOverlay()
	if len(model.transcriptOverlay.lines) == 0 {
		t.Fatal("open transcript overlay has no cached lines")
	}
	first := &model.transcriptOverlay.lines[0]
	_ = model.View()
	if first != &model.transcriptOverlay.lines[0] {
		t.Fatal("unchanged overlay view rebuilt its complete line cache")
	}
	model.closeTranscriptOverlay()
	if model.transcriptOverlay.lines != nil || model.transcriptOverlay.matches != nil {
		t.Fatal("closed overlay retained derived caches")
	}
}

func BenchmarkPresentationFirstPaint(b *testing.B) {
	controller := benchmarkPresentationController(b, 128)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		model, err := NewModel(context.Background(), controller)
		if err != nil {
			b.Fatal(err)
		}
		_ = model.View()
	}
}

func BenchmarkPresentationOutputUpdate(b *testing.B) {
	controller := benchmarkPresentationController(b, 64)
	projector := controller.Projector
	projector.TurnStarted("active-turn", "continue")
	projector.AssistantAccumulated("active-turn", "working", false)
	model, err := NewModel(context.Background(), controller)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		projector.AssistantAccumulated("active-turn", fmt.Sprintf("working revision %d", iteration), false)
		snapshot, snapshotErr := projector.Snapshot(context.Background())
		if snapshotErr != nil {
			b.Fatal(snapshotErr)
		}
		if installErr := model.installSnapshot(snapshot); installErr != nil {
			b.Fatal(installErr)
		}
		_ = model.View()
	}
}

func BenchmarkPresentationResizeReflow(b *testing.B) {
	model, err := NewModel(context.Background(), benchmarkPresentationController(b, 128))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		model.resize(40+iteration%81, 24)
		_ = model.View()
	}
}

func BenchmarkTranscriptHydration(b *testing.B) {
	model, err := NewModel(context.Background(), benchmarkPresentationController(b, 8))
	if err != nil {
		b.Fatal(err)
	}
	entries := benchmarkTranscriptEntries(transcriptPageSize, 512)
	message := TranscriptPageMsg{Page: frontend.TranscriptPage{
		Entries: entries,
		Start:   0,
		End:     len(entries),
		Total:   len(entries),
	}, Mode: transcriptPageInitial}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		updated, _ := model.Update(message)
		model = updated.(*Model)
	}
}

func BenchmarkLongTranscriptViewport(b *testing.B) {
	model := benchmarkLongTranscriptModel(b)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		_ = model.View()
	}
}

func BenchmarkTranscriptOverlayOpen(b *testing.B) {
	model := benchmarkLongTranscriptModel(b)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		model.openTranscriptOverlay()
		_ = model.View()
		model.closeTranscriptOverlay()
	}
}

func BenchmarkTranscriptSearch(b *testing.B) {
	model := benchmarkLongTranscriptModel(b)
	model.openTranscriptOverlay()
	state := &model.transcriptOverlay
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		state.query = "needle"
		state.recomputeMatches(state.lines)
	}
}

func benchmarkPresentationController(tb testing.TB, turns int) *fakeController {
	tb.Helper()
	projector, err := frontend.NewProjector("benchmark", frontend.ProjectionLimits{})
	if err != nil {
		tb.Fatal(err)
	}
	for turn := range turns {
		turnID := fmt.Sprintf("turn-%03d", turn)
		projector.TurnStarted(turnID, strings.Repeat(fmt.Sprintf("question %d ", turn), 4))
		projector.AssistantAccumulated(turnID, strings.Repeat("bounded answer ", 8), true)
		projector.TurnCompleted(turnID, "completed")
	}
	return &fakeController{Projector: projector}
}

func benchmarkLongTranscriptModel(tb testing.TB) *Model {
	tb.Helper()
	model, err := NewModel(context.Background(), benchmarkPresentationController(tb, 128))
	if err != nil {
		tb.Fatal(err)
	}
	entries := benchmarkTranscriptEntries(maxHydratedTranscriptEntries, 512)
	updated, _ := model.Update(TranscriptPageMsg{Page: frontend.TranscriptPage{
		Entries: entries,
		Start:   0,
		End:     len(entries),
		Total:   len(entries),
	}, Mode: transcriptPageInitial})
	return updated.(*Model)
}

func benchmarkTranscriptEntries(count, textBytes int) []frontend.TranscriptEntry {
	entries := make([]frontend.TranscriptEntry, 0, count)
	for index := range count {
		text := strings.Repeat("history ", max(1, textBytes/8))
		if index%11 == 0 {
			text += " needle"
		}
		entries = append(entries, frontend.TranscriptEntry{
			ID:        fmt.Sprintf("history-%03d", index),
			TurnID:    fmt.Sprintf("history-turn-%03d", index),
			Kind:      frontend.EntryUser,
			Text:      text,
			Complete:  true,
			Truncated: index%97 == 0,
		})
	}
	return entries
}

func assertPresentationCacheBounds(t *testing.T, model *Model) {
	t.Helper()
	for _, store := range []*semanticCellStore{&model.cells, &model.hydratedCells} {
		for _, cell := range store.ordered {
			if entries := len(cell.renderCache); entries > maxCellRenderCacheEntries {
				t.Fatalf("cell %q cache entries = %d", cell.Identity().ID, entries)
			}
		}
	}
	for id, cell := range model.staticCells {
		if entries := len(cell.renderCache); entries > maxCellRenderCacheEntries {
			t.Fatalf("static cell %q cache entries = %d", id, entries)
		}
	}
	for _, block := range model.document.blocks {
		group, ok := block.cell.(*activityGroupCell)
		if ok && len(group.renderCache) > maxCellRenderCacheEntries {
			t.Fatalf("activity group cache entries = %d", len(group.renderCache))
		}
	}
}
