package document

import "github.com/bogdanovich/mintclaw/pkg/interactions"

const (
	FormIntakeVersion             = "document.form_intake.v1"
	MaxFormIntakeInputBytes       = 16 * 1024
	MaxFormIntakePendingInputs    = 4
	MaxFormIntakeRetainedBytes    = 64 * 1024
	MaxFormIntakeExtractionPasses = 32
	MaxFormIntakeCandidates       = 8
	MaxFormIntakeReceipts         = 256
	maxFormIntakeRoleContextRunes = 768
	formIntakeBindingKind         = "intake_binding"
	formIntakeReceiptPrefix       = "form_intake."
)

type FormIntakeState string

const (
	FormIntakeActive FormIntakeState = "active"
	FormIntakePaused FormIntakeState = "paused"
	FormIntakeClosed FormIntakeState = "closed"
	// Suspended preserves a draft without capturing the route's next task.
	FormIntakeSuspended FormIntakeState = "suspended"
)

// FormIntakeContext is bounded, value-free presentation chosen by the agent.
// It is not native semantic planning or assignment authority.
type FormIntakeContext struct {
	CandidateFieldIDs []string `json:"candidate_field_ids"`
	FocusFieldID      string   `json:"focus_field_id,omitempty"`
	RoleContext       string   `json:"role_context,omitempty"`
	Question          string   `json:"question"`
	Language          string   `json:"language,omitempty"`
}

type FormIntakeStartRequest struct {
	JobID            string
	ExpectedRevision int64
	Owner            FormJobOwner
	Schema           FormFieldsFacts
	Context          FormIntakeContext
	IdempotencyKey   string
}

// FormIntakeProgress never includes raw input, interpreted facts, or hashes of
// personal values. Receipts identify authenticated transport inputs only.
type FormIntakeProgress struct {
	JobID         string            `json:"job_id"`
	ModeID        string            `json:"mode_id"`
	Revision      int64             `json:"revision"`
	State         FormIntakeState   `json:"state"`
	PendingInputs int               `json:"pending_inputs"`
	RetainedBytes int               `json:"retained_bytes"`
	InputCount    int               `json:"input_count"`
	PendingTasks  int               `json:"pending_tasks,omitempty"`
	TaskBytes     int               `json:"task_retained_bytes,omitempty"`
	Context       FormIntakeContext `json:"-"`
}

type FormIntakeStartResult struct {
	Job      FormJobRecord                       `json:"job"`
	Progress FormIntakeProgress                  `json:"intake"`
	Binding  interactions.ProtectedAnswerBinding `json:"protected_binding"`
	Reused   bool                                `json:"reused"`
}

type FormIntakeAdmission struct {
	Job      FormJobRecord      `json:"job"`
	Progress FormIntakeProgress `json:"intake"`
	Receipt  string             `json:"receipt"`
	Reused   bool               `json:"reused"`
}

// FormIntakeMessageRequest is trusted channel admission, not public tool input.
// MessageIdentity must be stable across question claims and standalone retries.
type FormIntakeMessageRequest struct {
	JobID            string
	ModeID           string
	ExpectedRevision int64
	Owner            FormJobOwner
	Schema           FormFieldsFacts
	MessageIdentity  string
	Text             string `json:"-"`
	RoutingReference string `json:"-"`
}

// FormIntakeInput is an ephemeral protected read. Even accidental ordinary JSON
// serialization cannot expose its utterance or interpretation context.
type FormIntakeInput struct {
	// AdmissionOrder is the immutable, one-based position in the native mode.
	// It is private interpretation context, never write or semantic authority.
	AdmissionOrder int `json:"-"`
	// AdmittedRevision is the immutable origin, not current write authority.
	AdmittedRevision int64 `json:"-"`
	// DialogIntent is derived from the last committed private pass, never approval.
	DialogIntent FormIntakeDialogIntent             `json:"-"`
	JobID        string                             `json:"job_id"`
	ModeID       string                             `json:"mode_id"`
	Reference    string                             `json:"reference"`
	Revision     int64                              `json:"revision"`
	Passes       int                                `json:"passes"`
	Consumed     bool                               `json:"consumed"`
	Intent       interactions.ProtectedAnswerIntent `json:"-"`
	Text         string                             `json:"-"`
	Context      FormIntakeContext                  `json:"-"`
	Accounted    []FormIntakeAccountedLiteral       `json:"-"`
	Findings     []FormFieldMappingBlocker          `json:"-"`
	// CompletedWindows is derived from committed pass outcomes, not a claim
	// that the remaining text is accounted. Failed reservations are excluded.
	CompletedWindows [][]string `json:"-"`
	// SupersededAdvisoryIDs is presentation evidence only: the still-current
	// native user decision was accepted strictly after this input's advice.
	SupersededAdvisoryIDs []string `json:"-"`
}

