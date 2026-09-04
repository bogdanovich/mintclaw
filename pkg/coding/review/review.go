// Package review defines bounded, frontend-neutral local code-review contracts.
package review

import (
	"fmt"
	"math"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

const (
	SchemaVersion          = 1
	MaxInstructionsBytes   = 16 << 10
	MaxSummaryBytes        = 64 << 10
	MaxExplanationBytes    = 16 << 10
	MaxTitleBytes          = 512
	MaxIdentityBytes       = 512
	MaxFindings            = 100
	MaxFindingLocationSpan = 20
	MaxResultBytes         = 2 << 20
)

type TargetKind string

const (
	TargetCurrent TargetKind = "current"
	TargetBase    TargetKind = "base"
	TargetCommit  TargetKind = "commit"
)

// Target selects one frozen repository evidence scope. Instructions refine
// that scope; they never replace it or grant additional capabilities.
type Target struct {
	Kind         TargetKind `json:"kind"`
	Ref          string     `json:"ref,omitempty"`
	Instructions string     `json:"instructions,omitempty"`
}

func (target Target) Validate() error {
	if err := validateOptionalText("review instructions", target.Instructions, MaxInstructionsBytes); err != nil {
		return err
	}
	switch target.Kind {
	case TargetCurrent:
		if target.Ref != "" {
			return fmt.Errorf("current review target cannot include a ref")
		}
	case TargetBase, TargetCommit:
		if err := validateRequiredText("review ref", target.Ref, MaxIdentityBytes); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported review target %q", target.Kind)
	}
	return nil
}

func (target Target) DiffTarget() codingworkspace.DiffTarget {
	return codingworkspace.DiffTarget{Kind: codingworkspace.DiffTargetKind(target.Kind), Ref: target.Ref}
}

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityMajor    Severity = "major"
	SeverityMinor    Severity = "minor"
)

type LocationState string

const (
	LocationCurrent   LocationState = "current"
	LocationStale     LocationState = "stale"
	LocationUnlocated LocationState = "unlocated"
)

// Finding is one actionable issue. A current location is admitted only after
// it has been proven to overlap a changed line in the frozen evidence.
type Finding struct {
	Severity      Severity      `json:"severity"`
	Title         string        `json:"title"`
	Explanation   string        `json:"explanation"`
	Confidence    float64       `json:"confidence"`
	LocationState LocationState `json:"location_state"`
	Path          string        `json:"path,omitempty"`
	StartLine     int           `json:"start_line,omitempty"`
	EndLine       int           `json:"end_line,omitempty"`
}

func (finding Finding) Validate() error {
	switch finding.Severity {
	case SeverityCritical, SeverityMajor, SeverityMinor:
	default:
		return fmt.Errorf("unsupported review finding severity %q", finding.Severity)
	}
	if err := validateRequiredText("review finding title", finding.Title, MaxTitleBytes); err != nil {
		return err
	}
	if err := validateRequiredText("review finding explanation", finding.Explanation, MaxExplanationBytes); err != nil {
		return err
	}
	if math.IsNaN(finding.Confidence) || math.IsInf(finding.Confidence, 0) ||
		finding.Confidence < 0 || finding.Confidence > 1 {
		return fmt.Errorf("review finding confidence must be between 0 and 1")
	}
	switch finding.LocationState {
	case LocationCurrent:
		if err := validateProjectPath(finding.Path); err != nil {
			return err
		}
		if finding.StartLine < 1 || finding.EndLine < finding.StartLine ||
			finding.EndLine-finding.StartLine+1 > MaxFindingLocationSpan {
			return fmt.Errorf("current review finding requires a bounded inclusive line range")
		}
	case LocationStale:
		if finding.Path != "" {
			if err := validateProjectPath(finding.Path); err != nil {
				return err
			}
		}
		if finding.StartLine != 0 || finding.EndLine != 0 {
			return fmt.Errorf("stale review finding cannot claim a current line range")
		}
	case LocationUnlocated:
		if finding.Path != "" || finding.StartLine != 0 || finding.EndLine != 0 {
			return fmt.Errorf("unlocated review finding cannot claim a path or line range")
		}
	default:
		return fmt.Errorf("unsupported review finding location state %q", finding.LocationState)
	}
	return nil
}

