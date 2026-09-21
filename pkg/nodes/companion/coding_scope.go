package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/coding/project"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

const (
	CodingWorkerProtocolV2       = 2
	CodingBranchPrefix           = "mintclaw"
	CodingCredentialSourceNative = "native"
	CodingProviderProfileDefault = "default"
	CodingCleanupRetain          = "retain"

	DefaultCodingTaskTimeout       = time.Hour
	DefaultCodingWorkerIdleTimeout = 5 * time.Minute
	DefaultCodingTaskRetention     = 7 * 24 * time.Hour
	DefaultCodingTaskConcurrency   = 1
	DefaultCodingEventBytes        = 64 << 10
	DefaultCodingResultBytes       = 128 << 10
	DefaultCodingArtifactCount     = 8
	DefaultCodingArtifactBytes     = int64(32 << 20)
	DefaultCodingArtifactsTotal    = int64(64 << 20)

	MaxCodingScopes            = 64
	MaxCodingTaskConcurrency   = 16
	MaxCodingEventBytes        = 512 << 10
	MaxCodingResultBytes       = 512 << 10
	MaxCodingArtifactCount     = 32
	MaxCodingArtifactBytes     = int64(1 << 30)
	MaxCodingArtifactsTotal    = int64(1 << 30)
	maxCodingTaskTimeout       = 24 * time.Hour
	maxCodingWorkerIdleTimeout = 30 * time.Minute
	codingScopeInspectTimeout  = 30 * time.Second
)

var (
	ErrCodingScopeNotFound = errors.New("coding scope alias not found")
	ErrCodingScopeStale    = errors.New("coding scope descriptor is stale")
	ErrCodingProfileDenied = errors.New("coding task profile is not allowed")
	ErrCodingScopeChanged  = errors.New("coding scope identity changed")
)

// CodingScopePolicy is operator-owned node-local authority. Paths,
// executables, providers, limits, and cleanup policy are never supplied by a
// gateway request or model.
type CodingScopePolicy struct {
	Revision                 string                `json:"revision"`
	Kind                     codingscope.Kind      `json:"kind"`
	SourceParent             string                `json:"source_parent"`
	Root                     string                `json:"root"`
	AllowedProfiles          []codingscope.Profile `json:"allowed_profiles"`
	WorkerExecutable         string                `json:"worker_executable"`
	WorkerProtocolVersion    int                   `json:"worker_protocol_version"`
	MintClawHome             string                `json:"mintclaw_home"`
	CredentialSource         string                `json:"credential_source"`
	ProviderProfile          string                `json:"provider_profile"`
	Model                    string                `json:"model"`
	Provider                 string                `json:"provider"`
	WorktreeParent           string                `json:"worktree_parent,omitempty"`
	BranchPrefix             string                `json:"branch_prefix,omitempty"`
	MaxConcurrentTasks       int                   `json:"max_concurrent_tasks,omitempty"`
	TaskTimeoutSeconds       int                   `json:"task_timeout_seconds,omitempty"`
	WorkerIdleTimeoutSeconds int                   `json:"worker_idle_timeout_seconds,omitempty"`
	EventBytesMax            int                   `json:"event_bytes_max,omitempty"`
	ResultBytesMax           int                   `json:"result_bytes_max,omitempty"`
	ArtifactCountMax         int                   `json:"artifact_count_max,omitempty"`
	ArtifactBytesMax         int64                 `json:"artifact_bytes_max,omitempty"`
	ArtifactsTotalBytesMax   int64                 `json:"artifacts_total_bytes_max,omitempty"`
	RetentionSeconds         int                   `json:"retention_seconds,omitempty"`
	CleanupPolicy            string                `json:"cleanup_policy,omitempty"`

	alias              string
	descriptorRevision string
	project            project.ProjectIdentity
	workerBuildID      string
	taskTimeout        time.Duration
	workerIdleTimeout  time.Duration
	retention          time.Duration
	sourceParentInfo   os.FileInfo
	rootInfo           os.FileInfo
	homeInfo           os.FileInfo
	worktreeParentInfo os.FileInfo
	workerInfo         os.FileInfo
}

