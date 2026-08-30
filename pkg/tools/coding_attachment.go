package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/h2non/filetype"
	_ "golang.org/x/image/webp"

	"github.com/bogdanovich/mintclaw/pkg/media"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	defaultAttachmentListLimit  = 50
	maxAttachmentListLimit      = 100
	defaultAttachmentReadBytes  = 32 << 10
	maxAttachmentReadBytes      = 64 << 10
	maxAttachmentImageDimension = 16384
	maxAttachmentImagePixels    = 40_000_000
	maxAttachmentGIFFrames      = 256
)

// CodingAttachmentTool selects durable coding-thread attachments without
// materializing every historical reference into later prompts.
type CodingAttachmentTool struct {
	store media.MediaStore
}

func NewCodingAttachmentTool() *CodingAttachmentTool {
	return &CodingAttachmentTool{}
}

func (t *CodingAttachmentTool) Name() string { return "coding_attachment" }

func (t *CodingAttachmentTool) Description() string {
	return "List or open durable files and images attached to this coding thread. " +
		"Use list to find an older attachment by filename and time, then open its exact ref. " +
		"Opening an image attaches it for vision; opening a UTF-8 file returns bounded content."
}

func (t *CodingAttachmentTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string", "enum": []string{"list", "open"},
			},
			"ref": map[string]any{
				"type": "string", "description": "Exact ref returned by list; required for open.",
			},
			"query": map[string]any{
				"type": "string", "description": "Optional case-insensitive filename filter for list.",
			},
			"offset": map[string]any{
				"type": "integer", "minimum": 0,
				"description": "List item offset, or UTF-8 byte offset when opening a file.",
			},
			"limit": map[string]any{
				"type": "integer", "minimum": 1,
				"description": "List item limit (max 100), or file byte limit (max 65536).",
			},
		},
		"required": []string{"action"},
	}
}

func (t *CodingAttachmentTool) SetMediaStore(store media.MediaStore) {
	t.store = store
}

func (t *CodingAttachmentTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	action, err := requiredStringArg(args, "action", "action")
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	catalog, ok := t.store.(media.ReferenceCatalog)
	if !ok || catalog == nil {
		return toolshared.ErrorResult("coding attachment catalog is unavailable")
	}
	switch action {
	case "list":
		return t.list(ctx, catalog, args)
	case "open":
		return t.open(ctx, catalog, args)
	default:
		return toolshared.ErrorResult("action must be list or open")
	}
}

type codingAttachmentListResponse struct {
	Attachments []media.Reference `json:"attachments"`
	Offset      int               `json:"offset"`
	NextOffset  int               `json:"next_offset,omitempty"`
	Total       int               `json:"total"`
}

