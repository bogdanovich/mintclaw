package review

import (
	"fmt"
	"slices"
	"strings"
)

// RenderStatePlain renders the frontend-neutral review projection for plain
// terminals and interactive panels.
func RenderStatePlain(state State) string {
	lines := []string{
		"Local code review",
		"target: " + renderTarget(state.Target),
		"phase: " + string(state.Phase),
	}
	if state.Progress != "" {
		lines = append(lines, "progress: "+state.Progress)
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
	return strings.Join(append([]string{
		"Local code review",
		"target: " + renderTarget(result.Target),
		"phase: " + string(phase),
		"",
	}, renderResultBody(result)...), "\n")
}

func renderTarget(target Target) string {
	value := string(target.Kind)
	if target.Ref != "" {
		value += " " + target.Ref
	}
	if target.Instructions != "" {
		value += "\ninstructions: " + target.Instructions
	}
	return value
}

func renderResultBody(result Result) []string {
	lines := []string{"summary: " + result.Summary}
	if result.EvidenceGeneration != "" {
		lines = append(lines, "evidence: "+result.EvidenceGeneration)
	}
	if result.ResolvedRevision != "" {
		lines = append(lines, "revision: "+result.ResolvedRevision)
	}
	if result.MergeBase != "" {
		lines = append(lines, "merge base: "+result.MergeBase)
	}
	lines = append(lines, fmt.Sprintf("findings: %d", len(result.Findings)))
	if result.Stale {
		lines = append(lines, "stale: true")
	}
	if result.Truncated {
		lines = append(lines, "truncated: true")
	}
	if result.Diagnostic != "" {
		lines = append(lines, "diagnostic: "+result.Diagnostic)
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
			finding.Severity,
			finding.Title,
		))
		lines = append(lines, "   location: "+renderLocation(finding))
		lines = append(lines, fmt.Sprintf("   confidence: %.2f", finding.Confidence))
		lines = append(lines, "   "+finding.Explanation)
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
			return fmt.Sprintf("%s:%d", finding.Path, finding.StartLine)
		}
		return fmt.Sprintf("%s:%d-%d", finding.Path, finding.StartLine, finding.EndLine)
	case LocationStale:
		if finding.Path != "" {
			return finding.Path + " (stale; no current line claimed)"
		}
		return "stale; no current location claimed"
	default:
		return "unlocated"
	}
}