// CodingScopeDescriptor is the bounded safe catalog projection. It never
// contains a filesystem path, executable, model, provider, or credential.
type CodingScopeDescriptor struct {
	Alias                    string                `json:"alias"`
	Revision                 string                `json:"revision"`
	Kind                     codingscope.Kind      `json:"kind"`
	AllowedProfiles          []codingscope.Profile `json:"allowed_profiles"`
	WorkerProtocolVersion    int                   `json:"worker_protocol_version"`
	MaxConcurrentTasks       int                   `json:"max_concurrent_tasks"`
	TaskTimeoutSeconds       int                   `json:"task_timeout_seconds"`
	WorkerIdleTimeoutSeconds int                   `json:"worker_idle_timeout_seconds"`
	EventBytesMax            int                   `json:"event_bytes_max"`
	ResultBytesMax           int                   `json:"result_bytes_max"`
	ArtifactCountMax         int                   `json:"artifact_count_max"`
	ArtifactBytesMax         int64                 `json:"artifact_bytes_max"`
	ArtifactsTotalBytesMax   int64                 `json:"artifacts_total_bytes_max"`
}

func (descriptor CodingScopeDescriptor) Validate() error {
	if !codingtask.ValidAlias(descriptor.Alias) || !validCodingDescriptorRevision(descriptor.Revision) ||
		!descriptor.Kind.Valid() ||
		descriptor.WorkerProtocolVersion != CodingWorkerProtocolV2 ||
		descriptor.MaxConcurrentTasks < 1 || descriptor.MaxConcurrentTasks > MaxCodingTaskConcurrency ||
		descriptor.TaskTimeoutSeconds < 1 ||
		descriptor.TaskTimeoutSeconds > int(maxCodingTaskTimeout/time.Second) ||
		descriptor.WorkerIdleTimeoutSeconds != int(DefaultCodingWorkerIdleTimeout/time.Second) ||
		descriptor.EventBytesMax < 1 || descriptor.EventBytesMax > MaxCodingEventBytes ||
		descriptor.ResultBytesMax < 1 || descriptor.ResultBytesMax > MaxCodingResultBytes ||
		descriptor.ArtifactCountMax < 1 || descriptor.ArtifactCountMax > MaxCodingArtifactCount ||
		descriptor.ArtifactBytesMax < 1 || descriptor.ArtifactBytesMax > MaxCodingArtifactBytes ||
		descriptor.ArtifactsTotalBytesMax < descriptor.ArtifactBytesMax ||
		descriptor.ArtifactsTotalBytesMax > MaxCodingArtifactsTotal {
		return errors.New("coding scope descriptor is invalid")
	}
	profiles, err := normalizeCodingProfiles(descriptor.Kind, descriptor.AllowedProfiles)
	if err != nil || len(profiles) != len(descriptor.AllowedProfiles) {
		return errors.New("coding scope descriptor profiles are invalid")
	}
	for index := range profiles {
		if profiles[index] != descriptor.AllowedProfiles[index] {
			return errors.New("coding scope descriptor profiles are not canonical")
		}
	}
	return nil
}

type CodingScopeCatalog struct {
	scopes map[string]CodingScopePolicy
}

func NewCodingScopeCatalog(scopes map[string]CodingScopePolicy) (*CodingScopeCatalog, error) {
	if scopes == nil {
		scopes = map[string]CodingScopePolicy{}
	}
	cloned := make(map[string]CodingScopePolicy, len(scopes))
	for alias, policy := range scopes {
		revision, revisionErr := codingScopeDescriptorRevision(policy)
		descriptor := codingScopeDescriptorForPolicy(alias, policy)
		if !codingtask.ValidAlias(alias) || policy.alias != alias || policy.project.ProjectRoot == "" ||
			policy.workerBuildID == "" || !validCodingDescriptorRevision(policy.descriptorRevision) ||
			policy.taskTimeout <= 0 || policy.workerIdleTimeout <= 0 || policy.retention <= 0 ||
			revisionErr != nil || revision != policy.descriptorRevision || descriptor.Validate() != nil {
			return nil, fmt.Errorf("invalid normalized coding scope %q", alias)
		}
		cloned[alias] = cloneCodingScopePolicy(policy)
	}
	if err := validateCodingScopeIsolation(cloned); err != nil {
		return nil, err
	}
	return &CodingScopeCatalog{scopes: cloned}, nil
}

func (catalog *CodingScopeCatalog) List() []CodingScopeDescriptor {
	if catalog == nil {
		return nil
	}
	descriptors := make([]CodingScopeDescriptor, 0, len(catalog.scopes))
	for alias, policy := range catalog.scopes {
		descriptors = append(descriptors, codingScopeDescriptorForPolicy(alias, policy))
	}
	sort.Slice(descriptors, func(left, right int) bool {
		return descriptors[left].Alias < descriptors[right].Alias
	})
	return descriptors
}

