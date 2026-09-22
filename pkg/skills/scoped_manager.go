package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/utils"
)

type SkillMutationOperation string

const (
	SkillMutationInstall SkillMutationOperation = "install"
	SkillMutationUpdate  SkillMutationOperation = "update"
	SkillMutationRemove  SkillMutationOperation = "remove"
	SkillMutationMove    SkillMutationOperation = "move"
)

type SkillMutationAction string

const (
	SkillMutationCreate   SkillMutationAction = "create"
	SkillMutationReplace  SkillMutationAction = "replace"
	SkillMutationDelete   SkillMutationAction = "delete"
	SkillMutationRelocate SkillMutationAction = "relocate"
)

type SkillInstallCompatibility struct {
	Runtime SkillRuntime             `json:"runtime"`
	Status  SkillCompatibilityStatus `json:"status"`
	Checks  []SkillRequirementCheck  `json:"checks,omitempty"`
	Message string                   `json:"message,omitempty"`
}

type SkillMutationPlan struct {
	Operation        SkillMutationOperation      `json:"operation"`
	Action           SkillMutationAction         `json:"action"`
	Scope            SkillInstallScope           `json:"scope"`
	Root             string                      `json:"root"`
	Target           string                      `json:"target"`
	SourceScope      SkillInstallScope           `json:"source_scope,omitempty"`
	SourceRoot       string                      `json:"source_root,omitempty"`
	Source           string                      `json:"source,omitempty"`
	SkillName        string                      `json:"skill_name"`
	Registry         string                      `json:"registry,omitempty"`
	Slug             string                      `json:"slug,omitempty"`
	RequestedVersion string                      `json:"requested_version,omitempty"`
	ResolvedVersion  string                      `json:"resolved_version,omitempty"`
	Origin           *OriginMetadata             `json:"origin,omitempty"`
	Compatibility    []SkillInstallCompatibility `json:"compatibility,omitempty"`
	DependencyGaps   []SkillRequirementCheck     `json:"dependency_gaps,omitempty"`
	Warnings         []string                    `json:"warnings,omitempty"`
	DryRun           bool                        `json:"dry_run"`
	Applied          bool                        `json:"applied"`
}

type SkillMoveRequest struct {
	Source SkillInstallTarget
	Target SkillInstallTarget
	Name   string
	DryRun bool
}

type SkillInstallRequest struct {
	Target   SkillInstallTarget
	Registry string
	Slug     string
	Version  string
	Replace  bool
	DryRun   bool
}

type ScopedSkillManager struct {
	registries              *RegistryManager
	environments            map[SkillRuntime]SkillCompatibilityEnvironment
	removeMovedSource       func(string) error
	beforeReplacementCommit func(string)
}

func NewScopedSkillManager(
	registries *RegistryManager,
	environments map[SkillRuntime]SkillCompatibilityEnvironment,
) *ScopedSkillManager {
	if registries == nil {
		registries = NewRegistryManager()
	}
	return &ScopedSkillManager{
		registries:        registries,
		environments:      environments,
		removeMovedSource: os.RemoveAll,
	}
}

func (manager *ScopedSkillManager) Install(
	ctx context.Context,
	request SkillInstallRequest,
) (SkillMutationPlan, error) {
	return manager.install(ctx, request, SkillMutationInstall, "", nil)
}

func (manager *ScopedSkillManager) Update(
	ctx context.Context,
	target SkillInstallTarget,
	name string,
	version string,
	dryRun bool,
) (SkillMutationPlan, error) {
	managed, err := inspectInstalledSkill(target, name)
	if err != nil {
		return SkillMutationPlan{}, err
	}
	if managed.OriginKind != ManagedSkillOriginThirdParty || managed.Origin == nil {
		return SkillMutationPlan{}, fmt.Errorf("skill %q has no immutable third-party origin to update", name)
	}
	return manager.install(ctx, SkillInstallRequest{
		Target: target, Registry: managed.Origin.Registry, Slug: managed.Origin.Slug,
		Version: version, Replace: true, DryRun: dryRun,
	}, SkillMutationUpdate, name, managed.Origin)
}

