package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var colorProfileMu sync.Mutex

const themeEnvironmentVariable = "MINTCLAW_TUI_THEME"

func configureColorProfile(noColor bool, environment []string) {
	colorProfileMu.Lock()
	defer colorProfileMu.Unlock()

	if noColor || noColorRequested(environment) {
		lipgloss.SetColorProfile(termenv.Ascii)
		return
	}
	lipgloss.SetColorProfile(termenv.EnvColorProfile())
}

func noColorRequested(environment []string) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return true
	}
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(name, "NO_COLOR") {
			return true
		}
	}
	return false
}

func currentCellColorLevel() cellColorLevel {
	switch lipgloss.ColorProfile() {
	case termenv.Ascii:
		return cellColorNone
	case termenv.ANSI:
		return cellColorANSI16
	case termenv.ANSI256:
		return cellColorANSI256
	default:
		return cellColorTrueColor
	}
}

func resolveCellTheme(environment []string) (cellTheme, error) {
	configured := environmentValue(environment, themeEnvironmentVariable)
	if configured == "" {
		configured = strings.TrimSpace(os.Getenv(themeEnvironmentVariable))
	}
	switch strings.ToLower(configured) {
	case "", "auto":
		return detectCellTheme(environment), nil
	case "dark":
		return cellThemeDark, nil
	case "light":
		return cellThemeLight, nil
	default:
		return cellThemeUnknown, fmt.Errorf(
			"coding TUI theme must be %q, %q, or %q",
			"auto",
			"dark",
			"light",
		)
	}
}

// detectCellTheme uses the conventional COLORFGBG background index instead
// of issuing an OSC query that can stall SSH/tmux startup or consume input.
func detectCellTheme(environment []string) cellTheme {
	value := environmentValue(environment, "COLORFGBG")
	if value == "" && len(environment) == 0 {
		value = strings.TrimSpace(os.Getenv("COLORFGBG"))
	}
	fields := strings.FieldsFunc(value, func(character rune) bool {
		return character == ';' || character == ':'
	})
	if len(fields) == 0 {
		return cellThemeDark
	}
	index, err := strconv.Atoi(strings.TrimSpace(fields[len(fields)-1]))
	if err != nil || index < 0 || index > 255 {
		return cellThemeDark
	}
	if ansiBackgroundIsLight(index) {
		return cellThemeLight
	}
	return cellThemeDark
}

func ansiBackgroundIsLight(index int) bool {
	base := [...][3]int{
		{0, 0, 0},
		{128, 0, 0},
		{0, 128, 0},
		{128, 128, 0},
		{0, 0, 128},
		{128, 0, 128},
		{0, 128, 128},
		{192, 192, 192},
		{128, 128, 128},
		{255, 0, 0},
		{0, 255, 0},
		{255, 255, 0},
		{0, 0, 255},
		{255, 0, 255},
		{0, 255, 255},
		{255, 255, 255},
	}
	if index < len(base) {
		color := base[index]
		return 299*color[0]+587*color[1]+114*color[2] >= 160_000
	}
	if index >= 232 {
		gray := 8 + (index-232)*10
		return gray >= 160
	}
	index -= 16
	red := index / 36
	green := index / 6 % 6
	blue := index % 6
	component := func(value int) int {
		if value == 0 {
			return 0
		}
		return 55 + value*40
	}
	luminance := 299*component(red) + 587*component(green) + 114*component(blue)
	return luminance >= 160_000
}