func codingScopeDescriptorForPolicy(alias string, policy CodingScopePolicy) CodingScopeDescriptor {
	return CodingScopeDescriptor{
		Alias: alias, Revision: policy.descriptorRevision, Kind: policy.Kind,
		AllowedProfiles:          append([]codingscope.Profile(nil), policy.AllowedProfiles...),
		WorkerProtocolVersion:    policy.WorkerProtocolVersion,
		MaxConcurrentTasks:       policy.MaxConcurrentTasks,
		TaskTimeoutSeconds:       policy.TaskTimeoutSeconds,
		WorkerIdleTimeoutSeconds: policy.WorkerIdleTimeoutSeconds,
		EventBytesMax:            policy.EventBytesMax,
		ResultBytesMax:           policy.ResultBytesMax,
		ArtifactCountMax:         policy.ArtifactCountMax,
		ArtifactBytesMax:         policy.ArtifactBytesMax,
		ArtifactsTotalBytesMax:   policy.ArtifactsTotalBytesMax,
	}
}

func (catalog *CodingScopeCatalog) resolve(
	ctx context.Context,
	alias string,
	revision string,
	profile codingscope.Profile,
) (CodingScopePolicy, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, codingScopeInspectTimeout)
	defer cancel()
	if catalog == nil {
		return CodingScopePolicy{}, ErrCodingScopeNotFound
	}
	policy, found := catalog.scopes[alias]
	if !found {
		return CodingScopePolicy{}, ErrCodingScopeNotFound
	}
	if revision != policy.descriptorRevision {
		return CodingScopePolicy{}, ErrCodingScopeStale
	}
	if !profileAllowed(policy.AllowedProfiles, profile) {
		return CodingScopePolicy{}, ErrCodingProfileDenied
	}
	if err := revalidateCodingPolicyPaths(policy); err != nil {
		return CodingScopePolicy{}, err
	}
	current, err := resolveCodingScope(ctx, policy.Root, profile)
	if err != nil {
		return CodingScopePolicy{}, err
	}
	if current != policy.project {
		return CodingScopePolicy{}, ErrCodingScopeChanged
	}
	buildID, err := codingtask.ExecutableBuildID(policy.WorkerExecutable)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("inspect coding worker executable: %w", err)
	}
	if buildID != policy.workerBuildID {
		return CodingScopePolicy{}, ErrCodingScopeChanged
	}
	return cloneCodingScopePolicy(policy), nil
}

