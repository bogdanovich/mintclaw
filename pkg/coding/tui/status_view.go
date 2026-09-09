package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func (m *Model) statusLine() string {
	state := m.snapshot
	primary := modelStatus(state.Metadata)
	segments := make([]string, 0, 6)
	switch {
	case m.refreshingWorkspace:
		segments = append(segments, "refreshing repository…")
	case strings.TrimSpace(m.workspaceNotice) != "":
		segments = append(segments, m.workspaceNotice)
	}
	if state.Runtime != nil && strings.TrimSpace(state.Runtime.ReasoningEffort) != "" {
		segments = append(segments, boundedSingleLine(state.Runtime.ReasoningEffort, 128))
	}
	directory := state.Metadata.CWD
	if strings.TrimSpace(directory) == "" {
		directory = state.Metadata.ProjectRoot
	}
	if directory = statusPathDisplay(directory, m.home); directory != "unavailable" {
		segments = append(segments, directory)
	}
	if branch := branchStatus(state.Workspace); branch != "unknown" && branch != "no-git" {
		segments = append(segments, branch)
	}
	if state.ContextUsage.LimitTokens > 0 {
		segments = append(segments, contextStatus(state.ContextUsage))
	}
	if !m.refreshingWorkspace && strings.TrimSpace(m.workspaceNotice) == "" {
		segments = append(segments, "Ctrl+R refresh")
	}
	return prioritizedStatusLine(m.width, primary, segments)
}

func compactionMode(compaction *frontend.CompactionState) string {
	if compaction != nil && compaction.Background {
		return "background"
	}
	return "blocking"
}

func compactionTrigger(reason string) string {
	switch strings.TrimSpace(reason) {
	case "manual":
		return "manual request"
	case "llm_retry":
		return "context overflow retry"
	case "proactive_budget":
		return "context pressure"
	case "summarize":
		return "session summarization"
	case "":
		return "unknown"
	default:
		return boundedSingleLine(reason, 128)
	}
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
