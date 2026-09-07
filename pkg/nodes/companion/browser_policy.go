package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browseraction"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

const (
	maxBrowserDriverArguments = 64
	maxBrowserDriverArgBytes  = 4096
	maxBrowserPrincipals      = 64
)

var browserPrincipalPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func companionManagedDriverArgument(argument string) bool {
	for _, managed := range []string{
		"--allowed-origins", "--blocked-origins", "--caps", "--config",
		"--proxy-server", "--proxy-bypass", "--cdp-endpoint", "--endpoint",
		"--extension", "--user-data-dir", "--storage-state", "--isolated",
		"--output-dir", "--output-mode", "--headless",
	} {
		if argument == managed || strings.HasPrefix(argument, managed+"=") {
			return true
		}
	}
	return false
}

// BrowserProfilePolicy is companion-local authority. Fields that identify the
// executable or host filesystem are never projected into capability catalogs.
type BrowserProfilePolicy struct {
	Enabled                bool                  `json:"enabled"`
	Revision               string                `json:"revision,omitempty"`
	AllowedAgents          []string              `json:"allowed_agents,omitempty"`
	AllowedActors          []string              `json:"allowed_actors,omitempty"`
	Driver                 string                `json:"driver,omitempty"`
	DriverExecutable       string                `json:"driver_executable,omitempty"`
	DriverExecutableSHA256 string                `json:"driver_executable_sha256,omitempty"`
	DriverArguments        []string              `json:"driver_arguments,omitempty"`
	ProfileDirectory       string                `json:"profile_directory,omitempty"`
	LockFile               string                `json:"lock_file,omitempty"`
	Mode                   string                `json:"mode,omitempty"`
	NetworkMode            string                `json:"network_mode,omitempty"`
	CapabilityMode         string                `json:"capability_mode,omitempty"`
	ApprovalMode           string                `json:"approval_mode,omitempty"`
	AllowedOrigins         []string              `json:"allowed_origins,omitempty"`
	Policy                 *browserpolicy.Policy `json:"policy,omitempty"`
	DryRun                 bool                  `json:"dry_run"`
	AllowApprovedActions   bool                  `json:"allow_approved_actions,omitempty"`
	AllowedActions         []string              `json:"allowed_actions,omitempty"`
	Headed                 bool                  `json:"headed"`
	Limits                 nodes.BrowserLimits   `json:"limits,omitempty"`

	// driverLauncherPath preserves the validated configured path before symlink
	// canonicalization. It is runtime-only authority used to derive the child
	// PATH without exposing host filesystem details in config or catalogs.
	driverLauncherPath string
	// browserExecutablePath records an explicitly configured Playwright browser
	// executable after validation. It remains companion-local and is rechecked
	// before the browser host starts so a stale catalog cannot authorize a
	// missing runtime.
	browserExecutablePath string
	browserExecutableHash string
	browserExecutableInfo os.FileInfo
}

// DriverLauncherDirectory returns the normalized launcher's directory for the
// private browser-driver child environment. It is not serialized or cataloged.
func (profile BrowserProfilePolicy) DriverLauncherDirectory() string {
	if profile.driverLauncherPath == "" {
		return filepath.Dir(profile.DriverExecutable)
	}
	return filepath.Dir(profile.driverLauncherPath)
}

func normalizeBrowserProfiles(
	profiles map[string]BrowserProfilePolicy,
	baseDir string,
) (map[string]BrowserProfilePolicy, error) {
	if len(profiles) > nodes.MaxBrowserProfiles {
		return nil, fmt.Errorf("browser_profiles exceeds the %d profile limit", nodes.MaxBrowserProfiles)
	}
	normalized := make(map[string]BrowserProfilePolicy, len(profiles))
	enabledProfiles := 0
	for alias, profile := range profiles {
		if !profile.Enabled {
			if !browserProfilePolicyEmpty(profile) {
				return nil, fmt.Errorf("disabled browser profile %q cannot configure authority", alias)
			}
			normalized[alias] = BrowserProfilePolicy{}
			continue
		}
		ready, err := normalizeBrowserProfile(alias, profile, baseDir)
		if err != nil {
			return nil, fmt.Errorf("validate browser profile %q: %w", alias, err)
		}
		normalized[alias] = ready
		enabledProfiles++
	}
	if enabledProfiles > 1 {
		return nil, errors.New("one enabled browser profile is admitted during B4 phase 1")
	}
	return normalized, nil
}

