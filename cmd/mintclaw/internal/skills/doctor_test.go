package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimeskills "github.com/bogdanovich/mintclaw/pkg/skills"
)

func TestSkillsDoctorRendersStableJSONAndReturnsExitTwoForMissingDependency(t *testing.T) {
	loader := compatibilityTestLoader(t, runtimeskills.SkillRequirementMissing)
	cmd := newDoctorCommand(func(context.Context, runtimeskills.SkillRuntime) (*runtimeskills.SkillsLoader, error) {
		return loader, nil
	})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	output := new(bytes.Buffer)
	cmd.SetOut(output)
	cmd.SetArgs([]string{"--runtime", "coding", "--json"})

	err := cmd.Execute()

	var exitError *ExitError
	require.ErrorAs(t, err, &exitError)
	assert.Equal(t, 2, exitError.Code)
	var report runtimeskills.SkillCompatibilityReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	assert.Equal(t, runtimeskills.SkillRuntimeCoding, report.Runtime)
	require.Len(t, report.Skills, 1)
	assert.Equal(t, runtimeskills.SkillCompatibilityMissingDependency, report.Skills[0].Status)
	assert.NotContains(t, output.String(), "secret instructions")
}

func TestSkillsDoctorDoesNotFailForReadySkill(t *testing.T) {
	loader := compatibilityTestLoader(t, runtimeskills.SkillRequirementAvailable)
	cmd := newDoctorCommand(func(context.Context, runtimeskills.SkillRuntime) (*runtimeskills.SkillsLoader, error) {
		return loader, nil
	})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"--runtime", "coding"})

	err := cmd.Execute()

	assert.NoError(t, err)
}

func TestSkillsDoctorExitCodeAllowsIntentionalRuntimeIncompatibility(t *testing.T) {
	report := runtimeskills.SkillCompatibilityReport{Skills: []runtimeskills.SkillCompatibility{{
		Name: "gateway-only", Status: runtimeskills.SkillCompatibilityRuntimeIncompatible,
	}}}
	assert.Equal(t, 0, skillsDoctorExitCode(report))
}

func TestParseSkillRuntimeRejectsUnknownValue(t *testing.T) {
	_, err := parseSkillRuntime("desktop")
	assert.Error(t, err)
}

func TestSkillsListIncludesScopeAndCompatibilityStatus(t *testing.T) {
	loader := compatibilityTestLoader(t, runtimeskills.SkillRequirementAvailable)
	cmd := newListCommand(func(context.Context, runtimeskills.SkillRuntime) (*runtimeskills.SkillsLoader, error) {
		return loader, nil
	})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	output := new(bytes.Buffer)
	cmd.SetOut(output)
	cmd.SetArgs([]string{"--runtime", "gateway"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, output.String(), "gateway runtime")
	assert.Contains(t, output.String(), "user")
	assert.Contains(t, output.String(), "ready")
}

func TestSkillsDoctorPropagatesLoaderErrors(t *testing.T) {
	want := errors.New("load failed")
	cmd := newDoctorCommand(func(context.Context, runtimeskills.SkillRuntime) (*runtimeskills.SkillsLoader, error) {
		return nil, want
	})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(nil)
	assert.ErrorIs(t, cmd.Execute(), want)
}

func compatibilityTestLoader(
	t *testing.T,
	toolState runtimeskills.SkillRequirementState,
) *runtimeskills.SkillsLoader {
	t.Helper()
	root := filepath.Join(t.TempDir(), "skills")
	directory := filepath.Join(root, "fixture")
	require.NoError(t, os.MkdirAll(filepath.Join(directory, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(`---
name: fixture
description: fixture description
---

secret instructions
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "agents", "mintclaw.yaml"), []byte(`
schema_version: 1
requirements:
  tools: [exec]
`), 0o644))
	return runtimeskills.NewSkillsLoader([]runtimeskills.SkillRoot{{
		Path: root, Scope: runtimeskills.SkillScopeUser, Runtime: runtimeskills.SkillRuntimeShared,
	}}).WithCompatibilityEnvironment(runtimeskills.SkillCompatibilityEnvironment{
		Runtime:         runtimeskills.SkillRuntimeCoding,
		OperatingSystem: "linux",
		ToolState: func(string) runtimeskills.SkillRequirementState {
			return toolState
		},
		ExecutableAvailable: func(string) bool { return true },
	})
}
