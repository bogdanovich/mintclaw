package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type fakeController struct {
	*frontend.Projector
	interrupts  atomic.Int32
	hardCancels atomic.Int32
	closes      atomic.Int32
	submits     atomic.Int32
}

func (f *fakeController) Submit(context.Context, string) error {
	f.submits.Add(1)
	return nil
}

func (f *fakeController) Interrupt(context.Context) error {
	f.interrupts.Add(1)
	return nil
}

func (f *fakeController) HardCancel(context.Context) error {
	f.hardCancels.Add(1)
	return nil
}
func (f *fakeController) Compact(context.Context) error        { return nil }
func (f *fakeController) Rename(context.Context, string) error { return nil }
func (f *fakeController) NewThread(context.Context) error      { return nil }
func (f *fakeController) Close(context.Context) error {
	f.closes.Add(1)
	return nil
}

func TestModelHandlesResizeAndMultilineBracketedPaste(t *testing.T) {
	controller, snapshot := newController(t)
	model, err := NewModel(controller, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 43, Height: 11})
	if width, height := model.Dimensions(); width != 43 || height != 11 {
		t.Fatalf("dimensions = %dx%d", width, height)
	}
	model = updateModel(t, model, tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("first line\n第二行 👩🏽‍💻"),
		Paste: true,
	})
	if got := model.ComposerValue(); got != "first line\n第二行 👩🏽‍💻" {
		t.Fatalf("composer value = %q", got)
	}
}

func TestModelUsesGracefulThenHardCancellation(t *testing.T) {
	controller, _ := newController(t)
	controller.TurnStarted("turn-1", "fix it")
	snapshot, _ := controller.Snapshot(t.Context())
	model, err := NewModel(controller, snapshot)
	if err != nil {
		t.Fatal(err)
	}

	updated, first := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(*Model)
	if first == nil {
		t.Fatal("first Ctrl+C produced no interrupt command")
	}
	first()
	if controller.interrupts.Load() != 1 || controller.hardCancels.Load() != 0 {
		t.Fatalf(
			"after first Ctrl+C: interrupts=%d hard=%d",
			controller.interrupts.Load(),
			controller.hardCancels.Load(),
		)
	}
	model = updateModel(t, model, DeltaMsg{
		Delta: controller.AssistantAccumulated("turn-1", "still streaming", false),
	})
	_, second := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if second == nil {
		t.Fatal("second Ctrl+C produced no hard-cancel command")
	}
	second()
	if controller.hardCancels.Load() != 1 {
		t.Fatalf("hard cancels = %d", controller.hardCancels.Load())
	}
}

func TestModelQuitsBeforeControllerCleanupWhileIdle(t *testing.T) {
	controller, snapshot := newController(t)
	model, err := NewModel(controller, snapshot)
	if err != nil {
		t.Fatal(err)
	}

	_, quit := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit == nil {
		t.Fatal("idle Ctrl+C produced no quit command")
	}
	message := quit()
	if _, ok := message.(tea.QuitMsg); !ok {
		t.Fatalf("idle Ctrl+C command returned %T, want tea.QuitMsg", message)
	}
	if controller.closes.Load() != 0 {
		t.Fatalf("model closed controller before terminal restoration: closes=%d", controller.closes.Load())
	}
}

func TestModelInterruptsAdmittedInitialTurnBeforeFirstDelta(t *testing.T) {
	controller, snapshot := newController(t)
	model, err := NewModel(controller, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.admitInitialTurn()

	_, interrupt := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if interrupt == nil {
		t.Fatal("pending initial turn produced no interrupt command")
	}
	interrupt()
	if controller.interrupts.Load() != 1 || controller.closes.Load() != 0 {
		t.Fatalf("interrupts=%d closes=%d", controller.interrupts.Load(), controller.closes.Load())
	}
	delta := controller.TurnStarted("turn-1", "fix it")
	model = updateModel(t, model, DeltaMsg{Delta: delta})
	if model.initialTurnPending {
		t.Fatal("authoritative turn lifecycle did not clear pending admission")
	}
}

func TestModelRecoversDroppedDeltaThroughControllerSnapshot(t *testing.T) {
	controller, snapshot := newController(t)
	model, err := NewModel(controller, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	controller.TurnStarted("turn-1", "fix it") // dropped
	latest := controller.AssistantAccumulated("turn-1", "working", false)

	updated, resync := model.Update(DeltaMsg{Delta: latest})
	model = updated.(*Model)
	if resync == nil {
		t.Fatal("dropped delta did not request a snapshot")
	}
	message, ok := resync().(SnapshotMsg)
	if !ok {
		t.Fatalf("resync message = %T", message)
	}
	model = updateModel(t, model, message)
	state := model.Snapshot()
	if state.Revision != latest.Revision || len(state.Entries) != 2 || state.Entries[1].Text != "working" {
		t.Fatalf("resynchronized model = %+v", state)
	}
}

func TestModelKeepsLongBoundedHistoryUsableAtNarrowSize(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{Entries: 128})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		turnID := fmt.Sprintf("turn-%d", i)
		projector.TurnStarted(turnID, "question")
		projector.AssistantAccumulated(turnID, "answer", true)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewModel(&fakeController{Projector: projector}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 12, Height: 7})
	if view := model.View(); view == "" || len(snapshot.Entries) != 128 || !snapshot.HasOlderEntries {
		t.Fatalf(
			"long-history model is not bounded and renderable: entries=%d older=%v",
			len(snapshot.Entries),
			snapshot.HasOlderEntries,
		)
	}
}

func TestModelUsesActualTinyTerminalDimensions(t *testing.T) {
	controller, snapshot := newController(t)
	model, err := NewModel(controller, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 1, Height: 1})
	if width, height := model.Dimensions(); width != 1 || height != 1 {
		t.Fatalf("dimensions = %dx%d, want 1x1", width, height)
	}
	if view := model.View(); view == "" || strings.Contains(view, "\n") {
		t.Fatalf("tiny view = %q", view)
	}
}

func TestNextDeltaCommandWatchesFromCurrentRevision(t *testing.T) {
	controller, snapshot := newController(t)
	delta := controller.TurnStarted("turn-1", "inspect")
	message := nextDeltaCmd(t.Context(), controller, snapshot.Revision)()
	update, ok := message.(DeltaMsg)
	if !ok {
		t.Fatalf("watch message = %T", message)
	}
	if update.Delta.Revision != delta.Revision {
		t.Fatalf("revision = %d, want %d", update.Delta.Revision, delta.Revision)
	}
}

func TestModelTracksTerminalFocusWithoutChangingComposer(t *testing.T) {
	controller, snapshot := newController(t)
	model, err := NewModel(controller, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft")})
	model = updateModel(t, model, tea.BlurMsg{})
	if !strings.Contains(model.View(), "terminal unfocused") || model.ComposerValue() != "draft" {
		t.Fatalf("blurred view=%q composer=%q", model.View(), model.ComposerValue())
	}
	model = updateModel(t, model, tea.FocusMsg{})
	if strings.Contains(model.View(), "terminal unfocused") || model.ComposerValue() != "draft" {
		t.Fatalf("focused view=%q composer=%q", model.View(), model.ComposerValue())
	}
}

func newController(t *testing.T) (*fakeController, frontend.ThreadSnapshot) {
	t.Helper()
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeController{Projector: projector}, snapshot
}

func updateModel(t *testing.T, model *Model, message tea.Msg) *Model {
	t.Helper()
	updated, _ := model.Update(message)
	result, ok := updated.(*Model)
	if !ok {
		t.Fatalf("updated model = %T", updated)
	}
	return result
}
