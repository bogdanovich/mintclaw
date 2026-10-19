// Package taskresult defines the result contract shared by tool execution,
// durable tasks, interactions, and presentation code.
package taskresult

import (
	"encoding/json"
	"strings"
)

const ReportSchemaV1 = "deliverable_report.v1"

// MaxStandaloneResultOutputBytes bounds the machine-readable result projected
// onto an authenticated outbound protocol message.
const MaxStandaloneResultOutputBytes = 64 * 1024

const (
	ObjectiveKindResult         = "result"
	ObjectiveKindExternalAction = "external_action"
	ObjectiveKindLiveHandoff    = "live_handoff"
	// ObjectiveKindResourceDisposition is an internal, bounded recovery
	// capability. It is never accepted from a caller-authored checklist.
	ObjectiveKindResourceDisposition = "resource_disposition"
	ReceiptKindResourceCleanup       = "resource_cleanup"
	ObjectiveRequirementRequired     = "required"
	ObjectiveRequirementIfNeeded     = "if_needed"
)

// NormalizeObjectiveRequirement admits conditional lifecycle work, not
// optional results or commits. Empty declarations preserve required semantics
// for existing callers and durable records.
func NormalizeObjectiveRequirement(requirement, kind string) (string, bool) {
	requirement = strings.TrimSpace(requirement)
	switch requirement {
	case "", ObjectiveRequirementRequired:
		return requirement, true
	case ObjectiveRequirementIfNeeded:
		return requirement, kind == ObjectiveKindLiveHandoff
	default:
		return "", false
	}
}

// Deliverable describes what a task produced, independent from model context,
// user-facing wording, and delivery state.
type Deliverable struct {
	Text              string            `json:"text,omitempty"`
	Artifacts         []Artifact        `json:"artifacts,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	Report            *Report           `json:"report,omitempty"`
	ObjectiveOutcome  *Outcome          `json:"objective_outcome,omitempty"`
	LifecycleReceipts []Receipt         `json:"lifecycle_receipts,omitempty"`
}

// Artifact describes a concrete produced output. Ref may be a media:// ref,
// file path tag, external URL, or another stable runtime reference.
type Artifact struct {
	Ref         string `json:"ref"`
	LocalPath   string `json:"local_path,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Delivered   bool   `json:"delivered,omitempty"`
	// Recovery is runtime-owned delivery authority, never producer-authored text.
	Recovery *ArtifactRecovery `json:"recovery,omitempty"`
}

// ArtifactRecovery preserves an existing media-delivery prerequisite across a
// durable delegated result. Each artifact carries one non-recursive claim.
type ArtifactRecovery struct {
	Kind              string `json:"kind"`
	ArtifactRef       string `json:"artifact_ref"`
	MediaRef          string `json:"media_ref"`
	WorkspaceID       string `json:"workspace_id"`
	AgentID           string `json:"agent_id"`
	ActorID           string `json:"actor_id"`
	RouteID           string `json:"route_id"`
	SessionID         string `json:"session_id"`
	ToolCallID        string `json:"tool_call_id"`
	AuthorityKind     string `json:"authority_kind,omitempty"`
	OperationID       string `json:"operation_id,omitempty"`
	DomainDeliveryID  string `json:"domain_delivery_id,omitempty"`
	DomainJobID       string `json:"domain_job_id,omitempty"`
	DomainOwnerDigest string `json:"domain_owner_digest,omitempty"`
}

// CloneArtifactRecovery copies one runtime-owned delivery prerequisite.
func CloneArtifactRecovery(input *ArtifactRecovery) *ArtifactRecovery {
	if input == nil {
		return nil
	}
	out := *input
	return &out
}

func CloneArtifacts(input []Artifact) []Artifact {
	if input == nil {
		return nil
	}
	out := append([]Artifact(nil), input...)
	for index := range out {
		out[index].Recovery = CloneArtifactRecovery(out[index].Recovery)
		if out[index].Recovery != nil {
			out[index].LocalPath = ""
		}
	}
	return out
}

type OutcomeStatus string

