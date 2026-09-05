package review

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// RenderStatePlain renders the frontend-neutral review projection for plain
// terminals and interactive panels.
func RenderStatePlain(state State) string {
	lines := []string{
		"Local code review",
		"target: " + renderTarget(state.Target),
		"phase: " + displayText(string(state.Phase)),
	}
	lines = appendMultilineField(lines, "instructions", state.Target.Instructions)
	if state.Progress != "" {
		lines = appendMultilineField(lines, "progress", state.Progress)
	}
	if state.Result != nil {
		lines = append(lines, "")
		lines = append(lines, renderResultBody(*state.Result)...)
		return strings.Join(lines, "\n")
	}
	if len(state.Findings) > 0 {
		lines = append(lines, "")
		lines = append(lines, renderFindings(state.Findings)...)
	}
	return strings.Join(lines, "\n")
}

// RenderResultPlain renders one immutable completed review result.
func RenderResultPlain(result Result) string {
	phase := PhaseCompleted
	if result.Stale {
		phase = PhaseStale
	}
	lines := []string{
		"Local code review",
		"target: " + renderTarget(result.Target),
		"phase: " + string(phase),
	}
	lines = appendMultilineField(lines, "instructions", result.Target.Instructions)
	lines = append(lines, "")
	return strings.Join(append(lines, renderResultBody(result)...), "\n")
}

func renderTarget(target Target) string {
	value := displayText(string(target.Kind))
	if target.Ref != "" {
		value += " " + displayText(target.Ref)
	}
	return value
}

func renderResultBody(result Result) []string {
	lines := appendMultilineField(nil, "summary", result.Summary)
	if result.EvidenceGeneration != "" {
		lines = append(lines, "evidence: "+displayText(result.EvidenceGeneration))
	}
	if result.ResolvedRevision != "" {
		lines = append(lines, "revision: "+displayText(result.ResolvedRevision))
	}
	if result.MergeBase != "" {
		lines = append(lines, "merge base: "+displayText(result.MergeBase))
	}
	lines = append(lines, fmt.Sprintf("findings: %d", len(result.Findings)))
	if result.Stale {
		lines = append(lines, "stale: true")
	}
	if result.Truncated {
		lines = append(lines, "truncated: true")
	}
	if result.Diagnostic != "" {
		lines = appendMultilineField(lines, "diagnostic", result.Diagnostic)
	}
	if len(result.Findings) > 0 {
		lines = append(lines, "")
		lines = append(lines, renderFindings(result.Findings)...)
	}
	return lines
}

func renderFindings(findings []Finding) []string {
	prioritized := append([]Finding(nil), findings...)
	slices.SortStableFunc(prioritized, func(left, right Finding) int {
		return severityPriority(left.Severity) - severityPriority(right.Severity)
	})
	lines := make([]string, 0, len(prioritized)*4)
	for index, finding := range prioritized {
		lines = append(lines, fmt.Sprintf(
			"%d. [%s] %s",
			index+1,
			displayText(string(finding.Severity)),
			displayText(finding.Title),
		))
		lines = append(lines, "   location: "+renderLocation(finding))
		lines = append(lines, fmt.Sprintf("   confidence: %.2f", finding.Confidence))
		lines = appendMultilineField(lines, "   explanation", finding.Explanation)
	}
	return lines
}

func severityPriority(severity Severity) int {
	switch severity {
	case SeverityCritical:
		return 0
	case SeverityMajor:
		return 1
	default:
		return 2
	}
}

func renderLocation(finding Finding) string {
	switch finding.LocationState {
	case LocationCurrent:
		if finding.StartLine == finding.EndLine {
			return fmt.Sprintf("%s:%d", displayText(finding.Path), finding.StartLine)
		}
		return fmt.Sprintf("%s:%d-%d", displayText(finding.Path), finding.StartLine, finding.EndLine)
	case LocationStale:
		if finding.Path != "" {
			return displayText(finding.Path) + " (stale; no current line claimed)"
		}
		return "stale; no current location claimed"
	default:
		return "unlocated"
	}
}

func appendMultilineField(lines []string, label string, value string) []string {
	if value == "" {
		return lines
	}
	lines = append(lines, label+":")
	indent := strings.Repeat(" ", len(label)-len(strings.TrimLeft(label, " "))+2)
	for _, line := range strings.Split(value, "\n") {
		lines = append(lines, indent+displayText(line))
	}
	return lines
}

func displayText(value string) string {
	var builder strings.Builder
	for _, current := range value {
		if current == '\t' {
			builder.WriteString(`\t`)
			continue
		}
		if !unicode.IsControl(current) {
			builder.WriteRune(current)
			continue
		}
		if current <= 0xff {
			_, _ = fmt.Fprintf(&builder, "\\x%02x", current)
		} else if current <= 0xffff {
			_, _ = fmt.Fprintf(&builder, "\\u%04x", current)
		} else {
			_, _ = fmt.Fprintf(&builder, "\\U%08x", current)
		}
	}
	return builder.String()
}
