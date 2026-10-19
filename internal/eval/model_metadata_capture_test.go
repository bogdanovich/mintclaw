package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

func TestModelMetadataCaptureRequiresExplicitExactPolicy(t *testing.T) {
	fixture := newAdmissionFixture(t)
	fixture.cfg.Diagnostics.TraceCapture = evalModelMetadataCapture()
	if validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace) == nil {
		t.Fatal("enabled capture was admitted without explicit opt-in")
	}
	fixture.preset.CaptureModelMetadata = true
	if err := validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace); err != nil {
		t.Fatal("exact bounded metadata policy was refused", err)
	}
	for name, change := range map[string]func(*config.DiagnosticTraceCaptureConfig){
		"content":          func(c *config.DiagnosticTraceCaptureConfig) { c.ContentMode = "redacted_content" },
		"unknown content":  func(c *config.DiagnosticTraceCaptureConfig) { c.ContentMode = "typo" },
		"absolute root":    func(c *config.DiagnosticTraceCaptureConfig) { c.StateDir = t.TempDir() },
		"relative root":    func(c *config.DiagnosticTraceCaptureConfig) { c.StateDir = "../other" },
		"bytes":            func(c *config.DiagnosticTraceCaptureConfig) { c.MaxTraceBytes++ },
		"records":          func(c *config.DiagnosticTraceCaptureConfig) { c.MaxRecords++ },
		"record bytes":     func(c *config.DiagnosticTraceCaptureConfig) { c.MaxRecordBytes++ },
		"retention":        func(c *config.DiagnosticTraceCaptureConfig) { c.RetentionHours++ },
		"count":            func(c *config.DiagnosticTraceCaptureConfig) { c.MaxTraces++ },
		"implicit default": func(c *config.DiagnosticTraceCaptureConfig) { c.MaxRecords = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			fixture.cfg.Diagnostics.TraceCapture = evalModelMetadataCapture()
			change(&fixture.cfg.Diagnostics.TraceCapture)
			if validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace) == nil {
				t.Fatal("unreviewed capture policy admitted")
			}
		})
	}
}

func TestModelMetadataCaptureRefusesAttachBeforeProbe(t *testing.T) {
	fixture := newAdmissionFixture(t)
	fixture.preset.CaptureModelMetadata = true
	fixture.cfg.Diagnostics.TraceCapture = evalModelMetadataCapture()
	fixture.writeConfig(t)
	_, err := Run(t.Context(), fixture.preset, []LoadedCase{loadedTestCase(t, "general.metadata.attach")},
		RunOptions{Home: fixture.home, OutputParent: fixture.output})
	if !errors.Is(err, ErrUnsafeTarget) || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
		t.Fatal("opt-in capture attached to a running target", err)
	}
}

func TestPrepareProfileOptsIntoFreshModelMetadataOnly(t *testing.T) {
	fixture, parent := preparationFixture(t)
	fixture.preset.CaptureModelMetadata = true
	seedCapture := fixture.cfg.Diagnostics.TraceCapture
	writeWorkspaceInput(t, fixture.workspace, "state/diagnostics/traces/old.json", "old private trace")
	result, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil || result.Outcome != "prepared" || !result.ModelMetadataPrepared || result.RuntimeRun ||
		fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
		t.Fatal("metadata preparation did not remain fresh/offline", err)
	}
	prepared, err := LoadPreset(result.PresetPath)
	if err != nil || !prepared.CaptureModelMetadata {
		t.Fatal("prepared preset lost explicit opt-in", err)
	}
	snapshot, err := config.NewRepository(result.ConfigPath).ReadDurable()
	if err != nil || snapshot.Config.Diagnostics.TraceCapture != evalModelMetadataCapture() ||
		validateConversationProfile(snapshot.Config, prepared, result.Workspace) != nil {
		t.Fatal("prepared capture policy differs", err)
	}
	traceRoot := diagnostictrace.ResolveStoreRoot(snapshot.Config.Diagnostics.TraceCapture.StateDir, result.Workspace)
	if traceRoot != filepath.Join(result.Workspace, "state", "diagnostics", "traces") {
		t.Fatal("capture escaped native default workspace root")
	}
	if _, err = os.Lstat(filepath.Join(result.Workspace, "state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("seed traces or native state were copied", err)
	}
	seed, err := config.NewRepository(fixture.preset.ConfigPath).ReadDurable()
	if err != nil || seed.Config.Diagnostics.TraceCapture != seedCapture {
		t.Fatal("preparation changed seed capture settings", err)
	}
	if validateFreshOwnedProfile(preparedProfile{
		config: snapshot.Config, home: result.Home, workspace: result.Workspace,
	}) != nil {
		t.Fatal("prepared metadata profile is not fresh")
	}
}

