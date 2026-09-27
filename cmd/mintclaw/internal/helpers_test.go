package internal

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestGetConfigPath(t *testing.T) {
	t.Setenv("HOME", "/tmp/home")

	got := GetConfigPath()
	want := filepath.Join("/tmp/home", ".mintclaw", "config.json")

	assert.Equal(t, want, got)
}

func TestGetConfigPath_WithMINTCLAW_HOME(t *testing.T) {
	t.Setenv(config.EnvHome, "/custom/mintclaw")
	t.Setenv("HOME", "/tmp/home")

	got := GetConfigPath()
	want := filepath.Join("/custom/mintclaw", "config.json")

	assert.Equal(t, want, got)
}

func TestGetConfigPath_PrefersExistingMainProfile(t *testing.T) {
	t.Setenv(config.EnvHome, "")
	t.Setenv(config.EnvConfig, "")
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	profileHome := filepath.Join(userHome, ".mintclaw", "main")
	require.NoError(t, os.MkdirAll(profileHome, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profileHome, "config.json"), []byte("{}"), 0o600))

	assert.Equal(t, filepath.Join(profileHome, "config.json"), GetConfigPath())
	assert.Equal(t, profileHome, GetMintClawHome())
}

func TestGetConfigPath_WithMINTCLAW_CONFIG(t *testing.T) {
	t.Setenv("MINTCLAW_CONFIG", "/custom/config.json")
	t.Setenv(config.EnvHome, "/custom/mintclaw")
	t.Setenv("HOME", "/tmp/home")

	got := GetConfigPath()
	want := "/custom/config.json"

	assert.Equal(t, want, got)
}

func TestGetConfigPath_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific HOME behavior varies; run on windows")
	}

	testUserProfilePath := `C:\Users\Test`
	t.Setenv("USERPROFILE", testUserProfilePath)

	got := GetConfigPath()
	want := filepath.Join(testUserProfilePath, ".mintclaw", "config.json")

	require.True(t, strings.EqualFold(got, want), "GetConfigPath() = %q, want %q", got, want)
}
