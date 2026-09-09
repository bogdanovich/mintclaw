package toolshared

import (
	"encoding/json"
	"io"
	"strings"
	"unicode"
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
	maxMCPIdentityBytes          = 1 << 10
	maxMCPPurposeBytes           = 2 << 10
	maxMCPResultBytes            = 16 << 10
)

const (
	mcpLoopHaltIdenticalSuccess = "identical_call_emergency_halt"
	mcpLoopHaltRepeatedFailure  = "same_tool_failure_halt"
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
	if observation.MCP != nil {
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
	if observation.MCP != nil {
		mcp, ok := sanitizeMCPObservation(*observation.MCP)
		if !ok {
			return nil
		}
		return &ToolObservation{MCP: &mcp}
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

// NewMCPObservation admits one native MCP presentation observation through
// the same fail-closed boundary used by coding runtime events.
func NewMCPObservation(observation MCPObservation) *ToolObservation {
	return SanitizeToolObservation(&ToolObservation{MCP: &observation})
}

func sanitizeMCPObservation(observation MCPObservation) (MCPObservation, bool) {
	switch observation.Outcome {
	case MCPOutcomeRunning, MCPOutcomeSucceeded, MCPOutcomeFailed, MCPOutcomeCanceled,
		MCPOutcomeTimedOut, MCPOutcomeUncertain:
	default:
		return MCPObservation{}, false
	}
	if observation.LoopHaltCode != "" {
		if observation.LoopHaltCode != mcpLoopHaltIdenticalSuccess &&
			observation.LoopHaltCode != mcpLoopHaltRepeatedFailure {
			return MCPObservation{}, false
		}
		if observation.Outcome == MCPOutcomeRunning || observation.LoopHaltCount <= 0 ||
			observation.LoopHaltThreshold <= 0 || observation.LoopHaltCount < observation.LoopHaltThreshold {
			return MCPObservation{}, false
		}
		if observation.LoopHaltCode == mcpLoopHaltIdenticalSuccess &&
			observation.Outcome != MCPOutcomeSucceeded {
			return MCPObservation{}, false
		}
		if observation.LoopHaltCode == mcpLoopHaltRepeatedFailure &&
			observation.Outcome == MCPOutcomeSucceeded {
			return MCPObservation{}, false
		}
	} else if observation.LoopHaltCount != 0 || observation.LoopHaltThreshold != 0 {
		return MCPObservation{}, false
	}
	if observation.LoopHaltCount < 0 || observation.LoopHaltThreshold < 0 {
		return MCPObservation{}, false
	}

	var truncated bool
	observation.Server, truncated = sanitizeMCPIdentity(observation.Server, maxMCPIdentityBytes)
	observation.Truncated = observation.Truncated || truncated
	observation.Tool, truncated = sanitizeMCPIdentity(observation.Tool, maxMCPIdentityBytes)
	observation.Truncated = observation.Truncated || truncated
	if observation.Server == "" || observation.Tool == "" {
		return MCPObservation{}, false
	}
	observation.Purpose, truncated = sanitizeMCPContentText(
		strings.TrimSpace(observation.Purpose),
		maxMCPPurposeBytes,
	)
	observation.Truncated = observation.Truncated || truncated
	observation.Result, truncated = sanitizeMCPObservationText(observation.Result, maxMCPResultBytes)
	observation.Truncated = observation.Truncated || truncated
	observation.Error, truncated = sanitizeMCPObservationText(observation.Error, maxMCPResultBytes)
	observation.Truncated = observation.Truncated || truncated

	if observation.Outcome == MCPOutcomeRunning && (observation.Result != "" || observation.Error != "") {
		return MCPObservation{}, false
	}
	if observation.Outcome == MCPOutcomeSucceeded && observation.Error != "" {
		return MCPObservation{}, false
	}
	if observation.Outcome != MCPOutcomeRunning && observation.Outcome != MCPOutcomeSucceeded &&
		observation.Result != "" {
		return MCPObservation{}, false
	}
	return observation, true
}

func sanitizeMCPIdentity(value string, maximum int) (string, bool) {
	value, truncated := sanitizeObservationText(strings.TrimSpace(value), maximum)
	value, controlsRemoved := normalizeMCPContentControls(value, false)
	return strings.TrimSpace(value), truncated || controlsRemoved
}

func sanitizeMCPContentText(value string, maximum int) (string, bool) {
	value, truncated := sanitizeObservationText(value, maximum)
	value, controlsRemoved := normalizeMCPContentControls(value, true)
	return value, truncated || controlsRemoved
}

func sanitizeMCPObservationText(value string, maximum int) (string, bool) {
	value = strings.ToValidUTF8(value, "�")
	if len(value) > maximum+maxCommandRedactionLookahead {
		trimmed := strings.TrimSpace(value)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return "[MCP JSON evidence omitted: oversized]", true
		}
		return sanitizeMCPContentText(value, maximum)
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err == nil {
		var trailing any
		if err = decoder.Decode(&trailing); err == io.EOF {
			preview := (diagnostictrace.Redactor{}).RedactJSON(decoded, maximum)
			preview, controlsRemoved := normalizeMCPContentControls(preview, true)
			return preview, len(value) > maximum || len(preview) >= maximum || controlsRemoved
		}
	}
	return sanitizeMCPContentText(value, maximum)
}

func normalizeMCPContentControls(value string, multiline bool) (string, bool) {
	original := value
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(character rune) rune {
		if multiline && (character == '\n' || character == '\t') {
			return character
		}
		if unicode.IsControl(character) {
			if !multiline {
				return ' '
			}
			return -1
		}
		return character
	}, value)
	return value, value != original
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