func (manager *ScopedSkillManager) Remove(
	target SkillInstallTarget,
	name string,
	dryRun bool,
) (SkillMutationPlan, error) {
	managed, err := inspectInstalledSkill(target, name)
	if err != nil {
		return SkillMutationPlan{}, err
	}
	plan := SkillMutationPlan{
		Operation: SkillMutationRemove,
		Action:    SkillMutationDelete,
		Scope:     target.Scope,
		Root:      target.Root,
		Target:    filepath.Join(target.Root, managed.Name),
		SkillName: managed.Name,
		Origin:    managed.Origin,
		DryRun:    dryRun,
	}
	if dryRun {
		return plan, nil
	}
	backup := filepath.Join(target.Root, fmt.Sprintf(".%s.mintclaw-remove-%d", managed.Name, time.Now().UnixNano()))
	if err := os.Rename(plan.Target, backup); err != nil {
		return plan, fmt.Errorf("stage skill removal: %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		if restoreErr := os.Rename(backup, plan.Target); restoreErr != nil {
			return plan, errors.Join(
				fmt.Errorf("remove skill %q: %w", managed.Name, err),
				fmt.Errorf("restore skill after failed removal: %w", restoreErr),
			)
		}
		return plan, fmt.Errorf("remove skill %q: %w", managed.Name, err)
	}
	plan.Applied = true
	return plan, nil
}

func (manager *ScopedSkillManager) Move(
	request SkillMoveRequest,
) (SkillMutationPlan, error) {
	if manager == nil {
		return SkillMutationPlan{}, errors.New("scoped skill manager is required")
	}
	if err := request.Source.Validate(); err != nil {
		return SkillMutationPlan{}, fmt.Errorf("validate source scope: %w", err)
	}
	if err := request.Target.Validate(); err != nil {
		return SkillMutationPlan{}, fmt.Errorf("validate target scope: %w", err)
	}
	managed, err := inspectInstalledSkill(request.Source, request.Name)
	if err != nil {
		return SkillMutationPlan{}, err
	}
	sourceDir := filepath.Join(request.Source.Root, managed.Name)
	targetDir := filepath.Join(request.Target.Root, managed.Name)
	if sourceDir == targetDir {
		return SkillMutationPlan{}, errors.New("source and target skill scopes resolve to the same path")
	}
	existing, err := inspectOptionalInstalledSkill(request.Target, managed.Name)
	if err != nil {
		return SkillMutationPlan{}, err
	}
	if existing != nil {
		return SkillMutationPlan{}, fmt.Errorf("skill %q already exists at %s", managed.Name, targetDir)
	}

	compatibility, gaps := manager.candidateCompatibility(
		request.Target,
		request.Source.Root,
		managed.Name,
		targetDir,
	)
	plan := SkillMutationPlan{
		Operation:      SkillMutationMove,
		Action:         SkillMutationRelocate,
		Scope:          request.Target.Scope,
		Root:           request.Target.Root,
		Target:         targetDir,
		SourceScope:    request.Source.Scope,
		SourceRoot:     request.Source.Root,
		Source:         sourceDir,
		SkillName:      managed.Name,
		Origin:         managed.Origin,
		Compatibility:  compatibility,
		DependencyGaps: gaps,
		DryRun:         request.DryRun,
	}
	if request.DryRun {
		return plan, nil
	}

	stageOwner, cleanup, err := prepareSkillStage(request.Target, false)
	if err != nil {
		return plan, err
	}
	defer cleanup()
	stageSkills := filepath.Join(stageOwner, "skills")
	if mkdirErr := os.Mkdir(stageSkills, 0o755); mkdirErr != nil {
		return plan, fmt.Errorf("create staged skill root: %w", mkdirErr)
	}
	stageDir := filepath.Join(stageSkills, managed.Name)
	if copyErr := copyManagedSkillTree(sourceDir, stageDir); copyErr != nil {
		return plan, fmt.Errorf("stage skill move: %w", copyErr)
	}
	staged, err := NewWorkspaceSkillInventory(stageOwner).Inspect(managed.Name)
	if err != nil {
		return plan, fmt.Errorf("inspect staged skill move: %w", err)
	}
	if !staged.Valid || staged.Revision != managed.Revision {
		return plan, errors.New("staged skill move does not match the validated source revision")
	}
	confirmed, err := inspectInstalledSkill(request.Source, managed.Name)
	if err != nil {
		return plan, fmt.Errorf("confirm skill before move: %w", err)
	}
	if confirmed.Revision != managed.Revision {
		return plan, errors.New("skill changed while preparing the move")
	}
	if err := manager.commitStagedSkill(request.Target, stageDir, targetDir, nil); err != nil {
		return plan, err
	}
	if err := removeMovedSkill(request.Source, managed, targetDir, manager.removeMovedSource); err != nil {
		return plan, err
	}
	plan.Applied = true
	return plan, nil
}