const (
	OutcomeSucceeded OutcomeStatus = "succeeded"
	OutcomePartial   OutcomeStatus = "partial"
	OutcomeBlocked   OutcomeStatus = "blocked"
)

// Outcome records how much of the caller-declared objective was verified.
type Outcome struct {
	Status         OutcomeStatus `json:"status"`
	CompletedItems []Item        `json:"completed_items,omitempty"`
	MissingItems   []string      `json:"missing_items,omitempty"`
	NotNeededItems []string      `json:"not_needed_items,omitempty"`
	Explanation    string        `json:"explanation,omitempty"`
	// UserSummary is the producer's complete answer admitted only after the
	// runtime validates its status and coverage of the retained output facts.
	UserSummary string `json:"user_summary,omitempty"`
}

type Item struct {
	Item      string           `json:"item"`
	Kind      string           `json:"kind,omitempty"`
	Receipts  []Receipt        `json:"receipts,omitempty"`
	Output    *ObjectiveOutput `json:"output,omitempty"`
	ExactJSON bool             `json:"exact_json,omitempty"`
}

// ObjectiveAcceptance describes the machine-checkable shape a caller expects
// from a read-only result objective. An omitted acceptance accepts any
// non-empty standalone output.
type ObjectiveAcceptance struct {
	OutputKind     string   `json:"output_kind,omitempty"`
	RequiredFields []string `json:"required_fields,omitempty"`
	MinItems       int      `json:"min_items,omitempty"`
	ExactJSON      bool     `json:"exact_json,omitempty"`
}

// CloneObjectiveAcceptance returns a detached copy safe for runtime handoff.
func CloneObjectiveAcceptance(input *ObjectiveAcceptance) *ObjectiveAcceptance {
	if input == nil {
		return nil
	}
	return &ObjectiveAcceptance{
		OutputKind:     input.OutputKind,
		RequiredFields: append([]string(nil), input.RequiredFields...),
		MinItems:       input.MinItems,
		ExactJSON:      input.ExactJSON,
	}
}

// ObjectiveOutput is the standalone payload produced for one result
// objective. Records intentionally use string fields so the runtime can render
// and transport them without depending on task-specific schemas.
type ObjectiveOutput struct {
	Kind         string              `json:"kind"`
	Text         string              `json:"text,omitempty"`
	Records      []map[string]string `json:"records,omitempty"`
	ArtifactRefs []string            `json:"artifact_refs,omitempty"`
	Truncated    bool                `json:"truncated,omitempty"`
}

// Receipt is durable, non-sensitive runtime evidence that a verifiable
// objective transition completed. Protected resource content must not be
// stored here.
type Receipt struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Target   string            `json:"target,omitempty"`
	Action   string            `json:"action,omitempty"`
	Tool     string            `json:"tool,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Report is the canonical structured representation of a durable result.
type Report struct {
	SchemaVersion string            `json:"schema_version"`
	ReportID      string            `json:"report_id"`
	ContentHash   string            `json:"content_hash"`
	GeneratedAt   int64             `json:"generated_at"`
	Summary       string            `json:"summary,omitempty"`
	Claims        []Claim           `json:"claims,omitempty"`
	FieldDeltas   []FieldDelta      `json:"field_deltas,omitempty"`
	Provenance    map[string]string `json:"provenance,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type Claim struct {
	Kind       string            `json:"kind"`
	Text       string            `json:"text"`
	Confidence string            `json:"confidence,omitempty"`
	SourceRefs []string          `json:"source_refs,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type FieldDelta struct {
	Field string `json:"field"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// CloneDeliverable returns a deep copy safe for storage or concurrent use.
func CloneDeliverable(input *Deliverable) *Deliverable {
	if input == nil {
		return nil
	}
	out := &Deliverable{
		Text:              input.Text,
		Artifacts:         CloneArtifacts(input.Artifacts),
		Metadata:          cloneStringMap(input.Metadata),
		Report:            CloneReport(input.Report),
		ObjectiveOutcome:  CloneOutcome(input.ObjectiveOutcome),
		LifecycleReceipts: CloneReceipts(input.LifecycleReceipts),
	}
	return out
}

