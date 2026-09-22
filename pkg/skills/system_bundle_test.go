package skills

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureSystemBundleMaterializesEmbeddedSkillsIndependentOfCWD(t *testing.T) {
	home := t.TempDir()
	originalWorkingDirectory, err := os.Getwd()
	require.NoError(t, err)
	otherWorkingDirectory := t.TempDir()
	require.NoError(t, os.Chdir(otherWorkingDirectory))
	t.Cleanup(func() { _ = os.Chdir(originalWorkingDirectory) })

	bundle, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	activeRoot, err := ActiveSystemBundleRoot(home)
	require.NoError(t, err)

	assert.Equal(t, bundle.Root, activeRoot)
	assert.Len(t, bundle.Fingerprint, 64)
	for _, name := range []string{"mintclaw-agent", "mintclaw-trace-debug", "skill-creator"} {
		assert.FileExists(t, filepath.Join(activeRoot, name, "SKILL.md"))
	}
	scriptInfo, err := os.Stat(filepath.Join(activeRoot, "tmux", "scripts", "find-sessions.sh"))
	require.NoError(t, err)
	assert.NotZero(t, scriptInfo.Mode().Perm()&0o111)
	assert.NoDirExists(t, filepath.Join(otherWorkingDirectory, "skills"))
}

func TestBundledPDFSkillRoutesOrdinaryFormRequestsThroughProtectedWorkflow(t *testing.T) {
	contents, err := embeddedSystemSkills.ReadFile("bundled/pdf/SKILL.md")
	require.NoError(t, err)
	contract := string(contents)
	for _, required := range []string{
		"Call `document` with `action: inspect` before choosing a strategy",
		"ordinary request to complete, fill in, or help with a supported form",
		"`action: form`, `form_action: start`",
		"exact receipt as `event_id`",
		"Never ask for an interaction ID or `/answer` syntax",
		"Translate ordinary correction intent yourself",
		"call `commit` once",
		"Reserve one-shot `fields` then `fill`",
	} {
		assert.Contains(t, contract, required)
	}
}

func TestEnsureSystemBundleDoesNotRewriteUnchangedGeneration(t *testing.T) {
	home := t.TempDir()
	first, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	manifestPath := filepath.Join(first.Root, systemBundleManifestName)
	activePath := filepath.Join(systemBundleRoot(home), systemBundleActiveName)
	sentinelTime := time.Unix(1_234_567, 0)
	require.NoError(t, os.Chtimes(manifestPath, sentinelTime, sentinelTime))
	require.NoError(t, os.Chtimes(activePath, sentinelTime, sentinelTime))
	manifestBefore, err := os.Stat(manifestPath)
	require.NoError(t, err)
	activeBefore, err := os.Stat(activePath)
	require.NoError(t, err)

	second, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	manifestAfter, err := os.Stat(manifestPath)
	require.NoError(t, err)
	activeAfter, err := os.Stat(activePath)
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Equal(t, manifestBefore.ModTime(), manifestAfter.ModTime())
	assert.Equal(t, activeBefore.ModTime(), activeAfter.ModTime())
}

func TestEnsureSystemBundleReplacesCorruptGenerationAsCompleteSet(t *testing.T) {
	home := t.TempDir()
	first, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(first.Root, "unexpected-skill"), 0o755))

	repaired, err := EnsureSystemBundle(home)
	require.NoError(t, err)

	assert.Equal(t, first.Fingerprint, repaired.Fingerprint)
	assert.NotEqual(t, first.Root, repaired.Root)
	assert.NoDirExists(t, filepath.Join(repaired.Root, "unexpected-skill"))
	assert.DirExists(t, filepath.Join(first.Root, "unexpected-skill"))
	activeRoot, err := ActiveSystemBundleRoot(home)
	require.NoError(t, err)
	assert.Equal(t, repaired.Root, activeRoot)
	again, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	assert.Equal(t, repaired, again)
}

func TestEnsureSystemBundleRepairsPermissionDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX execute bits")
	}

	home := t.TempDir()
	first, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	oldScriptPath := filepath.Join(first.Root, "tmux", "scripts", "find-sessions.sh")
	require.NoError(t, os.Chmod(oldScriptPath, 0o444))

	repaired, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	repairedScriptPath := filepath.Join(repaired.Root, "tmux", "scripts", "find-sessions.sh")
	repairedInfo, err := os.Stat(repairedScriptPath)
	require.NoError(t, err)

	assert.Equal(t, first.Fingerprint, repaired.Fingerprint)
	assert.NotEqual(t, first.Root, repaired.Root)
	assert.Equal(t, os.FileMode(0o555), repairedInfo.Mode().Perm())
	oldInfo, err := os.Stat(oldScriptPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o444), oldInfo.Mode().Perm())
	activeRoot, err := ActiveSystemBundleRoot(home)
	require.NoError(t, err)
	assert.Equal(t, repaired.Root, activeRoot)
}

