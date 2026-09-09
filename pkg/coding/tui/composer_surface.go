package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const maxPendingGuidanceRows = 5

func (m *Model) desiredComposerRows() int {
	maximum := min(maxComposerHeight, max(1, m.height/3))
	value := m.composer.Value()
	if value == "" {
		return 1
	}
	width := max(1, m.composer.Width())
	rows := 0
	for _, line := range strings.Split(value, "\n") {
		lineWidth := ansi.StringWidth(line)
		rows += max(1, (lineWidth+width-1)/width)
	}
	return min(maximum, max(1, rows))
}

func (m *Model) syncComposerDimensions() bool {
	desired := m.desiredComposerRows()
	if m.composer.Height() == desired {
		return false
	}
	m.composer.SetHeight(desired)
	return true
}

func (m *Model) reflowComposer() {
	position := m.captureViewportPosition()
	if !m.syncComposerDimensions() {
		return
	}
	m.updateSurfaceDimensions()
	m.refreshViewportAt(position)
}

func (m *Model) pendingGuidanceRows() int {
	if len(m.snapshot.PendingInputs) == 0 || m.height <= 4 {
		return 0
	}
	workingRows := 0
	if m.workingSurfaceVisible() {
		workingRows = 1
	}
	available := m.height - m.composer.Height() - workingRows - 3
	if available <= 0 {
		return 0
	}
	return min(maxPendingGuidanceRows, min(available, len(m.snapshot.PendingInputs)+1))
}

func (m *Model) pendingGuidanceView() string {
	maximum := m.pendingGuidanceRows()
	if maximum == 0 {
		return ""
	}
	label := "active turn"
	if m.snapshot.Activity == frontend.ActivityWaitingInput {
		label = "suspended continuation"
	}
	lines := []string{clipLine(fmt.Sprintf(
		"• Queued guidance for %s (%d)",
		label,
		len(m.snapshot.PendingInputs),
	), m.width)}
	remaining := maximum - 1
	visible := min(remaining, min(3, len(m.snapshot.PendingInputs)))
	for _, pending := range m.snapshot.PendingInputs[:visible] {
		text := strings.ReplaceAll(sanitizeTerminalText(pending.Text), "\n", " ")
		if pending.Truncated {
			text += " …"
		}
		lines = append(lines, clipLine("  ↳ "+text, m.width))
	}
	if len(lines) < maximum && visible < len(m.snapshot.PendingInputs) {
		lines = append(lines, clipLine(
			fmt.Sprintf("  … %d more queued", len(m.snapshot.PendingInputs)-visible),
			m.width,
		))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) tinyView(status string) string {
	if m.height <= 1 {
		return firstRenderedLine(m.composer.View(), m.width)
	}
	lines := make([]string, 0, m.height)
	if m.working.running {
		lines = append(lines, clipLine(m.working.line(m.keys.interrupt.Help().Key), m.width))
	}
	composerLines := strings.Split(m.composer.View(), "\n")
	showStatus := m.height-len(lines) >= 2
	availableComposer := m.height - len(lines)
	if showStatus {
		availableComposer--
	}
	start := max(0, len(composerLines)-availableComposer)
	for _, line := range composerLines[start:] {
		lines = append(lines, clipLine(line, m.width))
	}
	if showStatus && len(lines) < m.height {
		lines = append(lines, clipLine(status, m.width))
	}
	return strings.Join(lines[:min(len(lines), m.height)], "\n")
}

func firstRenderedLine(value string, width int) string {
	line, _, _ := strings.Cut(value, "\n")
	return clipLine(line, width)
}
