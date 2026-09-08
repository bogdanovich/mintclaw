package toolshared

import (
	"strings"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

const (
	maxRepositoryDiffFiles      = 64
	maxRepositoryDiffHunks      = 256
	maxRepositoryDiffLines      = 1024
	maxRepositoryDiffTextBytes  = 64 << 10
	maxRepositoryDiffValueBytes = 4 << 10
	maxRepositoryDiffLineBytes  = 8 << 10
)

// RepositoryDiffObservation is an immutable, bounded copy of one passive
// repository observation. It describes what was observed at one tool-call
// boundary; it never asserts that the current turn authored those changes.
type RepositoryDiffObservation struct {
	Diff codingworkspace.DiffResult
}

// NewRepositoryDiffObservation creates the safe typed observation attached to
// one repository_diff result. Invalid evidence fails closed.
func NewRepositoryDiffObservation(diff codingworkspace.DiffResult) *ToolObservation {
	return SanitizeToolObservation(&ToolObservation{
		RepositoryDiff: &RepositoryDiffObservation{Diff: diff},
	})
}

type repositoryDiffBudget struct {
	remaining int
	hunks     int
	lines     int
	truncated bool
}

func newRepositoryDiffBudget() *repositoryDiffBudget {
	return &repositoryDiffBudget{
		remaining: maxRepositoryDiffTextBytes,
		hunks:     maxRepositoryDiffHunks,
		lines:     maxRepositoryDiffLines,
	}
}

func (budget *repositoryDiffBudget) text(value string, maximum int) string {
	if value == "" {
		return ""
	}
	if budget.remaining <= 0 {
		budget.truncated = true
		return ""
	}
	maximum = min(maximum, budget.remaining)
	bounded, truncated := sanitizeObservationText(value, maximum)
	budget.remaining -= len(bounded)
	budget.truncated = budget.truncated || truncated
	return bounded
}

func sanitizeRepositoryDiffObservation(
	observation RepositoryDiffObservation,
) (RepositoryDiffObservation, bool) {
	diff := observation.Diff.Clone()
	if diff.SchemaVersion != codingworkspace.RepositoryDiffSchemaV1 || !validRepositoryDiffTarget(diff.Target) ||
		!validRepositoryDiffCounts(diff) {
		return RepositoryDiffObservation{}, false
	}

	budget := newRepositoryDiffBudget()
	diff.Target.Ref = budget.text(strings.TrimSpace(diff.Target.Ref), maxRepositoryDiffValueBytes)
	diff.ResolvedRevision = budget.text(diff.ResolvedRevision, maxRepositoryDiffValueBytes)
	diff.MergeBase = budget.text(diff.MergeBase, maxRepositoryDiffValueBytes)
	diff.Head = budget.text(diff.Head, maxRepositoryDiffValueBytes)
	diff.Branch = budget.text(diff.Branch, maxRepositoryDiffValueBytes)
	diff.Generation = budget.text(diff.Generation, maxRepositoryDiffValueBytes)
	diff.EvidenceGeneration = budget.text(diff.EvidenceGeneration, maxRepositoryDiffValueBytes)
	diff.UnavailableReason = budget.text(diff.UnavailableReason, maxRepositoryDiffValueBytes)
	diff.Warning = budget.text(diff.Warning, maxRepositoryDiffValueBytes)
	diff.BaselineID = budget.text(diff.BaselineID, maxRepositoryDiffValueBytes)
	if !validRepositoryDiffTarget(diff.Target) {
		return RepositoryDiffObservation{}, false
	}

	files := diff.Files
	diff.Files = make([]codingworkspace.DiffFile, 0, min(len(files), maxRepositoryDiffFiles))
	if len(files) > maxRepositoryDiffFiles {
		budget.truncated = true
	}
	for index := range min(len(files), maxRepositoryDiffFiles) {
		file, ok := sanitizeRepositoryDiffFile(files[index], budget)
		if !ok {
			return RepositoryDiffObservation{}, false
		}
		if file.Path == "" {
			budget.truncated = true
			break
		}
		diff.Files = append(diff.Files, file)
	}

	if diff.Provenance != nil {
		provenance, ok := sanitizeRepositoryDiffProvenance(*diff.Provenance, budget)
		if !ok {
			return RepositoryDiffObservation{}, false
		}
		diff.Provenance = &provenance
	}
	diff.Truncated = diff.Truncated || budget.truncated
	return RepositoryDiffObservation{Diff: diff}, true
}

func validRepositoryDiffTarget(target codingworkspace.DiffTarget) bool {
	switch target.Kind {
	case codingworkspace.DiffTargetCurrent:
		return strings.TrimSpace(target.Ref) == ""
	case codingworkspace.DiffTargetBase, codingworkspace.DiffTargetCommit:
		return strings.TrimSpace(target.Ref) != ""
	default:
		return false
	}
}

func validRepositoryDiffCounts(diff codingworkspace.DiffResult) bool {
	return diff.Additions >= 0 && diff.Deletions >= 0 && diff.BinaryFiles >= 0
}

func sanitizeRepositoryDiffFile(
	file codingworkspace.DiffFile,
	budget *repositoryDiffBudget,
) (codingworkspace.DiffFile, bool) {
	if file.Additions < 0 || file.Deletions < 0 || !validRepositoryDiffProvenance(file.Provenance) {
		return codingworkspace.DiffFile{}, false
	}
	file.Path = budget.text(file.Path, maxRepositoryDiffValueBytes)
	file.OriginalPath = budget.text(file.OriginalPath, maxRepositoryDiffValueBytes)
	file.Status = budget.text(file.Status, maxRepositoryDiffValueBytes)
	file.Omitted = budget.text(file.Omitted, maxRepositoryDiffValueBytes)
	file.ProvenanceReason = budget.text(file.ProvenanceReason, maxRepositoryDiffValueBytes)

	hunks := file.Hunks
	file.Hunks = make([]codingworkspace.DiffHunk, 0, min(len(hunks), budget.hunks))
	for _, source := range hunks {
		if budget.hunks <= 0 {
			file.Truncated = true
			budget.truncated = true
			break
		}
		if source.OldStart < 0 || source.OldLines < 0 || source.NewStart < 0 || source.NewLines < 0 {
			return codingworkspace.DiffFile{}, false
		}
		budget.hunks--
		hunk := source
		hunk.Header = budget.text(hunk.Header, maxRepositoryDiffValueBytes)
		lines := hunk.Lines
		hunk.Lines = make([]codingworkspace.DiffLine, 0, min(len(lines), budget.lines))
		for _, line := range lines {
			if budget.lines <= 0 || (budget.remaining <= 0 && line.Text != "") {
				hunk.Truncated = true
				file.Truncated = true
				budget.truncated = true
				break
			}
			if !validRepositoryDiffLine(line) {
				return codingworkspace.DiffFile{}, false
			}
			budget.lines--
			line.Text = budget.text(line.Text, maxRepositoryDiffLineBytes)
			hunk.Lines = append(hunk.Lines, line)
		}
		if len(hunk.Lines) < len(lines) {
			hunk.Truncated = true
			file.Truncated = true
		}
		file.Hunks = append(file.Hunks, hunk)
	}
	if len(file.Hunks) < len(hunks) {
		file.Truncated = true
	}
	budget.truncated = budget.truncated || file.Truncated
	return file, true
}

func validRepositoryDiffLine(line codingworkspace.DiffLine) bool {
	if line.OldLine < 0 || line.NewLine < 0 {
		return false
	}
	switch line.Kind {
	case "context", "addition", "deletion":
		return true
	default:
		return false
	}
}

func validRepositoryDiffProvenance(provenance codingworkspace.ProvenanceKind) bool {
	switch provenance {
	case "", codingworkspace.ProvenancePreExisting, codingworkspace.ProvenanceFirstObservedDuringThread,
		codingworkspace.ProvenanceResolvedSinceBaseline, codingworkspace.ProvenanceIndeterminate:
		return true
	default:
		return false
	}
}

func sanitizeRepositoryDiffProvenance(
	provenance codingworkspace.ProvenanceResult,
	budget *repositoryDiffBudget,
) (codingworkspace.ProvenanceResult, bool) {
	provenance.BaselineID = budget.text(provenance.BaselineID, maxRepositoryDiffValueBytes)
	provenance.CurrentGeneration = budget.text(provenance.CurrentGeneration, maxRepositoryDiffValueBytes)
	provenance.CurrentEvidenceGeneration = budget.text(
		provenance.CurrentEvidenceGeneration,
		maxRepositoryDiffValueBytes,
	)
	provenance.Reason = budget.text(provenance.Reason, maxRepositoryDiffValueBytes)
	paths := provenance.Paths
	provenance.Paths = make([]codingworkspace.ProvenancePath, 0, min(len(paths), maxRepositoryDiffFiles))
	if len(paths) > maxRepositoryDiffFiles {
		budget.truncated = true
	}
	for index := range min(len(paths), maxRepositoryDiffFiles) {
		path := paths[index]
		if !validRepositoryDiffProvenance(path.Provenance) || path.Provenance == "" {
			return codingworkspace.ProvenanceResult{}, false
		}
		path.Path = budget.text(path.Path, maxRepositoryDiffValueBytes)
		path.OriginalPath = budget.text(path.OriginalPath, maxRepositoryDiffValueBytes)
		path.Status = budget.text(path.Status, maxRepositoryDiffValueBytes)
		path.Reason = budget.text(path.Reason, maxRepositoryDiffValueBytes)
		if path.Path == "" {
			budget.truncated = true
			break
		}
		provenance.Paths = append(provenance.Paths, path)
	}
	return provenance, true
}
