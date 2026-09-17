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

func TestComposerGrowthUsesTextareaWordBoundaryWrapping(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	model.resize(12, 24)
	model = updateModel(t, model, tea.KeyMsg{
		Type: tea.KeyRunes, Runes: []rune("aaaaaa bbbbbb cccccc"),
	})
	wrappedRows := model.composer.LineInfo().Height
	if wrappedRows != 3 || model.composer.Height() != wrappedRows {
		t.Fatalf(
			"word-boundary composer rows = %d, textarea wrapped rows = %d; want 3",
			model.composer.Height(),
			wrappedRows,
		)
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
	for _, height := range []int{1, 2, 3, 4} {
		model.resize(40, height)
		view := model.View()
		if !strings.Contains(view, "›") || strings.Contains(view, "\n\n") ||
			len(strings.Split(view, "\n")) > height {
			t.Fatalf("tiny active terminal height %d view = %q", height, view)
		}
	}
	model.resize(40, 2)
	if view := model.View(); !strings.Contains(view, "to interrupt") {
		t.Fatalf("two-row active terminal omitted interrupt path: %q", view)
	}
}

func TestNormalTerminalSeparatesComposerAndFooter(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	projector.SteeringAccepted("turn-1", frontend.SteerInput{ID: "steer-1", Text: "focus on tests"})
	model, err := newModel(
		t.Context(),
		&fakeController{Projector: projector},
		modelOptions{adaptiveHeight: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, height := range []int{5, 8, 24} {
		model.resize(40, height)
		lines := strings.Split(model.View(), "\n")
		composerIndex := slices.IndexFunc(lines, func(line string) bool {
			return strings.Contains(line, "Ask MintClaw to do anything")
		})
		if composerIndex < 1 || composerIndex+1 >= len(lines) ||
			lines[composerIndex-1] != "" || lines[composerIndex+1] != "" {
			t.Fatalf("height %d composer gaps = %q", height, model.View())
		}
		if len(lines) > height {
			t.Fatalf("height %d rendered %d rows: %q", height, len(lines), model.View())
		}
	}
}

func TestActiveCommandPanelOnlyAddsComposerTopGapWhenItFits(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	model, err := newModel(t.Context(), &fakeController{Projector: projector}, modelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model.composer.SetValue("/status")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	model.resize(40, 6)
	compact := strings.Split(model.View(), "\n")
	if len(compact) > 6 {
		t.Fatalf("six-row active status rendered %d rows: %q", len(compact), model.View())
	}
	composerIndex := slices.IndexFunc(compact, func(line string) bool {
		return strings.Contains(line, "Ask MintClaw to do anything")
	})
	if composerIndex < 1 || compact[composerIndex-1] == "" ||
		composerIndex+1 >= len(compact) || compact[composerIndex+1] != "" {
		t.Fatalf("six-row active status used unsafe gaps: %q", model.View())
	}

	model.resize(40, 7)
	spacious := strings.Split(model.View(), "\n")
	composerIndex = slices.IndexFunc(spacious, func(line string) bool {
		return strings.Contains(line, "Ask MintClaw to do anything")
	})
	if composerIndex < 1 || spacious[composerIndex-1] != "" ||
		composerIndex+1 >= len(spacious) || spacious[composerIndex+1] != "" {
		t.Fatalf("seven-row active status omitted safe gaps: %q", model.View())
	}
	if len(spacious) > 7 {
		t.Fatalf("seven-row active status rendered %d rows: %q", len(spacious), model.View())
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
