package nodes

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
)

var (
	ErrBrowserHostDenied   = errors.New("companion browser authority denied")
	ErrBrowserHostBusy     = errors.New("companion browser profile is busy")
	ErrBrowserHostNotFound = errors.New("companion browser session not found")
	ErrBrowserHostStale    = errors.New("companion browser state is stale")
	ErrBrowserHostLost     = errors.New("companion browser session is lost")
)

const (
	BrowserDriverPlaywrightMCP = "playwright_mcp"
	BrowserProfileManaged      = "managed"
	BrowserNetworkExactOrigins = "exact_origins"
	BrowserNetworkPublicWeb    = "public_web"
	BrowserNetworkAnyHTTP      = "any_http"

	MaxBrowserProfiles           = 8
	MaxBrowserActions            = 16
	MaxBrowserScrollAmount       = 5
	MaxBrowserSessions           = 1
	MaxBrowserTabs               = 4
	MaxBrowserSessionSeconds     = 60 * 60
	MaxBrowserIdleSeconds        = 10 * 60
	MaxBrowserPreparedSeconds    = 5 * 60
	MaxBrowserActionSeconds      = 60
	MaxBrowserURLBytes           = 16 * 1024
	MaxBrowserTitleBytes         = 4 * 1024
	MaxBrowserDialogMessageBytes = 2 * 1024
	MaxBrowserSnapshotBytes      = 256 * 1024
	MaxBrowserScreenshotBytes    = 8 * 1024 * 1024
	MaxBrowserUploadBytes        = 32 * 1024 * 1024
	MaxBrowserDownloadBytes      = 32 * 1024 * 1024
	MaxBrowserSnapshotRefs       = 500
	MaxBrowserTextInputBytes     = 16 * 1024
	// JSON can encode one accepted input byte as a six-byte Unicode escape.
	// The fixed allowance covers the transport-only {"value": ...} wrapper.
	MaxBrowserEphemeralInputBytes = MaxBrowserTextInputBytes*6 + 128
	MaxBrowserContextInputBytes   = 64*1024 + 128
	MaxBrowserToolResultBytes     = 320 * 1024
	MaxBrowserRetentionSeconds    = 7 * 24 * 60 * 60
	MinBrowserToolResultBytes     = 64 * 1024
)

const (
	BrowserCommandSessionOpen   = "browser.session.open.v1"
	BrowserCommandSessionStatus = "browser.session.status.v1"
	BrowserCommandObserve       = "browser.observe.v1"
	BrowserCommandAct           = "browser.act.v1"
	BrowserCommandContexts      = "browser.contexts.v1"
	BrowserCommandSessionClose  = "browser.session.close.v1"
)

type BrowserLimits struct {
	Sessions        int `json:"sessions"`
	Tabs            int `json:"tabs"`
	SessionSeconds  int `json:"session_seconds"`
	IdleSeconds     int `json:"idle_seconds"`
	PreparedSeconds int `json:"prepared_seconds"`
	ActionSeconds   int `json:"action_seconds"`
	SnapshotBytes   int `json:"snapshot_bytes"`
	ScreenshotBytes int `json:"screenshot_bytes"`
	UploadBytes     int `json:"upload_bytes"`
	DownloadBytes   int `json:"download_bytes"`
	SnapshotRefs    int `json:"snapshot_refs"`
	TextInputBytes  int `json:"text_input_bytes"`
	ToolResultBytes int `json:"tool_result_bytes"`
	RetentionSecs   int `json:"retention_seconds"`
}

// decodeCanonicalBrowserLimits accepts canonical JSON numbers such as 6e1
// while preserving the integer-only limits contract. Node invocation
// canonicalization may use exponent notation for an original integer literal.
func decodeCanonicalBrowserLimits(data []byte) (BrowserLimits, error) {
	var values struct {
		Sessions        float64 `json:"sessions"`
		Tabs            float64 `json:"tabs"`
		SessionSeconds  float64 `json:"session_seconds"`
		IdleSeconds     float64 `json:"idle_seconds"`
		PreparedSeconds float64 `json:"prepared_seconds"`
		ActionSeconds   float64 `json:"action_seconds"`
		SnapshotBytes   float64 `json:"snapshot_bytes"`
		ScreenshotBytes float64 `json:"screenshot_bytes"`
		UploadBytes     float64 `json:"upload_bytes"`
		DownloadBytes   float64 `json:"download_bytes"`
		SnapshotRefs    float64 `json:"snapshot_refs"`
		TextInputBytes  float64 `json:"text_input_bytes"`
		ToolResultBytes float64 `json:"tool_result_bytes"`
		RetentionSecs   float64 `json:"retention_seconds"`
	}
	if err := json.Unmarshal(data, &values); err != nil {
		return BrowserLimits{}, err
	}
	numbers := []float64{
		values.Sessions, values.Tabs, values.SessionSeconds, values.IdleSeconds,
		values.PreparedSeconds, values.ActionSeconds, values.SnapshotBytes,
		values.ScreenshotBytes, values.UploadBytes, values.DownloadBytes,
		values.SnapshotRefs, values.TextInputBytes, values.ToolResultBytes,
		values.RetentionSecs,
	}
	for _, value := range numbers {
		if value < 0 || value > MaxBrowserUploadBytes || value != float64(int(value)) {
			return BrowserLimits{}, fmt.Errorf(
				"%w: browser limits must be bounded integers",
				ErrInvalidCapability,
			)
		}
	}
	return BrowserLimits{
		Sessions: int(values.Sessions), Tabs: int(values.Tabs),
		SessionSeconds: int(values.SessionSeconds), IdleSeconds: int(values.IdleSeconds),
		PreparedSeconds: int(values.PreparedSeconds), ActionSeconds: int(values.ActionSeconds),
		SnapshotBytes: int(values.SnapshotBytes), ScreenshotBytes: int(values.ScreenshotBytes),
		UploadBytes: int(values.UploadBytes), DownloadBytes: int(values.DownloadBytes),
		SnapshotRefs: int(values.SnapshotRefs), TextInputBytes: int(values.TextInputBytes),
		ToolResultBytes: int(values.ToolResultBytes), RetentionSecs: int(values.RetentionSecs),
	}, nil
}

func (limits BrowserLimits) Validate() error {
	if limits.Sessions != MaxBrowserSessions || limits.Tabs <= 0 || limits.Tabs > MaxBrowserTabs ||
		limits.SessionSeconds <= 0 || limits.SessionSeconds > MaxBrowserSessionSeconds ||
		limits.IdleSeconds <= 0 || limits.IdleSeconds > limits.SessionSeconds ||
		limits.IdleSeconds > MaxBrowserIdleSeconds ||
		limits.PreparedSeconds <= 0 || limits.PreparedSeconds > limits.SessionSeconds ||
		limits.PreparedSeconds > MaxBrowserPreparedSeconds ||
		limits.ActionSeconds <= 0 || limits.ActionSeconds > MaxBrowserActionSeconds ||
		limits.SnapshotBytes <= 0 || limits.SnapshotBytes > MaxBrowserSnapshotBytes ||
		limits.ScreenshotBytes <= 0 || limits.ScreenshotBytes > MaxBrowserScreenshotBytes ||
		limits.UploadBytes <= 0 || limits.UploadBytes > MaxBrowserUploadBytes ||
		limits.DownloadBytes <= 0 || limits.DownloadBytes > MaxBrowserDownloadBytes ||
		limits.SnapshotRefs <= 0 || limits.SnapshotRefs > MaxBrowserSnapshotRefs ||
		limits.TextInputBytes <= 0 || limits.TextInputBytes > MaxBrowserTextInputBytes ||
		limits.ToolResultBytes < MinBrowserToolResultBytes ||
		limits.ToolResultBytes > MaxBrowserToolResultBytes ||
		limits.RetentionSecs <= 0 || limits.RetentionSecs > MaxBrowserRetentionSeconds {
		return fmt.Errorf("%w: malformed browser limits", ErrInvalidCapability)
	}
	return nil
}

func (limits BrowserLimits) Effective() BrowserLimits {
	return BrowserLimits{
		Sessions:        effectiveBrowserLimit(limits.Sessions, MaxBrowserSessions),
		Tabs:            effectiveBrowserLimit(limits.Tabs, MaxBrowserTabs),
		SessionSeconds:  effectiveBrowserLimit(limits.SessionSeconds, MaxBrowserSessionSeconds),
		IdleSeconds:     effectiveBrowserLimit(limits.IdleSeconds, MaxBrowserIdleSeconds),
		PreparedSeconds: effectiveBrowserLimit(limits.PreparedSeconds, MaxBrowserPreparedSeconds),
		ActionSeconds:   effectiveBrowserLimit(limits.ActionSeconds, MaxBrowserActionSeconds),
		SnapshotBytes:   effectiveBrowserLimit(limits.SnapshotBytes, MaxBrowserSnapshotBytes),
		ScreenshotBytes: effectiveBrowserLimit(limits.ScreenshotBytes, MaxBrowserScreenshotBytes),
		UploadBytes:     effectiveBrowserLimit(limits.UploadBytes, MaxBrowserUploadBytes),
		DownloadBytes:   effectiveBrowserLimit(limits.DownloadBytes, MaxBrowserDownloadBytes),
		SnapshotRefs:    effectiveBrowserLimit(limits.SnapshotRefs, MaxBrowserSnapshotRefs),
		TextInputBytes:  effectiveBrowserLimit(limits.TextInputBytes, MaxBrowserTextInputBytes),
		ToolResultBytes: effectiveBrowserLimit(limits.ToolResultBytes, MaxBrowserToolResultBytes),
		RetentionSecs:   effectiveBrowserLimit(limits.RetentionSecs, MaxBrowserRetentionSeconds),
	}
}

