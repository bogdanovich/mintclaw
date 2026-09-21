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
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

const (
	CodingWorkerProtocolV1       = 1
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

	MaxCodingProjects           = 64
	MaxCodingTaskConcurrency    = 16
	MaxCodingEventBytes         = 512 << 10
	MaxCodingResultBytes        = 512 << 10
	MaxCodingArtifactCount      = 32
	MaxCodingArtifactBytes      = int64(1 << 30)
	MaxCodingArtifactsTotal     = int64(1 << 30)
	maxCodingTaskTimeout        = 24 * time.Hour
	maxCodingWorkerIdleTimeout  = 30 * time.Minute
	codingProjectInspectTimeout = 30 * time.Second
)

var (
	ErrCodingProjectNotFound = errors.New("coding project alias not found")
	ErrCodingProjectStale    = errors.New("coding project descriptor is stale")
	ErrCodingModeDenied      = errors.New("coding task mode is not allowed")
	ErrCodingProjectChanged  = errors.New("coding project identity changed")
)

// CodingProjectPolicy is operator-owned node-local authority. Paths,
// executables, providers, limits, and cleanup policy are never supplied by a
// gateway request or model.
type CodingProjectPolicy struct {
	Revision                 string                `json:"revision"`
	SourceParent             string                `json:"source_parent"`
	Root                     string                `json:"root"`
	AllowedModes             []codingtask.TaskMode `json:"allowed_modes"`
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

// CodingProjectDescriptor is the bounded safe catalog projection. It never
// contains a filesystem path, executable, model, provider, or credential.
type CodingProjectDescriptor struct {
	Alias                    string                `json:"alias"`
	Revision                 string                `json:"revision"`
	AllowedModes             []codingtask.TaskMode `json:"allowed_modes"`
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

func (descriptor CodingProjectDescriptor) Validate() error {
	if !codingtask.ValidAlias(descriptor.Alias) || !validCodingDescriptorRevision(descriptor.Revision) ||
		descriptor.WorkerProtocolVersion != CodingWorkerProtocolV1 ||
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
		return errors.New("coding project descriptor is invalid")
	}
	modes, err := normalizeCodingModes(descriptor.AllowedModes)
	if err != nil || len(modes) != len(descriptor.AllowedModes) {
		return errors.New("coding project descriptor modes are invalid")
	}
	for index := range modes {
		if modes[index] != descriptor.AllowedModes[index] {
			return errors.New("coding project descriptor modes are not canonical")
		}
	}
	return nil
}

type CodingProjectCatalog struct {
	projects map[string]CodingProjectPolicy
}

func NewCodingProjectCatalog(projects map[string]CodingProjectPolicy) (*CodingProjectCatalog, error) {
	if projects == nil {
		projects = map[string]CodingProjectPolicy{}
	}
	cloned := make(map[string]CodingProjectPolicy, len(projects))
	for alias, policy := range projects {
		revision, revisionErr := codingProjectDescriptorRevision(policy)
		descriptor := codingProjectDescriptorForPolicy(alias, policy)
		if !codingtask.ValidAlias(alias) || policy.alias != alias || policy.project.ProjectRoot == "" ||
			policy.workerBuildID == "" || !validCodingDescriptorRevision(policy.descriptorRevision) ||
			policy.taskTimeout <= 0 || policy.workerIdleTimeout <= 0 || policy.retention <= 0 ||
			revisionErr != nil || revision != policy.descriptorRevision || descriptor.Validate() != nil {
			return nil, fmt.Errorf("invalid normalized coding project %q", alias)
		}
		cloned[alias] = cloneCodingProjectPolicy(policy)
	}
	if err := validateCodingProjectIsolation(cloned); err != nil {
		return nil, err
	}
	return &CodingProjectCatalog{projects: cloned}, nil
}

func (catalog *CodingProjectCatalog) List() []CodingProjectDescriptor {
	if catalog == nil {
		return nil
	}
	descriptors := make([]CodingProjectDescriptor, 0, len(catalog.projects))
	for alias, policy := range catalog.projects {
		descriptors = append(descriptors, codingProjectDescriptorForPolicy(alias, policy))
	}
	sort.Slice(descriptors, func(left, right int) bool {
		return descriptors[left].Alias < descriptors[right].Alias
	})
	return descriptors
}

func codingProjectDescriptorForPolicy(alias string, policy CodingProjectPolicy) CodingProjectDescriptor {
	return CodingProjectDescriptor{
		Alias: alias, Revision: policy.descriptorRevision,
		AllowedModes:             append([]codingtask.TaskMode(nil), policy.AllowedModes...),
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

func (catalog *CodingProjectCatalog) resolve(
	ctx context.Context,
	alias string,
	revision string,
	mode codingtask.TaskMode,
) (CodingProjectPolicy, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, codingProjectInspectTimeout)
	defer cancel()
	if catalog == nil {
		return CodingProjectPolicy{}, ErrCodingProjectNotFound
	}
	policy, found := catalog.projects[alias]
	if !found {
		return CodingProjectPolicy{}, ErrCodingProjectNotFound
	}
	if revision != policy.descriptorRevision {
		return CodingProjectPolicy{}, ErrCodingProjectStale
	}
	if !modeAllowed(policy.AllowedModes, mode) {
		return CodingProjectPolicy{}, ErrCodingModeDenied
	}
	if err := revalidateCodingPolicyPaths(policy); err != nil {
		return CodingProjectPolicy{}, err
	}
	current, err := resolveCodingProject(ctx, policy.Root, mode)
	if err != nil {
		return CodingProjectPolicy{}, err
	}
	if current != policy.project {
		return CodingProjectPolicy{}, ErrCodingProjectChanged
	}
	buildID, err := codingtask.ExecutableBuildID(policy.WorkerExecutable)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("inspect coding worker executable: %w", err)
	}
	if buildID != policy.workerBuildID {
		return CodingProjectPolicy{}, ErrCodingProjectChanged
	}
	return cloneCodingProjectPolicy(policy), nil
}

func normalizeCodingProjects(
	projects map[string]CodingProjectPolicy,
	baseDir string,
) (map[string]CodingProjectPolicy, error) {
	if len(projects) > MaxCodingProjects {
		return nil, fmt.Errorf("coding project count exceeds %d", MaxCodingProjects)
	}
	if len(projects) == 0 {
		return map[string]CodingProjectPolicy{}, nil
	}
	if !codingPlatformSupported(runtime.GOOS) {
		return nil, fmt.Errorf("coding projects are unsupported on %s", runtime.GOOS)
	}
	normalized := make(map[string]CodingProjectPolicy, len(projects))
	for alias, input := range projects {
		if !codingtask.ValidAlias(alias) {
			return nil, fmt.Errorf("invalid coding project alias %q", alias)
		}
		policy, err := normalizeCodingProjectPolicy(alias, input, baseDir)
		if err != nil {
			return nil, err
		}
		normalized[alias] = policy
	}
	if err := validateCodingProjectIsolation(normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func normalizeCodingProjectPolicy(
	alias string,
	policy CodingProjectPolicy,
	baseDir string,
) (CodingProjectPolicy, error) {
	if !codingtask.ValidRevision(policy.Revision) {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q has an invalid revision", alias)
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
	if policy.WorkerProtocolVersion != CodingWorkerProtocolV1 {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q has an unsupported worker protocol", alias)
	}
	if policy.CredentialSource != CodingCredentialSourceNative {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q has an unsupported credential source", alias)
	}

	var err error
	policy.SourceParent, policy.sourceParentInfo, err = resolveRequiredCodingDirectory(
		baseDir,
		policy.SourceParent,
		"source parent",
	)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	policy.Root, policy.rootInfo, err = resolveRequiredCodingDirectory(baseDir, policy.Root, "project root")
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	if !properPathWithin(policy.SourceParent, policy.Root) {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q root is outside its source parent", alias)
	}

	policy.AllowedModes, err = normalizeCodingModes(policy.AllowedModes)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	inspectCtx, cancelInspect := context.WithTimeout(context.Background(), codingProjectInspectTimeout)
	resolvedProject, err := resolveCodingProject(
		inspectCtx,
		policy.Root,
		strongestCodingMode(policy.AllowedModes),
	)
	cancelInspect()
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	policy.project = resolvedProject

	workerExecutable, pathErr := resolveConfigPath(baseDir, policy.WorkerExecutable)
	if pathErr != nil || strings.TrimSpace(policy.WorkerExecutable) == "" ||
		!validCodingConfiguredPath(workerExecutable) {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q requires a worker executable", alias)
	}
	workerInfo, statErr := os.Lstat(workerExecutable)
	if statErr != nil || !validCodingWorkerFile(workerInfo) {
		return CodingProjectPolicy{}, fmt.Errorf(
			"coding project %q worker executable is not a direct executable file",
			alias,
		)
	}
	workerExecutable, err = filepath.EvalSymlinks(workerExecutable)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q resolve worker executable: %w", alias, err)
	}
	workerExecutable = filepath.Clean(workerExecutable)
	if !validCodingConfiguredPath(workerExecutable) {
		return CodingProjectPolicy{}, fmt.Errorf(
			"coding project %q worker executable resolves to an unsupported path",
			alias,
		)
	}
	policy.WorkerExecutable = workerExecutable
	policy.workerInfo, err = os.Stat(workerExecutable)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q inspect worker executable: %w", alias, err)
	}
	if !validCodingWorkerFile(policy.workerInfo) || !os.SameFile(workerInfo, policy.workerInfo) {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q worker executable identity changed", alias)
	}
	policy.workerBuildID, err = codingtask.ExecutableBuildID(workerExecutable)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q worker executable: %w", alias, err)
	}

	policy.MintClawHome, policy.homeInfo, err = resolveRequiredCodingDirectory(
		baseDir,
		policy.MintClawHome,
		"MintClaw home",
	)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	if pathsOverlapSimple(policy.Root, policy.MintClawHome) {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q root overlaps MintClaw home", alias)
	}

	// Native credentials stay in MintClawHome and are proven by worker
	// initialization before any prompt is sent; this catalog never reads or
	// projects credential material.
	if !codingtask.ValidIdentifier(policy.ProviderProfile) ||
		policy.ProviderProfile != CodingProviderProfileDefault ||
		!codingtask.ValidIdentifier(policy.Provider) || !validCodingModel(policy.Model) {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q has an invalid native provider policy", alias)
	}

	if modeAllowed(policy.AllowedModes, codingtask.TaskModeMutate) {
		if policy.BranchPrefix != CodingBranchPrefix {
			return CodingProjectPolicy{}, fmt.Errorf("coding project %q has an unsupported branch prefix", alias)
		}
		policy.WorktreeParent, policy.worktreeParentInfo, err = resolveRequiredCodingDirectory(
			baseDir,
			policy.WorktreeParent,
			"worktree parent",
		)
		if err != nil {
			return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
		}
		if pathsOverlapSimple(policy.WorktreeParent, policy.Root) ||
			pathsOverlapSimple(policy.WorktreeParent, filepath.Join(policy.MintClawHome, "coding")) {
			return CodingProjectPolicy{}, fmt.Errorf(
				"coding project %q worktree parent overlaps protected state",
				alias,
			)
		}
	} else if strings.TrimSpace(policy.WorktreeParent) != "" || strings.TrimSpace(policy.BranchPrefix) != "" {
		return CodingProjectPolicy{}, fmt.Errorf(
			"coding project %q cannot configure mutation authority without mutation mode",
			alias,
		)
	}

	if policy.MaxConcurrentTasks == 0 {
		policy.MaxConcurrentTasks = DefaultCodingTaskConcurrency
	}
	if policy.MaxConcurrentTasks < 1 || policy.MaxConcurrentTasks > MaxCodingTaskConcurrency {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q has invalid task concurrency", alias)
	}
	policy.taskTimeout, err = codingPolicyDuration(
		policy.TaskTimeoutSeconds,
		DefaultCodingTaskTimeout,
		maxCodingTaskTimeout,
		"task timeout",
	)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	policy.TaskTimeoutSeconds = int(policy.taskTimeout / time.Second)
	policy.workerIdleTimeout, err = codingPolicyDuration(
		policy.WorkerIdleTimeoutSeconds,
		DefaultCodingWorkerIdleTimeout,
		maxCodingWorkerIdleTimeout,
		"worker idle timeout",
	)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	policy.WorkerIdleTimeoutSeconds = int(policy.workerIdleTimeout / time.Second)
	if policy.workerIdleTimeout != DefaultCodingWorkerIdleTimeout {
		return CodingProjectPolicy{}, fmt.Errorf(
			"coding project %q worker idle timeout is not supported by protocol v1",
			alias,
		)
	}
	if policy.EventBytesMax, err = codingPolicyBound(
		policy.EventBytesMax,
		DefaultCodingEventBytes,
		MaxCodingEventBytes,
		"event bytes",
	); err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	if policy.ResultBytesMax, err = codingPolicyBound(
		policy.ResultBytesMax,
		DefaultCodingResultBytes,
		MaxCodingResultBytes,
		"result bytes",
	); err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	if policy.ArtifactCountMax, err = codingPolicyBound(
		policy.ArtifactCountMax,
		DefaultCodingArtifactCount,
		MaxCodingArtifactCount,
		"artifact count",
	); err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	if policy.ArtifactBytesMax, err = codingPolicyBound64(
		policy.ArtifactBytesMax,
		DefaultCodingArtifactBytes,
		MaxCodingArtifactBytes,
		"artifact bytes",
	); err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	if policy.ArtifactsTotalBytesMax, err = codingPolicyBound64(
		policy.ArtifactsTotalBytesMax,
		DefaultCodingArtifactsTotal,
		MaxCodingArtifactsTotal,
		"artifacts total bytes",
	); err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	if policy.ArtifactsTotalBytesMax < policy.ArtifactBytesMax {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q has invalid aggregate artifact bytes", alias)
	}
	policy.retention, err = codingPolicyDuration(
		policy.RetentionSeconds,
		DefaultCodingTaskRetention,
		codingtask.MaxRetainDuration,
		"retention",
	)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q: %w", alias, err)
	}
	policy.RetentionSeconds = int(policy.retention / time.Second)
	if policy.CleanupPolicy == "" {
		policy.CleanupPolicy = CodingCleanupRetain
	}
	if policy.CleanupPolicy != CodingCleanupRetain {
		return CodingProjectPolicy{}, fmt.Errorf(
			"coding project %q cleanup policy is not admitted by the first slice",
			alias,
		)
	}
	if codingPolicyHasAliasedAuthority(policy) {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q has aliased filesystem authority", alias)
	}
	policy.descriptorRevision, err = codingProjectDescriptorRevision(policy)
	if err != nil {
		return CodingProjectPolicy{}, fmt.Errorf("coding project %q descriptor revision: %w", alias, err)
	}
	return cloneCodingProjectPolicy(policy), nil
}

