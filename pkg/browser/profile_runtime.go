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
	if runtime.EphemeralRoot != "" {
		return config.BrowserProfileRuntimeConfig{}, errors.New(
			"managed browser profile cannot configure an ephemeral root",
		)
	}
	profileDirectory, lockFile, err := normalizeProfileRuntimeRoot(
		runtime.ProfileDirectory,
		runtime.LockFile,
		"profile directory",
	)
	if err != nil {
		return config.BrowserProfileRuntimeConfig{}, err
	}
	runtime.ProfileDirectory = profileDirectory
	runtime.LockFile = lockFile
	return runtime, nil
}

func normalizeEphemeralProfileRuntime(
	runtime config.BrowserProfileRuntimeConfig,
) (config.BrowserProfileRuntimeConfig, error) {
	if runtime.ProfileDirectory != "" {
		return config.BrowserProfileRuntimeConfig{}, errors.New(
			"ephemeral browser profile cannot configure a persistent profile directory",
		)
	}
	ephemeralRoot, lockFile, err := normalizeProfileRuntimeRoot(
		runtime.EphemeralRoot,
		runtime.LockFile,
		"ephemeral root",
	)
	if err != nil {
		return config.BrowserProfileRuntimeConfig{}, err
	}
	runtime.EphemeralRoot = ephemeralRoot
	runtime.LockFile = lockFile
	return runtime, nil
}

