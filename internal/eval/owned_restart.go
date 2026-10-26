package eval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/bogdanovich/mintclaw/internal/evaltarget"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const maxOwnedGatewayGenerations = 4

type ownedLaunchIdentity struct {
	home         os.FileInfo
	temporary    os.FileInfo
	executable   os.FileInfo
	presetSHA256 string
	inputsSHA256 string
	probe        health.RuntimeProbe
	generation   int
}

type ownedGatewayGeneration struct {
	Runtime   health.RuntimeProbe        `json:"runtime"`
	Lifecycle TargetLifecycle            `json:"lifecycle"`
	Budget    *TargetBudgetObservation   `json:"budget,omitempty"`
	Surfaces  *PrivateSurfaceObservation `json:"surfaces,omitempty"`
}

func newOwnedLaunchIdentity(
	root *os.Root, profile preparedProfile, executable string, previous *ownedGateway,
) (ownedLaunchIdentity, error) {
	var identity ownedLaunchIdentity
	home, err := root.Stat(".")
	program, programErr := os.Lstat(executable)
	if previous == nil {
		// Recording restart provenance must not change ordinary first-launch
		// admission. Incomplete identity only disables the future restart path.
		if err == nil {
			identity.home = home
		}
		if programErr == nil {
			identity.executable = program
		}
		identity.generation = 1
		return identity, nil
	}
	if err != nil || programErr != nil || !filepath.IsAbs(executable) || !home.IsDir() ||
		home.Mode().Perm() != 0o700 || !program.Mode().IsRegular() {
		return identity, ErrOwnedGateway
	}
	identity.home, identity.executable, identity.generation = home, program, 1
	if !previous.restartUsed.Load() || !previous.cleanOwnedExit() || previous.identity.generation < 1 ||
		previous.identity.generation >= maxOwnedGatewayGenerations ||
		previous.identity.home == nil || previous.identity.executable == nil ||
		!os.SameFile(home, previous.identity.home) || !sameCensusFile(program, previous.identity.executable) ||
		profile.home != previous.profile.home || profile.workspace != previous.profile.workspace ||
		ownedProfileInputsHash(profile) != previous.identity.inputsSHA256 {
		return ownedLaunchIdentity{}, ErrOwnedGateway
	}
	temporary, err := root.Lstat(ownedGatewayTempDirectory)
	if err != nil || !temporary.IsDir() || temporary.Mode().Perm() != 0o700 ||
		previous.identity.temporary == nil || !os.SameFile(temporary, previous.identity.temporary) {
		return ownedLaunchIdentity{}, ErrOwnedGateway
	}
	identity.temporary, identity.generation = temporary, previous.identity.generation+1
	return identity, nil
}

func ownedPresetHash(preset Preset) string {
	data, err := json.Marshal(preset)
	if err != nil {
		return ""
	}
	return digest(data)
}

func ownedProfileInputsHash(profile preparedProfile) string {
	root, err := os.OpenRoot(profile.home)
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()
	hashes := ""
	for _, name := range []string{"config.json", config.SecurityConfigFile} {
		data, _, err := readCensusFile(root, name, MaxManifestBytes)
		if err != nil {
			return ""
		}
		hashes += digest(data)
		clear(data)
	}
	return digest([]byte(hashes))
}

func (identity *ownedLaunchIdentity) bind(preset Preset, profile preparedProfile, target admittedTarget) {
	identity.presetSHA256, identity.inputsSHA256 = ownedPresetHash(preset), ownedProfileInputsHash(profile)
	identity.probe = target.probe
}

func (child *ownedGateway) cleanOwnedExit() bool {
	if child == nil || child.command == nil || child.command.Process == nil || child.done == nil ||
		!child.admitted || child.forced.Load() || child.log == nil {
		return false
	}
	select {
	case <-child.done:
		return child.err == nil && !child.log.truncated && child.command.ProcessState != nil &&
			child.command.ProcessState.ExitCode() == 0
	default:
		return false
	}
}

func remainingOwnedBudget(child *ownedGateway) (*TargetBudgetObservation, providers.CallBudgetLimits, error) {
	var limits providers.CallBudgetLimits
	if !child.cleanOwnedExit() || child.budgetSpec == nil || child.profile.config == nil {
		return nil, limits, ErrOwnedGateway
	}
	observation := observeTargetBudget(child, nil, child.profile.config.SensitiveDataReplacer())
	if observation == nil || observation.verdict() != Pass || observation.Spec != *child.budgetSpec {
		return observation, limits, ErrOwnedGateway
	}
	proof, err := evaltarget.DecodeProof([]byte(observation.Proof), *child.budgetSpec, child.command.Process.Pid)
	if err != nil {
		return observation, limits, ErrOwnedGateway
	}
	limits = child.budgetSpec.Limits
	limits.MaxCalls -= proof.Snapshot.Calls
	limits.MaxReportedTokens -= proof.Snapshot.ReportedTokens
	if _, err := providers.NewCallBudget(limits); err != nil {
		return observation, limits, ErrOwnedGateway
	}
	return observation, limits, nil
}

