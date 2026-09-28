package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	fstools "github.com/bogdanovich/mintclaw/pkg/tools/fs"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const documentModelTextLimit = toolshared.MaxLiveContextTextBytes

const documentLocalPathTokenPrefix = "local-path-sha256:"

const documentFormDiscoveryDigestDomain = "mintclaw.document_form_discovery.v1"

var canonicalDocumentMediaRef = regexp.MustCompile(
	`^media://(?:[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}|node-transfer-[a-f0-9]{32})$`,
)

type ownedDocumentMediaStore interface {
	media.MediaStore
	document.OwnedMediaResolver
	BindOwner(string, media.MediaOwner) error
}

type idempotentOwnedDocumentMediaStore interface {
	ownedDocumentMediaStore
	StoreIdempotentOwned(string, media.MediaMeta, string, string, media.MediaOwner) (string, error)
}

type documentArtifactSource interface {
	OpenArtifact(string) (io.ReadCloser, error)
}

type documentDeliveryInspector func(string) (outbox.DeliveryInspection, error)

type documentFormSchemaResolver func(
	context.Context,
	ownedDocumentMediaStore,
	string,
	media.MediaOwner,
) (document.FormFieldsFacts, error)

// DocumentTool is the sole deferred model surface for PDF1A inspection,
// extraction, and rendering. Attachments remain exact-current-turn refs. A
// configured local path may only enter through inspect, which retains the
// immutable snapshot behind a turn-owned ref for every later operation.
type DocumentTool struct {
	mu            sync.Mutex
	store         media.MediaStore
	scratchRoot   string
	stateRoot     string
	workspace     string
	restrict      bool
	allowPaths    []*regexp.Regexp
	deliveryState documentDeliveryInspector
	formJobs      *document.FormJobStore
	formAudit     document.FormAuditor
	formPolicy    document.FormAuditPolicy
	formSchema    documentFormSchemaResolver
	budget        *document.ExecutionBudget
	cleanupScopes map[string][]string
	localRefs     map[string]map[string]struct{}
}

// WithDocumentFormJobStore supplies the protected multi-turn form workflow
// store. The interaction sink owns its process lifetime.
func WithDocumentFormJobStore(store *document.FormJobStore) DocumentToolOption {
	return func(tool *DocumentTool) {
		tool.formJobs = store
	}
}

// WithDocumentFormAudit supplies the configured deliberative role and its
// explicitly equivalent fallback policy. A missing role fails closed when a
// workflow reaches audit; ordinary PDF actions remain available.
func WithDocumentFormAudit(policy document.FormAuditPolicy, auditor document.FormAuditor) DocumentToolOption {
	return func(tool *DocumentTool) {
		tool.formPolicy = policy
		tool.formAudit = auditor
	}
}

// WithDocumentStateRoot sets the private durable journal/generation root used
// by write-capable document actions.
func WithDocumentStateRoot(stateRoot string) DocumentToolOption {
	return func(tool *DocumentTool) {
		tool.stateRoot = strings.TrimSpace(stateRoot)
	}
}

// WithDocumentExecutionBudget shares process capacity across every document
// tool created by one runtime.
func WithDocumentExecutionBudget(budget *document.ExecutionBudget) DocumentToolOption {
	return func(tool *DocumentTool) {
		tool.budget = budget
	}
}

// WithDocumentDeliveryInspector supplies read-only access to the canonical
// durable outbox. The closure is resolved lazily so runtime outbox replacement
// during startup or recovery is visible to the long-lived document tool.
func WithDocumentDeliveryInspector(
	inspector func(string) (outbox.DeliveryInspection, error),
) DocumentToolOption {
	return func(tool *DocumentTool) {
		tool.deliveryState = inspector
	}
}

type DocumentToolOption func(*DocumentTool)

// WithDocumentLocalPathPolicy enables inspect-time admission of local PDFs
// through the same workspace/read-path boundary as first-party file tools.
func WithDocumentLocalPathPolicy(
	workspace string,
	restrict bool,
	allowPaths []*regexp.Regexp,
) DocumentToolOption {
	return func(tool *DocumentTool) {
		tool.workspace = strings.TrimSpace(workspace)
		tool.restrict = restrict
		tool.allowPaths = append([]*regexp.Regexp(nil), allowPaths...)
	}
}

func NewDocumentTool(options ...DocumentToolOption) *DocumentTool {
	tool := &DocumentTool{
		scratchRoot:   filepath.Join(os.TempDir(), "mintclaw_document_agent"),
		cleanupScopes: make(map[string][]string),
		localRefs:     make(map[string]map[string]struct{}),
	}
	for _, option := range options {
		option(tool)
	}
	return tool
}

func (tool *DocumentTool) Name() string { return "document" }

func (tool *DocumentTool) Description() string {
	return "Inspect, read, render, conversationally complete, directly fill, or verify an exact current PDF " +
		"attachment or authorized local PDF. For an ordinary form-completion request, inspect, use form discover, then " +
		"start the protected multi-turn form workflow with the returned field_schema_digest. The first protected question " +
		"requires the agent's short form_summary and collection_plan; use collect for a missing value and correct for an " +
		"existing value the user asked to replace; reserve direct fill for a complete explicit stable-ID map. " +
		"For fill, bind each user " +
		"datum to one unambiguous " +
		"discovered semantic field; never copy it across distinct people or sections to resolve ambiguity, and ask " +
		"for clarification or leave the field blank when the mapping is not unique. Verify requires both the exact " +
		"artifact ref and operation_id returned by fill"
}

func (tool *DocumentTool) PromptMetadata() toolshared.PromptMetadata {
	return toolshared.PromptMetadata{
		Layer:  toolshared.ToolPromptLayerCapability,
		Slot:   toolshared.ToolPromptSlotTooling,
		Source: toolshared.ToolPromptSourceRegistry,
	}
}