// Result is one immutable completed review item. Interrupted reviews do not
// synthesize a successful result.
type Result struct {
	SchemaVersion      int       `json:"schema_version"`
	ReviewID           string    `json:"review_id"`
	Target             Target    `json:"target"`
	EvidenceGeneration string    `json:"evidence_generation,omitempty"`
	ResolvedRevision   string    `json:"resolved_revision,omitempty"`
	MergeBase          string    `json:"merge_base,omitempty"`
	Summary            string    `json:"summary"`
	Findings           []Finding `json:"findings,omitempty"`
	Stale              bool      `json:"stale,omitempty"`
	Truncated          bool      `json:"truncated,omitempty"`
	Diagnostic         string    `json:"diagnostic,omitempty"`
	CompletedAt        time.Time `json:"completed_at"`
}

type Phase string

const (
	PhaseEntered     Phase = "entered"
	PhaseProgress    Phase = "progress"
	PhaseCompleted   Phase = "completed"
	PhaseInterrupted Phase = "interrupted"
	PhaseStale       Phase = "stale"
)

type EventKind string

const (
	EventProgress EventKind = "progress"
	EventFinding  EventKind = "finding"
)

// Event is an intermediate executor observation. Completion and interruption
// are owned by the controller, not trusted to an executor callback.
type Event struct {
	Kind     EventKind `json:"kind"`
	Progress string    `json:"progress,omitempty"`
	Finding  *Finding  `json:"finding,omitempty"`
}

func (event Event) Validate() error {
	switch event.Kind {
	case EventProgress:
		if event.Finding != nil {
			return fmt.Errorf("review progress event cannot contain a finding")
		}
		return validateRequiredText("review progress", event.Progress, MaxTitleBytes)
	case EventFinding:
		if event.Progress != "" || event.Finding == nil {
			return fmt.Errorf("review finding event requires exactly one finding")
		}
		return event.Finding.Validate()
	default:
		return fmt.Errorf("unsupported review event %q", event.Kind)
	}
}

// State is the bounded current review projection shared by frontends.
type State struct {
	ReviewID           string    `json:"review_id"`
	Target             Target    `json:"target"`
	Phase              Phase     `json:"phase"`
	EvidenceGeneration string    `json:"evidence_generation,omitempty"`
	Progress           string    `json:"progress,omitempty"`
	Findings           []Finding `json:"findings,omitempty"`
	Result             *Result   `json:"result,omitempty"`
}

func NewID() string { return uuid.NewString() }

func ValidateID(value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return fmt.Errorf("review ID must be a canonical UUID")
	}
	return nil
}

func (result Result) Validate() error {
	if result.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported review result schema %d", result.SchemaVersion)
	}
	if err := ValidateID(result.ReviewID); err != nil {
		return err
	}
	if err := result.Target.Validate(); err != nil {
		return err
	}
	if err := result.validateEvidenceIdentity(); err != nil {
		return err
	}
	if err := validateRequiredText("review summary", result.Summary, MaxSummaryBytes); err != nil {
		return err
	}
	if err := validateOptionalText("review diagnostic", result.Diagnostic, MaxExplanationBytes); err != nil {
		return err
	}
	if result.CompletedAt.IsZero() || result.CompletedAt.Location() != time.UTC {
		return fmt.Errorf("review completion time must be a nonzero UTC timestamp")
	}
	if len(result.Findings) > MaxFindings {
		return fmt.Errorf("review result exceeds %d findings", MaxFindings)
	}
	for index, finding := range result.Findings {
		if err := finding.Validate(); err != nil {
			return fmt.Errorf("review finding %d: %w", index, err)
		}
		if result.Stale && finding.LocationState == LocationCurrent {
			return fmt.Errorf("stale review result cannot contain a current finding location")
		}
	}
	return nil
}

