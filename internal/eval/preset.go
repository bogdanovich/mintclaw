package eval

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type Preset struct {
	Version              int                         `json:"version"`
	ID                   string                      `json:"id"`
	ConfigPath           string                      `json:"config_path"`
	ExpectedAgent        string                      `json:"expected_agent"`
	ExpectedWorkspace    string                      `json:"expected_workspace"`
	IsolationRoot        string                      `json:"isolation_root"`
	Capabilities         []string                    `json:"capabilities"`
	UserModel            string                      `json:"user_model,omitempty"`
	JudgeModel           string                      `json:"judge_model,omitempty"`
	WorkspaceFiles       []WorkspaceFile             `json:"workspace_files,omitempty"`
	CaptureModelMetadata bool                        `json:"capture_model_metadata,omitempty"`
	TargetCallBudget     *providers.CallBudgetLimits `json:"target_call_budget,omitempty"`
}

// WorkspaceFile admits one reviewed input, not a directory or runtime state.
type WorkspaceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// LoadPreset checks the manifest only. Loading credentials, contacting a
// gateway, and proving isolation belong to runtime preflight, not validation.
func LoadPreset(name string) (Preset, error) {
	var result Preset
	absolute, err := filepath.Abs(name)
	if err != nil {
		return result, ErrInvalidPreset
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return result, ErrInvalidPreset
	}
	defer func() { _ = root.Close() }()
	data, err := readBoundedFile(root, filepath.Base(absolute), MaxManifestBytes)
	if err != nil {
		return result, ErrInvalidPreset
	}
	result, err = DecodeManifest[Preset](data)
	if err != nil || result.Validate() != nil {
		return Preset{}, ErrInvalidPreset
	}
	base := filepath.Dir(absolute)
	result.ConfigPath = resolvePath(base, result.ConfigPath)
	result.ExpectedWorkspace = resolvePath(base, result.ExpectedWorkspace)
	result.IsolationRoot = resolvePath(base, result.IsolationRoot)
	if filepath.Dir(result.IsolationRoot) == result.IsolationRoot ||
		!pathWithin(result.IsolationRoot, result.ConfigPath) ||
		!pathWithin(result.IsolationRoot, result.ExpectedWorkspace) {
		return Preset{}, ErrInvalidPreset
	}
	return result, nil
}

func (preset Preset) Validate() error {
	if preset.Version != Version || !validID(preset.ID) || !validID(preset.ExpectedAgent) ||
		!validIDs(preset.Capabilities, 64) || !localPath(preset.ConfigPath) ||
		!localPath(preset.ExpectedWorkspace) || !localPath(preset.IsolationRoot) ||
		preset.UserModel != "" && !validID(preset.UserModel) ||
		preset.JudgeModel != "" && !validID(preset.JudgeModel) {
		return ErrInvalidPreset
	}
	if preset.TargetCallBudget != nil {
		if _, err := providers.NewCallBudget(*preset.TargetCallBudget); err != nil {
			return ErrInvalidPreset
		}
	}
	if (preset.TargetCallBudget != nil) != slices.Contains(preset.Capabilities, boundedCallsCapability) {
		return ErrInvalidPreset
	}
	if len(preset.WorkspaceFiles) > 64 {
		return ErrInvalidPreset
	}
	paths := make(map[string]bool, len(preset.WorkspaceFiles))
	for _, file := range preset.WorkspaceFiles {
		key := strings.ToLower(file.Path)
		if !preparableWorkspacePath(file.Path) || !validDigest(file.SHA256) || paths[key] {
			return ErrInvalidPreset
		}
		paths[key] = true
	}
	for name := range paths {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if paths[parent] {
				return ErrInvalidPreset
			}
		}
	}
	return nil
}

func preparableWorkspacePath(name string) bool {
	if !safeRelativePath(name) {
		return false
	}
	switch name {
	case "AGENTS.md", "SOUL.md", "IDENTITY.md", "USER.md", "TOOLS.md":
		return true
	default:
		return strings.HasPrefix(name, "skills/")
	}
}

func localPath(value string) bool {
	return boundedText(value, 1024) && !strings.ContainsAny(value, "\r\n") &&
		!strings.Contains(value, "://") && value != "." && value != string(filepath.Separator)
}

func resolvePath(base, name string) string {
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Join(base, name)
}

func pathWithin(root, name string) bool {
	relative, err := filepath.Rel(root, name)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