func (tool *DocumentTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"inspect", "extract", "render", "fields", "form", "fill", "verify"},
			},
			"source": map[string]any{
				"type": "string",
				"description": "Exact media:// ref from current attachment metadata or a successful local-path " +
					"inspect; verify requires the artifact ref returned by fill",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Inspect only: a local PDF path authorized by workspace policy. Use the returned source ref for extract or render.",
			},
			"pages": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": document.DefaultMaxExtractPages,
				"items": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
			},
			"max_characters": map[string]any{
				"type":    "integer",
				"minimum": 1,
				"maximum": document.DefaultMaxExtractChars,
			},
			"dpi": map[string]any{
				"type":    "integer",
				"minimum": 36,
				"maximum": 300,
			},
			"max_dimension": map[string]any{
				"type":    "integer",
				"minimum": 256,
				"maximum": document.HardMaxRenderEdge,
			},
			"retain": map[string]any{
				"type":        "boolean",
				"description": "Deliver and retain rendered pages only when the user requested them",
			},
			"assignments": documentFillAssignmentsSchema(),
			"operation_id": map[string]any{
				"type": "string",
				"description": "Exact operation_id returned by fill; pair it with fill's artifact ref for verify; " +
					"optional only for an exact fill retry",
			},
			"form_action": map[string]any{
				"type": "string",
				"enum": []string{
					"discover", "start", "collect", "continue", "clarify", "back", "status", "correct", "review",
					"commit", "cancel",
				},
				"description": "Agent-led protected form operation. discover returns a bounded field window and exact " +
					"field_schema_digest; start prepares a job from that digest without asking a question and returns " +
					"bounded candidate_fields; collect asks one explicitly selected field and MUST include a non-empty " +
					"question; continue accepts only answer_ref and never asks the next field; clarify and back accept " +
					"only an interaction-runtime navigation_ref and deterministically ask the authenticated field; " +
					"status, correct, review, commit, and cancel keep using the original job_id.",
			},
			"field_schema_digest": map[string]any{
				"type":        "string",
				"minLength":   sha256.Size * 2,
				"maxLength":   sha256.Size * 2,
				"description": "Exact field_schema_digest returned by form discover for this source; required for form start.",
			},
			"job_id": map[string]any{
				"type":        "string",
				"description": "Opaque form job identity returned by an earlier form operation.",
			},
			"answer_ref": map[string]any{
				"type":        "string",
				"description": "Exact protected_answer_ref returned after one form question is answered.",
			},
			"navigation_ref": map[string]any{
				"type": "string",
				"description": "Exact authenticated protected navigation ref supplied by the interaction runtime; " +
					"valid only for clarify or back.",
			},
			"field_id": map[string]any{
				"type":        "string",
				"description": "Stable field identity returned in form candidate_fields; required for collect or correct.",
			},
			"question": map[string]any{
				"type":      "string",
				"minLength": 1,
				"maxLength": interactions.MaxQuestionLength,
				"description": "REQUIRED for collect and correct: a user-facing question chosen by the agent for the " +
					"exact field. Explain the requested fact without exposing field IDs.",
			},
			"form_summary": map[string]any{
				"type":      "string",
				"minLength": 1,
				"maxLength": documentFormSummaryMaxRunes,
				"description": "Short value-free user-facing document summary authored by the agent. Required with " +
					"collection_plan for the first protected question, whether collecting or correcting a field.",
			},
			"collection_plan": map[string]any{
				"type":      "string",
				"minLength": 1,
				"maxLength": documentFormPlanMaxRunes,
				"description": "Short value-free user-facing collection plan authored by the agent. Required with " +
					"form_summary for the first protected question, whether collecting or correcting a field.",
			},
			"checked_label": map[string]any{
				"type":      "string",
				"minLength": 1,
				"maxLength": interactions.MaxOptionLabelLength,
				"description": "Localized user-facing choice that sets a checkbox. Supply together with " +
					"unchecked_label only for checkbox fields.",
			},
			"unchecked_label": map[string]any{
				"type":      "string",
				"minLength": 1,
				"maxLength": interactions.MaxOptionLabelLength,
				"description": "Localized user-facing choice that leaves a checkbox unset. Supply together with " +
					"checked_label only for checkbox fields.",
			},
		},
		"required": []string{"action"},
	}
}

// DurableArguments prevents a model-selected host path from being retained in
// assistant tool-call history. The digest token preserves loop identity while
// remaining unusable as filesystem authority.
func (*DocumentTool) DurableArguments(args map[string]any) (map[string]any, error) {
	projected := make(map[string]any, len(args))
	for key, value := range args {
		projected[key] = value
	}
	if rawPath, present := projected["path"]; present {
		projected["path"] = documentProtectedArgumentToken(rawPath)
	}
	if rawSource, present := projected["source"]; present && DocumentSourceArgumentProtected(rawSource) {
		projected["source"] = documentProtectedArgumentToken(rawSource)
	}
	if assignments, present := projected["assignments"]; present {
		projected["assignments"] = documentDurableAssignmentProjection(assignments)
	}
	return projected, nil
}

func (*DocumentTool) ProtectedDurableArguments(args map[string]any) bool {
	_, pathPresent := args["path"]
	_, assignmentsPresent := args["assignments"]
	rawSource, sourcePresent := args["source"]
	return pathPresent || assignmentsPresent || sourcePresent && DocumentSourceArgumentProtected(rawSource)
}

// ProtectedAnswerContinuationArguments returns the mechanical form action for
// one protected receipt. Value receipts are consumed before the model chooses
// the next semantic field; authenticated navigation receipts deterministically
// re-ask their bound field without a model-authored transition.
func (*DocumentTool) ProtectedAnswerContinuationArguments(reference string) (map[string]any, error) {
	reference = strings.TrimSpace(reference)
	if navigation, err := document.ParseFormProtectedNavigationReference(reference); err == nil {
		return map[string]any{
			"action":         "form",
			"form_action":    string(navigation.Action),
			"navigation_ref": reference,
		}, nil
	}
	if _, _, err := document.ParseFormProtectedAnswerReference(reference); err != nil {
		return nil, fmt.Errorf("invalid protected form answer reference: %w", err)
	}
	return map[string]any{
		"action":      "form",
		"form_action": "continue",
		"answer_ref":  reference,
	}, nil
}

// ProtectedAnswerContinuationFollowup keeps agent-led form collection inside
// the document tool for one deliberate transition after a protected answer is
// consumed. The model still chooses the next semantic field and authors the
// user-facing question; this validator only prevents prose re-prompts,
// confirmed-field repeats, and transitions outside the current form job.
func (*DocumentTool) ProtectedAnswerContinuationFollowup(
	result *toolshared.ToolResult,
) (*toolshared.ToolOnlyFollowup, error) {
	if result == nil || result.IsError || strings.TrimSpace(result.ForLLM) == "" {
		return nil, errors.New("protected form continuation result is unavailable")
	}
	var projection safeDocumentFormResult
	if err := json.Unmarshal([]byte(result.ForLLM), &projection); err != nil ||
		projection.SchemaVersion != documentFormWorkflowSchemaVersion ||
		projection.Operation != "form" || projection.FormAction != "continue" || projection.Job == nil {
		return nil, errors.New("protected form continuation result is invalid")
	}
	return documentFormToolOnlyFollowup(projection, false)
}

// ToolResultFollowup keeps a prepared or audit-blocked form job inside the
// document tool until the agent deliberately selects one exact field and asks
// its protected question. The runtime owns only the generic tool-only fence;
// all form/job semantics remain in this provider.
func (*DocumentTool) ToolResultFollowup(
	result *toolshared.ToolResult,
) (*toolshared.ToolOnlyFollowup, error) {
	if result == nil || result.IsError || strings.TrimSpace(result.ForLLM) == "" {
		return nil, nil
	}
	var projection safeDocumentFormResult
	if err := json.Unmarshal([]byte(result.ForLLM), &projection); err != nil {
		return nil, fmt.Errorf("decode trusted document result: %w", err)
	}
	if projection.SchemaVersion != documentFormWorkflowSchemaVersion || projection.Operation != "form" {
		return nil, nil
	}
	switch projection.FormAction {
	case "discover":
		return documentFormDiscoverToolOnlyFollowup(projection)
	case "start":
		if projection.Job == nil {
			return nil, errors.New("prepared form job is unavailable")
		}
		return documentFormToolOnlyFollowup(projection, true)
	case "review":
		if projection.Job == nil {
			return nil, errors.New("reviewed form job is unavailable")
		}
		if projection.Mapping != nil {
			return documentFormToolOnlyFollowup(projection, false)
		}
		return documentFormReviewToolOnlyFollowup(projection)
	default:
		return nil, nil
	}
}