func (result Result) validateEvidenceIdentity() error {
	require := func(label, value string) error {
		return validateRequiredText(label, value, MaxIdentityBytes)
	}
	reject := func(label, value string) error {
		if value != "" {
			return fmt.Errorf("%s is not valid for %s review evidence", label, result.Target.Kind)
		}
		return nil
	}

	switch result.Target.Kind {
	case TargetCurrent:
		if err := require("review evidence generation", result.EvidenceGeneration); err != nil {
			return err
		}
		if err := reject("resolved revision", result.ResolvedRevision); err != nil {
			return err
		}
		return reject("merge base", result.MergeBase)
	case TargetBase:
		if err := require("review evidence generation", result.EvidenceGeneration); err != nil {
			return err
		}
		if err := require("review resolved revision", result.ResolvedRevision); err != nil {
			return err
		}
		return require("review merge base", result.MergeBase)
	case TargetCommit:
		if err := reject("evidence generation", result.EvidenceGeneration); err != nil {
			return err
		}
		if err := require("review resolved revision", result.ResolvedRevision); err != nil {
			return err
		}
		return reject("merge base", result.MergeBase)
	default:
		return fmt.Errorf("unsupported review target %q", result.Target.Kind)
	}
}

func (result Result) Clone() Result {
	result.Findings = append([]Finding(nil), result.Findings...)
	return result
}

func (state State) Clone() State {
	state.Findings = append([]Finding(nil), state.Findings...)
	if state.Result != nil {
		result := state.Result.Clone()
		state.Result = &result
	}
	return state
}

// ValidateAgainstFrozenDiff proves that all current locations belong to the
// exact evidence generation and overlap lines added by the reviewed change.
func (result Result) ValidateAgainstFrozenDiff(diff codingworkspace.DiffResult) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if diff.SchemaVersion != codingworkspace.RepositoryDiffSchemaV1 {
		return fmt.Errorf("unsupported frozen diff schema %q", diff.SchemaVersion)
	}
	if !diff.RepositoryAvailable || diff.UnavailableReason != "" {
		return fmt.Errorf("review requires available frozen repository evidence")
	}
	if result.Target.DiffTarget() != diff.Target {
		return fmt.Errorf("review target does not match frozen diff target")
	}
	if result.EvidenceGeneration != diff.EvidenceGeneration ||
		result.ResolvedRevision != diff.ResolvedRevision || result.MergeBase != diff.MergeBase {
		return fmt.Errorf("review evidence identity does not match frozen diff")
	}
	if diff.Stale && !result.Stale {
		return fmt.Errorf("review over stale evidence must be marked stale")
	}
	if frozenDiffIncomplete(diff) && !result.Truncated {
		return fmt.Errorf("review over truncated evidence must be marked truncated")
	}
	for index, finding := range result.Findings {
		if finding.LocationState != LocationCurrent {
			continue
		}
		if !findingOverlapsChangedLine(finding, diff.Files) {
			return fmt.Errorf("review finding %d does not overlap a changed current line", index)
		}
	}
	return nil
}

func frozenDiffIncomplete(diff codingworkspace.DiffResult) bool {
	if diff.Truncated {
		return true
	}
	for _, file := range diff.Files {
		if file.Omitted != "" {
			return true
		}
	}
	return false
}

func findingOverlapsChangedLine(finding Finding, files []codingworkspace.DiffFile) bool {
	for _, file := range files {
		if file.Path != finding.Path {
			continue
		}
		for _, hunk := range file.Hunks {
			for _, line := range hunk.Lines {
				if line.Kind == "addition" && line.NewLine >= finding.StartLine && line.NewLine <= finding.EndLine {
					return true
				}
			}
		}
	}
	return false
}

func validateProjectPath(value string) error {
	if err := validateRequiredText("review finding path", value, MaxIdentityBytes); err != nil {
		return err
	}
	if value == "." || value != path.Clean(value) || strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "../") || strings.Contains(value, "\\") {
		return fmt.Errorf("review finding path must be a clean project-relative slash path")
	}
	return nil
}

func validateRequiredText(label, value string, limit int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", label)
	}
	return validateOptionalText(label, value, limit)
}

func validateOptionalText(label, value string, limit int) error {
	if value == "" {
		return nil
	}
	if value != strings.TrimSpace(value) || !utf8.ValidString(value) || len(value) > limit ||
		strings.IndexFunc(
			value,
			func(r rune) bool { return r == 0 || (unicode.IsControl(r) && r != '\n' && r != '\t') },
		) >= 0 {
		return fmt.Errorf("%s must be trimmed valid UTF-8 within %d bytes without unsafe controls", label, limit)
	}
	return nil
}