func normalizeCodingScopes(
	scopes map[string]CodingScopePolicy,
	baseDir string,
) (map[string]CodingScopePolicy, error) {
	if len(scopes) > MaxCodingScopes {
		return nil, fmt.Errorf("coding scope count exceeds %d", MaxCodingScopes)
	}
	if len(scopes) == 0 {
		return map[string]CodingScopePolicy{}, nil
	}
	if !codingPlatformSupported(runtime.GOOS) {
		return nil, fmt.Errorf("coding scopes are unsupported on %s", runtime.GOOS)
	}
	normalized := make(map[string]CodingScopePolicy, len(scopes))
	for alias, input := range scopes {
		if !codingtask.ValidAlias(alias) {
			return nil, fmt.Errorf("invalid coding scope alias %q", alias)
		}
		policy, err := normalizeCodingScopePolicy(alias, input, baseDir)
		if err != nil {
			return nil, err
		}
		normalized[alias] = policy
	}
	if err := validateCodingScopeIsolation(normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func normalizeCodingScopePolicy(
	alias string,
	policy CodingScopePolicy,
	baseDir string,
) (CodingScopePolicy, error) {
	if !codingtask.ValidRevision(policy.Revision) {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q has an invalid revision", alias)
	}
	if policy.Kind != codingscope.KindGitProject {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q has an unadmitted kind %q", alias, policy.Kind)
	}
	policy.alias = alias
	policy.descriptorRevision = ""
	policy.project = project.ProjectIdentity{}
	policy.workerBuildID = ""
	policy.taskTimeout = 0
	policy.workerIdleTimeout = 0
	policy.retention = 0
	policy.sourceParentInfo = nil
	policy.rootInfo = nil
	policy.homeInfo = nil
	policy.worktreeParentInfo = nil
	policy.workerInfo = nil
	if policy.WorkerProtocolVersion != CodingWorkerProtocolV2 {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q has an unsupported worker protocol", alias)
	}
	if policy.CredentialSource != CodingCredentialSourceNative {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q has an unsupported credential source", alias)
	}

	var err error
	policy.SourceParent, policy.sourceParentInfo, err = resolveRequiredCodingDirectory(
		baseDir,
		policy.SourceParent,
		"source parent",
	)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	policy.Root, policy.rootInfo, err = resolveRequiredCodingDirectory(baseDir, policy.Root, "scope root")
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	if !properPathWithin(policy.SourceParent, policy.Root) {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q root is outside its source parent", alias)
	}

	policy.AllowedProfiles, err = normalizeCodingProfiles(policy.Kind, policy.AllowedProfiles)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	inspectCtx, cancelInspect := context.WithTimeout(context.Background(), codingScopeInspectTimeout)
	resolvedProject, err := resolveCodingScope(
		inspectCtx,
		policy.Root,
		strongestCodingProfile(policy.AllowedProfiles),
	)
	cancelInspect()
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	policy.project = resolvedProject

	workerExecutable, pathErr := resolveConfigPath(baseDir, policy.WorkerExecutable)
	if pathErr != nil || strings.TrimSpace(policy.WorkerExecutable) == "" ||
		!validCodingConfiguredPath(workerExecutable) {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q requires a worker executable", alias)
	}
	workerInfo, statErr := os.Lstat(workerExecutable)
	if statErr != nil || !validCodingWorkerFile(workerInfo) {
		return CodingScopePolicy{}, fmt.Errorf(
			"coding scope %q worker executable is not a direct executable file",
			alias,
		)
	}
	workerExecutable, err = filepath.EvalSymlinks(workerExecutable)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q resolve worker executable: %w", alias, err)
	}
	workerExecutable = filepath.Clean(workerExecutable)
	if !validCodingConfiguredPath(workerExecutable) {
		return CodingScopePolicy{}, fmt.Errorf(
			"coding scope %q worker executable resolves to an unsupported path",
			alias,
		)
	}
	policy.WorkerExecutable = workerExecutable
	policy.workerInfo, err = os.Stat(workerExecutable)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q inspect worker executable: %w", alias, err)
	}
	if !validCodingWorkerFile(policy.workerInfo) || !os.SameFile(workerInfo, policy.workerInfo) {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q worker executable identity changed", alias)
	}
	policy.workerBuildID, err = codingtask.ExecutableBuildID(workerExecutable)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q worker executable: %w", alias, err)
	}

	policy.MintClawHome, policy.homeInfo, err = resolveRequiredCodingDirectory(
		baseDir,
		policy.MintClawHome,
		"MintClaw home",
	)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	if pathsOverlapSimple(policy.Root, policy.MintClawHome) {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q root overlaps MintClaw home", alias)
	}

	// Native credentials stay in MintClawHome and are proven by worker
	// initialization before any prompt is sent; this catalog never reads or
	// projects credential material.
	if !codingtask.ValidIdentifier(policy.ProviderProfile) ||
		policy.ProviderProfile != CodingProviderProfileDefault ||
		!codingtask.ValidIdentifier(policy.Provider) || !validCodingModel(policy.Model) {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q has an invalid native provider policy", alias)
	}

	if profileAllowed(policy.AllowedProfiles, codingscope.ProfileMutate) {
		if policy.BranchPrefix != CodingBranchPrefix {
			return CodingScopePolicy{}, fmt.Errorf("coding scope %q has an unsupported branch prefix", alias)
		}
		policy.WorktreeParent, policy.worktreeParentInfo, err = resolveRequiredCodingDirectory(
			baseDir,
			policy.WorktreeParent,
			"worktree parent",
		)
		if err != nil {
			return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
		}
		if pathsOverlapSimple(policy.WorktreeParent, policy.Root) ||
			pathsOverlapSimple(policy.WorktreeParent, filepath.Join(policy.MintClawHome, "coding")) {
			return CodingScopePolicy{}, fmt.Errorf(
				"coding scope %q worktree parent overlaps protected state",
				alias,
			)
		}
	} else if strings.TrimSpace(policy.WorktreeParent) != "" || strings.TrimSpace(policy.BranchPrefix) != "" {
		return CodingScopePolicy{}, fmt.Errorf(
			"coding scope %q cannot configure worktree authority without a worktree profile",
			alias,
		)
	}

	if policy.MaxConcurrentTasks == 0 {
		policy.MaxConcurrentTasks = DefaultCodingTaskConcurrency
	}
	if policy.MaxConcurrentTasks < 1 || policy.MaxConcurrentTasks > MaxCodingTaskConcurrency {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q has invalid task concurrency", alias)
	}
	policy.taskTimeout, err = codingPolicyDuration(
		policy.TaskTimeoutSeconds,
		DefaultCodingTaskTimeout,
		maxCodingTaskTimeout,
		"task timeout",
	)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	policy.TaskTimeoutSeconds = int(policy.taskTimeout / time.Second)
	policy.workerIdleTimeout, err = codingPolicyDuration(
		policy.WorkerIdleTimeoutSeconds,
		DefaultCodingWorkerIdleTimeout,
		maxCodingWorkerIdleTimeout,
		"worker idle timeout",
	)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	policy.WorkerIdleTimeoutSeconds = int(policy.workerIdleTimeout / time.Second)
	if policy.workerIdleTimeout != DefaultCodingWorkerIdleTimeout {
		return CodingScopePolicy{}, fmt.Errorf(
			"coding scope %q worker idle timeout is not supported by protocol v2",
			alias,
		)
	}
	if policy.EventBytesMax, err = codingPolicyBound(
		policy.EventBytesMax,
		DefaultCodingEventBytes,
		MaxCodingEventBytes,
		"event bytes",
	); err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	if policy.ResultBytesMax, err = codingPolicyBound(
		policy.ResultBytesMax,
		DefaultCodingResultBytes,
		MaxCodingResultBytes,
		"result bytes",
	); err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	if policy.ArtifactCountMax, err = codingPolicyBound(
		policy.ArtifactCountMax,
		DefaultCodingArtifactCount,
		MaxCodingArtifactCount,
		"artifact count",
	); err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	if policy.ArtifactBytesMax, err = codingPolicyBound64(
		policy.ArtifactBytesMax,
		DefaultCodingArtifactBytes,
		MaxCodingArtifactBytes,
		"artifact bytes",
	); err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	if policy.ArtifactsTotalBytesMax, err = codingPolicyBound64(
		policy.ArtifactsTotalBytesMax,
		DefaultCodingArtifactsTotal,
		MaxCodingArtifactsTotal,
		"artifacts total bytes",
	); err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	if policy.ArtifactsTotalBytesMax < policy.ArtifactBytesMax {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q has invalid aggregate artifact bytes", alias)
	}
	policy.retention, err = codingPolicyDuration(
		policy.RetentionSeconds,
		DefaultCodingTaskRetention,
		codingtask.MaxRetainDuration,
		"retention",
	)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q: %w", alias, err)
	}
	policy.RetentionSeconds = int(policy.retention / time.Second)
	if policy.CleanupPolicy == "" {
		policy.CleanupPolicy = CodingCleanupRetain
	}
	if policy.CleanupPolicy != CodingCleanupRetain {
		return CodingScopePolicy{}, fmt.Errorf(
			"coding scope %q cleanup policy is not admitted by the first slice",
			alias,
		)
	}
	if codingPolicyHasAliasedAuthority(policy) {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q has aliased filesystem authority", alias)
	}
	policy.descriptorRevision, err = codingScopeDescriptorRevision(policy)
	if err != nil {
		return CodingScopePolicy{}, fmt.Errorf("coding scope %q descriptor revision: %w", alias, err)
	}
	return cloneCodingScopePolicy(policy), nil
}