func resolveCodingProject(
	ctx context.Context,
	root string,
	mode codingtask.TaskMode,
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
	if mode == codingtask.TaskModeMutate {
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
		if err := requireCleanCodingProject(ctx, root); err != nil {
			return project.ProjectIdentity{}, err
		}
	}
	return identity, nil
}

func requireCleanCodingProject(ctx context.Context, root string) error {
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
		return "", errors.New("git output exceeds the coding project bound")
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

func revalidateCodingPolicyPaths(policy CodingProjectPolicy) error {
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
			return ErrCodingProjectChanged
		}
	}
	if !properPathWithin(policy.SourceParent, policy.Root) || pathsOverlapSimple(policy.Root, policy.MintClawHome) ||
		(policy.WorktreeParent != "" && (pathsOverlapSimple(policy.WorktreeParent, policy.Root) ||
			pathsOverlapSimple(policy.WorktreeParent, filepath.Join(policy.MintClawHome, "coding")))) {
		return ErrCodingProjectChanged
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

func normalizeCodingModes(modes []codingtask.TaskMode) ([]codingtask.TaskMode, error) {
	if len(modes) == 0 || len(modes) > 2 {
		return nil, errors.New("allowed modes must contain investigate and/or mutate")
	}
	seen := make(map[codingtask.TaskMode]struct{}, len(modes))
	for _, mode := range modes {
		if !mode.Valid() {
			return nil, errors.New("allowed modes contain an unsupported mode")
		}
		if _, duplicate := seen[mode]; duplicate {
			return nil, errors.New("allowed modes contain a duplicate")
		}
		seen[mode] = struct{}{}
	}
	result := make([]codingtask.TaskMode, 0, len(seen))
	for _, mode := range []codingtask.TaskMode{codingtask.TaskModeInvestigate, codingtask.TaskModeMutate} {
		if _, found := seen[mode]; found {
			result = append(result, mode)
		}
	}
	if len(result) != len(seen) {
		return nil, errors.New("allowed modes contain a profile that requires coding scopes")
	}
	return result, nil
}

func strongestCodingMode(modes []codingtask.TaskMode) codingtask.TaskMode {
	if modeAllowed(modes, codingtask.TaskModeMutate) {
		return codingtask.TaskModeMutate
	}
	return codingtask.TaskModeInvestigate
}

func modeAllowed(modes []codingtask.TaskMode, wanted codingtask.TaskMode) bool {
	for _, mode := range modes {
		if mode == wanted {
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

func validateCodingProjectIsolation(projects map[string]CodingProjectPolicy) error {
	aliases := make([]string, 0, len(projects))
	revisions := make(map[string]string, len(projects))
	for alias, policy := range projects {
		aliases = append(aliases, alias)
		if previous, duplicate := revisions[policy.descriptorRevision]; duplicate {
			return fmt.Errorf("coding projects %q and %q have the same descriptor revision", previous, alias)
		}
		revisions[policy.descriptorRevision] = alias
	}
	sort.Strings(aliases)
	for index, leftAlias := range aliases {
		left := projects[leftAlias]
		for _, rightAlias := range aliases[index+1:] {
			right := projects[rightAlias]
			if codingProjectAuthoritiesOverlap(left, right) {
				return fmt.Errorf(
					"coding projects %q and %q have overlapping repository authority",
					leftAlias,
					rightAlias,
				)
			}
		}
	}
	return nil
}

func codingProjectAuthoritiesOverlap(left CodingProjectPolicy, right CodingProjectPolicy) bool {
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

func codingPolicyHasAliasedAuthority(policy CodingProjectPolicy) bool {
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

func codingProjectDescriptorRevision(policy CodingProjectPolicy) (string, error) {
	payload := struct {
		Alias         string                  `json:"alias"`
		Policy        CodingProjectPolicy     `json:"policy"`
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

func cloneCodingProjectPolicy(policy CodingProjectPolicy) CodingProjectPolicy {
	policy.AllowedModes = append([]codingtask.TaskMode(nil), policy.AllowedModes...)
	return policy
}
