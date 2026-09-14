// Package document defines MintClaw's versioned document-operation contracts.
package document

import "time"

const (
	ReportSchemaVersion       = "mintclaw.document_report.v1"
	CapabilitySchemaVersion   = "mintclaw.document_capabilities.v1"
	DefaultMaxInputBytes      = int64(20 * 1024 * 1024)
	DefaultMaxPages           = 2_000
	DefaultMaxContentBytes    = int64(8 * 1024 * 1024)
	DefaultMaxObjects         = 200_000
	DefaultMaxRecursionDepth  = 64
	DefaultMaxExtractPages    = 20
	DefaultMaxExtractChars    = 256_000
	DefaultMaxRenderPages     = 8
	DefaultRenderDPI          = 144
	DefaultMaxRenderEdge      = 3_200
	HardMaxRenderEdge         = 4_096
	DefaultMaxPixelsPerPage   = int64(16_000_000)
	DefaultMaxRenderPixels    = int64(32_000_000)
	DefaultMaxArtifactBytes   = int64(32 * 1024 * 1024)
	DefaultMaxFormFields      = 256
	DefaultMaxFieldWidgets    = 1_024
	DefaultMaxFieldOptions    = 128
	DefaultMaxFieldTextBytes  = 512
	DefaultMaxFormReportBytes = 48 * 1024
)

type State string

const (
	StateSucceeded   State = "succeeded"
	StateUnavailable State = "unavailable"
	StateUnsupported State = "unsupported"
	StateDenied      State = "denied"
	StateCanceled    State = "canceled"
	StateFailed      State = "failed"
	StateUncertain   State = "uncertain"
)

type FailureCode string

const (
	FailureUnsupportedPlatform    FailureCode = "unsupported_platform"
	FailureInvalidInput           FailureCode = "invalid_input"
	FailureUnsupportedType        FailureCode = "unsupported_type"
	FailureLimitExceeded          FailureCode = "limit_exceeded"
	FailureSourceChanged          FailureCode = "source_changed"
	FailureSourceUnauthorized     FailureCode = "source_not_authorized"
	FailureCanceled               FailureCode = "canceled"
	FailureWorkerUnavailable      FailureCode = "worker_unavailable"
	FailureWorkerProtocol         FailureCode = "worker_protocol"
	FailureWorkerCrashed          FailureCode = "worker_crashed"
	FailureWorkerOutputLimit      FailureCode = "worker_output_limit"
	FailureWorkerTimeout          FailureCode = "worker_timeout"
	FailureWorkerInputMismatch    FailureCode = "worker_input_mismatch"
	FailureMalformedPDF           FailureCode = "malformed_pdf"
	FailurePasswordRequired       FailureCode = "password_required"
	FailureInspectionLimit        FailureCode = "inspection_limit"
	FailureInvalidPageSelection   FailureCode = "invalid_page_selection"
	FailureExtractionLimit        FailureCode = "extraction_limit"
	FailureRenderLimit            FailureCode = "render_limit"
	FailureTextUnavailable        FailureCode = "text_unavailable"
	FailureVisionUnavailable      FailureCode = "vision_unavailable"
	FailureArtifactInvalid        FailureCode = "artifact_invalid"
	FailureArtifactRegistration   FailureCode = "artifact_registration_failed"
	FailureUnsupportedFeature     FailureCode = "unsupported_feature"
	FailureBackendUnavailable     FailureCode = "backend_unavailable"
	FailureFormNotPresent         FailureCode = "form_not_present"
	FailureFormUnsupported        FailureCode = "form_unsupported"
	FailureFieldUnsupported       FailureCode = "field_unsupported"
	FailureFieldNotFound          FailureCode = "field_not_found"
	FailureFieldAmbiguous         FailureCode = "field_ambiguous"
	FailureFieldReadOnly          FailureCode = "field_read_only"
	FailureFieldValueInvalid      FailureCode = "field_value_invalid"
	FailureChoiceInvalid          FailureCode = "choice_invalid"
	FailureWriteConflict          FailureCode = "write_conflict"
	FailureWriteFailed            FailureCode = "write_failed"
	FailureJournalFailed          FailureCode = "journal_failed"
	FailureRecoveryUncertain      FailureCode = "recovery_uncertain"
	FailureAppearanceUnavailable  FailureCode = "appearance_unavailable"
	FailureAppearanceStale        FailureCode = "appearance_stale"
	FailureContentClipped         FailureCode = "content_clipped"
	FailureVerificationStructural FailureCode = "verification_structural_failed"
	FailureVerificationVisual     FailureCode = "verification_visual_failed"
	FailureDeliveryFailed         FailureCode = "delivery_failed"
	FailureDeliveryAmbiguous      FailureCode = "delivery_ambiguous"
	FailureInternal               FailureCode = "internal_failure"
)