func resolveCodingScope(
	ctx context.Context,
	root string,
	profile codingscope.Profile,
) (project.ProjectIdentity, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	info, err := os.Lstat(root)
	if err != nil || !validCodingDirectory(info) {
		return project.ProjectIdentity{}, errors.New("configured project root is not a direct directory")
	}
	identity, err := project.ResolveProject(ctx, root)
	if err != nil {
		return project.ProjectIdentity{}, fmt.Errorf("resolve project identity: %w", err)
	}
	if identity.ProjectRoot != root || identity.InvocationCWD != root {
		return project.ProjectIdentity{}, errors.New("configured root must be the canonical project root")
	}
	if profile.UsesIsolatedWorktree() {
		if identity.Kind != project.ProjectKindGitWorktree || identity.GitHead == "" || identity.GitBranch == "" {
			return project.ProjectIdentity{}, errors.New(
				"mutation requires a checked-out Git branch with committed HEAD",
			)
		}
		if identity.GitDir != identity.GitCommonDir {
			return project.ProjectIdentity{}, errors.New("mutation source cannot itself be a linked worktree")
		}
		superproject, inspectErr := runCodingGitText(ctx, root, "rev-parse", "--show-superproject-working-tree")
		if inspectErr != nil {
			return project.ProjectIdentity{}, fmt.Errorf("inspect mutation source topology: %w", inspectErr)
		}
		if superproject != "" {
			return project.ProjectIdentity{}, errors.New("mutation source cannot be a Git submodule")
		}
		if err := requireCleanCodingScope(ctx, root); err != nil {
			return project.ProjectIdentity{}, err
		}
	}
	return identity, nil
}

