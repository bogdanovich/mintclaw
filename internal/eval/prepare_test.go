package eval

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

const preparationTestKey = "synthetic-preparation-api-key-not-a-live-credential"

func preparationFixture(t *testing.T) (*admissionFixture, string) {
	t.Helper()
	fixture := newAdmissionFixture(t)
	fixture.cfg.ModelList[0].APIKeys = config.SimpleSecureStrings(preparationTestKey)
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil || os.Chmod(parent, 0o700) != nil {
		t.Fatal("private preparation parent unavailable", err)
	}
	return fixture, parent
}

func TestPrepareProfileCopiesOnlyPinnedInputsAndNativeCredentials(t *testing.T) {
	fixture, parent := preparationFixture(t)
	fixture.cfg.ModelList = append(fixture.cfg.ModelList, &config.ModelConfig{
		ModelName: "disabled", Provider: "openai", Model: "unused-model",
		APIKeys: config.SimpleSecureStrings("unused-private-model-key"),
	})
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"AGENTS.md": "Use only synthetic facts.\n", "skills/example/SKILL.md": "# Example\n",
		"skills/example/script.sh": "printf '%s\\n' synthetic\n", "USER.md": "unlisted private user details",
		"memory/old.md": "old session state", "sessions/old.json": "old conversation",
	} {
		writeWorkspaceInput(t, fixture.workspace, name, content)
		if strings.HasPrefix(name, "skills/") || name == "AGENTS.md" {
			fixture.preset.WorkspaceFiles = append(fixture.preset.WorkspaceFiles, WorkspaceFile{
				Path: name, SHA256: digest([]byte(content)),
			})
		}
	}
	writeAdmissionJSON(t, filepath.Join(fixture.home, "old-private-state.json"), map[string]string{"secret": "unused"})
	beforeConfig, err := os.ReadFile(fixture.preset.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeSecurity, err := os.ReadFile(filepath.Join(fixture.home, config.SecurityConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	result, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil || result.Outcome != "prepared" || result.RuntimeRun || result.WorkspaceFileCount != 3 ||
		!validDigest(result.PublicConfigSHA256) || !pathWithin(parent, result.Home) ||
		fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 || config.GetHome() != fixture.home {
		t.Fatal("preparation did not remain isolated/offline", err, result)
	}
	loaded, err := config.NewRepository(result.ConfigPath).ReadDurable()
	if err != nil {
		t.Fatal(err)
	}
	generated, err := LoadPreset(result.PresetPath)
	if err != nil || generated.ID != fixture.preset.ID || generated.IsolationRoot != result.Home ||
		len(
			generated.WorkspaceFiles,
		) != 3 || validateConversationProfile(loaded.Config, generated, result.Workspace) != nil ||
		validateFreshOwnedProfile(
			preparedProfile{config: loaded.Config, home: result.Home, workspace: result.Workspace},
		) != nil {
		t.Fatal("generated profile is not fresh/admitted", err)
	}
	settings, err := gatewayclient.ChannelSettings(loaded.Config)
	if err != nil || len(settings.Token.String()) != 64 || settings.Token.String() == "test-channel-token" ||
		len(loaded.Config.ModelList) != 1 || loaded.Config.ModelList[0].APIKey() != preparationTestKey ||
		loaded.Config.Gateway.Host != "127.0.0.1" || loaded.Config.Gateway.Port == fixture.cfg.Gateway.Port {
		t.Fatal("native credentials/listener were not independently prepared", err)
	}
	for _, file := range fixture.preset.WorkspaceFiles {
		data, readErr := os.ReadFile(filepath.Join(result.Workspace, file.Path))
		if readErr != nil || digest(data) != file.SHA256 {
			t.Fatal("pinned workspace input changed", readErr)
		}
	}
	for _, path := range []string{
		"workspace/USER.md", "workspace/memory", "workspace/sessions", ".mintclaw.pid", "old-private-state.json",
	} {
		if _, statErr := os.Lstat(filepath.Join(result.Home, path)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal("unlisted state copied", path, statErr)
		}
	}
	if err = filepath.WalkDir(result.Home, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, statErr := entry.Info()
		if statErr != nil || info.Mode().Perm()&0o077 != 0 || info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("profile entry is not private/regular", statErr)
		}
		if !entry.IsDir() &&
			!strings.Contains(path, string(filepath.Separator)+".security"+string(filepath.Separator)) {
			data, readErr := os.ReadFile(path)
			if readErr != nil || strings.Contains(string(data), preparationTestKey) ||
				strings.Contains(
					string(data),
					"test-channel-token",
				) || strings.Contains(string(data), "unused-private-model-key") {
				t.Fatal("public/profile metadata exposed copied credentials", readErr)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	afterConfig, _ := os.ReadFile(fixture.preset.ConfigPath)
	afterSecurity, _ := os.ReadFile(filepath.Join(fixture.home, config.SecurityConfigFile))
	if string(afterConfig) != string(beforeConfig) || string(afterSecurity) != string(beforeSecurity) {
		t.Fatal("seed configuration or security overlay changed")
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), preparationTestKey) ||
		strings.Contains(
			string(encoded),
			"test-channel-token",
		) || strings.Contains(string(encoded), "Use only synthetic") {
		t.Fatal("preparation receipt exposed private values", err)
	}
}

func TestPrepareProfileRefusesUnsafeInputsBeforeAllocation(t *testing.T) {
	for _, variant := range []string{"digest", "symlink file", "symlink directory", "oversize", "known credential", "directory"} {
		t.Run(variant, func(t *testing.T) {
			fixture, parent := preparationFixture(t)
			name, content := "AGENTS.md", "reviewed input"
			switch variant {
			case "symlink file":
				writeWorkspaceInput(t, fixture.workspace, "SOUL.md", content)
				if err := os.Symlink("SOUL.md", filepath.Join(fixture.workspace, name)); err != nil {
					t.Fatal(err)
				}
			case "symlink directory":
				name = "skills/linked/SKILL.md"
				writeWorkspaceInput(t, fixture.workspace, "skills/original/SKILL.md", content)
				if err := os.Symlink("original", filepath.Join(fixture.workspace, "skills/linked")); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(filepath.Join(fixture.workspace, name), 0o700); err != nil {
					t.Fatal(err)
				}
			default:
				switch variant {
				case "oversize":
					content = strings.Repeat("x", MaxManifestBytes+1)
				case "known credential":
					content = "private " + preparationTestKey
				}
				writeWorkspaceInput(t, fixture.workspace, name, content)
			}
			fixture.preset.WorkspaceFiles = []WorkspaceFile{{Path: name, SHA256: digest([]byte(content))}}
			if variant == "digest" {
				fixture.preset.WorkspaceFiles[0].SHA256 = strings.Repeat("0", 64)
			}
			result, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
			entries, readErr := os.ReadDir(parent)
			if !errors.Is(err, ErrPreparation) || result.Home != "" || readErr != nil || len(entries) != 0 ||
				fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
				t.Fatal("unsafe input allocated a profile or reached runtime", err, readErr)
			}
		})
	}
}

func TestPrepareProfileRejectsBroadSeedBeforeNativeRepository(t *testing.T) {
	fixture := newAdmissionFixture(t)
	fixture.cfg.Tools.Exec.Enabled = true
	fixture.writeConfig(t)
	lock := filepath.Join(fixture.home, config.SecurityConfigFile+".lock")
	if err := os.Remove(lock); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	_, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, t.TempDir())
	if !errors.Is(err, ErrPreparation) || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
		t.Fatal("broad seed reached runtime", err)
	}
	if _, err = os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("broad seed reached native lock/recovery", err)
	}
}