func TestOwnedModelMetadataRequiresPreparedEnabledCapture(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	fixture.preset.CaptureModelMetadata = true
	child, _, err := startOwnedGateway(t.Context(), fixture.preset, fixture.home)
	if !errors.Is(err, ErrOwnedGateway) || child != nil {
		t.Fatal("disabled source was started instead of prepared", err)
	}
	if _, err = os.Lstat(filepath.Join(fixture.home, ownedGatewayTempDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unprepared capture launched a child", err)
	}
}

func TestOwnedModelMetadataNeverAdoptsExistingTraceState(t *testing.T) {
	for name, alias := range map[string]bool{"directory": false, "symlink": true} {
		t.Run(name, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, "normal")
			fixture.preset.CaptureModelMetadata = true
			fixture.cfg.Diagnostics.TraceCapture = evalModelMetadataCapture()
			fixture.writeConfig(t)
			state := filepath.Join(fixture.workspace, "state")
			var err error
			if alias {
				err = os.Symlink(t.TempDir(), state)
			} else {
				err = os.Mkdir(state, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			child, _, err := startOwnedGateway(t.Context(), fixture.preset, fixture.home)
			if !errors.Is(err, ErrOwnedGateway) || child != nil {
				t.Fatal("old or aliased trace state was adopted", err)
			}
		})
	}
}

func TestOwnedModelMetadataScansPostStopFilesWithoutPrivateFacts(t *testing.T) {
	for _, mode := range []string{"metadata_safe", "metadata_leak", "metadata_early_exit", "metadata_startup_leak"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, mode)
			fixture.preset.CaptureModelMetadata = true
			fixture.cfg.Diagnostics.TraceCapture = evalModelMetadataCapture()
			if err := os.WriteFile(filepath.Join(fixture.home, ".security", "metadata-key"),
				[]byte(preparationTestKey), 0o600); err != nil {
				t.Fatal(err)
			}
			fixture.cfg.ModelList[0].APIKeys = config.SimpleSecureStrings("file://.security/metadata-key")
			settings, err := fixture.cfg.Channels.GetByType(config.ChannelMintClaw).GetDecoded()
			if err != nil {
				t.Fatal(err)
			}
			settings.(*config.MintClawSettings).SetToken("file://.security/metadata-channel-token")
			if err = os.WriteFile(filepath.Join(fixture.home, ".security", "metadata-channel-token"),
				[]byte("test-channel-token"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
				t.Fatal(err)
			}
			result, err := Run(
				t.Context(),
				fixture.preset,
				[]LoadedCase{loadedTestCase(t, "general.metadata.scan")},
				RunOptions{
					Home:         fixture.home,
					OutputParent: fixture.output,
					StartTarget:  true,
					SuiteTimeout: 15 * time.Second,
				},
			)
			if mode == "metadata_early_exit" {
				if !errors.Is(err, ErrOwnedGateway) {
					t.Fatal("startup failure was not retained", err)
				}
				return
			}
			if mode == "metadata_startup_leak" {
				if !errors.Is(err, ErrOwnedGateway) || !errors.Is(err, ErrPrivateEvidence) || result.Complete ||
					result.Verdict != Fail || result.PrivateSurfaces == nil || !result.PrivateSurfaces.LeakObserved ||
					result.TargetLifecycle == nil || result.TargetLifecycle.Admitted ||
					!result.TargetLifecycle.ExitObserved || pid.ProcessRunning(result.TargetLifecycle.PID) {
					t.Fatal("pre-readiness capture escaped post-stop privacy inspection", err)
				}
				return
			}
			if !result.Complete || result.TargetLifecycle == nil || result.TargetLifecycle.Code != "native_exit_zero" ||
				pid.ProcessRunning(result.TargetLifecycle.PID) || result.PrivateSurfaces == nil ||
				!result.PrivateSurfaces.valid() {
				t.Fatal("metadata was not scanned after owned stop", err, result.PrivateSurfaces)
			}
			if mode == "metadata_leak" {
				if !errors.Is(err, ErrNotPassed) || result.Verdict != Fail || !result.PrivateSurfaces.LeakObserved {
					t.Fatal("encoded credential in raw trace was hidden", err, result.PrivateSurfaces)
				}
			} else if err != nil || result.Verdict != Pass || result.PrivateSurfaces.Code != "observed" {
				t.Fatal("safe metadata capture did not pass bounded raw inspection", err, result.PrivateSurfaces)
			}
			if result.Usage != "unknown" || result.TargetUsage.AllCalls != "unknown" {
				t.Fatal("capture presence was promoted into complete usage")
			}
			encoded, err := json.Marshal(result)
			if err != nil || strings.Contains(string(encoded), preparationTestKey) {
				t.Fatal("raw trace credential entered report", err)
			}
		})
	}
}
