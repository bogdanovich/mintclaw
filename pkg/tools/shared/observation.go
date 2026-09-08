package toolshared

import (
	"strings"
	"unicode/utf8"

	codingplan "github.com/bogdanovich/mintclaw/pkg/coding/plan"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

const (
	// MaxPlanObservationSteps bounds both update_plan admission and frontend
	// observation production. Normal coding plans are intentionally concise.
	MaxPlanObservationSteps = codingplan.MaxSteps
	maxPlanExplanationBytes = codingplan.MaxExplanationBytes
	maxPlanStepBytes        = codingplan.MaxStepBytes
	maxPlanTextBytes        = codingplan.MaxTextBytes

	maxCommandOutputBytes        = 64 << 10
	maxCommandIdentityBytes      = 1 << 10
	maxCommandTranscriptEntries  = 256
	maxCommandRedactionLookahead = 1 << 10
	maxExplorationValueBytes     = 1 << 10
)

// NewPlanObservation validates, redacts, bounds, and clones one plan before it
// crosses into presentation state.
func NewPlanObservation(explanation string, steps []PlanStepObservation) (PlanObservation, error) {
	return codingplan.New(explanation, steps)
}

// SanitizeToolObservation returns an independent safe observation or nil when
// the value is empty, ambiguous, or invalid. This is the fail-closed admission
// boundary used after hooks and again by coding frontends.
func SanitizeToolObservation(observation *ToolObservation) *ToolObservation {
	if observation == nil {
		return nil
	}
	variants := 0
	if observation.Command != nil {
		variants++
	}
	if observation.Exploration != nil {
		variants++
	}
	if observation.Plan != nil {
		variants++
	}
	if observation.RepositoryDiff != nil {
		variants++
	}
	if variants != 1 {
		return nil
	}
	if observation.Command != nil {
		command := sanitizeCommandObservation(*observation.Command)
		return &ToolObservation{Command: &command}
	}
	if observation.Exploration != nil {
		exploration, ok := sanitizeExplorationObservation(*observation.Exploration)
		if !ok {
			return nil
		}
		return &ToolObservation{Exploration: &exploration}
	}
	if observation.RepositoryDiff != nil {
		repositoryDiff, ok := sanitizeRepositoryDiffObservation(*observation.RepositoryDiff)
		if !ok {
			return nil
		}
		return &ToolObservation{RepositoryDiff: &repositoryDiff}
	}
	plan, err := NewPlanObservation(observation.Plan.Explanation, observation.Plan.Steps)
	if err != nil {
		return nil
	}
	plan.Truncated = plan.Truncated || observation.Plan.Truncated
	return &ToolObservation{Plan: &plan}
}

func sanitizeExplorationObservation(
	exploration ExplorationObservation,
) (ExplorationObservation, bool) {
	switch exploration.Operation {
	case ExplorationRead, ExplorationList, ExplorationSearch:
	default:
		return ExplorationObservation{}, false
	}
	var truncated bool
	exploration.Path, truncated = sanitizeObservationText(
		strings.TrimSpace(exploration.Path),
		maxExplorationValueBytes,
	)
	exploration.Truncated = exploration.Truncated || truncated
	exploration.Pattern, truncated = sanitizeObservationText(
		strings.TrimSpace(exploration.Pattern),
		maxExplorationValueBytes,
	)
	exploration.Truncated = exploration.Truncated || truncated
	exploration.Workspace, truncated = sanitizeObservationText(
		strings.TrimSpace(exploration.Workspace),
		maxExplorationValueBytes,
	)
	exploration.Truncated = exploration.Truncated || truncated
	return exploration, true
}

func sanitizeCommandObservation(command CommandObservation) CommandObservation {
	var truncated bool
	command.Action, truncated = sanitizeObservationText(strings.TrimSpace(command.Action), maxCommandIdentityBytes)
	command.Truncated = command.Truncated || truncated
	command.Command, truncated = sanitizeObservationText(command.Command, maxCommandOutputBytes)
	command.Truncated = command.Truncated || truncated
	command.CWD, truncated = sanitizeObservationText(command.CWD, maxCommandIdentityBytes)
	command.Truncated = command.Truncated || truncated
	command.Input, truncated = sanitizeObservationText(command.Input, maxCommandOutputBytes)
	command.Truncated = command.Truncated || truncated
	command.Source, truncated = sanitizeObservationText(strings.TrimSpace(command.Source), maxCommandIdentityBytes)
	command.Truncated = command.Truncated || truncated
	command.Stdout, truncated = sanitizeObservationText(command.Stdout, maxCommandOutputBytes)
	command.Truncated = command.Truncated || truncated
	command.Stderr, truncated = sanitizeObservationText(command.Stderr, maxCommandOutputBytes)
	command.Truncated = command.Truncated || truncated
	command.Output, truncated = sanitizeObservationText(command.Output, maxCommandOutputBytes)
	command.Truncated = command.Truncated || truncated
	command.SessionID, truncated = sanitizeObservationText(command.SessionID, maxCommandIdentityBytes)
	command.Truncated = command.Truncated || truncated
	command.Status, truncated = sanitizeObservationText(strings.TrimSpace(command.Status), maxCommandIdentityBytes)
	command.Truncated = command.Truncated || truncated
	command.Transcript, truncated = sanitizeCommandTranscript(command.Transcript)
	command.Truncated = command.Truncated || truncated
	if command.ExitCode != nil {
		exitCode := *command.ExitCode
		command.ExitCode = &exitCode
	}
	return command
}

func sanitizeCommandTranscript(entries []CommandTranscriptEntry) ([]CommandTranscriptEntry, bool) {
	if len(entries) == 0 {
		return nil, false
	}
	truncated := len(entries) > maxCommandTranscriptEntries
	entries = entries[:min(len(entries), maxCommandTranscriptEntries)]
	coalesced := make([]CommandTranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		stream, streamTruncated := sanitizeObservationText(
			strings.TrimSpace(entry.Stream),
			maxCommandIdentityBytes,
		)
		truncated = truncated || streamTruncated
		text := strings.ToValidUTF8(entry.Text, "�")
		if len(coalesced) != 0 && coalesced[len(coalesced)-1].Stream == stream {
			last := &coalesced[len(coalesced)-1]
			maximum := maxCommandOutputBytes + maxCommandRedactionLookahead
			remaining := max(0, maximum-len(last.Text))
			last.Text += validUTF8Prefix(text, remaining)
			truncated = truncated || len(text) > remaining
			continue
		}
		entry.Stream = stream
		entry.Text = validUTF8Prefix(text, maxCommandOutputBytes+maxCommandRedactionLookahead)
		truncated = truncated || len(text) > len(entry.Text)
		coalesced = append(coalesced, entry)
	}
	result := make([]CommandTranscriptEntry, 0, len(coalesced))
	remaining := maxCommandOutputBytes
	for _, entry := range coalesced {
		if remaining <= 0 {
			truncated = true
			break
		}
		var textTruncated bool
		entry.Text, textTruncated = sanitizeObservationText(entry.Text, remaining)
		truncated = truncated || textTruncated
		remaining -= len(entry.Text)
		if entry.Text != "" {
			result = append(result, entry)
		}
	}
	return result, truncated
}

func validUTF8Prefix(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func sanitizeObservationText(value string, maximum int) (string, bool) {
	value = strings.ToValidUTF8(value, "�")
	truncated := len(value) > maximum
	value = (diagnostictrace.Redactor{}).RedactText(value, maximum)
	return value, truncated
}