// Only the retained owner can produce a successor. A used home is never
// admitted through the fresh-start entry point or a caller-supplied PID.
func restartOwnedGateway(
	ctx context.Context, previous *ownedGateway, preset Preset,
) (*ownedGateway, admittedTarget, ownedGatewayGeneration, error) {
	var generation ownedGatewayGeneration
	if ctx == nil || ctx.Err() != nil || previous == nil || previous.restartUsed.Load() ||
		previous.command == nil || previous.command.Process == nil || !previous.admitted ||
		previous.profile.config == nil || previous.identity.home == nil || previous.identity.temporary == nil ||
		previous.identity.executable == nil || !validDigest(previous.identity.inputsSHA256) ||
		previous.identity.generation < 1 || previous.identity.generation >= maxOwnedGatewayGenerations ||
		previous.identity.probe.PID != previous.command.Process.Pid ||
		previous.identity.presetSHA256 != ownedPresetHash(preset) || preset.ActiveTool != nil ||
		preset.TargetCallBudget == nil || !preset.CaptureModelMetadata || len(preset.WorkspaceFiles) != 0 ||
		len(previous.profile.config.Agents.List) != 1 ||
		previous.profile.config.Agents.List[0].ToolPolicy == nil ||
		len(previous.profile.config.Agents.List[0].ToolPolicy.Allow) != 0 {
		return nil, admittedTarget{}, generation, ErrOwnedGateway
	}
	profile, err := readPreparedProfile(preset, previous.profile.home)
	if err != nil || validateConversationProfile(profile.config, preset, profile.workspace) != nil ||
		profile.config.Tools.Document.Enabled || profile.config.Tools.Browser.Enabled ||
		profile.config.Tools.RequestUserInput.Enabled ||
		previous.identity.inputsSHA256 != ownedProfileInputsHash(profile) {
		return nil, admittedTarget{}, generation, ErrOwnedGateway
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 35*time.Second)
	generation.Runtime, generation.Lifecycle = previous.identity.probe, previous.stopAndWait(cleanupCtx)
	cleanupCancel()
	if !previous.cleanOwnedExit() {
		return nil, admittedTarget{}, generation, ErrOwnedGateway
	}
	observationCtx, observationCancel := context.WithTimeout(context.Background(), 30*time.Second)
	surfaces := observePrivateSurfaces(observationCtx, profile.home, nil, profile.config.SensitiveDataReplacer(), nil)
	observationCancel()
	generation.Surfaces = &surfaces
	budget, limits, err := remainingOwnedBudget(previous)
	generation.Budget = budget
	if err != nil || !observedOwnedProfile(&generation.Lifecycle, generation.Surfaces) || ctx.Err() != nil {
		return nil, admittedTarget{}, generation, ErrOwnedGateway
	}
	root, err := os.OpenRoot(profile.home)
	if err != nil {
		return nil, admittedTarget{}, generation, ErrOwnedGateway
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{".eval-gateway-owner", ".mintclaw.pid"} {
		if _, statErr := root.Lstat(name); !errors.Is(statErr, os.ErrNotExist) {
			return nil, admittedTarget{}, generation, ErrOwnedGateway
		}
	}
	if !previous.restartUsed.CompareAndSwap(false, true) {
		return nil, admittedTarget{}, generation, ErrOwnedGateway
	}
	next, err := launchOwnedGatewayGeneration(profile, previous.command.Path, &limits, previous)
	if err != nil {
		return nil, admittedTarget{}, generation, ErrOwnedGateway
	}
	target, err := admitOwnedGateway(ctx, next, preset, profile.home, nil, nil)
	if err != nil || target.probe.PID == generation.Runtime.PID ||
		target.probe.InstanceSHA256 == generation.Runtime.InstanceSHA256 ||
		target.probe.ConfigSHA256 != generation.Runtime.ConfigSHA256 ||
		target.probe.HomeSHA256 != generation.Runtime.HomeSHA256 ||
		target.probe.ConfigPathSHA256 != generation.Runtime.ConfigPathSHA256 ||
		target.probe.GitCommit != generation.Runtime.GitCommit ||
		target.probe.BuildVersion != generation.Runtime.BuildVersion {
		return next, admittedTarget{}, generation, ErrOwnedGateway
	}
	return next, target, generation, nil
}