func TestEnsureSystemBundleFailedPermissionRepairKeepsActiveGeneration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX execute bits")
	}

	home := t.TempDir()
	first, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	scriptPath := filepath.Join(first.Root, "tmux", "scripts", "find-sessions.sh")
	require.NoError(t, os.Chmod(scriptPath, 0o444))
	failingWriter := func(string, []byte, os.FileMode) error {
		return errors.New("injected permission repair failure")
	}

	_, err = ensureSystemBundleFromFS(
		home,
		embeddedSystemSkills,
		systemBundleSourceRoot,
		failingWriter,
	)
	require.ErrorContains(t, err, "injected permission repair failure")
	activeRoot, err := ActiveSystemBundleRoot(home)
	require.NoError(t, err)

	assert.Equal(t, first.Root, activeRoot)
	assert.FileExists(t, scriptPath)
}

func TestEnsureSystemBundleConcurrentPermissionRepairsKeepPublishedRoots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX execute bits")
	}

	home := t.TempDir()
	first, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	scriptPath := filepath.Join(first.Root, "tmux", "scripts", "find-sessions.sh")
	require.NoError(t, os.Chmod(scriptPath, 0o444))

	const writers = 8
	start := make(chan struct{})
	stopObserver := make(chan struct{})
	observerDone := make(chan struct{})
	observerErrors := make(chan error, 1)
	go func() {
		defer close(observerDone)
		for {
			select {
			case <-stopObserver:
				return
			default:
			}
			root, resolveErr := ActiveSystemBundleRoot(home)
			if resolveErr != nil {
				observerErrors <- resolveErr
				return
			}
			if _, statErr := os.Stat(root); statErr != nil {
				observerErrors <- statErr
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	results := make(chan SystemBundle, writers)
	errorsChannel := make(chan error, writers)
	var waitGroup sync.WaitGroup
	for range writers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			bundle, ensureErr := EnsureSystemBundle(home)
			results <- bundle
			errorsChannel <- ensureErr
		}()
	}
	close(start)
	waitGroup.Wait()
	close(stopObserver)
	<-observerDone
	close(results)
	close(errorsChannel)
	select {
	case observerErr := <-observerErrors:
		require.NoError(t, observerErr)
	default:
	}

	for ensureErr := range errorsChannel {
		require.NoError(t, ensureErr)
	}
	for result := range results {
		assert.Equal(t, first.Fingerprint, result.Fingerprint)
		assert.DirExists(t, result.Root)
	}
	assert.DirExists(t, first.Root)
	assert.FileExists(t, scriptPath)
	activeRoot, err := ActiveSystemBundleRoot(home)
	require.NoError(t, err)
	activeScriptInfo, err := os.Stat(filepath.Join(activeRoot, "tmux", "scripts", "find-sessions.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o555), activeScriptInfo.Mode().Perm())
}

func TestSystemBundleFileModeMatchesUsesExplicitWindowsPolicy(t *testing.T) {
	assert.True(t, systemBundleFileModeMatches("windows", 0o666, 0o555))
	assert.True(t, systemBundleFileModeMatches("linux", 0o555, 0o555))
	assert.False(t, systemBundleFileModeMatches("linux", 0o444, 0o555))
}

func TestEnsureSystemBundleChangedSourcePublishesCompleteGeneration(t *testing.T) {
	home := t.TempDir()
	firstSource := testSystemBundleFS("first")
	first, err := ensureSystemBundleFromFS(home, firstSource, "bundled", writeSystemBundleFile)
	require.NoError(t, err)
	secondSource := fstest.MapFS{
		"bundled/fixture/SKILL.md": {
			Data: []byte("---\nname: fixture\ndescription: second\n---\n"),
		},
	}

	second, err := ensureSystemBundleFromFS(home, secondSource, "bundled", writeSystemBundleFile)
	require.NoError(t, err)
	activeRoot, err := ActiveSystemBundleRoot(home)
	require.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join(activeRoot, "fixture", "SKILL.md"))
	require.NoError(t, err)

	assert.NotEqual(t, first.Fingerprint, second.Fingerprint)
	assert.Equal(t, second.Root, activeRoot)
	assert.Contains(t, string(contents), "second")
	assert.NoFileExists(t, filepath.Join(activeRoot, "fixture", "references", "details.md"))
	assert.DirExists(t, first.Root)
}

func TestRuntimeSystemBundleRootPinsProcessGeneration(t *testing.T) {
	home := t.TempDir()
	first, err := ensureSystemBundleFromFS(home, testSystemBundleFS("first"), "bundled", writeSystemBundleFile)
	require.NoError(t, err)
	second, err := ensureSystemBundleFromFS(home, testSystemBundleFS("second"), "bundled", writeSystemBundleFile)
	require.NoError(t, err)
	cacheKey := systemBundleCacheKey(home)
	ensuredSystemBundles.Store(cacheKey, first)
	t.Cleanup(func() { ensuredSystemBundles.Delete(cacheKey) })

	runtimeRoot, err := RuntimeSystemBundleRoot(home)
	require.NoError(t, err)
	activeRoot, err := ActiveSystemBundleRoot(home)
	require.NoError(t, err)

	assert.Equal(t, first.Root, runtimeRoot)
	assert.Equal(t, second.Root, activeRoot)
	assert.NotEqual(t, runtimeRoot, activeRoot)
}

func TestEnsureSystemBundleWriteFailureLeavesPreviousGenerationActive(t *testing.T) {
	home := t.TempDir()
	first, err := ensureSystemBundleFromFS(home, testSystemBundleFS("first"), "bundled", writeSystemBundleFile)
	require.NoError(t, err)
	writes := 0
	failingWriter := func(path string, data []byte, mode os.FileMode) error {
		writes++
		if writes == 2 {
			return errors.New("injected write failure")
		}
		return writeSystemBundleFile(path, data, mode)
	}

	_, err = ensureSystemBundleFromFS(home, testSystemBundleFS("second"), "bundled", failingWriter)
	require.ErrorContains(t, err, "injected write failure")
	activeRoot, err := ActiveSystemBundleRoot(home)
	require.NoError(t, err)
	assert.Equal(t, first.Root, activeRoot)
}

func TestEnsureSystemBundleConcurrentWritersConverge(t *testing.T) {
	home := t.TempDir()
	const writers = 8
	results := make(chan SystemBundle, writers)
	errorsChannel := make(chan error, writers)
	var waitGroup sync.WaitGroup
	for range writers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			bundle, err := EnsureSystemBundle(home)
			results <- bundle
			errorsChannel <- err
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errorsChannel)

	for err := range errorsChannel {
		require.NoError(t, err)
	}
	var fingerprint string
	for result := range results {
		if fingerprint == "" {
			fingerprint = result.Fingerprint
		}
		assert.Equal(t, fingerprint, result.Fingerprint)
	}
	activeRoot, err := ActiveSystemBundleRoot(home)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(systemBundleRoot(home), "generations", fingerprint), activeRoot)
}