func effectiveBrowserLimit(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

// BrowserProfileDescriptor is the model-safe projection of companion-local
// browser authority. Driver commands, endpoints, profile paths, lock paths,
// environment, and credentials intentionally never cross the node boundary.
type BrowserProfileDescriptor struct {
	Alias                string        `json:"alias"`
	Revision             string        `json:"revision"`
	Driver               string        `json:"driver"`
	Mode                 string        `json:"mode"`
	NetworkMode          string        `json:"network_mode"`
	DryRun               bool          `json:"dry_run"`
	AllowApprovedActions bool          `json:"allow_approved_actions,omitempty"`
	Headed               bool          `json:"headed"`
	Actions              []string      `json:"actions"`
	Limits               BrowserLimits `json:"limits"`
}

// Browser command payloads are the typed internal gateway-to-companion
// contract. They intentionally contain no transport endpoints, driver
// commands, profile paths, credentials, or model-selected node identity.
type BrowserSessionOpenInput struct {
	SessionID             string        `json:"session_id"`
	Profile               string        `json:"profile"`
	ProfileRevision       string        `json:"profile_revision"`
	BrowserPolicyRevision string        `json:"browser_policy_revision"`
	DryRun                bool          `json:"dry_run"`
	Limits                BrowserLimits `json:"limits"`
}

func (input *BrowserSessionOpenInput) UnmarshalJSON(data []byte) error {
	var value struct {
		SessionID             string          `json:"session_id"`
		Profile               string          `json:"profile"`
		ProfileRevision       string          `json:"profile_revision"`
		BrowserPolicyRevision string          `json:"browser_policy_revision"`
		DryRun                bool            `json:"dry_run"`
		Limits                json.RawMessage `json:"limits"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	limits, err := decodeCanonicalBrowserLimits(value.Limits)
	if err != nil {
		return err
	}
	*input = BrowserSessionOpenInput{
		SessionID: value.SessionID, Profile: value.Profile,
		ProfileRevision:       value.ProfileRevision,
		BrowserPolicyRevision: value.BrowserPolicyRevision,
		DryRun:                value.DryRun, Limits: limits,
	}
	return nil
}

type BrowserSessionStatusInput struct {
	SessionID       string `json:"session_id"`
	ProfileRevision string `json:"profile_revision"`
}

type BrowserObserveInput struct {
	SessionID          string `json:"session_id"`
	TabID              string `json:"tab_id"`
	SnapshotGeneration uint64 `json:"snapshot_generation"`
	Screenshot         bool   `json:"screenshot"`
}

func (input *BrowserObserveInput) UnmarshalJSON(data []byte) error {
	var value struct {
		SessionID          string          `json:"session_id"`
		TabID              string          `json:"tab_id"`
		SnapshotGeneration json.RawMessage `json:"snapshot_generation"`
		Screenshot         bool            `json:"screenshot"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	generation, err := decodeCanonicalBrowserGeneration(value.SnapshotGeneration)
	if err != nil {
		return fmt.Errorf("decode browser observe generation: %w", err)
	}
	*input = BrowserObserveInput{
		SessionID: value.SessionID, TabID: value.TabID,
		SnapshotGeneration: generation, Screenshot: value.Screenshot,
	}
	return nil
}

type BrowserAction struct {
	Kind           string `json:"kind"`
	URL            string `json:"url,omitempty"`
	Ref            string `json:"ref,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
	DestinationRef string `json:"destination_ref,omitempty"`
	DialogID       string `json:"dialog_id,omitempty"`
	Target         string `json:"target,omitempty"`
	Value          string `json:"value,omitempty"`
	Key            string `json:"key,omitempty"`
	Direction      string `json:"direction,omitempty"`
	Amount         int    `json:"amount,omitempty"`
	Decision       string `json:"decision,omitempty"`
	PromptProvided bool   `json:"prompt_provided,omitempty"`
}

func (action *BrowserAction) UnmarshalJSON(data []byte) error {
	var value struct {
		Kind           string          `json:"kind"`
		URL            string          `json:"url,omitempty"`
		Ref            string          `json:"ref,omitempty"`
		SourceRef      string          `json:"source_ref,omitempty"`
		DestinationRef string          `json:"destination_ref,omitempty"`
		DialogID       string          `json:"dialog_id,omitempty"`
		Target         string          `json:"target,omitempty"`
		Value          string          `json:"value,omitempty"`
		Key            string          `json:"key,omitempty"`
		Direction      string          `json:"direction,omitempty"`
		Amount         json.RawMessage `json:"amount,omitempty"`
		Decision       string          `json:"decision,omitempty"`
		PromptProvided bool            `json:"prompt_provided,omitempty"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	amount, err := decodeCanonicalBrowserGeneration(value.Amount)
	if err != nil || amount > MaxBrowserScrollAmount {
		return fmt.Errorf(
			"%w: browser scroll amount must be an integer from 1 to %d",
			ErrInvalidCapability,
			MaxBrowserScrollAmount,
		)
	}
	*action = BrowserAction{
		Kind: value.Kind, URL: value.URL, Ref: value.Ref, SourceRef: value.SourceRef,
		DestinationRef: value.DestinationRef, DialogID: value.DialogID, Target: value.Target,
		Value: value.Value, Key: value.Key, Direction: value.Direction, Amount: int(amount),
		Decision: value.Decision, PromptProvided: value.PromptProvided,
	}
	return nil
}

type BrowserActInput struct {
	SessionID               string        `json:"session_id"`
	TabID                   string        `json:"tab_id"`
	SnapshotGeneration      uint64        `json:"snapshot_generation"`
	ActionInvocationID      string        `json:"action_invocation_id"`
	Action                  BrowserAction `json:"action"`
	Effect                  string        `json:"effect"`
	CurrentOrigin           string        `json:"current_origin"`
	PreparedActionHash      string        `json:"prepared_action_hash"`
	BrowserPolicyRevision   string        `json:"browser_policy_revision"`
	ProfileRevision         string        `json:"profile_revision"`
	ExpectedRole            string        `json:"expected_role,omitempty"`
	ExpectedName            string        `json:"expected_name,omitempty"`
	DestinationExpectedRole string        `json:"destination_expected_role,omitempty"`
	DestinationExpectedName string        `json:"destination_expected_name,omitempty"`
	DialogType              string        `json:"dialog_type,omitempty"`
	DialogMessageDigest     string        `json:"dialog_message_digest,omitempty"`
	DialogMessageBytes      int           `json:"dialog_message_bytes,omitempty"`
	InputDigest             string        `json:"input_digest,omitempty"`
	InputBytes              int           `json:"input_bytes,omitempty"`
	ApprovalDigest          string        `json:"approval_digest,omitempty"`
}

func (input BrowserActInput) MarshalJSON() ([]byte, error) {
	type browserActInputWire BrowserActInput
	if input.Action.Kind != "dialog" {
		return json.Marshal(browserActInputWire(input))
	}
	return json.Marshal(struct {
		browserActInputWire
		DialogMessageBytes int `json:"dialog_message_bytes"`
	}{
		browserActInputWire: browserActInputWire(input),
		DialogMessageBytes:  input.DialogMessageBytes,
	})
}

func (input *BrowserActInput) UnmarshalJSON(data []byte) error {
	var value struct {
		SessionID               string          `json:"session_id"`
		TabID                   string          `json:"tab_id"`
		SnapshotGeneration      json.RawMessage `json:"snapshot_generation"`
		ActionInvocationID      string          `json:"action_invocation_id"`
		Action                  BrowserAction   `json:"action"`
		Effect                  string          `json:"effect"`
		CurrentOrigin           string          `json:"current_origin"`
		PreparedActionHash      string          `json:"prepared_action_hash"`
		BrowserPolicyRevision   string          `json:"browser_policy_revision"`
		ProfileRevision         string          `json:"profile_revision"`
		ExpectedRole            string          `json:"expected_role,omitempty"`
		ExpectedName            string          `json:"expected_name,omitempty"`
		DestinationExpectedRole string          `json:"destination_expected_role,omitempty"`
		DestinationExpectedName string          `json:"destination_expected_name,omitempty"`
		DialogType              string          `json:"dialog_type,omitempty"`
		DialogMessageDigest     string          `json:"dialog_message_digest,omitempty"`
		DialogMessageBytes      json.RawMessage `json:"dialog_message_bytes,omitempty"`
		InputDigest             string          `json:"input_digest,omitempty"`
		InputBytes              json.RawMessage `json:"input_bytes,omitempty"`
		ApprovalDigest          string          `json:"approval_digest,omitempty"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	generation, err := decodeCanonicalBrowserGeneration(value.SnapshotGeneration)
	if err != nil {
		return fmt.Errorf("decode browser action generation: %w", err)
	}
	inputBytes, err := decodeCanonicalBrowserGeneration(value.InputBytes)
	if err != nil {
		return fmt.Errorf("decode browser action input bytes: %w", err)
	}
	if inputBytes > MaxBrowserTextInputBytes {
		return fmt.Errorf("%w: browser action input bytes exceed the limit", ErrInvalidCapability)
	}
	dialogMessageBytes, err := decodeCanonicalBrowserGeneration(value.DialogMessageBytes)
	if err != nil || dialogMessageBytes > MaxBrowserDialogMessageBytes {
		return fmt.Errorf("%w: browser dialog message bytes exceed the limit", ErrInvalidCapability)
	}
	*input = BrowserActInput{
		SessionID: value.SessionID, TabID: value.TabID, SnapshotGeneration: generation,
		ActionInvocationID: value.ActionInvocationID, Action: value.Action,
		Effect: value.Effect, CurrentOrigin: value.CurrentOrigin,
		PreparedActionHash:    value.PreparedActionHash,
		BrowserPolicyRevision: value.BrowserPolicyRevision,
		ProfileRevision:       value.ProfileRevision,
		ExpectedRole:          value.ExpectedRole, ExpectedName: value.ExpectedName,
		DestinationExpectedRole: value.DestinationExpectedRole,
		DestinationExpectedName: value.DestinationExpectedName,
		DialogType:              value.DialogType, DialogMessageDigest: value.DialogMessageDigest,
		DialogMessageBytes: int(dialogMessageBytes),
		InputDigest:        value.InputDigest, InputBytes: int(inputBytes),
		ApprovalDigest: value.ApprovalDigest,
	}
	return nil
}

const (
	browserApprovalDigestDomain = "mintclaw.browser.act.approval.v1\x00"
	browserInputDigestDomain    = "mintclaw.browser.act.input.v1\x00"
	browserDialogDigestDomain   = "mintclaw.browser.dialog-message.v1\x00"
)

func BrowserInputDigest(value string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(browserInputDigestDomain))
	_, _ = hash.Write([]byte(value))
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func BrowserInputDigestMatches(digest string, value string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	expected := BrowserInputDigest(value)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(digest)) == 1
}

func BrowserDialogMessageDigest(dialogType, message string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(browserDialogDigestDomain))
	_, _ = hash.Write([]byte(dialogType))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(message))
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func BrowserDialogMessageDigestMatches(digest, dialogType, message string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	expected := BrowserDialogMessageDigest(dialogType, message)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(digest)) == 1
}

// BrowserApprovalDigest binds one approval-gated companion action to its
// complete typed input without forwarding human approval authority.
func BrowserApprovalDigest(input BrowserActInput) (string, error) {
	input.ApprovalDigest = ""
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(browserApprovalDigestDomain))
	_, _ = hash.Write(encoded)
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// BrowserApprovalDigestMatches verifies the action's supplied digest without
// exposing or reconstructing human approval credentials.
func BrowserApprovalDigestMatches(input BrowserActInput) bool {
	if len(input.ApprovalDigest) != sha256.Size*2 {
		return false
	}
	expected, err := BrowserApprovalDigest(input)
	return err == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(input.ApprovalDigest)) == 1
}

// BrowserClickEffect returns the conservative trusted effect for semantic
// click revalidation on both sides of the companion boundary.
func BrowserClickEffect(role string) string {
	if role == "button" {
		return "external_commit"
	}
	return "unknown"
}

func BrowserCheckRoleAllowed(kind, role string) bool {
	if role == "checkbox" || role == "switch" {
		return true
	}
	return kind == "check" && role == "radio"
}

// BrowserFillFieldAllowed is the companion-side minimum semantic deny policy.
// It uses only freshly resolved private accessibility metadata and fails
// closed for roles or names that cannot safely identify ordinary text input.
func BrowserFillFieldAllowed(role, name string) bool {
	return BrowserFillFieldAllowedWithPolicy(role, name, nil)
}

// BrowserFillFieldAllowedWithPolicy also applies companion-local private
// operator-designated sensitive identity fragments.
func BrowserFillFieldAllowedWithPolicy(role, name string, sensitiveTerms []string) bool {
	return browserpolicy.OrdinaryFillField(role, name, sensitiveTerms)
}

// BrowserPressKeyValid admits only document-scoped keys that cannot express
// browser chrome, operating-system, or arbitrary modifier shortcuts.
func BrowserPressKeyValid(key string) bool {
	switch key {
	case "Enter", "Space", "Escape", "Tab", "Shift+Tab", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
		"Home", "End", "PageUp", "PageDown", "Backspace", "Delete":
		return true
	default:
		return false
	}
}

type BrowserHostFeatures struct {
	Observe    bool `json:"observe"`
	Navigate   bool `json:"navigate"`
	Contexts   bool `json:"contexts"`
	Screenshot bool `json:"screenshot"`
	Download   bool `json:"download"`
}

type BrowserFrameContext struct {
	ID                 string `json:"frame_id"`
	ParentFrameID      string `json:"parent_frame_id,omitempty"`
	CreationSequence   uint64 `json:"creation_sequence"`
	Depth              int    `json:"depth"`
	DocumentGeneration uint64 `json:"document_generation"`
	URL                string `json:"url"`
	Origin             string `json:"origin"`
	Label              string `json:"label,omitempty"`
	Availability       string `json:"availability"`
	SafeFailure        string `json:"safe_failure,omitempty"`
}

type BrowserTabContext struct {
	ID                 string                `json:"tab_id"`
	Kind               string                `json:"kind"`
	CreationSequence   uint64                `json:"creation_sequence"`
	OpenerTabID        string                `json:"opener_tab_id,omitempty"`
	OpenerInvocationID string                `json:"opener_invocation_id,omitempty"`
	DocumentGeneration uint64                `json:"document_generation"`
	URL                string                `json:"url"`
	Origin             string                `json:"origin"`
	Title              string                `json:"title,omitempty"`
	Frames             []BrowserFrameContext `json:"frames,omitempty"`
	OmittedFrameCount  int                   `json:"omitted_frame_count,omitempty"`
	FramesTruncated    bool                  `json:"frames_truncated,omitempty"`
}

type BrowserContextCatalog struct {
	ID              string              `json:"context_catalog_id"`
	Generation      uint64              `json:"context_generation"`
	SelectedTabID   string              `json:"selected_tab_id"`
	SelectedFrameID string              `json:"selected_frame_id,omitempty"`
	Tabs            []BrowserTabContext `json:"tabs"`
	OmittedTabCount int                 `json:"omitted_tab_count,omitempty"`
	Truncated       bool                `json:"truncated,omitempty"`
}

type BrowserContextInput struct {
	SessionID         string `json:"session_id"`
	ProfileRevision   string `json:"profile_revision"`
	Operation         string `json:"operation"`
	RequestID         string `json:"request_id"`
	ContextCatalogID  string `json:"context_catalog_id,omitempty"`
	ContextGeneration uint64 `json:"context_generation,omitempty"`
	AuthorityDigest   string `json:"authority_digest,omitempty"`
	AuthorityBytes    int    `json:"authority_bytes,omitempty"`
	TabID             string `json:"tab_id,omitempty"`
	FrameID           string `json:"frame_id,omitempty"`
}

type BrowserContextResult struct {
	Operation       string                    `json:"operation"`
	Catalog         BrowserContextCatalog     `json:"context_catalog"`
	Observation     *BrowserObservationResult `json:"observation,omitempty"`
	ProtectedResult bool                      `json:"protected_result,omitempty"`
}

type BrowserSessionResult struct {
	SessionID     string              `json:"session_id"`
	State         string              `json:"state"`
	Reason        string              `json:"reason,omitempty"`
	Recovery      string              `json:"recovery,omitempty"`
	TabID         string              `json:"tab_id,omitempty"`
	Controller    string              `json:"controller,omitempty"`
	Features      BrowserHostFeatures `json:"features,omitempty"`
	ExpiresAt     int64               `json:"expires_at,omitempty"`
	IdleExpiresAt int64               `json:"idle_expires_at,omitempty"`
}

func (result *BrowserSessionResult) UnmarshalJSON(data []byte) error {
	var value struct {
		SessionID     string              `json:"session_id"`
		State         string              `json:"state"`
		Reason        string              `json:"reason,omitempty"`
		Recovery      string              `json:"recovery,omitempty"`
		TabID         string              `json:"tab_id,omitempty"`
		Controller    string              `json:"controller,omitempty"`
		Features      BrowserHostFeatures `json:"features,omitempty"`
		ExpiresAt     json.RawMessage     `json:"expires_at,omitempty"`
		IdleExpiresAt json.RawMessage     `json:"idle_expires_at,omitempty"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	expiresAt, err := decodeCanonicalBrowserTimestamp(value.ExpiresAt)
	if err != nil {
		return fmt.Errorf("decode browser session expiry: %w", err)
	}
	idleExpiresAt, err := decodeCanonicalBrowserTimestamp(value.IdleExpiresAt)
	if err != nil {
		return fmt.Errorf("decode browser session idle expiry: %w", err)
	}
	*result = BrowserSessionResult{
		SessionID: value.SessionID, State: value.State,
		Reason: value.Reason, Recovery: value.Recovery,
		TabID: value.TabID, Controller: value.Controller, Features: value.Features,
		ExpiresAt: expiresAt, IdleExpiresAt: idleExpiresAt,
	}
	return nil
}

// decodeCanonicalBrowserTimestamp accepts exponent notation emitted by the
// invocation canonicalizer without rounding values or weakening the integer
// wire contract. Status and close results omit these fields and decode as zero.
func decodeCanonicalBrowserTimestamp(data json.RawMessage) (int64, error) {
	if len(data) == 0 {
		return 0, nil
	}
	value, ok := new(big.Rat).SetString(string(data))
	if !ok || !value.IsInt() || value.Sign() < 0 || !value.Num().IsInt64() {
		return 0, fmt.Errorf("%w: browser timestamp must be a nonnegative integer", ErrInvalidCapability)
	}
	return value.Num().Int64(), nil
}

// decodeCanonicalBrowserGeneration accepts exponent notation emitted by the
// invocation canonicalizer while retaining the exact uint64 wire contract.
func decodeCanonicalBrowserGeneration(data json.RawMessage) (uint64, error) {
	if len(data) == 0 {
		return 0, nil
	}
	value, ok := new(big.Rat).SetString(string(data))
	if !ok || !value.IsInt() || value.Sign() < 0 || !value.Num().IsUint64() {
		return 0, fmt.Errorf(
			"%w: browser snapshot generation must be a nonnegative integer",
			ErrInvalidCapability,
		)
	}
	return value.Num().Uint64(), nil
}

func (result BrowserSessionResult) MarshalJSON() ([]byte, error) {
	value := map[string]any{"session_id": result.SessionID, "state": result.State}
	if result.Reason != "" {
		value["reason"] = result.Reason
	}
	if result.Recovery != "" {
		value["recovery"] = result.Recovery
	}
	if result.TabID != "" {
		value["tab_id"] = result.TabID
		value["controller"] = result.Controller
		value["features"] = result.Features
		value["expires_at"] = result.ExpiresAt
		value["idle_expires_at"] = result.IdleExpiresAt
	}
	return json.Marshal(value)
}

type BrowserElement struct {
	Ref  string `json:"ref"`
	Role string `json:"role"`
	Name string `json:"name"`
}

type BrowserDialogObservation struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type BrowserObservationResult struct {
	SessionID          string                    `json:"session_id"`
	TabID              string                    `json:"tab_id"`
	SnapshotGeneration uint64                    `json:"snapshot_generation"`
	URL                string                    `json:"url"`
	Origin             string                    `json:"origin"`
	Title              string                    `json:"title,omitempty"`
	Snapshot           string                    `json:"snapshot"`
	Elements           []BrowserElement          `json:"elements"`
	PendingDialog      *BrowserDialogObservation `json:"pending_dialog,omitempty"`
	Truncated          bool                      `json:"truncated"`
	ProtectedResult    bool                      `json:"protected_result,omitempty"`
}

func (result *BrowserObservationResult) UnmarshalJSON(data []byte) error {
	var value struct {
		SessionID          string                    `json:"session_id"`
		TabID              string                    `json:"tab_id"`
		SnapshotGeneration json.RawMessage           `json:"snapshot_generation"`
		URL                string                    `json:"url"`
		Origin             string                    `json:"origin"`
		Title              string                    `json:"title,omitempty"`
		Snapshot           string                    `json:"snapshot"`
		Elements           []BrowserElement          `json:"elements"`
		PendingDialog      *BrowserDialogObservation `json:"pending_dialog,omitempty"`
		Truncated          bool                      `json:"truncated"`
		ProtectedResult    bool                      `json:"protected_result,omitempty"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	generation, err := decodeCanonicalBrowserGeneration(value.SnapshotGeneration)
	if err != nil {
		return fmt.Errorf("decode browser observation generation: %w", err)
	}
	*result = BrowserObservationResult{
		SessionID: value.SessionID, TabID: value.TabID, SnapshotGeneration: generation,
		URL: value.URL, Origin: value.Origin, Title: value.Title, Snapshot: value.Snapshot,
		Elements: value.Elements, PendingDialog: value.PendingDialog, Truncated: value.Truncated,
		ProtectedResult: value.ProtectedResult,
	}
	return nil
}

type BrowserActResult struct {
	ActionInvocationID string                    `json:"action_invocation_id"`
	State              string                    `json:"state"`
	Reason             string                    `json:"reason,omitempty"`
	Observation        *BrowserObservationResult `json:"observation,omitempty"`
}

type BrowserHostOpenRequest struct {
	SessionID             string
	RoutedSessionID       string
	Profile               string
	ProfileRevision       string
	BrowserPolicyRevision string
	AgentID               string
	ActorID               string
	DryRun                bool
	Limits                BrowserLimits
}

type BrowserHostStatusRequest struct {
	SessionID       string
	RoutedSessionID string
	ProfileRevision string
	AgentID         string
	ActorID         string
}

type BrowserHostObserveRequest struct {
	SessionID          string
	RoutedSessionID    string
	TabID              string
	SnapshotGeneration uint64
	Screenshot         bool
	AgentID            string
	ActorID            string
}

type BrowserHostActRequest struct {
	SessionID               string
	RoutedSessionID         string
	TabID                   string
	SnapshotGeneration      uint64
	ActionInvocationID      string
	Action                  BrowserAction
	Effect                  string
	CurrentOrigin           string
	PreparedActionHash      string
	BrowserPolicyRevision   string
	ProfileRevision         string
	ApprovalDigest          string
	ExpectedRole            string
	ExpectedName            string
	DestinationExpectedRole string
	DestinationExpectedName string
	DialogType              string
	DialogMessageDigest     string
	DialogMessageBytes      int
	InputDigest             string
	InputBytes              int
	AgentID                 string
	ActorID                 string
}

type BrowserHostContextRequest struct {
	SessionID       string
	RoutedSessionID string
	ProfileRevision string
	Operation       string
	RequestID       string
	Authority       *BrowserContextCatalog
	TabID           string
	FrameID         string
	AgentID         string
	ActorID         string
}

func (profile BrowserProfileDescriptor) Validate() error {
	if err := (Alias(profile.Alias)).Validate(); err != nil ||
		!validInvocationIdentifier(profile.Revision) ||
		profile.Driver != BrowserDriverPlaywrightMCP || profile.Mode != BrowserProfileManaged ||
		(profile.NetworkMode != BrowserNetworkExactOrigins &&
			profile.NetworkMode != BrowserNetworkPublicWeb && profile.NetworkMode != BrowserNetworkAnyHTTP) ||
		profile.DryRun == profile.AllowApprovedActions || len(profile.Actions) == 0 ||
		len(profile.Actions) > MaxBrowserActions || !sort.StringsAreSorted(profile.Actions) {
		return fmt.Errorf("%w: malformed browser profile descriptor", ErrInvalidCapability)
	}
	seen := make(map[string]struct{}, len(profile.Actions))
	for _, action := range profile.Actions {
		if action != "check" && action != "click" && action != "dialog" && action != "download" && action != "drag" &&
			action != "fill" && action != "hover" && action != "navigate" && action != "press" &&
			action != "scroll" && action != "select" && action != "uncheck" {
			return fmt.Errorf("%w: unsupported browser action", ErrInvalidCapability)
		}
		if _, duplicate := seen[action]; duplicate {
			return fmt.Errorf("%w: duplicate browser action", ErrInvalidCapability)
		}
		seen[action] = struct{}{}
	}
	return profile.Limits.Validate()
}

func IsBrowserCommand(name string) bool {
	switch name {
	case BrowserCommandSessionOpen, BrowserCommandSessionStatus, BrowserCommandObserve,
		BrowserCommandAct, BrowserCommandContexts, BrowserCommandSessionClose:
		return true
	default:
		return false
	}
}

// BrowserCommandDescriptors returns the internal typed capability catalog for
// one or more already-normalized companion profiles. It intentionally omits a
// model contract: the gateway browser broker, not nodes_invoke, is the only
// model-facing surface.
func BrowserCommandDescriptors(profiles []BrowserProfileDescriptor) ([]CommandDescriptor, error) {
	profiles = CloneBrowserProfileDescriptors(profiles)
	if err := validateBrowserProfiles(profiles); err != nil {
		return nil, err
	}
	commands := []struct {
		name string
		risk Risk
	}{
		{BrowserCommandSessionOpen, RiskWrite},
		{BrowserCommandSessionStatus, RiskRead},
		{BrowserCommandObserve, RiskRead},
		{BrowserCommandAct, RiskWrite},
		{BrowserCommandContexts, RiskWrite},
		{BrowserCommandSessionClose, RiskWrite},
	}
	result := make([]CommandDescriptor, 0, len(commands))
	for _, command := range commands {
		result = append(result, CommandDescriptor{
			Name:            command.name,
			InputSchema:     BrowserCommandInputSchema(command.name, profiles),
			OutputSchema:    BrowserCommandOutputSchema(command.name, profiles),
			Risk:            command.risk,
			BrowserProfiles: CloneBrowserProfileDescriptors(profiles),
		})
	}
	if err := (CapabilityCatalog{Commands: result}).Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

func validateBrowserProfiles(profiles []BrowserProfileDescriptor) error {
	if len(profiles) == 0 || len(profiles) > MaxBrowserProfiles {
		return fmt.Errorf("%w: malformed browser profile count", ErrInvalidCapability)
	}
	prior := ""
	revisions := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if err := profile.Validate(); err != nil {
			return err
		}
		if prior != "" && profile.Alias <= prior {
			return fmt.Errorf("%w: browser profiles are not sorted", ErrInvalidCapability)
		}
		if _, duplicate := revisions[profile.Revision]; duplicate {
			return fmt.Errorf("%w: duplicate browser profile revision", ErrInvalidCapability)
		}
		revisions[profile.Revision] = struct{}{}
		prior = profile.Alias
	}
	return nil
}

func CloneBrowserProfileDescriptors(profiles []BrowserProfileDescriptor) []BrowserProfileDescriptor {
	result := make([]BrowserProfileDescriptor, len(profiles))
	for index := range profiles {
		result[index] = profiles[index]
		result[index].Actions = append([]string(nil), profiles[index].Actions...)
	}
	return result
}

func BrowserCommandInputSchema(command string, profiles []BrowserProfileDescriptor) json.RawMessage {
	return browserCommandInputSchema(command, profiles, []string{"read", "navigation", "download"}, false)
}

// legacyBrowserCommandInputSchema returns the exact browser input contract
// emitted before scroll was admitted. It is used only while validating a
// persisted companion catalog during a rolling upgrade; fresh discovery is
// always generated through BrowserCommandInputSchema.
func legacyBrowserCommandInputSchema(command string, profiles []BrowserProfileDescriptor) json.RawMessage {
	return browserCommandInputSchema(command, profiles, []string{"navigation", "download"}, true)
}

// legacyDryRunBrowserCommandInputSchema returns the exact session-open input
// contract emitted immediately before approved-action mode was admitted. It
// is accepted only for persisted catalogs whose profiles retain the legacy
// dry-run-only authority; fresh discovery always emits the current schema.
func legacyDryRunBrowserCommandInputSchema(
	command string,
	profiles []BrowserProfileDescriptor,
) json.RawMessage {
	return browserCommandInputSchema(command, profiles, []string{"read", "navigation", "download"}, true)
}

func browserCommandInputSchema(
	command string,
	profiles []BrowserProfileDescriptor,
	actEffects []string,
	legacyDryRunOpen bool,
) json.RawMessage {
	profileBranches := make([]any, 0, len(profiles))
	actionBranches := make([]any, 0, len(profiles))
	profileRevisions := make([]string, 0, len(profiles))
	allActions := make(map[string]struct{})
	for _, profile := range profiles {
		profileRevisions = append(profileRevisions, profile.Revision)
		profileRequired := []string{"profile", "profile_revision"}
		profileProperties := map[string]any{
			"profile":          map[string]any{"const": profile.Alias},
			"profile_revision": map[string]any{"const": profile.Revision},
			"limits":           browserLimitsSchema(profile.Limits),
		}
		if !legacyDryRunOpen {
			profileRequired = append(profileRequired, "dry_run")
			profileProperties["dry_run"] = map[string]any{"const": profile.DryRun}
		}
		profileBranches = append(profileBranches, map[string]any{
			"required": profileRequired, "properties": profileProperties,
		})
		for _, action := range profile.Actions {
			allActions[action] = struct{}{}
		}
		for _, action := range profile.Actions {
			effect := "navigation"
			switch action {
			case "dialog":
				effect = "external_commit"
			case "download":
				effect = "download"
			case "check", "fill", "select", "uncheck":
				effect = "local_edit"
			case "hover":
				effect = "read"
			case "drag":
				effect = "unknown"
			case "press":
				effect = "unknown"
			case "scroll":
				effect = "read"
			case "click":
				effect = "unknown"
			}
			required := []string{"profile_revision", "action", "effect"}
			if action == "download" || action == "click" || action == "drag" || action == "press" {
				required = append(required, "approval_digest")
			}
			properties := map[string]any{
				"profile_revision": map[string]any{"const": profile.Revision},
				"action":           browserActionSchema([]string{action}),
				"effect":           map[string]any{"const": effect},
			}
			if action == "click" || action == "fill" || action == "select" || action == "check" ||
				action == "uncheck" || action == "hover" || action == "drag" {
				required = append(required, "expected_role")
				properties["expected_role"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
				properties["expected_name"] = map[string]any{"type": "string", "maxLength": 4096}
			}
			if action == "drag" {
				required = append(required, "destination_expected_role")
				properties["destination_expected_role"] = map[string]any{
					"type": "string", "minLength": 1, "maxLength": 128,
				}
				properties["destination_expected_name"] = map[string]any{"type": "string", "maxLength": 4096}
			}
			if action == "fill" || action == "select" {
				required = append(required, "input_digest", "input_bytes")
				if action == "select" {
					properties["expected_role"] = map[string]any{"const": "combobox"}
				} else {
					properties["expected_role"] = map[string]any{"enum": []string{"searchbox", "textbox"}}
				}
				properties["input_digest"] = map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}
				properties["input_bytes"] = map[string]any{
					"type": "integer", "minimum": 1, "maximum": MaxBrowserTextInputBytes,
				}
			}
			if action == "check" || action == "uncheck" {
				properties["expected_role"] = map[string]any{"enum": []string{"checkbox", "radio", "switch"}}
				if action == "uncheck" {
					properties["expected_role"] = map[string]any{"enum": []string{"checkbox", "switch"}}
				}
			}
			if action == "dialog" {
				required = append(required, "dialog_type", "dialog_message_digest", "dialog_message_bytes")
				properties["effect"] = map[string]any{"enum": []string{"read", "external_commit"}}
				properties["dialog_type"] = map[string]any{
					"enum": []string{"alert", "beforeunload", "confirm", "prompt"},
				}
				properties["dialog_message_digest"] = map[string]any{
					"type": "string", "pattern": "^[a-f0-9]{64}$",
				}
				properties["dialog_message_bytes"] = map[string]any{
					"type": "integer", "minimum": 0, "maximum": MaxBrowserDialogMessageBytes,
				}
				properties["input_digest"] = map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}
				properties["input_bytes"] = map[string]any{
					"type": "integer", "minimum": 0, "maximum": MaxBrowserTextInputBytes,
				}
			}
			if action == "click" {
				properties["effect"] = map[string]any{"enum": []string{"external_commit", "unknown"}}
			}
			branch := map[string]any{
				"required":   required,
				"properties": properties,
			}
			if action == "fill" || action == "select" {
				branch["not"] = map[string]any{"required": []string{"approval_digest"}}
			}
			if action == "check" || action == "uncheck" || action == "hover" {
				branch["not"] = map[string]any{"anyOf": []any{
					map[string]any{"required": []string{"approval_digest"}},
					map[string]any{"required": []string{"input_digest"}},
					map[string]any{"required": []string{"input_bytes"}},
				}}
			}
			if action == "press" {
				branch["not"] = map[string]any{"anyOf": []any{
					map[string]any{"required": []string{"expected_role"}},
					map[string]any{"required": []string{"expected_name"}},
					map[string]any{"required": []string{"input_digest"}},
					map[string]any{"required": []string{"input_bytes"}},
				}}
			}
			if action == "click" {
				branch["oneOf"] = []any{
					map[string]any{"properties": map[string]any{
						"expected_role": map[string]any{"const": "button"},
						"effect":        map[string]any{"const": "external_commit"},
					}},
					map[string]any{"properties": map[string]any{
						"expected_role": map[string]any{"not": map[string]any{"const": "button"}},
						"effect":        map[string]any{"const": "unknown"},
					}},
				}
			}
			if action == "dialog" {
				decisionConstraint := []any{
					map[string]any{
						"required": []string{"approval_digest"},
						"properties": map[string]any{
							"action": map[string]any{"properties": map[string]any{
								"decision": map[string]any{"const": "accept"},
							}},
							"effect": map[string]any{"const": "external_commit"},
						},
					},
					map[string]any{
						"properties": map[string]any{
							"action": map[string]any{"properties": map[string]any{
								"decision":        map[string]any{"const": "dismiss"},
								"prompt_provided": map[string]any{"const": false},
							}},
							"effect": map[string]any{"const": "read"},
						},
						"not": map[string]any{"anyOf": []any{
							map[string]any{"required": []string{"approval_digest"}},
							map[string]any{"required": []string{"input_digest"}},
							map[string]any{"required": []string{"input_bytes"}},
						}},
					},
				}
				promptConstraint := []any{
					map[string]any{
						"required": []string{"input_digest"},
						"properties": map[string]any{
							"action": map[string]any{
								"required": []string{"prompt_provided"},
								"properties": map[string]any{
									"prompt_provided": map[string]any{"const": true},
								},
							},
							"dialog_type": map[string]any{"const": "prompt"},
						},
					},
					map[string]any{
						"properties": map[string]any{
							"action": map[string]any{"properties": map[string]any{
								"prompt_provided": map[string]any{"const": false},
							}},
						},
						"not": map[string]any{"anyOf": []any{
							map[string]any{"required": []string{"input_digest"}},
							map[string]any{"required": []string{"input_bytes"}},
						}},
					},
				}
				branch["allOf"] = []any{
					map[string]any{"oneOf": decisionConstraint},
					map[string]any{"oneOf": promptConstraint},
				}
			}
			forbiddenFields := []string{"destination_expected_role", "destination_expected_name"}
			if action == "drag" {
				forbiddenFields = []string{
					"dialog_type", "dialog_message_digest", "dialog_message_bytes", "input_digest", "input_bytes",
				}
			}
			forbidden := make([]any, 0, len(forbiddenFields))
			for _, field := range forbiddenFields {
				forbidden = append(forbidden, map[string]any{"required": []string{field}})
			}
			constraint := map[string]any{"not": map[string]any{"anyOf": forbidden}}
			if existing, ok := branch["allOf"].([]any); ok {
				branch["allOf"] = append(existing, constraint)
			} else {
				branch["allOf"] = []any{constraint}
			}
			actionBranches = append(actionBranches, branch)
		}
	}
	actions := make([]string, 0, len(allActions))
	for action := range allActions {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	identifier := map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength}
	digest := map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}
	properties := map[string]any{}
	required := []string{}
	var profileConstraint any
	add := func(name string, schema any) {
		properties[name] = schema
		required = append(required, name)
	}
	switch command {
	case BrowserCommandSessionOpen:
		add("session_id", identifier)
		add("profile", identifier)
		add("profile_revision", identifier)
		profileConstraint = map[string]any{"oneOf": profileBranches}
		add("browser_policy_revision", digest)
		if legacyDryRunOpen {
			add("dry_run", map[string]any{"const": true})
		} else {
			add("dry_run", map[string]any{"type": "boolean"})
		}
		add("limits", browserLimitsSchema(BrowserLimits{}.Effective()))
	case BrowserCommandSessionStatus, BrowserCommandSessionClose:
		add("session_id", identifier)
		add("profile_revision", map[string]any{"enum": profileRevisions})
	case BrowserCommandObserve:
		add("session_id", identifier)
		add("tab_id", identifier)
		add("snapshot_generation", map[string]any{"type": "integer", "minimum": 1})
		add("screenshot", map[string]any{"type": "boolean"})
	case BrowserCommandAct:
		if _, hasClick := allActions["click"]; hasClick {
			actEffects = append(actEffects, "external_commit", "unknown")
		}
		if _, hasPress := allActions["press"]; hasPress && !slices.Contains(actEffects, "unknown") {
			actEffects = append(actEffects, "unknown")
		}
		if _, hasDrag := allActions["drag"]; hasDrag && !slices.Contains(actEffects, "unknown") {
			actEffects = append(actEffects, "unknown")
		}
		if _, hasDialog := allActions["dialog"]; hasDialog && !slices.Contains(actEffects, "external_commit") {
			actEffects = append(actEffects, "external_commit")
		}
		if _, hasFill := allActions["fill"]; hasFill {
			actEffects = append(actEffects, "local_edit")
		}
		if _, hasCheck := allActions["check"]; hasCheck && !slices.Contains(actEffects, "local_edit") {
			actEffects = append(actEffects, "local_edit")
		}
		if _, hasUncheck := allActions["uncheck"]; hasUncheck && !slices.Contains(actEffects, "local_edit") {
			actEffects = append(actEffects, "local_edit")
		}
		if _, hasSelect := allActions["select"]; hasSelect && !slices.Contains(actEffects, "local_edit") {
			actEffects = append(actEffects, "local_edit")
		}
		add("session_id", identifier)
		add("tab_id", identifier)
		add("snapshot_generation", map[string]any{"type": "integer", "minimum": 1})
		add("action_invocation_id", identifier)
		add("action", browserActionSchema(actions))
		add("effect", map[string]any{"enum": actEffects})
		add("current_origin", map[string]any{
			"type": "string", "minLength": 1, "maxLength": MaxBrowserURLBytes,
		})
		add("prepared_action_hash", digest)
		add("browser_policy_revision", digest)
		add("profile_revision", identifier)
		_, hasClick := allActions["click"]
		_, hasFill := allActions["fill"]
		_, hasSelect := allActions["select"]
		_, hasDialog := allActions["dialog"]
		_, hasCheck := allActions["check"]
		_, hasUncheck := allActions["uncheck"]
		_, hasHover := allActions["hover"]
		_, hasDrag := allActions["drag"]
		if hasClick || hasFill || hasSelect || hasCheck || hasUncheck || hasHover || hasDrag {
			properties["expected_role"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
			properties["expected_name"] = map[string]any{"type": "string", "maxLength": 4096}
		}
		if hasDrag {
			properties["destination_expected_role"] = map[string]any{
				"type": "string", "minLength": 1, "maxLength": 128,
			}
			properties["destination_expected_name"] = map[string]any{"type": "string", "maxLength": 4096}
		}
		if hasFill || hasSelect || hasDialog {
			properties["input_digest"] = digest
			minimumInputBytes := 1
			if hasDialog {
				minimumInputBytes = 0
			}
			properties["input_bytes"] = map[string]any{
				"type": "integer", "minimum": minimumInputBytes, "maximum": MaxBrowserTextInputBytes,
			}
		}
		if hasDialog {
			properties["dialog_type"] = map[string]any{
				"enum": []string{"alert", "beforeunload", "confirm", "prompt"},
			}
			properties["dialog_message_digest"] = digest
			properties["dialog_message_bytes"] = map[string]any{
				"type": "integer", "minimum": 0, "maximum": MaxBrowserDialogMessageBytes,
			}
		}
		properties["approval_digest"] = digest
		profileConstraint = map[string]any{"oneOf": actionBranches}
	case BrowserCommandContexts:
		add("session_id", identifier)
		add("profile_revision", map[string]any{"enum": profileRevisions})
		add("operation", map[string]any{"enum": []string{"list", "open", "select", "close"}})
		add("request_id", identifier)
		properties["authority_digest"] = digest
		properties["context_catalog_id"] = identifier
		properties["context_generation"] = map[string]any{"type": "integer", "minimum": 1}
		properties["authority_bytes"] = map[string]any{
			"type": "integer", "minimum": 1, "maximum": MaxBrowserContextInputBytes,
		}
		properties["tab_id"] = identifier
		properties["frame_id"] = identifier
		profileConstraint = map[string]any{"oneOf": []any{
			map[string]any{
				"properties": map[string]any{"operation": map[string]any{"enum": []string{"list", "open"}}},
				"not": map[string]any{"anyOf": []any{
					map[string]any{"required": []string{"authority_digest"}},
					map[string]any{"required": []string{"authority_bytes"}},
					map[string]any{"required": []string{"context_catalog_id"}},
					map[string]any{"required": []string{"context_generation"}},
					map[string]any{"required": []string{"tab_id"}},
					map[string]any{"required": []string{"frame_id"}},
				}},
			},
			map[string]any{
				"required": []string{
					"authority_digest", "authority_bytes", "context_catalog_id", "context_generation", "tab_id",
				},
				"properties": map[string]any{"operation": map[string]any{"const": "select"}},
			},
			map[string]any{
				"required": []string{
					"authority_digest", "authority_bytes", "context_catalog_id", "context_generation", "tab_id",
				},
				"properties": map[string]any{"operation": map[string]any{"const": "close"}},
				"not":        map[string]any{"required": []string{"frame_id"}},
			},
		}}
	default:
		return json.RawMessage("false")
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": required, "properties": properties,
	}
	if profileConstraint != nil {
		schema["allOf"] = []any{profileConstraint}
	}
	return mustJSON(schema)
}

func BrowserCommandOutputSchema(
	command string,
	profiles []BrowserProfileDescriptor,
) json.RawMessage {
	if len(profiles) == 0 {
		return json.RawMessage("false")
	}
	limits := strictestBrowserLimits(profiles)
	identifier := map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength}
	state := map[string]any{
		"enum": []string{"opening", "ready", "closing", "closed", "lost", "unknown"},
	}
	safeReason := map[string]any{"type": "string", "maxLength": 128}
	baseStatus := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"session_id", "state"},
		"properties": map[string]any{
			"session_id": identifier, "state": state, "reason": safeReason,
			"recovery": map[string]any{"enum": []string{"none", "retry_status", "close", "operator"}},
		},
	}
	switch command {
	case BrowserCommandSessionStatus, BrowserCommandSessionClose:
		return mustJSON(baseStatus)
	case BrowserCommandSessionOpen:
		return mustJSON(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required": []string{
				"session_id",
				"state",
				"tab_id",
				"controller",
				"features",
				"expires_at",
				"idle_expires_at",
			},
			"properties": map[string]any{
				"session_id": identifier, "state": state, "tab_id": identifier,
				"controller": map[string]any{"const": "agent"},
				"features": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"observe", "navigate", "contexts", "screenshot", "download"},
					"properties": map[string]any{
						"observe": map[string]any{"type": "boolean"}, "navigate": map[string]any{"type": "boolean"},
						"contexts":   map[string]any{"type": "boolean"},
						"screenshot": map[string]any{"type": "boolean"}, "download": map[string]any{"type": "boolean"},
					},
				},
				"expires_at":      map[string]any{"type": "integer", "minimum": 1},
				"idle_expires_at": map[string]any{"type": "integer", "minimum": 1},
			},
		})
	case BrowserCommandObserve:
		return mustJSON(map[string]any{"oneOf": []any{
			rawSchema(browserObservationSchema(limits)),
			browserProtectedResultReceiptSchema(nil),
		}})
	case BrowserCommandAct:
		return mustJSON(map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"action_invocation_id", "state"},
			"properties": map[string]any{
				"action_invocation_id": identifier,
				"state":                map[string]any{"enum": []string{"accepted", "succeeded", "failed", "unknown"}},
				"reason":               safeReason,
				"observation":          rawSchema(browserObservationSchema(limits)),
				"artifact":             browserArtifactSchema(limits.DownloadBytes),
			},
		})
	case BrowserCommandContexts:
		return mustJSON(map[string]any{"oneOf": []any{
			browserContextCommandResultSchema(limits),
			browserProtectedResultReceiptSchema(map[string]any{
				"operation": map[string]any{"enum": []string{"list", "open", "select", "close"}},
			}),
		}})
	default:
		return json.RawMessage("false")
	}
}

func browserContextCommandResultSchema(limits BrowserLimits) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"operation", "context_catalog"},
		"properties": map[string]any{
			"operation":       map[string]any{"enum": []string{"list", "open", "select", "close"}},
			"context_catalog": browserContextCatalogSchema(),
			"observation":     rawSchema(browserObservationSchema(limits)),
		},
		"allOf": []any{map[string]any{"oneOf": []any{
			map[string]any{
				"properties": map[string]any{"operation": map[string]any{"const": "select"}},
				"required":   []string{"observation"},
			},
			map[string]any{
				"properties": map[string]any{
					"operation": map[string]any{"enum": []string{"list", "open", "close"}},
				},
				"not": map[string]any{"required": []string{"observation"}},
			},
		}}},
	}
}

// legacyBrowserPageResultOutputSchema is the exact observe/context result
// contract advertised before protected recovery receipts were introduced. It
// exists only for fail-closed registry migration; live catalogs must advertise
// BrowserCommandOutputSchema and renew approval against its new catalog hash.
func legacyBrowserPageResultOutputSchema(
	command string,
	profiles []BrowserProfileDescriptor,
) json.RawMessage {
	if len(profiles) == 0 {
		return json.RawMessage("false")
	}
	limits := strictestBrowserLimits(profiles)
	switch command {
	case BrowserCommandObserve:
		return browserObservationSchema(limits)
	case BrowserCommandContexts:
		return mustJSON(browserContextCommandResultSchema(limits))
	default:
		return json.RawMessage("false")
	}
}

// legacyBrowserSessionOpenOutputSchema is the exact session-open result
// contract advertised before browser context discovery was introduced. It is
// retained only to recognize durable registry records during a fail-closed
// upgrade; live catalogs must always advertise BrowserCommandOutputSchema.
func legacyBrowserSessionOpenOutputSchema(profiles []BrowserProfileDescriptor) json.RawMessage {
	if len(profiles) == 0 {
		return json.RawMessage("false")
	}
	identifier := map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength}
	state := map[string]any{
		"enum": []string{"opening", "ready", "closing", "closed", "lost", "unknown"},
	}
	return mustJSON(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"session_id",
			"state",
			"tab_id",
			"controller",
			"features",
			"expires_at",
			"idle_expires_at",
		},
		"properties": map[string]any{
			"session_id": identifier, "state": state, "tab_id": identifier,
			"controller": map[string]any{"const": "agent"},
			"features": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"observe", "navigate", "screenshot", "download"},
				"properties": map[string]any{
					"observe":    map[string]any{"type": "boolean"},
					"navigate":   map[string]any{"type": "boolean"},
					"screenshot": map[string]any{"type": "boolean"},
					"download":   map[string]any{"type": "boolean"},
				},
			},
			"expires_at":      map[string]any{"type": "integer", "minimum": 1},
			"idle_expires_at": map[string]any{"type": "integer", "minimum": 1},
		},
	})
}

func browserContextCatalogSchema() map[string]any {
	identifier := map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength}
	generation := map[string]any{"type": "integer", "minimum": 1}
	location := map[string]any{"type": "string", "minLength": 1, "maxLength": MaxBrowserURLBytes}
	frame := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{
			"frame_id", "creation_sequence", "depth", "document_generation",
			"url", "origin", "availability",
		},
		"properties": map[string]any{
			"frame_id": identifier, "parent_frame_id": identifier,
			"creation_sequence":   generation,
			"depth":               map[string]any{"type": "integer", "minimum": 1, "maximum": 8},
			"document_generation": generation,
			"url":                 location, "origin": location,
			"label":        map[string]any{"type": "string", "maxLength": 512},
			"availability": map[string]any{"enum": []string{"ready", "unavailable"}},
			"safe_failure": map[string]any{"type": "string", "maxLength": 128},
		},
	}
	tab := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{
			"tab_id", "kind", "creation_sequence", "document_generation", "url", "origin",
		},
		"properties": map[string]any{
			"tab_id": identifier, "kind": map[string]any{"enum": []string{"primary", "tab", "popup"}},
			"creation_sequence": generation, "opener_tab_id": identifier,
			"opener_invocation_id": identifier, "document_generation": generation,
			"url": location, "origin": location,
			"title":               map[string]any{"type": "string", "maxLength": 512},
			"frames":              map[string]any{"type": "array", "maxItems": 64, "items": frame},
			"omitted_frame_count": map[string]any{"type": "integer", "minimum": 0},
			"frames_truncated":    map[string]any{"type": "boolean"},
		},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{
			"context_catalog_id", "context_generation", "selected_tab_id", "tabs",
		},
		"properties": map[string]any{
			"context_catalog_id": identifier, "context_generation": generation,
			"selected_tab_id": identifier, "selected_frame_id": identifier,
			"tabs":              map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "items": tab},
			"omitted_tab_count": map[string]any{"type": "integer", "minimum": 0},
			"truncated":         map[string]any{"type": "boolean"},
		},
	}
}

func browserLimitsSchema(maximum BrowserLimits) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"sessions",
			"tabs",
			"session_seconds",
			"idle_seconds",
			"prepared_seconds",
			"action_seconds",
			"snapshot_bytes",
			"screenshot_bytes",
			"upload_bytes",
			"download_bytes",
			"snapshot_refs",
			"text_input_bytes",
			"tool_result_bytes",
			"retention_seconds",
		},
		"properties": map[string]any{
			"sessions":         map[string]any{"const": maximum.Sessions},
			"tabs":             map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.Tabs},
			"session_seconds":  map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.SessionSeconds},
			"idle_seconds":     map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.IdleSeconds},
			"prepared_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.PreparedSeconds},
			"action_seconds":   map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.ActionSeconds},
			"snapshot_bytes":   map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.SnapshotBytes},
			"screenshot_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.ScreenshotBytes},
			"upload_bytes":     map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.UploadBytes},
			"download_bytes":   map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.DownloadBytes},
			"snapshot_refs":    map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.SnapshotRefs},
			"text_input_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.TextInputBytes},
			"tool_result_bytes": map[string]any{
				"type":    "integer",
				"minimum": MinBrowserToolResultBytes,
				"maximum": maximum.ToolResultBytes,
			},
			"retention_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": maximum.RetentionSecs},
		},
	}
}

func browserActionSchema(actions []string) map[string]any {
	branches := make([]any, 0, len(actions))
	for _, action := range actions {
		switch action {
		case "navigate":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "url"},
				"properties": map[string]any{
					"kind": map[string]any{"const": "navigate"},
					"url":  map[string]any{"type": "string", "minLength": 1, "maxLength": MaxBrowserURLBytes},
				},
			})
		case "download":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "ref"},
				"properties": map[string]any{
					"kind": map[string]any{"const": "download"},
					"ref":  map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
				},
			})
		case "scroll":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "direction", "amount"},
				"properties": map[string]any{
					"kind":      map[string]any{"const": "scroll"},
					"direction": map[string]any{"enum": []string{"up", "down"}},
					"amount": map[string]any{
						"type": "integer", "minimum": 1, "maximum": MaxBrowserScrollAmount,
					},
				},
			})
		case "click":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "ref"},
				"properties": map[string]any{
					"kind": map[string]any{"const": "click"},
					"ref":  map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
				},
			})
		case "check", "uncheck", "hover":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "ref"},
				"properties": map[string]any{
					"kind": map[string]any{"const": action},
					"ref":  map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
				},
			})
		case "drag":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "source_ref", "destination_ref"},
				"properties": map[string]any{
					"kind":            map[string]any{"const": "drag"},
					"source_ref":      map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
					"destination_ref": map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
				},
			})
		case "dialog":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "dialog_id", "decision"},
				"properties": map[string]any{
					"kind":            map[string]any{"const": "dialog"},
					"dialog_id":       map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
					"decision":        map[string]any{"enum": []string{"accept", "dismiss"}},
					"prompt_provided": map[string]any{"type": "boolean"},
				},
			})
		case "file_chooser":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "ref", "artifact_ref"},
				"properties": map[string]any{
					"kind":         map[string]any{"const": "file_chooser"},
					"ref":          map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
					"artifact_ref": map[string]any{"type": "string", "minLength": 1, "maxLength": 512},
				},
			})
		case "select":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "ref"},
				"properties": map[string]any{
					"kind": map[string]any{"const": "select"},
					"ref":  map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
				},
			})
		case "fill":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "ref"},
				"properties": map[string]any{
					"kind": map[string]any{"const": "fill"},
					"ref":  map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
				},
			})
		case "press":
			branches = append(branches, map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"kind", "target", "key"},
				"properties": map[string]any{
					"kind":   map[string]any{"const": "press"},
					"target": map[string]any{"const": "document"},
					"key": map[string]any{"enum": []string{
						"Enter", "Space", "Escape", "Tab", "Shift+Tab", "ArrowUp", "ArrowDown",
						"ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown", "Backspace", "Delete",
					}},
				},
			})
		}
	}
	return map[string]any{"oneOf": branches}
}

func browserObservationSchema(limits BrowserLimits) json.RawMessage {
	return mustJSON(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"session_id",
			"tab_id",
			"snapshot_generation",
			"url",
			"origin",
			"snapshot",
			"elements",
			"truncated",
		},
		"properties": map[string]any{
			"session_id":          map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
			"tab_id":              map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
			"snapshot_generation": map[string]any{"type": "integer", "minimum": 1},
			"url":                 map[string]any{"type": "string", "maxLength": MaxBrowserURLBytes},
			"origin":              map[string]any{"type": "string", "maxLength": MaxBrowserURLBytes},
			"title":               map[string]any{"type": "string", "maxLength": MaxBrowserTitleBytes},
			"snapshot":            map[string]any{"type": "string", "maxLength": limits.SnapshotBytes},
			"truncated":           map[string]any{"type": "boolean"},
			"elements": map[string]any{
				"type": "array", "maxItems": limits.SnapshotRefs,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"ref", "role", "name"},
					"properties": map[string]any{
						"ref":  map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
						"role": map[string]any{"type": "string", "maxLength": 128},
						"name": map[string]any{"type": "string", "maxLength": 4096},
					},
				},
			},
			"pending_dialog": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"type", "message"},
				"properties": map[string]any{
					"type": map[string]any{"enum": []string{"alert", "beforeunload", "confirm", "prompt"}},
					"message": map[string]any{
						"type": "string", "maxLength": MaxBrowserDialogMessageBytes,
					},
				},
			},
			"screenshot": browserArtifactSchema(limits.ScreenshotBytes),
		},
	})
}

func browserArtifactSchema(maximumBytes int) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"transfer_id", "sha256", "size", "content_type"},
		"properties": map[string]any{
			"transfer_id":  map[string]any{"type": "string", "minLength": 1, "maxLength": MaxIDLength},
			"sha256":       map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"},
			"size":         map[string]any{"type": "integer", "minimum": 1, "maximum": maximumBytes},
			"content_type": map[string]any{"type": "string", "minLength": 1, "maxLength": 255},
		},
	}
}

func browserProtectedResultReceiptSchema(properties map[string]any) map[string]any {
	required := []string{"protected_result"}
	resultProperties := map[string]any{
		"protected_result": map[string]any{"const": true},
	}
	for name, schema := range properties {
		resultProperties[name] = schema
		required = append(required, name)
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": required, "properties": resultProperties,
	}
}

func validateBrowserInvocationInput(command string, input map[string]any) error {
	switch command {
	case BrowserCommandSessionOpen:
		return validateBrowserSessionOpenLimits(input)
	case BrowserCommandAct:
		action, ok := input["action"].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: malformed browser action", ErrInvalidInvocation)
		}
		if action["kind"] == "navigate" {
			if err := validateBrowserStringBytes(action, "url", MaxBrowserURLBytes, true); err != nil {
				return err
			}
			return validateBrowserStringBytes(input, "current_origin", MaxBrowserURLBytes, true)
		}
		if action["kind"] == "click" || action["kind"] == "drag" {
			if err := validateBrowserStringBytes(input, "current_origin", MaxBrowserURLBytes, true); err != nil {
				return err
			}
			if err := validateBrowserStringBytes(input, "expected_role", 128, true); err != nil {
				return err
			}
			if err := validateBrowserStringBytes(input, "expected_name", 4096, false); err != nil {
				return err
			}
			if action["kind"] == "drag" {
				if err := validateBrowserStringBytes(action, "source_ref", MaxIDLength, true); err != nil {
					return err
				}
				if err := validateBrowserStringBytes(action, "destination_ref", MaxIDLength, true); err != nil {
					return err
				}
				if action["source_ref"] == action["destination_ref"] {
					return fmt.Errorf("%w: browser drag references must be distinct", ErrInvalidInvocation)
				}
				if err := validateBrowserStringBytes(input, "destination_expected_role", 128, true); err != nil {
					return err
				}
				return validateBrowserStringBytes(input, "destination_expected_name", 4096, false)
			}
			return nil
		}
	}
	return nil
}

func validateBrowserSessionOpenLimits(input map[string]any) error {
	encoded, err := json.Marshal(input["limits"])
	if err != nil {
		return fmt.Errorf("%w: encode browser session limits", ErrInvalidInvocation)
	}
	limits, err := decodeCanonicalBrowserLimits(encoded)
	if err != nil {
		return fmt.Errorf("%w: decode browser session limits", ErrInvalidInvocation)
	}
	if err = limits.Validate(); err != nil {
		return fmt.Errorf("%w: malformed browser session limits", ErrInvalidInvocation)
	}
	return nil
}

func validateBrowserInvocationOutput(
	command string,
	limits BrowserLimits,
	output map[string]any,
) error {
	switch command {
	case BrowserCommandObserve:
		if output["protected_result"] == true {
			return nil
		}
		return validateBrowserObservationBytes(output, limits)
	case BrowserCommandContexts:
		if output["protected_result"] == true {
			return nil
		}
		return nil
	case BrowserCommandAct:
		observation, present := output["observation"]
		if !present {
			return nil
		}
		value, ok := observation.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: malformed browser observation", ErrInvalidInvocation)
		}
		return validateBrowserObservationBytes(value, limits)
	default:
		return nil
	}
}

func validateBrowserObservationBytes(
	observation map[string]any,
	limits BrowserLimits,
) error {
	for _, field := range []struct {
		name     string
		maximum  int
		required bool
	}{
		{"url", MaxBrowserURLBytes, true},
		{"origin", MaxBrowserURLBytes, true},
		{"title", MaxBrowserTitleBytes, false},
		{"snapshot", limits.SnapshotBytes, true},
	} {
		if err := validateBrowserStringBytes(
			observation,
			field.name,
			field.maximum,
			field.required,
		); err != nil {
			return err
		}
	}
	if pending, present := observation["pending_dialog"]; present {
		dialog, ok := pending.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: malformed browser pending dialog", ErrInvalidInvocation)
		}
		if err := validateBrowserStringBytes(dialog, "type", 16, true); err != nil {
			return err
		}
		if err := validateBrowserStringBytes(dialog, "message", MaxBrowserDialogMessageBytes, true); err != nil {
			return err
		}
	}
	return nil
}

func strictestBrowserLimits(profiles []BrowserProfileDescriptor) BrowserLimits {
	if len(profiles) == 0 {
		return BrowserLimits{}
	}
	limits := profiles[0].Limits
	for _, profile := range profiles[1:] {
		candidate := profile.Limits
		limits.Sessions = min(limits.Sessions, candidate.Sessions)
		limits.Tabs = min(limits.Tabs, candidate.Tabs)
		limits.SessionSeconds = min(limits.SessionSeconds, candidate.SessionSeconds)
		limits.IdleSeconds = min(limits.IdleSeconds, candidate.IdleSeconds)
		limits.PreparedSeconds = min(limits.PreparedSeconds, candidate.PreparedSeconds)
		limits.ActionSeconds = min(limits.ActionSeconds, candidate.ActionSeconds)
		limits.SnapshotBytes = min(limits.SnapshotBytes, candidate.SnapshotBytes)
		limits.ScreenshotBytes = min(limits.ScreenshotBytes, candidate.ScreenshotBytes)
		limits.UploadBytes = min(limits.UploadBytes, candidate.UploadBytes)
		limits.DownloadBytes = min(limits.DownloadBytes, candidate.DownloadBytes)
		limits.SnapshotRefs = min(limits.SnapshotRefs, candidate.SnapshotRefs)
		limits.TextInputBytes = min(limits.TextInputBytes, candidate.TextInputBytes)
		limits.ToolResultBytes = min(limits.ToolResultBytes, candidate.ToolResultBytes)
		limits.RetentionSecs = min(limits.RetentionSecs, candidate.RetentionSecs)
	}
	return limits
}

func validateBrowserStringBytes(
	object map[string]any,
	field string,
	maximum int,
	required bool,
) error {
	value, present := object[field]
	if !present && !required {
		return nil
	}
	text, ok := value.(string)
	if !present || !ok || len(text) > maximum {
		return fmt.Errorf(
			"%w: browser %s is outside byte bounds",
			ErrInvalidInvocation,
			field,
		)
	}
	return nil
}

func rawSchema(data json.RawMessage) map[string]any {
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		return map[string]any{"not": map[string]any{}}
	}
	return schema
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("false")
	}
	return data
}