func browserProfilePolicyEmpty(profile BrowserProfilePolicy) bool {
	return profile.Revision == "" && len(profile.AllowedAgents) == 0 &&
		len(profile.AllowedActors) == 0 && profile.Driver == "" &&
		profile.DriverExecutable == "" && profile.DriverExecutableSHA256 == "" &&
		len(profile.DriverArguments) == 0 && profile.ProfileDirectory == "" &&
		profile.LockFile == "" && profile.Mode == "" && profile.NetworkMode == "" &&
		profile.CapabilityMode == "" && profile.ApprovalMode == "" &&
		len(profile.AllowedOrigins) == 0 && profile.Policy == nil &&
		!profile.DryRun && !profile.AllowApprovedActions &&
		len(profile.AllowedActions) == 0 && !profile.Headed &&
		profile.Limits == (nodes.BrowserLimits{})
}

func normalizeBrowserProfile(
	alias string,
	profile BrowserProfilePolicy,
	baseDir string,
) (BrowserProfilePolicy, error) {
	profile.AllowedAgents = append([]string(nil), profile.AllowedAgents...)
	profile.AllowedActors = append([]string(nil), profile.AllowedActors...)
	profile.DriverArguments = append([]string(nil), profile.DriverArguments...)
	profile.AllowedOrigins = append([]string(nil), profile.AllowedOrigins...)
	profile.Policy = browserpolicy.ClonePolicy(profile.Policy)
	profile.AllowedActions = append([]string(nil), profile.AllowedActions...)
	if err := (nodes.Alias(alias)).Validate(); err != nil {
		return BrowserProfilePolicy{}, errors.New("browser profile alias is invalid")
	}
	if !browserPrincipalPattern.MatchString(profile.Revision) {
		return BrowserProfilePolicy{}, errors.New("revision is invalid")
	}
	if err := normalizeBrowserPrincipals("allowed_agents", &profile.AllowedAgents); err != nil {
		return BrowserProfilePolicy{}, err
	}
	if err := normalizeBrowserPrincipals("allowed_actors", &profile.AllowedActors); err != nil {
		return BrowserProfilePolicy{}, err
	}
	if len(profile.AllowedAgents) == 0 || len(profile.AllowedActors) == 0 {
		return BrowserProfilePolicy{}, errors.New("allowed_agents and allowed_actors must be non-empty")
	}
	if profile.Driver != nodes.BrowserDriverPlaywrightMCP {
		return BrowserProfilePolicy{}, errors.New("driver must be playwright_mcp")
	}
	executable, launcher, err := resolveBrowserExecutable(baseDir, profile.DriverExecutable)
	if err != nil {
		return BrowserProfilePolicy{}, err
	}
	profile.DriverExecutable = executable
	profile.driverLauncherPath = launcher
	if err = verifyBrowserExecutableDigest(executable, profile.DriverExecutableSHA256); err != nil {
		return BrowserProfilePolicy{}, err
	}
	if len(profile.DriverArguments) == 0 || len(profile.DriverArguments) > maxBrowserDriverArguments {
		return BrowserProfilePolicy{}, errors.New("driver_arguments must contain between 1 and 64 entries")
	}
	for _, argument := range profile.DriverArguments {
		if argument == "" || len(argument) > maxBrowserDriverArgBytes || strings.ContainsRune(argument, 0) {
			return BrowserProfilePolicy{}, errors.New("driver_arguments contains an invalid value")
		}
		if companionManagedDriverArgument(argument) {
			return BrowserProfilePolicy{}, errors.New(
				"driver_arguments contains an option reserved for the companion browser host",
			)
		}
	}
	profile.DriverArguments, profile.browserExecutablePath, profile.browserExecutableHash,
		profile.browserExecutableInfo, err = normalizeBrowserRuntimeExecutable(
		profile.DriverArguments,
		baseDir,
	)
	if err != nil {
		return BrowserProfilePolicy{}, err
	}
	profileDirectory, err := resolveExistingBrowserDirectory(baseDir, profile.ProfileDirectory)
	if err != nil {
		return BrowserProfilePolicy{}, fmt.Errorf("resolve profile_directory: %w", err)
	}
	profile.ProfileDirectory = profileDirectory
	lockFile, err := resolveBrowserLockFile(baseDir, profile.LockFile)
	if err != nil {
		return BrowserProfilePolicy{}, fmt.Errorf("resolve lock_file: %w", err)
	}
	if lockFile == profileDirectory || pathWithin(lockFile, profileDirectory) {
		return BrowserProfilePolicy{}, errors.New("lock_file must be a non-empty path outside profile_directory")
	}
	if info, statErr := os.Stat(lockFile); statErr == nil && info.IsDir() {
		return BrowserProfilePolicy{}, errors.New("lock_file must not be a directory")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return BrowserProfilePolicy{}, fmt.Errorf("stat lock_file: %w", statErr)
	}
	profile.LockFile = lockFile
	if profile.Mode != nodes.BrowserProfileManaged || profile.DryRun == profile.AllowApprovedActions {
		return BrowserProfilePolicy{}, errors.New(
			"profile requires managed mode and exactly one of dry_run or allow_approved_actions",
		)
	}
	if !browserpolicy.CapabilityModeValid(profile.CapabilityMode) {
		return BrowserProfilePolicy{}, errors.New("capability_mode is unsupported")
	}
	if !browserpolicy.ApprovalModeValid(profile.ApprovalMode) {
		return BrowserProfilePolicy{}, errors.New("approval_mode is unsupported")
	}
	restricted := profile.CapabilityMode == browserpolicy.CapabilityRestricted
	policyApproval := profile.ApprovalMode == browserpolicy.ApprovalPolicy
	if restricted != policyApproval || restricted != (profile.Policy != nil) {
		return BrowserProfilePolicy{}, errors.New(
			"capability_mode restricted, approval_mode policy, and policy must be configured together",
		)
	}
	if profile.Policy != nil {
		normalized, normalizeErr := browserpolicy.NormalizePolicy(*profile.Policy)
		if normalizeErr != nil {
			return BrowserProfilePolicy{}, normalizeErr
		}
		profile.Policy = &normalized
	}
	profile.Limits = profile.Limits.Effective()
	if err = profile.Limits.Validate(); err != nil {
		return BrowserProfilePolicy{}, fmt.Errorf("validate limits: %w", err)
	}
	if profile.NetworkMode == "" {
		return BrowserProfilePolicy{}, errors.New("network_mode is required")
	}
	if profile.NetworkMode != nodes.BrowserNetworkExactOrigins &&
		profile.NetworkMode != nodes.BrowserNetworkPublicWeb &&
		profile.NetworkMode != nodes.BrowserNetworkAnyHTTP {
		return BrowserProfilePolicy{}, errors.New("network_mode is unsupported")
	}
	if len(profile.AllowedOrigins) > 64 {
		return BrowserProfilePolicy{}, errors.New("allowed_origins exceeds the 64-entry limit")
	}
	if profile.NetworkMode == nodes.BrowserNetworkExactOrigins && len(profile.AllowedOrigins) == 0 {
		return BrowserProfilePolicy{}, errors.New("exact_origins requires allowed_origins")
	}
	if profile.NetworkMode != nodes.BrowserNetworkExactOrigins && len(profile.AllowedOrigins) != 0 {
		return BrowserProfilePolicy{}, errors.New("non-exact network mode cannot set allowed_origins")
	}
	seenOrigins := make(map[string]struct{}, len(profile.AllowedOrigins))
	for index, origin := range profile.AllowedOrigins {
		profile.AllowedOrigins[index], err = browserpolicy.NormalizePublicOrigin(origin)
		if err != nil {
			return BrowserProfilePolicy{}, err
		}
		if _, duplicate := seenOrigins[profile.AllowedOrigins[index]]; duplicate {
			return BrowserProfilePolicy{}, errors.New("allowed_origins contains a duplicate origin")
		}
		seenOrigins[profile.AllowedOrigins[index]] = struct{}{}
	}
	slices.Sort(profile.AllowedOrigins)
	if err = normalizeBrowserActions(&profile.AllowedActions); err != nil {
		return BrowserProfilePolicy{}, err
	}
	descriptor := browserProfileDescriptor(alias, profile)
	if err = descriptor.Validate(); err != nil {
		return BrowserProfilePolicy{}, err
	}
	return profile, nil
}

