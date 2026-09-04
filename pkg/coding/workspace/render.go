package workspace

import (
	"fmt"
	"strings"
	"unicode"
)

func RenderStatusPlain(status StatusResult) string {
	snapshot := status.Snapshot
	lines := []string{"Repository status"}
	if !snapshot.Git.Available {
		lines = append(lines, "unavailable: "+displayText(snapshot.Git.UnavailableReason))
		return appendEvidenceDiagnostics(lines, snapshot.Truncated || status.Stale, snapshot.Warning, status.Provenance)
	}
	lines = append(lines,
		"root: "+displayText(snapshot.Git.TopLevel),
		"branch: "+displayBranch(snapshot.Git),
	)
	if status.BaselineID != "" {
		lines = append(lines, "baseline: "+displayText(status.BaselineID))
	}
	if !snapshot.Git.StatusAvailable {
		lines = append(lines, "state: unavailable")
	} else if snapshot.Git.Dirty {
		lines = append(lines, "state: dirty")
	} else {
		lines = append(lines, "state: clean")
	}
	if snapshot.DiffStatAvailable {
		lines = append(lines, fmt.Sprintf(
			"summary: %d files · +%d -%d · %d binary",
			snapshot.DiffStat.Files,
			snapshot.DiffStat.Additions,
			snapshot.DiffStat.Deletions,
			snapshot.DiffStat.BinaryFiles,
		))
	}
	provenance := provenanceByPath(status.Provenance)
	for _, changed := range snapshot.ChangedPaths {
		label := ""
		if observed, ok := provenance[evidencePathKey(changed.Status, changed.OriginalPath, changed.Path)]; ok {
			label = provenanceLabel(observed.Provenance, observed.Reason)
		}
		lines = append(lines, displayText(changed.Status)+" "+displayRename(changed.OriginalPath, changed.Path)+label)
	}
	return appendEvidenceDiagnostics(lines, snapshot.Truncated || status.Stale, snapshot.Warning, status.Provenance)
}

func RenderDiffPlain(diff DiffResult) string {
	target := displayText(string(diff.Target.Kind))
	if diff.Target.Ref != "" {
		target += " " + displayText(diff.Target.Ref)
	}
	lines := []string{"Repository diff (" + target + ")"}
	if diff.UnavailableReason != "" {
		lines = append(lines, "unavailable: "+displayText(diff.UnavailableReason))
		return appendEvidenceDiagnostics(lines, diff.Truncated || diff.Stale, diff.Warning, diff.Provenance)
	}
	if diff.ResolvedRevision != "" {
		lines = append(lines, "resolved: "+displayText(diff.ResolvedRevision))
	}
	if diff.MergeBase != "" {
		lines = append(lines, "merge base: "+displayText(diff.MergeBase))
	}
	if diff.BaselineID != "" {
		lines = append(lines, "baseline: "+displayText(diff.BaselineID))
	}
	lines = append(lines, fmt.Sprintf(
		"summary: %d files · +%d -%d · %d binary",
		len(diff.Files),
		diff.Additions,
		diff.Deletions,
		diff.BinaryFiles,
	))
	for _, file := range diff.Files {
		label := provenanceLabel(file.Provenance, file.ProvenanceReason)
		lines = append(lines, displayText(file.Status)+" "+displayRename(file.OriginalPath, file.Path)+label)
		if file.Binary {
			lines = append(lines, "  [binary]")
		}
		if file.Submodule {
			lines = append(lines, "  [submodule]")
		}
		if file.Omitted != "" {
			lines = append(lines, "  [omitted: "+displayText(file.Omitted)+"]")
		}
		for _, hunk := range file.Hunks {
			lines = append(lines, fmt.Sprintf(
				"  @@ -%d,%d +%d,%d @@ %s",
				hunk.OldStart,
				hunk.OldLines,
				hunk.NewStart,
				hunk.NewLines,
				displayText(hunk.Header),
			))
			for _, line := range hunk.Lines {
				lines = append(lines, renderDiffLine(line))
			}
			if hunk.Truncated {
				lines = append(lines, "  [hunk truncated]")
			}
		}
	}
	return appendEvidenceDiagnostics(lines, diff.Truncated || diff.Stale, diff.Warning, diff.Provenance)
}

func provenanceLabel(kind ProvenanceKind, reason string) string {
	if kind == "" {
		return ""
	}
	label := " [" + displayText(string(kind))
	if reason != "" {
		label += ": " + displayText(reason)
	}
	return label + "]"
}

func renderDiffLine(line DiffLine) string {
	switch line.Kind {
	case "addition":
		return fmt.Sprintf("  +%6d %s", line.NewLine, displayText(line.Text))
	case "deletion":
		return fmt.Sprintf("  -%6d %s", line.OldLine, displayText(line.Text))
	default:
		return fmt.Sprintf("   %6d %s", line.NewLine, displayText(line.Text))
	}
}

func provenanceByPath(provenance *ProvenanceResult) map[string]ProvenancePath {
	result := make(map[string]ProvenancePath)
	if provenance == nil {
		return result
	}
	for _, path := range provenance.Paths {
		result[evidencePathKey(path.Status, path.OriginalPath, path.Path)] = path
	}
	return result
}

func appendEvidenceDiagnostics(
	lines []string,
	incomplete bool,
	warning string,
	provenance *ProvenanceResult,
) string {
	if incomplete {
		lines = append(lines, "[evidence incomplete or stale]")
	}
	if provenance != nil && provenance.Indeterminate && provenance.Reason != "" {
		lines = append(lines, "provenance: indeterminate ("+displayText(provenance.Reason)+")")
	}
	if warning != "" {
		lines = append(lines, "warning: "+displayText(warning))
	}
	return strings.Join(lines, "\n")
}

func displayBranch(git GitState) string {
	switch {
	case git.Unborn && git.Branch != "":
		return displayText(git.Branch) + " (unborn)"
	case git.Detached && git.Head != "":
		return displayText(git.Head) + " (detached)"
	case git.Branch != "":
		return displayText(git.Branch)
	default:
		return "unknown"
	}
}

func displayRename(original, current string) string {
	if original == "" {
		return displayText(current)
	}
	return displayText(original) + " -> " + displayText(current)
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