func normalizeProfileRuntimeRoot(
	configuredRoot string,
	configuredLockFile string,
	rootLabel string,
) (string, string, error) {
	profileDirectory := filepath.Clean(configuredRoot)
	lockFile := filepath.Clean(configuredLockFile)
	if !filepath.IsAbs(profileDirectory) || !filepath.IsAbs(lockFile) ||
		profileDirectory == string(filepath.Separator) || lockFile == string(filepath.Separator) {
		return "", "", errors.New("browser profile runtime paths are unsafe")
	}
	configuredProfileInfo, err := os.Lstat(profileDirectory)
	if err != nil || configuredProfileInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("browser %s identity is unsafe", rootLabel)
	}
	realProfile, err := filepath.EvalSymlinks(profileDirectory)
	if err != nil {
		return "", "", fmt.Errorf("browser %s identity is unsafe", rootLabel)
	}
	realProfile = filepath.Clean(realProfile)
	if realProfile != profileDirectory {
		return "", "", fmt.Errorf("browser %s identity is unsafe", rootLabel)
	}
	profileInfo, err := os.Lstat(profileDirectory)
	if err != nil || !profileInfo.IsDir() || profileInfo.Mode()&os.ModeSymlink != 0 ||
		validateBrowserRuntimeOwner(profileInfo, true) != nil {
		return "", "", fmt.Errorf(
			"browser %s is not private to the gateway account",
			rootLabel,
		)
	}
	lockParent := filepath.Dir(lockFile)
	configuredLockParent, err := os.Lstat(lockParent)
	if err != nil || configuredLockParent.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("browser profile lock parent identity is unsafe")
	}
	realLockParent, err := filepath.EvalSymlinks(lockParent)
	if err != nil {
		return "", "", errors.New("browser profile lock parent identity is unsafe")
	}
	realLockParent = filepath.Clean(realLockParent)
	if realLockParent != lockParent {
		return "", "", errors.New("browser profile lock parent identity is unsafe")
	}
	lockFile = filepath.Join(realLockParent, filepath.Base(lockFile))
	parentInfo, err := os.Lstat(realLockParent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 ||
		validateBrowserRuntimeOwner(parentInfo, true) != nil {
		return "", "", errors.New(
			"browser profile lock parent is not private to the gateway account",
		)
	}
	relative, err := filepath.Rel(profileDirectory, lockFile)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf(
			"browser profile lock must be outside the %s",
			rootLabel,
		)
	}
	lockInfo, err := os.Lstat(lockFile)
	if err == nil {
		if !lockInfo.Mode().IsRegular() || lockInfo.Mode()&os.ModeSymlink != 0 ||
			validateBrowserRuntimeOwner(lockInfo, false) != nil {
			return "", "", errors.New("browser profile lock identity is unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("inspect browser profile lock: %w", err)
	}
	return profileDirectory, lockFile, nil
}

func playwrightServerForProfile(
	server config.MCPServerConfig,
	profile config.BrowserProfileConfig,
) (config.MCPServerConfig, error) {
	if server.ExclusiveLockFile != "" && profile.Mode != config.BrowserProfileAttachedUser {
		return config.MCPServerConfig{}, errors.New("browser driver template contains a profile lock")
	}
	for _, argument := range server.Args {
		if playwrightProfileOwnedArgument(argument) {
			return config.MCPServerConfig{}, errors.New("browser driver template contains profile identity")
		}
	}
	var (
		runtime config.BrowserProfileRuntimeConfig
		err     error
	)
	switch profile.Mode {
	case config.BrowserProfileManaged:
		runtime, err = normalizeManagedProfileRuntime(profile.Runtime)
	case config.BrowserProfileEphemeral:
		runtime, err = normalizeEphemeralProfileRuntime(profile.Runtime)
	case config.BrowserProfileAttachedUser:
		if profile.Runtime != (config.BrowserProfileRuntimeConfig{}) || server.ExclusiveLockFile == "" {
			return config.MCPServerConfig{}, errors.New("attached browser runtime authority is invalid")
		}
		server = cloneMCPServerConfig(server)
		server.Args = append(server.Args, "--extension")
		return server, nil
	default:
		err = errors.New("browser profile mode is unsupported")
	}
	if err != nil {
		return config.MCPServerConfig{}, err
	}
	server = cloneMCPServerConfig(server)
	server.ExclusiveLockFile = runtime.LockFile
	if profile.Mode == config.BrowserProfileEphemeral {
		server.Args = append(server.Args, "--isolated")
	} else {
		server.Args = append(server.Args, "--user-data-dir", runtime.ProfileDirectory)
	}
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

func validatePlaywrightProfileArguments(
	arguments []string,
	profile config.BrowserProfileConfig,
) error {
	userDataDirectory := ""
	isolated := false
	headless := false
	extension := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--user-data-dir":
			if userDataDirectory != "" || index+1 >= len(arguments) {
				return errors.New("browser driver profile arguments are ambiguous")
			}
			index++
			userDataDirectory = arguments[index]
		case strings.HasPrefix(argument, "--user-data-dir="):
			if userDataDirectory != "" {
				return errors.New("browser driver profile arguments are ambiguous")
			}
			userDataDirectory = strings.TrimPrefix(argument, "--user-data-dir=")
		case argument == "--isolated":
			if isolated {
				return errors.New("browser driver profile arguments are ambiguous")
			}
			isolated = true
		case strings.HasPrefix(argument, "--isolated=") ||
			argument == "--storage-state" || strings.HasPrefix(argument, "--storage-state="):
			return errors.New("browser driver profile arguments are unsafe")
		case argument == "--headless":
			if headless {
				return errors.New("browser driver profile arguments are ambiguous")
			}
			headless = true
		case argument == "--extension":
			if extension {
				return errors.New("browser driver profile arguments are ambiguous")
			}
			extension = true
		case strings.HasPrefix(argument, "--extension="):
			return errors.New("browser driver profile arguments are unsafe")
		case strings.HasPrefix(argument, "--headless="):
			return errors.New("browser driver profile arguments are unsafe")
		}
	}
	if profile.Mode != config.BrowserProfileAttachedUser && headless == profile.Runtime.Headed {
		return errors.New("browser driver headed mode conflicts with profile authority")
	}
	switch profile.Mode {
	case config.BrowserProfileManaged:
		if extension || isolated || userDataDirectory != profile.Runtime.ProfileDirectory {
			return errors.New("managed browser driver identity conflicts with profile authority")
		}
	case config.BrowserProfileEphemeral:
		if extension || !isolated || userDataDirectory != "" {
			return errors.New("ephemeral browser driver identity conflicts with profile authority")
		}
	case config.BrowserProfileAttachedUser:
		if !extension || isolated || headless || userDataDirectory != "" ||
			profile.Runtime != (config.BrowserProfileRuntimeConfig{}) {
			return errors.New("attached browser driver identity conflicts with profile authority")
		}
	default:
		return errors.New("browser driver profile mode is unsupported")
	}
	return nil
}