func requireCleanCodingScope(ctx context.Context, root string) error {
	command := exec.CommandContext(
		ctx,
		"git",
		"-c",
		"core.fsmonitor=false",
		"-C",
		root,
		"status",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
	)
	command.Env = sanitizedCodingGitEnvironment()
	output := &presenceWriter{}
	command.Stdout = output
	err := command.Run()
	if err != nil {
		return fmt.Errorf("inspect mutation source status: %w", err)
	}
	if output.present {
		return errors.New("mutation source checkout is dirty")
	}
	return nil
}

type presenceWriter struct {
	present bool
}

func (writer *presenceWriter) Write(value []byte) (int, error) {
	writer.present = writer.present || len(value) > 0
	return len(value), nil
}

type boundedCodingGitWriter struct {
	value     strings.Builder
	remaining int
	truncated bool
}

func (writer *boundedCodingGitWriter) Write(value []byte) (int, error) {
	written := len(value)
	if writer.remaining > 0 {
		keep := min(writer.remaining, len(value))
		_, _ = writer.value.Write(value[:keep])
		writer.remaining -= keep
		writer.truncated = keep != len(value)
	} else if len(value) != 0 {
		writer.truncated = true
	}
	return written, nil
}

func runCodingGitText(ctx context.Context, root string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	command.Env = sanitizedCodingGitEnvironment()
	output := &boundedCodingGitWriter{remaining: codingtask.MaxPathBytes}
	command.Stdout = output
	if err := command.Run(); err != nil {
		return "", err
	}
	if output.truncated {
		return "", errors.New("git output exceeds the coding scope bound")
	}
	return strings.TrimSpace(output.value.String()), nil
}

func sanitizedCodingGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upperKey := strings.ToUpper(key)
		if strings.HasPrefix(upperKey, "GIT_") || upperKey == "LC_ALL" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(
		environment,
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_LITERAL_PATHSPECS=1",
	)
}

func revalidateCodingPolicyPaths(policy CodingScopePolicy) error {
	checks := []struct {
		path       string
		info       os.FileInfo
		directory  bool
		executable bool
	}{
		{path: policy.SourceParent, info: policy.sourceParentInfo, directory: true},
		{path: policy.Root, info: policy.rootInfo, directory: true},
		{path: policy.MintClawHome, info: policy.homeInfo, directory: true},
		{path: policy.WorkerExecutable, info: policy.workerInfo, executable: true},
	}
	if policy.WorktreeParent != "" {
		checks = append(checks, struct {
			path       string
			info       os.FileInfo
			directory  bool
			executable bool
		}{path: policy.WorktreeParent, info: policy.worktreeParentInfo, directory: true})
	}
	for _, check := range checks {
		if !revalidateCodingPolicyPath(
			check.path,
			check.info,
			check.directory,
			check.executable,
		) {
			return ErrCodingScopeChanged
		}
	}
	if !properPathWithin(policy.SourceParent, policy.Root) || pathsOverlapSimple(policy.Root, policy.MintClawHome) ||
		(policy.WorktreeParent != "" && (pathsOverlapSimple(policy.WorktreeParent, policy.Root) ||
			pathsOverlapSimple(policy.WorktreeParent, filepath.Join(policy.MintClawHome, "coding")))) {
		return ErrCodingScopeChanged
	}
	return nil
}

