package eval

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

const (
	readToolCapability   = "read_tool"
	readFixtureDirectory = "eval-fixtures"
	readFixturePath      = "eval-fixtures/recovery.txt"
	readMissingPath      = "eval-fixtures/missing.txt"
)

func hasReadTool(preset Preset) bool {
	return slices.Contains(preset.Capabilities, readToolCapability)
}

func readFixtureInput(preset Preset) (WorkspaceFile, bool) {
	for _, file := range preset.WorkspaceFiles {
		if file.Path == readFixturePath {
			return file, true
		}
	}
	return WorkspaceFile{}, false
}

func validateReadToolPreset(preset Preset) error {
	if !hasReadTool(preset) {
		return nil
	}
	file, pinned := readFixtureInput(preset)
	if !pinned || !validDigest(file.SHA256) || preset.TargetCallBudget == nil || !preset.CaptureModelMetadata ||
		preset.UserModel != "" || preset.JudgeModel == "" || preset.ActiveTool != nil ||
		slices.Contains(preset.Capabilities, "interactions") ||
		slices.Contains(preset.Capabilities, "pdf") || slices.Contains(preset.Capabilities, "browser") {
		return ErrInvalidPreset
	}
	return nil
}

func nativeReadToolEnabled(cfg *config.Config) bool {
	if cfg == nil || len(cfg.Agents.List) != 1 || cfg.Agents.List[0].ToolPolicy == nil {
		return false
	}
	policy := cfg.Agents.List[0].ToolPolicy
	return policy.Default == config.AgentCapabilityDefaultDeny && len(policy.Deny) == 0 &&
		slices.Contains(policy.Allow, "read_file")
}

func validateReadToolPolicy(cfg *config.Config, preset Preset) error {
	if hasReadTool(preset) {
		if validateReadToolPreset(preset) != nil || !nativeReadToolEnabled(cfg) ||
			!slices.Equal(cfg.Agents.List[0].ToolPolicy.Allow, []string{"read_file"}) ||
			cfg.Tools.RequestUserInput.Enabled {
			return ErrUnsafeTarget
		}
	}
	return nil
}

// The complete canonical directory census also proves the missing path absent.
// This is an owned-file observation, not hostile-filesystem attestation.
type ReadFixtureSnapshot struct {
	Code    string   `json:"code"`
	SHA256  string   `json:"sha256,omitempty"`
	Entries []string `json:"entries,omitempty"`
}

func observeReadFixture(ctx context.Context, root *os.Root) ReadFixtureSnapshot {
	unavailable := ReadFixtureSnapshot{Code: "unavailable"}
	if ctx.Err() != nil {
		return unavailable
	}
	before, err := root.Lstat(readFixtureDirectory)
	if err != nil || !before.IsDir() || before.Mode().Perm() != 0o700 {
		return unavailable
	}
	directory, err := root.Open(readFixtureDirectory)
	if err != nil {
		return unavailable
	}
	opened, statErr := directory.Stat()
	entries, readErr := directory.ReadDir(2)
	closeErr := directory.Close()
	if statErr != nil || !sameCensusFile(before, opened) || closeErr != nil ||
		readErr != nil && !errors.Is(readErr, io.EOF) || len(entries) != 1 || entries[0].Name() != "recovery.txt" {
		return unavailable
	}
	fileBefore, err := root.Lstat(readFixturePath)
	if err != nil || !fileBefore.Mode().IsRegular() || fileBefore.Mode().Perm() != 0o600 {
		return unavailable
	}
	data, err := readBoundedFile(root, readFixturePath, MaxManifestBytes)
	fileAfter, fileErr := root.Lstat(readFixturePath)
	after, dirErr := root.Lstat(readFixtureDirectory)
	if ctx.Err() != nil || err != nil || fileErr != nil || dirErr != nil || len(data) == 0 ||
		!sameCensusFile(fileBefore, fileAfter) || !sameCensusFile(before, after) {
		return unavailable
	}
	return ReadFixtureSnapshot{Code: "observed", SHA256: digest(data), Entries: []string{"recovery.txt"}}
}

func observeWorkspaceReadFixture(ctx context.Context, workspace string) ReadFixtureSnapshot {
	root, err := os.OpenRoot(filepath.Clean(workspace))
	if err != nil {
		return ReadFixtureSnapshot{Code: "unavailable"}
	}
	defer func() { _ = root.Close() }()
	return observeReadFixture(ctx, root)
}
