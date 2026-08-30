package tui

import (
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var colorProfileMu sync.Mutex

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
