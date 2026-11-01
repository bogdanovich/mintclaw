package eval

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func enableTestReadTool(t *testing.T, fixture *admissionFixture) {
	t.Helper()
	limits := targetBudgetTestLimits()
	fixture.preset.TargetCallBudget, fixture.preset.CaptureModelMetadata = &limits, true
	fixture.preset.Capabilities = []string{"conversation", boundedCallsCapability, readToolCapability}
	fixture.preset.JudgeModel = "eval-model"
	fixture.cfg.Agents.List[0].ToolPolicy.Allow = []string{"read_file"}
	fixture.cfg.Tools.RequestUserInput.Enabled = false
	const data = "mc-c3-fixture-synthetic-t84\n"
	fixture.preset.WorkspaceFiles = append(fixture.preset.WorkspaceFiles,
		WorkspaceFile{Path: readFixturePath, SHA256: digest([]byte(data))})
	if err := os.Mkdir(filepath.Join(fixture.workspace, readFixtureDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.workspace, readFixturePath), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadToolProfilePinsOneSyntheticFileWithoutWideningAuthority(t *testing.T) {
	for _, kind := range []string{
		"positive", "missing_capability", "missing_pin", "wrong_path", "bad_digest", "missing_budget",
		"missing_metadata", "model_user", "missing_judge", "write_tool", "outside_read", "native_read_missing",
		"question_tool",
	} {
		t.Run(kind, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			enableTestReadTool(t, fixture)
			switch kind {
			case "missing_capability":
				fixture.preset.Capabilities = slices.DeleteFunc(fixture.preset.Capabilities,
					func(value string) bool { return value == readToolCapability })
			case "missing_pin":
				fixture.preset.WorkspaceFiles = nil
			case "wrong_path":
				fixture.preset.WorkspaceFiles[0].Path = "eval-fixtures/other.txt"
			case "bad_digest":
				fixture.preset.WorkspaceFiles[0].SHA256 = "invalid"
			case "missing_budget":
				fixture.preset.TargetCallBudget = nil
			case "missing_metadata":
				fixture.preset.CaptureModelMetadata = false
			case "model_user":
				fixture.preset.UserModel = "eval-model"
			case "missing_judge":
				fixture.preset.JudgeModel = ""
			case "write_tool":
				fixture.cfg.Agents.List[0].ToolPolicy.Allow = append(
					fixture.cfg.Agents.List[0].ToolPolicy.Allow,
					"write_file",
				)
			case "outside_read":
				fixture.cfg.Agents.Defaults.AllowReadOutsideWorkspace = true
			case "native_read_missing":
				fixture.cfg.Agents.List[0].ToolPolicy.Allow = []string{"request_user_input"}
			case "question_tool":
				fixture.cfg.Tools.RequestUserInput.Enabled = true
				fixture.cfg.Agents.List[0].ToolPolicy.Allow = append(
					fixture.cfg.Agents.List[0].ToolPolicy.Allow,
					"request_user_input",
				)
			}
			err := fixture.preset.Validate()
			if err == nil {
				err = validateConversationProfile(fixture.cfg, fixture.preset, fixture.workspace)
			}
			if (err == nil) != (kind == "positive") || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
				t.Fatal("read-only profile escaped its pre-work pin", err)
			}
		})
	}
}

func TestReadToolFreshProfileAndSnapshotRefuseOtherState(t *testing.T) {
	for _, kind := range []string{"positive", "extra", "missing_exists", "symlink", "writable", "directory_mode"} {
		t.Run(kind, func(t *testing.T) {
			fixture, parent := preparationFixture(t)
			enableTestReadTool(t, fixture)
			if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
				t.Fatal(err)
			}
			prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
			if err != nil {
				t.Fatal("prepare an actually fresh owned profile", err)
			}
			preset, err := LoadPreset(prepared.PresetPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(config.EnvHome, prepared.Home)
			t.Setenv(config.EnvConfig, prepared.ConfigPath)
			profile, err := readPreparedProfile(preset, prepared.Home)
			if err != nil {
				t.Fatal("read fresh owned profile", err)
			}
			name := filepath.Join(profile.workspace, readFixturePath)
			switch kind {
			case "extra", "missing_exists":
				other := "other.txt"
				if kind == "missing_exists" {
					other = "missing.txt"
				}
				if os.WriteFile(filepath.Join(filepath.Dir(name), other), []byte("synthetic"), 0o600) != nil {
					t.Fatal("extra fixture setup")
				}
			case "symlink":
				if os.Rename(name, name+".old") != nil || os.Symlink("recovery.txt.old", name) != nil {
					t.Fatal("link setup")
				}
			case "writable":
				if os.Chmod(name, 0o666) != nil {
					t.Fatal("mode setup")
				}
			case "directory_mode":
				if os.Chmod(filepath.Dir(name), 0o777) != nil {
					t.Fatal("directory mode setup")
				}
			}
			snapshot := observeWorkspaceReadFixture(t.Context(), profile.workspace)
			freshErr := validateFreshOwnedProfile(profile)
			if (snapshot.Code == "observed") != (kind == "positive") || (freshErr == nil) != (kind == "positive") {
				t.Fatal("noncanonical fixture became fresh or observable", snapshot, freshErr)
			}
		})
	}
}

func TestReadToolPreparationCopiesOnlyPinnedFileAndKeepsFreshAdmission(t *testing.T) {
	fixture, parent := preparationFixture(t)
	enableTestReadTool(t, fixture)
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil || prepared.RuntimeRun {
		t.Fatal("read-only offline preparation", err)
	}
	preset, err := LoadPreset(prepared.PresetPath)
	if err != nil {
		t.Fatal(err)
	}
	file, ok := readFixtureInput(preset)
	snapshot := observeWorkspaceReadFixture(context.Background(), prepared.Workspace)
	if !ok || snapshot.Code != "observed" || snapshot.SHA256 != file.SHA256 ||
		!slices.Equal(snapshot.Entries, []string{"recovery.txt"}) || !strings.HasPrefix(prepared.Home, parent) {
		t.Fatal("prepared fixture pin or identity changed", snapshot)
	}
}