func (manager *ScopedSkillManager) install(
	ctx context.Context,
	request SkillInstallRequest,
	operation SkillMutationOperation,
	expectedName string,
	expectedOrigin *OriginMetadata,
) (SkillMutationPlan, error) {
	if manager == nil {
		return SkillMutationPlan{}, errors.New("scoped skill manager is required")
	}
	if err := request.Target.Validate(); err != nil {
		return SkillMutationPlan{}, err
	}
	registryName := strings.TrimSpace(request.Registry)
	if registryName == "" {
		registryName = "github"
	}
	if err := utils.ValidateSkillIdentifier(registryName); err != nil {
		return SkillMutationPlan{}, fmt.Errorf("invalid registry %q: %w", registryName, err)
	}
	registry := manager.registries.GetRegistry(registryName)
	if registry == nil {
		return SkillMutationPlan{}, fmt.Errorf("registry %q not found", registryName)
	}
	directory, err := registry.ResolveInstallDirName(request.Slug)
	if err != nil {
		return SkillMutationPlan{}, fmt.Errorf("invalid install target %q: %w", request.Slug, err)
	}
	if nameErr := ValidateSkillName(directory); nameErr != nil {
		return SkillMutationPlan{}, fmt.Errorf(
			"registry resolved invalid skill directory %q: %w",
			directory,
			nameErr,
		)
	}
	if expectedName != "" && directory != expectedName {
		return SkillMutationPlan{}, fmt.Errorf(
			"skill origin resolves to directory %q instead of installed name %q",
			directory,
			expectedName,
		)
	}

	targetDir := filepath.Join(request.Target.Root, directory)
	existing, err := inspectOptionalInstalledSkill(request.Target, directory)
	if err != nil {
		return SkillMutationPlan{}, err
	}
	action := SkillMutationCreate
	if existing != nil {
		action = SkillMutationReplace
		if !request.Replace {
			return SkillMutationPlan{}, fmt.Errorf(
				"skill %q already exists at %s; use update or explicit replacement",
				directory,
				targetDir,
			)
		}
		if existing.OriginKind != ManagedSkillOriginThirdParty || existing.Origin == nil {
			return SkillMutationPlan{}, fmt.Errorf(
				"skill %q cannot be replaced because immutable origin metadata is unavailable",
				directory,
			)
		}
		if expectedOrigin == nil {
			expectedOrigin = existing.Origin
		}
	}

	stageOwner, cleanup, err := prepareSkillStage(request.Target, request.DryRun)
	if err != nil {
		return SkillMutationPlan{}, err
	}
	defer cleanup()
	stageSkills := filepath.Join(stageOwner, "skills")
	if mkdirErr := os.Mkdir(stageSkills, 0o755); mkdirErr != nil {
		return SkillMutationPlan{}, fmt.Errorf("create staged skill root: %w", mkdirErr)
	}
	stageDir := filepath.Join(stageSkills, directory)
	result, err := registry.DownloadAndInstall(ctx, request.Slug, request.Version, stageDir)
	if err != nil {
		return SkillMutationPlan{}, fmt.Errorf("download skill %q: %w", request.Slug, err)
	}
	if result == nil {
		return SkillMutationPlan{}, errors.New("skill registry returned no install result")
	}
	if result.IsMalwareBlocked {
		return SkillMutationPlan{}, fmt.Errorf("skill %q is flagged as malicious and cannot be installed", request.Slug)
	}
	if validationErr := validateManagedInstall(stageOwner, directory, false); validationErr != nil {
		return SkillMutationPlan{}, fmt.Errorf(
			"registry archive for %q is not a valid skill: %w",
			request.Slug,
			validationErr,
		)
	}
	if originErr := WriteInstalledSkillOrigin(stageDir, registry, request.Slug, result.Version); originErr != nil {
		return SkillMutationPlan{}, fmt.Errorf("persist staged skill origin: %w", originErr)
	}
	if validationErr := validateManagedInstall(stageOwner, directory, true); validationErr != nil {
		return SkillMutationPlan{}, fmt.Errorf("validate staged skill origin: %w", validationErr)
	}
	origin, err := ReadSkillOrigin(stageDir)
	if err != nil {
		return SkillMutationPlan{}, err
	}
	if expectedOrigin != nil && !sameSkillOrigin(*expectedOrigin, origin) {
		return SkillMutationPlan{}, fmt.Errorf(
			"replacement would change immutable origin from %s:%s to %s:%s",
			expectedOrigin.Registry,
			expectedOrigin.Slug,
			origin.Registry,
			origin.Slug,
		)
	}

	compatibility, gaps := manager.candidateCompatibility(request.Target, stageSkills, directory, targetDir)
	plan := SkillMutationPlan{
		Operation:        operation,
		Action:           action,
		Scope:            request.Target.Scope,
		Root:             request.Target.Root,
		Target:           targetDir,
		SkillName:        directory,
		Registry:         registry.Name(),
		Slug:             origin.Slug,
		RequestedVersion: request.Version,
		ResolvedVersion:  result.Version,
		Origin:           &origin,
		Compatibility:    compatibility,
		DependencyGaps:   gaps,
		DryRun:           request.DryRun,
	}
	if result.IsSuspicious {
		plan.Warnings = append(plan.Warnings, "registry marked the skill as suspicious")
	}
	if request.DryRun {
		return plan, nil
	}
	if err := confirmInstallTargetUnchanged(request.Target, directory, existing); err != nil {
		return plan, err
	}
	if err := manager.commitStagedSkill(request.Target, stageDir, targetDir, existing); err != nil {
		return plan, err
	}
	plan.Applied = true
	return plan, nil
}

