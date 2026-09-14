package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const workingShimmerSweep = 2 * time.Second

type workingShimmerSpan struct {
	text             string
	red, green, blue uint8
}

func (indicator *workingIndicator) render(
	interruptKey string,
	context cellRenderContext,
	focused bool,
) string {
	line := clipLine(indicator.line(interruptKey), context.Width)
	if line == "" || context.ColorLevel == cellColorNone || indicator.mode == MotionDisabled {
		return line
	}
	phase := string(indicator.phase)
	index := strings.Index(line, phase)
	if index < 0 {
		return line
	}
	prefix, suffix := line[:index], line[index+len(phase):]
	styledPhase := phase
	switch {
	case focused && indicator.mode == MotionAnimated && context.ColorLevel == cellColorTrueColor:
		now := indicator.now()
		startedAt := indicator.phaseStartedAt
		if startedAt.IsZero() {
			startedAt = now
		}
		styledPhase = renderWorkingShimmer(
			workingShimmerSpans(phase, max(time.Duration(0), now.Sub(startedAt)), context.Theme),
		)
	case indicator.mode == MotionAnimated:
		styledPhase = dimWorkingText(phase)
	}
	return dimWorkingText(prefix) + styledPhase + dimWorkingText(suffix)
}

func dimWorkingText(text string) string {
	if text == "" {
		return ""
	}
	return "\x1b[2m" + text + "\x1b[0m"
}

func workingShimmerSpans(text string, elapsed time.Duration, theme cellTheme) []workingShimmerSpan {
	width := ansi.StringWidth(text)
	if width <= 0 {
		return nil
	}
	foreground, background := workingShimmerPalette(theme)
	halfWidth := math.Max(float64(width)*0.1, 3)
	position := math.Mod(max(float64(0), elapsed.Seconds()), workingShimmerSweep.Seconds())/
		workingShimmerSweep.Seconds()*(float64(width)+2*halfWidth) - halfWidth
	spans := make([]workingShimmerSpan, 0, len(text))
	column := 0.0
	for text != "" {
		grapheme, graphemeWidth := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
		if grapheme == "" {
			break
		}
		text = text[len(grapheme):]
		center := column + float64(graphemeWidth)/2
		column += float64(graphemeWidth)
		distance := math.Min(math.Abs(center-position)/halfWidth, 1)
		intensity := 0.5 * (1 + math.Cos(math.Pi*distance))
		alpha := 0.5 + 0.5*intensity
		color := blendWorkingColor(foreground, background, alpha)
		spans = append(spans, workingShimmerSpan{
			text: grapheme, red: color[0], green: color[1], blue: color[2],
		})
	}
	return spans
}

func workingShimmerPalette(theme cellTheme) (foreground [3]uint8, background [3]uint8) {
	if theme == cellThemeLight {
		return [3]uint8{16, 16, 16}, [3]uint8{240, 240, 240}
	}
	return [3]uint8{240, 240, 240}, [3]uint8{16, 16, 16}
}

func blendWorkingColor(foreground [3]uint8, background [3]uint8, alpha float64) [3]uint8 {
	var blended [3]uint8
	for index := range blended {
		value := alpha*float64(foreground[index]) + (1-alpha)*float64(background[index])
		blended[index] = uint8(math.Round(value))
	}
	return blended
}

func renderWorkingShimmer(spans []workingShimmerSpan) string {
	var rendered strings.Builder
	for _, span := range spans {
		fmt.Fprintf(&rendered, "\x1b[38;2;%d;%d;%dm%s\x1b[0m", span.red, span.green, span.blue, span.text)
	}
	return rendered.String()
}