type FormIntakePassRequest struct {
	Reference        string
	ExpectedRevision int64
	Owner            FormJobOwner
	Schema           FormFieldsFacts
	CandidateIDs     []string
	IdempotencyKey   string
	// RetryStale binds retries to their revision without resetting the durable
	// reservation budget. Only the private interpreter bridge uses this option.
	RetryStale bool
}

// FormIntakePass binds one bounded candidate view to the current job revision.
// Packet 2 must validate this exact reservation before accepting any proposal.
type FormIntakePass struct {
	Input        FormIntakeInput        `json:"-"`
	Fields       []FormField            `json:"-"`
	ID           string                 `json:"pass_id"`
	Revision     int64                  `json:"revision"`
	CandidateIDs []string               `json:"candidate_field_ids"`
	Reused       bool                   `json:"reused"`
	Outcome      *FormIntakeApplyResult `json:"-"`
}

type formIntakeBindingPayload struct {
	Version          string            `json:"version"`
	JobID            string            `json:"job_id"`
	ModeID           string            `json:"mode_id"`
	SchemaDigest     string            `json:"schema_digest"`
	ExpectedRevision int64             `json:"expected_revision"`
	Context          FormIntakeContext `json:"context"`
}

type formIntakeStoredMode struct {
	Version        string                              `json:"version"`
	ID             string                              `json:"id"`
	SchemaDigest   string                              `json:"schema_digest"`
	StartDigest    string                              `json:"start_digest"`
	StartRevision  int64                               `json:"start_revision"`
	State          FormIntakeState                     `json:"state"`
	Context        FormIntakeContext                   `json:"context"`
	InitialBinding interactions.ProtectedAnswerBinding `json:"initial_binding"`
	Inputs         []formIntakeStoredInput             `json:"inputs,omitempty"`
	TaskRoutes     []formTaskRouteStored               `json:"task_routes,omitempty"`
}

type formIntakeInputState string

const (
	formIntakeInputStaged    formIntakeInputState = "staged"
	formIntakeInputCommitted formIntakeInputState = "committed"
	formIntakeInputConsumed  formIntakeInputState = "consumed"
)

type formIntakeStoredInput struct {
	AdmissionOrder int                                `json:"admission_order,omitempty"`
	ID             string                             `json:"id"`
	MessageDigest  string                             `json:"message_digest"`
	BodyDigest     string                             `json:"body_digest"`
	InteractionID  string                             `json:"interaction_id,omitempty"`
	State          formIntakeInputState               `json:"state"`
	Intent         interactions.ProtectedAnswerIntent `json:"intent"`
	Text           string                             `json:"text,omitempty"`
	Origin         formIntakeBindingPayload           `json:"origin"`
	CreatedAt      int64                              `json:"created_at"`
	CommittedAt    int64                              `json:"committed_at,omitempty"`
	Reservations   []formIntakeStoredPass             `json:"reservations,omitempty"`
	OriginalBytes  int                                `json:"original_bytes,omitempty"`
	Accounted      []formIntakeSpan                   `json:"accounted,omitempty"`
}

// The bounded reservation list is also the budget: no separately mutable
// counter or latest-only binding can drift from durable replay identity.
type formIntakeStoredPass struct {
	ID          string `json:"id"`
	KeyDigest   string `json:"key_digest"`
	GroupDigest string `json:"group_digest,omitempty"`
	// ReceiptCount binds this pass to an existing immutable admission snapshot.
	ReceiptCount int                      `json:"receipt_count,omitempty"`
	Revision     int64                    `json:"revision"`
	CandidateIDs []string                 `json:"candidate_field_ids"`
	Outcome      *formIntakeStoredOutcome `json:"outcome,omitempty"`
}
