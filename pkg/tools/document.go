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
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const documentModelTextLimit = 32 * 1024

type ownedDocumentMediaStore interface {
	media.MediaStore
	document.OwnedMediaResolver
	BindOwner(string, media.MediaOwner) error
}

type documentArtifactSource interface {
	OpenArtifact(string) (io.ReadCloser, error)
}

// DocumentTool is the sole deferred model surface for PDF1A inspection,
// extraction, and rendering. It accepts only refs projected from the exact
// current turn; local paths and filenames are never selectors.
type DocumentTool struct {
	mu            sync.Mutex
	store         media.MediaStore
	scratchRoot   string
	cleanupScopes map[string][]string
}

func NewDocumentTool() *DocumentTool {
	return &DocumentTool{
		scratchRoot:   filepath.Join(os.TempDir(), "mintclaw_document_agent"),
		cleanupScopes: make(map[string][]string),
	}
}

func (tool *DocumentTool) Name() string { return "document" }

func (tool *DocumentTool) Description() string {
	return "Inspect, extract selected page text from, or render selected pages of the exact current PDF attachment"
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
				"enum": []string{"inspect", "extract", "render"},
			},
			"source": map[string]any{
				"type":        "string",
				"description": "Exact media:// ref from the current attachment metadata",
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
		},
		"required": []string{"action", "source"},
	}
}

func (tool *DocumentTool) SetMediaStore(store media.MediaStore) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	tool.store = store
}

func (*DocumentTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

func (tool *DocumentTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	action, _ := args["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	ref, _ := args["source"].(string)
	ref = strings.TrimSpace(ref)
	if !toolshared.ToolDocumentRefAllowed(ctx, ref) {
		return documentToolFailure(
			action,
			document.StateDenied,
			document.FailureSourceUnauthorized,
			"document source is unavailable for this turn",
		)
	}
	if err := validateDocumentActionOptions(action, args); err != nil {
		return documentToolFailure(
			action,
			document.StateFailed,
			document.FailureInvalidInput,
			"document action options are invalid",
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
	default:
		return documentToolFailure(
			action,
			document.StateFailed,
			document.FailureInvalidInput,
			"document action is invalid",
		)
	}
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
		"inspect": {"action": {}, "source": {}},
		"extract": {"action": {}, "source": {}, "pages": {}, "max_characters": {}},
		"render": {
			"action": {}, "source": {}, "pages": {}, "dpi": {}, "max_dimension": {}, "retain": {},
		},
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
	return nil
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
	SchemaVersion string                 `json:"schema_version"`
	Operation     string                 `json:"operation"`
	State         document.State         `json:"state"`
	Source        *safeDocumentSource    `json:"source,omitempty"`
	SelectedPages []int                  `json:"selected_pages,omitempty"`
	PageCount     *int                   `json:"page_count,omitempty"`
	Text          *document.TextFacts    `json:"extractable_text,omitempty"`
	Artifacts     []safeDocumentArtifact `json:"artifacts,omitempty"`
	Warnings      []string               `json:"warnings,omitempty"`
	Failure       *document.Failure      `json:"failure,omitempty"`
}

type safeDocumentSource struct {
	Ref    string `json:"ref"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type safeDocumentArtifact struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Pages       []int  `json:"pages"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

func documentToolReportResult(report document.Report) *toolshared.ToolResult {
	projection := safeDocumentReport{
		SchemaVersion: report.SchemaVersion,
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
		projection.Text = &report.Inspection.ExtractableText
		projection.Warnings = append([]string(nil), report.Inspection.Warnings...)
	}
	if report.Extraction != nil {
		projection.SelectedPages = append([]int(nil), report.Extraction.SelectedPages...)
	}
	if report.Rendering != nil {
		projection.SelectedPages = append([]int(nil), report.Rendering.SelectedPages...)
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
