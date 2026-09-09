package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestComposerUsesOneIdleRowAndGrowsForWrappedMultilineInput(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	model.resize(20, 24)
	if model.composer.Height() != 1 {
		t.Fatalf("idle composer height = %d, want 1", model.composer.Height())
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("界", 24))})
	if model.composer.Height() <= 1 || model.composer.Height() > maxComposerHeight {
		t.Fatalf("wrapped composer height = %d", model.composer.Height())
	}
	model.composer.Reset()
	model.reflowComposer()
	if model.composer.Height() != 1 {
		t.Fatalf("cleared composer height = %d, want 1", model.composer.Height())
	}
}

func TestActiveTurnEnterQueuesGuidanceOutsideSubmittedHistory(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	controller := &fakeController{Projector: projector}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(80, 24)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("focus on the parser")})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil || !model.submitting || !model.steering {
		t.Fatal("Enter did not start same-turn guidance admission")
	}
	message, ok := command().(SteerResultMsg)
	if !ok || message.Err != nil {
		t.Fatalf("steer command result = %#v", message)
	}
	if controller.submits.Load() != 0 || controller.steerCalls.Load() != 1 {
		t.Fatalf("controller calls: submits=%d steers=%d", controller.submits.Load(), controller.steerCalls.Load())
	}
	steers := controller.steeredInputs()
	if len(steers) != 1 || steers[0].Text != "focus on the parser" || steers[0].ID == "" {
		t.Fatalf("steered inputs = %+v", steers)
	}

	snapshot, err := controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: snapshot})
	if len(model.snapshot.PendingInputs) != 1 ||
		strings.Contains(renderedModelTranscript(model, 80), "focus on the parser") {
		t.Fatalf(
			"pending guidance leaked into transcript: %+v / %q",
			model.snapshot.PendingInputs,
			renderedModelTranscript(model, 80),
		)
	}
	if pending := model.pendingGuidanceView(); !strings.Contains(pending, "Queued guidance for active turn") ||
		!strings.Contains(pending, "↳ focus on the parser") {
		t.Fatalf("pending guidance surface = %q", pending)
	}
	model = updateModel(t, model, message)
	if model.ComposerValue() != "" || model.submitting || model.steering {
		t.Fatalf("accepted guidance left composer state: value=%q submitting=%v steering=%v",
			model.ComposerValue(), model.submitting, model.steering)
	}

	projector.SteeringInjected("turn-1", steers)
	snapshot, err = controller.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: snapshot})
	if len(model.snapshot.PendingInputs) != 0 ||
		!strings.Contains(renderedModelTranscript(model, 80), "focus on the parser") {
		t.Fatalf("injected guidance was not promoted to transcript: %+v / %q",
			model.snapshot.PendingInputs, renderedModelTranscript(model, 80))
	}
}

func TestFailedSteeringAndActiveAttachmentsPreserveDraft(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	controller := &fakeController{Projector: projector, steerErr: errors.New("turn closed")}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("keep this draft")})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("failed steer did not return an admission command")
	}
	model = updateModel(t, model, command())
	if model.ComposerValue() != "keep this draft" || model.err == nil || len(model.snapshot.PendingInputs) != 0 {
		t.Fatalf("failed steer state: value=%q err=%v pending=%+v",
			model.ComposerValue(), model.err, model.snapshot.PendingInputs)
	}

	controller.steerErr = nil
	model.composerAttachments = []composerAttachment{{placeholder: "[File: note.txt]"}}
	model.composer.SetValue("[File: note.txt]")
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command != nil || model.err == nil || model.ComposerValue() != "[File: note.txt]" {
		t.Fatalf("attachment steer state: command=%v err=%v value=%q", command, model.err, model.ComposerValue())
	}
}

func TestTinyActiveTerminalKeepsComposerAndInterruptPath(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(40, 2)
	view := model.View()
	if !strings.Contains(view, "to interrupt") || !strings.Contains(view, "›") || len(strings.Split(view, "\n")) != 2 {
		t.Fatalf("tiny active terminal view = %q", view)
	}
}

func TestComposerThemeResolutionUsesOverrideAndCOLORFGBG(t *testing.T) {
	tests := []struct {
		environment []string
		want        cellTheme
	}{
		{environment: []string{"MINTCLAW_TUI_THEME=light", "COLORFGBG=15;0"}, want: cellThemeLight},
		{environment: []string{"MINTCLAW_TUI_THEME=dark", "COLORFGBG=0;15"}, want: cellThemeDark},
		{environment: []string{"COLORFGBG=0;15"}, want: cellThemeLight},
		{environment: []string{"COLORFGBG=15;0"}, want: cellThemeDark},
		{environment: []string{"COLORFGBG=0;12"}, want: cellThemeDark},
	}
	for _, test := range tests {
		got, err := resolveCellTheme(test.environment)
		if err != nil || got != test.want {
			t.Fatalf("resolveCellTheme(%v) = %v, %v; want %v", test.environment, got, err, test.want)
		}
	}
	if _, err := resolveCellTheme([]string{"MINTCLAW_TUI_THEME=sepia"}); err == nil {
		t.Fatal("invalid theme override was accepted")
	}
}

func TestPendingGuidanceSurfaceIsBoundedAndDistinct(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	for index := range 8 {
		projector.SteeringAccepted("turn-1", frontend.SteerInput{
			ID: "steer-" + string(rune('a'+index)), Text: strings.Repeat("guidance ", 40),
		})
	}
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(40, 24)
	lines := strings.Split(model.pendingGuidanceView(), "\n")
	if len(lines) > maxPendingGuidanceRows || !slices.ContainsFunc(lines, func(line string) bool {
		return strings.Contains(line, "more queued")
	}) {
		t.Fatalf("bounded pending guidance lines = %#v", lines)
	}
}