func documentFormDiscoverToolOnlyFollowup(
	projection safeDocumentFormResult,
) (*toolshared.ToolOnlyFollowup, error) {
	sourceRef := strings.TrimSpace(projection.SourceRef)
	schemaDigest := strings.TrimSpace(projection.FieldSchemaDigest)
	if sourceRef == "" || !canonicalDocumentMediaRef.MatchString(sourceRef) ||
		!validDocumentFormSchemaDigest(schemaDigest) || projection.Mapping == nil {
		return nil, errors.New("discovered form transition is unavailable")
	}
	return &toolshared.ToolOnlyFollowup{
		Instruction: "The form was discovered successfully. Call the originating tool exactly once with " +
			"action=form, form_action=start, source equal to the exact source_ref, and field_schema_digest equal to " +
			"the exact digest from the result. Do not answer in prose or ask for form values yet. The prepared " +
			"follow-up will let you choose the first semantic field and present your form summary and collection plan.",
		ValidateArguments: func(arguments map[string]any) error {
			if len(arguments) != 4 || strings.TrimSpace(stringDocumentArg(arguments, "action")) != "form" ||
				strings.ToLower(strings.TrimSpace(stringDocumentArg(arguments, "form_action"))) != "start" ||
				strings.TrimSpace(stringDocumentArg(arguments, "source")) != sourceRef ||
				strings.TrimSpace(stringDocumentArg(arguments, "field_schema_digest")) != schemaDigest {
				return errors.New("protected form discovery follow-up must start the exact discovered source")
			}
			return validateDocumentActionOptions("form", arguments)
		},
	}, nil
}

func documentFormReviewToolOnlyFollowup(
	projection safeDocumentFormResult,
) (*toolshared.ToolOnlyFollowup, error) {
	if projection.Review == nil {
		return nil, nil
	}
	jobID := strings.TrimSpace(projection.Job.JobID)
	if jobID == "" || strings.TrimSpace(projection.Review.JobID) != jobID {
		return nil, errors.New("protected form review job is unavailable")
	}
	if projection.Review.Ready {
		return &toolshared.ToolOnlyFollowup{
			ResponseOnly: true,
			Instruction: "Summarize the value-free review counts and tell the user that no blockers remain. " +
				"Ask the user to confirm whether to finish and receive the verified PDF. Do not call any tool, " +
				"request or repeat a field value, expose IDs or values, claim the PDF was generated, or decide " +
				"commit/cancel on the user's behalf.",
		}, nil
	}
	blockerFields := make(map[string]struct{}, len(projection.Review.Blockers))
	for _, blocker := range projection.Review.Blockers {
		fieldID := strings.TrimSpace(blocker.FieldID)
		if fieldID != "" && strings.TrimSpace(blocker.Code) != "" {
			blockerFields[fieldID] = struct{}{}
		}
	}
	if len(blockerFields) == 0 {
		return nil, errors.New("protected form review has no actionable blocker")
	}
	return &toolshared.ToolOnlyFollowup{
		Instruction: "The protected form review found one or more value-free field blockers. " +
			"Call the originating tool exactly once with action=form, form_action=correct, the exact job_id, " +
			"one field_id from review.blockers, and a concise user-facing confirmation or correction question " +
			"in the user's language. Do not answer in prose, expose IDs to the user, or request a value outside " +
			"the protected interaction.",
		ValidateArguments: func(arguments map[string]any) error {
			for key := range arguments {
				switch key {
				case "action", "form_action", "job_id", "field_id", "question", "checked_label", "unchecked_label":
				default:
					return errors.New("protected form review follow-up contains an unrelated option")
				}
			}
			if strings.TrimSpace(stringDocumentArg(arguments, "action")) != "form" ||
				strings.ToLower(strings.TrimSpace(stringDocumentArg(arguments, "form_action"))) != "correct" ||
				strings.TrimSpace(stringDocumentArg(arguments, "job_id")) != jobID {
				return errors.New("protected form review follow-up changed the current job")
			}
			if _, ok := blockerFields[strings.TrimSpace(stringDocumentArg(arguments, "field_id"))]; !ok {
				return errors.New("protected form review follow-up did not select an audit blocker")
			}
			return validateDocumentActionOptions("form", arguments)
		},
	}, nil
}

func documentFormToolOnlyFollowup(
	projection safeDocumentFormResult,
	prepared bool,
) (*toolshared.ToolOnlyFollowup, error) {
	jobID := strings.TrimSpace(projection.Job.JobID)
	if jobID == "" {
		return nil, errors.New("protected form follow-up job is unavailable")
	}
	if projection.Mapping == nil {
		// A reviewed or terminal projection no longer needs a protected question
		// transition; ordinary review/commit rendering may resume.
		return nil, nil
	}
	if projection.Mapping.ReadyForReview {
		lead := "The protected answer was consumed and the form is ready for review. "
		if prepared {
			lead = "The prepared form is ready for review. "
		}
		return &toolshared.ToolOnlyFollowup{
			Instruction: lead +
				"Call the originating tool exactly once with action=form, form_action=review, and the exact job_id " +
				"from its result. Do not answer in prose or repeat any field question.",
			ValidateArguments: func(arguments map[string]any) error {
				if len(arguments) != 3 || strings.TrimSpace(stringDocumentArg(arguments, "action")) != "form" ||
					strings.ToLower(strings.TrimSpace(stringDocumentArg(arguments, "form_action"))) != "review" ||
					strings.TrimSpace(stringDocumentArg(arguments, "job_id")) != jobID {
					return errors.New("protected form follow-up must review the current job")
				}
				return validateDocumentActionOptions("form", arguments)
			},
		}, nil
	}

	actions := make(map[string]string)
	for _, candidate := range projection.Mapping.CandidateFields {
		fieldID := strings.TrimSpace(candidate.FieldID)
		blocker := strings.TrimSpace(candidate.Blocker)
		if fieldID == "" {
			continue
		}
		action := "correct"
		if blocker == "field_unresolved" {
			action = "collect"
		}
		actions[fieldID] = action
	}
	if len(actions) == 0 {
		return nil, errors.New("protected form follow-up has no unresolved candidate")
	}
	lead := "The protected answer was consumed. Continue the same form job by calling the originating tool "
	if prepared {
		lead = "The form job was prepared successfully. Start protected collection by calling the originating tool "
	}
	planInstruction := ""
	if prepared {
		planInstruction = " For this first protected question, include concise value-free form_summary and " +
			"collection_plan in the user's language."
	}
	return &toolshared.ToolOnlyFollowup{
		Instruction: lead +
			"exactly once. Select one candidate_fields entry; use form_action=collect for field_unresolved and " +
			"form_action=correct for any other candidate, including an existing value the user asked to replace. " +
			"Preserve the exact job_id and field_id, " +
			"and include a concise user-facing question in the user's language." + planInstruction +
			" Do not answer in prose, collect an existing value as missing, repeat a confirmed field unless the user " +
			"asked to correct it, expose IDs to the user, or request the protected value again.",
		ValidateArguments: func(arguments map[string]any) error {
			for key := range arguments {
				switch key {
				case "action", "form_action", "job_id", "field_id", "question", "checked_label", "unchecked_label":
				case "form_summary", "collection_plan":
					if !prepared {
						return errors.New("protected form follow-up contains an unrelated option")
					}
				default:
					return errors.New("protected form follow-up contains an unrelated option")
				}
			}
			if strings.TrimSpace(stringDocumentArg(arguments, "action")) != "form" ||
				strings.TrimSpace(stringDocumentArg(arguments, "job_id")) != jobID {
				return errors.New("protected form follow-up changed the current job")
			}
			fieldID := strings.TrimSpace(stringDocumentArg(arguments, "field_id"))
			expectedAction, ok := actions[fieldID]
			if !ok ||
				strings.ToLower(strings.TrimSpace(stringDocumentArg(arguments, "form_action"))) != expectedAction {
				return errors.New("protected form follow-up did not select an unresolved candidate")
			}
			formSummary := strings.TrimSpace(stringDocumentArg(arguments, "form_summary"))
			collectionPlan := strings.TrimSpace(stringDocumentArg(arguments, "collection_plan"))
			if prepared && (formSummary == "" || collectionPlan == "") {
				return errors.New("prepared form follow-up requires a summary and collection plan")
			}
			if !prepared && (formSummary != "" || collectionPlan != "") {
				return errors.New("form summary and collection plan are valid only for the initial question")
			}
			return validateDocumentActionOptions("form", arguments)
		},
	}, nil
}