func revalidateCodingPolicyPath(
	path string,
	expected os.FileInfo,
	directory bool,
	executable bool,
) bool {
	before, err := os.Lstat(path)
	if err != nil || expected == nil || !os.SameFile(expected, before) ||
		directory && !validCodingDirectory(before) ||
		executable && !validCodingWorkerFile(before) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(resolved) != path {
		return false
	}
	after, err := os.Lstat(path)
	return err == nil && os.SameFile(before, after) && os.SameFile(expected, after) &&
		(!directory || validCodingDirectory(after)) &&
		(!executable || validCodingWorkerFile(after))
}

func normalizeCodingProfiles(
	kind codingscope.Kind,
	profiles []codingscope.Profile,
) ([]codingscope.Profile, error) {
	if len(profiles) == 0 || len(profiles) > 2 {
		return nil, errors.New("allowed profiles must contain investigate and/or mutate")
	}
	seen := make(map[codingscope.Profile]struct{}, len(profiles))
	for _, profile := range profiles {
		if !profile.AllowedFor(kind) ||
			(profile != codingscope.ProfileInvestigate && profile != codingscope.ProfileMutate) {
			return nil, errors.New("allowed profiles contain an unadmitted profile")
		}
		if _, duplicate := seen[profile]; duplicate {
			return nil, errors.New("allowed profiles contain a duplicate")
		}
		seen[profile] = struct{}{}
	}
	result := make([]codingscope.Profile, 0, len(seen))
	for _, profile := range []codingscope.Profile{
		codingscope.ProfileInvestigate,
		codingscope.ProfileMutate,
	} {
		if _, found := seen[profile]; found {
			result = append(result, profile)
		}
	}
	return result, nil
}

func strongestCodingProfile(profiles []codingscope.Profile) codingscope.Profile {
	if profileAllowed(profiles, codingscope.ProfileMutate) {
		return codingscope.ProfileMutate
	}
	return codingscope.ProfileInvestigate
}

func profileAllowed(profiles []codingscope.Profile, wanted codingscope.Profile) bool {
	for _, profile := range profiles {
		if profile == wanted {
			return true
		}
	}
	return false
}

func canonicalConfiguredDirectory(path string, label string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !validCodingDirectory(info) || !validCodingConfiguredPath(path) {
		return "", fmt.Errorf("%s must be an existing direct directory", label)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	resolved = filepath.Clean(resolved)
	if !validCodingConfiguredPath(resolved) {
		return "", fmt.Errorf("%s resolves to an unsupported path", label)
	}
	resolvedInfo, err := os.Lstat(resolved)
	if err != nil || !validCodingDirectory(resolvedInfo) || !os.SameFile(info, resolvedInfo) {
		return "", fmt.Errorf("%s identity changed while resolving", label)
	}
	return resolved, nil
}

func resolveRequiredCodingDirectory(
	baseDir string,
	configured string,
	label string,
) (string, os.FileInfo, error) {
	if strings.TrimSpace(configured) == "" {
		return "", nil, fmt.Errorf("%s is required", label)
	}
	path, err := resolveConfigPath(baseDir, configured)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s: %w", label, err)
	}
	path, err = canonicalConfiguredDirectory(path, label)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if !validCodingDirectory(info) {
		return "", nil, fmt.Errorf("%s is not a direct directory", label)
	}
	return path, info, nil
}

func validCodingDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func validCodingWorkerFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 &&
		(runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0)
}

