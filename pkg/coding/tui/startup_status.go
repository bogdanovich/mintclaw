package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const (
	startupStatusMaximumWidth = 80
	startupStatusMinimumWidth = 24
)

func startupStatusEligible(snapshot frontend.ThreadSnapshot) bool {
	if snapshot.Runtime != nil && snapshot.Runtime.Resumed {
		return false
	}
	return len(snapshot.Items) == 0 && snapshot.LastTurn == nil && !activeWork(snapshot.Activity)
}

func (m *Model) startupStatusView() string {
	if !m.showStartupStatus || m.width < startupStatusMinimumWidth {
		return ""
	}
	lines := renderStartupStatus(m.snapshot, m.width, m.home)
	// Preserve one row above and below the composer plus the one-line footer.
	// On a short terminal, the input path is more important than the welcome card.
	if len(lines)+m.composer.Height()+3 > m.height {
		return ""
	}
	return strings.Join(lines, "\n")
}

func renderStartupStatus(snapshot frontend.ThreadSnapshot, terminalWidth int, home string) []string {
	width := min(max(1, terminalWidth), startupStatusMaximumWidth)
	if width < 3 {
		return nil
	}
	innerWidth := width - 2
	content := []string{
		startupStatusTitle(snapshot.Runtime),
		"",
		startupStatusField("model:", startupStatusModel(snapshot), innerWidth),
		startupStatusField("directory:", startupStatusDirectory(snapshot, home), innerWidth),
		startupStatusField("permissions:", startupStatusPermissions(snapshot.Runtime), innerWidth),
	}
	lines := make([]string, 0, len(content)+2)
	lines = append(lines, "╭"+strings.Repeat("─", innerWidth)+"╮")
	for _, line := range content {
		line = clipLine(line, innerWidth)
		lines = append(lines, "│"+line+strings.Repeat(" ", max(0, innerWidth-ansi.StringWidth(line)))+"│")
	}
	return append(lines, "╰"+strings.Repeat("─", innerWidth)+"╯")
}

func startupStatusTitle(runtimeStatus *frontend.RuntimeStatus) string {
	title := "  >_ MintClaw"
	if version := startupStatusVersion(runtimeStatus); version != "" {
		title += " (" + version + ")"
	}
	return title
}

func startupStatusVersion(runtimeStatus *frontend.RuntimeStatus) string {
	if runtimeStatus == nil {
		return ""
	}
	value := strings.TrimSpace(runtimeStatus.Version)
	if strings.HasPrefix(strings.ToLower(value), "mintclaw ") {
		value = strings.TrimSpace(value[len("mintclaw "):])
	}
	if fields := strings.Fields(value); len(fields) > 0 {
		return boundedSingleLine(fields[0], 64)
	}
	return ""
}

func startupStatusModel(snapshot frontend.ThreadSnapshot) string {
	model := fallbackStatusValue(snapshot.Metadata.Model)
	if snapshot.Runtime == nil || !snapshot.Runtime.ReasoningConfigured {
		return model
	}
	reasoning := strings.TrimSpace(snapshot.Runtime.ReasoningEffort)
	if reasoning == "" {
		return model
	}
	return model + " " + boundedSingleLine(reasoning, 128)
}

func startupStatusDirectory(snapshot frontend.ThreadSnapshot, home string) string {
	directory := snapshot.Metadata.CWD
	if strings.TrimSpace(directory) == "" {
		directory = snapshot.Metadata.ProjectRoot
	}
	return statusPathDisplay(directory, home)
}

func startupStatusPermissions(runtimeStatus *frontend.RuntimeStatus) string {
	if runtimeStatus != nil && runtimeStatus.Permission == frontend.PermissionFullAccess &&
		runtimeStatus.Autonomy == frontend.AutonomyYolo {
		return "YOLO mode"
	}
	return statusPermission(runtimeStatus)
}

func startupStatusField(label, value string, width int) string {
	const labelWidth = len("permissions:")
	prefix := fmt.Sprintf("  %-*s ", labelWidth, label)
	return prefix + clipLine(fallbackStatusValue(value), max(1, width-ansi.StringWidth(prefix)))
}