func confirmInstallTargetUnchanged(
	target SkillInstallTarget,
	name string,
	initial *ManagedSkill,
) error {
	current, err := inspectOptionalInstalledSkill(target, name)
	if err != nil {
		return fmt.Errorf("confirm skill before publication: %w", err)
	}
	if initial == nil {
		if current != nil {
			return fmt.Errorf("skill %q appeared while preparing installation; refusing to overwrite it", name)
		}
		return nil
	}
	if current == nil {
		return fmt.Errorf("skill %q disappeared while preparing replacement", name)
	}
	if current.Revision != initial.Revision {
		return fmt.Errorf("skill %q changed while preparing replacement", name)
	}
	if current.OriginKind != initial.OriginKind || (initial.Origin != nil &&
		(current.Origin == nil || !sameSkillOrigin(*initial.Origin, *current.Origin))) {
		return fmt.Errorf("skill %q origin changed while preparing replacement", name)
	}
	return nil
}

func inspectOptionalInstalledSkill(target SkillInstallTarget, name string) (*ManagedSkill, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	targetDir := filepath.Join(target.Root, name)
	info, err := os.Lstat(targetDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect installed skill %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("skill target %q must be a real directory", name)
	}
	managed, err := NewWorkspaceSkillInventory(target.OwnerRoot).Inspect(name)
	if err != nil {
		return nil, err
	}
	if !managed.Valid {
		return nil, errors.New(managed.ValidationErr)
	}
	return &managed, nil
}

func inspectInstalledSkill(target SkillInstallTarget, name string) (ManagedSkill, error) {
	name = strings.TrimSpace(strings.Trim(name, "/"))
	if err := ValidateSkillName(name); err != nil {
		return ManagedSkill{}, fmt.Errorf("invalid skill name %q: %w", name, err)
	}
	managed, err := inspectOptionalInstalledSkill(target, name)
	if err != nil {
		return ManagedSkill{}, err
	}
	if managed == nil {
		return ManagedSkill{}, fmt.Errorf("%w: %s", ErrManagedSkillNotFound, name)
	}
	return *managed, nil
}

func copyManagedSkillTree(source, destination string) error {
	if err := os.Mkdir(destination, 0o755); err != nil {
		return err
	}
	type directoryMode struct {
		path string
		mode fs.FileMode
	}
	directories := make([]directoryMode, 0)
	limits := DefaultManagedSkillLimits()
	entries := 0
	files := 0
	var totalBytes int64
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			directories = append(directories, directoryMode{path: destination, mode: info.Mode()})
			return nil
		}
		entries++
		if entries > limits.MaxEntries {
			return fmt.Errorf("skill tree exceeds %d entries", limits.MaxEntries)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || !validManagedRelativePath(filepath.ToSlash(relative), limits.MaxPath) {
			return fmt.Errorf("invalid skill-relative path %q", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill entry %q is a symlink", relative)
		}
		targetPath := filepath.Join(destination, relative)
		if info.IsDir() {
			if err := os.Mkdir(targetPath, 0o755); err != nil {
				return err
			}
			directories = append(directories, directoryMode{path: targetPath, mode: info.Mode()})
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill entry %q is not a regular file or directory", relative)
		}
		files++
		if files > limits.MaxFiles || info.Size() < 0 || info.Size() > limits.MaxBytes-totalBytes {
			return fmt.Errorf("skill tree exceeds bounded copy limits")
		}
		totalBytes += info.Size()
		if err := copyManagedSkillFile(path, targetPath, info, limits.MaxBytes); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Chmod(directories[index].path, directories[index].mode&managedSkillRevisionModeMask); err != nil {
			return err
		}
	}
	return nil
}

