package eval

import (
	"os"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

type preparedProfile struct {
	config    *config.Config
	home      string
	workspace string
}

// File admission is shared by run and judge-only work. Only run adds process
// identity and target capability admission; this reader performs no network I/O.
func readPreparedProfile(preset Preset, home string) (preparedProfile, error) {
	profile, before, err := inspectPreparedProfile(preset, home)
	if err != nil {
		return preparedProfile{}, err
	}
	cfg, err := config.LoadConfigReadOnly(preset.ConfigPath)
	if err != nil || !profileConfigUnchanged(profile.home, before) {
		return preparedProfile{}, ErrUnsafeTarget
	}
	profile.config = cfg
	return profile, nil
}

func inspectPreparedProfile(preset Preset, home string) (preparedProfile, string, error) {
	var result preparedProfile
	physical, err := privateHome(preset.IsolationRoot)
	if preset.Validate() != nil || err != nil || !samePhysicalPath(home, physical) ||
		!samePhysicalPath(config.GetHome(), physical) ||
		!samePhysicalPath(preset.ConfigPath, filepath.Join(physical, "config.json")) {
		return result, "", ErrUnsafeTarget
	}
	workspace, err := filepath.EvalSymlinks(preset.ExpectedWorkspace)
	if err != nil || !pathWithin(physical, workspace) {
		return result, "", ErrUnsafeTarget
	}
	root, err := os.OpenRoot(physical)
	if err != nil {
		return result, "", ErrUnsafeTarget
	}
	defer func() { _ = root.Close() }()
	data, err := readBoundedFile(root, "config.json", MaxManifestBytes)
	// Decode before the native loader's diagnostics. Prepared files are operator
	// owned and immutable; concurrent hostile filesystem mutation is not certified.
	var preview config.Config
	if err != nil || config.DecodeCurrentConfig(data, &preview) != nil {
		return result, "", ErrUnsafeTarget
	}
	for _, model := range preview.ModelList {
		if model == nil {
			return result, "", ErrUnsafeTarget
		}
	}
	return preparedProfile{config: &preview, home: physical, workspace: workspace}, digest(data), nil
}

func profileConfigUnchanged(home, before string) bool {
	root, err := os.OpenRoot(home)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	after, err := readBoundedFile(root, "config.json", MaxManifestBytes)
	return err == nil && before == digest(after)
}