func normalizeBrowserPrincipals(label string, values *[]string) error {
	if len(*values) > maxBrowserPrincipals {
		return fmt.Errorf("%s exceeds the 64-entry limit", label)
	}
	seen := make(map[string]struct{}, len(*values))
	for _, value := range *values {
		if !browserPrincipalPattern.MatchString(value) {
			return fmt.Errorf("%s contains an invalid principal", label)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains a duplicate principal", label)
		}
		seen[value] = struct{}{}
	}
	slices.Sort(*values)
	return nil
}

func normalizeBrowserActions(actions *[]string) error {
	if len(*actions) == 0 || len(*actions) > nodes.MaxBrowserActions {
		return fmt.Errorf("allowed_actions must contain between one and %d admitted actions", nodes.MaxBrowserActions)
	}
	seen := make(map[string]struct{}, len(*actions))
	for _, action := range *actions {
		if !browseraction.ActionKind(action).Valid() {
			return errors.New("allowed_actions contains an unsupported action")
		}
		if _, duplicate := seen[action]; duplicate {
			return errors.New("allowed_actions contains a duplicate action")
		}
		seen[action] = struct{}{}
	}
	slices.Sort(*actions)
	return nil
}

func resolveBrowserExecutable(baseDir, configured string) (string, string, error) {
	path, err := resolveConfigPath(baseDir, configured)
	if err != nil || strings.TrimSpace(configured) == "" {
		return "", "", errors.New("driver_executable is required")
	}
	launcherDirectory, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", "", fmt.Errorf("resolve driver_executable directory: %w", err)
	}
	launcherInfo, err := os.Stat(launcherDirectory)
	if err != nil || !launcherInfo.IsDir() || validateBrowserDriverDirectory(launcherInfo) != nil {
		return "", "", errors.New("driver_executable directory is not trusted")
	}
	launcherPath := filepath.Join(filepath.Clean(launcherDirectory), filepath.Base(path))
	realPath, err := filepath.EvalSymlinks(launcherPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve driver_executable: %w", err)
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", errors.New("driver_executable must be an executable regular file")
	}
	targetDirectory, err := os.Stat(filepath.Dir(realPath))
	if err != nil || !targetDirectory.IsDir() || validateBrowserDriverDirectory(targetDirectory) != nil {
		return "", "", errors.New("driver_executable target directory is not trusted")
	}
	return filepath.Clean(realPath), launcherPath, nil
}