func copyManagedSkillFile(source, destination string, expected fs.FileInfo, maxBytes int64) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	opened, err := input.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return errors.New("skill file changed identity while preparing the move")
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, expected.Mode().Perm())
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maxBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != expected.Size() || written > maxBytes {
		return errors.New("skill file changed while preparing the move")
	}
	return os.Chmod(destination, expected.Mode()&managedSkillRevisionModeMask)
}

func removeMovedSkill(
	source SkillInstallTarget,
	managed ManagedSkill,
	publishedTarget string,
	removeSourceTree func(string) error,
) error {
	sourceDir := filepath.Join(source.Root, managed.Name)
	confirmed, err := inspectInstalledSkill(source, managed.Name)
	if err != nil || confirmed.Revision != managed.Revision {
		cleanupErr := os.RemoveAll(publishedTarget)
		if err != nil {
			return errors.Join(fmt.Errorf("confirm skill before source removal: %w", err), cleanupErr)
		}
		return errors.Join(errors.New("skill changed while completing the move"), cleanupErr)
	}
	backup := filepath.Join(source.Root, fmt.Sprintf(".%s.mintclaw-move-%d", managed.Name, time.Now().UnixNano()))
	if err := os.Rename(sourceDir, backup); err != nil {
		return errors.Join(fmt.Errorf("stage moved skill removal: %w", err), os.RemoveAll(publishedTarget))
	}
	if removeSourceTree == nil {
		removeSourceTree = os.RemoveAll
	}
	if err := removeSourceTree(backup); err != nil {
		return fmt.Errorf(
			"remove moved skill source: %w; complete destination retained at %s; incomplete source backup may remain at %s",
			err,
			publishedTarget,
			backup,
		)
	}
	return nil
}

func prepareSkillStage(target SkillInstallTarget, dryRun bool) (string, func(), error) {
	var parent string
	if dryRun {
		parent = os.TempDir()
	} else {
		if err := EnsureSkillInstallRoot(target); err != nil {
			return "", func() {}, err
		}
		parent = target.Root
	}
	rawStageOwner, err := os.MkdirTemp(parent, ".mintclaw-skill-stage-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create skill staging directory: %w", err)
	}
	stageOwner, err := filepath.EvalSymlinks(rawStageOwner)
	if err != nil {
		_ = os.RemoveAll(rawStageOwner)
		return "", func() {}, fmt.Errorf("resolve skill staging directory: %w", err)
	}
	return stageOwner, func() {
		if err := os.RemoveAll(stageOwner); err != nil {
			slog.Warn("failed to remove skill staging directory", "path", stageOwner, "error", err)
		}
	}, nil
}

func validateManagedInstall(ownerRoot, name string, requireOrigin bool) error {
	managed, err := NewWorkspaceSkillInventory(ownerRoot).Inspect(name)
	if err != nil {
		return err
	}
	if !managed.Valid {
		return errors.New(managed.ValidationErr)
	}
	if requireOrigin && managed.OriginKind != ManagedSkillOriginThirdParty {
		return errors.New("installed skill origin metadata is unavailable")
	}
	return nil
}

func sameSkillOrigin(left, right OriginMetadata) bool {
	return left.OriginKind == right.OriginKind && left.Registry == right.Registry && left.Slug == right.Slug
}