// CloneOutcome returns a deep copy safe for storage or concurrent use.
func CloneOutcome(input *Outcome) *Outcome {
	if input == nil {
		return nil
	}
	out := &Outcome{
		Status:         input.Status,
		MissingItems:   append([]string(nil), input.MissingItems...),
		NotNeededItems: append([]string(nil), input.NotNeededItems...),
		Explanation:    input.Explanation,
		UserSummary:    input.UserSummary,
	}
	for _, item := range input.CompletedItems {
		cloned := Item{
			Item: item.Item, Kind: item.Kind, Output: CloneObjectiveOutput(item.Output),
			ExactJSON: item.ExactJSON,
		}
		for _, receipt := range item.Receipts {
			receipt.Metadata = cloneStringMap(receipt.Metadata)
			cloned.Receipts = append(cloned.Receipts, receipt)
		}
		out.CompletedItems = append(out.CompletedItems, cloned)
	}
	return out
}

// CloneObjectiveOutput returns a detached copy safe for storage or concurrent use.
func CloneObjectiveOutput(input *ObjectiveOutput) *ObjectiveOutput {
	if input == nil {
		return nil
	}
	out := &ObjectiveOutput{
		Kind:         input.Kind,
		Text:         input.Text,
		ArtifactRefs: append([]string(nil), input.ArtifactRefs...),
		Truncated:    input.Truncated,
	}
	for _, record := range input.Records {
		cloned := make(map[string]string, len(record))
		for key, value := range record {
			cloned[key] = value
		}
		out.Records = append(out.Records, cloned)
	}
	return out
}

// NormalizeObjectiveOutput validates and canonicalizes one standalone output.
// Only the payload for the selected kind is admitted so callers can safely
// transport the result without inferring semantics from loosely shaped JSON.
func NormalizeObjectiveOutput(
	input *ObjectiveOutput,
	acceptance *ObjectiveAcceptance,
) (*ObjectiveOutput, string) {
	if input == nil {
		return nil, "standalone objective output was required"
	}
	output := CloneObjectiveOutput(input)
	output.Kind = strings.TrimSpace(output.Kind)
	output.Text = strings.TrimSpace(output.Text)
	if output.Truncated {
		return nil, "standalone objective output was truncated"
	}
	if acceptance != nil && output.Kind != acceptance.OutputKind {
		return nil, "output kind did not match the declared acceptance contract"
	}
	switch output.Kind {
	case "text":
		if output.Text == "" {
			return nil, "standalone text output was required"
		}
		if len(output.Records) > 0 || len(output.ArtifactRefs) > 0 {
			return nil, "text output contained fields for a different output kind"
		}
		if acceptance != nil && acceptance.ExactJSON && !json.Valid([]byte(output.Text)) {
			return nil, "text output was not valid exact JSON"
		}
	case "records":
		if output.Text != "" || len(output.ArtifactRefs) > 0 {
			return nil, "record output contained fields for a different output kind"
		}
		if len(output.Records) == 0 && acceptance == nil {
			return nil, "at least one standalone record was required"
		}
		if len(output.Records) > 1024 {
			return nil, "record output exceeded the runtime item limit"
		}
		if acceptance != nil && len(output.Records) < acceptance.MinItems {
			return nil, "record output did not meet the declared minimum item count"
		}
		for _, record := range output.Records {
			if len(record) == 0 || len(record) > 64 {
				return nil, "each record must contain between 1 and 64 fields"
			}
			normalizedRecord := make(map[string]string, len(record))
			for key, value := range record {
				trimmedKey := strings.TrimSpace(key)
				trimmedValue := strings.TrimSpace(value)
				if trimmedKey == "" || trimmedValue == "" || len([]rune(trimmedKey)) > 64 ||
					len([]rune(trimmedValue)) > 4096 {
					return nil, "record fields require bounded non-empty names and values"
				}
				if _, duplicate := normalizedRecord[trimmedKey]; duplicate {
					return nil, "record output contained duplicate normalized field names"
				}
				normalizedRecord[trimmedKey] = trimmedValue
			}
			if acceptance != nil {
				for _, field := range acceptance.RequiredFields {
					if strings.TrimSpace(normalizedRecord[field]) == "" {
						return nil, "record output omitted a declared required field"
					}
				}
			}
			for key := range record {
				delete(record, key)
			}
			for key, value := range normalizedRecord {
				record[key] = value
			}
		}
	case "artifact":
		if output.Text != "" || len(output.Records) > 0 {
			return nil, "artifact output contained fields for a different output kind"
		}
		if len(output.ArtifactRefs) == 0 || len(output.ArtifactRefs) > 64 {
			return nil, "at least one bounded artifact reference was required"
		}
		for index, ref := range output.ArtifactRefs {
			ref = strings.TrimSpace(ref)
			if ref == "" || len([]rune(ref)) > 2048 {
				return nil, "artifact references must be bounded and non-empty"
			}
			output.ArtifactRefs[index] = ref
		}
	default:
		return nil, "output kind must be text, records, or artifact"
	}
	return output, ""
}

