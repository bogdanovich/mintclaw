package browser

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func normalizeManagedProfileRuntime(
	runtime config.BrowserProfileRuntimeConfig,
) (config.BrowserProfileRuntimeConfig, error) {
	profileDirectory := filepath.Clean(runtime.ProfileDirectory)
	lockFile := filepath.Clean(runtime.LockFile)
	if !filepath.IsAbs(profileDirectory) || !filepath.IsAbs(lockFile) ||
		profileDirectory == string(filepath.Separator) || lockFile == string(filepath.Separator) {
		return config.BrowserProfileRuntimeConfig{}, errors.New("browser profile runtime paths are unsafe")
	}
	configuredProfileInfo, err := os.Lstat(profileDirectory)
	if err != nil || configuredProfileInfo.Mode()&os.ModeSymlink != 0 {
		return config.BrowserProfileRuntimeConfig{}, errors.New("browser profile directory identity is unsafe")
	}
	realProfile, err := filepath.EvalSymlinks(profileDirectory)
	if err != nil {
		return config.BrowserProfileRuntimeConfig{}, errors.New("browser profile directory identity is unsafe")
	}
	profileDirectory = filepath.Clean(realProfile)
	profileInfo, err := os.Lstat(profileDirectory)
	if err != nil || !profileInfo.IsDir() || profileInfo.Mode()&os.ModeSymlink != 0 ||
		validateBrowserRuntimeOwner(profileInfo, true) != nil {
		return config.BrowserProfileRuntimeConfig{}, errors.New(
			"browser profile directory is not private to the gateway account",
		)
	}
	configuredLockParent, err := os.Lstat(filepath.Dir(lockFile))
	if err != nil || configuredLockParent.Mode()&os.ModeSymlink != 0 {
		return config.BrowserProfileRuntimeConfig{}, errors.New("browser profile lock parent identity is unsafe")
	}
	realLockParent, err := filepath.EvalSymlinks(filepath.Dir(lockFile))
	if err != nil {
		return config.BrowserProfileRuntimeConfig{}, errors.New("browser profile lock parent identity is unsafe")
	}
	realLockParent = filepath.Clean(realLockParent)
	lockFile = filepath.Join(realLockParent, filepath.Base(lockFile))
	parentInfo, err := os.Lstat(realLockParent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 ||
		validateBrowserRuntimeOwner(parentInfo, true) != nil {
		return config.BrowserProfileRuntimeConfig{}, errors.New(
			"browser profile lock parent is not private to the gateway account",
		)
	}
	relative, err := filepath.Rel(profileDirectory, lockFile)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return config.BrowserProfileRuntimeConfig{}, errors.New(
			"browser profile lock must be outside the profile directory",
		)
	}
	lockInfo, err := os.Lstat(lockFile)
	if err == nil {
		if !lockInfo.Mode().IsRegular() || lockInfo.Mode()&os.ModeSymlink != 0 ||
			validateBrowserRuntimeOwner(lockInfo, false) != nil {
			return config.BrowserProfileRuntimeConfig{}, errors.New("browser profile lock identity is unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return config.BrowserProfileRuntimeConfig{}, fmt.Errorf("inspect browser profile lock: %w", err)
	}
	runtime.ProfileDirectory = profileDirectory
	runtime.LockFile = lockFile
	return runtime, nil
}

func playwrightServerForProfile(
	server config.MCPServerConfig,
	profile config.BrowserProfileConfig,
) (config.MCPServerConfig, error) {
	if !profile.CanonicalAuthority() {
		return server, nil
	}
	if server.ExclusiveLockFile != "" {
		return config.MCPServerConfig{}, errors.New("browser driver template contains a profile lock")
	}
	for _, argument := range server.Args {
		if playwrightProfileOwnedArgument(argument) {
			return config.MCPServerConfig{}, errors.New("browser driver template contains profile identity")
		}
	}
	runtime, err := normalizeManagedProfileRuntime(profile.Runtime)
	if err != nil {
		return config.MCPServerConfig{}, err
	}
	server = cloneMCPServerConfig(server)
	server.ExclusiveLockFile = runtime.LockFile
	server.Args = append(server.Args, "--user-data-dir", runtime.ProfileDirectory)
	if !runtime.Headed {
		server.Args = append(server.Args, "--headless")
	}
	return server, nil
}

func playwrightProfileOwnedArgument(argument string) bool {
	for _, owned := range []string{
		"--extension", "--user-data-dir", "--storage-state", "--isolated", "--headless",
	} {
		if argument == owned || strings.HasPrefix(argument, owned+"=") {
			return true
		}
	}
	return false
}
