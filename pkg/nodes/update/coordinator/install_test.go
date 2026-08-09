//go:build linux || darwin

package coordinator

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAdoptionPublishesInitialPayloadAndRollsBackCreateOnly(t *testing.T) {
	stateDirectory := privateRoot(t)
	installation := testState(t).Installation
	configureRuntimeInstallation(&installation)
	installation.ConfigPath = filepath.Join(stateDirectory, "config.json")
	adoption, err := BeginAdoption(
		stateDirectory,
		installation,
		currentTestExecutable(t),
		"v1.0.0",
		"v1.0.0",
		os.Geteuid(),
		os.Getegid(),
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(stateDirectory, StoreDirectoryName))
	if err == nil {
		_ = store.Close()
		t.Fatal("adoption store lock was not retained")
	}
	if err = adoption.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(stateDirectory, StoreDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("coordinator root survived rollback: %v", err)
	}
}

func TestAdoptionCommitLeavesExactPrivateState(t *testing.T) {
	stateDirectory := privateRoot(t)
	installation := testState(t).Installation
	configureRuntimeInstallation(&installation)
	installation.ConfigPath = filepath.Join(stateDirectory, "config.json")
	adoption, err := BeginAdoption(
		stateDirectory,
		installation,
		currentTestExecutable(t),
		"v1.0.0",
		"v1.0.0",
		os.Geteuid(),
		os.Getegid(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = adoption.Commit(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(stateDirectory, StoreDirectoryName)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Active.Slot != SlotA || state.Active.Release != "v1.0.0" || state.Transaction != nil {
		t.Fatalf("initial state = %#v", state)
	}
	payloadInfo, err := os.Stat(filepath.Join(root, payloadAFileName))
	if err != nil || payloadInfo.Mode().Perm() != 0o500 {
		t.Fatalf("active payload mode = %v, %v", payloadInfo, err)
	}
	if _, err = BeginAdoption(
		stateDirectory, installation, currentTestExecutable(t), "v1.0.0", "v1.0.0", os.Geteuid(), os.Getegid(),
	); err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("second BeginAdoption() error = %v", err)
	}
}

func TestAdoptionCanReleaseCoordinatorLockBeforeLifecycleStart(t *testing.T) {
	stateDirectory := privateRoot(t)
	installation := testState(t).Installation
	configureRuntimeInstallation(&installation)
	installation.ConfigPath = filepath.Join(stateDirectory, "config.json")
	adoption, err := BeginAdoption(
		stateDirectory, installation, currentTestExecutable(t), "v1.0.0", "v1.0.0", os.Geteuid(), os.Getegid(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = adoption.ReleaseCoordinatorLock(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(stateDirectory, StoreDirectoryName))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = adoption.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptionRejectsWritableOrMismatchedExecutable(t *testing.T) {
	source := filepath.Join(t.TempDir(), "payload")
	data, err := os.ReadFile(currentTestExecutable(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, data, 0o722); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(source, 0o722); err != nil {
		t.Fatal(err)
	}
	stateDirectory := privateRoot(t)
	installation := testState(t).Installation
	configureRuntimeInstallation(&installation)
	installation.ConfigPath = filepath.Join(stateDirectory, "config.json")
	if _, err = BeginAdoption(
		stateDirectory, installation, source, "v1.0.0", "v1.0.0", os.Geteuid(), os.Getegid(),
	); err == nil || !strings.Contains(err.Error(), "non-writable") {
		t.Fatalf("writable executable error = %v", err)
	}
	if err = os.Chmod(source, 0o500); err != nil {
		t.Fatal(err)
	}
	if runtime.GOARCH == "amd64" {
		installation.Architecture = "arm64"
	} else {
		installation.Architecture = "amd64"
	}
	if _, err = BeginAdoption(
		stateDirectory, installation, source, "v1.0.0", "v1.0.0", os.Geteuid(), os.Getegid(),
	); err == nil || !strings.Contains(err.Error(), "platform tuple") {
		t.Fatalf("mismatched executable error = %v", err)
	}
}

func currentTestExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	// go test compiles the test binary with the ambient umask, so its mode can be
	// group/other-writable (for example umask 0002 yields 775) and openExecutable
	// rejects it as a companion payload. Stage a mode-normalized copy so adoption
	// fixtures are deterministic on any umask.
	fixture := filepath.Join(t.TempDir(), "current-test-executable")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture, 0o500); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func configureRuntimeInstallation(installation *Installation) {
	installation.Platform = runtime.GOOS
	installation.Architecture = runtime.GOARCH
	if runtime.GOOS == "darwin" {
		installation.Manager = "launchd"
		installation.Service = "com.mintclaw.node.main"
	}
}