func TestActiveSystemBundleRootRejectsSymlinkMarker(t *testing.T) {
	home := t.TempDir()
	bundle, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	activePath := filepath.Join(systemBundleRoot(home), systemBundleActiveName)
	outside := filepath.Join(t.TempDir(), "active.json")
	require.NoError(t, os.Rename(activePath, outside))
	if err = os.Symlink(outside, activePath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err = ActiveSystemBundleRoot(home)
	assert.ErrorContains(t, err, "not a regular file")
	assert.DirExists(t, bundle.Root)
}

func TestEnsureSystemBundleReplacesSymlinkMarker(t *testing.T) {
	home := t.TempDir()
	bundle, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	activePath := filepath.Join(systemBundleRoot(home), systemBundleActiveName)
	outside := filepath.Join(t.TempDir(), "active.json")
	require.NoError(t, os.Rename(activePath, outside))
	if err = os.Symlink(outside, activePath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	repaired, err := EnsureSystemBundle(home)
	require.NoError(t, err)
	info, err := os.Lstat(activePath)
	require.NoError(t, err)
	assert.Zero(t, info.Mode()&os.ModeSymlink)
	assert.Equal(t, bundle, repaired)
}

func TestEnsureSystemBundleRejectsSymlinkOwnedDirectory(t *testing.T) {
	home := t.TempDir()
	skillsRoot := filepath.Join(home, "skills")
	require.NoError(t, os.Mkdir(skillsRoot, 0o755))
	outside := t.TempDir()
	if err := os.Symlink(outside, systemBundleRoot(home)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := EnsureSystemBundle(home)
	assert.ErrorContains(t, err, "not a physical directory")
	entries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestActiveSystemBundleRootRejectsMalformedOrUnsafeMarker(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	for name, marker := range map[string]string{
		"unknown field": `{"schema_version":2,"fingerprint":"` + fingerprint +
			`","generation":"` + fingerprint + `","extra":true}`,
		"path traversal": `{"schema_version":2,"fingerprint":"` + fingerprint +
			`","generation":"../outside"}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			activePath := filepath.Join(systemBundleRoot(home), systemBundleActiveName)
			require.NoError(t, os.MkdirAll(filepath.Join(systemBundleRoot(home), "generations"), 0o755))
			require.NoError(t, os.WriteFile(activePath, []byte(marker), 0o644))

			_, err := ActiveSystemBundleRoot(home)
			assert.Error(t, err)
		})
	}
}

func testSystemBundleFS(description string) fstest.MapFS {
	return fstest.MapFS{
		"bundled/fixture/SKILL.md": {
			Data: []byte("---\nname: fixture\ndescription: " + description + "\n---\n"),
		},
		"bundled/fixture/references/details.md": {Data: []byte(description + " details\n")},
	}
}
