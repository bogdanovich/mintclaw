package worker

import (
	"strings"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

const (
	MaxRepositoryDiffFiles     = 64
	MaxRepositoryDiffHunks     = 256
	MaxRepositoryDiffLines     = 1024
	MaxRepositoryDiffTextBytes = 64 << 10
	MaxRepositoryDiffLineBytes = 8 << 10
)

type RepositoryDiffTarget struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
}

type RepositoryDiffProvenance struct {
	Indeterminate bool   `json:"indeterminate,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

type RepositoryDiffLine struct {
	Kind    string `json:"kind"`
	OldLine int    `json:"old_line,omitempty"`
	NewLine int    `json:"new_line,omitempty"`
	Text    string `json:"text"`
}

type RepositoryDiffHunk struct {
	OldStart  int                  `json:"old_start"`
	OldLines  int                  `json:"old_lines"`
	NewStart  int                  `json:"new_start"`
	NewLines  int                  `json:"new_lines"`
	Header    string               `json:"header,omitempty"`
	Lines     []RepositoryDiffLine `json:"lines,omitempty"`
	Truncated bool                 `json:"truncated,omitempty"`
}

type RepositoryDiffFile struct {
	Path             string               `json:"path"`
	OriginalPath     string               `json:"original_path,omitempty"`
	Status           string               `json:"status"`
	Binary           bool                 `json:"binary,omitempty"`
	Submodule        bool                 `json:"submodule,omitempty"`
	Symlink          bool                 `json:"symlink,omitempty"`
	Omitted          string               `json:"omitted,omitempty"`
	Additions        int                  `json:"additions,omitempty"`
	Deletions        int                  `json:"deletions,omitempty"`
	Hunks            []RepositoryDiffHunk `json:"hunks,omitempty"`
	Truncated        bool                 `json:"truncated,omitempty"`
	Provenance       string               `json:"provenance,omitempty"`
	ProvenanceReason string               `json:"provenance_reason,omitempty"`
}

// RepositoryDiff is the protocol-v1 renderer-neutral projection of one
// historical repository_diff tool observation. It intentionally excludes the
// duplicate per-path provenance index because each projected file carries its
// own provenance classification.
type RepositoryDiff struct {
	SchemaVersion       string                    `json:"schema_version"`
	Target              RepositoryDiffTarget      `json:"target"`
	ResolvedRevision    string                    `json:"resolved_revision,omitempty"`
	MergeBase           string                    `json:"merge_base,omitempty"`
	RepositoryAvailable bool                      `json:"repository_available"`
	Head                string                    `json:"head,omitempty"`
	Branch              string                    `json:"branch,omitempty"`
	Generation          string                    `json:"generation,omitempty"`
	EvidenceGeneration  string                    `json:"evidence_generation,omitempty"`
	Files               []RepositoryDiffFile      `json:"files,omitempty"`
	Additions           int                       `json:"additions,omitempty"`
	Deletions           int                       `json:"deletions,omitempty"`
	BinaryFiles         int                       `json:"binary_files,omitempty"`
	Truncated           bool                      `json:"truncated,omitempty"`
	Stale               bool                      `json:"stale,omitempty"`
	UnavailableReason   string                    `json:"unavailable_reason,omitempty"`
	Warning             string                    `json:"warning,omitempty"`
	BaselineID          string                    `json:"baseline_id,omitempty"`
	Provenance          *RepositoryDiffProvenance `json:"provenance,omitempty"`
}

type repositoryDiffWireBudget struct {
	remaining int
	hunks     int
	lines     int
	truncated bool
}

func repositoryDiffFromFrontend(source codingworkspace.DiffResult) (*RepositoryDiff, bool) {
	if source.SchemaVersion != codingworkspace.RepositoryDiffSchemaV1 || source.Additions < 0 ||
		source.Deletions < 0 || source.BinaryFiles < 0 || !validFrontendDiffTarget(source.Target) {
		return nil, true
	}
	budget := &repositoryDiffWireBudget{
		remaining: MaxRepositoryDiffTextBytes,
		hunks:     MaxRepositoryDiffHunks,
		lines:     MaxRepositoryDiffLines,
	}
	privateKeys := &diagnostictrace.PrivateKeyBlockRedactor{}
	targetRef, refTruncated := budget.structural(source.Target.Ref, MaxPathBytes)
	result := &RepositoryDiff{
		SchemaVersion:       codingworkspace.RepositoryDiffSchemaV1,
		Target:              RepositoryDiffTarget{Kind: string(source.Target.Kind), Ref: targetRef},
		RepositoryAvailable: source.RepositoryAvailable,
		Additions:           source.Additions,
		Deletions:           source.Deletions,
		BinaryFiles:         source.BinaryFiles,
		Truncated:           source.Truncated || refTruncated,
		Stale:               source.Stale,
	}
	result.ResolvedRevision, _ = budget.structural(source.ResolvedRevision, MaxPathBytes)
	result.MergeBase, _ = budget.structural(source.MergeBase, MaxPathBytes)
	result.Head, _ = budget.structural(source.Head, MaxPathBytes)
	result.Branch, _ = budget.structural(source.Branch, MaxPathBytes)
	result.Generation, _ = budget.structural(source.Generation, MaxPathBytes)
	result.EvidenceGeneration, _ = budget.structural(source.EvidenceGeneration, MaxPathBytes)
	result.UnavailableReason, _ = budget.content(source.UnavailableReason, MaxEventTextBytes)
	result.Warning, _ = budget.content(source.Warning, MaxEventTextBytes)
	result.BaselineID, _ = budget.structural(source.BaselineID, MaxPathBytes)

	result.Files = make([]RepositoryDiffFile, 0, min(len(source.Files), MaxRepositoryDiffFiles))
	if len(source.Files) > MaxRepositoryDiffFiles {
		budget.truncated = true
	}
	for index := range min(len(source.Files), MaxRepositoryDiffFiles) {
		file, ok := repositoryDiffFileFromFrontend(source.Files[index], budget, privateKeys)
		if !ok {
			return nil, true
		}
		if file.Path == "" {
			budget.truncated = true
			break
		}
		result.Files = append(result.Files, file)
	}
	if source.Provenance != nil {
		reason, truncated := budget.content(source.Provenance.Reason, MaxEventTextBytes)
		result.Provenance = &RepositoryDiffProvenance{
			Indeterminate: source.Provenance.Indeterminate,
			Reason:        reason,
		}
		budget.truncated = budget.truncated || truncated
	}
	if privateKeys.Open() {
		markLastWireRepositoryDiffHunkTruncated(result.Files)
		budget.truncated = true
	}
	result.Truncated = result.Truncated || budget.truncated
	return result, budget.truncated
}

func (budget *repositoryDiffWireBudget) structural(value string, maximum int) (string, bool) {
	if value == "" {
		return "", false
	}
	if budget.remaining <= 0 {
		budget.truncated = true
		return "", true
	}
	value, truncated := canonicalRepositoryDiffStructural(value, min(maximum, budget.remaining))
	budget.remaining -= len(value)
	budget.truncated = budget.truncated || truncated
	return value, truncated
}

func (budget *repositoryDiffWireBudget) content(value string, maximum int) (string, bool) {
	if value == "" {
		return "", false
	}
	if budget.remaining <= 0 {
		budget.truncated = true
		return "", true
	}
	value, truncated := canonicalRepositoryDiffContent(value, min(maximum, budget.remaining))
	budget.remaining -= len(value)
	budget.truncated = budget.truncated || truncated
	return value, truncated
}

func canonicalRepositoryDiffStructural(value string, maximum int) (string, bool) {
	original := value
	value = (diagnostictrace.Redactor{}).RedactText(value, maximum)
	value, normalized := boundedWireStructural(value, maximum)
	return value, normalized || value != original
}

func canonicalRepositoryDiffContent(value string, maximum int) (string, bool) {
	original := value
	value = (diagnostictrace.Redactor{}).RedactText(value, maximum)
	value, normalized := boundedWireContent(value, maximum)
	return value, normalized || value != original
}

func repositoryDiffFileFromFrontend(
	source codingworkspace.DiffFile,
	budget *repositoryDiffWireBudget,
	privateKeys *diagnostictrace.PrivateKeyBlockRedactor,
) (RepositoryDiffFile, bool) {
	if source.Additions < 0 || source.Deletions < 0 || !validFrontendDiffProvenance(source.Provenance) {
		return RepositoryDiffFile{}, false
	}
	path, _ := budget.structural(source.Path, MaxPathBytes)
	originalPath, _ := budget.structural(source.OriginalPath, MaxPathBytes)
	status, _ := budget.structural(source.Status, MaxAttachmentMeta)
	omitted, _ := budget.content(source.Omitted, MaxEventTextBytes)
	provenanceReason, _ := budget.content(source.ProvenanceReason, MaxEventTextBytes)
	file := RepositoryDiffFile{
		Path: path, OriginalPath: originalPath, Status: status,
		Binary: source.Binary, Submodule: source.Submodule, Symlink: source.Symlink,
		Omitted: omitted, Additions: source.Additions, Deletions: source.Deletions,
		Truncated: source.Truncated, Provenance: string(source.Provenance), ProvenanceReason: provenanceReason,
	}
	file.Hunks = make([]RepositoryDiffHunk, 0, min(len(source.Hunks), budget.hunks))
	for _, sourceHunk := range source.Hunks {
		if budget.hunks <= 0 {
			file.Truncated = true
			budget.truncated = true
			break
		}
		if sourceHunk.OldStart < 0 || sourceHunk.OldLines < 0 ||
			sourceHunk.NewStart < 0 || sourceHunk.NewLines < 0 {
			return RepositoryDiffFile{}, false
		}
		budget.hunks--
		header, _ := budget.content(sourceHunk.Header, MaxEventTextBytes)
		hunk := RepositoryDiffHunk{
			OldStart: sourceHunk.OldStart, OldLines: sourceHunk.OldLines,
			NewStart: sourceHunk.NewStart, NewLines: sourceHunk.NewLines,
			Header: header, Truncated: sourceHunk.Truncated,
		}
		hunk.Lines = make([]RepositoryDiffLine, 0, min(len(sourceHunk.Lines), budget.lines))
		for _, sourceLine := range sourceHunk.Lines {
			if budget.lines <= 0 || (budget.remaining <= 0 && sourceLine.Text != "") {
				hunk.Truncated = true
				file.Truncated = true
				budget.truncated = true
				break
			}
			if sourceLine.OldLine < 0 || sourceLine.NewLine < 0 || !validRepositoryDiffLineKind(sourceLine.Kind) {
				return RepositoryDiffFile{}, false
			}
			budget.lines--
			text, _ := privateKeys.RedactChunk(sourceLine.Text)
			text, truncated := budget.content(text, MaxRepositoryDiffLineBytes)
			hunk.Truncated = hunk.Truncated || truncated
			file.Truncated = file.Truncated || truncated
			hunk.Lines = append(hunk.Lines, RepositoryDiffLine{
				Kind: sourceLine.Kind, OldLine: sourceLine.OldLine, NewLine: sourceLine.NewLine, Text: text,
			})
		}
		if len(hunk.Lines) < len(sourceHunk.Lines) {
			hunk.Truncated = true
			file.Truncated = true
		}
		file.Hunks = append(file.Hunks, hunk)
	}
	if len(file.Hunks) < len(source.Hunks) {
		file.Truncated = true
		budget.truncated = true
	}
	return file, true
}

func markLastWireRepositoryDiffHunkTruncated(files []RepositoryDiffFile) {
	if len(files) == 0 {
		return
	}
	file := &files[len(files)-1]
	file.Truncated = true
	if len(file.Hunks) != 0 {
		file.Hunks[len(file.Hunks)-1].Truncated = true
	}
}

func validFrontendDiffTarget(target codingworkspace.DiffTarget) bool {
	if !validOptionalText(target.Ref, MaxPathBytes) || target.Ref != strings.TrimSpace(target.Ref) {
		return false
	}
	switch target.Kind {
	case codingworkspace.DiffTargetCurrent:
		return target.Ref == ""
	case codingworkspace.DiffTargetBase, codingworkspace.DiffTargetCommit:
		return target.Ref != ""
	default:
		return false
	}
}

func validFrontendDiffProvenance(provenance codingworkspace.ProvenanceKind) bool {
	switch provenance {
	case "", codingworkspace.ProvenancePreExisting, codingworkspace.ProvenanceFirstObservedDuringThread,
		codingworkspace.ProvenanceResolvedSinceBaseline, codingworkspace.ProvenanceIndeterminate:
		return true
	default:
		return false
	}
}

func validRepositoryDiff(diff RepositoryDiff) bool {
	if diff.SchemaVersion != codingworkspace.RepositoryDiffSchemaV1 || diff.Additions < 0 || diff.Deletions < 0 ||
		diff.BinaryFiles < 0 || len(diff.Files) > MaxRepositoryDiffFiles || !validRepositoryDiffTarget(diff.Target) ||
		!validCanonicalRepositoryDiffStructural(diff.ResolvedRevision, MaxPathBytes, false) ||
		!validCanonicalRepositoryDiffStructural(diff.MergeBase, MaxPathBytes, false) ||
		!validCanonicalRepositoryDiffStructural(diff.Head, MaxPathBytes, false) ||
		!validCanonicalRepositoryDiffStructural(diff.Branch, MaxPathBytes, false) ||
		!validCanonicalRepositoryDiffStructural(diff.Generation, MaxPathBytes, false) ||
		!validCanonicalRepositoryDiffStructural(diff.EvidenceGeneration, MaxPathBytes, false) ||
		!validCanonicalRepositoryDiffContent(diff.UnavailableReason, MaxEventTextBytes, false) ||
		!validCanonicalRepositoryDiffContent(diff.Warning, MaxEventTextBytes, false) ||
		!validCanonicalRepositoryDiffStructural(diff.BaselineID, MaxPathBytes, false) {
		return false
	}
	if diff.Provenance != nil &&
		!validCanonicalRepositoryDiffContent(diff.Provenance.Reason, MaxEventTextBytes, false) {
		return false
	}
	hunks, lines, textBytes := 0, 0, repositoryDiffTopLevelTextBytes(diff)
	privateKeys := &diagnostictrace.PrivateKeyBlockRedactor{}
	for _, file := range diff.Files {
		if !validCanonicalRepositoryDiffStructural(file.Path, MaxPathBytes, true) ||
			!validCanonicalRepositoryDiffStructural(file.OriginalPath, MaxPathBytes, false) ||
			!validCanonicalRepositoryDiffStructural(file.Status, MaxAttachmentMeta, false) ||
			!validCanonicalRepositoryDiffContent(file.Omitted, MaxEventTextBytes, false) ||
			file.Additions < 0 || file.Deletions < 0 ||
			!validWireDiffProvenance(file.Provenance) ||
			!validCanonicalRepositoryDiffContent(file.ProvenanceReason, MaxEventTextBytes, false) {
			return false
		}
		textBytes += len(file.Path) + len(file.OriginalPath) + len(file.Status) + len(file.Omitted) +
			len(file.ProvenanceReason)
		hunks += len(file.Hunks)
		for _, hunk := range file.Hunks {
			if hunk.OldStart < 0 || hunk.OldLines < 0 || hunk.NewStart < 0 || hunk.NewLines < 0 ||
				!validCanonicalRepositoryDiffContent(hunk.Header, MaxEventTextBytes, false) {
				return false
			}
			textBytes += len(hunk.Header)
			lines += len(hunk.Lines)
			for _, line := range hunk.Lines {
				if line.OldLine < 0 || line.NewLine < 0 || !validRepositoryDiffLineKind(line.Kind) ||
					!validCanonicalRepositoryDiffContent(line.Text, MaxRepositoryDiffLineBytes, false) {
					return false
				}
				if redacted, changed := privateKeys.RedactChunk(line.Text); changed || redacted != line.Text {
					return false
				}
				textBytes += len(line.Text)
			}
		}
	}
	return !privateKeys.Open() && hunks <= MaxRepositoryDiffHunks && lines <= MaxRepositoryDiffLines &&
		textBytes <= MaxRepositoryDiffTextBytes
}

func validRepositoryDiffTarget(target RepositoryDiffTarget) bool {
	if !validCanonicalRepositoryDiffStructural(target.Ref, MaxPathBytes, false) {
		return false
	}
	switch target.Kind {
	case string(codingworkspace.DiffTargetCurrent):
		return target.Ref == ""
	case string(codingworkspace.DiffTargetBase), string(codingworkspace.DiffTargetCommit):
		return strings.TrimSpace(target.Ref) != ""
	default:
		return false
	}
}

func validCanonicalRepositoryDiffStructural(value string, maximum int, required bool) bool {
	if !validOptionalText(value, maximum) || (required && value == "") {
		return false
	}
	canonical, _ := canonicalRepositoryDiffStructural(value, maximum)
	return canonical == value
}

func validCanonicalRepositoryDiffContent(value string, maximum int, required bool) bool {
	if !validContentText(value, maximum, required) {
		return false
	}
	canonical, _ := canonicalRepositoryDiffContent(value, maximum)
	return canonical == value
}

func validRepositoryDiffLineKind(kind string) bool {
	return kind == "context" || kind == "addition" || kind == "deletion"
}

func validWireDiffProvenance(provenance string) bool {
	switch provenance {
	case "", string(codingworkspace.ProvenancePreExisting),
		string(codingworkspace.ProvenanceFirstObservedDuringThread),
		string(codingworkspace.ProvenanceResolvedSinceBaseline), string(codingworkspace.ProvenanceIndeterminate):
		return true
	default:
		return false
	}
}

func repositoryDiffTopLevelTextBytes(diff RepositoryDiff) int {
	total := len(diff.Target.Ref) + len(diff.ResolvedRevision) + len(diff.MergeBase) + len(diff.Head) +
		len(diff.Branch) + len(diff.Generation) + len(diff.EvidenceGeneration) + len(diff.UnavailableReason) +
		len(diff.Warning) + len(diff.BaselineID)
	if diff.Provenance != nil {
		total += len(diff.Provenance.Reason)
	}
	return total
}