func validCodingConfiguredPath(path string) bool {
	if path == "" || len(path) > codingtask.MaxPathBytes || !utf8.ValidString(path) ||
		path != strings.TrimSpace(path) || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	slashed := strings.ReplaceAll(path, `\`, "/")
	if strings.HasPrefix(slashed, "//?/") || strings.HasPrefix(slashed, "//./") ||
		strings.HasPrefix(slashed, "/??/") || strings.HasPrefix(slashed, "//??/") {
		return false
	}
	for _, component := range strings.Split(slashed, "/") {
		if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return false
		}
	}
	for _, character := range path {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func properPathWithin(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && filepath.IsLocal(relative)
}

func validCodingModel(model string) bool {
	if model == "" || len(model) > codingtask.MaxModelIDBytes || !utf8.ValidString(model) ||
		model != strings.TrimSpace(model) {
		return false
	}
	for _, character := range model {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func codingPlatformSupported(goos string) bool {
	return goos == "darwin" || goos == "linux"
}

func validateCodingScopeIsolation(scopes map[string]CodingScopePolicy) error {
	aliases := make([]string, 0, len(scopes))
	revisions := make(map[string]string, len(scopes))
	for alias, policy := range scopes {
		aliases = append(aliases, alias)
		if previous, duplicate := revisions[policy.descriptorRevision]; duplicate {
			return fmt.Errorf("coding scopes %q and %q have the same descriptor revision", previous, alias)
		}
		revisions[policy.descriptorRevision] = alias
	}
	sort.Strings(aliases)
	for index, leftAlias := range aliases {
		left := scopes[leftAlias]
		for _, rightAlias := range aliases[index+1:] {
			right := scopes[rightAlias]
			if codingScopeAuthoritiesOverlap(left, right) {
				return fmt.Errorf(
					"coding scopes %q and %q have overlapping repository authority",
					leftAlias,
					rightAlias,
				)
			}
		}
	}
	return nil
}

func codingScopeAuthoritiesOverlap(left CodingScopePolicy, right CodingScopePolicy) bool {
	if sameCodingFile(left.rootInfo, right.rootInfo) || pathsOverlapSimple(left.Root, right.Root) ||
		pathsOverlapSimple(left.Root, right.MintClawHome) ||
		pathsOverlapSimple(right.Root, left.MintClawHome) ||
		(left.WorktreeParent != "" && right.WorktreeParent != "" &&
			(sameCodingFile(left.worktreeParentInfo, right.worktreeParentInfo) ||
				pathsOverlapSimple(left.WorktreeParent, right.WorktreeParent))) {
		return true
	}
	for _, pair := range []struct {
		worktreePath string
		worktreeInfo os.FileInfo
		rootPath     string
		rootInfo     os.FileInfo
		home         string
	}{
		{left.WorktreeParent, left.worktreeParentInfo, right.Root, right.rootInfo, right.MintClawHome},
		{right.WorktreeParent, right.worktreeParentInfo, left.Root, left.rootInfo, left.MintClawHome},
	} {
		if pair.worktreePath != "" && (sameCodingFile(pair.worktreeInfo, pair.rootInfo) ||
			pathsOverlapSimple(pair.worktreePath, pair.rootPath) ||
			pathsOverlapSimple(pair.worktreePath, filepath.Join(pair.home, "coding"))) {
			return true
		}
	}
	return false
}

func sameCodingFile(left os.FileInfo, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right)
}

func codingPolicyHasAliasedAuthority(policy CodingScopePolicy) bool {
	identities := []os.FileInfo{
		policy.sourceParentInfo,
		policy.rootInfo,
		policy.homeInfo,
		policy.worktreeParentInfo,
		policy.workerInfo,
	}
	for index, left := range identities {
		if left == nil {
			continue
		}
		for _, right := range identities[index+1:] {
			if right != nil && os.SameFile(left, right) {
				return true
			}
		}
	}
	return false
}

func codingScopeDescriptorRevision(policy CodingScopePolicy) (string, error) {
	payload := struct {
		Alias         string                  `json:"alias"`
		Policy        CodingScopePolicy       `json:"policy"`
		Project       project.ProjectIdentity `json:"project"`
		WorkerBuildID string                  `json:"worker_build_id"`
	}{
		Alias: policy.alias, Policy: policy, Project: policy.project, WorkerBuildID: policy.workerBuildID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validCodingDescriptorRevision(revision string) bool {
	decoded, err := hex.DecodeString(revision)
	return err == nil && len(decoded) == sha256.Size && revision == strings.ToLower(revision)
}

func pathsOverlapSimple(left string, right string) bool {
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && (relative == "." || filepath.IsLocal(relative) && relative != "..") {
			return true
		}
	}
	return false
}

func codingPolicyDuration(
	value int,
	fallback time.Duration,
	maximum time.Duration,
	label string,
) (time.Duration, error) {
	if value == 0 {
		return fallback, nil
	}
	maximumSeconds := int(maximum / time.Second)
	if value < 1 || value > maximumSeconds {
		return 0, fmt.Errorf("%s must be between 1 second and %s", label, maximum)
	}
	return time.Duration(value) * time.Second, nil
}

func codingPolicyBound(value int, fallback int, maximum int, label string) (int, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 1 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", label, maximum)
	}
	return value, nil
}

func codingPolicyBound64(value int64, fallback int64, maximum int64, label string) (int64, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 1 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", label, maximum)
	}
	return value, nil
}

func cloneCodingScopePolicy(policy CodingScopePolicy) CodingScopePolicy {
	policy.AllowedProfiles = append([]codingscope.Profile(nil), policy.AllowedProfiles...)
	return policy
}