func (manager *ScopedSkillManager) candidateCompatibility(
	target SkillInstallTarget,
	stageRoot string,
	name string,
	targetDir string,
) ([]SkillInstallCompatibility, []SkillRequirementCheck) {
	root := SkillRoot{
		Path: stageRoot, Scope: SkillScope(target.Scope), Runtime: installScopeRuntime(target.Scope),
		Trust: installScopeTrust(target.Scope), Boundary: stageRoot,
	}
	compatibility := make([]SkillInstallCompatibility, 0, len(target.RuntimeProducts()))
	gaps := make([]SkillRequirementCheck, 0)
	for _, runtimeProduct := range target.RuntimeProducts() {
		loader := NewSkillsLoader([]SkillRoot{root})
		environment, ok := manager.environments[runtimeProduct]
		if !ok {
			environment = NewSkillCompatibilityEnvironment(runtimeProduct)
		}
		loader.WithCompatibilityEnvironment(environment)
		report := loader.Compatibility(runtimeProduct)
		item := SkillCompatibility{Name: name, Runtime: runtimeProduct, Status: SkillCompatibilityMalformed}
		for _, candidate := range report.Skills {
			if candidate.Name == name {
				item = candidate
				break
			}
		}
		item.Path = targetDir
		compatibility = append(compatibility, SkillInstallCompatibility{
			Runtime: runtimeProduct, Status: item.Status, Checks: item.Checks, Message: item.Message,
		})
		for _, check := range item.Checks {
			if check.State != SkillRequirementAvailable {
				gaps = append(gaps, check)
			}
		}
	}
	return compatibility, gaps
}

func installScopeRuntime(scope SkillInstallScope) SkillRuntime {
	switch scope {
	case SkillInstallScopeRepository:
		return SkillRuntimeCoding
	case SkillInstallScopeWorkspace:
		return SkillRuntimeGateway
	default:
		return SkillRuntimeShared
	}
}

func installScopeTrust(scope SkillInstallScope) SkillTrust {
	if scope == SkillInstallScopeUser {
		return SkillTrustUser
	}
	return SkillTrustProject
}

func (manager *ScopedSkillManager) commitStagedSkill(
	target SkillInstallTarget,
	stageDir string,
	targetDir string,
	initial *ManagedSkill,
) error {
	if err := target.Validate(); err != nil {
		return err
	}
	if initial == nil {
		if err := os.Rename(stageDir, targetDir); err != nil {
			return fmt.Errorf("publish staged skill: %w", err)
		}
		return nil
	}

	backupOwner, err := os.MkdirTemp(target.OwnerRoot, ".mintclaw-replacement-")
	if err != nil {
		return fmt.Errorf("create replacement backup owner: %w", err)
	}
	backupRoot := filepath.Join(backupOwner, "skills")
	if mkdirErr := os.Mkdir(backupRoot, 0o700); mkdirErr != nil {
		_ = os.RemoveAll(backupOwner)
		return fmt.Errorf("create replacement backup root: %w", mkdirErr)
	}
	backup := filepath.Join(backupRoot, initial.Name)
	if manager.beforeReplacementCommit != nil {
		manager.beforeReplacementCommit(targetDir)
	}
	if renameErr := os.Rename(targetDir, backup); renameErr != nil {
		_ = os.RemoveAll(backupOwner)
		return fmt.Errorf("stage previous skill for replacement: %w", renameErr)
	}
	backupSkill, err := NewWorkspaceSkillInventory(backupOwner).Inspect(initial.Name)
	if err == nil {
		err = confirmManagedSkillUnchanged(*initial, backupSkill)
	}
	if err != nil {
		return restoreReplacementBackup(
			backupOwner,
			backup,
			targetDir,
			fmt.Errorf("skill %q changed while publishing replacement: %w", initial.Name, err),
		)
	}
	if err := os.Rename(stageDir, targetDir); err != nil {
		return restoreReplacementBackup(
			backupOwner,
			backup,
			targetDir,
			fmt.Errorf("publish staged skill: %w", err),
		)
	}
	if err := os.RemoveAll(backupOwner); err != nil {
		slog.Warn("failed to remove replaced skill backup", "path", backupOwner, "error", err)
	}
	return nil
}

func confirmManagedSkillUnchanged(initial, current ManagedSkill) error {
	if !current.Valid {
		return fmt.Errorf("installed skill is no longer valid: %s", current.ValidationErr)
	}
	if current.Revision != initial.Revision {
		return errors.New("installed revision no longer matches")
	}
	if current.OriginKind != initial.OriginKind || (initial.Origin != nil &&
		(current.Origin == nil || !sameSkillOrigin(*initial.Origin, *current.Origin))) {
		return errors.New("immutable origin no longer matches")
	}
	return nil
}

func restoreReplacementBackup(backupOwner, backup, targetDir string, cause error) error {
	if restoreErr := os.Rename(backup, targetDir); restoreErr != nil {
		return errors.Join(cause, fmt.Errorf("restore previous skill from %s: %w", backup, restoreErr))
	}
	if cleanupErr := os.RemoveAll(backupOwner); cleanupErr != nil {
		slog.Warn("failed to remove restored skill backup owner", "path", backupOwner, "error", cleanupErr)
	}
	return cause
}
