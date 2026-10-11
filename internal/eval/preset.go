package eval

import (
	"os"
	"path/filepath"
	"strings"
)

type Preset struct {
	Version           int      `json:"version"`
	ID                string   `json:"id"`
	ConfigPath        string   `json:"config_path"`
	ExpectedAgent     string   `json:"expected_agent"`
	ExpectedWorkspace string   `json:"expected_workspace"`
	IsolationRoot     string   `json:"isolation_root"`
	Capabilities      []string `json:"capabilities"`
	UserModel         string   `json:"user_model,omitempty"`
	JudgeModel        string   `json:"judge_model,omitempty"`
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
	return nil
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