type FactState string

const (
	FactPresent FactState = "present"
	FactAbsent  FactState = "absent"
	FactMixed   FactState = "mixed"
	FactUnknown FactState = "unknown"
)

type Authority struct {
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	ActorID     string `json:"actor_id,omitempty"`
	RouteID     string `json:"route_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

type DocumentRef struct {
	Ref              string    `json:"ref"`
	SourceRef        string    `json:"source_ref,omitempty"`
	OriginalFilename string    `json:"original_filename"`
	ContentType      string    `json:"content_type"`
	Size             int64     `json:"size"`
	SHA256           string    `json:"sha256"`
	Authority        Authority `json:"authority"`
	SourceKind       string    `json:"source_kind"`
	CreatedAt        time.Time `json:"created_at"`
	CleanupPolicy    string    `json:"cleanup_policy"`
}

type Limits struct {
	MaxInputBytes     int64 `json:"max_input_bytes"`
	MaxPages          int   `json:"max_pages"`
	MaxContentBytes   int64 `json:"max_content_bytes"`
	MaxObjects        int   `json:"max_objects"`
	MaxRecursionDepth int   `json:"max_recursion_depth"`
}

type Failure struct {
	Code    FailureCode `json:"code"`
	Message string      `json:"message"`
}

type Report struct {
	SchemaVersion string           `json:"schema_version"`
	OperationID   string           `json:"operation_id"`
	Operation     string           `json:"operation"`
	State         State            `json:"state"`
	Input         *DocumentRef     `json:"input,omitempty"`
	Limits        Limits           `json:"limits"`
	ReadLimits    *ReadLimits      `json:"read_limits,omitempty"`
	Inspection    *InspectionFacts `json:"inspection,omitempty"`
	Extraction    *ExtractionFacts `json:"extraction,omitempty"`
	Rendering     *RenderingFacts  `json:"rendering,omitempty"`
	Fields        *FormFieldsFacts `json:"fields,omitempty"`
	Artifacts     []Artifact       `json:"artifacts,omitempty"`
	Failure       *Failure         `json:"failure,omitempty"`
}

type ReadLimits struct {
	MaxPages         int   `json:"max_pages"`
	MaxCharacters    int   `json:"max_characters"`
	DPI              int   `json:"dpi"`
	MaxDimension     int   `json:"max_dimension"`
	MaxPixelsPerPage int64 `json:"max_pixels_per_page"`
	MaxTotalPixels   int64 `json:"max_total_pixels"`
	MaxArtifactBytes int64 `json:"max_artifact_bytes"`
}

type Artifact struct {
	Ref          string `json:"ref"`
	Kind         string `json:"kind"`
	ContentType  string `json:"content_type"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	SourceSHA256 string `json:"source_sha256"`
	Pages        []int  `json:"pages"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
}

type PageTextFacts struct {
	Page       int  `json:"page"`
	Characters int  `json:"characters"`
	Truncated  bool `json:"truncated,omitempty"`
}

type ExtractionFacts struct {
	Backend         BackendIdentity `json:"backend"`
	SelectedPages   []int           `json:"selected_pages"`
	Pages           []PageTextFacts `json:"pages"`
	TotalCharacters int             `json:"total_characters"`
	Truncated       bool            `json:"truncated,omitempty"`
}

type PageRenderFacts struct {
	Page   int `json:"page"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type RenderingFacts struct {
	Backend       BackendIdentity   `json:"backend"`
	SelectedPages []int             `json:"selected_pages"`
	Pages         []PageRenderFacts `json:"pages"`
	DPI           int               `json:"dpi"`
	TotalPixels   int64             `json:"total_pixels"`
}

type FormFieldKind string

const (
	FormFieldText     FormFieldKind = "text"
	FormFieldDate     FormFieldKind = "date"
	FormFieldCheckbox FormFieldKind = "checkbox"
	FormFieldRadio    FormFieldKind = "radio"
	FormFieldCombo    FormFieldKind = "combo"
	FormFieldList     FormFieldKind = "list"
)

type FormFieldLimits struct {
	MaxFields      int `json:"max_fields"`
	MaxWidgets     int `json:"max_widgets"`
	MaxOptions     int `json:"max_options_per_field"`
	MaxTextBytes   int `json:"max_text_bytes"`
	MaxReportBytes int `json:"max_report_bytes"`
}

type FormFieldOption struct {
	Export  string `json:"export"`
	Display string `json:"display"`
}

type FormFieldWidget struct {
	ID      string `json:"id"`
	Page    int    `json:"page"`
	Ordinal int    `json:"ordinal"`
}

type FormField struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	AlternateName string            `json:"alternate_name,omitempty"`
	Kind          FormFieldKind     `json:"kind"`
	ReadOnly      bool              `json:"read_only"`
	Required      bool              `json:"required"`
	Multiline     bool              `json:"multiline,omitempty"`
	MultiSelect   bool              `json:"multi_select,omitempty"`
	Editable      bool              `json:"editable,omitempty"`
	MaxLength     int               `json:"max_length,omitempty"`
	DateFormat    string            `json:"date_format,omitempty"`
	HasDefault    bool              `json:"has_default"`
	HasValue      bool              `json:"has_value"`
	Options       []FormFieldOption `json:"options,omitempty"`
	Widgets       []FormFieldWidget `json:"widgets"`
}

