package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type workingTestClock struct {
	now time.Time
}

func (clock *workingTestClock) Now() time.Time {
	return clock.now
}

func (clock *workingTestClock) Advance(duration time.Duration) {
	clock.now = clock.now.Add(duration)
}

func TestWorkingIndicatorPhasesUseTypedStateWithoutLeakingToolNames(t *testing.T) {
	tests := []struct {
		name     string
		snapshot frontend.ThreadSnapshot
		want     workingPhase
	}{
		{
			name:     "working",
			snapshot: frontend.ThreadSnapshot{Activity: frontend.ActivityRunning},
			want:     workingPhaseWorking,
		},
		{
			name: "exploring",
			snapshot: frontend.ThreadSnapshot{
				Activity: frontend.ActivityRunning,
				Items:    []frontend.PresentationItem{activeToolItem("read_file", nil)},
			},
			want: workingPhaseExploring,
		},
		{
			name: "foreground command",
			snapshot: frontend.ThreadSnapshot{
				Activity: frontend.ActivityRunning,
				Items: []frontend.PresentationItem{activeToolItem("exec", &frontend.CommandState{
					Status: frontend.CommandRunning,
				})},
			},
			want: workingPhaseRunning,
		},
		{
			name: "background command",
			snapshot: frontend.ThreadSnapshot{
				Activity: frontend.ActivityRunning,
				Items: []frontend.PresentationItem{activeToolItem("exec", &frontend.CommandState{
					Status: frontend.CommandRunning, Background: true,
				})},
			},
			want: workingPhaseBackground,
		},
		{
			name: "persisted background session",
			snapshot: frontend.ThreadSnapshot{
				Activity: frontend.ActivityRunning,
				Items: []frontend.PresentationItem{{
					Lifecycle: frontend.PresentationCompleted,
					Tool: &frontend.ToolState{Command: &frontend.CommandState{
						Status: frontend.CommandRunning, Background: true, SessionID: "session-1",
					}},
				}},
			},
			want: workingPhaseBackground,
		},
		{
			name: "completed background session",
			snapshot: frontend.ThreadSnapshot{
				Activity: frontend.ActivityRunning,
				Items: []frontend.PresentationItem{
					{
						Lifecycle: frontend.PresentationCompleted,
						Tool: &frontend.ToolState{Command: &frontend.CommandState{
							Status: frontend.CommandRunning, Background: true, SessionID: "session-1",
						}},
					},
					{
						Lifecycle: frontend.PresentationCompleted,
						Tool: &frontend.ToolState{Command: &frontend.CommandState{
							Status: frontend.CommandSucceeded, Background: true, SessionID: "session-1",
						}},
					},
				},
			},
			want: workingPhaseWorking,
		},
		{
			name:     "compacting",
			snapshot: frontend.ThreadSnapshot{Activity: frontend.ActivityCompacting},
			want:     workingPhaseCompacting,
		},
		{
			name:     "reviewing",
			snapshot: frontend.ThreadSnapshot{Activity: frontend.ActivityReviewing},
			want:     workingPhaseReviewing,
		},
		{
			name:     "interrupting",
			snapshot: frontend.ThreadSnapshot{Activity: frontend.ActivityInterrupting},
			want:     workingPhaseStopping,
		},
		{
			name: "unknown tool uses safe generic phase",
			snapshot: frontend.ThreadSnapshot{
				Activity: frontend.ActivityRunning,
				Items:    []frontend.PresentationItem{activeToolItem("SECRET_INTERNAL_TOOL", nil)},
			},
			want: workingPhaseRunning,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := phaseForSnapshot(test.snapshot, false); got != test.want {
				t.Fatalf("phase = %q, want %q", got, test.want)
			}
			clock := &workingTestClock{now: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)}
			indicator := newWorkingIndicator(MotionDisabled, clock.Now)
			indicator.sync(test.snapshot, false)
			line := indicator.line("ctrl+c")
			if strings.Contains(line, "SECRET_INTERNAL_TOOL") {
				t.Fatalf("working line leaked raw tool name: %q", line)
			}
		})
	}
}

