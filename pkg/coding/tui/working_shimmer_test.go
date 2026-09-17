package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestWorkingShimmerMatchesCodexWaveAndPreservesGraphemes(t *testing.T) {
	spans := workingShimmerSpans("Working", time.Second, cellThemeDark)
	wantBrightness := []uint8{128, 156, 212, 240, 212, 156, 128}
	gotBrightness := make([]uint8, 0, len(spans))
	for _, span := range spans {
		if span.red != span.green || span.green != span.blue {
			t.Fatalf("non-neutral shimmer span = %+v", span)
		}
		gotBrightness = append(gotBrightness, span.red)
	}
	if !slices.Equal(gotBrightness, wantBrightness) {
		t.Fatalf("midpoint brightness = %v, want %v", gotBrightness, wantBrightness)
	}
	lightSpans := workingShimmerSpans("Working", time.Second, cellThemeLight)
	lightBrightness := make([]uint8, 0, len(lightSpans))
	for _, span := range lightSpans {
		lightBrightness = append(lightBrightness, span.red)
	}
	wantLightBrightness := []uint8{128, 100, 44, 16, 44, 100, 128}
	if !slices.Equal(lightBrightness, wantLightBrightness) {
		t.Fatalf("light midpoint brightness = %v, want %v", lightBrightness, wantLightBrightness)
	}

	const graphemes = "e\u0301👨‍👩‍👧‍👦界"
	clusterSpans := workingShimmerSpans(graphemes, 0, cellThemeDark)
	if len(clusterSpans) != 3 {
		t.Fatalf("grapheme spans = %d, want 3: %+v", len(clusterSpans), clusterSpans)
	}
	if got := clusterSpans[0].text + clusterSpans[1].text + clusterSpans[2].text; got != graphemes {
		t.Fatalf("shimmer changed graphemes: %q", got)
	}
}

func TestWorkingShimmerFramesMoveSmoothOverAdjacentGraphemes(t *testing.T) {
	first := workingShimmerSpans("Working", 500*time.Millisecond, cellThemeDark)
	second := workingShimmerSpans("Working", 516*time.Millisecond, cellThemeDark)
	if slices.Equal(first, second) {
		t.Fatal("adjacent shimmer frames are identical")
	}
	for index := range min(len(first), len(second)) {
		if difference := absoluteByteDifference(first[index].red, second[index].red); difference > 7 {
			t.Fatalf("span %d brightness changed by %d between adjacent frames", index, difference)
		}
	}
	for milliseconds := 0; milliseconds <= 2000; milliseconds += 16 {
		frame := workingShimmerSpans("Working", time.Duration(milliseconds)*time.Millisecond, cellThemeDark)
		bright := 0
		peak := false
		for _, span := range frame {
			peak = peak || span.red > 220
			if span.red > 160 {
				bright++
			}
		}
		if peak && bright < 2 {
			t.Fatalf("frame %dms flashes one grapheme: %+v", milliseconds, frame)
		}
	}
}

func TestWorkingRendererSeparatesSemanticTextFromCapabilityFallbacks(t *testing.T) {
	clock := &workingTestClock{now: time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)}
	snapshot := frontend.ThreadSnapshot{Activity: frontend.ActivityRunning}
	indicator := newWorkingIndicator(MotionAnimated, clock.Now)
	indicator.sync(snapshot, false)
	semantic := indicator.line("ctrl+c")
	trueColor := cellRenderContext{Width: 80, Theme: cellThemeDark, ColorLevel: cellColorTrueColor}
	first := indicator.render("ctrl+c", trueColor, true)
	clock.Advance(500 * time.Millisecond)
	second := indicator.render("ctrl+c", trueColor, true)
	if first == second || !strings.Contains(first, "\x1b[38;2;") {
		t.Fatalf("truecolor shimmer frames did not move: %q / %q", first, second)
	}
	if ansi.Strip(first) != semantic || ansi.Strip(second) != semantic {
		t.Fatalf("styled working text changed semantics: %q / %q / %q", semantic, first, second)
	}
	bounded := indicator.render("ctrl+c", cellRenderContext{
		Width: 12, Theme: cellThemeDark, ColorLevel: cellColorTrueColor,
	}, true)
	if width := ansi.StringWidth(bounded); width > 12 {
		t.Fatalf("bounded shimmer width = %d: %q", width, bounded)
	}

	lowColor := cellRenderContext{Width: 80, Theme: cellThemeDark, ColorLevel: cellColorANSI256}
	lowFirst := indicator.render("ctrl+c", lowColor, true)
	clock.Advance(100 * time.Millisecond)
	lowSecond := indicator.render("ctrl+c", lowColor, true)
	if lowFirst != lowSecond || strings.Contains(lowFirst, "38;2") || ansi.Strip(lowFirst) != semantic {
		t.Fatalf("low-color fallback is not static and semantic: %q / %q", lowFirst, lowSecond)
	}

	noColor := cellRenderContext{Width: 80, Theme: cellThemeDark, ColorLevel: cellColorNone}
	if rendered := indicator.render(
		"ctrl+c",
		noColor,
		true,
	); rendered != semantic ||
		strings.Contains(rendered, "\x1b") {
		t.Fatalf("no-color working line = %q, want %q", rendered, semantic)
	}

	reduced := newWorkingIndicator(MotionReduced, clock.Now)
	reduced.sync(snapshot, false)
	reducedFirst := reduced.render("ctrl+c", trueColor, true)
	clock.Advance(100 * time.Millisecond)
	if reducedSecond := reduced.render("ctrl+c", trueColor, true); reducedFirst != reducedSecond ||
		strings.Contains(reducedFirst, "38;2") {
		t.Fatalf("reduced-motion fallback changed: %q / %q", reducedFirst, reducedSecond)
	}

	disabled := newWorkingIndicator(MotionDisabled, clock.Now)
	disabled.sync(snapshot, false)
	if rendered := disabled.render("ctrl+c", trueColor, true); rendered != disabled.line("ctrl+c") ||
		strings.Contains(rendered, "\x1b") {
		t.Fatalf("disabled-motion working line gained styling: %q", rendered)
	}
}