type FormFieldsFacts struct {
	SourceSHA256 string          `json:"source_sha256"`
	Backend      BackendIdentity `json:"backend"`
	Limits       FormFieldLimits `json:"limits"`
	Fields       []FormField     `json:"fields"`
}

// FormWriteFacts is path-free, value-free structural and visual evidence for
// a private fill candidate. A candidate is not final-ready until the parent
// validates and adopts its artifact descriptor.
type FormWriteFacts struct {
	Backend              BackendIdentity `json:"backend"`
	VisualBackend        BackendIdentity `json:"visual_backend"`
	SourceSHA256         string          `json:"source_sha256"`
	RequestSHA256        string          `json:"request_sha256"`
	OutputSHA256         string          `json:"output_sha256"`
	OutputSize           int64           `json:"output_size"`
	AffectedPages        []int           `json:"affected_pages"`
	StructuralAssertions int             `json:"structural_assertions"`
	CheckedFields        int             `json:"checked_fields"`
	CheckedWidgets       int             `json:"checked_widgets"`
	UnchangedFields      int             `json:"unchanged_fields"`
	AppearanceWidgets    int             `json:"appearance_widgets"`
	VisualAssertions     int             `json:"visual_assertions"`
	RenderedPages        int             `json:"rendered_pages"`
}

type StringFact struct {
	State FactState `json:"state"`
	Value string    `json:"value,omitempty"`
}

type IntegerFact struct {
	State FactState `json:"state"`
	Value *int      `json:"value,omitempty"`
}

type BackendIdentity struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Role          string `json:"role"`
	IsolationMode string `json:"isolation_mode,omitempty"`
}

type EncryptionFacts struct {
	State            FactState  `json:"state"`
	PasswordRequired FactState  `json:"password_required"`
	Permissions      StringFact `json:"permissions"`
}

type SignatureFacts struct {
	State       FactState   `json:"state"`
	Count       IntegerFact `json:"count"`
	Certified   FactState   `json:"certified"`
	Timestamped FactState   `json:"timestamped"`
}

type RestrictionFacts struct {
	State                FactState `json:"state"`
	EncryptedPermissions FactState `json:"encrypted_permissions"`
	DocMDP               FactState `json:"doc_mdp"`
	FieldMDP             FactState `json:"field_mdp"`
	UsageRights          FactState `json:"usage_rights"`
	ReaderExtensions     FactState `json:"reader_extensions"`
}

type AcroFormFacts struct {
	State      FactState   `json:"state"`
	FieldCount IntegerFact `json:"field_count"`
}

type XFAFacts struct {
	State          FactState  `json:"state"`
	Representation StringFact `json:"representation"`
	Rendering      StringFact `json:"rendering"`
}

type TextFacts struct {
	State            FactState `json:"state"`
	PagesWithText    int       `json:"pages_with_text"`
	PagesWithoutText int       `json:"pages_without_text"`
	PagesUnknown     int       `json:"pages_unknown"`
}

type InspectionFacts struct {
	Backend         BackendIdentity  `json:"backend"`
	PDFVersion      StringFact       `json:"pdf_version"`
	PageCount       IntegerFact      `json:"page_count"`
	Encryption      EncryptionFacts  `json:"encryption"`
	Signatures      SignatureFacts   `json:"signatures"`
	Restrictions    RestrictionFacts `json:"restrictions"`
	AcroForm        AcroFormFacts    `json:"acroform"`
	XFA             XFAFacts         `json:"xfa"`
	ExtractableText TextFacts        `json:"extractable_text"`
	Warnings        []string         `json:"warnings,omitempty"`
}

type OperationCapability struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type CapabilityReport struct {
	SchemaVersion string                         `json:"schema_version"`
	Platform      string                         `json:"platform"`
	Architecture  string                         `json:"architecture"`
	Operations    map[string]OperationCapability `json:"operations"`
	Limits        Limits                         `json:"limits"`
	ReadLimits    map[string]ReadLimits          `json:"read_limits"`
	FormLimits    map[string]FormFieldLimits     `json:"form_limits"`
}