func TestWorkingIndicatorElapsedPausesResumesAndResets(t *testing.T) {
	clock := &workingTestClock{now: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)}
	indicator := newWorkingIndicator(MotionDisabled, clock.Now)
	running := frontend.ThreadSnapshot{
		Activity: frontend.ActivityRunning,
		Items:    []frontend.PresentationItem{{TurnID: "turn-1"}},
	}
	indicator.sync(running, false)
	clock.Advance(65 * time.Second)
	if line := indicator.line("ctrl+c"); line != "Working (1m 05s • ctrl+c to interrupt)" {
		t.Fatalf("running line = %q", line)
	}

	waiting := running
	waiting.Activity = frontend.ActivityWaitingInput
	indicator.sync(waiting, false)
	clock.Advance(30 * time.Second)
	if elapsed := indicator.elapsedAt(clock.Now()); elapsed != 65*time.Second {
		t.Fatalf("paused elapsed = %s", elapsed)
	}

	indicator.sync(running, false)
	clock.Advance(2 * time.Second)
	if elapsed := indicator.elapsedAt(clock.Now()); elapsed != 67*time.Second {
		t.Fatalf("resumed elapsed = %s", elapsed)
	}

	completed := running
	completed.Activity = frontend.ActivityIdle
	indicator.sync(completed, false)
	if indicator.running || indicator.identity != "" || indicator.elapsed != 0 || indicator.line("ctrl+c") != "" {
		t.Fatalf("completed indicator was not reset: %+v", indicator)
	}
}

func TestWorkingIndicatorMotionModesRetainSemanticState(t *testing.T) {
	clock := &workingTestClock{now: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)}
	snapshot := frontend.ThreadSnapshot{Activity: frontend.ActivityRunning}
	lines := make(map[MotionMode]string)
	for _, mode := range []MotionMode{MotionAnimated, MotionReduced, MotionDisabled} {
		indicator := newWorkingIndicator(mode, clock.Now)
		indicator.sync(snapshot, false)
		lines[mode] = indicator.line("f12")
		for _, want := range []string{"Working", "0s", "f12 to interrupt"} {
			if !strings.Contains(lines[mode], want) {
				t.Fatalf("%s line omits %q: %q", mode, want, lines[mode])
			}
		}
	}
	if !strings.HasPrefix(lines[MotionAnimated], "• ") || !strings.HasPrefix(lines[MotionReduced], "• ") {
		t.Fatalf("animated/reduced indicators = %q / %q", lines[MotionAnimated], lines[MotionReduced])
	}
	if strings.HasPrefix(lines[MotionDisabled], "• ") || strings.HasPrefix(lines[MotionDisabled], "◦ ") {
		t.Fatalf("disabled indicator still animates: %q", lines[MotionDisabled])
	}
}

func TestWorkingIndicatorTickerIsCentralizedAndStopsWhenHiddenOrBlurred(t *testing.T) {
	clock := &workingTestClock{now: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)}
	indicator := newWorkingIndicator(MotionAnimated, clock.Now)
	indicator.sync(frontend.ThreadSnapshot{Activity: frontend.ActivityRunning}, false)
	first := indicator.schedule(t.Context(), true, true)
	if first == nil || !indicator.tickPending {
		t.Fatal("visible indicator did not schedule a tick")
	}
	if duplicate := indicator.schedule(t.Context(), true, true); duplicate != nil {
		t.Fatal("indicator scheduled a second concurrent tick")
	}
	staleGeneration := indicator.tickGeneration
	if hidden := indicator.schedule(t.Context(), false, true); hidden != nil || indicator.tickPending {
		t.Fatalf("hidden indicator retained ticker: pending=%v command=%v", indicator.tickPending, hidden)
	}
	if indicator.acceptTick(workingTickMsg{generation: staleGeneration}) {
		t.Fatal("hidden indicator accepted a stale tick")
	}
	if blurred := indicator.schedule(t.Context(), true, false); blurred != nil || indicator.tickPending {
		t.Fatalf("blurred indicator scheduled ticker: pending=%v command=%v", indicator.tickPending, blurred)
	}
}

func TestModelWorkingSurfaceUsesConfiguredInterruptBindingAndLayout(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	controller := &fakeController{Projector: projector}
	clock := &workingTestClock{now: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)}
	model, err := newModel(t.Context(), controller, modelOptions{
		motionMode: MotionReduced, interruptKeys: []string{"f12"}, now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(80, 24)
	if got := model.viewport.Height; got != 17 {
		t.Fatalf("active viewport height = %d, want 17", got)
	}
	if view := model.View(); !strings.Contains(view, "• Working (0s • f12 to interrupt)") {
		t.Fatalf("working surface missing from view: %q", view)
	}

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyF12})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("remapped interrupt key produced no command")
	}
	_ = command()
	if controller.interrupts.Load() != 1 {
		t.Fatalf("interrupt calls = %d", controller.interrupts.Load())
	}

	model = updateModel(t, model, tea.BlurMsg{})
	clock.Advance(4 * time.Second)
	if line := model.workingLine(); !strings.Contains(line, "4s") {
		t.Fatalf("blurred elapsed time lost foreground work: %q", line)
	}
	if model.working.tickPending {
		t.Fatal("blurred model retained animation ticker")
	}

	projector.TurnCompleted("turn-1", "completed")
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, SnapshotMsg{Snapshot: snapshot})
	if model.workingLine() != "" || model.viewport.Height != 18 {
		t.Fatalf("completed surface/layout = %q / %d", model.workingLine(), model.viewport.Height)
	}
}

