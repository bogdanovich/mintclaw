package tools

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/h2non/filetype"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	fstools "github.com/bogdanovich/mintclaw/pkg/tools/fs"
)

const (
	defaultImageEditMaxInputBytes = config.DefaultMaxMediaSize
	maxImageEditInputs            = 4
)

type imageAction string

const (
	imageActionGenerate imageAction = "generate"
	imageActionEdit     imageAction = "edit"
)

// WithImageGenerationInputPolicy applies the same workspace boundary used by
// local file tools to source images supplied to image editing.
func WithImageGenerationInputPolicy(
	restrict bool,
	maxInputBytes int,
	allowPaths []*regexp.Regexp,
) ImageGenerateToolOption {
	return func(tool *ImageGenerateTool) {
		tool.restrict = restrict
		if maxInputBytes > 0 {
			tool.maxInputBytes = maxInputBytes
		}
		tool.allowPaths = append([]*regexp.Regexp(nil), allowPaths...)
	}
}

func readImageActionAndInputs(args map[string]any) (imageAction, []string, error) {
	inputs, err := readImageInputLocations(args["input_images"])
	if err != nil {
		return "", nil, err
	}
	action := imageAction(strings.ToLower(strings.TrimSpace(readStringDefault(args, "action", ""))))
	if action == "" {
		if len(inputs) > 0 {
			action = imageActionEdit
		} else {
			action = imageActionGenerate
		}
	}
	switch action {
	case imageActionEdit:
		if len(inputs) == 0 {
			return "", nil, fmt.Errorf("edit action requires at least one input image")
		}
	case imageActionGenerate:
		if len(inputs) > 0 {
			return "", nil, fmt.Errorf("generate action cannot include input images; use edit")
		}
		if strings.TrimSpace(readStringDefault(args, "input_fidelity", "")) != "" {
			return "", nil, fmt.Errorf("input_fidelity is only valid for image editing")
		}
	default:
		return "", nil, fmt.Errorf("action must be generate or edit")
	}
	return action, inputs, nil
}

func readImageInputLocations(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		values = make([]string, 0, len(typed))
		for _, value := range typed {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("input_images must contain only paths or media references")
			}
			values = append(values, text)
		}
	default:
		return nil, fmt.Errorf("input_images must be an array")
	}
	if len(values) > maxImageEditInputs {
		return nil, fmt.Errorf("too many input images (maximum %d)", maxImageEditInputs)
	}
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
		if values[index] == "" {
			return nil, fmt.Errorf("input image %d is empty", index+1)
		}
	}
	return values, nil
}

func (t *ImageGenerateTool) resolveImageInputs(
	locations []string,
) ([]providers.ImageGenerationInput, error) {
	if len(locations) > maxImageEditInputs {
		return nil, fmt.Errorf("too many input images (maximum %d)", maxImageEditInputs)
	}
	maxBytes := t.maxInputBytes
	if maxBytes <= 0 {
		maxBytes = defaultImageEditMaxInputBytes
	}
	remaining := int64(maxBytes)
	inputs := make([]providers.ImageGenerationInput, 0, len(locations))
	for index, location := range locations {
		path, filename, err := t.resolveImageInputLocation(location)
		if err != nil {
			return nil, fmt.Errorf("input image %d is not accessible", index+1)
		}
		inputLimit := remaining
		data, err := readBoundedImageInput(path, inputLimit)
		if err != nil {
			return nil, fmt.Errorf("input image %d %w", index+1, err)
		}
		remaining -= int64(len(data))
		kind, err := filetype.Match(data)
		if err != nil || !supportedImageEditMIME(kind.MIME.Value) {
			return nil, fmt.Errorf("input image %d is not a supported PNG, JPEG, or WebP file", index+1)
		}
		filename = filepath.Base(strings.TrimSpace(filename))
		if filename == "" || filename == "." {
			filename = fmt.Sprintf("input-%d.%s", index+1, kind.Extension)
		}
		inputs = append(inputs, providers.ImageGenerationInput{
			Data:        data,
			Filename:    filename,
			ContentType: kind.MIME.Value,
		})
	}
	return inputs, nil
}

func (t *ImageGenerateTool) resolveImageInputLocation(location string) (string, string, error) {
	if strings.HasPrefix(location, "media://") {
		path, meta, err := t.mediaStore.ResolveWithMeta(location)
		if err != nil {
			return "", "", err
		}
		return path, meta.Filename, nil
	}
	path, err := fstools.ValidatePathWithAllowPaths(location, t.workspace, t.restrict, t.allowPaths)
	if err != nil {
		return "", "", err
	}
	return path, filepath.Base(path), nil
}

func readBoundedImageInput(path string, remaining int64) (data []byte, returnErr error) {
	if remaining <= 0 {
		return nil, fmt.Errorf("exceeds the input size limit")
	}
	file, err := openImageInput(path)
	if err != nil {
		return nil, fmt.Errorf("is not accessible")
	}
	defer func() {
		if closeErr := file.Close(); returnErr == nil && closeErr != nil {
			returnErr = fmt.Errorf("could not be read")
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("is not a regular file")
	}
	if info.Size() > remaining {
		return nil, fmt.Errorf("exceeds the input size limit")
	}
	data, err = io.ReadAll(io.LimitReader(file, remaining+1))
	if err != nil {
		return nil, fmt.Errorf("could not be read")
	}
	if int64(len(data)) > remaining {
		return nil, fmt.Errorf("exceeds the input size limit")
	}
	return data, nil
}

func supportedImageEditMIME(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}