func TestPrepareProfileRejectsAmbiguousAuthAndDestination(t *testing.T) {
	for _, variant := range []string{"no key", "auth method", "header", "proxy", "fallback", "url credential", "unknown role", "nested parent", "canceled"} {
		t.Run(variant, func(t *testing.T) {
			fixture, parent := preparationFixture(t)
			ctx := t.Context()
			model := fixture.cfg.ModelList[0]
			switch variant {
			case "no key":
				model.APIKeys = nil
			case "auth method":
				model.AuthMethod = "oauth"
			case "header":
				model.CustomHeaders = map[string]string{"X-Secret": "not-typed"}
			case "proxy":
				model.Proxy = "http://127.0.0.1:9999"
			case "fallback":
				model.Fallbacks = []string{"other"}
				fixture.cfg.ModelList = append(fixture.cfg.ModelList, &config.ModelConfig{
					ModelName: "other", Provider: "openai", Model: "other-model", Enabled: true,
					APIKeys: config.SimpleSecureStrings(preparationTestKey),
				})
			case "url credential":
				model.APIBase = "https://user:password@example.invalid"
			case "unknown role":
				fixture.preset.JudgeModel = "missing"
			case "nested parent":
				parent = fixture.output
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
				t.Fatal(err)
			}
			result, err := PrepareProfile(ctx, fixture.preset, fixture.home, parent)
			entries, readErr := os.ReadDir(parent)
			if !errors.Is(err, ErrPreparation) || result.Home != "" || readErr != nil || len(entries) != 0 ||
				fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
				t.Fatal("ambiguous provisioning allocated state or reached runtime", err, readErr)
			}
		})
	}
}