func TestUnfocusedWorkingViewRemainsStaticAcrossUnrelatedRenders(t *testing.T) {
	clock := &workingTestClock{now: time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)}
	projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "inspect")
	model, err := newModel(
		t.Context(),
		&fakeController{Projector: projector},
		modelOptions{motionMode: MotionAnimated, now: clock.Now},
	)
	if err != nil {
		t.Fatal(err)
	}
	model.colorLevel = cellColorTrueColor
	model.theme = cellThemeDark
	model.resize(80, 24)
	model = updateModel(t, model, tea.BlurMsg{})
	first := model.View()
	clock.Advance(500 * time.Millisecond)
	second := model.View()
	if first != second || strings.Contains(first, "\x1b[38;2;") {
		t.Fatalf("unfocused working view changed across renders: %q / %q", first, second)
	}
	if plain := ansi.Strip(first); !strings.Contains(plain, "Working (0s • ctrl+c to interrupt)") {
		t.Fatalf("unfocused fallback omitted semantic state: %q", plain)
	}
	if model.working.tickPending {
		t.Fatal("unfocused model retained a pending animation tick")
	}
}

func TestWorkingPhaseChangesRestartShimmerWithoutResettingElapsed(t *testing.T) {
	clock := &workingTestClock{now: time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)}
	indicator := newWorkingIndicator(MotionAnimated, clock.Now)
	running := frontend.ThreadSnapshot{
		Activity: frontend.ActivityRunning,
		Items:    []frontend.PresentationItem{{TurnID: "turn-1"}},
	}
	indicator.sync(running, false)
	initialStart := indicator.phaseStartedAt
	clock.Advance(700 * time.Millisecond)
	exploring := running
	exploring.Items = []frontend.PresentationItem{activeExplorationItem("read_file")}
	indicator.sync(exploring, false)
	if indicator.phase != workingPhaseExploring || !indicator.phaseStartedAt.Equal(clock.Now()) ||
		indicator.phaseStartedAt.Equal(initialStart) {
		t.Fatalf("phase change did not restart shimmer: %+v", indicator)
	}
	phaseStart := indicator.phaseStartedAt
	clock.Advance(100 * time.Millisecond)
	indicator.sync(exploring, false)
	if !indicator.phaseStartedAt.Equal(phaseStart) {
		t.Fatalf("same phase restarted shimmer: %s -> %s", phaseStart, indicator.phaseStartedAt)
	}
	if elapsed := indicator.elapsedAt(clock.Now()); elapsed != 800*time.Millisecond {
		t.Fatalf("phase change reset elapsed time: %s", elapsed)
	}
	clock.Advance(100 * time.Millisecond)
	nextTurn := frontend.ThreadSnapshot{
		Activity: frontend.ActivityRunning,
		Items:    []frontend.PresentationItem{{TurnID: "turn-2"}},
	}
	indicator.sync(nextTurn, false)
	if !indicator.phaseStartedAt.Equal(clock.Now()) || indicator.elapsedAt(clock.Now()) != 0 {
		t.Fatalf("new work identity did not restart phase and elapsed state: %+v", indicator)
	}
}

func TestWorkingTickIntervalOnlyUsesFrameCadenceForTrueColorMotion(t *testing.T) {
	for _, testCase := range []struct {
		mode      MotionMode
		trueColor bool
		want      time.Duration
	}{
		{mode: MotionAnimated, trueColor: true, want: shimmerTickInterval},
		{mode: MotionAnimated, trueColor: false, want: clockTickInterval},
		{mode: MotionReduced, trueColor: true, want: clockTickInterval},
		{mode: MotionDisabled, trueColor: true, want: clockTickInterval},
	} {
		if got := workingTickInterval(testCase.mode, testCase.trueColor); got != testCase.want {
			t.Fatalf(
				"tick interval for %s truecolor=%t = %s, want %s",
				testCase.mode,
				testCase.trueColor,
				got,
				testCase.want,
			)
		}
	}
}

func absoluteByteDifference(left, right uint8) uint8 {
	if left > right {
		return left - right
	}
	return right - left
}
