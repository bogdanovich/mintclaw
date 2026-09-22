package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheInventoryFingerprintChangesWhenOnlyPluginManifestChanges(t *testing.T) {
	root := t.TempDir()
	skillPath := filepath.Join(root, "example", "1.0.0", "skills", "example", "SKILL.md")
	manifestPath := filepath.Join(root, "example", "1.0.0", ".codex-plugin", "plugin.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(manifestPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: example\ndescription: example\n---\n"), 0o644))
	require.NoError(t, os.WriteFile(manifestPath, []byte(`{"license":"MIT"}`), 0o644))

	beforeEntries, err := collectCache(root, "test")
	require.NoError(t, err)
	require.Len(t, beforeEntries, 1)
	before, err := cacheInventoryFingerprint(root, beforeEntries)
	require.NoError(t, err)
	assert.Equal(t, "MIT", beforeEntries[0].License)

	require.NoError(t, os.WriteFile(manifestPath, []byte(`{"license":"Apache-2.0"}`), 0o644))
	afterEntries, err := collectCache(root, "test")
	require.NoError(t, err)
	after, err := cacheInventoryFingerprint(root, afterEntries)
	require.NoError(t, err)

	assert.Equal(t, "Apache-2.0", afterEntries[0].License)
	assert.NotEqual(t, before, after)
}