func TestPrepareProfileUsesDurableKeysAndFreshChannelIdentity(t *testing.T) {
	fixture, parent := preparationFixture(t)
	fixture.cfg.ModelList[0].APIKeys = config.SimpleSecureStrings(preparationTestKey, "synthetic-second-profile-key")
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINTCLAW_CHANNELS_MINTCLAW_TOKEN", "ambient-token-not-an-input")
	t.Setenv("MINTCLAW_GATEWAY_HOST", "0.0.0.0")
	var previousHome, previousToken string
	for range 2 {
		result, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
		if err != nil {
			t.Fatal("ambient overrides affected durable preparation", err)
		}
		snapshot, err := config.NewRepository(result.ConfigPath).ReadDurable()
		if err != nil {
			t.Fatal(err)
		}
		settings, err := gatewayclient.ChannelSettings(snapshot.Config)
		if err != nil || result.Home == previousHome || settings.Token.String() == previousToken ||
			settings.Token.String() == "ambient-token-not-an-input" || settings.Token.String() == "test-channel-token" ||
			snapshot.Config.Gateway.Host != "127.0.0.1" ||
			len(
				snapshot.Config.ModelList[0].APIKeys,
			) != 2 || snapshot.Config.ModelList[0].APIKey() != preparationTestKey ||
			snapshot.Config.ModelList[0].APIKeys[1].String() != "synthetic-second-profile-key" {
			t.Fatal("fresh native credential identity was not preserved", err)
		}
		previousHome, previousToken = result.Home, settings.Token.String()
	}
}

func TestPrepareProfileRefusesAggregateInputBudgetBeforeAllocation(t *testing.T) {
	fixture, parent := preparationFixture(t)
	content := strings.Repeat("x", MaxManifestBytes)
	for index := range 17 {
		name := "skills/" + string(rune('a'+index)) + ".md"
		writeWorkspaceInput(t, fixture.workspace, name, content)
		fixture.preset.WorkspaceFiles = append(fixture.preset.WorkspaceFiles, WorkspaceFile{
			Path: name, SHA256: digest([]byte(content)),
		})
	}
	result, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	entries, readErr := os.ReadDir(parent)
	if !errors.Is(err, ErrPreparation) || result.Home != "" || readErr != nil || len(entries) != 0 {
		t.Fatal("aggregate input bound allocated a profile", err, readErr)
	}
}