// StandaloneResultOutput returns the one machine-validated result payload that
// can be projected independently from presentation text after the whole outcome
// succeeds. Other completed objectives must carry their verified runtime receipts;
// receipt ownership and completion remain the authoritative producer's concern.
// Ambiguous results, incomplete outcomes, and oversized values are not projected.
func StandaloneResultOutput(input *Deliverable) *ObjectiveOutput {
	if input == nil || input.ObjectiveOutcome == nil ||
		input.ObjectiveOutcome.Status != OutcomeSucceeded ||
		len(input.ObjectiveOutcome.MissingItems) != 0 {
		return nil
	}
	var result *ObjectiveOutput
	for _, item := range input.ObjectiveOutcome.CompletedItems {
		switch item.Kind {
		case ObjectiveKindResult:
			if result != nil || item.Output == nil {
				return nil
			}
			result = item.Output
		case ObjectiveKindExternalAction, ObjectiveKindLiveHandoff:
			if item.Output != nil || len(item.Receipts) == 0 {
				return nil
			}
			for _, receipt := range item.Receipts {
				if strings.TrimSpace(receipt.ID) == "" || receipt.Kind != item.Kind {
					return nil
				}
			}
		default:
			return nil
		}
	}
	if result == nil {
		return nil
	}
	output, reason := NormalizeObjectiveOutput(result, nil)
	if reason != "" {
		return nil
	}
	encoded, err := json.Marshal(output)
	if err != nil || len(encoded) > MaxStandaloneResultOutputBytes {
		return nil
	}
	return output
}

// CloneReceipts returns detached receipts safe for storage or concurrent use.
func CloneReceipts(input []Receipt) []Receipt {
	if len(input) == 0 {
		return nil
	}
	out := make([]Receipt, len(input))
	for index, receipt := range input {
		out[index] = receipt
		out[index].Metadata = cloneStringMap(receipt.Metadata)
	}
	return out
}

// CloneReport returns a deep copy safe for storage or concurrent use.
func CloneReport(input *Report) *Report {
	if input == nil {
		return nil
	}
	out := &Report{
		SchemaVersion: input.SchemaVersion,
		ReportID:      input.ReportID,
		ContentHash:   input.ContentHash,
		GeneratedAt:   input.GeneratedAt,
		Summary:       input.Summary,
		FieldDeltas:   append([]FieldDelta(nil), input.FieldDeltas...),
		Provenance:    cloneStringMap(input.Provenance),
		Metadata:      cloneStringMap(input.Metadata),
	}
	for _, claim := range input.Claims {
		out.Claims = append(out.Claims, Claim{
			Kind:       claim.Kind,
			Text:       claim.Text,
			Confidence: claim.Confidence,
			SourceRefs: append([]string(nil), claim.SourceRefs...),
			Metadata:   cloneStringMap(claim.Metadata),
		})
	}
	return out
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