func TestWorkingSurfaceAccessibilityFixturesKeepExplicitState(t *testing.T) {
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	projector.ToolStarted("turn-1", "call-1", "read_file", "redacted")
	clock := &workingTestClock{now: time.Date(2026, 9, 7, 12, 0, 9, 0, time.UTC)}

	fixtures := []struct {
		name       string
		theme      cellTheme
		colorLevel cellColorLevel
		motion     MotionMode
		wantPrefix string
	}{
		{
			name:       "dark animated",
			theme:      cellThemeDark,
			colorLevel: cellColorTrueColor,
			motion:     MotionAnimated,
			wantPrefix: "• ",
		},
		{
			name:       "light reduced",
			theme:      cellThemeLight,
			colorLevel: cellColorANSI256,
			motion:     MotionReduced,
			wantPrefix: "• ",
		},
		{
			name:       "no color reduced",
			theme:      cellThemeDark,
			colorLevel: cellColorNone,
			motion:     MotionReduced,
			wantPrefix: "• ",
		},
		{name: "no color disabled", theme: cellThemeLight, colorLevel: cellColorNone, motion: MotionDisabled},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			model, modelErr := newModel(t.Context(), &fakeController{Projector: projector}, modelOptions{
				motionMode: fixture.motion, now: clock.Now,
			})
			if modelErr != nil {
				t.Fatal(modelErr)
			}
			model.theme = fixture.theme
			model.colorLevel = fixture.colorLevel
			line := model.workingLine()
			if !strings.HasPrefix(line, fixture.wantPrefix+"Exploring (0s • ctrl+c to interrupt)") {
				t.Fatalf("fixture line = %q", line)
			}
			if strings.Contains(line, "\x1b") {
				t.Fatalf("fixture emitted terminal styling into semantic state: %q", line)
			}
		})
	}
}

func TestRunPassesWorkingOptionsToModel(t *testing.T) {
	controller := newController(t)
	controller.TurnStarted("turn-1", "inspect")
	clock := &workingTestClock{now: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)}
	rendered := ""
	err := Run(t.Context(), controller, Options{
		MotionMode:    MotionDisabled,
		InterruptKeys: []string{"f12"},
		now:           clock.Now,
		newProgram: func(model tea.Model, _ ...tea.ProgramOption) program {
			codingModel, ok := model.(*Model)
			if !ok {
				t.Fatalf("model = %T", model)
			}
			rendered = codingModel.workingLine()
			return fakeProgram{model: model}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rendered != "Working (0s • f12 to interrupt)" {
		t.Fatalf("configured working line = %q", rendered)
	}
}

func TestResolveMotionModeSupportsOptionsAndEnvironment(t *testing.T) {
	for _, test := range []struct {
		name        string
		configured  MotionMode
		environment []string
		want        MotionMode
	}{
		{name: "default", want: MotionAnimated},
		{name: "configured", configured: MotionDisabled, environment: []string{motionEnvironmentVariable + "=animated"}, want: MotionDisabled},
		{name: "environment", environment: []string{motionEnvironmentVariable + "=reduced"}, want: MotionReduced},
		{name: "last environment value", environment: []string{motionEnvironmentVariable + "=animated", motionEnvironmentVariable + "=disabled"}, want: MotionDisabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(motionEnvironmentVariable, "")
			got, err := resolveMotionMode(test.configured, test.environment)
			if err != nil || got != test.want {
				t.Fatalf("resolve motion = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	t.Setenv(motionEnvironmentVariable, "")
	if _, err := resolveMotionMode("fast", nil); err == nil {
		t.Fatal("invalid motion mode was accepted")
	}
}

func activeToolItem(name string, command *frontend.CommandState) frontend.PresentationItem {
	return frontend.PresentationItem{
		ID: "tool-" + name, TurnID: "turn-1", Lifecycle: frontend.PresentationActive,
		Tool: &frontend.ToolState{
			TurnID: "turn-1", CallID: "call-" + name, Name: name,
			Status: frontend.ToolRunning, Command: command,
		},
	}
}