func normalizeBrowserRuntimeExecutable(
	arguments []string,
	baseDir string,
) ([]string, string, string, os.FileInfo, error) {
	normalized := append([]string(nil), arguments...)
	configuredIndex := -1
	configured := ""
	joined := false
	for index := 0; index < len(normalized); index++ {
		argument := normalized[index]
		switch {
		case argument == "--executable-path":
			if configuredIndex >= 0 || index+1 >= len(normalized) || normalized[index+1] == "" {
				return nil, "", "", nil, errors.New("browser executable path is invalid")
			}
			configuredIndex, configured = index+1, normalized[index+1]
			index++
		case strings.HasPrefix(argument, "--executable-path="):
			if configuredIndex >= 0 {
				return nil, "", "", nil, errors.New("browser executable path is duplicated")
			}
			configuredIndex = index
			configured = strings.TrimPrefix(argument, "--executable-path=")
			joined = true
		}
	}
	if configuredIndex < 0 {
		return normalized, "", "", nil, nil
	}
	executable, _, err := resolveBrowserExecutable(baseDir, configured)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("validate browser executable path: %w", err)
	}
	digest, info, err := browserExecutableDigest(executable)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("validate browser executable path: %w", err)
	}
	if joined {
		normalized[configuredIndex] = "--executable-path=" + executable
	} else {
		normalized[configuredIndex] = executable
	}
	return normalized, executable, digest, info, nil
}