func (t *CodingAttachmentTool) list(
	ctx context.Context,
	catalog media.ReferenceCatalog,
	args map[string]any,
) *toolshared.ToolResult {
	offset, err := boundedIntegerArg(args, "offset", 0, 0, 1<<20)
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	limit, err := boundedIntegerArg(
		args,
		"limit",
		defaultAttachmentListLimit,
		1,
		maxAttachmentListLimit,
	)
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	query, err := optionalStringArg(args, "query")
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	references, err := catalog.ListReferences(ctx)
	if err != nil {
		return toolshared.ErrorResult(fmt.Sprintf("list coding attachments: %v", err))
	}
	references = append([]media.Reference(nil), references...)
	slices.SortFunc(references, func(left, right media.Reference) int {
		if order := right.CreatedAt.Compare(left.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(left.Ref, right.Ref)
	})
	if query != "" {
		needle := strings.ToLower(query)
		filtered := references[:0]
		for _, reference := range references {
			if strings.Contains(strings.ToLower(reference.Filename), needle) {
				filtered = append(filtered, reference)
			}
		}
		references = filtered
	}
	total := len(references)
	if offset > total {
		offset = total
	}
	end := min(total, offset+limit)
	response := codingAttachmentListResponse{
		Attachments: append([]media.Reference(nil), references[offset:end]...),
		Offset:      offset,
		Total:       total,
	}
	if end < total {
		response.NextOffset = end
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return toolshared.ErrorResult(fmt.Sprintf("encode coding attachment list: %v", err))
	}
	return toolshared.SilentResult(string(encoded))
}

type codingAttachmentReadResponse struct {
	Ref         string `json:"ref"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Offset      int    `json:"offset"`
	NextOffset  int    `json:"next_offset,omitempty"`
	Content     string `json:"content"`
}

func (t *CodingAttachmentTool) open(
	ctx context.Context,
	catalog media.ReferenceCatalog,
	args map[string]any,
) *toolshared.ToolResult {
	ref, err := requiredStringArg(args, "ref", "ref")
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	data, reference, err := catalog.ReadReference(ctx, ref)
	if err != nil {
		return toolshared.ErrorResult(fmt.Sprintf("open coding attachment: %v", err))
	}
	if imageContentType, ok := verifiedAttachmentImageContentType(data); ok {
		return &toolshared.ToolResult{
			ForLLM: fmt.Sprintf(
				"Opened thread image %q (%s, %d bytes). Analyze the attached image.",
				reference.Filename,
				imageContentType,
				reference.Size,
			),
			ContextMedia: []string{reference.Ref},
		}
	}
	if !utf8.Valid(data) {
		return toolshared.ErrorResult(fmt.Sprintf(
			"attachment %q is not UTF-8 text; only images and UTF-8 files can be opened in context",
			reference.Filename,
		))
	}
	offset, err := boundedIntegerArg(args, "offset", 0, 0, len(data))
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	limit, err := boundedIntegerArg(
		args,
		"limit",
		defaultAttachmentReadBytes,
		1,
		maxAttachmentReadBytes,
	)
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	end := min(len(data), offset+limit)
	if !utf8.Valid(data[:offset]) {
		return toolshared.ErrorResult("offset splits a UTF-8 character; use the returned next_offset")
	}
	for end > offset && !utf8.Valid(data[offset:end]) {
		end--
	}
	if end == offset && offset < len(data) {
		_, width := utf8.DecodeRune(data[offset:])
		end += width
	}
	response := codingAttachmentReadResponse{
		Ref:         reference.Ref,
		Filename:    reference.Filename,
		ContentType: reference.ContentType,
		Size:        reference.Size,
		Offset:      offset,
		Content:     string(data[offset:end]),
	}
	if end < len(data) {
		response.NextOffset = end
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return toolshared.ErrorResult(fmt.Sprintf("encode coding attachment content: %v", err))
	}
	return toolshared.SilentResult(string(encoded))
}

func verifiedAttachmentImageContentType(data []byte) (string, bool) {
	kind, err := filetype.Match(data)
	if err != nil {
		return "", false
	}
	switch kind.MIME.Value {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return "", false
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !validAttachmentImageDimensions(config.Width, config.Height) {
		return "", false
	}
	if kind.MIME.Value == "image/gif" {
		decoded, decodeErr := gif.DecodeAll(bytes.NewReader(data))
		if decodeErr != nil || len(decoded.Image) == 0 || len(decoded.Image) > maxAttachmentGIFFrames {
			return "", false
		}
		var totalPixels uint64
		for _, frame := range decoded.Image {
			bounds := frame.Bounds()
			if !validAttachmentImageDimensions(bounds.Dx(), bounds.Dy()) {
				return "", false
			}
			totalPixels += uint64(bounds.Dx()) * uint64(bounds.Dy())
			if totalPixels > maxAttachmentImagePixels {
				return "", false
			}
		}
		return kind.MIME.Value, true
	}
	decoded, _, decodeErr := image.Decode(bytes.NewReader(data))
	if decodeErr != nil {
		return "", false
	}
	bounds := decoded.Bounds()
	if !validAttachmentImageDimensions(bounds.Dx(), bounds.Dy()) {
		return "", false
	}
	return kind.MIME.Value, true
}

func validAttachmentImageDimensions(width, height int) bool {
	if width <= 0 || height <= 0 || width > maxAttachmentImageDimension || height > maxAttachmentImageDimension {
		return false
	}
	return uint64(width)*uint64(height) <= maxAttachmentImagePixels
}

func boundedIntegerArg(args map[string]any, key string, defaultValue, minimum, maximum int) (int, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return defaultValue, nil
	}
	value, ok := raw.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		if integer, integerOK := raw.(int); integerOK {
			value = float64(integer)
		} else {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
	}
	if value < float64(minimum) || value > float64(maximum) {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
	}
	return int(value), nil
}
