package browser

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/bogdanovich/mintclaw/pkg/config"
	localmcp "github.com/bogdanovich/mintclaw/pkg/mcp"
)

const ephemeralRuntimePrefix = ".e1-"

var ephemeralRuntimeNamePattern = regexp.MustCompile(
	`^\.e1-[a-f0-9]{16}(?:\.quarantine)?$`,
)

// ephemeralRuntimeLease owns one anchored session directory. The Playwright
// worker closes the browser process before asking the lease to remove this
// directory. A failed deletion keeps the lease and directory available for an
// exact retry and therefore keeps the profile unavailable at the broker.
type ephemeralRuntimeLease struct {
	rootPath       string
	root           *os.Root
	rootInfo       os.FileInfo
	directoryInfo  os.FileInfo
	name           string
	quarantineName string
	lifecycle      *localmcp.ExclusiveServerLease
	rename         func(string, string) error
	removeAll      func(string) error
	sync           func() error
	closed         bool
}

func recoverEphemeralProfileRuntime(runtime config.BrowserProfileRuntimeConfig) error {
	normalized, err := normalizeEphemeralProfileRuntime(runtime)
	if err != nil {
		return err
	}
	lifecycleLease, err := localmcp.AcquireExclusiveServerLease(
		"browser_ephemeral_lifecycle",
		ephemeralLifecycleLockFile(normalized),
	)
	if err != nil {
		return fmt.Errorf("acquire browser ephemeral lifecycle lease: %w", err)
	}
	defer func() { _ = lifecycleLease.Close() }()
	recoveryLease, err := localmcp.AcquireExclusiveServerLease(
		"browser_ephemeral_recovery",
		normalized.LockFile,
	)
	if err != nil {
		return fmt.Errorf("acquire browser ephemeral recovery lease: %w", err)
	}
	defer func() { _ = recoveryLease.Close() }()
	root, rootInfo, err := openEphemeralRoot(normalized.EphemeralRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	rootDirectory, err := root.Open(".")
	if err != nil {
		return errors.New("browser ephemeral root is unavailable")
	}
	entries, readErr := rootDirectory.ReadDir(-1)
	closeErr := rootDirectory.Close()
	if readErr != nil || closeErr != nil {
		return errors.New("browser ephemeral root cannot be inspected")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if !ephemeralRuntimeNamePattern.MatchString(entry.Name()) {
			return fmt.Errorf("browser ephemeral root contains unrecognized entry %q", entry.Name())
		}
		info, statErr := root.Lstat(entry.Name())
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			validateBrowserRuntimeOwner(info, true) != nil {
			return fmt.Errorf("browser ephemeral recovery entry %q is unsafe", entry.Name())
		}
	}
	for _, entry := range entries {
		if err = root.RemoveAll(entry.Name()); err != nil {
			return fmt.Errorf("remove stale browser ephemeral runtime: %w", err)
		}
	}
	if err = syncRootDirectory(root); err != nil {
		return fmt.Errorf("sync browser ephemeral recovery: %w", err)
	}
	if current, statErr := os.Lstat(normalized.EphemeralRoot); statErr != nil ||
		!os.SameFile(rootInfo, current) {
		return errors.New("browser ephemeral root identity changed during recovery")
	}
	return nil
}

func createEphemeralRuntimeLease(
	runtime config.BrowserProfileRuntimeConfig,
	sessionID string,
) (*ephemeralRuntimeLease, error) {
	if !validIdentifier(sessionID) {
		return nil, ErrDenied
	}
	normalized, err := normalizeEphemeralProfileRuntime(runtime)
	if err != nil {
		return nil, err
	}
	lifecycleLease, err := localmcp.AcquireExclusiveServerLease(
		"browser_ephemeral_lifecycle",
		ephemeralLifecycleLockFile(normalized),
	)
	if err != nil {
		return nil, fmt.Errorf("acquire browser ephemeral lifecycle lease: %w", err)
	}
	root, rootInfo, err := openEphemeralRoot(normalized.EphemeralRoot)
	if err != nil {
		_ = lifecycleLease.Close()
		return nil, err
	}
	lease := &ephemeralRuntimeLease{
		rootPath:  normalized.EphemeralRoot,
		root:      root,
		rootInfo:  rootInfo,
		lifecycle: lifecycleLease,
		rename:    root.Rename,
		removeAll: root.RemoveAll,
		sync:      func() error { return syncRootDirectory(root) },
	}
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		_ = root.Close()
		_ = lifecycleLease.Close()
		return nil, errors.New("generate browser ephemeral runtime identity")
	}
	lease.name = ephemeralRuntimePrefix + hex.EncodeToString(random)
	lease.quarantineName = lease.name + ".quarantine"
	if !ephemeralRuntimeNamePattern.MatchString(lease.name) {
		_ = root.Close()
		_ = lifecycleLease.Close()
		return nil, ErrDenied
	}
	if err = root.Mkdir(lease.name, 0o700); err != nil {
		_ = root.Close()
		_ = lifecycleLease.Close()
		return nil, fmt.Errorf("create browser ephemeral runtime: %w", err)
	}
	lease.directoryInfo, err = root.Lstat(lease.name)
	if err != nil || !lease.directoryInfo.IsDir() || lease.directoryInfo.Mode()&os.ModeSymlink != 0 ||
		validateBrowserRuntimeOwner(lease.directoryInfo, true) != nil {
		return lease, errors.New("created browser ephemeral runtime is unsafe")
	}
	absoluteInfo, statErr := os.Lstat(lease.Path())
	if statErr != nil || !os.SameFile(lease.directoryInfo, absoluteInfo) {
		return lease, errors.New("created browser ephemeral runtime identity changed")
	}
	if err = syncRootDirectory(root); err != nil {
		return lease, fmt.Errorf("sync browser ephemeral runtime creation: %w", err)
	}
	return lease, nil
}

