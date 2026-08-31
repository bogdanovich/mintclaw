// Package taskresult defines the result contract shared by tool execution,
// durable tasks, interactions, and presentation code.
package taskresult

const ReportSchemaV1 = "deliverable_report.v1"

// Deliverable describes what a task produced, independent from model context,
// user-facing wording, and delivery state.
type Deliverable struct {
	Text             string            `json:"text,omitempty"`
	Artifacts        []Artifact        `json:"artifacts,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Report           *Report           `json:"report,omitempty"`
	ObjectiveOutcome *Outcome          `json:"objective_outcome,omitempty"`
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
	Explanation    string        `json:"explanation,omitempty"`
}

type Item struct {
	Item     string           `json:"item"`
	Kind     string           `json:"kind,omitempty"`
	Receipts []Receipt        `json:"receipts,omitempty"`
	Output   *ObjectiveOutput `json:"output,omitempty"`
}

// ObjectiveAcceptance describes the machine-checkable shape a caller expects
// from a read-only result objective. An omitted acceptance accepts any
// non-empty standalone output.
type ObjectiveAcceptance struct {
	OutputKind     string   `json:"output_kind,omitempty"`
	RequiredFields []string `json:"required_fields,omitempty"`
	MinItems       int      `json:"min_items,omitempty"`
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

// Receipt is durable, non-sensitive evidence that an external action reached
// a successful terminal state. Protected page content must not be stored here.
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
		Text:             input.Text,
		Artifacts:        append([]Artifact(nil), input.Artifacts...),
		Metadata:         cloneStringMap(input.Metadata),
		Report:           CloneReport(input.Report),
		ObjectiveOutcome: CloneOutcome(input.ObjectiveOutcome),
	}
	return out
}

// CloneOutcome returns a deep copy safe for storage or concurrent use.
func CloneOutcome(input *Outcome) *Outcome {
	if input == nil {
		return nil
	}
	out := &Outcome{
		Status:       input.Status,
		MissingItems: append([]string(nil), input.MissingItems...),
		Explanation:  input.Explanation,
	}
	for _, item := range input.CompletedItems {
		cloned := Item{Item: item.Item, Kind: item.Kind, Output: CloneObjectiveOutput(item.Output)}
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