// DocumentSourceArgumentProtected reports whether a model-authored document
// source is not a canonical opaque media reference and must be projected out
// of durable history, diagnostics, and logs.
func DocumentSourceArgumentProtected(value any) bool {
	source, ok := value.(string)
	return !ok || !canonicalDocumentMediaRef.MatchString(strings.TrimSpace(source))
}

func documentProtectedArgumentToken(value any) string {
	var encoded []byte
	if text, ok := value.(string); ok {
		encoded = []byte(strings.TrimSpace(text))
	} else {
		var err error
		encoded, err = json.Marshal(value)
		if err != nil {
			encoded = []byte(fmt.Sprintf("%T", value))
		}
	}
	digest := sha256.Sum256(encoded)
	return documentLocalPathTokenPrefix + hex.EncodeToString(digest[:])
}

// Document reports are already a bounded path-free projection and remain
// useful durable evidence even when the originating local path is protected.
func (*DocumentTool) ProtectedDurableResult(map[string]any) bool { return false }

func (tool *DocumentTool) SetMediaStore(store media.MediaStore) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	tool.store = store
}

func (*DocumentTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (tool *DocumentTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	ctx = document.WithExecutionBudget(ctx, tool.budget)
	action, _ := args["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	if err := validateDocumentActionOptions(action, args); err != nil {
		if action == "form" {
			result := documentFormToolFailure(
				"invalid_input",
				documentFormArgumentRecoveryMessage(args),
			).WithError(err)
			result.Control.PreserveToolVisibility = true
			return result
		}
		return documentToolFailure(
			action,
			document.StateFailed,
			document.FailureInvalidInput,
			"document action options are invalid",
		).WithError(err)
	}
	if action == "form" {
		store, mediaOwner, err := tool.executionAuthority(ctx)
		if err != nil {
			return documentFormToolFailure("source_not_authorized", "document authority is unavailable")
		}
		return tool.formWorkflow(ctx, store, mediaOwner, args)
	}
	ref, path, err := tool.resolveSource(ctx, action, args)
	if err != nil && action == "verify" {
		ref, err = tool.resolveRegisteredWriteArtifact(ctx, args)
		path = ""
	}
	if err != nil {
		return documentToolFailure(
			action,
			document.StateDenied,
			document.FailureSourceUnauthorized,
			"document source is unavailable for this authority",
		).WithError(err)
	}
	store, owner, err := tool.executionAuthority(ctx)
	if err != nil {
		return documentToolFailure(
			action,
			document.StateDenied,
			document.FailureSourceUnauthorized,
			"document authority is unavailable",
		).WithError(err)
	}

	switch action {
	case "inspect":
		if path != "" {
			return tool.inspectLocal(ctx, store, path, owner)
		}
		return tool.inspect(ctx, store, ref, owner)
	case "extract":
		return tool.extract(ctx, store, ref, owner, args)
	case "render":
		if !toolshared.ToolDocumentVisionAvailable(ctx) {
			return documentToolFailure(
				action,
				document.StateUnavailable,
				document.FailureVisionUnavailable,
				"the selected model route has no configured image-input path",
			)
		}
		return tool.render(ctx, store, ref, owner, args)
	case "fields":
		return tool.fields(ctx, store, ref, owner)
	case "fill":
		return tool.fill(ctx, store, ref, owner, args)
	case "verify":
		return tool.verifyFormWrite(ctx, store, ref, owner, args)
	default:
		return documentToolFailure(
			action,
			document.StateFailed,
			document.FailureInvalidInput,
			"document action is invalid",
		)
	}
}

// documentFormArgumentRecoveryMessage gives the model enough non-sensitive
// contract information to repair an incomplete form call. Returning only the
// generic document validation error makes a recoverable omitted argument look
// terminal and can cause the model to ask for protected values in plain text.
// The message is derived solely from the selected action, never from submitted
// values or validation-error text.
func documentFormArgumentRecoveryMessage(args map[string]any) string {
	formAction := strings.ToLower(strings.TrimSpace(stringDocumentArg(args, "form_action")))
	switch formAction {
	case "start":
		return "form start requires only source and the exact field_schema_digest returned by form discover; retry start"
	case "collect":
		return "form collect requires job_id, field_id, and a non-empty agent-authored question; " +
			"the first protected question also requires form_summary and collection_plan; retry the same field without asking in plain text"
	case "correct":
		return "form correct requires job_id, field_id, and a non-empty agent-authored question; " +
			"the first protected question also requires form_summary and collection_plan; retry the same field without asking in plain text"
	case "continue":
		return "form continue requires exactly one protected answer_ref; retry without selecting or asking another field"
	case "status", "review", "commit", "cancel":
		return "form " + formAction + " requires only job_id; retry the same protected form job"
	default:
		return "form requires one supported form_action and only the arguments documented for that action"
	}
}

func (tool *DocumentTool) resolveSource(
	ctx context.Context,
	action string,
	args map[string]any,
) (string, string, error) {
	rawRef, refPresent := args["source"]
	rawPath, pathPresent := args["path"]
	if refPresent == pathPresent {
		return "", "", errors.New("exactly one document source is required")
	}
	if pathPresent {
		path, ok := rawPath.(string)
		path = strings.TrimSpace(path)
		if !ok || path == "" {
			return "", "", errors.New("local path is invalid")
		}
		if action != "inspect" || strings.HasPrefix(path, "media://") || tool.workspace == "" {
			return "", "", errors.New("local path admission is unavailable")
		}
		if !toolshared.ToolDocumentLocalPathAllowed(ctx, path) {
			return "", "", errors.New("local path was not selected by the current user message")
		}
		resolved, err := fstools.ValidatePathWithAllowPaths(
			path,
			tool.workspace,
			tool.restrict,
			tool.allowPaths,
		)
		if err != nil {
			return "", "", errors.New("local path is outside the authorized read boundary")
		}
		return "", resolved, nil
	}
	ref, ok := rawRef.(string)
	ref = strings.TrimSpace(ref)
	if !ok || ref == "" {
		return "", "", errors.New("media reference is invalid")
	}
	if !strings.HasPrefix(ref, "media://") ||
		(!toolshared.ToolDocumentRefAllowed(ctx, ref) && !tool.localRefAllowed(ctx, ref)) {
		return "", "", errors.New("media reference is not authorized for this turn")
	}
	return ref, "", nil
}

func (tool *DocumentTool) executionAuthority(
	ctx context.Context,
) (ownedDocumentMediaStore, media.MediaOwner, error) {
	tool.mu.Lock()
	store, ok := tool.store.(ownedDocumentMediaStore)
	tool.mu.Unlock()
	if !ok || store == nil {
		return nil, media.MediaOwner{}, errors.New("authority-bound media store is unavailable")
	}
	actorID := strings.TrimSpace(toolshared.ToolActorID(ctx))
	if actorID == "" {
		actorID = strings.TrimSpace(toolshared.ToolSenderID(ctx))
	}
	if actorID == "" {
		actorID = strings.TrimSpace(toolshared.ToolAgentID(ctx))
	}
	routeSession := strings.TrimSpace(toolshared.ToolRouteSessionKey(ctx))
	if routeSession == "" {
		routeSession = strings.TrimSpace(toolshared.ToolSessionKey(ctx))
	}
	owner, err := media.NewMediaOwner(
		toolshared.ToolWorkspace(ctx),
		toolshared.ToolAgentID(ctx),
		actorID,
		routeSession,
		toolshared.ToolSessionKey(ctx),
		toolshared.ToolChannel(ctx),
		toolshared.ToolChatID(ctx),
		toolshared.ToolTopicID(ctx),
	)
	if err != nil {
		return nil, media.MediaOwner{}, err
	}
	return store, owner, nil
}

func (tool *DocumentTool) inspect(
	ctx context.Context,
	store ownedDocumentMediaStore,
	ref string,
	owner media.MediaOwner,
) *toolshared.ToolResult {
	snapshot, report := document.InspectMedia(
		ctx,
		store,
		ref,
		owner,
		document.AcquireOptions{ScratchRoot: tool.scratchRoot},
	)
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	return documentToolReportResult(report)
}

func (tool *DocumentTool) inspectLocal(
	ctx context.Context,
	store ownedDocumentMediaStore,
	path string,
	owner media.MediaOwner,
) *toolshared.ToolResult {
	snapshot, report := document.Inspect(
		ctx,
		path,
		document.AcquireOptions{ScratchRoot: tool.scratchRoot},
	)
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	if report.State != document.StateSucceeded || snapshot == nil || report.Input == nil {
		return documentToolReportResult(report)
	}
	ref, err := tool.registerLocalSnapshot(ctx, store, owner, snapshot, report)
	if err != nil {
		return documentToolFailure(
			"inspect",
			document.StateFailed,
			document.FailureArtifactRegistration,
			"authorized local document could not be retained for this turn",
		).WithError(err)
	}
	report.Input.SourceRef = ref
	report.Input.SourceKind = "agent_local_snapshot"
	report.Input.Authority = document.Authority{
		Kind:        "agent_local_snapshot",
		WorkspaceID: owner.WorkspaceID,
		AgentID:     owner.AgentID,
		ActorID:     owner.ActorID,
		RouteID:     owner.RouteID,
		SessionID:   owner.SessionID,
	}
	return documentToolReportResult(report)
}

func (tool *DocumentTool) extract(
	ctx context.Context,
	store ownedDocumentMediaStore,
	ref string,
	owner media.MediaOwner,
	args map[string]any,
) *toolshared.ToolResult {
	pages, _ := documentPagesArg(args["pages"])
	limits := document.ReadLimits{MaxPages: document.DefaultMaxExtractPages}
	if maximum, ok := documentIntArg(args["max_characters"]); ok {
		limits.MaxCharacters = maximum
	}
	snapshot, report := document.ExtractMedia(ctx, store, ref, owner, document.ReadOptions{
		Acquire: document.AcquireOptions{ScratchRoot: tool.scratchRoot},
		Pages:   pages,
		Limits:  limits,
	})
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	result := documentToolReportResult(report)
	if result.IsError || snapshot == nil {
		return result
	}
	extracted, err := document.ReadExtractedPages(snapshot, report)
	if err != nil {
		return documentToolFailure(
			"extract",
			document.StateFailed,
			document.FailureArtifactInvalid,
			"verified extraction artifact could not be read",
		).WithError(err)
	}
	result.ContextText = boundedDocumentText(extracted, documentModelTextLimit)
	return result
}

func (tool *DocumentTool) render(
	ctx context.Context,
	store ownedDocumentMediaStore,
	ref string,
	owner media.MediaOwner,
	args map[string]any,
) *toolshared.ToolResult {
	pages, _ := documentPagesArg(args["pages"])
	limits := document.ReadLimits{MaxPages: document.DefaultMaxRenderPages}
	if dpi, ok := documentIntArg(args["dpi"]); ok {
		limits.DPI = dpi
	}
	if dimension, ok := documentIntArg(args["max_dimension"]); ok {
		limits.MaxDimension = dimension
	}
	snapshot, report := document.RenderMedia(ctx, store, ref, owner, document.ReadOptions{
		Acquire: document.AcquireOptions{ScratchRoot: tool.scratchRoot},
		Pages:   pages,
		Limits:  limits,
	})
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	result := documentToolReportResult(report)
	if result.IsError || snapshot == nil {
		return result
	}

	retain, _ := args["retain"].(bool)
	refs, err := tool.registerRenderedArtifacts(ctx, store, owner, snapshot, report, !retain)
	if err != nil {
		return documentToolFailure(
			"render",
			document.StateFailed,
			document.FailureArtifactRegistration,
			"rendered pages could not be registered",
		).WithError(err)
	}
	if !retain {
		result.ContextMedia = refs
		return result
	}
	result.Media = refs
	result.Deliverable = documentRenderDeliverable(report, refs)
	result.WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
	return result
}

func (tool *DocumentTool) registerRenderedArtifacts(
	ctx context.Context,
	store ownedDocumentMediaStore,
	owner media.MediaOwner,
	snapshot documentArtifactSource,
	report document.Report,
	cleanupAtTurnEnd bool,
) ([]string, error) {
	if len(report.Artifacts) == 0 || report.Input == nil {
		return nil, errors.New("rendered artifact set is empty")
	}
	if err := os.MkdirAll(media.TempDir(), 0o700); err != nil {
		return nil, err
	}
	scope := "document-render-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	refs := make([]string, 0, len(report.Artifacts))
	cleanup := true
	defer func() {
		if cleanup {
			_ = store.ReleaseAll(scope)
		}
	}()
	for _, artifact := range report.Artifacts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, err := copyDocumentArtifactToMediaTemp(snapshot, artifact)
		if err != nil {
			return nil, err
		}
		storedRef, storeErr := store.Store(path, media.MediaMeta{
			Filename:      fmt.Sprintf("page-%04d.png", artifact.Pages[0]),
			ContentType:   "image/png",
			Source:        "tool:document",
			CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
		}, scope)
		if storeErr != nil {
			_ = os.Remove(path)
			return nil, storeErr
		}
		if bindErr := store.BindOwner(storedRef, owner); bindErr != nil {
			return nil, bindErr
		}
		refs = append(refs, storedRef)
	}
	cleanup = false
	if cleanupAtTurnEnd {
		tool.rememberCleanupScope(toolshared.ToolExecutionID(ctx), scope)
	}
	return refs, nil
}

