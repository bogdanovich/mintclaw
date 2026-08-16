package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func (m *Model) statusLine() string {
	state := m.reducer.State()
	activity := "activity " + activityStatus(state)
	segments := make([]string, 0, 7)
	switch {
	case m.refreshingWorkspace:
		segments = append(segments, "refreshing repository…")
	case strings.TrimSpace(m.workspaceNotice) != "":
		segments = append(segments, m.workspaceNotice)
	}
	details := []string{
		"project " + projectStatus(state.Metadata.ProjectRoot),
		"branch " + branchStatus(state.Workspace),
		"model " + modelStatus(state.Metadata),
		contextStatus(state.ContextUsage),
	}
	segments = append(segments, details...)
	if !m.refreshingWorkspace && strings.TrimSpace(m.workspaceNotice) == "" {
		segments = append(segments, "Ctrl+R refresh")
	}
	return prioritizedStatusLine(m.width, activity, segments)
}

func prioritizedStatusLine(width int, activity string, optional []string) string {
	line := activity
	for _, segment := range optional {
		candidate := line + " · " + segment
		if width > 0 && ansi.StringWidth(candidate) > width {
			continue
		}
		line = candidate
	}
	return clipLine(line, width)
}

func projectStatus(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return "unknown"
	}
	name := filepath.Base(filepath.Clean(root))
	if name == "." || name == string(filepath.Separator) {
		return boundedSingleLine(root, 256)
	}
	return boundedSingleLine(name, 256)
}

func branchStatus(snapshot *codingworkspace.Snapshot) string {
	if snapshot == nil {
		return "unknown"
	}
	git := snapshot.Git
	switch {
	case !git.Available:
		return "no-git"
	case git.Unborn:
		return boundedSingleLine(git.Branch, 256) + " (unborn)"
	case git.Detached:
		return "detached@" + shortHead(git.Head)
	case strings.TrimSpace(git.Branch) == "":
		return "unknown"
	default:
		return boundedSingleLine(git.Branch, 256)
	}
}

func shortHead(head string) string {
	head = strings.TrimSpace(head)
	if len(head) > 8 {
		return head[:8]
	}
	if head == "" {
		return "unknown"
	}
	return head
}

func modelStatus(metadata frontend.ThreadMetadata) string {
	model := boundedSingleLine(metadata.Model, 256)
	provider := boundedSingleLine(metadata.Provider, 128)
	switch {
	case model == "":
		return "unknown"
	case provider == "":
		return model
	default:
		return model + "/" + provider
	}
}

func contextStatus(usage frontend.ContextUsage) string {
	if usage.LimitTokens <= 0 {
		return "context unknown"
	}
	percent := usage.UsedTokens * 100 / usage.LimitTokens
	return fmt.Sprintf(
		"context %d%% (%s/%s)",
		percent,
		formatTokenCount(usage.UsedTokens),
		formatTokenCount(usage.LimitTokens),
	)
}

func formatTokenCount(tokens int) string {
	if tokens < 1_000 {
		return strconv.Itoa(tokens)
	}
	if tokens < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	}
	return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
}

func activityStatus(state frontend.ThreadSnapshot) string {
	activity := strings.TrimSpace(string(state.Activity))
	if activity == "" {
		activity = "idle"
	}
	status := strings.TrimSpace(state.Status)
	if status == "" || status == activity {
		return activity
	}
	return activity + "/" + boundedSingleLine(status, 256)
}