func browserExecutableDigest(path string) (string, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", nil, errors.New("browser executable is unavailable")
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil || validateBrowserExecutableFile(before) != nil {
		return "", nil, errors.New("browser executable is not a trusted regular file")
	}
	hasher := sha256.New()
	if _, err = io.Copy(hasher, file); err != nil {
		return "", nil, errors.New("browser executable could not be verified")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || validateBrowserExecutableFile(after) != nil {
		return "", nil, errors.New("browser executable identity changed")
	}
	return hex.EncodeToString(hasher.Sum(nil)), after, nil
}

func verifyBrowserExecutableDigest(path, expected string) error {
	decoded, err := hex.DecodeString(expected)
	if err != nil || len(decoded) != sha256.Size || expected != strings.ToLower(expected) {
		return errors.New("driver_executable_sha256 must be a lowercase SHA-256 digest")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read driver_executable: %w", err)
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	if _, err = io.Copy(hasher, file); err != nil {
		return fmt.Errorf("read driver_executable: %w", err)
	}
	if !slices.Equal(decoded, hasher.Sum(nil)) {
		return errors.New("driver_executable_sha256 does not match driver_executable")
	}
	return nil
}

func verifyBrowserProfileRuntimeIdentity(profile BrowserProfilePolicy) error {
	if profile.browserExecutablePath != "" {
		executable, _, err := resolveBrowserExecutable("", profile.browserExecutablePath)
		if err != nil || executable != profile.browserExecutablePath {
			return errors.New("browser runtime executable identity changed")
		}
		digest, info, err := browserExecutableDigest(executable)
		if err != nil || digest != profile.browserExecutableHash ||
			profile.browserExecutableInfo == nil || !os.SameFile(profile.browserExecutableInfo, info) {
			return errors.New("browser runtime executable identity changed")
		}
	}
	launcherDirectory := profile.DriverLauncherDirectory()
	realLauncherDirectory, err := filepath.EvalSymlinks(launcherDirectory)
	if err != nil || filepath.Clean(realLauncherDirectory) != launcherDirectory {
		return errors.New("browser executable identity changed")
	}
	launcherInfo, err := os.Stat(launcherDirectory)
	if err != nil || !launcherInfo.IsDir() || validateBrowserDriverDirectory(launcherInfo) != nil {
		return errors.New("browser executable identity changed")
	}
	launcherTarget, err := filepath.EvalSymlinks(profile.driverLauncherPath)
	if err != nil || filepath.Clean(launcherTarget) != profile.DriverExecutable {
		return errors.New("browser executable identity changed")
	}
	if err = verifyBrowserExecutableDigest(
		profile.DriverExecutable,
		profile.DriverExecutableSHA256,
	); err != nil {
		return errors.New("browser executable identity changed")
	}
	realProfile, err := filepath.EvalSymlinks(profile.ProfileDirectory)
	if err != nil || filepath.Clean(realProfile) != profile.ProfileDirectory {
		return errors.New("browser profile identity changed")
	}
	profileInfo, err := os.Stat(realProfile)
	if err != nil || !profileInfo.IsDir() || validateBrowserProfileDirectory(profileInfo) != nil {
		return errors.New("browser profile identity changed")
	}
	realLockParent, err := filepath.EvalSymlinks(filepath.Dir(profile.LockFile))
	if err != nil || filepath.Join(realLockParent, filepath.Base(profile.LockFile)) != profile.LockFile {
		return errors.New("browser lock identity changed")
	}
	parentInfo, err := os.Stat(realLockParent)
	if err != nil || !parentInfo.IsDir() || validateBrowserProfileDirectory(parentInfo) != nil {
		return errors.New("browser lock identity changed")
	}
	lockInfo, err := os.Lstat(profile.LockFile)
	if err == nil && (!lockInfo.Mode().IsRegular() || lockInfo.Mode()&os.ModeSymlink != 0) {
		return errors.New("browser lock identity changed")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("browser lock identity changed")
	}
	return nil
}

func resolveExistingBrowserDirectory(baseDir, configured string) (string, error) {
	path, err := resolveConfigPath(baseDir, configured)
	if err != nil || strings.TrimSpace(configured) == "" {
		return "", errors.New("path is required")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.IsDir() {
		return "", errors.New("path must be an existing directory")
	}
	if err = validateBrowserProfileDirectory(info); err != nil {
		return "", err
	}
	return filepath.Clean(realPath), nil
}

func resolveBrowserLockFile(baseDir, configured string) (string, error) {
	path, err := resolveConfigPath(baseDir, configured)
	if err != nil || strings.TrimSpace(configured) == "" {
		return "", errors.New("lock file path is required")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("resolve lock_file parent: %w", err)
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", errors.New("lock_file parent must be an existing directory")
	}
	if err = validateBrowserProfileDirectory(info); err != nil {
		return "", errors.New("lock_file parent must be private to the companion account")
	}
	path = filepath.Join(parent, filepath.Base(path))
	if info, err = os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("lock_file must not be a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect lock_file: %w", err)
	}
	return filepath.Clean(path), nil
}

func pathWithin(path, directory string) bool {
	relative, err := filepath.Rel(directory, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func browserProfileDescriptor(alias string, profile BrowserProfilePolicy) nodes.BrowserProfileDescriptor {
	actions := append([]string(nil), profile.AllowedActions...)
	if profile.DryRun {
		actions = slices.DeleteFunc(actions, func(action string) bool { return action == "drag" })
	}
	descriptor := nodes.BrowserProfileDescriptor{
		Alias: alias, Revision: profile.Revision, Driver: profile.Driver,
		Mode: profile.Mode, NetworkMode: profile.NetworkMode,
		CapabilityMode: profile.CapabilityMode, ApprovalMode: profile.ApprovalMode,
		DryRun: profile.DryRun, AllowApprovedActions: profile.AllowApprovedActions,
		Headed:  profile.Headed,
		Actions: actions,
		Limits:  profile.Limits,
	}
	if profile.Policy != nil {
		descriptor.PolicyRevision, _ = browserpolicy.PolicyRevision(*profile.Policy)
	}
	return descriptor
}

func browserProfileDescriptors(
	profiles map[string]BrowserProfilePolicy,
) ([]nodes.BrowserProfileDescriptor, error) {
	descriptors := make([]nodes.BrowserProfileDescriptor, 0, len(profiles))
	for alias, profile := range profiles {
		if profile.Enabled {
			descriptors = append(descriptors, browserProfileDescriptor(alias, profile))
		}
	}
	slices.SortFunc(descriptors, func(left, right nodes.BrowserProfileDescriptor) int {
		return strings.Compare(left.Alias, right.Alias)
	})
	if len(descriptors) == 0 {
		return []nodes.BrowserProfileDescriptor{}, nil
	}
	if _, err := nodes.BrowserCommandDescriptors(descriptors); err != nil {
		return nil, err
	}
	return descriptors, nil
}

// BrowserProfileDescriptors returns the safe typed projection consumed by a
// dedicated capability host without exposing companion-local driver details.
func BrowserProfileDescriptors(
	profiles map[string]BrowserProfilePolicy,
) ([]nodes.BrowserProfileDescriptor, error) {
	return browserProfileDescriptors(profiles)
}

// VerifyBrowserProfileRuntimeIdentity rechecks executable, profile, and lock
// identity immediately before a host starts a browser process.
func VerifyBrowserProfileRuntimeIdentity(profile BrowserProfilePolicy) error {
	return verifyBrowserProfileRuntimeIdentity(profile)
}

// HasEnabledBrowserProfile reports whether local browser authority requires a
// companion host. Disabled profiles never cause driver initialization.
func HasEnabledBrowserProfile(profiles map[string]BrowserProfilePolicy) bool {
	for _, profile := range profiles {
		if profile.Enabled {
			return true
		}
	}
	return false
}