type documentInputSource interface {
	OpenInput() (io.ReadCloser, error)
}

func (tool *DocumentTool) registerLocalSnapshot(
	ctx context.Context,
	store ownedDocumentMediaStore,
	owner media.MediaOwner,
	snapshot documentInputSource,
	report document.Report,
) (string, error) {
	executionID := strings.TrimSpace(toolshared.ToolExecutionID(ctx))
	if executionID == "" || report.Input == nil || report.State != document.StateSucceeded ||
		report.Input.ContentType != "application/pdf" || report.Input.Size <= 0 ||
		report.Input.Size > document.DefaultMaxInputBytes || len(report.Input.SHA256) != sha256.Size*2 {
		return "", errors.New("authorized local document descriptor is invalid")
	}
	if err := os.MkdirAll(media.TempDir(), 0o700); err != nil {
		return "", err
	}
	path, err := copyDocumentInputToMediaTemp(snapshot, *report.Input)
	if err != nil {
		return "", err
	}
	scope := "document-local-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ref, err := store.Store(path, media.MediaMeta{
		Filename:      "local-document.pdf",
		ContentType:   "application/pdf",
		Source:        "tool:document-local-admission",
		CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
	}, scope)
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err = store.BindOwner(ref, owner); err != nil {
		_ = store.ReleaseAll(scope)
		return "", err
	}
	tool.rememberLocalRef(executionID, scope, ref)
	return ref, nil
}