func ephemeralLifecycleLockFile(runtime config.BrowserProfileRuntimeConfig) string {
	return runtime.LockFile + config.BrowserEphemeralLifecycleLockSuffix
}

func openEphemeralRoot(path string) (*os.Root, os.FileInfo, error) {
	configured, err := os.Lstat(path)
	if err != nil || !configured.IsDir() || configured.Mode()&os.ModeSymlink != 0 ||
		validateBrowserRuntimeOwner(configured, true) != nil {
		return nil, nil, errors.New("browser ephemeral root identity is unsafe")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, errors.New("browser ephemeral root is unavailable")
	}
	anchored, err := root.Lstat(".")
	if err != nil || !os.SameFile(configured, anchored) {
		_ = root.Close()
		return nil, nil, errors.New("browser ephemeral root identity changed")
	}
	return root, configured, nil
}

func (lease *ephemeralRuntimeLease) Path() string {
	if lease == nil || lease.rootPath == "" || lease.name == "" {
		return ""
	}
	return filepath.Join(lease.rootPath, lease.name)
}

func (lease *ephemeralRuntimeLease) Close() error {
	if lease == nil || lease.closed {
		return nil
	}
	if lease.root == nil || lease.rootInfo == nil || lease.directoryInfo == nil ||
		lease.lifecycle == nil || lease.rename == nil || lease.removeAll == nil || lease.sync == nil ||
		!ephemeralRuntimeNamePattern.MatchString(lease.name) ||
		!ephemeralRuntimeNamePattern.MatchString(lease.quarantineName) {
		return errors.New("browser ephemeral cleanup authority is unavailable")
	}
	configured, err := os.Lstat(lease.rootPath)
	if err != nil || !os.SameFile(lease.rootInfo, configured) {
		return errors.New("browser ephemeral root identity changed before cleanup")
	}
	anchored, err := lease.root.Lstat(".")
	if err != nil || !os.SameFile(lease.rootInfo, anchored) {
		return errors.New("browser ephemeral root identity changed before cleanup")
	}
	currentName := lease.name
	current, statErr := lease.root.Lstat(currentName)
	if errors.Is(statErr, os.ErrNotExist) {
		currentName = lease.quarantineName
		current, statErr = lease.root.Lstat(currentName)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if err = lease.sync(); err != nil {
			return err
		}
		return lease.finish()
	}
	if statErr != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(lease.directoryInfo, current) || validateBrowserRuntimeOwner(current, true) != nil {
		return errors.New("browser ephemeral session identity changed before cleanup")
	}
	if currentName == lease.name {
		if _, quarantineErr := lease.root.Lstat(lease.quarantineName); quarantineErr == nil ||
			!errors.Is(quarantineErr, os.ErrNotExist) {
			return errors.New("browser ephemeral quarantine identity is unavailable")
		}
		if err = lease.rename(lease.name, lease.quarantineName); err != nil {
			return fmt.Errorf("quarantine browser ephemeral runtime: %w", err)
		}
		currentName = lease.quarantineName
		if err = lease.sync(); err != nil {
			return fmt.Errorf("sync browser ephemeral quarantine: %w", err)
		}
	}
	if err = lease.removeAll(currentName); err != nil {
		return fmt.Errorf("remove browser ephemeral runtime: %w", err)
	}
	if _, statErr = lease.root.Lstat(currentName); !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("browser ephemeral runtime remains after cleanup")
	}
	if err = lease.sync(); err != nil {
		return fmt.Errorf("sync browser ephemeral cleanup: %w", err)
	}
	return lease.finish()
}

func (lease *ephemeralRuntimeLease) finish() error {
	if err := lease.root.Close(); err != nil {
		return fmt.Errorf("close browser ephemeral root: %w", err)
	}
	lease.root = nil
	if err := lease.lifecycle.Close(); err != nil {
		return fmt.Errorf("close browser ephemeral lifecycle lease: %w", err)
	}
	lease.lifecycle = nil
	lease.closed = true
	return nil
}

func syncRootDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}