func copyDocumentInputToMediaTemp(snapshot documentInputSource, input document.DocumentRef) (string, error) {
	reader, err := snapshot.OpenInput()
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()
	output, err := os.CreateTemp(media.TempDir(), ".document-local-*.pdf")
	if err != nil {
		return "", err
	}
	path := output.Name()
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err = output.Chmod(0o600); err != nil {
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(reader, input.Size+1))
	if copyErr != nil || written != input.Size || hex.EncodeToString(hash.Sum(nil)) != input.SHA256 {
		return "", errors.New("authorized local document bytes do not match the immutable descriptor")
	}
	var trailing [1]byte
	if count, readErr := reader.Read(trailing[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return "", errors.New("authorized local document exceeds the immutable descriptor")
	}
	if err = output.Sync(); err != nil {
		return "", err
	}
	if err = output.Close(); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func (tool *DocumentTool) rememberLocalRef(executionID, scope, ref string) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	tool.cleanupScopes[executionID] = append(tool.cleanupScopes[executionID], scope)
	if tool.localRefs[executionID] == nil {
		tool.localRefs[executionID] = make(map[string]struct{})
	}
	tool.localRefs[executionID][ref] = struct{}{}
}

func (tool *DocumentTool) localRefAllowed(ctx context.Context, ref string) bool {
	executionID := strings.TrimSpace(toolshared.ToolExecutionID(ctx))
	if executionID == "" {
		return false
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	_, ok := tool.localRefs[executionID][ref]
	return ok
}

func copyDocumentArtifactToMediaTemp(snapshot documentArtifactSource, artifact document.Artifact) (string, error) {
	if artifact.Kind != "page_render" || artifact.ContentType != "image/png" ||
		artifact.Size <= 0 || artifact.Size > document.DefaultMaxArtifactBytes || len(artifact.Pages) != 1 {
		return "", errors.New("rendered artifact descriptor is invalid")
	}
	input, openErr := snapshot.OpenArtifact(artifact.Ref)
	if openErr != nil {
		return "", openErr
	}
	defer func() { _ = input.Close() }()
	output, createErr := os.CreateTemp(media.TempDir(), ".document-page-*.png")
	if createErr != nil {
		return "", createErr
	}
	path := output.Name()
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := output.Chmod(0o600); err != nil {
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, artifact.Size+1))
	if copyErr != nil || written != artifact.Size || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return "", errors.New("rendered artifact bytes do not match the verified descriptor")
	}
	var trailing [1]byte
	if count, readErr := input.Read(trailing[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return "", errors.New("rendered artifact exceeds the verified descriptor")
	}
	if err := output.Sync(); err != nil {
		return "", err
	}
	if err := output.Close(); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func (tool *DocumentTool) rememberCleanupScope(executionID, scope string) {
	if strings.TrimSpace(executionID) == "" {
		return
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	tool.cleanupScopes[executionID] = append(tool.cleanupScopes[executionID], scope)
}

// CleanupTurn releases non-retained current-turn image refs after the final
// model call. Retained refs remain under the existing MediaStore/outbox
// lifecycle and are deliberately absent from cleanupScopes.
func (tool *DocumentTool) CleanupTurn(ctx context.Context) error {
	executionID := strings.TrimSpace(toolshared.ToolExecutionID(ctx))
	tool.mu.Lock()
	store := tool.store
	scopes := append([]string(nil), tool.cleanupScopes[executionID]...)
	delete(tool.cleanupScopes, executionID)
	delete(tool.localRefs, executionID)
	tool.mu.Unlock()
	if store == nil {
		return nil
	}
	var cleanupErr error
	for _, scope := range scopes {
		cleanupErr = errors.Join(cleanupErr, store.ReleaseAll(scope))
	}
	return cleanupErr
}

func validateDocumentActionOptions(action string, args map[string]any) error {
	allowed := map[string]map[string]struct{}{
		"inspect": {"action": {}, "source": {}, "path": {}},
		"extract": {"action": {}, "source": {}, "pages": {}, "max_characters": {}},
		"render": {
			"action": {}, "source": {}, "pages": {}, "dpi": {}, "max_dimension": {}, "retain": {},
		},
		"fields": {"action": {}, "source": {}},
		"form": {
			"action": {}, "form_action": {}, "source": {}, "job_id": {}, "answer_ref": {}, "event_id": {},
			"navigation_ref": {}, "field_id": {}, "question": {}, "field_schema_digest": {}, "form_summary": {},
			"collection_plan": {}, "checked_label": {}, "unchecked_label": {},
		},
		"fill":   {"action": {}, "source": {}, "assignments": {}, "operation_id": {}},
		"verify": {"action": {}, "source": {}, "operation_id": {}},
	}
	actionAllowed, ok := allowed[action]
	if !ok {
		return errors.New("unsupported document action")
	}
	for key := range args {
		if _, ok := actionAllowed[key]; !ok {
			return fmt.Errorf("option %q does not apply to %s", key, action)
		}
	}
	if action == "extract" || action == "render" {
		pages, valid := documentPagesArg(args["pages"])
		if !valid || len(pages) == 0 || !slices.IsSorted(pages) {
			return errors.New("explicit sorted page selection is required")
		}
		for index, page := range pages {
			if page <= 0 || (index > 0 && pages[index-1] == page) {
				return errors.New("page selection must be positive, sorted, and unique")
			}
		}
	}
	if action == "fill" {
		if _, err := documentFillMapArg(args["assignments"]); err != nil {
			return err
		}
		if raw, present := args["operation_id"]; present {
			operationID, ok := raw.(string)
			if !ok || strings.TrimSpace(operationID) == "" {
				return errors.New("fill retry operation_id is invalid")
			}
		}
	}
	if action == "verify" {
		source, ok := args["source"].(string)
		if !ok || strings.TrimSpace(source) == "" {
			return errors.New("verify requires the exact artifact source returned by fill")
		}
		operationID, ok := args["operation_id"].(string)
		if !ok || strings.TrimSpace(operationID) == "" {
			return errors.New("verify requires an exact operation_id")
		}
	}
	if action == "form" {
		formAction, ok := args["form_action"].(string)
		formAction = strings.ToLower(strings.TrimSpace(formAction))
		if !ok || formAction == "" {
			return errors.New("form requires form_action")
		}
		hasSource := strings.TrimSpace(stringDocumentArg(args, "source")) != ""
		hasJob := strings.TrimSpace(stringDocumentArg(args, "job_id")) != ""
		hasAnswer := strings.TrimSpace(stringDocumentArg(args, "answer_ref")) != ""
		navigationReference := strings.TrimSpace(stringDocumentArg(args, "navigation_ref"))
		hasNavigation := navigationReference != ""
		hasLegacyEvent := strings.TrimSpace(stringDocumentArg(args, "event_id")) != ""
		hasField := strings.TrimSpace(stringDocumentArg(args, "field_id")) != ""
		schemaDigest := strings.TrimSpace(stringDocumentArg(args, "field_schema_digest"))
		hasSchemaDigest := schemaDigest != ""
		question := strings.TrimSpace(stringDocumentArg(args, "question"))
		hasQuestion := question != ""
		formSummary := strings.TrimSpace(stringDocumentArg(args, "form_summary"))
		collectionPlan := strings.TrimSpace(stringDocumentArg(args, "collection_plan"))
		hasFormSummary := formSummary != ""
		hasCollectionPlan := collectionPlan != ""
		checkedLabel := strings.TrimSpace(stringDocumentArg(args, "checked_label"))
		uncheckedLabel := strings.TrimSpace(stringDocumentArg(args, "unchecked_label"))
		hasCheckboxLabels := checkedLabel != "" || uncheckedLabel != ""
		if hasQuestion && (!utf8.ValidString(question) ||
			utf8.RuneCountInString(question) > interactions.MaxQuestionLength) {
			return errors.New("form question is invalid")
		}
		if hasFormSummary != hasCollectionPlan ||
			(hasFormSummary && (!utf8.ValidString(formSummary) || !utf8.ValidString(collectionPlan) ||
				utf8.RuneCountInString(formSummary) > documentFormSummaryMaxRunes ||
				utf8.RuneCountInString(collectionPlan) > documentFormPlanMaxRunes ||
				utf8.RuneCountInString(formSummary)+utf8.RuneCountInString(collectionPlan)+
					utf8.RuneCountInString(question)+4 > interactions.MaxQuestionLength)) {
			return errors.New("form summary and collection plan are invalid")
		}
		if hasCheckboxLabels && (checkedLabel == "" || uncheckedLabel == "" ||
			!utf8.ValidString(checkedLabel) || !utf8.ValidString(uncheckedLabel) ||
			utf8.RuneCountInString(checkedLabel) > interactions.MaxOptionLabelLength ||
			utf8.RuneCountInString(uncheckedLabel) > interactions.MaxOptionLabelLength ||
			strings.EqualFold(checkedLabel, uncheckedLabel)) {
			return errors.New("form checkbox labels are invalid")
		}
		switch formAction {
		case "discover":
			if !hasSource || hasJob || hasAnswer || hasNavigation || hasLegacyEvent || hasField || hasQuestion ||
				hasSchemaDigest ||
				hasFormSummary ||
				hasCollectionPlan ||
				hasCheckboxLabels {
				return errors.New("form discover requires only source")
			}
		case "start":
			if !hasSource || hasJob || hasAnswer || hasNavigation || hasLegacyEvent || hasField || hasQuestion ||
				(hasSchemaDigest && !validDocumentFormSchemaDigest(schemaDigest)) || hasFormSummary || hasCollectionPlan ||
				hasCheckboxLabels {
				return errors.New(
					"form start requires source and the exact field_schema_digest returned by form discover",
				)
			}
		case "collect":
			if hasSource || !hasJob || hasAnswer || hasNavigation || hasLegacyEvent || !hasField || !hasQuestion ||
				hasSchemaDigest {
				return errors.New("form collect or correction requires job_id, field_id, and question")
			}
		case "correct":
			if hasSource || !hasJob || hasAnswer || hasNavigation || hasLegacyEvent || !hasField || !hasQuestion ||
				hasSchemaDigest {
				return errors.New("form collect or correction requires job_id, field_id, and question")
			}
		case "continue":
			if hasSource || hasNavigation || hasField || hasQuestion || hasAnswer == hasLegacyEvent ||
				hasSchemaDigest ||
				hasFormSummary ||
				hasCollectionPlan ||
				hasCheckboxLabels {
				return errors.New("form continue requires exactly one answer_ref")
			}
		case "clarify", "back":
			navigation, navigationErr := document.ParseFormProtectedNavigationReference(navigationReference)
			if hasSource || hasJob || hasAnswer || hasLegacyEvent || !hasNavigation || hasField || hasQuestion ||
				hasSchemaDigest || hasFormSummary || hasCollectionPlan || hasCheckboxLabels || navigationErr != nil ||
				string(navigation.Action) != formAction {
				return errors.New("form clarify or back requires only its exact navigation_ref")
			}
		case "status", "review", "commit", "cancel":
			if hasSource || !hasJob || hasAnswer || hasNavigation || hasLegacyEvent || hasField || hasQuestion ||
				hasSchemaDigest ||
				hasFormSummary ||
				hasCollectionPlan ||
				hasCheckboxLabels {
				return errors.New("form status, review, commit, or cancel requires only job_id")
			}
		default:
			return errors.New("unsupported form_action")
		}
	}
	return nil
}

func validDocumentFormSchemaDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func stringDocumentArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func documentPagesArg(value any) ([]int, bool) {
	values, ok := value.([]any)
	if !ok {
		if pages, typed := value.([]int); typed {
			return append([]int(nil), pages...), true
		}
		return nil, false
	}
	pages := make([]int, 0, len(values))
	for _, value := range values {
		page, ok := documentIntArg(value)
		if !ok {
			return nil, false
		}
		pages = append(pages, page)
	}
	return pages, true
}

func documentIntArg(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case float64:
		integer := int(typed)
		return integer, typed == float64(integer)
	case json.Number:
		integer, err := strconv.Atoi(typed.String())
		return integer, err == nil
	default:
		return 0, false
	}
}

type safeDocumentReport struct {
	SchemaVersion     string                         `json:"schema_version"`
	OperationID       string                         `json:"operation_id,omitempty"`
	Operation         string                         `json:"operation"`
	State             document.State                 `json:"state"`
	Source            *safeDocumentSource            `json:"source,omitempty"`
	SelectedPages     []int                          `json:"selected_pages,omitempty"`
	PageCount         *int                           `json:"page_count,omitempty"`
	Inspection        *safeDocumentInspection        `json:"inspection,omitempty"`
	Text              *document.TextFacts            `json:"extractable_text,omitempty"`
	FormEligibility   *document.FormEligibilityFacts `json:"form_eligibility,omitempty"`
	Fields            *document.FormFieldsFacts      `json:"fields,omitempty"`
	FieldSchemaDigest string                         `json:"field_schema_digest,omitempty"`
	Write             *document.FormWriteFacts       `json:"write,omitempty"`
	Delivery          *safeDocumentDelivery          `json:"delivery,omitempty"`
	Artifacts         []safeDocumentArtifact         `json:"artifacts,omitempty"`
	Warnings          []string                       `json:"warnings,omitempty"`
	Failure           *document.Failure              `json:"failure,omitempty"`
}

type safeDocumentInspection struct {
	PDFVersion      document.StringFact       `json:"pdf_version"`
	PageCount       document.IntegerFact      `json:"page_count"`
	Encryption      document.EncryptionFacts  `json:"encryption"`
	Signatures      document.SignatureFacts   `json:"signatures"`
	Restrictions    document.RestrictionFacts `json:"restrictions"`
	AcroForm        document.AcroFormFacts    `json:"acroform"`
	XFA             document.XFAFacts         `json:"xfa"`
	Actions         document.ActionFacts      `json:"actions"`
	HybridForm      document.HybridFormFacts  `json:"hybrid_form"`
	ExtractableText document.TextFacts        `json:"extractable_text"`
}

type safeDocumentSource struct {
	Ref    string `json:"ref"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type safeDocumentArtifact struct {
	Ref         string `json:"ref,omitempty"`
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Pages       []int  `json:"pages"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

type safeDocumentDelivery struct {
	State       document.WriteOperationState `json:"state"`
	DeliveryID  string                       `json:"delivery_id"`
	ArtifactRef string                       `json:"artifact_ref,omitempty"`
}

func documentToolReportResult(report document.Report) *toolshared.ToolResult {
	projection := safeDocumentReport{
		SchemaVersion: report.SchemaVersion,
		OperationID:   report.OperationID,
		Operation:     report.Operation,
		State:         report.State,
		Failure:       report.Failure,
	}
	if report.Input != nil {
		projection.Source = &safeDocumentSource{
			Ref: report.Input.SourceRef, Size: report.Input.Size, SHA256: report.Input.SHA256,
		}
	}
	if report.Inspection != nil {
		projection.PageCount = report.Inspection.PageCount.Value
		projection.Inspection = &safeDocumentInspection{
			PDFVersion:      report.Inspection.PDFVersion,
			PageCount:       report.Inspection.PageCount,
			Encryption:      report.Inspection.Encryption,
			Signatures:      report.Inspection.Signatures,
			Restrictions:    report.Inspection.Restrictions,
			AcroForm:        report.Inspection.AcroForm,
			XFA:             report.Inspection.XFA,
			Actions:         report.Inspection.Actions,
			HybridForm:      report.Inspection.HybridForm,
			ExtractableText: report.Inspection.ExtractableText,
		}
		projection.Text = &report.Inspection.ExtractableText
		projection.Warnings = append([]string(nil), report.Inspection.Warnings...)
	}
	if report.FormEligibility != nil {
		eligibility := *report.FormEligibility
		eligibility.Blockers = append([]document.FormBlocker(nil), report.FormEligibility.Blockers...)
		projection.FormEligibility = &eligibility
	}
	if report.Extraction != nil {
		projection.SelectedPages = append([]int(nil), report.Extraction.SelectedPages...)
	}
	if report.Rendering != nil {
		projection.SelectedPages = append([]int(nil), report.Rendering.SelectedPages...)
	}
	if report.Fields != nil {
		fields := *report.Fields
		fields.Fields = append([]document.FormField(nil), report.Fields.Fields...)
		projection.Fields = &fields
		if report.Input == nil {
			return documentToolFailure(
				report.Operation,
				document.StateFailed,
				document.FailureInternal,
				"document field discovery source is unavailable",
			)
		}
		digest, digestErr := documentFormDiscoveryDigest(report.Input.SHA256, fields)
		if digestErr != nil {
			return documentToolFailure(
				report.Operation,
				document.StateFailed,
				document.FailureInternal,
				"document field discovery digest could not be derived",
			).WithError(digestErr)
		}
		projection.FieldSchemaDigest = digest
	}
	if report.Write != nil {
		write := *report.Write
		write.AffectedPages = append([]int(nil), report.Write.AffectedPages...)
		projection.Write = &write
	}
	for _, artifact := range report.Artifacts {
		projection.Artifacts = append(projection.Artifacts, safeDocumentArtifact{
			Kind:        artifact.Kind,
			ContentType: artifact.ContentType,
			Size:        artifact.Size,
			SHA256:      artifact.SHA256,
			Pages:       append([]int(nil), artifact.Pages...),
			Width:       artifact.Width,
			Height:      artifact.Height,
			Truncated:   artifact.Truncated,
		})
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return documentToolFailure(
			report.Operation,
			document.StateFailed,
			document.FailureInternal,
			"document report could not be encoded",
		).WithError(err)
	}
	return &toolshared.ToolResult{
		ForLLM:  string(encoded),
		IsError: report.State != document.StateSucceeded,
	}
}

func documentFormDiscoveryDigest(sourceDigest string, fields document.FormFieldsFacts) (string, error) {
	sourceDigest = strings.TrimSpace(sourceDigest)
	if !validDocumentFormSchemaDigest(sourceDigest) || sourceDigest != fields.SourceSHA256 {
		return "", errors.New("document field discovery source digest is invalid")
	}
	fieldSchemaDigest, err := document.FormFieldSchemaDigest(fields)
	if err != nil {
		return "", err
	}
	binding := strings.Join([]string{
		documentFormDiscoveryDigestDomain,
		sourceDigest,
		fieldSchemaDigest,
	}, "\x00")
	digest := sha256.Sum256([]byte(binding))
	return hex.EncodeToString(digest[:]), nil
}

func documentToolFailure(
	operation string,
	state document.State,
	code document.FailureCode,
	message string,
) *toolshared.ToolResult {
	encoded, _ := json.Marshal(safeDocumentReport{
		SchemaVersion: document.ReportSchemaVersion,
		Operation:     operation,
		State:         state,
		Failure:       &document.Failure{Code: code, Message: message},
	})
	return &toolshared.ToolResult{ForLLM: string(encoded), IsError: true}
}

func boundedDocumentText(pages []document.ExtractedPage, maximum int) string {
	var builder strings.Builder
	builder.WriteString("Protected extracted document text for the current model call only. Cite page numbers.\n")
	for _, page := range pages {
		prefix := fmt.Sprintf("[page %d]\n", page.Page)
		remaining := maximum - builder.Len()
		if remaining <= len(prefix) {
			break
		}
		builder.WriteString(prefix)
		remaining = maximum - builder.Len()
		text := page.Text
		if len(text) > remaining {
			text = text[:remaining]
			for !utf8.ValidString(text) && len(text) > 0 {
				text = text[:len(text)-1]
			}
		}
		builder.WriteString(text)
		if builder.Len() < maximum {
			builder.WriteByte('\n')
		}
		if len(text) < len(page.Text) {
			break
		}
	}
	return builder.String()
}

func documentRenderDeliverable(report document.Report, refs []string) *taskresult.Deliverable {
	deliverable := &taskresult.Deliverable{Metadata: map[string]string{
		"operation": "render",
	}}
	if report.Input != nil {
		deliverable.Metadata["source_sha256"] = report.Input.SHA256
	}
	for index, ref := range refs {
		artifact := report.Artifacts[index]
		deliverable.Artifacts = append(deliverable.Artifacts, taskresult.Artifact{
			Ref:         ref,
			Kind:        "image",
			Filename:    fmt.Sprintf("page-%04d.png", artifact.Pages[0]),
			ContentType: "image/png",
		})
		deliverable.Metadata["page_"+strconv.Itoa(index+1)] = strconv.Itoa(artifact.Pages[0])
		deliverable.Metadata["sha256_"+strconv.Itoa(index+1)] = artifact.SHA256
	}
	return deliverable
}
